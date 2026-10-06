package main

import (
	"context"
	"errors"
	"sort"
	"testing"
)

func TestWorkerPool(t *testing.T) {
	got := WorkerPool([]int{1, 2, 3, 4}, 2, func(v int) int { return v * 2 })
	sort.Ints(got)
	want := []int{2, 4, 6, 8}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestFanOutFanIn(t *testing.T) {
	got := FanOutFanIn([]int{1, 2, 3}, 2)
	sort.Ints(got)
	want := []int{1, 4, 9}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestPipelineKeepsEvenSquares(t *testing.T) {
	got := Pipeline(context.Background(), []int{1, 2, 3, 4})
	sort.Ints(got)
	want := []int{4, 16} // squares: 1,4,9,16 -> evens: 4,16
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestRunAllPropagatesFirstError(t *testing.T) {
	boom := errors.New("boom")
	err := RunAll(context.Background(), []func(context.Context) error{
		func(context.Context) error { return nil },
		func(context.Context) error { return boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if err := RunAll(context.Background(), []func(context.Context) error{
		func(context.Context) error { return nil },
	}); err != nil {
		t.Fatalf("all-ok RunAll err = %v, want nil", err)
	}
}
