-- Commitix-style intent table. Apply after 001_init.sql:
--   psql $DATABASE_URL -f testdata/migrations/002_init.sql
-- Requires: PostgreSQL 14+.
--
-- Increment rules (these are the whole point):
--   lease_generation  +1 ONLY on claim (new ownership epoch) and cancel.
--                     Recovery must NOT touch it, so a stale worker's fenced
--                     write is rejected after takeover.
--   attempt_count     +1 ONLY on claim. Recovery must NOT touch it, so a
--                     flaky recovery loop cannot exhaust a retry budget.
--   version           +1 on EVERY row update (optimistic guard).

CREATE TABLE IF NOT EXISTS commitix_intents (
                                                id               BIGSERIAL PRIMARY KEY,
                                                status           TEXT NOT NULL DEFAULT 'READY'
                                                CHECK (status IN ('READY','RUNNING','SUCCESS','FAILED','CANCELLED')),
    payload          JSONB NOT NULL,

    -- Ownership. Populated only while status = 'RUNNING'.
    worker_id        TEXT,
    lease_until      TIMESTAMPTZ,

    -- Fencing token. Changes only on ownership changes, never on recovery.
    lease_generation BIGINT NOT NULL DEFAULT 0,

    -- Retry accounting. Independent of recovery.
    attempt_count    INT NOT NULL DEFAULT 0,

    -- Optimistic concurrency guard.
    version          BIGINT NOT NULL DEFAULT 0,

    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_modified_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- A RUNNING row must carry an owner and an expiry; a non-RUNNING row
    -- must not. This makes an inconsistent recovery state unrepresentable.
    CONSTRAINT commitix_intents_lease_consistent CHECK (
(status =  'RUNNING' AND worker_id IS NOT NULL AND lease_until IS NOT NULL)
    OR
(status <> 'RUNNING' AND worker_id IS NULL     AND lease_until IS NULL)
    )
    );

-- Recovery scan: find RUNNING rows whose lease has expired.
-- Partial index keeps it small; the predicate matches QRecover.
CREATE INDEX IF NOT EXISTS commitix_intents_recovery_idx
    ON commitix_intents (lease_until)
    WHERE status = 'RUNNING';

-- Claim scan: find the lowest READY intent in FIFO order.
-- Partial index keeps it small; the predicate matches QClaim.
CREATE INDEX IF NOT EXISTS commitix_intents_ready_idx
    ON commitix_intents (id)
    WHERE status = 'READY';

-- Optional observability: find stale-owner takeovers for alerting.
CREATE INDEX IF NOT EXISTS commitix_intents_generation_idx
    ON commitix_intents (id, lease_generation);