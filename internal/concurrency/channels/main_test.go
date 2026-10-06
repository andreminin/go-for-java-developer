package main

import (
	"testing"
	"time"
)

func TestRangeUntilClose(t *testing.T) {
	nums := make(chan int)
	go func() {
		defer close(nums)
		for i := 1; i <= 3; i++ {
			nums <- i
		}
	}()
	sum := 0
	for v := range nums {
		sum += v
	}
	if sum != 6 {
		t.Fatalf("sum = %d, want 6", sum)
	}
}

func TestSelectTimeoutFires(t *testing.T) {
	never := make(chan string)
	select {
	case <-never:
		t.Fatal("should not receive from a channel nobody sends to")
	case <-time.After(10 * time.Millisecond):
		// Expected path: timeout wins.
	}
}

func TestDoneBroadcastStopsWorkers(t *testing.T) {
	done := make(chan struct{})
	finished := make(chan int, 2)
	for id := 1; id <= 2; id++ {
		go func(id int) {
			<-done
			finished <- id
		}(id)
	}
	close(done)
	seen := map[int]bool{}
	timeout := time.After(time.Second)
	for len(seen) < 2 {
		select {
		case id := <-finished:
			seen[id] = true
		case <-timeout:
			t.Fatal("workers did not observe close(done)")
		}
	}
}
