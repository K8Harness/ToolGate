# Brief: bare-proxy

## Problem

AI agents using the MCP SDK send `tools/call` requests over SSE. There is no interception layer — calls go directly to MCP servers with no session tracking, no policy, and no audit trail. Before any governance logic can be added, a working MCP-transparent proxy must exist.

## Current State

No code exists. This is a greenfield Go binary. The design documents (`AgentPlane_revised (1).md`, `v0.md`) specify the protocol and component responsibilities.

## Desired Outcome

A Go binary (`cmd/gateway`) that:
- Accepts MCP SDK connections over HTTP/SSE on `:8080/sse`
- Generates a unique `SessionID` per connection and a `TurnID` from request headers
- Injects `SessionID` and `TurnID` into the `meta` field of every MCP payload before forwarding
- Forwards the MCP JSON-RPC request to the configured upstream MCP server and returns the response
- Is testable end-to-end with a curl/test client and a minimal fake upstream MCP server

No policy evaluation, no Redis, no Postgres in this slice.

## Approach

Plain Go HTTP server with SSE support. MCP JSON-RPC parsed from the SSE event body, mutated (meta injection), then proxied to the upstream via HTTP client. Upstream MCP server URL configurable via environment variable or config file.

## Scope

- **In**: SSE endpoint, SessionID UUID generation, TurnID extraction from headers, MCP JSON-RPC parse + forward + return, `meta` field injection, a stub/fake upstream MCP server for testing, basic structured logging
- **Out**: Policy evaluation, Redis, Postgres, approval, eval runner, any persistence

## Boundary Candidates

- MCP protocol parsing layer (reusable by later slices for interception)
- Session/connection lifecycle management (hooks for later slices to register middleware)
- Upstream forwarding layer (later slices wrap this, not replace it)

## Out of Boundary

- Policy decisions of any kind (even passthrough logging) — that is slice 2
- Session state persistence — that is slice 3
- Approval routing — that is slice 4
- Eval orchestration — that is slice 5

## Upstream / Downstream

- **Upstream**: MCP protocol spec (canonical tool protocol), Go standard library + net/http
- **Downstream**: policy-gate (slice 2) will register an in-process middleware hook on the forwarding path established here

## Existing Spec Touchpoints

- **Extends**: none (first spec)
- **Adjacent**: none

## Constraints

- Go 1.22+
- Must speak the current MCP JSON-RPC-over-SSE protocol
- The internal forwarding pipeline designed here must be extensible without rewriting it in slice 2 (middleware chain or handler interface)
