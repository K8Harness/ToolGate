// Package main implements a fake Slack MCP server for eval-gate testing.
// It serves over Streamable HTTP (POST /mcp) on port 8084 and exposes
// GET /inspect to return a log of all tool calls received so far.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SendSlackMessageParams defines the input parameters for the send_slack_message tool.
type SendSlackMessageParams struct {
	Channel string `json:"channel"`
	Message string `json:"message"`
}

var (
	mu    sync.Mutex
	calls = []map[string]any{}
)

// sendSlackMessageHandler records the call and returns {"ok":true}.
func sendSlackMessageHandler(_ context.Context, _ *mcp.CallToolRequest, args SendSlackMessageParams) (*mcp.CallToolResult, any, error) {
	mu.Lock()
	calls = append(calls, map[string]any{"channel": args.Channel, "message": args.Message})
	mu.Unlock()
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: `{"ok":true}`},
		},
	}, nil, nil
}

// inspectHandler returns {"calls":[...]} with all recorded send_slack_message arguments.
func inspectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mu.Lock()
	snapshot := make([]map[string]any, len(calls))
	copy(snapshot, calls)
	mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"calls": snapshot})
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "fake-slack",
		Version: "1.0.0",
	}, nil)

	// Register send_slack_message tool: records the call and returns {"ok":true}.
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "send_slack_message",
			Description: "Send a Slack message (fake, always succeeds)",
		},
		sendSlackMessageHandler,
	)

	// Serve Streamable HTTP transport on /mcp.
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return server
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpHandler)
	mux.HandleFunc("/inspect", inspectHandler)

	log.Println("fake-slack MCP server listening on :8084")
	if err := http.ListenAndServe(":8084", mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
