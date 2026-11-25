 Mini Payment Processing System

A Go modular monolith for processing payments between users. PostgreSQL is the source of truth for financial data; Redis is reserved for supporting features such as caching and rate limiting.

## Technologies

- Go
- PostgreSQL 17
- Redis 7
- Docker Compose

## Local setup

1. Copy `.env.example` to `.env` and adjust the local credentials if needed.
2. Start Docker Desktop (or another Docker Engine with Compose).
3. Run `make config` to validate the Compose configuration.
4. Run `make up` to start PostgreSQL, Redis, RabbitMQ, Prometheus, Grafana, and Jaeger.
5. Run `make ps` to check container health.
6. Start the API with `go run ./cmd/api`.
7. Check `http://localhost:8080/health` and `http://localhost:8080/ready`.
8. Open Grafana at `http://localhost:3000` for the provisioned dashboards, and Prometheus at `http://localhost:9090`.

All services publish ports only on `127.0.0.1`. Their data is stored in Docker named volumes and survives container restarts. Redis persistence uses AOF, and Prometheus keeps 15 days of metrics by default.

## Environment variables

| Variable | Purpose | Example |
| --- | --- | --- |
| `APP_ADDR` | HTTP listen address | `:8080` |
| `POSTGRES_DB` | PostgreSQL database name | `payment_db` |
| `POSTGRES_USER` | PostgreSQL user | `payment_user` |
| `POSTGRES_PASSWORD` | Local PostgreSQL password | `local_dev_postgres_password` |
| `POSTGRES_HOST` | Host used by local clients | `127.0.0.1` |
| `POSTGRES_PORT` | Published PostgreSQL port | `5432` |
| `PGSSLMODE` | TLS mode for the PostgreSQL pool; the Compose container serves no TLS | `disable` |
| `REDIS_HOST` | Host used by local clients | `127.0.0.1` |
| `REDIS_PORT` | Published Redis port | `6379` |
| `REDIS_PASSWORD` | Local Redis password | `local_dev_redis_password` |
| `RABBITMQ_URL` | AMQP URL used by the worker | `amqp://payment_user:local_dev_rabbitmq_password@127.0.0.1:5672/` |
| `RABBITMQ_USER` / `RABBITMQ_PASSWORD` | Local RabbitMQ broker credentials | set in `.env` |
| `RABBITMQ_EXCHANGE` | Durable direct exchange | `payment.events` |
| `RABBITMQ_QUEUE` | Durable event queue | `payment.events` |
| `RABBITMQ_ROUTING_KEY` | Queue binding and publish routing key | `transfer.completed` |
| `OUTBOX_BATCH_SIZE` | Maximum events claimed per poll | `20` |
| `OUTBOX_POLL_INTERVAL` | Delay between polls and reconnect attempts | `1s` |
| `OUTBOX_LEASE_DURATION` | How long a worker owns claimed events | `30s` |
| `OUTBOX_MAX_BACKOFF` | Maximum retry delay | `5m` |
| `LOG_LEVEL` | JSON log level (`debug`, `info`, `warn`, or `error`) | `info` |
| `JWT_SECRET` | HMAC signing key, at least 32 bytes | replace the example placeholder |
| `JWT_ISSUER` | JWT issuer claim | `mini-payment-system` |
| `JWT_ACCESS_TOKEN_TTL` | Access token lifetime, from 1 second to 24 hours | `15m` |
| `API_METRICS_ADDR` / `WORKER_METRICS_ADDR` / `CONSUMER_METRICS_ADDR` | Metrics listener per process, must be distinct ports | `0.0.0.0:9091` |
| `PROMETHEUS_PORT` | Published Prometheus port | `9090` |
| `PROMETHEUS_RETENTION` | Prometheus retention window | `15d` |
| `GRAFANA_PORT` | Published Grafana port | `3000` |
| `GRAFANA_ADMIN_USER` | Grafana administrator login | `admin` |
| `GRAFANA_ADMIN_PASSWORD` | Grafana administrator password | set in `.env`, never commit it |
| `OTEL_TRACING_ENABLED` | Turn tracing on or off | `true` |
| `OTEL_SERVICE_NAME` | Overrides the per-process service name | unset by default |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP gRPC target, `host:port` or a URL | `localhost:4317` |
| `OTEL_TRACES_SAMPLER` | Sampler name | `parentbased_traceidratio` |
| `OTEL_TRACES_SAMPLER_ARG` | Sampling ratio for the ratio samplers, 0 to 1 | `1.0` |
| `JAEGER_UI_PORT` / `JAEGER_OTLP_PORT` | Published Jaeger ports | `16686` / `4317` |

