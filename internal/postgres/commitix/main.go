// This example demonstrates the Commitix intent pattern: lease-based claiming
// with a fencing token (lease_generation) and a recovery loop that releases
// expired leases so a healthy worker can take over from a stale one.
//
// It is the next step beyond SELECT ... FOR UPDATE SKIP LOCKED alone. SKIP
// LOCKED lets competing workers skip busy rows, but a crashed worker leaves
// the task "claimed" in application state forever. Commitix-style leasing
// adds an expiry (lease_until) plus a fence (lease_generation) so takeover
// is safe and a stale worker cannot write after losing its lease.
//
// The demo simulates rows in memory with an injectable clock so it runs
// without a database; the discipline matches the SQL below executed inside
// a transaction against PostgreSQL 14+ (see testdata/migrations/002_init.sql).
//
// Run: go run -race ./internal/postgres/commitix
package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Live query strings for real PostgreSQL runs (inside a transaction).
const (
	// Claim: pick the lowest READY intent, lock it, and take ownership.
	// lease_generation increments here — this is a NEW ownership epoch.
	QClaim = `
WITH candidate AS (
    SELECT id
    FROM commitix_intents
    WHERE status = 'READY'
    ORDER BY id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE commitix_intents i
SET status           = 'RUNNING',
    worker_id        = $1,
    lease_until      = now() + $2::interval,
    lease_generation = i.lease_generation + 1,
    attempt_count    = i.attempt_count + 1,
    version          = i.version + 1,
    last_modified_at = now()
FROM candidate c
WHERE i.id = c.id
RETURNING i.id, i.lease_generation, i.attempt_count;`

	// Recover: release expired leases. NOTE: lease_generation and
	// attempt_count are intentionally NOT touched. Recovery is not a new
	// ownership epoch and is not a new attempt; it only makes the intent
	// claimable again.
	QRecover = `
UPDATE commitix_intents
SET status           = 'READY',
    worker_id        = NULL,
    lease_until      = NULL,
    version          = version + 1,
    last_modified_at = now()
WHERE status = 'RUNNING'
  AND lease_until < now();`

	// Renew: heartbeat. Extends the lease only if the caller still owns it.
	QRenew = `
UPDATE commitix_intents
SET lease_until      = now() + $3::interval,
    version          = version + 1,
    last_modified_at = now()
WHERE id = $1
  AND worker_id = $2
  AND status = 'RUNNING'
  AND lease_generation = $4
RETURNING version;`

	// Complete: fenced write. If worker_id or lease_generation no longer
	// match, zero rows are affected and the write is silently discarded.
	QComplete = `
UPDATE commitix_intents
SET status           = $4,
    worker_id        = NULL,
    lease_until      = NULL,
    version          = version + 1,
    last_modified_at = now()
WHERE id = $1
  AND worker_id = $2
  AND lease_generation = $3;`
)

var (
	ErrNoIntent  = errors.New("no claimable intent")
	ErrLeaseLost = errors.New("lease lost: worker_id or lease_generation mismatch")
)

type Status string

const (
	StatusReady   Status = "READY"
	StatusRunning Status = "RUNNING"
	StatusSuccess Status = "SUCCESS"
	StatusFailed  Status = "FAILED"
)

// Clock is injectable so tests can advance time deterministically.
type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Intent models one row of commitix_intents. mu simulates the row lock taken
// by SELECT ... FOR UPDATE and held until the transaction commits.
//
// Why the mutex is a field and not on the Store: a real PostgreSQL row lock
// is per-row, not per-table. Two workers can hold two different intents
// concurrently; the store-wide mu only protects the map itself.
//
// Why Intent is never returned by value: copying it copies mu, which go vet
// rejects (correctly — a copied mutex is a latent bug). External inspection
// goes through Snapshot, which returns a mutex-free IntentView.
type Intent struct {
	mu              sync.Mutex
	ID              int64
	Status          Status
	Payload         string
	WorkerID        string // empty means none
	LeaseUntil      time.Time
	LeaseGeneration int64
	AttemptCount    int
	Version         int64
}

// IntentView is a mutex-free snapshot of an Intent for inspection. It exists
// so callers can read row state without copying the row's lock.
type IntentView struct {
	ID              int64
	Status          Status
	Payload         string
	WorkerID        string
	LeaseUntil      time.Time
	LeaseGeneration int64
	AttemptCount    int
	Version         int64
}

