package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const auditInsertSQL = `INSERT INTO audit_log (session_id, turn_id, tool_name, arguments, decision, reason) VALUES ($1, $2, $3, $4, $5, $6)`

type auditExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

type AuditRecord struct {
	SessionID string
	TurnID    string
	ToolName  string
	Arguments json.RawMessage
	Decision  string
	Reason    string
}

type AuditWriter struct {
	ch        chan AuditRecord
	pool      auditExecer
	log       *slog.Logger
	startOnce sync.Once
	wg        sync.WaitGroup
}

func NewAuditWriter(pool *pgxpool.Pool, log *slog.Logger) *AuditWriter {
	if pool == nil {
		panic("audit writer requires a configured insert backend")
	}

	return newAuditWriter(pool, log)
}

func newAuditWriter(pool auditExecer, log *slog.Logger) *AuditWriter {
	if pool == nil {
		panic("audit writer requires a configured insert backend")
	}

	if log == nil {
		log = slog.Default()
	}

	return &AuditWriter{
		ch:   make(chan AuditRecord, 256),
		pool: pool,
		log:  log,
	}
}

func (w *AuditWriter) Start(ctx context.Context) {
	w.startOnce.Do(func() {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()

			for {
				select {
				case <-ctx.Done():
					return
				case record := <-w.ch:
					if err := w.insert(ctx, record); err != nil {
						w.log.WarnContext(
							ctx,
							"audit log insert failed",
							"sessionId", record.SessionID,
							"turnId", record.TurnID,
							"toolName", record.ToolName,
							"decision", record.Decision,
							"error", err,
						)
					}
				}
			}
		}()
	})
}

func (w *AuditWriter) Write(record AuditRecord) {
	select {
	case w.ch <- record:
	default:
		w.log.Warn(
			"audit log channel full; dropping record",
			"sessionId", record.SessionID,
			"turnId", record.TurnID,
			"toolName", record.ToolName,
			"decision", record.Decision,
		)
	}
}

func (w *AuditWriter) insert(ctx context.Context, record AuditRecord) error {
	if w.pool == nil {
		return fmt.Errorf("audit writer misconfigured: missing insert backend")
	}

	_, err := w.pool.Exec(
		ctx,
		auditInsertSQL,
		record.SessionID,
		record.TurnID,
		record.ToolName,
		record.Arguments,
		record.Decision,
		record.Reason,
	)
	return err
}
