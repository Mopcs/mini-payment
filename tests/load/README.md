# Load tests

Grafana k6 scenarios for the payment API, plus a read-only procedure that checks
the financial invariants afterwards.

Performance testing and correctness testing are separate here on purpose:

- the k6 scenarios measure latency, throughput, and failure rate under load
- `verify/run-verify.sh` asks PostgreSQL whether the money still adds up

A green load test does not prove the ledger is correct, and a correct ledger does
not prove the system is fast. Neither replaces the Go test suite.

## Safety

The suite only ever talks to the URL in `BASE_URL`. There is no default target,
and three guards refuse to start a run:

1. `BASE_URL` is required; the init context throws without it.
2. A host containing `prod` is rejected outright.
3. A host that is not loopback needs `LOAD_TEST_ALLOW_REMOTE=true` as a second
   confirmation, for the case where the isolated environment lives on another
   machine.

All funds are simulated. The suite creates its own dedicated users and wallets
and only ever deposits through the API's own simulated deposit endpoint. It never
touches an existing user or wallet: every operation is authenticated as an
account it created, and the transfer endpoint only allows the owner to spend from
a wallet.

Rate limiting stays in the request path. The load environment has to raise the
budgets, because the defaults (10 auth requests and 30 financial requests per
minute per IP) are designed for humans, not for a load generator:

```console
load test configuration: rate limited while creating transfers (Retry-After: 42).
Raise RATE_LIMIT_AUTH_LIMIT and RATE_LIMIT_FINANCIAL_LIMIT for the load environment
```

The suite stops rather than measuring the limiter.

## Prerequisites

- Docker, for the pinned k6 image (`grafana/k6:2.4.0`)
- an isolated API instance with PostgreSQL, Redis, RabbitMQ, the outbox worker,
  and the consumer running

Start the infrastructure from the repository root, then the three processes in
separate shells with the rate limits raised:

```sh
docker compose up -d postgres redis rabbitmq

RATE_LIMIT_AUTH_LIMIT=1000 RATE_LIMIT_FINANCIAL_LIMIT=1000000 go run ./cmd/api
RATE_LIMIT_AUTH_LIMIT=1000 RATE_LIMIT_FINANCIAL_LIMIT=1000000 go run ./cmd/worker
RATE_LIMIT_AUTH_LIMIT=1000 RATE_LIMIT_FINANCIAL_LIMIT=1000000 go run ./cmd/consumer
```

The worker and the consumer have to run too, otherwise the outbox checks at the
end will legitimately fail: events would never be published or consumed.

Running the application in containers instead, with the rate limits raised
through an overlay, is covered in "Running the whole thing in containers" below.
Either way, do not point a load test at a stack that also serves anything else.

## Configuration

```sh
cp tests/load/.env.k6.example tests/load/.env.k6
# edit .env.k6, at minimum BASE_URL and LOAD_TEST_PASSWORD
```

`tests/load/.env.k6` is ignored by Git. The suite reads it through `docker run
--env-file`, so every value below is an environment variable.

