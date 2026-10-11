# Go for Java Developers

Structured Go learning materials for Java developers (Spring, JPA/JDBC, OS-thread
concurrency background) transitioning to idiomatic Go. The focus is not only syntax
but the philosophy: simplicity, composition, and explicitness.

All documentation, README files, and code comments are in English.

## Modules

| Module | Directory | What you learn |
|--------|-----------|----------------|
| 1. Concurrency and Parallelism | [internal/concurrency](internal/concurrency/README.md) | Goroutines, mutexes, atomics, channels, `select`, data races, deadlocks, Worker Pool, Fan-out/Fan-in, Pipeline, `errgroup` |
| 2. PostgreSQL and Transactions | [internal/postgres](internal/postgres/README.md) | MVCC, isolation levels, `SELECT FOR UPDATE` / `SKIP LOCKED`, Transactional Outbox and Inbox, Commitix-style intent leasing with a fencing token |
| 3. Idempotency | [internal/idempotency](internal/idempotency/README.md) | Idempotency keys, deduplication table, HTTP middleware |

Supporting materials:

- [Java to Go cheat sheet](docs/java-to-go-cheatsheet.md)
- [Glossary](docs/glossary.md)
- [SQL migrations](testdata/migrations/) — `001_init.sql` (accounts, outbox, inbox, idempotency_keys) and `002_init.sql` (commitix_intents)

## Requirements

- Go 1.21+ (tested with `go version`; runnable examples use only the standard
  library — the single external module, `github.com/lib/pq`, is used solely by
  the build-tagged integration tests in `internal/postgres/integration/`)
- PostgreSQL 14+ and Docker — optional, only for trying the DB-backed snippets live
  (see below); all `go run` demos and `go test` suites work without a database

## Run instructions

```bash
# Run a specific example
go run ./internal/concurrency/mutex
go run ./internal/concurrency/channels
go run ./internal/postgres/outbox
go run ./internal/postgres/commitix          # lease + fencing token demo

# Run with the race detector (mandatory for concurrency code)
go run -race ./internal/concurrency/goroutines
go run -race ./internal/concurrency/patterns

# Run all tests with the race detector
go test -race ./...

# Run tests for a single module
go test -race ./internal/concurrency/...
go test -race ./internal/postgres/...
go test -race ./internal/idempotency/...

# Live-PostgreSQL tests (require Docker up + DATABASE_URL; see below)
go test -race -count=1 -tags=integration ./internal/postgres/integration/...

# Topic index
go run ./cmd/demo
go run ./cmd/demo -module concurrency
```

## Running the PostgreSQL examples live with Docker

The Go examples in Module 2 (`internal/postgres/...`) run without a database:
each one simulates the PostgreSQL behavior in memory and prints the equivalent
live SQL. This section is optional — follow it when you want to feel the real
locking, snapshot, and `ON CONFLICT` semantics against PostgreSQL 16.

