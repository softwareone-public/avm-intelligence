---
goal: "Make discovery independently idempotent per module and centralize all Issue/Project projection in factory-projection"
version: "1.2"
date_created: "2026-10-09"
last_updated: "2026-10-09"
owner: "Ledermayer"
status: "Planned"
tags: [discovery, idempotency, projection, state, workflows]
---

# Introduction

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

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

This revision records Ledermayer's requested design and autonomous delivery mode;
the complete revised plan remains Planned until HUMAN-001 approval. Approval
authorizes its bounded implementation, PR creation, and merge through existing
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
  defaults, factory new-module-only scope, source pins, generations, capacity,
  network pacing, fencing, leases, accepted history, and no-dispatch-from-discovery.
- **REQ-015**: Source reads, state transactions, worker execution, collection,
  and board projection remain separate side-effect stages. `factory-projection`
  does not collect source data, dispatch workers, accept evidence, select
  analysis, publish data, or infer success from workflow status.
- **REQ-016**: A module has one current lifecycle status and at most one active
  stage job. `queue.moduleHasActiveClaim` remains the exclusion rule: a request
  in `claimed` or `running` blocks another claim for that module. The dispatcher
  must not create or start another job for that module in any state. Terminal,
  superseded, and historical records remain queryable but cannot control the
  current status or authorize concurrent work. Batching is allowed across
  different modules only. Lease expiry alone does not prove an old execution
  stopped; active or unknown Actions owners remain parked for verified recovery.
  Reduce status across required contexts, retaining older active and newer desired
  work without allowing another active execution owner.
- **REQ-017**: Stage handoff is a durable state machine. Discovery writes the
  initial queue record. The dispatcher reads due records and claims one. The
  worker computes one stage without state credentials; its trusted validation job
  seals completion and commits the next-stage and projection intents, then releases
  its claim. Produced facts are not accepted facts. The collector reads that next-stage
  intent, performs its single module stage, and writes the following stage and
  projection intent. The same pattern continues through publication and Pages.
  No stage reads the board to decide what work is due.
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

### GPT6-Luna Assignment Protocol

- Use GPT6-Luna for implementation assignments. This document does not launch
  agents. If that model is unavailable, report the capability blocker and obtain
  an impactful model-selection decision; never silently substitute a model.
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
| TASK-004 | TASK-002, TASK-003 / Discovery | Split catalog observation/outbox persistence from module enqueue. Persist exact immutable enrollment membership with catalog acceptance, including new-ID-only delta and manual selection provenance. Recover missing module work from that membership even when a later delta is empty. Reconcile only missing/due module records; catalog retry cannot repeat completed modules. No enrollment cursor, batch lease or global completion record owns module success. Test crashes before and between enrollment commits. | No | Pending |
| TASK-005 | TASK-004, TASK-008 / Discovery | Replace Cycle/ContinueCycle delivery coupling with a per-module processor in internal/discovery. Claim due work, resolve source outside the transaction, then atomically commit module source pin, request, lifecycle, work completion, next-stage intent, desired projection, fixed barrier deadline and receipt. No board call or wait. First unresolved discovery failure stops with earlier commits preserved and later work due. Replay uses module identity, not continuation cursor. | No | Pending |
| TASK-006 | TASK-005 / CLI + Workflows | Remove discovery projection commands, callbacks, token minting, and Issue/Project API calls from `cmd/factory/discovery_cycle.go`, `discovery_projection.go`, `native.go`, `main.go`, `manual-discovery.yml`, and `factory-discovery.yml`. Keep exact selectors, force behavior, pacing, catalog observation, and separate state checkout. A failed module exits with its durable state and safe error class. The current continuation remains historical and excluded from selection; do not edit or resume it on the legacy path. | No | Pending |
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
| TASK-013 | TASK-011, TASK-012 / Workflow Integrator | Add .github/workflows/factory-projection.yml with schedule */5 * * * *, reviewed main code, verified isolated state checkout and separate state/projection credentials. Fresh-read due work before minting projection token. No producer artifacts, workflow_dispatch, self-dispatch or required workflow_run fan-in. Use repository-wide non-cancelling concurrency; run bounded budgeted batches and leave remainder/retries for later ticks. Use valid Actions permissions: actions: read only for implemented run-observation APIs, plus proven corresponding App grants; reject nonexistent workflows: read. Activation remains evidence-gated. | No | Pending |
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
| TASK-021 | TASK-020, TASK-023, TASK-024, HUMAN-002 / Validation Agent | Execute only the preapproved exact live validation envelope: pending avm-utl-sku-finder plus at most one unchanged control. Verify all-stage intents, exact acknowledgement or automatic release, central mutation, stable requests, bindings and human preservation. Reconciliation precedes this test. No additional worker launch/collection/acceptance/publication beyond the approved envelope. Offline fixtures cannot satisfy live acceptance. | No | Pending |
| TASK-022 | TASK-021, HUMAN-002 / Activation Agent | Activate the requested five-minute projector schedule and verified producer families only within the approved rollout envelope and existing environments. Record exact runs/heads, budgets, receipts, catch-up and timeout outcomes. Unverified families stay paused. Scope/policy changes or missing live authorization are impactful decisions, not routine PR approvals. This does not establish product acceptance. | No | Pending |

