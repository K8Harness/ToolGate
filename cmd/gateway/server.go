package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/K8Harness/ToolGate/core/mcp"
)

const (
	mcpRoutePath       = "/mcp"
	mcpSessionIDHeader = "Mcp-Session-Id"
	codeInvalidRequest = -32600
)

var (
	keepaliveInterval    = 30 * time.Second
	keepaliveDeadline    = 5 * time.Second
	approvalWriteTimeout = 6 * time.Minute
)

type Server struct {
	config       *Config
	pipeline     *mcp.Pipeline
	forwarder    mcp.Handler
	guard        *ConcurrencyGuard
	slackWebhook http.Handler
	sessions     *SessionRegistry
	mux          *http.ServeMux
	log          *slog.Logger
}

func NewServer(config *Config, pipeline *mcp.Pipeline, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	server := &Server{
		config:   config,
		pipeline: pipeline,
		sessions: &SessionRegistry{},
		mux:      http.NewServeMux(),
		log:      log,
	}
	server.mux.HandleFunc("POST "+mcpRoutePath, server.handleMCPPost)
	server.mux.HandleFunc("GET "+mcpRoutePath, server.handleMCPGet)
	server.mux.HandleFunc("DELETE "+mcpRoutePath, server.handleMCPDelete)
	return server
}

func (s *Server) SetSlackWebhookHandler(handler http.Handler) {
	s.slackWebhook = handler
	if handler != nil {
		s.mux.Handle("POST /slack/actions", handler)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if recover() != nil {
			s.errorResponse(w, nil, mcp.CodeInternalError, "internal error")
		}
	}()
	s.mux.ServeHTTP(w, r)
}

func (s *Server) ListenAndServe() error {
	return s.httpServer().ListenAndServe()
}

func (s *Server) httpServer() *http.Server {
	addr := fmt.Sprintf(":%d", s.config.ListenPort)
	return &http.Server{
		Addr:         addr,
		Handler:      s,
		WriteTimeout: approvalWriteTimeout,
	}
}

func (s *Server) handleMCPPost(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSONRPCRequest(r.Body)
	if err != nil {
		s.errorResponse(w, nil, mcp.CodeParseError, "parse error")
		return
	}

	if req.Method == "initialize" {
		session := s.sessions.Create()
		w.Header().Set(mcpSessionIDHeader, session.ID)
		s.writeInitializeResponse(w, r.Context(), req)
		return
	}

	loggedCtx := s.requestContext(r, r.Header.Get(mcpSessionIDHeader))
	r = r.WithContext(loggedCtx)

	sessionID, ok := s.validatedSessionID(r)
	if !ok {
		if req.Method == "tools/call" {
			NewRequestLogger(s.log).LogOutcome(r.Context(), req, mcp.NewErrorResponse(req.ID, codeInvalidRequest, "invalid request"), nil)
		}
		s.errorResponse(w, req.ID, codeInvalidRequest, "invalid request")
		return
	}

	r = s.withRequestContext(r, sessionID)
	if s.pipeline == nil {
		if req.Method == "tools/call" {
			NewRequestLogger(s.log).LogOutcome(r.Context(), req, mcp.NewErrorResponse(req.ID, mcp.CodeInternalError, "internal error"), nil)
		}
		s.errorResponse(w, req.ID, mcp.CodeInternalError, "internal error")
		return
	}

	toolName := ""
	if req.Method == "tools/call" {
		if name, ok := toolNameFromParams(req.Params); ok {
			toolName = name
		}
	}

	resp, err := s.runPipeline(r.Context(), sessionID, toolName, req)
	if err != nil {
		if req.Method == "tools/call" {
			NewRequestLogger(s.log).LogOutcome(r.Context(), req, nil, err)
		}
		s.errorResponse(w, req.ID, jsonRPCCode(err), err.Error())
		return
	}

	if req.Method == "tools/call" {
		NewRequestLogger(s.log).LogOutcome(r.Context(), req, resp, nil)
	}
	s.writeJSONResponse(w, resp)
}

func (s *Server) runPipeline(
	ctx context.Context,
	sessionID string,
	toolName string,
	req *mcp.JSONRPCRequest,
) (*mcp.JSONRPCResponse, error) {
	if s.guard == nil {
		return s.pipeline.Run(ctx, req)
	}

	return s.guard.Execute(ctx, sessionID, mcp.TurnIDFromContext(ctx), toolName, func() (*mcp.JSONRPCResponse, error) {
		return s.pipeline.Run(ctx, req)
	})
}

func (s *Server) handleMCPGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.validatedSessionID(r); !ok {
		s.errorResponse(w, nil, codeInvalidRequest, "invalid request")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()

	controller := http.NewResponseController(w)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := controller.SetWriteDeadline(time.Now().Add(keepaliveDeadline)); err != nil {
				return
			}
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleMCPDelete(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := s.validatedSessionID(r)
	if !ok {
		s.errorResponse(w, nil, codeInvalidRequest, "invalid request")
		return
	}

	s.sessions.Delete(sessionID)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) requestContext(r *http.Request, sessionID string) context.Context {
	turnID := r.Header.Get(s.config.TurnIDHeader)
	if turnID == "" {
		turnID = newSessionID()
	}

	ctx := mcp.WithSessionID(r.Context(), sessionID)
	return mcp.WithTurnID(ctx, turnID)
}

func (s *Server) withRequestContext(r *http.Request, sessionID string) *http.Request {
	return r.WithContext(s.requestContext(r, sessionID))
}

func (s *Server) validatedSessionID(r *http.Request) (string, bool) {
	sessionID := r.Header.Get(mcpSessionIDHeader)
	if sessionID == "" {
		return "", false
	}
	if _, ok := s.sessions.Get(sessionID); !ok {
		return "", false
	}
	return sessionID, true
}

func (s *Server) writeInitializeResponse(w http.ResponseWriter, ctx context.Context, req *mcp.JSONRPCRequest) {
	if s.forwarder == nil {
		s.errorResponse(w, req.ID, mcp.CodeInternalError, "internal error")
		return
	}

	resp, err := s.forwarder.Handle(ctx, req)
	if err != nil {
		s.errorResponse(w, req.ID, jsonRPCCode(err), err.Error())
		return
	}
	s.writeJSONResponse(w, resp)
}

func decodeJSONRPCRequest(body io.Reader) (*mcp.JSONRPCRequest, error) {
	var req mcp.JSONRPCRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

func (s *Server) writeJSONResponse(w http.ResponseWriter, resp *mcp.JSONRPCResponse) {
	if resp == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.Error("write JSON-RPC response", "error", err)
	}
}

func (s *Server) errorResponse(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	s.writeJSONResponse(w, mcp.NewErrorResponse(id, code, message))
}

func jsonRPCCode(err error) int {
	type codeError interface {
		JSONRPCCode() int
	}

	var coded codeError
	if errors.As(err, &coded) {
		return coded.JSONRPCCode()
	}
	return mcp.CodeInternalError
}
