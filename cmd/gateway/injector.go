package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/K8Harness/ToolGate/core/mcp"
)

type ContextInjector struct{}

type metaInjectionError struct {
	err error
}

func (e *metaInjectionError) Error() string {
	if e.err == nil {
		return "internal error: meta injection failed"
	}
	return "internal error: meta injection failed: " + e.err.Error()
}

func (e *metaInjectionError) Unwrap() error {
	return e.err
}

func (e *metaInjectionError) JSONRPCCode() int {
	return mcp.CodeInternalError
}

func (i *ContextInjector) Handle(ctx context.Context, req *mcp.JSONRPCRequest) (*mcp.JSONRPCResponse, error) {
	if req.Method != "tools/call" {
		return nil, nil
	}

	nextParams, err := injectContextMeta(req.Params, mcp.SessionIDFromContext(ctx), mcp.TurnIDFromContext(ctx))
	if err != nil {
		return nil, &metaInjectionError{err: err}
	}
	req.Params = nextParams
	return nil, nil
}

func injectContextMeta(rawParams json.RawMessage, sessionID, turnID string) (json.RawMessage, error) {
	var params map[string]json.RawMessage
	if err := json.Unmarshal(rawParams, &params); err != nil {
		return nil, fmt.Errorf("decode params: %w", err)
	}

	meta := make(map[string]json.RawMessage)
	if rawMeta, ok := params["_meta"]; ok {
		if err := json.Unmarshal(rawMeta, &meta); err != nil {
			return nil, fmt.Errorf("decode _meta: %w", err)
		}
	}

	sessionRaw, err := json.Marshal(sessionID)
	if err != nil {
		return nil, fmt.Errorf("encode sessionId: %w", err)
	}
	turnRaw, err := json.Marshal(turnID)
	if err != nil {
		return nil, fmt.Errorf("encode turnId: %w", err)
	}
	meta["sessionId"] = sessionRaw
	meta["turnId"] = turnRaw

	rawMeta, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("encode _meta: %w", err)
	}
	params["_meta"] = rawMeta

	nextParams, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode params: %w", err)
	}
	return nextParams, nil
}
