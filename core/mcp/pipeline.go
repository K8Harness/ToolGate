package mcp

import "context"

// Pipeline executes registered handlers in order before a terminal handler.
type Pipeline struct {
	handlers []Handler
	terminal Handler
}

func NewPipeline(terminal Handler) *Pipeline {
	if terminal == nil {
		panic("mcp: nil terminal handler")
	}
	return &Pipeline{terminal: terminal}
}

// Use registers a handler to execute before the terminal.
// It must be called before serving starts and is not safe for concurrent use.
func (p *Pipeline) Use(h Handler) {
	p.handlers = append(p.handlers, h)
}

// Run executes handlers in registration order, then the terminal.
// It returns the first non-nil response or error from the chain.
func (p *Pipeline) Run(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error) {
	for _, handler := range p.handlers {
		resp, err := handler.Handle(ctx, req)
		if resp != nil || err != nil {
			return resp, err
		}
	}
	return p.terminal.Handle(ctx, req)
}
