//go:build integration

// Live-PostgreSQL tests for the outbox relay claim, inbox dedup, and
// idempotency dedup SQL. See doc.go for how to run them.
//
// Why these three: they are the statements whose correctness cannot be
// proven by reading alone — SKIP LOCKED concurrency semantics and
// ON CONFLICT row counts are database behaviors, so they are asserted
// against the real thing. Everything timing-sensitive (e.g. SSI aborts)
// is deliberately left to the README walkthroughs instead of flaky tests.
package integration

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/lib/pq"
)

// openDB connects to the compose database and applies the schema migration,
// so the test is self-sufficient once the container is up. Unreachable DB
// => Skip, keeping tag-less CI green.
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		// Default matches docker-compose.yml and the README walkthroughs.
		url = "postgres://app:app@localhost:5432/app?sslmode=disable"
	}
	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Skipf("integration needs PostgreSQL (%v); see doc.go", err)
	}
	if err := db.Ping(); err != nil {
		t.Skipf("integration needs PostgreSQL at %s (%v); see doc.go", url, err)
	}
	mig, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "migrations", "001_init.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	// Why Exec, not a migration tool: the file is plain idempotent DDL
	// (CREATE TABLE IF NOT EXISTS), and lib/pq runs multi-statement scripts.
	if _, err := db.Exec(string(mig)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestInboxDedupOnConflict proves the consumer-side exactly-once check:
// the second INSERT with the same event_id is a no-op (0 rows affected),
// not an error — the `INSERT 0 0` from the README walkthrough.
func TestInboxDedupOnConflict(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`TRUNCATE inbox`); err != nil {
		t.Fatal(err)
	}
	const q = `INSERT INTO inbox (event_id, type, payload)
	           VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`
	first, err := db.Exec(q, "evt-live-1", "BalanceCredited", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := first.RowsAffected(); n != 1 {
		t.Fatalf("first insert affected %d rows, want 1", n)
	}
	second, err := db.Exec(q, "evt-live-1", "BalanceCredited", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := second.RowsAffected(); n != 0 {
		t.Fatalf("redelivery affected %d rows, want 0 (ON CONFLICT no-op)", n)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("inbox rows = %d, want 1", count)
	}
}

// TestSkipLockedClaimIsDisjoint proves the outbox relay claim: while one
// transaction holds row locks, a competing relay with SKIP LOCKED sees zero
// rows instead of blocking — then sees all of them after commit.
// No sleeps: txA holds its locks synchronously in an open transaction, so
// the interleaving is deterministic.
func TestSkipLockedClaimIsDisjoint(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`TRUNCATE outbox`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := db.Exec(
			`INSERT INTO outbox (aggregate, aggregate_id, type, payload)
			 VALUES ('account', '1', 'BalanceDebited', '{"amount":100}')`,
		); err != nil {
			t.Fatal(err)
		}
	}

	const claim = `SELECT id FROM outbox WHERE published_at IS NULL
	               ORDER BY id LIMIT 10 FOR UPDATE`
	const claimSkip = claim + ` SKIP LOCKED`

	collect := func(tx *sql.Tx, query string) []int64 {
		t.Helper()
		rows, err := tx.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return ids
	}

	// Relay A claims (and holds) all 4 rows.
	txA, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer txA.Rollback()
	if got := collect(txA, claim); len(got) != 4 {
		t.Fatalf("relay A claimed %d rows, want 4", len(got))
	}

	// Relay B must skip A's locked rows, not block on them.
	txB, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer txB.Rollback()
	if got := collect(txB, claimSkip); len(got) != 0 {
		t.Fatalf("relay B claimed %d rows under contention, want 0 (SKIP LOCKED)", len(got))
	}
	if err := txB.Rollback(); err != nil {
		t.Fatal(err)
	}

	// After A commits, the rows are claimable again.
	if err := txA.Commit(); err != nil {
		t.Fatal(err)
	}
	txC, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer txC.Rollback()
	if got := collect(txC, claimSkip); len(got) != 4 {
		t.Fatalf("relay C claimed %d rows after commit, want 4", len(got))
	}
	if err := txC.Rollback(); err != nil {
		t.Fatal(err)
	}
}

// TestIdempotencyKeyDedupOnConflict proves the Module 3 atomic check end to
// end: re-INSERTing the same key is a no-op, so the first stored response is
// the one a retry would replay.
func TestIdempotencyKeyDedupOnConflict(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`TRUNCATE idempotency_keys`); err != nil {
		t.Fatal(err)
	}
	const q = `INSERT INTO idempotency_keys (key, request_hash, response_code, response_body)
	           VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`
	if _, err := db.Exec(q, "key-live", "h1", 200, `{"ok":true}`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(q, "key-live", "h1", 200, `{"ok":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Fatalf("re-insert affected %d rows, want 0", n)
	}
	var code int
	var sameBody bool
	// Why compare as jsonb, not text: PostgreSQL normalizes JSONB on output
	// (`{"ok":true}` comes back as `{"ok": true}`), so text comparison is
	// brittle. JSONB equality is semantic — exactly what a replay check needs.
	if err := db.QueryRow(
		`SELECT response_code, (response_body = '{"ok":true}'::jsonb)
		   FROM idempotency_keys WHERE key = 'key-live'`,
	).Scan(&code, &sameBody); err != nil {
		t.Fatal(err)
	}
	if code != 200 || !sameBody {
		t.Fatalf("stored response = (%d, semantic-match=%v), want (200, true)", code, sameBody)
	}
}