// Lease is a snapshot handed to a worker when it claims an intent. The worker
// must present it back to Renew / Complete; the (WorkerID, LeaseGeneration)
// pair is the fencing token.
type Lease struct {
	IntentID        int64
	WorkerID        string
	LeaseGeneration int64
	AttemptCount    int
	LeaseUntil      time.Time
	Payload         string
}

// Store is the in-memory analogue of the commitix_intents table.
type Store struct {
	mu      sync.Mutex
	nextID  int64
	intents map[int64]*Intent
	clk     Clock
	lease   time.Duration
}

func NewStore(lease time.Duration, clk Clock) *Store {
	if clk == nil {
		clk = realClock{}
	}
	return &Store{
		intents: make(map[int64]*Intent),
		clk:     clk,
		lease:   lease,
	}
}

// Enqueue inserts a new intent in READY state.
func (s *Store) Enqueue(payload string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.intents[s.nextID] = &Intent{
		ID:      s.nextID,
		Status:  StatusReady,
		Payload: payload,
	}
	return s.nextID
}

// Claim atomically takes ownership of the lowest READY intent.
// Mirrors QClaim: increments lease_generation and attempt_count.
func (s *Store) Claim(workerID string) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clk.Now()
	var picked *Intent
	for id := int64(1); id <= s.nextID; id++ {
		in, ok := s.intents[id]
		if !ok || in.Status != StatusReady {
			continue
		}
		picked = in
		break
	}
	if picked == nil {
		return Lease{}, ErrNoIntent
	}

	picked.Status = StatusRunning
	picked.WorkerID = workerID
	picked.LeaseUntil = now.Add(s.lease)
	picked.LeaseGeneration++ // new ownership epoch
	picked.AttemptCount++    // new execution attempt
	picked.Version++

	return Lease{
		IntentID:        picked.ID,
		WorkerID:        workerID,
		LeaseGeneration: picked.LeaseGeneration,
		AttemptCount:    picked.AttemptCount,
		LeaseUntil:      picked.LeaseUntil,
		Payload:         picked.Payload,
	}, nil
}

// Renew extends the lease. Mirrors QRenew: only succeeds while the caller
// still owns the intent (worker_id + lease_generation match).
func (s *Store) Renew(l Lease) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	in, ok := s.intents[l.IntentID]
	if !ok {
		return Lease{}, ErrLeaseLost
	}
	if in.Status != StatusRunning || in.WorkerID != l.WorkerID || in.LeaseGeneration != l.LeaseGeneration {
		return Lease{}, ErrLeaseLost
	}

	in.LeaseUntil = s.clk.Now().Add(s.lease)
	in.Version++
	l.LeaseUntil = in.LeaseUntil
	return l, nil
}

// Complete records the terminal outcome. Mirrors QComplete: the write is
// fenced by (worker_id, lease_generation). A stale worker that lost its
// lease gets ErrLeaseLost and its write is discarded.
func (s *Store) Complete(l Lease, success bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	in, ok := s.intents[l.IntentID]
	if !ok {
		return ErrLeaseLost
	}
	if in.Status != StatusRunning || in.WorkerID != l.WorkerID || in.LeaseGeneration != l.LeaseGeneration {
		return ErrLeaseLost
	}

	if success {
		in.Status = StatusSuccess
	} else {
		in.Status = StatusFailed
	}
	in.WorkerID = ""
	in.LeaseUntil = time.Time{}
	in.Version++
	return nil
}

// Recover releases expired RUNNING leases. Mirrors QRecover exactly:
// lease_generation and attempt_count are PRESERVED. Returns how many rows
// were released.
func (s *Store) Recover() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clk.Now()
	released := 0
	for _, in := range s.intents {
		if in.Status != StatusRunning {
			continue
		}
		if in.LeaseUntil.Before(now) {
			in.Status = StatusReady
			in.WorkerID = ""
			in.LeaseUntil = time.Time{}
			in.Version++
			// Intentionally NOT touched:
			//   in.LeaseGeneration
			//   in.AttemptCount
			released++
		}
	}
	return released
}

// Snapshot returns a mutex-free copy of an intent for inspection.
func (s *Store) Snapshot(id int64) (IntentView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ok := s.intents[id]
	if !ok {
		return IntentView{}, false
	}
	return IntentView{
		ID:              in.ID,
		Status:          in.Status,
		Payload:         in.Payload,
		WorkerID:        in.WorkerID,
		LeaseUntil:      in.LeaseUntil,
		LeaseGeneration: in.LeaseGeneration,
		AttemptCount:    in.AttemptCount,
		Version:         in.Version,
	}, true
}

