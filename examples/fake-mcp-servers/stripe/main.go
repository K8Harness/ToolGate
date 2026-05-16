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

// RefundParams defines the input parameters for the demo refund tools.
type RefundParams struct {
	Amount     int    `json:"amount"`
	CustomerID string `json:"customer_id"`
}

// DeleteRecordParams defines the input parameters for the demo delete tool.
type DeleteRecordParams struct {
	CustomerID string `json:"customer_id"`
}

// SendSlackMessageParams defines the input parameters for the demo Slack tool.
type SendSlackMessageParams struct {
	Channel string `json:"channel"`
	Message string `json:"message"`
}

func cannedJSONResult(payload string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: payload},
		},
	}
}

func createChargeHandler(_ context.Context, _ *mcp.CallToolRequest, _ CreateChargeParams) (*mcp.CallToolResult, any, error) {
	return cannedJSONResult(`{"id":"ch_fake_001","status":"succeeded"}`), nil, nil
}

func getCustomerHandler(_ context.Context, _ *mcp.CallToolRequest, args GetCustomerParams) (*mcp.CallToolResult, any, error) {
	payload := fmt.Sprintf(`{"id":%q,"name":"Test Customer"}`, args.CustomerID)
	return cannedJSONResult(payload), nil, nil
}

func refundSmallHandler(_ context.Context, _ *mcp.CallToolRequest, args RefundParams) (*mcp.CallToolResult, any, error) {
	payload := fmt.Sprintf(`{"ok":true,"tool":"refund_small","amount":%d,"customer_id":%q}`, args.Amount, args.CustomerID)
	return cannedJSONResult(payload), nil, nil
}

func refundLargeHandler(_ context.Context, _ *mcp.CallToolRequest, args RefundParams) (*mcp.CallToolResult, any, error) {
	payload := fmt.Sprintf(`{"ok":true,"tool":"refund_large","amount":%d,"customer_id":%q}`, args.Amount, args.CustomerID)
	return cannedJSONResult(payload), nil, nil
}

func deleteRecordHandler(_ context.Context, _ *mcp.CallToolRequest, args DeleteRecordParams) (*mcp.CallToolResult, any, error) {
	payload := fmt.Sprintf(`{"ok":true,"tool":"delete_record","customer_id":%q,"deleted":true}`, args.CustomerID)
	return cannedJSONResult(payload), nil, nil
}

func sendSlackMessageHandler(_ context.Context, _ *mcp.CallToolRequest, args SendSlackMessageParams) (*mcp.CallToolResult, any, error) {
	payload := fmt.Sprintf(`{"ok":true,"tool":"send_slack_message","channel":%q,"message":%q}`, args.Channel, args.Message)
	return cannedJSONResult(payload), nil, nil
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
		createChargeHandler,
	)

	// Register get_customer tool: returns a deterministic customer object.
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_customer",
			Description: "Retrieve a Stripe customer (fake)",
		},
		getCustomerHandler,
	)

	// Register the demo contract tools used by the eval-gate support agent.
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "refund_small",
			Description: "Process a small refund (demo contract)",
		},
		refundSmallHandler,
	)
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "refund_large",
			Description: "Process a large refund after approval (demo contract)",
		},
		refundLargeHandler,
	)
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "delete_record",
			Description: "Delete a customer record (demo contract)",
		},
		deleteRecordHandler,
	)
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "send_slack_message",
			Description: "Send a Slack message (demo contract)",
		},
		sendSlackMessageHandler,
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
