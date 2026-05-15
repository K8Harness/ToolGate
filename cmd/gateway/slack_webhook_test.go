package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// --- Mock Interfaces ---

// mockTicketStore satisfies the ticketStatusUpdater interface for webhook tests.
type mockTicketStore struct {
	updateStatusCalled bool
	updateStatusID     string
	updateStatusStatus string
	updateStatusBy     string
	updateStatusErr    error
}

func (m *mockTicketStore) UpdateStatus(_ context.Context, id, status, decidedBy string) error {
	m.updateStatusCalled = true
	m.updateStatusID = id
	m.updateStatusStatus = status
	m.updateStatusBy = decidedBy
	return m.updateStatusErr
}

// mockRedisPublisher satisfies the redisPublisher interface for webhook tests.
type mockRedisPublisher struct {
	publishCalled  bool
	publishChannel string
	publishMessage string
	publishErr     error
}

func (m *mockRedisPublisher) Publish(_ context.Context, channel string, message interface{}) error {
	m.publishCalled = true
	m.publishChannel = channel
	m.publishMessage = fmt.Sprintf("%v", message)
	return m.publishErr
}

// --- Test Helpers ---

// signSlackRequest computes the correct Slack HMAC-SHA256 signature for a test request.
func signSlackRequest(t *testing.T, signingSecret string, timestamp string, body []byte) string {
	t.Helper()
	base := "v0:" + timestamp + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte(base))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// buildSlackActionBody constructs a URL-encoded Slack action request body.
func buildSlackActionBody(t *testing.T, actionID, ticketID, userID string) []byte {
	t.Helper()
	payload := slackBlockActionsPayload{
		Type: "block_actions",
		User: slackUser{ID: userID},
		Actions: []slackAction{
			{ActionID: actionID, Value: ticketID},
		},
	}
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("buildSlackActionBody: marshal: %v", err)
	}
	// Slack sends the payload as a URL-encoded form field
	encoded := url.QueryEscape(string(jsonBytes))
	return []byte("payload=" + encoded)
}

// newSlackWebhookTestHandler creates a SlackWebhookHandler with mock dependencies.
func newSlackWebhookTestHandler(signingSecret string, tickets ticketStatusUpdater, redis redisPublisher) *SlackWebhookHandler {
	return newSlackWebhookHandlerWithDeps(signingSecret, tickets, redis, nil)
}

// buildSignedRequest creates an httptest request with correct Slack signature headers.
func buildSignedRequest(t *testing.T, signingSecret string, body []byte) *http.Request {
	t.Helper()
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	sig := signSlackRequest(t, signingSecret, timestamp, body)

	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)
	req.Header.Set("X-Slack-Signature", sig)
	return req
}

// --- Tests ---

