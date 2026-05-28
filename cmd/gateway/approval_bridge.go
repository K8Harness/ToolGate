package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrApprovalTimeout is returned when no decision arrives within the timeout window.
var ErrApprovalTimeout = errors.New("approval timeout")

// ApprovalDecision is the outcome of a completed approval wait.
type ApprovalDecision struct {
	Approved bool
	TicketID string
}

// ApprovalBridge abstracts the blocking wait mechanism. v0 implements with
// Redis Pub/Sub; the interface allows alternative mechanisms in later slices.
type ApprovalBridge interface {
	// WaitForDecision blocks until a resume signal arrives, ctx is cancelled,
	// or the 5-minute timeout fires. On timeout it updates the ticket status
	// to "expired" before returning ErrApprovalTimeout.
	WaitForDecision(ctx context.Context, ticketID, sessionID, turnID string) (ApprovalDecision, error)
}

// ticketStatusUpdater is a narrow interface for updating ticket status,
// satisfied by *TicketStore and fakeTicketStore in tests.
type ticketStatusUpdater interface {
	UpdateStatus(ctx context.Context, id, status, decidedBy string) error
}

// lockExtender is a narrow interface for extending session lock TTL,
// satisfied by *SessionLocker and fakeSessionLocker in tests.
type lockExtender interface {
	Extend(ctx context.Context, sessionID, turnID string) error
}

// approvalMessage represents a message received from a pub/sub channel.
type approvalMessage struct {
	payload string
	isNil   bool // true means simulate a nil/closed message
}

// approvalPubSub abstracts the Redis Pub/Sub subscription for testability.
type approvalPubSub interface {
	Channel() <-chan approvalMessage
	Close() error
}

// redisApprovalPubSub wraps a real redis.PubSub to satisfy approvalPubSub.
type redisApprovalPubSub struct {
	ps *redis.PubSub
	ch chan approvalMessage
}

func newRedisApprovalPubSub(ctx context.Context, client *redis.Client, channel string) *redisApprovalPubSub {
	ps := client.Subscribe(ctx, channel)
	rps := &redisApprovalPubSub{
		ps: ps,
		ch: make(chan approvalMessage, 1),
	}
	go rps.pump()
	return rps
}

func (r *redisApprovalPubSub) pump() {
	for msg := range r.ps.Channel() {
		if msg == nil {
			r.ch <- approvalMessage{isNil: true}
			return
		}
		r.ch <- approvalMessage{payload: msg.Payload}
	}
	// Channel closed (connection loss)
	r.ch <- approvalMessage{isNil: true}
}

func (r *redisApprovalPubSub) Channel() <-chan approvalMessage {
	return r.ch
}

func (r *redisApprovalPubSub) Close() error {
	return r.ps.Close()
}

// RedisApprovalBridge subscribes to a per-ticket Redis channel and blocks
// until an "approved" or "denied" signal arrives, the context is cancelled,
// or the 5-minute timeout fires. While waiting it extends the session lock TTL.
type RedisApprovalBridge struct {
	redis              *redis.Client
	tickets            ticketStatusUpdater
	locker             lockExtender
	timeout            time.Duration // fixed at 5 minutes in production
	lockExtendInterval time.Duration // lockTTL / 2
	log                *slog.Logger

	// newPubSub is overridable for tests; defaults to newRedisApprovalPubSub.
	newPubSub func(ctx context.Context, channel string) approvalPubSub
}

// NewRedisApprovalBridge constructs a production-ready RedisApprovalBridge.
func NewRedisApprovalBridge(
	rdb *redis.Client,
	tickets *TicketStore,
	locker *SessionLocker,
	lockTTL time.Duration,
	approvalTimeout time.Duration,
	log *slog.Logger,
) *RedisApprovalBridge {
	if log == nil {
		log = slog.Default()
	}
	b := &RedisApprovalBridge{
		redis:              rdb,
		tickets:            tickets,
		locker:             locker,
		timeout:            approvalTimeout,
		lockExtendInterval: lockTTL / 2,
		log:                log,
	}
	b.newPubSub = func(ctx context.Context, channel string) approvalPubSub {
		return newRedisApprovalPubSub(ctx, rdb, channel)
	}
	return b
}

// WaitForDecision blocks until a resume signal arrives, ctx is cancelled,
// or the timeout fires.
//
//   - "approved" signal: returns ApprovalDecision{Approved: true, TicketID: ticketID}
//   - "denied" signal: returns ApprovalDecision{Approved: false, TicketID: ticketID}
//   - nil message (connection loss): follows timeout path
//   - timeout: calls tickets.UpdateStatus(ctx, ticketID, "expired", ""), returns ErrApprovalTimeout
//   - ctx.Done(): returns ctx.Err()
func (b *RedisApprovalBridge) WaitForDecision(ctx context.Context, ticketID, sessionID, turnID string) (ApprovalDecision, error) {
	channelName := "approvals:" + ticketID
	pubsub := b.newPubSub(ctx, channelName)
	defer func() { _ = pubsub.Close() }()

	ticker := time.NewTicker(b.lockExtendInterval)
	defer ticker.Stop()

	timeout := time.NewTimer(b.timeout)
	defer timeout.Stop()

	for {
		select {
		case msg, ok := <-pubsub.Channel():
			if !ok || msg.isNil {
				// Connection loss: treat as timeout (req 5.3)
				b.log.Error("approval pub/sub channel closed; treating as timeout",
					"ticketID", ticketID,
					"sessionID", sessionID,
					"turnID", turnID,
				)
				return b.handleTimeout(ctx, ticketID, sessionID, turnID)
			}
			switch msg.payload {
			case "approved":
				return ApprovalDecision{Approved: true, TicketID: ticketID}, nil
			case "denied":
				return ApprovalDecision{Approved: false, TicketID: ticketID}, nil
			default:
				b.log.Error("unknown approval signal; ignoring",
					"payload", msg.payload,
					"ticketID", ticketID,
					"sessionID", sessionID,
				)
			}

		case <-ticker.C:
			if err := b.locker.Extend(ctx, sessionID, turnID); err != nil {
				// Lock extend errors are logged but do not abort the wait (req 1.2)
				b.log.Error("session lock extend failed during approval wait",
					"ticketID", ticketID,
					"sessionID", sessionID,
					"turnID", turnID,
					"error", err,
				)
			}

		case <-timeout.C:
			b.log.Error("approval wait timed out",
				"ticketID", ticketID,
				"sessionID", sessionID,
				"turnID", turnID,
			)
			return b.handleTimeout(ctx, ticketID, sessionID, turnID)

		case <-ctx.Done():
			b.log.Error("approval wait context cancelled",
				"ticketID", ticketID,
				"sessionID", sessionID,
				"turnID", turnID,
				"error", ctx.Err(),
			)
			return ApprovalDecision{}, ctx.Err()
		}
	}
}

// handleTimeout marks the ticket as expired and returns ErrApprovalTimeout.
func (b *RedisApprovalBridge) handleTimeout(ctx context.Context, ticketID, sessionID, turnID string) (ApprovalDecision, error) {
	if err := b.tickets.UpdateStatus(ctx, ticketID, "expired", ""); err != nil {
		b.log.Error("failed to update ticket status to expired",
			"ticketID", ticketID,
			"sessionID", sessionID,
			"turnID", turnID,
			"error", err,
		)
	}
	return ApprovalDecision{}, ErrApprovalTimeout
}