| Variable | Default | Meaning |
| --- | --- | --- |
| `BASE_URL` | **required** | Target origin, no trailing slash |
| `LOAD_TEST_PASSWORD` | **required** | Password for the test accounts, at least 12 characters |
| `LOAD_TEST_ALLOW_REMOTE` | `false` | Required for a non-loopback target |
| `LOAD_TEST_USER_PREFIX` | `k6-load` | Prefix of the generated account emails |
| `LOAD_TEST_EMAIL_DOMAIN` | `loadtest.example` | Domain of the generated account emails |
| `LOAD_TEST_SENDERS` | `8` | Users that send transfers |
| `LOAD_TEST_RECEIVERS` | `4` | Users that only receive |
| `LOAD_TEST_CONTENTION_VUS` | `4` | Virtual users hammering the hot wallet |
| `LOAD_TEST_CURRENCY` | `USD` | Wallet currency for the whole run |
| `LOAD_TEST_INITIAL_FUNDS` | `100000000` | Seed balance per sender, minor units |
| `LOAD_TEST_HOT_WALLET_FUNDS` | `1000000000` | Seed balance of the contention wallet |
| `LOAD_TEST_TRANSFER_AMOUNT` | `1000` | Amount per transfer, minor units |
| `LOAD_TEST_BASELINE_VUS` / `_DURATION` / `_PAUSE` | `3` / `30s` / `1` | Baseline shape |
| `LOAD_TEST_TRANSFER_VUS` / `_DURATION` / `_PAUSE` | `10` / `1m` / `0` | Transfer load shape |
| `LOAD_TEST_IDEMPOTENCY_REPEATS` | `5` | Attempts per idempotency key |
| `LOAD_TEST_STRESS_STAGES` | see below | `label:vus:duration` list |
| `LOAD_TEST_STRESS_READ_SHARE` | `0.3` | Fraction of stress iterations that only read |
| `THRESHOLD_HTTP_FAILED_RATE` | `0.01` | Failed request budget |
| `THRESHOLD_P95_MS` / `THRESHOLD_P99_MS` | `300` / `800` | Latency budgets for the normal scenarios |
| `THRESHOLD_CONTENTION_P95_MS` | `800` | p95 for the hot-wallet flow, whose latency is lock queueing time |
| `THRESHOLD_STRESS_P95_MS` / `_P99_MS` | `2000` / `5000` | Latency budgets for the stress run, which saturates on purpose |
| `THRESHOLD_TRANSFER_SUCCESS_RATE` | `0.99` | Accepted transfers per attempt |
| `THRESHOLD_BUSINESS_REJECTED` | `0` | Allowed business rejections |
| `LOAD_TEST_RESULTS_DIR` | `results` | Where the JSON report is written |

Three latency budgets exist because the three situations are not comparable.
A read is a query; a transfer is a database transaction; a transfer on the hot
wallet also waits behind every other virtual user on the same row lock, so its
latency is queueing time rather than service time; and a stress run deliberately
drives the system past its comfortable capacity. Holding all four to one budget
would either hide contention or turn every stress run red, and a threshold that
always fails is a threshold nobody reads. The error-rate and success-rate
budgets stay the same everywhere, because those must not degrade under load.

Default stress plan: `warmup:2:30s,low:10:1m,medium:25:2m,high:50:2m,recovery:5:1m`.

A malformed value is a configuration error with the variable's name, not a
silent fallback:

```console
load test configuration: LOAD_TEST_STRESS_STAGES entries must look like label:vus:duration, got "high"
```

## Test data

`setup()` runs once per test, never per iteration, and provisions a fixed set of
accounts from `LOAD_TEST_USER_PREFIX`:

| Role | Count | Purpose |
| --- | --- | --- |
| `sender-NN` | `LOAD_TEST_SENDERS` | One funded wallet each, sends transfers |
| `receiver-NN` | `LOAD_TEST_RECEIVERS` | One empty wallet each, only receives |
| `hot` | 1 | A single heavily funded wallet used by the contention scenario |

Provisioning is idempotent:

- registration falls back to login on `409 email_already_registered`, so a rerun
  reuses the same users instead of creating new ones
- a wallet is created only when the account has none in the run currency
- seeding deposits use a key derived from the amount, so repeating a run with the
  same `LOAD_TEST_INITIAL_FUNDS` is a replay and adds nothing
- every wallet is checked against another account's token and must answer `404`;
  a `200` means the fixture points at the wrong wallet and the run aborts

Two consequences worth knowing:

- **Changing `LOAD_TEST_PASSWORD` breaks the existing accounts.** They still hold
  the old password, so login fails. Use a new `LOAD_TEST_USER_PREFIX` instead.
- **Test users cannot be deleted once they have transfers.** `ledger_transactions`
  references `users` with `ON DELETE RESTRICT`, and ledger entries are
  append-only by trigger. The supported cleanup is to discard the whole isolated
  environment:

  ```sh
  docker compose down -v   # destroys the load environment's volumes
  ```

  Do not delete rows from `users`, `wallets`, or the ledger of a database you
  care about.

