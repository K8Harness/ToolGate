# Implementation Plan

- [ ] 1. Foundation — module setup and shared MCP types
- [x] 1.1 Initialize Go module, directory skeleton, and dependencies
  - Run `go mod init` with the project module path; create the directory tree matching the File Structure Plan: `cmd/gateway/`, `core/mcp/`
  - Add `github.com/modelcontextprotocol/go-sdk` via `go get`; run `go mod tidy` to pin all transitive dependencies
  - Commit `go.mod` and `go.sum`; verify `go build ./...` passes against empty stub files in each package
  - Confirm: `go build ./...` exits 0 with no errors and the two-directory tree (`cmd/gateway`, `core/mcp`) is present
  - _Requirements: 4.2_

- [x] 1.2 Define shared MCP JSON-RPC types and context key utilities
  - Define `JSONRPCRequest`, `JSONRPCResponse`, `JSONRPCError`, and `MCPMeta` structs with correct JSON field tags
  - Define typed context key constants (`ContextKeySessionID`, `ContextKeyTurnID`) and the four accessor/setter functions (`SessionIDFromContext`, `TurnIDFromContext`, `WithSessionID`, `WithTurnID`)
  - Define error code constants `CodeParseError = -32700` and `CodeInternalError = -32603`
  - Implement `NewErrorResponse(id, code, message)` constructor that returns a well-formed `*JSONRPCResponse`
  - Confirm: `go build ./core/mcp/...` succeeds; a standalone test calling `NewErrorResponse` asserts the returned struct has `jsonrpc: "2.0"`, the correct `code`, and the provided `message`
  - _Requirements: 3.1, 3.2, 3.3, 4.1, 6.1_

- [ ] 2. Pipeline infrastructure
- [x] 2.1 Define the Handler interface and function adapter
  - Declare `Handler` interface with `Handle(ctx context.Context, req *JSONRPCRequest) (*JSONRPCResponse, error)`
  - Declare `HandlerFunc` type that wraps the same signature and satisfies `Handler`
  - Document the three-value return contract in the source: `(nil, nil)` = continue; `(nil, error)` = halt with error; `(*JSONRPCResponse, nil)` = halt with response
  - Confirm: a `HandlerFunc` literal assigned to a `Handler` variable compiles without type assertion; `go vet ./core/mcp/...` reports no issues
  - _Requirements: 5.1, 5.2, 5.3, 5.4_

- [x] 2.2 Implement Pipeline with ordered registration and halt-aware execution
  - Implement `Pipeline` struct holding a registered-handler slice and a non-nil terminal handler
  - `NewPipeline(terminal Handler)` panics if `terminal` is nil
  - `Use(h Handler)` appends to the handler slice; safe to call only before `ListenAndServe`
  - `Run(ctx, req)` iterates the slice in registration order; returns the first non-nil result from any handler; calls terminal when all middleware handlers return `(nil, nil)`
  - Confirm: when two handlers are registered and the first returns a non-nil error, `Run` returns that error and the second handler and terminal are not invoked (verified by a spy handler that records calls)
  - _Requirements: 5.1, 5.2, 5.3, 5.4_

- [ ] 3. Core gateway components
- [x] 3.1 (P) Implement configuration loading with startup validation
  - Load `GATEWAY_PORT` (default 8080), `UPSTREAM_MCP_URL` (required), `TURN_ID_HEADER` (default `X-Mcp-Turn-Id`), `UPSTREAM_TIMEOUT` (default 30s), `SESSION_TTL` (default 60m) from environment variables
  - `LoadConfig()` returns a non-nil `error` — not a panic — when `UPSTREAM_MCP_URL` is empty or unset
  - Confirm: `LoadConfig()` with `UPSTREAM_MCP_URL` unset returns an error whose message names the missing variable; with only `UPSTREAM_MCP_URL` set, returns a `Config` with `ListenPort == 8080` and `TurnIDHeader == "X-Mcp-Turn-Id"`
  - _Requirements: 1.4, 4.2, 4.3_
  - _Boundary: Config_

