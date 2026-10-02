# Factory Board Operations

Owner: Ledermayer.

The AVM Intelligence Factory Project is a projection for human triage and
status visibility. It is not the scheduler, request store, accepted dataset, or
publication authority.

Project: [AVM Intelligence Factory](https://github.com/orgs/softwareone-public/projects/1)

## Views

| View | Purpose | Primary filter |
| --- | --- | --- |
| Factory board | Review all factory and module work by status. | All work types |
| Catalog | Scan the accepted module cohort. | Work type = Module analysis |
| Needs attention | Triage blocked, failed, and review-required work. | Status = Blocked, Failed, or Needs review |
| Delivery roadmap | Track product implementation. | Work type = Implementation |
| Event cohort | Keep the approved event sample visible. | Label or field = demo |

## Module Issue Contract

One active module has one recurring Issue identified by a stable hidden module
marker. A new source release or analysis attempt updates that Issue; it does not
create a new Issue. Retired modules close their recurring Issue only after the
catalog retirement rule is satisfied.

The Issue and Project item are projections of durable request state and accepted
results. A card move does not authorize work. Human notes remain outside the
factory-owned summary block.

V2 reuses the same recurring module Issue across analysis cycles. Each durable
module/context desired generation has one factory-owned cycle comment identified
by a stable hidden cycle marker; retries update that cycle's comment rather than
creating duplicates. The comment records the source SHA, run, outcome, and links
to verified evidence, selection-export, and deployment receipts as they become
available. Current status stays in the Issue summary and Project fields; comments
preserve prior cycle footprints. Never edit human comments or create an Issue per
cycle.

## Status Mapping

| Board status | Meaning |
| --- | --- |
| Backlog | Known work with no queued request. |
| Queued | Durable request is ready for a worker. |
| Analyzing | A worker run owns the request. |
| Validating | Artifacts are being checked before acceptance. |
| Publishing | An accepted commit is awaiting Pages deployment. |
| Done | Accepted result and projections are current. |
| Needs review | Human decision is required. |
| Failed | Infrastructure or processing failure is retryable. |
| Blocked | Required source, release, permission, or policy input is unavailable. |

## Current Milestone

On 2026-10-01, run `36869616628` produced the first accepted fleet cohort:
10 offline IaCLens analyses across four compute, three network, and three
platform/management resource modules. The result was reviewed and merged by
PR #28 at accepted commit `efdd161`.

The fleet workflow projects selected actionable work for this cohort and
applies factory phase/type labels. A data-change workflow also reconciles
accepted reports to Publishing with outcomes and source identity. The Pages
deployment receipt verifies accepted commit `efdd161535b8152e686c13c9785098bc0b0d15ec`,
but the Pages-completion workflow's successful run `36875075805` used workflow
SHA `4bbe923da41e8cc37f254adb62bf4bb7764c6411`. Cohort Issue #37 remains
Publishing with an Unassessed outcome and no published commit, so that callback
does not establish Done. See [F-001](FINDINGS.md#f-001-deployment-receipt-matches-board-reconciliation-does-not).
Use the canonical `data/` files and workflow artifacts for evidence; do not
treat an empty or stale board card as proof that a module was not analyzed. Full
blocked and retired reconciliation remains a follow-up.

## Next Board Slice

1. Create or reconcile one Issue and Project item per accepted module using the
   stable module marker.
2. Populate source version, source SHA, IaCLens version, last analyzed, outcome,
   and run URL from the accepted manifest.
3. Reconcile status from durable request state after every worker and publication
   run.
4. Link findings or remediation Issues separately from recurring module Issues.
