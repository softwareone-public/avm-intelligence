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

## Validation

- Start with the smallest test that can falsify the proposed behavior, then run
  focused package tests immediately after editing. Add race checks for changed
  concurrency paths. Do not use operational commands as validation.
- Before review, run the [CI](../workflows/ci.yml) and root-guide gates from the
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