Compose requires PostgreSQL and Redis passwords from `.env`; RabbitMQ fallback credentials are for local development only.
Environment variables already set in the shell take precedence over values loaded from `.env`.
Replace the `JWT_SECRET` placeholder in `.env` with a cryptographically random value before running the API; never commit `.env` or use the example value outside local development.

## Registration and login

- `POST /api/v1/auth/register` creates a user and an initial zero-balance USD wallet in one PostgreSQL transaction.
- `POST /api/v1/auth/login` verifies the email and password and returns the same generic error for either invalid value.

Passwords must contain at least 12 Unicode characters and at most 1024 bytes. They are stored as Argon2id hashes using 64 MiB memory, 3 iterations, 2 threads, a random 16-byte salt, and a 32-byte output. Access tokens use HS256 and contain only `sub` (the user UUID), `iat`, `exp`, and `iss`; no email or password data is included.

## Make commands

- `make up` — start the services in the background.
- `make down` — stop and remove the containers, keeping data volumes.
- `make logs` — follow service logs.
- `make ps` — show container status and health.
- `make restart` — restart the services.
- `make clean` — remove containers and data volumes. This permanently deletes PostgreSQL, Redis, and RabbitMQ data.
- `make config` — validate and print the Compose configuration.
- `make dashboards-validate` — run every dashboard PromQL query against the running Prometheus.

## Database migrations

The migration command reads PostgreSQL settings from `.env` (or the process environment) and connects to the local Compose service. Start PostgreSQL with `make up`, then apply all pending migrations with `make migrate-up`.

- `make migrate-down` — roll back one migration.
- `make migrate-version` — show the current version and whether the migration state is dirty.
- `make migrate-force VERSION=2` — example only: set migration bookkeeping to the manually verified version while recovering a dirty state. This does not run SQL; first inspect the database and manually repair or complete the interrupted migration. The command refuses to run unless the state is dirty.

Migration files live in `migrations/` and are not run automatically when the API starts. Wallet balances use integer amounts in each currency's smallest unit: cents for USD/EUR and whole yen for JPY. Deleting a user with a wallet is restricted by the foreign key.

## Simulated deposits and ledger

`POST /api/v1/wallets/{id}/deposits` is a **development/demo simulation only**. It creates funds in the local system; it does not move money or contact a payment provider. Do not expose this endpoint as a real-money deposit endpoint.

Send a positive integer `amount` in the currency's smallest unit and a matching `currency` (`USD`, `EUR`, or `JPY`), plus a unique `Idempotency-Key` header. Repeating the same authenticated user's key with the same wallet, amount, and currency returns the original result without crediting the wallet again. Reusing that key with different request parameters returns `409 Conflict`. Keys are stored with ledger transactions in PostgreSQL.

Each wallet has a ledger account, created by the database when the wallet is inserted; existing wallets receive accounts when migration 3 is applied. The corresponding system funding accounts are seeded for each supported currency. Deposits update the wallet and create equal positive wallet and negative funding entries in one transaction. Ledger entries are append-only; represent any correction with a compensating transaction rather than editing history. Migration 3's down migration refuses to remove the ledger schema while ledger entries exist.

`POST /api/v1/transfers` moves a positive integer amount between two wallets of the same currency. The authenticated user must own the source wallet; the destination can belong to another user. Transfers use the same per-user `Idempotency-Key` table as deposits, and reject a key reused for a different operation or payload. Both wallet rows are locked in UUID order, then the balances and two wallet ledger entries are committed together. Transfers do not use a system funding account. Migration 4's down migration refuses to remove transfer support while transfer records exist.

