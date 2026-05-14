# Research & Design Decisions

---
**Feature**: `bare-proxy`
**Discovery Scope**: New Feature (greenfield)
**Key Findings**:
  - The legacy `HTTP+SSE` MCP transport (`GET /sse`, spec 2024-11-05) is officially deprecated. Current canonical transport is Streamable HTTP (`POST /mcp`, spec 2025-03-26 / 2025-06-18).
  - MCP session identity is already defined by spec: `Mcp-Session-Id` HTTP header (server-assigned on initialize, client echoes on subsequent requests). The Gateway owns session ID assignment.
  - There is no standard MCP field for "turn ID". The `_meta` object in `params` supports arbitrary extension keys — `_meta.turnId` is the correct injection point.
  - An official Go SDK exists (`github.com/modelcontextprotocol/go-sdk`, v1.6.0, Google co-maintained) and provides canonical MCP message types. Recommended for protocol type adoption; not used as a full proxy framework.

---

## Research Log

### MCP Transport: SSE vs Streamable HTTP

- **Context**: The design brief referenced `GET /sse` as the transport endpoint, mirroring the 2024-11-05 spec. Research was needed to confirm which transport version to target.
- **Sources Consulted**:
  - MCP specification 2025-06-18: https://modelcontextprotocol.io/specification/2025-06-18/basic/transports
  - MCP transport future blog (Dec 2025): https://blog.modelcontextprotocol.io/posts/2025-12-19-mcp-transport-future/
  - 2026 MCP Roadmap: https://blog.modelcontextprotocol.io/posts/2026-mcp-roadmap/
- **Findings**:
  - The `HTTP+SSE` transport (GET /sse endpoint, POST /messages endpoint) is deprecated as of spec 2025-03-26.
  - Streamable HTTP uses a single `POST /mcp` endpoint for client→server; `GET /mcp` opens an SSE channel for server→client notifications.
  - Client identifies sessions via `Mcp-Session-Id` header; protocol version via `MCP-Protocol-Version` header.
  - SSE response from `POST /mcp` is only used when the server needs to stream multiple results (progress events). Simple JSON-RPC responses can be `application/json`.
  - The official Go SDK (`modelcontextprotocol/go-sdk` v1.6.0) targets Streamable HTTP.
- **Implications**: Design targets Streamable HTTP (`POST /mcp`, `GET /mcp`). Req 1.4's `/sse` path is superseded by this decision. The configurable port behavior is preserved.

### MCP `_meta` Field and Session/Turn Correlation

- **Context**: Requirements reference a `meta` field for SessionID and TurnID injection. MCP spec needed inspection.
- **Sources Consulted**: MCP schema reference, official Go SDK type definitions.
- **Findings**:
  - `_meta` is an optional, extensible object in the `params` of any MCP JSON-RPC request.
  - Only `progressToken` is spec-defined inside `_meta`. All other keys are allowed as extensions.
  - `Mcp-Session-Id` (HTTP header) is the spec's session correlation mechanism. The gateway assigns it on `initialize` and validates it on subsequent requests.
  - No standard field exists for turn-level correlation. Custom `X-Mcp-Turn-Id` HTTP header + `_meta.turnId` key is the cleanest extension point.
- **Implications**: Session ID follows the MCP spec header convention. Turn ID uses a custom header + `_meta` extension. The ContextInjector merges both into `_meta` of `params` before forwarding.

### Go SSE and Middleware Patterns

