package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestNewPipelinePanicsOnNilTerminal(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewPipeline(nil) did not panic")
		}
	}()

	NewPipeline(nil)
}

func TestPipelineRunScenarios(t *testing.T) {
	wantTerminal := testPipelineResponse(json.RawMessage(`1`), `{"ok":true}`)
	wantHalt := testPipelineResponse(json.RawMessage(`1`), `{"halted":true}`)
	wantErr := errors.New("halt")

	tests := []struct {
		name      string
		handlers  []pipelineTestHandler
		wantResp  *JSONRPCResponse
		wantErr   error
		wantCalls []string
	}{
		{
			name:      "calls terminal when no handlers registered",
			wantResp:  wantTerminal,
			wantCalls: []string{"terminal"},
		},
		{
			name: "continues to terminal after nil response and nil error",
			handlers: []pipelineTestHandler{
				{name: "first"},
			},
			wantResp:  wantTerminal,
			wantCalls: []string{"first", "terminal"},
		},
		{
			name: "halts on first handler error",
			handlers: []pipelineTestHandler{
				{name: "first", err: wantErr},
				{name: "second"},
			},
			wantErr:   wantErr,
			wantCalls: []string{"first"},
		},
		{
			name: "halts on first handler response",
			handlers: []pipelineTestHandler{
				{name: "first", resp: wantHalt},
				{name: "second"},
			},
			wantResp:  wantHalt,
			wantCalls: []string{"first"},
		},
		{
			name: "executes handlers in registration order before terminal",
			handlers: []pipelineTestHandler{
				{name: "first"},
				{name: "second"},
			},
			wantResp:  wantTerminal,
			wantCalls: []string{"first", "second", "terminal"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := testPipelineRequest()
			var calls []string

			pipeline := NewPipeline(HandlerFunc(func(ctx context.Context, gotReq *JSONRPCRequest) (*JSONRPCResponse, error) {
				calls = append(calls, "terminal")
				if gotReq != req {
					t.Fatalf("terminal req = %p, want %p", gotReq, req)
				}
				return wantTerminal, nil
			}))

			for _, handler := range tt.handlers {
				handler := handler
				pipeline.Use(HandlerFunc(func(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
					calls = append(calls, handler.name)
					return handler.resp, handler.err
				}))
			}

			got, err := pipeline.Run(context.Background(), req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Run error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.wantResp {
				t.Fatalf("Run response = %p, want %p", got, tt.wantResp)
			}
			if !reflect.DeepEqual(calls, tt.wantCalls) {
				t.Fatalf("calls = %v, want %v", calls, tt.wantCalls)
			}
		})
	}
}

type pipelineTestHandler struct {
	name string
	resp *JSONRPCResponse
	err  error
}

func testPipelineRequest() *JSONRPCRequest {
	return &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
	}
}

func testPipelineResponse(id json.RawMessage, result string) *JSONRPCResponse {
	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  json.RawMessage(result),
	}
}