`GET /api/v1/transfers` requires a bearer access token and returns completed transfers where the authenticated user's wallets are the source or destination. The `limit` parameter defaults to 20 and accepts 1–100; `offset` defaults to 0 and accepts 0–1,000,000. Optional `direction` accepts `incoming` or `outgoing`, and `currency` accepts `USD`, `EUR`, or `JPY` (case-insensitive). Invalid parameters return `400`. Results are ordered by creation time descending, then transfer ID descending. `total` is the count matching the same ownership and filter rules, before pagination; amounts are integer minor units.

```http
GET /api/v1/transfers?limit=20&offset=0&direction=incoming&currency=USD
Authorization: Bearer <access-token>
```

```json
{
  "items": [
    {
      "id": "6b8e9570-06c8-49d4-9bcb-8e902c68e781",
      "source_wallet_id": "a91f9b48-4934-42fb-a286-35b739178c8d",
      "destination_wallet_id": "d1bff4d0-0cd9-4234-9e8d-b1a7d318408f",
      "amount": 1500,
      "currency": "USD",
      "status": "completed",
      "created_at": "2026-11-24T00:00:00Z"
    }
  ],
  "limit": 20,
  "offset": 0,
  "total": 42
}
```

An empty result uses `"items": []`. The existing source history index (`ledger_transactions_wallet_created_idx`) and destination history index (`ledger_transactions_destination_wallet_idx`) support the two wallet relationships and ordering; no additional index is needed.

## Integration tests

PostgreSQL integration tests use `TEST_DATABASE_URL` and require a dedicated database whose name ends in `_test`; the test helper rejects other database names. Create that database separately and apply all migrations to it before running the tests. Never point `TEST_DATABASE_URL` or the migration command at the development database for test runs.

Example for PowerShell (adjust the host, port, username, and test-only credentials for your isolated PostgreSQL instance):

```powershell
$env:POSTGRES_HOST = '127.0.0.1'
$env:POSTGRES_PORT = '5433'
$env:POSTGRES_DB = 'payment_test'
$env:POSTGRES_USER = 'payment_user'
$env:POSTGRES_PASSWORD = 'test-only-password'
go run ./cmd/migrate up
$env:TEST_DATABASE_URL = 'postgres://payment_user:test-only-password@127.0.0.1:5433/payment_test?sslmode=disable'
go test ./...
go test -race ./...
go vet ./...
```

The transfer integration suite covers concurrent transfers from one funded wallet, transfers running in opposite directions, identical concurrent idempotency requests, conflicting reuse of a key, and rollback after a ledger write fails. It checks that balances stay non-negative, wallet totals are conserved, failed requests leave no transfer record, and every completed transfer has exactly two ledger entries whose amounts sum to zero. The race detector checks Go memory races; PostgreSQL concurrency behavior is checked by the integration tests against the dedicated database.

## Transactional outbox

The transactional outbox prevents a committed transfer from being lost between saving it and later notifying other systems. A `transfer.completed` event is inserted in the same PostgreSQL transaction as wallet balance updates, the transfer ledger record, ledger entries, and the idempotency result. If any part fails, PostgreSQL rolls the entire transaction back. Migration 5 persists events; Iteration 4.2's separate worker publishes them to RabbitMQ.

Migration 5 creates `outbox_events` with:

- `id` (UUID primary key), `aggregate_id` (the transfer UUID), and `aggregate_type` (`transfer`).
- `event_type` (currently `transfer.completed`) and `payload` (a JSON object).
- `created_at`, nullable `published_at`, non-negative `attempts` defaulting to zero, and nullable `last_error`.

Migration 6 adds nullable lease ownership (`locked_by`, `locked_until`) and `next_attempt_at`. Its partial `outbox_events_pending_poll_idx` supports polling unpublished events by retry time, creation time, and ID; the aggregate index supports looking up events for an aggregate. The transfer payload is:

```json
{
  "transfer_id": "transfer-uuid",
  "source_wallet_id": "source-wallet-uuid",
  "destination_wallet_id": "destination-wallet-uuid",
  "amount": 1500,
  "currency": "USD",
  "completed_at": "2026-11-24T00:00:00Z"
}
```