func compact(q string) string { return strings.Join(strings.Fields(q), " ") }

func printLease(prefix string, l Lease) {
	fmt.Printf("%s intent %d -> %s (gen=%d, attempts=%d)\n",
		prefix, l.IntentID, l.WorkerID, l.LeaseGeneration, l.AttemptCount)
}

func main() {
	fmt.Println("Live SQL (run inside a transaction against 002_init.sql):")
	fmt.Println("  claim   :", compact(QClaim))
	fmt.Println("  recover :", compact(QRecover))
	fmt.Println("  complete:", compact(QComplete))
	fmt.Println()

	const lease = 5 * time.Minute
	clk := newFakeClock()
	s := NewStore(lease, clk)

	// --- Setup: three intents queued. ---
	for _, p := range []string{"charge-card", "send-email", "ship-order"} {
		id := s.Enqueue(p)
		fmt.Printf("enqueue: intent %d (%s)\n", id, p)
	}
	fmt.Println()

	// --- Three workers claim three intents. ---
	fmt.Println("== claims (t=0) ==")
	leaseA, err := s.Claim("worker-A")
	if err != nil {
		panic(err)
	}
	leaseB, err := s.Claim("worker-B")
	if err != nil {
		panic(err)
	}
	leaseC, err := s.Claim("worker-C")
	if err != nil {
		panic(err)
	}
	printLease("claim  ", leaseA)
	printLease("claim  ", leaseB)
	printLease("claim  ", leaseC)
	if _, err := s.Claim("worker-D"); err != nil {
		fmt.Printf("claim   worker-D -> %v (all intents busy)\n", err)
	}
	fmt.Println()

	// --- worker-A crashes; B and C keep heartbeating. ---
	fmt.Println("== worker-A crashes; B and C renew their leases at t=+3m ==")
	clk.Advance(3 * time.Minute)
	leaseB, err = s.Renew(leaseB)
	if err != nil {
		panic(err)
	}
	leaseC, err = s.Renew(leaseC)
	if err != nil {
		panic(err)
	}
	fmt.Printf("  worker-B renewed, lease_until=%s\n", leaseB.LeaseUntil.Format("15:04:05"))
	fmt.Printf("  worker-C renewed, lease_until=%s\n", leaseC.LeaseUntil.Format("15:04:05"))
	fmt.Println()

	// --- Recovery runs at t=+6m. Only A's lease has expired. ---
	fmt.Println("== recovery loop runs at t=+6m ==")
	clk.Advance(3 * time.Minute)
	n := s.Recover()
	fmt.Printf("  recovery released %d intent(s)\n", n)
	in, _ := s.Snapshot(leaseA.IntentID)
	fmt.Printf("  intent %d: status=%s lease_generation=%d attempt_count=%d  (generation & attempts PRESERVED)\n",
		in.ID, in.Status, in.LeaseGeneration, in.AttemptCount)
	fmt.Println()

	// --- worker-D takes over A's intent. ---
	fmt.Println("== worker-D takes over ==")
	leaseD, err := s.Claim("worker-D")
	if err != nil {
		panic(err)
	}
	printLease("claim  ", leaseD)
	fmt.Printf("  takeover: lease_generation %d -> %d (new ownership epoch)\n",
		leaseA.LeaseGeneration, leaseD.LeaseGeneration)
	fmt.Println()

	// --- Stale worker-A wakes up and tries to write. ---
	fmt.Println("== stale worker-A wakes up and tries to record SUCCESS ==")
	err = s.Complete(leaseA, true)
	fmt.Printf("  worker-A Complete(gen=%d): %v  <- fencing rejects the stale write\n",
		leaseA.LeaseGeneration, err)
	in, _ = s.Snapshot(leaseA.IntentID)
	fmt.Printf("  intent %d is untouched: status=%s worker=%s gen=%d\n",
		in.ID, in.Status, in.WorkerID, in.LeaseGeneration)
	fmt.Println()

	// --- worker-D completes. ---
	fmt.Println("== worker-D completes ==")
	if err := s.Complete(leaseD, true); err != nil {
		panic(err)
	}
	in, _ = s.Snapshot(leaseD.IntentID)
	fmt.Printf("  intent %d: status=%s lease_generation=%d attempt_count=%d\n",
		in.ID, in.Status, in.LeaseGeneration, in.AttemptCount)
	fmt.Println()
	fmt.Println("Takeaway: SKIP LOCKED alone cannot recover a crashed worker; the")
	fmt.Println("lease + lease_generation fence makes takeover safe and idempotent.")
}
