package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
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

// LarkWebhookHandler handles POST /lark/actions requests from Lark.
// It verifies the request signature, parses the card action payload,
// updates the ticket status, and publishes a resume signal.
type LarkWebhookHandler struct {
	verificationToken string
	tickets           ticketStatusUpdater
	redis             redisPublisher
	log               *slog.Logger
}

// NewLarkWebhookHandler constructs a production-ready LarkWebhookHandler.
func NewLarkWebhookHandler(
	verificationToken string,
	tickets *TicketStore,
	rdb *redis.Client,
	log *slog.Logger,
) *LarkWebhookHandler {
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
	return &LarkWebhookHandler{
		verificationToken: verificationToken,
		tickets:           ts,
		redis:             pub,
		log:               log,
	}
}

// newLarkWebhookHandlerWithDeps constructs a LarkWebhookHandler with injected
// interface dependencies — used in tests to inject mocks.
func newLarkWebhookHandlerWithDeps(
	verificationToken string,
	tickets ticketStatusUpdater,
	redis redisPublisher,
	log *slog.Logger,
) *LarkWebhookHandler {
	if log == nil {
		log = slog.Default()
	}
	return &LarkWebhookHandler{
		verificationToken: verificationToken,
		tickets:           tickets,
		redis:             redis,
		log:               log,
	}
}

// larkActionValue carries the ticket_id and action from a button click.
type larkActionValue struct {
	TicketID string `json:"ticket_id"`
	Action   string `json:"action"`
}

// larkCardAction carries the button action data in a card callback.
type larkCardCallbackAction struct {
	Tag   string          `json:"tag"`
	Value larkActionValue `json:"value"`
}

// larkCallbackEnvelope unmarshals both the old flat v1 payload and the nested
// schema:"2.0" v2 payload that Lark now sends for card.action.trigger events.
//
// v1 (flat):  { "token": "...", "open_id": "...", "action": {...} }
// v2 (nested): { "schema":"2.0", "header":{"token":"..."}, "event":{"operator":{"open_id":"..."}, "action":{...}} }
type larkCallbackEnvelope struct {
	Schema string `json:"schema"`
	// v2 fields
	Header struct {
		Token string `json:"token"`
	} `json:"header"`
	Event struct {
		Operator struct {
			OpenID string `json:"open_id"`
		} `json:"operator"`
		Action larkCardCallbackAction `json:"action"`
	} `json:"event"`
	// v1 flat fields
	Token  string                 `json:"token"`
	OpenID string                 `json:"open_id"`
	Action larkCardCallbackAction `json:"action"`
}

const larkReplayWindowSeconds = 5 * 60 // 5 minutes

// computeLarkSignature returns hex(sha256(verificationToken + timestamp + nonce + body)).
// Both the gateway and mock-lark use this formula so signatures are mutually verifiable.
func computeLarkSignature(verificationToken, timestamp, nonce string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(verificationToken))
	h.Write([]byte(timestamp))
	h.Write([]byte(nonce))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// ServeHTTP processes POST /lark/actions requests.
// Processing order (non-negotiable for security):
//  0. Handle Lark URL verification challenge (no signature required — used during setup)
//  1. Read raw body (must precede any parsing for signature check)
//  2. Check timestamp replay window
//  3. Verify SHA-256 signature
//  4. Parse JSON payload
//  5. Route on action value
//  6. Extract ticketID
//  7. UpdateStatus → on error return 500
//  8. Publish resume signal → on error log warning, continue
//  9. Return 200
func (h *LarkWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Step 1: Read raw body before any parsing (required for signature verification).
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		h.log.Error("lark webhook: read body failed", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Step 0: Handle Lark URL verification challenge sent during callback URL setup.
	// Lark sends {"type":"url_verification","challenge":"..."} with no signature headers.
	var maybeChallenge struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if json.Unmarshal(rawBody, &maybeChallenge) == nil && maybeChallenge.Type == "url_verification" {
		h.log.Info("lark webhook: responding to URL verification challenge")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"challenge":"` + maybeChallenge.Challenge + `"}`))
		return
	}

	// Step 2–4: Verify request authenticity, then parse payload.
	// Three modes are supported:
	//   - Real Lark v2 (schema:"2.0"): token is in header.token; fields are nested under event.
	//   - Real Lark v1 (flat):         token is in the root "token" field.
	//   - mock-lark:                   HMAC headers (X-Lark-Request-Timestamp / Nonce / Signature).
	var envelope larkCallbackEnvelope
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		h.log.Error("lark webhook: unmarshal payload failed", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Normalise v1/v2 into flat variables.
	var token, openID string
	var action larkCardCallbackAction
	if envelope.Schema == "2.0" {
		token = envelope.Header.Token
		openID = envelope.Event.Operator.OpenID
		action = envelope.Event.Action
	} else {
		token = envelope.Token
		openID = envelope.OpenID
		action = envelope.Action
	}

	tsHeader := r.Header.Get("X-Lark-Request-Timestamp")
	if tsHeader == "" {
		// Real Lark path: verify using the token embedded in the body.
		if token != h.verificationToken {
			h.log.Warn("lark webhook: token mismatch")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	} else {
		// mock-lark path: verify using HMAC headers.
		tsUnix, err := strconv.ParseInt(tsHeader, 10, 64)
		if err != nil {
			h.log.Warn("lark webhook: invalid timestamp header", "header", tsHeader)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		delta := time.Now().Unix() - tsUnix
		if delta < 0 {
			delta = -delta
		}
		if delta > larkReplayWindowSeconds {
			h.log.Warn("lark webhook: request timestamp outside replay window",
				"timestamp", tsUnix,
				"delta_seconds", delta,
			)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		nonce := r.Header.Get("X-Lark-Request-Nonce")
		expectedSig := computeLarkSignature(h.verificationToken, tsHeader, nonce, rawBody)
		providedSig := r.Header.Get("X-Lark-Signature")
		if expectedSig != providedSig {
			h.log.Warn("lark webhook: signature mismatch")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}

	// Step 5–6: Route on action value; extract ticketID.
	ticketID := action.Value.TicketID
	userID := openID
	actionName := action.Value.Action

	if ticketID == "" {
		h.log.Warn("lark webhook: missing ticket_id in action value")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var status string
	switch actionName {
	case "approve":
		status = "approved"
	case "deny":
		status = "denied"
	default:
		h.log.Warn("lark webhook: unknown action",
			"action", actionName,
			"ticketID", ticketID,
		)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Step 7: Persist the decision to Postgres BEFORE publishing the signal.
	// On failure: return 500 (do NOT publish — decision not persisted, Lark will retry).
	if err := h.tickets.UpdateStatus(ctx, ticketID, status, userID); err != nil {
		h.log.Error("lark webhook: UpdateStatus failed",
			"ticketID", ticketID,
			"status", status,
			"error", err,
		)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Step 8: Publish resume signal to the per-ticket Redis channel.
	// On failure: log warning but return 200 — ticket is persisted; waiter will timeout.
	channel := "approvals:" + ticketID
	if err := h.redis.Publish(ctx, channel, status); err != nil {
		h.log.Warn("lark webhook: Redis Publish failed; ticket persisted, waiter will timeout",
			"ticketID", ticketID,
			"channel", channel,
			"error", err,
		)
	}

	h.log.Info("lark webhook: decision recorded", "ticketID", ticketID, "status", status, "userID", userID)

	// Step 9: Return HTTP 200 with an empty JSON body.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{}`))
}
