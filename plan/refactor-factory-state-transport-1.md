---
goal: "Replace REST state transport with verified Git reads and small ownership-scoped commits with fetch-first decision retries"
version: "2.0"
date_created: "2026-10-08"
last_updated: "2026-10-08"
owner: "Ledermayer"
status: "Planned"
tags: [state, transport, workflows, concurrency, performance]
---

# Introduction

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

Deliver one complete Git fetch/read/commit/push replacement for `factory-state`.
Build and verify the complete transport, transaction coordinator, command callers
and workflows locally before live activation. Implementation chunks are work
units, not independently deployable intermediate architectures. Remove obsolete
REST state readers/writers and transport-specific aliases before integration is
declared complete. No dual backend, migration framework or runtime fallback.

Ledermayer clarified on 2026-10-08 that atomic publication means one small commit
per operation within explicit ownership boundaries. Every attempt fetches fresh
state and reruns the state decision after a confirmed publication conflict.
Module ownership is enforced by durable claims and fences; Issues only project
that ownership. Provider-type and global ownership require independent checks.
This revision supersedes v1's custom exact-head CAS requirement and phase order.
It does not remove stale-owner, preservation or durable identity guarantees.

This document update authorizes no implementation, workflow dispatch, merge,
pause/resume, state write, policy change or deployment. Human gates below require
explicit approval. Status remains Planned until implementation is authorized.

This is a local execution breakdown of existing v2 TASK-038 state-layout/transport,
TASK-013/014 ownership, TASK-043 integration and TASK-028 rollout work. All TASK,
TEST, GATE and HUMAN identifiers below are local to this file. Version 2 replaces
the old local TASK-001 through TASK-015 descriptions; none was marked complete.

Observed baseline: discovery run `37723675785`, job `113136842703`, revision
`a57f1be3` downloaded 1,336 blobs twice with zero reuse. Reads took approximately
3m37.6s and 3m37.0s; the final expected-head commit took approximately 2.9s.
These are observed timings, not an SLA. Snapshot means immutable state data,
not a GitHub Actions artifact.

## 1. Requirements & Constraints

- **REQ-001**: Every trusted factory/manual job accessing state must obtain a separate verified checkout and pass it to every state command. Admin CI, provider-only collection/export, credential-free compute and pure artifact-to-data proposal construction do not receive incidental state access.
- **REQ-002**: Before every public state read or transaction attempt, fetch the configured branch and resolve exact head H. Domain calculations read one consistent snapshot at H; do not refetch between individual record reads inside a transaction.
- **REQ-003**: Cache verified blobs by Git object ID and retain them across operations in a process/job. Same-head repeated reads perform zero full REST blob downloads. Changed heads load only missing objects through Git transport.
- **REQ-004**: A successful mutating operation publishes one commit C with exactly one parent H, the fetched decision snapshot. Build from H plus only the validated operation delta and publish by ordinary non-force fast-forward push to the explicit state branch. Concurrent sibling commits cause rejection and fresh evaluation. No custom CAS protocol, server hook, force flag, force-with-lease or REST ref-write fallback is required or permitted. This relies on the monotonic branch assumption in CON-004, not arbitrary-ref-movement CAS equivalence.
- **REQ-005**: One coordinator owns the entire three-attempt decision budget. A confirmed contention rejection discards the candidate and reruns fetch/read/decision/validation/commit/push. Recheck ownership, generations, eligibility, relevant shared inputs, capacity and fences. Do not pull, merge, cherry-pick, rebase or replay a precomputed stale snapshot. No nested retry loops in transport, aggregate APIs or callers.
- **REQ-006**: Build each candidate with an isolated temporary Git index seeded from H's tree; stage validated changed paths and explicit authorized deletions only. A small commit means one logical operation and its necessary metadata, not a fixed arbitrary file limit. Preserve all unrelated bytes; never stage a whole worktree or infer deletion from unloaded records. No-op decisions produce no commit.
- **REQ-007**: Fetch/read failure stops the operation. Do not silently use stale checkout contents or restart a full REST download. Missing checkout support must fail visibly before a workflow advertises compliance.
- **REQ-008**: Readers expose immutable snapshots. Writers retain decision-head validation, monotonic fences, stable operation/event identities and contract/layout validation. Reconcile uncertain publication before retry or exhaustion, including on attempt three. Classify outcomes as committed, definitely rejected or unknown; do not turn an unknown result into a new claim.
- **REQ-009**: Use meaningful commit subjects and allowlisted provenance trailers. Changes to transport metadata must not alter evidence digests or caller-supplied timestamps.
- **REQ-010**: The acceptance target is removal of repeated full REST blob reads, not removal of snapshot validation or guarantees of 1-3 seconds. Measure cold/warm latency, transfers, head conflicts and validation cost independently.
- **REQ-011**: Maintain one durable active compute claim per module across acquisition and assessment. Only a confirmed committed claim permits dispatch. Multiple dispatcher processes, manual/factory callers and recovered stale workers must obey the same claim/fence check. Issue uniqueness and workflow concurrency groups are not locks.
- **REQ-012**: Enumerate every writer and decision read set. Dedicated service paths, immutable shared appends and explicitly authorized mutable handoffs are distinct categories. Discovery, projection, receipts and recovery are not implicitly excluded while compute runs; validate their actual record ownership. Do not claim that a single compute job serializes all module writes.
- **REQ-013**: Shared append IDs are stable for the logical operation. An existing identical record is an idempotent success; the same ID with different content fails closed. Shared/global decisions and provider-type records cannot rely on a module claim for exclusivity. Do not implement new provider lifecycle stages or new global capacity policy in this transport change.
- **REQ-014**: Retain one desired CLI contract: `--state-branch` and `--state-checkout-root` are required for state commands; Git derives each exact-H listing internally. Remove externally supplied listing flags and dispatch/worker transport aliases with their owned callers. State options are invalid on commands with no state access. Do not couple checkout support to discovery cycle, pacing or layout migration modes.
- **SEC-001**: State Git authentication uses the narrow App installation token through a nonpersisted credential helper/askpass or equivalent reviewed process boundary. Never place tokens in remote URLs, arguments, logs, commit messages or cached Git configuration; refresh expiring tokens and clean up credential files on every exit.
- **SEC-002**: One transport-owned subprocess runner uses explicit argument arrays, a resolved trusted Git executable and an allowlisted environment/configuration. Set `GIT_NO_REPLACE_OBJECTS=1` and disable prompts, hooks, signing, arbitrary helpers, redirects and non-HTTPS production protocols. Ignore ambient system/global config; reject unreviewed local includes, URL rewrites, proxy/helper overrides, alternates, grafts and external object/worktree paths. Never run checkout filters, textconv or code from state. Verify repository identity, ref, object hashes, regular file modes and paths; reject symlinks, escapes, replace objects and mixed layouts.
- **SEC-003**: Keep state-only writer credentials separate from Issue/Project credentials and credential-free compute. Trusted code comes from reviewed main, not the state branch.
- **SEC-004**: The CLI owns a refreshable state-token provider; Git receives only a scoped credential boundary, never App private keys or projection credentials. Fetch/push acquire a nonexpired token for the expected host/repository. Static-token callers fail with a typed expiry error if refresh is unavailable. Token refresh is not a decision retry. Credential files, if necessary, live in a private temporary directory with mode 0600 and are removed on success/error/cancellation; tests inspect logs, arguments and repository config for leaks.
- **CON-001**: Preserve all existing triggers, schedules, selectors, new-module-only discovery, force semantics, environment gates, action pins and protected-main review. This standard does not broaden discovery or launch workers.
- **CON-002**: Initial job checkout is depth 1, as in factory-dispatch. Incremental fetch must explicitly validate shallow history behavior; deepen only as required to prove parent/head relationships.
- **CON-003**: Three total decision attempts, not three retries. After confirmed contention on attempts 1 and 2, wait 100 ms plus uniformly sampled jitter in [0, 200 ms], respecting context cancellation. Inject sleeper/randomness in tests. After attempt 3, reconcile uncertain publication if necessary and return committed, unknown or retryable-conflict as appropriate. Authentication, policy, integrity, schema and ownership failures do not consume a contention retry. Preserve existing external API pacing and server-directed waits.
- **CON-004**: Ordinary writers only advance the branch, never reset/delete/recreate it. Verify effective policy and approved writer behavior at the live gate; bypass privileges do not authorize nonmonotonic writes. Detect observed branch deletion or non-descendant movement and stop for human investigation. This does not promise detection of an unseen reset-and-restore between observations. Strict CAS against arbitrary administrator resets is outside this design; requiring it would require renewed design approval.
- **CON-005**: Preserve active accepted evidence, receipts, replay identities and fence values. Remove obsolete transport/migration entry points, not the metadata needed to validate retained history. Harmless obsolete operational files may remain; delete only explicitly identified blockers through an authorized normal transaction. No bulk reset, evidence rewrite or new layout migration.