## Scenarios

| File | Writes? | What it measures |
| --- | --- | --- |
| `scenarios/baseline.js` | no | Read path: `/health`, `/ready`, wallet list, wallet read, transfer history |
| `scenarios/transfers.js` | yes | Concurrent transfers, plus a contention group on one hot wallet |
| `scenarios/idempotency.js` | yes | Replay correctness for one key, including the conflict case |
| `scenarios/stress.js` | yes | Staged virtual users from warm-up to recovery, 70% writes |

Run one with the wrapper, which pins the image and forwards `.env.k6`:

```sh
tests/load/run.sh baseline
tests/load/run.sh transfers
tests/load/run.sh idempotency
tests/load/run.sh stress
```

The equivalent k6 command, if you have k6 installed locally and exported the
variables yourself:

```sh
k6 run tests/load/scenarios/transfers.js
```

On Linux, a container reaches the host through host networking rather than
`host.docker.internal`:

```sh
K6_DOCKER_NETWORK=host BASE_URL=http://127.0.0.1:8080 tests/load/run.sh baseline
```

### Running the whole thing in containers

If the k6 container cannot reach the host, run the application in containers too
and put k6 on the same network. `docker-compose.load.yml` is an overlay that
raises the rate limits for the `api` service only, so the production file stays
untouched:

```sh
cp deployments/.env.production.example deployments/.env.production
# fill in the secrets, then from the repository root:
docker compose -f deployments/docker-compose.prod.yml \
  -f tests/load/docker-compose.load.yml up -d

K6_DOCKER_NETWORK=mini-payment-system-prod_backend sh tests/load/run.sh baseline
```

with `BASE_URL=http://api:8080` and `LOAD_TEST_ALLOW_REMOTE=true` in `.env.k6`,
because `api` is not a loopback name. The `run.sh` wrapper passes
`K6_DOCKER_NETWORK` to `docker run --network`, and k6 resolves `api` through the
Compose network.

### Baseline

Five read requests per iteration, three virtual users, one second of think time.
No writes, so it can be repeated without moving a single balance. This is the
number to compare a later run against: if the read path regressed, everything
downstream looks slower for a reason unrelated to transfers.

Run it on a fresh database and again after the write scenarios: the difference is
itself the measurement. In this repository it went from green to red as
`ledger_transactions` grew, for the reason described in "What these runs found".

### Transfers

Two scenarios in one run:

- `senders` — distinct users, each spending from its own wallet, one transfer per
  iteration
- `contention` — every virtual user authenticates as the same user and spends
  from the same hot wallet, which serialises on that row's lock

Every transfer carries a fresh idempotency key, uses the run currency, and is
funded in advance, so a `4xx` here means the fixture is wrong rather than the
system being busy.

Business responses are counted separately from server errors:

| Status | Code | Meaning here |
| --- | --- | --- |
| `400` | `invalid_amount`, `invalid_currency`, `invalid_transfer`, `invalid_wallet_id`, `invalid_idempotency_key`, `currency_mismatch` | The fixture is wrong |
| `404` | `wallet_not_found` | The source wallet is not owned by the caller |
| `409` | `idempotency_conflict` | A key was reused with a different payload — impossible with fresh keys |
| `422` | `insufficient_funds` | The sender ran out of seeded funds |
| `422` | `balance_limit` | The destination would overflow `int64` |

None of these count as success. `transfer_created` is the success metric, and the
threshold requires it to stay above `THRESHOLD_TRANSFER_SUCCESS_RATE`.
`transfer_business_rejected` must stay at or below `THRESHOLD_BUSINESS_REJECTED`
(default `0`), and any `5xx` fails the run through `transfer_server_errors`.

