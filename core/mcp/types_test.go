package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCodePolicyDeniedConstant(t *testing.T) {
	if CodePolicyDenied != -32001 {
		t.Fatalf("CodePolicyDenied = %d, want %d", CodePolicyDenied, -32001)
	}
}

func TestNewErrorResponseBuildsJSONRPCErrorResponse(t *testing.T) {
	id := json.RawMessage(`"req-1"`)

	resp := NewErrorResponse(id, CodeParseError, "parse error")

	if resp.JSONRPC != "2.0" {
		t.Fatalf("JSONRPC = %q, want %q", resp.JSONRPC, "2.0")
	}
	if string(resp.ID) != string(id) {
		t.Fatalf("ID = %s, want %s", resp.ID, id)
	}
	if resp.Error == nil {
		t.Fatal("Error is nil")
	}
	if resp.Error.Code != CodeParseError {
		t.Fatalf("Error.Code = %d, want %d", resp.Error.Code, CodeParseError)
	}
	if resp.Error.Message != "parse error" {
		t.Fatalf("Error.Message = %q, want %q", resp.Error.Message, "parse error")
	}
	if resp.Result != nil {
		t.Fatalf("Result = %s, want nil", resp.Result)
	}
}

func TestContextHelpersRoundTripSessionAndTurnIDs(t *testing.T) {
	ctx := context.Background()

	if got := SessionIDFromContext(ctx); got != "" {
		t.Fatalf("SessionIDFromContext(background) = %q, want empty string", got)
	}
	if got := TurnIDFromContext(ctx); got != "" {
		t.Fatalf("TurnIDFromContext(background) = %q, want empty string", got)
	}

	ctx = WithSessionID(ctx, "session-1")
	ctx = WithTurnID(ctx, "turn-1")

	if got := SessionIDFromContext(ctx); got != "session-1" {
		t.Fatalf("SessionIDFromContext(ctx) = %q, want %q", got, "session-1")
	}
	if got := TurnIDFromContext(ctx); got != "turn-1" {
		t.Fatalf("TurnIDFromContext(ctx) = %q, want %q", got, "turn-1")
	}
}

func TestSharedTypesMarshalWithExpectedFieldNames(t *testing.T) {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"refund"}`),
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal(JSONRPCRequest): %v", err)
	}
	if got, want := string(reqBytes), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"refund"}}`; got != want {
		t.Fatalf("JSONRPCRequest JSON = %s, want %s", got, want)
	}

	meta := MCPMeta{ProgressToken: "tok-1", SessionID: "session-1", TurnID: "turn-1"}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal(MCPMeta): %v", err)
	}
	if got, want := string(metaBytes), `{"progressToken":"tok-1","sessionId":"session-1","turnId":"turn-1"}`; got != want {
		t.Fatalf("MCPMeta JSON = %s, want %s", got, want)
	}
}
