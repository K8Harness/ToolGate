package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestHandlerFuncSatisfiesHandlerAndDelegates(t *testing.T) {
	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
	}
	want := &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  json.RawMessage(`{"ok":true}`),
	}

	var handler Handler = HandlerFunc(func(ctx context.Context, gotReq *JSONRPCRequest) (*JSONRPCResponse, error) {
		if ctx == nil {
			t.Fatal("ctx is nil")
		}
		if gotReq != req {
			t.Fatalf("req = %p, want %p", gotReq, req)
		}
		return want, nil
	})

	got, err := handler.Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if got != want {
		t.Fatalf("Handle response = %p, want %p", got, want)
	}
}
