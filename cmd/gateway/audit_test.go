package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewAuditWriterPanicsOnNilPool(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("NewAuditWriter(nil, ...) did not panic")
		}
	}()

	NewAuditWriter(nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
}

func TestAuditWriterWriteDropsAndWarnsWhenChannelFull(t *testing.T) {
	var buf bytes.Buffer
	writer := newAuditWriter(&auditExecStub{}, slog.New(slog.NewJSONHandler(&buf, nil)))

	for range cap(writer.ch) {
		writer.ch <- AuditRecord{}
	}

	done := make(chan struct{})
	go func() {
		writer.Write(AuditRecord{
			SessionID: "session-full",
			TurnID:    "turn-full",
			ToolName:  "refund",
			Arguments: json.RawMessage(`{"amount":99}`),
			Decision:  "deny",
			Reason:    "queue saturated",
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Write blocked on a full channel")
	}

	entry := decodeLogEntry(t, singleLogLine(t, &buf))
	assertLogString(t, entry, "level", "WARN")
	assertLogString(t, entry, "msg", "audit log channel full; dropping record")
	assertLogString(t, entry, "sessionId", "session-full")
	assertLogString(t, entry, "turnId", "turn-full")
	assertLogString(t, entry, "toolName", "refund")
	assertLogString(t, entry, "decision", "deny")
}

func TestAuditWriterStartExecutesInsertSQLAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stub := &auditExecStub{called: make(chan auditExecCall, 1)}
	writer := newAuditWriter(stub, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	writer.Start(ctx)

	record := AuditRecord{
		SessionID: "session-1",
		TurnID:    "turn-1",
		ToolName:  "refund",
		Arguments: json.RawMessage(`{"amount":42}`),
		Decision:  "allow",
		Reason:    "matched allow rule",
	}
	writer.Write(record)

	select {
	case call := <-stub.called:
		if call.sql != `INSERT INTO audit_log (session_id, turn_id, tool_name, arguments, decision, reason) VALUES ($1, $2, $3, $4, $5, $6)` {
			t.Fatalf("Exec SQL = %q", call.sql)
		}
		if got, want := len(call.args), 6; got != want {
			t.Fatalf("Exec arg count = %d, want %d", got, want)
		}
		if got, want := call.args[0], "session-1"; got != want {
			t.Fatalf("arg[0] = %v, want %q", got, want)
		}
		if got, want := call.args[1], "turn-1"; got != want {
			t.Fatalf("arg[1] = %v, want %q", got, want)
		}
		if got, want := call.args[2], "refund"; got != want {
			t.Fatalf("arg[2] = %v, want %q", got, want)
		}
		if got, want := string(call.args[3].(json.RawMessage)), `{"amount":42}`; got != want {
			t.Fatalf("arg[3] = %s, want %s", got, want)
		}
		if got, want := call.args[4], "allow"; got != want {
			t.Fatalf("arg[4] = %v, want %q", got, want)
		}
		if got, want := call.args[5], "matched allow rule"; got != want {
			t.Fatalf("arg[5] = %v, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for worker Exec call")
	}

	cancel()
	waitForAuditWriterStop(t, writer)
}

func TestAuditWriterStartLogsInsertFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf lockedBuffer
	writer := newAuditWriter(
		&auditExecStub{err: errors.New("insert failed")},
		slog.New(slog.NewJSONHandler(&buf, nil)),
	)
	writer.Start(ctx)

	writer.Write(AuditRecord{
		SessionID: "session-err",
		TurnID:    "turn-err",
		ToolName:  "refund",
		Arguments: json.RawMessage(`{"amount":1}`),
		Decision:  "deny",
		Reason:    "forced insert error",
	})

	deadline := time.Now().Add(time.Second)
	for {
		if line, ok := buf.singleLine(); ok {
			entry := decodeLogEntry(t, line)
			assertLogString(t, entry, "level", "WARN")
			assertLogString(t, entry, "msg", "audit log insert failed")
			assertLogString(t, entry, "sessionId", "session-err")
			assertLogString(t, entry, "turnId", "turn-err")
			assertLogString(t, entry, "toolName", "refund")
			assertLogString(t, entry, "decision", "deny")
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for insert failure log")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAuditWriterStartInsertsRowWithPostgresDecidedAt(t *testing.T) {
	ctx := context.Background()
	pool, err := NewDBPool(ctx, auditTestSchemaDSN(t, auditTestPostgresDSN(t)))
	if err != nil {
		t.Fatalf("NewDBPool() error = %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	if err := MigrateSchema(ctx, pool); err != nil {
		t.Fatalf("MigrateSchema() error = %v, want nil", err)
	}

	writerCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	writer := NewAuditWriter(pool, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	writer.Start(writerCtx)

	writer.Write(AuditRecord{
		SessionID: "session-pg",
		TurnID:    "turn-pg",
		ToolName:  "refund",
		Arguments: json.RawMessage(`{"amount":7}`),
		Decision:  "allow",
		Reason:    "postgres insert",
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		var decidedAt time.Time
		var arguments []byte
		var decision string
		err := pool.QueryRow(
			ctx,
			`SELECT decided_at, arguments, decision
			 FROM audit_log
			 WHERE session_id = $1 AND turn_id = $2 AND tool_name = $3`,
			"session-pg",
			"turn-pg",
			"refund",
		).Scan(&decidedAt, &arguments, &decision)
		if err == nil {
			if decidedAt.IsZero() {
				t.Fatal("decided_at is zero, want Postgres-populated timestamp")
			}
			var gotArguments map[string]any
			if err := json.Unmarshal(arguments, &gotArguments); err != nil {
				t.Fatalf("json.Unmarshal(arguments) error = %v, want nil", err)
			}
			if got, want := gotArguments["amount"], float64(7); got != want {
				t.Fatalf("arguments.amount = %v, want %v", got, want)
			}
			if decision != "allow" {
				t.Fatalf("decision = %q, want %q", decision, "allow")
			}
			cancel()
			waitForAuditWriterStop(t, writer)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for audit row: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type auditExecCall struct {
	sql  string
	args []any
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) singleLine() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.buf.Len() == 0 {
		return "", false
	}

	lines := strings.Split(strings.TrimSpace(b.buf.String()), "\n")
	if len(lines) != 1 {
		return "", false
	}

	return lines[0], true
}

type auditExecStub struct {
	called chan auditExecCall
	err    error
}

func (s *auditExecStub) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if s.called != nil {
		s.called <- auditExecCall{sql: sql, args: args}
	}
	return pgconn.NewCommandTag("INSERT 0 1"), s.err
}

func waitForAuditWriterStop(t *testing.T, writer *AuditWriter) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		writer.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("audit writer goroutine did not stop after context cancellation")
	}
}

func auditTestSchemaDSN(t *testing.T, baseDSN string) string {
	t.Helper()

	adminPool, err := pgxpool.New(context.Background(), baseDSN)
	if err != nil {
		t.Fatalf("pgxpool.New(admin) error = %v", err)
	}
	t.Cleanup(adminPool.Close)

	schemaName := "audit_writer_test_" + time.Now().Format("150405.000000000")
	if _, err := adminPool.Exec(context.Background(), `CREATE SCHEMA `+quoteIdentifier(schemaName)); err != nil {
		t.Fatalf("CREATE SCHEMA %q: %v", schemaName, err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(context.Background(), `DROP SCHEMA `+quoteIdentifier(schemaName)+` CASCADE`); err != nil {
			t.Fatalf("DROP SCHEMA %q: %v", schemaName, err)
		}
	})

	dsnURL, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", baseDSN, err)
	}

	query := dsnURL.Query()
	query.Set("search_path", schemaName)
	dsnURL.RawQuery = query.Encode()
	return dsnURL.String()
}

func auditTestPostgresDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv(testPostgresDSNEnv)
	if dsn == "" {
		t.Skipf("%s is not set; skipping real Postgres audit test", testPostgresDSNEnv)
	}
	return dsn
}
