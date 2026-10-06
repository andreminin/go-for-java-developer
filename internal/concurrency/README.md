# Concurrency in Go: A Guide for Java Developers

## Why This Matters for Java Developers

In Java, concurrency is built around OS threads and `java.util.concurrent`:
`Thread`, `ExecutorService`, `synchronized`, `ReentrantLock`, `BlockingQueue`.
In Go the model is fundamentally different: goroutines are multiplexed onto OS
threads by the runtime (initial stack ~2 KB vs ~1 MB for a Java thread), and
channels are a language-level primitive for communication.

This enables simpler, cheaper concurrent code — one goroutine per task instead of
thread pooling — but requires a mental-model shift: from "share memory and guard
it with locks" to "share memory by communicating" (CSP), with explicit error
handling and cancellation instead of interrupts and checked exceptions.

## Key Concepts

- **Goroutine** — a lightweight function execution managed by the Go runtime.
  Started with `go f()`, awaited with `sync.WaitGroup` (analog of `CountDownLatch`).
- **Channel (`chan T`)** — a typed conduit between goroutines with `close`
  broadcast semantics and `select` multiplexing. Closest Java analog: `BlockingQueue`.
- **`select`** — waits on multiple channel operations; the first ready one wins,
  with timeout via `time.After` and cancellation via `context.Context`.
- **`sync.Mutex` / `sync.RWMutex`** — mutual exclusion. Not reentrant (unlike Java
  `synchronized`); `RWMutex` prefers waiting writers over new readers.
- **`sync/atomic`** — lock-free counters/flags via functions (`AddInt64`,
  `CompareAndSwap`) or typed wrappers (`atomic.Int64`, `atomic.Bool`).
- **Data race / deadlock / livelock / starvation** — the classic hazards. Go's
  `-race` detector is a first-class CI tool, and the runtime panics on provable
  total deadlock ("all goroutines are asleep").
- **Worker Pool / Fan-out-Fan-in / Pipeline / errgroup** — the canonical patterns
  replacing `ExecutorService`, manual fan-out, `Stream` chains, and
  `CompletableFuture.allOf()`.

## Java vs Go: Comparison Table

| Java | Go | Comment |
|------|-----|---------|
| `new Thread(r).start()` | `go func() {}()` | Goroutine stack ~2 KB and managed by runtime; threads are OS-managed ~1 MB |
| `synchronized` / `ReentrantLock` | `sync.Mutex` + `defer mu.Unlock()` | Go mutex is NOT reentrant — double `Lock()` deadlocks; Java allows reentry |
| `ReentrantReadWriteLock` | `sync.RWMutex` | Go prioritizes waiting writers (new readers block); Java default lets readers barge |
| `AtomicInteger` | `atomic.AddInt64` / `atomic.Int64` | Function/type API, not methods; counters and flags only — mutex for compound invariants |
| `CountDownLatch` | `sync.WaitGroup` | `WaitGroup` is reusable (`Add` after `Wait`); latch is one-shot |
| `BlockingQueue` | `chan T` | Channels add `close` broadcast + `select`; queue depth is an explicit buffer size |
| `ExecutorService` fixed pool | Worker Pool with channels | ~30 lines, explicit queue depth, sender closes jobs channel |
| `CompletableFuture.allOf()` | goroutines + `errgroup` (see `patterns/`) | First error cancels siblings via context |
| `Thread.interrupt()` | `context.Context` cancellation | Cooperative and explicit: `select` on `ctx.Done()` |
| No built-in race tool | `go test -race` | ThreadSanitizer-based; mandatory in CI for concurrency code |

## Code Examples

### Example 1: Goroutines and WaitGroup (`goroutines/`)

```go
var wg sync.WaitGroup // In Java: CountDownLatch latch = new CountDownLatch(3);
wg.Add(1)
go func(id int) {
    defer wg.Done() // Guarantees countdown on every return path.
    fmt.Printf("goroutine %d done\n", id)
}(i)
wg.Wait() // In Java: latch.await();
```

What happens: three goroutines run concurrently; `Wait` blocks until all call `Done`.
How it looks in Java: `new Thread(...)` per task plus `latch.countDown()` in `finally`.
Pitfalls: capturing the loop variable directly (pass it as a parameter); forgetting
`Add` before `go` (negative-counter panic or missed wait).

### Example 2: Mutex vs RWMutex (`mutex/`)

```go
c.mu.Lock()
defer c.mu.Unlock() // Idiomatic Go: unlock via defer, like try/finally in Java.
c.m[key]++
```