The amount is an integer in the currency's smallest unit. Run the outbox integration coverage using the dedicated PostgreSQL test database setup above and `go test ./internal/service/wallet -run TransferOutboxIntegration`. The suite verifies event contents, replay behavior, failed transfers, independent events, and rollback when event insertion fails.

## Outbox worker and RabbitMQ

`make up` starts the official RabbitMQ 4 management image with persistent storage. The AMQP port is published on `127.0.0.1:5672`; the management UI is at `http://localhost:15672`. Local broker credentials come from `RABBITMQ_USER` and `RABBITMQ_PASSWORD` in `.env`. Compose's fallback credentials are for local development only; use separately managed credentials outside local development. Keep these credentials and `RABBITMQ_URL` in sync, URL-encoding special characters in the URL.

Run the API and worker as separate processes, each in its own terminal:

```powershell
go run ./cmd/api
go run ./cmd/worker
```

The API never connects to RabbitMQ. If the broker is unavailable, transfers continue to commit their outbox events, while the separate worker reconnects on its polling interval.

The worker claims due unpublished events in short PostgreSQL transactions with `FOR UPDATE SKIP LOCKED`, then commits those leases before publishing. Active leases prevent normal concurrent claims; an expired lease makes an event available after a worker crash. On publication failure, the worker clears the lease, increments `attempts`, records `last_error`, and schedules exponential backoff starting at one second, capped by `OUTBOX_MAX_BACKOFF`. Confirmed events are marked published and retained in PostgreSQL. RabbitMQ confirmations are awaited, and mandatory publishes that are returned as unroutable are retried.

Delivery is **at least once**, not exactly once. If RabbitMQ confirms a message but PostgreSQL fails to mark it published, the lease eventually expires and the same stable event ID may be published again. Consumers deduplicate using the AMQP message ID.

## Transfer event consumer

Run the consumer separately from the API and outbox publisher:

```powershell
go run ./cmd/consumer
```

Migration 7 adds `processed_events` for event ID deduplication and `notifications` for persisted user notifications. For each `transfer.completed` event, the consumer verifies the immutable event against the outbox and completed ledger transfer, then records outgoing and incoming notifications for the users who own the two wallets. These changes happen in one PostgreSQL transaction. Duplicate event IDs commit without adding notifications. The consumer does not change balances or ledger entries and does not call an external notification service.

The durable topology uses `payment.events` for the main queue, `payment.events.retry` with a broker-managed TTL delay, and `payment.events.dlx` / `payment.events.dlq` for invalid messages and exhausted retries. `CONSUMER_MAX_RETRIES` counts retries after the initial delivery. Retry count is carried in `x-retry-count`; the broker's redelivery flag is not used as an attempt counter. Each retry or dead-letter copy is published persistently with publisher confirmation before the original is acknowledged. A database commit happens before successful acknowledgement. Together with event-ID deduplication, this provides at-least-once delivery without duplicate notification side effects.

The main and dead-letter queues retain messages durably. Inspect stored notices with:

```sql
SELECT user_id, notification_type, payload, created_at
FROM notifications
ORDER BY created_at DESC, id DESC;
```

Configure `RABBITMQ_RETRY_*`, `RABBITMQ_DEAD_LETTER_*`, `RABBITMQ_RETRY_DELAY`, `CONSUMER_MAX_RETRIES`, `CONSUMER_PREFETCH_COUNT`, and `CONSUMER_RECONNECT_INTERVAL` in `.env`. The consumer shares the main queue declared by the publisher; both processes must use the same exchange, queue, and routing key.

Consumer database integration tests require a dedicated migrated database whose name ends in `_test`, configured with `CONSUMER_TEST_DATABASE_URL`. Keep it separate from `TEST_DATABASE_URL` and `OUTBOX_TEST_DATABASE_URL`. Run the database integration coverage with:

```powershell
$env:CONSUMER_TEST_DATABASE_URL = 'postgres://payment_user:test-only-password@127.0.0.1:5433/payment_consumer_test?sslmode=disable'
go test ./internal/consumer/transfer -run TestConsumerDatabaseIntegration -count=1
```

