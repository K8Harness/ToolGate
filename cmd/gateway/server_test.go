package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/K8Harness/ToolGate/core/mcp"
)

func TestServerPostInitializeCreatesSessionSetsHeaderAndForwardsRequest(t *testing.T) {
	forwarder := &captureHandler{
		response: &mcp.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage(`1`),
			Result:  json.RawMessage(`{"ok":true}`),
		},
	}
	server := newTestServer(t, forwarder)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"client":"test"}}`))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	sessionID := rec.Header().Get(mcpSessionIDHeader)
	if sessionID == "" {
		t.Fatal("Mcp-Session-Id header = empty, want generated session ID")
	}
	if _, ok := server.sessions.Get(sessionID); !ok {
		t.Fatalf("session %q not found in registry", sessionID)
	}
	if forwarder.callCount != 1 {
		t.Fatalf("forwarder call count = %d, want 1", forwarder.callCount)
	}
	if forwarder.request == nil {
		t.Fatal("forwarded request = nil, want decoded initialize request")
	}
	if forwarder.request.Method != "initialize" {
		t.Fatalf("forwarded method = %q, want initialize", forwarder.request.Method)
	}

	var body mcp.JSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if body.Error != nil {
		t.Fatalf("response error = %+v, want nil", body.Error)
	}
	if string(body.Result) != `{"ok":true}` {
		t.Fatalf("response result = %s, want {\"ok\":true}", body.Result)
	}
}

func TestServerPostToolsCallMissingSessionReturnsInvalidRequest(t *testing.T) {
	server := newTestServer(t, &captureHandler{})

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"refund"}}`))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertErrorCode(t, rec, -32600)
}

func TestServerPostMalformedJSONWithValidSessionReturnsParseError(t *testing.T) {
	server := newTestServer(t, &captureHandler{})
	session := server.sessions.Create()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":`))
	req.Header.Set(mcpSessionIDHeader, session.ID)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertErrorCode(t, rec, mcp.CodeParseError)
}

func TestServerRequestContextUsesConfiguredTurnIDHeader(t *testing.T) {
	server := newTestServer(t, &captureHandler{})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set(server.config.TurnIDHeader, "turn-from-header")

	ctx := server.requestContext(req, "session-1")

	if got := mcp.SessionIDFromContext(ctx); got != "session-1" {
		t.Fatalf("SessionIDFromContext() = %q, want session-1", got)
	}
	if got := mcp.TurnIDFromContext(ctx); got != "turn-from-header" {
		t.Fatalf("TurnIDFromContext() = %q, want turn-from-header", got)
	}
}

func TestServerRequestContextGeneratesTurnIDWhenHeaderMissing(t *testing.T) {
	server := newTestServer(t, &captureHandler{})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)

	ctx := server.requestContext(req, "session-2")

	if got := mcp.SessionIDFromContext(ctx); got != "session-2" {
		t.Fatalf("SessionIDFromContext() = %q, want session-2", got)
	}
	if got := mcp.TurnIDFromContext(ctx); got == "" {
		t.Fatal("TurnIDFromContext() = empty, want generated turn ID")
	}
}

func TestServerWithRequestContextStoresSessionAndTurnOnRequest(t *testing.T) {
	server := newTestServer(t, &captureHandler{})
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set(server.config.TurnIDHeader, "turn-from-header")

	nextReq := server.withRequestContext(req, "session-ctx")

	if got := mcp.SessionIDFromContext(nextReq.Context()); got != "session-ctx" {
		t.Fatalf("SessionIDFromContext() = %q, want session-ctx", got)
	}
	if got := mcp.TurnIDFromContext(nextReq.Context()); got != "turn-from-header" {
		t.Fatalf("TurnIDFromContext() = %q, want turn-from-header", got)
	}
}

func TestServerGetMCPWritesSSEKeepalive(t *testing.T) {
	server := newTestServer(t, &captureHandler{})
	session := server.sessions.Create()

	originalInterval := keepaliveInterval
	keepaliveInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		keepaliveInterval = originalInterval
	})

	ts := httptest.NewServer(server)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set(mcpSessionIDHeader, session.ID)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /mcp: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString: %v", err)
	}
	if line != ": keepalive\n" {
		t.Fatalf("first SSE line = %q, want %q", line, ": keepalive\\n")
	}
}

