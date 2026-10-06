// This example demonstrates the PostgreSQL deduplication table.
//
// Schema (see testdata/migrations/001_init.sql):
//
//	CREATE TABLE idempotency_keys (
//	    key           TEXT PRIMARY KEY,
//	    request_hash  TEXT NOT NULL,
//	    response_code INT NOT NULL,
//	    response_body JSONB,
//	    created_at    TIMESTAMPTZ NOT NULL DEFAULT now());
//
// Algorithm (the atomic check is the whole trick):
//  1. INSERT the key with ON CONFLICT DO NOTHING.
//  2. If the row was inserted => first delivery: execute, UPDATE the row with
//     request_hash + response, return the fresh response.
//  3. If the key existed => redelivery: compare stored request_hash with the
//     current request; same hash => replay stored response; different hash =>
//     409 Conflict (key reuse with a different payload is a client bug).
//
// Retention: DELETE WHERE created_at < now() - INTERVAL '72 hours' via
// cron/pg_cron. In Java/Spring the same table + SQL applies; only the
// repository spelling changes.
//
// This demo is in-memory so it runs without PostgreSQL; the state machine
// mirrors the SQL exactly.
//
// Run: go run ./internal/idempotency/dedup_table
package main

import (
	"errors"
	"fmt"
	"sync"
)

// Stored response replayed on redelivery.
type Response struct {
	Code int
	Body string
}

// Record mirrors one idempotency_keys row.
type Record struct {
	RequestHash string
	Resp        Response
	HasResp     bool // False between INSERT and UPDATE (executing state).
}

// Table is the in-memory idempotency_keys. mu serializes check-and-insert
// exactly like the PRIMARY KEY constraint serializes concurrent INSERTs.
type Table struct {
	mu   sync.Mutex
	rows map[string]*Record
}

func NewTable() *Table { return &Table{rows: make(map[string]*Record)} }

// ErrConflict is returned when a key is reused with a different request hash.
var ErrConflict = errors.New("idempotency key reused with different request payload (409)")

// Do executes-or-replays op for key. op runs at most once per key.
// outcome is "executed" (first delivery) or "replayed" (redelivery).
func (t *Table) Do(key, reqHash string, op func() Response) (resp Response, outcome string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if rec, exists := t.rows[key]; exists {
		// ON CONFLICT branch: key seen before.
		if !rec.HasResp {
			// A concurrent execution is in flight for this key. Production
			// systems either block-and-poll the row or return 409/425; here
			// we report the race explicitly.
			return Response{}, "", errors.New("duplicate in-flight request for key (retry shortly)")
		}
		if rec.RequestHash != reqHash {
			return Response{}, "", ErrConflict
		}
		return rec.Resp, "replayed", nil
	}
	// INSERT ... ON CONFLICT DO NOTHING succeeded: we own this key.
	// Why unlock during op is a design decision: holding the lock models the
	// row lock; real systems release the DB txn and re-UPDATE afterwards.
	rec := &Record{RequestHash: reqHash}
	t.rows[key] = rec
	t.mu.Unlock()
	resp = op() // Execute the business operation EXACTLY once.
	t.mu.Lock()
	rec.Resp, rec.HasResp = resp, true
	return resp, "executed", nil
}

func main() {
	tbl := NewTable()
	executions := 0
	charge := func() Response {
		executions++
		return Response{Code: 200, Body: `{"charge":"ch_1","amount":100}`}
	}

	fmt.Println("== 1. first delivery executes ==")
	resp, outcome, err := tbl.Do("key-abc", "hash-v1", charge)
	fmt.Printf("outcome=%s resp=%+v err=%v executions=%d\n", outcome, resp, err, executions)

	fmt.Println("== 2. retry with same key+hash replays stored response ==")
	resp, outcome, err = tbl.Do("key-abc", "hash-v1", charge)
	fmt.Printf("outcome=%s resp=%+v err=%v executions=%d (still 1)\n", outcome, resp, err, executions)

	fmt.Println("== 3. same key with DIFFERENT payload is a 409 ==")
	_, _, err = tbl.Do("key-abc", "hash-v2-different-payload", charge)
	fmt.Printf("err=%v executions=%d\n", err, executions)

	fmt.Println()
	fmt.Println("Live SQL: INSERT INTO idempotency_keys (key, request_hash, ...)")
	fmt.Println("          VALUES ($1,$2,...) ON CONFLICT DO NOTHING;")
}