### Transaction And Ownership Contract

Reader: authenticate -> fetch branch -> verify H and object identities -> decode
required state at H -> return immutable snapshot. No commit is produced.

Writer: stable operation identity -> fetch H -> reconcile existing operation ->
pure decision at H -> validate ownership/read set/delta -> build C(parent H) ->
non-force push -> classify/reconcile outcome. A confirmed conflict restarts the
whole state decision from fetch. No-op returns its verified snapshot/result.
Reads after successful writes fetch again; returning the current committed result
does not require a redundant read.

The coordinator accepts a pure decision callback plus a domain result resolver.
The callback receives the immutable snapshot and fixed caller inputs, and returns
the declared read/write scopes, events/delta and proposed result. It cannot access
the network, execute Git, dispatch work, mint tokens or mutate external state.
The resolver reconstructs the original committed result from durable records on
replay. Domain packages retain typed public APIs; the transport never chooses a
module or computes a fence. Reuse existing event digests/replay metadata instead
of introducing a parallel operation ledger. Caller-supplied timestamps and
logical operation identity stay fixed across contention attempts; actual lease
validity is rechecked against an injected current clock before publication.

| Scope | Permitted mutation | Required proof |
| --- | --- | --- |
| Module request/attempt | Explicit discovery creation and dispatcher/worker/recovery handoffs | Current request/generation, active module claim, allowed transition, lease/run identity and fence as applicable; no blanket service ownership assumption |
| Module projection/receipt/selection | Only the owning reducer's declared fields/records | Own lease/fence or exact receipt/current-input checks; compute claim is not a substitute |
| Immutable shared append | Create one stable-ID record, never overwrite | Absent ID or byte/content-identity-equivalent replay; differing content is an integrity conflict |
| Global control/catalog/capacity | Narrow reducer-owned transaction | Explicit shared read set re-evaluated on every attempt; preserve unrelated module state |
| Provider resource type | Canonical full provider address plus kind/type and required version/context | Independent existing owner/fence or append identity; module exclusivity does not authorize provider writes; absent lifecycle remains out of scope |
| Layout/transaction metadata | State serializer only | Preserve contract and durable replay/fence invariants; include required metadata in the same operation commit |

Request paths in the in-progress diagram have multiple sequential writers.
Record these as mutable handoffs in the ownership inventory; do not silently
redesign them as event sourcing or add a module-wide distributed mutex.

### Publication Outcomes And Side Effects

| Outcome | Coordinator action | External action |
| --- | --- | --- |
| Push confirms C, or verified state contains matching durable operation | Return original committed result and accepted commit identity | Existing idempotent handoff may proceed |
| Confirmed non-fast-forward/ref-lock contention without accepted operation | Discard candidate; bounded jitter; fetch and recompute if budget remains | None |
| Authentication/policy/validation failure | Preserve exact safe error classification; stop | None |
| Timeout/disconnect/ambiguous response | Fetch and check candidate ancestry plus durable operation identity/content; current tip may be a descendant of C | None until commitment is proven |
| Reconciliation proves no acceptance on a valid monotonic descendant of H | Treat as definitely rejected and retry within remaining decision budget | None |
| Reconciliation unavailable, nonmonotonic or contradictory | Return typed unknown with safe operation/candidate IDs; recover by same identity, not a new operation | Park; never blindly relaunch |

Reconciliation is attempted even after the final push attempt and does not create
a fourth decision attempt. A cancelled/expired context returns unknown if proof
cannot be obtained; do not ignore cancellation to force a read. Repeated calls
resolve the same logical operation before preparing another candidate.

Source collection, computation, Issue mutations, workflow launches and artifact
delivery remain outside the callback. Dispatch is claim -> confirmed commit ->
existing exact-attempt launch/recovery -> separate run-binding transaction.
Uncertain launch outcomes use existing run lookup/recovery, not state-loop replay.
Do not claim exactly-once external execution merely from a Git commit.

### Git Objects And Process Boundary

The checkout supplies the job-local object database, not trusted worktree data.
Fetch the explicit configured branch, resolve H, validate its tree, and read raw
objects by OID. Generate any NUL-delimited listing inside this boundary at H;
never reuse an external listing from a previous command. Blob bytes may be reused
by OID; decoded snapshots may only be reused for the same verified H and contract.
Separate CLI processes reuse Git objects on disk, not an assumed shared Go cache.
Fetch still occurs for every public read and transaction attempt.