### Implementation Phase 7: Repair The Interrupted UTL Work

- **GOAL-007**: Reconcile sku-finder after merge and before TASK-021 validation.
  Task dependencies, not section order, govern execution. Exact live scope must
  already be approved under HUMAN-002; otherwise stop for that impactful decision.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- |
| TASK-023 | TASK-020, HUMAN-002 / Reconciliation Agent | Reread head, exact continuation and owner immediately before the approved fenced handoff. Active/unknown owner blocks takeover; wait or use only explicitly approved release. Bind existing v0.4.0 pin and queued request without duplication, preserving five skips and generation. Commit module handoff/projection intent first; separately mark global legacy continuation terminal after proof of handoff. Never cross owners in one module transaction, edit JSON directly or resume legacy code. Unknown/conflicting identities park only this module. | No | Pending |
| TASK-024 | TASK-023, HUMAN-002 / Projection Validation Agent | Consume the reconciled state-only projection through the central projector within the approved exact envelope. Reconcile original Issue/Project/evidence identities before retry, recording exact acknowledgement or explicit unknown/needs-review. No dispatcher claim or worker launch is needed to create Backlog: Discovery/reconciliation already wrote the intent. Prove head ancestry, one acquisition request and no duplicate effects before TASK-021. | No | Pending |

### Approval And Evidence Gates

| Gate | Required Human Action | Evidence / Stop Condition |
| --- | --- | --- |
| HUMAN-001 | Approve the complete revised plan once | Enables bounded GPT6-Luna implementation and autonomous PR/merge delivery under existing rules. This edit does not record that approval as complete. |
| HUMAN-002 | Resolve an impactful decision only when not already authorized | Covers exact live maintenance/validation/reconciliation/activation envelope, active-owner release, new scope, model unavailability, and incompatible externally enforced approvals. Bundle known decisions at plan approval; do not ask again for an approved envelope. No blanket cancellation, reset, policy bypass or inferred live authority. |

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
| GATE-006 | Approved bounded live validation proves state-only discovery, central projection, and no duplicate effect on replay. |
| GATE-007 | Approved producer-family activation has observed results; unverified families remain paused. |
| GATE-008 | The interrupted sku-finder operation is reconciled under its existing request identity, with the legacy continuation terminal and ineligible. |

The continuation
`discovery-cycle-d9db4db56f36ddfd1f6cc4d862f6f065b9012cb931eeb960d4e7d80fa891eb93`
was last observed at cursor 5/6 for `avm-utl-sku-finder`, with
`pending_delivery=false`, owner `37894014485-1`, and lease checkpoint
`2335460b7a858f1c332ce4554f0ac57061730392`. Re-read the head, owner, and expiry
before TASK-023. Do not resume the legacy code path.

### Bounded Agent Work Packets

