-- Migrations for the postgres / outbox / inbox / idempotency examples.
-- Apply with: psql $DATABASE_URL -f testdata/migrations/001_init.sql
-- Requires: PostgreSQL 14+.

CREATE TABLE IF NOT EXISTS accounts (
    id      BIGSERIAL PRIMARY KEY,
    owner   TEXT NOT NULL,
    balance BIGINT NOT NULL DEFAULT 0 CHECK (balance >= 0)
);

-- Transactional outbox: written atomically with business data.
CREATE TABLE IF NOT EXISTS outbox (
    id         BIGSERIAL PRIMARY KEY,
    aggregate  TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    type       TEXT NOT NULL,
    payload    JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS outbox_unpublished_idx
    ON outbox (id) WHERE published_at IS NULL;

-- Transactional inbox: consumer-side deduplication.
CREATE TABLE IF NOT EXISTS inbox (
    event_id     TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    payload      JSONB NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ
);

-- Idempotency deduplication table shared by Module 3 examples.
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key           TEXT PRIMARY KEY,
    request_hash  TEXT NOT NULL,
    response_code INT NOT NULL,
    response_body JSONB,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Retention cleanup, e.g. via cron/pg_cron:
-- DELETE FROM idempotency_keys WHERE created_at < now() - INTERVAL '72 hours';
-- DELETE FROM outbox WHERE published_at IS NOT NULL AND published_at < now() - INTERVAL '7 days';
