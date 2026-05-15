package main

import (
	"context"
	"fmt"

	"github.com/K8Harness/ToolGate/core/mcp"
)

type ConcurrencyGuard struct {
	locker     *SessionLocker
	rwlock     *TurnRWLock
	classifier *OperationClassifier
}

func NewConcurrencyGuard(
	locker *SessionLocker,
	rwlock *TurnRWLock,
	classifier *OperationClassifier,
) *ConcurrencyGuard {
	return &ConcurrencyGuard{
		locker:     locker,
		rwlock:     rwlock,
		classifier: classifier,
	}
}

func (g *ConcurrencyGuard) Execute(
	ctx context.Context,
	sessionID string,
	turnID string,
	toolName string,
	fn func() (*mcp.JSONRPCResponse, error),
) (resp *mcp.JSONRPCResponse, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic during guarded execution: %v", recovered)
			resp = nil
		}
	}()

	if toolName == "" {
		return fn()
	}

	if err := g.locker.Acquire(ctx, sessionID, turnID); err != nil {
		return nil, err
	}
	defer func() {
		if releaseErr := g.locker.Release(ctx, sessionID, turnID); releaseErr != nil && err == nil {
			err = releaseErr
			resp = nil
		}
	}()

	class := OperationClassWrite
	if g.classifier != nil {
		class = g.classifier.Classify(toolName)
	}

	if class == OperationClassRead {
		if err := g.rwlock.ReadLock(ctx, turnID); err != nil {
			return nil, err
		}
		defer func() {
			if releaseErr := g.rwlock.ReadUnlock(ctx, turnID); releaseErr != nil && err == nil {
				err = releaseErr
				resp = nil
			}
		}()
		return fn()
	}

	ownerToken, err := g.rwlock.WriteLock(ctx, turnID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if releaseErr := g.rwlock.WriteUnlock(ctx, turnID, ownerToken); releaseErr != nil && err == nil {
			err = releaseErr
			resp = nil
		}
	}()

	return fn()
}
