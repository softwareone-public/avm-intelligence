---
description: "Use when creating, inspecting, reviewing or merging GitHub pull requests, or changing GitHub Actions workflows, dispatch inputs, workflow handoffs, permissions or manual-to-factory promotion."
applyTo: ".github/workflows/**,cmd/factory/*workflow*test.go"
---
# GitHub Operations

Follow the [root guide](../../AGENTS.md) and [workflow guide](../workflows/AGENTS.md).
For pull requests, use authenticated `gh pr` and `gh api` first, including creation,
inspection, diffs, comments, reviews, status checks and merges. Prefer structured
`--json` output where supported and explicit repository/PR identity. Do not use
Playwright/browser automation for routine PR operations; reserve it for explicit
user requests or functionality unavailable through the CLI/API. Use GitHub MCP
as a fallback if the CLI is unavailable or fails. Required checks, merge approval
and protected-main controls remain mandatory; never use admin bypass as a shortcut.

When the user approves a plan, that approval authorizes autonomous execution of
its planned tasks and routine implementation decisions; do not request another
approval for each task. For approved in-scope changes, implement and validate,
create a focused branch and open a PR when protected-main contribution flow
requires it. Enable/use auto-merge on eligible PRs when supported by existing
repository policy. Poll PR checks and merge status until auto-merge completes or
a concrete blocker requires user input. Once merged, verify the merge, preserve
unrelated worktree edits, fetch `origin/main`, switch to `main`, and fast-forward
it before continuing. Never change repository settings, required checks, branch
protection, or auto-merge policy as part of this authority. Explicit scope limits,
workflow rollout gates, environment approvals and approvals for live state writes,
collection, dispatch, acceptance, publication or deployment remain mandatory; a
plan approval or PR merge does not satisfy separately named gates.

For non-PR operations, prefer available GitHub/GitKraken tools. If no suitable tool is available or a
tool fails, is unreliable, or lacks authentication, use `gh` for GitHub operations
and `git` for repository operations instead of stopping solely on that tool failure.
Before retrying a write through another transport, check whether the first attempt
succeeded to avoid duplicate PRs, issues, comments or other writes. Verify CLI
availability and authentication when needed; never assume they are authenticated
or expose credentials. If authentication is required, have the user complete it
directly in the terminal. Fallbacks retain the same scope, approvals and safety
rules. Local validation never dispatches a workflow or changes remote state.

## State Checkout Requirement

Every trusted `factory-*` or `manual-*` job accessing `factory-state` checks out
the explicit repository/ref into a separate depth-1 `work/state` directory with
`persist-credentials: false`, using the scoped state App token for checkout.
Commands receive `--state-branch factory-state --state-checkout-root work/state`;
the verified Git backend derives and validates the tree internally. Do not
generate or pass tree listings, expose a token through `FACTORY_STATE_TOKEN`, or
retain dispatch/worker-specific checkout aliases. The CLI mints operation-time
credentials from App credentials behind its temporary credential provider. Keep
live-head fetches, fences and bounded whole-decision retries; never use pull,
merge or rebase as a state transaction. Do not give credential-free compute jobs
state access. Existing workflow upgrades retain approval gates; this instruction
does not authorize live runs or remote writes.
This requirement applies only to the factory/manual families, not `admin-*` jobs.
Admin workflows must not receive incidental factory-state access or credentials.

## Workflow Families

- Use `factory-<purpose>.yml` and `name: factory-<purpose>` for autonomous
  workflows. They must not expose `workflow_dispatch`. Preserve approved event,
  branch, schedule, concurrency, permission, and environment boundaries.
- Use `manual-<purpose>.yml` and `name: manual-<purpose>` for manual workflows.
  Their only trigger is `workflow_dispatch`. Create a separate manual counterpart
  when needed; never add dispatch to a factory workflow.
- Use `admin-<purpose>.yml` and `name: admin-<purpose>` for non-factory repository
  administration, CI and PR/proposal validation, for example `admin-ci` and
  `admin-proposal-validation`. Triggers follow the administrative purpose and
  approved trust boundary; admin workflows are not restricted to dispatch-only
  or autonomous-only triggers. They do not perform factory lifecycle work,
  collection, dispatch, acceptance or deployment. Preserve trusted-base validation,
  permissions and required status-check job identities when renaming existing CI
  workflows; update callers/tests/links under separately approved implementation.
- Dispatch-only machine handoffs remain manual workflows until a separately
  reviewed autonomous transport replaces them. Preserve exact request, attempt,
  fence, run, commit, artifact, and receipt validation.
