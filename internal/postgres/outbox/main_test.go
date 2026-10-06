package main

import (
	"errors"
	"testing"
)

func TestSaveIsAtomicWithEvent(t *testing.T) {
	s := NewStore(1000, func(Event) error { return nil })
	s.SaveDebited(100, "p1")
	if got := s.Balance(); got != 900 {
		t.Fatalf("balance = %d, want 900", got)
	}
	if got := s.Unpublished(); got != 1 {
		t.Fatalf("unpublished = %d, want 1", got)
	}
}

func TestRelayPublishesOnceAndSurvivesRestart(t *testing.T) {
	sent := 0
	s := NewStore(1000, func(Event) error { sent++; return nil })
	s.SaveDebited(100, "p1")
	if err := s.Relay(); err != nil {
		t.Fatal(err)
	}
	if err := s.Relay(); err != nil { // Simulated restart: second tick.
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want 1 (no duplicate publish)", sent)
	}
	if got := s.Unpublished(); got != 0 {
		t.Fatalf("unpublished = %d, want 0", got)
	}
}

func TestRelayFailureLeavesEventUnpublished(t *testing.T) {
	boom := errors.New("broker down")
	s := NewStore(1000, func(Event) error { return boom })
	s.SaveDebited(100, "p1")
	if err := s.Relay(); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if got := s.Unpublished(); got != 1 {
		t.Fatalf("unpublished = %d, want 1 (retry later)", got)
	}
}
