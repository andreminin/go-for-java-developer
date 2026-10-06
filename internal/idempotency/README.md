# Idempotency in Go: Keys, Deduplication, Middleware

## Why Idempotency Matters

In distributed systems, a client may resend a request due to a timeout or a
dropped connection. Without idempotency, a retry creates a duplicate charge,
order, or booking. Idempotency guarantees that repeating an operation has the
same effect as performing it once (`f(f(x)) == f(x)`).

## Key Concepts

- **Idempotency key** — a client-generated unique token (usually a UUID in the
  `Idempotency-Key` header) identifying one logical operation.
- **Key scope** — per endpoint + customer/tenant (never global unless the
  operation truly is). Same UUID against two endpoints must not collide.
- **Natural object ID** — `invoice_id` / `order_id` as the key when the domain
  already provides uniqueness; no extra header needed.
- **Body hash fallback** — `sha256` of the request body when no key is sent;
  fragile (whitespace changes the hash) and cannot prove equal intent.
- **Deduplication table** — `idempotency_keys(key PK, request_hash, response_code,
  response_body, created_at)` with atomic `INSERT ... ON CONFLICT DO NOTHING`,
  stored-response replay, and a 24–72h retention window.
- **HTTP middleware** — extracts the key, checks before the handler, saves the
  response after, and short-circuits redeliveries so the handler runs once.

## Java vs Go: Comparison Table

| Java | Go | Comment |
|------|-----|---------|
| Spring Retry / Resilience4j retry | Client resends same `Idempotency-Key` | Retry policy lives on the client; the server dedups — both sides needed |
| `OncePerRequestFilter` / interceptor | `net/http` middleware (`func(http.Handler) http.Handler`) | Same shape: wrap chain, pre-check, post-save; no framework required |
| JPA `@Id` on dedup entity + `save` catching constraint violation | `INSERT ... ON CONFLICT DO NOTHING` + affected-rows check | The atomic check must be ONE statement; read-then-write races |
| `ResponseEntity` caching wrapper | `httptest.ResponseRecorder`-style capture | Capture status + body to persist for replay |
| Manual `ConcurrentHashMap` dedup | Same in-memory map for demos only | Process-local dedup dies with the instance — PostgreSQL table is the real store |

## Code Examples

### Example 1: Idempotency Keys (`keys/`)

```go
// Preferred: client sends Idempotency-Key: <uuid> per operation.
key := ScopedKey("POST /payments", "tenant-acme", "7d0f-c90a-uuid")
```

What happens: header keys and natural IDs are namespaced by endpoint + tenant;
the body-hash fallback is shown to break on a single space.
How it looks in Java: same taxonomy inside a Spring filter or API-gateway policy.
Pitfalls: global (unscoped) keys colliding across endpoints; reusing one key for two
different operations; trusting body hashes for money movements.

### Example 2: Deduplication Table (`dedup_table/`)

```go
// 1. INSERT key ON CONFLICT DO NOTHING. 2. Inserted => execute + UPDATE row.
// 3. Existed + same request_hash => replay stored response.
// 4. Existed + different hash => 409 Conflict (client bug).
resp, outcome, err := tbl.Do(key, reqHash, op)
```

What happens: `op` runs exactly once per key even under a 20-goroutine delivery
storm; retries replay; key reuse with a changed payload returns `ErrConflict`.
How it looks in Java: same SQL (`ON CONFLICT DO NOTHING`) in a `@Transactional`
repository method; conflict branch returns the stored response entity.
Pitfalls: read-then-insert in two statements (concurrent duplicates both execute);
storing the response in a separate transaction from execution (crash => executed
but unreplayable); no retention cleanup (unbounded table growth).

### Example 3: HTTP Middleware (`middleware/`)

```go
// Missing key on POST => 400 (fail fast beats silent duplicates).
// Known key => replay stored {code, body} with Idempotent-Replayed: true.
// Unknown key => run handler, capture response, persist, return.
srv := httptest.NewServer(store.Middleware(handler))
```

What happens: first POST executes (handler count 1); retry returns byte-identical
response with `Idempotent-Replayed: true`; keyless POST gets 400.
How it looks in Java: `OncePerRequestFilter` doing the same header check + JPA lookup.
Pitfalls: applying middleware to safe methods (`GET` needs no keys); replaying without
preserving the original status code; caching streaming/SSE responses.

## Algorithm

1. Client sends a request with `Idempotency-Key`.
2. Server attempts to insert the key into the deduplication table
   (`INSERT ... ON CONFLICT DO NOTHING`).
3. If insert succeeds — execute the operation, save request hash + response.
4. If key exists with the same request hash — return the stored response.
5. If key exists with a different hash — return 409 Conflict.

## Common Mistakes and How to Avoid Them

- **Read-then-write instead of atomic insert** — two concurrent first-deliveries both
  see "key absent" and both execute. Fix: single `INSERT ... ON CONFLICT DO NOTHING`.
- **Unscoped keys** — one UUID namespace for all endpoints/tenants; a payment key
  collides with a refund key. Fix: `endpoint + tenant + raw` composite key.
- **Key reuse across different payloads** — client bug that must surface as 409, not
  silent replay of a stale response. Fix: compare `request_hash` on conflict.
- **Response saved in a different transaction** — crash between execute and save loses
  the replay record; the retry then executes twice. Fix: same-transaction save.
- **In-memory-only dedup in production** — works on one instance, duplicates behind a
  load balancer or after restart. Fix: PostgreSQL table as the source of truth.
- **No retention window** — table grows forever; or keys expire too fast and late
  retries (chargebacks, webhook redelivery) re-execute. Fix: 24–72h for payments.

## Exercises

1. Back `dedup_table` with real PostgreSQL: implement `Do` with `BEGIN; INSERT ...
   ON CONFLICT DO NOTHING RETURNING; <execute>; UPDATE ...; COMMIT` and prove the
   concurrent test still passes with two processes, not just two goroutines.
2. Add a `GET /status?key=` debug endpoint to the middleware that returns whether a
   key is `executing`, `completed`, or `unknown` — then define the client behavior
   for each state during a timeout.
3. Implement the retention cleanup (`DELETE WHERE created_at < now() - INTERVAL
   '72 hours'`) as a `pg_cron` job plus a Go fallback ticker; argue which one owns
   the schedule.
4. Extend the middleware to hash and compare request bodies on conflict (returning 409
   on mismatch, like `dedup_table`), and decide the status code for in-flight keys
   (`409` vs `425 Too Early`).

## Additional Resources

- Stripe / Adyen idempotency-key documentation: the industry reference for header
  semantics, scope, and retention windows.
- Go libraries: `idempotent`, `once`, `idem` — compare their storage backends and
  conflict policies before adopting one.
- PostgreSQL docs: `INSERT ... ON CONFLICT` semantics and index behavior backing
  the atomic check.
