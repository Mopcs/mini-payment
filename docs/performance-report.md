# Performance analysis

Measured findings from iteration 7.2. Every number below came from an isolated
local stack — PostgreSQL 17, Redis, RabbitMQ, API, outbox worker, consumer. No
production data was used and no load test targeted a shared stack. Where a
measurement was not taken, it is labelled as such.

The k6 suite and its safety rules are in
[../tests/load/README.md](../tests/load/README.md).

## TL;DR

| Bottleneck | Evidence | Change | Measured result |
| --- | --- | --- | --- |
| Transfer history triggered a sequential scan and per-query JIT | `Seq Scan`, cost 899 108, JIT 795.7 ms of 1500.3 ms | Join from wallets + `UNION` | Page query 22.1 ms; endpoint p95 584 → 35.5 ms |
| Outbox relay waited out the poll interval with a backlog | 8.6 ms/event, 172 ms/batch, ~830 ms idle per tick | Re-claim immediately after a full batch | ~20/s → ~94/s, no extra concurrency |
| Callers queued for pool connections | `acquired` 10 = `MaxConns`, 49 280 empty acquires / 2 h | Made settings configurable, defaults unchanged | Still present; not a proven database limit |
| Same-wallet lock waits | 4–7 waiting sessions, **0** deadlocks, hot p95 206 ms | None — UUID ordering is correct | Reported as queueing time with its own budget |

No index, isolation level, financial transaction boundary, or guarantee was
changed to improve these numbers.

## 1. Baseline

Scenarios were run before and after the changes. Raw JSON reports are in
`tests/load/results/` (gitignored).

| Workload | Before | After |
| --- | --- | --- |
| Read baseline, 3 VUs / 30 s | p95 585 ms, 6/9 thresholds | 514 requests, 15.2/s, p50 4 ms, p95 74 ms, p99 120 ms, 0% failed, 9/9 |
| Transfers, 10 senders + 4 hot VUs / 1 min | 11 815 requests, 186.2/s, p95 138 ms (earlier host run) | 8 241 requests, 127.0/s, p50 88 ms, p95 198 ms, p99 306 ms, 0% failed, 8/8 |
| Hot-wallet branch | Failed the shared 300 ms budget | p50 93.6 ms, p95 205.6 ms, p99 319.8 ms; normal p95 194.2 ms |
| Idempotency | 2/2 | 2/2 |
| Stress, 2→50 VUs | p95 1 107 ms, p99 1 681 ms vs 300/800 budget | Same load; dedicated 2 000/5 000 budgets, 6/6 |
| Outbox publishing | ~20 events/s | ~94 events/s |
| Consumer | Kept up; no stale events | Same, 12/12 invariants |

The transfer rows are **not** a controlled A/B: they are separate runs on a
growing data set with different account cohorts. The history and outbox
comparisons below are controlled before/after runs of the same path.

Supporting evidence:

- `log_min_duration_statement` showed 220–412 ms with one VU and 700–1 277 ms
  with three: the time was in PostgreSQL, not the Go handler.
- `n_dead_tup` was 518, no CPU quota existed, and prepared plans were not stale.
- `payment_postgres_pool_connections{state="acquired"}` sat at 10 (`MaxConns`)
  while `payment_postgres_pool_empty_acquire_total` reached 49 280 over two hours.

## 2. Query plans

Seven paths were inspected with `EXPLAIN (ANALYZE, BUFFERS)` on a dedicated
database: wallet lookup, transfer creation, idempotency lookup, transfer
history, ledger insertion, outbox claiming, processed-event lookup. Six used
their existing indexes. Only history was pathological.

**Hypothesis.** `EXISTS(source) OR EXISTS(destination)` is not sargable for this
shape. PostgreSQL scanned `ledger_transactions` and the high cost crossed
`jit_above_cost` (100 000), so it compiled the query on every execution.

Original plan, replayed on the same data:

```text
Limit  (cost=899107.52..899107.57 rows=20)
  ->  Sort  (cost=899107.52..899210.39 rows=41145)
        Sort Method: top-N heapsort  Memory: 29kB
        ->  Seq Scan on ledger_transactions lt
              (cost=0.00..898012.67 rows=41145)
            actual time=24.118..35.590 rows=8828
JIT: Generation 7.308 ms, Inlining 121.930 ms,
     Optimization 430.079 ms, Emission 236.387 ms, Total 795.703 ms
Execution Time: 1500.305 ms
```

The scan itself took ~36 ms; JIT took ~796 ms. An earlier sample showed
`66.253 + 144.645 + 89.008 = 301.871 ms` of JIT in a 337 ms execution.

**Change.** Join from the user's wallets into `ledger_transactions`, combining
both directions with `UNION`:

```text
Aggregate  (cost=4178.18..4178.19)     Execution Time: 64.896 ms  -- count
Limit      (cost=4233.46..4233.51)     Execution Time: 22.141 ms  -- page
  -> Sort -> HashAggregate -> Append
       -> Index Scan using wallets_user_currency_unique
       -> Bitmap Index Scan on ledger_transactions_wallet_created_idx
```