Each row is one fresh GPT6-Luna assignment. Parent tasks complete only when all
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
| WP-09 | TASK-004 | WP-06 | Catalog acceptance/enrollment persistence and crash recovery; no module processor refactor. |
| WP-10 | TASK-005 | WP-07, WP-09 | internal/discovery per-module producer, receipts and exact handoffs. |
| WP-11 | TASK-006 | WP-10 | cmd/factory Discovery adapter: remove board delivery, retain state intents. |
| WP-12 | TASK-006 | WP-11 | Discovery workflow changes and neighboring workflow-contract tests only. |
| WP-13 | TASK-007 | WP-10, WP-12 | Discovery first/middle/last failure, replay, force and ownership fixtures. |
| WP-14 | TASK-009 | WP-08 | Dispatcher claim/release intent and acknowledgement integration. |
| WP-15 | TASK-009 | WP-14 | Acquire-source trusted completion handoff; compute credential-isolation tests. |
| WP-16 | TASK-009 | WP-15 | Assessment trusted completion handoff with context-specific input tests. |
| WP-17 | TASK-009 | WP-16 | Collector intent/receipt handoff and exact consumer checks. |
| WP-18 | TASK-009 | WP-17 | Acceptance intent production without bypassing reviewed evidence boundaries. |
| WP-19 | TASK-009 | WP-18 | Selection intent production and multi-context reducer preservation. |
| WP-20 | TASK-009 | WP-19 | Publication immutable global receipt and module fan-out/replay. |
| WP-21 | TASK-009 | WP-20 | Pages exact deployment membership and independent module acknowledgements. |
| WP-22 | TASK-009A | WP-21 | Cross-stage exclusion, active/unknown run recovery and duplicate-module batch tests. |
| WP-23 | TASK-011 | WP-21 | Neutral projector claims, latest-status selection and API-budget batch admission. |
| WP-24 | TASK-011A | WP-23 | Projector owner/in-flight recovery and delayed-write fixtures. |
| WP-25 | TASK-012 | WP-24 | Issue create/update/owned-label reconciliation and exact effect proof. |
| WP-26 | TASK-012 | WP-25 | Project item/field reconciliation and partial-effect recovery. |
| WP-27 | TASK-012 | WP-26 | Evidence-comment effects, exact projection_done and supersession tests. |
| WP-28 | TASK-013 | WP-27 | Scheduled central workflow, valid permissions and non-cancelling concurrency tests. |
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
- **DEP-008**: GPT6-Luna availability for bounded work packets. Model unavailability and incompatible enforced approvals are impactful blockers, not grounds for silent fallback or policy bypass.

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
- **TEST-004**: Concurrent batches and stages produce one current owner for a module. A second claim for a claimed or running module fails. Unrelated modules proceed independently. Stale owners and fences cannot overwrite newer state.
- **TEST-005**: Unknown publication is reconciled from the receipt at the candidate and descendant heads. Unavailable proof remains unknown and allocates no new identity.
- **TEST-006**: Recover catalog enrollment from immutable accepted selection membership after crashes before/between module commits, even when the next new-ID delta is empty. Completed modules are not rerun and unrelated modules are not enrolled.
- **TEST-007**: Discovery, dispatcher, trusted acquisition/assessment completion, collector and later producers atomically emit expected handoff/projection intents with exact provenance. Preserve required-context reducer precedence and older active/newer desired work. Compute gets no state credentials.
- **TEST-008**: Repository-wide tests identify all Issue/Project mutation methods and token requests. Only the projector can invoke them. State-token use is distinguished from projection-token use.
- **TEST-009**: Exercise 10/50/100 batch ceilings with default 50 and actual admission limited by per-effect API/time/reserve cost. Claim/checkpoint modules independently; park module errors and continue neighbors. Global API waits/integrity/cancellation stop safely. Concurrent invocations cannot own the same module and unfinished work resumes next tick.
- **TEST-010**: Lost responses for Issue creation/update, Project item/field changes, and evidence comments reconcile through their own markers. Human notes and unowned labels remain byte-identical.
- **TEST-011**: Retryable, permanent, rate-limit, protected-review, active-lease, cancellation, and unknown outcomes map respectively to retry time, needs-review, server wait, no-op, wait, and reconcile-before-retry. Logs contain no raw API body or credential.
- **TEST-012**: Workflow tests verify */5 * * * * on reviewed main, isolated verified state, separate credentials, no producer artifact trust, repository-wide non-cancelling concurrency and valid least-privilege permissions. Simulate producer failure after commit and delayed/skipped schedule ticks; later ticks discover pending work without workflow_run fan-in. Reject invalid workflows: read permission.
- **TEST-013**: Existing active, retryable, needs-review, and pending evidence records survive replacement. The projector changes only factory-owned fields and comments.
- **TEST-014**: The approved live test proves state-only discovery and central projection for the exact scope. Local fixtures do not satisfy it.
- **TEST-015**: Uncertain state publication and uncertain remote projection are both reconciled by their original identities before retry.
- **TEST-016**: One module with two due stages receives only one active job. A worker or collector batch containing multiple modules fails if any module appears twice. Completion releases the current claim before the next stage can start.
- **TEST-017**: Exact acknowledgement allows a claim; stale module/generation/transition/intent/fingerprint tuples do not. At fifteen minutes, automatic barrier release permits otherwise eligible work once, with unchanged deadline on retries and no fabricated projection_done. Unknown canonical state, active owners and acceptance/policy failures remain blocked.
- **TEST-018**: Simulate board outage while canonical work advances via timeout. Recovery projects latest status only, preserving all required independent evidence effects and human content. Superseded status cannot overwrite latest; uncertain old writes remain reconciled before conflicting effects.
- **TEST-019**: Delay an old projector request across lease expiry/new intent creation. Active/unknown run observations prevent takeover; fencing rejects stale acknowledgement but is never treated as remote fencing. Lost-write-response recovery uses the original effect identity and never guesses absent effect from an unresolved request.
- **TEST-020**: Crash publication/Pages fan-out after one module acknowledgement. Replay verifies the original global receipt, updates only missing module references and does not repeat deployment or mark nonmember modules Done.
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
- **RISK-010**: GPT6-Luna or compatible automatic merge permissions may be unavailable. Verify capabilities before execution; do not claim model availability or remove enforced protections to satisfy autonomy.
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