- [x] 3.2 (P) Build the upstream MCP forwarder
  - Note: this is the only Task 3 subtask in `core/mcp`; all other Task 3 subtasks live in `cmd/gateway` — no file contention among parallel workers
  - Implement `UpstreamForwarder` as a `Handler` that `POST`s the `JSONRPCRequest` to the configured upstream URL with `Content-Type: application/json` and `Accept: application/json, text/event-stream`
  - Detect upstream response content-type: unmarshal `application/json` body directly; for `text/event-stream`, read lines until a `data:` line, then unmarshal that payload
  - Return `(nil, wrappedError)` with code `-32603` for network errors, non-200 status codes, response decode failures, and timeout
  - Propagate well-formed JSON-RPC error responses from the upstream unchanged — do not re-wrap them in an outer error
  - Confirm: a test with a stub upstream returning `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}` results in the `UpstreamForwarder` returning a `*JSONRPCResponse` whose `Error.Code` is `-32601` and `Error.Message` is `"method not found"`, with no additional wrapping
  - _Requirements: 4.1, 4.4, 4.5, 6.2_
  - _Boundary: UpstreamForwarder_

- [x] 3.3 (P) Implement the in-memory session registry
  - Store `Session{ID string, CreatedAt time.Time}` values in a `sync.Map`
  - `Create()` generates a session ID using `crypto/rand` (not `math/rand`) and stores the session before returning it
  - `Get(id)` and `Delete(id)` are safe for concurrent use without additional locking
  - Confirm: two goroutines calling `Create()` concurrently return distinct, non-empty IDs; a session retrieved via `Get()` immediately after `Create()` returns `true`; the same session is absent (`false`) after `Delete()`
  - _Requirements: 1.1, 1.3_
  - _Boundary: SessionRegistry_

- [ ] 3.4 (P) Build the context injector pipeline handler
  - Only act on `tools/call` requests; return `(nil, nil)` immediately for all other method names
  - Read `sessionId` and `turnId` from the request context using the helpers defined in `core/mcp/types.go`
  - Unmarshal `req.Params` as a raw JSON object; read the existing `_meta` key if present; set `sessionId` and `turnId` keys without removing other keys (including `progressToken`); re-marshal `_meta` and `params` back into `req.Params`
  - Return `(nil, error)` with code `-32603` if any marshal or unmarshal step fails; never forward a partially-mutated payload
  - Confirm: a `tools/call` request with `_meta.progressToken = "tok-1"` in the context arrives at the next handler with `_meta.progressToken` still set to `"tok-1"` alongside the injected `sessionId` and `turnId`
  - _Requirements: 3.1, 3.2, 3.3, 3.4_
  - _Boundary: ContextInjector_

- [ ] 3.5 (P) Build the request logger pipeline handler
  - Emit one structured log entry per `Handle` call using `log/slog` with a `slog.NewJSONHandler` writing to stdout
  - Log fields for all requests: `sessionId` (from context), `turnId` (from context), `method` (from `req.Method`)
  - For `tools/call` requests only, additionally log `toolName` extracted from `params.name`; for all other methods, omit `toolName`
  - Never read or log `params.arguments`
  - Always return `(nil, nil)` to continue the pipeline after logging
  - Confirm: a `tools/call` invocation produces exactly one JSON log line containing keys `sessionId`, `turnId`, `method`, and `toolName`; the key `arguments` does not appear anywhere in the output; a `tools/list` invocation produces a line with `sessionId`, `turnId`, and `method` but no `toolName`
  - _Requirements: 7.1, 7.2, 7.3_
  - _Boundary: RequestLogger_

