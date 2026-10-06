# AVM Intelligence Agent Guide

AVM Intelligence is a Go factory for AVM discovery, acquisition, assessment,
evidence acceptance, selection, projection, and publication. Read
[decisions](docs/decisions.md), [TODOs](docs/TODOS.md),
[findings](docs/FINDINGS.md), and the relevant contract docs before changing
workflow or state behavior. The parent workspace's `../plan/` contains the
approved v2 operating and delivery plans; the v1 plan is historical where it
conflicts with v2.

## Working agreement

- Present a concise plan and wait for Ledermayer's approval before implementing.
  After approval, proceed within that scope; ask before expanding it.
- Read this guide and the nearest directory guide before editing. Nested guides
  supplement these safety rules; they do not replace them.
- Check [findings](docs/FINDINGS.md) and [TODOs](docs/TODOS.md) before work.
  Record every substantive finding in the findings register and every actionable
  follow-up in the TODO register; reuse existing entries and v2 task IDs.
  Distinguish source-proven risks, observed incidents, local verification, and
  live outcomes. Do not mark activation or product acceptance from fixtures.
- Preserve unrelated worktree changes. Do not commit, create branches, or change
  remote settings unless requested. Use GitHub MCP tools before CLI fallbacks.
- Maintain these guides when verified repository practices change. Update the
  [Go instructions](.github/instructions/go-development.instructions.md) when
  Go tooling, conventions, package boundaries, or validation gates change.
  Keep guidance concise and link to contracts instead of copying them.

## Directory guides

| Area | Local guidance |
| --- | --- |
| CLI and workflow-contract tests | [cmd/factory](cmd/factory/AGENTS.md) |
| Domain packages | [internal](internal/AGENTS.md) |
| Actions workflows | [workflows](.github/workflows/AGENTS.md) |
| Versioned data contracts | [schemas](schemas/AGENTS.md) |
| Evidence and task registers | [docs](docs/AGENTS.md) |

For a read-only lifecycle or trust-boundary review, use the
[Factory Contract Reviewer](.github/agents/factory-contract-reviewer.agent.md).
For existing projection-only recovery, use the
[recovery skill](.github/skills/v2-projection-recovery/SKILL.md); do not rerun
discovery or dispatch as a board repair.

## Authority and safety

- `factory-state` is the durable queue and lifecycle authority. Accepted data
  and verified receipts govern canonical evidence and publication. Issues and
  GitHub Projects are human-facing projections, never queue or approval inputs.
- Keep acquisition, assessment, evidence acceptance, selection export, board
  projection, and Pages deployment as separate stages. Discovery does not
  dispatch workers. Do not broaden a manual target or start workers, live
  collection, acceptance, or deployment without the applicable user approval.
- State changes use expected-head transactions and fencing. Do not edit state
  branch JSON directly, force-update refs, or let a stale callback supersede a
  newer generation.
- Preserve the protected `main` PR flow and environment approvals. State-writer
  Contents permissions and reduced Issue/Project permissions are separate.
  When an API fails, use its exact error to distinguish App configuration,
  installation grants, and user-token scope; do not infer an organization-level
  blocker from missing visibility alone.
- The board is not canonical. Preserve human Issue notes and only update the
  factory-owned summary, labels, and configured fields through the projector.

## Code and validation

The CLI is under `cmd/factory`; domain packages are under `internal/`; schemas,
fixtures, workflows, and operational configuration have separate ownership.
Keep workflows pinned and least-privilege, and add workflow-contract tests when
changing their triggers, permissions, or command scope.

Use the Go version in `go.mod` (1.27.1 at the current baseline). Before review:

```sh
gofmt -w <changed-go-files>
go test ./... -count=1
go vet ./...
go run ./cmd/factory --show-toolchain
```

For workflow or schema changes, also run the relevant YAML/schema checks from
CI. Use fixtures for network and GitHub behavior; do not require credentials in
unit tests. Summarize live workflow outcomes separately from local test results.