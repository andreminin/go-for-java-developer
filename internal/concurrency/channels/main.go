// This example demonstrates channels and select.
//
// Core idea (CSP): "Do not communicate by sharing memory; instead, share
// memory by communicating." A channel is a typed pipe between goroutines.
// Java has no language equivalent; the closest is BlockingQueue, but
// channels add close-broadcast and select-multiplexing as primitives.
//
// Run: go run -race ./internal/concurrency/channels
package main

import (
	"fmt"
	"time"
)

func main() {
	fmt.Println("== 1. unbuffered channel: handoff rendezvous ==")
	// Unbuffered: send blocks until a receiver is ready and vice versa.
	// In Java: SynchronousQueue — every put waits for a take.
	unbuf := make(chan string)
	go func() {
		// Why this works without sleep: the send rendezvous with the receive.
		unbuf <- "hello"
	}()
	fmt.Println("received:", <-unbuf)

	fmt.Println("== 2. buffered channel: queue with capacity ==")
	// Buffered: sends block only when the buffer is full.
	// In Java: new ArrayBlockingQueue<>(2).
	buf := make(chan int, 2)
	buf <- 1
	buf <- 2
	fmt.Println("drained:", <-buf, <-buf)

	fmt.Println("== 3. select with timeout ==")
	slow := make(chan string, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		slow <- "late answer"
	}()
	// In Java: future.get(timeout) with ExecutorService; here select races
	// channel readiness against a timer — cancellation is explicit.
	select {
	case v := <-slow:
		fmt.Println("got:", v)
	case <-time.After(50 * time.Millisecond):
		fmt.Println("timed out after 50ms (slow task still running)")
	}

	fmt.Println("== 4. done channel: broadcast cancellation ==")
	// Closing a channel wakes ALL receivers with zero values — a broadcast.
	// In Java: CountDownLatch.countDown() or interrupting threads one by one.
	done := make(chan struct{})
	go worker(done, 1)
	go worker(done, 2)
	time.Sleep(30 * time.Millisecond)
	close(done) // Broadcast "stop" to every worker at once.
	time.Sleep(30 * time.Millisecond)

	fmt.Println("== 5. range over channel until close ==")
	// The idiom: the SENDER closes when no more values come; receivers range.
	// Closing from the receiver side panics on send — ownership matters.
	nums := make(chan int)
	go func() {
		defer close(nums)
		for i := 1; i <= 3; i++ {
			nums <- i
		}
	}()
	sum := 0
	for v := range nums {
		sum += v
	}
	fmt.Println("sum =", sum, "(expect 6)")
}

func worker(done <-chan struct{}, id int) {
	select {
	case <-done:
		fmt.Printf("worker %d stopping\n", id)
	case <-time.After(time.Second):
		fmt.Printf("worker %d finished work\n", id)
	}
}
