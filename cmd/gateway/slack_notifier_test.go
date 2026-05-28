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

// capturedLarkRequests holds requests captured by the two-endpoint test server.
type capturedLarkRequests struct {
	tokenBody []byte
	msgAuth   string
	msgBody   []byte
}

// newLarkTestServer creates a test server that handles both the token endpoint and
// the message endpoint, capturing requests for assertion.
func newLarkTestServer(t *testing.T, msgStatusCode int) (*httptest.Server, *capturedLarkRequests) {
	t.Helper()
	cap := &capturedLarkRequests{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/auth/v3/tenant_access_token/internal":
			cap.tokenBody = body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"mock-token","expire":7200}`))
		case "/im/v1/messages":
			cap.msgAuth = r.Header.Get("Authorization")
			cap.msgBody = body
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(msgStatusCode)
			if msgStatusCode == http.StatusOK {
				_, _ = w.Write([]byte(`{"code":0}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func newTestLarkClient(t *testing.T, serverURL, appID, appSecret, chatID string) *LarkClient {
	t.Helper()
	return newLarkClientWithHTTP(appID, appSecret, chatID, serverURL, &http.Client{}, slog.Default())
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

// TestLarkClientFetchesTokenBeforeSendingMessage verifies the token endpoint is called
// and the resulting token is used in the Authorization header.
func TestLarkClientFetchesTokenBeforeSendingMessage(t *testing.T) {
	t.Parallel()

	srv, cap := newLarkTestServer(t, http.StatusOK)
	client := newTestLarkClient(t, srv.URL, "cli_app", "app_secret", "oc_chat")
	ticket := sampleTicketRecord()

	if err := client.SendApprovalRequest(t.Context(), "ticket-001", ticket); err != nil {
		t.Fatalf("SendApprovalRequest() error = %v, want nil", err)
	}

	if cap.tokenBody == nil {
		t.Fatal("token endpoint was not called")
	}
	var tokenReq map[string]string
	if err := json.Unmarshal(cap.tokenBody, &tokenReq); err != nil {
		t.Fatalf("parse token request body: %v", err)
	}
	if tokenReq["app_id"] != "cli_app" {
		t.Errorf("token request app_id = %q, want %q", tokenReq["app_id"], "cli_app")
	}
	if cap.msgAuth != "Bearer mock-token" {
		t.Errorf("message Authorization = %q, want %q", cap.msgAuth, "Bearer mock-token")
	}
}

// TestLarkClientSendsCorrectChatID verifies receive_id in the message payload equals the chatID.
func TestLarkClientSendsCorrectChatID(t *testing.T) {
	t.Parallel()

	srv, cap := newLarkTestServer(t, http.StatusOK)
	chatID := "oc_my_channel"
	client := newTestLarkClient(t, srv.URL, "cli_app", "secret", chatID)
	ticket := sampleTicketRecord()

	if err := client.SendApprovalRequest(t.Context(), "ticket-002", ticket); err != nil {
		t.Fatalf("SendApprovalRequest() error = %v, want nil", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(cap.msgBody, &payload); err != nil {
		t.Fatalf("parse message body: %v", err)
	}
	if payload["receive_id"] != chatID {
		t.Errorf("receive_id = %q, want %q", payload["receive_id"], chatID)
	}
	if payload["msg_type"] != "interactive" {
		t.Errorf("msg_type = %q, want %q", payload["msg_type"], "interactive")
	}
}

// TestLarkClientCardContainsToolDetails verifies the card content includes tool, args, session.
func TestLarkClientCardContainsToolDetails(t *testing.T) {
	t.Parallel()

	srv, cap := newLarkTestServer(t, http.StatusOK)
	client := newTestLarkClient(t, srv.URL, "cli_app", "secret", "oc_chat")
	ticket := sampleTicketRecord()
	ticketID := "ticket-003"

	if err := client.SendApprovalRequest(t.Context(), ticketID, ticket); err != nil {
		t.Fatalf("SendApprovalRequest() error = %v, want nil", err)
	}

	// The content field is a JSON string containing the card JSON.
	var msgPayload map[string]interface{}
	if err := json.Unmarshal(cap.msgBody, &msgPayload); err != nil {
		t.Fatalf("parse message body: %v", err)
	}
	contentStr, ok := msgPayload["content"].(string)
	if !ok {
		t.Fatalf("content field is not a string: %T", msgPayload["content"])
	}

	var card map[string]interface{}
	if err := json.Unmarshal([]byte(contentStr), &card); err != nil {
		t.Fatalf("parse card JSON: %v", err)
	}

	elements, ok := card["elements"].([]interface{})
	if !ok || len(elements) < 2 {
		t.Fatalf("expected at least 2 card elements, got: %v", card["elements"])
	}

	divBlock, ok := elements[0].(map[string]interface{})
	if !ok {
		t.Fatalf("elements[0] is not an object: %T", elements[0])
	}
	textObj, ok := divBlock["text"].(map[string]interface{})
	if !ok {
		t.Fatalf("elements[0].text is not an object: %T", divBlock["text"])
	}
	textContent, _ := textObj["content"].(string)

	for _, want := range []string{ticket.ToolName, "ls -la", ticket.SessionID} {
		if !strings.Contains(textContent, want) {
			t.Errorf("card text missing %q\ntext: %s", want, textContent)
		}
	}
}

// TestLarkClientButtonValuesContainTicketID verifies Approve/Deny buttons embed the ticketID.
func TestLarkClientButtonValuesContainTicketID(t *testing.T) {
	t.Parallel()

	srv, cap := newLarkTestServer(t, http.StatusOK)
	client := newTestLarkClient(t, srv.URL, "cli_app", "secret", "oc_chat")
	ticket := sampleTicketRecord()
	ticketID := "ticket-val-004"

	if err := client.SendApprovalRequest(t.Context(), ticketID, ticket); err != nil {
		t.Fatalf("SendApprovalRequest() error = %v, want nil", err)
	}

	var msgPayload map[string]interface{}
	_ = json.Unmarshal(cap.msgBody, &msgPayload)
	contentStr, _ := msgPayload["content"].(string)
	var card map[string]interface{}
	_ = json.Unmarshal([]byte(contentStr), &card)
	elements := card["elements"].([]interface{})
	actionBlock := elements[1].(map[string]interface{})
	actions := actionBlock["actions"].([]interface{})

	if len(actions) < 2 {
		t.Fatalf("expected 2 buttons, got %d", len(actions))
	}
	for i, a := range actions {
		btn := a.(map[string]interface{})
		value, _ := btn["value"].(map[string]interface{})
		if value["ticket_id"] != ticketID {
			t.Errorf("button[%d].value.ticket_id = %q, want %q", i, value["ticket_id"], ticketID)
		}
	}
}

// TestLarkClientButtonActionsAreApproveAndDeny verifies button values carry correct action names.
func TestLarkClientButtonActionsAreApproveAndDeny(t *testing.T) {
	t.Parallel()

	srv, cap := newLarkTestServer(t, http.StatusOK)
	client := newTestLarkClient(t, srv.URL, "cli_app", "secret", "oc_chat")
	ticket := sampleTicketRecord()

	if err := client.SendApprovalRequest(t.Context(), "ticket-005", ticket); err != nil {
		t.Fatalf("SendApprovalRequest() error = %v, want nil", err)
	}

	var msgPayload map[string]interface{}
	_ = json.Unmarshal(cap.msgBody, &msgPayload)
	contentStr, _ := msgPayload["content"].(string)
	var card map[string]interface{}
	_ = json.Unmarshal([]byte(contentStr), &card)
	elements := card["elements"].([]interface{})
	actionBlock := elements[1].(map[string]interface{})
	actions := actionBlock["actions"].([]interface{})

	approve := actions[0].(map[string]interface{})
	approveValue, _ := approve["value"].(map[string]interface{})
	if approveValue["action"] != "approve" {
		t.Errorf("button[0].value.action = %q, want %q", approveValue["action"], "approve")
	}

	deny := actions[1].(map[string]interface{})
	denyValue, _ := deny["value"].(map[string]interface{})
	if denyValue["action"] != "deny" {
		t.Errorf("button[1].value.action = %q, want %q", denyValue["action"], "deny")
	}
}

// TestLarkClientNon200MessageResponseReturnsError verifies a non-200 from the message endpoint.
func TestLarkClientNon200MessageResponseReturnsError(t *testing.T) {
	t.Parallel()

	srv, _ := newLarkTestServer(t, http.StatusInternalServerError)
	client := newTestLarkClient(t, srv.URL, "cli_app", "secret", "oc_chat")

	err := client.SendApprovalRequest(t.Context(), "ticket-006", sampleTicketRecord())
	if err == nil {
		t.Fatal("SendApprovalRequest() error = nil, want error for non-200 message response")
	}
}

// TestLarkClientTokenFetchFailureReturnsError verifies token endpoint failure propagates.
func TestLarkClientTokenFetchFailureReturnsError(t *testing.T) {
	t.Parallel()

	// Server that returns a non-200 on token endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	client := newLarkClientWithHTTP("bad_id", "bad_secret", "oc_chat", srv.URL, &http.Client{}, slog.Default())
	err := client.SendApprovalRequest(t.Context(), "ticket-007", sampleTicketRecord())
	if err == nil {
		t.Fatal("SendApprovalRequest() error = nil, want error for token fetch failure")
	}
}

// TestLarkClientTruncatesLongArguments verifies args longer than the limit are truncated.
func TestLarkClientTruncatesLongArguments(t *testing.T) {
	t.Parallel()

	srv, cap := newLarkTestServer(t, http.StatusOK)
	client := newTestLarkClient(t, srv.URL, "cli_app", "secret", "oc_chat")

	longArgs := `{"command":"` + strings.Repeat("a", 3100) + `"}`
	ticket := TicketRecord{
		SessionID: "sess-trunc",
		TurnID:    "turn-trunc",
		ToolName:  "bash",
		Arguments: json.RawMessage(longArgs),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	if err := client.SendApprovalRequest(t.Context(), "ticket-008", ticket); err != nil {
		t.Fatalf("SendApprovalRequest() error = %v, want nil", err)
	}

	var msgPayload map[string]interface{}
	_ = json.Unmarshal(cap.msgBody, &msgPayload)
	contentStr, _ := msgPayload["content"].(string)
	if strings.Contains(contentStr, strings.Repeat("a", 3100)) {
		t.Error("card content contains untruncated long argument, want truncated")
	}
	if !strings.Contains(contentStr, larkArgsTruncateMark) {
		t.Error("card content missing truncation mark")
	}
}

// TestNewLarkClientStoresCredentials verifies the constructor stores credentials correctly.
func TestNewLarkClientStoresCredentials(t *testing.T) {
	t.Parallel()

	client := NewLarkClient("cli_my_app", "my_secret", "oc_my_chat", larkAPIBaseURL, slog.Default())
	if client.appID != "cli_my_app" {
		t.Errorf("appID = %q, want %q", client.appID, "cli_my_app")
	}
	if client.appSecret != "my_secret" {
		t.Errorf("appSecret = %q, want %q", client.appSecret, "my_secret")
	}
	if client.chatID != "oc_my_chat" {
		t.Errorf("chatID = %q, want %q", client.chatID, "oc_my_chat")
	}
	if client.httpClient == nil {
		t.Error("httpClient = nil, want a default *http.Client")
	}
}

// TestLarkClientHTTPClientError verifies that a connection failure returns a wrapped error.
func TestLarkClientHTTPClientError(t *testing.T) {
	t.Parallel()

	client := newLarkClientWithHTTP("id", "secret", "chat", "http://127.0.0.1:0", &http.Client{}, slog.Default())
	err := client.SendApprovalRequest(t.Context(), "ticket-err-009", sampleTicketRecord())
	if err == nil {
		t.Fatal("SendApprovalRequest() error = nil, want error for connection refused")
	}
}
