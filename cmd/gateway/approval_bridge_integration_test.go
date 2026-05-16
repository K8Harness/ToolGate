package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func TestApprovalBridgeIntegrationApprovedFlow(t *testing.T) {
	ctx := context.Background()
	_, redisClient, store, _, bridge := newApprovalBridgeIntegrationHarness(t, 2*time.Second, 2*time.Second, time.Hour)

	ticketID := insertApprovalBridgeTestTicket(t, ctx, store, "session-approve-int", "turn-approve-int")

	type result struct {
		decision ApprovalDecision
		err      error
	}
	done := make(chan result, 1)
	go func() {
		decision, err := bridge.WaitForDecision(ctx, ticketID, "session-approve-int", "turn-approve-int")
		done <- result{decision: decision, err: err}
	}()

	waitForApprovalSubscriber(t, ctx, redisClient, ticketID)

	if err := store.UpdateStatus(ctx, ticketID, "approved", "U-APPROVER"); err != nil {
		t.Fatalf("UpdateStatus(approved) error = %v, want nil", err)
	}
	if err := redisClient.Publish(ctx, "approvals:"+ticketID, "approved").Err(); err != nil {
		t.Fatalf("Publish(approved) error = %v, want nil", err)
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("WaitForDecision() error = %v, want nil", got.err)
	}
	if !got.decision.Approved {
		t.Fatal("decision.Approved = false, want true")
	}
	if got.decision.TicketID != ticketID {
		t.Fatalf("decision.TicketID = %q, want %q", got.decision.TicketID, ticketID)
	}

	assertTicketStatus(t, ctx, store.pool, ticketID, "approved")
}

func TestApprovalBridgeIntegrationDeniedFlow(t *testing.T) {
	ctx := context.Background()
	_, redisClient, store, _, bridge := newApprovalBridgeIntegrationHarness(t, 2*time.Second, 2*time.Second, time.Hour)

	ticketID := insertApprovalBridgeTestTicket(t, ctx, store, "session-deny-int", "turn-deny-int")

	type result struct {
		decision ApprovalDecision
		err      error
	}
	done := make(chan result, 1)
	go func() {
		decision, err := bridge.WaitForDecision(ctx, ticketID, "session-deny-int", "turn-deny-int")
		done <- result{decision: decision, err: err}
	}()

	waitForApprovalSubscriber(t, ctx, redisClient, ticketID)

	if err := store.UpdateStatus(ctx, ticketID, "denied", "U-DENIER"); err != nil {
		t.Fatalf("UpdateStatus(denied) error = %v, want nil", err)
	}
	if err := redisClient.Publish(ctx, "approvals:"+ticketID, "denied").Err(); err != nil {
		t.Fatalf("Publish(denied) error = %v, want nil", err)
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("WaitForDecision() error = %v, want nil", got.err)
	}
	if got.decision.Approved {
		t.Fatal("decision.Approved = true, want false")
	}
	if got.decision.TicketID != ticketID {
		t.Fatalf("decision.TicketID = %q, want %q", got.decision.TicketID, ticketID)
	}

	assertTicketStatus(t, ctx, store.pool, ticketID, "denied")
}

func TestApprovalBridgeIntegrationTimeoutUpdatesTicketToExpired(t *testing.T) {
	ctx := context.Background()
	_, _, store, _, bridge := newApprovalBridgeIntegrationHarness(t, 100*time.Millisecond, 2*time.Second, time.Hour)

	ticketID := insertApprovalBridgeTestTicket(t, ctx, store, "session-timeout-int", "turn-timeout-int")

	_, err := bridge.WaitForDecision(ctx, ticketID, "session-timeout-int", "turn-timeout-int")
	if !errors.Is(err, ErrApprovalTimeout) {
		t.Fatalf("WaitForDecision() error = %v, want ErrApprovalTimeout", err)
	}

	assertTicketStatus(t, ctx, store.pool, ticketID, "expired")
}

func TestApprovalBridgeIntegrationUpdateStatusIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, _, store, _, _ := newApprovalBridgeIntegrationHarness(t, 2*time.Second, 2*time.Second, time.Hour)

	ticketID := insertApprovalBridgeTestTicket(t, ctx, store, "session-idempotent-int", "turn-idempotent-int")

	if err := store.UpdateStatus(ctx, ticketID, "approved", "U-FIRST"); err != nil {
		t.Fatalf("UpdateStatus(first) error = %v, want nil", err)
	}
	if err := store.UpdateStatus(ctx, ticketID, "approved", "U-SECOND"); err != nil {
		t.Fatalf("UpdateStatus(second) error = %v, want nil", err)
	}

	var (
		status     string
		decisionBy string
	)
	err := pool.QueryRow(ctx, `SELECT status, decision_by FROM ticket WHERE id = $1`, ticketID).Scan(&status, &decisionBy)
	if err != nil {
		t.Fatalf("SELECT ticket row: %v", err)
	}
	if status != "approved" {
		t.Fatalf("status = %q, want %q", status, "approved")
	}
	if decisionBy != "U-FIRST" {
		t.Fatalf("decision_by = %q, want %q (second idempotent call must be a no-op)", decisionBy, "U-FIRST")
	}
}

