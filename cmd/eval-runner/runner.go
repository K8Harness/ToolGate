package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const caseRunnerHTTPTimeout = 60 * time.Second

type CaseRunner struct {
	AgentBaseURL string
	DB           *pgxpool.Pool
	client       *http.Client
}

func NewCaseRunner(agentBaseURL string, db *pgxpool.Pool) *CaseRunner {
	return &CaseRunner{
		AgentBaseURL: strings.TrimRight(agentBaseURL, "/"),
		DB:           db,
		client: &http.Client{
			Timeout: caseRunnerHTTPTimeout,
		},
	}
}

func (r *CaseRunner) Run(ctx context.Context, c EvalCase) ([]TraceRow, error) {
	sessionID, err := r.trigger(ctx, c.Input)
	if err != nil {
		return nil, err
	}

	rows, err := r.DB.Query(
		ctx,
		`SELECT tool_name, decision, arguments
		 FROM audit_log
		 WHERE session_id = $1
		 ORDER BY decided_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var trace []TraceRow
	for rows.Next() {
		var row TraceRow
		if err := rows.Scan(&row.ToolName, &row.Decision, &row.Arguments); err != nil {
			return nil, err
		}
		trace = append(trace, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return trace, nil
}

func (r *CaseRunner) trigger(ctx context.Context, input string) (string, error) {
	body, err := json.Marshal(map[string]string{"input": input})
	if err != nil {
		return "", fmt.Errorf("marshal trigger request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.AgentBaseURL+"/trigger", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build trigger request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("trigger returned HTTP %d: %s", resp.StatusCode, firstBytes(resp.Body, 256))
	}

	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode trigger response: %w", err)
	}

	return payload.SessionID, nil
}

func firstBytes(r io.Reader, limit int64) string {
	body, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return fmt.Sprintf("read response body: %v", err)
	}
	return string(body)
}
