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
  Module-scoped logs order top-level fields as `time`, `level`, `module_id`,
  `msg`, then other attributes in text and JSON. Keep standard slog encoding,
  grouped attributes and module-free output unchanged; test inherited/direct
  identity, sibling isolation and concurrent output at the central formatter.
- Use the shared `internal/progress` operation reporter for long discovery,
  state and projection boundaries. Propagate its context for module/phase-aware
  retries; close it on every return. INFO progress must show the current module,
  source decision, queued/prepared versus committed work and the stages actually
  performed. Discovery reports state handoffs; the projector reports remote
  Issue/Project effects.
  Heartbeats must not renew leases or change cancellation/retry semantics.
- Manual discovery uses singleton state transitions and a CLI-owned state-only
  completion callback. Keep the exact selection and RES/UTL/PTN order in fixture
  tests. Discovery must not call Issue/Project APIs or mint projection tokens;
  it writes lifecycle and projection intents for the scheduled projector.
  `state.BlobReadCache` is run-local and
  caches immutable Git blob contents only. `state.VerifiedGitBackend` fetches and
  verifies the live branch head and derives the tree internally; workflows pass
  the isolated checkout root, never a caller-generated `ls-tree` listing. The
  checkout is an object cache, not state authority. All production writes use
  `state.TransactionCoordinator` and the non-force Git publisher; generic
  expected-head commit helpers and deployment/layout migration APIs remain
  fixture/domain utilities and are not CLI transport fallbacks. Module-v2 keeps
  logical event paths and durable replay/fence metadata. CLI checkout support is
  independent of discovery cycle, pacing and layout utilities. State transport
  errors use typed Git failure categories; do not restore REST-state API error
  handling or token/listing aliases.
  Manual paced discovery keeps exact scope, continuation cursor, lease and fence
  in state. Resume only the stored authorized scope and never repeats a completed
  module's source work. Respect the shared pacer, server pauses, parent
  cancellation and expected-head checks. A completed state handoff does not mean
  board delivery; the scheduled projector owns remote effects, bounded retries,
  evidence comments and delivery acknowledgements. Issues and Project fields
  are never queue authority.

## Validation

- Start with the smallest test that can falsify the proposed behavior, then run
  focused package tests immediately after editing. Add race checks for changed
  concurrency paths. Do not use operational commands as validation.
- Before review, run the [CI](../workflows/admin-ci.yml) and root-guide gates from the
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