The consumer package's unit tests, including retry routing and acknowledgement ordering, run with `go test ./internal/consumer/transfer -count=1`. Set `TEST_RABBITMQ_URL` to a dedicated broker for any RabbitMQ integration checks; never point it at the development broker.

The RabbitMQ test uses unique exchanges and queues on that broker and verifies a delayed retry and dead-letter routing:

```powershell
$env:TEST_RABBITMQ_URL = 'amqp://test-user:test-password@127.0.0.1:5673/'
go test ./internal/consumer/transfer -run TestRabbitMQDeliveryIntegration -count=1
```

Worker repository tests use a separate database named with the `_test` suffix via `OUTBOX_TEST_DATABASE_URL`; keep it separate from `TEST_DATABASE_URL` because Go runs package tests concurrently. Create it and apply all migrations before testing. RabbitMQ integration tests are opt-in and require a separate test broker; set `TEST_RABBITMQ_URL` to its AMQP URL, never to the development broker. Then run:

```powershell
$env:OUTBOX_TEST_DATABASE_URL = 'postgres://payment_user:test-only-password@127.0.0.1:5433/outbox_worker_test?sslmode=disable'
go test ./internal/worker/outbox -count=1
go test -race ./internal/worker/outbox -count=1
```

## Monitoring

Prometheus and Grafana run in Docker Compose and scrape the three Go processes,
which run on the host. Each process exposes its own endpoint:

| Process | Default endpoint | Metrics |
| --- | --- | --- |
| API | `0.0.0.0:9091/metrics` | HTTP request totals, duration, in-progress requests, pgxpool statistics |
| Worker | `0.0.0.0:9092/metrics` | Claimed and published events, publish failures, retries, active jobs, oldest pending age, pgxpool statistics |
| Consumer | `0.0.0.0:9093/metrics` | Deliveries, processed and duplicate events, failures, dead letters, processing duration, pgxpool statistics |

All three also expose Go runtime and process metrics.

### Start the stack

```sh
docker compose up -d      # PostgreSQL, Redis, RabbitMQ, Prometheus, Grafana
go run ./cmd/api          # terminal 1
go run ./cmd/worker       # terminal 2
go run ./cmd/consumer     # terminal 3
```

Grafana starts only after the Prometheus health check passes, and both
monitoring UIs bind to loopback.

| UI | URL |
| --- | --- |
| Prometheus | http://127.0.0.1:9090 |
| Prometheus targets | http://127.0.0.1:9090/targets |
| Grafana | http://127.0.0.1:3000 |

Sign in with `GRAFANA_ADMIN_USER` and `GRAFANA_ADMIN_PASSWORD` from `.env`.
Anonymous access and sign-up are disabled.

### Dashboards

Dashboard JSON lives in `deployments/monitoring/grafana/dashboards/` and is
version controlled:

- `api-overview.json` — request rate, status mix, 5xx share, latency p50/p95/p99, in-progress requests, pool utilization, connection acquisition.
- `payment-pipeline.json` — transfer acceptance, outbox publishing, consumer processing, plus a notes panel explaining why those three stages are different numbers.

Grafana loads them through the file provider in
`deployments/monitoring/grafana/provisioning/dashboards/dashboards.yml`, which
points at `/var/lib/grafana/dashboards` inside the container. The provider
polls that directory every 30 seconds and `allowUiUpdates` is off, so the files
are the source of truth: edit the JSON, not the browser. The Prometheus
datasource is provisioned from
`deployments/monitoring/grafana/provisioning/datasources/prometheus.yml` with
the fixed uid `prometheus`, which every panel references.

### Scrape targets

`deployments/monitoring/prometheus/prometheus.yml` scrapes four targets every
15 seconds with a 10 second timeout: Prometheus itself, `payment-api`,
`payment-worker`, and `payment-consumer`.

The Go processes run on the host while Prometheus runs in a container, so the
targets use `host.docker.internal`, which Compose maps to the host gateway
through `extra_hosts`. That is why `.env.example` binds the metrics listeners to
`0.0.0.0`: a loopback-only listener is unreachable from inside the container.

If Prometheus runs on the host, or the processes run on another machine, replace
`host.docker.internal` with a reachable address:

