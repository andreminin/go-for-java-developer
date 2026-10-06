# Glossary

Definitions of key terms used across the modules, with Java anchors where helpful.

- **Goroutine**: A lightweight function execution managed by the Go runtime (initial stack ~2 KB, grows/shrinks dynamically). Java anchor: a cheaper `Thread` — you can run hundreds of thousands of them, so you use one per task instead of pooling threads.
- **Channel (`chan T`)**: A typed, thread-safe conduit for sending values between goroutines. Java anchor: a combination of `BlockingQueue` plus built-in `close` broadcast and `select` multiplexing. Closing a channel signals "no more values" to all receivers.
- **CSP (Communicating Sequential Processes)**: The concurrency model behind Go: "Do not communicate by sharing memory; instead, share memory by communicating." Independent processes coordinate by message passing (channels) rather than shared mutable state plus locks.
- **`select`**: A control structure that waits on multiple channel operations, executing the first one that becomes ready (with optional `default` and timeout via `time.After`). Java anchor: like `Selector.select()` for NIO, but for goroutine channels.
- **Data race**: Two unsynchronized accesses to the same memory where at least one is a write. Go detects these at runtime with `go test -race`. Java anchor: same definition as in JMM, but Go's detector is a first-class, always-on-in-CI tool.
- **Deadlock**: A cycle of goroutines each waiting for a resource held by another (Coffman conditions: mutual exclusion, hold-and-wait, no preemption, circular wait). The Go runtime panics with "all goroutines are asleep" when it can prove total deadlock.
- **Livelock**: Goroutines keep reacting to each other (e.g. retrying `CompareAndSwap`) without making progress. Unlike deadlock, threads are running but no work completes.
- **Starvation**: A goroutine never gets the resource (e.g. a writer starved by continuous readers). `sync.Mutex` enters "starvation mode" after ~1 ms of waiting, handing the lock directly to the waiter.
- **MVCC (Multi-Version Concurrency Control)**: PostgreSQL's mechanism where readers see a snapshot and never block writers; writers create new row versions. Java anchor: like optimistic `READ_COMMITTED` snapshots without read locks.
- **Isolation level**: The visibility contract of a transaction: Read Committed (PG default), Repeatable Read (snapshot for the whole transaction, no phantoms in PG), Serializable (SSI — aborts on dangerous patterns with `could not serialize access`, caller must retry).
- **SSI (Serializable Snapshot Isolation)**: PostgreSQL's Serializable implementation: tracks read/write dependencies and aborts transactions that could break serializability. Cost: the application must retry serialization failures.
- **Lost update**: Two transactions read the same row, then both write based on the stale value — one update is silently lost. Fix: `SELECT ... FOR UPDATE` (pessimistic row lock) or optimistic retry on version column.
- **`SELECT ... FOR UPDATE`**: Row-level pessimistic lock held until transaction end. Variants: `SKIP LOCKED` (skip locked rows — for queues), `NOWAIT` (fail fast instead of blocking).
- **Transactional Outbox**: Pattern solving the dual-write problem (DB write + message publish cannot be atomic): the event is stored in an `outbox` table in the same transaction as business data; a separate relay publishes it to the broker.
- **Transactional Inbox**: Mirror pattern on the consumer: the incoming event ID is inserted into an `inbox` table with `ON CONFLICT DO NOTHING` in the same transaction that processes it, making consumption idempotent.
- **Idempotency**: Property that repeating an operation has the same effect as doing it once (`f(f(x)) == f(x)`). Achieved via idempotency keys plus a deduplication table that stores request hash and response for replay.
- **Idempotency key**: A client-generated unique token (usually a UUID in the `Idempotency-Key` header) scoping the operation (per endpoint + customer). The server uses it for insert-once deduplication.
- **Deduplication table**: A table like `idempotency_keys(key PK, request_hash, response_code, response_body, created_at)` with atomic `INSERT ... ON CONFLICT DO NOTHING` check and a 24–72h retention window.
- **Race detector (`-race`)**: Go's ThreadSanitizer-based runtime instrumentation (`go run -race`, `go test -race`) that reports unsynchronized memory accesses with goroutine stacks. Heavier and slower — for tests/CI, not production.
- **`sync.WaitGroup`**: Counter-based gate (`Add`/`Done`/`Wait`) for waiting on a dynamic set of goroutines. Java anchor: `CountDownLatch`, but reusable.
- **`errgroup.Group`**: `golang.org/x/sync` helper running goroutines as a group: first error cancels the derived context and `Wait()` returns it. Java anchor: `CompletableFuture.allOf()` with exception propagation.
- **Worker Pool / Fan-out/Fan-in / Pipeline**: The three canonical Go concurrency patterns: bounded workers consuming a jobs channel; splitting work across N goroutines then merging results; chaining stage channels (`in -> stage1 -> stage2 -> out`) with proper `close` ownership.