Start depth 1. For ancestry needed by reconciliation or nonmonotonic detection,
deepen that branch in increments of 64 commits, up to 4096 additional commits per
operation. If proof remains unavailable, stop with unknown/integrity classification
as appropriate; do not guess or download state through REST. These are named,
testable limits, not authorization to fetch other refs. Serialize concurrent use
of one transport instance; separate processes use separate checkout directories.

Use a private temporary index initialized from H, raw object writes, tree creation
and commit creation without hooks/filters/signing. Validate C's parent/tree before
push. Production remote and push target are explicit and credential-free, never
derived from branch tracking/push config. Local bare remotes are test-only injection
points unavailable to production CLI options. Preserve repository safety checks.

### Commit Provenance

Example commit, with absent identities omitted rather than guessed:

```text
state(discovery): record catalog observation for 2 modules

Factory-Operation: discovery.persist
Factory-Repository: softwareone-public/avm-intelligence
Factory-Workflow: factory-discovery.yml
Factory-Run: 37723675785
Factory-Run-Attempt: 1
Factory-Job: persist
Factory-Head: <expected-parent-sha>
Factory-Event: <durable-event-or-transaction-id>
Factory-Module: <canonical-module-id-for-single-module-operations>
Factory-Request: <request-id-if-present>
Factory-Attempt: <attempt-id-if-present>
Factory-Fence: <fence-if-present>
```

Allowlist operation/component names; bound subject length to 100 characters and
trailer values to 256 characters; reject control characters and credential-like
inputs. Record commit SHA and safe run URL in operational logs after successful
publication. Group transactions report counts and durable transaction IDs, not
unbounded lists or payload dumps. Lost acknowledgements compare durable event
identity and accepted head, not commit message text alone.

Fix author and committer to `AVM Intelligence Factory` and
`factory@avm-intelligence.invalid`; do not inherit a runner's Git identity.
Commit timestamps are transport metadata; they do not change caller timestamps or
evidence hashes. Rejected local candidate objects are not published operations.
Emit one terminal CLI error; operation-level incomplete events use WARN with
allowlisted categories, module/phase, attempt count and elapsed time.

### Workflow Coverage Matrix

| Workflow | State-access jobs | Required treatment |
| --- | --- | --- |
| factory-discovery | persist, including subsequent projection command | Add verified checkout; enable shared checkout support for persistence and projection without module-cycle/new-scope changes. |
| manual-discovery | persist | Preserve existing checkout and continuation scopes; let the transport fetch and derive the listing for every operation, including resumed segments. |
| factory-dispatch, manual-dispatch, manual-dispatch-validation | claim-and-dispatch | Preserve checkout and exact claims; use common fetch-before-operation implementation. |
| factory-worker | prepare, validate-and-seal | Preserve existing trusted checkouts; share reader/writer contract. Compute and pure proposal preparation remain excluded. |
| manual-worker | prepare, validate-and-seal | Add verified checkout and common options; leave credential-free compute and pure proposal preparation unchanged. |
| manual-worker-validation | reusable worker call | No steps permitted in reusable-call job; inherit checkout in factory-worker trusted jobs. |
| manual-recovery | inspect-and-recover | Add checkout; reuse objects across inspection and verified recovery with a fresh head for each operation. |
| manual-operations | operate | Add checkout to authorized state controls; retain operator allowlist and audit/fencing. |
| manual-projection-flush, manual-projection-recovery | project | Add checkout for state intents, leases and acknowledgements; never add discovery or dispatch. |
| manual-catalog-export | export | Add checkout for durable catalog outbox reads. Main review-branch delivery remains separate. |
| manual-evidence-export | export | Add checkout for exact attempt inspection/seal validation and repeat commands. |
| manual-accept-evidence | import | Add checkout for exact attempt/receipt verification and fenced acceptance import. |
| manual-provider-collect, manual-provider-export | none currently | Do not add state tokens or checkout without a separately reviewed state dependency. |
| admin-ci, admin-proposal-validation | none | Excluded; retain existing PR trust boundaries. |

## 2. Implementation Steps

### Execution Rules For Small Chunks

Each task is one handoff unit. Read this section, the task, its referenced tests
and nearest AGENTS/instructions before editing. Do not explore the entire roadmap.
Before implementation starts, HUMAN-001 must be approved. Each task owner records
changed files/symbols, exact checks/results, unresolved blockers and the next ready
task in the completion column's linked evidence or a short checkpoint below its
phase. Do not create a separate task register. Change `No` to `Yes` only after the
task's named acceptance tests pass; date it and cite the verified code revision.

Dependencies are completion dependencies, not permission to deploy. A missing
prerequisite stops that task. No model may weaken a test, change the desired
contract or widen live scope to get past a blocker. Ask Ledermayer for an explicit
design amendment when a required invariant is impossible. Shared files have one
owner at a time; parallel agents never edit the same file. A coordinator integrates
parallel results and reruns the combined package tests before releasing successors.
Passing fixtures is not live activation. Do not commit/create branches without
separate authorization, even when work is split into many local chunks.

Test groups contain assertions delivered at different layers. A task-level test
reference requires the assertions assigned below to that task and its completed
prerequisites, not assertions owned by successors. Record the remaining assertion
owner at handoff; do not mark an entire test group passed from a partial fixture.
All TEST-001 through TEST-015 assertions are mandatory before TASK-018 releases
GATE-002. This rule also governs phase exits and GATE-001.

| Delivering Task | Assertions Required At That Boundary |
| --- | --- |
| TASK-001 | In-memory API/decision/outcome contract fixtures only; no Git, CLI or domain integration claims |
| TASK-002 | Subprocess isolation, credential cleanup and injected expiry/cancellation fixtures; CLI credential wiring remains TASK-011 |
| TASK-003 | Fresh-read/cache/object-integrity and shallow read proofs; publication/reconciliation ancestry remains TASK-005 |
| TASK-004 | Tree/delta/provenance fixtures; publication-completion logging remains TASK-005/006 and CLI stream integration TASK-011 |
| TASK-005 | Actual Git publication races, single-attempt outcome/ancestry classification and fault injection; bounded decision loop and original-result reconciliation remain TASK-006 |
| TASK-006 | Complete transport/coordinator retry/reconciliation/side-effect contracts with synthetic decision callbacks; actual domain callers remain TASK-008/009/010 |
| TASK-007 | Review/tests for assertions delivered through TASK-006 only; no claim that domain, CLI or workflow integration is complete |
| TASK-008, TASK-009, TASK-010 | Respective domain assertions and adjacent package regressions; TEST-012 CLI assertions remain TASK-011 |
| TASK-011 | Command/credential/flag/provenance fixtures; TEST-013 checks common argument contract with local command fixtures, while actual workflow adoption remains TASK-013/014 |
| TASK-012 | Removed-backend/alias checks and converted-command no-REST fixtures; full suite after workflow conversion at TASK-017 |
| TASK-013 | Actual manual workflow assertions; factory/reusable end-to-end caller coverage completes at TASK-014 |
| TASK-014 | Complete manual/factory/reusable workflow assertion set |
| TASK-016, TASK-017, TASK-018 | Benchmarks, integrated full suite and complete-diff review; no partial test-group acceptance at release gate |

