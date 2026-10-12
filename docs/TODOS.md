# Factory Tasks

Owner: Ledermayer.

Updated: 2026-10-12. Status: approved ten-module pilot remains incomplete. All ten facts receipts and exact-used-type provider stub snapshots for the 12 eligible provider-version pairs are accepted and receipt-imported. The exact AzureRM 3.117.1 snapshot now supplies the ten remaining major-3 context inputs; all 70 provider contexts have exact accepted inputs. PR #367 resolved the earlier immutable facts-receipt policy identity mismatch by storing refreshed context sets in policy-import receipts ordered by committed state generation; historical facts receipts remain unchanged. The reviewed provider policy covers the cohort’s evidenced provider addresses and major tracks. PRs #384–#394 addressed source traversal, response/page/byte limits, rate-limit handling, safe diagnostics, issue-search scope, normalized source bodies and pool metrics, and aligned the proposal and snapshot document ceilings. Retry 38235430362 passed collection, proposal validation, and snapshot enrichment, then failed because the publication proposal builder rejected the already content-identified enriched snapshot. This branch accepts a supplied ID only when it matches recomputation. Collection caps, exact input identities, the 10 MiB body cap, and 10,000-reference cap remain unchanged. Provider resource-intelligence proposals have been accepted and receipt-imported. Assessment, selection, and publication have not run.

## Scope And Authority

**Independent module projection plan execution checkpoint (2026-10-09):** The
read-only WP-01 source inventory is recorded in
[`refactor-independent-module-projection-1.md`](../plan/refactor-independent-module-projection-1.md).
It confirms current direct discovery-to-projection workflow coupling and that
Issue/Project mutation code is in `internal/github`. A read-only `gh api` Git
tree/blob lookup verified `factory-state` head
`2335460b7a858f1c332ce4554f0ac57061730392` contains `state/layout.json` with
profile `module-v2`, contract digest
`layout-contract-3c6413132489b687b85dd531f36382e6b17d81cb2187bf8de99af46f2792ecbd`,
and 1,109 logical-to-physical mappings. The Contents API 404 was misleading;
the exact blob read succeeded. The full per-kind inventory and owner matrix are
now recorded in the plan; TASK-001A/B source/layout acceptance is complete.
HUMAN-001 was explicitly approved in chat on 2026-10-09. WP-03 added the
module-scoped operation-receipt path, typed payload validation, and focused
identity/state fixtures. The test runner passed tests in
`internal/state/store_test.go`, `internal/state/layout_test.go`, and
`internal/identity/paths_test.go`; coordinator-owned historical proof (WP-04),
full TASK-002 acceptance, and live/activation gates remain open. No remote state
was written and no workflow was dispatched.

WP-04 coordinator checkpoint (2026-10-09): `RunOperationTransaction` now takes
fixed expected events, commits a module receipt, verifies complete receipt and
historical digest proof before replay, and returns the immutable result after
later record advancement. Changed same-ID content and missing/tampered proof
fixtures fail closed; unknown publication recovery checks visible receipt
proof. `go test ./internal/state -count=1` and
`go test -race ./internal/state -count=1` pass. Discovery producer migration,
sku-finder historical fixture, and full TASK-002/GATE-001 acceptance remain
open. No live state write or workflow dispatch occurred.

Post-WP-04 offline gates (2026-10-09): `go mod verify`,
`go test ./... -count=1`, `go vet ./...`, and
`go run ./cmd/factory --list-dimensions` passed. Repository gofmt and
`git diff --check` passed. This is local evidence only; it does not complete
GATE-001 or authorize live operations or activation.

WP-05 local sku-finder fixture checkpoint (2026-10-09): exact request/pin,
module transaction digest set and continuation were read through `gh` from
`factory-state` head `2335460b7a858f1c332ce4554f0ac57061730392`. Current source
pin evidence shows v0.3.0; the plan's v0.4.0 narrative is not substituted into
the fixture. The local receipt operation commits and replays without duplicating
the request/pin or claiming that the failed candidate transaction was
historically committed. Focused fixture passes. This does not perform live
reconciliation, state write, workflow dispatch, or satisfy GATE-008.

WP-06 discovery-work contract checkpoint (2026-10-09): Request/attempt state
does not represent source-resolution failures before request creation or bind
that work to its immutable operation receipt. Added the module-owned logical
and physical path, typed control record, allowlisted state/error validation,
owner/lease/fence consistency checks, retry validation, and module-scoped
receipt-reference validation. Focused state/identity tests pass. Producer
integration and reconciliation against the receipt remain open; no live state
was written.

WP-06 validation: 25 focused state/layout/identity tests, touched state,
identity and discovery package tests, state race tests, full `go mod verify`,
`go test ./... -count=1`, `go vet ./...`, dimension listing, repository gofmt
and `git diff --check` pass. This does not satisfy receipt-authority
integration, TASK-003 completion, GATE-001, or live/activation gates.

WP-07 neutral projection contract/path checkpoint (2026-10-09): Added the
neutral desired intent, provenance-bearing handoff with fixed 15-minute
deadline, exact-tuple acknowledgement, and separate timeout release. Registered
module-owned logical/physical paths and state record validation for each kind.
Tests cover exact intent identity, stale acknowledgement tuples, fixed deadline,
release never being success, owner/path/kind binding, immutable handoff, and
module-v2 round-trip. New package has no GitHub client. Producer/consumer
integration and workflow behavior remain open; no live state was written.

WP-08 queue barrier checkpoint: Claim eligibility verifies the matching durable
handoff and receipt proof, accepts only an exact acknowledgement, or records a
separate timeout release atomically with an otherwise eligible claim. Missing
predecessor/handoff, stale tuple and active owner remain blocked. Queue and race
tests pass. WP-14 now supplies provenance-bearing claim handoffs; downstream
worker completions, projector integration, cross-stage tests, and live gates
remain open, so this branch is not ready for full dispatch or deployment.

WP-09A/B catalog enrollment checkpoint: immutable accepted membership is now a
versioned catalog contract and discovery persists it with global catalog
acceptance before independent per-module transactions. Recovery uses stored
membership even when later catalog delta is empty; no cursor/lease owns module
completion. Crash-recovery and package fixtures plus full repository checks
pass. This is local evidence only; no state branch write or workflow run.

WP-14 dispatcher handoff checkpoint (2026-10-09): provenance-bearing claim APIs
now require the receipt-capable operation coordinator and atomically commit the
claimed request, attempt/fence, desired projection intent, stage handoff, and
immutable operation receipt. Selection is fixed before receipt creation and
revalidated against the callback snapshot; changed selection/event content fails
closed. Exact module/request/next claim CLI paths consume the supplied GitHub
workflow, commit, run and attempt provenance. Queue and CLI package tests and
queue race tests pass locally. Worker completion packets WP-15 through WP-21,
projector integration WP-23 through WP-29, cross-stage WP-33, and full gates
remain open. This is fixture-only evidence; no workflow was dispatched and no
live state was written.

WP-15 acquire-source completion checkpoint (2026-10-09): trusted sealing now
requires the exact request, attempt, dispatcher, bound run/attempt and fence,
and commits the sealed request, completed attempt, neutral `needs-review`
projection intent, acceptance-stage handoff and immutable owner-scoped
operation receipt together. The receipt result preserves the facts ID and
artifact digest reference; accepted evidence remains on the reviewed proposal/
receipt path, and no assessment request is created before evidence acceptance.
Replay uses the original seal time and operation identity, not a new run-based
operation. The reusable workflow passes reviewed workflow/commit provenance only
to the trusted seal; compute tests reject state credentials, state checkout and
state command options. `go test ./cmd/factory ./internal/worker ./internal/queue -count=1`
and `go test -race ./cmd/factory ./internal/worker ./internal/queue -count=1`
pass locally. This completes only WP-15's offline slice; WP-16 through WP-21,
projector integration (WP-23 through WP-29), cross-stage validation (WP-33),
and live verification remain open. No workflow was dispatched and no live state
was written.

WP-16 assessment completion checkpoint (2026-10-09): the trusted worker seal now
accepts exact assessment attempts and atomically commits the sealed request,
completed attempt, neutral `needs-review` projection intent, acceptance-stage
handoff and owner-scoped operation receipt. Trusted preparation/sealing re-reads
the exact accepted facts and provider snapshots, validates snapshot content,
checks requested definition IDs against the compiled registry, and rejects
artifact context or definition results outside the request vector. Receipt replay
preserves context, facts, definitions, snapshot IDs, target versions, fingerprint
and artifact digest. Fixtures verify run/attempt/fence/dispatcher ownership,
multi-context reducer preservation, replay and compute credential isolation.
`go test ./cmd/factory ./internal/worker ./internal/queue -count=1`, the matching
race suite, worker workflow contract tests, `go test ./... -count=1`, `go vet
./...`, `go mod verify`, dimension listing, gofmt and `git diff --check` pass
locally. No workflow was dispatched and no live state was written. WP-17 through
WP-21, projector integration (WP-23 through WP-29), cross-stage validation
(WP-33), and live verification remain open.

