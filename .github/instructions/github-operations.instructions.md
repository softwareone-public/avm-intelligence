---
description: "Use when creating, changing, reviewing, or operating GitHub Actions workflows, dispatch inputs, workflow handoffs, permissions, or manual-to-factory promotion."
applyTo: ".github/workflows/**,cmd/factory/*workflow*test.go"
---
# GitHub Operations

Follow the [root guide](../../AGENTS.md) and [workflow guide](../workflows/AGENTS.md).
Use GitHub MCP tools before CLI fallbacks. Local validation never dispatches a
workflow or changes remote state.

## Workflow Families

- Use `factory-<purpose>.yml` and `name: factory-<purpose>` for autonomous
  workflows. They must not expose `workflow_dispatch`. Preserve approved event,
  branch, schedule, concurrency, permission, and environment boundaries.
- Use `manual-<purpose>.yml` and `name: manual-<purpose>` for manual workflows.
  Their only trigger is `workflow_dispatch`. Create a separate manual counterpart
  when needed; never add dispatch to a factory workflow.
- Dispatch-only machine handoffs remain manual workflows until a separately
  reviewed autonomous transport replaces them. Preserve exact request, attempt,
  fence, run, commit, artifact, and receipt validation.
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
  RES/UTL/PTN and canonical ID order, resolving/committing/projecting/acknowledging
  one selected module before the next. A protected or failed delivery stops the
  cycle without resetting leases, retry waits or review. Full catalog
  observation/export bookkeeping must not cause out-of-scope module lifecycle,
  retirement, request or board writes. Existing-ID release monitoring is separate.
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