func TestServerDeleteMCPDeletesExistingSession(t *testing.T) {
	server := newTestServer(t, &captureHandler{})
	session := server.sessions.Create()

	req := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	req.Header.Set(mcpSessionIDHeader, session.ID)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if _, ok := server.sessions.Get(session.ID); ok {
		t.Fatalf("session %q still exists after delete", session.ID)
	}
}

func TestServerPostToolsCallRunsPipelineWithRequestContext(t *testing.T) {
	terminal := &captureHandler{
		response: &mcp.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage(`1`),
			Result:  json.RawMessage(`{"ok":true}`),
		},
	}
	server := newTestServer(t, &captureHandler{})
	server.pipeline = mcp.NewPipeline(terminal)
	session := server.sessions.Create()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"refund"}}`))
	req.Header.Set(mcpSessionIDHeader, session.ID)
	req.Header.Set(server.config.TurnIDHeader, "turn-42")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if terminal.callCount != 1 {
		t.Fatalf("pipeline terminal call count = %d, want 1", terminal.callCount)
	}
	if got := mcp.SessionIDFromContext(terminal.ctx); got != session.ID {
		t.Fatalf("SessionIDFromContext() = %q, want %q", got, session.ID)
	}
	if got := mcp.TurnIDFromContext(terminal.ctx); got != "turn-42" {
		t.Fatalf("TurnIDFromContext() = %q, want turn-42", got)
	}

	var body mcp.JSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if body.Error != nil {
		t.Fatalf("response error = %+v, want nil", body.Error)
	}
	if string(body.Result) != `{"ok":true}` {
		t.Fatalf("response result = %s, want {\"ok\":true}", body.Result)
	}
}

func TestServerPostToolsCallPipelineErrorReturnsJSONRPCError(t *testing.T) {
	server := newTestServer(t, &captureHandler{})
	server.pipeline = mcp.NewPipeline(mcp.HandlerFunc(func(ctx context.Context, req *mcp.JSONRPCRequest) (*mcp.JSONRPCResponse, error) {
		return nil, serverCodedError{code: -32001, message: "policy denied"}
	}))
	session := server.sessions.Create()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"refund"}}`))
	req.Header.Set(mcpSessionIDHeader, session.ID)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertErrorCode(t, rec, -32001)
}

func TestServerServeHTTPRecoversFromPanicAndKeepsServing(t *testing.T) {
	var calls int
	server := newTestServer(t, &captureHandler{})
	server.pipeline = mcp.NewPipeline(mcp.HandlerFunc(func(ctx context.Context, req *mcp.JSONRPCRequest) (*mcp.JSONRPCResponse, error) {
		calls++
		if calls == 1 {
			panic("boom")
		}
		return &mcp.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"ok":true}`),
		}, nil
	}))
	session := server.sessions.Create()

	firstReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"refund"}}`))
	firstReq.Header.Set(mcpSessionIDHeader, session.ID)
	firstRec := httptest.NewRecorder()
	server.ServeHTTP(firstRec, firstReq)
	assertErrorCode(t, firstRec, mcp.CodeInternalError)

	secondReq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund"}}`))
	secondReq.Header.Set(mcpSessionIDHeader, session.ID)
	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)

	if secondRec.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", secondRec.Code, http.StatusOK)
	}
	var body mcp.JSONRPCResponse
	if err := json.Unmarshal(secondRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode second response body: %v", err)
	}
	if body.Error != nil {
		t.Fatalf("second response error = %+v, want nil", body.Error)
	}
}

