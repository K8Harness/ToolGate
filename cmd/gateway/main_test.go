package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/K8Harness/ToolGate/core/mcp"
)

func TestRunGatewayReturnsErrorWhenConfigMissing(t *testing.T) {
	t.Setenv("UPSTREAM_MCP_URL", "")

	var stderr bytes.Buffer
	code := runGateway(&stderr)

	if code != 1 {
		t.Fatalf("runGateway() code = %d, want 1", code)
	}
	if got := stderr.String(); got == "" {
		t.Fatal("stderr = empty, want config error output")
	}
}

func TestRunGatewayReturnsErrorWhenRedisDSNMissing(t *testing.T) {
	t.Setenv("UPSTREAM_MCP_URL", "http://example.invalid")
	t.Setenv("POSTGRES_DSN", "postgres://localhost:5432/toolgate?sslmode=disable")
	t.Setenv("REDIS_DSN", "")

	var stderr bytes.Buffer
	code := runGateway(&stderr)

	if code != 1 {
		t.Fatalf("runGateway() code = %d, want 1", code)
	}
	if got := stderr.String(); !strings.Contains(got, "REDIS_DSN") {
		t.Fatalf("stderr = %q, want missing REDIS_DSN error", got)
	}
}

func TestRunGatewayFatalfsWhenPolicyLoadFails(t *testing.T) {
	t.Setenv("UPSTREAM_MCP_URL", "http://example.invalid")
	t.Setenv("POSTGRES_DSN", "postgres://localhost:5432/toolgate?sslmode=disable")
	t.Setenv("REDIS_DSN", "redis://localhost:6379/0")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test-token")
	t.Setenv("SLACK_SIGNING_SECRET", "test-signing-secret")
	t.Setenv("SLACK_CHANNEL", "#approvals")
	t.Setenv("POLICY_FILE", filepath.Join(t.TempDir(), "missing-policy.yaml"))

	message := interceptFatalf(t, func() {
		runGateway(io.Discard)
	})

	if !strings.Contains(message, "policy load failed") {
		t.Fatalf("fatal message = %q, want failed policy load check", message)
	}
	if !strings.Contains(message, "missing-policy.yaml") {
		t.Fatalf("fatal message = %q, want missing policy file path", message)
	}
}

func TestRunGatewayFatalfsWhenPolicyYAMLIsInvalid(t *testing.T) {
	t.Setenv("UPSTREAM_MCP_URL", "http://example.invalid")
	t.Setenv("POSTGRES_DSN", "postgres://localhost:5432/toolgate?sslmode=disable")
	t.Setenv("REDIS_DSN", "redis://localhost:6379/0")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test-token")
	t.Setenv("SLACK_SIGNING_SECRET", "test-signing-secret")
	t.Setenv("SLACK_CHANNEL", "#approvals")
	t.Setenv("POLICY_FILE", writePolicyFile(t, "rules: ["))

	message := interceptFatalf(t, func() {
		runGateway(io.Discard)
	})

	if !strings.Contains(message, "policy load failed") {
		t.Fatalf("fatal message = %q, want failed policy load check", message)
	}
	if !strings.Contains(message, "decode policy") {
		t.Fatalf("fatal message = %q, want decode failure detail", message)
	}
}

func TestRunGatewayFatalfsWhenPostgresInitFails(t *testing.T) {
	t.Setenv("UPSTREAM_MCP_URL", "http://example.invalid")
	t.Setenv("POSTGRES_DSN", "postgres://127.0.0.1:1/toolgate?sslmode=disable&connect_timeout=1")
	t.Setenv("REDIS_DSN", testRedisDSN(t))
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test-token")
	t.Setenv("SLACK_SIGNING_SECRET", "test-signing-secret")
	t.Setenv("SLACK_CHANNEL", "#approvals")
	t.Setenv("POLICY_FILE", writePolicyFile(t, `
defaultAction: deny
budgets:
  maxToolCallsPerTurn: 3
rules:
  - tool: refund
    action: allow
`))

	message := interceptFatalf(t, func() {
		runGateway(io.Discard)
	})

	if !strings.Contains(message, "postgres initialization failed") {
		t.Fatalf("fatal message = %q, want postgres initialization failure", message)
	}
	if !strings.Contains(message, "ping postgres") {
		t.Fatalf("fatal message = %q, want ping failure detail", message)
	}
}

func TestBuildGatewayServerFailsWhenRedisInitFails(t *testing.T) {
	ctx := context.Background()
	policyPath := writePolicyFile(t, `
defaultAction: allow
budgets:
  maxToolCallsPerTurn: 3
rules:
  - tool: refund
    action: allow
`)

	config := &Config{
		ListenPort:      8080,
		PolicyFilePath:  policyPath,
		PostgresDSN:     "postgres://127.0.0.1:1/toolgate?sslmode=disable&connect_timeout=1",
		RedisDSN:        "redis://127.0.0.1:1/0",
		UpstreamMCPURL:  "http://example.invalid",
		TurnIDHeader:    defaultTurnIDHeader,
		UpstreamTimeout: time.Second,
		SessionTTL:      time.Minute,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	server, cleanup, err := buildGatewayServer(ctx, config, logger)
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil when Redis init fails")
	}
	if server != nil {
		t.Fatal("server != nil, want nil when Redis init fails")
	}
	if err == nil {
		t.Fatal("buildGatewayServer() error = nil, want redis initialization failure")
	}
	if !strings.Contains(err.Error(), "redis initialization failed") {
		t.Fatalf("error = %q, want redis initialization context", err)
	}
}

