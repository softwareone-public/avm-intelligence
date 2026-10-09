---
goal: "Correct the factory-state transport plan before implementation"
version: "1.1"
date_created: "2026-10-08"
last_updated: "2026-10-08"
owner: "Ledermayer"
status: "Draft"
tags: [critique, state, transport, workflows, concurrency]
---

# Introduction

![Status: Draft](https://img.shields.io/badge/status-Draft-lightgrey)

Critique of
[refactor-factory-state-transport-1.md](refactor-factory-state-transport-1.md).
The Git transport direction addresses the observed repeated-download problem.
The disposition below supersedes the review recommendations that follow;
historical findings remain for traceability, not as competing implementation
requirements. Do not introduce backward compatibility to resolve these gaps.

This is a review artifact, not an implementation plan and not a new v2 task.
`FIND-*` identifiers are local to this file. No workflow, state write, or
remote setting was changed during the review.

On 2026-10-08, Ledermayer clarified that AVM Intelligence is greenfield, he is
the sole maintainer, and implementation must target the desired state. Do not
add compatibility readers, dual transports, or migrations for an approach that
has already proven inadequate. Harmless obsolete operational files may remain.
Delete an obsolete state file in the normal desired-state write only when it
blocks validation or operation. This exception does not apply to accepted
evidence, receipts, or publication history.

## Revision 2 Disposition: 2026-10-08

Ledermayer clarified ownership-scoped small commits, fetch-first whole-state-decision
retries, and a complete greenfield replacement rather than an intermediate rollout.
The [version 2 plan](refactor-factory-state-transport-1.md) now governs implementation.
All resolutions below are planning resolutions only; no runtime proof is claimed.

| Finding | Disposition |
| --- | --- |
| R1 | Addressed in planning: complete transport and callers before qualification; human maintenance window, reviewed merge, manual proof and separate autonomous resumption. |
| R2 | Superseded by clarified intent: ordinary non-force publication under an explicit monotonic-branch assumption, with fresh decision retries. No custom exact-head CAS mechanism or proof against arbitrary administrator resets is required. |
| R3 | Addressed in planning: committed/rejected/unknown outcomes, final-attempt reconciliation, descendant recognition and original-result replay; implementation tests remain required. |
| R4 | Addressed in planning: one coordinator, three total decisions, injected jitter and side-effect-free callbacks; explicit module/provider/global ownership inventory. |
| R5 | Addressed in planning: one desired CLI/transport, removal of obsolete aliases/REST/migration entry points, no fallback selector; retained evidence/replay/fences remain protected. |
| R6 | Addressed in planning: one hardened Git process boundary, credential provider, hostile-config tests and fixed commit identity. |

The historical R2 requirement below must not be reintroduced by a smaller-model
implementation chunk. Follow the version 2 requirement/test IDs and human gates.
This documentation revision and its PR do not activate the transport or authorize
live operations. The diagram remains an in-progress design, not proof of exclusive
writer behavior; sequential request handoffs need explicit ownership checks.

## Current Review: 2026-10-08

**Historical recommendation, superseded by the disposition above.** This review covers the attached
plan and local source, not a live transport experiment. No tests, workflow runs,
remote writes or ruleset inspection were performed for this revision. Earlier
live assertions below are historical evidence, not newly verified facts.

### R1 - High: Phase Dependencies Put Promotion Before Writer Proof

**Evidence:** Phase 2 TASK-007 promotes factory callers after bounded manual
verification, but Phase 3 depends on Phases 1-2 and only then implements and
proves Git writes. The introduction requires one coherent replacement, while
ALT-004 rejects a permanent Git-read/REST-write backend.

**Failure scenario:** following the tasks serially either promotes a hybrid
transport before proving the final writer, or blocks Phase 2 waiting for work
that depends on Phase 2. Replacing a shared backend also changes autonomous
callers even if their workflow YAML is untouched. Manual-first adoption cannot
be established by counting edited workflow files.

**Required revision:** complete local read/write transport and transaction
fixtures first, then trusted command/workflow integration, then separately
approved manual verification and factory promotion. Define whether the complete
replacement stays unmerged until the manual gate or how reviewed revisions are
isolated for that gate. Do not invent a permanent backend selector or duplicate
production implementation. Tests must trace shared constructors and reusable
workflow callers as well as YAML steps.

### R2 - High: Atomic Expected-Head Publication Is Still An Unmade Decision

**Evidence:** REQ-004 correctly requires CAS plus fast-forward publication, but
TASK-009 postpones choosing its mechanism. The current backend checks the ref
at [the reviewed pre-replacement baseline](https://github.com/softwareone-public/avm-intelligence/blob/2d5609c97b9297a70d788b34fa882f4eff522691/internal/state/github_backend.go), then separately
sends only the new SHA and `force: false` at
[publication code](https://github.com/softwareone-public/avm-intelligence/blob/2d5609c97b9297a70d788b34fa882f4eff522691/internal/state/github_backend.go). Its behavior is not a
complete exact-head oracle for the new implementation.

**Failure scenario:** ordinary competing children of H are rejected by normal
fast-forward checks. That test alone does not prove exact-head CAS. If the
current ref is an ancestor A of expected H, C(parent H) can fast-forward A even
though A is not H. This is a required adversarial case, not a claim that an
ancestor reset occurred in production or is permitted by current policy.

**Required revision:** specify a publication protocol that submits H as the
server-checked old object ID, with C's parent fixed to H and no non-fast-forward
authorization. Explain how the implementation prevents a push client from
substituting the freshly advertised ref for H. Do not prescribe a GitHub-hosted
custom hook without proving that capability exists. Prove sibling conflict,
ancestor movement, movement after advertisement, branch deletion and shallow
parent handling before enabling writes. A CLI flag name or mocked HTTP 422 is
not protocol proof; do not introduce force-push to obtain a lease.

### R3 - High: Lost Acknowledgement Needs An Outcome State Machine

**Evidence:** REQ-008 and TEST-005 require reconciliation but do not define its
ordering. On a failed ref response, the existing
[backend](https://github.com/softwareone-public/avm-intelligence/blob/2d5609c97b9297a70d788b34fa882f4eff522691/internal/state/github_backend.go) turns any changed head into
`ErrHeadMoved`, even if it is the writer's successfully published C. The
[aggregate loop](../internal/state/delta.go#L86) returns that error on its final
attempt without another read.

**Failure scenario:** the third publication succeeds but its response is lost.
The claim is durable while the caller receives failure instead of the committed
claim result. A subsequent writer may already have advanced beyond C, so
checking only `currentHead == C` is insufficient. This is source-proven control
flow, not an observed duplicate dispatch or lost claim incident.

**Required revision:** define committed, definitely rejected and unknown
publication outcomes. Reconcile the candidate and durable event identity before
decision retry or exhaustion; return the original committed result. On failed
reconciliation reads, retain an unknown/recoverable outcome rather than claiming
rejection. Test acknowledgement loss on attempt three, a descendant of C,
unavailable verification, and recovery without a second claim or external launch.

### R4 - High: No Layer Owns Fresh Decision Re-Evaluation

**Evidence:** REQ-005 and CON-003 promise three fresh decision attempts. However,
[ApplyAggregateDelta](../internal/state/delta.go#L58) retries fixed events against
declared read-set fingerprints, and `Backend.Commit` receives an already-computed
snapshot. Neither can rerun a claim or capacity decision. The same backend error
path can also replace an authentication failure with `ErrHeadMoved` if another
writer advances concurrently.

**Failure scenario:** adding retries to Git publication can republish a stale
decision; adding another loop above existing retry loops can exceed three total
attempts. Re-evaluation that includes dispatch or projection side effects can
repeat those effects.

**Required revision:** name one decision-level transaction owner, with a pure
decision function over immutable H and one shared attempt budget. Explicitly
distinguish safe fixed-event reuse for an unchanged declared read set from a
fresh domain decision when relevant inputs change. Collection and external
effects remain outside retries. Preserve authentication, policy, validation and
unknown-outcome classifications. Test changed capacity/ownership/fences, disjoint
updates, exactly three total decision attempts, and authentication denial during
unrelated head movement.

### R5 - Medium: Greenfield Intent Conflicts With Compatibility Tasks

**Evidence:** TASK-001 preserves existing flags as compatible aliases and retains
layout migration validation; TASK-008 asks for equivalence with the REST backend;
TASK-015 calls for rollback of transport selection. None defines an external
compatibility consumer that requires these mechanisms.

**Required revision:** select one desired command contract and update owned
callers/tests together. Remove retired aliases, unused REST production transport
and obsolete migration entry points where no desired-state operation needs them.
Use domain invariants as the oracle, not byte-for-byte equivalence with existing
transport behavior. Preserve historical event identity, accepted evidence,
receipts and fences: those are active correctness obligations, not obsolete
transport compatibility. Rewrite rollback as stopping affected operations and
deploying a reviewed correction or compatible known-good code revision, with
durable state preserved; do not require a runtime REST fallback switch.

### R6 - Medium: Git Process Isolation Needs A Concrete Contract

**Evidence:** SEC-001/002 name strong goals, but TASK-003/008 do not assign the
credential provider and subprocess policy. The existing
[checkout seeder](../internal/state/checkout.go#L15) only verifies files and hashes;
it never executes Git. Moving fetch/commit/push into the application creates a
new process and credential boundary, not just an extension of that cache.

**Failure scenario:** a correct remote URL check can be undermined by inherited
URL rewrites, helpers, hooks, alternate object stores or Git environment options.
Token renewal between workflow segments does not by itself define credential
refresh inside a long operation. These are design risks, not verified exploits.

**Required revision:** name the transport-owned command runner and credential
provider. Specify allowed executables, arguments, environment/config sources,
repository endpoints and credential hosts; disable interactive prompting and
unreviewed helpers/hooks. Define fixed author/committer identity, fresh isolated
index ownership, cancellation/cleanup, expiry handling and sanitized errors.
Treat initial checkout and later subprocesses as separate boundaries. Add hostile
config/environment, expired-token, cancelled-operation and credential-leak tests.

### Already Addressed And Remaining Measurement Detail

The current plan explicitly requires fresh reads, immutable snapshot boundaries,
fail-closed fetches, object reuse, shallow-history validation, safe provenance,
credential-free compute and separate decode/validation measurement. Those are
not missing requirements and should not remain blanket blockers from the older
review. The workflow matrix now distinguishes existing checkouts from additions;
verify every command consumer during implementation rather than declaring the
whole matrix wrong based on its prior version.

Keep the zero-REST-blob target. Define benchmark host/Git version, sample count,
warm-up, state sizes, cache lifetime across separate CLI processes, and measured
boundary before using p95 <= 3 seconds as a gate. Fresh fetches may still make
network requests with zero new object bytes. Full decode/validation remains real
work. Local protocol fixtures cannot certify GitHub/App authorization, and live
manual evidence cannot certify a production latency SLA.

### Recommended Decision Order

1. Resolve R1/R2: choose publication mechanics and a feasible manual-first gate.
2. Specify R3/R4/R6: outcome handling, retry ownership and process isolation.
3. Remove R5 compatibility work; implement one transport and update owned callers.
4. Run invariant/protocol/workflow fixtures and controlled benchmarks.
5. Obtain approval for exact-scope live manual verification, then separately
	promote factory callers. Record actual evidence under TASK-038/043/028.

The findings above are review recommendations, not authorization to implement.
The older review is retained below for traceability; its statuses are historical.

## 1. Requirements & Constraints

- **REQ-001**: Judge the transport plan against current code and the desired Git transport, not against preservation of the REST writer.
- **REQ-002**: Distinguish source-proven behavior, observed runs, and live ruleset inspection from hypotheses.
- **REQ-003**: Do not treat this critique as approval to implement, dispatch, write state, or change branch rules.
- **SEC-001**: A ruleset bypass is not approval for force-push, hook execution, credential exposure, or unapproved live writes.
- **CON-001**: `main` remains pull-request protected. `protect-state` applies only to `factory-state`.

## 2. Review Findings

### Review Phase 1: Desired-State Correction

- **GOAL-001**: Remove compatibility work from the implementation path.

| Finding | Description | Severity | Status |
| --- | --- | --- | --- |
| FIND-001 | Do not retain the REST snapshot loader, prove byte-for-byte writer equivalence, preserve dual commit formats, or add a transport rollback switch. Replace the inadequate production path in the approved change and delete unused code. | Correction | Accepted |
| FIND-002 | Leave harmless obsolete `factory-state` files in place. Delete one only when it blocks the desired read or validation. Do not build a migration. | Constraint | Accepted |
| FIND-003 | The active `protect-state` ruleset targets only `factory-state`. Repository admin and `app-avm-intelligence` have Always allow. Restrict updates, restrict deletions, and block force pushes are enabled. Signed commits, pull requests, and status checks are not required. The ruleset is not a reason to retain the API writer. | Evidence | Verified |

### Review Phase 2: Blocking Design Gaps

- **GOAL-002**: Name the missing correctness mechanism before implementation.

| Finding | Description | Severity | Status |
| --- | --- | --- | --- |
| FIND-004 | Fast-forward push is not expected-head compare-and-swap. With bypass, a normal push whose parent is a stale ancestor can still advance the branch. The plan must choose either a compare-and-swap ref update or a server-side rule/hook that rejects every update whose first parent is not the current head. No force push does not provide this guarantee. | Blocker | Open |
| FIND-005 | The workflow matrix misstates current coverage. Factory dispatch, factory worker prepare/seal, and manual discovery already check out state and seed `BlobReadCache`. Manual worker, recovery, operations, projection, catalog export, evidence export, and acceptance do not consistently do so. Manual operations also omits `--state-branch`. Implement one desired command contract and close the actual gaps. | Blocker | Open |
| FIND-006 | Manual discovery remints its token inside the continuation loop, while its state checkout and listing are created once outside that loop. Every command needs fetch, exact-head verification, object reuse, and fail-closed behavior. A failed fetch must not use the stale worktree or restart REST downloads. | Blocker | Open |
| FIND-007 | `fetch-depth: 1` cannot prove an arbitrary expected parent. The plan must name the deepen/fetch operation, its failure behavior, and how it avoids executing or trusting state-branch content. | Blocker | Open |
| FIND-008 | Commit provenance needs a fixed factory author/committer, an allowlisted subject and trailer schema, length and character limits, and tests that reject tokens, private keys, raw errors, and control characters. A length bound alone is not redaction. | Major | Open |

### Review Phase 3: Non-Blocking Boundaries

- **GOAL-003**: Keep adjacent work explicit.

| Finding | Description | Severity | Status |
| --- | --- | --- | --- |
| FIND-009 | Replacing blob transport does not remove full snapshot decode and validation. Selected-aggregate loading is a separate optimization and must not be implied by the transport exit gate. | Boundary | Open |
| FIND-010 | Checkout options are currently valid only with `--discovery-module-cycle`. Extending them to every state command must not enable migration, pacing, continuation, or projection behavior on scheduled discovery. | Major | Open |
| FIND-011 | Local synthetic benchmarks, approved manual live evidence, and factory promotion are separate gates. Do not make production measurement an exit condition for merging code. | Boundary | Open |
| FIND-012 | The security boundaries are sound: no tokens in URLs or logs, no hooks or state-branch code execution, no force push, no pull/rebase, credential-free compute excluded, and admin workflows excluded. | Accepted | Verified |

## 3. Alternatives

- **ALT-001**: Preserve the REST writer until Git publication is proven equivalent. Rejected. That spends effort preserving an inadequate path in a greenfield system.
- **ALT-002**: Treat ruleset bypass as sufficient concurrency control. Rejected. Bypass permits the App update; it does not reject a stale fast-forward.
- **ALT-003**: Add a protocol-only implementation pause. Not required if FIND-004 selects the publication mechanism before coding starts.

## 4. Dependencies

- **DEP-001**: [refactor-factory-state-transport-1.md](refactor-factory-state-transport-1.md), current state backend, and the workflow coverage inspected on 2026-10-08.
- **DEP-002**: Live ruleset inspection of `protect-state` on 2026-10-08. No ruleset setting was changed.
- **DEP-003**: [AGENTS.md](../AGENTS.md) greenfield delivery guidance.

## 5. Files

- **FILE-001**: Historical baseline files `internal/state/github_backend.go`, `checkout.go` and `tree_delta.go` (removed by the approved v2 replacement), plus retained `verified_cache.go` and `delta.go`.
- **FILE-002**: `.github/workflows/factory-discovery.yml`, `manual-discovery.yml`, `manual-worker.yml`, `manual-operations.yml`, and the other state-access workflows named by FIND-005.
- **FILE-003**: `cmd/factory/native.go`, `main.go`, `discovery_cycle.go`, and `discovery_projection.go`: current option and backend ownership boundaries.

## 6. Testing

- **TEST-001**: Two commits prepared from the same head: only one publication succeeds, and the loser re-evaluates rather than pushing a stale descendant.
- **TEST-002**: Lost acknowledgement reconciles the durable event without a duplicate commit or worker launch.
- **TEST-003**: Same-head repeated reads use zero blob downloads. A changed head fetches only missing objects.
- **TEST-004**: Invalid repository, symlink, replaced object, corrupt hash, token expiry, and fetch failure stop with sanitized diagnostics.
- **TEST-005**: Workflow fixtures prove checkout consumption only for trusted state jobs and preserve current discovery, dispatch, credential, and trigger scope.
- **TEST-006**: Commit metadata rejects secret and control-character inputs without changing evidence or event hashes.

## 7. Risks & Assumptions

- **RISK-001**: Selecting the wrong publication mechanism makes every later workflow adoption unsafe. Resolve FIND-004 first.
- **RISK-002**: Deleting obsolete operational files can remove data that is still referenced. Delete only a file that blocks validation or the desired operation.
- **ASSUMPTION-001**: Harmless obsolete operational state may remain indefinitely.
- **ASSUMPTION-002**: Accepted evidence, receipts, and publication history remain outside the obsolete-file deletion rule.

## 8. Related Specifications / Further Reading

- [Transport plan](refactor-factory-state-transport-1.md)
- [State transport discussion](../docs/diagrams/factory_lifecycle.md#state-transport-and-atomic-writes)
- [Agent guide](../AGENTS.md)
- [Findings](../docs/FINDINGS.md)
- [Tasks](../docs/TODOS.md)