The APIs below are proposed implementation names, not claims that they exist.
Freeze concrete Go signatures in TASK-001 before dependent work; prefer extending
existing types/helpers over introducing duplicate abstractions. Use existing
neighboring test files; create only cohesive transport/coordinator files where
the existing file has no suitable ownership. No compatibility wrappers.

### Implementation Phase 1: Contract And Transport Foundation

- **GOAL-001**: Freeze the executable transaction boundary and establish verified reads. Exit: TASK-001 through TASK-004 pass; no workflow is activated.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- | --- |
| TASK-001 | HUMAN-001 / Coordinator | In `internal/state/store.go`, define the desired `RunTransaction` decision/result-resolution contract and typed publication outcomes; retain `ReadSnapshot` as the public fresh-read boundary. Inventory all `Backend`, `CommitExpectedHead`, `ApplyEventsAtHead`, `ApplyAggregateDelta` and constructor consumers using targeted search. Record exact owning symbols/read-write scopes in this plan's checkpoint, including concrete files under `internal/catalog`, `internal/discovery`, `internal/queue`, `internal/github` and `internal/publish`. Define desired `GitBackend`, credential-provider and command-runner signatures with no runtime selector. Use in-memory fixtures to establish TEST-003/004/006 assertions. Exit: package compiles, signatures and owner map are recorded, no unmapped state writer remains. | Yes (local; pending branch commit) | 2026-10-08 |
| TASK-002 | TASK-001 / Transport | Implement the single hardened subprocess boundary in `internal/state/git_transport.go` and neighboring tests, with injectable test runner, context cancellation, process/config allowlists, credential broker cleanup and safe diagnostic categories. Production accepts only the configured HTTPS repository; local bare remotes are test injection only. Implement SEC-001 through SEC-004 and TEST-008/009. Do not edit CLI or workflows. | Yes (local; PR delivery pending) | 2026-10-08 |
| TASK-003 | TASK-002 / Transport | Implement `GitBackend` fresh fetch/H verification/raw-object reading in `internal/state/git_backend.go`; adapt `checkout.go` and `verified_cache.go` for OID reuse, no stale listings, same-H decoded reuse and per-instance serialization. Enforce shallow proof limits and immutable returned snapshots. TEST-001/002/007 pass with real temporary bare Git repositories and no credentials. No REST fallback on a failed read. | Yes (local; PR delivery pending) | 2026-10-08 |
| TASK-004 | TASK-003 / Transport | Add candidate construction from H plus validated delta in `git_backend.go` and `tree_delta.go`: isolated index, explicit removals, one-parent C, object verification and bounded provenance in `git_provenance.go`. Retain logical event/fence identities while removing reliance on REST tree payloads. TEST-005/010 verify unchanged bytes, no-op, deletion, metadata and secret rejection. | No | Pending |

**TASK-001 checkpoint (2026-10-08; baseline `df372dca89e92f3d47b5272c70d35881e5f56d03`):**
The proposed contracts are declared in `internal/state/store.go`: `GitBackend`,
`GitCandidate`, typed `PublicationOutcome`, `TransactionCoordinator`,
`TransactionDecisionFunc`, `TransactionResultResolver`, `StateCredentialProvider`,
and `GitCommandRunner`. The in-memory contract fixture is
`TestTransactionContractCarriesDecisionScopeAndPublicationOutcomes` in
`internal/state/store_test.go`. Local acceptance is complete; the change remains
uncommitted pending branch commit, so no implementation revision is available to
cite yet. `gofmt -w internal/state/store.go
internal/state/store_test.go` and `go test ./internal/state -count=1` pass.

Writer ownership inventory at this baseline:

- `internal/queue`: `ClaimNext`, `ClaimRequest`, `ClaimRequestWithLifecycle`,
	`ClaimModule`, `BindActionsRun`, `BindActionsRunWithLifecycle`, `Heartbeat`,
	`RecoverAttempt` and `ApplyOperationalCommand` in `dispatch.go` and
	`operations.go`; claims use `commitClaimEvents` and may call
	`ApplyAggregateDelta`, while lifecycle/request/operations transitions call
	`ApplyEventsAtHead`. Queue also owns `ClaimModuleRequest` as the pure request
	reducer. Acceptance, assessment-input import, selection/deselection/export
	acknowledgement and deployment transitions currently live in `internal/state`
	(`ImportEvidenceReceipt`, `ImportAssessmentInputs`, `SelectAccepted`,
	`DeselectRetired`, `ImportSelectionReceipt`, `PrepareDeployment`,
	`AuthorizeDeployment`, `ImportDeploymentReceipt`), not in queue.
- `internal/discovery`: `Persist` in `transaction.go`, `Cycle` in `cycle.go`, and
	continuation read/parking/checkpoint operations in `continuation.go` own catalog
	observation, pin, request, projection-intent and cursor writes. Source resolution
	and delivery are outside the state transaction. `Cycle` calls
	`ApplyAggregateDelta` for declared owner/shared reads and otherwise uses
	`ApplyEventsAtHead`.
- `internal/github`: `ReplayAcknowledgedProjections`, `SaveProjectionIntent(s)`,
	`ClaimProjectionBatch(ForModules)`, `RecordProjectionOutcome`,
	`CheckpointProjectionSnapshot` in `projection_state.go`, and
	`QueueRunEvidence(Batch)` in `run_evidence.go` own projection/delivery records;
	Issue/Project mutations remain outside state reducers. Their common event path
	is `commitProjectionEvents` -> `ApplyEventsAtHead`.
- `internal/catalog` and `internal/publish`: targeted search found no
	`state.Backend`, `ReadSnapshot`, `CommitExpectedHead`, `ApplyEventsAtHead`,
	`ApplyEvent(s)` or `ApplyAggregateDelta` consumers. They construct/validate
	catalog and proposal data; durable state mutation is owned by discovery/state.
