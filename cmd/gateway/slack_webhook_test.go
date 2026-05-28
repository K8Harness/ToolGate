package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	calls              *[]string
}

func (m *mockTicketStore) UpdateStatus(_ context.Context, id, status, decidedBy string) error {
	m.updateStatusCalled = true
	m.updateStatusID = id
	m.updateStatusStatus = status
	m.updateStatusBy = decidedBy
	if m.calls != nil {
		*m.calls = append(*m.calls, "update")
	}
	return m.updateStatusErr
}

// mockRedisPublisher satisfies the redisPublisher interface for webhook tests.
type mockRedisPublisher struct {
	publishCalled  bool
	publishChannel string
	publishMessage string
	publishErr     error
	calls          *[]string
}

func (m *mockRedisPublisher) Publish(_ context.Context, channel string, message interface{}) error {
	m.publishCalled = true
	m.publishChannel = channel
	m.publishMessage = fmt.Sprintf("%v", message)
	if m.calls != nil {
		*m.calls = append(*m.calls, "publish")
	}
	return m.publishErr
}

// --- Test Helpers ---

// signLarkRequest computes the correct Lark signature for a test request.
func signLarkRequest(t *testing.T, verificationToken, timestamp, nonce string, body []byte) string {
	t.Helper()
	return computeLarkSignature(verificationToken, timestamp, nonce, body)
}

