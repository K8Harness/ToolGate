package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func extractPayload(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()

	if len(result.Content) != 1 {
		t.Fatalf("content len = %d, want 1", len(result.Content))
	}

	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *mcp.TextContent", result.Content[0])
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(text.Text), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	return payload
}

func TestRefundSmallHandler(t *testing.T) {
	result, _, err := refundSmallHandler(context.Background(), nil, RefundParams{
		Amount:     50,
		CustomerID: "cust_001",
	})
	if err != nil {
		t.Fatalf("refundSmallHandler() error = %v", err)
	}

	payload := extractPayload(t, result)
	if got, want := payload["tool"], "refund_small"; got != want {
		t.Fatalf("payload tool = %v, want %q", got, want)
	}
	if got, want := payload["ok"], true; got != want {
		t.Fatalf("payload ok = %v, want %v", got, want)
	}
}

func TestSendSlackMessageHandler(t *testing.T) {
	result, _, err := sendSlackMessageHandler(context.Background(), nil, SendSlackMessageParams{
		Channel: "#support",
		Message: "***REDACTED***",
	})
	if err != nil {
		t.Fatalf("sendSlackMessageHandler() error = %v", err)
	}

	payload := extractPayload(t, result)
	if got, want := payload["tool"], "send_lark_message"; got != want {
		t.Fatalf("payload tool = %v, want %q", got, want)
	}
	if got, want := payload["message"], "***REDACTED***"; got != want {
		t.Fatalf("payload message = %v, want %q", got, want)
	}
}

