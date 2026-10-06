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

GitHub workflow naming, manual inputs, and manual-first promotion follow the
[GitHub operations instructions](.github/instructions/github-operations.instructions.md).

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

## Logging

- Report meaningful operation starts, completions, skips, retries and failures
  through the shared structured logger, not ad hoc progress prints. Include UTC
  timestamps, severity and stable event messages; attach component/phase,
  module/request IDs, counts, skip/retry reasons and elapsed time where relevant.
- Use `INFO` for normal progress, `DEBUG` for detailed checks, `WARN` for
  recoverable or incomplete execution, and `ERROR` for failed operations.
  Assessment `pass`, `fail`, `unchecked` and `not_applicable` are result fields,
  not logging severity or process exit codes. Do not log every function call.
- The factory configures output and filtering centrally through `--log-level`
  and `--log-format`, with `FACTORY_LOG_LEVEL`/`FACTORY_LOG_FORMAT` as defaults.
  Explicit flags win. Normal operational logs go to stdout; commands emitting
  JSON or tokens on stdout send logs to stderr. Preserve help and result output.
- Never log credentials, argument/environment dumps, raw source/configuration,
  whole inputs/artifacts or unreviewed parser/API error bodies. Use allowlisted
  attributes and safe diagnostic categories. Return contextual errors upward;
  the CLI owns the single terminal error log and nonzero exit.
- Telemetry must not change evidence, content hashes, timestamps supplied by
  callers, fencing, retry behavior or acceptance/publication semantics. Log a
  completion only after that operation succeeds; test stream isolation,
  filtering, privacy and evidence determinism with fixtures.
- Discovery and projection must show scope, actual processing order, module
  position/total, phase starts and outcomes at `INFO`. Include source-change
  and force decisions, request IDs, state commits, Issue/Project operations,
  explicit no-ops and final counts. Prepared events are not committed work.
- Emit periodic progress for long external reads/writes with module/phase and
  elapsed time. These messages describe waiting, not successful work, and must
  stop when the operation returns or its context is cancelled. Retries and API
  budget waits retain safe reasons and module/phase context. Keep one terminal
  `ERROR`; operation-level incomplete outcomes use `WARN` without raw errors.

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
go run ./cmd/factory --list-dimensions
```

For workflow or schema changes, also run the relevant YAML/schema checks from
CI. Use fixtures for network and GitHub behavior; do not require credentials in
unit tests. Summarize live workflow outcomes separately from local test results.