// This example demonstrates the Transactional Inbox pattern.
//
// Mirror of the outbox on the consumer side: a redelivered event (broker
// at-least-once, webhook retry from a provider) must not be applied twice.
// Solution: INSERT the event ID into an `inbox` table with
// ON CONFLICT DO NOTHING in the SAME transaction that applies the business
// effect. The insert is the atomic "have I seen this?" check.
//
// Live SQL (see testdata/migrations/001_init.sql):
//
//	BEGIN;
//	INSERT INTO inbox (event_id, type, payload) VALUES ($1,$2,$3)
//	ON CONFLICT DO NOTHING;
//	-- if row was inserted: apply business effect (credit account, ...)
//	COMMIT;
//
// In Java/Spring: @Transactional method doing inboxRepo.insertIgnore +
// accountRepo.credit. Duplicate delivery hits the conflict branch and replays
// nothing. Combined with outbox on the producer, the pipeline is
// end-to-end exactly-once *effect* over at-least-once delivery.
//
// Run: go run ./internal/postgres/inbox
package main

import (
	"fmt"
	"sync"
)

// Consumer applies each event_id at most once. seen plays the inbox table
// (PRIMARY KEY on event_id); balance is the business state updated in the
// same critical section (the analog of one DB transaction).
type Consumer struct {
	mu      sync.Mutex
	seen    map[string]bool
	balance int
	applies int
}

func NewConsumer(balance int) *Consumer {
	return &Consumer{seen: make(map[string]bool), balance: balance}
}

// Handle processes one delivery. Returns (applied, skipped).
// Duplicate event_id => ON CONFLICT DO NOTHING branch: no second credit.
func (c *Consumer) Handle(eventID string, credit int) (applied, skipped bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[eventID] {
		return false, true // Conflict branch: already processed.
	}
	c.seen[eventID] = true
	c.balance += credit
	c.applies++
	return true, false
}

func (c *Consumer) Balance() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.balance
}

func main() {
	c := NewConsumer(1000)

	fmt.Println("== 1. first delivery applies ==")
	applied, skipped := c.Handle("evt-1", 100)
	fmt.Printf("applied=%v skipped=%v balance=%d\n", applied, skipped, c.Balance())

	fmt.Println("== 2. broker redelivery of evt-1 is ignored ==")
	applied, skipped = c.Handle("evt-1", 100)
	fmt.Printf("applied=%v skipped=%v balance=%d (unchanged)\n", applied, skipped, c.Balance())

	fmt.Println("== 3. new event evt-2 applies normally ==")
	applied, skipped = c.Handle("evt-2", 50)
	fmt.Printf("applied=%v skipped=%v balance=%d\n", applied, skipped, c.Balance())

	fmt.Println()
	fmt.Println("Live SQL: INSERT INTO inbox (event_id, type, payload) VALUES ($1,$2,$3)")
	fmt.Println("          ON CONFLICT DO NOTHING; -- then apply effect in the same txn")
}