// buildLarkActionBody constructs a Lark card callback JSON body.
func buildLarkActionBody(t *testing.T, action, ticketID, openID string) []byte {
	t.Helper()
	payload := larkCardCallbackPayload{
		OpenID: openID,
		Action: larkCardCallbackAction{
			Tag: "button",
			Value: larkActionValue{
				TicketID: ticketID,
				Action:   action,
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("buildLarkActionBody: marshal: %v", err)
	}
	return body
}

// newLarkWebhookTestHandler creates a LarkWebhookHandler with mock dependencies.
func newLarkWebhookTestHandler(verificationToken string, tickets ticketStatusUpdater, redis redisPublisher) *LarkWebhookHandler {
	return newLarkWebhookHandlerWithDeps(verificationToken, tickets, redis, nil)
}

// buildSignedLarkRequest creates an httptest request with correct Lark signature headers.
func buildSignedLarkRequest(t *testing.T, verificationToken string, body []byte) *http.Request {
	t.Helper()
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := "test-nonce-abc"
	sig := signLarkRequest(t, verificationToken, timestamp, nonce, body)

	req := httptest.NewRequest(http.MethodPost, "/lark/actions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lark-Request-Timestamp", timestamp)
	req.Header.Set("X-Lark-Request-Nonce", nonce)
	req.Header.Set("X-Lark-Signature", sig)
	return req
}

// --- Tests ---

// TestLarkWebhookApproveAction verifies that a valid approve action results in:
// - UpdateStatus called with "approved" and the correct ticketID and openID
// - Redis Publish called on the correct channel
// - HTTP 200 returned
func TestLarkWebhookApproveAction(t *testing.T) {
	t.Parallel()

	const verificationToken = "test-verification-token"
	const ticketID = "ticket-approve-001"
	const openID = "ou_U12345"

	var calls []string
	tickets := &mockTicketStore{calls: &calls}
	redis := &mockRedisPublisher{calls: &calls}
	handler := newLarkWebhookTestHandler(verificationToken, tickets, redis)

	body := buildLarkActionBody(t, "approve", ticketID, openID)
	req := buildSignedLarkRequest(t, verificationToken, body)
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
	if tickets.updateStatusBy != openID {
		t.Errorf("UpdateStatus decidedBy = %q, want %q", tickets.updateStatusBy, openID)
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
	if got, want := strings.Join(calls, ","), "update,publish"; got != want {
		t.Errorf("call order = %q, want %q", got, want)
	}
}

// TestLarkWebhookDenyAction verifies that a valid deny action results in:
// - UpdateStatus called with "denied"
// - Redis Publish called with "denied"
// - HTTP 200 returned
func TestLarkWebhookDenyAction(t *testing.T) {
	t.Parallel()

	const verificationToken = "test-verification-token"
	const ticketID = "ticket-deny-002"
	const openID = "ou_U67890"

	var calls []string
	tickets := &mockTicketStore{calls: &calls}
	redis := &mockRedisPublisher{calls: &calls}
	handler := newLarkWebhookTestHandler(verificationToken, tickets, redis)

	body := buildLarkActionBody(t, "deny", ticketID, openID)
	req := buildSignedLarkRequest(t, verificationToken, body)
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
	if !redis.publishCalled {
		t.Fatal("Redis Publish not called on deny action")
	}
	if redis.publishMessage != "denied" {
		t.Errorf("Redis Publish message = %q, want %q", redis.publishMessage, "denied")
	}
	if got, want := strings.Join(calls, ","), "update,publish"; got != want {
		t.Errorf("call order = %q, want %q", got, want)
	}
}

// TestLarkWebhookBadSignatureReturns400 verifies that a request with incorrect signature
// returns HTTP 400 and does not call UpdateStatus or Redis Publish.
func TestLarkWebhookBadSignatureReturns400(t *testing.T) {
	t.Parallel()

	const verificationToken = "test-verification-token"
	const ticketID = "ticket-bad-sig-003"

	tickets := &mockTicketStore{}
	redis := &mockRedisPublisher{}
	handler := newLarkWebhookTestHandler(verificationToken, tickets, redis)

	body := buildLarkActionBody(t, "approve", ticketID, "ou_U99999")
	timestamp := fmt.Sprintf("%d", time.Now().Unix())

	req := httptest.NewRequest(http.MethodPost, "/lark/actions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lark-Request-Timestamp", timestamp)
	req.Header.Set("X-Lark-Request-Nonce", "some-nonce")
	req.Header.Set("X-Lark-Signature", "badsignature0000000000000000000000000000000000000000000000000000")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("ServeHTTP() status = %d, want %d (bad signature)", rec.Code, http.StatusBadRequest)
	}
	if tickets.updateStatusCalled {
		t.Error("UpdateStatus called with bad signature, want no DB call")
	}
	if redis.publishCalled {
		t.Error("Redis Publish called with bad signature, want no Redis call")
	}
}

// TestLarkWebhookReplayAttackReturns400 verifies that a request with a timestamp
// older than 5 minutes is rejected with HTTP 400.
func TestLarkWebhookReplayAttackReturns400(t *testing.T) {
	t.Parallel()

	const verificationToken = "test-verification-token"
	const ticketID = "ticket-replay-004"

	tickets := &mockTicketStore{}
	redis := &mockRedisPublisher{}
	handler := newLarkWebhookTestHandler(verificationToken, tickets, redis)

	body := buildLarkActionBody(t, "approve", ticketID, "ou_U11111")
	oldTimestamp := fmt.Sprintf("%d", time.Now().Add(-6*time.Minute).Unix())
	nonce := "test-nonce"
	sig := signLarkRequest(t, verificationToken, oldTimestamp, nonce, body)

	req := httptest.NewRequest(http.MethodPost, "/lark/actions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lark-Request-Timestamp", oldTimestamp)
	req.Header.Set("X-Lark-Request-Nonce", nonce)
	req.Header.Set("X-Lark-Signature", sig)
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

// TestLarkWebhookUpdateStatusFailureReturns500 verifies that when UpdateStatus
// returns an error, the handler returns HTTP 500 and does NOT publish to Redis.
func TestLarkWebhookUpdateStatusFailureReturns500(t *testing.T) {
	t.Parallel()

	const verificationToken = "test-verification-token"
	const ticketID = "ticket-db-fail-005"

	tickets := &mockTicketStore{updateStatusErr: fmt.Errorf("db connection error")}
	redis := &mockRedisPublisher{}
	handler := newLarkWebhookTestHandler(verificationToken, tickets, redis)

	body := buildLarkActionBody(t, "approve", ticketID, "ou_U22222")
	req := buildSignedLarkRequest(t, verificationToken, body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("ServeHTTP() status = %d, want %d (UpdateStatus failure)", rec.Code, http.StatusInternalServerError)
	}
	if redis.publishCalled {
		t.Error("Redis Publish called after UpdateStatus failure, want no publish")
	}
}

// TestLarkWebhookUnknownActionReturns400 verifies that an unknown action value
// results in HTTP 400 with no DB or Redis calls.
func TestLarkWebhookUnknownActionReturns400(t *testing.T) {
	t.Parallel()

	const verificationToken = "test-verification-token"
	const ticketID = "ticket-unknown-006"

	tickets := &mockTicketStore{}
	redis := &mockRedisPublisher{}
	handler := newLarkWebhookTestHandler(verificationToken, tickets, redis)

	body := buildLarkActionBody(t, "some_unknown_action", ticketID, "ou_U33333")
	req := buildSignedLarkRequest(t, verificationToken, body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("ServeHTTP() status = %d, want %d (unknown action)", rec.Code, http.StatusBadRequest)
	}
	if tickets.updateStatusCalled {
		t.Error("UpdateStatus called for unknown action, want no DB call")
	}
	if redis.publishCalled {
		t.Error("Redis Publish called for unknown action, want no Redis call")
	}
}

// TestLarkWebhookRedisPublishFailureReturns200 verifies that when Redis Publish
// fails, the handler still returns HTTP 200 (ticket is persisted; waiter will timeout).
func TestLarkWebhookRedisPublishFailureReturns200(t *testing.T) {
	t.Parallel()

	const verificationToken = "test-verification-token"
	const ticketID = "ticket-redis-fail-007"

	tickets := &mockTicketStore{}
	redis := &mockRedisPublisher{publishErr: fmt.Errorf("redis publish error")}
	handler := newLarkWebhookTestHandler(verificationToken, tickets, redis)

	body := buildLarkActionBody(t, "approve", ticketID, "ou_U44444")
	req := buildSignedLarkRequest(t, verificationToken, body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("ServeHTTP() status = %d, want %d (Redis failure should still return 200)", rec.Code, http.StatusOK)
	}
	if !tickets.updateStatusCalled {
		t.Fatal("UpdateStatus not called, want called before Redis publish attempt")
	}
}

// TestNewLarkWebhookHandlerConstructor verifies the constructor sets the verification token.
func TestNewLarkWebhookHandlerConstructor(t *testing.T) {
	t.Parallel()

	h := NewLarkWebhookHandler("my-token", nil, nil, nil)
	if h == nil {
		t.Fatal("NewLarkWebhookHandler returned nil")
	}
	if h.verificationToken != "my-token" {
		t.Errorf("verificationToken = %q, want %q", h.verificationToken, "my-token")
	}
}
