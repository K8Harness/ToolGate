// Package main implements a fake Zendesk MCP server for eval-gate testing.
// It serves deterministic canned responses over Streamable HTTP (POST /mcp) on port 8083.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CreateTicketParams defines the input parameters for the create_ticket tool.
type CreateTicketParams struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	CustomerID  string `json:"customer_id"`
}

// CloseTicketParams defines the input parameters for the close_ticket tool.
type CloseTicketParams struct {
	TicketID string `json:"ticket_id"`
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "fake-zendesk",
		Version: "1.0.0",
	}, nil)

	// Register create_ticket tool: returns a deterministic ticket object.
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "create_ticket",
			Description: "Create a Zendesk support ticket (fake, always succeeds)",
		},
		func(_ context.Context, _ *mcp.CallToolRequest, _ CreateTicketParams) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: `{"id":"tkt_fake_001","status":"open"}`},
				},
			}, nil, nil
		},
	)

	// Register close_ticket tool: returns a deterministic closed-ticket object.
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "close_ticket",
			Description: "Close a Zendesk support ticket (fake)",
		},
		func(_ context.Context, _ *mcp.CallToolRequest, args CloseTicketParams) (*mcp.CallToolResult, any, error) {
			payload := fmt.Sprintf(`{"id":%q,"status":"closed"}`, args.TicketID)
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: payload},
				},
			}, nil, nil
		},
	)

	// Serve Streamable HTTP transport on /mcp.
	// Unrecognized tool calls automatically return JSON-RPC error -32601 (go-sdk default).
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return server
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)

	log.Println("fake-zendesk MCP server listening on :8083")
	if err := http.ListenAndServe(":8083", mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
