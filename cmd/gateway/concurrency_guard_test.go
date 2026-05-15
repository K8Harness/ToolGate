package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/K8Harness/ToolGate/core/mcp"
)

func TestConcurrencyGuardBypassesLocksWhenToolNameEmpty(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	locker, rwlock, guard := newTestConcurrencyGuard(t)
	sessionID := uniqueSessionLockerID("session-guard-bypass")

	if err := locker.Acquire(ctx, sessionID, "held-turn"); err != nil {
		t.Fatalf("Acquire() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		_ = locker.Release(context.Background(), sessionID, "held-turn")
	})

	if err := rwlock.ReadLock(ctx, "blocked-turn"); err != nil {
		t.Fatalf("ReadLock() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		_ = rwlock.ReadUnlock(context.Background(), "blocked-turn")
	})

	called := false
	resp, err := guard.Execute(ctx, sessionID, "blocked-turn", "", func() (*mcp.JSONRPCResponse, error) {
		called = true
		return newGuardTestResponse(), nil
	})
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil", err)
	}
	if !called {
		t.Fatal("Execute() did not call fn for empty toolName")
	}
	if resp == nil {
		t.Fatal("Execute() response = nil, want response")
	}
}

func TestConcurrencyGuardSameSessionSameTurnReadCallsRunConcurrently(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, _, guard := newTestConcurrencyGuard(t)
	sessionID := uniqueSessionLockerID("session-guard-read")
	turnID := uniqueSessionLockerID("turn-guard-read")

	release := make(chan struct{})
	bothInFlight := make(chan struct{})
	var inFlight atomic.Int32
	var once sync.Once
	errCh := make(chan error, 2)

	for range 2 {
		go func() {
			_, err := guard.Execute(ctx, sessionID, turnID, "get_balance", func() (*mcp.JSONRPCResponse, error) {
				if inFlight.Add(1) == 2 {
					once.Do(func() { close(bothInFlight) })
				}
				defer inFlight.Add(-1)
				<-release
				return newGuardTestResponse(), nil
			})
			errCh <- err
		}()
	}

	select {
	case <-bothInFlight:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("read Execute() calls did not overlap within the same turn")
	}

	close(release)

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("Execute() error = %v, want nil", err)
		}
	}
}

func TestConcurrencyGuardSameSessionDifferentTurnsSerialize(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, _, guard := newTestConcurrencyGuard(t)
	sessionID := uniqueSessionLockerID("session-guard-serialize")
	firstTurnID := uniqueSessionLockerID("turn-guard-first")
	secondTurnID := uniqueSessionLockerID("turn-guard-second")

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	secondStarted := make(chan struct{})
	errCh := make(chan error, 2)

	go func() {
		_, err := guard.Execute(ctx, sessionID, firstTurnID, "get_balance", func() (*mcp.JSONRPCResponse, error) {
			close(firstStarted)
			<-firstRelease
			return newGuardTestResponse(), nil
		})
		errCh <- err
	}()

	select {
	case <-firstStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first Execute() did not start")
	}

	go func() {
		_, err := guard.Execute(ctx, sessionID, secondTurnID, "get_balance", func() (*mcp.JSONRPCResponse, error) {
			close(secondStarted)
			return newGuardTestResponse(), nil
		})
		errCh <- err
	}()

	select {
	case <-secondStarted:
		t.Fatal("second Execute() started before first completed")
	case <-time.After(125 * time.Millisecond):
	}

	close(firstRelease)

	select {
	case <-secondStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second Execute() did not start after first completed")
	}

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("Execute() error = %v, want nil", err)
		}
	}
}

func TestConcurrencyGuardSameSessionDifferentTurnsTimeout(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, _, guard := newTestConcurrencyGuardWithTimeouts(t, 5*time.Second, 120*time.Millisecond)
	sessionID := uniqueSessionLockerID("session-guard-timeout")
	firstTurnID := uniqueSessionLockerID("turn-guard-timeout-first")
	secondTurnID := uniqueSessionLockerID("turn-guard-timeout-second")

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	secondCalled := atomic.Bool{}
	errCh := make(chan error, 2)

	go func() {
		_, err := guard.Execute(ctx, sessionID, firstTurnID, "get_balance", func() (*mcp.JSONRPCResponse, error) {
			close(firstStarted)
			<-firstRelease
			return newGuardTestResponse(), nil
		})
		errCh <- err
	}()

	select {
	case <-firstStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first Execute() did not start")
	}

	start := time.Now()
	go func() {
		_, err := guard.Execute(ctx, sessionID, secondTurnID, "get_balance", func() (*mcp.JSONRPCResponse, error) {
			secondCalled.Store(true)
			return newGuardTestResponse(), nil
		})
		errCh <- err
	}()

	secondErr := <-errCh
	elapsed := time.Since(start)
	close(firstRelease)

	if secondCalled.Load() {
		t.Fatal("second Execute() called fn despite same-session timeout")
	}

	var timeoutErr *LockTimeoutError
	if !errors.As(secondErr, &timeoutErr) {
		t.Fatalf("second Execute() error = %T, want *LockTimeoutError", secondErr)
	}
	if elapsed < 120*time.Millisecond {
		t.Fatalf("second Execute() elapsed = %v, want >= %v", elapsed, 120*time.Millisecond)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("first Execute() error = %v, want nil", err)
	}
}

