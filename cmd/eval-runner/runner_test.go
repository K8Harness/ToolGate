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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestCaseRunnerRunReturnsTraceRowsInDecidedAtOrder(t *testing.T) {
	ctx := context.Background()
	pool := newRunnerTestPool(t, ctx)
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS audit_log (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			tool_name TEXT NOT NULL,
			arguments JSONB NOT NULL,
			decision TEXT NOT NULL,
			reason TEXT,
			decided_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		t.Fatalf("CREATE TABLE audit_log: %v", err)
	}

	rowsToInsert := []struct {
		toolName  string
		decision  string
		arguments string
		decidedAt time.Time
	}{
		{
			toolName:  "lookup_customer",
			decision:  "allow",
			arguments: `{"customer_id":"c1"}`,
			decidedAt: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
		},
		{
			toolName:  "create_ticket",
			decision:  "approvalRequired",
			arguments: `{"amount":12000}`,
			decidedAt: time.Date(2026, time.January, 2, 3, 4, 6, 0, time.UTC),
		},
		{
			toolName:  "send_lark_message",
			decision:  "allow",
			arguments: `{"message":"approved"}`,
			decidedAt: time.Date(2026, time.January, 2, 3, 4, 7, 0, time.UTC),
		},
	}

	for i, row := range rowsToInsert {
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO audit_log (session_id, turn_id, tool_name, arguments, decision, reason, decided_at)
			 VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7)`,
			"s1",
			fmt.Sprintf("turn-%d", i+1),
			row.toolName,
			row.arguments,
			row.decision,
			"test",
			row.decidedAt,
		); err != nil {
			t.Fatalf("INSERT audit_log row %d: %v", i, err)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("request method = %q, want %q", r.Method, http.MethodPost)
		}
		if r.URL.Path != "/trigger" {
			t.Fatalf("request path = %q, want /trigger", r.URL.Path)
		}

		var payload struct {
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("Decode(request body): %v", err)
		}
		if payload.Input != "large-refund" {
			t.Fatalf("request input = %q, want large-refund", payload.Input)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"session_id": "s1"}); err != nil {
			t.Fatalf("Encode(response): %v", err)
		}
	}))
	defer server.Close()

	runner := NewCaseRunner(server.URL, pool)
	trace, err := runner.Run(ctx, EvalCase{Name: "large-refund-approval", Input: "large-refund"})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	want := []TraceRow{
		{ToolName: "lookup_customer", Decision: "allow", Arguments: json.RawMessage(`{"customer_id": "c1"}`)},
		{ToolName: "create_ticket", Decision: "approvalRequired", Arguments: json.RawMessage(`{"amount": 12000}`)},
		{ToolName: "send_lark_message", Decision: "allow", Arguments: json.RawMessage(`{"message": "approved"}`)},
	}
	if len(trace) != len(want) {
		t.Fatalf("len(trace) = %d, want %d", len(trace), len(want))
	}
	for i := range want {
		if trace[i].ToolName != want[i].ToolName {
			t.Fatalf("trace[%d].ToolName = %q, want %q", i, trace[i].ToolName, want[i].ToolName)
		}
		if trace[i].Decision != want[i].Decision {
			t.Fatalf("trace[%d].Decision = %q, want %q", i, trace[i].Decision, want[i].Decision)
		}
		if string(trace[i].Arguments) != string(want[i].Arguments) {
			t.Fatalf("trace[%d].Arguments = %s, want %s", i, trace[i].Arguments, want[i].Arguments)
		}
	}
}

func TestCaseRunnerRunReturnsErrorOnTriggerFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "trigger exploded", http.StatusBadGateway)
	}))
	defer server.Close()

	runner := NewCaseRunner(server.URL, nil)
	trace, err := runner.Run(context.Background(), EvalCase{Name: "trigger-failure", Input: "any"})
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil")
	}
	if trace != nil {
		t.Fatalf("Run() trace = %#v, want nil", trace)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("Run() error = %q, want status code", err.Error())
	}
	if !strings.Contains(err.Error(), "trigger exploded") {
		t.Fatalf("Run() error = %q, want response body excerpt", err.Error())
	}
}

func TestCaseRunnerRunReturnsErrorWhenDBPoolClosed(t *testing.T) {
	ctx := context.Background()
	pool := newRunnerTestPool(t, ctx)

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS audit_log (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			session_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			tool_name TEXT NOT NULL,
			arguments JSONB NOT NULL,
			decision TEXT NOT NULL,
			reason TEXT,
			decided_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		t.Fatalf("CREATE TABLE audit_log: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"session_id": "s1"}); err != nil {
			t.Fatalf("Encode(response): %v", err)
		}
	}))
	defer server.Close()

	pool.Close()

	runner := NewCaseRunner(server.URL, pool)
	trace, err := runner.Run(ctx, EvalCase{Name: "closed-db", Input: "any"})
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil")
	}
	if trace != nil {
		t.Fatalf("Run() trace = %#v, want nil", trace)
	}
}

func newRunnerTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping Postgres container test in short mode")
	}

	container, err := postgres.Run(
		ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("gateway"),
		postgres.WithUsername("gateway"),
		postgres.WithPassword("gateway"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("postgres testcontainer unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Fatalf("TerminateContainer() error = %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("ConnectionString() error = %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New(testcontainer dsn) error = %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("pool.Ping() error = %v", err)
	}

	if _, err := pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pgcrypto`); err != nil {
		pool.Close()
		t.Fatalf("CREATE EXTENSION pgcrypto: %v", err)
	}

	return pool
}
