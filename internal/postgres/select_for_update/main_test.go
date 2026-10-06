package main

import (
	"sync"
	"testing"
)

func TestLockedWithdrawalsAreExact(t *testing.T) {
	a := &Account{balance: 1000}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.WithdrawLocked(100) }()
	}
	wg.Wait()
	if a.balance != 0 {
		t.Fatalf("balance = %d, want 0", a.balance)
	}
}

func TestPlainWithdrawalWithoutOverlapIsExact(t *testing.T) {
	// Without the rendezvous channels WithdrawPlain is sequential and exact;
	// the test pins the single-threaded behavior while main() shows the race.
	a := &Account{balance: 1000}
	a.WithdrawPlain(100, nil, nil)
	if a.balance != 900 {
		t.Fatalf("balance = %d, want 900", a.balance)
	}
}