// TestSlackWebhookApproveAction verifies that a valid approve action results in:
// - UpdateStatus called with "approved" and the correct ticketID and userID
// - Redis Publish called on the correct channel
// - HTTP 200 returned
func TestSlackWebhookApproveAction(t *testing.T) {
	t.Parallel()

	const signingSecret = "test-signing-secret"
	const ticketID = "ticket-approve-001"
	const userID = "U12345"

	tickets := &mockTicketStore{}
	redis := &mockRedisPublisher{}
	handler := newSlackWebhookTestHandler(signingSecret, tickets, redis)

	body := buildSlackActionBody(t, "approval_approve", ticketID, userID)
	req := buildSignedRequest(t, signingSecret, body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("ServeHTTP() status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !tickets.updateStatusCalled {
		t.Fatal("UpdateStatus not called, want called with 'approved'")
	}
	if tickets.updateStatusID != ticketID {
		t.Errorf("UpdateStatus ticketID = %q, want %q", tickets.updateStatusID, ticketID)
	}
	if tickets.updateStatusStatus != "approved" {
		t.Errorf("UpdateStatus status = %q, want %q", tickets.updateStatusStatus, "approved")
	}
	if tickets.updateStatusBy != userID {
		t.Errorf("UpdateStatus decidedBy = %q, want %q", tickets.updateStatusBy, userID)
	}
	if !redis.publishCalled {
		t.Fatal("Redis Publish not called, want called after successful UpdateStatus")
	}
	wantChannel := "approvals:" + ticketID
	if redis.publishChannel != wantChannel {
		t.Errorf("Redis Publish channel = %q, want %q", redis.publishChannel, wantChannel)
	}
	if redis.publishMessage != "approved" {
		t.Errorf("Redis Publish message = %q, want %q", redis.publishMessage, "approved")
	}
}

// TestSlackWebhookDenyAction verifies that a valid deny action results in:
// - UpdateStatus called with "denied"
// - Redis Publish called with "denied"
// - HTTP 200 returned
func TestSlackWebhookDenyAction(t *testing.T) {
	t.Parallel()

	const signingSecret = "test-signing-secret"
	const ticketID = "ticket-deny-002"
	const userID = "U67890"

	tickets := &mockTicketStore{}
	redis := &mockRedisPublisher{}
	handler := newSlackWebhookTestHandler(signingSecret, tickets, redis)

	body := buildSlackActionBody(t, "approval_deny", ticketID, userID)
	req := buildSignedRequest(t, signingSecret, body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("ServeHTTP() status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !tickets.updateStatusCalled {
		t.Fatal("UpdateStatus not called, want called with 'denied'")
	}
	if tickets.updateStatusStatus != "denied" {
		t.Errorf("UpdateStatus status = %q, want %q", tickets.updateStatusStatus, "denied")
	}
	if tickets.updateStatusBy != userID {
		t.Errorf("UpdateStatus decidedBy = %q, want %q", tickets.updateStatusBy, userID)
	}
	if !redis.publishCalled {
		t.Fatal("Redis Publish not called on deny action")
	}
	if redis.publishMessage != "denied" {
		t.Errorf("Redis Publish message = %q, want %q", redis.publishMessage, "denied")
	}
}

// TestSlackWebhookBadHMACReturns400 verifies that a request with incorrect HMAC
// returns HTTP 400 and does not call UpdateStatus or Redis Publish.
func TestSlackWebhookBadHMACReturns400(t *testing.T) {
	t.Parallel()

	const signingSecret = "test-signing-secret"
	const ticketID = "ticket-bad-sig-003"

	tickets := &mockTicketStore{}
	redis := &mockRedisPublisher{}
	handler := newSlackWebhookTestHandler(signingSecret, tickets, redis)

	body := buildSlackActionBody(t, "approval_approve", ticketID, "U99999")
	timestamp := fmt.Sprintf("%d", time.Now().Unix())

	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)
	req.Header.Set("X-Slack-Signature", "v0=badhmacsignaturevalue00000000000000000000000000000000000000000000")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("ServeHTTP() status = %d, want %d (bad HMAC)", rec.Code, http.StatusBadRequest)
	}
	if tickets.updateStatusCalled {
		t.Error("UpdateStatus called with bad signature, want no DB call")
	}
	if redis.publishCalled {
		t.Error("Redis Publish called with bad signature, want no Redis call")
	}
}

// TestSlackWebhookReplayAttackReturns400 verifies that a request with a timestamp
// older than 5 minutes is rejected with HTTP 400.
func TestSlackWebhookReplayAttackReturns400(t *testing.T) {
	t.Parallel()

	const signingSecret = "test-signing-secret"
	const ticketID = "ticket-replay-004"

	tickets := &mockTicketStore{}
	redis := &mockRedisPublisher{}
	handler := newSlackWebhookTestHandler(signingSecret, tickets, redis)

	body := buildSlackActionBody(t, "approval_approve", ticketID, "U11111")

	// Use a timestamp that is 6 minutes in the past (outside 5-minute window)
	oldTimestamp := fmt.Sprintf("%d", time.Now().Add(-6*time.Minute).Unix())
	sig := signSlackRequest(t, signingSecret, oldTimestamp, body)

	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", oldTimestamp)
	req.Header.Set("X-Slack-Signature", sig)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("ServeHTTP() status = %d, want %d (replay attack)", rec.Code, http.StatusBadRequest)
	}
	if tickets.updateStatusCalled {
		t.Error("UpdateStatus called on replay attack, want no DB call")
	}
	if redis.publishCalled {
		t.Error("Redis Publish called on replay attack, want no Redis call")
	}
}

