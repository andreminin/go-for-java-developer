// This example demonstrates the Transactional Outbox pattern.
//
// Dual-write problem: updating business data AND publishing an event cannot
// be atomic — if the process crashes between DB commit and broker publish,
// the event is lost (or published twice on retry). In Java/Spring this bites
// with @Transactional + KafkaTemplate.send in the same method.
//
// Solution: INSERT the event into an `outbox` table in the SAME transaction
// as the business update (atomic by definition), then a separate Relay polls
// unpublished rows (SELECT ... FOR UPDATE SKIP LOCKED) and publishes them,
// marking published_at. At-least-once delivery + idempotent consumers.
//
// Live SQL (see testdata/migrations/001_init.sql):
//
//	BEGIN;
//	UPDATE accounts SET balance = balance - $1 WHERE id = $2;
//	INSERT INTO outbox (aggregate, aggregate_id, type, payload)
//	VALUES ('account', $1, 'BalanceDebited', $2);
//	COMMIT;
//	-- relay: SELECT id, payload FROM outbox WHERE published_at IS NULL
//	--        ORDER BY id LIMIT 100 FOR UPDATE SKIP LOCKED;
//
// Libraries: o4x, pg-outbox, outboxer (mentioned for real projects).
// This demo is in-memory so it runs without PostgreSQL.
//
// Run: go run ./internal/postgres/outbox
package main

import (
	"fmt"
	"sync"
)

// Event is one outbox row.
type Event struct {
	ID        int64
	Type      string
	Payload   string
	Published bool
}

// Store simulates accounts + outbox with transactional semantics: SaveDebited
// applies the balance change and the outbox insert atomically under one lock
// (the analog of a single BEGIN/COMMIT). A crash between them is impossible
// by construction — that impossibility IS the pattern.
type Store struct {
	mu        sync.Mutex
	balance   int
	outbox    []Event
	nextID    int64
	publish   func(Event) error // Fake broker; fails when nil-behaving test wants.
	published []Event
}

func NewStore(balance int, publish func(Event) error) *Store {
	return &Store{balance: balance, publish: publish}
}

// SaveDebited debits and records the event atomically.
// In Java: @Transactional method doing accountRepo.save + outboxRepo.save.
func (s *Store) SaveDebited(amount int, payload string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.balance -= amount
	s.nextID++
	s.outbox = append(s.outbox, Event{ID: s.nextID, Type: "BalanceDebited", Payload: payload})
}

// Relay publishes unpublished events (oldest first) and marks them published.
// In production: one SELECT ... FOR UPDATE SKIP LOCKED batch per worker tick;
// concurrent relays never grab the same row. Here the mutex plays that role.
func (s *Store) Relay() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.outbox {
		if s.outbox[i].Published {
			continue // Already delivered: skip (relay restarted mid-batch).
		}
		if err := s.publish(s.outbox[i]); err != nil {
			return err // Leave unpublished: next tick retries (at-least-once).
		}
		s.outbox[i].Published = true
		s.published = append(s.published, s.outbox[i])
	}
	return nil
}

func (s *Store) Balance() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.balance
}

func (s *Store) Unpublished() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.outbox {
		if !e.Published {
			n++
		}
	}
	return n
}

func main() {
	var sent []string
	store := NewStore(1000, func(e Event) error {
		sent = append(sent, e.Payload)
		return nil
	})

	fmt.Println("== 1. business update + outbox insert commit atomically ==")
	store.SaveDebited(100, `{"account":1,"amount":100}`)
	fmt.Printf("balance=%d unpublished=%d (broker not yet contacted)\n", store.Balance(), store.Unpublished())

	fmt.Println("== 2. relay publishes and marks published ==")
	if err := store.Relay(); err != nil {
		fmt.Println("relay err:", err)
		return
	}
	fmt.Printf("sent=%v unpublished=%d\n", sent, store.Unpublished())

	fmt.Println("== 3. relay restart is safe: nothing published twice ==")
	if err := store.Relay(); err != nil {
		fmt.Println("relay err:", err)
		return
	}
	fmt.Printf("sent=%v (unchanged), unpublished=%d\n", sent, store.Unpublished())
}
