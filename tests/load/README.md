# Load tests

Grafana k6 scenarios for the payment API, plus a read-only check of the
financial invariants after the run.

**Two separate questions:** k6 measures latency, throughput, and failures;
`verify/run-verify.sh` asks PostgreSQL whether the money still adds up. A green
load test does not prove the ledger is correct, and a correct ledger does not
prove the system is fast.

## Safety first

The suite talks only to `BASE_URL`. There is no default target, and three guards
stop a run:

1. `BASE_URL` is required.
2. A host containing `prod` is rejected outright.
3. A non-loopback host needs `LOAD_TEST_ALLOW_REMOTE=true`.

All funds are simulated. Each run creates its own users and wallets and only
uses the API's simulated deposit endpoint. It never touches existing accounts.

Rate limiting stays enabled. The defaults (10 auth / 30 financial requests per
minute per IP) are for humans, so raise them on the load target:

```sh
RATE_LIMIT_AUTH_LIMIT=1000 RATE_LIMIT_FINANCIAL_LIMIT=100000 go run ./cmd/api
RATE_LIMIT_AUTH_LIMIT=1000 RATE_LIMIT_FINANCIAL_LIMIT=100000 go run ./cmd/worker
RATE_LIMIT_AUTH_LIMIT=1000 RATE_LIMIT_FINANCIAL_LIMIT=100000 go run ./cmd/consumer
```

The worker and consumer must run, otherwise the outbox checks will legitimately
fail. Never point this suite at a shared or production stack.

## Quick setup

Prerequisites: Docker and an isolated stack with PostgreSQL, Redis, RabbitMQ,
the API, the worker, and the consumer. k6 runs from the pinned
`grafana/k6:2.4.0` image.

```sh
cp tests/load/.env.k6.example tests/load/.env.k6
# set BASE_URL and LOAD_TEST_PASSWORD at minimum
tests/load/run.sh baseline
```

`.env.k6` is gitignored and passed to the container with `--env-file`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `BASE_URL` | required | Target origin, no trailing slash |
| `LOAD_TEST_PASSWORD` | required | Test account password, at least 12 characters |
| `LOAD_TEST_ALLOW_REMOTE` | `false` | Confirmation for non-loopback targets |
| `LOAD_TEST_USER_PREFIX` / `LOAD_TEST_EMAIL_DOMAIN` | `k6-load` / `loadtest.example` | Generated account names |
| `LOAD_TEST_SENDERS` / `_RECEIVERS` / `_CONTENTION_VUS` | `8` / `4` / `4` | Fixture and contention size |
| `LOAD_TEST_CURRENCY` | `USD` | One currency per run |
| `LOAD_TEST_INITIAL_FUNDS` / `_HOT_WALLET_FUNDS` | `100000000` / `1000000000` | Seed balances in minor units |
| `LOAD_TEST_TRANSFER_AMOUNT` | `1000` | Amount per transfer |
| `LOAD_TEST_BASELINE_VUS` / `_DURATION` / `_PAUSE` | `3` / `30s` / `1` | Read scenario shape |
| `LOAD_TEST_TRANSFER_VUS` / `_DURATION` / `_PAUSE` | `10` / `1m` / `0` | Transfer scenario shape |
| `LOAD_TEST_IDEMPOTENCY_REPEATS` | `5` | Repeats per idempotency key |
| `LOAD_TEST_STRESS_STAGES` | see below | `label:vus:duration` list |
| `LOAD_TEST_STRESS_READ_SHARE` | `0.3` | Read fraction in stress |
| `THRESHOLD_HTTP_FAILED_RATE` | `0.01` | Failed request budget |
| `THRESHOLD_P95_MS` / `_P99_MS` | `300` / `800` | Normal latency budgets |
| `THRESHOLD_CONTENTION_P95_MS` | `800` | Hot-wallet queueing budget |
| `THRESHOLD_STRESS_P95_MS` / `_P99_MS` | `2000` / `5000` | Overload latency budgets |
| `THRESHOLD_TRANSFER_SUCCESS_RATE` / `_BUSINESS_REJECTED` | `0.99` / `0` | Business correctness budgets |
| `LOAD_TEST_RESULTS_DIR` | `results` | JSON report directory |

