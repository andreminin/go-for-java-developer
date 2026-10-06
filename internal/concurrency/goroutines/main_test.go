package main

import "testing"

func TestSumParallel(t *testing.T) {
	cases := map[int]int{0: 0, 1: 1, 10: 55, 100: 5050}
	for n, want := range cases {
		if got := SumParallel(n); got != want {
			t.Errorf("SumParallel(%d) = %d, want %d", n, got, want)
		}
	}
}
