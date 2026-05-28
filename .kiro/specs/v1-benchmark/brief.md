# Brief: v1-benchmark

## Problem

v1's architectural commitments — Postgres advisory locks instead of Redis SETNX, checkpointed approval instead of synchronous hold, resource-keyed locks instead of per-turn RWLock — come with latency and throughput claims in v1.md §Track 5: p50 allow-passthrough < 1ms, p95 policy-allow < 5ms, p95 resource-lock-uncontended < 8ms, throughput ≥ 5k rps on a 2-vCPU gateway pod. These are *targets to validate, not assume*. Without a reproducible benchmark, the v1 architecture ships with marketing claims; with a benchmark, it ships with evidence (and the numbers get rewritten when reality disagrees with the targets).

## Current State

- No `bench/` directory exists
- No load-generation harness in the codebase
- No published latency or throughput numbers
- v0 `make demo` runs end-to-end but has no performance instrumentation

## Desired Outcome

- New `bench/` directory with: `README.md` (reproduction steps), `docker-compose.bench.yml` (gateway + fake stripe MCP + Postgres + Redis-optional), `scenarios/01-06.yaml` (the six scenarios from v1.md §Track 5b), `run.sh` (invokes vegeta or k6 across all scenarios)
- Scenarios cover the v1 architectural surface: allow-passthrough, policy-allow, policy-deny, resource-lock-uncontended, resource-lock-contended, approval-pending
- `run.sh` produces a markdown report (p50 / p95 / p99 + throughput per scenario)
- Report committed to the repo and refreshed on every release tag (DoD #10)
- Nightly CI run executes the benchmark and uploads the markdown artifact (not blocking on PRs — keeps CI cheap)
- If measured numbers disagree with v1.md's targets, the targets in `project.md` get rewritten; the report is the authoritative source

## Approach

Stand up `bench/` with the exact layout from v1.md §Track 5b. Use vegeta (single-binary Go tool, lightweight) unless k6's scripting flexibility is needed during the design phase. Write scenario YAMLs matching the six scenarios. `run.sh` orchestrates: `docker compose up -d --wait`, run vegeta against each scenario, produce per-scenario JSON, aggregate into a single markdown report. Wire a nightly GitHub Actions workflow that runs the suite and uploads the artifact. Commit the latest report at each release tag.

## Scope

- **In**: `bench/` directory + all subfiles; vegeta (or k6) integration; the 6 scenarios from v1.md; markdown report generator; nightly CI workflow; report-at-release-tag automation.
- **Out**: Continuous benchmarking on every PR (too noisy + expensive); benchmark coverage for the Redis session-locker backend (the Postgres backend is the v1 default; the Redis backend is leaving the codebase within two weeks of v1 ship); cross-cloud or production-environment benchmarks (Compose-only); profiling tooling beyond what vegeta/k6 provide.

## Boundary Candidates

- Load generator choice (vegeta vs. k6 — pick one in design phase, justified against scenario complexity)
- Scenario data model (YAML per v1.md)
- Report format (markdown table per scenario; aggregate p50/p95/p99 + RPS columns; raw vegeta/k6 JSON archived alongside)
- CI cadence (nightly, not per-PR)

## Out of Boundary

- Cross-region or multi-zone benchmarks (Compose-only)
- Production-environment benchmarks (Compose-only)
- A "compare release N vs. N-1" diffing tool (manual git-log review of the committed reports is sufficient for v1)
- Benchmarks for non-v1 paths (verification tokens, Rego, etc. — those are v2+)

## Upstream / Downstream

- **Upstream**: All four prior v1 specs (`v1-lock-substrate`, `v1-resource-locks`, `v1-identity-model`, `v1-approval-checkpoint`) must be merged so the benchmark exercises the actual shipping system
- **Downstream**: `project.md` latency budget (gets corrected if measured numbers disagree with targets)

## Existing Spec Touchpoints

- **Extends**: none
- **Adjacent**: All prior v1 specs (benchmark scenarios exercise their request paths)

## Constraints

- DoD #10: `bench/` produces a markdown report; the report is committed and refreshed on every release tag
- Compose-only — no Kubernetes, no cloud
- Targets are aspirational, not contractual — the benchmark is the artifact, not the targets
- CI: nightly, not per-PR (keep PRs cheap)
- Load generator must be a single binary or single-image container so reproducing the benchmark requires only Docker