What happens: read-modify-write is serialized; 100 goroutines x 1000 increments = 100000.
How it looks in Java: `lock.lock(); try { ... } finally { lock.unlock(); }`.
Pitfalls: double `Lock()` from one goroutine deadlocks (Java would reenter);
every map access path must hold the lock — plain Go maps panic on concurrent use.

### Example 3: Atomics (`atomic/`)

```go
var n atomic.Int64
n.Add(1) // In Java: counter.incrementAndGet();
```

What happens: lock-free counter safe under `-race`.
How it looks in Java: `AtomicLong` methods.
Pitfalls: atomics do not compose — an invariant over two variables needs a mutex.

### Example 4: Channels and Select (`channels/`)

```go
select {
case v := <-slow:
    fmt.Println("got:", v)
case <-time.After(50 * time.Millisecond): // In Java: future.get(50, MILLISECONDS).
    fmt.Println("timed out")
}
```

What happens: unbuffered handoff, buffered queue, timeout race, `close(done)`
broadcast, and range-until-close are demonstrated in one runnable file.
How it looks in Java: `SynchronousQueue` / `ArrayBlockingQueue` + `Future.get(timeout)`.
Pitfalls: sender closes, never the receiver; sending on a closed channel panics;
unbuffered send blocks until a receiver arrives.

### Example 5: Concurrency Problems (`problems/`)

BAD (data race) then GOOD (mutex); lock-ordering fix for deadlock; backoff fix for
CAS livelock; starvation-mode note for `sync.Mutex`.

```go
// WRONG: lost update, -race reports WARNING: DATA RACE
n++
// CORRECT: serialized by mutex
mu.Lock(); n++; mu.Unlock()
```

What happens: the bad counter usually returns < 20000; the good one exactly 20000.
How it looks in Java: identical lost update as unsynchronized `i++`.
Pitfalls: assuming tests catch races without `-race`; "fixing" deadlock with sleeps
instead of breaking a Coffman condition (lock order, timeouts, try-lock + backoff).

### Example 6: Patterns (`patterns/`)

Worker Pool, Fan-out/Fan-in, Pipeline, and an `errgroup`-style `RunAll` returning
the first error while cancelling siblings:

```go
// In Java: CompletableFuture.allOf(futures).join() rethrowing the first failure.
if err := RunAll(ctx, tasks); err != nil { ... }
```

What happens: bounded parallelism, merged results, chained stages, error propagation.
Pitfalls: closing the output channel before all workers finish (panic on send);
leaking goroutines by ignoring `ctx.Done()` in pipeline stages.

## Common Mistakes and How to Avoid Them

- **Copying a Mutex** — `sync.Mutex` must not be copied after first use (`go vet`
  flags it). Why it happens: passing a struct containing a mutex by value, as if it
  were a Java reference. Fix: use pointers (`*SafeCounter`) and check `go vet`.
- **Forgetting `defer Unlock()`** — an early return leaves the mutex held and every
  other goroutine blocks forever. Fix: `Lock(); defer Unlock()` on adjacent lines.
- **Assuming reentrancy** — calling a locked method from another locked method on
  the same mutex deadlocks. Fix: split into internal unlocked helpers or restructure.
- **Closing a channel from the receiver** — panics when the sender next sends.
  Fix: the sender (or an owner goroutine after `wg.Wait()`) closes.
- **Goroutine leaks** — a goroutine blocked on send/receive with no cancellation
  lives forever. Fix: `select` on `ctx.Done()` / `done` in every blocking loop.
- **Testing without `-race`** — races are schedule-dependent and pass locally, then
  fail in production. Fix: `go test -race ./...` in CI, always.

## Exercises

1. Change `SafeCounter` to use `sync.RWMutex`: `Get` takes `RLock`, `Inc` takes
   `Lock`. Benchmark readers vs writers and explain when it wins.
2. Rewrite `SumParallel` with a results channel instead of disjoint slots. Compare
   clarity and performance.
3. Add a `select` + `time.After` timeout to the Worker Pool so jobs exceeding 100 ms
   are skipped. What changes for result ordering?
4. Extend `RunAll` to collect ALL errors (like a validation report) instead of the
   first one. What backpressure do you need on the error channel?
5. Run every example with `go test -race` / `go run -race` and deliberately reintroduce
   one race — read the detector's goroutine stacks and trace both accesses.

## Additional Resources

- Go docs: [Share Memory By Communicating](https://go.dev/blog/codelab-share),
  [Go Concurrency Patterns](https://go.dev/blog/pipelines), `sync` and
  `golang.org/x/sync/errgroup` package docs.
- Cox-Buday: "Go's race detector" rationale; ThreadSanitizer algorithm behind `-race`.
- For the Java-anchored II: `java.util.concurrent` javadoc (latches, barriers, pools)
  side by side with `sync`, `context`, and `time` package docs.
