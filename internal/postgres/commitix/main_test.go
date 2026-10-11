package main

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestClaimHappyPath(t *testing.T) {
	clk := newFakeClock()
	s := NewStore(5*time.Minute, clk)
	id := s.Enqueue("job")

	l, err := s.Claim("worker-A")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if l.IntentID != id {
		t.Fatalf("IntentID = %d, want %d", l.IntentID, id)
	}
	if l.LeaseGeneration != 1 {
		t.Fatalf("LeaseGeneration = %d, want 1", l.LeaseGeneration)
	}
	if l.AttemptCount != 1 {
		t.Fatalf("AttemptCount = %d, want 1", l.AttemptCount)
	}
	in, _ := s.Snapshot(id)
	if in.Status != StatusRunning || in.WorkerID != "worker-A" {
		t.Fatalf("intent = %+v, want RUNNING by worker-A", in)
	}
}

func TestClaimEmptyQueue(t *testing.T) {
	s := NewStore(time.Minute, newFakeClock())
	if _, err := s.Claim("w"); !errors.Is(err, ErrNoIntent) {
		t.Fatalf("Claim err = %v, want ErrNoIntent", err)
	}
}

// The core Commitix invariant: a worker whose lease expired and whose intent
// was taken over cannot record a result.
func TestStaleWorkerCannotCompleteAfterTakeover(t *testing.T) {
	clk := newFakeClock()
	s := NewStore(5*time.Minute, clk)
	id := s.Enqueue("job")

	stale, err := s.Claim("worker-A")
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}

	// A crashes: no heartbeat. Advance past its lease.
	clk.Advance(6 * time.Minute)
	if n := s.Recover(); n != 1 {
		t.Fatalf("Recover = %d, want 1", n)
	}

	// B takes over.
	fresh, err := s.Claim("worker-B")
	if err != nil {
		t.Fatalf("claim B: %v", err)
	}
	if fresh.LeaseGeneration != stale.LeaseGeneration+1 {
		t.Fatalf("takeover generation = %d, want %d",
			fresh.LeaseGeneration, stale.LeaseGeneration+1)
	}

	// Stale A tries to record success with its old generation.
	if err := s.Complete(stale, true); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale Complete err = %v, want ErrLeaseLost", err)
	}
	in, _ := s.Snapshot(id)
	if in.Status != StatusRunning || in.WorkerID != "worker-B" {
		t.Fatalf("stale write mutated state: %+v", in)
	}

	// Fresh B succeeds.
	if err := s.Complete(fresh, true); err != nil {
		t.Fatalf("fresh Complete: %v", err)
	}
	in, _ = s.Snapshot(id)
	if in.Status != StatusSuccess {
		t.Fatalf("status = %s, want SUCCESS", in.Status)
	}
}

// Recovery must NOT be an ownership epoch: generation and attempt count are
// preserved across the RUNNING -> READY transition.
func TestRecoveryPreservesLeaseGenerationAndAttemptCount(t *testing.T) {
	clk := newFakeClock()
	s := NewStore(5*time.Minute, clk)
	id := s.Enqueue("job")

	if _, err := s.Claim("w"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	clk.Advance(6 * time.Minute)
	if n := s.Recover(); n != 1 {
		t.Fatalf("Recover = %d, want 1", n)
	}

	in, _ := s.Snapshot(id)
	if in.Status != StatusReady {
		t.Fatalf("status = %s, want READY", in.Status)
	}
	if in.LeaseGeneration != 1 {
		t.Fatalf("lease_generation = %d, want 1 (recovery must NOT bump it)",
			in.LeaseGeneration)
	}
	if in.AttemptCount != 1 {
		t.Fatalf("attempt_count = %d, want 1 (recovery must NOT bump it)",
			in.AttemptCount)
	}
	if in.WorkerID != "" {
		t.Fatalf("worker_id = %q, want empty", in.WorkerID)
	}

	// The next claim is the real new epoch: generation 2, attempt 2.
	l2, err := s.Claim("w2")
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if l2.LeaseGeneration != 2 {
		t.Fatalf("reclaim generation = %d, want 2", l2.LeaseGeneration)
	}
	if l2.AttemptCount != 2 {
		t.Fatalf("reclaim attempt_count = %d, want 2", l2.AttemptCount)
	}
}

func TestRecoveryLeavesFreshLeasesAlone(t *testing.T) {
	clk := newFakeClock()
	s := NewStore(5*time.Minute, clk)
	s.Enqueue("job")

	l, err := s.Claim("w")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	clk.Advance(2 * time.Minute)
	if _, err := s.Renew(l); err != nil {
		t.Fatalf("renew: %v", err)
	}
	// 4m since claim, but only 2m since renew, lease is 5m -> still fresh.
	clk.Advance(2 * time.Minute)
	if n := s.Recover(); n != 0 {
		t.Fatalf("Recover = %d, want 0", n)
	}
}

func TestRenewFailsAfterLeaseLost(t *testing.T) {
	clk := newFakeClock()
	s := NewStore(5*time.Minute, clk)
	s.Enqueue("job")

	stale, err := s.Claim("A")
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}
	clk.Advance(6 * time.Minute)
	s.Recover()
	if _, err := s.Claim("B"); err != nil {
		t.Fatalf("claim B: %v", err)
	}
	if _, err := s.Renew(stale); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale Renew err = %v, want ErrLeaseLost", err)
	}
}

// Run with -race. Exactly one winner per intent, no double-claim.
func TestConcurrentClaimsAreDistinct(t *testing.T) {
	s := NewStore(time.Minute, realClock{})
	const n = 50
	for i := 0; i < n; i++ {
		s.Enqueue(fmt.Sprintf("job-%d", i))
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[int64]bool, n)
	ok := 0

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l, err := s.Claim(fmt.Sprintf("w-%d", i))
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[l.IntentID] {
				t.Errorf("intent %d claimed twice", l.IntentID)
			}
			seen[l.IntentID] = true
			ok++
		}(i)
	}
	wg.Wait()

	if ok != n {
		t.Fatalf("claimed %d, want %d", ok, n)
	}
}
