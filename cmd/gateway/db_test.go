package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const testPostgresDSNEnv = "TOOLGATE_TEST_POSTGRES_DSN"

func TestNewDBPoolConfiguresPoolAndPingsPostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := NewDBPool(ctx, testSchemaDSN(t, testPostgresDSN(t)))
	if err != nil {
		t.Fatalf("NewDBPool() error = %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	cfg := pool.Config()
	if cfg.MaxConns != 5 {
		t.Fatalf("MaxConns = %d, want 5", cfg.MaxConns)
	}
	if cfg.MinConns != 1 {
		t.Fatalf("MinConns = %d, want 1", cfg.MinConns)
	}
	if cfg.MaxConnIdleTime != 5*time.Minute {
		t.Fatalf("MaxConnIdleTime = %s, want 5m", cfg.MaxConnIdleTime)
	}
	if cfg.HealthCheckPeriod != time.Minute {
		t.Fatalf("HealthCheckPeriod = %s, want 1m", cfg.HealthCheckPeriod)
	}
}

func TestMigrateSchemaCreatesExpectedTablesIndexesAndConstraints(t *testing.T) {
	ctx := context.Background()
	pool, err := NewDBPool(ctx, testSchemaDSN(t, testPostgresDSN(t)))
	if err != nil {
		t.Fatalf("NewDBPool() error = %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	if err := MigrateSchema(ctx, pool); err != nil {
		t.Fatalf("MigrateSchema() first call error = %v, want nil", err)
	}
	if err := MigrateSchema(ctx, pool); err != nil {
		t.Fatalf("MigrateSchema() second call error = %v, want nil", err)
	}

	assertTableExists(t, ctx, pool, "audit_log")
	assertTableExists(t, ctx, pool, "ticket")

	assertColumnExists(t, ctx, pool, "audit_log", "session_id", "text")
	assertColumnExists(t, ctx, pool, "audit_log", "turn_id", "text")
	assertColumnExists(t, ctx, pool, "audit_log", "tool_name", "text")
	assertColumnExists(t, ctx, pool, "audit_log", "arguments", "jsonb")
	assertColumnExists(t, ctx, pool, "audit_log", "decision", "text")
	assertColumnExists(t, ctx, pool, "audit_log", "reason", "text")
	assertColumnExists(t, ctx, pool, "audit_log", "decided_at", "timestamp with time zone")

	assertColumnExists(t, ctx, pool, "ticket", "session_id", "text")
	assertColumnExists(t, ctx, pool, "ticket", "turn_id", "text")
	assertColumnExists(t, ctx, pool, "ticket", "tool_name", "text")
	assertColumnExists(t, ctx, pool, "ticket", "arguments", "jsonb")
	assertColumnExists(t, ctx, pool, "ticket", "status", "text")
	assertColumnExists(t, ctx, pool, "ticket", "decision_by", "text")
	assertColumnExists(t, ctx, pool, "ticket", "decided_at", "timestamp with time zone")
	assertColumnExists(t, ctx, pool, "ticket", "expires_at", "timestamp with time zone")
	assertColumnExists(t, ctx, pool, "ticket", "payload", "jsonb")
	assertColumnExists(t, ctx, pool, "ticket", "created_at", "timestamp with time zone")

	assertIndexExists(t, ctx, pool, "audit_log", "audit_log_session_turn")
	assertIndexExists(t, ctx, pool, "ticket", "ticket_status_expires")

	assertConstraintContains(
		t,
		ctx,
		pool,
		"audit_log",
		[]string{"decision", "allow", "deny", "approvalRequired", "budgetExceeded"},
	)
	assertConstraintContains(
		t,
		ctx,
		pool,
		"ticket",
		[]string{"status", "pending", "approved", "rejected", "expired", "cancelled"},
	)
}

func TestMigrateSchemaUsesPostgresClockForAuditLogDecidedAt(t *testing.T) {
	ctx := context.Background()
	pool, err := NewDBPool(ctx, testSchemaDSN(t, testPostgresDSN(t)))
	if err != nil {
		t.Fatalf("NewDBPool() error = %v, want nil", err)
	}
	t.Cleanup(pool.Close)

	if err := MigrateSchema(ctx, pool); err != nil {
		t.Fatalf("MigrateSchema() error = %v, want nil", err)
	}

	args, err := json.Marshal(map[string]any{"amount": 42})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v, want nil", err)
	}

	var decidedAt time.Time
	var serverNow time.Time
	err = pool.QueryRow(
		ctx,
		`INSERT INTO audit_log (session_id, turn_id, tool_name, arguments, decision, reason)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING decided_at, CURRENT_TIMESTAMP`,
		"session-1",
		"turn-1",
		"refund",
		args,
		"allow",
		"test insert",
	).Scan(&decidedAt, &serverNow)
	if err != nil {
		t.Fatalf("insert audit_log row: %v", err)
	}

	if !decidedAt.Equal(serverNow) {
		t.Fatalf("decided_at = %s, want server CURRENT_TIMESTAMP %s", decidedAt, serverNow)
	}
}

func testSchemaDSN(t *testing.T, baseDSN string) string {
	t.Helper()

	adminPool, err := pgxpool.New(context.Background(), baseDSN)
	if err != nil {
		t.Fatalf("pgxpool.New(admin) error = %v", err)
	}
	t.Cleanup(adminPool.Close)

	schemaName := fmt.Sprintf("policy_gate_db_test_%d", time.Now().UnixNano())
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

func testPostgresDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv(testPostgresDSNEnv)
	if dsn == "" {
		t.Skipf("%s is not set; skipping real Postgres migration test", testPostgresDSNEnv)
	}
	return dsn
}

func assertTableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tableName string) {
	t.Helper()

	var exists bool
	err := pool.QueryRow(
		ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = current_schema()
			  AND table_name = $1
		)`,
		tableName,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("check table %q exists: %v", tableName, err)
	}
	if !exists {
		t.Fatalf("table %q does not exist in schema %q", tableName, currentSchema(t, ctx, pool))
	}
}

func assertColumnExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tableName, columnName, dataType string) {
	t.Helper()

	var actualDataType string
	err := pool.QueryRow(
		ctx,
		`SELECT data_type
		 FROM information_schema.columns
		 WHERE table_schema = current_schema()
		   AND table_name = $1
		   AND column_name = $2`,
		tableName,
		columnName,
	).Scan(&actualDataType)
	if err != nil {
		t.Fatalf("lookup column %s.%s: %v", tableName, columnName, err)
	}
	if actualDataType != dataType {
		t.Fatalf("%s.%s data_type = %q, want %q", tableName, columnName, actualDataType, dataType)
	}
}

func assertIndexExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tableName, indexName string) {
	t.Helper()

	var exists bool
	err := pool.QueryRow(
		ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM pg_indexes
			WHERE schemaname = current_schema()
			  AND tablename = $1
			  AND indexname = $2
		)`,
		tableName,
		indexName,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("check index %q exists: %v", indexName, err)
	}
	if !exists {
		t.Fatalf("index %q does not exist for table %q", indexName, tableName)
	}
}

func assertConstraintContains(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tableName string, wantParts []string) {
	t.Helper()

	rows, err := pool.Query(
		ctx,
		`SELECT pg_get_constraintdef(c.oid)
		 FROM pg_constraint c
		 JOIN pg_class rel ON rel.oid = c.conrelid
		 JOIN pg_namespace nsp ON nsp.oid = rel.relnamespace
		 WHERE nsp.nspname = current_schema()
		   AND rel.relname = $1
		   AND c.contype = 'c'`,
		tableName,
	)
	if err != nil {
		t.Fatalf("query constraints for %q: %v", tableName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var definition string
		if err := rows.Scan(&definition); err != nil {
			t.Fatalf("scan constraint definition for %q: %v", tableName, err)
		}

		allPresent := true
		for _, wantPart := range wantParts {
			if !strings.Contains(definition, wantPart) {
				allPresent = false
				break
			}
		}
		if allPresent {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate constraints for %q: %v", tableName, err)
	}

	t.Fatalf("no check constraint for %q contained all parts %v", tableName, wantParts)
}

func currentSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()

	var schemaName string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schemaName); err != nil {
		t.Fatalf("SELECT current_schema(): %v", err)
	}
	return schemaName
}

func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
