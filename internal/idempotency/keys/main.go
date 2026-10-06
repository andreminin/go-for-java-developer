// This example demonstrates idempotency key types and their scope.
//
// Why: in distributed systems a client may resend a request after a timeout
// or dropped connection. Without idempotency, a retry creates a duplicate
// charge/order/booking. The key lets the server recognize "same operation".
//
// Key types (in order of preference):
//  1. Explicit header key: Idempotency-Key: <uuid> — client-generated per
//     operation. Best: unambiguous, survives retries verbatim.
//  2. Natural object ID: invoice_id, order_id — domain-unique already.
//  3. Request body hash: fallback when the client sends no key; fragile —
//     any semantically-irrelevant byte change (whitespace, key order) alters
//     the hash, and equal hashes do not prove equal intent.
//
// Scope: a key is unique per (endpoint + customer/tenant), never globally,
// unless the operation truly is global. Retention: 24–72h for payments
// (chargebacks arrive late; storage is cheap; replays after expiry re-execute).
//
// In Java the same taxonomy applies (Spring Retry / custom filters); only the
// plumbing differs. Libraries in Go: idempotent, once, idem.
//
// Run: go run ./internal/idempotency/keys
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Key kinds, strongest first.
type KeyKind int

const (
	HeaderKey   KeyKind = iota // Client-supplied Idempotency-Key header (preferred).
	ObjectIDKey                // Natural domain ID: invoice_id, order_id.
	BodyHashKey                // Fallback: hash of the request body (fragile).
)

// ScopedKey namespaces a raw key by endpoint + tenant so that the same UUID
// reused against two endpoints (or two customers) cannot collide.
// In Java: the same namespacing inside a Spring idempotency filter.
func ScopedKey(endpoint, tenant, raw string) string {
	return strings.Join([]string{endpoint, tenant, raw}, "|")
}

// BodyHash is the LAST-RESORT key: sha256 of the canonical body bytes.
// Why fragile: formatting-only changes produce a different key (missed dedup),
// and it cannot distinguish "retry of op A" from "new op with same bytes".
func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return "bodyhash:" + hex.EncodeToString(sum[:])
}

func main() {
	fmt.Println("== 1. explicit header key (preferred) ==")
	// Client: POST /payments with Idempotency-Key: 7d0f-...
	// Retry sends the SAME header value; server dedups on it.
	fmt.Println("scoped:", ScopedKey("POST /payments", "tenant-acme", "7d0f-c90a-uuid"))

	fmt.Println("== 2. natural object ID ==")
	// Client: POST /invoices with {"invoice_id": "inv-2024-001", ...}.
	// The domain ID already uniquely identifies the operation.
	fmt.Println("scoped:", ScopedKey("POST /invoices", "tenant-acme", "inv-2024-001"))

	fmt.Println("== 3. body hash fallback (fragile) ==")
	h1 := BodyHash([]byte(`{"amount":100,"currency":"USD"}`))
	h2 := BodyHash([]byte(`{"amount":100, "currency":"USD"}`)) // Same intent, extra space.
	fmt.Println("hash without space:", h1)
	fmt.Println("hash with space:   ", h2)
	fmt.Println("equal:", h1 == h2, "(a single space defeats the dedup — prefer explicit keys)")

	fmt.Println()
	fmt.Println("Scope rule: key is unique per endpoint + customer/tenant.")
	fmt.Println("Retention rule: keep keys 24-72h for payments, then expire.")
}
