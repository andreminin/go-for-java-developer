// This example demonstrates goroutines and sync.WaitGroup.
//
// In Java, the analog is `new Thread(() -> ...).start()` plus a
// `CountDownLatch` to wait for completion. In Go a goroutine starts with
// the `go` keyword and costs ~2 KB of stack (vs ~1 MB for a Java thread),
// so one-goroutine-per-task is idiomatic instead of thread pooling.
//
// Run: go run ./internal/concurrency/goroutines
package main

import (
	"fmt"
	"sync"
)

func main() {
	fmt.Println("== 1. fire-and-wait with WaitGroup ==")
	// In Java: CountDownLatch latch = new CountDownLatch(3);
	var wg sync.WaitGroup
	for i := 1; i <= 3; i++ {
		wg.Add(1)
		// Why pass i as a parameter: the loop variable is reused across
		// iterations (pre-Go-1.22 semantics readers may still expect), so a
		// closure capturing it directly can observe the wrong value.
		// Passing it explicitly freezes the value per goroutine.
		// In Java each lambda iteration captures an effectively-final copy.
		go func(id int) {
			defer wg.Done() // Idiomatic Go: Done via defer so early returns still count down.
			fmt.Printf("goroutine %d done\n", id)
		}(i)
	}
	wg.Wait() // In Java: latch.await();
	fmt.Println("all goroutines finished")

	fmt.Println("== 2. collecting results ==")
	fmt.Printf("sum 1..10 = %d\n", SumParallel(10))
}

// SumParallel sums 1..n using one goroutine per chunk and WaitGroup.
// It shows the standard "spawn, write to disjoint slots, wait, combine"
// shape. Disjoint slots avoid sharing memory; no mutex is needed.
func SumParallel(n int) int {
	const workers = 4
	partials := make([]int, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// Stride partitioning: worker w sums w+1, w+1+workers, ...
			for i := w + 1; i <= n; i += workers {
				partials[w] += i
			}
		}(w)
	}
	wg.Wait()
	total := 0
	for _, p := range partials {
		total += p
	}
	return total
}
