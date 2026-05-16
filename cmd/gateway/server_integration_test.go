package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/K8Harness/ToolGate/core/mcp"
)

func TestGatewayIntegrationSessionInitAndToolsCallInjectsMeta(t *testing.T) {
	var captured mcp.JSONRPCRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("Decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if captured.Method == "initialize" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"server":"ok"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`))
	}))
	defer upstream.Close()

	ts := newGatewayIntegrationServer(t, upstream.URL)
	defer ts.Close()

	initRec := postJSON(t, ts.URL+"/mcp", "", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"client":"test"}}`)
	sessionID := initRec.Header().Get(mcpSessionIDHeader)
	if sessionID == "" {
		t.Fatal("initialize response missing Mcp-Session-Id header")
	}

	toolRec := postJSON(t, ts.URL+"/mcp", sessionID, "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund","arguments":{"amount":10}}}`)
	if toolRec.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d, want %d", toolRec.Code, http.StatusOK)
	}

	params := decodeIntegrationParams(t, captured.Params)
	meta := decodeIntegrationMeta(t, params)
	assertIntegrationString(t, meta["sessionId"], sessionID)
	assertNonEmptyIntegrationString(t, meta["turnId"])
}

func TestGatewayIntegrationUsesTurnIDHeader(t *testing.T) {
	var captured mcp.JSONRPCRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("Decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if captured.Method == "initialize" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"server":"ok"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`))
	}))
	defer upstream.Close()

	ts := newGatewayIntegrationServer(t, upstream.URL)
	defer ts.Close()

	sessionID := initializeSession(t, ts.URL)
	postJSON(t, ts.URL+"/mcp", sessionID, "turn-123", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund"}}`)

	params := decodeIntegrationParams(t, captured.Params)
	meta := decodeIntegrationMeta(t, params)
	assertIntegrationString(t, meta["turnId"], "turn-123")
}

func TestGatewayIntegrationGeneratesTurnIDWhenHeaderMissing(t *testing.T) {
	var captured mcp.JSONRPCRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("Decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if captured.Method == "initialize" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"server":"ok"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`))
	}))
	defer upstream.Close()

	ts := newGatewayIntegrationServer(t, upstream.URL)
	defer ts.Close()

	sessionID := initializeSession(t, ts.URL)
	postJSON(t, ts.URL+"/mcp", sessionID, "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund"}}`)

	params := decodeIntegrationParams(t, captured.Params)
	meta := decodeIntegrationMeta(t, params)
	assertNonEmptyIntegrationString(t, meta["turnId"])
}

func TestGatewayIntegrationMergesExistingMeta(t *testing.T) {
	var captured mcp.JSONRPCRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("Decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if captured.Method == "initialize" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"server":"ok"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`))
	}))
	defer upstream.Close()

	ts := newGatewayIntegrationServer(t, upstream.URL)
	defer ts.Close()

	sessionID := initializeSession(t, ts.URL)
	postJSON(t, ts.URL+"/mcp", sessionID, "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund","_meta":{"progressToken":"tok-2"}}}`)

	params := decodeIntegrationParams(t, captured.Params)
	meta := decodeIntegrationMeta(t, params)
	assertIntegrationString(t, meta["progressToken"], "tok-2")
	assertIntegrationString(t, meta["sessionId"], sessionID)
	assertNonEmptyIntegrationString(t, meta["turnId"])
}

func TestGatewayIntegrationPropagatesUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if bytes.Contains(mustReadAll(t, r.Body), []byte(`"initialize"`)) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"server":"ok"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"bad params"}}`))
	}))
	defer upstream.Close()

	ts := newGatewayIntegrationServer(t, upstream.URL)
	defer ts.Close()

	sessionID := initializeSession(t, ts.URL)
	rec := postJSON(t, ts.URL+"/mcp", sessionID, "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund"}}`)

	var resp mcp.JSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32602 || resp.Error.Message != "bad params" {
		t.Fatalf("error response = %+v, want upstream error", resp.Error)
	}
}

func TestGatewayIntegrationReturnsInternalErrorWhenUpstreamUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	upstreamURL := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("Close listener: %v", err)
	}

	config := &Config{
		ListenPort:      8080,
		UpstreamMCPURL:  upstreamURL,
		TurnIDHeader:    defaultTurnIDHeader,
		UpstreamTimeout: time.Second,
		SessionTTL:      time.Minute,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	forwarder := mcp.NewUpstreamForwarder(upstreamURL, time.Second)
	pipeline := mcp.NewPipeline(forwarder)
	pipeline.Use(NewRequestLogger(logger))
	pipeline.Use(&ContextInjector{})
	server := NewServer(config, pipeline, logger)
	server.forwarder = forwarder
	sessionID := server.sessions.Create().ID
	ts := httptest.NewServer(server)
	defer ts.Close()

	rec := postJSON(t, ts.URL+"/mcp", sessionID, "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund"}}`)
	assertIntegrationErrorCode(t, rec, mcp.CodeInternalError)
}

