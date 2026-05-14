package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/K8Harness/ToolGate/core/mcp"
)

func TestRequestLoggerToolsCallLogsSafeStructuredFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewRequestLogger(slog.New(slog.NewJSONHandler(&buf, nil)))
	req := &mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params: json.RawMessage(`{
			"name":"refund",
			"arguments":{"token":"secret-token","amount":10}
		}`),
	}

	resp, err := logger.Handle(contextWithLoggerIDs("session-1", "turn-1"), req)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if resp != nil {
		t.Fatalf("Handle response = %#v, want nil", resp)
	}

	line := singleLogLine(t, &buf)
	entry := decodeLogEntry(t, line)

	assertLogString(t, entry, "sessionId", "session-1")
	assertLogString(t, entry, "turnId", "turn-1")
	assertLogString(t, entry, "method", "tools/call")
	assertLogString(t, entry, "toolName", "refund")
	if _, ok := entry["arguments"]; ok {
		t.Fatalf("log entry unexpectedly contains arguments key: %v", entry)
	}
	if strings.Contains(line, "arguments") {
		t.Fatalf("log line unexpectedly contains arguments key: %s", line)
	}
	if strings.Contains(line, "secret-token") {
		t.Fatalf("log line unexpectedly contains argument value: %s", line)
	}
}

func TestRequestLoggerNonToolsCallOmitsToolName(t *testing.T) {
	var buf bytes.Buffer
	logger := NewRequestLogger(slog.New(slog.NewJSONHandler(&buf, nil)))
	req := &mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
		Params:  json.RawMessage(`{"cursor":"page-1"}`),
	}

	resp, err := logger.Handle(contextWithLoggerIDs("session-2", "turn-2"), req)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if resp != nil {
		t.Fatalf("Handle response = %#v, want nil", resp)
	}

	entry := decodeLogEntry(t, singleLogLine(t, &buf))
	assertLogString(t, entry, "sessionId", "session-2")
	assertLogString(t, entry, "turnId", "turn-2")
	assertLogString(t, entry, "method", "tools/list")
	if _, ok := entry["toolName"]; ok {
		t.Fatalf("log entry unexpectedly contains toolName for non-tools/call request: %v", entry)
	}
}

func contextWithLoggerIDs(sessionID, turnID string) context.Context {
	ctx := mcp.WithSessionID(context.Background(), sessionID)
	return mcp.WithTurnID(ctx, turnID)
}

func singleLogLine(t *testing.T, buf *bytes.Buffer) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("captured %d log lines, want exactly 1: %q", len(lines), buf.String())
	}
	return lines[0]
}

func decodeLogEntry(t *testing.T, line string) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("Unmarshal log entry %q: %v", line, err)
	}
	return entry
}

func assertLogString(t *testing.T, entry map[string]any, key, want string) {
	t.Helper()
	got, ok := entry[key]
	if !ok {
		t.Fatalf("log entry missing %q: %v", key, entry)
	}
	gotString, ok := got.(string)
	if !ok {
		t.Fatalf("log entry %q has type %T, want string", key, got)
	}
	if gotString != want {
		t.Fatalf("log entry %q = %q, want %q", key, gotString, want)
	}
}