Prerequisites: [Docker](https://docs.docker.com/get-docker/) with the Compose plugin
(`docker compose version`).

```bash
# 1. Start PostgreSQL (first start pulls postgres:16-alpine)
docker compose up -d db

# 2. Apply the schema (equivalent to: make db-migrate):
#    001_init.sql -> accounts, outbox, inbox, idempotency_keys
#    002_init.sql -> commitix_intents
docker compose exec -T db psql -U app -d app -v ON_ERROR_STOP=1 \
  -f /testdata/migrations/001_init.sql
docker compose exec -T db psql -U app -d app -v ON_ERROR_STOP=1 \
  -f /testdata/migrations/002_init.sql

# 3. (Optional) point follow-up work at the database
export DATABASE_URL=postgres://app:app@localhost:5432/app?sslmode=disable

# 4. Open a psql shell to try the live SQL quoted by each example
docker compose exec db psql -U app -d app
```

### Try it: lost update vs `SELECT ... FOR UPDATE`

From `internal/postgres/select_for_update` (`QBalanceForUpdate`). Open **two**
psql shells (two terminals) and run the statements interleaved as shown —
the second `FOR UPDATE` blocks until the first transaction commits, then sees
the already-updated row:

```sql
-- setup (either shell)
INSERT INTO accounts (owner, balance) VALUES ('alice', 1000) RETURNING id; -- id = 1

-- shell A                          -- shell B (second terminal)
BEGIN;                               BEGIN;
SELECT balance FROM accounts
 WHERE id = 1 FOR UPDATE;            -- (waits...)
-- balance = 1000
                                     SELECT balance FROM accounts
                                      WHERE id = 1 FOR UPDATE; -- blocks until A commits
UPDATE accounts
 SET balance = balance - 100
 WHERE id = 1;
COMMIT;                              -- B unblocks here and reads 900, not 1000
                                     UPDATE accounts
                                      SET balance = balance - 100
                                      WHERE id = 1;
                                     COMMIT;
-- either shell:
SELECT balance FROM accounts WHERE id = 1; -- 800: no update lost
```

Without `FOR UPDATE` (plain `SELECT` in both shells) both read 1000 and the
final balance is 900 — the lost update from the demo, reproduced for real.

### Try it: outbox relay claim with `SKIP LOCKED`

From `internal/postgres/outbox` (relay query). Competing relay workers each
claim a batch without blocking on each other's rows:

```sql
INSERT INTO outbox (aggregate, aggregate_id, type, payload)
VALUES ('account', '1', 'BalanceDebited', '{"amount":100}');

BEGIN;
SELECT id FROM outbox
 WHERE published_at IS NULL ORDER BY id LIMIT 10 FOR UPDATE SKIP LOCKED;
-- publish the payload to your broker here, then:
UPDATE outbox SET published_at = now() WHERE id = 1;
COMMIT;
```

### Try it: inbox / idempotency `ON CONFLICT DO NOTHING`

From `internal/postgres/inbox` and `internal/idempotency/dedup_table`.
The second insert in each pair is a no-op (`INSERT 0 0`) — the atomic
"have I seen this?" check the Go demos simulate with a mutex:

```sql
INSERT INTO inbox (event_id, type, payload)
VALUES ('evt-1', 'BalanceCredited', '{}') ON CONFLICT DO NOTHING; -- INSERT 0 1
INSERT INTO inbox (event_id, type, payload)
VALUES ('evt-1', 'BalanceCredited', '{}') ON CONFLICT DO NOTHING; -- INSERT 0 0

INSERT INTO idempotency_keys (key, request_hash, response_code, response_body)
VALUES ('key-abc', 'h1', 200, '{"ok":true}') ON CONFLICT DO NOTHING;
```

### Try it: stale worker takeover with a fencing token

From `internal/postgres/commitix` (the `commitix_intents` table). This is
the one behavior a plain `SKIP LOCKED` queue cannot give you: a crashed
worker's lease expires, the intent is released, a healthy worker takes
over, and the stale worker's late write is rejected by the fence.

The two fields that make it work are `lease_generation` (a fencing token
that advances **only on ownership change**, never on recovery) and
`attempt_count` (advances **only on claim**, so a flaky recovery loop
cannot exhaust a retry budget).

```sql
-- setup: one READY intent
INSERT INTO commitix_intents (payload) VALUES ('{"job":"charge-card"}')
RETURNING id; -- id = 1

-- worker-A claims it (simulating the CTE claim as a single UPDATE for
-- readability; the real statement uses SELECT ... FOR UPDATE SKIP LOCKED
-- in a CTE — see QClaim in the demo)
UPDATE commitix_intents
SET status           = 'RUNNING',
    worker_id        = 'worker-A',
    lease_until      = now() + interval '5 minutes',
    lease_generation = lease_generation + 1,   -- 0 -> 1
    attempt_count    = attempt_count + 1,      -- 0 -> 1
    version          = version + 1
WHERE id = 1;

-- worker-A crashes mid-task. Simulate the crash by forcing the lease into
-- the past (a real crash just stops heartbeating and time does this).
UPDATE commitix_intents
SET lease_until = now() - interval '1 second'
WHERE id = 1;

-- recovery sweep: release the expired lease.
-- NOTE what is NOT in the SET list: lease_generation and attempt_count.
-- Recovery is not an ownership change and is not a retry.
UPDATE commitix_intents
SET status      = 'READY',
    worker_id   = NULL,
    lease_until = NULL,
    version     = version + 1
WHERE status = 'RUNNING' AND lease_until < now();   -- UPDATE 1

-- verify: generation and attempt_count unchanged
SELECT status, lease_generation, attempt_count FROM commitix_intents WHERE id = 1;
--  READY | 1 | 1

-- worker-B takes over. This IS an ownership change: generation advances.
UPDATE commitix_intents
SET status           = 'RUNNING',
    worker_id        = 'worker-B',
    lease_until      = now() + interval '5 minutes',
    lease_generation = lease_generation + 1,   -- 1 -> 2
    attempt_count    = attempt_count + 1,      -- 1 -> 2
    version          = version + 1
WHERE id = 1;

-- stale worker-A wakes up and tries to record SUCCESS with its old token.
-- The fence is the WHERE clause: worker_id and lease_generation no longer
-- match, so zero rows are affected and the write is silently discarded.
UPDATE commitix_intents
SET status           = 'SUCCESS',
    worker_id        = NULL,
    lease_until      = NULL,
    version          = version + 1
WHERE id = 1
  AND worker_id      = 'worker-A'
  AND lease_generation = 1;                  -- UPDATE 0  <- the whole point

-- state still belongs to worker-B:
SELECT status, worker_id, lease_generation FROM commitix_intents WHERE id = 1;
--  RUNNING | worker-B | 2

-- worker-B completes normally with its current token.
UPDATE commitix_intents
SET status           = 'SUCCESS',
    worker_id        = NULL,
    lease_until      = NULL,
    version          = version + 1
WHERE id = 1
  AND worker_id      = 'worker-B'
  AND lease_generation = 2;                  -- UPDATE 1
```
The `UPDATE 0` on the stale worker's write is the difference between this
pattern and a bare `SELECT ... FOR UPDATE SKIP LOCKED` queue. Under the
plain queue, worker-A's late write would clobber worker-B's state.

Both halves of this walkthrough are proven in code:

- **In-memory:** `go test -race ./internal/postgres/commitix/...`
  (`TestStaleWorkerCannotCompleteAfterTakeover`,
  `TestRecoveryPreservesLeaseGenerationAndAttemptCount`)
- **Against live PostgreSQL:**
  `go test -race -count=1 -tags=integration ./internal/postgres/integration/...`
  (`TestCommitixStaleWorkerCompleteIsRejected`,
  `TestCommitixRecoveryPreservesGenerationAndAttemptCount`)

**What fencing does *not* protect:** external side effects. If worker-A
already called a payment API before crashing, the fencing token stops
worker-A from writing a database result, but it cannot undo the API call —
worker-B will retry it. Commitix is at-least-once, so any operation whose
duplicate execution is unacceptable needs its own idempotency or
deduplication layer.

### Stop and clean up

```bash
docker compose stop db      # stop, keep data in the pgdata volume
docker compose down -v      # stop and DELETE all data (fresh start next time)
```

## Project layout

This project follows the [golang-standards/project-layout](https://github.com/golang-standards/project-layout):

- `cmd/` — entry points only (`main.go` parses flags and calls `Run()`). No business logic.
  In Java terms: a `public static void main` that wires the app and delegates.
- `internal/` — private packages the Go compiler prevents other modules from importing.
  Learning code lives here, grouped by module and topic.
- `pkg/` — public packages intended for external reuse (kept minimal in a learning repo).
- `docs/` — cheat sheet and glossary.
- `testdata/` — SQL migrations for the postgres/outbox/inbox/idempotency/commitix examples
  (`001_init.sql`, `002_init.sql`).

## Contributing: how to add a new example

1. Create a directory under the right module, e.g. `internal/concurrency/mychannel/`.
2. Add a `main.go` in `package main` that is self-contained and runnable via `go run ./...`.
   - Header comment: what the example demonstrates.
   - `// In Java: ...` comment showing the equivalent Java code.
   - Where applicable, show BAD code first, then GOOD code.
   - Print expected output so readers can compare.
3. Add a `main_test.go` (or `xxx_test.go`) asserting correctness; concurrency tests
   must be meaningful under `-race`.
4. Follow the comment golden rule: explain **why**, not **what**.
5. Run before pushing:
   `gofmt -l .`, `go vet ./...`, `go test -race ./...`
   (shortcuts: `make fmt vet test-race`; full list: `make help`).
   Live-DB checks: `make db-up db-migrate test-integration` — the
   `db-migrate` target applies both `001_init.sql` and `002_init.sql`, and
   the integration tests skip (not fail) when PostgreSQL is unreachable,
   so tag-less CI stays green on machines without Docker.
6. Never commit IDE-specific files (`.idea/`, `.vscode/`, `*.iml`) — they are
   already in `.gitignore`; keep them out of PRs.
7. If your commit changes any runnable command (README snippet, Makefile target,
   migration step), the commit message must state the verification environment:
   OS, tool versions (`go version`, `docker --version`, image tag), and the
   observed result (exit code or key output lines). Verified documentation beats
   documentation — and the claim must be re-checkable from the message alone.
