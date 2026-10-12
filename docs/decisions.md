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

## Approved HUMAN-002 Provider Policy Scope

On 2026-10-12, Ledermayer approved extending the reviewed provider collection
allowlist and assessment tracks to the providers evidenced by accepted facts for
the exact ten-module HUMAN-002 pilot. This approval is limited to those used
providers and their evidenced major tracks; it does not authorize broader
provider collection. The current [`config/providers.yaml`](../config/providers.yaml)
already covers the cohort's provider addresses and tracks through the reviewed
policy updates in PRs #357 and #398, so this approval requires no additional
policy expansion for the current cohort. Any future provider or track additions
remain subject to their own evidence and review.

## Workflow Families And Promotion

Approved by Ledermayer on 2026-10-06 for local implementation. Workflow filenames
and display names use `factory-*` for autonomous events without manual dispatch,
and `manual-*` for dispatch-only operation. Split mixed entry points rather than
adding manual dispatch to factory workflows. Preserve the current autonomous
workload, permissions, approval environments, concurrency and lifecycle stages;
this approval does not activate any additional unattended stage or live run.

Module-targeted manual inputs are RES/PTN/UTL booleans (default true), `force`
(default false), and `module_names` CSV. On 2026-10-06, Ledermayer approved
nonblank names taking precedence over all type checkboxes, whether checked or
unchecked. Without names, only checked types are selected; invalid IDs or empty
scope fail closed. Exact request/attempt/receipt, provider and global
operations retain meaningful safety-specific inputs. Forced operation never
bypasses accepted history, fencing, active ownership, provenance or review.

Develop changes in manual workflows first, verify locally and through separately
approved bounded manual runs, then promote reviewed proven behavior to factory
workflows. Keep the manual counterpart. Dispatch-only worker handoffs remain in
the manual family until a separate reviewed transport change. See the
[operations standard](../.github/instructions/github-operations.instructions.md).

On 2026-10-06, Ledermayer required exact discovery follow-on scope: manual runs
resolve only requested IDs or checked categories, while factory discovery
resolves only newly added catalog IDs. The explicit new-module mode preserves
an empty scope without fleet fallback. Requests, lifecycle/retirement intents
and board delivery may not extend beyond that scope. Full catalog observations
and catalog-export membership remain separate complete bookkeeping. Existing
IDs, including new releases of those IDs and previously blocked sources, are
not re-resolved by new-ID-only factory discovery; use an explicit manual scope
until a separate reviewed release/recovery mechanism is provided. This scope
correction does not add schedules, dispatch workers or approve live recovery.

On 2026-10-09, Ledermayer clarified release discovery: the initial baseline is
the currently released semantic version for each active module, with no older
release backfill. Nightly discovery must check every active catalog module for
new stable AVM releases, including IDs already known to the catalog, and retain
every newly observed version when more than one arrived between scans. The
new-ID-only factory scope is superseded for scheduled release discovery. This
does not dispatch workers; versions wait in the durable module slot through
assessment and verified publication.

Ledermayer subsequently approved correcting manual discovery to one complete
module cycle, not a fleet-wide commit followed by a projection sweep. Shared
catalog observation occurs once. RES then UTL then PTN, each ordered by module
ID, complete source resolution, singleton module state commit and fenced
Issue/Project delivery acknowledgement before the next module starts. Failed,
actively owned, retry-delayed or review-required delivery stops the cycle while
retaining prior commits and pending work. Reduced Project metadata/budgets and
immutable state blobs are reused within the run; live head and fencing checks
remain. Current catalog exports use an atomic reference to the latest immutable
outbox for the generation, preserving earlier candidates and legacy reads.
Factory discovery retains its existing new-ID-only path until separate promotion.

The approved v2 lifecycle plan supersedes the direct Issue/Project delivery
portion of that 2026-10-06 decision. Discovery now commits state-only source,
request, lifecycle and projection handoffs; the scheduled `factory-projection`
workflow is the sole Issue/Project writer. Discovery never waits on board
delivery, and a projection failure does not replay completed source work. The
projector remains hard-disabled in the review branch until WP-41/HUMAN-002.

Per-module state folders were proposed separately. They can clarify ownership,
but do not eliminate shared branch-head races or global transaction metadata.
No state-path migration is approved here; retain current paths, IDs, expected-head
writes and fencing until a compatibility/migration plan is reviewed.