Default stress plan:
`warmup:2:30s,low:10:1m,medium:25:2m,high:50:2m,recovery:5:2m`.

Different latency budgets exist because a read, a database transaction, a
lock-queued transfer, and deliberate overload are not comparable. Error-rate
and success-rate budgets stay strict everywhere.

## Scenarios

| Scenario | Writes | What it measures |
| --- | --- | --- |
| `baseline` | No | `/health`, `/ready`, wallet list/read, transfer history |
| `transfers` | Yes | Concurrent transfers plus a hot-wallet contention group |
| `idempotency` | Yes | Replay and conflict behaviour for one key |
| `stress` | Yes | Staged user ramp, 70% writes / 30% reads |

```sh
tests/load/run.sh baseline
tests/load/run.sh transfers
tests/load/run.sh idempotency
tests/load/run.sh stress
```

On Linux, use host networking:

```sh
K6_DOCKER_NETWORK=host BASE_URL=http://127.0.0.1:8080 tests/load/run.sh baseline
```

If k6 cannot reach the host, run the application on the same Compose network:

```sh
docker compose -f deployments/docker-compose.prod.yml \
  -f tests/load/docker-compose.load.yml up -d

K6_DOCKER_NETWORK=mini-payment-system-prod_backend sh tests/load/run.sh baseline
```

Set `BASE_URL=http://api:8080` and `LOAD_TEST_ALLOW_REMOTE=true` in that case.

### What the scenarios check

- **baseline** — repeat on a fresh database and after writes. In this project
  the history endpoint degraded as `ledger_transactions` grew; see below.
- **transfers** — `senders` use distinct wallets; `contention` uses one hot
  wallet. Every transfer has a fresh idempotency key and is funded in advance.
  A `4xx` means the fixture is wrong, not that the system is busy.
- **idempotency** — one VU, one iteration, `checks == 1`. Verifies 201 on every
  attempt, the same transfer ID, one real creation, replay headers on the rest,
  one balance movement, and `409 idempotency_conflict` for a changed payload.
- **stress** — staged `ramping-vus`. This is a shape that finds a knee on a
  development machine, not a capacity claim.

## Test data

`setup()` runs once per test and provisions senders, receivers, and one hot
wallet. It is idempotent: registration falls back to login on `409`, wallets are
created only when missing, and seed deposits use a stable key so reruns replay
instead of adding funds. Wallet ownership is checked with another account's
token; a `200` aborts the run.

Two consequences:

- Changing `LOAD_TEST_PASSWORD` breaks existing accounts. Use a new
  `LOAD_TEST_USER_PREFIX` instead.
- Test users cannot be deleted once they have transfers: ledger rows reference
  users with `ON DELETE RESTRICT` and entries are append-only. Discard the
  isolated environment instead: `docker compose down -v`.

## Reading results

Each run prints a summary and writes `results/<scenario>-<timestamp>.json`:

```text
=== transfers ===
requests      11815 (186.2/s)
failed        0.00%
duration      p50 62ms  p95 138ms  p99 206ms
checks        100.00%
thresholds    8 passed, 0 failed
```

Inspect the headline without `jq` (not installed in this environment):

```sh
python -c 'import json,sys; print(json.load(open(sys.argv[1]))["headline"])' \
  results/transfers-*.json
```

| Failed threshold | Usual cause |
| --- | --- |
| `http_req_failed` | More than 1% requests failed, including `4xx` |
| `http_req_duration p95/p99` | Slow tail from locks, pool waits, or CPU saturation |
| `transfer_created` | Business rejections; load never reached the ledger |
| `transfer_business_rejected` | Fixture ran out of funds or used a wrong wallet |
| `transfer_server_errors` | A `5xx` — a bug, not a capacity limit |

Tighten thresholds one at a time, after you trust your own baseline.

### Results depend on the machine

Host and container runs differ because all services and k6 compete for the same
CPU, Docker Desktop adds a VM boundary, and pool size and disk speed move p95.
Compare runs made on the same machine with the same configuration; do not
compare against numbers in a README.

Observed on one Windows laptop, for orientation only:

