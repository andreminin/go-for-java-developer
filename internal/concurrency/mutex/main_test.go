package main

import (
	"sync"
	"testing"
)

func TestSafeCounterConcurrent(t *testing.T) {
	c := NewSafeCounter()
	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				c.Inc("k")
			}
		}()
	}
	wg.Wait()
	if got := c.Get("k"); got != 20*500 {
		t.Fatalf("got %d, want %d", got, 20*500)
	}
}

func TestCacheReadWrite(t *testing.T) {
	c := NewCache()
	c.Set("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) = %v,%v want 1,true", v, ok)
	}
	c.Set("a", 2)
	if v, _ := c.Get("a"); v != 2 {
		t.Fatalf("after Set, Get(a) = %d want 2", v)
	}
}