- `internal/state`: `ApplyEvent(s)`, `ApplyEventsAtHead`, `CommitExpectedHead`,
	and `ApplyAggregateDelta` are the current generic mutation APIs. Layout
	migration, evidence acceptance, input import, selection/export receipts and
	deployment prepare/authorize/receipt imports are concrete writers listed above
	or implemented in `layout.go`, `evidence.go`, `inputs.go`, `selection.go` and
	`deployment.go`. The current `GitHubBackend` is the REST implementation targeted
	for replacement; CLI setup constructors are `newFactoryStateBackend` and
	`newVerifiedFactoryStateBackend` in `cmd/factory/main.go`, with checkout seeding
	in `seedDispatchCheckout`.

No writer was found under `internal/catalog` or `internal/publish`; all discovered
durable state writers are assigned to the state, queue, discovery or GitHub owner
lanes above. Existing `ApplyAggregateDelta` has its own retry loop over prebuilt
events; TASK-008/009 must replace this path with coordinator decision callbacks,
not preserve that retry as a nested adapter. No domain caller, CLI setup, Git
transport or workflow was changed in TASK-001.

**TASK-002 checkpoint (2026-10-08; branch `feat/git-state-transport-runner`):**
Added `SafeGitCommandRunner` and `FileStateCredentialProvider` in
`internal/state/git_transport.go`. Git calls use an explicit executable,
allowlisted subcommands and argument vectors, a clean environment with global and
system config disabled, replacement objects disabled, prompts/hooks/signing and
non-HTTPS protocols blocked, and bounded output. Failures return safe typed
categories without raw stderr; credential expiry remains a typed error. Credential
tokens live only in mode-0600 temporary
files; a mode-0700 generated askpass helper is bound through `GIT_ASKPASS` and
removed with its directory on callback success/error. Invalid targets, expired
contexts, and provider/callback errors fail closed with safe diagnostics.
`internal/state/git_transport_test.go` verifies actual local Git execution,
hostile command/config/environment rejection, credential-free HTTPS URL checks,
typed safe errors and credential expiry, askpass username/password behavior, and
cleanup on success, failure, and callback cancellation. `gofmt`,
`go test ./internal/state -count=1`, and `go test -race ./internal/state -count=1`
pass; editor diagnostics report no errors. This is local fixture evidence only:
no App token, production GitHub authentication, CLI credential wiring, or live
state operation was exercised. The changes are uncommitted pending PR delivery.

**TASK-003 checkpoint (2026-10-08):** Added `VerifiedGitBackend` in
`internal/state/git_backend.go`. Each `Read` and `Head` validates the trusted
checkout identity/configuration, obtains an operation-scoped credential, performs
a fresh explicit depth-1 fetch of only the configured HTTPS branch, resolves the
exact `FETCH_HEAD` commit/tree OIDs, and reads raw blobs by OID. The checkout
worktree is never read as state; immutable blob bytes are hash-verified and
reused by object ID across reads, while every read observes the remote ref again.
The reader rejects non-regular/symlink/executable paths, non-state paths,
malformed or duplicate listings, invalid objects, hostile local URL/include/
credential/ref config and fetch failures even when prior objects are cached.
Package-private local bare-remote injection is available only to state-package
fixtures; production URLs remain credential-free HTTPS and fetch history controls
remain fixed to depth 1. `internal/state/git_backend_test.go` covers cold/warm
reads, immutable snapshots, changed-head incremental object reads, failed fetch,
unsafe config and hostile tree listings against real temporary bare repositories.
`gofmt`, `go test ./internal/state -count=1`, `go test -race ./internal/state
-count=1`, and editor diagnostics pass. This proves local Git fixtures only; no
GitHub/App authentication or live state reads were performed. `Publish` remains
unimplemented until TASK-005; CLI wiring remains TASK-011. TASK-003 changes are
uncommitted pending PR delivery.

### Implementation Phase 2: Publication And Decision Retry

- **GOAL-002**: Prove the finished Git writer and coordinator locally before command adoption. Exit: TASK-005 through TASK-007 pass; no live writes.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- | --- |
| TASK-005 | TASK-004 / Transport | Add explicit non-force push and committed/rejected/unknown classification in `git_backend.go`. Use real bare-remote races for sibling rejection and movement during publication; stop on observed nonmonotonic/deleted branches. Add acknowledgement-loss fault injection after a successful real ref update, not only mocked rejection. Implement bounded ancestry proof. TEST-003/006/007/009 pass. Transport performs no automatic decision retries. | No | Pending |
| TASK-006 | TASK-005 / State | Implement `RunTransaction` in `internal/state/transaction.go` with three total decision attempts, injected clock/jitter/sleeper, durable identity reconciliation and original result resolution. Adapt `store.go`/`delta.go` to remove nested retry behavior; convert unsafe event-only retry entry points to explicit decision callbacks. Immutable append handling follows REQ-013. TEST-003/004/006/011 cover final-attempt lost ack, changed read sets, differing replay payload, cancellation and retry counts. Domain wrappers need not yet be fully converted, but no temporary adapter may retry a stale snapshot. | No | Pending |
| TASK-007 | TASK-006 / Reviewer | Run focused state tests/race tests and obtain read-only Factory Contract Reviewer review of TEST-001 through TEST-011, especially preservation, outcome classification and side-effect exclusion. Repair blocking findings in the owning task and rerun its tests. Record GATE-001 evidence; do not declare GitHub/App authentication proven by a local remote. | No | Pending |

### Implementation Phase 3: Domain And Command Conversion