- **Sources Consulted**: Thoughtbot Go SSE tutorial, go.jetify.com/sse package, net/http docs, mark3labs/mcp-go source.
- **Findings**:
  - Go 1.22 `net/http` ServeMux supports method+path routing (`POST /mcp`, `GET /mcp`) natively.
  - Standard library SSE is sufficient for v0 GET /mcp keepalive endpoint; no external SSE library needed.
  - `http.NewResponseController` (Go 1.20+) is the correct API for per-write deadlines; replaces deprecated `CloseNotifier`.
  - Two viable pipeline patterns: (A) `func(http.Handler) http.Handler` functional wrapping; (B) typed `Handler` interface with `(ctx, *JSONRPCRequest) → (*JSONRPCResponse, error)`.
  - Pattern B is preferred: it operates on typed MCP messages rather than raw HTTP, enabling policy handlers in later slices to work with structured tool call data without re-parsing.
  - Common goroutine leak: SSE handler blocked on channel send to disconnected client. Fix: buffered channels + `select` on `ctx.Done()`.
- **Implications**: Use Pattern B pipeline. Use stdlib for HTTP server and SSE GET handler. No external router needed.

### Official Go MCP SDK Evaluation

- **Sources Consulted**: github.com/modelcontextprotocol/go-sdk, github.com/mark3labs/mcp-go.
- **Findings**:
  - `modelcontextprotocol/go-sdk` v1.6.0: official, Google co-maintained, targets Streamable HTTP. Provides `StreamableHTTPHandler`, `AddSendingMiddleware`/`AddReceivingMiddleware`, and canonical MCP type definitions.
  - `mark3labs/mcp-go`: community-maintained, implements spec up to 2025-11-25, more commonly used in production. Provides hook API that resembles the pipeline pattern.
  - Both SDKs assume you are either a server or a client. A transparent proxy (which must understand the protocol but not register specific tools) is not a first-class SDK use case.
  - Using the full SDK framework for a proxy would require registering all upstream tools dynamically (tool discovery first), then forwarding each call. This adds complexity that is not required in v0 (bare proxy).
  - The SDK's MCP type definitions (`JSONRPCRequest`, `JSONRPCResponse`, etc.) can be adopted selectively without using the full framework.
- **Implications**: Adopt MCP message types from the official SDK as a dependency. Do not use the SDK's server/client framework for the proxy layer. Build a lightweight HTTP-level proxy with typed message mutation.

---

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations |
|--------|-------------|-----------|---------------------|
| Full SDK proxy | Use go-sdk Server+Client; register all upstream tools on init | Protocol compliance guaranteed | Requires tool discovery before forwarding; SDK opinionated structure may conflict with pipeline design |
| HTTP reverse proxy + JSON mutation | Use `httputil.ReverseProxy`; intercept request body to mutate `_meta` | Simple, transparent, no tool schema knowledge required | Must handle SSE response streaming through the reverse proxy |
| HTTP handler + typed pipeline (selected) | Custom HTTP handler; decode body to typed struct; run Pipeline; re-encode; POST to upstream | Full control over interception, clean Pipeline interface for later slices, no tool discovery required | More code than SDK approach; must correctly implement MCP protocol details |

**Selected**: HTTP handler + typed pipeline. Rationale: gives full control over the interception lifecycle, produces the cleanest Pipeline interface for policy-gate (slice 2) to extend, and avoids the tool-discovery complexity of the SDK proxy pattern.

---

## Design Decisions

### Decision: Streamable HTTP over Legacy SSE Transport

- **Context**: Brief specified `GET /sse` endpoint; MCP spec has moved to Streamable HTTP.
- **Alternatives Considered**:
  1. Legacy SSE (`GET /sse` + `POST /messages`) — matches brief verbatim
  2. Streamable HTTP (`POST /mcp` + `GET /mcp`) — current spec
  3. Support both — highest compatibility
- **Selected Approach**: Streamable HTTP only. The brief's constraint says "Must speak the current MCP JSON-RPC-over-SSE protocol" — Streamable HTTP is the current protocol.
- **Rationale**: v0 demo uses the Python MCP SDK which supports Streamable HTTP. Legacy SSE is on a deprecation path; building on it creates tech debt immediately.
- **Trade-offs**: Any existing agent using the old `GET /sse` transport would need updating. Acceptable for v0 (demo only, no existing users).
- **Follow-up**: If the Python demo agent's SDK version does not support Streamable HTTP, fall back to adding a legacy SSE endpoint as an alias.

