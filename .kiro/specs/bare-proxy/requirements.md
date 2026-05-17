# Requirements Document

## Introduction

ToolGate's bare-proxy is the first vertical slice of the v0 MCP gateway. It gives AI platform teams a transparent Go proxy that intercepts MCP SDK tool calls over SSE, enriches each call with session and turn metadata, and forwards it to a configurable upstream MCP server — providing the extensible forwarding pipeline that all subsequent governance slices (policy, locking, approval) will build upon.

## Boundary Context

- **In scope**: SSE endpoint, SessionID and TurnID assignment, MCP JSON-RPC payload enrichment (meta injection), transparent forwarding to a single configurable upstream MCP server, extensible per-request handler registration, structured logging, upstream and handler error propagation
- **Out of scope**: Policy evaluation of any kind, Redis, Postgres, approval routing, eval orchestration, authentication, multi-upstream routing, any form of persistence
- **Adjacent expectations**: The extensible handler interface established here is the integration point for `policy-gate` (slice 2). That slice registers handlers into this pipeline; this spec does not implement any handlers beyond transparent forwarding.

## Requirements

### Requirement 1: SSE Endpoint and Session Management

**Objective:** As an AI platform developer, I want the gateway to accept MCP SDK connections over HTTP/SSE, so that agent tool calls can be intercepted and governed without changes to the agent or the upstream MCP server.

#### Acceptance Criteria
1. When an MCP client connects to the gateway's SSE endpoint, the Gateway shall establish the SSE connection and assign a unique SessionID to that connection.
2. While an SSE connection is active, the Gateway shall maintain the connection and process all MCP JSON-RPC messages received over it.
3. When an SSE connection closes (client disconnect or server shutdown), the Gateway shall release the associated SessionID.
4. The Gateway shall accept connections on a configurable port (default: 8080) at the path `/sse`.

### Requirement 2: Turn Identification

**Objective:** As an AI platform developer, I want each request turn to carry a traceable TurnID, so that multi-step agent interactions can be correlated across tool calls.

#### Acceptance Criteria
1. When an MCP `tools/call` request is received and a TurnID header is present, the Gateway shall extract and use that value as the TurnID for the request.
2. When an MCP `tools/call` request is received and no TurnID header is present, the Gateway shall generate a unique TurnID for that request.
3. The Gateway shall use the same TurnID for all tool calls received within a single turn.

### Requirement 3: MCP Payload Enrichment

**Objective:** As an AI platform developer, I want session and turn metadata injected into every outgoing MCP payload, so that upstream MCP servers and downstream governance layers can correlate calls without requiring changes to the agent.

#### Acceptance Criteria
1. When the Gateway forwards an MCP JSON-RPC request, the Gateway shall inject the SessionID and TurnID into the `meta` field of the payload before forwarding.
2. When the `meta` field already exists in the incoming MCP payload, the Gateway shall merge the SessionID and TurnID into it without removing existing keys.
3. When the `meta` field does not exist in the incoming MCP payload, the Gateway shall create it containing the SessionID and TurnID.
4. The Gateway shall not modify any field of the MCP JSON-RPC payload other than `meta`.

### Requirement 4: Upstream Forwarding

**Objective:** As an AI platform developer, I want tool calls forwarded transparently to the configured upstream MCP server, so that agent behavior is unchanged while the proxy intercepts the call path.

#### Acceptance Criteria
1. When the Gateway receives an MCP `tools/call` request, the Gateway shall forward the enriched request to the configured upstream MCP server and return the upstream response to the MCP client.
2. The Gateway shall read the upstream MCP server URL from a configurable source at startup.
3. If the upstream MCP server URL is not configured at startup, the Gateway shall refuse to start and emit an error indicating the missing configuration.
4. If the upstream MCP server returns an error response, the Gateway shall propagate the error response to the MCP client unchanged.
5. If the upstream MCP server is unreachable, the Gateway shall return a JSON-RPC error response to the MCP client indicating the upstream is unavailable.

### Requirement 5: Extensible Request Processing Pipeline

**Objective:** As an AI platform developer, I want the Gateway's forwarding pipeline to support registered per-request handlers, so that governance logic (policy evaluation, concurrency locking, approval routing) can be added by later slices without modifying the core proxy.

#### Acceptance Criteria
1. The Gateway shall support registration of per-request handlers that execute sequentially before the upstream forward is issued.
2. When a registered handler signals that a request should be halted, the Gateway shall return the handler-provided JSON-RPC error response to the MCP client and shall not forward the request to the upstream.
3. When no handlers are registered, the Gateway shall forward requests directly to the upstream with no additional processing.
4. The Gateway shall apply registered handlers in the order they were registered.

### Requirement 6: Error Handling and Resilience

**Objective:** As an AI platform developer, I want the gateway to return well-formed JSON-RPC error responses for all failure cases, so that MCP clients receive structured feedback rather than hanging connections or unhandled crashes.

#### Acceptance Criteria
1. If the Gateway receives a malformed MCP JSON-RPC payload, the Gateway shall return a JSON-RPC parse error response to the MCP client.
2. If an upstream request exceeds the configured timeout, the Gateway shall return a JSON-RPC error response to the MCP client indicating a timeout.
3. The Gateway shall not terminate or crash due to a per-request error; errors shall be isolated to the affected request.

### Requirement 7: Structured Logging and Observability

**Objective:** As an AI platform operator, I want structured log output for each request, so that I can trace individual tool calls during development and testing without a dedicated tracing backend.

#### Acceptance Criteria
1. When the Gateway processes an MCP `tools/call` request, the Gateway shall emit a structured log entry containing: SessionID, TurnID, MCP tool name, MCP operation, and the forwarding outcome (success or error).
2. The Gateway shall emit log entries in a structured, machine-readable format.
3. The Gateway shall not log MCP payload argument values by default, to avoid inadvertent credential or PII exposure in logs.
