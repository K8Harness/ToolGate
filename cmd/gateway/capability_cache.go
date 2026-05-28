package main

import (
	"encoding/json"
	"sync"

	"github.com/K8Harness/ToolGate/core/mcp"
)

// capabilityCache stores the last successful initialize and tools/list responses
// so the gateway can serve them when the upstream MCP server is temporarily unavailable.
type capabilityCache struct {
	mu       sync.RWMutex
	initResp json.RawMessage
	toolResp json.RawMessage
}

func (c *capabilityCache) setInit(resp *mcp.JSONRPCResponse) {
	if resp == nil {
		return
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	c.mu.Lock()
	c.initResp = b
	c.mu.Unlock()
}

func (c *capabilityCache) getInit(id json.RawMessage) *mcp.JSONRPCResponse {
	c.mu.RLock()
	b := c.initResp
	c.mu.RUnlock()
	return unmarshalWithID(b, id)
}

func (c *capabilityCache) setToolList(resp *mcp.JSONRPCResponse) {
	if resp == nil {
		return
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	c.mu.Lock()
	c.toolResp = b
	c.mu.Unlock()
}

func (c *capabilityCache) getToolList(id json.RawMessage) *mcp.JSONRPCResponse {
	c.mu.RLock()
	b := c.toolResp
	c.mu.RUnlock()
	return unmarshalWithID(b, id)
}

func unmarshalWithID(b json.RawMessage, id json.RawMessage) *mcp.JSONRPCResponse {
	if b == nil {
		return nil
	}
	var resp mcp.JSONRPCResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil
	}
	resp.ID = id
	return &resp
}
