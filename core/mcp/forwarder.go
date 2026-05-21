package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

type UpstreamForwarder struct {
	upstreamURL       string
	httpClient        *http.Client
	mu                sync.Mutex
	upstreamSessionID string
}

type upstreamError struct {
	code int
	msg  string
	err  error
}

func (e *upstreamError) Error() string {
	if e.err == nil {
		return e.msg
	}
	return e.msg + ": " + e.err.Error()
}

func (e *upstreamError) Unwrap() error {
	return e.err
}

func (e *upstreamError) JSONRPCCode() int {
	return e.code
}

func NewUpstreamForwarder(upstreamURL string, timeout time.Duration) *UpstreamForwarder {
	return &UpstreamForwarder{
		upstreamURL: upstreamURL,
		httpClient:  &http.Client{Timeout: timeout},
	}
}

func (f *UpstreamForwarder) Handle(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	resp, stale, err := f.doRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	// Upstream session was lost (upstream restart); re-initialize and retry once.
	if stale {
		if reinitErr := f.revalidateSession(ctx, req); reinitErr != nil {
			return nil, reinitErr
		}
		resp, _, err = f.doRequest(ctx, req)
	}
	return resp, err
}

// doRequest sends req to the upstream. It returns stale=true when the upstream
// rejected the request with 404/400 because our cached session is no longer valid.
func (f *UpstreamForwarder) doRequest(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, bool, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, false, wrapUpstreamError("encode upstream request", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, f.upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, false, wrapUpstreamError("build upstream request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	f.mu.Lock()
	sessionID := f.upstreamSessionID
	f.mu.Unlock()
	if sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", sessionID)
	}

	httpResp, err := f.httpClient.Do(httpReq)
	if err != nil {
		return nil, false, wrapUpstreamError(upstreamFailureMessage(err), err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if req.Method == "initialize" {
		if id := httpResp.Header.Get("Mcp-Session-Id"); id != "" {
			f.mu.Lock()
			f.upstreamSessionID = id
			f.mu.Unlock()
		}
	}

	// Stale session: upstream restarted and dropped our session.
	if sessionID != "" && req.Method != "initialize" &&
		(httpResp.StatusCode == http.StatusNotFound || httpResp.StatusCode == http.StatusBadRequest) {
		f.mu.Lock()
		f.upstreamSessionID = ""
		f.mu.Unlock()
		return nil, true, nil
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, false, wrapUpstreamError(fmt.Sprintf("upstream returned status %d", httpResp.StatusCode), nil)
	}

	resp, err := decodeUpstreamResponse(httpResp)
	if err != nil {
		return nil, false, wrapUpstreamError("decode upstream response", err)
	}
	return resp, false, nil
}

// revalidateSession sends a minimal initialize to the upstream to establish a
// fresh session, discarding the result (the real initialize response was already
// sent to the client from the first connect).
func (f *UpstreamForwarder) revalidateSession(ctx context.Context, original *JSONRPCRequest) error {
	initReq := &JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      original.ID,
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"gateway","version":"1.0.0"}}`),
	}
	_, _, err := f.doRequest(ctx, initReq)
	return err
}

func decodeUpstreamResponse(resp *http.Response) (*JSONRPCResponse, error) {
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		mediaType = ""
	}

	var payload []byte
	if mediaType == "text/event-stream" {
		payload, err = readFirstSSEData(resp.Body)
		if err != nil {
			return nil, err
		}
	} else {
		payload, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
	}

	var out JSONRPCResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func readFirstSSEData(r io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("missing SSE data line")
}

func wrapUpstreamError(message string, err error) error {
	return &upstreamError{
		code: CodeInternalError,
		msg:  message,
		err:  err,
	}
}

func upstreamFailureMessage(err error) string {
	if err == nil {
		return "upstream request failed"
	}
	if strings.Contains(err.Error(), "Client.Timeout") || strings.Contains(err.Error(), "context deadline exceeded") {
		return "upstream timeout"
	}
	return "upstream request failed"
}
