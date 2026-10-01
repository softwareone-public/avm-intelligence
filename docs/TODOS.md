# Factory Tasks

Owner: Ledermayer.

Updated: 2026-10-01. Status: planned; v2 implementation scope approved; activation approval pending.

## Scope And Authority

Complete AVM Intelligence through one production path. The v2 implementation
scope is approved; this checklist does not authorize repository control changes,
live collection, or deployment.

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

Keep current canonical PR reviews and Pages approvals until a separately approved
data-only automation policy is enforced. Confirm that self-contained runtime
allows reviewed compiled Go libraries and necessary GitHub/public source APIs.
No Terraform/provider/upstream code execution, mandatory AI, private-content
transfer, blanket App bypass, automatic visibility change or autonomous remediation
is authorized. Preserve `pass`, `fail`, `unchecked` and `not_applicable`; keep
execution failure, coverage, freshness and delivery status separate.

## Execution Rules

There are 43 v2 tasks and 22 required validation cases below. All tasks other
than TASK-001 through TASK-010 remain open; existing code and historical
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
- [x] **TASK-002: Inspect actual controls read-only.** Inventory repository rules, Project field IDs/options, job-specific App permissions and Pages environments. Verify whether a JSON-only `factory-state` branch can be approved without weakening `main`; otherwise stop activation pending an approved store. Assess narrow unattended-policy feasibility now, not after implementation. Evidence and the organization-rules caveat are recorded in [FINDINGS.md](FINDINGS.md#task-002-read-only-control-inventory); verify organization-level rules before state-write activation.
- [x] **TASK-003: Freeze recoverable evidence.** Record the canonical commit, candidate/report hashes, full module IDs, actual deployed commit/receipt and reproducible site/data. Recheck the first cohort's publication/board reconciliation using real receipts. Reproduce the local 232 discovered / 145 accepted report baseline and its coverage counts without treating them as live-site evidence or permanent expected totals. Evidence and the verified board mismatch are recorded in [FINDINGS.md](FINDINGS.md#task-003-frozen-baseline-evidence).
- [x] **TASK-004: Reconcile the evidence register.** Consolidated the source-proven v2 gaps, evidence links, and current status distinctions in [FINDINGS.md](FINDINGS.md#source-proven-v2-gaps-not-incident-claims). Corrected historical deployment/reconciliation claims only from the verified receipt and live Project state. Source-proven risks remain distinct from observed run incidents.

## Phase 1: Correctness Containment

Dependency: Phase 0 approval. Exit: a targeted update preserves the complete
accepted cohort, native-migration expectations are captured, and mismatched
workflow/deployment SHAs cannot produce Done.

- [x] **TASK-005: Establish the lossless-acceptance regression.** Added a two-module base/one-module update regression requiring unrelated reports and requests to remain byte-identical, plus failed, stale, duplicate, and conflicting worker-delta cases. The preservation regression first failed because the untouched report disappeared; it passes after TASK-006. Evidence: [acceptance regression](../internal/publish/accept_test.go) and [invalid-delta tests](../internal/publish/candidate_test.go).
- [x] **TASK-006: Preserve the canonical base.** Added `ApplyModuleDelta` in [accept.go](../internal/publish/accept.go); `AcceptCandidate` remains as a compatibility entry point. The merge copies the accepted tree, overlays validated reports and request changes, regenerates full candidate membership, and swaps atomically. The factory CLI now calls the delta API. `go test ./... -count=1` passes.
- [x] **TASK-007: Restore fleet input continuity.** The workflow seeds prior discovery, requests, and source pins; resolved pins are retained in the accepted bundle for later moved-tag checks. `PlanAcceptedCatalog` receives explicit type/ID selection, preserves full catalog membership, and marks filtered entries `not_selected` without creating requests or projecting them. Schedules remain plan-only. The legacy baseline has no exact pin history, so earlier tag moves cannot be reconstructed; see [FINDINGS.md](FINDINGS.md#task-003-frozen-baseline-evidence). `go test ./... -count=1` passes.
- [x] **TASK-008: Freeze native semantic expectations.** Added synthetic schema-v2 legacy-output fixtures under [testdata/assessment](../testdata/assessment/legacy-v0.1.0.json), tied by test to the configured ruleset and pinned IaCLens producer. The corpus records component/example ownership, explicit root/module and backend-signal classifications, deterministic outcomes, and rootless/nested unknowns. No legacy CLI or analyzer behavior changed. `go test ./... -count=1` passes.
- [x] **TASK-009: Correct publication identity.** Pages publication now uploads a run-scoped receipt containing accepted commit, data/site digests, producer run, and candidate module membership. The completion workflow accepts only successful manual runs from `main`, downloads that run's receipt and accepted dataset commit, and the Done projector verifies producer run, dataset digest, and exact membership before token creation or GitHub writes. Missing/mismatched receipts fail closed. Full Go tests pass; the historical mismatch is documented in [FINDINGS F-001](FINDINGS.md#f-001-deployment-receipt-matches-board-reconciliation-does-not) and requires a new live run after merge.
- [x] **TASK-010: Repair partial projection correctly.** `UpsertFleet` now skips only a matching bot-owned summary with a post-success acknowledgement marker. It writes that marker only after Project fields and owned labels succeed; partial failure leaves the projection retryable. Published commit participates in the summary and Project field sync. Recovery tests cover Project failure, label failure, retry, verified no-op, and changed commit. No fleet-wide drift audit was added; bounded repair intents and audits remain TASK-026/TASK-027.

## Phase 2: Durable Discovery And Claims

Dependency: Phase 1 and approved state-branch write policy; TASK-012 also requires
TASK-016. Exit: unchanged discoveries across fresh workspaces create no duplicate
work, competing claimers produce one owner, and canceled dispatch is recoverable.

- [ ] **TASK-011: Implement transactional state.** Add proposed `internal/state/` with `ReadSnapshot`, `ApplyEvent` and `CommitExpectedHead`. Validate typed JSON, idempotent events, expected-head fast-forward writes and monotonic fencing. Test concurrent writers, stale events and crash-before-acknowledgment. Execute only reviewed pinned `main` code, never state-branch content.
- [ ] **TASK-012: Model acquisition and assessment separately.** After TASK-016, extend [request.go](../internal/queue/request.go) with `acquire_source`/`assess`, context/track, desired generation, leases/fences and retry classes. Acquisition pins immutable source and extractor capabilities without requiring future facts or known eligibility; accepted facts wake assessment with pinned definitions/snapshots. Persist applicability/candidate states, explicit `latest-source` fallback, per-context coalescing and one compute claim. Seal trusted completion before releasing compute; retain reusable results and `awaiting_acceptance` independently.
- [ ] **TASK-013: Persist discovery and independent catalog publication.** Adapt [state.go](../internal/catalog/state.go) and source resolution to atomically record full observations, release/tag/SHA history, pins, acquisition and Backlog/Blocked/Needs review/retirement intents. Emit `catalog` outbox proposals even without successful analysis. Preserve release-less modules, two-successful-observation retirement and moved-tag detection; keep overall latest separate from track candidates. A v5 change must not discard v4 work, and unknown applicability is not no-track fallback.
- [ ] **TASK-014: Separate discovery, dispatch and recovery.** Select source-priority work, then oldest desired time, then stable module ID, including cold-start acquisition. Claim a factory attempt before dispatch and bind the Actions run idempotently afterward. Heartbeat, verify actual run state before reclaim, retain ownership on unknown lookup, and reject duplicate dispatched owners. Approval waits release the one compute slot.
- [ ] **TASK-015: Add authorized operational commands.** Persist identity-validated pause/resume/retry/refresh events, reasons, global disable and idempotent wake-ups. Accept only allowlisted module IDs and approved policy; neither Issue text nor card movement authorizes work. Prove claims continue when projection is unavailable.

## Phase 3: Native Facts And Evidence

Dependency: Phase 1; schemas/fixtures may run alongside Phase 2. TASK-016 precedes
TASK-012. Exit: legacy dry-run output remains readable; two versions and two
same-source assessments coexist with independently visible dimensions and coverage.
No migration merge or producer activation occurs in this phase.

- [ ] **TASK-016: Freeze schemas and identities.** Define contracts under [schemas](../schemas/normalized-analysis-v1.schema.json) for requests, sealed completion, immutable source/facts/provenance, definitions/dimensions/analyses, provider/core snapshots, six proposal kinds, selections, presentation and deployment. Implement the operating-model canonical paths/hashes, safe-path fixtures and reference validation. Keep `source.json` immutable, aliases append-only, same-content producer provenance separate and dimension results content-addressed; never relabel legacy producer evidence.
- [ ] **TASK-017: Build the factory-owned extractor.** Add proposed `internal/terraform/ExtractFacts` using a maintained, license-reviewed, pinned Go HCL library for native HCL and Terraform JSON. Preserve safe full provider requirements/bindings/aliases, resource/data usage, child source/version/provider mappings, unit paths, locations and per-capability coverage. Reject malformed/unsafe input; exclude arbitrary source expressions and machine paths. Use no IaCLens internal imports or analyzer subprocess. Run the migration corpus and revise the effort estimate before widening extraction.
- [ ] **TASK-018: Build scoped dimension/check assessment.** Add proposed `internal/assessment/` with compiled versioned definitions/registry, reviewed classification, `RunDimensions` and CLI list/select commands. Checks consume supplied facts/snapshots offline, expose applicability/input IDs/evidence/outcomes/execution/coverage and reuse independent results. Map public legacy deterministic checks through reviewed fixtures; changed semantics get new identities and unmapped checks stay historical. An optional-input failure cannot erase successful dimensions.
- [ ] **TASK-019: Add dry-run-first migration.** Extend [cmd/factory/main.go](../cmd/factory/main.go) to map legacy records into SHA-keyed revision/analysis history. Preserve original report bytes/digests and known provenance. Missing facts, versions or input identities stay unavailable, never inferred from today's configuration or latest release.
- [ ] **TASK-020: Build complete, explicit indexes.** Generate catalog/dataset/track/module/provider/dimension-dependency/presentation indexes from accepted records and selection receipts. Never choose current analysis by sorting history. Enforce shared facts, immutable dimension references, full provider identity, exact target vectors, monotonic as-of selection exports and no dangling or self-referential hashes.
- [ ] **TASK-021: Prove reader readiness only.** Add dual reads to [site/catalog.go](../internal/site/catalog.go) and [github/projection.go](../internal/github/projection.go). Compare dry-run baseline IDs, counts, original bytes and unchanged-meaning check evidence; review semantic differences. Leave production activation unchanged until TASK-043; retain historical readers, not an automatic IaCLens runtime fallback.

## Phase 4: Independent Module Delivery

Dependency: Phase 2 plus Phase 3 contracts/readers. Integrate TASK-022 through
TASK-027 with fixtures before TASK-043, then TASK-028 live validation. Provider
snapshots may be fixtures until Phase 5. Exit: one module can publish while
another fails, late work cannot supersede newer desired work, and projection
converges after partial failure/throttling.

- [ ] **TASK-022: Integrate native workers and trusted validation.** Adapt [module.yml](../.github/workflows/module.yml) for fenced acquisition/assessment, facts reuse and independent dimension outcomes. Seal completion before compute release; workers hold no canonical write credentials. Submit facts-only or analysis-bearing `module_evidence` artifacts, remove the all-worker-success barrier, and prove cold-start acquisition through accepted facts to assessment before activation.
- [ ] **TASK-023: Accept six typed proposal kinds.** Add `ApplyProposal` with `ApplyModuleDelta` as evidence adapter in the publish package. Support `module_evidence`, `catalog`, `provider_snapshot`, `module_observation`, `selection_export` and `presentation`, each with its subject/path/producer/precondition rules. Evidence merge adds history only. Selection exports use separate data PRs with expected pointers and monotonic subject sequences enforced at actual merge; rebuild indexes and rerun checks/reviews on base changes. Integrate catalog outbox entries, preserve unrelated records and forbid force pushes or fleet barriers.
- [ ] **TASK-024: Reconcile acceptance, selection and exports.** Implement receipt import, `SelectAccepted` CAS and durable selection outbox. CAS matches current desired inputs, lifecycle, eligibility and sealed completion, not a later track's lease; atomically persist selection, sequence, receipt and export intent. Define provider/policy input-import invalidation generations. Export merge never reselects; stale evidence is historical. Recover lost acknowledgments/exports without duplicate PRs or analysis, preserve restore fence/generation high-water marks and park missing unexported state for review.
- [ ] **TASK-025: Verify complete-dataset publication.** Extend [manifest.go](../internal/publish/manifest.go) with dataset/generator identity and module/context analysis-plus-selection membership. Serialize deployment generations, including authorized rollback, and recheck ancestry/current target after approval. Verify actual receipts and CAS-match active generation plus desired/selected inputs before Done. Reversed/duplicate callbacks are history; pending/unknown deployment never inherits prior success.
- [ ] **TASK-026: Implement bounded Projection v2.** Update [client.go](../internal/github/client.go) around durable intents/bindings, paginated snapshots, exact-marker filtering, complete owned-field diffs, budgets and fenced retries. An empty queue makes zero Issue/Project calls. Checkpoint snapshot pagination and partial mutations; account separately for REST, GraphQL, search and secondary limits. Use event/15-minute flushes and a separate weekly drift audit, not a nightly fleet rewrite.
- [ ] **TASK-027: Share the lifecycle reducer.** Implement `ReduceModuleStatus` in the queue package from the operating-model truth table, including acquisition, applicability, fallback, mixed tracks and controlling-source fields. Projection delivery uses only `pending`, `projecting`, `projected`, `retryable`, `needs-review`; retirement/reappearance reuse the same Issue binding. Preserve human text/non-owned labels and exact markers; migrate to stable bot summaries without deleting notes. Verify all configured fields and current required selections, recheck generations around remote writes and retain repair intents for stale writes. Resolve blocked/retired lifecycle backlog without conflating delivery with quality.
- [ ] **TASK-043: Perform the integrated native cutover.** Only after TASK-022 through TASK-027 fixture gates, pause legacy registration/acceptance, reconcile/drain/fence old attempts and repeat migration dry-run against current `main`. Merge reviewed migration preserving original bytes and explicit legacy as-of selections. Activate one native producer/integrated writer, retire IaCLens execution/download pins and run TEST-021 on the first native candidate and late old callbacks. Preserve readers/recovery evidence; do not enable broad scheduling before TASK-028.
- [ ] **TASK-028: Validate one module, then ten.** After TASK-043 and live authorization, exercise the same production path and record cold start, sealed completion, evidence/export PR receipts, actual commits, deployment membership, mixed-track reduction, note preservation, API headroom and independent failure recovery. Fixture provider inputs do not establish live provider coverage. These gates do not constitute full-product acceptance.

## Phase 5: Provider Intelligence

Dependency: Phase 3 contracts; fixture work may run alongside Phase 4, but live
collection requires Phase 4 recovery gates. Exit: relevant provider changes rerun
only dependent enrichment with no source scan and cannot turn incomplete coverage
into pass.

- [ ] **TASK-040: Complete five-kind extraction.** Extend the native extractor to ephemeral/action bindings, nested provider/core functions and relevant lock selections across HCL/JSON. Test custom providers/aliases, child mappings, comments/strings, malformed input, lock scope and all five symbol kinds. Preserve unknowns; empty arrays are not proof of implemented support. Never execute providers.
- [ ] **TASK-029: Define provider collection policy.** Add proposed `config/providers.yaml` and `internal/provider/` with full provider addresses, approved public sources, licenses, per-kind capabilities, limits and immutable manifests. Start with AzureRM and AzAPI; mark other observed providers not collected. Test cross-provider/name and same-provider/cross-kind collisions.
- [ ] **TASK-030: Collect versioned provider and core metadata.** Implement `CollectVersionMetadata`, `CollectTypeCatalog`, `CollectChangeKnowledge` and separate `CollectCoreFunctionMetadata`. Pin allowlisted public docs/source/releases/issues with conditional requests, bounded pagination, source/collector digests and explicit errors/coverage. Emit independent `provider_snapshot` proposals even with zero dependent modules. Store resources, datasources, ephemerals, provider functions and actions separately from versioned Terraform core functions. Undocumented kinds/missing core context remain unavailable; no plugins.
- [ ] **TASK-031: Assess currency and type evidence offline.** Register checks from proposed `internal/intelligence/` against accepted facts/pinned snapshots. Use a tested constraint library; distinguish selected-version freshness, track eligibility, type availability, issue relevance and heuristic confidence. Missing transitive dependencies are unknown; example locks do not define consumers' installed versions and major badges/issue mentions prove neither compatibility nor defects.
- [ ] **TASK-032: Index dependencies and invalidate narrowly.** Derive full provider/type usage and dimension dependencies from accepted facts. Relevant accepted snapshots enqueue enrichment-only refresh; use provider-level fallback when symbol extraction is incomplete. Reuse unaffected dimensions and retain visibly stale last-good observations on collector failure.
- [ ] **TASK-033: Integrate independent collection and enrichment.** Add provider workflows that accept snapshots, import receipts/invalidation through TASK-024 and enqueue `assess` via TASK-012. Persist sanitized failed-collection health independently from last-good data. Prove zero source checkout/parser/AI on refresh; rerun only dependent dimensions. Retain active-major history and review new provider allowlists before activation.
- [ ] **TASK-041: Bootstrap provider tracks within bounds.** Implement newest-first AzureRM 4.x/5.x candidate acquisition with reviewed release-count/request/byte/wall-time limits and persistent progress. Share facts but keep independent selections; exhausted search or missing dependencies stays unresolved, not latest-compatible or Done. Require explicit policy for deeper backfill and track retirement.

## Phase 6: Portal And Controlled Rollout

Dependency: Phase 4 for catalog/operations and Phase 5 for provider/currency.
Exit: every baseline surface, all-five-kind extraction, check evidence, history,
refresh, recovery and separately approved unattended delivery is verified.
Unavailable upstream evidence stays explicit; unimplemented required capability
or missing automation approval means the product remains incomplete.

- [ ] **TASK-034: Export safe as-of presentation state.** Add `internal/site/ExportPresentation`, immutable allowlisted snapshots and coalesced `presentation` proposals via TASK-023. Trigger on discovery/lifecycle, selection, collector health and daily freshness checkpoints, never receipt arrival alone or lease heartbeats. Render catalog/module/history with shared reduced status, coverage and explicit generation/time; include never-analyzed/blocked/retired modules. Distinguish this dataset's contents from prior verified publication; expose no raw control state and never certify the page's own deployment.
- [ ] **TASK-035: Implement every evidence/navigation surface.** Add Provider/Type, Currency and Quality views with five-kind navigation, track/version/analysis selectors, reciprocal usage and dimension/check evidence. Add dedicated Issues and Activity views separating factory/module/provider origins and discovery/source/assessment/acceptance/deployment times. Provide empty/stale/error states; use fixtures until TASK-036 integration. Optional version diffs and aggregate scores are not completion requirements.
- [ ] **TASK-036: Collect public module observations independently.** Add sanitized issue/release/commit collectors, immutable `module_observation` proposals, acceptance/health/invalidation integration and wiring into TASK-035. Keep human-reviewed use-case curation in proposed `curation/`, never collector-owned policy. Define evidence/freshness contracts with public/synthetic fixtures, not private rules or predecessor scores.
- [ ] **TASK-037: Approve branding and verify all views.** Obtain approved SoftwareOne assets and usage/font rights before styling. After TASK-034 through TASK-036 integration, test every baseline surface at 390x844, 768x1024, 1440x900 and 200% zoom. Check navigation, reciprocal links, keyboard use, accessible empty/stale/error/coverage states and distinct timestamp meanings; do not invent official branding or misleading scores.
- [ ] **TASK-038: Operationalize recovery and observability.** Add dashboards/runbooks for queue age, unknown coverage, collector freshness, leases, review/export waits, publication/projection lag, API cost/headroom, storage growth and artifact expiry. Test pause/resume, exact-commit redeploy, board-only repair and authorized rollback without reanalysis. Preserve state/outbox and canonical history; never force-reset branches or fabricate missing selections. A code-only rollback must not strand data unreadable.
- [ ] **TASK-042: Separately approve and enforce unattended delivery.** Obtain Ledermayer/administrator approval of the exact eligibility boundary before implementing/activating data PR auto-merge and serialized publication. Require trusted producers, schema/preservation/current-base/path checks; code/config/schema/workflow/permissions/dependencies and exceptions retain human review. Test invalid producers and ineligible changes first. If enforcement or approval is absent, retain current PR/Pages approvals and report approval-gated operation, not a completed dark factory. No blanket App bypass.
- [ ] **TASK-039: Accept the complete baseline.** After TASK-042 and TEST-001 through TEST-022, record integrated source/facts/dimension/check coverage, five-kind support, retained history/tracks, provider-only refresh, all portal surfaces, recovery and verified unattended receipts. Enable broader scheduling only after prior rollout/permission gates. Obtain Ledermayer's acceptance of the evidence; do not waive required capability/policy for an event or replace the production path with a demo.

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
- [x] Project selected actionable fleet work to recurring Issues, Project items, and phase/type labels using the [board operations contract](board-operations.md).
- [x] Reconcile accepted module reports to Publishing board state after canonical data changes.
- [x] Skip unchanged board projections and retry transient GitHub GraphQL service errors.
- [x] Retry transient GitHub REST projection failures without weakening validation.
- [x] Add the Pages-completion projection workflow. Historical implementation milestone only, not proof that Done matched the deployed accepted commit; see [F-001](FINDINGS.md#f-001-deployment-receipt-matches-board-reconciliation-does-not) and follow-up TASK-009/TASK-025.
- [x] Post-filter board Issue search to exact stable module markers before fleet reconciliation.