- [ ] 4. HTTP server and binary wiring
- [ ] 4.1 Build the HTTP server skeleton with session lifecycle and request context enrichment
  - Register `POST /mcp`, `GET /mcp`, and `DELETE /mcp` routes using Go 1.22 `ServeMux` method+path patterns
  - `POST /mcp` — `initialize` path: create a session in the registry, set `Mcp-Session-Id` response header, forward the `initialize` request directly to the upstream forwarder (bypassing the pipeline handlers)
  - `POST /mcp` — all other methods: validate the `Mcp-Session-Id` header against the registry; return `-32600` if absent or unknown; extract TurnID from `Config.TurnIDHeader` header or generate a new one when the header is absent; store both IDs in the request context via the helpers in `core/mcp/types.go`; decode the request body as `JSONRPCRequest` and return `-32700` on parse failure
  - `GET /mcp`: validate `Mcp-Session-Id`; set SSE response headers; send an SSE comment keepalive (`": keepalive\n\n"`) every 30 seconds until `r.Context().Done()` fires; use `http.NewResponseController` to set a 5-second write deadline per keepalive write
  - `DELETE /mcp`: validate `Mcp-Session-Id`; call `SessionRegistry.Delete`; return `200 OK`
  - Confirm: `POST /mcp` with a missing `Mcp-Session-Id` header and method `tools/call` returns a JSON body with `error.code == -32600`; `POST /mcp` with an invalid (non-JSON) body and any valid session returns `error.code == -32700`
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 2.1, 2.2, 2.3_

- [ ] 4.2 Integration — wire pipeline execution into the HTTP server with error handling and panic recovery
  - In `handleMCPPost` (for non-initialize requests), call `pipeline.Run(ctx, req)` after context enrichment; write the returned `*JSONRPCResponse` as `application/json` with status `200`; if `Run` returns an error, call the internal `errorResponse` helper with the error's JSON-RPC code and message
  - Implement `errorResponse(w, id, code, message)` to write a well-formed `JSONRPCResponse` with the provided error fields
  - Wrap `ServeHTTP` with a deferred `recover()`; on panic, call `errorResponse` with `-32603` and `"internal error"` before returning
  - Confirm: a handler registered with `Use` that calls `panic("test")` causes the server to return a `-32603` JSON-RPC error response to the client without terminating the server process; a subsequent valid request to the same server succeeds
  - _Requirements: 5.2, 6.1, 6.3_

- [ ] 4.3 Assemble and start the gateway binary
  - In `main.go`: call `LoadConfig()`; exit non-zero with an error message to stderr if config loading fails (before binding any port)
  - Construct `UpstreamForwarder` with the upstream URL and timeout from config; construct `Pipeline` with the forwarder as terminal; register `RequestLogger` then `ContextInjector` via `Use`
  - Construct `Server` with the pipeline, session registry, config, and a JSON slog logger; call `ListenAndServe`
  - Confirm: `UPSTREAM_MCP_URL=http://localhost:9999 go run ./cmd/gateway` prints a startup message and listens on port 8080 (verified by a successful TCP connect); running without `UPSTREAM_MCP_URL` prints an error to stderr and exits with a non-zero code without binding any port
  - _Requirements: 4.3_

- [ ] 5. Unit tests
- [ ] 5.1 (P) Pipeline execution semantics unit tests
  - Empty pipeline (no `Use` calls): `Run` calls terminal directly and returns its result
  - Single middleware returning `(nil, nil)`: terminal is called, terminal's result is returned
  - Single middleware returning `(nil, error)`: terminal is NOT called; the error is propagated to the caller
  - Single middleware returning `(*JSONRPCResponse, nil)`: terminal is NOT called; the response is propagated
  - Two middlewares: execution order matches registration order; the second middleware is not called when the first halts
  - Confirm: all five cases pass as a table-driven test in `core/mcp/pipeline_test.go`; a spy `HandlerFunc` asserts call counts
  - _Requirements: 5.1, 5.2, 5.3, 5.4_
  - _Boundary: Pipeline_

- [ ] 5.2 (P) ContextInjector _meta mutation unit tests
  - `tools/call` with no `_meta` in params: output `_meta` contains exactly `sessionId` and `turnId`, no other keys
  - `tools/call` with `_meta.progressToken = "tok-1"`: output `_meta` has `progressToken`, `sessionId`, and `turnId`
  - Non-`tools/call` method (e.g., `tools/list`): `req.Params` is byte-for-byte identical after `Handle` returns
  - Malformed `params` (invalid JSON): returns a non-nil error; `req.Params` is not modified
  - Confirm: all four cases pass in `cmd/gateway/injector_test.go`; the merge case explicitly asserts that `progressToken` equals `"tok-1"` in the resulting params
  - _Requirements: 3.1, 3.2, 3.3, 3.4_
  - _Boundary: ContextInjector_

