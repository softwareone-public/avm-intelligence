---
description: "Use when developing, debugging, reviewing, or testing AVM Intelligence Go code, dependencies, and toolchain conventions."
applyTo: "**/*.go,go.mod,go.sum"
---
# Go Development

Follow the [root guide](../../AGENTS.md) and nearest directory guide.

## Implementation

- Use the version declared in [go.mod](../../go.mod); CI reads that file.
  The runtime uses compiled native checks; do not add executable analyzers,
  provider binaries or downloaded runtime plugins.
- Keep CLI orchestration in `cmd/factory` and domain decisions in `internal`.
  Reuse typed validators, canonical ID/path helpers, and established libraries.
- Follow neighboring APIs and formatting. Use contextual errors with `%w` when
  wrapping, named constants for policy values, and descriptive variable names.
  Document functions, types, and non-trivial intent; avoid narration of obvious
  assignments. Do not add abstractions or dependency upgrades without need.
- Preserve deterministic identities, append-only accepted history, explicit
  nulls, and independent assessment outcomes. Keep concurrency transitions
  fenced and idempotent through the owning state/queue APIs.
- Reuse neighboring tests with synthetic or approved public fixtures,
  `httptest`, and `t.TempDir`. Unit tests must not need credentials or live APIs.

## Logging

- Follow the [repository logging policy](../../AGENTS.md#logging). Use Go's
  standard `log/slog` for operational events; only the CLI configures handlers,
  output streams and minimum severity. Reuse `slog.With` for stable IDs and
  `InfoContext`/`WarnContext` when a context is available. Do not add a second
  logger implementation or configure global handlers in domain packages.
- Log meaningful phase boundaries at `INFO`, individual checks and scope skips
  at `DEBUG`, incomplete/recoverable execution at `WARN`, and terminal command
  failures at `ERROR`. Keep assessment statuses as attributes. Return `%w`
  errors rather than logging the same failure at every layer.
- Use allowlisted scalar fields, not whole records or raw diagnostics. Keep
  elapsed time and log timestamps outside serialized evidence and identity
  calculations. Use `time.Since` for elapsed time, not supplied evidence times.
- Preserve JSON/token stdout. Test level/env/flag behavior, stream routing,
  privacy and byte-identical evidence across logging settings. Tests replacing
  the process logger or stdout/stderr must be sequential and restore them.
- Use the shared `internal/progress` operation reporter for long discovery,
  state and projection boundaries. Propagate its context for module/phase-aware
  retries; close it on every return. INFO progress must show the current module,
  source decision, queued/prepared versus committed work and Issue/Project stage.
  Heartbeats must not renew leases or change cancellation/retry semantics.
- Manual discovery's `discovery.Cycle` uses singleton state transitions and a
  CLI-owned durable-delivery callback. Keep the cycle barrier, exact selection
  and RES/UTL/PTN order in fixture tests. `state.BlobReadCache` is run-local and
  caches immutable blob contents only; refs, trees, fences and snapshots must
  still be checked live. Preserve expected-head failures and historical outboxes.
- Preserve typed projection causes across batch/cycle wrapping. Emit request
  WARNs distinguishing local reserve blocks and server limits with safe counters
  and module/phase; the single terminal ERROR includes completion/retry context.
- Evidence comments are keyed by module/phase/run-attempt, not generation.
  Persist pending work before mutations and require verified App-owned comment
  receipts before acknowledgement. Test lost-checkpoint deduplication, protected
  ownership, new forced-run markers and private diagnostic filtering.

## Validation

- Start with the smallest test that can falsify the proposed behavior, then run
  focused package tests immediately after editing. Add race checks for changed
  concurrency paths. Do not use operational commands as validation.
- Before review, run the [CI](../workflows/factory-ci.yml) and root-guide gates from the
  repository root:

```sh
gofmt -w <changed-go-files>
test -z "$(gofmt -l cmd internal)"
go mod verify
go test ./... -count=1
go vet ./...
go run ./cmd/factory --list-dimensions
```

- Use the workflow/schema guides for their additional contract checks. Report
  exact failures and skipped checks; keep live outcomes separate from local
  validation. Do not fix unrelated failures or loosen a gate to make it pass.
- Record substantive findings in [FINDINGS.md](../../docs/FINDINGS.md) and
  actionable follow-ups in [TODOS.md](../../docs/TODOS.md).

## Keep This Instruction Current

- When an approved change alters Go tooling, dependencies, conventions, package
  ownership, or validation commands, verify the new practice against `go.mod`,
  CI, and neighboring code, then update this instruction in the same change.
- Keep commands executable and links accurate. Remove superseded guidance;
  do not copy the task register, pin volatile versions here, or turn a transient
  failure into a permanent rule. Mention instruction updates in the close-out.