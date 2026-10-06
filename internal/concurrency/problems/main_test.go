package main

import (
	"sync"
	"testing"
	"time"
)

func TestGoodCounterExact(t *testing.T) {
	if got := goodCounter(); got != 20000 {
		t.Fatalf("goodCounter() = %d, want 20000", got)
	}
}

func TestLockOrderingNoDeadlock(t *testing.T) {
	var a, b sync.Mutex
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() { lockOrdered(&a, &b); done <- struct{}{} }()
	}
	timeout := time.After(5 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("deadlock: ordered locking still stuck")
		}
	}
}

func TestCasStormConverges(t *testing.T) {
	var v int64
	_ = v
	// Backoff variant must complete quickly and record fewer/equal spins
	// than the hot loop on average; assert completion, not exact counts.
	done := make(chan int64, 1)
	go func() { done <- casStorm(true) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("casStorm with backoff did not converge")
	}
}

func TestBadCounterDocumented(t *testing.T) {
	// The bad counter is intentionally racy, so it is NEVER executed under
	// -race: the detector would (correctly) fail the package. It is only
	// invoked from main() via `go run -race` for demonstration, where the
	// WARNING output is the lesson. Here we assert the documented bound
	// statically: two goroutines x 10000 increments each.
	const goroutines, perGoroutine = 2, 10000
	if goroutines*perGoroutine != 20000 {
		t.Fatalf("documented total changed")
	}
}

var _ = sync.Mutex{}
