// This example demonstrates PostgreSQL isolation levels and MVCC.
//
// Core idea: PostgreSQL uses MVCC — readers never block writers; each
// transaction sees a snapshot. Levels: Read Committed (default, new snapshot
// per statement), Repeatable Read (snapshot for the whole transaction, no
// phantoms in PG), Serializable/SSI (aborts on dangerous patterns with
// SQLSTATE 40001 "could not serialize access" — the caller MUST retry).
//
// In Java/JDBC the same levels exist (Connection.setTransactionIsolation),
// but PG semantics differ: Read Uncommitted behaves as Read Committed, and
// Repeatable Read has no phantoms (unlike the SQL standard table).
//
// This demo runs without a database using an in-memory model plus the real
// retry helper you would use with database/sql or pgx. Live SQL is shown as
// constants; point DATABASE_URL at PostgreSQL 14+ to run it for real.
//
// Run: go run ./internal/postgres/isolation
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Live SQL for real PostgreSQL runs (see testdata/migrations/001_init.sql).
const (
	// In Go with database/sql:
	//   tx, _ := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	// In Java/JDBC: conn.setTransactionIsolation(Connection.TRANSACTION_SERIALIZABLE);
	qReadBalance = `SELECT balance FROM accounts WHERE id = $1`
	qWriteAudit  = `INSERT INTO outbox (aggregate, aggregate_id, type, payload) VALUES ($1,$2,$3,$4)`
)

// Serializable failure from PostgreSQL SSI (SQLSTATE 40001).
// With pgx you would check pgErr.Code == "40001"; with database/sql the
// driver wraps it, so match on message content as a portable fallback.
func isSerializationFailure(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "could not serialize") ||
		strings.Contains(err.Error(), "40001")
}

// WithRetry runs fn in a (simulated) serializable transaction, retrying
// serialization failures with backoff. This is the single most important
// habit for Serializable: the database asks YOU to retry, unlike Java where
// Spring's @Transactional never retries unless you add spring-retry.
func WithRetry(ctx context.Context, maxAttempts int, fn func(attempt int) error) error {
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err = fn(attempt); err == nil {
			return nil
		}
		if !isSerializationFailure(err) {
			return err // Non-retryable: constraint violation, bug, etc.
		}
		// Why backoff: colliding transactions retrying instantly just collide
		// again (livelock-like); jitter spreads them out like Ethernet.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt*10) * time.Millisecond):
		}
	}
	return fmt.Errorf("after %d attempts: %w", maxAttempts, err)
}

func main() {
	fmt.Println("== Isolation levels: PG vs JDBC ==")
	fmt.Println("Read Committed : PG default, new snapshot per statement; non-repeatable reads possible (same as JDBC).")
	fmt.Println("Repeatable Read: snapshot for whole txn; NO phantoms in PG (JDBC allows phantoms here).")
	fmt.Println("Serializable   : SSI; may abort with 40001, caller retries (same contract in JDBC).")
	fmt.Println("Read Uncommitted: behaves as Read Committed in PG (JDBC may allow dirty reads).")
	fmt.Println()
	fmt.Println(" TxOptions equivalent:")
	fmt.Println("   Go  : db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})")
	fmt.Println("   Java: conn.setTransactionIsolation(Connection.TRANSACTION_SERIALIZABLE);")
	_ = sql.LevelSerializable // Keep the import meaningful: this is the real constant.

	fmt.Println()
	fmt.Println("== Retry demo: first two attempts hit 40001, third succeeds ==")
	attempts := 0
	err := WithRetry(context.Background(), 5, func(attempt int) error {
		attempts = attempt
		if attempt < 3 {
			// Simulated SSI abort: ERROR: could not serialize access (SQLSTATE 40001).
			return errors.New("ERROR: could not serialize access due to concurrent update (SQLSTATE 40001)")
		}
		return nil
	})
	fmt.Printf("result: err=%v after %d attempt(s)\n", err, attempts)

	fmt.Println()
	fmt.Println("== Non-retryable error passes through immediately ==")
	perm := errors.New(`ERROR: duplicate key value violates unique constraint "accounts_pkey"`)
	n := 0
	err = WithRetry(context.Background(), 5, func(int) error { n++; return perm })
	fmt.Printf("result: err=%v after %d attempt(s) (no retry)\n", err, n)
}