Running out of funds is the one case that is expected rather than broken: a long
run with a small `LOAD_TEST_INITIAL_FUNDS` can drain a sender. Raise the seed
instead of relaxing the threshold.

### Idempotency

One virtual user, one iteration, and `checks == 1` as the only threshold, so a
single failed assertion fails the run. It asserts:

- every attempt returns `201`
- every attempt reports the same `transfer_id`
- exactly one attempt created the transfer, the rest carry `Idempotent-Replay: true`
- the sender's balance moved by exactly one amount
- the same key with a different amount is rejected with `409 idempotency_conflict`
- that rejected attempt changed nothing

This is why it is separate from the throughput scenarios: it measures a
guarantee, not a rate.

### Stress

Staged `ramping-vus` from the plan in `LOAD_TEST_STRESS_STAGES`. Each entry ramps
to its target over its duration, so the run climbs instead of jumping. The mix is
70% transfers and 30% reads by default.

The defaults are a shape that usually finds a knee on a development machine.
They are not a capacity claim about the system, and they are not a target anyone
promised.

## Reading the results

Every run prints a block and writes a JSON report:

```text
=== transfers ===
target        http://api:8080
command       k6 run scenarios/transfers.js
requests      2882 (71.6/s)
failed        0.00%
duration      p50 127ms  p95 288ms  p99 382ms
checks        100.00%
thresholds    8 passed, 0 failed
report        results/transfers-2026-11-26T04-30-27-700Z.json
```

A non-zero exit code means at least one threshold failed. The report keeps the
scenario, the full options block (virtual users, stages, thresholds), the
threshold verdicts, and every metric k6 collected, so two runs can be compared
field by field. `jq '.headline' results/transfers-*.json` is usually enough.

What a failed threshold means:

| Threshold | Failure means |
| --- | --- |
| `http_req_failed` | More than 1% of requests failed, including `4xx` |
| `http_req_duration{p(95)}` | The slow tail grew; check PostgreSQL locks and pool saturation |
| `http_req_duration{p(99)}` | A few requests stalled badly; usually lock waits or a pool acquire |
| `transfer_created` | Transfers were rejected for business reasons, so the load never reached the ledger |
| `transfer_business_rejected` | The fixture ran out of funds or hit a wrong wallet |
| `transfer_server_errors` | A `5xx` happened; this is a bug, not a capacity limit |

The defaults are loose on purpose. Tighten them only after a run you trust, and
tighten one at a time so a regression stays attributable.

## Why results depend on the machine

Every number here is bounded by the environment that produced it:

- the API, PostgreSQL, Redis, RabbitMQ, the worker, and the consumer may share
  one laptop, so they compete for the same CPU
- k6 itself needs CPU; at high virtual user counts it becomes part of the
  measurement
- Docker Desktop on macOS and Windows runs containers in a virtual machine with
  its own CPU and memory ceiling
- PostgreSQL connection pool size, container CPU limits, and disk speed all move
  the p95

Compare runs made on the same machine with the same configuration. Do not compare
against a number in a README, including this one.

For orientation only, one set of runs on a single Windows laptop with Docker
Desktop, with all six services and k6 sharing the CPU, produced:

| Scenario | Requests | Rate | Failed | p50 | p95 | p99 |
| --- | --- | --- | --- | --- | --- | --- |
| `baseline` | 361 | 12.9/s | 0.00% | 2 ms | 258 ms | 359 ms |
| `transfers` | 2882 | 71.6/s | 0.00% | 127 ms | 288 ms | 382 ms |
| `idempotency` | 86 | 9.4/s | 0.00% | 4 ms | 476 ms | 520 ms |
| `stress` | 3970 | 39.1/s | 0.00% | 204 ms | 1210 ms | 1796 ms |

Those are observations of one environment, not a benchmark. The `stress` row is
the interesting one: latency degrades sharply at the top stage while the error
rate stays at zero, which is what saturation without failure looks like. The
`baseline` p95 is dominated by the first pass through cold caches; the per
endpoint p95 in that same report was much lower (`health` 3.5 ms, `ready` 5.7 ms,
`wallets_list` 4.1 ms, `wallet_get` 6.2 ms, `transfer_history` 17.7 ms).

