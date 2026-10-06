package main

import (
	"sync"
	"testing"
)

func TestDuplicateDeliveryAppliesOnce(t *testing.T) {
	c := NewConsumer(1000)
	if applied, _ := c.Handle("e1", 100); !applied {
		t.Fatal("first delivery should apply")
	}
	if applied, skipped := c.Handle("e1", 100); applied || !skipped {
		t.Fatalf("redelivery: applied=%v skipped=%v, want false,true", applied, skipped)
	}
	if got := c.Balance(); got != 1100 {
		t.Fatalf("balance = %d, want 1100", got)
	}
}

func TestConcurrentRedeliveryAppliesOnce(t *testing.T) {
	c := NewConsumer(0)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Handle("same-event", 5) }()
	}
	wg.Wait()
	if got := c.Balance(); got != 5 {
		t.Fatalf("balance = %d, want 5 (exactly-once effect)", got)
	}
}
