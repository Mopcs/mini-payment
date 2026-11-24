 az# Mini Payment Processing System

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
4. Run `make up` to start PostgreSQL, Redis, and RabbitMQ.
5. Run `make ps` to check container health.
6. Start the API with `go run ./cmd/api`.
7. Check `http://localhost:8080/health` and `http://localhost:8080/ready`.

All three services publish ports only on `127.0.0.1`. Their data is stored in Docker named volumes and survives container restarts. Redis persistence uses AOF.

## Environment variables

| Variable | Purpose | Example |
| --- | --- | --- |
| `APP_ADDR` | HTTP listen address | `:8080` |
| `POSTGRES_DB` | PostgreSQL database name | `payment_db` |
| `POSTGRES_USER` | PostgreSQL user | `payment_user` |
| `POSTGRES_PASSWORD` | Local PostgreSQL password | `local_dev_postgres_password` |
| `POSTGRES_HOST` | Host used by local clients | `127.0.0.1` |
| `POSTGRES_PORT` | Published PostgreSQL port | `5432` |
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

Delivery is **at least once**, not exactly once. If RabbitMQ confirms a message but PostgreSQL fails to mark it published, the lease eventually expires and the same stable event ID may be published again. Consumers must deduplicate using the AMQP message ID. This iteration includes no consumer, dead-letter queue, or metrics.

Worker repository tests use a separate database named with the `_test` suffix via `OUTBOX_TEST_DATABASE_URL`; keep it separate from `TEST_DATABASE_URL` because Go runs package tests concurrently. Create it and apply all migrations before testing. RabbitMQ integration tests are opt-in and require a separate test broker; set `TEST_RABBITMQ_URL` to its AMQP URL, never to the development broker. Then run:

```powershell
$env:OUTBOX_TEST_DATABASE_URL = 'postgres://payment_user:test-only-password@127.0.0.1:5433/outbox_worker_test?sslmode=disable'
go test ./internal/worker/outbox -count=1
go test -race ./internal/worker/outbox -count=1
```