func TestApprovalBridgeIntegrationExtendsSessionLockWhileWaiting(t *testing.T) {
	ctx := context.Background()
	lockTTL := 2 * time.Second // large enough that the lock survives subscriber setup under -race
	pool, redisClient, store, locker, bridge := newApprovalBridgeIntegrationHarness(t, 10*time.Second, lockTTL, 50*time.Millisecond)

	sessionID := "session-extend-int"
	turnID := "turn-extend-int"
	ticketID := insertApprovalBridgeTestTicket(t, ctx, store, sessionID, turnID)

	if err := locker.Acquire(ctx, sessionID, turnID); err != nil {
		t.Fatalf("Acquire() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		_ = locker.Release(context.Background(), sessionID, turnID)
	})

	type result struct {
		decision ApprovalDecision
		err      error
	}
	done := make(chan result, 1)
	go func() {
		decision, err := bridge.WaitForDecision(ctx, ticketID, sessionID, turnID)
		done <- result{decision: decision, err: err}
	}()

	waitForApprovalSubscriber(t, ctx, redisClient, ticketID)

	time.Sleep(250 * time.Millisecond)

	lockTTLAfterWait := redisClient.TTL(ctx, sessionLockKey(sessionID)).Val()
	if lockTTLAfterWait <= 0 {
		t.Fatalf("session lock TTL after waiting = %v, want > 0 (bridge should extend lock)", lockTTLAfterWait)
	}

	if err := store.UpdateStatus(ctx, ticketID, "approved", "U-EXTEND"); err != nil {
		t.Fatalf("UpdateStatus(approved) error = %v, want nil", err)
	}
	if err := redisClient.Publish(ctx, "approvals:"+ticketID, "approved").Err(); err != nil {
		t.Fatalf("Publish(approved) error = %v, want nil", err)
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("WaitForDecision() error = %v, want nil", got.err)
	}
	if !got.decision.Approved {
		t.Fatal("decision.Approved = false, want true")
	}

	assertTicketStatus(t, ctx, pool, ticketID, "approved")
}

func newApprovalBridgeIntegrationHarness(t *testing.T, timeout, lockTTL, lockExtendInterval time.Duration) (*pgxpool.Pool, *redis.Client, *TicketStore, *SessionLocker, *RedisApprovalBridge) {
	t.Helper()

	ctx := context.Background()
	pool, err := NewDBPool(ctx, testSchemaDSN(t, testPostgresDSN(t)))
	if err != nil {
		t.Fatalf("NewDBPool() error = %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	if err := MigrateSchema(ctx, pool); err != nil {
		t.Fatalf("MigrateSchema() error = %v, want nil", err)
	}

	redisClient, err := NewRedisClient(Config{RedisDSN: testRedisDSN(t)})
	if err != nil {
		t.Fatalf("NewRedisClient() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		_ = redisClient.Close()
	})

	store := NewTicketStore(pool)
	locker := NewSessionLocker(redisClient, lockTTL, 250*time.Millisecond)
	bridge := NewRedisApprovalBridge(redisClient, store, locker, lockTTL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bridge.timeout = timeout
	bridge.lockExtendInterval = lockExtendInterval

	return pool, redisClient, store, locker, bridge
}

func insertApprovalBridgeTestTicket(t *testing.T, ctx context.Context, store *TicketStore, sessionID, turnID string) string {
	t.Helper()

	ticketID, err := store.Insert(ctx, TicketRecord{
		SessionID: sessionID,
		TurnID:    turnID,
		ToolName:  "refund_large",
		Arguments: json.RawMessage(`{"amount":9001}`),
		ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Insert() error = %v, want nil", err)
	}
	return ticketID
}

func assertTicketStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ticketID, want string) {
	t.Helper()

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM ticket WHERE id = $1`, ticketID).Scan(&status); err != nil {
		t.Fatalf("SELECT ticket status: %v", err)
	}
	if status != want {
		t.Fatalf("status = %q, want %q", status, want)
	}
}

func waitForApprovalSubscriber(t *testing.T, ctx context.Context, redisClient *redis.Client, ticketID string) {
	t.Helper()

	channel := "approvals:" + ticketID
	deadline := time.Now().Add(2 * time.Second)
	for {
		counts, err := redisClient.PubSubNumSub(ctx, channel).Result()
		if err != nil {
			t.Fatalf("PubSubNumSub(%q) error = %v", channel, err)
		}
		if counts[channel] > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for Redis subscriber on %q", channel)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
