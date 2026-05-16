package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const createPgcryptoExtensionSQL = `CREATE EXTENSION IF NOT EXISTS pgcrypto`

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS audit_log (
		id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
		session_id  TEXT        NOT NULL,
		turn_id     TEXT        NOT NULL,
		tool_name   TEXT        NOT NULL,
		arguments   JSONB       NOT NULL,
		decision    TEXT        NOT NULL
		            CHECK (decision IN ('allow', 'deny', 'approvalRequired', 'budgetExceeded')),
		reason      TEXT,
		decided_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS audit_log_session_turn ON audit_log (session_id, turn_id)`,
	`CREATE TABLE IF NOT EXISTS ticket (
		id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
		session_id  TEXT        NOT NULL,
		turn_id     TEXT        NOT NULL,
		tool_name   TEXT        NOT NULL,
		arguments   JSONB       NOT NULL,
		status      TEXT        NOT NULL DEFAULT 'pending'
		            CONSTRAINT ticket_status_check CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'cancelled')),
		decision_by TEXT,
		decided_at  TIMESTAMPTZ,
		expires_at  TIMESTAMPTZ NOT NULL,
		payload     JSONB,
		created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS ticket_status_expires ON ticket (status, expires_at)`,
	// Repair: replace legacy 'rejected' check constraint with 'denied' (approval-flow canonical value).
	// Idempotent: no-op when ticket_status_check already has the correct definition.
	`DO $$
DECLARE
	cname TEXT;
BEGIN
	SELECT conname INTO cname
	FROM pg_constraint
	WHERE conrelid = 'ticket'::regclass
	  AND contype = 'c'
	  AND pg_get_constraintdef(oid) LIKE '%rejected%';
	IF cname IS NOT NULL THEN
		EXECUTE format('ALTER TABLE ticket DROP CONSTRAINT %I', cname);
		ALTER TABLE ticket ADD CONSTRAINT ticket_status_check
			CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'cancelled'));
	END IF;
END $$`,
}

func NewDBPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}

	cfg.MaxConns = 5
	cfg.MinConns = 1
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return pool, nil
}

func MigrateSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, createPgcryptoExtensionSQL); err != nil {
		return fmt.Errorf("ensure pgcrypto extension: %w", err)
	}

	for _, statement := range schemaStatements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("apply schema statement: %w", err)
		}
	}

	return nil
}