## Monitoring during a run

The three processes expose Prometheus metrics on `API_METRICS_ADDR` (9091),
`WORKER_METRICS_ADDR` (9092), and `CONSUMER_METRICS_ADDR` (9093). The Compose
monitoring stack scrapes all three; see the "Monitoring" section of the root
README.

The existing dashboards already cover this. `deployments/monitoring/grafana/
dashboards/api-overview.json` has request rate, latency, error rate, and pool
utilisation; `payment-pipeline.json` has the outbox and consumer view. Nothing
new is provisioned for load testing.

Queries worth keeping open in Prometheus while a run is in progress:

```promql
# Request rate by route
sum by (route) (rate(payment_http_requests_total[1m]))

# p95 and p99 latency by route
histogram_quantile(0.95, sum by (le, route) (rate(payment_http_request_duration_seconds_bucket[1m])))
histogram_quantile(0.99, sum by (le, route) (rate(payment_http_request_duration_seconds_bucket[1m])))

# Server error ratio
sum(rate(payment_http_requests_total{status_code=~"5.."}[1m]))
  / sum(rate(payment_http_requests_total[1m]))

# Requests in flight
sum(payment_http_requests_in_progress)

# PostgreSQL pool: acquired versus total, and how often a caller had to wait
payment_postgres_pool_connections{state="acquired"}
payment_postgres_pool_connections{state="total"}
rate(payment_postgres_pool_empty_acquire_total[1m])

# Outbox backlog age and publish rate
payment_outbox_oldest_pending_age_seconds
sum(rate(payment_outbox_events_published_total[1m]))

# Consumer throughput and failures
sum(rate(payment_consumer_messages_processed_total[1m]))
sum(rate(payment_consumer_processing_failures_total[1m]))

# Rate limiter decisions, by outcome
sum by (outcome) (rate(payment_ratelimit_decisions_total[1m]))

# Go runtime pressure
go_goroutines
rate(go_memstats_heap_inuse_bytes[1m])
rate(process_cpu_seconds_total[1m])
```

`payment_outbox_oldest_pending_age_seconds` climbing while transfers keep
succeeding is the signature of a worker that cannot keep up: the API is fine and
the pipeline is behind. `payment_postgres_pool_empty_acquire_total` climbing
means callers are queueing for connections, which usually explains a p99 spike
long before the database itself looks busy.

## Financial invariant verification

Run this after a load test, while the environment still holds the data:

```sh
tests/load/verify/run-verify.sh
```

It executes `verify/invariants.sql` through `psql` and fails when any check
reports a violation. The SQL is read-only: every statement is a `SELECT`, because
a verification step that could change the data proves nothing.

Against a database that is not the local Compose one:

```sh
VERIFY_DATABASE_URL="postgres://user:pass@host:5432/payment_db?sslmode=disable" \
  tests/load/verify/run-verify.sh
```

Against a container that does not publish the database port, which is the case
for the production Compose file:

```sh
VERIFY_DOCKER_CONTAINER=mini-payment-system-prod-postgres-1 \
  tests/load/verify/run-verify.sh
```

Twelve checks, each of which must report zero:

| Check | Invariant |
| --- | --- |
| negative balances | No wallet is below zero |
| wallet vs ledger | `wallets.balance` equals the sum of its ledger account entries |
| two entries | Every ledger transaction has exactly two entries |
| sums to zero | Those entries sum to zero |
| transfer direction | The source is debited and the destination credited by the recorded amount |
| funds conserved | Per currency, wallet balances equal total deposits |
| one effect per key | No idempotency key produced two transactions |
| outbox exists | Every transfer has a `transfer.completed` outbox event |
| outbox published | Nothing stays unpublished past `PENDING_AGE_SECONDS` (default 60) |
| consumer caught up | Published events are processed within the same window |
| notifications | Each processed transfer has exactly two |
| no duplicates | No event produced two notifications for one user |

