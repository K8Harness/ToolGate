package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// --- Fakes ---

// fakeTicketStore captures UpdateStatus calls for test assertions.
type fakeTicketStore struct {
	updateStatusCalled bool
	updateStatusID     string
	updateStatusStatus string
	updateStatusBy     string
	updateStatusErr    error
}

func (f *fakeTicketStore) UpdateStatus(_ context.Context, id, status, decidedBy string) error {
	f.updateStatusCalled = true
	f.updateStatusID = id
	f.updateStatusStatus = status
	f.updateStatusBy = decidedBy
	return f.updateStatusErr
}

// fakeSessionLocker captures Extend calls and can be configured to return errors.
type fakeSessionLocker struct {
	extendCalled bool
	extendErr    error
}

func (f *fakeSessionLocker) Extend(_ context.Context, _, _ string) error {
	f.extendCalled = true
	return f.extendErr
}

// fakeApprovalPubSub simulates a Redis Pub/Sub channel for tests.
// It delivers messages from the messages channel, and nil when closed.
type fakeApprovalPubSub struct {
	ch chan approvalMessage
}

func newFakeApprovalPubSub() *fakeApprovalPubSub {
	return &fakeApprovalPubSub{
		ch: make(chan approvalMessage, 1),
	}
}

func (f *fakeApprovalPubSub) Channel() <-chan approvalMessage {
	return f.ch
}

func (f *fakeApprovalPubSub) Close() error {
	return nil
}

func (f *fakeApprovalPubSub) sendApproved() {
	f.ch <- approvalMessage{payload: "approved"}
}

func (f *fakeApprovalPubSub) sendDenied() {
	f.ch <- approvalMessage{payload: "denied"}
}

func (f *fakeApprovalPubSub) sendNil() {
	f.ch <- approvalMessage{isNil: true}
}

// --- Test helpers ---

func newTestApprovalBridge(tickets ticketStatusUpdater, locker lockExtender, pubsub approvalPubSub, timeout time.Duration) *RedisApprovalBridge {
	bridge := &RedisApprovalBridge{
		tickets:            tickets,
		locker:             locker,
		timeout:            timeout,
		lockExtendInterval: timeout / 2,
		log:                slog.Default(),
	}
	bridge.newPubSub = func(_ context.Context, _ string) approvalPubSub {
		return pubsub
	}
	return bridge
}

// --- Tests ---

func TestApprovalBridgeApprovedSignalReturnsApprovedDecision(t *testing.T) {
	t.Parallel()

	tickets := &fakeTicketStore{}
	locker := &fakeSessionLocker{}
	pubsub := newFakeApprovalPubSub()
	bridge := newTestApprovalBridge(tickets, locker, pubsub, 5*time.Second)

	ctx := context.Background()
	ticketID := "ticket-approve-1"

	// Deliver the signal before WaitForDecision blocks to avoid races
	pubsub.sendApproved()

	decision, err := bridge.WaitForDecision(ctx, ticketID, "session-1", "turn-1")

	if err != nil {
		t.Fatalf("WaitForDecision() error = %v, want nil", err)
	}
	if !decision.Approved {
		t.Fatal("decision.Approved = false, want true")
	}
	if decision.TicketID != ticketID {
		t.Fatalf("decision.TicketID = %q, want %q", decision.TicketID, ticketID)
	}
	if tickets.updateStatusCalled {
		t.Fatal("UpdateStatus called on approved path, want no call")
	}
}

func TestApprovalBridgeDeniedSignalReturnsDeniedDecision(t *testing.T) {
	t.Parallel()

	tickets := &fakeTicketStore{}
	locker := &fakeSessionLocker{}
	pubsub := newFakeApprovalPubSub()
	bridge := newTestApprovalBridge(tickets, locker, pubsub, 5*time.Second)

	ctx := context.Background()
	ticketID := "ticket-deny-1"

	pubsub.sendDenied()

	decision, err := bridge.WaitForDecision(ctx, ticketID, "session-2", "turn-2")

	if err != nil {
		t.Fatalf("WaitForDecision() error = %v, want nil", err)
	}
	if decision.Approved {
		t.Fatal("decision.Approved = true, want false")
	}
	if decision.TicketID != ticketID {
		t.Fatalf("decision.TicketID = %q, want %q", decision.TicketID, ticketID)
	}
	// Denied path: ticket status update is the webhook handler's job, NOT the bridge's
	if tickets.updateStatusCalled {
		t.Fatal("UpdateStatus called on denied path, want no call (that is webhook handler's job)")
	}
}