`UNION` preserves self-transfer deduplication. `ORDER BY created_at DESC,
id DESC` and the single `RepeatableRead` snapshot for count plus page are
unchanged. No user input is interpolated into SQL.

**Index changes: none.** Existing composite indexes and the partial
`outbox_events_pending_poll_idx (next_attempt_at, created_at, id) WHERE
published_at IS NULL` matched the measured plans.

## 3. Transaction contention

The transfer transaction locks both wallets with `FOR UPDATE` in UUID order,
checks ownership and balance, writes balances, transaction row, ledger entries,
and outbox event, verifies the ledger, then commits. No RabbitMQ or other
network operation occurs inside it.

During a one-minute run with 4 hot-wallet VUs:

- 120 samples at 500 ms: 6–7 sessions waiting on `Lock` (`tuple`,
  `transactionid`).
- 100 samples at 200 ms: 4 waiting, peak active transaction age 77 ms.
- `pg_stat_database.deadlocks` was **0** before and after.
- Hot p95 205.6 ms vs 194.2 ms for disjoint wallets — real but modest queueing.
- `TestTransferIntegration` (16 concurrent opposite-direction transfers plus
  replay and concurrent-source cases) passed.

Deterministic UUID ordering works. Weakening isolation or moving financial
writes outside the transaction would trade correctness for an unmeasured gain.

## 4. Go profiling

`internal/profiling` serves pprof only when `PROFILING_ENABLED=true`, on a
loopback-only listener, never on the public API mux. Mutex/block sampling is
enabled only while running and reset on shutdown. Verified: 200 on
`127.0.0.1:6061`, 404 on `:8081`; goroutine, heap, mutex, block, and allocs
endpoints all returned 200. Capture commands are in the root README.

A 20 s CPU profile during load: 370 ms of samples (1.85% wall time),
`runtime.cgocall` 120 ms flat, `encoding/json.Encode` 40 ms cumulative, no
significant application hotspot. Heap allocations were dominated by
`argon2.initBlocks` (1 600 MB cumulative) during account setup, not steady state.

**No HTTP or Go runtime change was made.** Reducing argon2 work would weaken
security; replacing the encoder or adding `sync.Pool` has no supporting profile.

## 5. Pool, RabbitMQ, Redis

**pgxpool.** `POSTGRES_MAX_CONNS` (10), `POSTGRES_MIN_CONNS` (1),
`POSTGRES_MAX_CONN_LIFETIME` (1h), and `POSTGRES_MAX_CONN_IDLE_TIME` (30m) are
configurable with the previous hardcoded defaults. `min > max` is rejected.
Three processes × 10 connections = 30, below PostgreSQL's default 100. The
limit was not raised: caller queuing alone does not prove the database can use
more connections.

**Outbox worker.** Measured per event: `ExtendLease` 3.4 ms, publish + trace
fork 3.3 ms (~302/s), `MarkPublished` 1.9 ms ≈ 8.6 ms/event, or 172 ms per
20-event batch. The remaining ~830 ms of each tick was idle. The worker now
claims another batch immediately after a full one; drain rate rose from ~20/s
to ~94/s, near the ~116/s single-worker ceiling. Batch size, poll interval,
lease, retries, confirms, durability, and concurrency are unchanged. No ACK
before database commit.

**Redis.** Only rate-limit counters; no balance cache. Statistics after load:
55 282 `EVALSHA`, 1 `EVAL` (script load), 55 282 `INCR`, 31 `PEXPIRE`,
0 rejected connections, 11 clients. One Lua round trip per limited request, no
saturation. No change.

## 6. Regression

- `gofmt -l .` — no output; `go vet ./...` — clean.
- `go test ./...` and `go test -race ./...` with dedicated test databases — pass.
- k6 `baseline` 9/9, `transfers` 8/8, 0% failures.
- `tests/load/verify/run-verify.sh` — 12/12 invariants pass.
- No assertion or threshold was loosened. Separate budgets exist only for lock
  queueing and deliberate overload; error and correctness budgets are unchanged.

## 7. Remaining work

1. **Pool size** — run a controlled two-size experiment under the same k6
   profile, watching p95 and database connections. The settings are already
   configurable.
2. **Hot-wallet queueing** — inherent to one serialisable source wallet;
   reducing it needs a product-level accounting change.
3. **Outbox ceiling** — near ~116/s for one worker. Measure a second instance
   against `SKIP LOCKED` before adding concurrency.
4. **Consumer throughput** — no standalone events/s benchmark; only invariant
   evidence. Add a dedicated benchmark.
5. **Password hashing** — dominates setup allocations; any change must preserve
   argon2id parameters and get its own threat model.
6. **JIT thresholds** — left at PostgreSQL defaults. Revisit only if another
   measured plan shows the same compilation overhead.
