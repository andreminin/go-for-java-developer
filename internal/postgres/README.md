# PostgreSQL for Go Developers: Transactions, Outbox, Idempotency

## Why This Matters for Java Developers

In Java, transactions usually hide behind Spring `@Transactional`, JPA, and JDBC
(`Connection.setTransactionIsolation`, `setAutoCommit(false)`). The same ACID
guarantees apply in Go, but the wiring is explicit: `db.BeginTx(ctx, &sql.TxOptions{...})`,
deferred `Rollback` (a no-op after `Commit`), and manual retry of serialization
failures. PostgreSQL specifics — MVCC snapshots, `SELECT ... FOR UPDATE`,
`SKIP LOCKED` queues, and the outbox/inbox patterns — transfer directly; only the
plumbing syntax changes.

## Key Concepts

- **MVCC** — readers never block writers; each statement/transaction sees a snapshot.
  No read locks for plain `SELECT`, unlike pessimistic JPA locking.
- **Read Committed** (PG default) — new snapshot per statement; non-repeatable reads
  and phantoms possible. Same contract as JDBC default.
- **Repeatable Read** — snapshot for the whole transaction; in PostgreSQL there are
  NO phantoms (stronger than the SQL standard and JDBC expectations).
- **Serializable (SSI)** — tracks dependencies and aborts with SQLSTATE `40001`
  (`could not serialize access`); the application must retry with backoff.
- **`SELECT ... FOR UPDATE`** — row-level pessimistic lock held until transaction end;
  fixes lost updates. `SKIP LOCKED` builds queues (competing workers skip busy rows);
  `NOWAIT` fails fast instead of blocking.
- **Transactional Outbox** — event row inserted in the same transaction as business
  data; an async relay publishes it. Solves the dual-write (DB + broker) atomicity gap.
- **Transactional Inbox** — `INSERT ... ON CONFLICT DO NOTHING` on event ID in the
  same transaction that applies the effect; makes consumption idempotent.

## Java vs Go: Comparison Table

| Level / Concept | PostgreSQL | Java/JDBC | Go |
|---|---|---|---|
| Read Uncommitted | Behaves as Read Committed | May allow dirty reads | `sql.LevelReadUncommitted` (PG upgrades it) |
| Read Committed | Default; snapshot per statement | Non-repeatable reads possible | `sql.LevelReadCommitted` (default `TxOptions`) |
| Repeatable Read | Snapshot per txn; **no phantoms** | Phantoms possible | `sql.LevelRepeatableRead` |
| Serializable | SSI; abort `40001` + client retry | Full serialization; retry still needed on PG | `sql.LevelSerializable` + `WithRetry` (see `isolation/`) |
| Row lock | `SELECT ... FOR UPDATE` | Same SQL via JDBC / JPA `PESSIMISTIC_WRITE` | Same SQL via `database/sql` or `pgx` |
| Queue scan | `FOR UPDATE SKIP LOCKED` | Same SQL (no JPA equivalent) | Same SQL in relay tick |
| Outbox relay libs | `o4x`, `pg-outbox`, `outboxer` | Same pattern with Spring + Debezium/Kafka | Same pattern with `pgx` + poller |
| Txn annotation | — | `@Transactional` (+ spring-retry for 40001) | Explicit `BeginTx` / `Commit` / `Rollback` + `WithRetry` |

## Code Examples

### Example 1: Isolation Levels and Serializable Retry (`isolation/`)

```go
// In Java: conn.setTransactionIsolation(Connection.TRANSACTION_SERIALIZABLE);
tx, _ := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
```

What happens: the demo documents per-level snapshot semantics and runs `WithRetry`,
which retries only `40001`/`could not serialize` errors with linear backoff and passes
constraint violations through immediately.
How it looks in Java: Spring `@Transactional(isolation = SERIALIZABLE)` plus a
`@Retryable(SQLException with 40001)` aspect — in Go the retry is a plain function.
Pitfalls: retrying non-idempotent side effects (send email BEFORE commit); retrying
forever without a cap; swallowing the original error instead of `%w`-wrapping it.

### Example 2: Lost Update and Its Solution (`select_for_update/`)

Problem:

```go
// WRONG: lost update — both txns read 1000, both write 900.
var balance int
tx.QueryRow("SELECT balance FROM accounts WHERE id = $1", id).Scan(&balance)
balance -= 100
tx.Exec("UPDATE accounts SET balance = $1 WHERE id = $2", balance, id)
```

Solution with `SELECT FOR UPDATE`:

```go
// CORRECT: second txn blocks on the row lock, then reads 900.
tx.QueryRow("SELECT balance FROM accounts WHERE id = $1 FOR UPDATE", id).Scan(&balance)
balance -= 100
tx.Exec("UPDATE accounts SET balance = $1 WHERE id = $2", balance, id)
```

What happens: the runnable demo forces the interleaving in memory (both read 1000,
both write 900 → 900 instead of 800), then shows the locked version yielding 800.
How it looks in Java: identical SQL via JDBC `SELECT ... FOR UPDATE` with
`autoCommit=false`, or JPA `@Lock(PESSIMISTIC_WRITE)`.
Pitfalls: locking rows in different orders across code paths (deadlock — impose a
global order, e.g. `ORDER BY id`); holding the lock across network calls (timeouts);
using `SELECT FOR UPDATE` where optimistic retry on a version column would scale better.

### Example 3: Transactional Outbox (`outbox/`)

```go
// One atomic commit: business change + event row. The relay publishes later.
store.SaveDebited(100, `{"account":1,"amount":100}`) // BEGIN; UPDATE; INSERT; COMMIT
store.Relay() // SELECT ... FOR UPDATE SKIP LOCKED -> publish -> mark published
```