The last four are timing dependent. Run the verification after the queue has
drained, or raise `PENDING_AGE_SECONDS`:

```sh
PENDING_AGE_SECONDS=300 tests/load/verify/run-verify.sh
```

Draining is not instant. The outbox worker publishes one event per broker
confirm, so its ceiling is well below what the API can accept during a stress
run. After the stress scenario in this repository the backlog reached roughly
1900 unpublished events and took a few minutes to clear; the checks reported
them as violations until it did, which is the correct behaviour. Watch it with:

```sql
SELECT count(*) FILTER (WHERE published_at IS NULL) AS pending,
       max(now() - created_at) FILTER (WHERE published_at IS NULL) AS oldest
  FROM outbox_events;
```

The checks were also validated the other way round: applied to a scratch copy of
the schema seeded with deliberate violations (a negative balance, a
single-entry transaction, a swapped transfer, a duplicated idempotency key, a
stale outbox event, a missing notification), all twelve reported a non-zero
count and the script exited `1`. A check that has only ever printed `ok` proves
nothing.

PostgreSQL is the authority for all of this. Prometheus is not consulted: a
counter can be right while the ledger is wrong.

## What these runs found

Two findings, both real and neither fixed here, because this iteration is the
measurement, not the optimisation.

**`GET /api/v1/transfers` degrades with table size.** The history predicate is
`EXISTS(...) OR EXISTS(...)` against `wallets`, which PostgreSQL cannot turn into
an index scan, so every history request runs a sequential scan of
`ledger_transactions` twice, plus a top-N sort. On a database with a few hundred
transfers the history endpoint had a p95 of 17.7 ms and the baseline passed; once
the table held about 8 400 rows the same endpoint reached a p95 of 743 ms and the
baseline thresholds failed. Measured on that data:

| Formulation | Plan | Time |
| --- | --- | --- |
| `EXISTS(..) OR EXISTS(..)` (current) | Seq scan, cost 130744 | 82 ms |
| two joins combined with `UNION` | Index scans, cost 720 | 7.4 ms |

The count and the page both pay this. A rewrite is worth doing, but it belongs in
its own change with its own tests, not folded into a load-testing iteration.

**The outbox relay saturates before the API does.** Publishing is serial and waits
for a broker confirm per message. During the stress scenario the API accepted
transfers faster than the relay published them, leaving roughly 1 900 events
queued; the backlog drained in a few minutes at about 22 events per second. No
request failed, so this is a throughput ceiling rather than a correctness bug, but
it means the pipeline's ceiling is far below the API's.

Neither finding was inferred from a dashboard. Both were reproduced with `EXPLAIN`
and with row counts taken from PostgreSQL.

## Limitations

- One currency per run. The API supports USD, EUR, and JPY; the fixture uses one
  so that every transfer satisfies the currency-match rule.
- The contention scenario measures lock waits on a single wallet. It does not
  attempt to provoke deadlocks: the transfer path locks wallets in UUID order, so
  `A→B` and `B→A` cannot deadlock each other.
- `setup()` is sequential. With `LOAD_TEST_SENDERS` above roughly 10 the auth rate
  limit must be raised, which the safety section already requires.
- No k6 browser or protocol beyond HTTP; nothing here exercises WebSockets,
  because the system has none.
- The suite was validated by running all four scenarios against a containerised
  stack on one machine. It has not been run against a multi-host deployment, and
  no capacity numbers are claimed: the figures above are single-run observations
  of one laptop.
- The outbox relay is the first component to saturate. It publishes serially and
  waits for a broker confirm per message, so a run that outpaces it leaves a
  backlog that the invariant checks will report until it drains. That is a real
  property of the design, not a test artefact, and this suite does not fix it.
- Thresholds ship as defaults tuned to pass on a development machine. They are a
  regression guard. Re-derive them from your own baseline before trusting them.
