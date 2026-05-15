package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/K8Harness/ToolGate/core/mcp"
	"github.com/redis/go-redis/v9"
)

const sessionLockPollInterval = 50 * time.Millisecond

var (
	sessionLockAcquireScript = `
local ttl = tonumber(ARGV[2])
local current = redis.call('GET', KEYS[1])
if not current then
	redis.call('SET', KEYS[1], ARGV[1], 'PX', ttl)
	redis.call('SET', KEYS[2], 1, 'PX', ttl)
	return 1
end
if current == ARGV[1] then
	redis.call('INCR', KEYS[2])
	redis.call('PEXPIRE', KEYS[1], ttl)
	redis.call('PEXPIRE', KEYS[2], ttl)
	return 1
end
return 0
`
	sessionLockReleaseScript = `
local current = redis.call('GET', KEYS[1])
if current ~= ARGV[1] then
	return -1
end
local remaining = redis.call('DECR', KEYS[2])
if remaining <= 0 then
	redis.call('DEL', KEYS[1], KEYS[2])
	return 0
end
	return remaining
`
	sessionLockExtendScript = `
local ttl = tonumber(ARGV[2])
local current = redis.call('GET', KEYS[1])
if current ~= ARGV[1] then
	return 0
end
redis.call('PEXPIRE', KEYS[1], ttl)
redis.call('PEXPIRE', KEYS[2], ttl)
return 1
`
)

type LockTimeoutError struct {
	Message string
}

func (e *LockTimeoutError) Error() string {
	if e == nil || e.Message == "" {
		return "session lock acquisition timed out"
	}
	return e.Message
}

func (e *LockTimeoutError) JSONRPCCode() int {
	return mcp.CodeSessionBusy
}

type SessionLocker struct {
	rdb            *redis.Client
	lockTTL        time.Duration
	acquireTimeout time.Duration
}

func NewSessionLocker(rdb *redis.Client, lockTTL, acquireTimeout time.Duration) *SessionLocker {
	return &SessionLocker{
		rdb:            rdb,
		lockTTL:        lockTTL,
		acquireTimeout: acquireTimeout,
	}
}

func (l *SessionLocker) Acquire(ctx context.Context, sessionID, turnID string) error {
	keys := []string{sessionLockKey(sessionID), sessionLockRefCountKey(sessionID)}
	args := []any{turnID, l.lockTTL.Milliseconds()}
	deadline := time.Now().Add(l.acquireTimeout)

	for {
		acquired, err := l.evalBoolResult(ctx, sessionLockAcquireScript, keys, args...)
		if err != nil {
			return fmt.Errorf("acquire session lock for session %q turn %q: %w", sessionID, turnID, err)
		}
		if acquired {
			return nil
		}
		if l.acquireTimeout <= 0 || !time.Now().Before(deadline) {
			return &LockTimeoutError{
				Message: fmt.Sprintf("session %q is busy for turn %q", sessionID, turnID),
			}
		}

		wait := sessionLockPollInterval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *SessionLocker) Release(ctx context.Context, sessionID, turnID string) error {
	keys := []string{sessionLockKey(sessionID), sessionLockRefCountKey(sessionID)}
	if _, err := l.rdb.Eval(ctx, sessionLockReleaseScript, keys, turnID).Int(); err != nil {
		return fmt.Errorf("release session lock for session %q turn %q: %w", sessionID, turnID, err)
	}
	return nil
}

func (l *SessionLocker) Extend(ctx context.Context, sessionID, turnID string) error {
	keys := []string{sessionLockKey(sessionID), sessionLockRefCountKey(sessionID)}
	extended, err := l.evalBoolResult(ctx, sessionLockExtendScript, keys, turnID, l.lockTTL.Milliseconds())
	if err != nil {
		return fmt.Errorf("extend session lock for session %q turn %q: %w", sessionID, turnID, err)
	}
	if !extended {
		return errors.New("session lock is not held by this turn")
	}
	return nil
}

func (l *SessionLocker) evalBoolResult(ctx context.Context, script string, keys []string, args ...any) (bool, error) {
	result, err := l.rdb.Eval(ctx, script, keys, args...).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func sessionLockKey(sessionID string) string {
	return "session:" + sessionID + ":lock"
}

func sessionLockRefCountKey(sessionID string) string {
	return "session:" + sessionID + ":refcount"
}