- **GOAL-003**: Every existing state operation uses the same verified transport and decision retry contract. Exit: no production REST state path, alias or nested retry remains.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- | --- |
| TASK-008 | TASK-007 / Queue | In `internal/queue/dispatch.go` and its neighboring tests, convert `ClaimNext`, `ClaimRequest`, `ClaimRequestWithLifecycle`, `ClaimModule`, run binding, heartbeat and recovery to pure decision callbacks plus committed-result resolvers. Preserve exact-request scope, one active module claim and stale-fence rejection. Convert queue-owned operations, acceptance and selection writers found by TASK-001 within this same exclusive queue ownership lane; complete each symbol family and its tests before the next. TEST-004/006/011/012 pass. No dispatch call inside a callback and no new global capacity policy. | No | Pending |
| TASK-009 | TASK-007 / Discovery | Convert the TASK-001 writer inventory in `internal/catalog` and `internal/discovery`, including continuation checkpoints, to coordinator-backed decisions. Keep remote source resolution outside callbacks; retries validate the fixed collected observation against current state and park if it is no longer applicable. Preserve exact scope, continuation/pacing deadlines and projection IDs. TEST-011/012 plus existing discovery/continuation fixtures pass. No edits to queue/state/CLI files. | No | Pending |
| TASK-010 | TASK-007 / Projection And Publication | Convert the TASK-001 writers in `internal/github` and `internal/publish` to coordinator-backed lease/ack/receipt/publication decisions. Keep Issue/Project mutations, artifact reads and proposal delivery outside callbacks. Preserve human notes, original committed results and exact receipts. TEST-006/011/012 plus existing package fixtures pass. Queue-owned acceptance/selection code belongs to TASK-008, not this task. | No | Pending |
| TASK-011 | TASK-008, TASK-009, TASK-010 / CLI Integrator | In `cmd/factory/main.go` and `native.go`, replace `newFactoryStateBackend`, `newVerifiedFactoryStateBackend` and `seedDispatchCheckout` with one instance-scoped verified setup boundary. Wire `--state-branch`/`--state-checkout-root`, refreshable state credentials and provenance through every mapped caller: `discovery_cycle.go`, `discovery_projection.go`, `catalog_proposal.go`, `evidence_proposal.go`, `evidence_acceptance.go`, `work.go` and any additional TASK-001 owners. Remove external listing flags and old transport aliases together; reject state options on non-state modes. TEST-009/012/013 and existing CLI tests pass. Preserve JSON/token stdout isolation. | No | Pending |
| TASK-012 | TASK-011 / Integrator | Remove unused production REST state implementation from `github_backend.go`, stale seeding wrappers and obsolete transport/migration entry points across the mapped owners. Move still-needed App token/error/pacing utilities to their proper owner rather than deleting unrelated GitHub APIs. Retain replay metadata needed by current durable records; no new layout migration. Search all constructor/interface/flag references again and record zero obsolete production callers. Update transport-specific tests to desired invariants instead of REST byte-equivalence. Run focused removal/converted-command checks; TEST-014 proves no state REST calls. Full suite follows workflow conversion at TASK-017. | No | Pending |

TASK-008, TASK-009 and TASK-010 may run in parallel after GATE-001 because their
domain directories are disjoint. They must not edit the frozen shared state API.
If a shared API change is needed, stop affected lanes, let the coordinator make
and validate it once, then resume. Within TASK-008, symbol families are serial
subchunks: claims -> binding/heartbeat/recovery -> controls -> acceptance/selection.
Within TASK-009: catalog -> discovery -> continuations. Within TASK-010: projection
leases/acks -> publication/receipt writers. Record each subchunk's tests at handoff.
Everything else is serial unless this document explicitly allows parallel work.

### Implementation Phase 4: Complete Workflow Integration

- **GOAL-004**: Wire both manual and factory jobs to the final command contract in the same replacement change. Exit: executable workflow contracts pass with schedules and trust boundaries unchanged. Integration is not activation.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- | --- |
| TASK-013 | TASK-012 / Workflow Integrator | Update all manual state jobs in the coverage matrix to pinned depth-1 separate checkouts, explicit repo/ref, `persist-credentials: false`, common branch/root flags and operation-time credentials. Remove shell-generated listing arguments; the CLI verifies H internally. Include explicit branch on manual operations. Preserve continuation scope, token renewal and reusable-call syntax; no compute/provider-only/admin state access. In `cmd/factory/catalog_proposal_test.go`, `provider_workflow_test.go` and neighboring workflow tests, execute shell/selection fixtures and verify job-level credential boundaries. TEST-013 passes. | No | Pending |
| TASK-014 | TASK-013 / Workflow Integrator | Update factory discovery, dispatch and trusted worker jobs in the matrix to the same completed transport contract, including separate projection commands. Preserve `--discovery-new-modules-only`, transaction `projection_module_ids`, action pins, schedules, permissions and environments. No scheduled workload change or live dispatch. Run combined manual/factory/reusable workflow fixtures, not string-only assertions. TEST-013 passes with no skipped required tools. | No | Pending |
| TASK-015 | TASK-014 / Documentation Integrator | Update `docs/factory-operations.md`, `docs/diagrams/factory_lifecycle.md`, `.github/instructions/go-development.instructions.md`, `.github/instructions/github-operations.instructions.md` and `.github/workflows/AGENTS.md` to the verified Git-only contract. Remove obsolete guidance to seed REST caches, supply listings or preserve retired aliases/migrations. Describe claims vs projected Issues and replay after unknown publication. Link, rather than redesign, the in-progress drawio. Synchronize this plan and central registers with local evidence; preserve historical incidents. | No | Pending |

### Implementation Phase 5: Offline Qualification

- **GOAL-005**: Qualify the complete replacement, including performance, before any merge that changes active callers. Exit: GATE-002 and GATE-003 recorded.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- | --- |
| TASK-016 | TASK-015 / Performance | Add benchmarks in existing `internal/state` test files for 1,336 and 10,000 regular state files. TEST-015 specifies sample counts, cold/warm boundaries and metrics. Record raw results and environment in this plan's phase checkpoint; exact-head public snapshots require separate HUMAN-002 approval. No live writes or artificial delays outside test injection. | No | Pending |
| TASK-017 | TASK-015 / Validator | Run formatting, module verification, full Go tests, vet, dimension listing, relevant state/queue/discovery/GitHub/publish race tests and workflow YAML/bash/selection checks specified in Section 6. Confirm no credentials/network dependency in unit tests and no production REST state path. Record exact commands, skips/failures and GATE-002. Do not fix unrelated failures or silently waive a required gate. | No | Pending |
| TASK-018 | TASK-016, TASK-017 / Reviewer | Obtain Factory Contract Reviewer review of the complete diff and evidence, including ownership inventory, shared globals, exact-attempt dispatch recovery, Git credentials and the absence of intermediate transport. Resolve blocking findings and rerun affected gates. Record GATE-003; performance shortfall or unproven security/correctness blocks readiness, not just a footnote. Prepare the exact live verification scope and expected postconditions for human approval. | No | Pending |

TASK-016 and TASK-017 may run in parallel only if TASK-017 uses a fixed source
revision and TASK-016 owns benchmark-only files. Rerun affected tests after their
integration. TASK-018 always reviews the integrated final revision.

### Implementation Phase 6: Human-Controlled Activation

- **GOAL-006**: Verify and activate the finished replacement without an intermediate backend. Exit: approved manual evidence and separately approved factory resumption. All tasks here are blocked until their named human gates are recorded.