func TestServerPostToolsCallLogsSuccessOutcome(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	terminal := &captureHandler{
		response: &mcp.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage(`1`),
			Result:  json.RawMessage(`{"ok":true}`),
		},
	}

	server := newTestServerWithLogger(t, terminal, logger)
	server.pipeline = mcp.NewPipeline(terminal)
	server.pipeline.Use(NewRequestLogger(logger))
	session := server.sessions.Create()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"refund","arguments":{"token":"secret-token"}}}`))
	req.Header.Set(mcpSessionIDHeader, session.ID)
	req.Header.Set(server.config.TurnIDHeader, "turn-logger")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	outcome := findLogEntryWithField(t, buf.String(), "outcome")
	assertLogString(t, outcome, "sessionId", session.ID)
	assertLogString(t, outcome, "turnId", "turn-logger")
	assertLogString(t, outcome, "method", "tools/call")
	assertLogString(t, outcome, "toolName", "refund")
	assertLogString(t, outcome, "outcome", "success")
	if _, ok := outcome["arguments"]; ok {
		t.Fatalf("outcome log unexpectedly contains arguments: %v", outcome)
	}
	if strings.Contains(buf.String(), "secret-token") {
		t.Fatalf("logs unexpectedly contain argument value: %s", buf.String())
	}
}

func TestServerPostToolsCallLogsErrorOutcome(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	server := newTestServerWithLogger(t, &captureHandler{}, logger)
	server.pipeline = mcp.NewPipeline(mcp.HandlerFunc(func(ctx context.Context, req *mcp.JSONRPCRequest) (*mcp.JSONRPCResponse, error) {
		return nil, serverCodedError{code: -32001, message: "policy denied"}
	}))
	server.pipeline.Use(NewRequestLogger(logger))
	session := server.sessions.Create()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"refund"}}`))
	req.Header.Set(mcpSessionIDHeader, session.ID)
	req.Header.Set(server.config.TurnIDHeader, "turn-logger")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertErrorCode(t, rec, -32001)
	outcome := findLogEntryWithField(t, buf.String(), "outcome")
	assertLogString(t, outcome, "outcome", "error")
	if got := outcome["errorCode"]; got != float64(-32001) {
		t.Fatalf("errorCode = %v, want -32001", got)
	}
}

func TestServerHTTPServerUsesApprovalSafeWriteTimeout(t *testing.T) {
	server := newTestServer(t, &captureHandler{})

	httpServer := server.httpServer()

	if httpServer == nil {
		t.Fatal("httpServer() = nil, want configured server")
	}
	if httpServer.Handler != server {
		t.Fatal("httpServer handler mismatch, want server")
	}
	if httpServer.WriteTimeout < 6*time.Minute {
		t.Fatalf("WriteTimeout = %v, want at least %v", httpServer.WriteTimeout, 6*time.Minute)
	}
}

type captureHandler struct {
	callCount int
	ctx       context.Context
	request   *mcp.JSONRPCRequest
	response  *mcp.JSONRPCResponse
	err       error
}

func (h *captureHandler) Handle(ctx context.Context, req *mcp.JSONRPCRequest) (*mcp.JSONRPCResponse, error) {
	h.callCount++
	h.ctx = ctx
	if req != nil {
		clone := *req
		h.request = &clone
	}
	return h.response, h.err
}

func newTestServer(t *testing.T, forwarder mcp.Handler) *Server {
	return newTestServerWithLogger(t, forwarder, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func newTestServerWithLogger(t *testing.T, forwarder mcp.Handler, logger *slog.Logger) *Server {
	t.Helper()
	config := &Config{
		ListenPort:      8080,
		UpstreamMCPURL:  "http://example.invalid",
		TurnIDHeader:    defaultTurnIDHeader,
		UpstreamTimeout: time.Second,
		SessionTTL:      time.Minute,
	}
	server := NewServer(config, nil, logger)
	server.forwarder = forwarder
	return server
}

type serverCodedError struct {
	code    int
	message string
}

func (e serverCodedError) Error() string {
	return e.message
}

func (e serverCodedError) JSONRPCCode() int {
	return e.code
}

func findLogEntryWithField(t *testing.T, raw, field string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entry := decodeLogEntry(t, line)
		if _, ok := entry[field]; ok {
			return entry
		}
	}
	t.Fatalf("no log entry with field %q found in %q", field, raw)
	return nil
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp mcp.JSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("response error = nil, want JSON-RPC error")
	}
	if resp.Error.Code != want {
		t.Fatalf("error.code = %d, want %d", resp.Error.Code, want)
	}
}