- Only `factory-projection` mints the reduced Issue/Project token and applies
  projection effects. Manual projection recovery and flush workflows commit or
  validate exact state targets; the scheduled projector performs delivery.
- Remove version prefixes from workflow names and filenames, not versioned data
  contracts, durable identities, historical evidence, or artifact protocols.
  Update callers, provenance allowlists, tests, and operational links together.
  Preserve required status-check job identities when renaming workflows.

## Manual Inputs

Module-targeted workflows use these inputs, in this order:

| Input | Type | Default | Meaning |
| --- | --- | --- | --- |
| `res` | boolean | true | Resource modules (RES) |
| `ptn` | boolean | true | Pattern modules (PTN) |
| `utl` | boolean | true | Utility modules (UTL) |
| `force` | boolean | false | Repeat the supported operation for the selected scope |
| `module_names` | string | empty | Comma-separated canonical AVM module IDs |

- A nonblank name list selects exactly those IDs and ignores type checkboxes,
  whether checked or unchecked; reject unknown, inactive, malformed, or duplicate
  IDs. With no list, resolve only explicitly selected types from the authoritative
  catalog. No list and no selected types, or an empty resolved scope, fails closed;
  never fall back to the full fleet.
- Keep selection separate from force. An ordinary targeted run must not refresh
  unchanged work. Force never bypasses fencing, leases, provenance, approvals,
  accepted history, or duplicate-run ownership.
- Discovery follow-on work must remain within its scope: manual IDs/categories,
  or newly added catalog IDs for factory discovery. Factory persistence must use
  `--discovery-new-modules-only`; empty new-ID scope never means all modules.
  Factory projection consumes only transaction `projection_module_ids`. Manual
  discovery uses `--discovery-module-cycle`: one shared catalog observation, then
  RES/UTL/PTN and canonical ID order, resolving the source and committing each
  selected module's state handoffs before the next. It never projects to Issues
  or Projects. A source or state-commit failure stops the cycle without resetting
  leases, retry waits or review; scheduled projection handles delivery separately.
  Full catalog observation/export bookkeeping must not cause out-of-scope module
  lifecycle, retirement, request or board writes. Existing-ID release monitoring
  is separate.
- Read booleans from `inputs`, not truthy string values in `github.event.inputs`.
  Pass expressions through quoted environment variables, not shell interpolation.
- Provider/global-control and exact-request, attempt, or receipt operations keep
  meaningful operation-specific inputs. Do not add inert module controls or
  substitute generic selectors for their required safety identities. Unsupported
  force behavior must be rejected, not silently ignored.

## Manual-First Promotion

Manual discovery may accept exact `continuation_id` for a durable deferred cycle.
Resume uses stored scope, with no new names or force, no fresh catalog collection
and no automatic dispatch. Summaries distinguish completed/deferred/stopped;
delivery counts alone cannot certify terminal cursor persistence. Retain the
bounded 27-minute execution segments inside the approved six-hour job. The job
automatically waits for durable retry deadlines and resumes the exact ID with
no fresh names/force or catalog collection; preserve rolling pacing costs across
segments and mint fresh state tokens. A 350-minute loop bound leaves cleanup
time; exhaustion fails visibly with durable continuation retained. No new
workflow dispatch permissions, schedules or environment bypass. Validate the
loop with executable time/result/failure fixtures.

- Develop new behavior in the manual family first. Keep the autonomous factory
  workload unchanged while validating the manual counterpart.
- Run focused offline contract/fixture checks, then obtain applicable approval
  for bounded live manual validation. Record exact runs, commits, and verified
  postconditions separately from local test results.
- Promote only proven, reviewed manual behavior into the factory counterpart.
  Retain the manual entry point for future changes and recovery. New schedules,
  unattended stages, or wider targets retain their explicit rollout gates.
- Keep discovery, compute, acceptance, selection, projection, and publication
  separate. Preserve pinned actions, least privilege, trusted code, and protected
  main PR delivery. Force is not an approval or a bypass mechanism.

## Validation

- Add neighboring workflow-contract tests for names, triggers, dispatch input
  types/defaults, exact-target propagation, and unchanged factory scope.
- Execute shell syntax and fixture-based selection/provenance checks; assertions
  that YAML merely contains a string are insufficient for behavioral changes.
- Run the [workflow checks](../workflows/AGENTS.md) and repository CI gates.
  Report missing tools and skips. Record findings and remaining promotion/live
  verification work in the existing task and findings registers.