func TestNewGatewayServerBuildsPipelineAndForwarder(t *testing.T) {
	config := &Config{
		ListenPort:      8080,
		RedisDSN:        "redis://localhost:6379/0",
		UpstreamMCPURL:  "http://example.invalid",
		TurnIDHeader:    defaultTurnIDHeader,
		UpstreamTimeout: time.Second,
		SessionTTL:      time.Minute,
	}

	server := newGatewayServer(config)

	if server == nil {
		t.Fatal("newGatewayServer() = nil, want server")
	}
	if server.pipeline == nil {
		t.Fatal("server.pipeline = nil, want pipeline")
	}
	if server.forwarder == nil {
		t.Fatal("server.forwarder = nil, want upstream forwarder")
	}
	if server.log == nil {
		t.Fatal("server.log = nil, want logger")
	}
}

func TestBuildGatewayServerRegistersPolicyGateBeforeForwarder(t *testing.T) {
	ctx := context.Background()
	dsn := testSchemaDSN(t, testPostgresDSN(t))

	policyPath := writePolicyFile(t, `
defaultAction: allow
budgets:
  maxToolCallsPerTurn: 3
rules:
  - tool: delete_record
    action: deny
`)

	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`))
	}))
	defer upstream.Close()

	config := &Config{
		ListenPort:      8080,
		PolicyFilePath:  policyPath,
		PostgresDSN:     dsn,
		RedisDSN:        testRedisDSN(t),
		UpstreamMCPURL:  upstream.URL,
		TurnIDHeader:    defaultTurnIDHeader,
		UpstreamTimeout: time.Second,
		SessionTTL:      time.Minute,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	server, cleanup, err := buildGatewayServer(ctx, config, logger)
	if err != nil {
		t.Fatalf("buildGatewayServer() error = %v, want nil", err)
	}
	t.Cleanup(cleanup)

	sessionID := server.sessions.Create().ID
	ts := httptest.NewServer(server)
	defer ts.Close()

	rec := postJSON(t, ts.URL+"/mcp", sessionID, "turn-1", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"delete_record","arguments":{"id":"abc"}}}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp mcp.JSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("response error = nil, want policy deny error")
	}
	if resp.Error.Code != mcp.CodePolicyDenied {
		t.Fatalf("error.code = %d, want %d", resp.Error.Code, mcp.CodePolicyDenied)
	}
	if resp.Error.Message != "denied by policy" {
		t.Fatalf("error.message = %q, want denied by policy", resp.Error.Message)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
	}
}

func TestBuildGatewayServerRegistersSlackWebhookRoute(t *testing.T) {
	ctx := context.Background()
	dsn := testSchemaDSN(t, testPostgresDSN(t))

	policyPath := writePolicyFile(t, `
defaultAction: allow
budgets:
  maxToolCallsPerTurn: 3
`)

	config := &Config{
		ListenPort:         8080,
		PolicyFilePath:     policyPath,
		PostgresDSN:        dsn,
		RedisDSN:           testRedisDSN(t),
		UpstreamMCPURL:     "http://example.invalid",
		TurnIDHeader:       defaultTurnIDHeader,
		UpstreamTimeout:    time.Second,
		SessionTTL:         time.Minute,
		SessionLockTTL:     time.Minute,
		LockAcquireTimeout: 250 * time.Millisecond,
		SlackBotToken:      "xoxb-test-token",
		SlackSigningSecret: "test-signing-secret",
		SlackChannel:       "#approvals",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	server, cleanup, err := buildGatewayServer(ctx, config, logger)
	if err != nil {
		t.Fatalf("buildGatewayServer() error = %v, want nil", err)
	}
	t.Cleanup(cleanup)

	ts := httptest.NewServer(server)
	defer ts.Close()

	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader("payload=%7B%7D"))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /slack/actions status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	resp, err := http.Post(ts.URL+"/slack/actions", "application/x-www-form-urlencoded", strings.NewReader("payload=%7B%7D"))
	if err != nil {
		t.Fatalf("POST /slack/actions via httptest server: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("network POST /slack/actions status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestBuildGatewayServerWiresConcurrencyGuard(t *testing.T) {
	ctx := context.Background()
	dsn := testSchemaDSN(t, testPostgresDSN(t))

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

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`))
	}))
	defer upstream.Close()

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

	if server.guard == nil {
		t.Fatal("server.guard = nil, want ConcurrencyGuard")
	}
	if server.guard.locker == nil {
		t.Fatal("server.guard.locker = nil, want SessionLocker")
	}
	if server.guard.rwlock == nil {
		t.Fatal("server.guard.rwlock = nil, want TurnRWLock")
	}
	if server.guard.classifier == nil {
		t.Fatal("server.guard.classifier = nil, want OperationClassifier")
	}
	if got := server.guard.classifier.Classify("refund"); got != OperationClassRead {
		t.Fatalf("classifier.Classify(%q) = %v, want %v", "refund", got, OperationClassRead)
	}
}

func interceptFatalf(t *testing.T, fn func()) (message string) {
	t.Helper()

	original := logFatalf
	t.Cleanup(func() {
		logFatalf = original
	})

	logFatalf = func(format string, args ...any) {
		message = formatMessage(format, args...)
		panic(errFatalfIntercepted)
	}

	defer func() {
		recovered := recover()
		if !errors.Is(asError(recovered), errFatalfIntercepted) {
			t.Fatalf("panic = %v, want fatalf interception", recovered)
		}
	}()

	fn()
	t.Fatal("runGateway() returned without calling logFatalf")
	return message
}

func writePolicyFile(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(contents)+"\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
	return path
}

func formatMessage(format string, args ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}

var errFatalfIntercepted = errors.New("log fatalf intercepted")

func asError(v any) error {
	if v == nil {
		return nil
	}
	err, _ := v.(error)
	return err
}