| Task | Dependencies / Owner | Description And Acceptance | Completed | Date |
| --- | --- | --- | --- | --- | --- |
| TASK-019 | TASK-018, HUMAN-003 / Ledermayer And Operator | Establish a maintenance window: record currently enabled affected autonomous workflows; disable their entry points using approved GitHub controls without editing schedules. Block new manual state operations by operator agreement. Inspect in-flight writers/leases; drain them to verified completion or recover/fence them through existing authorized operations. Never blindly cancel a worker or clear a lease. Record GATE-004 when no old-code writer can publish after the replacement merge. Do not alter branch protections. | No | Pending |
| TASK-020 | TASK-019, HUMAN-004 / Ledermayer | Merge the complete reviewed replacement through protected-main PR controls, with autonomous entry points still paused. No partial transport PR is deployable. Follow repository post-merge fetch/main/fast-forward procedure while preserving local work. Record merged revision and successful required checks. This task does not run a state command. | No | Pending |
| TASK-021 | TASK-020, HUMAN-005 / Approved Operator | At the exact merged revision, execute only the approved manual scope. Verify Git read/write authentication, fresh reads, small changed-path commits, stable parent chain, zero REST blob downloads, exact operation replay and unrelated-state preservation. Dispatch/worker/acceptance tests require separately enumerated approval even within this gate; otherwise record them unverified and do not activate those callers. Record run/job/commit IDs, operation IDs and pre/postconditions in `docs/FINDINGS.md` and this plan. Do not manufacture live conflicts or lost acknowledgements; those are fixture gates unless expressly approved. | No | Pending |
| TASK-022 | TASK-021, HUMAN-006 / Ledermayer And Operator | After GATE-005 for each caller family, re-enable only its previously enabled autonomous entry points. Observe their exact run/commit/postconditions; do not add schedules, broaden selectors or enable new lifecycle stages. Leave unverified families paused. Record GATE-006 and pending work under existing TASK-028/038/043; no claim of overall product acceptance. | No | Pending |
| TASK-023 | Any failed activation gate, HUMAN-007 / Ledermayer And Operator | Keep or place affected operations in the approved paused state; preserve history, receipts, queued work and leases. Diagnose using safe evidence and ship a reviewed Git-only correction. A code rollback is allowed only with explicit approval and proof it retains the desired transport/state contract; never switch to REST, reset state, force-push, silently rerun unknown operations or bypass environment checks. Repeat affected gates before resumption. This is a contingency, not a mandatory failure exercise. | No | Pending |

### Human Gates And Evidence

| Gate | Required Human Action | Evidence / Stop Condition |
| --- | --- | --- |
| HUMAN-001 | Approve local implementation of this revision | Until then only planning/review is authorized; no inferred implementation approval |
| HUMAN-002 | Optional approval for exact public snapshot benchmark | Repository/ref/exact SHA and read-only scope; absent approval use synthetic trees only |
| HUMAN-003 | Approve exact maintenance/pause/drain scope | Enumerated callers, in-flight ownership and operator; no blanket cancel/reset |
| HUMAN-004 | Approve PR merge under maintenance window | GATE-002/003/004, exact reviewed revision, protected-main checks |
| HUMAN-005 | Approve bounded manual runs | Exact workflows/modules/requests/receipts, permitted side effects and expected postconditions; verify effective App/policy/environment behavior |
| HUMAN-006 | Approve per-family autonomous resumption | GATE-005 evidence for that family; previously enabled workload only |
| HUMAN-007 | Approve failure containment or corrective release | Exact pause/recovery/code action; no implicit transport fallback or state reset |

| Gate | Completion Criteria |
| --- | --- |
| GATE-001 | Local Git transport/coordinator invariants and focused contract review pass |
| GATE-002 | Complete replacement and offline validation pass; reproducible benchmark results from TASK-016 satisfy the controlled objective before TASK-018 releases this gate |
| GATE-003 | Complete-diff contract review has no unresolved blocking findings |
| GATE-004 | Maintenance prevents old-code writers from publishing across merge; active ownership is accounted for |
| GATE-005 | Approved manual scope at merged revision has exact verified durable postconditions and effective authentication/policy evidence |
| GATE-006 | Approved resumed family has observed correct autonomous behavior; no claim for unverified families |

## 3. Alternatives

- **ALT-001**: Parallel REST downloads reduce latency but still repeat unnecessary object transfers; rejected as the production state transport.
- **ALT-002**: Checkout without cache consumption adds cost while leaving slow reads unchanged; rejected.
- **ALT-003**: Pull/rebase/force-push operational state weakens ownership and expected-head safety; rejected.
- **ALT-004**: Permanent Git-read/REST-write transport duplicates maintenance and obscures the standard; rejected. Git transport must implement the required transaction semantics directly.

## 4. Dependencies

- **DEP-001**: Existing module-v2 layout contract, expected-head/fencing APIs and reviewed App branch grants.
- **DEP-002**: Git executable on trusted Actions runners; noninteractive authentication and token renewal with no secret leakage.
- **DEP-003**: Separate approval for live state transactions and factory promotion; local tests do not establish runtime activation.

## 5. Files

- **FILE-001**: `cmd/factory/main.go`, `native.go` and owning handlers inventoried in TASK-001 and converted in TASK-011; common options, credentials and provenance.
- **FILE-002**: `internal/state/checkout.go`, `verified_cache.go`, `store.go`, `delta.go`, `tree_delta.go` and proposed `git_transport.go`, `git_backend.go`, `transaction.go`, `git_provenance.go`; replace production REST state implementation in `github_backend.go`, retain domain invariants and reuse neighboring tests.
- **FILE-003**: `.github/workflows/factory-*.yml` and `.github/workflows/manual-*.yml`, limited to the matrix's trusted state-access jobs; no admin/compute permission expansion.
- **FILE-004**: Existing CLI workflow/command tests, `.github/workflows/AGENTS.md`, GitHub/Go instructions and evidence/task registers; verified standard and rollout evidence.
- **FILE-005**: State-writing functions inventoried under `internal/queue`, `internal/catalog`, `internal/discovery`, `internal/github` and `internal/publish`; pure decisions, durable result resolution and explicit ownership. Exact symbols are recorded by TASK-001 before parallel conversion begins.

## 6. Testing