### Decision: Custom Pipeline over SDK Middleware

- **Context**: Whether to use the official SDK's `AddSendingMiddleware`/`AddReceivingMiddleware` or build a custom `Handler` interface.
- **Alternatives Considered**:
  1. SDK middleware — hooks into the SDK's send/receive cycle
  2. Custom typed `Handler` interface (selected)
- **Selected Approach**: Custom `Handler` interface `Handle(ctx context.Context, req *JSONRPCRequest) → (*JSONRPCResponse, error)`.
- **Rationale**: SDK middleware would bind the pipeline to the SDK's internal types and lifecycle. The custom interface is owned by this codebase, making it straightforward for policy-gate (slice 2) and later slices to register handlers without SDK constraints.
- **Trade-offs**: More code to write vs. adopting SDK middleware. The interface is simple enough that this cost is low.
- **Follow-up**: If the SDK's transport primitives become convenient in later slices, the pipeline can still be wrapped around them.

### Decision: `_meta.sessionId` + `_meta.turnId` as Injection Keys

- **Context**: No standard MCP field for session or turn correlation.
- **Selected Approach**: Inject `sessionId` and `turnId` as custom keys inside the standard `_meta` object in `params`. Session uses the `Mcp-Session-Id` HTTP header for transport-level correlation; `_meta` carries it into the tool payload for upstream visibility.
- **Rationale**: `_meta` explicitly allows unknown extension keys per spec. Using it avoids introducing a non-standard JSON-RPC field.
- **Trade-offs**: Upstream MCP servers receive `_meta` keys they didn't request. This is spec-compliant (servers must ignore unknown `_meta` keys). If an upstream is strict about `_meta` validation, this could cause issues — low risk for v0 fake MCP servers.

### Decision: In-Memory Session Registry (no persistence)

- **Context**: Session state must be tracked per connection.
- **Selected Approach**: `sync.Map`-based in-memory registry. Session lost on gateway restart.
- **Rationale**: Postgres-backed session store is spec'd for slice 3. v0 bare-proxy does not require durability; in-memory is sufficient for the demo.
- **Trade-offs**: Gateway restart drops all active sessions. Clients must re-initialize. Acceptable for v0.
- **Follow-up**: slice 3 (session-mgmt) replaces this with Redis-backed storage.

---

## Risks & Mitigations

- **SSE streaming upstream responses**: Some MCP upstream servers may respond to `tools/call` with `Content-Type: text/event-stream` instead of `application/json`. The forwarder must detect content-type and read/parse SSE events correctly. Mitigation: implement SSE event reader in `UpstreamForwarder`; test against fake MCP server that uses both response types.
- **goroutine leak in GET /mcp handler**: SSE keepalive goroutines must exit on client disconnect. Mitigation: always `select` on `r.Context().Done()`; use `http.NewResponseController` for write deadlines.
- **Partial-write on body mutation**: If re-encoding `_meta` into `req.Params` fails, the request must not be forwarded with a partial payload. Mitigation: fail closed (return -32603 internal error) if any marshal/unmarshal step fails.
- **Python SDK Streamable HTTP support**: If the demo Python SDK version only supports legacy SSE, the gateway transport decision must be revisited. Mitigation: verify Python SDK version during demo setup in eval-gate (slice 5).

---

## References

- [MCP Specification 2025-06-18 — Transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)
- [MCP Specification 2025-06-18 — Tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)
- [modelcontextprotocol/go-sdk v1.6.0](https://github.com/modelcontextprotocol/go-sdk)
- [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go)
- [go.jetify.com/sse](https://pkg.go.dev/go.jetify.com/sse)
- [Go 1.22 ServeMux method+path routing](https://pkg.go.dev/net/http#ServeMux)
- [http.NewResponseController (Go 1.20+)](https://pkg.go.dev/net/http#NewResponseController)
- [log/slog (Go 1.21+)](https://pkg.go.dev/log/slog)
