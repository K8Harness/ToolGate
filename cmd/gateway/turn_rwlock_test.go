package main

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestTurnRWLockConcurrentReadLocksShareReaderCount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newSessionLockerTestClient(t)
	rwlock := NewTurnRWLock(client, 5*time.Second, 200*time.Millisecond)
	turnID := uniqueSessionLockerID("turn-rw-readers")

	errCh := make(chan error, 2)
	start := make(chan struct{})

	for range 2 {
		go func() {
			<-start
			errCh <- rwlock.ReadLock(ctx, turnID)
		}()
	}

	close(start)

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("ReadLock() error = %v, want nil", err)
		}
	}

	assertRedisIntValue(t, client, turnReadersKey(turnID), 2)

	if err := rwlock.ReadUnlock(ctx, turnID); err != nil {
		t.Fatalf("ReadUnlock() first error = %v, want nil", err)
	}
	if err := rwlock.ReadUnlock(ctx, turnID); err != nil {
		t.Fatalf("ReadUnlock() second error = %v, want nil", err)
	}
}

func TestTurnRWLockWriteLockWaitsForReadersToDrain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newSessionLockerTestClient(t)
	rwlock := NewTurnRWLock(client, 5*time.Second, 300*time.Millisecond)
	turnID := uniqueSessionLockerID("turn-rw-wait")

	if err := rwlock.ReadLock(ctx, turnID); err != nil {
		t.Fatalf("ReadLock() error = %v, want nil", err)
	}

	type writeResult struct {
		token string
		err   error
	}

	resultCh := make(chan writeResult, 1)
	go func() {
		token, err := rwlock.WriteLock(ctx, turnID)
		resultCh <- writeResult{token: token, err: err}
	}()

	select {
	case result := <-resultCh:
		t.Fatalf("WriteLock() completed early with token=%q err=%v, want blocked while reader is held", result.token, result.err)
	case <-time.After(75 * time.Millisecond):
	}

	if err := rwlock.ReadUnlock(ctx, turnID); err != nil {
		t.Fatalf("ReadUnlock() error = %v, want nil", err)
	}

	result := <-resultCh
	if result.err != nil {
		t.Fatalf("WriteLock() error = %v, want nil", result.err)
	}
	if result.token == "" {
		t.Fatal("WriteLock() token = empty, want non-empty owner token")
	}

	assertRedisIntValue(t, client, turnReadersKey(turnID), 0)

	if err := rwlock.WriteUnlock(ctx, turnID, result.token); err != nil {
		t.Fatalf("WriteUnlock() error = %v, want nil", err)
	}
}

func TestTurnRWLockConcurrentWriteLocksSerialize(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newSessionLockerTestClient(t)
	rwlock := NewTurnRWLock(client, 5*time.Second, 400*time.Millisecond)
	turnID := uniqueSessionLockerID("turn-rw-writers")

	firstToken, err := rwlock.WriteLock(ctx, turnID)
	if err != nil {
		t.Fatalf("WriteLock() first error = %v, want nil", err)
	}
	if firstToken == "" {
		t.Fatal("WriteLock() first token = empty, want non-empty owner token")
	}

	type writeResult struct {
		token string
		err   error
	}

	resultCh := make(chan writeResult, 1)
	go func() {
		token, err := rwlock.WriteLock(ctx, turnID)
		resultCh <- writeResult{token: token, err: err}
	}()

	select {
	case result := <-resultCh:
		t.Fatalf("second WriteLock() completed early with token=%q err=%v, want blocked while first writer holds lock", result.token, result.err)
	case <-time.After(75 * time.Millisecond):
	}

	if err := rwlock.WriteUnlock(ctx, turnID, firstToken); err != nil {
		t.Fatalf("WriteUnlock() first error = %v, want nil", err)
	}

	result := <-resultCh
	if result.err != nil {
		t.Fatalf("second WriteLock() error = %v, want nil", result.err)
	}
	if result.token == "" {
		t.Fatal("second WriteLock() token = empty, want non-empty owner token")
	}
	if result.token == firstToken {
		t.Fatal("second WriteLock() token reused first token, want distinct owner tokens")
	}

	if err := rwlock.WriteUnlock(ctx, turnID, result.token); err != nil {
		t.Fatalf("WriteUnlock() second error = %v, want nil", err)
	}
}

func TestTurnRWLockWriteUnlockWrongTokenKeepsWriterLock(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newSessionLockerTestClient(t)
	rwlock := NewTurnRWLock(client, 5*time.Second, 200*time.Millisecond)
	turnID := uniqueSessionLockerID("turn-rw-token")

	token, err := rwlock.WriteLock(ctx, turnID)
	if err != nil {
		t.Fatalf("WriteLock() error = %v, want nil", err)
	}

	if err := rwlock.WriteUnlock(ctx, turnID, "wrong-token"); err != nil {
		t.Fatalf("WriteUnlock() wrong token error = %v, want nil", err)
	}

	assertRedisStringValue(t, client, turnWriteLockKey(turnID), token)

	if err := rwlock.WriteUnlock(ctx, turnID, token); err != nil {
		t.Fatalf("WriteUnlock() correct token error = %v, want nil", err)
	}

	assertRedisKeyAbsent(t, client, turnWriteLockKey(turnID))
}

func assertRedisStringValue(t *testing.T, client *redis.Client, key, want string) {
	t.Helper()

	got, err := client.Get(context.Background(), key).Result()
	if err != nil {
		t.Fatalf("GET %q error = %v, want nil", key, err)
	}
	if got != want {
		t.Fatalf("GET %q = %q, want %q", key, got, want)
	}
}
