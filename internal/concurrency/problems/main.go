// This example demonstrates classic concurrency problems and their fixes.
//
// Covered: data race (with -race), deadlock (Coffman cycle, detected via
// timeout instead of hanging the demo), livelock (CAS retry storm), and
// mutex starvation mode. Each shows BAD code then GOOD code.
//
// Run: go run -race ./internal/concurrency/problems
package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	fmt.Println("== 1. DATA RACE: bad vs good ==")
	fmt.Printf("bad counter  = %d (likely < 20000: increments lost)\n", badCounter())
	fmt.Printf("good counter = %d (expect 20000)\n", goodCounter())
	fmt.Println("What happens: run with -race to see WARNING: DATA RACE on the bad path.")
	fmt.Println("How it looks in Java: same lost update as unsynchronized i++.")

	fmt.Println("== 2. DEADLOCK: lock ordering ==")
	// Coffman conditions need mutual exclusion + hold-and-wait + no preemption
	// + circular wait. Breaking any one (here: global lock order) prevents it.
	var a, b sync.Mutex
	// BAD (commented, would hang): go1 locks a→b while go2 locks b→a.
	// GOOD: both sides lock in the same order a→b.
	done := make(chan struct{})
	go func() { lockOrdered(&a, &b); done <- struct{}{} }()
	go func() { lockOrdered(&a, &b); done <- struct{}{} }()
	timeout := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-timeout:
			fmt.Println("DEADLOCK detected: goroutines stuck (would print in bad version)")
			return
		}
	}
	fmt.Println("no deadlock: consistent lock order a->b on both sides")

	fmt.Println("== 3. LIVELOCK: CAS retry without backoff vs with backoff ==")
	// Livelock: threads run but make no progress — two CAS loops keep
	// colliding. Fix: randomized/exponential backoff (like Ethernet).
	fmt.Println("spins without backoff:", casStorm(false), "(many wasted attempts)")
	fmt.Println("spins with backoff:   ", casStorm(true), "(fewer wasted attempts)")

	fmt.Println("== 4. STARVATION: mutex hands over after ~1ms wait ==")
	// Why no code demo: sync.Mutex enters starvation mode automatically when
	// a waiter has been blocked >1ms — the lock is handed directly to it.
	// Lesson for Java devs: Go chose bounded unfairness; Java's default
	// ReentrantLock/ReadWriteLock can starve writers indefinitely under
	// continuous readers unless constructed fair(true).
	fmt.Println("documented behavior: waiter blocked >1ms gets the lock directly.")
}

// BAD: unsynchronized read-modify-write. Under -race this reports a race;
// even without the detector the result is usually short of 20000.
func badCounter() int {
	n := 0
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10000; i++ {
				n++ // RACE: load, increment, store interleaves with the other goroutine.
			}
		}()
	}
	wg.Wait()
	return n
}

// GOOD: same work serialized by a mutex.
func goodCounter() int {
	var mu sync.Mutex
	n := 0
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10000; i++ {
				mu.Lock()
				n++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return n
}

func lockOrdered(a, b *sync.Mutex) {
	// Global order: always a before b. Breaks the circular-wait condition,
	// the same fix as lock-ordering discipline in Java (or tryLock + backoff).
	a.Lock()
	defer a.Unlock()
	time.Sleep(time.Millisecond) // Widen the race window so ordering matters.
	b.Lock()
	defer b.Unlock()
}

// casStorm runs two goroutines hammering one atomic with CAS.
// Returns total failed attempts. With backoff the losers sleep briefly,
// letting the winner finish instead of colliding forever.
func casStorm(backoff bool) int64 {
	var v atomic.Int64
	var fails atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				for {
					old := v.Load()
					if v.CompareAndSwap(old, old+1) {
						break
					}
					fails.Add(1)
					// Without backoff both loops collide hot (livelock-like);
					// with backoff the loser yields so the winner progresses.
					if backoff {
						time.Sleep(time.Microsecond)
					}
				}
			}
		}()
	}
	wg.Wait()
	return fails.Load()
}