// TestSlackWebhookUpdateStatusFailureReturns500 verifies that when UpdateStatus
// returns an error, the handler returns HTTP 500 and does NOT publish to Redis.
func TestSlackWebhookUpdateStatusFailureReturns500(t *testing.T) {
	t.Parallel()

	const signingSecret = "test-signing-secret"
	const ticketID = "ticket-db-fail-005"

	tickets := &mockTicketStore{updateStatusErr: fmt.Errorf("db connection error")}
	redis := &mockRedisPublisher{}
	handler := newSlackWebhookTestHandler(signingSecret, tickets, redis)

	body := buildSlackActionBody(t, "approval_approve", ticketID, "U22222")
	req := buildSignedRequest(t, signingSecret, body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("ServeHTTP() status = %d, want %d (UpdateStatus failure)", rec.Code, http.StatusInternalServerError)
	}
	if redis.publishCalled {
		t.Error("Redis Publish called after UpdateStatus failure, want no publish (decision not persisted)")
	}
}

// TestSlackWebhookUnknownActionIDReturns400 verifies that an unknown action_id
// results in HTTP 400 with no DB or Redis calls.
func TestSlackWebhookUnknownActionIDReturns400(t *testing.T) {
	t.Parallel()

	const signingSecret = "test-signing-secret"
	const ticketID = "ticket-unknown-006"

	tickets := &mockTicketStore{}
	redis := &mockRedisPublisher{}
	handler := newSlackWebhookTestHandler(signingSecret, tickets, redis)

	body := buildSlackActionBody(t, "some_unknown_action", ticketID, "U33333")
	req := buildSignedRequest(t, signingSecret, body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("ServeHTTP() status = %d, want %d (unknown action_id)", rec.Code, http.StatusBadRequest)
	}
	if tickets.updateStatusCalled {
		t.Error("UpdateStatus called for unknown action_id, want no DB call")
	}
	if redis.publishCalled {
		t.Error("Redis Publish called for unknown action_id, want no Redis call")
	}
}

// TestSlackWebhookRedisPublishFailureReturns200 verifies that when Redis Publish
// fails, the handler still returns HTTP 200 (ticket is persisted; waiter will timeout).
func TestSlackWebhookRedisPublishFailureReturns200(t *testing.T) {
	t.Parallel()

	const signingSecret = "test-signing-secret"
	const ticketID = "ticket-redis-fail-007"

	tickets := &mockTicketStore{}
	redis := &mockRedisPublisher{publishErr: fmt.Errorf("redis publish error")}
	handler := newSlackWebhookTestHandler(signingSecret, tickets, redis)

	body := buildSlackActionBody(t, "approval_approve", ticketID, "U44444")
	req := buildSignedRequest(t, signingSecret, body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("ServeHTTP() status = %d, want %d (Redis failure should still return 200)", rec.Code, http.StatusOK)
	}
	if !tickets.updateStatusCalled {
		t.Fatal("UpdateStatus not called, want called before Redis publish attempt")
	}
}

// TestNewSlackWebhookHandlerConstructor verifies the constructor sets fields correctly.
func TestNewSlackWebhookHandlerConstructor(t *testing.T) {
	t.Parallel()

	// This test uses the production constructor with *TicketStore and *redis.Client
	// which require real dependencies. We just test that it does not panic with nil logger.
	// The actual behavior is tested by the mock-based tests above.
	h := NewSlackWebhookHandler("secret", nil, nil, nil)
	if h == nil {
		t.Fatal("NewSlackWebhookHandler returned nil")
	}
	if h.signingSecret != "secret" {
		t.Errorf("signingSecret = %q, want %q", h.signingSecret, "secret")
	}
}