- **TEST-001**: Cold depth-1 checkout and two same-H public reads each fetch; zero REST blob calls, identical logical snapshots and no mutation through returned snapshot aliases. Verify reuse within one process and Git-object reuse across separate CLI processes.
- **TEST-002**: Remote changes one module between reads; fetch resolves the new H, retains unchanged OIDs and never reuses stale decoded state. Fetch failure stops even when a valid older checkout exists.
- **TEST-003**: Real bare-remote sibling race: only one candidate push succeeds, the loser fetches and recomputes; disjoint updates both survive. A competing push after advertisement is rejected safely. Assert three total decisions at most, two bounded jitter waits, one parent per commit and no merge/rebase/force operation.
- **TEST-004**: Concurrent dispatcher instances select the same module/request; only the committed owner can launch. Stale run/fence, expired lease, changed generation and relevant shared inputs reject stale work. Existing global capacity constraints, where present, are recomputed; a module claim cannot authorize provider/global writes.
- **TEST-005**: Delta commit preserves unrelated bytes, supports explicit allowed deletion, rejects undeclared ownership and unloaded-record deletion, includes required replay metadata and emits no commit for a no-op. Same append ID/content is replay; differing content fails closed.
- **TEST-006**: Lose acknowledgement after successful real push on attempts one and three; reconcile at C and at a descendant of C to the original result with no duplicate commit/claim/import. Failed reconciliation returns unknown; same-ID recovery resolves before another decision. Preserve authentication/policy classification during unrelated head movement. Test cancellation and definite rejection separately.
- **TEST-007**: Reject wrong repository/ref, symlink/executable/tree mode, corrupt object, replaced object, mixed layout and path escape. Depth-1 parent and descendant reconciliation work; deepen in 64-commit increments up to the bound, then stop without guessing. Observed deleted/nonmonotonic ref stops; this is not proof against unseen administrator resets.
- **TEST-008**: Hostile Git config/environment cannot execute hooks/helpers/filters/signing, rewrite endpoints, supply alternates/grafts or redirect credentials. Verify explicit push ref and repository, isolated index cleanup and no production local-remote escape hatch.
- **TEST-009**: Expired state credentials refresh before external operations or fail with a typed error; cancellation/error/success clean up credentials. Inspect process arguments, config, logs and commit metadata for token/private-key/projection-secret leakage. No App key reaches Git and no authentication failure becomes a contention retry.
- **TEST-010**: Fixed author/committer, subject/trailer bounds and identity allowlists; reject controls, raw errors and credential-shaped inputs. Valid provenance leaves evidence bytes, event IDs and caller timestamps unchanged. Emit successful-publication logs only after proof; preserve stdout result/token isolation.
- **TEST-011**: Decision callbacks have no external effects. Force conflict while instrumenting source/Issue/dispatch/artifact calls: only state decisions repeat. Inventory tests cover every converted writer, append replay, shared input changes, no nested retry loops and original result resolution.
- **TEST-012**: Domain/CLI regressions preserve exact requests/modules, scheduled new-ID-only scope, empty-scope behavior, continuation cursors/pacing, independent contexts, recovery parking, receipt identity and human Issue notes. State setup is instance-scoped; missing root/branch fails before writes and non-state modes reject state flags.
- **TEST-013**: Parse all affected YAML; execute shell syntax, selectors and credential/argument fixtures. Every trusted state command consumes the common checkout contract; no stale listing flags, added triggers or broader scopes. Reusable-call jobs inherit trusted callee setup without illegal steps; compute/admin/provider-only jobs gain no state credentials.
- **TEST-014**: Instrument state transport in command fixtures to fail any REST state blob/tree/commit/ref access and verify every state mode still uses Git. Remove obsolete production backend constructors/aliases and stale snapshot adapters. REST for unrelated GitHub APIs and scoped token minting remains allowed.
- **TEST-015**: Synthetic 1,336-file and 10,000-file trees: 10 independent cold-checkout samples and, per size/scenario, 5 discarded warm-ups plus 100 measured iterations. Scenarios: same-H read, one-module update, successful publication, one injected conflict and three-attempt exhaustion. Report nearest-rank p50/p95, transferred bytes/objects, fetch count, decision count, decode/validation and publication timings separately; fixture instrumentation must not log secrets. Record OS/CPU/RAM/Git/Go versions, fixture bytes, local bare-remote topology and process/cache lifetime. Warm non-contention whole-operation p95 objective is <= 3 seconds; report retries separately. Require zero REST state blob reads in every scenario. Public/GitHub measurements are separate, explicitly approved evidence, never a production SLA.

Required offline gate commands, from repository root, after integration:

```sh
gofmt -w <changed-go-files>
test -z "$(gofmt -l cmd internal)"
go mod verify
go test ./... -count=1
go vet ./...
go run ./cmd/factory --list-dimensions
go test -race ./internal/state ./internal/queue ./internal/discovery ./internal/github ./internal/publish ./cmd/factory -count=1
go test ./cmd/factory -run Workflow -count=1
git diff --check
```

The formatting command takes the actual changed Go paths, not the literal angle
bracket expression. Workflow tests must execute the repository YAML/bash/selection
checks with required tools present; skipped tests do not satisfy the gate. No
operational CLI command is a local validation command. TASK-017 records validation
results; TASK-018 releases GATE-002 only after TASK-016 benchmark evidence also
passes. If a required tool or test fails, record the blocker, do not certify readiness.

## 7. Risks & Assumptions

- **RISK-001**: Contention is unavoidable; correctness depends on non-force sibling rejection, fresh decisions, ownership/fences and the monotonic-branch assumption, not the expected 1-5 second window. This is not strict CAS under arbitrary administrative resets.
- **RISK-002**: Shallow history, branch policies, token expiration or Git configuration can invalidate push assumptions; test and resolve them in the Git transport. Invalid or conflicting transactions stop without reverting to a slow or weaker write path.
- **RISK-003**: Full-state decode/serialization may remain costly after transport optimization; measure ownership-scoped follow-up separately instead of claiming all snapshot work is eliminated.
- **ASSUMPTION-001**: Runner-local objects are reusable within a job; no persistent shared runner cache is assumed.
- **ASSUMPTION-002**: Current provider-only workflows and pure proposal-building jobs do not read operational state; verify the matrix during implementation before adding access.
- **RISK-004**: Shared backend replacement affects all callers immediately on merge. The human maintenance window is mandatory; unaccounted old-code writers block merge/activation. Do not solve this by adding a backend toggle or credential bypass.
- **RISK-005**: One compute owner does not serialize discovery, receipt, projection or provider writes. Missing writer/read-set coverage blocks TASK-001; an Issue is not a lock. Unknown external launch/publication outcomes must remain recoverable rather than be silently retried.
- **ASSUMPTION-003**: Current layout and replay metadata can remain readable without old transport or migration entry points. If an exact approved state snapshot disproves this, stop for a narrowly scoped design amendment; never rewrite accepted history or invent a migration silently.

## 8. Related Specifications / Further Reading

- [State transport discussion](../docs/diagrams/factory_lifecycle.md#state-transport-and-atomic-writes)
- [Workflow guidance](../.github/workflows/AGENTS.md)
- [GitHub operations](../.github/instructions/github-operations.instructions.md)
- [Layout configuration](../config/layout.yaml)
- [Native delivery plan](feature-native-pipeline-delivery-1.md)
- [Findings](../docs/FINDINGS.md) and [tasks](../docs/TODOS.md)
- Parent workspace authoritative v2 operating/delivery plans retain ownership of task IDs and activation gates.