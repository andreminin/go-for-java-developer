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

// openDB connects to the compose database and applies the schema migrations,
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
	// Why loop over both files: they are plain idempotent DDL
	// (CREATE TABLE IF NOT EXISTS), and lib/pq runs multi-statement scripts.
	// 002 adds commitix_intents; keeping it here means the Commitix tests
	// never need a second setup step.
	for _, name := range []string{"001_init.sql", "002_init.sql"} {
		mig, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "migrations", name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := db.Exec(string(mig)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
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

// ---------------------------------------------------------------------------
// Commitix: lease-based claim with a fencing token (lease_generation).
//
// These tests pin the three invariants that make takeover safe. The claim
// SQL is the same shape as QClaim in internal/postgres/commitix_demo, run
// against the real commitix_intents table so the CTE + FOR UPDATE SKIP
// LOCKED interaction is exercised end to end.
// ---------------------------------------------------------------------------

// queryer is satisfied by *sql.DB and *sql.Tx, so the same claim helper can
// be run either as a single atomic statement (DB) or held open across an
// interleaving (Tx) — needed by the SKIP LOCKED disjointness test.
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

// commitixClaim runs the atomic claim statement and returns the claimed
// (id, lease_generation, attempt_count). id == 0 means nothing was claimable.
//
// Why a CTE and not two statements: the SELECT FOR UPDATE SKIP LOCKED and
// the UPDATE must run in one statement, otherwise another session could
// interleave between "I picked this row" and "I own it".
func commitixClaim(t *testing.T, q queryer, workerID, lease string) (int64, int64, int) {
	t.Helper()
	const claim = `
WITH candidate AS (
    SELECT id FROM commitix_intents
    WHERE status = 'READY'
    ORDER BY id LIMIT 1
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
RETURNING i.id, i.lease_generation, i.attempt_count`
	var id, gen int64
	var attempts int
	err := q.QueryRow(claim, workerID, lease).Scan(&id, &gen, &attempts)
	if err == sql.ErrNoRows {
		return 0, 0, 0 // empty queue — not an error
	}
	if err != nil {
		t.Fatal(err)
	}
	return id, gen, attempts
}

// commitixRecover mirrors QRecover. Returns rows released. The whole point
// is what it does NOT touch: lease_generation and attempt_count.
func commitixRecover(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`
UPDATE commitix_intents
SET status           = 'READY',
    worker_id        = NULL,
    lease_until      = NULL,
    version          = version + 1,
    last_modified_at = now()
WHERE status = 'RUNNING'
  AND lease_until < now()`)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := res.RowsAffected()
	return n
}

// commitixSeed inserts n READY intents.
func commitixSeed(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	if _, err := db.Exec(`TRUNCATE commitix_intents`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := db.Exec(
			`INSERT INTO commitix_intents (payload) VALUES ($1)`,
			`{"job":"demo"}`,
		); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCommitixClaimSetsLeaseAndEpoch proves the happy path: a claim flips
// READY -> RUNNING, sets worker_id and lease_until, and advances both the
// ownership epoch (lease_generation) and the retry counter (attempt_count).
func TestCommitixClaimSetsLeaseAndEpoch(t *testing.T) {
	db := openDB(t)
	commitixSeed(t, db, 1)

	id, gen, attempts := commitixClaim(t, db, "worker-A", "5 minutes")
	if id == 0 {
		t.Fatal("claim returned no row")
	}
	if gen != 1 {
		t.Fatalf("lease_generation = %d, want 1", gen)
	}
	if attempts != 1 {
		t.Fatalf("attempt_count = %d, want 1", attempts)
	}

	var status, worker string
	var hasLease bool
	var version int64
	if err := db.QueryRow(
		`SELECT status, worker_id, lease_until IS NOT NULL, version
		   FROM commitix_intents WHERE id = $1`, id,
	).Scan(&status, &worker, &hasLease, &version); err != nil {
		t.Fatal(err)
	}
	if status != "RUNNING" || worker != "worker-A" || !hasLease || version != 1 {
		t.Fatalf("row = (%s, %s, hasLease=%v, version=%d), want (RUNNING, worker-A, true, 1)",
			status, worker, hasLease, version)
	}
}

// TestCommitixClaimOnEmptyQueue: no claimable rows is not an error; the
// UPDATE ... FROM an empty candidate affects zero rows, so RETURNING yields
// sql.ErrNoRows. The helper maps that to id == 0.
func TestCommitixClaimOnEmptyQueue(t *testing.T) {
	db := openDB(t)
	commitixSeed(t, db, 0)

	if id, _, _ := commitixClaim(t, db, "worker-A", "5 minutes"); id != 0 {
		t.Fatalf("claim on empty queue returned id = %d, want 0", id)
	}
}

// TestCommitixClaimSkipsBusyIntents proves the SKIP LOCKED disjointness the
// pattern depends on: while one open transaction holds a claimed row, a
// competing claim skips it and takes the next READY row instead of blocking.
// No sleeps: txA holds its lock synchronously.
func TestCommitixClaimSkipsBusyIntents(t *testing.T) {
	db := openDB(t)
	commitixSeed(t, db, 2)

	txA, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer txA.Rollback()
	idA, genA, _ := commitixClaim(t, txA, "worker-A", "5 minutes")
	if idA == 0 {
		t.Fatal("worker-A claimed nothing")
	}
	if genA != 1 {
		t.Fatalf("worker-A gen = %d, want 1", genA)
	}

	// txA has not committed. txB's claim must skip txA's row.
	txB, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer txB.Rollback()
	idB, genB, _ := commitixClaim(t, txB, "worker-B", "5 minutes")
	if idB == 0 {
		t.Fatal("worker-B claimed nothing; SKIP LOCKED should have found the second row")
	}
	if idB == idA {
		t.Fatalf("both workers claimed the same id %d; SKIP LOCKED not effective", idA)
	}
	if genB != 1 {
		t.Fatalf("worker-B gen = %d, want 1 (independent epoch per intent)", genB)
	}

	if err := txA.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := txB.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestCommitixRecoveryPreservesGenerationAndAttemptCount is the invariant
// that makes the fencing token meaningful: recovery is not an ownership
// change and is not a retry, so neither lease_generation nor attempt_count
// may move across the RUNNING -> READY transition.
func TestCommitixRecoveryPreservesGenerationAndAttemptCount(t *testing.T) {
	db := openDB(t)
	commitixSeed(t, db, 1)

	id, gen, attempts := commitixClaim(t, db, "worker-A", "5 minutes")
	if id == 0 {
		t.Fatal("claim returned no row")
	}
	if gen != 1 || attempts != 1 {
		t.Fatalf("after claim: gen=%d attempts=%d, want 1/1", gen, attempts)
	}

	// Simulate a crashed worker: force the lease into the past rather than
	// waiting. This is exactly the state a stale worker leaves behind.
	if _, err := db.Exec(
		`UPDATE commitix_intents SET lease_until = now() - interval '1 second'
		  WHERE id = $1`, id,
	); err != nil {
		t.Fatal(err)
	}

	if n := commitixRecover(t, db); n != 1 {
		t.Fatalf("recovery released %d rows, want 1", n)
	}

	var status string
	var genAfter int64
	var attemptsAfter int
	if err := db.QueryRow(
		`SELECT status, lease_generation, attempt_count
		   FROM commitix_intents WHERE id = $1`, id,
	).Scan(&status, &genAfter, &attemptsAfter); err != nil {
		t.Fatal(err)
	}
	if status != "READY" {
		t.Fatalf("status = %s, want READY", status)
	}
	if genAfter != 1 {
		t.Fatalf("lease_generation = %d after recovery, want 1 (must NOT bump)", genAfter)
	}
	if attemptsAfter != 1 {
		t.Fatalf("attempt_count = %d after recovery, want 1 (must NOT bump)", attemptsAfter)
	}
}

// TestCommitixRecoveryLeavesFreshLeaseAlone: recovery is a targeted sweep,
// not a blanket reset. A lease that has not expired must be untouched.
func TestCommitixRecoveryLeavesFreshLeaseAlone(t *testing.T) {
	db := openDB(t)
	commitixSeed(t, db, 1)

	if id, _, _ := commitixClaim(t, db, "worker-A", "5 minutes"); id == 0 {
		t.Fatal("claim returned no row")
	}
	if n := commitixRecover(t, db); n != 0 {
		t.Fatalf("recovery released %d rows with a fresh lease, want 0", n)
	}
}

// TestCommitixStaleWorkerCompleteIsRejected is the whole point of the
// fencing token. After takeover, the old worker's Complete statement must
// affect zero rows: the WHERE clause (worker_id, lease_generation) no longer
// matches, so the stale write is silently discarded and the new owner's
// state is untouched. This is the failure mode a plain SKIP LOCKED queue
// cannot prevent.
func TestCommitixStaleWorkerCompleteIsRejected(t *testing.T) {
	db := openDB(t)
	commitixSeed(t, db, 1)

	// Worker A claims and records its fencing token.
	id, staleGen, _ := commitixClaim(t, db, "worker-A", "5 minutes")
	if id == 0 {
		t.Fatal("worker-A claimed nothing")
	}

	// A crashes: no heartbeat, lease expires.
	if _, err := db.Exec(
		`UPDATE commitix_intents SET lease_until = now() - interval '1 second'
		  WHERE id = $1`, id,
	); err != nil {
		t.Fatal(err)
	}
	if n := commitixRecover(t, db); n != 1 {
		t.Fatalf("recovery released %d rows, want 1", n)
	}

	// Worker B takes over: the generation must advance.
	_, freshGen, attemptsAfter := commitixClaim(t, db, "worker-B", "5 minutes")
	if freshGen != staleGen+1 {
		t.Fatalf("takeover generation = %d, want %d (stale+1)", freshGen, staleGen+1)
	}
	if attemptsAfter != 2 {
		t.Fatalf("attempt_count after takeover = %d, want 2", attemptsAfter)
	}

	// Stale A wakes up and tries to record SUCCESS with its old token.
	// Same shape as QComplete: the fence is the WHERE clause.
	const complete = `
UPDATE commitix_intents
SET status = $4, worker_id = NULL, lease_until = NULL,
    version = version + 1, last_modified_at = now()
WHERE id = $1 AND worker_id = $2 AND lease_generation = $3`

	stale, err := db.Exec(complete, id, "worker-A", staleGen, "SUCCESS")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := stale.RowsAffected(); n != 0 {
		t.Fatalf("stale Complete affected %d rows, want 0 (fence must reject)", n)
	}

	// State must still belong to worker-B.
	var status, worker string
	var genNow int64
	if err := db.QueryRow(
		`SELECT status, worker_id, lease_generation
		   FROM commitix_intents WHERE id = $1`, id,
	).Scan(&status, &worker, &genNow); err != nil {
		t.Fatal(err)
	}
	if status != "RUNNING" || worker != "worker-B" || genNow != freshGen {
		t.Fatalf("stale write mutated state: (%s, %s, gen=%d), want (RUNNING, worker-B, %d)",
			status, worker, genNow, freshGen)
	}

	// Fresh B completes: exactly one row affected.
	fresh, err := db.Exec(complete, id, "worker-B", freshGen, "SUCCESS")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := fresh.RowsAffected(); n != 1 {
		t.Fatalf("fresh Complete affected %d rows, want 1", n)
	}
}
