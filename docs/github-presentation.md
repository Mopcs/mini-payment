# GitHub presentation

Suggestions for repository metadata. No fake badges or invented URLs are used.

| Item | Recommendation |
| --- | --- |
| Description | One-sentence summary below |
| Topics | Tags below |
| CI badge | Add only after the remote and workflow status are verified |
| README opening | Already concise and states that deposits are simulated |

The Git remote in this checkout is `https://github.com/Mopcs/mini-payment.git`.
The Go module is `github.com/Mopcs/mini-payment-system`. If that difference is
intentional, keep it; if not, fix the module or the remote before publishing a
badge.

## Repository description

> Go modular monolith for wallet transfers with a PostgreSQL double-entry
> ledger, idempotent deposits and transfers, transactional outbox, RabbitMQ
> consumer, Prometheus/Grafana observability, and k6 load tests. Deposits are
> simulated.

Under 350 characters and limited to implemented features.

## Suggested topics

```text
go, golang, backend, rest-api, postgresql, redis, rabbitmq, jwt,
double-entry-ledger, idempotency, transactional-outbox, docker-compose,
prometheus, grafana, opentelemetry, k6, load-testing
```

`microservices` can be added for discovery, but the implementation is a modular
monolith with three processes, not a set of independently deployed services.

## CI badge

The workflow is `.github/workflows/ci.yml`. It triggers on pushes and pull
requests to `main` or `master`, uses the Go version from `go.mod`, and runs
formatting, vet, migration, hygiene, Compose, and race-test jobs with real
PostgreSQL, Redis, and RabbitMQ services.

Add a badge only after confirming the workflow is green on the default branch:

```markdown
[![CI](https://github.com/<owner>/<repo>/actions/workflows/ci.yml/badge.svg)](https://github.com/<owner>/<repo>/actions/workflows/ci.yml)
```

Replace `<owner>` and `<repo>` with the verified public values. A badge that
points to the wrong repository is worse than no badge.

## Interview talking points

- Money as `BIGINT` minor units; append-only double-entry ledger with deferred
  balance checks.
- Deterministic UUID-order wallet locking, so A→B and B→A cannot deadlock.
- Idempotency with `(user_id, idempotency_key)` and a 32-byte request hash.
- Transactional outbox, publisher confirms, at-least-once delivery, and a
  consumer deduplicated by event ID.
- Measured performance work: a JIT-inducing query plan was rewritten and the
  outbox drain loop was tuned from measured per-event costs.
- Known limitation: services import concrete PostgreSQL repositories rather
  than interfaces; changing storage would require a deliberate refactor.
