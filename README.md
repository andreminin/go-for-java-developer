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

- Go 1.21+ (tested with `go version`; examples use only the standard library)
- PostgreSQL 14+ — only needed to run the DB-backed snippets live; all tests run without a database

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
