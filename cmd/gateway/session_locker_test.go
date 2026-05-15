package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/K8Harness/ToolGate/core/mcp"
	"github.com/redis/go-redis/v9"
)

// SessionLocker tests require a live Redis instance via TOOLGATE_TEST_REDIS_DSN.
// For local validation against the compose stack, run them where Redis is reachable
// as redis://redis:6379/0 (for example, from a docker compose gateway container).

func TestSessionLockerAcquireReenterAndRelease(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newSessionLockerTestClient(t)
	locker := NewSessionLocker(client, 5*time.Second, 200*time.Millisecond)

	sessionID := uniqueSessionLockerID("session")
	turnID := uniqueSessionLockerID("turn")

	if err := locker.Acquire(ctx, sessionID, turnID); err != nil {
		t.Fatalf("Acquire() error = %v, want nil", err)
	}

	assertRedisIntValue(t, client, sessionLockRefCountKey(sessionID), 1)

	if err := locker.Acquire(ctx, sessionID, turnID); err != nil {
		t.Fatalf("Acquire() re-entry error = %v, want nil", err)
	}

	assertRedisIntValue(t, client, sessionLockRefCountKey(sessionID), 2)

	if err := locker.Release(ctx, sessionID, turnID); err != nil {
		t.Fatalf("Release() first error = %v, want nil", err)
	}

	assertRedisIntValue(t, client, sessionLockRefCountKey(sessionID), 1)

	if err := locker.Release(ctx, sessionID, turnID); err != nil {
		t.Fatalf("Release() second error = %v, want nil", err)
	}

	assertRedisKeyAbsent(t, client, sessionLockKey(sessionID))
	assertRedisKeyAbsent(t, client, sessionLockRefCountKey(sessionID))
}

func TestSessionLockerAcquireTimesOutForDifferentTurn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newSessionLockerTestClient(t)
	locker := NewSessionLocker(client, 5*time.Second, 120*time.Millisecond)

	sessionID := uniqueSessionLockerID("session")
	firstTurnID := uniqueSessionLockerID("turn")
	secondTurnID := uniqueSessionLockerID("turn")

	if err := locker.Acquire(ctx, sessionID, firstTurnID); err != nil {
		t.Fatalf("Acquire() first turn error = %v, want nil", err)
	}

	start := time.Now()
	err := locker.Acquire(ctx, sessionID, secondTurnID)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Acquire() second turn error = nil, want timeout")
	}

	var timeoutErr *LockTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("Acquire() error = %T, want *LockTimeoutError", err)
	}
	if timeoutErr.JSONRPCCode() != mcp.CodeSessionBusy {
		t.Fatalf("JSONRPCCode() = %d, want %d", timeoutErr.JSONRPCCode(), mcp.CodeSessionBusy)
	}
	if elapsed < 120*time.Millisecond {
		t.Fatalf("Acquire() elapsed = %v, want >= %v", elapsed, 120*time.Millisecond)
	}
}

func TestSessionLockerExtendRefreshesTTLAndFailsAfterRelease(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := newSessionLockerTestClient(t)
	lockTTL := 3 * time.Second
	locker := NewSessionLocker(client, lockTTL, 200*time.Millisecond)

	sessionID := uniqueSessionLockerID("session")
	turnID := uniqueSessionLockerID("turn")

	if err := locker.Acquire(ctx, sessionID, turnID); err != nil {
		t.Fatalf("Acquire() error = %v, want nil", err)
	}

	time.Sleep(1100 * time.Millisecond)

	beforeLockTTL := redisTTLSeconds(t, client, sessionLockKey(sessionID))
	beforeRefTTL := redisTTLSeconds(t, client, sessionLockRefCountKey(sessionID))
	if beforeLockTTL >= int(lockTTL/time.Second) {
		t.Fatalf("lock TTL before Extend() = %d, want less than %d", beforeLockTTL, int(lockTTL/time.Second))
	}
	if beforeRefTTL >= int(lockTTL/time.Second) {
		t.Fatalf("refcount TTL before Extend() = %d, want less than %d", beforeRefTTL, int(lockTTL/time.Second))
	}

	if err := locker.Extend(ctx, sessionID, turnID); err != nil {
		t.Fatalf("Extend() error = %v, want nil", err)
	}

	afterLockTTL := redisTTLSeconds(t, client, sessionLockKey(sessionID))
	afterRefTTL := redisTTLSeconds(t, client, sessionLockRefCountKey(sessionID))
	if afterLockTTL < int(lockTTL/time.Second)-1 {
		t.Fatalf("lock TTL after Extend() = %d, want refreshed near %d", afterLockTTL, int(lockTTL/time.Second))
	}
	if afterRefTTL < int(lockTTL/time.Second)-1 {
		t.Fatalf("refcount TTL after Extend() = %d, want refreshed near %d", afterRefTTL, int(lockTTL/time.Second))
	}

	if err := locker.Release(ctx, sessionID, turnID); err != nil {
		t.Fatalf("Release() error = %v, want nil", err)
	}

	if err := locker.Extend(ctx, sessionID, turnID); err == nil {
		t.Fatal("Extend() after Release() error = nil, want non-nil")
	}
}

func newSessionLockerTestClient(t *testing.T) *redis.Client {
	t.Helper()

	cfg := Config{RedisDSN: testRedisDSN(t)}
	client, err := NewRedisClient(cfg)
	if err != nil {
		t.Fatalf("NewRedisClient() error = %v, want nil", err)
	}

	t.Cleanup(func() {
		_ = client.Close()
	})

	return client
}

func uniqueSessionLockerID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func assertRedisIntValue(t *testing.T, client *redis.Client, key string, want int) {
	t.Helper()

	got, err := client.Get(context.Background(), key).Int()
	if err != nil {
		t.Fatalf("GET %q error = %v, want nil", key, err)
	}
	if got != want {
		t.Fatalf("GET %q = %d, want %d", key, got, want)
	}
}

func assertRedisKeyAbsent(t *testing.T, client *redis.Client, key string) {
	t.Helper()

	_, err := client.Get(context.Background(), key).Result()
	if !errors.Is(err, redis.Nil) {
		t.Fatalf("GET %q error = %v, want redis.Nil", key, err)
	}
}

func redisTTLSeconds(t *testing.T, client *redis.Client, key string) int {
	t.Helper()

	ttl, err := client.TTL(context.Background(), key).Result()
	if err != nil {
		t.Fatalf("TTL %q error = %v, want nil", key, err)
	}
	if ttl <= 0 {
		t.Fatalf("TTL %q = %v, want positive TTL", key, ttl)
	}
	return int(ttl / time.Second)
}
