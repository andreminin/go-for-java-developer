package main

import (
	"context"
	"errors"
	"testing"
)

func TestWithRetrySucceedsAfterSerializationFailures(t *testing.T) {
	calls := 0
	err := WithRetry(context.Background(), 5, func(int) error {
		calls++
		if calls < 3 {
			return errors.New("could not serialize access (40001)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestWithRetryDoesNotRetryPermanentErrors(t *testing.T) {
	calls := 0
	perm := errors.New("duplicate key value violates unique constraint")
	err := WithRetry(context.Background(), 5, func(int) error { calls++; return perm })
	if !errors.Is(err, perm) {
		t.Fatalf("err = %v, want %v", err, perm)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry)", calls)
	}
}

func TestWithRetryGivesUpAfterMaxAttempts(t *testing.T) {
	err := WithRetry(context.Background(), 3, func(int) error {
		return errors.New("could not serialize access")
	})
	if err == nil {
		t.Fatal("err = nil, want exhaustion error")
	}
}

func TestIsSerializationFailure(t *testing.T) {
	if !isSerializationFailure(errors.New("ERROR: could not serialize access (40001)")) {
		t.Error("expected true for SSI message")
	}
	if isSerializationFailure(errors.New("duplicate key")) {
		t.Error("expected false for constraint violation")
	}
	if isSerializationFailure(nil) {
		t.Error("expected false for nil")
	}
}
