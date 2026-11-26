# Architecture decisions

This document explains the trade-offs behind the implemented design. It is for
interview discussion, not a list of planned features.

| Decision | Main reason | Main trade-off |
| --- | --- | --- |
| Modular monolith | Three processes already separate the expensive boundaries | Scaling is an operator choice, not service ownership |
| PostgreSQL as source of truth | One transactional boundary for money | Shared connection limit across processes |
| Integer minor units | Exact arithmetic, no floating point | Presentation must format units |
| Double-entry ledger | Auditable, impossible to commit unbalanced | Two extra inserts and a deferred check |
| Idempotency keys | Retries must not move money twice | Keys live as long as the ledger |
| Deterministic wallet locks | A→B and B→A cannot deadlock | Hot wallets are serialisation points |
| Transactional outbox | No lost or phantom events across DB and broker | Asynchronous delivery and a backlog to drain |
| At-least-once delivery | Never lose an event on a crash | Duplicates are possible |
| Idempotent consumer | Duplicates become safe no-ops | One short DB transaction per delivery |
| Redis without balances | Avoid a second source of truth | Rate limiting fails closed when Redis is down |

## Modular monolith

**Decision.** Ship three Go processes from one module: API, outbox worker, and
consumer. They share the same source tree and database but have independent
entrypoints under `cmd/`.

**Why.** The domain is small, and the expensive boundaries are already present:
HTTP at the edge, PostgreSQL transactions in the middle, and RabbitMQ at the
asynchronous edge. Splitting into many deployable services would add network
hops and distributed debugging without solving a problem this system has.

**Trade-off.** All processes are deployed together from one repository, so they
must be scaled independently by operator choice rather than by service
ownership. The service layer also imports concrete PostgreSQL repositories
instead of interfaces; changing storage would require a deliberate refactor.

## PostgreSQL is the financial source of truth

**Decision.** Every balance, ledger row, idempotency record, outbox row, and
processed-event marker is committed to PostgreSQL. Redis holds only rate-limit
counters.

**Why.** Financial correctness needs one authoritative transactional boundary.
PostgreSQL gives us row locks, uniqueness constraints, deferred constraint
triggers, and durable commits in one place.

**Trade-off.** PostgreSQL connection count is shared by all three processes.
Pool sizes are configurable but deliberately conservative (10 per process by
default) because `MaxConns` is not free.

## Integer minor units

**Decision.** Amounts and balances are `BIGINT` in PostgreSQL and `int64` in Go.

**Why.** Floating point cannot represent currency amounts exactly. Storing
cents (or whole yen) keeps arithmetic deterministic and lets the database
enforce positivity and overflow boundaries.

**Trade-off.** Presentation code must format minor units, and conversion
between currencies is deliberately not implemented.

## Double-entry ledger

**Decision.** Each financial transaction writes exactly two `ledger_entries`:
a debit and a credit that sum to zero. A deferred trigger checks the entries at
commit time, and a separate trigger rejects updates and deletes.

**Why.** A mutable balance alone cannot explain where money came from. An
append-only ledger gives an auditable history; the deferred check makes an
unbalanced transaction impossible to commit.

**Trade-off.** Two extra inserts per transaction and a trigger that reads the
rows at commit. This is acceptable for a payment ledger, where correctness is
worth more than a small write overhead.

## Idempotency

**Decision.** Deposits and transfers require an `Idempotency-Key` header. The
`(user_id, idempotency_key)` uniqueness constraint reserves the operation, and
a 32-byte `request_hash` distinguishes a genuine retry from key reuse with a
different payload.

**Why.** Clients retry on timeouts. Without idempotency a retry could move
money twice. The hash check turns a dangerous retry into either the original
result or a `409 idempotency_conflict`.

**Trade-off.** Keys must be stored for the lifetime of the ledger; they are not
expired. Replay keeps a short transaction open to read the original result.

## Row-level locking in deterministic order

**Decision.** A transfer locks both wallet rows with `SELECT ... FOR UPDATE`,
ordered by UUID bytes. Ownership, currency, amount, and balance checks run
after both locks are held.

**Why.** Concurrent transfers from the same wallet must serialise. Locking both
rows in the same order means an A→B transfer and a B→A transfer cannot wait on
each other in opposite order, so the system does not create deadlocks.

**Trade-off.** A hot wallet is a serialisation point: contention is queueing
time, not service time. It is measured separately in the load suite rather
than hidden by weakening isolation.

## Transactional outbox

**Decision.** The transfer transaction inserts an `outbox_events` row before it
commits. The worker claims unpublished rows later and publishes them to
RabbitMQ.

**Why.** Writing to a broker and committing to a database cannot be one atomic
operation. If the transfer committed and the publish failed, the event would be
lost. If publishing happened first, a rollback would leave a phantom event.
The outbox makes the database the single commit point.

**Trade-off.** Delivery is asynchronous, so a worker outage creates a backlog.
The claim query uses a partial index and `FOR UPDATE SKIP LOCKED`; leases,
retries, and backoff keep a stalled worker from blocking the queue.

## At-least-once event delivery

**Decision.** RabbitMQ messages are durable, published with confirms, and
acked by the consumer only after its database transaction commits. Failures go
to a retry exchange and then to a dead-letter queue.

**Why.** A crash between processing and ACK causes redelivery. At-least-once
delivery accepts that duplication is possible in exchange for not losing
events. Publisher confirms make sure the broker accepted a message before the
outbox row is marked published.

**Trade-off.** Consumers must tolerate duplicates; the API must not assume
notifications are immediate.

## Idempotent consumer processing

**Decision.** The consumer verifies each message against the stored outbox
row, inserts an `event_id` marker with `ON CONFLICT DO NOTHING`, and writes
notifications in the same transaction. A duplicate delivery is a successful
no-op.

**Why.** At-least-once delivery guarantees duplicates will happen. The marker
makes processing safe, and verification against the persisted event prevents a
forged message from creating notifications.

**Trade-off.** The consumer reads PostgreSQL for every delivery; a hot event
still causes a short transaction even when it is a duplicate.

## Redis is not used for balances

**Decision.** Redis stores rate-limit counters and answers readiness pings. No
balance, ledger row, or idempotency record is cached there.

**Why.** A cache of financial state would introduce a second source of truth
and a stale-read problem. Rate limiting is a counters problem where an
approximate, shared budget is acceptable, and Redis has the right primitives.

**Trade-off.** Rate limiting fails closed when Redis is unavailable. That is a
deliberate availability trade-off: an unmetered financial endpoint is worse
than a temporarily rejected request.
