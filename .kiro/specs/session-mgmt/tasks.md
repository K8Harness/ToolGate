# Implementation Plan

- [ ] 1. Foundation — dependency, configuration, and shared constants
- [x] 1.1 Add go-redis/v9 dependency and extend gateway config with Redis fields
  - Run `go get github.com/redis/go-redis/v9@v9.19.0`; confirm entry appears in go.mod and go.sum
  - Add `RedisDSN string` (env `REDIS_DSN`), `SessionLockTTL time.Duration` (env `SESSION_LOCK_TTL`, default `60s`), and `LockAcquireTimeout time.Duration` (env `LOCK_ACQUIRE_TIMEOUT`, default `5s`) to the `Config` struct in `cmd/gateway/config.go`
  - Add startup validation: return a descriptive error if `REDIS_DSN` is empty
  - Observable: `go build ./...` succeeds with no compile errors; starting the gateway without `REDIS_DSN` exits with a clear error message naming the missing variable
  - _Requirements: 2.5, 2.6_

- [x] 1.2 Add Redis service to Docker Compose and build the Redis client factory
  - Add `redis:7-alpine` service to `docker-compose.yml` with a TCP healthcheck on port 6379; add `REDIS_DSN=redis://redis:6379/0` to the gateway service env block; add `redis` to the gateway's `depends_on` with `condition: service_healthy`
  - Implement `NewRedisClient(cfg Config) (*redis.Client, error)` in `cmd/gateway/redis.go`: parse the DSN with `redis.ParseURL()`, dial the client, call `Ping()` to validate connectivity at startup; return a descriptive error on failure
  - Observable (infra): `docker compose up` starts a healthy Redis service; `docker compose ps` shows redis as healthy before gateway starts
  - Observable (factory): gateway startup log confirms Redis connectivity; starting with an unreachable Redis address returns an error before the HTTP server binds
  - _Requirements: 2.4_

- [x] 1.3 Add CodeSessionBusy error code and OperationClasses field to AgentPolicy
  - Add `CodeSessionBusy = -32002` constant to `core/mcp/types.go`
  - Add `OperationClasses map[string]string \`yaml:"operationClasses"\`` to the `AgentPolicy` struct in `core/policy/policy.go` (where AgentPolicy is defined); `cmd/gateway/policy_gate.go` only consumes `*corepolicy.AgentPolicy` and does not own the struct — no change needed there for this field
  - Observable: a `policy.yaml` containing an `operationClasses` block loads without error and the field is populated; `mcp.CodeSessionBusy` is accessible from the `core/mcp` package and equals `-32002`
  - _Requirements: 4.1_
  - _Note: task.md originally named `cmd/gateway/policy_gate.go` as the edit site; corrected to `core/policy/policy.go` to match code reality (design.md §Allowed Dependencies: "may extend but not restructure")_

- [ ] 2. Core lock primitives and operation classifier
- [x] 2.1 (P) Build the OperationClassifier
  - Implement `OperationClass` type (values: `OperationClassRead`, `OperationClassWrite`) and `OperationClassifier` struct in `cmd/gateway/classifier.go`
  - Implement `NewOperationClassifier(classes map[string]string) *OperationClassifier` and `Classify(toolName string) OperationClass`
  - Explicit map entries (from `AgentPolicy.OperationClasses`) take precedence over the default heuristic
  - Default heuristic: names beginning with `read_`, `get_`, or `list_` → `OperationClassRead`; all others → `OperationClassWrite`
  - Observable: `Classify("get_payment")` returns `OperationClassRead`; `Classify("create_refund")` returns `OperationClassWrite`; an explicit map entry `"create_refund": "read"` overrides the heuristic for that name
  - _Requirements: 4.1, 4.2_
  - _Boundary: OperationClassifier_

- [x] 2.2 (P) Build the SessionLocker with reference-counted Redis mutex
  - Implement `LockTimeoutError` struct in `cmd/gateway/session_locker.go`; it must implement `Error() string` and `JSONRPCCode() int` returning `mcp.CodeSessionBusy` (-32002)
  - Implement `SessionLocker` with `NewSessionLocker(rdb *redis.Client, lockTTL, acquireTimeout time.Duration) *SessionLocker`
  - `Acquire(ctx, sessionID, turnID string) error`: Lua script — if `session:<id>:lock` absent → SET value=turnID + EX TTL + init `session:<id>:refcount`=1; if value matches turnID → INCR refcount + EXPIRE both keys; else → return 0 (blocked); busy-wait at 50ms intervals until acquired or `acquireTimeout` elapses, then return `LockTimeoutError`
  - `Release(ctx, sessionID, turnID string) error`: Lua script — if value matches turnID → DECR refcount; if refcount ≤ 0 → DEL both keys
  - `Extend(ctx, sessionID, turnID string) error`: Lua script — if value matches turnID → EXPIRE both keys by TTL; else return error
  - Observable: Acquire with no prior lock returns nil and `session:<id>:refcount` equals 1 in Redis; re-entry with same turnID sets refcount to 2; two sequential Release calls delete both keys; Acquire by a different turnID while locked returns `LockTimeoutError` with `JSONRPCCode()` == -32002 after the configured timeout
  - _Requirements: 1.1, 2.1, 2.2, 2.3, 2.4, 2.5, 2.6_
  - _Boundary: SessionLocker_