```yaml
  - job_name: payment-api
    static_configs:
      - targets: ["192.168.1.20:9091"]
```

If the Go processes are containerised later, use Compose service names instead:

```yaml
      - targets: ["api:9091"]
```

Reload after editing, without restarting the container:

```sh
curl -X POST http://127.0.0.1:9090/-/reload
```

### Configure Grafana credentials

Set `GRAFANA_ADMIN_USER` and `GRAFANA_ADMIN_PASSWORD` in `.env` before the first
start; Compose refuses to start Grafana when either is missing. Grafana applies
the password only when it creates the admin user in an empty database, so
changing it later needs the Grafana UI or `docker compose down -v`, which also
deletes the Grafana and Prometheus volumes. `.env` is gitignored and
`.env.example` holds placeholders only.

### Security

Both monitoring ports are published on `127.0.0.1` only, and Grafana
authentication stays enabled. Metrics carry no user, wallet, or transfer
identifiers: labels are limited to method, route pattern, and status code. For
production, keep the stack on a private network, put Grafana behind an
authenticated reverse proxy, and firewall the metrics ports so only Prometheus
can reach them. Do not expose 9090 or 3000 publicly.

### Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| Target is DOWN with `connect: connection refused` | The process is not running, or it listens on another port. Start it and compare `API_METRICS_ADDR` with the target in `deployments/monitoring/prometheus/prometheus.yml`. |
| Targets are DOWN with `context deadline exceeded` | The listener is on `127.0.0.1`, so the container cannot reach it. Set `API_METRICS_ADDR=0.0.0.0:9091` (and the worker/consumer equivalents) and restart the processes. |
| Targets are DOWN after editing the scrape config | Prometheus validates the config on reload. Check `docker compose logs prometheus` and run the `promtool` check below. |
| Grafana shows "No data" | The datasource works but the query has no series. Send a request to the API or widen the time range; panels that depend on 5xx responses stay empty until an error happens. |
| Grafana runs but has no dashboards | The JSON is missing from `deployments/monitoring/grafana/dashboards/`, or the provisioning path does not match the mount. Check `docker compose logs grafana`. |
| Worker logs `failed to connect ... tls error` | `PGSSLMODE` is not `disable` and the container serves no TLS certificate. |
| Prometheus is unhealthy right after `up` | The first start creates the time series database. The health check covers it and Grafana already waits for it. |

### Validate the monitoring configuration

```sh
docker compose config
docker run --rm -v "$(pwd -W)/deployments/monitoring/prometheus:/cfg:ro" \
  --entrypoint promtool prom/prometheus:v3.16.0-busybox check config /cfg/prometheus.yml
python deployments/monitoring/validate_dashboards.py
curl http://127.0.0.1:9090/api/v1/targets
```

`validate_dashboards.py` needs only the standard library and a reachable
Prometheus. It substitutes the Grafana-only variables and reports `OK`, `EMPTY`,
or `ERROR` for every panel query.

## Distributed tracing

Every process exports OpenTelemetry traces over OTLP. A transfer can be followed
from the HTTP request that accepted it, through the transaction and the outbox,
across RabbitMQ, and into the consumer that records the notification.

### Components and span names

| Process | Span | What it covers |
| --- | --- | --- |
| API | `POST /api/v1/transfers` | Incoming HTTP request, named after the route pattern |
| API | `transfer.validate` | Request validation before any database work |
| API | `transfer.create` | The whole transfer transaction |
| API | `transfer.claim_idempotency`, `transfer.lock_wallets`, `transfer.update_balances`, `transfer.write_ledger_entries`, `transfer.commit` | Transaction steps |
| API | `outbox.enqueue` | Writing the `transfer.completed` event |
| Worker | `outbox.claim`, `outbox.publish`, `outbox.mark_published`, `outbox.schedule_retry` | Publishing loop |
| Consumer | `consumer.process`, `consumer.decode`, `notification.record` | Delivery handling |
| All | `SELECT`, `INSERT`, `UPDATE`, `BEGIN`, `COMMIT`, `pool.acquire` | pgx, created by `otelpgx` |

The database spans come from the pgx tracer, so the repository does not wrap
every query by hand.

