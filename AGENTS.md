# AVM Intelligence Agent Guide

AVM Intelligence is a Go factory for AVM discovery, acquisition, assessment,
evidence acceptance, selection, projection, and publication. Read
[decisions](docs/decisions.md), [TODOs](docs/TODOS.md),
[findings](docs/FINDINGS.md), and the relevant contract docs before changing
workflow or state behavior. The parent workspace's `../plan/` contains the
approved v2 operating and delivery plans; the v1 plan is historical where it
conflicts with v2.

## Working agreement

- Plan first when the request needs planning. Once the user approves a plan, that
  approval authorizes autonomous execution of its planned tasks; do not request
  approval again for each task or routine implementation decision. Implement,
  validate, and deliver the approved scope without pausing. Ask before expanding
  scope, changing explicit constraints, performing separately gated live operations,
  or making decisions reserved to the user.
- Read this guide and the nearest directory guide before editing. Nested guides
  supplement these safety rules; they do not replace them.
- Check [findings](docs/FINDINGS.md) and [TODOs](docs/TODOS.md) before work.
  Record every substantive finding in the findings register and every actionable
  follow-up in the TODO register; reuse existing entries and v2 task IDs.
  Distinguish source-proven risks, observed incidents, local verification, and
  live outcomes. Do not mark activation or product acceptance from fixtures.
- Preserve unrelated worktree changes. Create a focused branch and open a PR when
  repository contribution flow requires review or the change should be delivered
  through protected `main`; do not commit directly to protected `main`.
  For GitHub PRs, prefer authenticated `gh pr` and `gh api` for creation,
  inspection, checks, reviews and merges instead of browser automation or MCP.
  Use browser automation only when explicitly requested or CLI/API support is
  insufficient. Verify uncertain write outcomes before fallback retries and
  check authentication; do not stop solely because an integration failed.
- Repository auto-merge may be enabled on an eligible PR when supported by the
  existing repository policy and the PR satisfies its required review, checks,
  and protection rules. Enable or use auto-merge, poll
  the PR status/checks until it merges or reaches a genuine blocker, then verify
  the merge and consolidate local `main` before continuing: preserve uncommitted
  work, fetch `origin/main`, switch to `main`, and fast-forward. Stop on checkout
  or fast-forward conflicts instead of resetting or discarding changes.
- Do not change repository settings, branch protections, required checks, or
  auto-merge policy. The authorization is to use existing repository behavior,
  not alter it. All explicit task gates, scope limits, environment approvals,
  and live-operation approvals remain in force.
- Maintain these guides when verified repository practices change. Update the
  [Go instructions](.github/instructions/go-development.instructions.md) when
  Go tooling, conventions, package boundaries, or validation gates change.
  Keep guidance concise and link to contracts instead of copying them.

## Greenfield delivery

AVM Intelligence is greenfield and Ledermayer is the sole maintainer. Spend
implementation effort on the approved desired state, not on migrating or
preserving an approach that has already proven inadequate.

- Do not add compatibility readers, dual transports, shims, or migration phases
  merely because an earlier iteration exists. Replace the obsolete path in the
  approved change and delete code that the desired design no longer uses.
- Leave harmless obsolete state files in place. When one blocks validation or
  the desired operation, delete that file in the normal desired-state write;
  do not build a migration for it. This applies only to operational state, not
  to accepted evidence, receipts, or publication history.
- `main` remains pull-request protected. The active `protect-state` ruleset
  targets `factory-state`; Repository admin and `app-avm-intelligence` bypass
  it with Always allow. Do not treat that ruleset as a reason to retain a
  weaker transport or add a state-branch exception. Bypass does not authorize
  force-push, hook execution, credential exposure, or unapproved live writes.

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