func TestApprovalBridgeTimeoutUpdatesTicketAndReturnsErrApprovalTimeout(t *testing.T) {
	t.Parallel()

	tickets := &fakeTicketStore{}
	locker := &fakeSessionLocker{}
	pubsub := newFakeApprovalPubSub()
	// Use a very short timeout so the test doesn't wait 5 minutes
	bridge := newTestApprovalBridge(tickets, locker, pubsub, 50*time.Millisecond)

	ctx := context.Background()
	ticketID := "ticket-timeout-1"

	_, err := bridge.WaitForDecision(ctx, ticketID, "session-3", "turn-3")

	if !errors.Is(err, ErrApprovalTimeout) {
		t.Fatalf("WaitForDecision() error = %v, want ErrApprovalTimeout", err)
	}
	if !tickets.updateStatusCalled {
		t.Fatal("UpdateStatus not called on timeout, want called with 'expired'")
	}
	if tickets.updateStatusStatus != "expired" {
		t.Fatalf("UpdateStatus status = %q, want %q", tickets.updateStatusStatus, "expired")
	}
	if tickets.updateStatusID != ticketID {
		t.Fatalf("UpdateStatus ticketID = %q, want %q", tickets.updateStatusID, ticketID)
	}
	if tickets.updateStatusBy != "" {
		t.Fatalf("UpdateStatus decidedBy = %q, want empty string (system-triggered)", tickets.updateStatusBy)
	}
}

func TestApprovalBridgeNilMessageFollowsTimeoutPath(t *testing.T) {
	t.Parallel()

	tickets := &fakeTicketStore{}
	locker := &fakeSessionLocker{}
	pubsub := newFakeApprovalPubSub()
	// Use a short timeout to verify the timeout path triggers after nil message
	bridge := newTestApprovalBridge(tickets, locker, pubsub, 200*time.Millisecond)

	ctx := context.Background()
	ticketID := "ticket-nil-1"

	// Send a nil/closed-channel message to simulate Redis connection loss
	pubsub.sendNil()

	_, err := bridge.WaitForDecision(ctx, ticketID, "session-4", "turn-4")

	if !errors.Is(err, ErrApprovalTimeout) {
		t.Fatalf("WaitForDecision() error = %v, want ErrApprovalTimeout (nil message follows timeout path)", err)
	}
	if !tickets.updateStatusCalled {
		t.Fatal("UpdateStatus not called after nil message, want called with 'expired'")
	}
	if tickets.updateStatusStatus != "expired" {
		t.Fatalf("UpdateStatus status = %q, want %q", tickets.updateStatusStatus, "expired")
	}
}

func TestApprovalBridgeContextCancelReturnsCtxErr(t *testing.T) {
	t.Parallel()

	tickets := &fakeTicketStore{}
	locker := &fakeSessionLocker{}
	pubsub := newFakeApprovalPubSub()
	bridge := newTestApprovalBridge(tickets, locker, pubsub, 5*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	ticketID := "ticket-cancel-1"

	// Cancel the context to trigger ctx.Done() path
	cancel()

	_, err := bridge.WaitForDecision(ctx, ticketID, "session-5", "turn-5")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitForDecision() error = %v, want context.Canceled", err)
	}
	if tickets.updateStatusCalled {
		t.Fatal("UpdateStatus called on ctx.Done path, want no call")
	}
}

func TestApprovalBridgeLockExtendErrorIsLoggedAndWaitContinues(t *testing.T) {
	t.Parallel()

	tickets := &fakeTicketStore{}
	// Configure locker to return errors on Extend
	locker := &fakeSessionLocker{extendErr: errors.New("redis extend failed")}
	pubsub := newFakeApprovalPubSub()
	// Use a tick interval shorter than the signal delay
	bridge := newTestApprovalBridge(tickets, locker, pubsub, 5*time.Second)
	// Make the lock extend interval very short to ensure it fires before our signal
	bridge.lockExtendInterval = 10 * time.Millisecond

	ctx := context.Background()
	ticketID := "ticket-extend-err-1"

	// Deliver signal after a brief delay to allow lock extend to fire first
	go func() {
		time.Sleep(50 * time.Millisecond)
		pubsub.sendApproved()
	}()

	decision, err := bridge.WaitForDecision(ctx, ticketID, "session-6", "turn-6")

	if err != nil {
		t.Fatalf("WaitForDecision() error = %v, want nil (extend errors are logged, not fatal)", err)
	}
	if !decision.Approved {
		t.Fatal("decision.Approved = false, want true")
	}
	if !locker.extendCalled {
		t.Fatal("Extend never called, want at least one call")
	}
}
