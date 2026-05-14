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

func TestPipelineRunCallsTerminalWhenNoHandlersRegistered(t *testing.T) {
	req := testPipelineRequest()
	want := testPipelineResponse(req.ID, `{"ok":true}`)
	var calls []string

	pipeline := NewPipeline(HandlerFunc(func(ctx context.Context, gotReq *JSONRPCRequest) (*JSONRPCResponse, error) {
		calls = append(calls, "terminal")
		if gotReq != req {
			t.Fatalf("terminal req = %p, want %p", gotReq, req)
		}
		return want, nil
	}))

	got, err := pipeline.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got != want {
		t.Fatalf("Run response = %p, want %p", got, want)
	}
	if !reflect.DeepEqual(calls, []string{"terminal"}) {
		t.Fatalf("calls = %v, want [terminal]", calls)
	}
}

func TestPipelineRunExecutesHandlersInRegistrationOrderBeforeTerminal(t *testing.T) {
	req := testPipelineRequest()
	want := testPipelineResponse(req.ID, `{"ok":true}`)
	var calls []string

	pipeline := NewPipeline(HandlerFunc(func(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
		calls = append(calls, "terminal")
		return want, nil
	}))
	pipeline.Use(HandlerFunc(func(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
		calls = append(calls, "first")
		return nil, nil
	}))
	pipeline.Use(HandlerFunc(func(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
		calls = append(calls, "second")
		return nil, nil
	}))

	got, err := pipeline.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got != want {
		t.Fatalf("Run response = %p, want %p", got, want)
	}
	if !reflect.DeepEqual(calls, []string{"first", "second", "terminal"}) {
		t.Fatalf("calls = %v, want [first second terminal]", calls)
	}
}

func TestPipelineRunHaltsOnFirstHandlerError(t *testing.T) {
	req := testPipelineRequest()
	wantErr := errors.New("halt")
	var calls []string

	pipeline := NewPipeline(HandlerFunc(func(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
		calls = append(calls, "terminal")
		return testPipelineResponse(req.ID, `{"ok":true}`), nil
	}))
	pipeline.Use(HandlerFunc(func(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
		calls = append(calls, "first")
		return nil, wantErr
	}))
	pipeline.Use(HandlerFunc(func(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
		calls = append(calls, "second")
		return nil, nil
	}))

	got, err := pipeline.Run(context.Background(), req)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if got != nil {
		t.Fatalf("Run response = %v, want nil", got)
	}
	if !reflect.DeepEqual(calls, []string{"first"}) {
		t.Fatalf("calls = %v, want [first]", calls)
	}
}

func TestPipelineRunHaltsOnFirstHandlerResponse(t *testing.T) {
	req := testPipelineRequest()
	want := testPipelineResponse(req.ID, `{"halted":true}`)
	var calls []string

	pipeline := NewPipeline(HandlerFunc(func(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
		calls = append(calls, "terminal")
		return testPipelineResponse(req.ID, `{"ok":true}`), nil
	}))
	pipeline.Use(HandlerFunc(func(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
		calls = append(calls, "first")
		return want, nil
	}))
	pipeline.Use(HandlerFunc(func(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
		calls = append(calls, "second")
		return nil, nil
	}))

	got, err := pipeline.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got != want {
		t.Fatalf("Run response = %p, want %p", got, want)
	}
	if !reflect.DeepEqual(calls, []string{"first"}) {
		t.Fatalf("calls = %v, want [first]", calls)
	}
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