func TestConcurrencyGuardDifferentSessionsDoNotBlockAndUseIndependentRedisKeys(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	locker, _, guard := newTestConcurrencyGuard(t)
	client := locker.rdb
	sessionA := uniqueSessionLockerID("session-guard-isolated-a")
	sessionB := uniqueSessionLockerID("session-guard-isolated-b")
	turnA := uniqueSessionLockerID("turn-guard-isolated-a")
	turnB := uniqueSessionLockerID("turn-guard-isolated-b")

	release := make(chan struct{})
	bothInFlight := make(chan struct{})
	var inFlight atomic.Int32
	var once sync.Once
	errCh := make(chan error, 2)

	run := func(sessionID, turnID string) {
		_, err := guard.Execute(ctx, sessionID, turnID, "get_balance", func() (*mcp.JSONRPCResponse, error) {
			if inFlight.Add(1) == 2 {
				assertRedisStringValue(t, client, sessionLockKey(sessionA), turnA)
				assertRedisStringValue(t, client, sessionLockKey(sessionB), turnB)
				once.Do(func() { close(bothInFlight) })
			}
			defer inFlight.Add(-1)
			<-release
			return newGuardTestResponse(), nil
		})
		errCh <- err
	}

	go run(sessionA, turnA)
	go run(sessionB, turnB)

	select {
	case <-bothInFlight:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Execute() calls for different sessions did not overlap")
	}

	if sessionLockKey(sessionA) == sessionLockKey(sessionB) {
		t.Fatal("session lock keys share a namespace, want unique session-scoped keys")
	}

	close(release)

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("Execute() error = %v, want nil", err)
		}
	}
}

func TestConcurrencyGuardBudgetCounterStaysAccurateUnderConcurrentReads(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, _, guard := newTestConcurrencyGuard(t)
	sessionID := uniqueSessionLockerID("session-guard-budget")
	turnID := uniqueSessionLockerID("turn-guard-budget")

	release := make(chan struct{})
	bothInFlight := make(chan struct{})
	var started atomic.Int32
	var budgetCounter atomic.Int32
	var once sync.Once
	errCh := make(chan error, 2)

	for range 2 {
		go func() {
			_, err := guard.Execute(ctx, sessionID, turnID, "get_balance", func() (*mcp.JSONRPCResponse, error) {
				budgetCounter.Add(1)
				if started.Add(1) == 2 {
					once.Do(func() { close(bothInFlight) })
				}
				<-release
				return newGuardTestResponse(), nil
			})
			errCh <- err
		}()
	}

	select {
	case <-bothInFlight:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("concurrent read Execute() calls did not overlap")
	}

	close(release)

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("Execute() error = %v, want nil", err)
		}
	}

	if got := budgetCounter.Load(); got != 2 {
		t.Fatalf("budget counter = %d, want 2", got)
	}
}

func TestConcurrencyGuardPanicDoesNotLeakLocks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, _, guard := newTestConcurrencyGuard(t)
	sessionID := uniqueSessionLockerID("session-guard-panic")
	turnID := uniqueSessionLockerID("turn-guard-panic")

	func() {
		defer func() {
			_ = recover()
		}()

		_, _ = guard.Execute(ctx, sessionID, turnID, "create_refund", func() (*mcp.JSONRPCResponse, error) {
			panic("boom")
		})
	}()

	done := make(chan error, 1)
	go func() {
		_, err := guard.Execute(ctx, sessionID, turnID, "create_refund", func() (*mcp.JSONRPCResponse, error) {
			return newGuardTestResponse(), nil
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Execute() after panic error = %v, want nil", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Execute() after panic blocked, want released locks")
	}
}

func newTestConcurrencyGuard(t *testing.T) (*SessionLocker, *TurnRWLock, *ConcurrencyGuard) {
	t.Helper()

	client := newSessionLockerTestClient(t)
	locker := NewSessionLocker(client, 5*time.Second, 250*time.Millisecond)
	rwlock := NewTurnRWLock(client, 5*time.Second, 250*time.Millisecond)
	classifier := NewOperationClassifier(nil)

	return locker, rwlock, NewConcurrencyGuard(locker, rwlock, classifier)
}

func newTestConcurrencyGuardWithTimeouts(
	t *testing.T,
	lockTTL time.Duration,
	acquireTimeout time.Duration,
) (*SessionLocker, *TurnRWLock, *ConcurrencyGuard) {
	t.Helper()

	client := newSessionLockerTestClient(t)
	locker := NewSessionLocker(client, lockTTL, acquireTimeout)
	rwlock := NewTurnRWLock(client, lockTTL, acquireTimeout)
	classifier := NewOperationClassifier(nil)

	return locker, rwlock, NewConcurrencyGuard(locker, rwlock, classifier)
}

func newGuardTestResponse() *mcp.JSONRPCResponse {
	return &mcp.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Result:  json.RawMessage(`{"ok":true}`),
	}
}
