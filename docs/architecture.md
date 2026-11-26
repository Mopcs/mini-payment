# Architecture diagrams

These diagrams describe the implemented system, not a target design. The
runtime is a modular monolith with three processes: `cmd/api`, `cmd/worker`,
and `cmd/consumer`. All financial state lives in PostgreSQL.

## System architecture

```mermaid
flowchart LR
    Client["HTTP client"]

    subgraph App["Application processes"]
        API["cmd/api<br/>HTTP handlers, JWT, wallets, transfers"]
        Worker["cmd/worker<br/>outbox relay"]
        Consumer["cmd/consumer<br/>event processor"]
    end

    subgraph Data["State and messaging"]
        PG[("PostgreSQL<br/>ledger, outbox, notifications")]
        Redis[("Redis<br/>rate-limit counters")]
        Rabbit["RabbitMQ<br/>durable events, retry, DLQ"]
    end

    subgraph Observability["Observability"]
        Prom["Prometheus"]
        Grafana["Grafana"]
        Jaeger["Jaeger<br/>OTLP trace backend"]
    end

    Client -->|"REST over HTTP"| API
    API -->|"SQL transactions"| PG
    API -->|"INCR/PEXPIRE and readiness"| Redis
    PG -->|"claim unpublished rows"| Worker
    Worker -->|"publish with confirms"| Rabbit
    Rabbit -->|"at-least-once delivery"| Consumer
    Consumer -->|"processed events + notifications"| PG

    API -.->|"/metrics"| Prom
    Worker -.->|"/metrics"| Prom
    Consumer -.->|"/metrics"| Prom
    Prom --> Grafana
    API -.->|"OTLP spans"| Jaeger
    Worker -.->|"OTLP spans"| Jaeger
    Consumer -.->|"OTLP spans"| Jaeger
```

Notes:

- The API also serves `/metrics` on its public listener; each process has a
  separate metrics port for Prometheus scraping.
- pprof is served only when `PROFILING_ENABLED=true` on a separate loopback
  listener; it is never part of the public API handler chain.
- Redis stores no balances, ledger rows, or idempotency records.

## Transfer sequence

```mermaid
sequenceDiagram
    participant C as Client
    participant A as API
    participant P as PostgreSQL

    C->>A: POST /api/v1/transfers (Bearer + Idempotency-Key)
    A->>A: Validate JWT and request body
    A->>P: BEGIN
    A->>P: INSERT ledger_transactions (claim idempotency key)
    alt key already exists with same hash
        P-->>A: existing transaction ID
        A->>P: SELECT stored transfer result
        A->>P: COMMIT
        P-->>A: original result
        A-->>C: 201 with Idempotent-Replay: true
    else new transfer
        P-->>A: new transaction ID
        A->>P: SELECT both wallets FOR UPDATE in UUID order
        P-->>A: owner, currency, balance
        A->>P: UPDATE wallets (debit source, credit destination)
        A->>P: UPDATE ledger_transactions with balances after
        A->>P: INSERT two ledger_entries (-amount, +amount)
        A->>P: INSERT outbox_events
        A->>P: COMMIT (deferred balanced check runs)
        P-->>A: committed
        A-->>C: 201 with transfer result
    end
```

Important details:

- Ownership, currency, amount, and balance checks happen after both wallet
  rows are locked, in a deterministic UUID order.
- The outbox row is written inside the same transaction as the money movement.
  A rollback cannot publish a transfer that did not happen.
- The deferred trigger verifies exactly two balanced entries before COMMIT can
  succeed. Idempotency conflicts abort the transaction.

## Outbox and RabbitMQ processing

```mermaid
sequenceDiagram
    participant W as Outbox worker
    participant P as PostgreSQL
    participant R as RabbitMQ
    participant Cn as Consumer

    W->>P: SELECT unpublished events FOR UPDATE SKIP LOCKED (batch)
    P-->>W: claimed events
    loop each event
        W->>P: Extend lease if needed
        W->>R: Publish durable message with traceparent
        R-->>W: Publisher confirmation
        W->>P: UPDATE outbox_events SET published_at
    end

    R->>Cn: Deliver message (at least once)
    Cn->>P: BEGIN
    Cn->>P: Verify message against stored outbox row
    Cn->>P: INSERT processed_events ON CONFLICT DO NOTHING
    Cn->>P: INSERT notifications
    P-->>Cn: COMMIT
    alt success, including duplicate
        Cn->>R: ACK
    else retryable failure
        Cn->>R: Publish to retry exchange, then ACK original
    else permanent failure or retries exhausted
        Cn->>R: Publish to dead-letter queue, then ACK original
    end
```

Important details:

- The worker drains another batch immediately when the previous one was full,
  and otherwise waits for the configured poll interval.
- A message is ACKed only after the notification transaction commits. A
  duplicate delivery commits nothing and is still ACKed.
- Publisher confirms, durable exchanges and queues, at-least-once delivery,
  bounded retry, and a dead-letter queue are part of the implemented topology.
