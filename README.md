# Go for Java Developers

Structured Go learning materials for Java developers (Spring, JPA/JDBC, OS-thread
concurrency background) transitioning to idiomatic Go. The focus is not only syntax
but the philosophy: simplicity, composition, and explicitness.

All documentation, README files, and code comments are in English.

## Modules

| Module | Directory | What you learn |
|--------|-----------|----------------|
| 1. Concurrency and Parallelism | [internal/concurrency](internal/concurrency/README.md) | Goroutines, mutexes, atomics, channels, `select`, data races, deadlocks, Worker Pool, Fan-out/Fan-in, Pipeline, `errgroup` |
| 2. PostgreSQL and Transactions | [internal/postgres](internal/postgres/README.md) | MVCC, isolation levels, `SELECT FOR UPDATE` / `SKIP LOCKED`, Transactional Outbox and Inbox |
| 3. Idempotency | [internal/idempotency](internal/idempotency/README.md) | Idempotency keys, deduplication table, HTTP middleware |

Supporting materials:

- [Java to Go cheat sheet](docs/java-to-go-cheatsheet.md)
- [Glossary](docs/glossary.md)
- [SQL migrations](testdata/migrations/001_init.sql)

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

# Run with the race detector (mandatory for concurrency code)
go run -race ./internal/concurrency/goroutines
go run -race ./internal/concurrency/patterns

# Run all tests with the race detector
go test ./... -race

# Run tests for a single module
go test -race ./internal/concurrency/...
go test -race ./internal/postgres/...
go test -race ./internal/idempotency/...

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

# 2. Apply the schema: accounts, outbox, inbox, idempotency_keys
docker compose exec -T db psql -U app -d app -v ON_ERROR_STOP=1 \
  -f /testdata/migrations/001_init.sql

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
- `testdata/` — SQL migrations for the postgres/outbox/inbox/idempotency examples.

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
   `gofmt -l .`, `go vet ./...`, `go test -race ./...`.
6. Never commit IDE-specific files (`.idea/`, `.vscode/`, `*.iml`) — they are
   already in `.gitignore`; keep them out of PRs.
7. If your commit message claims live verification (e.g. "verified against
   PostgreSQL via compose"), say exactly what you ran: container image,
   migration file, and the queries or test command with their observed output.
   Verified documentation beats documentation.`.
