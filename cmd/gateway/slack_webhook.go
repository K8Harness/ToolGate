package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisPublisher abstracts the Redis Publish operation for testability.
// Satisfied by *redis.Client and mockRedisPublisher in tests.
type redisPublisher interface {
	Publish(ctx context.Context, channel string, message interface{}) error
}

// realRedisPublisher wraps *redis.Client to satisfy the redisPublisher interface.
type realRedisPublisher struct {
	client *redis.Client
}

func (r *realRedisPublisher) Publish(ctx context.Context, channel string, message interface{}) error {
	return r.client.Publish(ctx, channel, message).Err()
}

// SlackWebhookHandler handles POST /slack/actions requests from Slack.
// It verifies the HMAC-SHA256 signature, parses the action payload,
// updates the ticket status, and publishes a resume signal.
type SlackWebhookHandler struct {
	signingSecret string
	tickets       ticketStatusUpdater
	redis         redisPublisher
	log           *slog.Logger
}

// NewSlackWebhookHandler constructs a production-ready SlackWebhookHandler.
// It accepts the concrete *TicketStore and *redis.Client types as specified in design.md.
func NewSlackWebhookHandler(
	signingSecret string,
	tickets *TicketStore,
	rdb *redis.Client,
	log *slog.Logger,
) *SlackWebhookHandler {
	if log == nil {
		log = slog.Default()
	}
	var pub redisPublisher
	if rdb != nil {
		pub = &realRedisPublisher{client: rdb}
	}
	var ts ticketStatusUpdater
	if tickets != nil {
		ts = tickets
	}
	return &SlackWebhookHandler{
		signingSecret: signingSecret,
		tickets:       ts,
		redis:         pub,
		log:           log,
	}
}

// newSlackWebhookHandlerWithDeps constructs a SlackWebhookHandler with injected
// interface dependencies — used in tests to inject mocks.
func newSlackWebhookHandlerWithDeps(
	signingSecret string,
	tickets ticketStatusUpdater,
	redis redisPublisher,
	log *slog.Logger,
) *SlackWebhookHandler {
	if log == nil {
		log = slog.Default()
	}
	return &SlackWebhookHandler{
		signingSecret: signingSecret,
		tickets:       tickets,
		redis:         redis,
		log:           log,
	}
}

// slackBlockActionsPayload is the internal representation of a Slack block_actions payload.
type slackBlockActionsPayload struct {
	Type    string        `json:"type"`
	User    slackUser     `json:"user"`
	Actions []slackAction `json:"actions"`
}

// slackUser carries the Slack user ID from the action callback.
type slackUser struct {
	ID string `json:"id"`
}

// slackAction represents a single interactive component action.
type slackAction struct {
	ActionID string `json:"action_id"`
	Value    string `json:"value"`
}

const slackReplayWindowSeconds = 5 * 60 // 5 minutes

// ServeHTTP processes POST /slack/actions requests.
// Processing order is NON-NEGOTIABLE for security (design.md):
//  1. Read raw body (must precede any parsing for HMAC)
//  2. Check timestamp replay window
//  3. Verify HMAC-SHA256 signature
//  4. Parse URL-encoded payload field → unmarshal JSON
//  5. Route on action_id
//  6. Extract ticketID
//  7. UpdateStatus → on error return 500
//  8. Publish resume signal → on error log warning, continue
//  9. Return 200
func (h *SlackWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Step 1: Read raw request body into []byte BEFORE any parsing (required for HMAC).
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		h.log.Error("slack webhook: read body failed", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Step 2: Extract and validate timestamp (replay attack prevention, req 3.4).
	tsHeader := r.Header.Get("X-Slack-Request-Timestamp")
	tsUnix, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		h.log.Warn("slack webhook: invalid timestamp header", "header", tsHeader)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	delta := time.Now().Unix() - tsUnix
	if delta < 0 {
		delta = -delta
	}
	if delta > slackReplayWindowSeconds {
		h.log.Warn("slack webhook: request timestamp outside replay window",
			"timestamp", tsUnix,
			"delta_seconds", delta,
		)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Step 3: Verify HMAC-SHA256 signature (req 3.2, 3.3).
	baseString := "v0:" + tsHeader + ":" + string(rawBody)
	mac := hmac.New(sha256.New, []byte(h.signingSecret))
	mac.Write([]byte(baseString))
	expectedSig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	providedSig := r.Header.Get("X-Slack-Signature")
	if !hmac.Equal([]byte(expectedSig), []byte(providedSig)) {
		h.log.Warn("slack webhook: signature mismatch")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Step 4: URL-decode the `payload` form field from rawBody; unmarshal into struct (req design step 6).
	formValues, err := url.ParseQuery(string(rawBody))
	if err != nil {
		h.log.Error("slack webhook: parse form body failed", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	payloadEncoded := formValues.Get("payload")
	if payloadEncoded == "" {
		h.log.Warn("slack webhook: missing payload field")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	payloadJSON, err := url.QueryUnescape(payloadEncoded)
	if err != nil {
		h.log.Error("slack webhook: unescape payload failed", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var payload slackBlockActionsPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		h.log.Error("slack webhook: unmarshal payload failed", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(payload.Actions) == 0 {
		h.log.Warn("slack webhook: no actions in payload")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Step 5–6: Route on action_id; extract ticketID from button value (req design steps 7–8).
	action := payload.Actions[0]
	userID := payload.User.ID
	ticketID := action.Value

	var status string
	switch action.ActionID {
	case "approval_approve":
		status = "approved"
	case "approval_deny":
		status = "denied"
	default:
		h.log.Warn("slack webhook: unknown action_id",
			"action_id", action.ActionID,
			"ticketID", ticketID,
		)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Step 7: Persist the decision to Postgres BEFORE publishing the signal (req 4.3).
	// On failure: return 500 (do NOT publish — decision not persisted, Slack will retry).
	if err := h.tickets.UpdateStatus(ctx, ticketID, status, userID); err != nil {
		h.log.Error("slack webhook: UpdateStatus failed",
			"ticketID", ticketID,
			"status", status,
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Step 8: Publish resume signal to the per-ticket Redis channel (req 4.1, 4.2).
	// On failure: log warning but return 200 — ticket is persisted; waiter will timeout (req 5.3).
	channel := "approvals:" + ticketID
	if err := h.redis.Publish(ctx, channel, status); err != nil {
		h.log.Warn("slack webhook: Redis Publish failed; ticket persisted, waiter will timeout",
			"ticketID", ticketID,
			"channel", channel,
			"error", err,
		)
	}

	// Step 9: Return HTTP 200 to dismiss the Slack button interaction (req 4.4).
	w.WriteHeader(http.StatusOK)
}