What happens: debit and event commit together; the relay publishes at-least-once and
marks `published_at`; restarts publish nothing twice; broker failures leave rows
unpublished for the next tick.
How it looks in Java: `@Transactional` service method + a scheduled relay poller.
Pitfalls: publishing BEFORE commit (ghost events on rollback); marking published
before the broker ack (lost events); unbounded `SELECT` without `LIMIT` + `SKIP LOCKED`.

### Example 4: Transactional Inbox (`inbox/`)

```go
// INSERT INTO inbox (event_id, ...) VALUES ($1,...) ON CONFLICT DO NOTHING;
// Conflict => redelivery: skip the business effect.
applied, skipped := consumer.Handle("evt-1", 100)
```

What happens: first delivery credits, redelivery is a no-op, new events apply normally;
a 20-goroutine redelivery storm still credits exactly once.
How it looks in Java: same SQL in a `@Transactional` webhook handler.
Pitfalls: checking "seen" in a separate transaction from the effect (race window —
keep them in ONE txn); unbounded inbox growth (add retention cleanup).

## Live verification (integration tests)

The `integration/` package asserts the quoted SQL against real PostgreSQL:
`ON CONFLICT DO NOTHING` row counts, disjoint `SKIP LOCKED` claims under two
competing transactions, and semantic JSONB replay comparison. Verified against
`postgres:16-alpine` via `docker-compose.yml` (see the main README's Docker
section for setup and the exact walkthrough queries):

```bash
docker compose up -d db   # + apply testdata/migrations/001_init.sql once
go test -tags=integration -race ./internal/postgres/integration/
```

Without the `integration` tag (or without the container) the suite stays green:
the tests Skip when the database is unreachable, and plain `go test ./...`
ignores the files entirely. One maintenance note: the tests use the
`github.com/lib/pq` driver, so keep it in `go.mod` with
`go mod tidy -tags=integration` — a tag-less tidy would drop it.

> **Callout: `jsonb` normalizes — compare semantically, not as text.**
> These two inserts produce the *same* stored value, and the `SELECT` returns TRUE:
> ```sql
> INSERT INTO t (body) VALUES ('{"a": 1}');
> INSERT INTO t (body) VALUES ('{"a":1}');
> SELECT body = '{"a":  1}'::jsonb FROM t; -- TRUE
> ```
> `jsonb` parses to a binary form and discards insignificant whitespace (and key
> order — plus duplicate keys, keeping the last). The integration test hit exactly
> this: `{"ok":true}` came back as `{"ok": true}`. Comparing `body::text` against
> a literal is a footgun; compare `jsonb = jsonb` instead. Preemptive warning for
> what you will hit next: if a response body must round-trip byte-for-byte, use
> `json` (preserves everything verbatim), not `jsonb`. For idempotency replay this
> usually doesn't matter — clients parse JSON, they don't diff bytes.

> **Production note: `database/sql` vs `pgx`.** This repo teaches the standard
> `database/sql` interface (`TxOptions`, `BeginTx`) because it is stable,
> driver-agnostic, and maps 1:1 to JDBC concepts. For production Go services the
> industry default is `pgx` (native protocol, no `database/sql` overhead unless
> you use `pgx/stdlib`): prepared-statement caching, typed scanning without
> `*string`/`sql.Null*` ceremony, `COPY` support, and `LISTEN/NOTIFY`. Learn the
> semantics here, reach for `pgx` at work.

## Common Mistakes and How to Avoid Them

- **No retry on `40001`** — treating a serialization abort as a hard failure.
  Why it happens: Spring hides retries unless configured, so developers never learn
  the contract. Fix: `WithRetry` for every `LevelSerializable` transaction.
- **Dual write (DB + broker) without outbox** — event lost on crash between commit
  and publish. Fix: outbox table + relay; never publish inside the business txn.
- **Missing `SKIP LOCKED` in queue polling** — relay workers block on each other's
  rows instead of sharing work. Fix: `ORDER BY id LIMIT N FOR UPDATE SKIP LOCKED`.
- **Inconsistent lock order** — txn A locks rows 1→2, txn B locks 2→1: deadlock under
  load. Fix: always lock in a canonical order (`ORDER BY id`).
- **Inbox check outside the effect transaction** — two concurrent redeliveries both see
  "unseen" and both apply. Fix: `ON CONFLICT DO NOTHING` + effect in ONE transaction.

## Exercises

1. Point `isolation/` at a real PostgreSQL (`DATABASE_URL`) and reproduce a
   serialization anomaly with two concurrent transfers under `LevelSerializable`.
   Log attempts and confirm `WithRetry` converges.
2. Extend `select_for_update/` with a version-column optimistic path
   (`UPDATE ... WHERE id=$1 AND version=$2`, retry on 0 rows affected) and benchmark
   it against `FOR UPDATE` under low vs high contention.
3. Add batching + `LIMIT 100` to `outbox.Relay` and simulate a relay crash mid-batch;
   prove no event is lost or duplicated.
4. Give `inbox` a retention cleanup (`DELETE WHERE received_at < now() - interval`)
   and argue why deleting only *processed* rows older than the redelivery window is safe.

## Additional Resources

- PostgreSQL docs: MVCC, Transaction Isolation, Explicit Locking (`FOR UPDATE`,
  `SKIP LOCKED`), each with the exact anomaly tables.
- `database/sql` (`TxOptions`) and `pgx` docs for `BeginTx`, error codes (`40001`),
  and `SKIP LOCKED` queue patterns.
- Outbox/inbox: Debezium outbox pattern write-ups; `o4x`, `pg-outbox`, `outboxer`
  READMEs for production relay implementations.