A shared versioned layout contract for both roots was proposed by Ledermayer,
not implemented by the cycle correction. Share identity taxonomy and typed path
resolution, not evidence/control authority or storage semantics. A config change
may select future-write layout only after reviewed reader/validator/export
compatibility; historical moves require a separate explicit migration.

On 2026-10-06, Ledermayer requested implementation through merge with
`manual-discovery` performing the state-layout transition on its next authorized
run. `factory-state` uses GitHub App-only, non-force expected-head writes, not
PRs or manual merge reviews. Keep protected-main code PRs and existing workflow
environments; no permission bypass or additional scheduling is authorized.
The first configured manual cycle converts the complete current state tree
atomically before resolving selected modules, preserving logical records,
legacy replay digests, generations, fences, pending outboxes and Issue bindings.
The persisted contract fixes physical module/global ownership. Logical event
paths remain compatibility identifiers, not physical stage directories. Shared
catalog/import/deployment/batch records remain global. Historical replay hashes
whose owner cannot be reconstructed remain in the global compatibility ledger;
new module events use module-local transaction metadata. Accepted-data paths
and historical publication/proposal hashes are not relocated by this transition.
The new manual transport uses a credential-free separate state checkout to seed
verified immutable blobs; refs and expected-head checks remain live. Unknown
record owners, unsupported contracts, mixed layouts and integrity failures stop
before migration. Ledermayer will dispatch the manual run; local fixtures and a
read-only live snapshot audit do not establish live cutover acceptance.

Ledermayer approved precise budget diagnostics and module/phase/run-attempt
Issue evidence on 2026-10-06. Local reserve stops and GitHub rate-limit responses
have distinct typed causes and WARN categories; terminal failures retain scope
and retry metadata without exposing upstream diagnostics. Limits and ownership
policy are unchanged. Evidence identity excludes source/outcome so same-attempt
retries update one App-owned comment; new runs, attempts or phases, including
forced rediscovery, create new comments. Pending evidence and content-validated
receipts participate in fenced acknowledgement. Discovery/projection recovery
are currently wired; other lifecycle projections retain their existing gates.

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
[`docs/factory-operations.md`](factory-operations.md).

## Versioned Partial Site Publication

On 2026-10-10, Ledermayer confirmed that the first data baseline includes only
the current stable semantic version of each active AVM module; older releases
are not backfilled. Nightly discovery enrolls later stable versions as separate
contexts, and one module slot serializes their assessment and publication. Newer
versions remain queued until exact verified deployment membership releases the
slot. Accepted module/version analysis under `data/modules/` and provider
intelligence under `data/providers/` retain independent receipts, histories,
and acceptance. During site publication, the site cycle queues only eligible
assessment requests from exact accepted inputs and reduces already accepted
results; it does not dispatch workers or wait for new assessments. Worker
execution and acceptance remain separate gated stages, and their results appear
in a later site cycle. One site assembler reads both roots from the same
accepted commit and emits one combined Pages artifact and exact membership
receipt; independent
deployments to the same Pages site could replace each other's view and are not
the chosen design. The site may show partial context coverage, while module-wide
status requires a durable complete required-track set. Missing contexts remain
unknown and cannot be treated as complete or inapplicable. This decision does
not authorize live collection, assessment, acceptance, publication, deployment,
or schedule activation.

## Normalized Provider Intelligence Documents

Provider resource-intelligence proposals and enriched provider snapshots store
each complete source body once per exact provider snapshot. Used resource stubs
reference those immutable documents by a deterministic ID bound to source
metadata, body digest, and observation time. Provider snapshot schema v2 keeps
its historical inline-source reader; new enrichment writes schema v3 with a
snapshot-level document pool and sorted stub references. Validators reject
missing, mismatched, unreferenced, duplicated, or over-budget documents and
references. Portal rows resolve the same source links from either version.
This changes representation only: provider collection limits, accepted-data
receipts, human review, per-version history, and assessment gates remain in
force.

The normalized proposal pool and accepted provider snapshot permit at most 2,500
documents, 10 MiB of unique document bodies, and 10,000 resource-to-document
references. The document ceiling accommodates the observed exact-used-type
AzureRM pilot set; the existing body, per-response, request, page, and wall-time
limits remain unchanged. Collection remains bound to the exact accepted facts
and provider snapshot identities.
