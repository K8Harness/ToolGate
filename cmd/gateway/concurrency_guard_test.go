package main

import (
	"context"
	"encoding/json"
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

func newGuardTestResponse() *mcp.JSONRPCResponse {
	return &mcp.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Result:  json.RawMessage(`{"ok":true}`),
	}
}
