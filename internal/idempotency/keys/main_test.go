package main

import (
	"strings"
	"testing"
)

func TestScopedKeyNamespaces(t *testing.T) {
	a := ScopedKey("POST /payments", "acme", "k1")
	b := ScopedKey("POST /refunds", "acme", "k1")
	if a == b {
		t.Fatal("same raw key on different endpoints must not collide")
	}
	c := ScopedKey("POST /payments", "other", "k1")
	if a == c {
		t.Fatal("same raw key for different tenants must not collide")
	}
	if !strings.Contains(a, "k1") {
		t.Fatalf("scoped key %q should embed raw key", a)
	}
}

func TestBodyHashIsDeterministicButWhitespaceSensitive(t *testing.T) {
	h1 := BodyHash([]byte(`{"a":1}`))
	h2 := BodyHash([]byte(`{"a":1}`))
	if h1 != h2 {
		t.Fatal("same bytes must hash equally")
	}
	h3 := BodyHash([]byte(`{"a": 1}`))
	if h1 == h3 {
		t.Fatal("whitespace change should alter the fallback hash (documents fragility)")
	}
}
