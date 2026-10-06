// This example demonstrates SELECT ... FOR UPDATE and its variants.
//
// Problem (lost update): two transactions read balance=1000, each subtracts
// 100, both write 900 — one update is silently lost (final 900, not 800).
// In Java/JDBC the same bug exists with plain SELECT + UPDATE.
//
// Solution: SELECT ... FOR UPDATE takes a row lock held until COMMIT, so the
// second transaction blocks and then reads the already-updated row.
// Variants: SKIP LOCKED (task queues — skip busy rows), NOWAIT (fail fast).
//
// Live SQL (PostgreSQL 14+, see testdata/migrations/001_init.sql):
//
//	SELECT balance FROM accounts WHERE id = $1 FOR UPDATE;
//	SELECT id FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT 10 FOR UPDATE SKIP LOCKED;
//	SELECT balance FROM accounts WHERE id = $1 FOR UPDATE NOWAIT;
//
// This demo simulates row locking in memory so it runs without a database;
// the locking discipline is identical to what PostgreSQL enforces.
//
// Run: go run -race ./internal/postgres/select_for_update
package main

import (
	"fmt"
	"sync"
)

// Live query strings for real PostgreSQL runs.
const (
	QBalanceForUpdate = `SELECT balance FROM accounts WHERE id = $1 FOR UPDATE`
	QQueueSkipLocked  = `SELECT id FROM outbox WHERE published_at IS NULL ORDER BY id LIMIT 10 FOR UPDATE SKIP LOCKED`
	QBalanceNoWait    = `SELECT balance FROM accounts WHERE id = $1 FOR UPDATE NOWAIT`
)

// Account models one row. mu simulates the PostgreSQL row lock taken by
// SELECT ... FOR UPDATE and held until transaction end (Commit/Unlock here).
// In Java/JDBC: SELECT ... FOR UPDATE inside a transaction with autoCommit=false.
type Account struct {
	mu      sync.Mutex
	balance int
}

// WithdrawPlain is the BUG: read, compute, write without a row lock.
// Two concurrent calls interleave: both read 1000, both write 900.
func (a *Account) WithdrawPlain(amount int, ready, goAhead chan struct{}) {
	b := a.balance // Dirty-window read: no lock held.
	if ready != nil {
		close(ready) // Signal "I have read", let the peer read too...
		<-goAhead    // ...then both write back based on the same stale value.
	}
	a.balance = b - amount
}

// WithdrawLocked is the FIX: the whole read-modify-write holds the row lock,
// exactly like SELECT ... FOR UPDATE ... UPDATE ... COMMIT in one txn.
func (a *Account) WithdrawLocked(amount int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.balance -= amount
}

func main() {
	fmt.Println("Live SQL:")
	fmt.Println(" ", QBalanceForUpdate)
	fmt.Println(" ", QQueueSkipLocked)
	fmt.Println(" ", QBalanceNoWait)
	fmt.Println()

	fmt.Println("== BUG: two concurrent -100 withdrawals without row lock ==")
	lost := &Account{balance: 1000}
	ready1, ready2 := make(chan struct{}), make(chan struct{})
	goAhead := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); lost.WithdrawPlain(100, ready1, goAhead) }()
	go func() { defer wg.Done(); lost.WithdrawPlain(100, ready2, goAhead) }()
	<-ready1
	<-ready2 // Both have now read 1000; release them to write back 900.
	close(goAhead)
	wg.Wait()
	fmt.Printf("balance = %d (want 800; got 900 => one update LOST)\n", lost.balance)

	fmt.Println("== FIX: same workload with SELECT FOR UPDATE discipline ==")
	safe := &Account{balance: 1000}
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() { defer wg.Done(); safe.WithdrawLocked(100) }()
	}
	wg.Wait()
	fmt.Printf("balance = %d (expect 800)\n", safe.balance)

	fmt.Println()
	fmt.Println("Queue guidance: use FOR UPDATE SKIP LOCKED so competing relay")
	fmt.Println("workers skip busy rows instead of blocking on each other; use")
	fmt.Println("NOWAIT when you'd rather fail fast than queue behind a lock.")
}
