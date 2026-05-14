package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type jsonRPCCodeError interface {
	JSONRPCCode() int
}

func TestUpstreamForwarderPostsJSONRequestWithExpectedHeaders(t *testing.T) {
	req := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"refund"}`),
	}

	var gotMethod, gotContentType, gotAccept string
	var gotBody JSONRPCRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("Decode forwarded body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	defer upstream.Close()

	resp, err := NewUpstreamForwarder(upstream.URL, time.Second).Handle(context.Background(), req)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if resp == nil || string(resp.Result) != `{"ok":true}` {
		t.Fatalf("response result = %v, want {ok:true}", resp)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotAccept != "application/json, text/event-stream" {
		t.Fatalf("Accept = %q, want application/json, text/event-stream", gotAccept)
	}
	if gotBody.JSONRPC != req.JSONRPC || string(gotBody.ID) != string(req.ID) || gotBody.Method != req.Method || string(gotBody.Params) != string(req.Params) {
		t.Fatalf("forwarded body = %+v, want %+v", gotBody, req)
	}
}

func TestUpstreamForwarderPropagatesJSONRPCErrorResponseUnchanged(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
	}))
	defer upstream.Close()

	resp, err := NewUpstreamForwarder(upstream.URL, time.Second).Handle(context.Background(), testForwarderRequest())
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("response error is nil")
	}
	if resp.Error.Code != -32601 {
		t.Fatalf("Error.Code = %d, want -32601", resp.Error.Code)
	}
	if resp.Error.Message != "method not found" {
		t.Fatalf("Error.Message = %q, want method not found", resp.Error.Message)
	}
}

func TestUpstreamForwarderParsesFirstSSEDataResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": ready\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"from\":\"sse\"}}\n\n"))
	}))
	defer upstream.Close()

	resp, err := NewUpstreamForwarder(upstream.URL, time.Second).Handle(context.Background(), testForwarderRequest())
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if resp == nil || string(resp.Result) != `{"from":"sse"}` {
		t.Fatalf("response result = %v, want SSE result", resp)
	}
}

func TestUpstreamForwarderReturnsInternalErrorForNon200Status(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer upstream.Close()

	_, err := NewUpstreamForwarder(upstream.URL, time.Second).Handle(context.Background(), testForwarderRequest())
	assertJSONRPCErrorCode(t, err, CodeInternalError)
}

func TestUpstreamForwarderReturnsInternalErrorForDecodeFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	defer upstream.Close()

	_, err := NewUpstreamForwarder(upstream.URL, time.Second).Handle(context.Background(), testForwarderRequest())
	assertJSONRPCErrorCode(t, err, CodeInternalError)
}

func TestUpstreamForwarderReturnsInternalErrorForNetworkFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	url := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("Close listener: %v", err)
	}

	_, err = NewUpstreamForwarder(url, time.Second).Handle(context.Background(), testForwarderRequest())
	assertJSONRPCErrorCode(t, err, CodeInternalError)
}

func TestUpstreamForwarderReturnsInternalErrorForTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"late":true}}`))
	}))
	defer upstream.Close()

	_, err := NewUpstreamForwarder(upstream.URL, time.Nanosecond).Handle(context.Background(), testForwarderRequest())
	assertJSONRPCErrorCode(t, err, CodeInternalError)
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("error = %q, want timeout context", err.Error())
	}
}

func testForwarderRequest() *JSONRPCRequest {
	return &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"refund"}`),
	}
}

func assertJSONRPCErrorCode(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatal("error is nil")
	}
	var coded jsonRPCCodeError
	if !errors.As(err, &coded) {
		t.Fatalf("error %T does not expose JSONRPCCode()", err)
	}
	if got := coded.JSONRPCCode(); got != want {
		t.Fatalf("JSONRPCCode() = %d, want %d", got, want)
	}
}
