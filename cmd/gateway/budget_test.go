package main

import (
	"sync"
	"testing"
)

func TestBudgetTrackerIncrementAndGetSequential(t *testing.T) {
	tracker := NewBudgetTracker()

	for i := 1; i <= 3; i++ {
		if got := tracker.IncrementAndGet("session-1", "turn-1"); got != i {
			t.Fatalf("IncrementAndGet() call %d = %d, want %d", i, got, i)
		}
	}
}

func TestBudgetTrackerIncrementAndGetIsolatedBySessionAndTurn(t *testing.T) {
	tracker := NewBudgetTracker()

	if got := tracker.IncrementAndGet("session-1", "turn-1"); got != 1 {
		t.Fatalf("IncrementAndGet(session-1, turn-1) = %d, want 1", got)
	}
	if got := tracker.IncrementAndGet("session-1", "turn-2"); got != 1 {
		t.Fatalf("IncrementAndGet(session-1, turn-2) = %d, want 1", got)
	}
	if got := tracker.IncrementAndGet("session-2", "turn-1"); got != 1 {
		t.Fatalf("IncrementAndGet(session-2, turn-1) = %d, want 1", got)
	}
	if got := tracker.IncrementAndGet("session-1", "turn-1"); got != 2 {
		t.Fatalf("IncrementAndGet(session-1, turn-1) second call = %d, want 2", got)
	}
}

func TestBudgetTrackerIncrementAndGetConcurrent(t *testing.T) {
	tracker := NewBudgetTracker()

	const goroutines = 32
	const incrementsPerGoroutine = 16

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range incrementsPerGoroutine {
				tracker.IncrementAndGet("session-concurrent", "turn-concurrent")
			}
		}()
	}
	wg.Wait()

	want := goroutines * incrementsPerGoroutine
	if got := tracker.IncrementAndGet("session-concurrent", "turn-concurrent"); got != want+1 {
		t.Fatalf("final IncrementAndGet() = %d, want %d", got, want+1)
	}
}
