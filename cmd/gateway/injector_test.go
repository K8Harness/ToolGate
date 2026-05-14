package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/K8Harness/ToolGate/core/mcp"
)

func TestContextInjectorCreatesMetaForToolsCall(t *testing.T) {
	req := &mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"refund","arguments":{"amount":10}}`),
	}
	ctx := contextWithSessionAndTurn("session-1", "turn-1")

	resp, err := (&ContextInjector{}).Handle(ctx, req)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if resp != nil {
		t.Fatalf("Handle response = %#v, want nil", resp)
	}

	params := decodeParams(t, req.Params)
	meta := decodeMeta(t, params)
	if len(meta) != 2 {
		t.Fatalf("_meta has %d keys, want exactly sessionId and turnId: %v", len(meta), meta)
	}
	assertRawString(t, meta["sessionId"], "session-1")
	assertRawString(t, meta["turnId"], "turn-1")
}

func TestContextInjectorPreservesProgressTokenInMeta(t *testing.T) {
	req := &mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"refund","_meta":{"progressToken":"tok-1","other":true}}`),
	}
	ctx := contextWithSessionAndTurn("session-2", "turn-2")

	resp, err := (&ContextInjector{}).Handle(ctx, req)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if resp != nil {
		t.Fatalf("Handle response = %#v, want nil", resp)
	}

	params := decodeParams(t, req.Params)
	meta := decodeMeta(t, params)
	if len(meta) != 4 {
		t.Fatalf("_meta has %d keys, want progressToken, other, sessionId, turnId: %v", len(meta), meta)
	}
	assertRawString(t, meta["progressToken"], "tok-1")
	assertRawString(t, meta["sessionId"], "session-2")
	assertRawString(t, meta["turnId"], "turn-2")
	if string(meta["other"]) != "true" {
		t.Fatalf("_meta.other = %s, want true", meta["other"])
	}
}

func TestContextInjectorLeavesNonToolsCallParamsUnchanged(t *testing.T) {
	original := json.RawMessage(`{"name":"list","_meta":{"progressToken":"tok-1"}}`)
	req := &mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
		Params:  append(json.RawMessage(nil), original...),
	}

	resp, err := (&ContextInjector{}).Handle(contextWithSessionAndTurn("session-3", "turn-3"), req)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if resp != nil {
		t.Fatalf("Handle response = %#v, want nil", resp)
	}
	if string(req.Params) != string(original) {
		t.Fatalf("Params = %s, want byte-for-byte unchanged %s", req.Params, original)
	}
}

func TestContextInjectorMalformedParamsReturnsCodedErrorAndLeavesParamsUnchanged(t *testing.T) {
	original := json.RawMessage(`{"name":`)
	req := &mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  append(json.RawMessage(nil), original...),
	}

	resp, err := (&ContextInjector{}).Handle(contextWithSessionAndTurn("session-4", "turn-4"), req)
	if err == nil {
		t.Fatal("Handle error = nil, want coded error")
	}
	if resp != nil {
		t.Fatalf("Handle response = %#v, want nil", resp)
	}
	assertJSONRPCCode(t, err, mcp.CodeInternalError)
	if string(req.Params) != string(original) {
		t.Fatalf("Params = %s, want unchanged malformed params %s", req.Params, original)
	}
}

type codedError interface {
	JSONRPCCode() int
}

func contextWithSessionAndTurn(sessionID, turnID string) context.Context {
	ctx := mcp.WithSessionID(context.Background(), sessionID)
	return mcp.WithTurnID(ctx, turnID)
}

func decodeParams(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var params map[string]json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("Unmarshal params %s: %v", raw, err)
	}
	return params
}

func decodeMeta(t *testing.T, params map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	raw, ok := params["_meta"]
	if !ok {
		t.Fatal("params missing _meta")
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("Unmarshal _meta %s: %v", raw, err)
	}
	return meta
}

func assertRawString(t *testing.T, raw json.RawMessage, want string) {
	t.Helper()
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal raw string %s: %v", raw, err)
	}
	if got != want {
		t.Fatalf("string value = %q, want %q", got, want)
	}
}

func assertJSONRPCCode(t *testing.T, err error, want int) {
	t.Helper()
	var coded codedError
	if !errors.As(err, &coded) {
		t.Fatalf("error %T does not expose JSONRPCCode()", err)
	}
	if got := coded.JSONRPCCode(); got != want {
		t.Fatalf("JSONRPCCode() = %d, want %d", got, want)
	}
}
