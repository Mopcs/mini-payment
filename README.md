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

Compose requires both passwords from `.env`; the example values are for local development only.
Environment variables already set in the shell take precedence over values loaded from `.env`.

## Make commands

- `make up` — start the services in the background.
- `make down` — stop and remove the containers, keeping data volumes.
- `make logs` — follow service logs.
- `make ps` — show container status and health.
- `make restart` — restart the services.
- `make clean` — remove containers and data volumes. This permanently deletes PostgreSQL and Redis data.
- `make config` — validate and print the Compose configuration.
