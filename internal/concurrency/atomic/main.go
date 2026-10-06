// This example demonstrates sync/atomic.
//
// In Java the analogs are AtomicInteger/AtomicLong/AtomicReference with
// methods like incrementAndGet(). In Go the API is function-based
// (atomic.AddInt64, atomic.CompareAndSwapInt64) plus typed wrappers
// (atomic.Int64, atomic.Bool) since Go 1.19.
//
// Rule of thumb: atomics fit counters and flags. As soon as an invariant
// spans multiple variables, use a Mutex instead — atomics compose poorly.
//
// Run: go run -race ./internal/concurrency/atomic
package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

func main() {
	fmt.Println("== atomic counter vs mutex counter ==")
	var n atomic.Int64 // Zero value is ready to use, like new AtomicLong(0).
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				// In Java: counter.incrementAndGet();
				n.Add(1)
			}
		}()
	}
	wg.Wait()
	fmt.Println("counter =", n.Load(), "(expect 50000)")

	fmt.Println("== compare-and-swap: lock-free one-time init ==")
	var initialized atomic.Bool
	init := func(id int) {
		// Only the goroutine that flips false->true wins; the rest skip.
		// In Java: if (flag.compareAndSet(false, true)) { ... }
		if initialized.CompareAndSwap(false, true) {
			fmt.Printf("goroutine %d performs init\n", id)
		}
	}
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(id int) { defer wg.Done(); init(id) }(g)
	}
	wg.Wait()

	fmt.Println("== classic function API (works on plain int64) ==")
	var total int64
	// Why pointer: the atomic funcs take *int64 so the runtime can emit
	// the right hardware instruction; passing by value could not be atomic.
	atomic.AddInt64(&total, 41)
	atomic.AddInt64(&total, 1)
	fmt.Println("total =", atomic.LoadInt64(&total))
}
