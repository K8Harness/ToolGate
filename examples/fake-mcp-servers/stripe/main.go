// Package main implements a fake Stripe MCP server for eval-gate testing.
// It serves deterministic canned responses over Streamable HTTP (POST /mcp) on port 8082.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CreateChargeParams defines the input parameters for the create_charge tool.
type CreateChargeParams struct {
	Amount     int    `json:"amount"`
	Currency   string `json:"currency"`
	CustomerID string `json:"customer_id"`
}

// GetCustomerParams defines the input parameters for the get_customer tool.
type GetCustomerParams struct {
	CustomerID string `json:"customer_id"`
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "fake-stripe",
		Version: "1.0.0",
	}, nil)

	// Register create_charge tool: returns a deterministic charge object.
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "create_charge",
			Description: "Create a Stripe charge (fake, always succeeds)",
		},
		func(_ context.Context, _ *mcp.CallToolRequest, _ CreateChargeParams) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: `{"id":"ch_fake_001","status":"succeeded"}`},
				},
			}, nil, nil
		},
	)

	// Register get_customer tool: returns a deterministic customer object.
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_customer",
			Description: "Retrieve a Stripe customer (fake)",
		},
		func(_ context.Context, _ *mcp.CallToolRequest, args GetCustomerParams) (*mcp.CallToolResult, any, error) {
			payload := fmt.Sprintf(`{"id":%q,"name":"Test Customer"}`, args.CustomerID)
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

	log.Println("fake-stripe MCP server listening on :8082")
	if err := http.ListenAndServe(":8082", mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
