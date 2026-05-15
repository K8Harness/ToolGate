package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestTicketStoreUpdateStatusTransitionsPendingToApproved(t *testing.T) {
	ctx := context.Background()
	pool, err := NewDBPool(ctx, testSchemaDSN(t, testPostgresDSN(t)))
	if err != nil {
		t.Fatalf("NewDBPool() error = %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	if err := MigrateSchema(ctx, pool); err != nil {
		t.Fatalf("MigrateSchema() error = %v, want nil", err)
	}

	store := NewTicketStore(pool)
	expiresAt := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Microsecond)
	record := TicketRecord{
		SessionID: "session-update-status",
		TurnID:    "turn-update-status",
		ToolName:  "refund_large",
		Arguments: json.RawMessage(`{"amount":9001}`),
		ExpiresAt: expiresAt,
	}

	id, err := store.Insert(ctx, record)
	if err != nil {
		t.Fatalf("Insert() error = %v, want nil", err)
	}

	const decidedBy = "U0123ABC"
	if err := store.UpdateStatus(ctx, id, "approved", decidedBy); err != nil {
		t.Fatalf("UpdateStatus() error = %v, want nil", err)
	}

	var (
		status     string
		decisionBy string
		decidedAt  time.Time
	)
	err = pool.QueryRow(
		ctx,
		`SELECT status, decision_by, decided_at FROM ticket WHERE id = $1`,
		id,
	).Scan(&status, &decisionBy, &decidedAt)
	if err != nil {
		t.Fatalf("SELECT updated ticket row: %v", err)
	}

	if status != "approved" {
		t.Fatalf("status = %q, want %q", status, "approved")
	}
	if decisionBy != decidedBy {
		t.Fatalf("decision_by = %q, want %q", decisionBy, decidedBy)
	}
	if decidedAt.IsZero() {
		t.Fatal("decided_at is zero, want Postgres-populated timestamp")
	}
}

func TestTicketStoreUpdateStatusIsIdempotentForTerminalStatus(t *testing.T) {
	ctx := context.Background()
	pool, err := NewDBPool(ctx, testSchemaDSN(t, testPostgresDSN(t)))
	if err != nil {
		t.Fatalf("NewDBPool() error = %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	if err := MigrateSchema(ctx, pool); err != nil {
		t.Fatalf("MigrateSchema() error = %v, want nil", err)
	}

	store := NewTicketStore(pool)
	expiresAt := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Microsecond)
	record := TicketRecord{
		SessionID: "session-idempotent",
		TurnID:    "turn-idempotent",
		ToolName:  "refund_large",
		Arguments: json.RawMessage(`{"amount":100}`),
		ExpiresAt: expiresAt,
	}

	id, err := store.Insert(ctx, record)
	if err != nil {
		t.Fatalf("Insert() error = %v, want nil", err)
	}

	// First transition: pending -> approved
	if err := store.UpdateStatus(ctx, id, "approved", "U0123ABC"); err != nil {
		t.Fatalf("UpdateStatus() first call error = %v, want nil", err)
	}

	// Second transition: already approved, attempting denied — must succeed silently (idempotent)
	if err := store.UpdateStatus(ctx, id, "denied", "U9999ZZZ"); err != nil {
		t.Fatalf("UpdateStatus() second call (idempotent) error = %v, want nil", err)
	}

	var status string
	err = pool.QueryRow(ctx, `SELECT status FROM ticket WHERE id = $1`, id).Scan(&status)
	if err != nil {
		t.Fatalf("SELECT ticket status: %v", err)
	}

	// Status must still be "approved" — the second call must have been a no-op
	if status != "approved" {
		t.Fatalf("status = %q after idempotent call, want %q (no update expected)", status, "approved")
	}
}

func TestNewTicketStorePanicsOnNilPool(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewTicketStore(nil) did not panic")
		}
	}()

	NewTicketStore(nil)
}

func TestTicketStoreInsertCreatesPendingTicketRow(t *testing.T) {
	ctx := context.Background()
	pool, err := NewDBPool(ctx, testSchemaDSN(t, testPostgresDSN(t)))
	if err != nil {
		t.Fatalf("NewDBPool() error = %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	if err := MigrateSchema(ctx, pool); err != nil {
		t.Fatalf("MigrateSchema() error = %v, want nil", err)
	}

	store := NewTicketStore(pool)
	expiresAt := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Microsecond)
	record := TicketRecord{
		SessionID: "session-ticket",
		TurnID:    "turn-ticket",
		ToolName:  "refund_large",
		Arguments: json.RawMessage(`{"amount":9001}`),
		ExpiresAt: expiresAt,
	}

	id, err := store.Insert(ctx, record)
	if err != nil {
		t.Fatalf("Insert() error = %v, want nil", err)
	}
	if id == "" {
		t.Fatal("Insert() id = empty string, want generated UUID")
	}

	var (
		sessionID string
		turnID    string
		toolName  string
		arguments []byte
		status    string
		gotExpiry time.Time
		createdAt time.Time
	)
	err = pool.QueryRow(
		ctx,
		`SELECT session_id, turn_id, tool_name, arguments, status, expires_at, created_at
		 FROM ticket
		 WHERE id = $1`,
		id,
	).Scan(&sessionID, &turnID, &toolName, &arguments, &status, &gotExpiry, &createdAt)
	if err != nil {
		t.Fatalf("SELECT inserted ticket row: %v", err)
	}

	if sessionID != record.SessionID {
		t.Fatalf("session_id = %q, want %q", sessionID, record.SessionID)
	}
	if turnID != record.TurnID {
		t.Fatalf("turn_id = %q, want %q", turnID, record.TurnID)
	}
	if toolName != record.ToolName {
		t.Fatalf("tool_name = %q, want %q", toolName, record.ToolName)
	}
	var gotArguments map[string]any
	if err := json.Unmarshal(arguments, &gotArguments); err != nil {
		t.Fatalf("json.Unmarshal(inserted arguments) error = %v, want nil", err)
	}
	var wantArguments map[string]any
	if err := json.Unmarshal(record.Arguments, &wantArguments); err != nil {
		t.Fatalf("json.Unmarshal(want arguments) error = %v, want nil", err)
	}
	if got, want := gotArguments["amount"], wantArguments["amount"]; got != want {
		t.Fatalf("arguments.amount = %v, want %v", got, want)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want %q", status, "pending")
	}
	if !gotExpiry.Equal(record.ExpiresAt) {
		t.Fatalf("expires_at = %s, want %s", gotExpiry, record.ExpiresAt)
	}
	if createdAt.IsZero() {
		t.Fatal("created_at is zero, want Postgres-populated timestamp")
	}
}
