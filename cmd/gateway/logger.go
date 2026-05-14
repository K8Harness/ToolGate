package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/K8Harness/ToolGate/core/mcp"
)

type RequestLogger struct {
	log *slog.Logger
}

func NewRequestLogger(log *slog.Logger) *RequestLogger {
	if log == nil {
		log = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	return &RequestLogger{log: log}
}

func (l *RequestLogger) Handle(ctx context.Context, req *mcp.JSONRPCRequest) (*mcp.JSONRPCResponse, error) {
	l.log.InfoContext(ctx, "mcp request", l.attrs(ctx, req)...)
	return nil, nil
}

func (l *RequestLogger) LogOutcome(ctx context.Context, req *mcp.JSONRPCRequest, resp *mcp.JSONRPCResponse, err error) {
	attrs := append([]any{}, l.attrs(ctx, req)...)
	if err != nil {
		attrs = append(attrs, "outcome", "error", "errorCode", jsonRPCCode(err))
		l.log.WarnContext(ctx, "mcp request outcome", attrs...)
		return
	}
	if resp != nil && resp.Error != nil {
		attrs = append(attrs, "outcome", "error", "errorCode", resp.Error.Code)
		l.log.WarnContext(ctx, "mcp request outcome", attrs...)
		return
	}
	attrs = append(attrs, "outcome", "success")
	l.log.InfoContext(ctx, "mcp request outcome", attrs...)
}

func (l *RequestLogger) attrs(ctx context.Context, req *mcp.JSONRPCRequest) []any {
	attrs := []any{
		"sessionId", mcp.SessionIDFromContext(ctx),
		"turnId", mcp.TurnIDFromContext(ctx),
		"method", req.Method,
	}

	if req.Method == "tools/call" {
		if toolName, ok := toolNameFromParams(req.Params); ok {
			attrs = append(attrs, "toolName", toolName)
		}
	}
	return attrs
}

func toolNameFromParams(rawParams json.RawMessage) (string, bool) {
	if len(rawParams) == 0 {
		return "", false
	}

	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rawParams, &params); err != nil {
		return "", false
	}
	if params.Name == "" {
		return "", false
	}
	return params.Name, true
}