func TestGatewayIntegrationReturnsParseErrorForMalformedBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"server":"ok"}}`))
	}))
	defer upstream.Close()

	ts := newGatewayIntegrationServer(t, upstream.URL)
	defer ts.Close()

	sessionID := initializeSession(t, ts.URL)
	rec := postRaw(t, ts.URL+"/mcp", sessionID, "", `{"jsonrpc":`)
	assertIntegrationErrorCode(t, rec, mcp.CodeParseError)
}

func TestGatewayIntegrationReturnsInvalidRequestForMissingSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"server":"ok"}}`))
	}))
	defer upstream.Close()

	ts := newGatewayIntegrationServer(t, upstream.URL)
	defer ts.Close()

	rec := postJSON(t, ts.URL+"/mcp", "", "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund"}}`)
	assertIntegrationErrorCode(t, rec, codeInvalidRequest)
}

func TestGatewayIntegrationSSEKeepalive(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"server":"ok"}}`))
	}))
	defer upstream.Close()

	ts := newGatewayIntegrationServer(t, upstream.URL)
	defer ts.Close()

	sessionID := initializeSession(t, ts.URL)

	originalInterval := keepaliveInterval
	keepaliveInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		keepaliveInterval = originalInterval
	})

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/mcp", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set(mcpSessionIDHeader, sessionID)

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /mcp: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString: %v", err)
	}
	if len(line) == 0 || line[0] != ':' {
		t.Fatalf("first SSE line = %q, want comment keepalive", line)
	}
}

func TestGatewayIntegrationPanicIsolation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if bytes.Contains(mustReadAll(t, r.Body), []byte(`"initialize"`)) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"server":"ok"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`))
	}))
	defer upstream.Close()

	var panics int32
	ts := newGatewayIntegrationServerWithExtraHandler(t, upstream.URL, mcp.HandlerFunc(func(ctx context.Context, req *mcp.JSONRPCRequest) (*mcp.JSONRPCResponse, error) {
		if atomic.AddInt32(&panics, 1) == 1 {
			panic("boom")
		}
		return nil, nil
	}))
	defer ts.Close()

	sessionID := initializeSession(t, ts.URL)

	first := postJSON(t, ts.URL+"/mcp", sessionID, "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund"}}`)
	assertIntegrationErrorCode(t, first, mcp.CodeInternalError)

	second := postJSON(t, ts.URL+"/mcp", sessionID, "", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"refund"}}`)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d", second.Code, http.StatusOK)
	}
	var resp mcp.JSONRPCResponse
	if err := json.Unmarshal(second.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode second response body: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("second response error = %+v, want nil", resp.Error)
	}
}

func TestGatewayIntegrationToolsCallCreatesAndReleasesSessionLock(t *testing.T) {
	ctx := context.Background()
	dsn := testSchemaDSN(t, testPostgresDSN(t))
	turnID := "turn-lock-check"

	releaseUpstream := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(releaseUpstream) }) }

	upstreamStarted := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcp.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Method == "initialize" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"server":"ok"}}`))
			return
		}
		close(upstreamStarted)
		select {
		case <-releaseUpstream:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`))
	}))
	defer func() {
		closeRelease()
		upstream.CloseClientConnections()
		upstream.Close()
	}()

	policyPath := writePolicyFile(t, `
defaultAction: allow
budgets:
  maxToolCallsPerTurn: 3
operationClasses:
  refund: read
rules:
  - tool: refund
    action: allow
`)

	config := &Config{
		ListenPort:         8080,
		PolicyFilePath:     policyPath,
		PostgresDSN:        dsn,
		RedisDSN:           testRedisDSN(t),
		UpstreamMCPURL:     upstream.URL,
		TurnIDHeader:       defaultTurnIDHeader,
		UpstreamTimeout:    time.Second,
		SessionTTL:         time.Minute,
		SessionLockTTL:     time.Minute,
		LockAcquireTimeout: 250 * time.Millisecond,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	server, cleanup, err := buildGatewayServer(ctx, config, logger)
	if err != nil {
		t.Fatalf("buildGatewayServer() error = %v, want nil", err)
	}
	t.Cleanup(cleanup)

	redisClient, err := NewRedisClient(*config)
	if err != nil {
		t.Fatalf("NewRedisClient() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		_ = redisClient.Close()
	})

	ts := httptest.NewServer(server)
	defer func() {
		ts.CloseClientConnections()
		ts.Close()
	}()

	sessionID := initializeSession(t, ts.URL)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- postJSON(t, ts.URL+"/mcp", sessionID, turnID, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund"}}`)
	}()

	select {
	case <-upstreamStarted:
	case <-time.After(time.Second):
		t.Fatal("tools/call did not reach upstream")
	}

	assertRedisStringValue(t, redisClient, sessionLockKey(sessionID), turnID)

	closeRelease()

	rec := <-done
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d, want %d", rec.Code, http.StatusOK)
	}

	assertRedisKeyAbsent(t, redisClient, sessionLockKey(sessionID))
}

