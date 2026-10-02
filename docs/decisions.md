# AVM Intelligence Decisions

Owner: Ledermayer. These decisions describe the approved factory boundaries
and the current product milestone. They do not authorize public visibility,
AI-based semantic analysis, or autonomous remediation.

## V2 Implementation Approval

Approved by Ledermayer on 2026-10-01. The seven selected designs in the
[operating model](../../plan/design-avm-intelligence-operating-model-2.md) are
approved for implementation through the completion-first v2 plan as one
production path. The
[delivery plan](../../plan/refactor-avm-intelligence-delivery-2.md) continues
to govern task order and rollout gates.

The self-contained runtime may use reviewed, pinned compiled Go libraries.
GitHub APIs and necessary public-source APIs are permitted platform and input
dependencies. IaCLens, Python, Terraform/provider binaries, mandatory AI, and
dynamically loaded checks are excluded from the runtime. Terraform, provider,
and upstream code must not be executed.

This implementation approval does not authorize permission or visibility
changes, unattended data acceptance or publication, live collection, or
deployment. Current pull-request reviews and Pages approvals remain in force;
unattended delivery retains its separate approval gate under TASK-042.

## TASK-043 Clean-Start Authorization

On 2026-10-02, Ledermayer authorized a clean-start cutover. Existing
normalized-analysis-v1 candidate and reports remain byte-for-byte historical,
read-only data; do not import them as v2 facts, native analyses, or selected
results. Start v2 from fresh discovery and validate one native candidate before
any broader dispatch. This supersedes the earlier TASK-043 proposal to import
explicit legacy as-of selections. It does not authorize bypassing protected
`main`, the `data-publisher` environment, required review, broad scheduling, or
deployment controls.

The decisions below preserve earlier sponsorship, event, pilot, and access
history. The event-specific scope and IaCLens consumer pin are not the selected
future runtime or delivery scope; v2 governs where they conflict.

| Decision | Status | Record |
| --- | --- | --- |
| SoftwareOne sponsors AVM Intelligence. | Approved | Recorded by Ledermayer on 2026-10-01. |
| Repository, Project, and Pages remain internal until a separate public-content approval. | Approved | No visibility change is included in the factory skeleton. |
| First event date is 2026-10-13. | Approved | The event uses a reduced pilot, not full-catalog intelligence. |
| Event analysis baseline is offline IaCLens. Semantic analysis is preferred only after a bounded public sample and approved budget. | Approved | Semantic mode remains disabled. |
| Public data excludes private client source, reports, screenshots, organization identifiers, credentials, and private Git history. | Approved | Factory fixtures are synthetic. |
| Pilot cohort is one public AVM module before any fleet run. | Approved | No module is selected by this skeleton. |
| One organization GitHub App, `app-avm-intelligence`, is the approved identity. Jobs must mint reduced installation tokens. | Approved | The private key remains in the `data-publisher` environment and is not available to this PR. |
| Project `AVM Intelligence Factory` is the work-tracking projection. | Approved | https://github.com/orgs/softwareone-public/projects/1 |

## Consumer Pin

| Input | Value |
| --- | --- |
| IaCLens release | `v0.1.0` |
| Commit | `c9d714fd6cb7da88ffcb4b049f39480263cc2875` |
| Report schema | 2 |
| Ruleset format | 1 |
| Linux worker asset | `iaclens_0.1.0_linux_amd64.tar.gz` |
| Linux worker SHA-256 | `390ef9d188341c85d42e16adf61cbf6b91effeee9ec0af99c492ab50b08d6238` |

The factory treats `source_digest` as an opaque IaCLens inventory digest, not as
the upstream Git commit. It joins inventory and analysis units by path and
preserves `pass`, `fail`, `unchecked`, and `not_applicable` as distinct results.

## Product Milestone: First Accepted Fleet Cohort

| Item | Record |
| --- | --- |
| Date | 2026-10-01 |
| Workflow run | `36869616628` |
| Accepted pull request | [PR #28](https://github.com/softwareone-public/avm-intelligence/pull/28) |
| Accepted commit | `efdd161535b8152e686c13c9785098bc0b0d15ec` |
| Cohort | 10 offline resource-module analyses: 4 compute, 3 network, 3 platform/management |
| Semantic analysis | Disabled |
| Pages publication | Verified at `https://ideal-adventure-62gqzpm.pages.github.io/` |
| Pages workflow run | `36874979207` |
| Board projection | Pages-completion run `36875075805` completed successfully, but this did not establish convergence: cohort Issue #37 remains Publishing with Unassessed outcome and no published commit. See [F-001](FINDINGS.md#f-001-deployment-receipt-matches-board-reconciliation-does-not). |

This is a product milestone, not a certification claim. The accepted dataset is
canonical and reviewable; the Project board remains a human-facing projection,
and Pages publication remains a separate approved operation.

## Board Operating Decision

The `AVM Intelligence Factory` Project is the operational projection for human
triage. It is not the scheduler, durable request store, canonical dataset, or
publication authority. Module Issues use one stable hidden module marker and
remain open while the module is active. Status, source identity, outcome, run
URL, and publication commit are reconciled from durable state and accepted
results. Card moves do not authorize work, and human Issue notes are preserved.

The field/status mapping and next implementation slice are recorded in
[`docs/board-operations.md`](board-operations.md).
