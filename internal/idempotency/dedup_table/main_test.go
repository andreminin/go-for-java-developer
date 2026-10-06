package main

import (
	"errors"
	"sync"
	"testing"
)

func TestFirstExecutesThenReplays(t *testing.T) {
	tbl := NewTable()
	calls := 0
	op := func() Response { calls++; return Response{Code: 200, Body: "ok"} }

	resp, outcome, err := tbl.Do("k", "h", op)
	if err != nil || outcome != "executed" || resp.Code != 200 {
		t.Fatalf("first: resp=%v outcome=%s err=%v", resp, outcome, err)
	}
	resp, outcome, err = tbl.Do("k", "h", op)
	if err != nil || outcome != "replayed" || resp.Body != "ok" {
		t.Fatalf("retry: resp=%v outcome=%s err=%v", resp, outcome, err)
	}
	if calls != 1 {
		t.Fatalf("op executed %d times, want exactly 1", calls)
	}
}

func TestKeyReuseWithDifferentHashConflicts(t *testing.T) {
	tbl := NewTable()
	op := func() Response { return Response{Code: 200} }
	if _, _, err := tbl.Do("k", "h1", op); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tbl.Do("k", "h2", op); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestConcurrentSameKeyExecutesOnce(t *testing.T) {
	tbl := NewTable()
	calls := 0
	var mu sync.Mutex
	op := func() Response {
		mu.Lock()
		calls++
		mu.Unlock()
		return Response{Code: 200, Body: "ok"}
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = tbl.Do("k", "h", op)
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("op executed %d times under concurrency, want 1", calls)
	}
}
