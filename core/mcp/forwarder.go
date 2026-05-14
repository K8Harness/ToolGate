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
	"time"
)

type UpstreamForwarder struct {
	upstreamURL string
	httpClient  *http.Client
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
	body, err := json.Marshal(req)
	if err != nil {
		return nil, wrapUpstreamError("encode upstream request", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, f.upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, wrapUpstreamError("build upstream request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	httpResp, err := f.httpClient.Do(httpReq)
	if err != nil {
		return nil, wrapUpstreamError(upstreamFailureMessage(err), err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, wrapUpstreamError(fmt.Sprintf("upstream returned status %d", httpResp.StatusCode), nil)
	}

	resp, err := decodeUpstreamResponse(httpResp)
	if err != nil {
		return nil, wrapUpstreamError("decode upstream response", err)
	}
	return resp, nil
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
