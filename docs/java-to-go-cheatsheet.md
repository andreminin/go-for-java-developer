# Java to Go Cheat Sheet

Compact mapping for Java developers. Left: what you know. Right: idiomatic Go.

## Concurrency

| Java | Go | Comment |
|------|-----|---------|
| `synchronized` block / method | `sync.Mutex` + `defer mu.Unlock()` | Go mutex is NOT reentrant: double `Lock()` from one goroutine deadlocks; Java `synchronized` is reentrant |
| `ReentrantLock` | `sync.Mutex` | No `tryLock` with timeout on `Mutex`; use channels or `select` for cancellation instead |
| `ReentrantReadWriteLock` | `sync.RWMutex` | Go gives priority to waiting writers: new readers block while a writer waits (prevents writer starvation); Java default is non-fair with barging readers |
| `AtomicInteger` / `AtomicLong` | `sync/atomic` funcs (`atomic.AddInt64`, `atomic.CompareAndSwapInt64`) or `atomic.Int64` type (Go 1.19+) | Function/type-based API, not methods on a wrapper class; use for counters and flags only, mutex for compound invariants |
| `volatile boolean flag` | `atomic.Bool` or channel close / `context.Context` cancellation | Closing a channel broadcasts to all receivers; a `volatile` flag needs polling |
| `CountDownLatch` | `sync.WaitGroup` (`Add` / `Done` / `Wait`) | `WaitGroup` counter is reusable via `Add` after `Wait` returns; `CountDownLatch` is one-shot |
| `CyclicBarrier` | `sync.WaitGroup` + extra channel, or `golang.org/x/sync/errgroup` | No direct stdlib barrier; compose channels |
| `BlockingQueue<T>` | `chan T` (buffered: `make(chan T, n)`) | Channels are language primitives with `close` semantics and `select`; queues are library classes |
| `ExecutorService` / thread pool | Worker Pool pattern with channels + `sync.WaitGroup` | No built-in pool; you build one in ~30 lines and control queue depth explicitly |
| `CompletableFuture` / `allOf()` | goroutine + `errgroup.Group` (`golang.org/x/sync/errgroup`) | `errgroup` propagates the first non-nil error and cancels siblings via context |
| `Thread.interrupt()` | `context.Context` cancellation (`WithCancel`, `WithTimeout`) | Cancellation is cooperative and explicit: check `ctx.Done()` in `select` |
| `ThreadLocal` | goroutine ID is deliberately unavailable; pass values via `context.Context` or function args | Do not try to emulate `ThreadLocal` with goroutine hacks |

## Types, OOP, Errors

| Java | Go | Comment |
|------|-----|---------|
| `class User { ... }` | `type User struct { ... }` | No constructors, no inheritance; use factory funcs like `NewUser(...)` |
| `implements` keyword | Implicit interface satisfaction | A type implements an interface by having the methods; no declaration needed — enables small interfaces (`io.Reader`, `error`) |
| `extends` (inheritance) | Embedding (`type T struct { Base }`) | Composition, not inheritance: promoted methods can be shadowed; no virtual dispatch to "subclass" |
| `Exception` hierarchy | `error` interface + explicit returns | No exceptions for expected failures; `panic` is for unrecoverable bugs (like unchecked `Error`), not control flow |
| `try { } catch { }` | `if err != nil { return err }` | Explicit error paths; wrap with `%w` and `fmt.Errorf` for context (analog of exception chaining) |
| `try-with-resources` | `defer f.Close()` | LIFO deferred calls run at function return; the analog of `finally` |
| `Optional<T>` | Pointer (`*T`) or `(T, bool)` / `(T, error)` tuple | Comma-ok idiom (`v, ok := m[k]`) replaces `Optional` for maps and type assertions |
| `enum` | `const` block + `iota`, or `type Status string` | No rich enums; `String()` method gives display names |
| `List<T>` / `ArrayList` | Slice (`[]T`) | Slices are views over arrays with `len`/`cap`; `append` may reallocate — always use the return value |
| `Map<K,V>` / `HashMap` | `map[K]V` + `sync.Map` for concurrent use | Plain maps are NOT thread-safe; concurrent read+write panics or races — guard with mutex or use channels |
| Generics (`class Box<T>`) | Generics since Go 1.18 (`func Min[T constraints.Ordered]`) | Simpler constraint system (`comparable`, `any`); no wildcards, no erasure surprises |
| `static` methods / fields | Package-level funcs and vars | No classes to hang statics on; package is the unit of encapsulation (lowercase = private) |
| Annotations (`@Override`, Spring) | Struct tags (`` `json:"name"` ``) + code generation (`go generate`) | No reflection-driven DI at runtime by default; prefer explicit wiring |
| `synchronized` collections | Guard `map`/slice with `sync.Mutex`/`sync.RWMutex` | Same rule as `Collections.synchronizedMap`: every access path must hold the lock |

## Build, Runtime, Tooling

| Java | Go | Comment |
|------|-----|---------|
| Maven / Gradle | `go build`, `go test`, `go vet`, `go mod` | Single toolchain; `go.mod` pins deps like `pom.xml` but with content hashes in `go.sum` |
| JaCoCo / coverage gates | `go test -cover ./...` | Built-in coverage without plugins |
| FindBugs / SpotBugs | `go vet` + race detector (`-race`) | `go test -race` instruments memory accesses at runtime — run it in CI always |
| JFR / async-profiler | `net/http/pprof`, `go tool pprof`, `go test -bench` | Profiling is stdlib; import `_ "net/http/pprof"` and scrape |
| JVM flags (`-Xmx`) | `GOGC`, `GOMAXPROCS`, `GOMEMLIMIT` env vars | Runtime tuning via env, not flags |
