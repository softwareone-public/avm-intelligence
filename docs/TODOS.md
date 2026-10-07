# Factory Tasks

Owner: Ledermayer.

Updated: 2026-10-06. Status: planned; staged cutover authorized; implementation, verification, and activation gates remain open.

## Scope And Authority

Complete AVM Intelligence through one production path. The v2 implementation
scope is approved; the additional staged-cutover authorization is recorded below.
Task status and required evidence still govern activation.

- [Operating model](../../plan/design-avm-intelligence-operating-model-2.md): authoritative architecture, contracts, seven selected designs and product baseline.
- [Delivery plan](../../plan/refactor-avm-intelligence-delivery-2.md): authoritative task IDs, dependency order, tests and rollout gates. All `TASK-*` and `TEST-*` IDs below belong to this v2 plan.
- [Projection v2](../../plan/board-projection-v2.md): subordinate board reconciliation contract and API budgets.
- [Cohesion closure](../../plan/review-avm-intelligence-cohesion-2.md#closure-register): nine findings resolved in documentation, not implemented or runtime-verified.
- [Historical v1 roadmap](../../plan/architecture-iaclens-avm-intelligence-1.md#introduction): historical context only where factory requirements conflict with v2. Do not import its IaCLens release tasks, event deliverables, four-worker setting or superseded runtime requirements.

The selected baseline is a dedicated JSON-only `factory-state` branch, automatic
pickup of eligible durable work, one compute worker with independent acceptance
and publication, retained evidence with AzureRM 4.x/5.x tracks, read-only provider
metadata, factory-owned Go extraction/dimension assessment, and completion-first
delivery. The board is a projection, never the queue or approval authority.

Keep current canonical PR reviews and Pages approvals until the TASK-042
data-only automation policy is implemented, tested, and effectively enforced.
Confirm that self-contained runtime
allows reviewed compiled Go libraries and necessary GitHub/public source APIs.
No Terraform/provider/upstream code execution, mandatory AI, private-content
transfer, blanket App bypass, automatic visibility change or autonomous remediation
is authorized. Preserve `pass`, `fail`, `unchecked` and `not_applicable`; keep
execution failure, coverage, freshness and delivery status separate.

## Staged Cutover Authorization

On 2026-10-04, Ledermayer explicitly authorized live public-source collection,
state/data acceptance, request-scoped dispatch, deployment, and unattended
delivery to complete the v2 cutover. Exercise this authority only through the
approved provider allowlist, exact accepted version/commit targets, existing
fencing and receipt contracts, and the ordered TASK-043/TASK-028 rollout. The
authorization is not evidence that any collection, acceptance, dispatch, or
deployment has occurred.

This authorization does not permit upstream code/provider execution, private
content transfer, broader source/provider allowlists, guessed or `latest`
targets, direct or force writes to protected branches, blanket permission
bypasses, visibility changes, or broad scheduling before TASK-028. Continue to
use reviewed data/evidence/selection PRs and exact deployment receipts. TASK-042
must encode and pass TEST-022 for the eligible data-only unattended boundary;
code, configuration, schema, workflow, permission, dependency, and exception
changes retain human review. Verify effective repository/App permissions and
environment protections before each corresponding remote write. TASK-039 still
requires recorded evidence and Ledermayer's final product acceptance.

## Execution Rules

There are 43 v2 tasks and 22 required validation cases below. All tasks other
than TASK-001 through TASK-015 and TASK-016 remain open; existing code and historical
milestones do not establish the new contracts. Mark a task complete only with
reviewed implementation and linked verification evidence, and keep its status
synchronized with the delivery plan.
Record incidents and source-proven risks separately in [FINDINGS.md](FINDINGS.md).

Tasks are ordered within a phase unless explicitly permitted otherwise. The
cross-phase critical order is Phase 0 approval -> Phase 1 -> TASK-016 -> TASK-012;
Phase 2 plus Phase 3 reader/contract readiness -> TASK-022 through TASK-027 ->
TASK-043 -> TASK-028. TASK-016 may run alongside TASK-011 after Phase 1. Phase 5
fixtures may run alongside Phase 4, but production collection requires Phase 4
recovery gates. TASK-042 must precede final TASK-039 acceptance.

Start each implementation with the smallest falsifying check, reuse neighboring
tests, rerun focused validation after edits, then pass required repository CI.
Use synthetic or approved public fixtures only. A failed gate stops rollout;
preserve accepted history, pending state and human review controls. One/ten-module
runs validate the production path, not a separate demo or permanent catalog limit.

## Phase 0: Approval And Baseline

Dependency: none. Exit: implementation/dependency decisions are recorded and the
baseline dataset/site is reproducible. State writes and unattended delivery retain
their separate administrator approval gates.

- [x] **TASK-001: Confirm implementation boundaries.** Review the seven selected designs and obtain overall implementation approval; confirm compiled-library/platform dependencies. Decision and approval evidence: [v2 implementation approval](decisions.md#v2-implementation-approval). Preserve completion-first scope; keep unattended policy pending until TASK-042 and do not change visibility or permissions.
- [x] **TASK-002: Inspect actual controls read-only.** Inventory repository rules, Project field IDs/options, job-specific App permissions and Pages environments. Repository-level rules may protect `factory-state` without weakening `main`. Organization-level rules were not visible; this is unknown, not a blocker to implementation or repository configuration. Verify the effective write operation before activation and stop only if an enforced rule actually denies it. Assess narrow unattended-policy feasibility now, not after implementation. Evidence: [FINDINGS.md](FINDINGS.md#task-002-read-only-control-inventory).
- [x] **TASK-003: Freeze recoverable evidence.** Record the canonical commit, candidate/report hashes, full module IDs, actual deployed commit/receipt and reproducible site/data. Recheck the first cohort's publication/board reconciliation using real receipts. Reproduce the local 232 discovered / 145 accepted report baseline and its coverage counts without treating them as live-site evidence or permanent expected totals. Evidence and the verified board mismatch are recorded in [FINDINGS.md](FINDINGS.md#task-003-frozen-baseline-evidence).
- [x] **TASK-004: Reconcile the evidence register.** Consolidated the source-proven v2 gaps, evidence links, and current status distinctions in [FINDINGS.md](FINDINGS.md#source-proven-v2-gaps-not-incident-claims). Corrected historical deployment/reconciliation claims only from the verified receipt and live Project state. Source-proven risks remain distinct from observed run incidents.

## Phase 1: Correctness Containment

Dependency: Phase 0 approval. Exit: a targeted update preserves the complete
accepted cohort, native-migration expectations are captured, and mismatched
workflow/deployment SHAs cannot produce Done.

- [x] **TASK-005: Establish the lossless-acceptance regression.** Added a two-module base/one-module update regression requiring unrelated reports and requests to remain byte-identical, plus failed, stale, duplicate, and conflicting worker-delta cases. The preservation regression first failed because the untouched report disappeared; it passes after TASK-006. Evidence: [acceptance regression](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/internal/publish/accept_test.go) and [invalid-delta tests](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/internal/publish/candidate_test.go).
- [x] **TASK-006: Preserve the canonical base.** Added `ApplyModuleDelta` in [accept.go](../internal/publish/accept.go); `AcceptCandidate` remains as a compatibility entry point. The merge copies the accepted tree, overlays validated reports and request changes, regenerates full candidate membership, and swaps atomically. The factory CLI now calls the delta API. `go test ./... -count=1` passes.
- [x] **TASK-007: Restore fleet input continuity.** The workflow seeds prior discovery, requests, and source pins; resolved pins are retained in the accepted bundle for later moved-tag checks. `PlanAcceptedCatalog` receives explicit type/ID selection, preserves full catalog membership, and marks filtered entries `not_selected` without creating requests or projecting them. Schedules remain plan-only. The legacy baseline has no exact pin history, so earlier tag moves cannot be reconstructed; see [FINDINGS.md](FINDINGS.md#task-003-frozen-baseline-evidence). `go test ./... -count=1` passes.
- [x] **TASK-008: Freeze native semantic expectations.** Added synthetic schema-v2 legacy-output fixtures under [testdata/assessment](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/testdata/assessment/legacy-v0.1.0.json), tied by test to the configured ruleset and pinned IaCLens producer. The corpus records component/example ownership, explicit root/module and backend-signal classifications, deterministic outcomes, and rootless/nested unknowns. No legacy CLI or analyzer behavior changed. `go test ./... -count=1` passes.
- [x] **TASK-009: Correct publication identity.** Pages publication now uploads a run-scoped receipt containing accepted commit, data/site digests, producer run, and candidate module membership. The completion workflow accepts only successful manual runs from `main`, downloads that run's receipt and accepted dataset commit, and the Done projector verifies producer run, dataset digest, and exact membership before token creation or GitHub writes. Missing/mismatched receipts fail closed. Full Go tests pass; the historical mismatch is documented in [FINDINGS F-001](FINDINGS.md#f-001-deployment-receipt-matches-board-reconciliation-does-not) and requires a new live run after merge.
- [x] **TASK-010: Repair partial projection correctly.** `UpsertFleet` now skips only a matching bot-owned summary with a post-success acknowledgement marker. It writes that marker only after Project fields and owned labels succeed; partial failure leaves the projection retryable. Published commit participates in the summary and Project field sync. Recovery tests cover Project failure, label failure, retry, verified no-op, and changed commit. No fleet-wide drift audit was added; bounded repair intents and audits remain TASK-026/TASK-027.

## Phase 2: Durable Discovery And Claims

Dependency: Phase 1. Implement and test transactional state locally; live branch
activation requires repository-admin approval of repository-level rules and an
effective write check. Unknown organization rules are not a prerequisite; stop
activation only on a concrete policy denial. TASK-012 also requires TASK-016.
Exit: unchanged discoveries across fresh workspaces create no duplicate work,
competing claimers produce one owner, and canceled dispatch is recoverable.

- [x] **TASK-011: Implement transactional state.** Added [`internal/state`](../internal/state/) with validated versioned records, idempotent event application, monotonic fencing, and a GitHub Git-Database backend that commits against the exact parent and updates refs with `force: false`. Tests cover competing writers, stale fences, changed event replays, commit-success/lost-ack recovery, typed-tree reads, and a ref race. `go test ./... -count=1`, state race tests and `go vet ./internal/state` pass. No branch was created and no live write was attempted; repository-level policy and effective write verification remain activation gates. Run only reviewed pinned `main` code, never state-branch content.
- [x] **TASK-012: Model acquisition and assessment separately.** Added v2 `WorkContract` constructors for `acquire_source` and `assess`, with context/track, desired generation, extractor/facts/definition/snapshot inputs, leases/fences, retry classes, applicability and candidate states. Acquisition does not require Facts ID or known applicability; accepted facts wake separate context assessments. Added per-context coalescing, latest-source fallback only after every configured track is proven not applicable, one active compute claim per module, and seal-before-release transitions with distinct `facts_accepted`, `awaiting_acceptance`, and accepted-analysis states. The legacy request API remains intact. Evidence/tests: [work request model](../internal/queue/work_request.go) and [work request tests](../internal/queue/work_request_test.go). Full Go tests, queue/state race tests and vet pass. Workflow dispatch and live state activation remain later gates.
- [x] **TASK-013: Persist discovery and independent catalog publication.** Adapt [state.go](../internal/catalog/state.go) and source resolution to atomically record full observations, release/tag/SHA history, pins, acquisition and Backlog/Blocked/Needs review/retirement intents. Emit `catalog` outbox proposals even without successful analysis. Preserve release-less modules, two-successful-observation retirement and moved-tag detection; keep overall latest separate from track candidates. A v5 change must not discard v4 work, and unknown applicability is not no-track fallback.
- [ ] **TASK-044: Verify scheduled dispatch live.** Runs `37578553338` and `37583177815` failed; see [F-021](FINDINGS.md#f-021-first-scheduled-dispatch-reread-the-full-state-tree) and [F-022](FINDINGS.md#f-022-dispatch-still-downloads-the-initial-state-tree). Verify the next run after checkout seeding, including one claim and worker handoff. `ClaimNext` and exact-module `ClaimModule` are wired through `factory-dispatch` and `manual-dispatch`. After merge, verify one scheduled claim and one manual module claim, including their worker handoffs and `no_work` behavior. Do not treat local tests as live activation; board projection remains F-019.
- [x] **TASK-014: Separate discovery, dispatch and recovery.** Added source-priority/age/stable-ID selection, expected-head attempt claims with monotonic fences, idempotent Actions run binding, heartbeats, and recovery that retains unknown owners and reclaims only after explicit absence and lease expiry. Added manual-only [v2 discovery](../.github/workflows/factory-discovery.yml), [dispatcher](../.github/workflows/manual-dispatch.yml), [worker handoff](../.github/workflows/manual-worker.yml), and [recovery](../.github/workflows/manual-recovery.yml) workflows. Tests cover competing dispatchers, duplicate binds, crash-after-dispatch lookup, unknown runs, heartbeat ownership, expiry/reclaim, and fence advancement. Full Go tests, queue/state race tests, vet, YAML parsing and diff checks pass. Workflows were not run; worker computation remains TASK-022 and activation remains gated.
- [x] **TASK-015: Add authorized operational commands.** Added reviewed [operations policy](../config/operations.yaml) and identity-validated pause/resume/retry/refresh/global-disable/global-enable commands. Require allowlisted `GITHUB_ACTOR`, active accepted module IDs and a non-empty reason; Issue bodies and card moves provide no authority. Persist audit/control records and stable wakeup outbox entries atomically and idempotently. `ClaimNext` enforces module pause and global disable without consulting projection records. Evidence: [operations reducer](../internal/queue/operations.go), [tests](../internal/queue/operations_test.go), and [v2 operations workflow](../.github/workflows/manual-operations.yml). Full Go/race/vet/YAML/diff checks pass. Workflow was not run.

## Phase 3: Native Facts And Evidence

Dependency: Phase 1; schemas/fixtures may run alongside Phase 2. TASK-016 precedes
TASK-012. Exit: legacy dry-run output remains readable; two versions and two
same-source assessments coexist with independently visible dimensions and coverage.
No migration merge or producer activation occurs in this phase.

- [x] **TASK-016: Freeze schemas and identities.** Added [factory-data-v2.schema.json](../schemas/factory-data-v2.schema.json) for requests, sealed completion, immutable source/facts/provenance, definitions/dimensions/analyses, provider/core snapshots, all six proposal kinds, selections, presentation, dataset and deployment receipts. Added versioned canonical IDs, safe path builders, typed reference validation, and append-only version-observation helpers under `internal/identity/`. Tests cover explicit nulls, canonical paths/hashes, alias history, reference failures, and schema `$ref`s. The legacy [normalized-analysis-v1 schema](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/schemas/normalized-analysis-v1.schema.json) remains unchanged for historical reads; no legacy producer evidence is relabeled. Full Go suite passes.
- [x] **TASK-017: Build the factory-owned extractor.** Added `internal/terraform/ExtractFacts` with a standalone strict [facts-v3 schema](../schemas/factory-facts-v3.schema.json); frozen factory-data-v2 remains unchanged. HCL v2.25.0 (MPL-2.0) parses native HCL and Terraform JSON without evaluation. V3 preserves static provider version constraints, multiple child-provider mappings, variable/output description presence, `required_version`, backend/cloud setting presence, resource/data bindings, module source/version, safe paths/locations and per-capability coverage; setting values and arbitrary expressions are never serialized. Synthetic conformance tests pass. Read-only catalog discovery returned 232 modules; source resolution pinned `avm-res-network-virtualnetwork` v0.22.2 at `c08763d616fc9f1932075a70d53fa826dfbdc72f`, and static extraction validated 18 units/201 symbols from that checkout. Ephemeral/function/action and unknown top-level blocks remain partial for TASK-040. Per Ledermayer's direction, the legacy v1 corpus is not a gate for this initial slice. The approved estimate remains 2-4 days for minimum typed facts; the wider static-facts estimate remains 5-10 days and is not widened here. No persistent discovery workflow or producer activation.
- [x] **TASK-018: Build scoped dimension/check assessment.** Added the compiled v2 dimension/check registry, schema-checked immutable definitions/results, deterministic independent `RunDimensions`, exact input fingerprints, explicit applicability/status/execution/coverage, and snapshot-scoped result reuse. Mapped the six public deterministic Go checks (`input-descriptions`, `output-descriptions`, module backend/cloud ownership, deployment core version, and declared provider sources) against the reviewed legacy corpus; the disabled Jev `module-purpose` check remains historical. Path conventions establish unit role/ownership; module/deployment kind requires an explicit reviewed override except examples with backend/cloud signals. Added offline CLI `--list-dimensions`, `--list-checks`, and `--assess-facts` with selected dimensions, optional schema-v2 provider snapshots, and optional classification overrides. Tests confirm missing optional inputs do not erase completed dimensions and deterministic outcomes match `legacy-v0.1.0.json`. See [dimensions and checks guide](dimensions-and-checks.md). Full Go tests, vet, race tests, schema checks and diff checks pass. No state or producer activation.
- [x] **TASK-019: Add dry-run-first migration.** Added the read-only [`--plan-legacy-migration` planner](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/cmd/factory/migration.go). All 145 candidate reports map to the candidate's matching, pre-build accepted discovery generation and to byte-addressed `legacy-analysis-<sha256>` paths under the revision's `analyses/` history. Candidate IDs/counts, repository joins, archive IDs/paths, and recomputed exact-byte report digests match; known producer/ruleset/source provenance is retained. Missing release versions, native facts, and native analysis inputs remain explicit; no accepted data is written or legacy evidence relabeled. Focused provenance/path tests, `go test ./...`, `go vet ./...`, formatting and diff checks pass. The full migration merge and producer cutover remain gated for TASK-043. Baseline: [`work/task019-migration-plan.json`](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/work/task019-migration-plan.json).
- [x] **TASK-020: Build complete, explicit indexes.** Added the deterministic [`internal/index`](../internal/index/) builder, canonical index paths and [`factory-index-v1.schema.json`](../schemas/factory-index-v1.schema.json). It emits catalog, dataset, module, track, provider, dimension-dependency and presentation indexes from typed accepted records; validates digest coverage and cross-record references; preserves shared facts, full provider addresses and exact target vectors; and derives current selections only from contiguous monotonic receipts. Dataset/index identities exclude self-hashes and timestamps from semantic IDs. Added `--build-indexes --index-records <records.json> --indexes-out <fresh-directory>`; output is staged outside `--data-root`, verified against the dataset digest map, and never overwrites an existing candidate. Focused ordering, dangling-reference, as-of, target-vector, content-identity, schema and CLI tests pass; full `go test ./...`, `go vet ./...`, formatting and diff checks pass. No accepted data or producer state was changed.
- [x] **TASK-021: Prove reader readiness only.** Added opt-in [`site.CompareLegacyAndIndex`](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/internal/site/catalog.go) and [`github.FromIndexedFleet`](../internal/github/projection.go) readiness readers; active renderer/projector paths remain unchanged. The site comparison checks generation, module IDs/counts, source SHAs and exact report-byte digests against the generated dataset; it records legacy check-evidence count/digest. The GitHub adapter verifies current context/receipt history/analysis membership/source and returns a projection identical to `FromFleet`. The reviewed legacy fixture preserves all six mapped check status/severity outcomes; message and evidence enrichments are recorded in [dimensions-and-checks.md](dimensions-and-checks.md), including the provider block-versus-entry location difference. Provider-free semantics remain outside the reviewed fixture; `module-purpose` remains historical. Focused dual-read and semantic tests, full tests, vet, schema/format/diff checks pass. No active reader switch, accepted-data rewrite, fallback, or producer activation.

## Phase 4: Independent Module Delivery

Dependency: Phase 2 plus Phase 3 contracts/readers. Integrate TASK-022 through
TASK-027 with fixtures before TASK-043, then TASK-028 live validation. Provider
snapshots may be fixtures until Phase 5. Exit: one module can publish while
another fails, late work cannot supersede newer desired work, and projection
converges after partial failure/throttling.

- [x] **TASK-022: Integrate native workers and trusted validation (fixture gate).** Added manual-only [v2_worker.yml](../.github/workflows/manual-worker.yml) with trusted prepare, credential-free read-only compute, and trusted validate/seal jobs. Preparation binds only after validating the claimed fence and exact accepted source pin or facts/snapshot inputs; compute emits facts-only or analysis-bearing `module_evidence`; trusted sealing validates ownership, fence, content identity and digest before releasing the lease. Cold-start fixtures cover facts completion/acceptance followed by independent context assessments that reuse facts without source checkout. The single-module workflow has no all-worker matrix barrier; workers cannot write accepted data. The legacy [module.yml](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/.github/workflows/module.yml) remains unchanged until the separately gated TASK-043 cutover. Full `go test ./...`, `go vet ./...`, workflow YAML parsing, and diff checks pass. No workflow was run, no canonical state was written, and no producer was activated.
- [x] **TASK-023: Accept six typed proposal kinds (fixture gate).** Added `ApplyProposal` with typed `ApplyModuleDeltaProposal` evidence adapter. Supports `module_evidence`, `catalog`, `provider_snapshot`, `module_observation`, `selection_export` and `presentation` with subject/path/producer/precondition rules. Evidence is append-only; selection exports enforce expected pointers and monotonic sequences. Catalog outbox proposals publish independently of worker evidence. Read-only proposal validation covers pull requests and merge groups, applies only on a current typed base, rebuilds full indexes from merged records, and requires regenerated output to match the synthetic merge tree exactly. The applier writes only to a separate candidate data root and does not push or force-push.

	**Verification and activation boundary (2026-10-02):** Synthetic tests cover all six kinds, catalog-only publication, stale bases/pointers, sequence regression, omitted rebuilt indexes, cross-kind paths, file modes, and preservation. `go test ./...`, `go vet ./...`, workflow YAML parsing, and diff checks pass. The legacy five-argument `ApplyModuleDelta` remains available for normalized candidates. The required merge-queue status check still needs repository branch-rule configuration and was not activated here. No workflow was executed, no PR or canonical data branch was written, and no producer was activated.
- [x] **TASK-024: Reconcile acceptance, selection and exports (fixture gate).** Added `ImportEvidenceReceipt` to verify a typed `module_evidence` merge receipt against its sealed request/completion, atomically mark the request accepted, and preserve evidence history without selecting. `SelectAccepted` requires that imported receipt and matches active catalog lifecycle, current per-context request, own-context source pin, imported provider/policy generations and sealed completion; another track's lease/pin does not fence this selection. The CAS atomically writes the immutable receipt, monotonic pointer and selection export outbox. `ImportSelectionReceipt` acknowledges merge without reselecting; retries are idempotent and missing outbox state parks for review. `ImportAssessmentInputs` advances context generations atomically, supports zero-dependent provider snapshots, and rejects stale work. `DeselectRetired` emits a higher-sequence null receipt and prevents late promotion.

	**Verification and activation boundary (2026-10-02):** Tests cover evidence import before selection, provider/policy invalidation, track-specific source-pin races, independent track work, lost acknowledgments, duplicate imports, sequence advancement, retirement deselection and missing-outbox recovery. `go test ./...`, `go vet ./...`, and editor diagnostics pass. No state branch or canonical data was written; live state import remains part of TASK-043.
- [x] **TASK-025: Verify complete-dataset publication (fixture gate).** Added indexed publication manifest schema v3 with dataset ID, generator identity, deployment generation, and full module/context analysis-plus-selection receipt membership. `VerifyIndexedDeploymentReceipt` rebuilds indexes from the exact data tree and compares all manifest identities. Added state-backed monotonic deployment generations, post-approval target/ancestry rechecks, explicit rollback authorization, and receipt reconciliation that keeps reversed callbacks historical. `VerifyDoneSelection` requires the latest active deployed generation and exact current desired analysis/selection membership; pending, failed, or unknown receipts cannot inherit prior Done.

	**Verification and activation boundary (2026-10-02):** Tests cover legacy manifest compatibility, indexed round-trip verification, moved targets, non-ancestor candidates, authorized rollback, duplicate/failed/late callbacks, exact Done membership, and pending-generation fencing. `go test ./...`, `go vet ./...`, and editor diagnostics pass. No Pages deployment or remote publication state was written.
- [x] **TASK-026: Implement bounded Projection v2 (fixture gate).** Added durable per-module delivery intents/bindings and bounded priority batches with leases, monotonic fences, per-module checkpoints, and retry/review states. Empty batches make zero network or state calls. The client enforces separate REST, Search, GraphQL and secondary budgets, bootstraps rate limits only for non-empty batches, paginates exact-marker Issue and Project-item snapshots, persists page cursors, caches Project metadata/labels/field values for the run, and mutates only fields that differ. The no-op path verifies Issue summary, owned labels, and configured fields; one module's failure does not block another. Projection generation is checked before writes and outcomes are checkpointed immediately.

	**Verification and activation boundary (2026-10-02):** Fixtures cover empty fast path, resource reserves, rate-limit pause with zero network calls, paginated exact-marker filtering, incomplete snapshot rejection, per-field diffs, drift repair, priority ordering, partial success, stale-generation fencing, and independent failure. `go test ./...`, `go vet ./...`, and editor diagnostics pass. Event/15-minute flushes and weekly audit remain unactivated until after live validation.
- [x] **TASK-027: Share the lifecycle reducer (fixture gate).** Added `queue.ReduceModuleStatus` for retirement, Needs review, Blocked, Failed, phase precedence, acquisition/fallback, mixed tracks, and controlling context. It keeps desired and active generations separate, requires exact current selection and active deployment membership for Done, and treats empty unresolved contexts as Blocked. GitHub projection consumes reducer output through `FromReducedStatus`; delivery uses only `pending`, `projecting`, `projected`, `retryable`, and `needs-review`. Retirement writes a higher-sequence null selection; exact Issue markers, human notes, and non-owned labels are preserved by the projector.

	**Verification and activation boundary (2026-10-02):** Reducer fixtures cover mixed Blocked/Publishing and Backlog/Analyzing tracks, newer desired work with older active work, exact deployment membership, stale receipt rejection, retirement, Needs review precedence, latest-source fallback, and empty contexts. Projection tests verify field/label repair after partial failure and summary-only matches cannot suppress drift. `go test ./...`, `go vet ./...`, and editor diagnostics pass. No projection workflow was run.
- [ ] **TASK-043: Perform the integrated native cutover (clean start).** After TASK-022 through TASK-027 fixture gates, pause legacy registration/acceptance and reconcile/drain/fence old attempts. Preserve existing legacy candidate/reports byte-for-byte as read-only history; do not import them as v2 selections, facts, or native analyses. Bootstrap fresh v2 discovery/state, publish its typed catalog through a reviewed data PR, and ensure the dataset plus module metadata/indexes are accepted before dispatching any worker. Wire sealed worker evidence to a reviewed evidence PR, import only its merge receipt, and export selections through a separate reviewed PR. Reuse each existing module Issue across cycles; add one idempotent factory-owned comment per durable module/context generation, preserving human comments and never creating per-cycle Issues. Validate exactly one native producer/integrated writer and run TEST-021 against its first candidate and late old callbacks. Retire IaCLens execution/download pins only after the native path is verified. Preserve readers/recovery evidence; do not enable broad scheduling before TASK-028.

	**Cutover checkpoint (2026-10-02):** PR #176 is merged at `c50042d`; resolver fix PR #177 at `4c78d07`; catalog pointer PR #179 at `0727c1f`; typed catalog PR #195 at `63b73bb`; and evidence handoff PR #196 at `246f85b`. PR #195 provides metadata/indexes for the 232-module generation-1 catalog and the initial dataset manifest; the accepted-site build succeeded against the merged data. Discovery run `37040389851` committed 232 modules and 157 acquisition requests to `factory-state` at `078de92cc3c0a6d534b5082a4df3445fb2fd81d3`. One manual dispatch, run `37070052577`, claimed attempt `attempt-178d1effaf7e898ade119d067f73bef01104e266f2feee7870bb83a48c25e097` and dispatched worker run `37070429311` for `avm-ptn-aca-lza-hosting-environment` at source `9bb7ab42291a0afdc9842d4c09b5f8f03b4c095d`. Acquisition compute and trusted completion sealing succeeded; the request is awaiting evidence acceptance. The final review-branch job failed because the proposal CLI reused the seal-only `--validation-id` flag; no evidence branch or evidence PR was pushed. A dedicated-flag fix is being prepared, and the actual worker artifact now builds a five-change proposal locally against `246f85b`. State heads advanced through claim `f806bf6`, run binding `fa7123f`, and sealed completion `74b10a4`; no canonical data, analysis, or selection was changed. Legacy `Fleet plan` and `Pilot module` remain manually disabled; the 145 legacy reports remain byte-for-byte read-only with no legacy facts, analyses, or selections imported. Assessment wake-up, selection export/merge reconciliation, and TEST-021 remain pending. Broad scheduling remains off pending TASK-028.

	**Discovery/Backlog cutover checkpoint (2026-10-04, PRs #202-#208 merged):** Nightly full-catalog discovery and manual `discover_modules` targeting are deployed; discovery never dispatches workers. PR #207 makes the manual dispatcher require an exact request ID, so it cannot claim another queue entry. Forced discovery run `37121374133` created generation-2 `acquire_source` work for `avm-ptn-aca-lza-hosting-environment` without replacing generation-1 accepted facts. Backlog projection recovered in run `37219371339`; see [F-015](FINDINGS.md#f-015-targeted-discovery-projection-outlived-its-lease). Targeted dispatcher run `37225689343` claimed attempt `attempt-23bc217a1c4752aa3138f9f81469d72410cfed3817ffb7217caadb70f5cad15d` at fence 2; worker run `37225938725` completed facts-only compute and trusted sealing. Evidence PR #208 merged at `b9190ae`; receipt import run `37227201589` succeeded. Generation-2 request is now `facts_accepted` with facts ID `facts-05c3b20696541c453f28136ff067cc8d7137d74c4a09864971717b0d22ab1d1f`. No assessment request, analysis, selection, or deployment exists. Snapshot-driven AzureRM 4.x/5.x assessment contexts and wake-up wiring remain unconfigured; keep assessment parked until accepted exact-version snapshots and policy inputs exist. No assessment or broader dispatch was started.
	**V1 retirement inventory (2026-10-06, after PR #215 merge at `12d42dc`):** Ledermayer approved inventory/planning only. The [retirement plan](../plan/refactor-avm-intelligence-v1-retirement-1.md) records CLI, executable pins, workflows, queue/catalog/source/projector/publication couplings, schemas, fixtures, data and documentation ownership against exact merged main. F-016 records the 145 dangling report references in the original worktree; [F-017](FINDINGS.md#f-017-v1-retirement-crosses-shared-native-contracts) records native contract dependencies. The clean baseline has 292 legacy JSON records and 473 typed native records, with no accepted native analyses/selections/provider snapshots/presentation snapshots. Proposed ordering is reviewed native-only callers/fixtures, a narrowly constrained local maintenance validator, a separate exact-base data cleanup PR with regenerated dataset/indexes, then documentation closure. Review the new maintenance boundary and reconcile parent-plan preservation expectations before implementing; no v1 code/data removal or live operation occurred. TASK-043 and native rollout/product gates remain open.
	**Offline retirement checkpoint (2026-10-06):** Ledermayer approved complete local code/config/workflow/fixture/data retirement and rejected the proposed workflow-driven maintenance path. The external analyzer/toolchain, pilot/fleet commands, local-file queue, report readers/projections, normalized schema/corpus, migration adapter and six old workflow files are removed locally. Native dispatch in `internal/queue/dispatch.go` is retained; the initial inventory's contrary classification was corrected. Native helpers formerly defined in removed code/tests were isolated and retained. All 292 legacy JSON records are absent; two offline native rebuilds were byte-identical, all 237 native source records and 232 catalog modules were preserved, and all 472 dataset digests verify. `TestRepositoryDatasetIsNativeAndRebuildable` guards the actual tree. Native status/severity/evidence fixtures, negative retired-command/workflow tests, missing-work rejection and native indexed receipt tests pass with the full offline Go suite. No new cleanup schema/envelope/workflow, live collection, dispatch, state write, deployment or merge occurred. The [retirement record](../plan/refactor-avm-intelligence-v1-retirement-1.md) supersedes the initial maintenance proposal; [F-016/F-017](FINDINGS.md#f-017-v1-retirement-crosses-shared-native-contracts) record local preservation/isolation. Resolve the single mixed PR's trusted-base typed-proposal review constraint in [F-018](FINDINGS.md#f-018-combined-retirement-pr-is-outside-the-typed-proposal-gate) before claiming merge readiness. Native presentation/Pages integration and live rollout remain open; TASK-043 is not complete.
- [ ] **TASK-028: Validate one module, then ten.** After TASK-043 and live authorization, exercise the same production path and record cold start, sealed completion, evidence/export PR receipts, actual commits, deployment membership, mixed-track reduction, note preservation, API headroom and independent failure recovery. Fixture provider inputs do not establish live provider coverage. These gates do not constitute full-product acceptance.

## Phase 5: Provider Intelligence

Dependency: Phase 3 contracts; fixture work may run alongside Phase 4, but live
collection requires Phase 4 recovery gates. Exit: relevant provider changes rerun
only dependent enrichment with no source scan and cannot turn incomplete coverage
into pass.

- [ ] **TASK-040: Complete five-kind extraction.** Extend the native extractor to ephemeral/action bindings, nested provider/core functions and relevant lock selections across HCL/JSON. Test custom providers/aliases, child mappings, comments/strings, malformed input, lock scope and all five symbol kinds. Preserve unknowns; empty arrays are not proof of implemented support. Never execute providers. **Offline checkpoint (2026-10-04):** native HCL now extracts ephemeral/action bindings and nested core/provider-qualified function calls; unit-root `.terraform.lock.hcl` selections are separate `provider_lock` facts and are wired into trusted acquisition. JSON ephemeral/action blocks are extracted, but function-call traversal in JSON remains partial; TASK-040 stays open until that gap and the full conformance matrix are closed.
- [ ] **TASK-029: Define provider collection policy.** Add proposed `config/providers.yaml` and `internal/provider/` with full provider addresses, approved public sources, licenses, per-kind capabilities, limits and immutable manifests. Start with AzureRM and AzAPI; mark other observed providers not collected. Test cross-provider/name and same-provider/cross-kind collisions.
- [ ] **TASK-030: Collect versioned provider and core metadata.** Implement `CollectVersionMetadata`, `CollectTypeCatalog`, `CollectChangeKnowledge` and separate `CollectCoreFunctionMetadata`. Pin allowlisted public docs/source/releases/issues with conditional requests, bounded pagination, source/collector digests and explicit errors/coverage. Emit independent `provider_snapshot` proposals even with zero dependent modules. Store resources, datasources, ephemerals, provider functions and actions separately from versioned Terraform core functions. Undocumented kinds/missing core context remain unavailable; no plugins. **Offline checkpoint (2026-10-04):** fixture-tested exact-version type catalogs verify tag/commit identity, bounded pagination, per-kind coverage, optional ETag/Last-Modified revalidation, and proposal/apply integration. Memory and file-backed caches exist, but no workflow configures a persistent cache or runs collection. Release enumeration, change knowledge, core functions, and workflow/receipt wiring remain open. No live requests were made.
- [ ] **TASK-031: Assess currency and type evidence offline.** Register checks from proposed `internal/intelligence/` against accepted facts/pinned snapshots. Use a tested constraint library; distinguish selected-version freshness, track eligibility, type availability, issue relevance and heuristic confidence. Missing transitive dependencies are unknown; example locks do not define consumers' installed versions and major badges/issue mentions prove neither compatibility nor defects. **Offline checkpoint (2026-10-04):** the compiled registry now assesses provider type inventory across extracted kinds and checks declared version constraints against exact accepted target snapshots; target vectors participate in the currency result fingerprint. Track eligibility, freshness/history, and issue relevance remain open.
- [ ] **TASK-032: Index dependencies and invalidate narrowly.** Derive full provider/type usage and dimension dependencies from accepted facts. Relevant accepted snapshots enqueue enrichment-only refresh; use provider-level fallback when symbol extraction is incomplete. Reuse unaffected dimensions and retain visibly stale last-good observations on collector failure. **Offline checkpoint (2026-10-04):** the generated dependency index now projects per-module/unit provider types and extraction coverage from accepted typed facts; a pure queue planner selects exact affected types or provider-level fallback. Durable invalidation events, state writes, stale-last-good handling, and assessment enqueue integration remain open.
- [ ] **TASK-033: Integrate independent collection and enrichment.** Add provider workflows that accept snapshots, import receipts/invalidation through TASK-024 and enqueue `assess` via TASK-012. Persist sanitized failed-collection health independently from last-good data. Prove zero source checkout/parser/AI on refresh; rerun only dependent dimensions. Retain active-major history and review new provider allowlists before activation.

	**Command integration checkpoint (2026-10-05, PR #213 merged at `35ebdd3`; attribution corrected 2026-10-06):** added explicit `--collect-provider-catalog` wiring from reviewed policy through bounded pinned collection and persistent conditional cache to a reviewable provider snapshot proposal. Fixture integration applies the proposal only to a temporary candidate, reuses cached responses, and rejects mismatched release pins without creating proposal output. Invalid pins/generation/time, mixed CLI modes, existing output and overlapping/symlink-aliased data/cache/output roots fail before network. No live collection, accepted-data/state write, assessment dispatch, or deployment occurred. Workflow/receipt/health/invalidation and assessment wake-up integration remain open.

	**Workflow implementation checkpoint (2026-10-05, PR #214 merged at `f4fefc3`; attribution corrected 2026-10-06):** manual `v2_provider_collect.yml` uses the existing publisher environment, read-only Contents/Actions access, exact current-main/code/data fencing, and explicit provider/version/revision/generation/time inputs. It stages and locally applies a proposal only to a temporary candidate, then uploads separate proposal/cache artifacts. Optional cache restore verifies repository, successful manual workflow/run attempt, code commit, policy digest and provider pins; expired/mismatched artifacts fail closed. Tests cover manual-only triggers, reviewed allowlist choices, commit-pinned actions, read-only/no-state/no-dispatch boundaries, exact-base/cache checks and executable shell syntax. No collection workflow was dispatched and no live provider metadata, accepted data/state, assessment, or deployment was changed. Reviewed data-branch/PR preparation, acceptance receipts, health/invalidation and assessment wake-up remain open.

	**Review-export implementation checkpoint (2026-10-05; commit `17e3843` retained in the retirement PR, not previously merged as PR #214):** manual `v2_provider_export.yml` verifies an exact successful collection run, same repository/main/code/base, and numeric run attempt before downloading the attempt-specific proposal. It applies the typed proposal to temporary candidates, validates the actual staged path set against an archived exact base, rechecks current main, and prepares only a `factory/v2-provider-*` review branch through the existing publisher environment. Tests execute the run-provenance filter against successful, failed, unfinished, foreign, wrong-workflow/event/branch, stale-base and malformed-attempt fixtures; workflow contracts also cover action pins, permissions, path checks and shell syntax. No collection or export workflow was dispatched, no provider data was accepted, and no state import, assessment, or deployment occurred. Data PR review/merge, verified acceptance receipts, health/invalidation and assessment wake-up remain open.
- [ ] **TASK-041: Bootstrap provider tracks within bounds.** Implement newest-first AzureRM 4.x/5.x candidate acquisition with reviewed release-count/request/byte/wall-time limits and persistent progress. Share facts but keep independent selections; exhausted search or missing dependencies stays unresolved, not latest-compatible or Done. Require explicit policy for deeper backfill and track retirement.

## Phase 6: Portal And Controlled Rollout

Dependency: Phase 4 for catalog/operations and Phase 5 for provider/currency.
Exit: every baseline surface, all-five-kind extraction, check evidence, history,
refresh, recovery and separately approved unattended delivery is verified.
Unavailable upstream evidence stays explicit; unimplemented required capability
or missing automation approval means the product remains incomplete.

- [ ] **TASK-034: Export safe as-of presentation state.** Add `internal/site/ExportPresentation`, immutable allowlisted snapshots and coalesced `presentation` proposals via TASK-023. Trigger on discovery/lifecycle, selection, collector health and daily freshness checkpoints, never receipt arrival alone or lease heartbeats. Render catalog/module/history with shared reduced status, coverage and explicit generation/time; include never-analyzed/blocked/retired modules. Distinguish this dataset's contents from prior verified publication; expose no raw control state and never certify the page's own deployment. **Offline checkpoint (2026-10-04):** exact-base presentation proposals and accepted-snapshot-only rendering are implemented; typed indexes and dataset manifests are regenerated in the proposal. Snapshot identity, catalog membership, lifecycle, coverage JSON, and provider manifests are validated. Trigger/coalescing integration, history, shared lifecycle reduction, and publication/deployment distinctions remain open.
- [ ] **TASK-035: Implement every evidence/navigation surface.** Add Provider/Type, Currency and Quality views with five-kind navigation, track/version/analysis selectors, reciprocal usage and dimension/check evidence. Add dedicated Issues and Activity views separating factory/module/provider origins and discovery/source/assessment/acceptance/deployment times. Provide empty/stale/error states; use fixtures until TASK-036 integration. Optional version diffs and aggregate scores are not completion requirements. **Offline checkpoint (2026-10-04):** static Catalog, Module, Quality, Provider/Type, Currency, Issues, Activity, and Operations pages render from accepted typed records with explicit empty states and evidence-only copy. Context/version/analysis selectors, reciprocal navigation, module-observation data, and full viewport/accessibility verification remain open.
- [ ] **TASK-036: Collect public module observations independently.** Add sanitized issue/release/commit collectors, immutable `module_observation` proposals, acceptance/health/invalidation integration and wiring into TASK-035. Keep human-reviewed use-case curation in proposed `curation/`, never collector-owned policy. Define evidence/freshness contracts with public/synthetic fixtures, not private rules or predecessor scores.
- [ ] **TASK-037: Approve branding and verify all views.** Obtain approved SoftwareOne assets and usage/font rights before styling. After TASK-034 through TASK-036 integration, test every baseline surface at 390x844, 768x1024, 1440x900 and 200% zoom. Check navigation, reciprocal links, keyboard use, accessible empty/stale/error/coverage states and distinct timestamp meanings; do not invent official branding or misleading scores.

	**Local browser checkpoint (2026-10-05):** presentation proposal `proposal-641610d1958e95a35b7fdda985f9f6784cc0b9e362503e5ccb1816221715dfe4` was applied only to a temporary local candidate from base `e02c83c9bc9e251efdb8807f8dbeb152f6fded7a`. All eight surfaces were checked at 390x844, 768x1024 and 1440x900; a long dataset-ID overflow was fixed and no page-level overflow remained. Top-level current-page navigation was fixed and regression-tested. Keyboard catalog search reduced 232 rows to one match and restored all rows; module navigation and keyboard return worked. Mobile/desktop screenshots were inspected. All surfaces also passed a 200% CSS-scaling layout check, which does not certify actual browser zoom. Branding approval, full keyboard/accessibility coverage, populated/stale/error fixtures and integrated reciprocal evidence remain open. No accepted data or remote publication was changed.
- [ ] **TASK-038: Operationalize recovery and observability.** Add dashboards/runbooks for queue age, unknown coverage, collector freshness, leases, review/export waits, publication/projection lag, API cost/headroom, storage growth and artifact expiry. Test pause/resume, exact-commit redeploy, board-only repair and authorized rollback without reanalysis. Preserve state/outbox and canonical history; never force-reset branches or fabricate missing selections. A code-only rollback must not strand data unreadable.

	**Workflow-family checkpoint (2026-10-06):** local `factory-*`/`manual-*` migration preserves existing autonomous events, schedule, approval environments and required job names. Manual discovery/projection recovery expose RES/PTN/UTL, force and canonical CSV inputs; provider/global/exact-identity exceptions retain safety-specific inputs. Fixture checks cover selection versus refresh, delivery/lease/review/backoff preservation, exact historical producer paths and fail-closed recovery lookup. The [GitHub operations standard](../.github/instructions/github-operations.instructions.md) requires manual-first validation before factory promotion. After reviewed merge and applicable approval, verify bounded manual discovery and projection recovery on exact modules, confirm historical receipt/run visibility, and record exact live postconditions before promoting any new autonomous behavior. Local fixtures do not close TASK-038, TASK-043 or rollout gates.

	**Manual selection correction (2026-10-06):** after [run 37458406590 failed before discovery](FINDINGS.md#manual-module-list-rejected-without-categories), Ledermayer approved module lists overriding all category checkboxes. The local discovery/projection-recovery correction and regressions preserve exact targets and reject invalid or empty scopes; the discovery force label explicitly describes queued reprocessing. After reviewed merge and applicable approval, rerun the exact ten-module list with categories unchecked and verify the selected request/projection scope. Confirm unchanged-source behavior with force off separately from authorized forced reprocessing. Local checks do not establish live activation or close TASK-038/TASK-043.

	Structured factory logging was approved by Ledermayer on 2026-10-06 and is
	implemented locally: configurable severity/format, UTC timestamps, safe
	module-stage/dimension progress, existing retry events, and JSON/token stdout
	isolation. Focused regression evidence is recorded in
	[FINDINGS.md](FINDINGS.md#factory-logging-source-proven-gap-and-local-implementation).
	Verify the log visibility and level overrides on the next authorized Actions
	run after review/merge; do not dispatch work solely as a logging check.
	Dashboards, recovery runbooks and live observability acceptance remain open.

	**Discovery/projection progress and scope (2026-10-06):** Ledermayer approved logging guidance and instrumentation after [the observed silent projection wait](FINDINGS.md#discovery-and-projection-progress-gap), then required requested manual scope and new-catalog-ID-only factory scope for all follow-on module work. Fixtures cover INFO module/phase visibility, 30-second waiting cleanup, privacy, truthful outcomes, telemetry determinism, empty/new-ID scopes, preserved unselected retirement intents and exact board targets. Review caught and corrected unsafe source failure log reasons and missing checkpoint module context. Verify logs/scope on the next authorized manual run after merge; do not rerun discovery merely to repair an already-persisted projection. Existing-ID releases and blocked-source retries need explicit manual scope or a separately reviewed mechanism. The subsequently approved manual orchestration correction is recorded below; generated legacy-description migration preserving human notes, idempotent per-run evidence comments and live rollout gates remain open.

	**Manual cycle correction (2026-10-06):** Ledermayer approved and local fixtures verify one shared observation followed by RES/UTL/PTN ordered singleton resolve/commit/Issue/Project/acknowledgement cycles, stopping before later modules on failed/protected delivery. Immutable blob reuse preserves live heads/fences; Project metadata/budgets are shared. Catalog exports retain earlier outboxes and select an exact generation-bound current reference. Evidence: [FINDINGS.md](FINDINGS.md#manual-module-cycle-correction). After reviewed merge, obtain applicable approval for bounded live verification; do not promote this behavior into factory discovery based solely on fixtures. Separately review module-owned state folders, compatibility readers/migration and global metadata/queue ownership; a directory move alone does not remove shared branch-head conflicts. No state-path migration is implemented.

	Review regressions now bind cycle delivery to current durable discovery across ordinary retries and maintain catalog references across every retained writer. Verify these boundaries live without broadening scope. For the separate layout proposal, design one versioned module/provider path contract with `data` and `state` roots, distinct evidence/control leaf types, full provider addresses and version/snapshot identity, shared readers/writers/validators/indexes, and explicit compatibility/migration validation. Do not treat a source-config change as permission to move existing immutable records, reset queue ownership or bypass branch-head fencing.
	The [common layout and state handling plan](#common-layout-and-state-handling-plan) below makes this follow-up executable in ordered, separately gated phases. Its status is proposed for Ledermayer's review, not implementation or migration approval.
- [ ] **TASK-042: Enforce the authorized unattended-delivery boundary.** Ledermayer authorized unattended delivery on 2026-10-04; encode the exact data-only eligibility boundary and verify effective administrator/repository enforcement before activation. Require trusted producers, schema/preservation/current-base/path checks; code/config/schema/workflow/permissions/dependencies and exceptions retain human review. Pass TEST-022 against invalid producers and ineligible changes first. Until enforcement is verified, retain current PR/Pages approvals and report approval-gated operation, not a completed dark factory. No blanket App bypass.

	**Run evidence and budget diagnostics (2026-10-06, TASK-038):** [run 37475542630](FINDINGS.md#module-nine-local-budget-stop-and-missing-run-evidence) completed eight modules, stopped at module nine's local reserve and never started module ten. Approved local changes add distinct local/GitHub WARNs and typed terminal scope/retry context, plus module/phase/run-attempt comments required for acknowledgement. Fixtures cover App ownership, deduplication, forced-run/phase/attempt identity, lost-checkpoint retry and protected delivery. Verify on exact approved modules after merge; do not rerun forced discovery for board-only recovery. Extend the shared writer when F-019's remaining lifecycle projections are wired. Review budget pacing/replenishment separately; this change does not increase quota, and comment calls can exhaust it sooner. Live recovery and TASK-038 acceptance remain open.
- [ ] **TASK-039: Accept the complete baseline.** After TASK-042 and TEST-001 through TEST-022, record integrated source/facts/dimension/check coverage, five-kind support, retained history/tracks, provider-only refresh, all portal surfaces, recovery and verified unattended receipts. Enable broader scheduling only after prior rollout/permission gates. Obtain Ledermayer's acceptance of the evidence; do not waive required capability/policy for an event or replace the production path with a demo.

## Common Layout And State Handling Plan

**TASK-038 unchanged-work correction (2026-10-07):** Ledermayer required no
unnecessary processing on force-off discovery. Local regression/correction
skips acknowledged current delivery before queuing new run evidence, across
different run IDs. Preserve new release/force and pending recovery; an Issue's
Backlog stage alone never decides queue eligibility. Read-only source release
checks and durable catalog/cursor bookkeeping remain. Verify explicit skip logs
on the next authorized run after merge; do not rerun solely for logging or reset
pending delivery. Actual work still requires per-run evidence comments.

**Approved Recommended Design (2026-10-07):** local implementation batches only
read-only skips, verifies live heads for decoded reuse, deduplicates tags within
one release check, unifies counts and explains pacing waits. Automatic exact-ID
continuation waits within the same approved six-hour job, refreshes state tokens
and preserves rolling costs between segments; no self-dispatch or worker launch.
After PR review/merge, verify a separately authorized bounded run's counts,
automatic deadline wait/resume and skip read/write reductions. Cancellation or
350-minute exhaustion retains durable continuation; do not promise unlimited
hosted-job execution or automatically resume previously deferred operations.

**Approved evidence failure head correction (2026-10-07):** run `37578969689`
exposed loss of the last committed head when evidence saving deferred, causing
parking to fail expected-head validation. Preserve the known head on failure
and report checkpoint cause separately from its pacing trigger. Focused fixture
tests reproduce the exact transaction ordering. After PR review/merge, verify
the next separately authorized run parks at this boundary; existing 67 completed
modules and pending module 68 require exact-ID recovery with live ownership
checks, not rediscovery/force. Do not treat local tests as recovery acceptance.

**Approved final parking correction (2026-10-07):** run `37573787391` failed
while saving deferral, not during module analysis; exact cursor retained 55
completed and pending module 56. Reserve final parking capacity from routine
checkpoints, bound local-only parking waits and require `deferral_saved` before
automatic resume. After review/merge verify the next separately authorized
run can park near allowance exhaustion. Recover the existing exact continuation
only after current head/ownership/backoff checks and explicit live authorization;
this fix does not resume it or establish TASK-038 acceptance.

**Approved five-minute local revalidation (2026-10-07):** implement classified
local probes without increasing limits. Tests cover repeated probes without
admission/charges, protected server and exhausted primary deadlines, equivalent
generation metadata and quota-only pre-claim checks. After review/merge, verify
classified wait messages and five-minute rechecks on a separately authorized
run; successful probe still depends on real capacity. Legacy delays remain
unchanged and the current running job does not hot-reload this correction.

**Live pacing observation (2026-10-07):** PR #241 is merged at `a8916af`.
Ledermayer reported fast six-UTL skips in run `37570064588`, and progress after
the automatic inherited-retry wait in RES run `37570334741`. Keep the current
policy while observing the remaining scope. Record final completion, additional
wait causes and pending delivery outcomes when available; do not mark TASK-038
accepted from progress alone. Revisit local-versus-server durable retry
classification/revalidation only if the observed behavior warrants an approved
adjustment. See [pacing layers and count meanings](factory-operations.md#budget-diagnostics).

**Approved minimal-cycle implementation (2026-10-07):** paced manual discovery
now checks releases before a snapshot-only acknowledgement skip, avoiding the
intermediate pending checkpoint and delivery reread for true no-ops. One fenced
completion cursor write per module remains; force/new source/pending recovery
and autonomous scope are unchanged. Offline fixtures cover checkpoint counts,
release checks, pending resume and stale-head rejection; summary fixtures
distinguish checks/skips/changed sources and remaining cycles. After review and
merge, verify these counts on the next separately authorized bounded run or
exact continuation. Do not rerun for logging alone or treat deferred run
`37561438122` (17 completed of 162 selected) as fleet completion.

Run `37559848326` subsequently failed at Redis, position 18 after 17 completed
cycles, because its existing delivery retry deadline `02:29:49.214269052Z`
was still future at `02:11:01Z`. The local correction preserves that deadline
as typed continuation deferral rather than a generic job failure. Verify both
unchanged acknowledged skips and protected pending retry on the next authorized
run after merge; never reset the Redis record or force discovery as recovery.

**TASK-038 manual pacing checkpoint (2026-10-06):** approved local implementation
adds shared weighted state/projection pacing, actual primary reserve/reset
handling and fenced exact-ID continuation. Completed modules and pending forced
source work are not replayed; remaining scope is visible in the job summary.
The 30-minute job bound and permissions/schedules remain unchanged. See
[operations](factory-operations.md#manual-discovery-pacing-and-continuation) and
[budget evidence](FINDINGS.md#module-nine-local-budget-stop-and-missing-run-evidence).
After review/merge, verify a bounded user selection and its exact continuation,
recording refs, cursor/fence identities and retries. Do not close broader gates
or promote factory behavior from fixtures; unconfigured callers keep old policy.

**Pacing usability follow-up (2026-10-07):** first live paced RES run
`37557331436` deferred after 17/162 cycles until `02:29:49.214Z`; exact-ID
resume `37558545407` ran early at `01:44:29.922Z` and correctly made no module
progress. Preserve that cursor. Review fewer necessary state/board mutations
and automatic due-only exact-ID resumption as the preferred usability option;
longer bounded jobs/token renewal or discovery/projection decoupling are
alternatives requiring explicit approval. Do not merely raise the local hourly
counter or imply GitHub quota exhaustion from default diagnostic zeros.

Owner: Ledermayer. Status: manual state population implementation authorized on
2026-10-06; broader layout phases and live verification remain separately gated.
This is a subplan of TASK-038, coordinated with TASK-016/TASK-020 contracts,
TASK-024 receipts and TASK-043 cutover. Existing task/test IDs and completion
states remain unchanged. Follow the [recorded layout boundary](decisions.md)
and [source findings](FINDINGS.md#manual-module-cycle-correction).

**Manual state population authorization (2026-10-06):** Ledermayer subsequently
requested implementation through protected-main merge and explicitly required
App-only `factory-state` updates without manual merge reviews. The next
user-dispatched `manual-discovery` is authorized to establish the module-owned
state profile itself, preserving existing durable state rather than starting
over. This updates the earlier separate state-cutover approval requirement for
that entry point only; it does not authorize dispatch by the coding agent,
accepted-data relocation, autonomous promotion or product-gate closure.

**Local implementation checkpoint:** the compiled declarative
[layout contract](../config/layout.yaml) and persisted state marker map module
requests/attempts, pins, controls, selection outboxes/receipts, evidence receipts
and projection delivery/bindings to module roots. Catalog observations/outboxes,
shared imports, batch records and fleet deployments remain global. A compatible
backend reads both profiles; every retained writer preserves the target tree's
profile. Legacy event hashes remain unchanged in a compatibility ledger; new
module events advance local transaction metadata. Manual singleton deltas
declare module/global and catalog source-pin preconditions with at most three
non-force commit attempts. Other transitions retain conservative full-head CAS.
The separate verified checkout seeds only immutable blobs; unchanged contents
are referenced by Git object identity. Inverse migration retains post-cutover
writes and requires the exact current head.

Provider/major contexts retain every path segment, including `azurerm/4`,
`azurerm/5`, `azapi/2` and full registry/namespace/provider/major contexts.
Payload context identities must match selection paths; existing accepted
context IDs are not renamed. New contexts do not imply accepted snapshots,
track configuration, assessment dispatch or provider coverage.

**First live attempt:** run `37498519069` at `447eef7` verified cold-cache use
(651 blobs reused, zero blob downloads), but failed inside the initial migration
commit before any module cycle. The durable head remained `68cb75a` and no
layout marker exists. Safe typed state API diagnostics are prepared locally;
the underlying tree/commit/ref error remains unknown. After diagnostic merge,
inspect the next user-dispatched same-scope run's operation/status/request ID
and verified ref outcome. Do not infer a permission blocker or mark migration
successful from the cache result. Evidence is recorded in
[module layout findings](FINDINGS.md#module-layout-manual-population).

**Bounded live success (2026-10-06):** user-dispatched run `37500289235` at
`f7a0739` completed migration at `c989e33`, then one cycle for
`avm-res-resources-resourcegroup`, final state head `9830437`. Read-only
verification found the layout marker, 232 module transaction files and no
legacy root transaction file. This supersedes the earlier pending first-run
outcome, not the remaining rollout/acceptance gates; the earlier API failure's
cause remains unproven. See the same findings entry for exact receipts.

Ledermayer's module-first logging request is implemented locally with focused
text/JSON, standard-format, grouping, stdout/privacy and race regressions.
Review/merge and visibility on the next authorized run remain follow-ups; do
not dispatch discovery solely to check formatting.

**Bounded state writer follow-up:** run `37504086545` at `87a25f9` failed
`create_tree` with HTTP 422 while resending 889 paths; durable head remained
`6792d2a`. The local writer now submits exact-parent file deltas, preserving
unrelated paths and explicitly deleting removals. Actual-head offline checks
confirm an unchanged 888-file tree produces no changes and one observation
produces three entries. Review/regressions cover incomplete responses, stale
refs, preservation and allowlisted validation diagnostics. After merge, verify
the same user-selected scope and record changed/deleted counts and the durable
ref outcome. Payload pressure is source-proven; the precise upstream 422 cause
and live resolution remain unproven. Do not broaden scope or permissions.

**Live writer verification (2026-10-06):** user-dispatched run `37507296743`
at merged `af7e597` succeeded with all ten selected discovery/delivery cycles,
duration `4m22.781120661s`, no new catalog proposal and no changed lifecycle
projection targets. Final durable head `5e1ad4b` matches the live ref. Bounded
tree writes and requested module-first log order are visible; no 422 occurred.
This closes the pending same-scope transport/format visibility check above,
not the upstream-cause investigation, full performance gate or worker-to-
publication/rollout tasks. Exact evidence remains in the linked findings entry.

An offline audit of state head `68cb75aae4205ca641080c8780984438ea183235`
preserved all 650 record payloads byte-for-byte, all 968 replay digests, the
existing fence and 232 module owners. See
[verification and remaining gates](FINDINGS.md#module-layout-manual-population).
The complete original phases below remain the broader TASK-038 plan, not a
claim that every phase is implemented: selected-aggregate-only decoding,
provider operational contracts, accepted-data relocation, complete declared
delta integration, controlled performance gates and live verification remain
open. Do not dispatch wider work or mark TASK-038 complete from this checkpoint.

### Goal And Boundaries

Replace scattered module operational records with module-owned aggregates,
share one versioned folder contract across accepted `data/` and operational
`state/`, and remove cold serial blob downloads through bulk local loading.
One selected module must retain the existing resolve/commit/deliver/acknowledge
barrier. Provider and global operations keep their separate ownership.

The proposed design has these non-negotiable correctness rules:

- `factory-state` remains the durable authority; Issues and Project stages
	remain projections. One displayed stage is not a lock. Preserve durable
	claims, monotonic fences, leases, retry/review protection and late-callback
	rejection, including during migration and recovery.
- Module-owned mutable state is discoverable under one module root without
	searching the old stage-oriented directories. Shared provider evidence is
	referenced by exact immutable IDs, not copied into every module. Global
	controls and catalog membership remain explicit commit preconditions.
- Use one `factory-state` branch and non-force expected-head updates. App-only
	access does not serialize runs, and Git has no directory-scoped force push.
	No permission changes, branch-rule bypasses or force pushes are in scope.
- One transition has one authorized owner and an atomic durable write set.
	A stage may need separate claim, progress, seal and acknowledgement commits;
	do not compress them into one write at the expense of crash recovery.
- Unrelated branch changes may trigger bounded semantic retries. Changes to
	the transition's module, ownership or relevant shared inputs require fresh
	validation or a conflict; never blindly replay side effects or rebase JSON.
- Preserve content identities, accepted history, pending outboxes, Issue
	bindings, timestamps, generations, replay history and fence high-water marks.
	A layout-config edit is not permission to move or reinterpret old records.

### Proposed Folder Contract

Add a reviewed `config/layout.yaml` and a typed resolver in `internal/identity/`.
Use named, supported layout profiles: `legacy-v1` describes today's exact paths;
`module-v2` describes the target below. Persist each tree's layout version and
the applicable contract digest; readers select that contract, not whatever
profile a later code checkout happens to select. Missing markers mean only the
documented legacy layout. Unknown versions/digests and ambiguous mixed layouts
fail closed. Configuration contains declarative directory/leaf mappings only,
not executable hooks, arbitrary templates or caller-selected traversal paths.

| Subject | Accepted root on `main` | Operational root on `factory-state` |
| --- | --- | --- |
| Module | `data/modules/<res|ptn|utl>/<canonical-module-id>/` | `state/modules/<res|ptn|utl>/<canonical-module-id>/` |
| Provider | `data/providers/<registry>/<namespace>/<provider>/` | `state/providers/<registry>/<namespace>/<provider>/` |
| Provider version | `versions/<version>/snapshots/<snapshot-id>/` below provider root | `versions/<version>/snapshots/<snapshot-id>/` below provider root for operational receipts only |
| Provider type | `resources`, `datasources`, `ephemerals`, `functions`, `actions` below the pinned snapshot | Same five-kind taxonomy when operational records require type ownership; no duplicated accepted payloads |
| Terraform core | Separate versioned core snapshot root; never treated as a provider | Separate core collection/import ownership if needed |
| Global | Catalog, dataset, shared definitions and derived cross-module indexes | Layout metadata, catalog observations, global controls, shared imports and fleet deployment/batch records |

Within each module's operational root, put `transaction.json`, lifecycle and
module controls, source pins/observations, requests, attempts, selections,
projection intents/delivery/bindings/evidence, module outboxes and publication
receipts. Keep immutable evidence under the accepted module's revisions and
context-specific selections/history; the two roots share identity and ownership
taxonomy, not identical evidence/control leaf documents.

An ownership table in the resolver must assign every existing record kind to
exactly one module, provider/core or global aggregate. Explicitly classify
catalog exports, fleet deployment receipts, projection batches and provider
invalidation; do not put fleet-wide records into an arbitrary module folder.
Request/attempt lookup and queue enumeration may use derived owner indexes,
but index entries never grant ownership or replace aggregate validation.

### Transaction And Read Contract

Move module replay history, generation and fencing metadata from the global
transaction document into the owning module's transaction document. Retain
separate global/provider metadata for their events. Preserve the existing fence
key and token semantics; physical relocation cannot restart counters. Migrate
legacy event IDs/digests without recomputing their meaning from a new path.

Replace complete-snapshot writes for module transitions with an explicit delta
containing the owned paths, expected aggregate generation/tree identity,
ownership/fence, event replay identity and relevant global/provider preconditions.
Apply that delta to the latest base tree, preserving all unloaded/unmodified
paths. Validate deletions explicitly. Multi-aggregate operations must declare
their complete read/write sets and commit atomically on the same branch; retain
full-snapshot validation for migration, integrity audits and those operations
until their narrower contract is proven.

For unrelated branch movement, reread the current ref and affected aggregates,
revalidate every declared precondition, and rebuild the commit with that head as
its sole parent. Retry at most three commit attempts, with context cancellation
and bounded jitter; then return a typed retryable conflict to the existing
orchestrator without changing ownership. Same-module or shared-precondition
changes do not qualify as unrelated retries. Verify a lost acknowledgement by
the durable event/receipt, not by assuming that a changed head proves failure.
Retry only the state transition, never discovery, dispatch or Issue mutations
that already succeeded.

Bulk loading uses a separate, shallow state checkout with credentials not
persisted. Run reviewed code only from `main`. Read immutable Git objects at an
exact commit, verify repository identity, paths, modes and object IDs, and seed
the run-local blob cache. Live refs and commit preconditions remain mandatory;
fetch missing/changed objects when the remote head advances. Initially decode
the full snapshot to preserve current semantics. Once aggregates are supported,
load the selected module and explicitly required shared preconditions directly;
do not search old stage folders or discover module ownership by scanning them.
Sparse checkout is optional and not assumed to reduce network transfer.

### Ordered Delivery Phases

Execute phases in this order; each requires the prior exit gate. All items below
are unimplemented. Review code through normal PRs; live gates remain separate.

| Phase | Implementation work | Exit gate |
| --- | --- | --- |
| 0. Inventory and approve | Map `internal/identity/paths.go`, `internal/state/store.go` path rules, state/discovery/queue/GitHub writers, CLI lookups, publish validators, index/site readers and schemas to the ownership table. Inventory path-bearing hashes/references and all retained workflow producers. Produce an exact-base migration mapping and benchmark fixture. | Ledermayer approves the concrete leaf-path map, identity compatibility, ownership and retry contracts before code changes. No unmapped record kinds or unknown path-dependent identities remain. |
| 1. Central resolver without moves | Add `config/layout.yaml`, typed profile loading/path resolution and neighboring identity tests. Route writers/readers/validators/indexes/exports through it while selecting `legacy-v1`. Keep frozen schemas unchanged; add versioned contracts only where required. | Existing paths and emitted bytes are unchanged. Round-trip path/kind/owner tests reject traversal, symlinks, cross-module/provider writes, unknown profiles and ambiguous layouts. Existing readers remain compatible. |
| 2. Bulk cold loading | Extend `state.BlobReadCache`/`GitHubBackend.Read` with verified local-object loading and fallback. Wire explicit CLI input and a pinned second checkout into `manual-discovery.yml` only; add neighboring workflow-contract tests. Keep the legacy transaction format and API commit path. | Cached and REST snapshots are identical; stale checkout and externally changed heads are observed; malformed objects fail closed; warm local blobs require zero per-blob REST downloads. Measure combined checkout plus command time. |
| 3. Aggregate transactions | Implement module/provider/global transaction metadata, declared preconditions and delta commits in `internal/state/`; integrate queue claims/recovery, discovery and projection acknowledgements through owning APIs. Add derived lookup indexes only where existing request-ID APIs require them. | Competing same-module claims yield one owner; concurrent unrelated modules both survive; relevant control/provider changes invalidate stale transitions; multi-aggregate commits remain atomic; lost acknowledgements deduplicate. No force update or unchanged-record replacement occurs. |
| 4. Compatibility and offline migration | Implement an exact-head read-only migration planner and staged candidate validator. Map legacy paths/metadata to `module-v2`, relocate references explicitly and rebuild affected indexes/manifests. Readers accept documented old/new profiles; writers use the profile persisted in their target tree. | Two migrations of the same input are byte-identical; record counts, identity inventory, generations, fences, replay history, pending work, bindings and receipts reconcile. Duplicate old/new owners fail closed. Any unavoidable identity change stops for explicit review. |
| 5. Reviewed cutover | After separate approval, pause/drain or fence every retained state writer/callback, capture exact `main`/state heads and recoverable snapshots, and recheck both bases. Publish accepted-data relocation through a reviewed PR; switch state in one expected-head transaction with `force: false`. Verify compatibility across the two-branch interval before resuming writers. | Both roots match their declared profiles; queues/owners/outboxes and Issue bindings are preserved; a stale base aborts; old callbacks are rejected or handled by reviewed compatibility. Rollback is tested before activation. |
| 6. Bounded live verification | With approval, run the same production path for one module, then ten, including unchanged/forced cases and an explicitly authorized parallel-different-module case. Record refs, scope, request/fence identities, receipts, API counts and timings in FINDINGS. | No unrelated state changes or duplicate effects; old history remains readable; cold-loading improvement is measured. Promote checkout/aggregate behavior to factory workflows only after reviewed manual evidence; do not enable schedules or wider dispatch. |

### Verification And Rollback

Use neighboring fixture tests rather than credentials or operational commands.
Cover TEST-001/002/004/006/007/008/010/012/014/017/019/021 as applicable, plus
workflow contracts and the repository Go/schema/YAML gates. Git is allowed only
in trusted workflow transport/preparation, not as a new dependency of extraction,
assessment or accepted-data rendering under TEST-019.

Required regressions include two module deltas prepared from the same branch
head, a provider/global precondition change between validation and commit,
same-module conflicting owners, expired leases/stale callbacks, commit-success
with lost acknowledgement, deletion preservation, migration with outstanding
outboxes and a rollback after new-layout writes. Migration must retain replay
protection for pre-migration event IDs, even when their old paths are gone.

Benchmark at least five offline cold/warm iterations against the same 649-file
fixture shape and a larger synthetic state tree. Report median/p95 checkout,
snapshot decode and commit time, request counts and bytes separately. Proposed
performance gate: at least 80% fewer cold REST requests and 50% lower median
cold checkout-plus-read time versus the legacy loader in a controlled comparison;
live runner measurements remain separate, not promised results. Include integrity
and retry overhead rather than timing only the filesystem read.

Rollback never resets or force-pushes a branch. Before cutover, prove a
compatibility-capable code rollback and a forward, expected-head inverse
migration that preserves writes made since cutover. If inversion cannot retain
new records/owners/receipts, keep writers paused and use compatibility-capable
recovery rather than restoring an old snapshot over newer state. Keep old-layout
readers until recovery/retention gates explicitly permit their removal.

Review approval must cover the concrete folder/ownership map, local transaction
metadata, non-force retry rules, compatibility and migration strategy, and the
performance gates. Approval to implement phases does not authorize a live
cutover. No acceptance, deployment, schedule, permission or product gate is
closed by this plan.

## Required Validation

These are test obligations, not executed test results. Full definitions live in
the [delivery testing contract](../../plan/refactor-avm-intelligence-delivery-2.md#6-testing).
Store verification evidence with the owning task; all applicable repository CI
gates remain required in addition to these behavior checks.

- [ ] **TEST-001:** Preserve unrelated accepted modules byte-for-byte across all six proposal kinds; reject malformed/stale/cross-kind writes. Publish catalog-only additions/retirement with zero successful workers.
- [ ] **TEST-002:** Reuse durable state across clean workspaces; unchanged input creates no duplicate request. Empty-facts discovery runs acquisition and wakes assessment after restart. Test moved tags, two-clean-observation retirement, failed/partial feeds and unresolved applicability.
- [ ] **TEST-003:** Type and explicit-ID filters affect scheduling only; excluded modules remain cataloged and are not falsely blocked.
- [ ] **TEST-004:** Competing claims/duplicate dispatch yield one owner. Test dispatch crash, lease expiry, unknown run lookup and stale callbacks; sealed v4 evidence survives later v5 claims. Restore high-water marks; missing unexported state stops for recovery review.
- [ ] **TEST-005:** Run known-root/nested/rootless/component fixtures through native checks and HCL/JSON extraction; cover aliases, child mappings, five kinds, functions and lock scope. Compare unchanged legacy semantics and require reviewed differences.
- [ ] **TEST-006:** Preserve multiple versions/policies, immutable same-SHA source records, append-only aliases and equal-facts/new-producer provenance. Reuse byte-identical dimension IDs; reject dangling references and invented migration facts.
- [ ] **TEST-007:** A failed module or rejected PR cannot block another module or catalog/provider proposal. Recover lost acceptance/export acknowledgments and CAS-before-export crashes without duplicate PRs/reanalysis. Artifact expiry has explicit recollection state.
- [ ] **TEST-008:** Inject discovery, retirement and provider/policy imports after validation/before merge, and after selection CAS/before export. Old evidence is historical and exports are as-of. Reverse export order/crash reconciliation; preserve monotonic pointers, unrelated records and independent v4/v5 selections.
- [ ] **TEST-009:** Reject workflow/deployment SHA mismatches and incorrect analysis/selection membership. Reverse/duplicate callbacks and authorized rollback cannot inherit stale success. Test pending/unknown deployment, mixed Blocked/Publishing and Backlog/Analyzing, unresolved/empty contexts, real no-track fallback and all-current-context Done.
- [ ] **TEST-010:** New desired B survives active A. Retirement issues a higher-sequence null selection and rejects late promotion; failed Issue closure remains retryable. Reappearance reuses the Issue, and stale remote writes leave repair intent rather than acknowledge old Done.
- [ ] **TEST-011:** An unchanged 145-module fixture with no intents makes zero Issue/Project calls; an explicit bounded audit may read but makes zero mutations when state matches.
- [ ] **TEST-012:** Preserve cursors/partial success under Issue-success/field-failure, 429, rate-limit 403, transient 5xx/GraphQL errors, secondary throttles, budget exhaustion and lease expiry. Duplicate markers and actual permission/schema errors require review.
- [ ] **TEST-013:** Verify every configured Project field, owned label, publication identity, stable summary and exact marker. Preserve human notes/non-owned labels; repair drift only through a bounded audit or relevant intent.
- [ ] **TEST-014:** Keep provider/address/kind/name identities distinct; incomplete pagination, unsupported kinds, absent extraction and empty unfetched snapshots never become complete/pass. Accept zero-dependent provider snapshots and separate pinned core metadata; missing core context stays unavailable.
- [ ] **TEST-015:** Refresh provider-dependent dimensions with zero checkout, parsing or external analysis. Keep unrelated dimensions byte-identical, record new inputs and display stale last-good data honestly.
- [ ] **TEST-016:** Cover pessimistic/multiple/excluded/prerelease/invalid constraints. Constraints and example locks cannot imply consumer installation; heuristic issue matches cannot become confirmed defects.
- [ ] **TEST-017:** Reject path/symlink/config/workflow injection, wrong producer/run/attempt, untrusted code, raw source expressions and mismatched source/tool hashes in artifacts and publication.
- [ ] **TEST-018:** Navigate Catalog, Module, Quality, Provider/Type, Currency, Issues, Activity and Operations at all Phase 6 viewports with never-analyzed/blocked/retired/stale/failed/partial fixtures. Check reciprocal links, origins, timestamps, keyboard/accessibility and health-only presentation updates without leaked state. Failed/delayed deployment cannot self-certify; receipt arrival alone creates zero exports/deployments.
- [ ] **TEST-019:** Extract, assess and render supplied fixtures with IaCLens, Python, Terraform, provider binaries and Git absent from executable paths and network denied. Reject hidden subprocess/network calls, unknown/duplicate registry IDs and arbitrary executable configuration; optional failure preserves completed results. Test build/platform dependencies separately.
- [ ] **TEST-020:** Selected reruns preserve input/version identity and unrelated dimensions; disabled/unselected/missing/error checks never pass. Cold-start/bounded bootstrap shares facts across independent tracks; missing dependencies/budget exhaustion stays unresolved, never empty-set Done.
- [ ] **TEST-021:** TASK-021 leaves production activation unchanged; TASK-043 waits for TASK-022 through TASK-027 fixture integration. Validate its first native workflow/writer/selection candidate, reject fenced old callbacks and preserve legacy bytes/provenance/unmapped checks. TASK-028 follows cutover.
- [ ] **TEST-022:** Before unattended-policy activation, test eligible data-only proposals versus code/config/schema/workflow/dependency/path changes, spoofed producers, stale bases and exceptions. Reject invalid receipts, retain human review for exceptions and preserve existing approval waits when policy is unapproved.

## Initial Operating Bounds

Implement these reviewed starting values through the named tasks, not ad hoc
workflow constants. They are design defaults, not evidence that schedules or
permissions are currently configured. Enabling them still requires phase gates.

| Control | Initial contract | Owner |
| --- | --- | --- |
| Discovery | Nightly `17 2 * * *` plus authorized manual refresh; invalid feeds retain last-good state. | TASK-013, TASK-014 |
| Compute/recovery | State-change wake-ups and 15-minute recovery; one compute slot, 20-minute worker bound, 30-minute lease and 5-minute heartbeat target; verify terminal run before reclaim. | TASK-012, TASK-014, TASK-022 |
| Provider collection | Daily conditional fetch, one provider per claim; bounded responses, extraction, artifact size, pages and wall time in reviewed configuration. | TASK-029, TASK-030, TASK-033 |
| Projection | Events and 15-minute flush; at most 25 changed modules/batch, 20% primary-budget reserve, stop sooner on any enforced limit; weekly bounded drift audit. | TASK-026, TASK-027 |
| Projection order | Due retries, discovery/changed results, queued/analyzing state, then blocked/retired cleanup; stable module ID within each class. | TASK-026 |
| Transient retries | Three immediate attempts with jitter/server-directed delay, then durable next-retry time; permission/schema failures require review. | TASK-026, TASK-030, TASK-033 |
| Artifact/state retention | Sanitized pending-review artifacts 30 days; failed-attempt summaries 90 days with outcome/expiry-preserving compaction; accepted facts/results retained. No raw source-bearing extraction or legacy reports uploaded by default. | TASK-022, TASK-038 |
| Storage | Measure bytes, writer latency and review/export age before changing retention, compaction, storage or coalescing policy; Git history is not bounded physical retention. | TASK-038 |
| Publication | Serialize complete snapshots from canonical changes or authorized exact-commit repair, with trusted generation/membership receipts and current approval policy. Receipt-only updates cannot create a build loop. | TASK-025, TASK-034, TASK-042 |

## Product Acceptance Surfaces

Use this baseline in TASK-035, TASK-037 and TASK-039. Required unavailable
evidence must be shown honestly; missing implementation is not an acceptable
substitute for a baseline feature.

| Surface | Required experience |
| --- | --- |
| Catalog | All discovered modules, desired/analyzed/published versions, lifecycle, search and type/domain/provider filters. |
| Module | Component/example ownership, evidence/coverage, source/tool/policy identities, version and analysis selectors. |
| Quality | Dimension navigation, assessed/applicable/unchecked counts and per-check status/evidence matrix. |
| Provider/Type | Provider-qualified observed usage versus documented catalog coverage, all five kinds and reciprocal module links. |
| Currency | Constraint eligibility and change evidence with exact input/snapshot versions/dates, not unsupported runtime-compatibility claims. |
| Issues | Separate factory operations, module upstream and provider issues, with provenance and remediation links. |
| Activity | Distinct discovery, source release, assessment, acceptance and deployment timestamps. |
| Operations | Backlog/blocked/failed/stale visibility, sanitized as-of collector health, recovery information and Factory board links. |

## Cohesion Finding Traceability

All nine findings are closed only at documentation level. Their runtime
counterexamples remain required by the tasks/tests above; do not reopen rejected
design alternatives to satisfy them.

| Finding | Required delivery evidence |
| --- | --- |
| CR-001: Cutover ordering | TASK-021, TASK-043, TASK-028; TEST-021. |
| CR-002: Cross-branch selection race | TASK-012, TASK-023, TASK-024, TASK-025; TEST-004, TEST-007, TEST-008, TEST-009, TEST-010. |
| CR-003: Cold-start acquisition | TASK-012, TASK-013, TASK-014, TASK-022, TASK-041; TEST-002, TEST-020. |
| CR-004: Independent non-module proposals | TASK-013, TASK-016, TASK-023, TASK-024, TASK-030, TASK-033, TASK-036; TEST-001, TEST-007, TEST-014, TEST-015. |
| CR-005: Public as-of state | TASK-016, TASK-023, TASK-025, TASK-034; TEST-009, TEST-018. |
| CR-006: Multi-track reducer/projection state | TASK-027, TASK-034; TEST-009, TEST-010, TEST-012, TEST-013. |
| CR-007: Immutable source/dimensions | TASK-016, TASK-018, TASK-020; TEST-006, TEST-020. |
| CR-008: Missing view/core-collector ownership | TASK-030, TASK-034, TASK-035, TASK-036, TASK-037; TEST-014, TEST-018. |
| CR-009: Roadmap precedence | Maintain the authority links above and v2-scoped IDs under TASK-004; do not adopt conflicting v1 factory requirements. |

## Previous Open Items

The former four open entries are carried forward without duplicate work:

| Previous item | Current disposition |
| --- | --- |
| Projection v2 redesign | TASK-026 and TASK-027, with integrated rollout TASK-043/TASK-028. |
| Verify first-cohort Done; blocked/retired lifecycle | TASK-003 verifies historical deployment evidence; TASK-009/TASK-025 correct publication identity; TASK-027 and TASK-028 verify lifecycle/recovery. |
| Approved SoftwareOne assets | TASK-037 requires approved assets before styling. |
| Bounded semantic sample | Deferred outside the completion baseline; still requires explicit public-input, budget and processing approval. No mandatory AI dependency or live sample is introduced. |

Optional version diffs, aggregate scores, richer use-case comparisons, broader
provider coverage, deeper historical backfill, isolated executable schema
collection and autonomous remediation are not new completion tasks. Each needs
its own approved scope/policy where applicable. Do not resurrect an event
deadline, demo branch/snapshot, alternate runtime or standalone IaCLens release
dependency. Public repository/Project/Pages visibility remains separately gated.

## Historical Completed Milestones

The entries below are preserved from the previous TODO list, not reverified by
this documentation update. Their checkmarks record implementation/milestone
history, not compliance with v2. In particular, prior local "durable" requests,
fleet aggregation and exact-commit Done claims are subject to TASK-005 through
TASK-014 and TASK-022 through TASK-027 verification/correction. Event scope and
IaCLens pins below are historical, not the selected future runtime or deadline.

- [x] Record sponsorship, event scope, public-data policy, and the IaCLens `v0.1.0` pin.
- [x] Add the fixture-only factory skeleton and read-only CI.
- [x] Prove the single public AVM pilot transaction: analysis run `36847787791`, Issue #8, and Project item with source provenance. Keep its state `Publishing` / `Unassessed` until publication succeeds.
- [x] Upsert the pilot Issue and Project item with a reduced installation token; preserve human Issue notes on retries.
- [x] Resolve active accepted catalog modules to the highest published stable SemVer commit; reject moved tags and conflicting `v`/unprefixed aliases.
- [x] Add durable request state with complete analysis fingerprints, unchanged-work coalescing, failed-attempt retries, one newer pending input per active module, run ownership, and lost-run reconciliation.
- [x] Build a deterministic planner from accepted catalog state and resolved pins; register per-module work and emit dispatch batches capped at 100 while keeping unresolved/retired modules explicit.
- [x] Resolve immutable release pins for every active accepted module in stable ID order, fail closed on any unresolved module, and preserve prior pins for moved-tag verification.
- [x] Reconcile durable fleet requests from explicit run states, promote pending work, and emit bounded recovery batches without inferring absent runs.
- [x] Orchestrate scheduled dry-run plans and authorized manual fleet workers. Discovery state remains separate from published data; suspicious shrinkage requires review and modules retire only after two successful observations.
- [x] Add exact-commit publication manifests and an approval-gated Pages deployment workflow; deployment fails closed until accepted data and generated site trees exist.
- [x] Validate and assemble all successful worker artifacts into a complete candidate data tree with plan, request, provenance, duplicate, and completeness checks.
- [x] Atomically bundle validated candidate data, accepted discovery state, and durable requests in the single aggregation job.
- [x] Submit the accepted bundle as a data-only pull request with current-HEAD conflict detection and non-force branch updates.
- [x] Build an evidence-first static catalog and module detail site from accepted data without aggregate scores or unassessed claims.
- [x] Add the manual exact-commit Pages deployment workflow. Historical implementation milestone only: the standalone workflow uses `data-publisher` without a required reviewer; see [TASK-002 controls](FINDINGS.md#task-002-read-only-control-inventory) and [F-001](FINDINGS.md#f-001-deployment-receipt-matches-board-reconciliation-does-not).
- [x] Keep accepted modules without a published stable release explicit and blocked instead of aborting the fleet plan.
- [x] Add manual resource/pattern/utility module filters and short-lived per-worker artifacts; retain accepted bundles longer for review.
- [x] Add optional manual module-ID selection for fast targeted analysis runs.
- [x] Accept and review the first ten-module canonical cohort from run `36869616628` in PR #28.
- [x] Deploy and verify the first accepted cohort at `https://ideal-adventure-62gqzpm.pages.github.io/`.
- [x] Project selected actionable fleet work to recurring Issues, Project items, and phase/type labels using the [board operations contract](factory-operations.md).
- [x] Reconcile accepted module reports to Publishing board state after canonical data changes.
- [x] Skip unchanged board projections and retry transient GitHub GraphQL service errors.
- [x] Retry transient GitHub REST projection failures without weakening validation.
- [x] Add the Pages-completion projection workflow. Historical implementation milestone only, not proof that Done matched the deployed accepted commit; see [F-001](FINDINGS.md#f-001-deployment-receipt-matches-board-reconciliation-does-not) and follow-up TASK-009/TASK-025.
- [x] Post-filter board Issue search to exact stable module markers before fleet reconciliation.