- [ ] 2.3 (P) Build the TurnRWLock with per-turn reader-writer lock
  - Implement `TurnRWLock` in `cmd/gateway/turn_rwlock.go` with `NewTurnRWLock(rdb *redis.Client, lockTTL, acquireTimeout time.Duration) *TurnRWLock`
  - `ReadLock(ctx, turnID string) error`: Lua script — if `turn:<id>:wlock` absent → INCR `turn:<id>:readers` + EXPIRE; else → return 0 (blocked); busy-wait until timeout
  - `ReadUnlock(ctx, turnID string) error`: Lua script — DECR `turn:<id>:readers`
  - `WriteLock(ctx, turnID string) (ownerToken string, err error)`: generate `ownerToken` via `crypto/rand`; Lua script — if `turn:<id>:readers` == 0 and `wlock` absent → SET wlock NX ownerToken EX TTL; else → return 0; busy-wait until timeout
  - `WriteUnlock(ctx, turnID, ownerToken string) error`: Lua script — compare-and-delete: if `turn:<id>:wlock` == ownerToken → DEL; else → no-op
  - Observable: two concurrent `ReadLock` calls both return nil with `turn:<id>:readers` == 2 in Redis; a `WriteLock` call blocks while readers > 0 and returns after `ReadUnlock`; two concurrent `WriteLock` calls serialize (second blocked until first calls `WriteUnlock`); `WriteUnlock` with the wrong token leaves the wlock key intact
  - _Requirements: 3.1, 3.2, 3.3, 3.4_
  - _Boundary: TurnRWLock_

- [ ] 3. Integration — ConcurrencyGuard and server wiring
- [ ] 3.1 Build ConcurrencyGuard combining all lock primitives
  - Implement `ConcurrencyGuard` in `cmd/gateway/concurrency_guard.go` with `NewConcurrencyGuard(locker *SessionLocker, rwlock *TurnRWLock, classifier *OperationClassifier) *ConcurrencyGuard`
  - `Execute(ctx context.Context, sessionID, turnID, toolName string, fn func() (*mcp.JSONRPCResponse, error)) (*mcp.JSONRPCResponse, error)`: when `toolName` is empty (non-`tools/call` method), call `fn()` directly with no locking; otherwise classify toolName → acquire session mutex → acquire RWLock slot → call `fn()` → deferred release in reverse order (RWLock first, then session mutex)
  - Use `defer` for both releases; use `recover()` inside `Execute` to prevent panics from leaking held locks
  - Budget counter accuracy is automatic: `BudgetTracker.IncrementAndGet()` runs inside `fn()`, which is called after both locks are held
  - Observable: two goroutines calling `Execute` with the same sessionID/turnID and a read-class `toolName` have both `fn()` calls in-flight simultaneously (verified with a channel barrier); two goroutines calling `Execute` with different turnIDs for the same sessionID serialize (second `fn()` does not start until first completes)
  - _Depends: 2.1, 2.2, 2.3_
  - _Requirements: 1.1, 2.3, 5.1_
  - _Boundary: ConcurrencyGuard_

- [ ] 3.2 Wire ConcurrencyGuard into the gateway server and startup sequence
  - Add `guard *ConcurrencyGuard` field to the `Server` struct in `cmd/gateway/server.go`
  - In `handleMCPPost`, extract the tool name from `req.Params` for `tools/call` requests; replace the direct `pipeline.Run()` call with `guard.Execute(ctx, sessionID, turnID, toolName, fn)` where `fn` wraps `pipeline.Run()`
  - In `cmd/gateway/main.go`, instantiate `NewRedisClient`, `NewSessionLocker`, `NewTurnRWLock`, `NewOperationClassifier(policy.OperationClasses)`, and `NewConcurrencyGuard`; pass the guard into `Server`; close the Redis client in the shutdown sequence
  - A gateway restart drops all in-flight Redis lock keys via TTL; new requests may acquire locks immediately after restart — document this behavior in a code comment at the `NewRedisClient` call site
  - Observable: `go build ./cmd/gateway` compiles with no errors; a `tools/call` request processed end-to-end creates a `session:<id>:lock` key in Redis during execution and the key is absent after the response is returned; `go build ./...` still succeeds with no regressions
  - _Depends: 3.1_
  - _Requirements: 2.3, 2.4, 4.1, 5.1_

