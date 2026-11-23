# Mini Payment Processing System

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
4. Run `make up` to start PostgreSQL and Redis.
5. Run `make ps` to check container health.
6. Start the API with `go run ./cmd/api`.
7. Check `http://localhost:8080/health` and `http://localhost:8080/ready`.

Both services publish ports only on `127.0.0.1`. Their data is stored in Docker named volumes and survives container restarts. Redis persistence uses AOF.

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
| `LOG_LEVEL` | JSON log level (`debug`, `info`, `warn`, or `error`) | `info` |
| `JWT_SECRET` | HMAC signing key, at least 32 bytes | replace the example placeholder |
| `JWT_ISSUER` | JWT issuer claim | `mini-payment-system` |
| `JWT_ACCESS_TOKEN_TTL` | Access token lifetime, from 1 second to 24 hours | `15m` |

Compose requires both passwords from `.env`; the example values are for local development only.
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
- `make clean` — remove containers and data volumes. This permanently deletes PostgreSQL and Redis data.
- `make config` — validate and print the Compose configuration.

## Database migrations

The migration command reads PostgreSQL settings from `.env` (or the process environment) and connects to the local Compose service. Start PostgreSQL with `make up`, then apply all pending migrations with `make migrate-up`.

- `make migrate-down` — roll back one migration.
- `make migrate-version` — show the current version and whether the migration state is dirty.
- `make migrate-force VERSION=2` — example only: set migration bookkeeping to the manually verified version while recovering a dirty state. This does not run SQL; first inspect the database and manually repair or complete the interrupted migration. The command refuses to run unless the state is dirty.

Migration files live in `migrations/` and are not run automatically when the API starts. Wallet balances use integer amounts in each currency's smallest unit: cents for USD/EUR and whole yen for JPY. Deleting a user with a wallet is restricted by the foreign key.