WP-17 review checkpoint (2026-10-09; contract correction 2026-10-11): the
temporary `internal/queue/collector.go` typed consumer validator did not
implement collection and is outside the desired provider-owned collector flow;
it has been removed. Ledermayer clarified the resource-intelligence purpose:
start from acquired resource/provider stubs; collect provider changelogs,
GitHub releases, resource-specific provider issues, and Terraform Registry
upgrade guidance as version-pinned evidence; propose/accept those updates into
provider stubs, then wake assessment for exact accepted snapshot IDs and AVM
version generations. The existing provider catalog collector is metadata
collection, not this pipeline. A bounded proposal contract now requires the
reviewed provider policy and facts/snapshot reference IDs, binds GitHub
sources to the configured provider repository and release tags, binds Registry
sources to the exact provider, validates the full acquisition-facts identity,
and preserves multiple distinct changelog files while collapsing exact
duplicates. At that checkpoint the constructor did not prove the referenced
facts, snapshots, or stub digests were present in the accepted data tree. The
2026-10-10 accepted-input verification checkpoint below supersedes that gap.
Focused provider tests pass. WP-17 still needs network handlers,
durable proposal receipts, human acceptance into
provider stubs, accepted-snapshot wake-up and exact consumer integration. No
collection, fabricated accepted facts, or assessment enqueue was performed.
See [F-023](FINDINGS.md#f-023-resource-intelligence-collector-contract-needs-source-specific-schemas).

WP-17 issue-version contract note (2026-10-09): the POC issue harvester matches
resource names and stores issue metadata, but does not prove affected/fixed
provider versions. The new collector must retain the issue and explicit version
references as separate facts; collection-time provider version is not evidence
that an issue affects that version. The typed proposal now requires an explicit
version basis and only labels issue evidence with the exact provider snapshot
where it was observed. See [F-023](FINDINGS.md#f-023-resource-intelligence-collector-contract-needs-source-specific-schemas).

WP-17 proposal-boundary verification (2026-10-09): after tightening the
proposal to validate complete acquisition facts, exact provider policy/snapshot
references, provider-repository/release-tag URLs, Registry provider URLs, and
multiple distinct changelog files, `go test ./internal/provider -count=1`,
`go test -race ./internal/provider`, and `git diff --check` pass. The whole Go
suite, schema checks, workflow-contract checks, queue/state/provider/projection
race suite, discovery race suite, `go vet ./...`, `go mod verify`, dimension
listing, and repository-wide gofmt check also pass on this implementation
state. This remains proposal-contract evidence only; no source fetch, acceptance,
state write or workflow dispatch occurred.

WP-17 multi-version proposal correction (2026-10-10): review found proposal
validation sorted and keyed provider snapshots only by address, rejecting the
same provider's second accepted major version. Snapshot ordering and uniqueness
now use the tuple `(provider_address, provider_version, snapshot_id)`, and a
regression fixture proves versions 4.2.0 and 5.0.1 remain together in one
canonical proposal. Focused and full provider tests, `go test ./... -count=1`,
provider/queue/state/projection race tests, `go mod verify`, and `git diff
--check` pass. This fixes the multi-version proposal contract only; WP-17 still
needs provider-owned output, source handlers, accepted receipts, and assessment
wake-up. No network collection or live state operation occurred.

WP-17 accepted-input verification checkpoint (2026-10-10):
`provider.LoadAcceptedResourceIntelligenceInputs` now reads one validated
accepted data tree, resolves the exact module facts/source revision and exact
indexed provider snapshots, verifies content digests and provider stub
membership, and returns sealed inputs. `NewResourceIntelligenceProposal` now
requires that verified input value and rejects changed inputs or resources not
present in both accepted facts and provider stubs. Focused provider tests pass.
This closes only the trusted input-reference slice. Source-specific network
handlers, durable proposal receipts, human acceptance into provider stubs, and
accepted-snapshot assessment wake-up remain open; no source fetch, acceptance,
state write, assessment enqueue, or workflow dispatch occurred.

WP-17 provider-stub output checkpoint (2026-10-10): `assessment.ProviderEntry`
now stores typed source evidence with content digests and explicit version
basis; the strict factory-data-v3 schema covers those fields. The provider
enrichment builder verifies sealed accepted facts, provider stubs and reviewed
policy, then returns a new immutable provider snapshot. The portal renders
exact versioned source links. Provider, assessment, and site focused tests
pass. Source HTTP collectors, CLI/workflow wiring, durable accepted-provider
generation receipt, and wake-up remain unimplemented. No live source reads,
accepted writes, or deployments occurred.

WP-17 source-handler checkpoint (2026-10-10): the fixture-testable resource
collector now reads exact-tag GitHub releases, all root `CHANGELOG*.md` files
at that tag, exact-resource GitHub issue results, and versioned Terraform
Registry major upgrade guides. It keeps issue observation version separate
from affected-version claims and retains split changelog files. The end-to-end
fixture runs all four sources from sealed accepted facts/stubs and verifies the
proposal. Provider package and race tests pass. No live request ran; command and
workflow integration now exposes a manual proposal-only command/workflow and
routes its artifact through the existing reviewed provider export. The command
requires exact accepted facts and one indexed provider snapshot; an empty result
does not emit a proposal. Provider acceptance provenance/receipt and exact
generation wake-up remain open. Workflow was not dispatched and no live request
ran.

WP-17 accepted-provider import checkpoint (2026-10-10): the trusted import path
now verifies one exact provider snapshot in the accepted index and derives its
affected module/release/provider-major contexts only from operation-proven facts
acceptance receipts. The state transaction records the exact accepted commit,
trusted workflow provenance, immutable write-set proof, and release-scoped
generation advances. A dispatch-only `manual-import-provider-snapshot`
workflow and contract tests now connect the exact accepted `main` tree to that
state import. Focused provider/state/CLI/workflow tests pass. This writes no
assessment request: exact accepted snapshot vectors and durable generations
still need a site-cycle planner that queues eligible work and retains deferred
contexts. No workflow was dispatched, no state was written, and no assessment
or publication was performed.

WP-17 policy identity correction (2026-10-10): required context sets identify
the reviewed provider-track policy with `provider-policy-*` IDs. Assessment
input imports now validate that identity type, and selection compares an
imported policy against the exact policy ID in the accepted facts receipt's
required-context set instead of looking for it among compiled dimension IDs.
Focused state tests pass. The site-cycle wake producer is still open; this
correction does not create queued work or establish accepted policy provenance.

WP-17 durable wake slice (2026-10-10): `queue.EnqueueAssessmentWake` now
persists exact eligible module requests with an immutable module-owned
operation receipt, retains deferred contexts in the receipt result, and replays
the original result without rewriting request records that advanced afterward.
Focused queue replay tests pass. It is not yet called by site preparation and
does not derive its input vector from accepted state/data; WP-17 remains open.

WP-17 accepted-input site-cycle checkpoint (2026-10-10): site preparation now
derives assessment requests only from active catalog modules, operation-proven
facts acceptance receipts, the exact release/source pin, the receipt's complete
required-context set, exact accepted facts, and receipt-proven provider snapshot
and policy imports. Eligible requests are persisted with the durable wake
operation; missing or mismatched accepted inputs remain deferred, and no
dispatch occurs. The provider snapshot workflow imports the reviewed policy
identity for the same exact targets. Empty-receipt workflow fixtures prove no
request is invented, and state tests now prove exact provider/policy import
receipts can be read together for one release-scoped context. Focused package,
race, and workflow-contract tests pass. WP-17 still lacks a positive end-to-end
site-planner fixture and has not been exercised on a live accepted tree; WP-19
selection reduction and its publication handoff remain open. No workflow was
dispatched or state branch written.

WP-17 policy context refresh finding (2026-10-12): The user approved limiting
provider policy coverage to providers and major tracks used by the exact ten
accepted pilot facts sets. The reviewed configuration already includes those
external provider addresses and tracks. The accepted facts receipts, however,
embed an earlier policy identity with AzureRM 4/5 eligible and other used
providers unresolved. Exact state readback shows the current policy import ID
differs from the immutable facts-receipt policy ID, and
`planAcceptedSiteAssessments` defers contexts on that mismatch. Provider
snapshot imports for AzureRM 3.117.1, 4.81.0, and 5.9.0 succeeded; only 4/5
matched eligible contexts (ten each), and no assessment was dispatched. A
focused fix is being prepared to persist policy-derived context sets inside
the exact policy input-import receipt, linked to operation-proven facts
acceptance receipts, without rewriting accepted evidence. Assessment and later
provider snapshots remain gated on that fix and exact receipt-backed inputs.

WP-18 evidence acceptance review (2026-10-09): receipt replay now returns the
original acceptance and handoff when a trusted retry has a new run/commit and
acceptance timestamp. Run provenance remains stored on the original handoff but
does not mint logical identity. Review added validation of the retry's full
workflow/commit/run/attempt provenance before the replay path returns. Facts
acceptance writes no assessment inputs; assessment acceptance writes no
selection. Focused state/projection tests and state/queue race tests pass. No
workflow or live state operation occurred.

WP-19 review checkpoint (2026-10-09): Ledermayer clarified that a lifecycle
context is a released semantic version of an AVM module. Seed current releases
only without backfill; nightly discovery queues newer versions. A module has a
single active assessment slot across versions, so newer versions wait in
Backlog. Keep assessment and selection receipts per version and append-only;
module projection must retain both latest desired and older active state. This
resolves the required-context meaning. WP-19 still needs intent aggregation and
selection producer provenance. The incomplete one-context intent change was
removed. Recorded as [F-024](FINDINGS.md#f-024-selection-must-reduce-version-scoped-module-state).
Follow-up confirmation (2026-10-09): Ledermayer approved holding the single
module-version slot through exact verified publication, and approved creating a
new desired assessment generation for the same AVM release when accepted
provider intelligence changes, preserving earlier analysis/selection history.
Code review confirmed the existing one-active-claim check covers only running
stage jobs, not review through publication. Added WP-19A for the durable slot
and version reducer; WP-19 depends on it, and WP-21 releases only from verified
deployment membership. These are implementation packets under the approved
semantics, not additional policy decisions.

WP-19A generation advancement checkpoint (2026-10-09): a held module slot now
accepts a higher desired generation only for its same AVM release and updates
that generation atomically with the claim. Older generations and newer AVM
releases remain nondispatchable while the slot is active. Focused queue tests
pass; this supports accepted provider-input re-assessment without releasing the
module slot.

WP-19A FIFO correction (2026-10-09): when no slot exists yet, claim eligibility
now blocks any newer AVM release while an older release request for that module
remains queued, even when the older request is temporarily retry-delayed. This
prevents request timestamp or ID ordering from starting a later release first.
The released-slot check also rejects older releases after publication while
allowing a later release or a newer generation of the published release.
The focused release-order test, `go test -race ./internal/queue -count=1`, and
state/projection/publication/index tests pass. This remains local queue evidence;
selection handoffs and verified publication slot release are still incomplete.

WP-19 version-preservation checkpoint (2026-10-09): the assessment wake planner
now requires and propagates the exact AVM release. Selection receipt schema v3
preserves `avm_release_version`; published module/track indexes retain receipt
IDs that resolve to that version. Selection validates its release against the
active module slot and exact source pin in `PinHistory`. Fixtures prove distinct
releases keep distinct immutable selection receipts. Schema, queue, state,
index, and publication tests pass. WP-19 is still incomplete: required-context
reduction, exact predecessor acknowledgement, and a receipt-backed
selection-to-publication handoff are not implemented. No workflow or live state
operation occurred.

WP-19 acceptance-barrier checkpoint (2026-10-10): `SelectAccepted` now
requires the exact assessment acceptance handoff ID, validates its operation
receipt and write-set proof, checks the accepted facts/generation/fingerprint,
and requires either that exact projection acknowledgement or its durable
timeout release. Fixtures reject missing/wrong handoffs and acknowledgements,
and accept an exact timeout release. `go test ./internal/state -run
'Selection|Deployment' -count=1` passes. WP-19 remains incomplete: the durable
complete required-context set, module-wide reducer, selection producer receipt,
and selection-to-publication handoff are still open. No workflow or live state
operation occurred.

Assessment context-vector checkpoint (2026-10-10): `PlanAssessmentWake` now
returns a defensive copy of every caller-supplied reviewed context alongside
eligible requests and deferred reasons, so downstream state preparation can
retain contexts that did not produce a request. The focused queue test verifies
that applicable/unresolved and not-applicable contexts remain present and that
the returned vector cannot be changed through caller aliases. This does not
prove the input list is complete. The following WP-18 implementation checkpoint
supersedes this planner-only limitation by deriving and persisting the set at
trusted facts acceptance; selection reduction remains open under WP-19.

WP-18 contract review (2026-10-10): `provider.DeriveRequiredAssessmentContexts`
now defines independent per-provider-major contexts using validated accepted
facts and the reviewed `track_majors` policy, with explicit unresolved entries
for missing policy, empty majors, or incomplete provider-requirement coverage.
The derived set has deterministic module/source/release/facts/policy identity;
it does not form Cartesian provider-version vectors or use runnable requests as
proof of completeness. The trusted acceptance CLI derives it from the exact
accepted tree and persists it in the immutable facts-acceptance receipt. State,
provider and CLI tests plus workflow-contract tests pass. WP-18's local
implementation is complete; WP-19 still must consume and reduce the set. Provider data
export currently stages a review branch but has no durable acceptance receipt
or exact shared-snapshot generation wake, which remains an independent WP-17
completion item. No external action or live operation occurred.

WP-17 version-scoped invalidation checkpoint (2026-10-10): accepted-input
targets and module state now include the exact AVM release. Generation
high-water scans, operation identity/replay, provider/policy invalidation and
selection's accepted-input check are keyed by `(module, AVM release, provider
context)`. The full Go suite and `go test -race ./internal/state ./internal/queue
./internal/projection -count=1` pass, including a fixture proving a provider
import for one release leaves another release untouched. This closes local generation isolation only; no provider acceptance
receipt, exact accepted-tree import producer, or wake workflow was added.

WP-18 acceptance persistence checkpoint (2026-10-10): the trusted evidence
acceptance command loads facts by exact identity from the verified merged data
tree, derives the context set using the reviewed policy, and stores it in the
receipt with the exact AVM release. Receipt validation rejects a set rebound to
another release or facts identity; operation replay preserves its immutable set.
Workflow-contract, focused state/provider/CLI, full-suite and relevant race
tests pass. Selection reduction remains WP-19 work. No workflow dispatch,
state write, or live acceptance occurred.

WP-19 context-selection checkpoint (2026-10-10): selection now resolves the
facts acceptance receipt through its operation receipt and historical write-set
proof, requires the selected context to be applicable and eligible in that
receipt's exact module/source/release/facts set, and rejects unresolved or
unlisted contexts. A valid selection atomically writes its immutable selection
and export, a module projection intent with selected/required/unresolved counts,
and a provenance-bearing publication handoff under an operation receipt. The
module slot generation must cover the selected request. Focused state, queue,
projection and deployment tests pass. This does not complete WP-19: the module
reducer still lacks other durable request/active-stage and publication
acknowledgement inputs, and selection has no integrated site-publication caller.
No workflow or live state operation occurred.

WP-19 coverage-history verification checkpoint (2026-10-10): review found the
selection coverage reducer treated a malformed prior pointer/receipt as if the
context simply had no selection. Coverage now fails closed on invalid durable
history, and non-null selection receipts must match their original applied
event digest before they count. A fixture proves an otherwise well-formed but
unapplied selection cannot be accepted as history. Focused state/queue/projection
tests pass. This hardens local selection reduction; request/activity reduction,
site-cycle selection and Pages acknowledgement remain open. No workflow or
state write occurred.

WP-19 request-aware projection checkpoint (2026-10-10): selection projection
now reduces the full accepted context set with current release-scoped requests.
It preserves queued, active, review, blocked, and failed states when another
required provider context has not reached selection, and retains explicit
partial-coverage counts. A focused fixture proves selecting one context cannot
hide a second queued context behind `publishing`; state, queue, and projection
tests pass. This is not the complete module/version reducer: site-cycle
selection, producer/handoff integration, cross-release projection, and Pages
acknowledgement remain open. No workflow or live state write occurred.

WP-19 site-cycle selection checkpoint (2026-10-10): state now enumerates only
current accepted assessment candidates with exact evidence operation/write-set
proof and a valid predecessor acknowledgement or durable barrier release. Site
preparation invokes `SelectAccepted` with its exact workflow provenance; the
state operation rechecks request, generation, slot, imports, source pin, and
handoff at commit. Selection replay from a later site cycle reuses the current
receipt instead of creating another sequence. Focused and race tests pass.
This writes a state selection outbox; it does not put the receipt in
`data/modules/`. A trusted selection-export proposal/acceptance path is still
needed before combined Pages membership can include it. Cross-release
projection and deployment acknowledgement integration remain open. No
workflow was dispatched or live state written.

**WP-19 release-scoped export contract checkpoint (2026-10-10):** Review found
that selection proposals still addressed `module/context`, and their pointer
and sequence checks could not distinguish two AVM releases sharing one provider
context. Proposal subjects now include `module/release/context`; validation
matches the receipt, current pointer, history sequence, and track target vector
to that exact tuple. The generated index schema carries the release on track
target vectors, and a fixture accepts sequence 1 independently for two releases
while preserving both current selections. Review also found that rebased
selection proposals skipped module/track extension checks; validation now
requires exact append-only preservation on every base, with a fixture rejecting
dropped prior track history. Focused index, publish, state, and CLI tests pass
locally. The state outbox still has no trusted proposal builder
and reviewed export workflow, so WP-19/WP-20 remain incomplete. No workflow or
live operation occurred.

**WP-19/WP-20 selection export workflow checkpoint (2026-10-11):** A trusted
CLI now reads exactly one current pending selection outbox and builds a
proposal from the exact accepted data tree. It verifies accepted generated
index digests, emits the immutable receipt plus changed module, track, and
dataset indexes, and uses the constrained proposal applier to prove the full
candidate rebuild. A manual-only workflow validates the exact `main` base,
reads `factory-state` through a separate verified checkout, stages only
proposal-declared data, reapplies the proposal, and pushes a review branch.
During a later gated site cycle, an accepted canonical receipt is compared to
its durable outbox before `ImportSelectionReceipt` acknowledges the merge.
Focused publish/CLI/workflow tests and shell parsing pass. No workflow was
dispatched and no state or canonical data was written. Cross-release lifecycle
reduction, live accepted-tree planning evidence, and full delivery gates remain
open.

WP-19/WP-21 release-scoped publication checkpoint (2026-10-10): review found
selection pointers and dataset/deployment membership were keyed only by module
and provider context, which would collapse the same track selected for a later
AVM release. Selection pointers and sequence high-water marks now use the exact
module/release/context key. Current selections, dataset membership, publication
manifest validation, and Done verification preserve and require that release.
The dataset manifest is version 3, and the checked-in generated manifest was
rebuilt. Regression fixtures prove two releases of one module/provider context
remain distinct and deployment proof cannot cross release boundaries. Focused
index, publish, state, and CLI tests pass. WP-19 reduction/site invocation,
WP-20 combined publication, and WP-21 Pages acknowledgement/fan-out remain
incomplete; no workflow or live operation occurred.

WP-21 module acknowledgement checkpoint (2026-10-10): state now verifies a
successful current Pages receipt against the exact module/release membership,
selection receipts, current assessment requests, and accepted inputs. A
module-owned operation transaction writes an immutable acknowledgement,
updates the lifecycle slot with the receipt and membership digest, and emits a
projection intent that reports Done only when required contexts are all
selected; otherwise it reports partial. `VerifyDoneSelection` now requires
that acknowledgement and released slot. Fixtures cover exact release scoping
and lost-ack replay. Focused state tests pass locally. The site workflow does
not call this fan-out yet, and combined publication remains incomplete; no
workflow or live operation occurred.

WP-35 review correction (2026-10-11): the independent read-only review of
merge commit `ec2b93bd` found verified partial Pages membership released the
module slot before unresolved required contexts finished. Fan-out now retains
the slot for unresolved required tracks and when exact deployment membership
omits a selected required context. Regression fixtures prove an explicitly
unresolved context retains the slot, ineligible contexts do not inflate required
coverage, and a newer AVM release remains queued across partial deployments
until exact membership includes both required selections. Focused state tests
pass locally; independent post-fix review and repository gates remain pending.
No workflow or live state operation occurred.

WP-20 combined-root checkpoint (2026-10-10): the indexed site publication
manifest now carries separate deterministic digests for `data/modules/` and
`data/providers/`, in addition to the exact accepted commit, full data-tree
digest, dataset identity, and release-scoped membership. Durable deployment
generations and callbacks retain both root digests; authorization compares the
artifact to the prepared generation, and replay rejects a same-run callback
whose provider-root digest or Pages deployment key differs. Missing provider
data uses the canonical empty-tree digest for partial first publication. The
manual Pages workflow now prepares the state generation, verifies the same
site/data artifact after transfer, authorizes it behind `data-publisher`,
deploys once behind `github-pages`, records the accepted commit SHA as the
Pages deployment key (the pinned action exposes no `deployment_id` output),
and fans out exact module/version acknowledgements. A module whose published
membership is stale is explicitly deferred while independent module fan-out
continues. Focused publish/state/CLI/workflow-contract tests pass locally. The
workflow still lacks the WP-17 exact-input assessment wake and WP-19 site-cycle
selection caller, so it does not yet run assessment planning before publication.
No workflow was dispatched and no live state or Pages operation occurred.

WP-19/WP-20 site-cycle caller correction (2026-10-11): the current local
`prepareIndexedSitePublication` path now imports accepted selection exports,
plans requests only from exact accepted module/provider inputs at the supplied
commit, and applies receipt-backed accepted results into durable selection
outboxes before preparing the combined publication generation. The earlier
workflow-gap statement above is superseded for this local code state. Full
accepted-provider collector provenance, complete WP-19 reducer/cross-stage
coverage, required gates, and live operations remain open. No workflow ran.

WP-22 fan-out fixture checkpoint (2026-10-10): the Pages batch reducer now has
a fixture proving multiple provider contexts for one module/release produce
one acknowledgement target while separate AVM releases remain distinct and
sorted. Existing queue/state fixtures cover active slots, unknown run
recovery, stale deployment callbacks and exact release acknowledgement. The
focused CLI/state/queue tests pass; WP-22 cross-stage integration coverage and
the rest of the implementation remain open. No workflow was dispatched and no
live state or deployment occurred.

WP-17 version-scoped invalidation checkpoint (2026-10-10): accepted-input
targets and module state now include the exact AVM release. Generation
high-water scans, operation identity/replay, provider/policy invalidation and
selection's accepted-input check are keyed by `(module, AVM release, provider
context)`. `go test ./internal/state ./internal/queue -count=1` passes, including
a fixture proving a provider import for one release leaves another release
untouched. This closes local generation isolation only; no provider acceptance
receipt, exact accepted-tree import producer, or wake workflow was added.

Publication-direction checkpoint (2026-10-10): Ledermayer confirmed that the
initial catalog remains current-release-only, module/version data and provider
data are accepted independently under `data/modules/` and `data/providers/`, and
the then-current plan said assessment runs with one site publication from the
exact shared accepted head; the later site-cycle clarification below supersedes
that shorthand. The portal may show partial context coverage; module status still requires a
durable complete track set. The first catalog-only site publish was manually
dispatched by Ledermayer: `manual-site-publish` run
[38078412163](https://github.com/softwareone-public/avm-intelligence/actions/runs/38078412163)
completed successfully for `1639c9c50cf1391124c91922ae72abf5bddd0fff`, and
Pages deployment `6985523656` succeeded. This verifies the partial catalog site
path only; no assessment, source collection, acceptance, or new publication was
performed. Work is now isolated in `feature/versioned-partial-lifecycle-pr`; the
original uncommitted `main` worktree remains unchanged. This branch starts from
the merged catalog-site commit; no PR exists yet.

Nightly discovery clarification (2026-10-09): the initial baseline is each
active AVM module's currently released semantic version, with no older-release
backfill. Subsequent nightly scans must durably enroll every new stable release
since its per-module baseline, including multiple releases between polls.
Discovery queues those release contexts and starts no worker; the module slot
serializes them through verified publication. Updated WP-09C/WP-10 through
WP-12 and tests in the approved plan. No schedule has been activated.

Post-review offline gates (2026-10-09): after correcting the discovery test's
fixed noon timestamp so its active-lease assertion stays within the test lease,
`go mod verify`, `go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`, `go test ./cmd/factory -run Workflow
-count=1`, the state/queue/projection race suite, the focused discovery race
test, gofmt, and `git diff --check` pass. The first full-suite run failed only
that stale-time assertion; the rerun passed. WP-17/WP-19 contract blockers mean
the approved lifecycle is not workflow-testable, so no focused branch or PR was
created. No workflow was dispatched and no live operation occurred.

WP-09C/WP-19A implementation checkpoint (2026-10-09): stable release polling
now resolves every published stable tag after the durable baseline, retains
intermediate versions, and pins each to an immutable source commit. Discovery
now connects that resolver to each module's durable `PinHistory`: the first
observation establishes a no-backfill current-release baseline, and later
observations atomically add every newer stable pin, one exact acquisition
request per AVM version, an operation receipt and exact handoffs. The module
queue persists an active lifecycle slot in module-v2 state atomically with
claim. Later stages require the held release; accepted same-release input
changes can advance the slot generation atomically with the next claim, while
older generations and newer releases remain blocked. The slot has an exact
deployment proof shape for WP-21 but no release writer yet.
Focused discovery/source/queue tests and the full Go suite pass at this
checkpoint. No schedule activation or live state/source access occurred.
F-025's original offline persistence gap is closed; end-to-end workflow
testability remains blocked by WP-17 collectors/accepted-snapshot wake-up,
assessment release propagation, WP-19 selection handoffs, WP-20 publication,
and WP-21 verified membership release. Queued-release FIFO ordering has since
been locally enforced. No branch
or PR was created.

Queue review correction (2026-10-09): AVM release version now contributes to
acquisition and assessment request fingerprints. Consumer handoff lookup also
matches the exact wrapped input fingerprint, so two release contexts that both
use generation 1 cannot share a handoff or acknowledgement. Queue focused tests
pass after updating fixtures to use the producer contract's exact fingerprint.

Nightly release-scope correction (2026-10-09): the scheduled workflow previously
passed `--discovery-new-modules-only`, so known catalog IDs were not checked for
new AVM releases. It now uses the default all-active selection; a discovery
fixture proves existing modules remain in scope when a catalog ID is added.
Immutable catalog selections now distinguish `factory-all-active` from manual
exact scope. Workflow contract and focused discovery tests pass. The durable
per-module release pin/request transaction remains the recovery authority; an
unchanged-catalog fixture now proves all active modules enqueue the new stable
release separately from their baseline. A crash fixture now commits a complete
multi-release batch for one module, interrupts before the next module, and
recovers all active modules on an empty catalog delta. It relies on upstream
tags remaining discoverable; an independent durable release-enrollment record
before module commit remains open WP-09B work. See
[F-025](FINDINGS.md#f-025-discovery-membership-is-still-module-scoped). No
workflow or live state operation occurred.

- **TASK-038/043 follow-up: implement and verify the revised state transport after approval.**
	[Plan version 2](../plan/refactor-factory-state-transport-1.md) defines the
	complete Git-only replacement, ownership-scoped commits, fetch-first bounded
	decision retries and uncertain-outcome reconciliation. Execute its dependency-
	ordered chunks and named tests; no compatibility backend or arbitrary-reset CAS
	protocol is required. HUMAN-001 authorizes implementation, not HUMAN-003 through
	HUMAN-007. Plan publication and merged implementation are not activation or
	product acceptance; all such evidence remains open.
	Apply the plan's assertion-delivery table at each chunk: no successor assertion
	is an early prerequisite, and no partial fixture satisfies the final full gate.
	Current implementation checkpoint (2026-10-09): the integrated Git-only
	transport change merged through PR #309 as `9f683cb2`. Full Go tests, vet,
	module verification, dimension listing, touched-package race tests and workflow
	contract tests pass locally; remote Admin checks passed before merge. TEST-015,
	GATE-002 and GATE-003 remain open. No live operation or authentication was
	exercised. The approved transport plan is
	[version 2](../plan/refactor-factory-state-transport-1.md); its local checkpoint
	distinguishes edited code from accepted task/gate evidence. Nothing has been
	backfilled to mark TASK-008 through TASK-015 complete because their full named
	acceptance evidence is not recorded. No workflow was dispatched; HUMAN-003
	maintenance approval and HUMAN-005/006 live verification/resumption remain open.

	Transport TASK-015 documentation checkpoint (2026-10-09): the operations guide,
	lifecycle companion, Go/GitHub/workflow instructions, v2 transport plan and
	central registers now describe the common `--state-branch`/
	`--state-checkout-root` contract, temporary operation-time credentials,
	internal tree derivation, coordinator replay and stage-separated authority.
	Module-v2 serialization and explicit migration remain domain APIs; production
	CLI/workflows do not invoke them. Transport TASK-015 remains open until its
	workflow/documentation tests and repository checks pass. TEST-015 benchmarking,
	TASK-017 integrated qualification and TASK-018/GATE-003 complete-diff review
	remain pending.
	TASK-016 local checkpoint (2026-10-09): fixed-one-iteration synthetic Git
	benchmarks exposed and removed per-blob subprocess overhead from verified reads
	and candidate parent loading. The cold-read fixture now uses a fresh empty
	checkout per sample. Full Go suite, vet, module verification, dimension listing,
	workflow tests, state-caller race tests and focused 1-/10-sample cold-read runs
	pass locally. These checks do not satisfy TEST-015: warmup/100-iteration
	publication and retry scenarios, independent cold samples at both sizes,
	nearest-rank p50/p95 across required scenarios, Git transfer/object/fetch and
	decision accounting, and complete environment/results capture remain open.
	Local bare-remote stdout bytes are not hosted wire-transfer bytes. TASK-016
	remains open; no public snapshot or live operation was used.
	Bounded live discovery verification (2026-10-09): after PRs #311-#314 fixed
	Actions checkout refspec/config handling and relative state-root resolution,
	run 37890050530 succeeded on `avm-res-network-virtualnetwork`, force off.
	State fetch/read and catalog generation 3/observation commit succeeded; the
	selected source was unchanged and already acknowledged, so module work and
	Issue/Project projection were correctly skipped. Final `factory-state` head:
	`438dd6198f3ce889fbaea85896bc8cbf54ce780f`. Evidence and limitations are in
	[FINDINGS.md](FINDINGS.md). This does not satisfy TEST-015, GATE-002/003, nor
	prove changed-source acquisition, changed-module projection, worker dispatch,
	acceptance, or activation; those gates remain open.
	UTL exact-continuation resume (run 37894014485) also failed at
	avm-utl-sku-finder. It committed only a lease-owner/generation checkpoint;
	cursor remains 5/6 and pending_delivery=false. Source inspection found the
	discovery transaction generates an operation ID without assigning it to any
	state event, contrary to DecisionCoordinator's stable-operation-ID contract.
	See [FINDINGS.md](FINDINGS.md). No further retry until a reviewed fix preserves
	per-record event identities and satisfies that contract; do not start a fresh
	UTL cycle or force.

- **TASK-002 follow-up: admin validation cutover.** After reviewed additive admin
	workflows land, verify live `Admin CI / Test` and `Admin Proposal Validation`
	checks, then separately approve/update required-check policies. Keep the
	factory-prefixed validation workflows until the replacement checks and effective
	policies are proven. Local tests do not certify activation; no policy change or
	legacy workflow deletion is included in the additive implementation.
	Update 2026-10-08: additive PR #302 merged; required-check policy is verified
	as `Admin CI / Test`. Legacy removal is now authorized in a separate test PR;
	verify its live admin CI/proposal checks before declaring retirement complete.

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

	**Independent module projection follow-up (2026-10-09):** Ledermayer confirmed AVM release versions are lifecycle contexts while provider tracks retain separate assessment/selection history. The newer approved refactor's WP-19 must persist the complete required-context set, consume the exact acceptance projection acknowledgement, and produce selection provenance/handoff before projecting module-wide `Publishing`; the existing per-context selection receipt alone does not establish that status. No live selection occurred.
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

**Initial pipeline plan (2026-10-07):** Ledermayer requested planning only.
The [native pipeline delivery plan](../plan/feature-native-pipeline-delivery-1.md)
orders dedicated reusable worker transport and durable Queued/Analyzing
projection, recoverable reviewed evidence PR delivery/receipt import, independent
provider acceptance and assessment wake-up, analysis proposals, selection export
and exact-dataset publication. Trusted validation remains a separate job but is
proposed as Analyzing on the board. TASK-042 auto-merge, selective CI/binary
release, new schedules and live operations are excluded. Implementation and
promotion require approval; all current task/test completion states remain.

**Implementation authorized (2026-10-07):** Ledermayer approved autonomous
implementation and scoped PR creation/merges, preserving effective review and
environment gates. STEP-001 has a locally verified aggregate run-binding repair
with unrelated/same-module races and lost-ack fixtures. Failed live preparation
root cause, cold loading and heartbeat/recovery verification remain open. No
new live operation or autonomous promotion is established by local tests.

**Reusable worker staging (2026-10-07):** STEP-002 extracts the existing jobs
into `factory-worker` (`workflow_call` only) behind a manual-only
`manual-worker-validation` entry point. Existing dispatch and `manual-worker`
stay unchanged: replacing their active wrapper would otherwise promote the
new execution path into scheduled runs without manual-first proof. Exact
validation-producer paths and reusable job names are recognized by receipt and
recovery filters. Local fixtures verify input propagation, permission ceilings,
credential isolation and concurrency ownership. Before promotion, obtain
bounded manual-run approval and record environment gates, actual job names,
run/attempt/artifact binding and review-branch behavior. Cold loading, heartbeat,
full lifecycle projection and remaining pipeline phases stay open.

**Merged reusable staging and cold-load follow-up (2026-10-07):** PR #284
merged at `9f085cd` after both checks passed. Trusted reusable prepare/seal
jobs now locally seed immutable blobs through the existing verified checkout
API; paired paths fail before backend creation, and compute receives no state
checkout or credentials. Full Go tests/vet and focused option/workflow fixtures
pass. The active scheduled path is still unchanged; bounded manual proof and
transport promotion are not complete. Cold-loading performance must be measured
in an approved run, not inferred from fixture wiring.

**Bounded validation authorized (2026-10-07):** Ledermayer approved one exact
live worker validation. The previously failed claim has expired and a newer
scheduled worker is active; neither is reused. A manual-only exact-target
`manual-dispatch-validation` claims fresh eligible work and calls the reusable
worker in the same run, with no scheduled transport change. Receipt/recovery
recognize its exact producer; unbound recovery verifies the durable dispatcher's
run and attempt, rejecting later reruns. Local handoff/provenance fixtures pass.
Publish through checked PR before dispatch; record actual target/run/fence and
results separately. This does not authorize broad dispatch or gate closure.

**Failed live gate (2026-10-07):** PR #286 merged; exact-request validation
run `37615138597` claimed only the resource-group target and loaded state with
zero blob downloads, then reusable preparation failed to obtain environment App
credentials before compute. Existing secret names and successful caller mint
are verified, not their reusable availability. Diagnose the environment-secret
boundary without broad secret inheritance, permission changes or another live
run; preserve unbound attempt `attempt-613c8050...`, dispatcher `37615138597-1`,
fence 1 and its durable lease. Full identities/outcomes are in FINDINGS.
STEP-001/002 and transport promotion remain open; no facts, analysis, acceptance
or deployment was produced by this validation.

Credential follow-up narrows reusable App secrets to mint-step env and tests
both trusted jobs plus compute isolation. Local gates pass; runtime secret
resolution remains unproven. Continue local pipeline work while keeping live
retry/recovery and scheduled promotion stopped at their respective gates; do
not treat the failed live gate as a requirement for new implementation approval.

**STEP-003 adapter checkpoint (2026-10-07):** request-to-context reduction and
active lifecycle projection mappings are fixture-verified; explicit acquisition
progress no longer becomes synthetic Backlog. Atomic transition delivery remains
unwired. Use a separate module-owned lifecycle intent/generation contract rather
than reuse discovery intents: unchanged rediscovery/protected lowercase states
and work-generation high-water coupling must remain untouched. Then wire full
required-context reduction, claim/bind/seal/acceptance/recovery and bounded flush.
No live board change or STEP-003 completion is claimed.

Reader-first lifecycle contract is locally verified and production-disabled.
Before enabling its event helper, deploy compatible readers and drain/fence
retained old jobs; then integrate full required-context reduction and exact-input
delivery checks. Current helper deliberately declines mixed-context modules.
This is not STEP-003 completion or autonomous emission approval.

Full-context adapter follow-up now joins explicitly resolved policy contexts to
durable requests, retaining older active/newer desired generations, rejecting
missing contexts and conflicting duplicates, and using latest-source acquisition
only for proven no-track fallback. Wire reviewed policy/selection/deployment
inputs before using it to emit complete module lifecycle intents.

STEP-001 preparation retry now supports the exact already-bound Running
run/attempt after input revalidation; foreign identities remain rejected.
Focused fixture/full Go/vet/race checks pass. Sealed completions remain protected;
this repair does not rerun a worker or complete live recovery/heartbeat gates.

STEP-004 reader follow-up consumes current acquisition lifecycle records through
the existing projection builder and independently fences derived delivery at
save/claim/mutation/acknowledgement, including preflight before budget reads.
Production record emission and manual flush wiring remain disabled/unimplemented;
test-only records do not establish live Queued/Analyzing projection. Full-context
selection/deployment inputs and paced mid-wait authority races remain required.

Explicit opt-in atomic acquisition claim/bind APIs are fixture-verified, with
default production disabled. No CLI/workflow caller enables them. Wire only
after reader compatibility/drain gates; then validate exact-request lifecycle
emission and bounded projection flush. Mixed contexts and later acceptance/
selection/deployment transitions remain outside this acquisition-only slice.

Manual pending-only projection flush is implemented locally as
`--flush-projection-modules` / `manual-projection-flush`. Unique canonical IDs
and active accepted catalog membership are required; missing/converged/protected
delivery is not reopened. Due lifecycle authority is checked before token work
and inside run-evidence queuing. Fixtures prove stale evidence does not change
head/delivery bytes; full Go/vet/GitHub race gates pass and contract review found
no blockers. No live flush was launched. Reader/caller emission, full-context
transitions and bounded live proof remain open; STEP-004 is not complete.

STEP-005 trusted proposal preparation now validates the persisted seal and bound
producer/observation using single-decoded input values, with root/flag preflight
before state access. Offline fixtures/full gates pass. Wire attempt-specific
artifact acquisition, durable delivery identity and recoverable review branch/PR
creation next; partial-builder-output recovery and actual live delivery remain
open. This helper does not mark STEP-005 or evidence acceptance complete.

Manual exact-attempt acquisition export is locally wired through persisted seal
validation, attempt-specific successful job checks, immutable artifact IDs and
current-main typed proposal application. Non-force review-branch reuse and
deterministic seal/base timestamps are tested. No live export occurred. Add
idempotent PR opening and durable outbox binding, acceptance/expiry recovery and
bounded live delivery proof before marking STEP-005/006 complete.

STEP-006 review client primitive is fixture-verified, requiring trusted
exact-head payload validation and enforcing current refs/marker/repository/base
identity. It has no merge or ref-write authority. Wire the archived-tree typed
validator and reduced review token into the exact-attempt workflow, persist
delivery/PR receipts, and prove lost-ack replay before activating PR opening.

Manual PR-opening caller is implemented locally with exact raw-object typed
validation, ancestry, shared bounds and a reduced review token. Export reuses
an equal-tree current-base branch and opens/reuses its exact PR with a receipt
artifact. Offline gates pass; no workflow was run. Durable delivery state,
acceptance import automation, effective App grants/event checks and bounded live
PR proof remain before STEP-005/006 completion or autonomous promotion.

STEP-010 assessment proposal/export support is locally verified: exact accepted
facts/definitions/provider vector, immutable result/analysis/provenance staging,
content-identity enforcement and same-ID timestamp reuse preserve selections and
history. Normal data-root facts/provider reads are corrected. Full offline gates
pass. Accepted-definition bootstrap, provider receipt/wake-up, durable delivery
and integrated live assessment/selection/publication remain open; no STEP-010
completion or product acceptance is claimed.

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
  **Catalog-first release slice (2026-10-10):** the checked-in accepted dataset contains 232 catalog modules and no accepted presentation snapshot, so the existing indexed builder could not produce a site. The focused release change derives a temporary view from accepted records at the dataset timestamp, writes no data or presentation history, and discloses that the view is derived. A dispatch-only, main-only Pages workflow builds the site and deploys it through the existing `github-pages` environment. Catalog rendering and workflow contracts are locally tested; no workflow was dispatched and no deployment or live publication is claimed. The broader TASK-034 lifecycle, coalescing, history, and freshness work remains open.
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

**Approved claim/checkpoint wait conversion (2026-10-07):** run `37581694257`
showed a correctly parked deferral resume into a fatal exit because the owner
claim (and the initial resume read) could hit the same local pacing wall and
returned it raw, with the failing save also clobbering the committed head.
Every routine paced deferral now parks or proves the existing durable deferral
and exits zero with a refreshed deadline so the approved in-job wait loop
resumes; only genuinely unsavedable outcomes keep the failure, and
`deferral_saved` remains the resume gate. After review/merge, verify on the
next separately authorized paced run that a claim-boundary wall waits instead
of failing; retain continuation `discovery-cycle-fb2209f0…` untouched unless
that run resumes it under current head/ownership checks. Do not treat local
fixtures as TASK-038 acceptance.

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
| 2. Bulk cold loading | Historical proposal superseded by the approved Git-only replacement in [plan version 2](../plan/refactor-factory-state-transport-1.md). Production uses `VerifiedGitBackend` and operation-time Git credentials; there is no REST snapshot/API commit fallback or caller-supplied tree listing. | Qualify the verified Git transport under TEST-001/002/007/009/014/015; these remain local/offline gates until evidence is recorded. |
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

WP-17/18 lifecycle-boundary correction (2026-10-11): facts acceptance now
commits its request, immutable acceptance receipt, and projection intent in one
operation receipt, without promising a module-queue `collector` successor.
Provider resource collectors remain provider-owned; only exact accepted
provider/policy inputs may feed assessment planning. Assessment acceptance
continues to emit its selection handoff. Focused state, queue, and projection
tests pass. Provider-root acceptance receipt/wake integration and the positive
accepted-tree planner fixture remain open; no collection, workflow, or live
state operation occurred.

WP-17/18 contract correction validation (2026-10-11): facts acceptance no
longer emits a collector consumer handoff; assessment acceptance still emits
the selection handoff. `go test ./... -count=1`, `go vet ./...`, `go mod
verify`, `go run ./cmd/factory --list-dimensions`, the workflow-contract subset,
YAML parsing, schema package tests, race tests for state/queue/projection/
discovery/publish/factory CLI, global gofmt check, and `git diff --check` pass.
Provider-root acceptance provenance and the positive exact accepted-tree
assessment planner fixture remain open; no workflow or live operation occurred.

WP-22 cross-stage slot fixture (2026-10-11): the deployment lifecycle test now
asserts the release slot remains active after selection and after the exact
Pages receipt is imported, then becomes released only with the module-owned
acknowledgement's exact deployment receipt ID. A callback for another AVM
release is rejected. The focused state test passes; broader duplicate-module
batch and projector integration coverage remain open. No deployment occurred.

WP-22 duplicate-context fan-out fixture (2026-10-11): the replayable global
Pages-receipt fixture now includes two selected provider contexts for one
module alongside two other modules. One module acknowledgement is recorded
before replay; replay completes the remaining module acknowledgements without
duplicating the already acknowledged module. The focused state test passes.
This closes the duplicate-context fixture slice only; broader cross-stage
projector integration remains open. No deployment occurred.

WP-23 API admission correction (2026-10-11): projection now reads due state
before GitHub's rate-limit endpoint, makes no rate-limit call for an empty
scoped batch, bounds non-empty claims by headroom above REST/GraphQL/search/
secondary reserves, and stores the admission snapshot in the durable batch.
Focused projector and workflow-contract tests pass. This is a partial WP-23
slice; effect-cost-aware admission, latest-status selection, and the central
scheduled projector are still open. No workflow or remote projection ran.

WP-23 singleton claim and neighbor continuation (2026-10-11): the scheduled
projector now claims one priority-selected due module immediately before
processing it, records its outcome, and continues after a module-specific retry
or review result. API reserve exhaustion stops further claims and leaves other
due modules unclaimed. The one-request-per-resource admission floor remains
conservative but does not estimate every effect cost; latest-status reduction
is still open. Focused factory/GitHub tests pass locally; no workflow or live
projection ran.

WP-23 neutral acknowledgement foundation (2026-10-11): projector delivery can
persist an exact neutral `projection_acknowledgement` in the same state
transaction as successful delivery, and an unchanged visible projection is
re-read when its attached neutral handoff advances. Neutral projection
generation is independent of delivery generation. Focused GitHub tests and
race tests pass. No production delivery producer attaches neutral identity,
and delivery is still discovery-shaped; the neutral-intent bridge, canonical
latest-status reducer, effect-cost admission, and batch/cursor retirement remain
open. No live acknowledgement or GitHub mutation occurred.

WP-23 active-slot status correction (2026-10-11): discovery now derives its
module-wide status from the release held by the active lifecycle slot. It uses
the exact active request before facts acceptance and the verified accepted
required-context set afterward; a newer queued release cannot replace the
active release's status/source in discovery's handoff. Queue and discovery
fixtures pass locally. This closes only the discovery-side active-release
regression; the scheduled neutral-intent bridge and all remaining WP-23 work
remain open. No workflow, remote projection, or live state write occurred.

WP-23 generation-order correction (2026-10-11): handoff work identity now
remains release/context scoped while neutral projection identity is module-wide.
Dispatch, discovery, evidence acceptance, selection, Pages acknowledgement
fan-out, and trusted worker completion preserve the module projection generation
high-water mark. Worker completion stores its exact event set in the operation
receipt and replays that set after later projection changes. Focused fixtures
cover an older active release completing beneath a newer neutral intent and
replay after the neutral intent advances again. This corrects the identified
producer/replay paths; WP-23 neutral delivery integration and the broader
workflow-testable scope remain open. See F-040. No workflow or live operation
occurred.

WP-23 managed-label cleanup admission correction (2026-10-11): Issue projection
now rejects more than ten stale factory-owned labels before issuing any label
write. Per-module REST/mutation admission reserves the full ten-label cleanup
ceiling. A fixture proves overflow is rejected with zero requests, and a cost
fixture proves the upper bound is budgeted. Focused `internal/github` tests and
`git diff --check` pass. Full gates and read-only re-review remain open. No
workflow or live GitHub operation occurred.

WP-23 scheduled neutral-intent bridge (2026-10-11): the scheduled projector
now materializes changed neutral module intents from the fresh accepted
catalog before selecting due delivery work. It binds the projector record to
the exact desired-intent tuple, leaves an identical intent unchanged across
later schedules, and fences stale neutral identities before every GitHub
mutation. `Done` additionally requires the exact active deployment receipt and
released slot. Neutral intents declaring separate evidence effects are rejected
until the projector has a consumer for those effects. Confirmed retired modules
may project only the neutral retired status. The exact neutral identity is
checked during due enumeration, claim, preflight, every remote mutation, and
acknowledgement. A fixture changes the neutral generation during a remote read
and proves the next mutation is fenced.
Focused factory/GitHub/state tests pass locally. This is not full WP-23
completion: the canonical latest-status reducer, evidence-effect delivery,
cost-aware admission and workflow cross-stage validation remain open. No
workflow or live projection occurred.

WP-23 review fixes (2026-10-11): unsupported evidence effects now leave only
their module intent pending while valid neighboring intents materialize and
become due. `SaveProjectionIntents` recomputes its merge from the transaction's
fresh callback snapshot and rejects a stale neutral identity or changed write
set. Focused factory/GitHub fixtures pass. Latest-status catch-up, evidence
reduction across producers, evidence-effect delivery, cost-aware admission and
removal of the durable batch cursor remain open. A new outage fixture confirms
the bridge reads the current module-wide neutral record after it advances; this
is not cross-producer reducer proof. No remote projection occurred.

WP-23 ceiling checkpoint (2026-10-11): the scheduled projector now accepts
10/50/100 module ceilings, defaults to 50, and considers due retry/priority
work first while claiming each module separately. Workflow contract and CLI
fixtures pass. The legacy durable projection-batch record/cursor remains in the
claim/outcome implementation, and admission still uses a per-module reserve
floor rather than per-effect cost estimates; WP-23 remains incomplete. For this
worktree checkpoint, `go mod verify`, `go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`, `go test ./cmd/factory -run Workflow
-count=1`, the required race suite, global gofmt check, and `git diff --check`
pass. No workflow ran.

WP-24 expired-owner recovery checkpoint (2026-10-11): reclaim now retires an
expired running projection batch in the same expected-head transaction that
reassigns its due intent, and stale owner outcomes are rejected by the new
fence. A focused fixture also confirms an expired owner can perform a read but
cannot begin another GitHub mutation. A delayed Issue-create fixture applies
one remote effect, recovers the lease while its response is blocked, and proves
the stale owner cannot begin the follow-up label write. A new owner reads the
Issue snapshot, adopts that create without duplicating it, repairs owned labels,
and completes the acknowledgement. Focused github package and race tests plus
`git diff --check` pass. WP-24 is locally complete for its owner and delayed
Issue-effect contract; broader projector reconciliation and workflow coverage
remain open in later packets. No live GitHub mutation occurred.

Accepted-site planner fixture (2026-10-11): a positive fixture now builds an
indexed accepted data tree with exact module facts and provider stubs, seeds
operation-proven facts acceptance and its current source pin, and imports the
exact provider snapshot plus reviewed policy through the state API. The site
planner emits the eligible provider-major request and defers the other required
major whose provider snapshot is absent. The positive exact-tree fixture gap is
closed locally; this does not establish live acceptance, dispatch, or assessment.

WP-25 Issue effect recovery checkpoint (2026-10-11): the GitHub fixture now
applies Issue-body and owned-label updates before simulating a lost response.
Retries read current Issue content and labels, preserve the human label, finish
Project reconciliation, and add the projection acknowledgement only after the
full projection succeeds. The delayed WP-24 Issue-create fixture also proves
recovery adopts an already-created Issue instead of duplicating it. Focused
GitHub tests pass. WP-25's offline effect-recovery slice is complete; WP-26
still owns Project item/field ambiguity and partial-effect recovery. No live
GitHub mutation occurred.

WP-26 Project effect recovery checkpoint (2026-10-11): fixtures apply a Project
field update and Project item create before dropping their responses. A later
run reads the actual Project item snapshot, skips the already-applied field,
and adopts the existing item without duplicating it. The field fixture also
found that an absent field was needlessly written when its desired text was
empty; cached snapshots now treat absent/empty equivalently while still sending
a clear when a non-empty current value must be removed. Focused package and race
tests pass. WP-26's local Project effect-recovery contract is complete; WP-27
evidence-comment effects and cross-stage integration remain open. No live
GitHub mutation occurred.

WP-27 evidence effect checkpoint (2026-10-11): comment creation and update
fixtures now apply the effect before losing the response; retries find the exact
owned marker and return its original comment ID without duplicate writes.
Existing delivery tests prove projection acknowledgement remains unset until
all pending comments complete, an old receipt cannot clear changed pending
content, and same-attempt changed evidence reopens delivery. Focused and race
tests pass. WP-27's local evidence/comment and supersession contract is complete;
scheduled central projection and cross-stage integration remain. No live
GitHub mutation occurred.

WP-28 scheduled projector checkpoint (2026-10-11): added the autonomous
five-minute `factory-projection` workflow with empty workflow permissions,
Contents read at job scope, the existing `data-publisher` environment, pinned
actions, a verified isolated `factory-state` checkout and repository-wide
non-cancelling concurrency. Its CLI command reads fresh due intents before
minting the reduced projection token and runs the existing budgeted batch path.
Workflow-contract, YAML and shell syntax tests pass; full `cmd/factory` and
`internal/github` tests and race tests pass, as does `git diff --check`. This
closes WP-28's local packet only. Latest-status selection, effect-cost-aware
admission and cross-stage integration remain; no workflow or live projection
was run.

WP-29 manual recovery checkpoint (2026-10-11): manual recovery now prepares
only exact selected durable intents and returns without GitHub token minting or
board calls. Explicit force reopens only acknowledged delivery; leases,
needs-review, fingerprints and retry deadlines are preserved. Manual flush
validates its exact selected modules against existing due delivery and leaves
the due intents for `factory-projection` to consume. Workflow YAML and shell
contracts, package tests and race tests pass. Neither manual workflow nor a
live projection was run. The due intents are durable authority; a separate
operator-request receipt record has not been added.

WP-30 projection credential boundary checkpoint (2026-10-11): removed the
standalone reduced-token mint command and direct module projection command.
The scheduled due-intent projector is the only production caller of
`github.MintToken`; state-only token minting remains available. A guard test
scans production factory commands and all workflow definitions for the retired
paths. Focused and race `cmd/factory` tests, workflow YAML/shell contracts and
`git diff --check` pass. No workflow or live projection was run.

WP-31 single-owner source proof (2026-10-11): removed the unused direct module
projection adapter. Production command sources now have one reduced-token mint
site and one `ProjectClaimedBatch` call site in the scheduled projector. A
guard test scans command sources and workflow definitions for retired direct
CLI paths. Full `cmd/factory` tests and race tests pass; no live mutation
occurred.

WP-32 operations documentation checkpoint (2026-10-11): updated lifecycle and
factory operations docs plus GitHub workflow instructions to describe scheduled
due-intent projection, state-only manual requests, credential ownership and the
unactivated schedule. No live workflow or projection was run.

WP-33 cross-stage fan-out checkpoint (2026-10-11): a three-module projection
fixture proves failed, completed and unstarted outcomes remain independent. A
three-module Pages receipt fixture replays global receipt import after partial
fan-out, then completes all module-owned acknowledgements without duplicates.
It exposed an owner-filtering bug in acknowledgement replay; the fix skips
valid receipts belonging to other modules and still validates the selected
module's exact receipt. Existing barrier, retry, fence, evidence and human-label
fixtures remain in force. Full state/GitHub package and race tests pass. This is
offline evidence; full TASK-016 review and live deployment remain open.

WP-34 offline gate checkpoint (2026-10-11): `gofmt` and
`test -z "$(gofmt -l cmd internal)"`, `go mod verify`, `go test ./... -count=1`,
`go vet ./...`, `go run ./cmd/factory --list-dimensions`, focused
workflow-contract tests, YAML parsing for all workflow files, shell syntax
checks through workflow fixtures, race tests for `cmd/factory`,
`internal/github`, `internal/state`, `internal/queue`, `internal/projection`,
`internal/discovery`, and `internal/publish`, and `git diff --check` pass. No
workflow was dispatched and no state or production data was written. This
closes local gate execution only; independent review, cutover evidence, PR
checks, and separately gated live work remain.

PR CI correction (2026-10-11): initial checks exposed two integration issues.
The repository proposal validator requires data changes to be data-only, so
`data/dataset.json` was removed from PR history and its pre-existing local edit
was preserved uncommitted. Admin CI then exposed that the v3 index reader
rejected the checked-in v2 generated dataset before rebuilding it. The reader
now uses only stable generation metadata and rebuilds identity/membership from
canonical accepted records; site publication passes its exact build time. The
repository's accepted dataset was not changed. Focused and full tests, vet,
module verification, dimension listing, workflow/YAML checks, race tests for
factory/GitHub/state/queue/projection/discovery/publish/index/site, and
`git diff --check` pass. PR #317 is open and ready for review; Admin CI and
Admin Proposal Validation pass on head `b798bdeb`. The independent contract
review and separately gated rollout remain pending. No workflow or live
operation was run.

2026-10-11 reconciliation update: PR #317 merged at
`ec2b93bd56d7ca13d543c94d4672e9d73e65410a`; PR #318 merged at
`e4e8206fd70441b264f7e3823c9d55911e1b0847`, which is the current `main`
base for this reconciled worktree. Earlier notes describing PR #317 as open
are historical. The current branch has uncommitted approved implementation
changes and no PR for this remaining scope.

WP-17/18 planner correction (2026-10-11): the latest source includes durable
provider-snapshot/policy input import receipts and the positive exact accepted-
tree assessment planner fixture recorded under F-029. Earlier TODO notes that
these two slices remain open are superseded. Provider-root acceptance provenance
and shared-consumer evidence deduplication still need a final contract review;
no workflow was dispatched and no live input import or assessment occurred.

WP-17 site-cycle wake integration (2026-10-11): extended the positive fixture
through `queueAcceptedSiteAssessments`. It proves the site-cycle path persists
only the exact eligible release/context request, preserves the other context as
deferred in the immutable wake receipt, and leaves the request queued without
dispatch. The exact accepted tree and provider/policy input receipts are all
fixture-backed. A separate two-consumer fixture loads distinct modules' exact
facts against the same provider/version snapshot and proves identical source
evidence converges to the same one-copy provider stub snapshot. Provider-root
acceptance provenance remains open for contract review; no live import,
collection, assessment, or workflow ran.

WP-23 generation-order correction (2026-10-11): release/context work generation
and module-wide neutral projection identity are now independent. The identified
state producers preserve the neutral generation high-water mark, and worker
completion receipts replay their original event sets after later projection
changes. Targeted package tests, the full Go suite, vet, dependency verification,
dimension listing, formatting and diff checks pass; race tests for state,
queue, discovery, projection and factory CLI pass. Neutral projector delivery
integration and later cross-stage gates remain open. No live operation occurred.

WP-24 recovery correction (2026-10-11): the prior local checkpoint is
superseded by TASK-011A review. Expiry alone no longer makes a `projecting`
module due, and newer desired content keeps the unresolved claim while fencing
its old callback. WP-24 remains incomplete until the scheduled projector can
verify prior workflow-run termination and reconcile original per-effect
identities before clearing the owner or starting a conflicting write. This is
a source-proven safety contract gap, not a live incident. Focused
`go test ./internal/github -count=1` passes; no workflow or live operation
occurred. PR readiness remains blocked on this recovery contract and the other
open implementation/review packets.

WP-23 claim-state verification (2026-10-11): after removing durable batch
records/cursors and changing expiry behavior, `gofmt`, `git diff --check`,
`go mod verify`, `go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`, `go test ./cmd/factory -run Workflow
-count=1`, and race tests for factory, github, state, queue, projection,
discovery, and publish all pass locally. No workflow or live API/state operation
occurred. This does not close TASK-011A/WP-24: recovery still needs verified
prior-run termination and per-effect reconciliation before an expired or fenced
owner can be released. Other cross-stage and independent review gates remain.

WP-19 selection coverage correction (2026-10-11): prior selection coverage now
requires the exact current assessment `RequestID`, in addition to matching
release, generation, source, facts, and input fingerprint. The focused regression
fixture and selection tests pass. This closes that local identity check only;
full lifecycle integration and the remaining projector/recovery/review packets
remain open. No workflow or live operation occurred.

WP-24 exact owner recovery checkpoint (2026-10-11): scheduled projection
claims persist numeric Actions run ID and attempt alongside their lease.
Recovery candidates are limited to expired claims with both values. The
workflow reads the exact attempt and parent run with Actions read permission;
missing, active, mismatched, or non-projector observations leave the claim
parked. A verified terminal owner is released by an expected-head transaction
that increments its fence and sets delivery pending, never projected. The same
scheduled run then performs normal Issue and Project readback/reconciliation
before mutations. Focused GitHub/state and workflow-contract fixtures pass;
the workflow was not dispatched, and no state branch or live GitHub effect was
written. Independent contract review and the full gate set remain open.

WP-24 safety correction (2026-10-11): independent contract review found that
Actions attempt termination does not prove an already accepted GitHub mutation
has finished propagating. Recovery now increments the fence, clears the dead
lease, and parks delivery as `needs-review` with an explicit unknown-effect
reason. It stays out of the due queue, and a newer desired projection cannot
clear the hold, even when its fingerprint matches an older acknowledged
value. Focused owner-recovery/workflow tests pass. A reviewed operator
resolution for these parked effects remains open; no workflow or live operation
occurred.

WP-23 active-slot materialization correction (2026-10-11): the scheduled
neutral-intent bridge now applies the shared active-module reducer before
creating a GitHub delivery. A newer queued neutral intent retains its exact
handoff identity, while the visible module status and release source identity
stay with the currently held AVM version. The focused projector regression
passes. Canonical reduction outside active slots, evidence-effect fan-out,
cross-stage workflow integration, WP-24 operator resolution, and the remaining
offline review gates are still open. No workflow or remote projection ran.

WP-23 active-slot transaction race correction (2026-10-11): independent review
found that lifecycle state could advance after neutral materialization while
the neutral identity remained unchanged. The projection save transaction now
rechecks active-slot status and source identity against the callback snapshot;
scheduled materialization skips an equal-identity delivery only while its
lifecycle authority remains valid. The integration fixture verifies the stored
delivery is rebuilt from Backlog to Analyzing and that an unknown remote-effect
hold remains parked. Focused fixtures cover the commit race and persisted
rematerialization. Full offline gates now pass after this correction, recorded
with the queued-release reducer checkpoint below. No workflow or remote
projection ran. WP-23 and the overall implementation remain incomplete.

WP-23 queued-release projection correction (2026-10-11): the shared module
projection reducer now selects the active slot's release, or when no slot is
active the oldest eligible queued source release under dispatch's release-order
rules. Discovery and the scheduled neutral bridge use that reducer, while the
neutral handoff retains the newest producer identity. A commit-time fixture
proves that adding an older release between preparation and save fences the
newer release delivery. Focused queue, discovery, GitHub, and factory tests
pass. The full gate rerun also passes: `go mod verify`, `go test ./... -count=1`,
`go vet ./...`, dimension listing, workflow-contract tests, required race tests,
global gofmt check, and `git diff --check`. Reduction for other idle lifecycle
states, evidence-effect fan-out, effect-cost admission, cross-stage workflow
review, and full WP-23 remain open. No workflow, state branch write, or remote
projection ran.

WP-17 contract review checkpoint (2026-10-11): read-only Factory Contract
Reviewer found no remaining source-level gap in provider-root acceptance
provenance or shared-consumer deduplication. The trusted import receipt binds
the exact accepted commit and snapshot; assessment wake planning consumes only
receipt-proven exact provider/policy generations; and a two-consumer fixture
proves identical provider evidence converges to one shared snapshot entry.
Together with the site-cycle wake integration fixture, this closes WP-17's
local implementation and contract slices. Focused provider and site-planner
tests, provider race tests, formatting, and diff checks pass. This is source
review and fixture evidence only: no collection, accepted merge/import, state
write, workflow dispatch, assessment, or remote protection review occurred.

WP-21 retirement-membership correction (2026-10-11): Pages fan-out now accepts
an exact current retirement-deselection receipt only for a catalog-retired
module, and does not count it as published analysis or required-context
coverage. Fixtures cover a non-required deselection alongside complete required
membership, and a required deselection that keeps the slot active. Independent
review found the all-deselected case has no analysis-backed source identity or
required-context proof. It now returns `ErrDeploymentNotCurrent`, which the
site importer defers per target while continuing other module fan-out; the
module acknowledgement is omitted and its slot remains active. This fail-closed
path is covered locally, but the terminal retirement contract for all-deselected
memberships remains unresolved. WP-21 and review readiness remain open. Focused
state tests and state race tests pass. No workflow, state branch write, or live
Pages operation occurred.

WP-22 cross-stage slot/claim fixtures (2026-10-11): module state remains active
after an authorized deployment has no Pages callback, and remains active after
partial Pages membership. The complete exact membership writes the matching
deployment receipt and membership digest into the released slot. A queue
fixture proves a later AVM release cannot be claimed while the prior slot is
active, then can be claimed after the slot carries verified release proof;
existing fixtures cover unknown worker-run retention and replayed multi-module
fan-out without duplicate acknowledgements. Focused state and queue tests pass.
These fixtures cover local state/queue boundaries, not one live or full workflow
run. The WP-21 all-deselected retirement contract, remaining WP-22 review, and
cross-stage projector integration remain open. No workflow or live operation
occurred.

WP-34 current-worktree gate rerun (2026-10-11): `go mod verify`,
`go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`,
`go test ./cmd/factory -run Workflow -count=1`, global gofmt check,
`git diff --check`, and race tests for `cmd/factory`, `internal/github`,
`internal/projection`, `internal/discovery`, `internal/publish`, `internal/state`,
and `internal/queue` pass on the reconciled worktree. This is local verification
only. WP-21's all-deselected terminal contract, WP-23 effect-cost admission,
WP-24 reviewed resolution for unknown remote effects, and final cross-stage
contract review remain open. No workflow, state branch write, or live
GitHub/Pages operation occurred.

WP-23 scheduled admission correction (2026-10-11): the scheduled client now
creates one run-local Pacer and uses a 50-minute context under the workflow's
60-minute job limit, checking the deadline before another claim. Review's
reported nil `Pacer.Seed` panic was not reproducible because `Seed` is nil-safe;
the actual risk was missing rolling-window pacing. Singleton admission now
estimates per-module REST, GraphQL, Search, secondary-request, mutation, and
runtime costs, and skips an unaffordable high-cost candidate to consider a
cheaper due neighbor. Actual API calls remain subject to Pacer enforcement,
since remote pagination and label cleanup can add work after claim. Focused
factory and GitHub tests pass, including paced budget bootstrap, admission
ordering, safe pre-send cancellation retry, and uncertain in-flight cancellation
review. WP-23 remains incomplete pending full workload-cost review and final
integration checks. No workflow or live GitHub operation occurred.

WP-23 verification after scheduled admission changes (2026-10-11):
`go mod verify`, `go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`, factory workflow-contract tests,
global `gofmt -l cmd internal`, and `git diff --check` pass. Race tests pass for
`cmd/factory` and `internal/github`. These local gates do not close the remaining
remote-effect cost review, retirement-membership decision, operator resolution,
or cross-stage contract review. No workflow, state write, or live GitHub/Pages
operation occurred.

WP-23 lease and Project-pagination review correction (2026-10-11): read-only
review found admission compared effect duration to the 50-minute run timeout
while claims expired after 15 minutes, and did not include cold Project item
pagination. The projector now uses a 55-minute claim lease with five minutes
reserved for outcome persistence and cleanup, admits against the earlier of the
claim expiry and run deadline, and cancels each effect at that absolute
lease-minus-reserve deadline. Project item snapshots have a ten-page ceiling;
the cold-cache cost estimate reserves all ten GraphQL reads. Focused
`go test ./cmd/factory ./internal/github -count=1` and `git diff --check` pass.
The per-request Pacer still governs runtime variation, especially managed-label
cleanup. Race and root gates will be rerun before review. No workflow or live
GitHub operation occurred.

WP-23 evidence-comment pagination review correction (2026-10-11): independent
re-review found evidence-marker lookup could scan unbounded Issue comments,
while the request estimate allowed only two calls per pending item. Evidence
lookup now stops after ten pages; admission budgets all ten GETs plus one
possible create/update per pending comment. A fixture proves the scan stops at
the cap without writing a comment, and a cost fixture proves the worst-case
reads are reserved. `go test ./internal/github ./cmd/factory -count=1`,
`go test -race ./internal/github -count=1`, and `git diff --check` pass. Root
gates and read-only re-review will be rerun. No workflow or live GitHub
operation occurred.

WP-34 final offline gate rerun after WP-23 review fixes (2026-10-11):
`gofmt`, `go mod verify`, `go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`,
`go test ./cmd/factory -run Workflow -count=1`, `git diff --check`, and race
tests for `cmd/factory`, `internal/github`, `internal/projection`,
`internal/discovery`, `internal/publish`, `internal/state`, and
`internal/queue` pass on the current worktree. Workflow-contract tests include
the repository YAML and embedded shell validation. This is local verification
only. WP-21 all-deselected retirement, WP-24 operator resolution, full WP-35
review, PR checks and live gates remain open; no workflow or live operation
occurred.

WP-28 schedule activation guard (2026-10-11): read-only contract review found
the five-minute workflow would execute automatically after merge, before
WP-41/HUMAN-002. The scheduled trigger and reviewed workflow remain in scope,
but the job is now hard-disabled in the review branch and its contract test
requires that guard. Only a separately approved activation change may enable
the job. Focused workflow-contract tests and YAML/shell parsing pass. No
workflow, state write or live projection occurred.

WP-34 gate rerun after label-cost and schedule-guard fixes (2026-10-11):
`go mod verify`, `go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`,
`go test ./cmd/factory -run Workflow -count=1`,
`test -z "$(gofmt -l cmd internal)"`, `git diff --check`, and required race
tests for factory, GitHub, projection, discovery, publish, state and queue all
pass. Workflow tests parsed YAML and checked embedded shell syntax. These are
offline results only. WP-21 retirement policy, WP-24 operator resolution,
final WP-35 review and PR checks remain open. No workflow or live operation
occurred.

WP-35 read-only contract review after WP-23/WP-28 fixes (2026-10-11): the
review confirmed scheduled projection cannot execute after merge because the
job is hard-disabled, and verified the ten-label cleanup ceiling is reserved
in effect admission. It found no additional source-level lifecycle or
provenance defect. WP-21 all-deselected retirement and WP-24 unknown-effect
operator resolution remain intentional fail-closed decisions awaiting product
direction; WP-35 cannot close until their selected contracts are implemented
and reviewed. No workflow or live operation occurred.

WP-32 documentation correction (2026-10-11): final source audit found manual
discovery board-write instructions and assessment/Pages-as-unwired descriptions
in the operations guide, workflow instructions, Go guide and old decision
record, plus obsolete Go pacing/comment procedures. The current guide/diagram
now describe state-only discovery, separate scheduled delivery and the combined
site lifecycle; the old decision is marked superseded and the separate lifecycle
diagram is labeled historical. F-019 distinguishes its historical v1 gap from
v2 source wiring, and the operations status table documents `Retired`. This
resolves F-047 locally; `go test ./cmd/factory -run Workflow -count=1`, stale-text
search and `git diff --check` pass. Full offline gates remain pending. No
workflow or remote projection occurred.

WP-34 gate rerun after WP-32 documentation corrections (2026-10-11):
`test -z "$(gofmt -l cmd internal)"`, `go mod verify`,
`go test ./... -count=1`, `go vet ./...`,
`go run ./cmd/factory --list-dimensions`,
`go test ./cmd/factory -run Workflow -count=1`, required race tests for
`cmd/factory`, `internal/github`, `internal/projection`, `internal/discovery`,
`internal/publish`, `internal/state`, and `internal/queue`, and
`git diff --check` pass on the current worktree. Workflow fixtures parse the
repository YAML and validate embedded shell. This is local verification only;
WP-21 and WP-24 policies, final independent review, PR update and gated live
work remain. No workflow or live operation occurred.

WP-21 all-deselected retirement checkpoint (2026-10-11): per Ledermayer's
decision, exact receipt-backed proof may release a retired module's version
slot without asserting assessment completion. Fan-out now binds the slot's
accepted request to the AVM release, source SHA, and facts ID; verifies the
facts acceptance operation receipt and complete required-context set; requires
an exact current null-selection receipt for every eligible context in the exact
verified Pages membership; and requires accepted catalog retirement. It writes
a neutral `retired` projection with no published assessment commit and releases
the slot under that exact Pages receipt/membership digest. An omitted required
context stays parked. Focused state tests pass; full race/review gates remain.
No workflow, state branch write, or Pages operation occurred.

WP-24 exact-readback operator resolution checkpoint (2026-10-11): per
Ledermayer's decision, an uncertain terminated projector effect is adopted only
when read-only verification exactly matches every factory-owned Issue and
Project surface. Missing or mixed effects stay parked. The manual recovery
workflow accepts exact module IDs for this path, capped at ten modules; state
adoption consumes a private client-minted readback value rather than a
caller-supplied binding. Incomplete Issue search results and duplicate Project
membership fail closed. Focused GitHub/state/CLI and workflow-contract tests
pass locally. Table fixtures reject changed title, identity marker, summary,
managed labels, each owned Project field, and missing or mismatched Issue-to-
Project membership; another proves an exact old effect acknowledgement leaves
a superseding desired projection pending. Independent read-only review found no
code blocker and requested correction of the historical F-044 narrative, which
is now labeled as pre-fix behavior. No workflow was dispatched, no state branch
was written, and no live GitHub read or mutation occurred. Full gates remain
open at the time of that checkpoint; see the final WP-34 rerun below.

WP-34 final offline gate rerun after WP-21/WP-24 review corrections
(2026-10-11): `gofmt` check, `go mod verify`, `go test ./... -count=1`,
`go vet ./...`, `go run ./cmd/factory --list-dimensions`, workflow-contract
tests (including YAML and embedded shell), `git diff --check`, and race tests
for `cmd/factory`, `internal/github`, `internal/projection`,
`internal/discovery`, `internal/publish`, `internal/state`, and `internal/queue`
pass locally. A read-only contract review found no code blocker and prompted
correction of the stale historical narrative in F-044. These are offline
results only. No workflow dispatch, live GitHub read/mutation, state branch
write, Pages deployment, or schedule activation occurred. PR CI and review
remain pending.

WP-17 nested changelog coverage correction (2026-10-11): WP-35 review found the
source handler listed only provider-repository root files, which could silently
omit nested changelogs. Collection now reads the recursive Git tree at the exact
provider release tag, retains nested and root `CHANGELOG*.md`, `.markdown`, and
`.txt` files, rejects unsafe paths, and fails closed on a truncated GitHub tree.
The existing request, response-byte, total-byte, and wall-time limits bound the
operation. Focused provider tests and
`go test -race ./internal/provider -count=1` pass. Missing, null, malformed
envelopes, or missing truncation status fail closed, while an explicit empty
tree remains a valid no-changelog result. This addresses changelog-named files;
provider-specific indexes with other names remain outside the collector
contract. The receipt-backed provider import and exact site-cycle wake are
fixture-backed as recorded in earlier WP-17 checkpoints; final cross-stage
review and live collection remain open. No collection or workflow dispatch
occurred.

Site-cycle assessment execution clarification (2026-10-11): “assessment with
site publication” means the site cycle imports accepted selection exports,
queues eligible assessment requests from exact accepted facts/provider/policy
receipts, reduces already accepted assessment results, then publishes the
current exact accepted shared data head. It does not dispatch workers or wait
for a new assessment to finish. New results require their separately gated
worker and acceptance stages and appear in a subsequent site cycle. This matches
the current `prepareIndexedSitePublication` implementation and preserves the
no-dispatch boundary. No workflow was run.

HUMAN-002 pilot scope (2026-10-11): Ledermayer approved one one-time
end-to-end pilot for exactly `avm-res-apimanagement-service`,
`avm-res-authorization-roleassignment`,
`avm-res-compute-capacityreservationgroup`, `avm-res-compute-disk`,
`avm-res-compute-virtualmachine`,
`avm-res-compute-virtualmachinescaleset`,
`avm-res-managedidentity-userassignedidentity`,
`avm-res-network-applicationgateway`, `avm-res-network-networkinterface`, and
`avm-res-network-virtualnetwork`. Keep all 232 catalog entries in the portal;
only the ten receive pilot processing and assessment. Authorized stages are
current-release discovery without force/backfill, acquisition, provider
intelligence collection, assessment after exact accepted inputs, reviewed
acceptance, and one publication under existing workflow/environment gates.
The scheduled projector remains disabled. The interrupted UTL continuation is
explicitly excluded. TASK-021 is updated in the plan; TASK-022 schedule
activation remains unapproved and pending.

Pilot workflow fix checkpoint (2026-10-11): the first bounded manual discovery
attempt, run [38157662354](https://github.com/softwareone-public/avm-intelligence/actions/runs/38157662354),
failed because the workflow omitted the positive Actions run ID required by the
manual-cycle CLI. Post-run verification found no `factory-state` head change or
module handoff. The feature branch now passes `GITHUB_RUN_ID` and adds CLI and
workflow contract tests. The exact run has not been retried. Site publication
now requires an explicit active-module list for input-driven assessment
planning, selection reconciliation, and selection, while still building the
full accepted catalog. Focused CLI/workflow tests and the full `cmd/factory`
suite pass. These were local fixture results only. The reviewed change merged
as PR #320, after which the exact cohort was dispatched and exposed the
separate transaction identity defect in F-049. No module handoff completed;
provider data has not been collected/accepted, assessment has not run/been
accepted, and no pilot publication occurred.

Discovery pilot transaction correction (2026-10-11, TASK-021): production
`DecisionCoordinator` rejects a mutating decision unless its stable operation
ID is also present as an event ID. PR #321 corrected that contract and merged
as `465925f6b2198eeb19f3e07abd119aeee66beafa`. After rereading the exact
continuation owner, expired lease, cursor, and active-run list, run
[38160309883](https://github.com/softwareone-public/avm-intelligence/actions/runs/38160309883)
resumed the saved ten-module cycle and completed all ten current-release checks
with `force=false`. The catalog remained the full 232 modules. The release
pins were unchanged, so no acquisition work was generated by that cycle.

Current-release request binding checkpoint (2026-10-11, TASK-021): the first
exact request dispatch,
[38160598353](https://github.com/softwareone-public/avm-intelligence/actions/runs/38160598353),
failed at claim before any attempt or worker launch. The selected requests are
queued for the exact current source SHAs but omit `avm_release_version` and
lack receipt-backed acquisition handoffs, both required by claim eligibility.
No state commit, acquisition, provider
collection, assessment, evidence acceptance, or publication followed. PR #322
merged the exact-current-release version-binding repair as
`fd4b73e01e2dd1f0a73f8357447cac875e4edff7`. PR #323 then made public source
resolution anonymous in paced manual discovery while preserving autonomous
behavior.

Exact no-force run
[38161477827](https://github.com/softwareone-public/avm-intelligence/actions/runs/38161477827)
stopped on module 1 during source resolution and wrote no module handoff or
version-bound request. After PR #323, the saved continuation was resumed as
[38162238273](https://github.com/softwareone-public/avm-intelligence/actions/runs/38162238273);
it again stopped in source resolution, with cursor `next=0`, no module request,
receipt, or handoff, and an expired generation-2 lease. The safe log does not
establish the preparation error. F-050/F-051 retain these separate observed
outcomes; no acquisition, provider collection, assessment, acceptance, or
publication followed.

Source review also found that paced continuation lacked the exact
receipt-backed request/handoff required by dispatch. F-052 records this
source-proven code defect; run 38162238273 stopped before reaching that write.
The focused local correction commits module-owned work with an operation
receipt, replays its serializable resolution and exact write set when the
cursor lags, then checkpoints the catalog outbox and cursor. Its recovery
fixture verifies projection-required state and catalog-reference persistence.
The discovery package suite passes locally; full repository gates and PR review
remain pending. Do not retry the live continuation before merge; afterwards
reread its exact owner, lease, scope, and active runs. Verify each exact request,
handoff, and receipt before dispatching only the approved requests.

TASK-021 remains pending. The full 232-module catalog remains in scope for the
portal, while only the ten approved modules receive pilot processing and
assessment. No provider collection, assessment, acceptance, or publication is
claimed. TASK-022 and the interrupted UTL exclusion remain unchanged.

TASK-021 receipt-backed continuation correction (2026-10-11): PR #324 merged
as `7c9f38a899068da3ba326f60fb413db0210cda30`; its required CI and local full,
workflow, and race gates passed. The next exact continuation,
[38164375052](https://github.com/softwareone-public/avm-intelligence/actions/runs/38164375052),
failed before a module transaction. Read-only state inspection at
`fa26ddb1172ffec45089a902c7184709e452a418` found the authorized ten-module
cursor at `next=0`, owned by that run at generation 3, with its lease expired.
No manual discovery run is active. Local reproduction isolated four
versionless acquisition requests whose historic fingerprints fail current
validation; the coalescer validates the full request history and stops before
preparing a new request. F-053 records the observed state and cause.

The focused correction on `fix/repair-historical-versionless-acquisitions`
adds receipt-backed compare-and-delete only for exact, unstarted versionless
request records whose sole validation error is the obsolete fingerprint.
Valid versionless records continue to be superseded and retained. The deletion
requires the exact current record and fails on any change; the module receipt
retains the full proof. Focused state, queue, and discovery suites pass. The
full local gate set also passes: `go mod verify`, full tests, vet, dimension
listing, workflow-contract tests, required race tests, gofmt, and
`git diff --check`. PR review/merge and retry of this exact saved continuation
remain pending. After merge, reread the exact owner, lease, scope, head, and active
runs before the authorized ten-module no-force resume. Do not dispatch
acquisition or start provider collection until each new request, handoff, and
receipt is verified. Provider collection, assessment, acceptance, and
publication have not occurred; the schedule remains disabled and the UTL
continuation stays excluded.

TASK-021 post-PR workflow-boundary review (2026-10-11): PR #325 merged as
`6bcd6c0f4c052e0d06df4c5d1a18b7993c0c72c2`. The saved ten-module cursor was
re-read after merge: exact scope unchanged, `next=0`, owner run `38164375052`,
generation 3, expired lease, `pending_delivery=false`, no active manual
discovery run, and `factory-state` head
`fa26ddb1172ffec45089a902c7184709e452a418`. Before resuming, source review
found five trusted workflows still generate an unused `git ls-tree`
state-tree file, contrary to the verified-checkout-root contract. F-054 records
the code finding. The focused correction on
`fix/remove-derived-state-tree-listings` removes these redundant commands and
adds a YAML/shell workflow-contract test. Full tests, vet, module verification,
dimension listing, workflow-contract tests, the CLI race test, gofmt, and
`git diff --check` passed locally. PR #326 merged as
`c0bc39eae7719b51d7e17135f812eeab08a21a5d`. The exact continuation was re-read
and resumed as run `38166707534`; it wrote an owner checkpoint and then stopped
before the first module transaction. F-055 records the newly observed blocker.
The full catalog, ten-module processing boundary, disabled schedule, and UTL
exclusion remain unchanged.

TASK-021 unrelated-request isolation checkpoint (2026-10-11): run
[38166707534](https://github.com/softwareone-public/avm-intelligence/actions/runs/38166707534)
prepared but did not commit the first module handoff. At its resulting state
head, a read-only audit found 193 versionless acquisition requests across the
catalog. The module handoff/status reducer validated all of them, so unrelated
legacy requests could block the exact pilot module. F-055 records the observed
run and source path. Local changes scope status reduction, exact request/module
claims, module lifecycle readers, and accepted-assessment queue coalescing to
the affected module set; whole-queue dispatch remains globally validated.
Focused regressions reproduce the unrelated-history condition. Run the exact
queue and discovery suites, then the repository review gates. Do not resume the
saved cursor while its generation-4 lease is active. After review/merge, re-read
owner, lease, scope, head and active runs, and resume only the same approved
continuation. This remains one-time ten-module processing with a full 232-module
portal catalog; the schedule stays disabled and the interrupted UTL continuation
stays excluded.

TASK-021 worker-preparation checkpoint (2026-10-11): the exact pilot
acquisition attempts for apimanagement, roleassignment, and virtualnetwork
failed before run binding. F-056 records the three runs and the source-proven
relative-versus-absolute `work/state` comparison. PR #328 merged the normalization
fix at `9fbcc25926cb68cade76741900591cfe77bbcf73`. Recovery runs `38172345160`,
`38172464820`, and `38172551570` retired the three exact unbound attempts.
Verified state at `48f3735917d50c18924b0b595a5d0749b40c98e8` showed all ten
release-bound requests queued without owners. The first retry,
`38172762923`, then failed before claiming the apimanagement request because the
queue found both its original input handoff and the prior `dispatch:` handoff;
F-057 records the incident and source cause. No state write or worker dispatch
occurred. A narrow queue fix and claim/recovery/retry regression now pass the
focused test. Full repository gates and PR review remain pending. Keep all ten
requests undispatched until merge and fresh preflight. The portal retains the
full 232-module catalog; only the exact ten receive pilot processing; scheduled
projection remains disabled, and the interrupted UTL continuation remains excluded.

TASK-021 retry and worker-seal checkpoint (2026-10-11): PR #329 merged as
`9dfcdf4f945b67c85620d34717584943b0989096`, and the first exact retry,
dispatcher run `38173736565`, successfully claimed apimanagement at attempt 2,
fence 2 and launched worker `38173784861`. The queue fix is confirmed live.
The worker prepared the pinned source and computed a facts artifact, but its
trusted seal failed. At state head `e2aa7b1cebb1494adbe38b204cdb886fe502ca9f`,
the exact attempt is bound to that terminal failed run and the request remains
running; the other nine requests are still queued. F-058 records the observed
failure and source-confirmed omission of request/dispatcher/workflow/commit
inputs from `manual-worker.yml`. The focused fix adds those inputs and scopes
App credentials to minting steps; manual and factory workflow contract tests
pass locally. Full tests, vet, module verification, dimension listing, the
workflow-contract suite, `cmd/factory` race tests, gofmt, and diff checks pass.
PR review remains pending. After merge, recover
only this exact failed attempt, verify its request is requeued, and retry it
before dispatching any of the other nine. Keep the full 232-module catalog,
exact ten-module pilot scope, schedule-off and UTL-exclusion boundaries.

TASK-021 post-PR prepare failure checkpoint (2026-10-11): PR #330 merged at
`bb7409442cbeb76278015960c56f27a939912f6f` and supplied trusted seal
provenance. The next apimanagement retry, worker run
[38174351615](https://github.com/softwareone-public/avm-intelligence/actions/runs/38174351615),
failed in `Bind run and prepare exact accepted inputs` before preparation. A
read-only inspection confirms `factory-state` head
`c1ccc616f2890bcdf187a82fb7ee60ae60e5caa1`: exact request
`avm-res-apimanagement-service-b0c863b45394f47e0863a8c39b29ac2a903a4e0935df82a9d2da79bb71e294c6`
is claimed at fence 3 with attempt
`attempt-e16bdbb6cb9a12b47f6a81215023424b1d4e42302be8894047e350f2eb812be6`,
but the attempt is unbound (`run_id` and `run_attempt` are null); its lease
expires at `2026-10-11T21:27:14.414761777Z`. The other nine pilot requests
remain queued. Source inspection found that PR #330 scoped App credentials to
minting only, although both prepare and seal CLI state operations construct the
credentialed state backend. The focused correction supplies App credentials to
only the mint, prepare, and seal steps in both worker workflows, with
workflow-contract assertions for exact step scope; focused tests pass. After
the correction, `go mod verify`, `go test ./... -count=1`, `go vet ./...`,
dimension listing, workflow-contract tests, required race tests, global gofmt,
and `git diff --check` pass. After review and merge, reconcile only this exact
unbound attempt, verify it is requeued, then retry only apimanagement. No
provider collection, assessment, evidence acceptance, or publication occurred.
The full catalog remains 232 modules; only the approved ten receive pilot
processing, and scheduled projection remains disabled.

TASK-021 post-PR seal failure checkpoint (2026-10-11): PR #331 merged the
step-scoped state credential fix at
`f25fa24fe06802a5e70711b1d68588f9346d0cde`. Recovery run `38175007854`
retired the exact unbound fence-3 attempt, and exact-request dispatcher run
`38175086623` requeued/claimed apimanagement at fence 4. Worker run
[38175129120](https://github.com/softwareone-public/avm-intelligence/actions/runs/38175129120)
passed prepare and compute but its trusted seal failed. The exact state head
`4870c19a91df27229bf1083510bab91c259c3ee3` still has that request running
with attempt `attempt-12844522d62f95b11e804f72512ebec2f961f6def54fd4bf470d049b1f241ead`
dispatched and bound to `38175129120-1`; no seal receipt or accepted facts
exist. F-060 records the cause: completion candidate provenance used
`worker.complete` while the Git candidate allowlist accepts `worker.seal`.
The narrow correction aligns that existing contract. A read-only local
reconstruction of the full snapshot and artifact passes the actual Git
candidate builder with the corrected ID; full gates and PR review remain
pending. After merge, recover only this attempt, verify the exact v0.1.0
request is queued, then retry only apimanagement. No provider collection,
assessment, acceptance, or publication occurred. Keep all 232 modules in the
catalog, process only the approved ten, keep scheduled projection disabled,
and leave the UTL continuation excluded.

TASK-021 evidence acceptance checkpoint (2026-10-12): Ledermayer approved
PR #333, the exact apimanagement `v0.1.0` facts proposal, and it merged at
`ab5f2d4a0e87604ea92780f1429a00c50fa1a29b` after both required checks passed.
The exact acceptance workflow run `38183547573` passed attempt/run/artifact
verification, then failed before proposal reconstruction or receipt import:
its credential-free main checkout ran `git fetch origin main` without a
credential and Git could not read the HTTPS username. The receipt-import step
did not run; no acceptance receipt was created. F-061 records the source cause.
A narrow correction verifies the live main ref SHA through the workflow's
authenticated GitHub API client while retaining local ancestry and exact-tree
checks. Its focused workflow-contract tests pass locally. After this fix is
reviewed and merged, rerun only the same acceptance import for the exact
attempt and evidence commit, verify the receipt, and proceed to provider
intelligence only when its accepted-input gates are satisfied. No provider
collection, assessment, or publication has occurred; the 232-module catalog,
ten-module pilot scope, schedule-off, and UTL-exclusion boundaries remain.

TASK-021 evidence acceptance resolution (2026-10-12): PR #334 merged the
credential-free main-ref check at `1146a569ddd8831fe84c25d9a76c4941b2bfb689`.
Acceptance run `38183808001` imported the exact apimanagement v0.1.0 facts
receipt `evidence-acceptance-21e1380876d3fc1cb5cffdcfd3493a39e09ab25c0ed88bde711a476586d86dcb`;
`factory-state` advanced to `ecdb7a5a`. The durable request is `facts_accepted`
and binds the merged facts, source pin, completion, and evidence commit. The
required assessment context set is incomplete: AzureRM 4 and 5 are configured,
but AzAPI has no assessment tracks and `modtm`/`random` are absent from the
reviewed policy. No provider collection, assessment, or publication has yet
occurred. A source audit found the same Git-fetch credential defect in the
provider-input importer and three site-publication main-ref checks; the
read-only fix and regression tests are in PR #335. Do not assess until exact
accepted contexts are available under the reviewed policy. All ten-module,
full-catalog, schedule-off, and UTL-exclusion boundaries remain in force.

TASK-021 provider snapshot checkpoint (2026-10-12): PR #336 merged the
AzureRM tag-alias correction; PR #337 corrected the AzureRM documentation
layout at `7e270331628ea113119cc2d8710d0b533986c3b9`. Retry `38184386667`
produced an empty unexported/unaccepted proposal because the collector queried
the wrong directories (F-063). After #337, exact retry `38184814905` passed base
and release checks but failed at resource collection. A bounded reproduction
with the same provider commit returned `provider response exceeds 1048576 bytes`
under the reviewed 1 MiB response limit (F-064). No proposal or accepted
snapshot exists. On 2026-10-12, the user chose exact-type collection from exact
accepted pilot facts, with other provider catalog coverage unavailable; limits
remain unchanged. PR #346 merged the scoped collector and manual workflow at
`a92386a520c8c91d5875af3a900ea2ba8068db2c`; focused provider, assessment,
publisher, CLI, schema, workflow-contract, full Go, vet, dimension, module
verification, and required race checks passed locally. No provider collection
retry has run. Collect only for exact accepted facts references, then
independently review/import the provider receipt and assess. AzureRM 4.81.0 is
pending; AzureRM 5.9.0, AzAPI, resource-intelligence collection, assessment,
and the single publication remain pending. All pilot scope, full catalog,
schedule-off, and UTL-exclusion gates remain.

F-065 follow-up (2026-10-12): the user confirmed the App's
`pull_requests:write` grant. This resolves the permission approval question but
not the prior HTTP 422, so no second token-mint retry was run. Authenticated
`gh` opened the deterministic role-assignment facts proposal as PR #345; both
required checks passed, and it merged at `c891bd91d8bfdcf42a4a76ef974c9fd8de99300b`.
The exact acceptance receipt run `38203919905` stopped before state import
because of the proposal newline mismatch recorded in F-066. PR #346 fixed the
normalized comparison. Exact retry `38204224810` imported receipt
`evidence-acceptance-6e5145d6a9a2255b43fc1312b512599b12f92c42eaaca5ac86ad8fe2b2ae2262`
at factory-state head `8817e06b9e7e052250633e536c38f67ceb8165c2`. The durable
request is `facts_accepted`, bound to source `8f90a2110e81def1c63f8f81f44e60554793e50e`,
facts `facts-9b5915fd8837dcb00c039ffaecba927608641d208c125a887c85ecab1234f77b`,
and release `v0.3.1`. The PR body described `v0.1.0`; state receipt/request is
the authoritative version identity. The required context set is incomplete:
AzureRM 4/5 are eligible; time, azapi, azuread, random, and modtm lack resolved
assessment tracks under existing policy. Do not report assessment or selection
for this module until the exact required contexts are satisfied.

PR #340 corrected the state credential omission; PR #341 added safe
reduced-token diagnostics. Retry `38186351045` again passed state inspection
and evidence building, then failed with HTTP 422 during reduced PR-review-token
mint. F-065 records the exact requested permissions and the documented generic
422 meaning. After PR #343 added safe structured parsing, retry `38186967130` still
returned HTTP 422 without validation fields. Repository settings show the App
installed, but viewing its configuration requires sudo reauthentication. The
request's exact validation cause remains open; do not retry token mint blindly.

F-066 resolution (2026-10-12): run `38203919905` verified the role-assignment
attempt and merged proposal, then failed at a bytewise `proposal.json`
comparison due to a trailing newline without writing state. PR #346 compares
normalized proposal JSON and preserves byte-exact accepted-data comparison.
Exact retry `38204224810` imported the receipt successfully; F-066 is resolved.

HUMAN-002 cohort checkpoint (2026-10-12, after receipt import): APIM
`v0.1.0` and role assignment `v0.3.1` have exact accepted facts receipts. The
other eight approved modules remain queued at discovered current releases:
capacityreservationgroup `v0.1.0`, disk `v0.4.1`, virtualmachine `v0.21.0`,
virtualmachinescaleset `v0.11.1`, userassignedidentity `v0.5.3`,
applicationgateway `v0.5.3`, networkinterface `v0.1.2`, and virtualnetwork
`v0.22.2`. These are durable queued requests, not accepted facts. No provider
collection, assessment, selection, or site publication has run.

TASK-021 acquisition follow-up (2026-10-12): six of the eight remaining
module workers have completed facts-only computation and trusted sealing;
their exact attempts still need current state verification and reviewed
evidence delivery/import. Capacityreservationgroup attempt
`attempt-5248941fc814b33042d40cc372092e6f0f712c6e23b90f4a2f40447d0469e663`
has reviewed facts proposal PR #348 merged at
`438d566650feda4e79ae48a9bc3f75fcf48a171a`; receipt import run `38205323371`
succeeded with receipt
`evidence-acceptance-eaf4ebdb542717c3ecedf57f5351489953ab93556cec94a89b00078e9dd49ec7`
at state head `1da9fef0423a78a740055a00ff6c6d028067fb07`. Virtualnetwork worker
run `38204985459` failed before artifact or
trusted completion because its pinned source has Terraform under
`tests/unit/fixtures`; F-067 records the reproduced source cause and local
correction. Keep its exact request parked until the correction is reviewed
and merged, then reread and recover that attempt under its current fence before
retrying it. Do not dispatch any other cohort request until any prior slot
owner is sealed, accepted, or exactly recovered. After facts are accepted,
continue exact-type provider collection only for those accepted facts.
Assessment still requires the complete reviewed provider/policy context
vector; selection and the single site publication remain later gates.

TASK-021 provider-context policy checkpoint (2026-10-12): Ledermayer approved
extending the policy for providers used by the accepted ten-module cohort.
`feat/pilot-provider-contexts` adds exact tracks for AzureRM, AzAPI, AzureAD,
modtm, cloudinit, http, null, random, time, and tls; canonicalizes provider
addresses at extraction and consumer boundaries; admits major-zero tracks; and
excludes Terraform-native `terraform_data` and `terraform_remote_state` from
external provider assessment and resource-intelligence collection. The full
Go suite, vet, module verification, dimension listing, required race tests,
workflow-contract/YAML/shell checks, gofmt, and diff checks pass locally; PR
review passed in PR #357 at `bf6ba2f3`. The accepted facts and current generated
dependency index are unchanged; any derived-index rebuild must use the normal reviewed data
proposal path. No provider collection, provider snapshot acceptance,
assessment, selection, or publication has run. Continue TASK-021 with exact
accepted provider inputs before assessment, then review assessment acceptance,
selection, and the one authorized site publication. Preserve the full catalog,
exact ten-module processing scope, schedule-off, and UTL-exclusion boundaries.

TASK-021 major-zero importer follow-up (2026-10-12): after PR #357 merged, the
exact collection preflight found `cmd/factory` still rejected a provider
snapshot whose release major is zero. `fix/provider-major-zero-import`
removes that stale guard and validates exact stable SemVer through one shared
parser; the full Go suite, vet, module verification, dimensions, required race,
workflow, formatting, and diff checks pass locally; PR #358 merged at
`4cf7a61b`. No provider collection or input import has run yet. The approved
cohort scope, full catalog, schedule-off, and UTL-exclusion remain unchanged.

TASK-021 provider export checkout correction (2026-10-12): exact-scope AzureRM
4.81.0 collection run `38210420584` succeeded on accepted base
`e7d842a50ab97eaffccf91795afcc3d8b55d4b4b` and produced proposal
`proposal-728c5206256f8a6809831fd72225d0a08df2519dfb7381192eba077f0968b556`,
snapshot `snapshot-8ad3c964e112aa59b05de294323f4921365e7d0269fe866a394668582412e3d2`,
with 91 entries and explicit incomplete coverage for types outside the ten
accepted facts. Export run `38210637759` failed during checkout before candidate
validation or branch creation. Its checkout attempted SHA-prefixed branch and
tag refspecs; the collection run fetched the same SHA directly. The export now
checks out the dispatch's `main` ref while retaining its exact `GITHUB_SHA`,
working-tree SHA, and remote-main equality guards. Focused workflow tests and
the normal PR checks must pass before retrying export against a fresh collection
run on merged code. F-070 records the observed failure. No provider data was
accepted and no assessment, selection, or publication occurred.

TASK-021 provider snapshot import correction (2026-10-12): snapshot PR #361
merged at `c53e62c230a37957befc96bdc5ce91c87a1b029f`. Import run
`38211458129` verified that exact accepted `main` commit, minted its scoped
state token, and checked out `factory-state`, then stopped before the CLI ran
because `work/v2` did not exist for the workflow's temporary import JSON. The
state ref remained `e09a01fe65fe625220ca1a14a05c9032bbfedf54`, confirming no
state write. PR #362 merged the missing-directory correction at
`cff993bce5bccca51a720eacf1766a6c240e0708`; run `38211717933` confirmed the
temporary import file was created, then failed while deriving targets. Read-only
reproduction against the current `factory-state` snapshot identifies two older
schema-v1 pattern facts receipts which lack exact context sets and are invalid
under the current v2 receipt validator. The state ref remained
`e09a01fe65fe625220ca1a14a05c9032bbfedf54`; no write occurred. PR
`fix/skip-legacy-acceptance-targets` skips only those historical schema-v1
receipts for target derivation, still requires complete proof for v2 facts
receipts, and rejects unknown receipt schemas. Merge it under normal checks,
then retry the exact import. Read-only verification after the correction
derives only the ten approved module-version targets for AzureRM 4. F-071
records the first failure and F-072 this independent target-scan defect.

TASK-021 AzureRM 4.81.0 accepted-input checkpoint (2026-10-12): PR #363 merged
the historical-receipt target correction at
`a1ef25e8ddfea89d74563973494523ba2d2bdba7`. Collection run `38211128661`
successfully produced 91 exact-used-type entries for the ten accepted facts
(83 resources and 8 data sources, all scoped entries present); provider catalog
coverage outside that exact set and action/ephemeral/function kinds remain
unavailable. Export run `38211227754` created a data-only branch, and provider
snapshot PR #361 merged at `c53e62c230a37957befc96bdc5ce91c87a1b029f`.
Import run `38212245275` succeeded against exact main commit
`a1ef25e8ddfea89d74563973494523ba2d2bdba7`, creating snapshot import
`input-import-d1261f49d7d3545c9f28031653e0965439a02f4d40c5f1b835fb09e279f31828`
and policy import
`input-import-a9531480f4b2b2a764323c0b28eab90b87aa12e6ebfa6da9d1893c35acb736b8`.
Both receipts target exactly the ten approved AVM module releases and the
AzureRM 4 context; desired generation is 2 after the snapshot and policy
imports. The final `factory-state` head is
`59109a1f7d4759f8fc5c4df1cdf01d17a4e1f569`. No assessment was dispatched.
Continue exact-type collection and receipt import for the remaining configured
provider versions; do not assess until each module has its complete exact
provider/policy context vector. Assessment acceptance, selection, and the single
site publication remain pending. Preserve the full catalog, exact ten-module
processing scope, schedule-off, and UTL-exclusion boundaries.

TASK-021 scoped-provider mapping resolution (2026-10-12): PR #368 merged the
path mapping fix at `b31c7cfa`. The corrected AzureAD 2.53.1 collection run
`38217245221` marked all ten exact used types present (6 resources, 4 data
sources); data PR #369 merged at `5c2d5c91`. Its exact-scope state import failed
without changing factory-state; see F-075.

TASK-021 historical facts context-reader checkpoint (2026-10-12): state import
run `38217624738` failed on two historical schema-v1 pattern facts receipts
before either policy or provider snapshot input committed. Fresh state readback
confirmed unchanged `factory-state` head
`5a042e4c4684dca393cb6e50fe226c93274e2d80`. A narrow v1-skip/unknown-schema
rejection fix and regression test are in progress on
`fix/skip-v1-facts-context-receipts`. Do not retry until its PR merges; then
retry only the exact AzureAD 2.53.1 import at the exact accepted main commit
required by the workflow.

TASK-021 historical context-reader resolution (2026-10-12): PR #370 merged at
`ad8684fd`, skipping only historical v1 facts receipts in context-set discovery
while rejecting unknown schemas. Retry run `38218364660` succeeded against exact
main commit `ad8684fd1d283d25d95e0aaf03c33a4b025a5417`. It imported ten policy
context sets for the pilot (70 eligible targets) and the AzureAD 2.53.1 snapshot
for its two eligible module releases. Final `factory-state` head:
`d0e93124fcdc17b9f1e642e9486034f89f1b550b`. Continue collecting and importing
remaining exact used provider tracks. No assessment has been dispatched.

TASK-021 provider snapshot completion (2026-10-12): exact-used-type snapshots
for the eligible cohort contexts are now accepted and receipt-imported. The provider snapshot
PRs #372 through #380 and their required checks merged; documentation PR #371
also merged; `main` then stood at
`5dd59b96a238e4190423759d938639fe3c09c92c` and final `factory-state` is
`7a08d92831aae8aa0eaa08d485d0d672df6710af`. The state contains 60 accepted
module/context references across 12 distinct provider-version pairs. AzureRM
3.117.1 has no eligible target. The reviewed policy already covers the exact
provider addresses and major tracks found in the ten accepted facts records.
The separate provider resource-intelligence collector was limited to one
module’s facts per proposal, which would omit other cohort-used types or require
repeated overlapping collections. A bounded change is in progress to accept
the exact ten facts references and collect the used-type union for one provider
snapshot per proposal. No resource-intelligence source collection, assessment,
assessment acceptance, selection, or publication has run.

TASK-021 batched resource-intelligence contract (2026-10-12): PR #381 merged
at `f6db3a11`; it updates the manual workflow and CLI to require the exact ten
approved module facts references, validate each against accepted data, bind all
source and facts digests into a version-2 proposal, and enrich one exact
provider snapshot with the union of resource, data-source, and ephemeral types
from those facts. The existing human-reviewed provider export and
receipt-backed input import remain the acceptance path. During pre-dispatch
review, PR #382 fixed two source-proven blockers: the collector had counted
all HTTP requests against `max_pages`, and issue searches lacked the workflow
token. PR #383 then allowed used types to remain with empty source lists while
preserving their accepted stub identity. The first exact AzureRM 4.81.0 live
attempt, run [38224613771](https://github.com/softwareone-public/avm-intelligence/actions/runs/38224613771),
failed at the recursive Git tree response (43,103 entries, 14,452,106 bytes,
truncated against the reviewed 1 MiB response limit) before producing a
proposal. PR #384 merged the bounded nonrecursive traversal. The exact retry
against merged base `2d06d0cd` passed input guards and tree collection but
failed on a GitHub issue-search response larger than 1 MiB; run
[38225536751](https://github.com/softwareone-public/avm-intelligence/actions/runs/38225536751)
produced no proposal. PR #385 reduced issue page size to 10; that retry reached
the configured page limit for `azurerm_client_config` while more issues remained.
Run [38226166980](https://github.com/softwareone-public/avm-intelligence/actions/runs/38226166980)
produced no proposal. PR #386 raises AzureRM's issue page, total-byte, and
wall-time budgets only to existing policy hard caps. The retry against
`33e5518e` reached GitHub's search API rate limit; a read-only exact-scope
diagnostic confirmed HTTP 403 with `X-RateLimit-Remaining: 0` at issue page 31.
PR #387 adds reset-aware waits and bounded secondary-limit backoff; the retry
log shows five rate-limit waits before a sanitized failure at 5m48s. It produced
no proposal. PR #388 merged safe source-family and failure-class diagnostics,
without raw source or API error content. PR #389 narrowed issue searches to
exact used-type titles without changing collection bounds. Retry
[38228561232](https://github.com/softwareone-public/avm-intelligence/actions/runs/38228561232),
reported the 50 MiB aggregate response limit during issue collection for
`azurerm_nat_gateway`; it produced no proposal. F-084 records the broad issue
query correction. After that change, retry
[38229807068](https://github.com/softwareone-public/avm-intelligence/actions/runs/38229807068)
failed inside the CLI after two issue-search waits. PR #390 added stage and
byte-count telemetry. The next exact-scope retry,
[38230704211](https://github.com/softwareone-public/avm-intelligence/actions/runs/38230704211),
collected 2,425 source attachments (127,282,288 body bytes) and failed the
proposal's 1,000-source/10 MiB bound because full shared documents were copied
to matching resource stubs. PR #391 merged normalized source documents and
per-stub references. Its exact-scope retry,
[38232963754](https://github.com/softwareone-public/avm-intelligence/actions/runs/38232963754),
passed input guards and collection but again failed with `artifact_budget`;
the attachment metrics did not expose unique document count or body bytes.
F-087 tracks safe normalized-pool metrics and distinct failure classes. After
that correction is reviewed and merged, retry only AzureRM 4.81.0 from the
then-current accepted base, review and import any resulting snapshot, then
continue the remaining eligible pairs sequentially.
Do not dispatch assessments until every eligible context has exact accepted
provider and policy inputs.

TASK-021 normalized-source checkpoint (2026-10-12): PR #391 merged source
bodies once per exact provider snapshot and sorted content-bound IDs from
resource stubs. New accepted provider snapshots use schema v3; schema v2 inline
evidence remains readable, and the portal resolves both forms. Its exact-scope
retry, run 38232963754, completed collection but failed proposal validation with
`artifact_budget`; normalized document count and byte totals were not logged.
No proposal or accepted data/state change resulted. A safe metrics and failure
class update is in progress before the next retry of only AzureRM 4.81.0.

TASK-021 normalized-pool bound checkpoint (2026-10-12): after PR #392 merged,
exact retry [38233844043](https://github.com/softwareone-public/avm-intelligence/actions/runs/38233844043)
collected 2,425 resource references to 1,987 unique source documents containing
9,259,142 bytes. Proposal validation identified only the 1,000-document cap;
the existing 10 MiB body and 10,000-reference bounds passed. This branch raises
the fixed document ceiling to 2,500 and adds a fixture for the observed
1,987-document payload. Per-source collection caps and exact accepted inputs
remain unchanged. PR #393 merged the proposal-side bound. Retry
[38234739970](https://github.com/softwareone-public/avm-intelligence/actions/runs/38234739970)
then passed proposal validation with those counts but failed during snapshot
enrichment because `assessment.ValidateProviderSnapshot` still capped normalized
documents at 1,000. PR #394 aligns that shared snapshot cap and extends the
fixture through enrichment and full provider snapshot validation. The failed
run produced no uploaded artifact, accepted provider data, state write,
assessment, selection, or publication.

TASK-021 provider proposal builder checkpoint (2026-10-12): retry
[38235430362](https://github.com/softwareone-public/avm-intelligence/actions/runs/38235430362)
passed collection, proposal validation, and snapshot enrichment, then failed
while building the publication proposal. A local synthetic reproduction using
the exact accepted facts and provider snapshot produced the same
`provider snapshot metadata or source identity is invalid` error. The enriched
provider snapshot already carries its content-derived ID, while the proposal
builder previously required an empty ID before recomputing it. This branch lets
the builder accept a precomputed ID only when it matches recomputation and adds
a tamper-rejection test. No proposal artifact, accepted provider data, state
write, assessment, selection, or publication resulted from the live run.

WP-17 / TASK-021 source-collection scope review (2026-10-12): the existing
workflow’s single-module facts input scoped collection to only that module’s
resources even though accepted provider snapshots contain the exact used-type
union for the cohort. The implementation PR changes the input to the approved
ten-reference facts scope and records every source revision and facts digest in
the proposal. Provider source collection and acceptance remain unperformed
until that code is reviewed and merged.

TASK-021 provider resource-intelligence pilot checkpoint (2026-10-12): PR #396
merged the exact AzureRM 4.81.0 proposal from collection run 38236336476; its
manifest has all 91 used types and 1,987 source documents (1,981 issues, five
changelog files, one release note), with no Registry upgrade guide match.
Receipt-import run 38237149165 succeeded against accepted `main` commit
`b8a31b8175a819f50223c5b745b935e1928a6630`; `factory-state` advanced to
`e82de47bed036b2bd594852f1c3c17e4d75920da`. PR #397 merged the AzAPI 2.13.0
snapshot from collection run 38237357158; receipt import run 38237629497
succeeded after exact-main validation. The modtm 0.4.0 run 38237742864 found no
matching source records and emitted no proposal. AzureAD 2.53.1 run 38237824042
failed closed at the initial 10-page issue-search cap for `azuread_application`;
F-089 tracks the bounded retry. Remaining provider-version pairs, complete
accepted context verification, assessments, evidence acceptance, selection,
and the one site publication remain pending. Registry-guide and no-source
results stay explicitly unavailable; no live assessment or publication is
claimed.

TASK-021 provider resource-intelligence continuation (2026-10-12): AzureAD
2.53.1's exact retry run 38238149822 completed after PR #398 raised only its
issue-search page ceiling within existing hard bounds. AzureAD snapshot PR #399
merged and receipt import run 38238447047 succeeded; AzureAD 3.10.0 collection
run 38238562199 and snapshot PR #400 also passed both required checks, and its
receipt import run 38238903448 succeeded. AzureRM 5.9.0 collection run
38238992076 exposed F-090: the exact snapshot marks `azurerm_ai_services`
absent, while the loader incorrectly requires a stub for every facts-used type.
The narrow correction is under review; no AzureRM 5.9.0 proposal or state write
resulted. CloudInit, HTTP, Null, Random, Time, and TLS provider/version pairs
remain. No assessment, evidence acceptance, selection, or site publication has
run.

TASK-021 AzureRM 5.9.0 resource-intelligence checkpoint (2026-10-12): PR #401
resolved F-090 by skipping only exact types explicitly marked absent in accepted
snapshot coverage. Retry run 38239505680 collected the other 90 used types and
preserved `azurerm_ai_services` as absent, with 1,983 source documents: 1,976
issues, six changelog files, and one release note. Snapshot PR #402 passed both
required checks and merged; receipt import run 38240117329 succeeded against
accepted main `cf0cc3409a49474292d6146ce76a4314495b87c6`, advancing
`factory-state` to `7ac3b989b0d8606bc310c28647aa7945b774c71e`. CloudInit, HTTP,
Null, Random, Time, and TLS provider/version pairs remain. No assessment,
evidence acceptance, selection, or publication has run.

TASK-021 provider-intelligence collection completion (2026-10-12): exact used-
type resource-intelligence snapshots for the remaining Random 3.9.1, Time
0.14.2, and TLS 4.4.1 contexts merged in PRs #407, #408, and #409. Their
receipt imports succeeded in runs 38241942788, 38242430722, and 38242825518;
current accepted `main` is `af4703d2efed851d05d0c084549285ae5b3455a2`, and
`factory-state` is `9c3e0d5cf76b486ed8ce8c572ee1cc25c1cb0e9b`. Across the
approved ten release facts receipts, the state now records 70 provider-major
context generations under one assessment policy ID. At this checkpoint, sixty
contexts had exact accepted provider snapshot inputs; the ten AzureRM major-3
inputs remained outstanding. Modtm 0.4.0 produced no matching provider-
intelligence sources and no proposal. The collected source families contain no
matching Terraform Registry upgrade guides, so that coverage remains
unavailable. No assessment, assessment acceptance, selection, or Pages
publication had occurred.

TASK-021 exact-context correction (2026-10-12): review of accepted facts showed
AzureRM 3.117.1 was eligible for provider-intelligence collection across the
cohort's required AzureRM 3 contexts. Collection produced an exact used-type
snapshot covering 89 present types and two explicitly absent types; proposal PR
#411 merged and its exact state import succeeded in run 38244604867. The
receipt-backed matrix now has exact provider and policy inputs for all 70
provider-major contexts. This resolves F-092; assessment and acceptance remain
pending. F-091 remains open until a queue-only assessment wake is merged. The
single final Pages publication has not run.

TASK-021 queue-only assessment wake checkpoint (2026-10-12): PR #412 added the
exact ten-module assessment wake, merged at `d558a47bbcbb0973a400e3bc1e079d387b502451`.
Its manual run 38245996054 passed the main-commit fence, then failed before
state checkout because the job lacked the `data-publisher` environment that
provides the state-writer App secrets. No state write, worker dispatch, or
assessment occurred. F-093 records the incident. A correction now attaches the
existing environment; after it merges, retry through that environment's normal
approval gate, verify the durable 70-context queue result, then run bounded
assessment workers. Acceptance remains review-gated and the one Pages
publication remains pending.

TASK-021 state-provenance correction (2026-10-12): after PR #413 supplied the
environment-scoped credentials, manual run 38246376310 reached the queue
transaction and failed because `assessment.wake` was missing from the state
candidate provenance allowlist. Read-only ref verification confirmed
`factory-state` stayed at `0b0831b545f836914488b533f6e2d212487d3e3b`; no requests
were committed. F-094 records the source-proven cause. A narrow allowlist entry
and regression test are under review. Retry only after that correction merges;
then verify the committed per-module wake receipts and exact request/deferred
counts before dispatching assessment workers.

TASK-021 assessment-wake retry checkpoint (2026-10-12): PR #414 added the
narrow `assessment.wake` provenance allowlist entry and regression test. Retry
run 38247029462 still failed with the generic sanitized terminal error, while
read-only ref inspection confirmed `factory-state` remained unchanged at
`0b0831b545f836914488b533f6e2d212487d3e3b`. F-095 records the missing safe
diagnostic stage/category. PR #415 added safe failure attributes; retry
38247903801 then identified `phase=plan_accepted_inputs`, still without a module
or finer stage, and again made no state write. F-095 records the remaining
diagnostic gap. A narrower planner instrumentation correction is under review;
before the next one-time wake retry, merge it and identify the exact receipt or
context stage from safe fields. No worker dispatch, assessment, acceptance,
selection, or publication has occurred.