- [ ] 4. Validation — unit, integration, and end-to-end tests
- [ ] 4.1 (P) Unit tests for OperationClassifier
  - Test `Classify()` with an explicit `"read"` map entry → `OperationClassRead`
  - Test `Classify()` with an explicit `"write"` map entry → `OperationClassWrite`
  - Test `Classify()` with `get_`, `read_`, `list_` prefix and no map entry → `OperationClassRead` (heuristic)
  - Test `Classify()` with no matching prefix and no map entry → `OperationClassWrite` (heuristic default)
  - Test that an explicit map entry overrides the heuristic for the same tool name
  - Observable: `go test ./cmd/gateway/... -run TestOperationClassifier` exits 0 with all cases passing
  - _Requirements: 4.1, 4.2_
  - _Boundary: OperationClassifier_

- [ ] 4.2 (P) Unit tests for SessionLocker
  - Use a real Redis instance in tests (via testcontainers, miniredis, or a test-local `docker run redis:7-alpine`); document the approach in a `TestMain` or build tag comment
  - Test Acquire with no prior lock → returns nil; `session:<id>:refcount` equals 1 in Redis
  - Test re-entry with same turnID → refcount increments to 2; two Release calls → both keys deleted from Redis
  - Test Acquire by a different turnID while locked → returns `LockTimeoutError`; `JSONRPCCode()` returns -32002
  - Test Extend while lock is held → both key TTLs are refreshed (verify via `TTL` command); Extend after release → returns a non-nil error
  - Observable: `go test ./cmd/gateway/... -run TestSessionLocker` exits 0; Redis key assertions pass at each step using a direct Redis client in test setup
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6_
  - _Boundary: SessionLocker_

- [ ] 4.3 (P) Unit tests for TurnRWLock
  - Test two concurrent `ReadLock` calls → both return nil; `turn:<id>:readers` equals 2 in Redis
  - Test `WriteLock` call while readers > 0 → blocks; returns after `ReadUnlock`; readers counter equals 0 after unlock
  - Test two concurrent `WriteLock` calls → only one acquires the wlock key; second serialized and succeeds only after the first calls `WriteUnlock`
  - Test `WriteUnlock` with wrong ownerToken → wlock key unchanged in Redis; correct token deletes the key
  - Observable: `go test ./cmd/gateway/... -run TestTurnRWLock` exits 0; goroutine timing assertions (using channels or `sync.WaitGroup`) confirm parallel-read and serialized-write behavior
  - _Requirements: 3.1, 3.2, 3.3, 3.4_
  - _Boundary: TurnRWLock_

- [ ] 4.4 Integration tests for ConcurrencyGuard with concurrent goroutines
  - Test same session + same turn, two read-class Execute calls: use a channel barrier inside each `fn()` to verify both are in-flight simultaneously before either returns
  - Test same session + different turns: second Execute blocks until first `fn()` returns; if `acquireTimeout` elapses, second receives `LockTimeoutError`
  - Test different sessionIDs: two Execute calls proceed without any blocking; verify independent Redis key namespaces (no shared `session:<id>:lock` key)
  - Test budget counter accuracy: two concurrent Execute calls with the same session/turn use a shared atomic counter inside `fn()` instead of a real BudgetTracker; verify the counter reaches 2 with no race (run with `-race` flag)
  - Observable: `go test -race ./cmd/gateway/... -run TestConcurrencyGuard` exits 0; timing probes confirm parallel reads; `-race` detector reports no data races
  - _Depends: 3.1_
  - _Requirements: 1.1, 2.1, 5.1_

- [ ] 4.5 End-to-end validation via Docker Compose with real Redis
  - Start the full Docker Compose stack (gateway + Redis + fake upstream + Postgres)
  - Send a `tools/call` request to the gateway; verify the response is correct and `redis-cli keys 'session:*'` shows the lock key during processing and is empty after the response
  - Simulate a gateway restart mid-turn (SIGKILL the gateway container); restart the container; verify the next request for the same session succeeds within the `SessionLockTTL` window (no stuck lock, TTL expiry allows re-acquisition)
  - Observable: `docker compose up` + request script produces a correct JSON-RPC response; post-restart request succeeds without a `LockTimeoutError`; `redis-cli keys 'session:*'` is empty after the session completes
  - _Depends: 3.2_
  - _Requirements: 2.4_