### Enable and disable

Tracing is on by default. Set `OTEL_TRACING_ENABLED=false` to turn it off: no
exporter is created, no spans are produced, and the global provider stays
untouched. The processes behave exactly as they did before tracing existed, so
environments without a collector can keep it disabled.

### Service names and endpoint

`OTEL_EXPORTER_OTLP_ENDPOINT` defaults to `localhost:4317`. Both `host:port` and
a URL are accepted; `https://` turns on TLS, `http://` and bare `host:port` do
not. Each binary uses its own default service name — `mini-payment-api`,
`mini-payment-worker`, `mini-payment-consumer` — and `OTEL_SERVICE_NAME`
overrides all three when you want a single name.

Spans are batched and flushed on shutdown with a five second timeout, so a
SIGTERM does not drop the tail of a trace.

### Sampling

`OTEL_TRACES_SAMPLER` accepts `always_on`, `always_off`, `traceidratio`,
`parentbased_always_on`, `parentbased_always_off`, and
`parentbased_traceidratio`. The default is `parentbased_traceidratio` with
`OTEL_TRACES_SAMPLER_ARG=1.0`, which samples everything locally but still
respects a sampling decision made upstream. For a busy environment start with
`0.1` and lower it from there.

### Trace context propagation

The context travels as a W3C `traceparent` header. Nothing is stored in the
event payload, so the financial contract is unchanged.

| Hop | Where the context lives |
| --- | --- |
| Client → API | `traceparent` request header |
| API → outbox | `outbox_events.trace_parent` and `trace_state` columns |
| Worker → RabbitMQ | `traceparent` AMQP header |
| RabbitMQ → consumer | the same AMQP header |

The worker runs in another process, so it rebuilds the context from the
database columns instead of relying on memory. The consumer does not continue
the producer trace as a child: it starts a new trace and adds a span link, so
the producer span is not stretched across the time the message waited in the
queue. In Jaeger the link shows up as a `FOLLOWS_FROM` reference.

Migration `000008_add_outbox_trace_context` adds the two columns with an empty
default, so rows written before this change simply start a new trace.

### Local backend

Jaeger is part of the Compose stack:

```sh
docker compose up -d jaeger
```

- UI: `http://127.0.0.1:16686`
- OTLP gRPC ingestion: `127.0.0.1:4317`

Both ports bind to loopback. Jaeger stores traces in memory, so they disappear
when the container stops.

To run without Jaeger, either leave it out of `docker compose up` and set
`OTEL_TRACING_ENABLED=false`, or keep tracing on and accept that exports fail
quietly — the batch processor drops spans and the processes keep running.

### Finding a trace

Paste the trace ID into the search box in the Jaeger UI, or query the API:

```sh
curl "http://127.0.0.1:16686/api/traces/<trace-id>"
```

Log lines carry the same identifiers, so the fastest path from a log record to
a trace is to copy `trace_id` from the JSON log and search for it. A record
logged with a context gets `trace_id` and `span_id` appended automatically by
the shared handler.

### Privacy

- Spans record `http.route` (the pattern, for example `/api/v1/wallets/{id}`)
  and never `url.path`, so wallet and transfer identifiers stay out of traces.
- Authorization headers, cookies, passwords, and request bodies are not
  recorded. The middleware sets no header attributes at all.
- Idempotency keys are never attached to a span.
- `otelpgx` records the SQL statement text but not the bind parameters, so
  amounts and identifiers are not visible in database spans.
- Error status messages carry the Go error text, which describes the failure
  rather than the data.

### Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| No traces at all | `OTEL_TRACING_ENABLED` is `false`, or the endpoint points somewhere unreachable. Check the `tracing enabled` log line for the resolved service, endpoint, and sampler. |
| Only API spans, no worker spans | The worker is not running, or it started before migration `000008` was applied. |
| Only the API span, no transaction steps | The pool was created before tracing started; the tracer is attached when the pool is built. |
| Consumer trace has no link | The message was published without a `traceparent` header, which happens when the worker ran with tracing disabled. |
| Jaeger UI is empty but spans are exported | Jaeger keeps traces in memory and restarts empty. Check the time range in the UI. |
