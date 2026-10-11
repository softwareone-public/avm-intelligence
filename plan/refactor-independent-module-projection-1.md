---
goal: "Make discovery independently idempotent per module and centralize all Issue/Project projection in factory-projection"
version: "1.2"
date_created: "2026-10-09"
last_updated: "2026-10-10"
owner: "Ledermayer"
status: "In progress"
tags: [discovery, idempotency, projection, state, workflows]
---

# Introduction

![Status: In progress](https://img.shields.io/badge/status-In%20progress-orange)

Replace the manual discovery batch cursor with independently durable module
work, and make `factory-projection` the only service that reads or mutates
GitHub Issues and Projects. Every stage workflow, including Discovery, commits
its stage result, next-stage intent where required, and desired projection intent
to `factory-state`. Producers make no Issue or Project API call.

The dispatcher reads the durable queue created by discovery. It claims at most
one current stage job for a module and starts one single-stage worker for that
job. A worker batch may contain multiple modules, but the same module cannot be
claimed or processed twice concurrently. Acquisition and assessment are separate
worker workflows; their trusted completion jobs write the durable handoff, while
compute stays credential-free. `factory-projection` processes API-budgeted batches
on a five-minute schedule and is the only board/Issue mutation workflow. Each
success writes an exact `projection_done` acknowledgement. The next stage normally
waits for that acknowledgement, but automatically releases the projection barrier
after fifteen minutes without claiming board success. After an outage, projection
catches up to the latest status rather than replaying obsolete board transitions.

A module has one current lifecycle status, derived from its current durable
request and attempt. Historical requests and attempts remain immutable records,
but they do not create a second current status or authorize a second active job.
Lost publication acknowledgements reuse the same stable operation identity;
they never create another logical operation.

This revision records Ledermayer's requested design and autonomous delivery mode.
HUMAN-001 approval is recorded below, and implementation remains in progress.
That approval authorizes bounded implementation, PR creation, and merge through existing
repository rules without an additional discretionary human PR/merge checkpoint.
It does not bypass required reviews, environment approvals, or separately reserved
live-operation gates. The proposed projection schedule is part of this plan's
activation scope, not evidence that a schedule has been installed or activated.
The diagram at
[factory_lifecycle.drawio](../docs/diagrams/factory_lifecycle.drawio) remains
user-owned design input. Where this plan changes control ownership, the diagram
is updated only after the contract is approved. All identifiers in this document
are local to this plan. Existing v2 task and human gates remain authoritative
and are not discharged by this plan or its fixtures.

**Current evidence and blocker (2026-10-09):** Discovery run
[37890050530](https://github.com/softwareone-public/avm-intelligence/actions/runs/37890050530)
proved a bounded state read/catalog commit and a no-op for one unchanged module;
it did not exercise changed-source acquisition or a board write. UTL run
[37891565986](https://github.com/softwareone-public/avm-intelligence/actions/runs/37891565986)
committed a catalog observation and a six-module continuation, skipped five
unchanged modules, and stopped on changed `avm-utl-sku-finder`. Exact continuation
resume [37894014485](https://github.com/softwareone-public/avm-intelligence/actions/runs/37894014485)
failed at the same module. Source inspection found the module transaction passes
a stable operation ID that is not the ID of any event in its decision, while
`state.DecisionCoordinator` requires an event whose ID equals the operation ID.
The later run committed only a continuation lease checkpoint; its cursor remains
5/6. This plan resolves that defect first, removes discovery's direct board writes
and batch-cursor authority, and retains Discovery as a projection-intent producer.

## 1. Requirements & Constraints

- **REQ-001**: `factory-state` remains the only queue, lifecycle, module-work,
  projection-intent, lease, fence, and retry authority. Issues and Projects never
  determine eligibility, ownership, completion, acceptance, or retry.
- **REQ-002**: A batch is only an invocation envelope. It is not an atomic
  lifecycle transaction, ownership lock, completion cursor, or retry authority.
  Discovery may select exact module IDs; the dispatcher may start a bounded set
  of worker jobs. Neither batch owns module completion.
- **REQ-003**: Every selected module has an independently durable discovery-work
  record under that module's ownership before processing begins. It records
  desired input identity, operation identity, state, lease/fence, safe failure
  category, and retry time. No module's completion depends on another module.
- **REQ-004**: A successful discovery operation commits only that module's source
  pin, next-stage queue record, lifecycle state, discovery-work completion,
  projection intent, and operation receipt. Shared catalog observation and outbox
  records use separate stable-ID transactions and are not prerequisites for
  module completion. A module transaction write set contains only records owned
  by that module.
- **REQ-005**: The first unresolved module failure stops the current discovery
  invocation. Earlier module commits remain complete, the failed module retains
  its classified durable state, and later modules remain unclaimed and due.
- **REQ-006**: A module operation has a stable identity derived from canonical
  module ID, operation kind, desired generation/input fingerprint, extractor
  contract/capabilities, and explicit force generation where applicable. The
  workflow run ID is provenance, not a new logical operation identity. Caller
  timestamps and operation inputs remain fixed across retries.
- **REQ-007**: Existing data-record event IDs and digests remain unchanged. A
  new receipt event is the only event whose ID equals the coordinator operation
  ID. Its digest necessarily includes that event ID and is excluded from the
  data-record preservation requirement. The receipt payload is hashed before the
  receipt event is constructed; it lists the canonical write set but never embeds
  its own digest. The coordinator, not each caller, verifies every write-set
  entry before replay can succeed. Same identity with changed content fails
  closed. This is owner-scoped operation identity, not a global ledger.
- **REQ-008**: Unknown Git publication or remote projection outcome is neither
  success nor rejection. State recovery reads the durable receipt and verifies
  its complete write set before retry. Projection recovery reads back the exact
  remote effect before retry. No new operation identity is minted while an
  outcome remains unknown.
- **REQ-009**: Discovery performs no Issue or Project API call and mints no reduced
  projection token. It atomically writes queue and projection intents and stops
  without waiting for board acknowledgement. The consumer evaluates the barrier.
- **REQ-010**: Every stage, including Discovery, that changes the human-visible lifecycle writes its
  projection intent in the same module-owned transaction as the lifecycle or
  next-stage queue change. The dispatcher writes the intent when it claims or
  releases work. The worker and collector write the intent when they commit
  their stage result and the next required stage. Later assessment, acceptance,
  selection, publication, and Pages stages do the same. Unknown state publication
  is reconciled before claiming either success or absence of an intent. The
  projector consumes only verified durable state. No producer mutates the board.
- **REQ-011**: `factory-projection` is the only workflow allowed to create or
  update recurring module Issues, factory-owned comments, labels, configured
  Project fields/items, or projection acknowledgements. It consumes current
  durable intents and records its outcome in state.
- **REQ-012**: Projection batches have configurable ceilings of 10, 50, or 100
  distinct modules, default 50. Actual admission fits measured REST/GraphQL/search
  and secondary-limit budgets, estimated per-effect cost, execution time, and
  cleanup reserve. Claim immediately before processing and checkpoint each module
  independently. A module-specific failure is parked; unrelated modules continue.
  API-wide pauses, state-integrity failures, cancellation, or exhausted reserves
  stop the invocation. Unstarted work stays due; there is no batch completion lock.
- **REQ-013**: Projection identity binds module, desired generation, intent
  fingerprint, target surface, and stable effect identity. Issue body, summary,
  owned labels, Project item, and each evidence comment require their own
  immutable effect marker. Human notes, comments, and non-owned labels are
  preserved byte-for-byte. A human edit that makes an effect ambiguous leaves
  the intent unknown or needs-review; it is not overwritten by guesswork.
- **REQ-014**: Preserve exact manual ID/category precedence, `force=false`
  defaults, source pins, generations, capacity, network pacing, fencing, leases,
  accepted history, and no-dispatch-from-discovery. The initial lifecycle baseline
  enrolls only each active AVM module's currently released semantic version; it
  does not import older releases. Nightly factory discovery enumerates later
  stable releases for enrolled modules and durably enrolls every newly observed
  release since the baseline, even if multiple releases appeared between polls.
  New versions wait in the module lifecycle slot; discovery never starts workers.
- **REQ-015**: Source reads, state transactions, worker execution, collection,
  and board projection remain separate side-effect stages. `factory-projection`
  does not collect source data, dispatch workers, accept evidence, select
  analysis, publish data, or infer success from workflow status.
- **REQ-016**: A module has one current lifecycle status, one active AVM-release
  context slot, and at most one active stage job. A slot is acquired before the
  first worker stage for a released module version and remains bound to that
  version and desired generation through acquisition, assessment, evidence
  acceptance, selection, publication, and Pages. Worker completion releases only
  the stage claim; it does not release the lifecycle slot. Release the slot only
  after a module-owned receipt proves exact membership in a verified deployment.
  Unknown publication/deployment outcomes retain the slot. Newer released
  versions remain Backlog until the slot is released. Accepted provider
  intelligence changes may create a newer desired generation for the same AVM
  release; preserve prior analysis and selection history. `queue.moduleHasActiveClaim`
  still prevents concurrent stage jobs, but is not by itself the lifecycle-slot
  authority. Lease expiry alone does not prove an old execution stopped; active
  or unknown Actions owners remain parked for verified recovery. Module status
  reduction preserves an older active slot and newer desired version.
- **REQ-017**: Stage handoff is a durable state machine bound to the module's
  active AVM-release slot. Discovery writes immutable queued version membership
  and initial work. The dispatcher acquires the module slot before claiming the
  first stage, then reads only eligible due work. The
  worker computes one stage without state credentials; its trusted validation job
  seals completion and commits the next-stage and projection intents, then releases
  its claim. Produced facts are not accepted facts. The collector reads that next-stage
  intent, performs its single module stage, and writes the following stage and
  projection intent. The same pattern continues through publication and Pages.
  Only exact verified deployment fan-out releases the version slot. No stage
  reads the board to decide what work is due.
- **REQ-018**: Each next-stage JSON intent has schema version, stable intent ID,
  module ID, desired generation, transition ID, input fingerprint, operation
  receipt reference, producing workflow identity, producing run/attempt provenance,
  allowlisted consumer stage, required projection identity, created-at time, and
  fixed barrier deadline. Run provenance does not mint a fresh logical identity.
  Validate payload ownership and paths through TASK-001A, not guessed filenames.
- **REQ-019**: The consumer checks canonical predecessor completion and the exact
  acknowledgement tuple `(module_id, desired_generation, transition_id,
  projection_intent_id, projection_fingerprint)` in its claim transaction. Default
  `projection_barrier_timeout` is 15 minutes from durable handoff creation, fixed
  across retries. At/after the deadline, automatically persist a replayable
  `projection_barrier_released` result and claim if all other eligibility checks
  pass. Never write synthetic `projection_done`. Release bypasses only projection
  availability, never unknown canonical state, fencing, acceptance, provenance,
  policy, or execution ownership. No human approval is needed for timeout release.
- **REQ-020**: `factory-projection` uses schedule `*/5 * * * *` as the durable-work
  polling mechanism. GitHub scheduling can be delayed or skipped; this is not a
  five-minute SLA. Each later invocation rereads due state and retry deadlines.
  Producer-completion fan-in is not required for correctness. No self-dispatch or
  incidental change to dispatcher schedules is included.
- **REQ-021**: Coalesce obsolete status projections into the latest current
  desired status, without losing independently required evidence-comment effects.
  Superseded intents remain historical and cannot overwrite newer status. A later
  acknowledgement does not impersonate an earlier tuple; earlier barriers release
  automatically at their original deadlines. Unknown older remote writes must be
  reconciled before conflicting newer effects are sent.
- **SEC-001**: State Contents credentials and reduced Issue/Project credentials
  remain separate. Producers receive state credentials only when they write
  state. Only `factory-projection` may mint the reduced projection token, and
  only after it finds due intents. A workflow that uses the App key solely to
  mint a state token is not a projection-credential violation.
- **SEC-002**: The projector runs reviewed code from `main`, reads a separate
  verified depth-1 `factory-state` checkout, derives trees internally, reads a
  fresh head, and never executes code or helpers from the state branch.
- **CON-001**: Preserve protected-main rules, pinned actions, `data-publisher`
  environment protections, least privilege, and previously authorized workloads.
  There is no extra plan-level human approval for PR creation or merge. Existing
  required checks/reviews still apply; agents cannot supply a prohibited approval
  or bypass a protection. An incompatible repository requirement is an impactful
  decision to escalate, not permission to change repository settings.
- **CON-002**: The five-minute projection schedule is the requested reconciliation
  design. Its activation follows HUMAN-001 and the evidence/live gates below.
  Broader targets, unrelated schedules, permissions, auto-merge policy changes,
  or environment changes remain impactful decisions requiring human authorization.
- **CON-003**: Retain every stage's state-only projection-intent production, but
  no non-projector workflow may mutate Issues or Projects. Cutover replaces
  all direct board writers with one projector. State-only operator controls may
  remain where the existing approval model requires them.
- **CON-004**: Preserve durable replay and fencing history. Existing discovery
  continuation records may remain only when they are terminal and excluded from
  the new scheduler. Do not edit branch JSON directly, bulk-reset state, or add
  a migration.
- **CON-005**: `outcome_unknown` remains non-actionable until the same identity
  is reconciled. `needs_review` remains terminal until an explicitly new desired
  generation or operator action creates a distinct successor. Neither state
  authorizes a fresh operation ID or a second active job.
- **PAT-001**: Reuse `state.DecisionCoordinator`, `queue.ReduceModuleStatus`,
  `queue.ClaimModule`, the existing one-active-claim rule, projection fingerprint
  and ownership logic, and `slog`/`internal/progress`. Desired projection state
  moves to a neutral contract. Existing delivery, lease, fence, retry, and
  binding fields remain projector-owned records. `ProjectionBatch` cursor
  authority is retired with the old batch lock; its historical records stay
  inert and are not used for claims.
- **PAT-002**: Keep operation replay identity separate from per-record event
  identity. Extend the existing owner transaction metadata only after its
  partition and serialization are proven. Do not create a parallel global
  operation ledger or substitute a payload hash for logical operation identity.

## 1.1 Control Decisions And Execution Agreement

These decisions supersede the Discovery exclusions in version 1.1. Full plan
approval is still pending; task completion and live evidence remain separate.

| Decision | Contract |
| --- | --- |
| Projection ownership | Discovery and every lifecycle producer write state-only intents; only factory-projection performs board/Issue mutations. |
| Natural pacing | Each consumer normally waits for its exact predecessor projection acknowledgement; automatic timeout release prevents indefinite board-outage blocking. |
| Periodic recovery | Poll projection state every five minutes; use due timestamps and bounded API-budgeted batches, not workflow completion as queue authority. |
| Catch-up | Project latest status; retain required evidence effects separately; never replay stale status merely to animate the board. |
| Worker ownership | Separate acquire-source and assessment workflows; trusted workflow completion jobs write handoffs, not credential-free compute jobs. |
| Module independence | Commit module-owned records independently; persist catalog enrollment membership globally without a completion cursor. |
| Delivery autonomy | Plan approval authorizes routine implementation, PR creation, checks, eligible auto-merge and local consolidation; no separate discretionary PR/merge approval. |
| Human intervention | Ask only for plan approval, impactful unresolved decisions, or an externally enforced/reserved approval that agents cannot satisfy. |
| Layout prerequisite | TASK-001A proves actual layout and paths before any new record contract is implemented. |

### Transition And Acknowledgement Contract

| Producer | Durable result / handoff | Consumer prerequisite |
| --- | --- | --- |
| Discovery | Source pin, initial request, projection intent, fixed barrier deadline, operation receipt | Dispatcher requires verified discovery result and exact acknowledgement or automatic barrier release. |
| Dispatcher | One stage claim/attempt and projection intent | Trusted worker preparation requires its exact claim/fence and acknowledgement or release; waiting must not silently expire the claim. |
| Acquire-source workflow | Trusted completion seal, evidence reference, next-stage intent, projection intent | Collector/acceptance follows its existing provenance and review boundary plus acknowledgement or release. |
| Assessment workflow | Trusted analysis completion, next-stage intent, projection intent | Selection/publication retains accepted-input and receipt checks plus acknowledgement or release. |
| Collector / acceptance / selection | Verified stage result, next-stage intent if required, projection intent | Each named consumer checks predecessor identity, policy and ownership independently of board availability. |
| Publication / Pages | Immutable global publication/deployment receipt, then idempotent module-owned receipt references and projection intents | Module Done requires exact verified deployment membership; fan-out replay never repeats deployment. |
| Factory-projection | Per-effect bindings and exact projection_done acknowledgement | Only an exact tuple satisfies a barrier; unknown effects are reconciled, not guessed successful. |

### Bounded Assignment Protocol

- Use the caller's active model for implementation assignments without selecting
  or switching models. This document does not launch agents.
- A fresh agent owns one work packet from the table below, not an entire phase.
  A coordinator orders packets and integrates results but does not absorb several
  implementation slices. Default limit is five production files per packet,
  excluding directly corresponding tests and checkpoint/register updates.
- If a packet exceeds that limit or crosses another ownership boundary, split it
  into numbered child assignments before coding, retaining parent acceptance and
  dependencies. This routine split needs no human approval and changes no scope.
- Parallel assignments require disjoint write paths and completed dependencies.
  Never have two agents edit the same files concurrently. Shared contract owners
  finish before dependent producers begin.
- Each handoff names base SHA, files changed, contract/API decisions, exact focused
  checks and results, remaining blockers, and parent task evidence. Update this
  checkpoint and existing FINDINGS/TODOS; do not fabricate acceptance from fixtures.
- Open PRs ready for review and deliver automatically through existing checks and
  allowed merge behavior. Partial packets must not activate mixed board writers;
  keep unsafe intermediate slices on the integration branch until cutover qualifies.

## 2. Implementation Steps

### Implementation Phase 1: Prove Layout, Ownership, And The Mutation Inventory

- **GOAL-001**: Establish the exact layout and credential inventory before adding
  records. Exit when every state writer and board mutation has a named owner,
  path, and credential, and the live layout is known.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- |
| TASK-001 | HUMAN-001 / Coordinator | Build the complete producer and mutation inventory before changing behavior. Search all Go callers of `RunTransaction`, `createIssue`, `updateIssue`, Project item/field mutations, `SaveProjectionIntent`, and projection-token minting. Search every workflow for `--mint-state-token`, `--mint-projection-token`, and `AVM_WRITER_PRIVATE_KEY`. For each hit record workflow, command, read owners, logical and physical write paths, board effect, and whether the credential mints state or projection access. Include discovery, dispatch, worker, collector, acceptance, recovery, operations, evidence, provider, and publication paths. The inventory is complete only when repository searches return no unclassified hit. Record it in this plan's checkpoint. | No | Pending |
| TASK-001A | TASK-001 / State | Prove the layout contract before designing new records. Determine whether the current `factory-state` head is legacy layout or `module-v2` by reading its existing layout metadata through the normal read API; do not modify it. Document the required logical path, payload `module_id`, kind, physical path, and aggregate owner for every existing discovery, request, attempt, projection, catalog, and continuation record involved in this plan. Identify which current module transaction events resolve outside the module owner, including catalog outbox and continuation records. This task is a hard gate for TASK-002 and TASK-003. | No | Pending |
| TASK-001B | TASK-001A / State | Define the ownership matrix in this checkpoint. Catalog observation, immutable enrollment membership and catalog outbox are global stable-ID transactions. Discovery owns its module source pin, request, lifecycle, discovery work, next-stage and projection intents, barrier deadline and receipt. Later producers own their module transitions and intents. Publication/Pages global receipts fan out to independently replayable module references, never a cross-owner module transaction. Add negative examples and the logical/physical mapping before implementation. | No | Pending |

### Implementation Phase 2: Repair Operation Identity

- **GOAL-002**: Make one module operation publishable and replayable without
  changing another module or any existing data-record digest. Exit when the
  coordinator enforces the receipt contract and the sku-finder payload is
  represented without claiming the historical failed operation was byte-preserved.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- |
| TASK-002 | TASK-001A, TASK-001B / State | Specify receipt paths/kinds through identity layout and OperationalRecordPath. Payload module_id resolves ownership. Persist immutable result data/reference and sorted write-set entries before building the receipt event whose ID equals operation ID; exclude its own digest. Extend DecisionCoordinator with coordinator-owned verification of caller expected identity and complete historical event/path/digest proof before resolver success or unknown-publication recovery. Legitimately advanced current records do not invalidate historical proof; missing proof and changed content under the same identity fail closed. Split schema/path and coordinator packets below. | No | Pending |
| TASK-002A | TASK-002 / Discovery | Add a read-only fixture representing the current sku-finder continuation, queued request, source pin, lease, and failed event set. Redact secrets and record the source head used. The test proves that adding the receipt creates a new valid operation and commit; it must not claim byte-for-byte preservation of the never-committed historical operation. Replay of the new operation returns the original result without a duplicate request or source-pin mutation. Conflicting request or pin content fails closed. This fixture authorizes no live read beyond normal local test data and no state write. | No | Pending |
| TASK-003 | TASK-001A, TASK-002 / Identity + State | Add `discovery_work` only if TASK-001A proves that no existing module request or control record already provides the required state. Its logical and physical paths come from that proof. Fields are schema version, module ID, desired fingerprint, receipt reference, work state, attempt, owner run, owner generation/fence, lease expiry, retry time, and allowlisted error category. The receipt remains the replay authority. Work state cannot override a contradictory receipt result. Reject owner mismatch, stale fence, invalid state, unsafe error text, fingerprint inconsistency, and any board-derived field. | No | Pending |

### Implementation Phase 3: Make Discovery A State-Only Intent Producer

- **GOAL-003**: Discovery writes queue and desired projection intents without a
  board API call or batch completion cursor. Earlier commits survive failures.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- |
| TASK-004 | TASK-002, TASK-003 / Discovery | Split catalog observation/outbox persistence from module/version enqueue. Persist exact immutable enrollment membership with catalog acceptance: initial current-release baseline only (no older-version backfill), then every later stable AVM module release observed nightly, plus manual selection provenance. Recover missing version work from membership even when a later delta is empty. Reconcile only missing/due module/version records; catalog retry cannot repeat completed contexts. No enrollment cursor, batch lease or global completion record owns module success. Test crashes before and between enrollment commits and multiple releases between polls. | No | Pending |
| TASK-005 | TASK-004, TASK-008 / Discovery | Replace Cycle/ContinueCycle delivery coupling with a per-module/version processor in internal/discovery. Persist the initial current-release baseline without older-version backfill. On nightly discovery, enumerate every stable AVM release after each enrolled module's durable baseline/watermark, retain immutable release membership, and enqueue each new version behind the module slot. Resolve source outside the transaction, then atomically commit module source pin, version-scoped request/lifecycle, work completion, next-stage intent, desired projection, fixed barrier deadline and receipt. No board call or worker dispatch. First unresolved module/version failure stops with earlier commits preserved and later work due. Replay uses module/version identity, not continuation cursor. | No | Pending |
| TASK-006 | TASK-005 / CLI + Workflows | Remove discovery projection commands, callbacks, token minting, and Issue/Project API calls from `cmd/factory/discovery_cycle.go`, `discovery_projection.go`, `native.go`, `main.go`, `manual-discovery.yml`, and `factory-discovery.yml`. Add the approved nightly release-discovery schedule without dispatching workers. Keep exact manual selectors, force behavior, pacing, catalog observation, and separate state checkout. A failed module/version exits with durable state and safe error class. The current continuation remains historical and excluded from selection; do not edit or resume it on the legacy path. | No | Pending |
| TASK-007 | TASK-005, TASK-006 / Discovery + Queue | Map source and transaction failures to the existing work states. Retryable failure records an allowlisted category and retry time without marking assessment failure. Policy or integrity failure becomes `needs_review`. Unknown publication retains the operation ID and blocks claim advancement until reconciliation. Add first, middle, and last module failure tests, plus replay, force refresh, empty scope, exact order, lease expiry, stale owner, and cross-module preservation tests. | No | Pending |

### Implementation Phase 4: Unify Stage Intents And Consumer Barriers

- **GOAL-004**: Every lifecycle producer, including Discovery, atomically writes
  its desired projection and handoff. Every consumer enforces exact acknowledgement
  or automatic timeout release without weakening canonical safety checks.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- |
| TASK-008 | TASK-001B / Projection Contract Owner | Create neutral desired-intent types in internal/projection without a GitHub client. Retain proven projection_intent logical kind. Include REQ-018 producer/consumer provenance, stable transition identity, desired status/fingerprint, owned fields and independent evidence effects. Keep remote IDs, delivery leases/fences and acknowledgements projector-owned. Define exact projection_done and separate barrier-release contracts; add schema/path/owner/digest fixtures before producer conversion. | No | Pending |
| TASK-008A | TASK-002, TASK-008 / Queue | Implement exact barrier evaluation in internal/queue and stage consumers. Persist a fixed fifteen-minute deadline at handoff creation. Claim transaction verifies predecessor receipt and exact projection_done, or atomically records automatic projection_barrier_released at/after deadline before claiming. Stale acknowledgements, missing canonical completion, unknown publication or active execution never unlock work. Worker preparation waiting preserves valid ownership through the existing lease/recovery contract; no unattended lease reset. | No | Pending |
| TASK-009 | TASK-008A / Producer Integrator | Integrate independently assigned dispatcher, acquisition completion, assessment completion, collector, acceptance, selection, publication and Pages packets below. Each trusted writer commits its stage result, successor if required and projection intent atomically. Discovery conversion belongs to TASK-005. Reuse ReduceModuleStatus across required contexts with older active/newer desired work. Replay succeeds only for identical identity/content; newer status supersedes old desired effects without erasing evidence. | No | Pending |
| TASK-009A | TASK-009 / Queue | Preserve the current single-job rule explicitly at every new handoff. `ClaimModule` and request-specific claim paths continue to reject a module with a `claimed` or `running` request. Worker and collector completion must release or transition that same request before another stage can be claimed. Tests cover two queued stages for one module, concurrent dispatchers, expired lease recovery, and a batch containing several modules. The batch test must fail if any module is selected twice. | No | Pending |
| TASK-010 | TASK-009 / Credential Boundary Owner | Remove projection-token minting and Issue/Project mutation calls from all non-projector workflows. Do not remove state-token minting merely because the same App private key is present. The contract test distinguishes the two. Evidence, review, and data APIs unrelated to Issue/Project observability retain their approved boundaries. A non-projector workflow containing a projection mutation method or projection-token request fails the test. | No | Pending |

### Implementation Phase 5: Build The Single Projection Service

- **GOAL-005**: One reviewed service applies every durable intent to Issues and
  Projects and checkpoints each module independently.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- |
| TASK-011 | TASK-008, TASK-009 / GitHub Projection | Refactor internal/github projection claims to neutral intents and latest-status selection. Admit distinct modules under REQ-012 budgets, default ceiling 50, with selectable 10/100 ceilings. Claim one module immediately before processing; retain per-module priority, fresh-head checks, lease/fence, evidence and bindings. Park module-specific failures and continue neighbors; stop on API-wide pause/integrity/cancellation/reserve exhaustion. No ProjectionBatch lock/cursor. | No | Pending |
| TASK-011A | TASK-011 / GitHub Projection | Serialize production projector invocations with repository-wide non-cancelling concurrency and retain module lease/fence checks. Do not take over solely because time expired: active or unknown prior runs/in-flight writes remain unresolved. Same identity takeover follows verified owner termination and per-effect reconciliation. State fencing rejects stale acknowledgements but is not GitHub-side write fencing. Test delayed old requests across expiry and newer intent arrival; no conflicting retry without proof. | No | Pending |
| TASK-012 | TASK-011A / GitHub Projection | Reconcile Issue create/update, owned labels/summary, Project item/fields and evidence comments by stable per-effect identities. Persist exact projection_done only after complete verified match. Lost acknowledgement retries reconcile the original identity; unprovable in-flight effects stay unknown and contradictions become needs-review. Coalesce stale status but retain pending evidence; settle older uncertain effects before conflicting latest writes. Automatic barrier release leaves projection unknown/pending and never fakes success. | No | Pending |
| TASK-013 | TASK-011, TASK-012 / Workflow Integrator | Add .github/workflows/factory-projection.yml with schedule */5 * * * *, reviewed main code, verified isolated state checkout and separate state/projection credentials. Fresh-read due work before minting projection token. No producer artifacts, workflow_dispatch, self-dispatch or required workflow_run fan-in. Use repository-wide non-cancelling concurrency; run bounded budgeted batches and leave remainder/retries for later ticks. Use valid Actions permissions: actions: read only for implemented run-observation APIs, plus proven corresponding App grants; reject nonexistent workflows: read. Keep the job hard-disabled in the mergeable PR; only WP-41/HUMAN-002 may enable it after exact live validation. | No | Pending |
| TASK-014 | TASK-013 / Manual Projection Owner | Retain manual-projection recovery/flush only as state-only exact-target requests, consumed by the scheduled projector; remove their direct API/token behavior. Preserve operator allowlists, original intent identities and deadlines. Scheduled polling recovers producer commits even when workflows fail afterward and resumes due work without a new producer event. Test delayed/skipped ticks and next-tick catch-up; schedules are not an SLA. | No | Pending |

### Implementation Phase 6: Remove Direct Board Writers And Qualify Cutover

- **GOAL-006**: Prove offline that all stages write state-only intents, automatic
  barriers preserve canonical safety, and one scheduled projector owns mutations.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- |
| TASK-015 | TASK-006, TASK-010, TASK-013, TASK-014 / Integrator | Delete obsolete direct discovery delivery callbacks/flags and all other non-projector board API paths, not producer intents. Align docs/issue-lifecycle.md, docs/factory-operations.md and affected repository instructions with this approved contract. Update user-owned lifecycle diagram only within approved design. Search must show one production mutation owner and no compute credentials. | No | Pending |
| TASK-016 | TASK-015 / Validator | Verify receipts after legitimate record advancement, enrollment crash recovery, exact barriers and automatic timeout, every producer including Discovery, one-active-execution exclusion, projector-only APIs, scheduled retry, latest-status catch-up, independent evidence, unknown writes and human preservation. Use at least three modules to prove completed/failed/unstarted cases. Separate module parking from invocation-wide stops and test global receipt fan-out replay. | No | Pending |
| TASK-017 | TASK-016 / Offline Validator | Run formatting, `go mod verify`, full tests, vet, dimension listing, required race tests, workflow/YAML/shell/selection tests, and credential searches. Record exact commands and results. This task dispatches no workflow. | No | Pending |
| TASK-018 | TASK-017 / Reviewer | Obtain independent read-only Factory Contract Reviewer review with operation identity, ownership, multi-context status, every producer, automatic barrier safety, global fan-out, unknown effects, credential isolation, scheduled liveness and direct-writer removal. Resolve blockers in separate bounded fix packets and retain the full review; this is agent review, not a discretionary human PR gate. | No | Pending |
| TASK-019 | TASK-018, HUMAN-002 / Cutover Agent | Audit named active workflows/leases/owners and safe drain procedure. Proceed autonomously only within an already approved exact maintenance envelope; an unapproved pause/cancel/owner-release or policy change is impactful and must be escalated. No blanket cancellation or lease clearing. Preserve UTL state until TASK-023 and prove no old writer can make a late board mutation. | No | Pending |
| TASK-020 | TASK-019 / Delivery Agent | Open/update ready-for-review integration PR and merge automatically when existing required checks/reviews and permitted merge rules pass. No HUMAN merge gate or discretionary confirmation. Use eligible existing auto-merge; change no protection or setting. Report an enforced review/environment blocker as impactful rather than bypassing it. Verify merge SHA and fast-forward local main while preserving unrelated work. | No | Pending |
| TASK-021 | TASK-020, HUMAN-002 / Validation Agent | Execute the separately approved one-time end-to-end pilot for exactly `avm-res-apimanagement-service`, `avm-res-authorization-roleassignment`, `avm-res-compute-capacityreservationgroup`, `avm-res-compute-disk`, `avm-res-compute-virtualmachine`, `avm-res-compute-virtualmachinescaleset`, `avm-res-managedidentity-userassignedidentity`, `avm-res-network-applicationgateway`, `avm-res-network-networkinterface`, and `avm-res-network-virtualnetwork`: discover current releases without force/backfill; acquire; collect version-pinned provider intelligence from approved sources; assess only after exact accepted provider/policy inputs; review and accept evidence; publish once. Keep the full 232-module catalog on the portal while only this cohort receives pilot processing. Use existing review/environment gates; verify exact receipts, provenance, one-active-version slot, preserved history, and no duplicate effects. No schedule activation. Fixtures do not satisfy live acceptance. The interrupted UTL continuation is explicitly outside this scope and is not resumed/retried. | No | Pending |
| TASK-022 | TASK-021, HUMAN-002 / Activation Agent | **Not authorized by the ten-module one-time pilot.** Keep the five-minute projector schedule and recurring producer families disabled. Any later schedule/family activation requires its own exact approved envelope and existing environment/repository gates; record exact runs/heads, budgets, receipts, catch-up and timeout outcomes. This does not establish product acceptance. | No | Pending |

### Implementation Phase 7: Parked Interrupted UTL Work

- **GOAL-007 (parked)**: The historical sku-finder continuation is outside the
  approved ten-module pilot. Do not resume/retry it, dispatch a worker, or alter
  its operational state under this HUMAN-002 envelope. It is not a prerequisite
  for TASK-021; any future reconciliation requires a separate explicit decision.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- |
| TASK-023 | Parked / Reconciliation Agent | Historical UTL handoff and continuation terminalization are not authorized by the ten-module pilot. Do not resume/retry the continuation, dispatch work, or change its operational state. Reconsider only under a separately approved exact scope. This is not a TASK-021 dependency. | No | Parked |
| TASK-024 | Parked / Projection Validation Agent | Historical UTL projection reconciliation is not authorized by the ten-module pilot. Do not read-repair by mutation, project, or retry an effect under this scope. Reconsider only under a separately approved exact scope. This is not a TASK-021 dependency. | No | Parked |

### Approval And Evidence Gates

| Gate | Required Human Action | Evidence / Stop Condition |
| --- | --- | --- |
| HUMAN-001 | Approve the complete revised plan once | **Approved by Ledermayer on 2026-10-09 in chat.** Enables bounded implementation assignments and autonomous PR/merge delivery under existing rules. This approval is not implementation, validation, live-operation, or activation evidence. |
| HUMAN-002 | Resolve an impactful decision only when not already authorized | **Approved by Ledermayer in chat on 2026-10-11 for one one-time end-to-end pilot covering exactly the ten modules listed in TASK-021.** The portal retains the full catalog; only those ten receive pilot processing/assessment. Authorized stages are current-release discovery (no force/backfill), acquisition, provider intelligence collection, assessment after exact accepted inputs, reviewed evidence acceptance, and one publication. Existing review/environment gates still apply. Recurring schedules remain off. The historical UTL continuation is excluded. Any broader scope, owner release, policy change, force/backfill, retry/resume of that continuation, or schedule activation is not authorized here. No blanket cancellation, reset, policy bypass or inferred authority. |

Version 1.1 HUMAN-003 through HUMAN-006 are replaced by this decision-based
agreement, not marked completed. Previously reserved live/environment gates
remain effective until explicitly covered by a human-approved exact envelope.
PR creation and compliant merging themselves have no additional human checkpoint.

| Gate | Completion Criteria |
| --- | --- |
| GATE-001 | Layout, ownership matrix, receipt verification, and replay fixtures pass without cross-owner writes or historical digest changes. |
| GATE-002 | Every approved producer including Discovery writes intents atomically; consumers require exact acknowledgement or automatic timed release; no producer has a board API path or projection credential. |
| GATE-003 | One active job and one current status per module are enforced across concurrent batches and stages. |
| GATE-004 | One scheduled budgeted projector owns mutations, independently checkpoints modules, reconciles unknown effects and catches up latest status without losing evidence. |
| GATE-005 | Offline qualification and read-only contract review pass with no unresolved blocker. |
| GATE-006 | The exact one-time ten-module end-to-end pilot completes under HUMAN-002 with receipt-backed discovery, acquisition, accepted provider inputs, assessment, reviewed acceptance and one Pages publication; no duplicate effects or history overwrite. |
| GATE-007 | Separately approved producer-family activation has observed results; this pilot does not activate schedules and unverified families remain paused. |
| GATE-008 | Separately parked historical sku-finder reconciliation. It is outside the ten-module pilot and must not be resumed/retried or state-mutated under this HUMAN-002 envelope. |

Historical last-observed continuation state (not authorization to inspect or
mutate it):
`discovery-cycle-d9db4db56f36ddfd1f6cc4d862f6f065b9012cb931eeb960d4e7d80fa891eb93`
was last observed at cursor 5/6 for `avm-utl-sku-finder`, with
`pending_delivery=false`, owner `37894014485-1`, and lease checkpoint
`2335460b7a858f1c332ce4554f0ac57061730392`. This remains historical context
only. Do not resume the legacy code path or alter this state under the pilot.

**WP-01 source inventory checkpoint (2026-10-09, read-only):** The production
Go/workflow inventory found durable discovery/catalog writes in
`internal/discovery`, module-cycle and continuation writes in
`internal/discovery/cycle.go` and `continuation.go`, queue claims/recovery in
`internal/queue/dispatch.go`, worker completion in `cmd/factory/work.go`, and
acceptance/operations in `internal/state/evidence.go` and
`internal/queue/operations.go`. Projection intent, claim, delivery checkpoint,
and binding state is owned by `internal/github/projection_state.go`; actual
Issue/Project mutations and run-evidence comments are implemented under
`internal/github`. Current discovery workflows invoke direct projection after
state persistence; discovery cycles also deliver each module before proceeding.
State-token minting is explicit in state-writing workflows. The reduced
Issue/Project token is minted internally by Go for projection; no workflow
contains `--mint-projection-token`. Review-token and workflow `GITHUB_TOKEN`
uses are separate credentials. Provider collection/export and catalog/evidence
proposal publication do not mutate Issues or Projects. Several selection,
assessment-input and deployment transaction APIs have no production caller in
the checked-in CLI/workflows. The full classified inventory is retained in the
WP-01 handoff; this checkpoint does not claim live workflow execution or complete
TASK-001 acceptance.

**Remote layout proof (2026-10-09, read-only):** Used `gh api` to verify
`factory-state` at `2335460b7a858f1c332ce4554f0ac57061730392`, query its Git tree,
and fetch the exact `state/layout.json` blob
`fef65fc5c2c4f029cacf4d84b1e4b718da955ecb`. Parsed metadata reports schema
version 1, profile `module-v2`, contract digest
`layout-contract-3c6413132489b687b85dd531f36382e6b17d81cb2187bf8de99af46f2792ecbd`,
migration base `68cb75aae4205ca641080c8780984438ea183235`, and 1,109 logical-to-
physical path mappings. The Contents API returned 404 despite the branch and
tree proving the path exists; the Git Trees/blob endpoints provided the
authoritative read. This corrects the earlier false Not Found conclusion.
TASK-001A's live profile and TASK-001B's owner matrix are now recorded below.
No state branch write, workflow dispatch, or live lifecycle operation was
performed.

**TASK-001B ownership matrix (source and live manifest, 2026-10-09):** The
verified `module-v2` manifest and compiled `identity.OperationalRecordPath`
agree that module records retain stable logical paths while physical records
live beneath `state/modules/{type}/{module_id}/`. `module_id` in each typed
payload determines the module owner; path/kind/payload disagreement fails
validation. The live manifest contains 1,109 mappings across logical roots
`state/global`, `state/modules`, and legacy-compatible `state/exports` entries
under the module layout physical root. The source-proven matrix is:

| Record family | Logical path / kind | Physical owner | Writer / cross-owner note |
| --- | --- | --- | --- |
| Accepted catalog | `state/discovery/catalog.json` / `discovery_catalog` | `state/global/catalog/current.json` | Discovery catalog acceptance; global stable-ID transaction. |
| Catalog observation | `state/discovery/observations/{observation_id}.json` / `discovery_observation` | `state/global/catalog/observations/{id}.json` | Discovery; global immutable observation transaction. |
| Catalog proposal | `state/exports/{proposal_id}.json` / `export`, payload has no module ID | `state/global/outboxes/catalog/{id}.json` | Discovery/catalog outbox; global stable-ID transaction, separate from module completion. |
| Discovery cycle | `state/discovery/cycles/{cycle_id}.json` / `discovery_cycle` | `state/global/discovery/cycles/{id}.json` | Manual discovery continuation owner/lease/cursor; global only. It must not be in a module write set. |
| Source pin | `state/discovery/source-pins/{module_id}.json` / `source_pins` | `state/modules/{type}/{module_id}/source/pins.json` | Discovery; module-owned, payload module ID and logical filename must agree. |
| Acquisition/assessment request | `state/requests/{request_id}.json` / `request` | `state/modules/{type}/{module_id}/requests/{request_id}.json` | Discovery/queue; module-owned by request payload. |
| Attempt/lease/fence | `state/attempts/{request_id}/{attempt_id}.json` / `attempt` | `state/modules/{type}/{module_id}/attempts/{request_id}/{attempt_id}.json` | Dispatcher/worker/recovery; module owner from attempt payload; active-claim exclusion remains request-specific. |
| Module lifecycle/input | `state/modules/{module_id}.json` / `module` | `state/modules/{type}/{module_id}/assessment-inputs.json` | Assessment input import; module owner from payload. |
| Module control | `state/operations/modules/{module_id}.json` / `module_control` | `state/modules/{type}/{module_id}/controls.json` | Operator command; module-scoped. |
| Desired projection | `state/projection/intents/{module_id}.json` / `projection_intent` | `state/modules/{type}/{module_id}/projection/intent.json` | Discovery and projection producers; module-scoped desired state. |
| Projection lifecycle | `state/projection/lifecycle/{module_id}.json` / `projection_lifecycle` | `state/modules/{type}/{module_id}/projection/lifecycle.json` | Queue/projection lifecycle adapter; module-owned. |
| Projection delivery and binding | `state/projection/delivery/{module_id}.json`, `state/projection/bindings/{module_id}.json` / `projection_delivery`, `projection_binding` | `state/modules/{type}/{module_id}/projection/{delivery,binding}.json` | Projector-owned remote identity, lease/fence and outcome; not producer-owned. |
| Projection batch | `state/projection/batches/{batch_id}.json` / `projection_batch` | `state/global/projection/batches/{id}.json` | Projector legacy batch/cursor state; global, not module completion authority. |
| Selection and publication | `state/selections/{module_id}/{context}/...` / `selection`; `state/publications/{id}.json` / `publication` with module ID | `state/modules/{type}/{module_id}/selections/...`, `publications/evidence/{id}.json` | Module-owned stage records. Global deployment receipt `state/publications/deployments.json` maps to `state/global/publications/deployments.json`; fan-out must be separate per module. |
| Global controls/operations/imports | `state/operations/global.json`, `state/operations/commands/{id}.json`, `state/inputs/imports/{id}.json` / corresponding kinds | `state/global/...` | Global operator/control and input records; not module completion. |

**Cross-owner examples:** current `discovery.Persist` builds one transaction
containing a global observation/catalog record, module pins/requests/projection
intents, and a global catalog export when applicable. The manual module-cycle
path additionally appends its global continuation checkpoint to that module's
events before `RunTransaction`; this violates the desired module-only write-set
boundary. The existing `ReadSet.Owners` names only `moduleID` there, but the
event set still includes globally owned continuation state. Catalog acceptance
and immutable catalog/outbox persistence therefore need separate global
stable-ID transactions from independently committed module work. Negative
examples: a module receipt must not own catalog membership or catalog outbox;
a module transaction must not include `state/discovery/cycles/...` or
`state/global/projection/batches/...`; a global deployment receipt must not
make one module's Done status depend on a multi-module event. TASK-001A/B are
complete from the verified manifest plus source ownership proof; receipt path
and coordinator design may now proceed. No state branch write or workflow run
occurred.

**WP-03 receipt contract checkpoint (2026-10-09):** Added the module-scoped
`operation_receipt` logical path and codec, plus a receipt payload containing
the logical operation ID, immutable JSON result and digest, and sorted
event/path/kind/digest write-set entries. The receipt event is constructed with
the operation ID and excluded from its own write set. The state record validator
binds the path to module and operation identity and rejects invalid result
digests, duplicate/unsorted event IDs, the receipt ID in its own set, and
cross-owner/global records. Focused identity, state and layout tests pass.
Coordinator-owned historical write-set verification and replay recovery remain
WP-04; this packet does not claim complete TASK-002 or GATE-001 acceptance.
HUMAN-001 approval is recorded above. No live state operation occurred.

**WP-04 coordinator proof checkpoint (2026-10-09):** Added
`RunOperationTransaction`, which receives fixed expected module events separate
from the decision callback. The coordinator adds the operation receipt event,
verifies exact event/path/kind/digest membership and durable event proofs before
replay, and returns the immutable result stored in the receipt after mutable
records advance. Missing or tampered proof and changed expected content under
the same operation ID fail closed. Unknown-publication recovery applies the same
proof when the receipt is visible, while legacy coordinator calls preserve their
prior behavior. Full `go test ./internal/state -count=1` and
`go test -race ./internal/state -count=1` pass. This remains the coordinator
contract slice only; discovery integration, full TASK-002 acceptance, the UTL
fixture, and GATE-001 are not complete.

**WP-05 sku-finder fixture checkpoint (2026-10-09, read-only source):** Queried
`factory-state` at head `2335460b7a858f1c332ce4554f0ac57061730392` through `gh`
Git Trees/blob APIs. The exact module aggregate, request, source-pin and
continuation blobs were inspected without writing state. The fixture captures
the queued request ID
`avm-utl-sku-finder-3671cb5053264385bf22c73fb89c35220d9b89ceb87c2cd30374307388a46c1b`,
generation-1 `acquire_source`, source v0.3.0 at
`4b6c302921ec88e7ebdcddc599ddffc080bde76b`, the five prior owner event
digests, and the six-module continuation at cursor 5 with the same five
completed skips and sku-finder as the changed target. The actual current pin is
v0.3.0, not the plan narrative's v0.4.0; no historical or live data is rewritten
by the fixture. The failed operation's candidate event set was never committed,
so the test explicitly does not claim its preservation. A receipt-bearing new
stable operation commits and replays without changing request/pin bytes or
rerunning the decision. The focused test passes. This is local fixture evidence,
not live reconciliation or GATE-008 acceptance.

**WP-06 discovery-work contract checkpoint (2026-10-09):** Existing request and
attempt records cover queued/claimed/running work, desired fingerprint and
generation, attempt/fence/lease, retry class/time and sealed completion. They do
not represent source-resolution failure before a request exists or bind that
pre-request state to its immutable operation receipt, so a separate module-owned
control record is required. Added `discovery_work` at logical path
`state/discovery/work/{module_id}.json`, physically
`state/modules/{type}/{module_id}/discovery/work.json`. Its typed payload
contains the desired fingerprint, operation ID, optional receipt reference,
work state, attempt, owner generation/run/lease, retry time and allowlisted
failure category. Validators reject path/payload owner mismatch, invalid
states/categories, malformed identities/fingerprints, inconsistent owner/lease,
retry-field contradictions and foreign receipt paths. Focused state/identity
tests pass. This slice does not yet integrate discovery writes or make work
state authoritative over the operation receipt; those remain required
acceptance conditions. No live state was written.

**WP-06 offline gates (2026-10-09):** The 25 focused state/layout/identity tests,
touched `state`, `identity` and `discovery` package tests, state race suite, and
full repository checks (`go mod verify`, `go test ./... -count=1`,
`go vet ./...`, and `go run ./cmd/factory --list-dimensions`) passed. Repository
gofmt and `git diff --check` passed. This proves local record/path behavior
only; it does not complete receipt-authority reconciliation, producer
integration, or any live/activation gate.

**WP-07 path/schema follow-on checkpoint (2026-10-09):** The existing neutral
contracts did not yet have durable operational kinds or canonical logical paths
for stage handoffs, exact projection acknowledgements, and timeout barrier
releases. Added module-owned paths `state/projection/handoffs/{module_id}/{handoff_id}.json`,
`state/projection/acknowledgements/{module_id}/{projection_intent_id}.json`, and
`state/projection/barrier-releases/{module_id}/{projection_intent_id}.json`.
Their operational kinds are `stage_handoff`, `projection_acknowledgement`, and
`projection_barrier_release`; module-v2 maps each below
`state/modules/{type}/{module_id}/projection/{handoffs|acknowledgements|barrier-releases}/{record_id}.json`.
Identity path resolution verifies the path module and content-addressed ID,
while state validation binds payload ownership and exact projection tuple. A
handoff cannot be replaced with changed bytes at its ID; a barrier release is a
distinct `projection_barrier_released` payload and rejects `projection_done`.
Fixtures cover owner mapping, kind/path rejection, immutable handoff replacement,
and module-v2 encode/decode round-trip. No new schema was needed; these records
use the operational state registry at schema version 1. Focused identity/state/
projection tests, `go test ./... -count=1`, `go vet ./...`, gofmt for changed Go
files, and `git diff --check` pass. This is the durable path/validation slice
only: queue barrier evaluation, producer/consumer integration, live writes and
workflow activation remain unverified and out of scope. No API, state write, or
workflow was invoked.

**WP-07 neutral projection contract checkpoint (2026-10-09):** Added
`internal/projection` desired intent, independent evidence effects, stage handoff,
exact `projection_done` tuple, and separate timeout-release contracts. Workflow
run/attempt provenance does not affect stable handoff identity; the deadline is
fixed at handoff creation, and timeout release cannot claim projection success.
Added module-owned record kinds and paths: `stage_handoff` at
`state/projection/handoffs/{module_id}/{handoff_id}.json`,
`projection_acknowledgement` at
`state/projection/acknowledgements/{module_id}/{projection_intent_id}.json`,
and `projection_barrier_release` at
`state/projection/barrier-releases/{module_id}/{projection_intent_id}.json`.
The verified module-v2 codec maps each beneath that module's `projection/`
directory; state fixtures verify module ownership, serialization round-trip,
path/kind binding, immutable handoff content, exact acknowledgement tuples and
separate non-success release status. A stale projection intent ID is rejected.
The neutral package has no GitHub dependency or delivery lease state. Focused
projection/identity/state tests pass. Producer/consumer path use and workflow
behavior remain subsequent packets; no live state was written.

**WP-08 exact queue barrier checkpoint (2026-10-09):** Queue claim selection now
requires one matching durable stage handoff and a verified predecessor operation
receipt/write set. Only an exact projection acknowledgement tuple unlocks before
the fixed deadline. At/after the deadline, the same claim transaction records a
separate `projection_barrier_released` record and never synthesizes
`projection_done`; active module ownership, unknown receipt, missing handoff or
stale/mismatched acknowledgement stays blocked. Queue package and race tests
pass. Queue claims now correctly require producer handoffs, so this branch is
not deployable until the producer/completion packets (WP-10, WP-14 through
WP-21) write compatible handoffs. Existing manual producers have not been
converted and must not be cut over.

**WP-09 catalog enrollment checkpoints (2026-10-09):** Split the former packet
into WP-09A (catalog contract) and WP-09B (discovery persistence), preserving
ownership boundaries. WP-09A adds `catalog.AcceptedSelection`: immutable sorted
module membership with schema version, catalog generation, provenance and a
content-derived stable ID; validation and JSON decoding reject corrupt,
duplicate, noncanonical or changed same-ID membership. WP-09B stores that exact
selection in the immutable accepted discovery observation before resolving
modules, so retries recover the accepted scan membership even when the catalog
delta is empty. Catalog acceptance/observation
is committed separately; module event sets commit independently per owner and
catalog proposal refresh uses its own global transaction. Crash fixtures cover
before acceptance and after/between module commits. No enrollment cursor,
batch lease or global completion record was added. Catalog/discovery tests and
full repository gates pass. WP-09 persistence is local fixture evidence only;
there was no live write or workflow run.

**Offline repository gates (2026-10-09):** `go mod verify`,
`go test ./... -count=1`, `go vet ./...`, and
`go run ./cmd/factory --list-dimensions` passed after WP-04. The repository
gofmt check and `git diff --check` also passed. These local gates do not
establish GATE-001, live validation, workflow activation, or product acceptance.

### Bounded Agent Work Packets

Each row is one fresh bounded assignment using the caller's active model. Parent tasks complete only when all
their packets and acceptance evidence pass. Dependencies reference packet IDs;
TASK/HUMAN gates above still apply. Packet IDs are local to this plan.

| Packet | Parent | Dependencies | Owned Slice And Required Handoff |
| --- | --- | --- | --- |
| WP-01 | TASK-001 | HUMAN-001 | Read-only producer/API/credential inventory; complete classified search checkpoint. |
| WP-02 | TASK-001A, TASK-001B | WP-01 | State layout proof and owner matrix; exact existing head/path evidence and schema decisions. |
| WP-03 | TASK-002 | WP-02 | internal/identity and state receipt schema/path codec fixtures; no coordinator behavior yet. |
| WP-04 | TASK-002 | WP-03 | internal/state coordinator complete historical proof and immutable replay-result validation. |
| WP-05 | TASK-002A | WP-04 | Discovery sku-finder fixture and replay/conflict tests; no live operation. |
| WP-06 | TASK-003 | WP-04 | Proven discovery-work control record or verified reuse, validators and fence tests. |
| WP-07 | TASK-008 | WP-02 | Neutral internal/projection desired/handoff/acknowledgement contract and schema fixtures. |
| WP-08 | TASK-008A | WP-04, WP-07 | internal/queue barrier evaluator and atomic timeout-release claim tests. |
| WP-09A | TASK-004 / Catalog Contract | WP-06 | Immutable module-version enrollment membership: current-release baseline and later stable release deltas, deterministic identity, provenance, and validation in internal/catalog. This child does not claim persistence or recovery. |
| WP-09B | TASK-004 / Discovery Persistence | WP-09A | Persist catalog observation/outbox separately from immutable module-version membership in internal/discovery; prove crash recovery across accepted membership and empty later release deltas without module completion cursor. |
| WP-09C | TASK-004 / Source Releases | WP-09B | Enumerate bounded stable AVM releases after the durable baseline for each enrolled module, resolve exact immutable commits, and retain every new version since the prior nightly poll; no pre-baseline backfill. |
| WP-10 | TASK-005 | WP-07, WP-09C | internal/discovery per-module/version producer, receipts and exact handoffs. |
| WP-11 | TASK-006 | WP-10 | cmd/factory Discovery adapter: remove board delivery, retain state intents. |
| WP-12 | TASK-006 | WP-11 | Discovery workflow changes and neighboring workflow-contract tests only. |
| WP-13 | TASK-007 | WP-10, WP-12 | Discovery first/middle/last failure, replay, force and ownership fixtures. |
| WP-14 | TASK-009 | WP-08 | Dispatcher claim/release intent and acknowledgement integration. |
| WP-15 | TASK-009 | WP-14 | Acquire-source trusted completion handoff; compute credential-isolation tests. |
| WP-16 | TASK-009 | WP-15 | Assessment trusted completion handoff with context-specific input tests. |
| WP-17 | TASK-009 | WP-16 | Resource intelligence collectors consume accepted module facts and provider stubs, then write shared, provider-owned enrichment under `data/providers/`, keyed by exact provider address/version/resource type. Pin source-specific changelogs, GitHub releases, resource issues, and Registry upgrade guidance; issue observation version is not affected-version proof. Deduplicate shared resource evidence across module consumers. Persist a provider-root proposal/receipt and require human acceptance before advancing that provider snapshot generation. Only exact accepted snapshot generations may wake assessment. Never fabricate accepted facts or write module-owned copies of provider intelligence. |
| WP-18 | TASK-009 | WP-17 | Acceptance intent production without bypassing reviewed evidence boundaries; persist the complete provider-track set derived from exact accepted module facts and reviewed track policy, including deferred contexts. |
| WP-19A | TASK-009A / Lifecycle Slot | WP-18 | Add durable per-module AVM-version slot authority and queued-context reduction. Acquire before first stage; retain through all worker/acceptance/selection/publication stages; unknown deployment holds the slot. WP-21 adds exact receipt release. |
| WP-19 | TASK-009 | WP-19A, WP-18 | Select each accepted provider-track result only after its exact assessment acceptance acknowledgement; reduce status from the complete durable required-track set, retain explicit partial coverage, and write a receipt-backed selection-to-publication handoff per released AVM version. |
| WP-20 | TASK-009 | WP-19 | Keep module/version and provider publication receipts/outboxes independent. During the site publication cycle, queue only eligible assessment requests from exact accepted inputs and reduce already accepted results; assemble both data roots from one exact accepted commit, then write one immutable combined-site publication receipt and replayable fan-out. The site cycle does not dispatch workers or wait for new assessments; worker execution and acceptance remain separate gated stages. |
| WP-21 | TASK-009 | WP-20 | Verify one combined Pages deployment against exact module-version membership and issue independent module acknowledgements. For active modules, release only when every required eligible context is selected and included. For a catalog-retired module with only deselections, release and project neutral `retired` only when the exact accepted facts/context receipts prove a complete required set and exact current null-selection receipts cover every eligible context in that Pages membership; do not claim assessment completion. Unresolved or absent contexts keep the slot active. Never let separate root deployments replace the same site. |
| WP-22 | TASK-009A | WP-21 | Cross-stage slot/claim exclusion, active/unknown run recovery, exact publication release and duplicate-module batch tests. |
| WP-23 | TASK-011 | WP-21 | Neutral projector claims, latest-status selection and API-budget batch admission. |
| WP-24 | TASK-011A | WP-23 | Projector owner/in-flight recovery and delayed-write fixtures. |
| WP-25 | TASK-012 | WP-24 | Issue create/update/owned-label reconciliation and exact effect proof. |
| WP-26 | TASK-012 | WP-25 | Project item/field reconciliation and partial-effect recovery. |
| WP-27 | TASK-012 | WP-26 | Evidence-comment effects, exact projection_done and supersession tests. |
| WP-28 | TASK-013 | WP-27 | Scheduled central workflow remains hard-disabled in the mergeable PR; contract test proves merge alone cannot activate job. |
| WP-29 | TASK-014 | WP-28 | State-only manual recovery/flush requests and scheduled retry fixtures. |
| WP-30 | TASK-010 | WP-12, WP-21, WP-29 | Non-projector credentials/API retirement and exhaustive boundary tests. |
| WP-31 | TASK-015 | WP-30 | Final obsolete adapter cleanup and one-owner search proof. |
| WP-32 | TASK-015 | WP-31 | Operations/lifecycle/approved diagram documentation and applicable instruction updates. |
| WP-33 | TASK-016 | WP-13, WP-22, WP-32 | Cross-module, outage/barrier, catch-up and global fan-out integration fixtures. |
| WP-34 | TASK-017 | WP-33 | Offline required gates; exact command/results checkpoint and no live writes. |
| WP-35 | TASK-018 | WP-34 | Independent read-only contract review; each blocker returns as a separate fix packet. |
| WP-36 | TASK-019 | WP-35, HUMAN-002 | Named cutover/drain evidence within authorized envelope; no blanket pause/reset. |
| WP-37 | TASK-020 | WP-36 | Autonomous ready PR, checks, compliant merge and local main consolidation. |
| WP-38 | TASK-023 | WP-37, HUMAN-002 | Exact fenced UTL handoff, then separately terminalize historical continuation. |
| WP-39 | TASK-024 | WP-38, HUMAN-002 | Bounded original-effect projection reconciliation; no worker dispatch. |
| WP-40 | TASK-021 | WP-39, HUMAN-002 | Approved exact live validation and replay proof, separated from fixtures. |
| WP-41 | TASK-022 | WP-40, HUMAN-002 | Approved schedule/family activation and observed outcomes; no product-acceptance claim. |

**WP-19/WP-21 release-membership checkpoint (2026-10-10):** Selection state
pointers and sequence counters are keyed by `(module, AVM release, provider
context)`. Index history/current selections, dataset membership, publication
manifest validation, and deployment completion proof carry that same release
identity. Dataset manifest schema/identity is version 3. Fixtures prove two AVM
releases using the same provider context remain separate and that a deployment
cannot satisfy another release's completion check. Focused index, publish,
state, and CLI tests pass locally. This resolves only exact release identity;
WP-19 still lacks the complete lifecycle reducer and site-cycle caller, WP-20
combined receipt/fan-out remains open, and WP-21 acknowledgements/slot release
remain open. No workflow or live operation occurred.

**WP-21 module fan-out checkpoint (2026-10-10):** State now has a module-owned,
operation-receipt-backed acknowledgement for one verified Pages generation and
AVM release. It checks exact current selection and assessment membership, then
commits the acknowledgement, lifecycle-slot release proof, and partial/Done
projection intent in one module transaction. Done verification requires the
acknowledgement and released slot. State fixtures cover replay and reject
cross-release proof. The Pages workflow and combined publication receipt still
do not invoke this API; WP-20 and workflow integration remain open. No workflow
or live operation occurred.

**WP-35 independent review finding (2026-10-11):** Read-only review of merge
commit `ec2b93bd` found module fan-out released the lifecycle slot after any
verified membership, even when its module projection was partial. WP-21 now
requires complete required-context selection and exact Pages membership before
release; partial acknowledgements keep the slot active. Regression fixtures
cover an explicitly unresolved context, ineligible-context exclusion, a
selected context omitted from Pages membership, newer-version backlog, and
release after a later complete deployment. Focused state tests pass locally.
Independent post-fix review and repository gates are pending; no live workflow
or state operation occurred.

**WP-20 separate-root digest checkpoint (2026-10-10):** The indexed combined
publication manifest now carries separate digests for `data/modules/` and
`data/providers/`, alongside the full data-tree digest, exact accepted commit,
dataset ID and release-scoped membership. State deployment plans, generations,
and callbacks preserve both root digests and reject a same-run replay that
changes either digest. A missing root has a canonical empty-tree digest for
partial publication. The manual Pages workflow now prepares that durable
generation, verifies the exact artifact after transfer, authorizes it behind
the data-publisher environment, and deploys once behind github-pages. The pinned
`actions/deploy-pages` action does not expose a `deployment_id` output; its
build version is `GITHUB_SHA`, and the Pages status API accepts that exact
commit SHA as the deployment key. The receipt importer requires the key to
match the manifest's accepted commit before invoking WP-21 fan-out.
Authorization compares the artifact's dataset and all digests to the prepared
state generation.
Module/version acknowledgements that are not current are deferred while other
fan-out targets continue. Focused publish/state/CLI/workflow-contract tests
pass locally. WP-17 exact-input assessment wake-up and WP-19 site-cycle
selection remain open, so assessment planning is not yet run before publication.
No workflow or live operation occurred.

**WP-17 accepted-input site-cycle checkpoint (2026-10-10):** Site preparation
now derives requests from exact accepted data and state: active catalog
membership, operation-proven facts acceptance and required-context receipts,
the current release/source pin, exact accepted facts, and imported provider
snapshot/policy receipts. It persists eligible requests through the durable
wake operation; deferred inputs do not become assessment requests, and this
path does not dispatch. Provider snapshot import also imports the reviewed
policy identity for the same exact targets. Focused package, race, and
workflow-contract tests pass, and state fixtures prove the exact two input
receipts for one release-scoped context. The earlier positive-fixture gap was
closed on 2026-10-11: a site-cycle fixture now runs planning through the durable
wake caller, verifies only the exact eligible request is persisted, and checks
the deferred context remains in its immutable receipt without dispatch. A
separate two-consumer fixture loads distinct modules' exact facts against one
provider/version snapshot and proves identical source evidence converges to the
same one-copy provider stub snapshot. Provider-root acceptance provenance
remains open for contract review. No workflow or live state write occurred.

**WP-19 request-aware projection checkpoint (2026-10-10):** Selection
projection now reduces every eligible context in the exact accepted
module/version context set against current release-scoped requests. A queued
context remains `backlog`; active, awaiting-review, failed, and unresolved
contexts retain their states while selected/required/unresolved counts remain
explicit. A fixture proves selecting one context cannot hide another queued
context. Focused state, queue, and projection tests pass. Cross-release
lifecycle reduction, selection invocation from the site cycle, and deployment
acknowledgement integration remain open. No workflow or live state write
occurred.

**WP-19 site-cycle selection checkpoint (2026-10-10):** State now enumerates
only current accepted assessments with exact operation/write-set proof and a
valid acceptance projection acknowledgement or durable timeout release. Site
preparation invokes `SelectAccepted` with its exact workflow provenance; the
selection transaction revalidates request, generation, lifecycle slot, inputs,
source pin, and predecessor handoff. Later site cycles reuse the current
selection receipt. Focused and race tests pass. This creates a state outbox and
selection-to-publication handoff, but no trusted path yet imports that outbox
into reviewed `data/modules/` history. WP-19 still lacks cross-release
reduction, and WP-20 must establish the selection export/acceptance boundary
before the combined site can publish newly selected results. No workflow was
dispatched or live state written.

**WP-19 release-scoped proposal checkpoint (2026-10-10):** Selection proposal
subjects, receipt checks, current-pointer lookup, sequence validation, and track
target vectors now preserve the exact AVM release alongside provider context.
A fixture proves that two releases can each have sequence 1 and remain current
without replacing one another. Review also closed a stale-base validation gap:
module and track histories must preserve all existing entries and append exactly
one receipt even when the proposal base has changed. Fixtures cover independent
release sequences and rejection of dropped track history. Focused index,
publish, state, and CLI tests pass. No trusted state-outbox exporter or reviewed
selection-export workflow exists yet, so selection acceptance and publication
remain incomplete.

**WP-19/WP-20 selection export workflow checkpoint (2026-10-11):** The
state-backed CLI now turns one exact pending selection outbox into a proposal
against the accepted data tree, including rebuilt module, track, and dataset
indexes. The manual-only workflow verifies its exact base and state checkout,
stages only declared canonical data, reapplies the proposal, and prepares a
review branch. The next gated site cycle reconciles a merged selection only
when the exact accepted receipt matches its durable state outbox. Focused
publish/CLI/workflow tests pass; no live workflow or state write occurred.
Cross-release lifecycle reduction and remaining WP-17/WP-20/WP-21 integration
and review gates remain open.

**WP-19/WP-20 site-cycle caller correction (2026-10-11):** The current local
`prepareIndexedSitePublication` path imports accepted selection exports,
plans requests only from exact accepted module/provider inputs at the supplied
commit, and applies receipt-backed accepted results into durable selection
outboxes before preparing the combined publication generation. This supersedes
the earlier statement that the site-cycle caller was absent. Complete WP-19
cross-stage coverage, provider collector provenance, required gates, and live
operations remain open; no workflow ran.

**WP-17 pre-clarification review (2026-10-09; superseded 2026-10-11):** The
then-current `internal/queue/collector.go` validated an acquire-source handoff, accepted
facts identity, request/attempt receipt proof, fence, and projection barrier.
It does not implement a collector or define its producer, exact accepted
provider/policy inputs, output artifact/identity, or state transition. The
provider catalog collector is metadata discovery and is not a substitute for
this module stage. Provider and policy snapshots remain independently collected
and human-accepted shared inputs; assessment may only be queued for the exact
accepted context generation and snapshot set. Therefore WP-17's typed consumer
tests are partial contract evidence. Do not collect module facts, fabricate an
accepted-facts record, or enqueue assessment until the owning design specifies
the collector's bounded inputs, output, acceptance boundary, and ownership.
This blocked WP-17 at that checkpoint. Ledermayer's clarification specifies
provider-owned collectors over accepted facts and provider stubs, with shared
provider-root proposals and human acceptance. The module-queue validator was
removed on 2026-10-11 because that collection path is independent of module
assessment slot claims. Bounded source schemas, durable provider acceptance,
and exact accepted-input wake-up remain implementation work.

**WP-18 acceptance replay correction (2026-10-09):** Review found that a retry
with a new trusted workflow run or acceptance timestamp conflicted with the
original evidence acceptance, even though run provenance and caller time do not
define its logical identity. Replay now validates the same accepted evidence
identity and write-set proof, then returns the original immutable receipt and
handoff (including original producer run provenance). Facts acceptance still
does not fabricate assessment inputs, and assessment acceptance does not select
analysis. Focused state/queue/projection tests and the state/queue race suites
pass. Replay now validates all retry producer fields before returning the
original immutable run provenance. This is local evidence only. No workflow was
run or live state changed.

**WP-09C/WP-19A implementation checkpoint (2026-10-09):**
`source.Resolver.ResolveStableReleasesAfter` now verifies the durable baseline,
enumerates all newer stable releases within the existing pagination bound, and
resolves each release to an immutable source commit. Discovery now calls this
resolver after the first current-release baseline and persists each observed
release pin, its version-specific request, exact handoff and operation receipt
in the module transaction. A fixture proves that two releases between polls
are retained; focused discovery/source/queue tests and the full Go suite pass at
this checkpoint. A crash fixture now commits a multi-release batch for one
module, interrupts before the next, and recovers every active module on an empty
catalog delta. This still depends on upstream tags remaining discoverable; the
independent immutable release-enrollment membership required by WP-09B is not
yet present. Queue claims acquire a module-v2 lifecycle slot in the same
transaction as request/attempt claims. Later stages require the held AVM
release; a newer accepted generation for that same release advances the slot
atomically with its claim, while older generations and newer releases remain
blocked. The release-proof type requires the deployment receipt, exact
membership digest, and verification time, but WP-21 has not implemented the
release transition. WP-17 remains partial pending
source-specific collectors and accepted-snapshot wake-up; assessment release
propagation, selection, publication, and Pages receipt fan-out remain open.
Queued-release FIFO ordering is now locally enforced. No schedule was activated,
no live state or source was accessed, and the
implementation is not yet workflow-testable.

**Queue release-identity correction (2026-10-09):** Review found that AVM
release version was not included in request fingerprints and queue handoff lookup
matched only module, generation and stage. Both are now exact release-context
identities: acquisition and assessment fingerprints include the AVM release,
and handoff lookup compares the wrapped exact request fingerprint plus accepted
facts ID when present. A fixture with two same-module releases sharing
generation 1 verifies the correct handoff is selected. Focused queue tests pass.

**WP-19 version identity checkpoint (2026-10-09):** Assessment wake planning
now takes and propagates the exact held AVM release. Selection receipt schema
v3 preserves that version; published indexes keep receipt IDs that resolve to
the versioned receipt. Selection checks the active slot and matching release/
source pin instead of rejecting a valid older release because a newer module
pin exists. Schema, queue, state, index, and publication tests pass. This does
not complete WP-19: the required-context reducer, predecessor acknowledgement,
and receipt-backed selection handoff remain open.

**WP-19A FIFO correction (2026-10-09):** Before a module has an active slot,
claim eligibility now prevents a newer queued AVM release from starting while
an older release for that module remains queued, including during the older
request's retry delay. A released slot also rejects older releases after
publication while allowing a later release or a new generation for the same
release. Focused release-order, released-slot, queue race, and downstream state,
projection, publication, and index tests pass locally. This is not a live queue
observation; selection and publication handoffs remain incomplete.

**WP-19 pre-clarification review (2026-10-09):** `SelectAccepted` identifies one
context, but `queue.ReduceModuleStatus` requires the authoritative reviewed set
of required contexts to derive a module status. Catalog records currently carry
no context policy, and there is no accepted required-context input in the
selection operation. A single-context `publishing` intent could erase another
context's blocked, review, or active state. The selection API also has no
selection-stage producer provenance for a handoff; assessment-acceptance
provenance cannot be reused as selection provenance. WP-19 remains incomplete
until the required-context source, aggregation behavior, and producer/handoff
contract are defined. No partial single-context intent is retained.

**Lifecycle clarification from Ledermayer (2026-10-09):** A lifecycle context
is a released semantic version of one AVM module. The initial enrollment takes
the currently released version only; it does not backfill earlier releases.
Nightly discovery enqueues later released versions as separate contexts. A
module has one active assessment slot across its versions; a newly discovered
version waits in Backlog until the prior active assessment finishes. Assessment
and selection history remain version-scoped and append-only. Reprocessing the
same AVM version after an accepted provider-intelligence input changes advances
that context's desired generation and preserves earlier analysis/selection
receipts. The module-level projection reducer uses current desired version plus
any older active version, and must not erase either state.

**Follow-up decisions from Ledermayer (2026-10-09):** The module's one active
version slot remains held until the exact verified publication receipt covers
that version; releasing a worker claim does not release the lifecycle slot.
When an accepted provider-intelligence snapshot changes for resources used by
an already assessed AVM release, enqueue a new desired generation for that same
release context and preserve all earlier analysis and selection receipts.
Neither decision permits assessment against unaccepted collector output.

**Context and publication clarification from Ledermayer (2026-10-09):** The
assessment lifecycle identity is the released AVM module version. Provider
tracks remain distinct assessment contexts and their results remain separately
versioned; the module-wide slot serializes their lifecycle work against other
AVM releases of that module. The initial seed uses each module's current stable
release only, and nightly discovery retains every later stable release without
overwriting older version history. This confirms the product behavior but does
not supply the missing durable required-context set: deferred/inapplicable
contexts are currently omitted from assessment requests, so selection still
cannot safely derive a complete module projection from the request set alone.

**WP-19 acceptance-barrier checkpoint (2026-10-10):** `SelectAccepted` now
requires the exact assessment acceptance handoff ID, validates the operation
receipt and its write-set proof, checks accepted facts/generation/fingerprint,
and requires that exact projection acknowledgement or its durable timeout
release. Fixtures reject wrong or missing predecessor proof and accept an exact
timeout release. The focused selection/deployment state tests pass. WP-19 still
needs the durable complete required-context set, module-wide reducer, selection
producer receipt, and receipt-backed selection-to-publication handoff. No
workflow or live state operation occurred.

**WP-17/WP-18 contract review (2026-10-10):** The provider collection workflow
stages proposals on a reviewed data branch, but no durable provider acceptance
receipt or exact shared-snapshot generation wake follows the merge. Also,
`config/providers.yaml` is collection policy with configured `track_majors`.
`provider.DeriveRequiredAssessmentContexts` now defines these as independent
provider-major contexts, binds a deterministic set to exact module/source/AVM
release/facts/policy identities, and preserves missing/empty policy or partial
facts as unresolved contexts rather than omitting them. It does not create
Cartesian provider-version vectors. The trusted acceptance command derives the
set from the exact accepted facts tree and reviewed policy and persists it in
the immutable facts-acceptance receipt with the exact AVM release. Focused
state/provider/CLI and workflow-contract tests pass. WP-17 accepted-provider receipt/wake remains open; no live
acceptance or wake was performed.

**WP-19 context-selection checkpoint (2026-10-10):** `SelectAccepted` now
proves the exact facts-acceptance receipt through its operation receipt and
write-set history, and only selects a context marked applicable and eligible
in that durable set. It rejects unresolved and unlisted contexts. Selection
commits its immutable receipt/export, a publishing projection intent carrying
selected/required/unresolved counts, a provenance-bearing publication handoff,
and an operation receipt atomically. Slot generation must cover the selected
request. Focused state, queue, projection and deployment tests pass. This is
partial WP-19 implementation: aggregation still omits other durable request
and active-stage states and deployment acknowledgements, and there is no
integrated site-publication caller. No workflow or live state operation
occurred.

**WP-17 version-scoped invalidation checkpoint (2026-10-10):** State input
imports, module generation records, request high-water scans, replay identity,
and selection validation now key by exact AVM release and provider context.
Before this change, a provider acceptance for one release could invalidate the
same track on another release. Focused state/queue tests, including a distinct
release fixture, the full Go suite, and state/queue/projection race tests pass.
This does not implement provider acceptance receipts,
accepted-tree binding, or assessment wake-up.

**WP-18 acceptance persistence checkpoint (2026-10-10):** The trusted evidence
acceptance command loads facts by exact identity from the verified merged data
tree, derives the deterministic provider-major context set using the reviewed
policy, and stores it in the immutable acceptance receipt with the exact AVM
release. Receipt validation rejects a set rebound to another release or facts
identity; operation replay preserves its original set. Focused tests and
workflow-contract tests pass. Selection reduction remains WP-19 work. No
workflow dispatch or live acceptance occurred.

**WP-17 version-scoped invalidation checkpoint (2026-10-10):** State input
imports, module generation records, request high-water scans, replay identity,
and selection validation now key by exact AVM release and provider context.
Before this change, a provider acceptance for one release could invalidate the
same track on another release. Focused state/queue tests, including a distinct
release fixture, pass. This does not implement provider acceptance receipts,
accepted-tree binding, or assessment wake-up.

**WP-19 selection-history verification checkpoint (2026-10-10):** Selection
coverage now propagates corrupt prior pointer/receipt errors instead of
silently treating them as an unselected context. Non-null prior selection
receipts also require their exact historical applied event digest before
counting toward the accepted required-context set. A fixture with a valid
content identity but no applied event fails closed. Focused state/queue/
projection tests pass. This does not aggregate other current requests or active
stages, invoke site-cycle selection, or acknowledge Pages deployment; WP-19
remains incomplete.

**Publication direction confirmed by Ledermayer (2026-10-10):** Provider
snapshots under `data/providers/` and AVM module/version analysis under
`data/modules/` are accepted and versioned independently. The first assessment
cycle is not part of factory discovery; assessment runs with the site
publication operation after its exact inputs have been accepted. One site
assembler reads both roots from one exact accepted commit, builds the combined
indexes, and produces one Pages artifact and deployment receipt for that exact
membership. Separate deployments of the same Pages site are not allowed because
an older artifact could replace a newer combined view. The first baseline uses
only each active AVM module's current stable release; it does not backfill old
releases. The site may publish partial coverage while provider contexts are
deferred, but module-wide status must come from a durable complete required-track
set and preserve each context result. Unknown or missing tracks cannot be
treated as not applicable or complete. Keep module/provider acceptance receipts,
histories, and data roots independent while the site builder combines them.
Exact root digests, per-version membership, and deployment proof remain
required.

**Implementation follow-up (2026-10-09):** Source review confirms current
`moduleHasActiveClaim` excludes only requests in `claimed`/`running`; it cannot
hold ownership across evidence review, selection export, publication and Pages.
`SelectAccepted` also does not consume the exact acceptance handoff/acknowledgement
or produce a selection-stage handoff. Added WP-19A as the durable module-version
slot/reducer foundation. WP-19 must build on that authority, and WP-21 releases
the slot only through exact module-owned deployment membership. The conceptual
decisions are approved; this is remaining implementation work, not a request for
another policy decision.

Resource-intelligence collection starts from the resources/provider versions
recorded in acquisition facts and the corresponding provider stubs. Independent
collectors cover provider changelog files, GitHub provider release notes,
resource-specific provider issues, and Terraform Registry upgrade guidance.
Collection results are source-scoped, version-pinned evidence; they are
proposed into provider stubs and only become assessment inputs after the
existing acceptance boundary advances the exact shared snapshot generation.
Assessment is queued only against those accepted snapshot IDs and the exact
AVM-version context generation. The collectors do not mutate module facts or
assert acceptance themselves. This clarifies the intended WP-17 producer and
acceptance boundary. WP-19 still needs a durable required-context set and
selection provenance/handoff. The publication model is now confirmed in the
2026-10-10 direction above; WP-20/WP-21 must implement independent data-root
receipts with one combined exact-head site build and deployment proof.

**WP-17 proposal-boundary checkpoint (2026-10-09):** The typed provider
proposal now requires the reviewed provider collection policy and accepted
facts/provider snapshot reference identities and validates acquisition-facts
content. At this checkpoint its pure constructor did not prove those references
were present in the accepted data tree or that snapshot stub digests matched
accepted records. The 2026-10-10 accepted-input verification checkpoint below
supersedes that gap. It preserves the configured
source repository in the proposal, restricts GitHub source URLs to that
repository and exact release tags for changelog/release artifacts, restricts
Registry URLs to the provider address, and retains distinct changelog files
while collapsing exact duplicates. Focused provider tests pass. This closes the
source-linkage proposal-contract slice only; WP-17 still lacks source-specific
network handlers, durable proposal receipt/write, human acceptance into
provider stubs, and an accepted-snapshot assessment wake. No collection or
acceptance occurred.

The referenced avm-metadata POC matches provider issues to resource names but
does not establish which provider version an issue affects. WP-17 must preserve
issue evidence and explicit version references without converting the provider
version at collection time into an affected-version assertion. The proposal
contract now requires an explicit version basis: exact release tag for GitHub
release/changelog sources, exact Registry version path, and provider snapshot
version for issues observed at that time. Issue observation is not proof of
version impact. Focused provider fixtures pass; collection handlers, trusted
accepted-record lookup and the human-acceptance write path remain open.

**WP-17 proposal workflow checkpoint (2026-10-10):** The new
`manual-provider-resource-intelligence` command/workflow consumes an exact
accepted module facts identity and one indexed provider snapshot from the exact
current main base, then stages any new evidence as an enriched provider-root
snapshot proposal. Its artifact is accepted by the existing reviewed provider
export path. The workflow is manual, environment-gated, read-only on accepted
data/state, and validates a temporary candidate before uploading. Empty source
results produce no proposal. Focused CLI/provider/workflow-contract tests and
shell parsing pass locally; the workflow was not dispatched. WP-17 remains
incomplete pending durable provider acceptance provenance and receipt, exact
accepted-generation assessment wake-up, and shared-consumer/deduplication
integration. No live source reads, acceptance, state writes, or deployment
occurred.

**WP-17 accepted-provider import checkpoint (2026-10-10):** The CLI now loads
one exact provider snapshot from the validated accepted provider index, then
derives affected contexts from durable facts-acceptance receipts whose complete
operation write sets still verify. Targets are limited to the exact provider
address and stable major and retain the AVM release dimension. One state CAS
records the snapshot ID, accepted commit, trusted import workflow provenance,
target generations, module event write set, and replay receipt. The new
`manual-import-provider-snapshot` workflow accepts only an exact current-main
commit and writes factory state behind the existing publisher environment.
Focused state/provider/CLI/workflow-contract tests pass locally; no workflow or
state write occurred. This binds provider acceptance to durable input
invalidation, but it does not create assessment requests. WP-17 remains open
until the site-cycle planner consumes exact accepted context generations and
snapshot vectors to queue only eligible work while preserving deferred tracks.

**WP-17 policy identity correction (2026-10-10):** Required context sets carry
the reviewed provider-track policy as a `provider-policy-*` identity, distinct
from compiled assessment dimension `definition-*` IDs. State input validation
and selection now preserve that distinction and bind imported policy state to
the exact accepted facts receipt's context set. Focused state tests pass. This
does not supply accepted policy provenance or the site-cycle assessment wake;
those remain open WP-17 contracts.

**WP-17 durable wake slice (2026-10-10):** `queue.EnqueueAssessmentWake` now
stores a prepared module wake as queued requests with an immutable operation
receipt. Deferred contexts remain in the receipt result, and replay returns the
original result after a request record advances without restoring the old
payload. Focused queue tests pass. No accepted state/data planner or site-cycle
caller exists yet; WP-17 remains incomplete.

**Nightly AVM release scope correction (2026-10-09):** The scheduled discovery
workflow previously passed `--discovery-new-modules-only`, which excluded new
releases from already catalogued modules. It now uses the default immutable
`factory-all-active` selection, checks every active module ID nightly, and
retains the exact manual-selector behavior. Fixtures prove existing modules
remain selected after a catalog addition and a new release is enqueued for
known modules when the catalog delta is empty; workflow-contract tests verify the
scheduled job does not narrow to new IDs, project board state, or dispatch
workers. This is local fixture evidence only. WP-09B still needs crash/replay
proof for discovered release membership across partial module transactions and
empty catalog deltas. No workflow or live operation occurred.

**WP-17 bounded changelog path correction (2026-10-11):** WP-35 review found
the provider resource-intelligence collector enumerated only the repository
root, which could silently omit nested split changelogs. The collector now
enumerates the recursive Git tree at the exact release tag, selects normalized
`CHANGELOG*.md`, `.markdown`, and `.txt` blob paths, and fails closed on a
truncated tree. Existing per-request, total-byte, request-count, and wall-time
bounds apply. Fixtures cover root plus nested split files, invalid paths,
missing/null/malformed envelopes, absent truncation status, an explicit empty
tree, and truncated-tree rejection. This closes filename-based nested changelog
discovery only. The receipt-backed provider import and exact site-cycle wake
have fixture coverage in the WP-17 checkpoints above; final cross-stage review,
workflow validation, and live collection remain open. No live source read or
collection occurred.

**WP-20 site-cycle execution clarification (2026-10-11):** The site cycle
imports accepted selection exports, queues eligible requests only from exact
accepted facts/provider/policy receipts, reduces already accepted results, and
publishes the current exact accepted shared data head. It does not dispatch
workers or wait for new assessment completion. Fresh results require the
separately gated worker and acceptance stages and appear in a later site cycle.
This matches `prepareIndexedSitePublication` and preserves the no-dispatch
boundary; it is not live workflow evidence.

**WP-23 active-slot status correction (2026-10-11):** The discovery producer now
reduces status from the AVM release held by the active module slot, using the
exact slot request before facts acceptance and the receipt-backed required
context set afterward. A newer queued release therefore cannot replace the
active release's status/source in the module handoff. Queue and discovery
fixtures pass locally. This is a narrow producer-side correction only; the
neutral-intent delivery bridge, shared current-status reduction across all
producers, effect-cost admission, and batch/cursor retirement remain open. No
workflow or live projection occurred.

**WP-23 module generation-order correction (2026-10-11):** The handoff now
separates release/context work generation from module-wide neutral projection
identity. Identified producers preserve the current module generation, and
trusted worker completion receipts replay their immutable original event set
after later neutral intents advance. Focused fixtures cover the older-active
release case and replay. Scheduled neutral-intent delivery remains open; do not
mark WP-23 or the full scope complete from these local fixtures. See F-040.

## 3. Alternatives

- **ALT-001**: Keep direct board delivery inside Discovery. Rejected: Discovery writes projection intent, but only the projector performs remote effects.
- **ALT-002**: Retain the batch continuation as completion authority. Rejected because one module failure couples unrelated modules.
- **ALT-003**: Use Issue labels or Project columns as the queue. Rejected because board state is human-editable and non-authoritative.
- **ALT-004**: Let each stage update the board directly if projection is lagging. Rejected because it creates competing writers and ambiguous acknowledgement.
- **ALT-005**: Retry an uncertain remote write under a new projection identity. Rejected because the first write may have succeeded.
- **ALT-006**: Force an existing data event ID to equal the operation ID. Rejected because it changes that event digest and breaks replay compatibility.
- **ALT-007**: Add a second per-module status or discovery-specific claim beside the existing request/attempt claim. Rejected because a module must have one current status and one active job.
- **ALT-008**: Require board acknowledgement forever. Rejected because an outage must not indefinitely block canonical progress; automatic timed release is explicitly separate from projection success.
- **ALT-009**: Replay every historical board status after outage. Rejected in favor of latest-status catch-up with independent evidence-effect retention.
- **ALT-010**: Depend only on producer-completion events for projection retry. Rejected because pending work needs scheduled reconciliation without another producer event.

## 4. Dependencies

- **DEP-001**: Merged Git-only transport on protected `main` (`8e0275e2` at plan creation); `factory-state` remains durable authority.
- **DEP-002**: Existing `DecisionCoordinator`, verified publisher, and owner-scoped aggregate metadata. Generic receipt verification is a planned coordinator change, not a resolver convention.
- **DEP-003**: Existing module-v2 logical/physical contract and `queue.ReduceModuleStatus`. New records require the TASK-001A layout proof and explicit schema updates.
- **DEP-004**: Existing request, attempt, and `moduleHasActiveClaim` behavior. This plan extends that exclusion rule to every stage rather than replacing it.
- **DEP-005**: Existing projection delivery, binding, budget, and pacing records. Desired intent moves out of the GitHub client dependency boundary; delivery state does not move into producer packages.
- **DEP-006**: Existing data-publisher environment and separate state/board grants. Verify effective scopes at TASK-019/021/022 within HUMAN-002's authorized envelope; do not alter protection settings.
- **DEP-007**: GitHub Actions schedule semantics on the default branch, serialized projector execution and any actual run-observation API grants. Valid actions: read permission applies where those APIs are used; workflows: read is not an Actions permission. Schedule delay is expected, not a queue identity change.
- **DEP-008**: Availability of an implementation agent for bounded work packets. Agent availability and incompatible enforced approvals are execution constraints; preserve the approved scope and do not bypass policy.

## 5. Files

- **FILE-001**: `internal/discovery/cycle.go`, `transaction.go`, `continuation.go`; remove direct board delivery and batch-cursor authority; add independent queue/projection intent writes and catalog enrollment recovery.
- **FILE-002**: `internal/identity/layout.go`, `internal/state/store.go`, `layout.go`, `transaction.go`; proven logical/physical paths, receipt validation, and coordinator write-set verification.
- **FILE-003**: New `internal/projection/` neutral desired/handoff/acknowledgement contract and tests, with no GitHub API dependency; `internal/queue/` exact barrier and automatic release evaluation.
- **FILE-004**: `internal/queue/`, worker, collector, acceptance, selection, publication, and Pages producers; each approved stage writes its next-stage record and projection intent.
- **FILE-005**: `internal/github/projection_state.go`, `client.go`, `project_items.go`, `run_evidence.go`; one projector claim, readback, and acknowledgement path.
- **FILE-006**: `cmd/factory/discovery_cycle.go`, `discovery_projection.go`, `main.go`, `native.go`; remove direct discovery delivery commands, retain intent producers and state-only controls.
- **FILE-007**: `.github/workflows/factory-projection.yml`, producer/manual recovery workflows and contract tests; five-minute schedule, serialized budgeted polling, credential-free compute and direct-writer retirement.
- **FILE-008**: `docs/issue-lifecycle.md`, `docs/factory-operations.md`, `docs/diagrams/factory_lifecycle.drawio`, `docs/FINDINGS.md`, `docs/TODOS.md`; update only after approval and implementation evidence.

## 6. Testing

- **TEST-001**: The receipt-bearing sku-finder fixture commits through `DecisionCoordinator`, preserves every pre-existing data-event ID and digest, and replays its result without a duplicate request or pin. It does not claim the never-committed historical event set was unchanged.
- **TEST-002**: Identical historical operation replay returns its immutable result after later claim/completion/source/intent advancement. Missing historical write-set proof or changed proposed content under the same identity fails closed. Current mutable payload advancement alone is not an identity conflict.
- **TEST-003**: In a multi-module envelope, earlier modules commit once, one module fails, and later modules remain due. The next invocation replays completed work as a no-op and processes only due work. Catalog failure does not rerun a completed module.
- **TEST-004**: Concurrent batches and stages produce one current lifecycle slot per module across AVM release contexts and one stage owner within that slot. A second version cannot start while an earlier one is in review/publication or has unknown deployment. Unrelated modules proceed independently. Stale owners and fences cannot overwrite newer state.
- **TEST-005**: Unknown publication is reconciled from the receipt at the candidate and descendant heads. Unavailable proof remains unknown and allocates no new identity.
- **TEST-006**: Recover immutable module/version enrollment after crashes before/between commits, including baseline and multiple new releases between nightly polls. The initial baseline imports only each module's then-current release; older releases are never backfilled. Empty later release deltas do not lose queued versions or rerun completed work.
- **TEST-007**: Discovery, dispatcher, trusted acquisition/assessment completion, collector and later producers atomically emit expected handoff/projection intents with exact provenance. Preserve required-context reducer precedence and older active/newer desired work. Compute gets no state credentials.
- **TEST-008**: Repository-wide tests identify all Issue/Project mutation methods and token requests. Only the projector can invoke them. State-token use is distinguished from projection-token use.
- **TEST-009**: Exercise 10/50/100 batch ceilings with default 50 and actual admission limited by per-effect API/time/reserve cost. Claim/checkpoint modules independently; park module errors and continue neighbors. Global API waits/integrity/cancellation stop safely. Concurrent invocations cannot own the same module and unfinished work resumes next tick.
- **TEST-010**: Lost responses for Issue creation/update, Project item/field changes, and evidence comments reconcile through their own markers. Human notes and unowned labels remain byte-identical.
- **TEST-011**: Retryable, permanent, rate-limit, protected-review, active-lease, cancellation, and unknown outcomes map respectively to retry time, needs-review, server wait, no-op, wait, and reconcile-before-retry. Logs contain no raw API body or credential.
- **TEST-012**: Workflow tests verify */5 * * * * on reviewed main, isolated verified state, separate credentials, no producer artifact trust, repository-wide non-cancelling concurrency and valid least-privilege permissions. Simulate producer failure after commit and delayed/skipped schedule ticks; later ticks discover pending work without workflow_run fan-in. Reject invalid workflows: read permission.
- **TEST-013**: Existing active, retryable, needs-review, and pending evidence records survive replacement. The projector changes only factory-owned fields and comments.
- **TEST-014**: The approved live test proves state-only discovery and central projection for the exact scope. Local fixtures do not satisfy it.
- **TEST-015**: Uncertain state publication and uncertain remote projection are both reconciled by their original identities before retry.
- **TEST-016**: One module with two due AVM-version contexts acquires one lifecycle slot. Worker completion releases only its stage claim; acceptance, selection, publication, and unknown Pages outcomes retain the slot. Partial Pages membership, including an unresolved required context or a selected context omitted from deployed membership, records partial status and retains the slot. Only a module-owned reference to a verified deployment whose exact membership covers every required eligible context releases it and allows the next version to start. Batches never claim a module twice.
- **TEST-017**: Exact acknowledgement allows a claim; stale module/generation/transition/intent/fingerprint tuples do not. At fifteen minutes, automatic barrier release permits otherwise eligible work once, with unchanged deadline on retries and no fabricated projection_done. Unknown canonical state, active owners and acceptance/policy failures remain blocked.
- **TEST-018**: Simulate board outage while canonical work advances via timeout. Recovery projects latest status only, preserving all required independent evidence effects and human content. Superseded status cannot overwrite latest; uncertain old writes remain reconciled before conflicting effects.
- **TEST-019**: Delay an old projector request across lease expiry/new intent creation. Active/unknown run observations prevent takeover; fencing rejects stale acknowledgement but is never treated as remote fencing. Lost-write-response recovery uses the original effect identity and never guesses absent effect from an unresolved request.
- **TEST-020**: Crash publication/Pages fan-out after one module acknowledgement. Replay verifies the original global receipt, updates only missing module references and does not repeat deployment or mark nonmember modules Done.
- **TEST-023**: Nightly release discovery processes every stable semantic version published after the per-module baseline, including several versions between polls, while excluding all older historical releases and preserving exact release-tag-to-commit pins.
- **TEST-021**: Audit all work-packet dependencies, bounded ownership and required handoff evidence. No discretionary human PR/merge gate exists; existing required checks/reviews/environment rules remain enforced. No activation occurs from partial mixed-writer packets.

## 7. Risks & Assumptions

- **RISK-001**: Confusing operation identity with event identity can corrupt replay. The receipt is a new event, and all pre-existing event digests must be tested before changing the shared coordinator.
- **RISK-002**: A receipt file alone does not satisfy replay. Generic coordinator verification is mandatory because current resolvers can return success from a subset of records.
- **RISK-003**: The live layout may not be the assumed module layout. TASK-001A blocks record design until the head and path codec are proven.
- **RISK-004**: Scheduled invocations can be delayed/skipped. Durable due-state polling recovers on later ticks; backlog age and overdue retries need observable logs, not a claimed five-minute SLA.
- **RISK-005**: A successful remote mutation can lose its acknowledgement. Per-effect markers and readback are mandatory before another side effect.
- **RISK-006**: Rate limits or missing grants leave projection pending, but automatic barrier release prevents indefinite canonical blocking. It does not fix GitHub availability, canonical state failure or missing execution permissions. Invocation success never implies batch completion.
- **RISK-007**: Partial cutover can expose two board writers. Deliver qualified integration atomically and drain old runs. A temporary zero-writer interval is tolerable only within the approved maintenance envelope; durable intents and timeout release preserve recovery, not fabricated acknowledgement.
- **RISK-008**: Introducing discovery-work status beside request status could create two current truths. The request/attempt remains authoritative for the active job; contradictory work state fails closed.
- **RISK-009**: State fencing cannot fence GitHub requests already in flight. Serialize production runs and reconcile unresolved effects before conflicting writes; ambiguity may require an impactful operator decision while canonical work continues after its barrier timeout.
- **RISK-010**: An implementation agent or compliant merge capability may be unavailable. Verify required repository capabilities before execution; do not remove enforced protections to satisfy autonomy.
- **ASSUMPTION-001**: Module ownership can host the approved module records without changing accepted evidence or provider/global ownership, subject to TASK-001A.
- **ASSUMPTION-002**: Approved scheduled projector runs can read durable due state from reviewed main without producer artifacts. Effective grants and environment availability remain verification requirements.
- **ASSUMPTION-003**: Where an existing remote effect lacks a sufficient marker, reconciliation remains unknown or needs-review. The plan does not infer success from a partial match.
- **ASSUMPTION-004**: The failed continuation and queued request remain preserved. They become terminal only through the approved fenced handoff.
- **ASSUMPTION-005**: Five-minute schedule, fifteen-minute barrier timeout and default batch ceiling 50 are proposed conservative defaults in this revision. HUMAN-001 approves them; changes thereafter remain reviewed configuration, not silent retry-time identity changes.

## 8. Related Specifications / Further Reading

- [Lifecycle and state authority](../docs/issue-lifecycle.md)
- [Factory workflow and state transport operations](../docs/factory-operations.md)
- [Current target lifecycle diagram](../docs/diagrams/factory_lifecycle.drawio)
- [Decision records](../docs/decisions.md)
- [State transport implementation plan](refactor-factory-state-transport-1.md)
- [Failed UTL run and exact-continuation resume evidence](../docs/FINDINGS.md)
- [Central task and rollout register](../docs/TODOS.md)

**WP-17/18 lifecycle-boundary correction (2026-10-11):** Facts acceptance
commits its request, immutable acceptance receipt, and projection intent with an
operation receipt, without creating a module-queue `collector` successor. The
temporary typed `queue.ValidateCollectorHandoff` contract was removed because
the approved resource-intelligence collectors are provider-owned and their
proposal/acceptance lifecycle is independent of the module assessment slot.
Assessment acceptance still emits its exact selection handoff. Focused
state/queue/projection tests pass. Provider-root acceptance receipt and exact
accepted-input wake integration, plus a positive accepted-tree planner fixture,
remain open; no live collection, workflow, or state operation occurred.

**Local gate checkpoint (2026-10-11):** `go test ./... -count=1`,
`go vet ./...`, `go mod verify`, `go run ./cmd/factory --list-dimensions`,
the workflow-contract subset, YAML parsing, schema package tests, race tests for
state/queue/projection/discovery/publish/factory CLI, global gofmt check, and
`git diff --check` pass. These fixtures do not establish accepted provider
inputs or live outcomes. Provider-root acceptance provenance, a positive exact
accepted-tree assessment planner fixture, cross-stage lifecycle coverage, and
the remaining projector packets still block PR readiness.

**WP-22 cross-stage slot fixture (2026-10-11):** The deployment lifecycle
test asserts the release slot stays active after selection and exact Pages
receipt import, then releases only with the module-owned acknowledgement's exact
deployment receipt ID. A callback for another AVM version is rejected. The
focused state test passes. Duplicate-module batch and full cross-stage
projector integration coverage remain open; no deployment occurred.

**WP-22 duplicate-context fan-out fixture (2026-10-11):** The replayable
global deployment receipt fixture now combines two selected provider contexts
for one module with two additional modules. It records one module acknowledgement
before replay and verifies that replay completes the remaining modules without
duplicating the existing acknowledgement. The focused state test passes. This
closes the duplicate-context fixture slice only; cross-stage projector
integration remains open. No deployment occurred.

**Accepted-tree planner fixture (2026-10-11):** A positive CLI-package fixture
now validates an exact indexed accepted tree, operation-proven facts acceptance,
release source pin, provider snapshot and policy import receipts, and the
assessment request emitted for the accepted provider-major context. The other
required provider major remains explicitly deferred because its accepted
snapshot is absent. The previously open positive planner-fixture blocker is
closed locally; this is fixture evidence only, not a live operation.

**WP-23 API admission correction (2026-10-11):** The existing projector claimed
leases before reading GitHub's rate-limit budget. The scoped run path now checks
due state first, refreshes the budget only for non-empty work, caps the claim
using per-resource headroom above reserves, and persists that pre-claim budget
snapshot with the batch. Low headroom leaves intents unclaimed. Focused projector
and workflow-contract tests pass. This is a WP-23 slice only: the per-module
admission floor does not model every remote effect cost, and latest-status
selection plus central scheduled-projector integration remain open. No workflow
was dispatched and no live GitHub projection occurred.

**WP-23 singleton claim correction (2026-10-11):** The scheduled projector now
claims one priority-selected module at a time and records each outcome before
claiming the next. A durable module-specific retry or review result no longer
stops unrelated due modules; shared API reserve exhaustion stops the run before
another claim. A focused fixture verifies singleton claim ordering by priority
and that completed modules are not claimed again. Effect-cost-aware admission
and latest-status reduction remain open. No workflow or live projection ran.

**WP-23 exact-ack foundation (2026-10-11):** Successful delivery can now
persist the exact neutral projection acknowledgement in the same state
transaction as the delivery receipt. Delivery and neutral projection
generations are validated independently. A fixture also verifies that a newer
neutral handoff with unchanged visible content is re-read before it is
acknowledged. Focused GitHub and race tests pass. No production delivery
producer currently attaches the neutral identity, and the latest-status
reducer, neutral-intent bridge, cost-aware admission, and batch/cursor removal
remain open. No live projection occurred.

**WP-24 expired-owner checkpoint (2026-10-11):** Reclaim now marks the old
running batch expired and clears its lease in the same transaction that fences
and reassigns the due intent. Focused github package/race tests prove stale
callbacks are rejected and an expired owner cannot begin another mutation.
The delayed Issue-create fixture applies one remote effect, recovers the lease
while its response is blocked, then confirms the stale owner cannot begin its
follow-up label write. The new owner reads the Issue snapshot, adopts the
in-flight create without duplication, repairs owned labels and Project fields,
and records the acknowledgement. Focused package and race tests pass. This
completes WP-24's local fixture contract; no live GitHub mutation occurred.

**WP-25 Issue effect checkpoint (2026-10-11):** Issue-body and owned-label
fixtures now apply remote state before simulating a lost response. Retried
projection reads those effects, preserves a human label, repairs remaining
Project state, and writes the final Issue acknowledgement only after success.
The delayed WP-24 create fixture proves retry adoption without duplicate Issue
creation. Focused github package tests pass. WP-25's offline Issue effect slice
is complete; Project item/field ambiguity remains WP-26. No live mutation ran.

**WP-26 Project effect recovery checkpoint (2026-10-11):** Fixtures apply a
Project field update and Project item create before losing their responses. A
later run reads the actual item snapshot, skips the already-applied field, and
adopts the existing item instead of creating a duplicate. Cached snapshots now
treat absent fields as equivalent to desired empty values while still clearing
known non-empty values; focused tests cover both paths. WP-26's local effect
contract is complete; WP-27 evidence-comment effects and cross-stage integration
remain. No live mutation ran.

**WP-27 evidence effect checkpoint (2026-10-11):** Evidence comment create and
update fixtures now apply their changes before losing the response. Retry finds
the exact App-owned marker and reuses the original comment ID. Existing
supersession and projection-completion tests prove pending evidence is not
acknowledged early and an old receipt cannot clear newer content. Focused
package and race tests pass. WP-27's local evidence/comment contract is
complete; central scheduled projection and cross-stage integration remain. No
live GitHub mutation ran.

**WP-28 scheduled projector checkpoint (2026-10-11):** Added the autonomous
five-minute `factory-projection` workflow with empty workflow permissions,
job-scoped Contents read, the existing `data-publisher` environment, pinned
actions, reviewed `main` code, an isolated verified `factory-state` checkout,
and repository-wide non-cancelling concurrency. The new CLI command derives a
sorted due-module scope from a fresh validated state snapshot before minting
the reduced projection token, then reuses budgeted projection batches. Contract
tests parse the workflow YAML, check its schedule, triggers, permissions,
checkouts, due-state command and shell syntax; command tests reject missing
trusted run identity and unrelated options. Full `cmd/factory` and
`internal/github` package tests and race tests pass, as does `git diff --check`.
This completes WP-28's workflow packet locally. API-cost-aware admission,
latest-status catch-up and integrated cross-stage recovery remain open. No
workflow was dispatched and no live GitHub mutation occurred.

**WP-29 manual recovery checkpoint (2026-10-11):** Replaced the manual direct
projection command with `--request-projection-recovery`: it validates the
allowlisted exact module scope, initializes only missing intents, and reopens
acknowledged delivery only when the explicit force input is selected. Existing
intent fingerprints, active leases, review states, and retry deadlines remain
authoritative. The flush path now validates only exact selected modules with
due existing delivery and returns them to the scheduled poller; it does not
change stored retry deadlines. Both manual workflows retain state-only checkout
credentials and no longer mint reduced projection credentials or make board
calls. Tests cover scope, protected state, future and elapsed retry deadlines,
workflow YAML and shell contracts; `cmd/factory` and `internal/github` tests and
race suites plus `git diff --check` pass. Manual runs and live projection were
not performed. This closes WP-29's offline workflow packet; operator requests
have no separate receipt record, while existing due projection intents remain
the durable work authority.

**WP-30 projection credential boundary checkpoint (2026-10-11):** Removed the
standalone reduced-token mint command and the direct module projection command.
The scheduled due-intent path is now the only production `github.MintToken`
caller and the only path to `ProjectClaimedBatch`; state-only token minting
remains available to authorized state writers. A guard test scans production
factory commands and all workflow definitions for those retired command paths.
Focused and race `cmd/factory` suites, YAML/shell contracts and
`git diff --check` pass. External evidence, review and data APIs retain their
separate boundaries. No live workflow or GitHub projection occurred.

**WP-31 single-owner source proof (2026-10-11):** Removed the unused direct
module projection adapter and verified production command sources contain one
reduced-token mint site and one `ProjectClaimedBatch` call site, both in the
scheduled projection implementation. A persistent test scans command sources
and every workflow definition for the retired direct CLI paths. Full
`cmd/factory` tests and race tests pass. GitHub API reconciliation methods
remain in the projection package and are called only through this scheduled
runner; no live mutation occurred.

**WP-32 operations documentation checkpoint (2026-10-11):** Updated lifecycle
and operations docs to describe the five-minute due-intent owner, state-only
manual recovery/flush behavior, credential separation and unactivated schedule.
Updated the GitHub workflow instructions with the single-owner boundary. These
docs distinguish local implementation from live activation; no workflow or
live projection was performed.

**WP-32 documentation correction (2026-10-11):** A final source audit found
manual board-write instructions and assessment/Pages-as-unwired descriptions
in the operations guide, workflow instructions, Go guide and an old decision
record, plus obsolete Go pacing/comment procedures. Updated the current
operations guide/diagram for state-only discovery, separate scheduled delivery
and combined site publication; marked the old direct-delivery decision
superseded and the separate lifecycle diagram historical; distinguished F-019's
historical v1 gap from v2 source wiring. The status mapping includes `Retired`.
The documentation inconsistency is F-047. `go test ./cmd/factory -run Workflow
-count=1`, stale-text search, and `git diff --check` pass; full offline gates
remain pending. No workflow or live projection occurred.

**WP-34 gate rerun after WP-32 documentation corrections (2026-10-11):**
`test -z "$(gofmt -l cmd internal)"`, `go mod verify`,
`go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`,
`go test ./cmd/factory -run Workflow -count=1`, race tests for factory,
GitHub, projection, discovery, publish, state and queue, and `git diff --check`
pass on the current worktree. Workflow fixtures parse repository YAML and
validate embedded shell. This is local verification only; the two lifecycle
policies, final independent review, PR update and gated live work remain. No
workflow or live operation occurred.

**WP-33 cross-stage fan-out checkpoint (2026-10-11):** Expanded the projector
batch fixture to three modules and asserts the failed module is parked, the
neighbor is acknowledged, and the third remains unstarted with its pending
intent intact. Added a three-module publication fixture that imports one exact
global Pages receipt, interrupts after one module acknowledgement, reimports
the same receipt, and completes all module-owned acknowledgements idempotently.
That integration exposed and fixed owner filtering in acknowledgement replay:
replay now ignores other modules' valid acknowledgement records before
validating the requested module's exact path and receipt. Existing queue tests
cover fixed barrier deadlines, stale/unknown predecessors and active-owner
exclusion; existing projection tests cover retry deadlines, current-generation
fencing, pending evidence, unknown effects, and human-label preservation.
`go test ./internal/state ./internal/github -count=1`, their race suites, and
`git diff --check` pass. This is local fixture evidence only; full
TASK-016/overall cross-stage review and live deployment remain open.

**WP-34 offline gate checkpoint (2026-10-11):** Final local gates pass:
`gofmt` and `test -z "$(gofmt -l cmd internal)"`, `go mod verify`,
`go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`, focused workflow-contract tests,
YAML parsing for all workflow files, `bash -n` through workflow fixtures,
race tests for `cmd/factory`, `internal/github`, `internal/state`,
`internal/queue`, `internal/projection`, `internal/discovery`, and
`internal/publish`, and `git diff --check` all pass. No workflow was dispatched,
and no state or production data was written. This closes local gate execution
only; independent review, PR checks and separately gated live work remain.

**WP-34 PR-base dataset correction (2026-10-11):** CI correctly rejected the
first mixed code/data commit under the data-only proposal contract. The
checked-in dataset change was excluded from the PR while its pre-existing local
content was preserved uncommitted. CI then exposed that the v3 reader rejected
the current generated v2 dataset before rebuilding its indexes. The reader now
uses only stable generation metadata and recalculates dataset identity and
release membership from accepted source records; site preparation binds the
exact publication timestamp. Full tests, vet, module verification, dimension
listing, workflow/YAML checks, required race suites including index/site, and
`git diff --check` pass locally. No workflow or live operation ran. PR checks
pass on ready-for-review PR #317 at head `b798bdeb`. The independent contract
review and separately gated live cutover remain open; no merge or live action
was performed.

**Reconciliation status (2026-10-11):** PR #317 merged at
`ec2b93bd56d7ca13d543c94d4672e9d73e65410a`; PR #318 merged at
`e4e8206fd70441b264f7e3823c9d55911e1b0847`, the current `main` base for the
reconciled worktree. The uncommitted implementation here is separate from both
merged PRs and has no PR yet. The F-040 generation-order correction is covered
by targeted tests and repository gates. WP-17's exact accepted-tree planner
fixture and state input-import receipt are implemented locally; provider-root
acceptance provenance and shared-consumer deduplication still need review.
Remaining WP-19 through WP-33 integration and independent review gates remain,
although multiple packet slices are complete; do not create a PR until the
approved change is workflow-testable and the required offline gates and
independent contract review pass. No live operation occurred.

WP-23 neutral scheduled-delivery checkpoint (2026-10-11): the scheduled
projector now materializes changed neutral intents from the fresh accepted
catalog, attaches the exact neutral identity, preserves identical intents
across schedules, and revalidates it during due enumeration, claim, preflight,
each remote mutation, and acknowledgement. Confirmed inactive catalog modules
may project only `retired` status.
`Done` requires exact current deployment proof. A fixture changes the neutral
generation during a remote read and proves the next mutation is fenced.
Declared evidence effects currently fail closed because independent effect
delivery is not yet implemented. Focused factory/GitHub/state tests pass. This
is a local integration slice only; canonical status reduction, evidence-effect
delivery, cost-aware admission, workflow review, and full offline gates remain
open. No workflow or live operation occurred.

**WP-23 review fixes (2026-10-11):** An unsupported neutral evidence effect
leaves only its module pending while valid neighbors materialize. Projection
save decisions recompute the merge from the transaction callback's fresh
snapshot and reject a changed neutral identity or write set. Focused factory
and GitHub fixtures pass. An outage fixture confirms the bridge consumes the
current module-wide neutral record after it advances, but cross-producer status
reduction, evidence-effect delivery, cost-aware admission, and batch/cursor
removal remain open; no remote projection occurred.

**WP-23 module-ceiling checkpoint (2026-10-11):** Scheduled projection now
supports 10/50/100 module ceilings, defaults to 50, and orders due retries and
priorities before selecting that run's scope. Focused workflow and CLI fixtures
pass. Claims are still represented by the legacy durable projection-batch
record/cursor, and admission still uses a per-module reserve floor rather than
per-effect cost estimates. `go mod verify`, `go test ./... -count=1`,
`go vet ./...`, dimension listing, workflow-contract tests, the required race
suite, global gofmt check, and `git diff --check` pass for this worktree
checkpoint. WP-23 remains incomplete; no workflow ran.

**WP-23 claim-state correction (2026-10-11):** Durable multi-module projection
batch records and batch cursors have been removed; each claim, budget snapshot,
remote-read checkpoint, lease and fence live on one delivery record. Focused
GitHub tests and `go test -race ./internal/github -count=1` pass. TASK-011A
review then found that automatic reclaim on lease expiry could overlap an
unknown in-flight remote write. Expired projecting deliveries now remain parked,
and a newer desired status preserves the unresolved owner while fencing its old
callback. This supersedes the older WP-24 checkpoint that allowed expiry-only
reclaim. Verified prior-run termination and effect reconciliation still need an
explicit recovery implementation before WP-24 or the full workflow-testable
scope can be considered complete. No workflow or live operation occurred.

**WP-19 exact coverage correction (2026-10-11):** Prior selected context
coverage now requires the pointer's request ID to equal the exact current
assessment request, in addition to generation and accepted-input fingerprint.
A regression fixture with a valid receipt and mismatched request pointer remains
unselected. Focused state tests pass; WP-19 cross-stage and site-cycle gates
remain open. No workflow or live operation occurred.

**WP-24 exact owner recovery checkpoint (2026-10-11):** Scheduled claims now
persist their exact Actions run ID and attempt. The workflow queries both the
exact attempt and the parent run; only a terminal exact projector attempt on
`main`, with no active parent attempt, can release the matching expired claim.
Recovery increments the fence and returns delivery to pending. The same
scheduled execution then uses the standard Issue/Project readback and
read-before-write reconciliation path; recovery never acknowledges success.
Focused GitHub/state fixtures and workflow-contract checks pass. No workflow,
state branch write, or live GitHub operation occurred. This closes the local
WP-24 recovery slice; independent contract review and cross-stage integration
remain open.

**WP-24 safety correction (2026-10-11):** The exact terminal Actions attempt
proves that its process stopped, but cannot prove an already accepted GitHub
effect has settled. Recovery now increments the fence, clears the dead lease,
and parks the delivery as `needs-review` with an explicit unknown-effect
reason. It does not return the module to the due queue, and newer desired
content preserves the hold, including a newer equivalent intent whose
fingerprint matches an older acknowledgement. Fixtures cover the parked state
and both changed/equivalent newer-intent paths. This supersedes the prior
checkpoint's automatic retry after read-before-write. A reviewed operator
resolution remains needed for parked effects; WP-24 and workflow-testable
delivery are not complete. No workflow, state branch write, or live GitHub
effect occurred.

**WP-23 active-slot materialization correction (2026-10-11):** The scheduled
neutral-intent bridge applies the shared active-module reducer before creating
a GitHub delivery. A newer queued neutral intent remains the exact handoff
identity, while visible status and source version/SHA remain bound to the
currently held AVM release. A focused fixture covers an older active source
acquisition, a queued newer release, and a stale Backlog intent. Canonical
reduction outside active slots, evidence-effect delivery, workflow integration,
and the remaining offline/review gates stay open. No workflow or remote
projection ran.

**WP-23 active-slot transaction race correction (2026-10-11):** Read-only
contract review found that an active request could advance after materializer
preparation while the neutral intent identity stayed constant. The state save
transaction now revalidates projected status and source version/SHA against
the current active-slot reduction. Scheduled materialization skips an
equal-identity delivery only while its lifecycle authority remains valid. A
fixture verifies the persisted delivery is rebuilt from Backlog to Analyzing
and an unknown remote-effect hold remains parked. Focused tests cover the
request transition between preparation and commit plus persisted stale-delivery
rematerialization. The full offline gate rerun passes, as recorded in the
queued-release correction checkpoint below. WP-23, WP-24 operator resolution,
full workflow integration and independent review remain open. No workflow or
remote projection ran.

**WP-23 queued-release projection correction (2026-10-11):** The shared
module-status projection reducer now prefers the active lifecycle slot, then
the oldest eligible queued source release when the slot is free. Discovery
handoffs and scheduled neutral materialization use the same result; the neutral
identity still represents the newest producer handoff. A race fixture adds an
older release after preparation and confirms the save is fenced. Focused queue,
discovery, GitHub, and factory tests pass. Full offline gates also pass:
`go mod verify`, `go test ./... -count=1`, `go vet ./...`, dimension listing,
workflow-contract tests, required race tests, global gofmt check, and
`git diff --check`. Other idle lifecycle states, evidence-effect fan-out,
cost-aware admission, and workflow integration remain open. No workflow, state
branch write, or live projection occurred.

**WP-17 contract review checkpoint (2026-10-11):** A read-only Factory
Contract Reviewer found no source-level gap in provider-root acceptance
provenance or shared-consumer evidence deduplication. The exact accepted commit
and provider snapshot are bound by the trusted import receipt; the site planner
requires receipt-proven accepted provider/policy generations; and the added
two-consumer fixture proves identical source evidence converges into one shared
provider snapshot entry. The site-cycle integration fixture persists only the
exact eligible request and leaves it queued. This closes WP-17's local
implementation and contract slices, not live acceptance. No collection,
workflow, state write, assessment, or remote-protection review occurred. Later
cross-stage and projector packets remain open.

**WP-21 retirement-membership review correction (2026-10-11):** Pages fan-out
verifies exact current null-selection receipts for retired modules and excludes
them from published and required-context counts. If an analysis-backed member
establishes the accepted source and required-context set, non-required
deselections may coexist with a complete deployment; required deselections
leave the acknowledgement partial and the slot active. Independent review
found the all-deselected case has no analysis-backed source or required-set
proof. It is explicitly deferred as `ErrDeploymentNotCurrent`, so the importer
continues fan-out for other module/releases without acknowledging or releasing
this module. Focused fixtures and `go test -race ./internal/state -count=1`
pass locally. The all-deselected retirement completion contract remains open,
as do WP-21 integration review and later cross-stage gates. No workflow, state
branch write, or live Pages operation occurred.

**WP-22 slot/claim boundary checkpoint (2026-10-11):** Fixtures now prove an
authorized deployment with no imported Pages callback leaves the module slot
active; partial exact membership also retains it; and complete required
membership releases it with the exact deployment receipt and membership digest.
A queue fixture rejects a later AVM release while the earlier slot is active
and claims that queued request after verified release proof is present. Existing
fixtures retain unknown worker-run ownership and replay global deployment
fan-out without duplicate module acknowledgements. Focused state and queue tests
pass locally. These are separate local state and queue boundary fixtures, not a
single full-workflow execution. The WP-21 all-deselected retirement outcome,
remaining WP-22 review, and WP-33 cross-stage workflow integration remain open.
No workflow, state branch write, or live Pages operation occurred.

**Current-worktree offline gates (2026-10-11):** `go mod verify`,
`go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`,
`go test ./cmd/factory -run Workflow -count=1`, global gofmt check,
`git diff --check`, and required race tests for factory, GitHub, projection,
discovery, publish, state, and queue pass on the reconciled worktree. This is
local verification only and does not close WP-21's all-deselected retirement
contract, WP-23 effect-cost admission, WP-24 unknown-effect operator resolution,
or the final cross-stage contract review. No workflow, state branch write, or
live GitHub/Pages operation occurred.

**WP-23 scheduled admission and cancellation checkpoint (2026-10-11):** The
scheduled path now initializes one run-local Pacer and bounds all state
materialization, token mint, budget reads, and delivery work with a 50-minute
application context under the workflow's 60-minute timeout. It stops before a
new singleton claim when the context expires, with a separate one-minute
durable-outcome cleanup bound. Pre-claim admission estimates resource requests,
secondary requests, mutations, and request time from each intent; it preserves
due priority while allowing a cheaper later candidate to proceed. The Pacer
still enforces every actual request because remote pagination and issue-label
cleanup vary. Pre-send cancellation is recorded as transient retryable work;
transport cancellation after a mutation starts remains needs-review. Focused
factory/GitHub tests pass for budget bootstrap, cost-based neighbor selection,
deadline/workflow margin, pre-send zero-send cancellation and later retry, and
uncertain in-flight cancellation. This is local evidence only. WP-23's full
remote-effect cost review, WP-21 retirement resolution, WP-24 operator
resolution, and final cross-stage gates remain open. No workflow or live
operation occurred.

**WP-23 managed-label cleanup admission correction (2026-10-11):** Independent
review found Issue projection could delete an unbounded number of stale
factory-owned labels after pre-claim admission. Cleanup is now capped at ten
per Issue and rejects overflow before label writes; effect admission reserves
the full REST/mutation ceiling. Focused GitHub tests pass. Full gates and
independent re-review remain open. No workflow or live GitHub operation
occurred.

**WP-28 schedule activation guard (2026-10-11):** Read-only review identified
that merging the scheduled workflow would otherwise activate its job before
WP-41/HUMAN-002. The workflow retains its reviewed five-minute trigger and
least-privilege body, but the projection job is hard-disabled in this PR. Its
contract test rejects removal of the guard. Only the separately approved
activation packet may enable it after live validation. No workflow was run.

**WP-23 lease/pagination review correction (2026-10-11):** Independent review
found that duration admission used the run deadline rather than the shorter
claim lease, and cold Project item pagination was absent from the GraphQL cost.
The scheduled owner now gives each claim a 55-minute lease, reserves five
minutes for durable outcome/cleanup, admits against the exact claim expiry and
workflow deadline, and cancels effect execution at the absolute
lease-minus-reserve deadline. Cold Project item reads stop at ten pages; the
estimate reserves those ten GraphQL requests. Fixtures cover over-window
evidence cost, pagination cost, and the page ceiling. Focused factory/GitHub
tests pass. Variable managed-label cleanup still relies on Pacer enforcement;
rerun race/root gates and final review remain. No workflow or live GitHub
operation occurred.

**WP-23 evidence-comment pagination correction (2026-10-11):** Independent
re-review found evidence marker lookup could scan unbounded Issue comments
while the admission estimate reserved only two REST calls per comment. The
lookup now caps at ten pages and admission reserves those ten reads plus one
possible create/update per pending evidence item. A multi-page cap fixture and
worst-case estimate fixture pass with the focused GitHub/factory suites and the
GitHub race suite. Full gates and another contract review remain. No workflow or
live GitHub operation occurred.

**WP-34 offline gates after WP-23 review fixes (2026-10-11):** `gofmt`,
`go mod verify`, `go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`,
`go test ./cmd/factory -run Workflow -count=1`, `git diff --check`, and race
tests for factory, GitHub, projection, discovery, publish, state, and queue
pass on the current worktree. Workflow-contract tests cover repository YAML and
embedded shell syntax. These are local results only. WP-21 all-deselected
retirement, WP-24 operator resolution, complete WP-35 review and PR checks
remain open. No workflow or live operation occurred.

**WP-34 gate rerun after bounded-label and schedule-guard fixes (2026-10-11):**
`go mod verify`, `go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`, workflow-contract tests, global
gofmt check, `git diff --check`, and required race tests for factory, GitHub,
projection, discovery, publish, state and queue all pass. Workflow tests parse
YAML and check embedded shell syntax. The projector job remains hard-disabled;
no workflow ran. These are offline results only. WP-21 retirement policy,
WP-24 operator resolution, final WP-35 review and PR checks remain open.

**WP-35 read-only review after WP-23/WP-28 fixes (2026-10-11):** The reviewer
confirmed merge cannot execute the hard-disabled scheduled job and that the
ten-label cleanup bound is included in admission. No additional source-level
lifecycle or provenance defect was found. The all-deselected retirement
completion rule and unknown remote-effect operator resolution remain explicit
open decisions in F-044/F-041. WP-35 remains incomplete until those selected
contracts are implemented and re-reviewed; no workflow or live operation
occurred.

**WP-21 retirement provenance review (2026-10-11):** The accepted acquire-source
receipt already binds exact AVM release, source SHA, facts ID, accepted commit,
and complete required-context set, with historical write-set verification.
This provides a candidate basis for a terminal `Retired` acknowledgement
without claiming assessment completion when exact current null-selection
receipts cover required contexts and the exact Pages receipt includes them. The
contract and implementation remain open pending product direction; this review
does not mark retirement completion accepted or implemented. No workflow, state
write, or Pages operation occurred.

**WP-21 all-deselected retirement implementation (2026-10-11):** Implemented
receipt-backed retirement fan-out. Exact accepted facts/context provenance,
complete eligible-context coverage, current null-selection pointers, retired
catalog state, and the verified Pages membership are required before writing a
terminal retired projection and releasing the version slot. The retired
projection reports zero selected/published contexts and carries no assessment
publication commit. Omitted required contexts remain parked. Focused state
fixtures pass; race and cross-stage review gates remain. No workflow, state
branch write, or Pages operation occurred.

**WP-24 exact-readback resolution (2026-10-11):** Ledermayer selected exact
remote readback adoption: only a complete exact Issue/Project match may resolve
a terminated owner's uncertain effect; absent or mixed effects remain parked.
The manual recovery workflow supports an explicitly selected exact module list,
capped at ten. `ReadbackFleet` returns a private proof value only after all
factory-owned surfaces match, and state adoption accepts that value rather than
a caller-provided binding. Incomplete Issue search results and duplicate
Project membership fail closed. Table fixtures reject mismatched title, marker,
summary, managed labels, every owned Project field, and absent or mismatched
Issue membership; a supersession fixture leaves newer desired state pending
after adopting the exact old effect. Independent read-only review found no code
blocker and prompted a correction that labels the old F-044 behavior historical.
No workflow, live GitHub read, state write, or remote mutation occurred. The
workflow remains review-only; full gates are open.

**WP-34 final offline gates after WP-21/WP-24 review (2026-10-11):** The
repository-wide gofmt check, `go mod verify`, `go test ./... -count=1`,
`go vet ./...`, dimension listing, workflow-contract tests including YAML and
embedded shell, `git diff --check`, and required race tests for factory,
GitHub, projection, discovery, publish, state, and queue all pass. Independent
read-only contract review found no code blocker; its F-044 chronology correction
is recorded. No workflow, live GitHub read/mutation, state write, Pages deploy,
or schedule activation occurred. PR CI/review remain pending.

**TASK-021 pilot discovery correction (2026-10-11):** PR #320 merged the
explicit run-identity and exact ten-module site-planning contract. The first
post-merge cohort run recorded the unchanged full catalog and a durable exact
continuation, then stopped before its first module handoff. Resuming that
continuation under the same authorized scope claimed generation 2 but failed
at the first module. Local reproduction through the production
`DecisionCoordinator` established that the module-cycle transaction's stable
operation ID was not present among its events, violating the coordinator's
replay identity contract. F-049 records the original runs and cursor. PR #321
merged the transaction-identity correction as
`465925f6b2198eeb19f3e07abd119aeee66beafa`. After rereading the exact owner,
expired lease, cursor, and active manual-discovery runs, run #38160309883
resumed the saved continuation and completed all ten current-release checks
against the unchanged full catalog. It reported zero source changes and
generated no new acquisition requests.

**TASK-021 current-release request binding correction (2026-10-11):** The
first exact acquisition request failed at the dispatcher claim step in run
#38160598353 before an attempt or worker was launched. The queued requests have
the exact current source SHA but omit `avm_release_version` and lack the exact
receipt-backed acquisition handoff required by claim eligibility. The previous discovery
cycle left these requests unchanged because the pinned sources had no new
releases. F-050 records the live observation and inferred cause. A focused
no-force correction on `fix/discovery-versioned-current-release` queues a
current-pin versioned request only when an exact matching unstarted
versionless request exists, supersedes that queued record, and commits the
receipt-backed acquisition handoff without refresh generation. TASK-021
remains pending. After review and merge, perform the exact ten-module current
release discovery check, verify its receipts, and dispatch only the resulting
versioned requests. Provider collection, assessment, acceptance, and
publication remain unperformed. The full 232-module catalog, TASK-022 schedule
gate, and explicit UTL exclusion are unchanged.

**TASK-021 source-resolution checkpoint (2026-10-11):** PR #322 merged the
current-release request binding repair. Exact no-force run #38161477827 on
merged `main` accepted the unchanged full catalog, then failed source
resolution on the first cohort module before any release check or handoff. Its
exact continuation is
`discovery-cycle-1b9439bafddbdb9a1617caffdd6dee5eaea3179529da8230693502b105151b85`,
cursor `next=0`, owner generation 1, lease through
`2026-10-11T18:03:37.073800484Z`. The run passed the workflow repository's
`GITHUB_TOKEN` to an upstream Azure public-repository resolver; GitHub's
documented repository limit makes this a likely cause, but the upstream error
is redacted and the diagnosis remains unverified (F-051). A manual-only fix
uses anonymous public-source reads while preserving factory-discovery. After
review/merge and lease expiry, resume this exact cursor under the approved
ten-module scope. No acquisition, provider collection, assessment, acceptance,
or publication has occurred; TASK-021 remains pending.

**TASK-021 receipt-backed continuation checkpoint (2026-10-11):** PR #324
merged as `7c9f38a899068da3ba326f60fb413db0210cda30`, with required CI and
local full/workflow/race gates passing. The following exact continuation,
[38164375052](https://github.com/softwareone-public/avm-intelligence/actions/runs/38164375052),
failed before a module transaction. Read-only inspection at
`factory-state` head `fa26ddb1172ffec45089a902c7184709e452a418` confirms the
ten-module approved cursor remains at `next=0`, owned by run `38164375052` at
generation 3; its lease has expired and no manual discovery run is active.
Four unstarted versionless acquisition records have fingerprints from an
older input shape. Current `queue.CoalesceRequests` validates the complete
request history and rejects those records before it can prepare a version-bound
request. F-053 records this live failure and the source-confirmed cause.

The focused correction in `fix/repair-historical-versionless-acquisitions`
deletes only exact, unstarted versionless request records whose sole validation
failure is the obsolete fingerprint, using a compare-and-delete event in the
module operation receipt. Valid old requests remain superseded history. Focused
state, queue, and discovery tests pass. The full local gate set also passes:
`go mod verify`, full tests, vet, dimension listing, workflow-contract tests,
required race tests, gofmt, and `git diff --check`. PR review/merge and the exact
continuation retry remain pending. After merge, re-read the owner, lease,
scope, state head, and active runs before resuming the authorized current-release
cycle. Verify every request/handoff/receipt before dispatching acquisition.
Provider collection, assessment, acceptance, and publication have not occurred.
The full catalog and ten-module processing boundary are unchanged, the
scheduled projector remains disabled, and the UTL continuation remains
excluded.

**TASK-021 worker candidate provenance checkpoint (2026-10-11):** PR #331
fixed step-scoped credentials; recovery run `38175007854` and exact dispatcher
run `38175086623` safely retired the fence-3 attempt and started the exact
apimanagement request at fence 4. Worker run `38175129120` passed prepare and
compute, but trusted seal failed before state publication. At state head
`4870c19a91df27229bf1083510bab91c259c3ee3`, that attempt remains dispatched
and bound to the worker; no completion receipt or accepted facts exist.
F-060 records the verified source cause: completion used provenance operation
`worker.complete`, absent from the state Git allowlist, which already accepts
`worker.seal`. The correction aligns the caller and adds a regression; a
read-only 1,133-record snapshot/artifact fixture succeeds through the real Git
candidate builder after the change. After reviewed merge, recover only this
exact attempt and verify requeue before retrying apimanagement. No provider
collection, assessment, acceptance, or publication occurred. The full 232-module
catalog and exact ten-module processing limit remain; scheduled projection
stays disabled and the interrupted UTL continuation remains excluded.

**TASK-021 post-PR workflow-boundary checkpoint (2026-10-11):** PR #325 merged
as `6bcd6c0f4c052e0d06df4c5d1a18b7993c0c72c2`. Post-merge read-only checks
confirm the exact approved ten-module cursor remains at `next=0`, owned by run
`38164375052` at generation 3, with an expired lease, no pending delivery, no
active manual discovery run, and `factory-state` head
`fa26ddb1172ffec45089a902c7184709e452a418`. Before resuming, review found
five trusted workflows still generate an unused `git ls-tree` listing, while
the current state transport requires the verified backend to derive the tree
from `work/state`. F-054 records this source-level contract divergence; no
integrity incident or live operation is claimed. The focused correction in
`fix/remove-derived-state-tree-listings` removes the redundant commands and
adds a parsed YAML/shell workflow test. Full tests, vet, module verification,
dimension listing, workflow-contract tests, the CLI race test, gofmt, and
`git diff --check` pass locally. PR merge and the saved-cursor retry remain
pending. After merge, re-read
the owner, lease, exact scope, state head, and active runs before the authorized
no-force continuation. All 232 catalog modules stay on the portal, only the
ten approved modules receive pilot work, the scheduled projector stays off,
and the interrupted UTL cursor remains excluded.

**TASK-021 worker recovery and retry checkpoint (2026-10-11):** PR #328
merged the relative checkout-root correction at
`9fbcc25926cb68cade76741900591cfe77bbcf73`. After exact preflight, recovery
runs `38172345160`, `38172464820`, and `38172551570` retired the three
failed attempts and requeued their requests. Verified state at
`48f3735917d50c18924b0b595a5d0749b40c98e8`
showed all ten release-bound acquisition requests queued without owners. The
first retry, run `38172762923`, failed before claim or worker dispatch because
the prerequisite lookup treated the original input handoff and prior dispatch
handoff as duplicate consumer inputs; F-057 records the exact incident and
source cause. The local fix excludes claim-generated `dispatch:` handoffs from
upstream-input selection and adds claim/recover/retry coverage. Focused queue
tests pass; full review gates and PR review remain pending. After merge, reread
the state head, ten exact requests, attempts and active Actions before retrying
only the first request. Keep the full 232-module portal catalog, exact ten
processing scope, recurring schedule off and interrupted UTL cursor excluded.

**TASK-021 worker seal checkpoint (2026-10-11):** PR #329 merged the retry
handoff correction as `9dfcdf4f945b67c85620d34717584943b0989096`. The exact
apimanagement retry then claimed at attempt 2/fence 2 in dispatcher run
`38173736565` and launched worker run `38173784861`, confirming the queue fix
live. Preparation and facts computation succeeded, but the trusted seal failed.
At verified state head `e2aa7b1cebb1494adbe38b204cdb886fe502ca9f`, that exact
attempt is bound to the terminal failed worker and its request remains running;
the other nine requests remain queued. F-058 records the source-proven manual
workflow omission. The local correction supplies request, dispatcher, workflow,
and commit provenance and scopes App credentials to minting steps. Focused and
workflow-contract tests, full Go tests, vet, module verification, dimension
listing, `cmd/factory` race tests, gofmt and diff checks pass locally; PR review
remains pending.
After merge, recover only this exact attempt and verify it is requeued before
retrying apimanagement. Continue the exact ten-module cohort only; retain all
232 catalog modules, schedule-off, and UTL-exclusion boundaries.

**TASK-021 worker preparation credential checkpoint (2026-10-11):** PR #330
merged the trusted seal provenance fix. Its next retry, worker run
`38174351615`, failed before binding or compute because the prepare CLI lacked
App credentials required to initialize the state backend. Verified state head
`c1ccc616f2890bcdf187a82fb7ee60ae60e5caa1` records only the exact
apimanagement request claimed at fence 3 with an unbound attempt and a lease
through `2026-10-11T21:27:14.414761777Z`; the other nine pilot requests remain
queued. F-059 records the incident. The focused correction gives credentials
only to trusted mint, prepare, and seal steps in both worker workflows and
tests that compute receives none. The full Go suite, vet, module verification,
dimension listing, workflow-contract tests, required race tests, global gofmt,
and diff checks pass locally. After reviewed merge, recover only the exact
unbound attempt and verify the requeue before retrying. No collection,
assessment, acceptance, or publication occurred. The portal remains the full
232-module catalog with only the ten approved pilot modules receiving work;
scheduled projection remains disabled and the UTL continuation remains
excluded.
