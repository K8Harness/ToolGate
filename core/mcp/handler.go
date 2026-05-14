package mcp

import "context"

// Handler processes an MCP JSON-RPC request in the pipeline.
//
// Return contract:
//   - (nil, nil) means continue to the next handler.
//   - (nil, error) means halt with that error.
//   - (*JSONRPCResponse, nil) means halt with that response.
type Handler interface {
	Handle(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error)
}

// HandlerFunc adapts a function to the Handler interface.
type HandlerFunc func(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error)

func (f HandlerFunc) Handle(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	return f(ctx, req)
}
