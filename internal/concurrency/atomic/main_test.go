package main

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestAtomicCounter(t *testing.T) {
	var n atomic.Int64
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				n.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := n.Load(); got != 1000 {
		t.Fatalf("got %d, want 1000", got)
	}
}

func TestCompareAndSwapOnce(t *testing.T) {
	var flag atomic.Bool
	winners := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if flag.CompareAndSwap(false, true) {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}
