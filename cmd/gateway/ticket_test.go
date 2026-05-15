package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

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
