// This example demonstrates the four canonical Go concurrency patterns:
//
//  1. Worker Pool — bound concurrency with a jobs channel (like ExecutorService
//     with a fixed thread pool, but the queue depth is explicit).
//  2. Fan-out / Fan-in — split work across N goroutines, merge results.
//  3. Pipeline — chain stage channels; each stage closes its output.
//  4. errgroup-style error propagation — first error cancels siblings via a
//     done channel (mirrors CompletableFuture.allOf() with exception handling;
//     the real library is golang.org/x/sync/errgroup, reimplemented here in
//     stdlib so the example has zero dependencies).
//
// Run: go run -race ./internal/concurrency/patterns
package main

import (
	"context"
	"fmt"
	"sync"
)

// ---------- 1. Worker Pool ----------

// WorkerPool runs fn over jobs with at most workers goroutines.
// In Java: Executors.newFixedThreadPool(workers) + submit + shutdown/await.
func WorkerPool(jobs []int, workers int, fn func(int) int) []int {
	jobsCh := make(chan int)
	resultsCh := make(chan int, len(jobs))
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Range ends when the sender closes jobsCh — standard ownership.
			for j := range jobsCh {
				resultsCh <- fn(j)
			}
		}()
	}
	go func() {
		defer close(jobsCh)
		for _, j := range jobs {
			jobsCh <- j
		}
	}()
	wg.Wait()
	close(resultsCh)
	out := make([]int, 0, len(jobs))
	for r := range resultsCh {
		out = append(out, r)
	}
	return out
}

// ---------- 2. Fan-out / Fan-in ----------

// FanOutFanIn squares every input using workers goroutines, then merges.
// Fan-out: N goroutines read from one channel. Fan-in: one loop merges.
func FanOutFanIn(inputs []int, workers int) []int {
	in := make(chan int)
	out := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for v := range in {
				out <- v * v
			}
		}()
	}
	go func() {
		defer close(in)
		for _, v := range inputs {
			in <- v
		}
	}()
	go func() {
		wg.Wait()
		close(out) // Why here: close only after ALL workers are done.
	}()
	var res []int
	for v := range out {
		res = append(res, v)
	}
	return res
}

// ---------- 3. Pipeline ----------

// Pipeline builds gen -> square -> filter(even) stages.
// Each stage owns and closes its output channel; cancellation flows via ctx.
// In Java: Stream.of(...).map(...).filter(...) but executed concurrently.
func Pipeline(ctx context.Context, inputs []int) []int {
	gen := func() <-chan int {
		out := make(chan int)
		go func() {
			defer close(out)
			for _, v := range inputs {
				select {
				case out <- v:
				case <-ctx.Done():
					return
				}
			}
		}()
		return out
	}
	square := func(in <-chan int) <-chan int {
		out := make(chan int)
		go func() {
			defer close(out)
			for v := range in {
				select {
				case out <- v * v:
				case <-ctx.Done():
					return
				}
			}
		}()
		return out
	}
	keepEven := func(in <-chan int) <-chan int {
		out := make(chan int)
		go func() {
			defer close(out)
			for v := range in {
				if v%2 != 0 {
					continue
				}
				select {
				case out <- v:
				case <-ctx.Done():
					return
				}
			}
		}()
		return out
	}
	var res []int
	for v := range keepEven(square(gen())) {
		res = append(res, v)
	}
	return res
}

// ---------- 4. errgroup-style group ----------

// RunAll runs tasks concurrently, returning the first non-nil error and
// cancelling siblings through ctx. This mirrors errgroup.Group semantics:
// in Java, CompletableFuture.allOf(...).join() rethrowing the first failure.
func RunAll(ctx context.Context, tasks []func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, len(tasks))
	var wg sync.WaitGroup
	for _, task := range tasks {
		wg.Add(1)
		go func(t func(context.Context) error) {
			defer wg.Done()
			// Why select on ctx first: a cancelled sibling skips useless work.
			select {
			case <-ctx.Done():
				return
			default:
			}
			if err := t(ctx); err != nil {
				// Non-blocking send keeps the FIRST error; cancel() stops others.
				select {
				case errCh <- err:
					cancel()
				default:
				}
			}
		}(task)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		return err // First recorded error wins.
	}
	return nil
}

func main() {
	fmt.Println("== Worker Pool (3 workers, square 1..6) ==")
	fmt.Println(WorkerPool([]int{1, 2, 3, 4, 5, 6}, 3, func(v int) int { return v * v }))

	fmt.Println("== Fan-out/Fan-in (square 1..5) ==")
	fmt.Println(FanOutFanIn([]int{1, 2, 3, 4, 5}, 3))

	fmt.Println("== Pipeline (squares of 1..6, keep even) ==")
	fmt.Println(Pipeline(context.Background(), []int{1, 2, 3, 4, 5, 6}))

	fmt.Println("== RunAll: one task fails ==")
	err := RunAll(context.Background(), []func(context.Context) error{
		func(context.Context) error { return nil },
		func(context.Context) error { return fmt.Errorf("boom") },
		func(context.Context) error { return nil },
	})
	fmt.Println("RunAll err:", err)
}