| Setup | Scenario | Requests | Rate | Failed | p50 | p95 | p99 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| containers | `baseline` | 361 | 12.9/s | 0.00% | 2 ms | 258 ms | 359 ms |
| containers | `transfers` | 2882 | 71.6/s | 0.00% | 127 ms | 288 ms | 382 ms |
| containers | `stress` | 3970 | 39.1/s | 0.00% | 204 ms | 1210 ms | 1796 ms |
| host | `baseline` | 525 | 15.6/s | 0.00% | 4 ms | 34 ms | 110 ms |
| host | `transfers` | 11815 | 186.2/s | 0.00% | 62 ms | 138 ms | 206 ms |
| host | `stress` | 67269 | 170.9/s | 0.00% | 32 ms | 666 ms | 1359 ms |

### Useful Prometheus queries during a run

```promql
sum by (route) (rate(payment_http_requests_total[1m]))
histogram_quantile(0.95, sum by (le, route) (rate(payment_http_request_duration_seconds_bucket[1m])))
sum(rate(payment_http_requests_total{status_code=~"5.."}[1m])) / sum(rate(payment_http_requests_total[1m]))
payment_postgres_pool_connections{state="acquired"}
rate(payment_postgres_pool_empty_acquire_total[1m])
payment_outbox_oldest_pending_age_seconds
sum(rate(payment_outbox_events_published_total[1m]))
sum(rate(payment_consumer_messages_processed_total[1m]))
```

A climbing `payment_outbox_oldest_pending_age_seconds` means the API is fine but
the pipeline is behind. A climbing `payment_postgres_pool_empty_acquire_total`
means callers are queueing for connections.

## Financial invariant verification

Run this after a load test, while the data is still present:

```sh
tests/load/verify/run-verify.sh
```

It executes `verify/invariants.sql` through `psql`; every statement is a
`SELECT`, so verification cannot change the data.

Other connection paths:

```sh
VERIFY_DATABASE_URL="postgres://user:pass@host:5432/payment_db?sslmode=disable" \
  tests/load/verify/run-verify.sh

VERIFY_DOCKER_CONTAINER=mini-payment-system-prod-postgres-1 \
  tests/load/verify/run-verify.sh

PENDING_AGE_SECONDS=300 tests/load/verify/run-verify.sh
```

The 12 checks cover negative balances, wallet-vs-ledger balance, exactly two
entries per transaction, zero sum, transfer direction, funds conservation, one
effect per idempotency key, outbox presence, publish delay, consumer lag,
notification completeness, and duplicate notifications.

The last four are timing dependent. Wait for the queue to drain or raise
`PENDING_AGE_SECONDS`. Drain is not instant: the relay was measured at ~20
events/s before the fix and ~94/s after; a stress run can leave thousands of
events pending. Watch with:

```sql
SELECT count(*) FILTER (WHERE published_at IS NULL) AS pending,
       max(now() - created_at) FILTER (WHERE published_at IS NULL) AS oldest
FROM outbox_events;
```

These checks were also validated against a scratch database seeded with
deliberate violations; all twelve failed as expected. Prometheus is not
consulted: a counter can be right while the ledger is wrong.

## What these runs found

Two real bottlenecks were found with `EXPLAIN` and PostgreSQL row counts, then
fixed and re-measured. Full evidence is in
[../../docs/performance-report.md](../../docs/performance-report.md).

- **Transfer history degraded with table size.** The old `EXISTS(...) OR
  EXISTS(...)` predicate forced a sequential scan and triggered per-query JIT.
  Rewriting it as indexed joins with `UNION` cut the page query from 1500.3 ms
  (including 795.7 ms JIT) to 22.1 ms; endpoint p95 went from 584 ms to 35.5 ms.
- **The outbox relay saturated first.** Publishing is serial and waits for a
  broker confirm. The worker now claims another batch immediately after a full
  one, raising measured drain rate from ~20/s to ~94/s without extra
  concurrency or weaker confirms.

## Limitations

- One currency per run; the API supports USD, EUR, and JPY, but the fixture uses
  one to satisfy the currency-match rule.
- Contention measures lock waits on one wallet; it does not try to provoke
  deadlocks, because UUID-ordered locking prevents A→B / B→A deadlocks.
- `setup()` is sequential; above roughly 10 senders the auth rate limit must be
  raised.
- Only HTTP is exercised — no browser, WebSockets, or multi-host deployment.
- Thresholds are development defaults, not capacity guarantees.
