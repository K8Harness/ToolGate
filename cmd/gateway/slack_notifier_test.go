package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- Helpers ---

// slackBlockPayload mirrors the minimal JSON structure we send to Slack chat.postMessage.
type slackBlockPayload struct {
	Channel string        `json:"channel"`
	Blocks  []interface{} `json:"blocks"`
}

// capturedSlackRequest holds the decoded request captured by the test server.
type capturedSlackRequest struct {
	authHeader string
	body       []byte
}

func newSlackTestServer(t *testing.T, statusCode int) (*httptest.Server, *capturedSlackRequest) {
	t.Helper()
	cap := &capturedSlackRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.authHeader = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		cap.body = body
		w.WriteHeader(statusCode)
		if statusCode == http.StatusOK {
			// Minimal Slack API success response
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func newTestSlackClient(t *testing.T, serverURL, botToken, channel string) *SlackClient {
	t.Helper()
	log := slog.Default()
	client := newSlackClientWithHTTP(botToken, channel, &http.Client{}, log)
	client.apiBaseURL = serverURL
	return client
}

func sampleTicketRecord() TicketRecord {
	return TicketRecord{
		SessionID: "sess-abc",
		TurnID:    "turn-xyz",
		ToolName:  "bash",
		Arguments: json.RawMessage(`{"command":"ls -la"}`),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
}

// --- Tests ---

// TestSlackClientSendsCorrectActionIDs verifies that the Block Kit message includes
// action_id values "approval_approve" and "approval_deny" on the buttons.
func TestSlackClientSendsCorrectActionIDs(t *testing.T) {
	t.Parallel()

	srv, cap := newSlackTestServer(t, http.StatusOK)
	client := newTestSlackClient(t, srv.URL, "xoxb-test-token", "C12345")
	ticket := sampleTicketRecord()
	ticketID := "ticket-001"

	if err := client.SendApprovalRequest(t.Context(), ticketID, ticket); err != nil {
		t.Fatalf("SendApprovalRequest() error = %v, want nil", err)
	}

	// Parse the sent body to inspect action IDs
	var payload map[string]interface{}
	if err := json.Unmarshal(cap.body, &payload); err != nil {
		t.Fatalf("could not parse request body: %v\nbody: %s", err, string(cap.body))
	}

	blocks, ok := payload["blocks"].([]interface{})
	if !ok || len(blocks) < 2 {
		t.Fatalf("expected at least 2 blocks, got: %v", payload["blocks"])
	}

	actionsBlock, ok := blocks[1].(map[string]interface{})
	if !ok {
		t.Fatalf("blocks[1] is not an object: %T", blocks[1])
	}
	if actionsBlock["type"] != "actions" {
		t.Fatalf("blocks[1].type = %q, want %q", actionsBlock["type"], "actions")
	}

	elements, ok := actionsBlock["elements"].([]interface{})
	if !ok || len(elements) < 2 {
		t.Fatalf("expected 2 button elements, got: %v", actionsBlock["elements"])
	}

	approveBtn, ok := elements[0].(map[string]interface{})
	if !ok {
		t.Fatal("approve button is not a map")
	}
	if approveBtn["action_id"] != "approval_approve" {
		t.Errorf("approve button action_id = %q, want %q", approveBtn["action_id"], "approval_approve")
	}

	denyBtn, ok := elements[1].(map[string]interface{})
	if !ok {
		t.Fatal("deny button is not a map")
	}
	if denyBtn["action_id"] != "approval_deny" {
		t.Errorf("deny button action_id = %q, want %q", denyBtn["action_id"], "approval_deny")
	}
}

// TestSlackClientButtonValueIsTicketID verifies that both buttons carry the ticket ID as value.
func TestSlackClientButtonValueIsTicketID(t *testing.T) {
	t.Parallel()

	srv, cap := newSlackTestServer(t, http.StatusOK)
	client := newTestSlackClient(t, srv.URL, "xoxb-test-token", "C12345")
	ticket := sampleTicketRecord()
	ticketID := "ticket-val-002"

	if err := client.SendApprovalRequest(t.Context(), ticketID, ticket); err != nil {
		t.Fatalf("SendApprovalRequest() error = %v, want nil", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(cap.body, &payload); err != nil {
		t.Fatalf("could not parse request body: %v", err)
	}

	blocks := payload["blocks"].([]interface{})
	actionsBlock := blocks[1].(map[string]interface{})
	elements := actionsBlock["elements"].([]interface{})

	for i, elem := range elements {
		btn := elem.(map[string]interface{})
		if btn["value"] != ticketID {
			t.Errorf("button[%d].value = %q, want %q", i, btn["value"], ticketID)
		}
	}
}

// TestSlackClientSendsAuthorizationHeader verifies the Authorization: Bearer <botToken> header.
func TestSlackClientSendsAuthorizationHeader(t *testing.T) {
	t.Parallel()

	srv, cap := newSlackTestServer(t, http.StatusOK)
	botToken := "xoxb-my-secret-token"
	client := newTestSlackClient(t, srv.URL, botToken, "C12345")
	ticket := sampleTicketRecord()

	if err := client.SendApprovalRequest(t.Context(), "ticket-hdr-003", ticket); err != nil {
		t.Fatalf("SendApprovalRequest() error = %v, want nil", err)
	}

	want := "Bearer " + botToken
	if cap.authHeader != want {
		t.Errorf("Authorization header = %q, want %q", cap.authHeader, want)
	}
}

// TestSlackClientNon200ReturnsWrappedError verifies that a non-200 response from Slack
// results in a wrapped error being returned.
func TestSlackClientNon200ReturnsWrappedError(t *testing.T) {
	t.Parallel()

	srv, _ := newSlackTestServer(t, http.StatusInternalServerError)
	client := newTestSlackClient(t, srv.URL, "xoxb-test-token", "C12345")
	ticket := sampleTicketRecord()

	err := client.SendApprovalRequest(t.Context(), "ticket-err-004", ticket)
	if err == nil {
		t.Fatal("SendApprovalRequest() error = nil, want wrapped error for non-200 status")
	}
}

// TestSlackClientTruncatesLongArguments verifies that arguments > 2000 chars are truncated
// so the Slack block text stays within limits.
func TestSlackClientTruncatesLongArguments(t *testing.T) {
	t.Parallel()

	srv, cap := newSlackTestServer(t, http.StatusOK)
	client := newTestSlackClient(t, srv.URL, "xoxb-test-token", "C12345")

	// Build a very large arguments JSON value (> 3000 chars)
	longArgs := `{"command":"` + strings.Repeat("a", 3100) + `"}`
	ticket := TicketRecord{
		SessionID: "sess-trunc",
		TurnID:    "turn-trunc",
		ToolName:  "bash",
		Arguments: json.RawMessage(longArgs),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	if err := client.SendApprovalRequest(t.Context(), "ticket-trunc-005", ticket); err != nil {
		t.Fatalf("SendApprovalRequest() error = %v, want nil", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(cap.body, &payload); err != nil {
		t.Fatalf("could not parse request body: %v", err)
	}

	blocks := payload["blocks"].([]interface{})
	sectionBlock, ok := blocks[0].(map[string]interface{})
	if !ok {
		t.Fatal("blocks[0] is not an object")
	}
	textObj, ok := sectionBlock["text"].(map[string]interface{})
	if !ok {
		t.Fatal("blocks[0].text is not an object")
	}
	text, ok := textObj["text"].(string)
	if !ok {
		t.Fatal("blocks[0].text.text is not a string")
	}

	// The raw long args string should NOT appear verbatim; total text should be well under Slack's 3000-char limit
	if len(text) > 3000 {
		t.Errorf("section text length = %d, want <= 3000 chars (Slack block limit)", len(text))
	}
}

// TestSlackClientSectionBlockContainsToolDetails verifies the section block includes
// tool name, arguments, and session ID — and does NOT duplicate ToolName on a
// separate *Operation:* line.
func TestSlackClientSectionBlockContainsToolDetails(t *testing.T) {
	t.Parallel()

	srv, cap := newSlackTestServer(t, http.StatusOK)
	client := newTestSlackClient(t, srv.URL, "xoxb-test-token", "C12345")
	ticket := sampleTicketRecord()
	ticketID := "ticket-section-006"

	if err := client.SendApprovalRequest(t.Context(), ticketID, ticket); err != nil {
		t.Fatalf("SendApprovalRequest() error = %v, want nil", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(cap.body, &payload); err != nil {
		t.Fatalf("could not parse request body: %v", err)
	}

	blocks := payload["blocks"].([]interface{})
	sectionBlock := blocks[0].(map[string]interface{})
	if sectionBlock["type"] != "section" {
		t.Errorf("blocks[0].type = %q, want %q", sectionBlock["type"], "section")
	}

	textObj := sectionBlock["text"].(map[string]interface{})
	if textObj["type"] != "mrkdwn" {
		t.Errorf("blocks[0].text.type = %q, want %q", textObj["type"], "mrkdwn")
	}

	text := textObj["text"].(string)

	// Tool name, arguments, and session ID must all appear.
	checks := []struct {
		field string
		value string
	}{
		{"ToolName", ticket.ToolName},
		{"Arguments", `"command":"ls -la"`},
		{"SessionID", ticket.SessionID},
	}
	for _, c := range checks {
		if !strings.Contains(text, c.value) {
			t.Errorf("section text missing %s %q\ntext: %s", c.field, c.value, text)
		}
	}

	// The *Operation:* label must not appear — ToolName already conveys the
	// operation; a separate *Operation:* line would only duplicate it.
	if strings.Contains(text, "*Operation:*") {
		t.Errorf("section text contains redundant *Operation:* label\ntext: %s", text)
	}

	// ToolName should appear exactly once (under the *Tool:* label).
	if count := strings.Count(text, ticket.ToolName); count != 1 {
		t.Errorf("ToolName %q appears %d time(s) in section text, want exactly 1\ntext: %s",
			ticket.ToolName, count, text)
	}
}

// TestSlackClientSendsToConfiguredChannel verifies the channel field in the payload.
func TestSlackClientSendsToConfiguredChannel(t *testing.T) {
	t.Parallel()

	srv, cap := newSlackTestServer(t, http.StatusOK)
	channel := "C-MY-CHANNEL"
	client := newTestSlackClient(t, srv.URL, "xoxb-test-token", channel)
	ticket := sampleTicketRecord()

	if err := client.SendApprovalRequest(t.Context(), "ticket-ch-007", ticket); err != nil {
		t.Fatalf("SendApprovalRequest() error = %v, want nil", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(cap.body, &payload); err != nil {
		t.Fatalf("could not parse request body: %v", err)
	}

	if payload["channel"] != channel {
		t.Errorf("payload.channel = %q, want %q", payload["channel"], channel)
	}
}

// TestSlackClientHTTPClientError verifies that a failed HTTP request returns an error.
func TestSlackClientHTTPClientError(t *testing.T) {
	t.Parallel()

	// Use an invalid URL that will fail to connect
	log := slog.Default()
	client := newSlackClientWithHTTP("xoxb-token", "C12345", &http.Client{}, log)
	client.apiBaseURL = "http://127.0.0.1:0" // No listener — connection refused

	ticket := sampleTicketRecord()
	err := client.SendApprovalRequest(t.Context(), "ticket-httperr-008", ticket)
	if err == nil {
		t.Fatal("SendApprovalRequest() error = nil, want error for failed HTTP request")
	}
}

// TestNewSlackClientReturnsSensibleDefaults ensures NewSlackClient sets botToken and channel.
func TestNewSlackClientReturnsSensibleDefaults(t *testing.T) {
	t.Parallel()

	botToken := "xoxb-new-client"
	channel := "C-NEW"
	log := slog.Default()
	client := NewSlackClient(botToken, channel, slackAPIBaseURL, log)

	if client.botToken != botToken {
		t.Errorf("client.botToken = %q, want %q", client.botToken, botToken)
	}
	if client.channel != channel {
		t.Errorf("client.channel = %q, want %q", client.channel, channel)
	}
	if client.httpClient == nil {
		t.Error("client.httpClient = nil, want a default *http.Client")
	}
}
