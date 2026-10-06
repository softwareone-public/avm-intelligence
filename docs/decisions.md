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

At the time of this implementation approval, permission or visibility changes,
unattended data acceptance or publication, live collection, and deployment were
not authorized. The later staged-cutover authorization below grants those
operations within its stated scope and gates. Current pull-request reviews and
Pages approvals remain in force until TASK-042 is effectively enforced.

## TASK-043 Clean-Start Authorization

On 2026-10-02, Ledermayer authorized a clean-start cutover. Past reports must
not be imported as native facts, analyses, or selected results. The later
offline-retirement direction below moves their retention to immutable Git
history rather than the current tree. Start from native discovery and validate one native candidate before
any broader dispatch. This supersedes the earlier TASK-043 proposal to import
explicit legacy as-of selections. It does not authorize bypassing protected
`main`, the `data-publisher` environment, required review, broad scheduling, or
deployment controls.

## Offline Runtime Retirement

On 2026-10-06, after PR #215 merged, Ledermayer approved complete local removal
of the former runtime, workflows, configurations, compatibility readers, fixtures
and data. The next reviewed change should contain the finished native-only tree.
No cleanup workflow, additional proposal kind, standalone retirement envelope,
provider/source collection or remote operation is authorized or required for
the local transformation.

Regenerate derived indexes through native Go builders on this computer. Preserve
native source/facts/provenance bytes, canonical identities, state fencing and
receipt semantics. Preserve factual history at immutable commits, not as a
callable fallback runtime. This direction supersedes the proposed multi-PR
workflow-driven cleanup in the initial retirement plan.

Normal review, schema/preservation checks and protected-main controls remain.
The existing trusted-base proposal validator rejects mixed code/data PRs;
offline retirement does not silently disable that boundary or establish its
effective branch-rule requirement. Record the review constraint separately
from the locally verified data cleanup in [FINDINGS.md](FINDINGS.md).

## Staged Cutover Activation Authorization

On 2026-10-04, Ledermayer explicitly authorized live public-source collection,
state/data acceptance, dispatch, deployment, and unattended delivery to complete
the v2 cutover. Exercise this authorization only through the approved provider
allowlist, exact accepted version/commit targets, existing fencing and receipt
contracts, and the ordered TASK-043/TASK-028 rollout. This authorization is not
evidence that any collection, acceptance, dispatch, or deployment has occurred.

The authorization does not permit upstream code/provider execution, private
content transfer, broader source/provider allowlists, guessed or `latest`
targets, direct or force writes to protected branches, blanket permission
bypasses, visibility changes, or broad scheduling before TASK-028. Continue to
use reviewed data/evidence/selection PRs and exact deployment receipts. TASK-042
must encode and pass TEST-022 for the eligible data-only unattended boundary;
code, configuration, schema, workflow, permission, dependency, and exception
changes retain human review. Verify effective repository/App permissions and
environment protections before each corresponding remote write. TASK-039 still
requires recorded evidence and Ledermayer's final product acceptance.

The decisions below govern current native operation. Earlier event, consumer
pin and pilot decisions remain in
[immutable history](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/docs/decisions.md),
not current executable configuration.

| Decision | Status | Record |
| --- | --- | --- |
| SoftwareOne sponsors AVM Intelligence. | Approved | Recorded by Ledermayer on 2026-10-01. |
| Repository, Project, and Pages remain internal until a separate public-content approval. | Approved | No visibility change is included in the factory skeleton. |
| Public data excludes private client source, reports, screenshots, organization identifiers, credentials, and private Git history. | Approved | Factory fixtures are synthetic. |
| One organization GitHub App, `app-avm-intelligence`, is the approved identity. Jobs must mint reduced installation tokens. | Approved | The private key remains in the `data-publisher` environment and is not available to this PR. |
| Project `AVM Intelligence Factory` is the work-tracking projection. | Approved | https://github.com/orgs/softwareone-public/projects/1 |

## Workflow Families And Promotion

Approved by Ledermayer on 2026-10-06 for local implementation. Workflow filenames
and display names use `factory-*` for autonomous events without manual dispatch,
and `manual-*` for dispatch-only operation. Split mixed entry points rather than
adding manual dispatch to factory workflows. Preserve the current autonomous
workload, permissions, approval environments, concurrency and lifecycle stages;
this approval does not activate any additional unattended stage or live run.

Module-targeted manual inputs are RES/PTN/UTL booleans (default true), `force`
(default false), and `module_names` CSV. Names narrow selected types; invalid or
empty scope fails closed. Exact request/attempt/receipt, provider and global
operations retain meaningful safety-specific inputs. Forced operation never
bypasses accepted history, fencing, active ownership, provenance or review.

Develop changes in manual workflows first, verify locally and through separately
approved bounded manual runs, then promote reviewed proven behavior to factory
workflows. Keep the manual counterpart. Dispatch-only worker handoffs remain in
the manual family until a separate reviewed transport change. See the
[operations standard](../.github/instructions/github-operations.instructions.md).

## Historical Publication Evidence

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

This is a historical milestone, not a certification of current native delivery.
Its original accepted tree remains available at the recorded commit. The current
Project remains non-canonical and native publication requires new exact receipts.

## Board Operating Decision

The `AVM Intelligence Factory` Project is the operational projection for human
triage. It is not the scheduler, durable request store, canonical dataset, or
publication authority. Module Issues use one stable hidden module marker and
remain open while the module is active. Status, source identity, outcome, run
URL, and publication commit are reconciled from durable state and accepted
results. Card moves do not authorize work, and human Issue notes are preserved.

The field/status mapping and next implementation slice are recorded in
[`docs/board-operations.md`](board-operations.md).
