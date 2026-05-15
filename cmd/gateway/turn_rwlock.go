package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const turnLockPollInterval = 50 * time.Millisecond

var (
	turnReadLockScript = `
local ttl = tonumber(ARGV[1])
if redis.call('EXISTS', KEYS[2]) == 1 then
	return 0
end
local readers = redis.call('INCR', KEYS[1])
redis.call('PEXPIRE', KEYS[1], ttl)
return readers
`
	turnReadUnlockScript = `
return redis.call('DECR', KEYS[1])
`
	turnWriteLockScript = `
local ttl = tonumber(ARGV[2])
local readers = tonumber(redis.call('GET', KEYS[1]) or '0')
if readers ~= 0 then
	return 0
end
if redis.call('SET', KEYS[2], ARGV[1], 'NX', 'PX', ttl) then
	return 1
end
return 0
`
	turnWriteUnlockScript = `
local current = redis.call('GET', KEYS[1])
if current ~= ARGV[1] then
	return 0
end
return redis.call('DEL', KEYS[1])
`
)

type TurnRWLock struct {
	rdb            *redis.Client
	lockTTL        time.Duration
	acquireTimeout time.Duration
}

func NewTurnRWLock(rdb *redis.Client, lockTTL, acquireTimeout time.Duration) *TurnRWLock {
	return &TurnRWLock{
		rdb:            rdb,
		lockTTL:        lockTTL,
		acquireTimeout: acquireTimeout,
	}
}

func (rw *TurnRWLock) ReadLock(ctx context.Context, turnID string) error {
	keys := []string{turnReadersKey(turnID), turnWriteLockKey(turnID)}
	deadline := time.Now().Add(rw.acquireTimeout)

	for {
		acquired, err := rw.evalAcquireResult(ctx, turnReadLockScript, keys, rw.lockTTL.Milliseconds())
		if err != nil {
			return fmt.Errorf("acquire read lock for turn %q: %w", turnID, err)
		}
		if acquired {
			return nil
		}
		if rw.acquireTimeout <= 0 || !time.Now().Before(deadline) {
			return &LockTimeoutError{
				Message: fmt.Sprintf("turn %q read lock acquisition timed out", turnID),
			}
		}
		if err := waitForTurnLockRetry(ctx, deadline); err != nil {
			return err
		}
	}
}

func (rw *TurnRWLock) ReadUnlock(ctx context.Context, turnID string) error {
	if _, err := rw.rdb.Eval(ctx, turnReadUnlockScript, []string{turnReadersKey(turnID)}).Int(); err != nil {
		return fmt.Errorf("release read lock for turn %q: %w", turnID, err)
	}
	return nil
}

func (rw *TurnRWLock) WriteLock(ctx context.Context, turnID string) (string, error) {
	ownerToken, err := newTurnWriteOwnerToken()
	if err != nil {
		return "", fmt.Errorf("generate write lock owner token: %w", err)
	}

	keys := []string{turnReadersKey(turnID), turnWriteLockKey(turnID)}
	deadline := time.Now().Add(rw.acquireTimeout)

	for {
		acquired, err := rw.evalAcquireResult(ctx, turnWriteLockScript, keys, ownerToken, rw.lockTTL.Milliseconds())
		if err != nil {
			return "", fmt.Errorf("acquire write lock for turn %q: %w", turnID, err)
		}
		if acquired {
			return ownerToken, nil
		}
		if rw.acquireTimeout <= 0 || !time.Now().Before(deadline) {
			return "", &LockTimeoutError{
				Message: fmt.Sprintf("turn %q write lock acquisition timed out", turnID),
			}
		}
		if err := waitForTurnLockRetry(ctx, deadline); err != nil {
			return "", err
		}
	}
}

func (rw *TurnRWLock) WriteUnlock(ctx context.Context, turnID, ownerToken string) error {
	if _, err := rw.rdb.Eval(ctx, turnWriteUnlockScript, []string{turnWriteLockKey(turnID)}, ownerToken).Int(); err != nil {
		return fmt.Errorf("release write lock for turn %q: %w", turnID, err)
	}
	return nil
}

func (rw *TurnRWLock) evalAcquireResult(ctx context.Context, script string, keys []string, args ...any) (bool, error) {
	result, err := rw.rdb.Eval(ctx, script, keys, args...).Int()
	if err != nil {
		return false, err
	}
	return result != 0, nil
}

func waitForTurnLockRetry(ctx context.Context, deadline time.Time) error {
	wait := turnLockPollInterval
	if remaining := time.Until(deadline); remaining < wait {
		wait = remaining
	}

	timer := time.NewTimer(wait)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func newTurnWriteOwnerToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}

	token[6] = (token[6] & 0x0f) | 0x40
	token[8] = (token[8] & 0x3f) | 0x80

	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		token[0:4],
		token[4:6],
		token[6:8],
		token[8:10],
		token[10:16],
	), nil
}

func turnReadersKey(turnID string) string {
	return "turn:" + turnID + ":readers"
}

func turnWriteLockKey(turnID string) string {
	return "turn:" + turnID + ":wlock"
}