- [ ] 5.3 (P) RequestLogger field safety unit tests
  - `tools/call` log entry contains keys `sessionId`, `turnId`, `method`, and `toolName`
  - `tools/call` log entry does not contain the key `arguments` nor any value from the arguments map
  - `tools/list` log entry contains `sessionId`, `turnId`, and `method` but no `toolName`
  - Confirm: all three assertions pass in `cmd/gateway/logger_test.go` by capturing slog output into a `bytes.Buffer` and JSON-decoding the emitted line to inspect keys
  - _Requirements: 7.1, 7.2, 7.3_
  - _Boundary: RequestLogger_

- [ ] 5.4 (P) SessionRegistry concurrent operations unit tests
  - Sequential: `Create()` returns a non-empty ID; `Get(id)` returns `(session, true)`; `Delete(id)` followed by `Get(id)` returns `(nil, false)`
  - Concurrent: ten goroutines each call `Create()` concurrently; assert all ten returned IDs are distinct
  - Confirm: all assertions pass in `cmd/gateway/session_test.go` with no data-race warnings under `go test -race`
  - _Requirements: 1.1, 1.3_
  - _Boundary: SessionRegistry_

- [ ] 6. Integration and validation
- [ ] 6.1 Integration tests with inline fake upstream and full HTTP client
  - Stand up the complete gateway (`Server` + `Pipeline` + all handlers) via `httptest.NewServer`; use a second `httptest.NewServer` as the fake upstream that captures and validates received requests
  - Test 1 — session init and tools/call: `POST /mcp {initialize}` → assert `Mcp-Session-Id` header in response; `POST /mcp {tools/call}` with that header → fake upstream asserts `_meta.sessionId` and `_meta.turnId` are present
  - Test 2 — TurnID from header: send `X-Mcp-Turn-Id: T1` → fake upstream asserts `_meta.turnId == "T1"`
  - Test 3 — TurnID generated: send no `X-Mcp-Turn-Id` → fake upstream asserts `_meta.turnId` is non-empty
  - Test 4 — _meta merge: send params with `_meta.progressToken = "tok-2"` → fake upstream asserts `progressToken` present alongside injected keys
  - Test 5 — upstream error propagation: fake upstream returns `{"error":{"code":-32602,"message":"bad params"}}` → client receives the same error code and message
  - Test 6 — upstream unreachable: point gateway at a closed port → client receives `error.code == -32603` within timeout
  - Test 7 — malformed request body: send non-JSON body → client receives `error.code == -32700`
  - Test 8 — missing session: send `tools/call` without `Mcp-Session-Id` → client receives `error.code == -32600`
  - Test 9 — SSE keepalive (req 1.2): `GET /mcp` with valid `Mcp-Session-Id`; set `X-Accel-Buffering: no`; read the first line within 35 seconds; assert it is a valid SSE comment (starts with `:`); close the connection
  - Test 10 — per-request panic isolation (req 6.3): register a pipeline handler via `Use` that panics; send a `tools/call`; assert the response is `-32603`; send a second valid request to the same server; assert the server is still alive and returns a success response
  - Confirm: all ten test cases pass in a single integration test file; the fake upstream's request inspector uses `json.Unmarshal` and asserts exact field presence and values
  - _Requirements: 1.1, 1.2, 2.1, 2.2, 2.3, 3.1, 3.2, 3.3, 3.4, 4.1, 4.4, 4.5, 6.1, 6.3_

- [ ]* 6.2 E2E test — live binary with real HTTP client
  - Build the gateway binary with `go build -o /tmp/agentplane-gateway ./cmd/gateway` and start it as a subprocess pointing at a local fake upstream
  - Use `net/http` (not `httptest`) to send a real `POST /mcp {initialize}` request and assert a `200` response containing a `Mcp-Session-Id` header
  - Send `DELETE /mcp` with the session ID; assert `200 OK`; wait for the subprocess to exit cleanly after `SIGTERM`
  - Confirm: the binary binds port 8080, responds correctly to a real HTTP client, and exits cleanly; the response body is valid JSON with `jsonrpc: "2.0"`
  - _Requirements: 1.1, 1.4, 4.1, 4.3_
