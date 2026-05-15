package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const ticketInsertSQL = `INSERT INTO ticket (session_id, turn_id, tool_name, arguments, expires_at) VALUES ($1, $2, $3, $4, $5) RETURNING id`

type TicketRecord struct {
	SessionID string
	TurnID    string
	ToolName  string
	Arguments json.RawMessage
	ExpiresAt time.Time
}

type TicketStore struct {
	pool *pgxpool.Pool
}

func NewTicketStore(pool *pgxpool.Pool) *TicketStore {
	if pool == nil {
		panic("ticket store requires a configured postgres pool")
	}

	return &TicketStore{pool: pool}
}

func (s *TicketStore) Insert(ctx context.Context, record TicketRecord) (string, error) {
	var id string
	err := s.pool.QueryRow(
		ctx,
		ticketInsertSQL,
		record.SessionID,
		record.TurnID,
		record.ToolName,
		record.Arguments,
		record.ExpiresAt,
	).Scan(&id)
	if err != nil {
		return "", err
	}

	return id, nil
}