func newGatewayIntegrationServer(t *testing.T, upstreamURL string) *httptest.Server {
	return newGatewayIntegrationServerWithExtraHandler(t, upstreamURL, nil)
}

func newGatewayIntegrationServerWithExtraHandler(t *testing.T, upstreamURL string, extra mcp.Handler) *httptest.Server {
	t.Helper()
	config := &Config{
		ListenPort:      8080,
		UpstreamMCPURL:  upstreamURL,
		TurnIDHeader:    defaultTurnIDHeader,
		UpstreamTimeout: time.Second,
		SessionTTL:      time.Minute,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	forwarder := mcp.NewUpstreamForwarder(upstreamURL, time.Second)
	pipeline := mcp.NewPipeline(forwarder)
	pipeline.Use(NewRequestLogger(logger))
	pipeline.Use(&ContextInjector{})
	if extra != nil {
		pipeline.Use(extra)
	}

	server := NewServer(config, pipeline, logger)
	server.forwarder = forwarder
	return httptest.NewServer(server)
}

func initializeSession(t *testing.T, baseURL string) string {
	t.Helper()
	rec := postJSON(t, baseURL+"/mcp", "", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"client":"test"}}`)
	sessionID := rec.Header().Get(mcpSessionIDHeader)
	if sessionID == "" {
		t.Fatal("initialize response missing Mcp-Session-Id header")
	}
	return sessionID
}

func postJSON(t *testing.T, url, sessionID, turnID, body string) *httptest.ResponseRecorder {
	t.Helper()
	return postRaw(t, url, sessionID, turnID, body)
}

func postRaw(t *testing.T, url, sessionID, turnID, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()

	client := &http.Client{}
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		httpReq.Header.Set(mcpSessionIDHeader, sessionID)
	}
	if turnID != "" {
		httpReq.Header.Set(defaultTurnIDHeader, turnID)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()

	rec.Code = resp.StatusCode
	for k, values := range resp.Header {
		for _, value := range values {
			rec.Header().Add(k, value)
		}
	}
	if _, err := io.Copy(rec.Body, resp.Body); err != nil {
		t.Fatalf("Copy response body: %v", err)
	}
	return rec
}

func decodeIntegrationParams(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var params map[string]json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("Unmarshal params: %v", err)
	}
	return params
}

func decodeIntegrationMeta(t *testing.T, params map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(params["_meta"], &meta); err != nil {
		t.Fatalf("Unmarshal _meta: %v", err)
	}
	return meta
}

func assertIntegrationString(t *testing.T, raw json.RawMessage, want string) {
	t.Helper()
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal string: %v", err)
	}
	if got != want {
		t.Fatalf("string value = %q, want %q", got, want)
	}
}

func assertNonEmptyIntegrationString(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal string: %v", err)
	}
	if got == "" {
		t.Fatal("string value = empty, want non-empty")
	}
}

func assertIntegrationErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want int) {
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

func mustReadAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return body
}
