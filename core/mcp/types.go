package mcp

import (
	"context"
	"encoding/json"
)

// Standard JSON-RPC error codes used by the gateway.
const (
	CodeParseError    = -32700
	CodePolicyDenied  = -32001
	CodeInternalError = -32603
)

type contextKey int

const (
	ContextKeySessionID contextKey = iota
	ContextKeyTurnID
)

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// MCPMeta is the _meta object injected into outgoing params.
type MCPMeta struct {
	ProgressToken any    `json:"progressToken,omitempty"`
	SessionID     string `json:"sessionId,omitempty"`
	TurnID        string `json:"turnId,omitempty"`
}

func NewErrorResponse(id json.RawMessage, code int, message string) *JSONRPCResponse {
	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
		},
	}
}

func SessionIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ContextKeySessionID).(string)
	return id
}

func TurnIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ContextKeyTurnID).(string)
	return id
}

func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ContextKeySessionID, id)
}

func WithTurnID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ContextKeyTurnID, id)
}
