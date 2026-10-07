# Findings

## Source-Proven V2 Gaps (Not Incident Claims)

These behaviors were verified in the checked-out factory source at baseline
`main` commit `908ee877c0ff379b5552fa9558f43aa1233d74bb`. They describe
implementation risks, not claims that each consequence occurred in production.
Run-specific events and receipts are kept separately in F-001 through F-013.

| Area | Source-proven behavior and evidence | Follow-up |
| --- | --- | --- |
| Preservation | [`Assemble`](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/internal/publish/candidate.go) builds from dispatched modules; [`replaceAcceptedBundle`](../internal/publish/accept.go) stages that candidate as a replacement; [`fleet-plan.yml`](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/.github/workflows/fleet-plan.yml) installs it with `rsync --delete`. A targeted accepted cohort can therefore omit unrelated canonical reports. No production deletion is asserted. | TASK-005, TASK-006, TASK-023 |
| Durable state | [`fleet-plan.yml`](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/.github/workflows/fleet-plan.yml) starts discovery/planning in a fresh run workspace without loading prior accepted discovery, requests, or source pins. [`queue.Start`](../internal/queue/request.go) claims through local files, not a cross-workspace CAS. Multiple Actions workspaces therefore do not share durable ownership. | TASK-007, TASK-011, TASK-013, TASK-014 |
| Ownership and acceptance | Fleet assembly depends on the worker matrix succeeding; successful module artifacts cannot independently advance past a failed sibling. Worker completion is also distinct from reviewed canonical acceptance. Selection filters must not turn omitted modules into blocked work. | TASK-007, TASK-022, TASK-024, TASK-027 |
| Normalization and assessment | [`Normalize`](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/internal/iaclens/normalize.go) retains a symbol count but not typed provider requirements or symbol bindings. The current reports therefore cannot establish zero provider usage or broad assessment coverage from missing facts; the local unknown/unchecked counts are measurements, not explanations. | TASK-008, TASK-016, TASK-017, TASK-018, TASK-040 |
| Facts schema versioning | Frozen [`factory-data-v2`](../schemas/factory-data-v2.schema.json) remains strict and unchanged. New [`factory-facts-v3`](../schemas/factory-facts-v3.schema.json) provides typed provider constraints and multi-provider child mappings; consumers must select the matching facts schema version and preserve v2 historical reads. | TASK-017, TASK-018, TASK-019, TASK-021 |
| Projection | At baseline, [`client.go`](../internal/github/client.go) could skip from an Issue summary without verifying Project fields/labels. TASK-010 now writes a bot-owned acknowledgement only after all per-module surfaces succeed and includes the published commit in summary/field sync; external drift audits and durable repair intents remain Projection v2 work. Current projection paths still omit blocked/retired modules. Run F-013 separately records the observed rate-limit incident. | TASK-010, TASK-026, TASK-027 |
| Deployment identity | [`project-pages-complete.yml`](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/.github/workflows/project-pages-complete.yml) takes `workflow_run.head_sha` as the published commit instead of validating the deployment's accepted-commit receipt. F-001 records the verified deployed receipt and Issue #37 counterexample. | TASK-009, TASK-025, TASK-028 |
| Portal coverage | [`site/catalog.go`](https://github.com/softwareone-public/avm-intelligence/blob/12d42dcfbd9c2f413435f203c98ef5a50ef5a173/internal/site/catalog.go) builds from candidate reports rather than the complete discovered catalog, so catalog-only, blocked, and retired modules and the required history/provider/activity views are not represented by the current site. | TASK-034, TASK-035, TASK-036 |

## Evidence Status

### Module Layout Manual Population

On 2026-10-06, Ledermayer requested Common Layout/TASK-038 implementation through
merge and required the next user-dispatched manual discovery to establish the
new module-owned operational layout. `factory-state` must remain App-only with
no manual PR/merge reviews; protected-main code PRs remain separate.

Source inspection identified two migration hazards: state event digests include
logical paths, and the previous Git tree commit overlaid a base tree without
deleting removed paths. The implementation preserves logical event identities
and rebuilds the complete validated expected-head tree without an overlay;
unchanged cached objects are referenced by SHA instead of resent as contents.
Module/global physical ownership is contract-bound and persisted. Legacy event
hashes remain in a compatibility ledger; new module events and fence metadata
are local to the owning transaction. Mixed layouts, unknown digests, duplicate
owners, missing transaction metadata and undefined record kinds fail closed.

Read-only inspection of `factory-state` head
`68cb75aae4205ca641080c8780984438ea183235` found 651 JSON blobs, all below
`state/`, with no operational provider/health records requiring unresolved
ownership. The downloaded exact-head snapshot was migrated only in memory:
all 650 record payloads remained byte-identical, 232 module owners mapped,
968 replay digests and the existing fence were retained, and two serializations
matched. This is actual-state compatibility evidence, not a live migration.
Effective state rules reported deletion/non-fast-forward/update restrictions,
not a pull-request review rule. Main requires a PR and the `Test` check with
zero required approving reviews. No repository permissions/rules were changed.

Focused local fixtures pass for old/new reads, persisted profile validation,
historical replay, stale fences, lost migration acknowledgement, inverse
migration preserving new writes, unsafe checkout rejection, REST/cache snapshot
equivalence, live head checks, declared aggregate conflicts and migrated ordered
manual cycles without duplicate work. Manual workflow contracts preserve scope,
force semantics, action pins, credential isolation, environments and no-dispatch
boundaries. The first configured manual run converts the whole state layout
atomically, then performs only the selected module work. Physical relocation
is distinct from generation refresh; use force only for intended reprocessing.

Contract review caught a nested-context assumption before merge. The resolver
now preserves provider/major context segments and validates payload/path
agreement. Regressions cover `azurerm/4`, `azurerm/5`, `azapi/2`, full provider
addresses and distinct accepted selection/index paths. The final source review
reported no blocking findings. Repository formatting, dependency verification,
full Go tests, vet, JSON dimension listing, relevant state/discovery/queue/GitHub/
CLI race tests and whitespace checks pass locally. These results do not certify
actual Actions checkout timing or effective App write success.

Remaining TASK-038 work includes full delta integration beyond singleton
discovery, selected-aggregate-only loading, accepted-data relocation and exact
dataset/proposal regeneration, typed provider/core operational ownership,
controlled checkout-plus-read benchmarks and bounded live verification. No
workflow was dispatched, state branch written, provider collected or deployment
performed during local verification. TASK-038/043 and rollout gates stay open.

**First live attempt (2026-10-06):** user-dispatched
[run 37498519069](https://github.com/softwareone-public/avm-intelligence/actions/runs/37498519069/job/112389209182)
at merged `447eef7` loaded all 651 state blobs from the verified checkout with
zero blob downloads; snapshot loading completed in approximately 696 ms.
Migration reached `expected_head_commit` at 16:48:26.358Z, then failed at
16:48:35.285Z after approximately 8.927 seconds. No module cycle started.
Read-only inspection afterwards found the same state head
`68cb75aae4205ca641080c8780984438ea183235`, the original 651 files and no
`state/layout.json` marker. The state transition did not commit; detached Git
object creation cannot be determined from these logs.

The CLI's generic sanitized error erased the failing API operation and status.
The precise tree/commit/ref failure and upstream cause are therefore unknown,
not evidence of a permission, ruleset, rate-limit or tree-validation problem.
The local diagnostic correction adds typed allowlisted operation/status/reason
and validated GitHub request IDs through error wrapping, plus truthful write
stage starts/completions. Transport, response-read and response-decode failures
are distinct; arbitrary response bodies, headers, tokens and URLs stay out of
logs. Focused backend/CLI privacy and cancellation regressions pass. No write
retry, permission change or discovery rerun is introduced. After reviewed merge,
Ledermayer's next same-scope manual run is needed to observe the actual API
failure or successful migration. Live cutover and TASK-038 remain open.

**Successful bounded retry (2026-10-06):** user-dispatched
[run 37500289235](https://github.com/softwareone-public/avm-intelligence/actions/runs/37500289235/job/112395234779)
at `f7a0739` completed successfully. Its log records migration of 650 logical
records across 232 module owners at
`c989e33a044d8586688e55c9e0cd4edda16211dc`, followed by one completed cycle for
`avm-res-resources-resourcegroup`, position 1/1. Final durable head is
`983043712cb963a1903989e0f48844c47068e0d6`; the command reported generation 1,
zero changed lifecycle projection targets and one completed cycle. Read-only
tree inspection found 886 blobs, `state/layout.json`, 232 module transaction
files and no legacy `state/transaction.json`. This verifies bounded live
migration/cycle completion, not every preservation/performance/rollout gate or
the cause of the earlier failure. No worker/acceptance/deployment was
started by the logging correction, and broader TASK-038 gates remain open.

Ledermayer then requested earlier module identity in logs. A central slog
adapter now orders top-level fields `time`, `level`, `module_id`, `msg`, then
remaining attributes in text and JSON. It preserves standard escaping, grouped
attributes, UTC/filtering/stdout isolation and module-free output; inherited
and direct module IDs both move without losing values. Focused race-tested
fixtures cover field order, byte-identical module-free standard encoding,
grouped values and concurrent sibling/parent identity isolation. This local
formatting change does not alter persisted evidence or trigger live work.
Review additionally caught and corrected mutable bound-value formatting,
record-level inline LogValuer promotion, empty-attribute omission and nil/panic
marshaler recovery before merge. Tests compare the adapter's bound snapshots,
quoted bytes and grouped/module-free output byte-for-byte against standard Go
handlers; LogValuers and bound Stringer/Marshaler values resolve once. Repository
Go/vet/format/dependency/output checks and logging/progress race tests pass.

**Full-tree 422 and bounded writer correction (2026-10-06):** user-dispatched
[run 37504086545](https://github.com/softwareone-public/avm-intelligence/actions/runs/37504086545/job/112408199312)
at `87a25f9` failed while creating a tree with 889 files. The API returned HTTP
422, request ID `B042:33AB2B:241AA:30A9D:6AC53062`, after approximately 9.7
seconds in that call. No commit/ref completion was logged. Read-only inspection
confirmed durable head `6792d2a119202c0452604827c581462fe42c5d9f` remained.
This identifies the failed endpoint, not the exact upstream validation or
service-limit cause; the logs do not justify a permissions change.

Source inspection found every transition rebuilt and submitted the complete
root tree, even for one new observation. The local correction compares the
complete validated snapshot with the exact immutable parent tree, submits only
changed/additional paths through `base_tree`, and explicitly sends `sha: null`
for every removed file. Unchanged objects are not resent. This supersedes the
earlier no-overlay strategy while retaining its deletion/preservation guarantee;
an overlay without explicit removals is still unsafe. Full-snapshot semantics,
expected-parent commits, non-force ref updates, identities and fencing remain.

Offline audit against the actual 888-file tree at `6792d2a` produced zero entries
for an unchanged rebuild and three entries for a synthetic new observation
(observation, layout manifest and transaction metadata). Fixtures cover a
1,001-file base with a four-entry add/change/delete transition, unrelated byte
preservation, immutable object reuse, unsafe/truncated/missing parent manifests,
missing/null result SHAs and ref races. Review caught and fixed missing-result
SHA fallback and missing-parent-manifest gaps before merge. No ref/commit write
is allowed after incomplete tree responses. Safe 422 diagnostics now retain
only allowlisted validation resource/field/code enums, never messages/values.
The correction reduces observed payload pressure but is not proof that the
specific GitHub rejection is resolved; verify the next user-dispatched exact
scope after merge. No discovery or state operation was run by this correction.

**Live bounded writer verification (2026-10-06):** user-dispatched
[run 37507296743](https://github.com/softwareone-public/avm-intelligence/actions/runs/37507296743/job/112419086071)
at merged `af7e597` completed successfully. All ten selected module cycles
completed, and the terminal summary reported generation 1, no new catalog
proposal, zero changed lifecycle projection targets, `completed_cycles=10` and
duration `4m22.781120661s`. Final state head
`5e1ad4be2ceb11a2506f1fa5a0715736a48e63c0` matches the live branch ref checked
read-only afterwards. Logs show bounded changed-entry tree creation followed
by successful commit/ref updates throughout the selected scope; no 422 failure
occurred in this run. Module IDs appear between severity and message as requested.
This is live confirmation of the corrected transport and ten discovery/delivery
cycles, not retrospective proof of the earlier GitHub rejection's cause, the
formal checkout-performance gate, or completion of native worker acceptance,
selection/publication and TASK-028/TASK-038/TASK-043 rollout requirements. No
workflow was dispatched or remote state changed by this read-only verification.

### Manual Module List Rejected Without Categories

On 2026-10-06, [manual-discovery run 37458406590](https://github.com/softwareone-public/avm-intelligence/actions/runs/37458406590/job/112251590545)
at `4483dde` failed in scope validation with "Select at least one AVM module
type." Ledermayer supplied ten resource module IDs with all category checkboxes
unchecked. The run stopped before token creation, catalog discovery and state
writes. Source inspection confirmed a second category requirement in CLI
validation and the shared selector; lists previously narrowed checked types.

Ledermayer approved exact-list precedence regardless of checkbox values. The
local correction applies to manual discovery and the shared manual projection
recovery path; autonomous workflows and force/dispatch semantics are unchanged.
The discovery force label now states that it queues source reprocessing even
when unchanged, rather than implying a metadata-only refresh. Local regressions
cover names-only and mixed-category lists, all eight checkbox combinations,
whitespace-only lists, invalid targets and empty scope. Reviewed merge and
bounded live verification remain under TASK-038/TASK-043; no new live run or
remote state change is claimed.

### Workflow Families: Source-Proven Coupling And Local Migration

On 2026-10-06, source inspection found that discovery's exact manual ID list
served both as selection and forced refresh. Projection recovery advanced an
existing delivery generation instead of retaining its acknowledgement, lease,
review and retry state. Worker receipt/recovery paths recognized one workflow
filename; a rename-only migration could reject historical receipts or miss an
unbound historical run. These are source-proven risks, not new live incidents.

Ledermayer approved the factory/manual naming split, meaningful manual-input
exceptions, and manual-first promotion. Local implementation separates selected
source resolution from forced refresh, preserves complete catalog observations,
and replays only acknowledged projection delivery without resetting ownership,
review or backoff. Factory discovery retains its nightly schedule and scope.
Exact historical/new worker and provider producer paths remain recognized;
failed recovery searches cannot establish absence. Versioned artifact names,
durable identities and required job names are unchanged.

Focused fixture checks pass for unchanged/forced discovery, canonical CSV/type
selection, all checkbox combinations, replay ownership, workflow family/input
contracts, action pins, shell syntax, exact producer paths, duplicate historical
lookup pages and failed API searches. No workflow was dispatched, remote state
changed, or manual behavior promoted based on local fixtures. Live verification
and promotion remain under TASK-038/TASK-043 and the existing rollout gates.

Contract review caught an empty-intent save in the new recovery orchestration
when all selected delivery records already existed. The local correction keeps
the expected head without saving an empty batch; orchestration fixtures now
cover ordinary existing-delivery no-op and forced replay across acknowledged,
active, retryable and review states. This was caught locally, not in a live run.

Final local gates pass: formatting, `go mod verify`, `go test ./... -count=1`,
`go vet ./...`, dimension listing with valid JSON stdout, race tests for CLI,
discovery and GitHub projection, and whitespace checks. New editor diagnostics
are resolved; older test cognitive-complexity warnings remain unchanged.

### Factory Logging: Source-Proven Gap And Local Implementation

On 2026-10-06, source inspection found plain CLI completion prints without a
shared timestamp/severity policy, and worker processing exposed only its final
artifact summary. JSON-result and token-minting commands also use stdout as a
machine-readable interface. This is a source-proven observability gap under
TASK-038, not evidence of a production execution failure.

Ledermayer approved shared structured logging, configurable severity/format,
module-stage progress and repository guidance. The local implementation uses
standard-library `log/slog`, UTC timestamps, `--log-level`/`--log-format` and
`FACTORY_LOG_LEVEL`/`FACTORY_LOG_FORMAT`, with explicit flags taking precedence.
Operational logs use stdout; JSON/token output modes route logs to stderr.
Worker discovery/extraction, independent dimensions/checks and existing GitHub
retry decisions emit safe structured events. Terminal error rendering avoids
raw parser diagnostics/API bodies; contextual errors remain available to callers.

Focused CLI, worker, assessment and GitHub tests pass. Regressions verify UTC
formatting, filtering, flag/environment precedence, clean JSON stdout, a single
terminal failure, source privacy and byte-identical acquisition artifacts across
debug/info/error logging.
Final local gates pass: formatting, `go mod verify`, `go test ./... -count=1`,
`go vet ./...`, and compiled dimension listing. Race tests pass for CLI, worker,
assessment and GitHub packages; piping dimension stdout through `jq` validates
that stderr progress does not corrupt JSON. New logging-related editor
diagnostics are resolved; the pre-existing cognitive-complexity warning in
`collectProposalWorkflowEvidence` remains untouched. Logging standards are
preserved in the root, CLI, domain and Go instruction guides.
No workflow was dispatched and no live Actions logging outcome is claimed.

#### Discovery And Projection Progress Gap

The shared browser snapshot for [run 37459768909](https://github.com/softwareone-public/avm-intelligence/actions/runs/37459768909/job/112256136918)
at `0ddb11e` showed persistence completing in 4m 4s with only aggregate logs,
then projection at 25m 45s with only its command-start event. The persistence
log reported ten projection targets and state head
`fd29c56535930af48c520e963d4d0db57971ca62`. This is an observed visibility gap;
the snapshot does not prove where time was spent or a final delivery outcome.

Source inspection found sequential per-module board mutations and durable
outcome checkpoints already inside a claimed batch. The batch is not a single
bulk board mutation. Snapshot reads, pagination/checkpointing and individual
Issue/Project operations lacked sufficient INFO progress.

Ledermayer approved guidance and instrumentation first, followed by PR delivery.
The local change adds module/phase boundaries, source/force/request decisions,
state commit and delivery outcomes, pagination/cache/no-op progress, contextual
retries and 30-second waiting messages. It preserves actual ordering, state
events, API writes, lease/fence/retry behavior and human Issue notes. Tests cover
heartbeat cleanup, severity filtering, privacy, failure truthfulness, identical
discovery state and unchanged projection write counts across log levels.
No workflow was dispatched or cancelled to verify logging. Per-module
discovery/persistence/projection orchestration, RES/UTL/PTN ordering, bounded
legacy-description migration and idempotent evidence comments remain a separate
design/implementation follow-up under TASK-038/TASK-043.

Contract review caught a new WARN path that could log raw upstream source
diagnostics and a pagination checkpoint callback that lost active module context.
Both were corrected locally: failure reasons are allowlisted for telemetry,
and the checkpoint callback receives the current operation context. Fixtures
verify sentinel API messages stay out of logs and checkpoints retain module
identity. The pre-existing accepted-time source-pin assignment is unchanged.

Ledermayer additionally required manual discovery and every follow-on module
operation to stay within requested scope, and factory discovery to target only
new catalog IDs. Source inspection found unrestricted nightly source resolution
and targeted discovery retirement intents outside selection. The correction
adds explicit new-ID-only factory persistence, keeps empty scope restricted,
and suppresses out-of-scope retirement intents while retaining complete catalog
bookkeeping. Fixtures verify zero source requests for empty scope, one added-ID
scope with unchanged prior module records, manual/factory unselected retirement
preservation, and exact workflow projection-result propagation. Existing-ID
new-release monitoring and blocked-source retry no longer occur through
new-ID-only factory discovery; explicit manual selection remains available.
This scope correction was requested separately from the deferred ordering and
per-module orchestration redesign. No live run or recovery is claimed.

#### Manual Module Cycle Correction

Ledermayer clarified and approved the missing orchestration change on
2026-10-06: source resolution, module state commit and Issue/Project
acknowledgement must complete for one selected module before the next starts.
The logging/scope PR had intentionally retained the fleet-wide transaction and
later projection step; logging alone did not implement this requirement.

The local manual-only correction records shared catalog observation once,
orders RES/UTL/PTN and canonical IDs, and uses singleton state transitions with
a CLI-owned delivery barrier. Protected/retry/review delivery cannot be reset
by new discovery intent. Failed delivery leaves prior acknowledgements and the
target's committed pending work intact and stops later source requests. The
existing factory new-ID-only path is unchanged.

State reads previously refetched every blob. The manual run now caches immutable
blob contents by SHA while still reading current refs/trees and checking every
commit's expected head. Project metadata and reduced budgets are reused across
cycles. Per-module source pins can produce several catalog outboxes for one
generation; an atomically committed current reference resolves the exact latest
immutable outbox, retaining earlier candidates and fail-closed legacy ambiguity.

Focused fixtures verify commit-before-delivery, stop-before-next-module,
RES/UTL/PTN ordering, real fenced claims/acknowledgements, nonduplicating ordinary
retry, protected and acknowledged delivery, stale-head rejection, live-head
checks with blob reuse, and exact/dangling catalog reference handling. No live
run was dispatched, cancelled or repaired for this change; live verification
and autonomous promotion remain under TASK-038/TASK-043.

The proposed module-owned folder layout remains a separate design question.
Existing state paths already identify many module records, but all writers
advance one branch head and update transaction metadata. Different folders do
not eliminate these expected-head conflicts. No path/identity migration occurred.

Contract review caught two local retry/export risks: an unchanged retry could
acknowledge delivery for an older source after newer discovery had committed,
and a retained non-cycle writer could leave the current catalog reference stale.
The correction binds delivery to the durable discovery fingerprint/source,
including ordinary retries, and atomically advances the catalog reference for
every outbox writer. Fixtures exercise older acknowledged/retryable delivery
against newer discovery and cycle-to-non-cycle pin updates within one catalog
generation, retaining prior outboxes. These were caught locally, not live.

Ledermayer also proposed one configurable layout contract for both accepted
`data/` and operational `state/`. Existing canonical data paths are centralized
in `internal/identity/paths.go`, while operational paths/allowlists are separate.
A future shared versioned contract should cover writers, readers, validators,
indexes and exports; share module/provider identity taxonomy but preserve
distinct evidence/control record layouts and full registry/namespace/provider
identity with immutable version/snapshot addressing. Configuration changes
cannot silently move historical records or alter frozen contracts; explicit
compatibility and migration gates remain required. No layout config was added.

#### Module Nine Local Budget Stop And Missing Run Evidence

[Manual discovery run 37475542630](https://github.com/softwareone-public/avm-intelligence/actions/runs/37475542630/job/112309875464)
at `de9160f` acknowledged eight modules. Module nine,
`avm-res-network-networkinterface`, committed discovery and updated Issue #42,
then stopped before the Project Domain update at 14:09:55 UTC. Delivery at state
commit `ebd8d5f49f740c369d2b90d1ca65555e0075a033` remained retryable; module ten
never started. Its budget checkpoint recorded 61 GraphQL, 17 REST and 2 search
calls, with secondary remaining 20 equal to the configured reserve. Primary
remaining was GraphQL 9939 and REST 14553. This proves a local guard, not an
observed GitHub rejection for that call.

The code labelled this `rate_limited`, lost its typed cause and emitted generic
input advice. The projector had no per-run Issue evidence writer. Ledermayer
approved distinct typed local/GitHub diagnostics, scoped request WARNs and
module/phase/run-attempt comments. Local implementation preserves safe cause,
overall position/completed/retry context and queues App-owned evidence before
mutation. Content-validated comment receipts are required for fenced delivery
acknowledgement. Failed writes and lost checkpoints retain pending work.
Existing budgets are unchanged; additional comment calls consume that quota.

Fixtures cover local zero-call stops versus server 429 warnings, privacy and
typed propagation, new run/phase/attempt identity, same-attempt updates,
pagination/ambiguous markers, protected ownership, comment failure after board
success, lost acknowledgement without duplicate Issues/comments and stale
receipts. Discovery/projection recovery are currently wired; other lifecycle
projections remain under F-019/TASK-038. No live comment or recovery is claimed.
Budget pacing/replenishment and live verification remain separate follow-ups.

**Larger manual scopes (2026-10-06):** user-dispatched
[run 37511917312](https://github.com/softwareone-public/avm-intelligence/actions/runs/37511917312)
selected 232 modules, completed five, then stopped at module six
`avm-res-app-containerapp` during Project Status update. Its guard reported
GraphQL remaining 9943, reserve 100, secondary remaining/reserve 20/20.
[Run 37518913969](https://github.com/softwareone-public/avm-intelligence/actions/runs/37518913969)
selected 162 resource modules, completed eleven, then stopped at module twelve
`avm-res-avs-privatecloud` during owned-label synchronization. Its guard reported
REST remaining 14236, reserve 500, secondary remaining/reserve 20/20. Both
terminal causes were local, not server-directed rate-limit rejection. Earlier
source WARNs described modules without a published stable release; those module
cycles still completed with Blocked status. Later incomplete-operation WARNs
propagated the single delivery-budget stop through nested stages.

Source inspection confirms `SecondaryAllowance=100` and `SecondaryReserve=20`
are a run-local request counter, not GitHub's secondary quota or a time window.
The initial `/rate_limit` lookup consumes one local allowance; up to 79 further
projection requests (including pagination/retries) are admitted before stopping.
The counter is shared across singleton cycles and never replenishes with time;
the REST/GraphQL/Search primary reserves are fixed at 500/100/20. This is a
bounded stop/checkpoint mechanism, not adaptive request pacing. The existing
TASK-038 budget-pacing follow-up should cover time-based weighted secondary
accounting, primary reset/reserve handling, shared installation traffic and
durable continuation; merely increasing the constant is not equivalent. No
runtime, quota, permission or scheduling change was made by this assessment.

**Approved manual pacing implementation (2026-10-06):** Ledermayer requested
implementation. The local manual-only path replaces the fixed projection-call
allowance with shared weighted rolling App admission for state/projection,
one-second mutation spacing, actual primary quota/reset refresh and server
backoff. Defaults retain 20% primary headroom and conservative secondary/content
margins. Long waits return typed deadlines. A fenced global discovery-cycle
record retains exact scope, completed prefix and atomic pending-delivery
position. Resume skips already-committed catalog/source work, including force.
The summary distinguishes persisted completion, deferral and stopped recovery.

Review caught and repaired reset refresh, cleanup isolation, bounded request/
module/orchestration lifetimes, exact retry deadlines, secondary 403 typing,
post-wait ownership checks, frozen-scope validation and false final completion
reporting. Fixtures cover over 80 real projection reads, rolling replenishment,
weighted spacing, reset crossing, non-bypassable server pauses, zero writes
after fence rejection, forced-source resume, corrupt scope, failed parking
retaining its cause and executable manual guards. No live run or autonomous
promotion occurred. Cross-process secondary traffic is handled conservatively
through headroom/server feedback, not a globally measured quota. TASK-038 and
live pacing gates remain open.

**First live paced scope and early continuation (2026-10-07):**
[run 37557331436](https://github.com/softwareone-public/avm-intelligence/actions/runs/37557331436)
at `1ac17d1` completed 17 of 162 resource-module cycles, then deferred at
`avm-res-cache-redis`. It returned exact cycle
`discovery-cycle-6808b8a0ec29e17b33b37a5d354a5c5432b349a21c357e9d49ad060def438ac6`
and retry time `2026-10-07T02:29:49.214Z`, with 145 modules remaining.
[Continuation run 37558545407](https://github.com/softwareone-public/avm-intelligence/actions/runs/37558545407)
returned at `01:44:29.922Z`, before that deadline, preserving the same completed
count and retry time. Both workflow conclusions were success, but neither
represented completed fleet discovery. The early resume is a documented guard,
not evidence that the remaining modules were lost or replayed.

The terminal projection pause was local. The pacer conservatively charges all
mutations, including tree/commit/ref writes, against 400 content mutations/hour
with 12 reserved cleanup slots. This model creates hour-scale waits; it is not
proof that GitHub had rejected 388 requests or that every Git operation consumes
GitHub's general content-generation allowance identically. The logs' default
zero primary counters on a paced failure are not measured remaining quota.
Ledermayer reported dissatisfaction with manual resumption. Proposed follow-up
options are fewer safe state/board mutations plus automatic exact-ID resumption,
longer bounded jobs with token renewal, or separately approved decoupling of
catalog discovery and board flushing. No option, schedule, permission change,
quota relaxation or rerun is authorized by this analysis. TASK-038 remains open.

**Unchanged Backlog re-delivery correction (2026-10-07):** Ledermayer reported
repeated work in user-started run `37559848326` with force off and required that
unnecessary module work be skipped. While the run remained in progress, CLI
logs were unavailable; the precise live module outcomes were not inferred.
Source inspection showed unchanged acquisition is already coalesced/skipped,
but `moduleCycleProjector.deliver` queued a new run-evidence comment before its
same-run no-op check. A fixture with an acknowledged Backlog projection from
run `1-1` and current run `2-1` failed: it advanced durable state and attempted
token creation for unnecessary delivery.

The local correction checks the existing current discovery binding, projected
delivery state, exact acknowledged fingerprint and empty pending evidence before
queuing any run evidence. An unchanged acknowledged module now returns without
token/network or delivery-state writes across fresh runs. Changed source/force,
obsolete bindings, stale acknowledgements, unfinished/retry/protected delivery
and pending evidence retain their required processing/recovery path. Public
release checks and shared catalog/cursor bookkeeping remain necessary discovery
operations; existing Backlog is not itself evidence that newer releases cannot
exist. Focused cross-run and force/recovery/continuation fixtures pass. This
updates the earlier per-run evidence requirement only for true no-op modules;
actual work still retains App-owned per-run evidence. No live job was cancelled,
dispatched or changed by this correction; reviewed merge and live visibility
remain separate gates.

**TASK-038 automatic wait and batched skips (2026-10-07):** Ledermayer approved
the Recommended Design and PR delivery after UTL run `37566005913` skipped six
modules and RES run `37566269078` deferred with 30 checked, 29 completed,
19 skipped and zero source changes. Logs show ten completed delivery cycles
and a local pacing stop while posting evidence for
`avm-res-compute-proximityplacementgroup`, retry at `04:20:51.877Z`.
Those counts are different stages, not contradictory; the WARN omitted detail
and the paced error incorrectly displayed unavailable legacy counters as zero.

Local implementation unifies progress counters and adds completed deliveries,
identifies controlling pacing causes, batches only read-only skips (sixteen or
one minute, flushing before mutations), and checks the live head before reuse
of an isolated decoded snapshot. Per-resolution tag reuse avoids duplicate
checks without hiding moved tags. One approved job automatically waits/resumes
exact durable IDs across bounded segments; state tokens refresh and local
rolling costs survive segments. The six-hour job/350-minute loop still have a
finite bound; cancellation and unsafe results stop, with durable work preserved.
No self-dispatch/worker launch, fresh scope or environment bypass was added.
Offline fixtures cover automatic wait/resume/error/deadline, cache isolation,
head movement, mixed skip/delivery flush, missing cold resume and hourly cost
restoration. No live run was started to validate these changes.
Full tests, vet, module verification, formatting, dimension listing and race
checks across touched domain/CLI packages pass. Executable workflow fixtures
cover safe automatic continuation without remote calls. Existing environment
editor validation remains unchanged; local fixtures are not live acceptance.

**TASK-038 committed head lost on failed evidence queue (2026-10-07):**
[run 37578969689](https://github.com/softwareone-public/avm-intelligence/actions/runs/37578969689/job/112654094172)
at `1a366c4` completed 67 modules and stopped at
`avm-res-hybridcontainerservice-provisionedclusterinstance`. Intent writes
advanced state to `74689cfe1d4689ca23d792b0422baaa9b3cefa25`. The next evidence
transaction created its tree but deferred before commit creation. Read-only
inspection found the cursor still running, next 67, pending delivery true.
Parking reloaded all 1,170 blobs from cache but failed before its commit began.

Source inspection identified a separate bug: `deliver` assigned both head and
error from the failed evidence transaction, replacing the last successfully
committed head with an empty value. The cycle then parked against its earlier
head and failed expected-head validation. The joined error's pacing message
obscured that checkpoint failure. Ledermayer approved preserving the known
committed head until evidence saving succeeds and introducing typed parking
diagnostics. Local regressions reproduce successful intent save followed by
evidence deferral and verify retained head, original cause, no remote projection
and private-body-free parking diagnostics. Existing reserve and fencing rules
are unchanged; no live continuation was resumed or edited.
Full tests, vet, module verification, formatting, dimension listing and focused
discovery/CLI race checks passed. Existing large-function complexity warnings
remain; the new parking logger has no editor diagnostics. Local verification
does not establish live recovery or full-scope completion.

**TASK-038 failed parking checkpoint (2026-10-07):**
[run 37573787391](https://github.com/softwareone-public/avm-intelligence/actions/runs/37573787391/job/112638045236)
at `54cc9cc` stopped at module 56, `avm-res-documentdb-databaseaccount`.
An ordinary state write deferred locally, then parking also deferred at
`create_tree`. Exact-head read-only inspection of `35b3dc245b074e8f50aa560f656a95a9f186aa19`
found continuation `discovery-cycle-5a4c244838ec24c44f56925230ff558a011f5b67bc7b1e05285d7be7ac0eac39`
still running, cursor 55, pending delivery true and lease expiry `05:47:10Z`.
The 55 completed modules were durable; the final deferral was not saved.
Available logs established a local delay but not its exact pacing constraint.

Ledermayer approved reserving final parking capacity separately from routine
checkpoints. Local implementation reserves three mutation slots, 23 minute
points and 11 primary calls for cursor reads and tree/commit/ref writes; normal
checkpoints cannot consume them. Final parking waits locally for at most five
minutes under parent cancellation/deadline; server pauses, actual exhaustion,
rolling limits and expected-head checks remain enforced. `deferral_saved`
distinguishes a saved deferred cursor from an existing continuation, and the
workflow refuses unsaved deferrals. Fixtures reproduce checkpoint rejection
followed by successful parking, local replenishment, server/cancellation guards
and truthful failed-parking flags. No live continuation was resumed or edited.
Full tests, vet, formatting, module verification, dimension listing and focused
race checks passed. Executable workflow fixtures reject unsaved deferrals.
These local checks do not establish live recovery or full-scope completion.

**TASK-038 five-minute local rechecks (2026-10-07):** Ledermayer approved
implementing and merging local retry revalidation after run `37570334741`
paused again at `avm-res-dataprotection-resourceguard` with 43 completed,
31 skipped, 12 delivery completions and retry deadline `05:12:58.352984347Z`.
Live logs showed error class `budget`; the exact request-level constraint was
not visible and no GitHub rejection was established.

The local correction caps newly classified local rolling/reserve delays at a
five-minute probe, not at guaranteed admission. Durable delivery stores the
reason and rejected request cost; paced retries refresh live quotas and check
retained costs before claim/comment mutations. Still-blocked probes do not
charge the rejected request; pending source work is not replayed. Active server
pauses, actual exhausted primary quotas and historical unclassified deadlines
retain protection. Equivalent generations preserve retry metadata and success
clears it. Waiting logs include the durable reason. Quota allowances are not
raised and no running workflow or durable branch record was edited.
Full tests, vet, formatting, module verification, dimension listing and focused
domain/CLI race checks passed. Offline quota transport tests verify local
rechecks stop before claims and server retries are not probed early. Existing
large-function complexity diagnostics remain; fixtures do not certify live
throughput or shorten historical unclassified deadlines.

**TASK-038 inherited retry observed after merge (2026-10-07):** PR #241 merged
as `a8916af`. Ledermayer reported the six-UTL skip test in
[run 37570064588](https://github.com/softwareone-public/avm-intelligence/actions/runs/37570064588)
was fast. Read-only live logs from
[run 37570334741](https://github.com/softwareone-public/avm-intelligence/actions/runs/37570334741/job/112627289716)
showed 29 completed/skipped, 30 release-checked, no changed sources and no
completed deliveries. At module 30, `avm-res-compute-proximityplacementgroup`,
source was unchanged and no lifecycle events were prepared. Its pending
delivery retained the previous run's retry deadline `04:20:51.87740364Z`;
the job automatically waited under continuation
`discovery-cycle-2a59d3421a4b2ced9a354d717c00341ff3b70d7a43a012b152e90d2804450563`.
This was inherited backoff, not observed exhaustion of this run's fresh quota.
Ledermayer subsequently reported the workflow proceeded and requested monitoring
before any pacing adjustment. This establishes reported progress past the wait,
not verified full-scope completion or product acceptance. No policy/state change
was made during this inspection or documentation update.

**TASK-038 minimal unchanged cycle (2026-10-07):**
[run 37561438122](https://github.com/softwareone-public/avm-intelligence/actions/runs/37561438122)
at `b6ed1b0` selected 162 modules with force off and reported deferred after
17 completed cycles, with retry time `2026-10-07T02:29:49.214269052Z`.
This is a deferred scope, not fleet completion. Browser inspection did not
establish every module's delivery outcome. Source inspection confirmed unchanged
modules still wrote an intermediate pending-delivery checkpoint and reread
projection state even when current delivery was already acknowledged.

Ledermayer approved a paced-only minimal path: check releases as before, then
use that same live snapshot and the existing acknowledgement/binding/ownership
predicates to skip delivery when source/lifecycle events are empty and force is
off. Skip omits the intermediate pending checkpoint, never the per-module
expected-head cursor completion/lease refresh. Concurrent head movement rejects
completion; pending resume, force, new releases, stale bindings and protected
delivery retain the full path. Nonpaced and autonomous callers are unchanged.
Results and the manual summary distinguish checked, skipped, changed and
remaining modules. Focused offline fixtures verify checkpoint counts, release
checks, pending resume, concurrent-head rejection and executable summary output.
These are local results; no workflow dispatch or live state mutation was made.
Full `go test ./... -count=1`, `go vet ./...`, `go mod verify`, formatting,
dimension listing and focused race checks passed. Workflow fixtures parse YAML
and execute summary shell/jq against complete, deferred and historical results.
Final read-only contract review found no blocking defect. Editor diagnostics
still flag complexity/parameter limits in existing large orchestration functions
and the unchanged `data-publisher` environment; no rollout approval is implied.

**Completed run and protected Redis retry (2026-10-07):**
[run 37559848326](https://github.com/softwareone-public/avm-intelligence/actions/runs/37559848326/job/112594416097)
at `1ac17d1` later completed with failure after 17 cycles. Module 18,
`avm-res-cache-redis`, committed state at
`7dbb6962b11f490e97e1310d9ae724ba074eb96c`, then stopped immediately after
reading projection delivery, before another Issue/Project request. Read-only
inspection of that exact commit found delivery `retryable`, error class `budget`,
one pending evidence item, no lease owner and retry time
`2026-10-07T02:29:49.214269052Z`. The attempted delivery at `02:11:01Z` was
early; the guard correctly protected pending work. The defect was generic
error rendering/failed-job classification of an expected stored wait.

The local correction returns a typed deferred deadline for retryable delivery
that is not yet due. Existing paced continuation parks using that deadline;
it neither clears backoff nor skips the pending evidence. Tests cover exact
deadline propagation, protection before due and allowance at due, alongside
the cross-run no-op correction above. No claim is made that this run performed
new acquisition for unchanged modules or suffered a new API quota rejection.

Contract review caught local pending-evidence/generation acknowledgement,
historical outcome, stale same-attempt receipt, GraphQL-limit typing and combined
checkpoint/API-cause gaps. Repairs retain pending work across equivalent newer
generations, queue phase outcome as pending, deliver the current run's completed
comment last, and preserve earlier pending runs as unacknowledged/incomplete.
Verified comment context allows head-only retry no-ops while changed source or
status reopens the same marker. HTTP-200 GraphQL limits are typed/WARNed, and a
failed checkpoint preserves its API cause and retry time through wrapping.
Focused regressions pass for every review case. These were found locally and
do not establish new live outcomes or increased budget capacity.

The final review also caught HTTP-200 GraphQL limits losing response reset
headers. Typed GraphQL errors now retain the server-directed deadline before
choosing a recovery retry time; a header-bearing regression verifies this.

**TASK-038 owner-claim deferral exited fatal (2026-10-07):**
[run 37581694257](https://github.com/softwareone-public/avm-intelligence/actions/runs/37581694257/job/112662612363)
at `3f1ea67` deferred correctly at 06:48:26Z with 78 completed, 154 remaining,
retry `06:53:22.051Z` and `deferral_saved=true`; the workflow waited and the
next segment minted a token at 06:53:22Z. The resume then failed at 06:53:39Z:
the WARN showed `deferred=false deferral_saved=false continuation_durable=true`
and the terminal ERROR carried `error_type=*errors.joinError`,
`pacing_resource=rest pacing_retry_at=06:58:39.022Z server_limited=false`, so
`persist-discovery` exited 1 and `set -e` ended the retry loop despite the
durable cursor still being a valid saved deferral at head `eeaf18f9…`.

Source inspection showed the parked module-loop path was never reached: on
resume, `ContinueCycle` first claims the cursor (running status, incremented
fence, ten-minute lease) through an ordinary checkpoint write, and the initial
exact-cursor read precedes it. Either can hit the local rolling wall. Those
escapes returned the `DeferredError` raw instead of parking, and the failing
`head, err = saveContinuation(…)` assignment also clobbered the committed head
to an empty string, so even a park attempt there would have failed
expected-head validation. The five-minute recheck bound explains the reported
`pacing_retry_at`.

Ledermayer approved making every routine pacing deferral an ordinary durable
wait. The claim save now parks like the module loop (retry deadline never
earlier than the stored one), the initial resume read retries once under the
reserved cleanup budget, and the post-module checkpoint parks partial progress
or re-saves a terminal complete marker through parking rather than deferring
it. When both the claim and its parking retry hit the local wall, the exact
durable cursor is re-read under reserved capacity and only its on-disk saved
deferral or completion is reported as the wait; durability is never attested
from the in-memory record. The CLI
adds a final net: a paced typed deferral that still escapes verifies the exact
durable cursor under reserved capacity and converts a saved deferral or
completion into a normal exit-0 wait, while an unsavable or unverifiable state
keeps the honest failure and the workflow's `deferral_saved` proof remains the
resume gate. Re-claiming a cursor whose modules all completed but whose
terminal marker was lost now closes it without replaying acknowledged work;
that closure relies on the durable invariant that `Next` advances only after
the module's delivery callback returned successfully or a skip check proved
current acknowledged delivery.
Fixtures cover claim-park, double-wall proof, checkpoint-progress park, complete-
marker survival, unmarked completion closure, proof rejection of running or
missing cursors, and farthest-deadline extraction. No continuation, state
branch, workflow input, quota allowance or parking reserve was edited, and no
live run was dispatched.
Full tests, vet, formatting, module verification, dimension listing and focused
discovery/CLI race checks passed locally. Read-only contract review then caught
the double-wall branch initially attesting durability from the in-memory cursor;
it now re-reads the exact record under the reserved cleanup budget and reports
only on-disk facts (proof failures keep the honest joined error). Review
observations about parking-reserve consumption and closure fencing were
re-checked against the write paths: park/checkpoint contexts derive from the
parent orchestration context, at most one cursor mutation per park occurs, the
proof runs before the deferred pacing-session save, and `Next`/pending-delivery
advance only within transactions that already commit delivery or a proven
acknowledged skip, so no bypass was found. Existing editor complexity warnings
remain and grow slightly in `ContinueCycle`; no rollout or live acceptance is
implied. This does not mark TASK-038 accepted;
live verification requires the next separately authorized paced run to observe
wait-instead-of-fail at a claim boundary.

Source-proven gaps above are not incident reports. F-001 records a receipt-verified
deployment plus a live board mismatch. F-003 through F-013 retain the historical
run evidence and their current resolution status; a resolved implementation fix
does not erase the event, and workflow success alone does not prove a data or
projection postcondition.

## F-001: Deployment Receipt Matches; Board Reconciliation Does Not

Status: deployment verified; historical first-cohort mismatch confirmed; receipt-gated correction implemented but not yet deployed

The manual Pages workflow verifies an exact accepted commit, builds the
evidence-first site from accepted `data/`, and refuses to continue if the data
tree or generated output is missing. The first ten-module cohort is canonical
and has been deployed successfully at
https://ideal-adventure-62gqzpm.pages.github.io/.

The live `publication.json` receipt names accepted commit
`efdd161535b8152e686c13c9785098bc0b0d15ec`, data digest
`e82ea6516cf020a61a7149168d29b62b3dd9b757face0f7a92497d7a175fb0ad`, site
digest `202ef677c1f7f2c8060bfe8638554e560696598c2a5ecf9c54200174a2a3c48e`, and
producer run `36874979207`. Rebuilding from that exact commit reproduced both
digests.

Pages-completion workflow run `36875075805` succeeded from workflow commit
`4bbe923da41e8cc37f254adb62bf4bb7764c6411`, not the deployed accepted commit.
At the time inspected, the Project card and Issue #37 for cohort module
`avm-res-compute-disk` showed `Publishing`; `Outcome` was `Unassessed` and
`Published commit` was empty. That callback is not evidence of board
reconciliation. TASK-009 now requires a run-bound receipt and verifies its
accepted commit, data digest, and complete module membership before any Done
projection. This correction is implemented locally but has not been merged or
used for a new live reconciliation; the historical mismatch remains unresolved
until a new verified receipt projects successfully. TASK-025 owns the richer
generation/membership fencing.

## F-002: Accepted bundle is canonical; publication remains separate

Status: resolved for the first cohort

PR19 validates plan/request/report identity and assembles all successful worker
artifacts into a complete candidate data tree. PR20 atomically bundles that tree
with accepted discovery and durable requests. PR21/PR27 submit accepted data
through the repository's pull-request rule. PR22 adds the static site build and
PR23 adds a manual exact-commit Pages workflow. The standalone
`publish-pages.yml` path uses `data-publisher`, which has no required reviewer;
the separate `pages-publisher` environment is not used by that workflow.
PR28 merged the first ten-module accepted bundle at `efdd161`.

Next action: correct publication identity under TASK-009/TASK-025, then
reconcile blocked and retired module states through the shared lifecycle.

## F-003: Release-less modules aborted fleet resolution

Status: resolved in PR24

The first live fleet attempt failed during source resolution because many
accepted AVM modules have no published stable SemVer release. The resolver
treated that expected module-level condition as a systemic failure, preventing
the planner from marking those modules blocked.

Resolution: release-less modules are now omitted from the pin set so the
planner records them as blocked; API failures, invalid release data, ambiguous
aliases, and moved accepted tags remain fatal. The targeted ten-module run
confirmed the corrected behavior.

## F-004: Candidate acceptance used stale request state

Status: resolved in follow-up fix

The first filtered fleet rerun assembled 144 reports successfully, but
acceptance read the original plan request directory, where those requests were
still `queued`. The terminal `accepted` request files existed inside each
worker artifact but were not overlaid before bundle acceptance.

Resolution: the aggregation job overlays terminal worker request files over the
plan request directory before acceptance. The same fix adds explicit module-type
checkboxes and reduces transient per-worker artifact retention to two days;
plan artifacts retain seven days and accepted bundles retain longer for review.

## F-005: Canonical writer lacked Git credentials

Status: resolved in follow-up fix

The resource-only run reached the canonical writer, but `actions/checkout` had
removed credentials before `git fetch origin main`, producing `could not read
Username for 'https://github.com'`. The writer could not compare or push the
accepted commit.

Resolution: the writer checkout retains the workflow token, and manual runs can
now provide a comma-separated module-ID list to reduce validation and worker
runtime while testing the pipeline.

## F-006: Direct canonical push is blocked by repository rules

Status: resolved in follow-up fix

The targeted ten-module run produced a valid accepted bundle, but the canonical
writer's direct push to `main` was rejected by repository rule `GH013`: changes
must be made through a pull request. Adding an App exception would weaken the
repository's intended review boundary, and the workflow was using the built-in
Actions token rather than the writer App anyway.

Resolution: the writer now pushes a `factory/data-<run-id>` branch and opens a
pull request. Pages deployment is skipped until that pull request is reviewed
and merged; the existing manual Pages workflow remains available afterward.

## F-007: Fleet board projection is partial

Status: partially resolved

The fleet workflow now projects selected actionable requests to one recurring
Issue and Project item per module, including factory phase and AVM type labels.
Canonical data changes now also reconcile accepted reports to Publishing with
outcomes and source identity. A successful Pages deployment now reconciles the
same exact commit to Done. The Project is still not a complete view of fleet
analysis state because blocked and retired transitions are not yet reconciled.

Next action: extend the projection worker to reconcile blocked and retired
states from durable requests and catalog retirement records. Keep the board
non-canonical and preserve human Issue notes.

## F-008: Pages was not enabled for GitHub Actions

Status: resolved in follow-up fix

The first Pages deployment failed in run `36873541817` during
`actions/configure-pages` with `Get Pages site failed: Not Found`. The accepted
data, site build, and publication manifest were not reached as failure causes.

Resolution: both Pages workflows now pass `enablement: true` to the pinned
configure-pages action. If the repository policy still prevents automatic
enablement, configure Settings > Pages > Build and deployment > GitHub Actions
once, then rerun the workflow.

## F-009: Module marker search matched prefixes

Status: resolved in follow-up fix

The all-module projection run failed for `avm-ptn-alz` because GitHub issue
search also returned valid markers such as `avm-ptn-alz-management`. The client
treated substring search results as duplicate exact module Issues.

Resolution: search results are now post-filtered for the complete hidden marker
`<!-- factory-module-id:<module-id> -->`; multiple exact markers still fail
closed and require manual cleanup.

## F-010: Accepted board projection exceeded the job timeout

Status: resolved in follow-up fix

The all-module accepted projection run `36884171956` was cancelled at the
15-minute job limit while sequentially reconciling the full accepted cohort.
The projection command produced no data error; the bound was too small for the
number of GitHub Issue, label, and Project field calls.

Resolution: the accepted projection job now has a 60-minute bound and emits
per-module progress. If the full cohort approaches that bound, shard the
projection into bounded batches rather than weakening projection validation.

## F-011: Full projection retried unchanged Publishing modules

Status: resolved in follow-up fix

Run `36886840571` processed all 145 accepted modules even though most already
had the desired `Publishing` summary. It then stopped on a transient GraphQL
service error while projecting module 8.

Resolution: unchanged `Publishing` and `Done` factory summaries are skipped
even when the current run URL changes. The board writer remains fail-closed for
real validation and permission errors.

## F-012: GitHub REST projection calls can transiently fail

Status: resolved in follow-up fix

Run `36886840571` stopped at module 132 after GitHub returned HTTP 500 during a
projection call. The module data was not invalid; the sequential projection
process lacked REST retry handling.

Resolution: GitHub REST 429 and 5xx responses now retry three times with
backoff. GraphQL transient service errors retain the same retry behavior.

## F-013: Full projection exhausts the GitHub App rate limit

Status: open; redesign proposed

The full accepted-data projection run processed 145 modules with repeated
Issue search, label, Project item, and Project field calls. It exhausted the
GitHub App installation rate limit while projecting
`avm-res-network-networkinterface`. Existing `Publishing` modules were still
being entered into the projection loop, and the projection job had no durable
per-module cursor or API budget.

Decision: stop treating board projection as one monolithic post-acceptance
transaction. Discovery, planning, analysis, acceptance, projection, and Pages
publication will be independent stages. Projection v2 will use one read
snapshot, exact desired fingerprints, bounded per-module batches, rate-limit
reserves, retry/backoff, durable leases/cursors, and partial success. The
design is recorded in [`plan/board-projection-v2.md`](../../plan/board-projection-v2.md).

The Project remains a non-canonical human projection. Rate limits or Project
outages must pause projection without invalidating accepted data or analysis.

## F-015: Targeted discovery projection outlived its lease

Status: resolved; targeted Backlog projection acknowledged

Manual discovery run `37121374133` targeted
`avm-ptn-aca-lza-hosting-environment` at source
`9bb7ab42291a0afdc9842d4c09b5f8f03b4c095d`. It preserved the earlier
`facts_accepted` request and created the forced acquisition request at generation
2. The run did not dispatch a worker.

The bounded projector claimed delivery attempt 1 with a 15-minute lease. It
updated Issue #45's factory summary to `Backlog`, but did not finish the label,
Project-field, and acknowledgement path before the lease expired. The workflow
then failed with `projection intent generation or lease fence is stale`; the
durable delivery record remains `projecting` at fence 1 with the expired lease.
The `factory:status-publishing` label remains. The current token could not read
Project fields, so their final state is unverified. Accepted facts and the
queued acquisition remain intact.

PR #203 merged the lease-renewal fix at `b5f7f5e`; batch and outstanding module
leases now renew atomically at snapshot and completed-module checkpoints. PR
#204 merged the manual, module-scoped projection-only recovery workflow at
`c07da98`. Recovery run `37214602781` claimed generation 3 at attempt 2/fence 2
and updated Issue #45's summary, but exposed an invalid direct selection on the
`ProjectV2FieldConfiguration` union. PR #205 corrected Project item queries to
select field names through `ProjectV2FieldCommon` and merged at `4cc174b`.

Projection-only recovery run `37219371339` then succeeded. The durable delivery
is `projected` at generation 4, attempt 3/fence 3, with an acknowledged
fingerprint; Issue #45 has the factory projection acknowledgement and
`factory:status-backlog` label. The successful delivery checkpoint follows the
Issue, Project-field, and label synchronization path. The current personal
token lacks `read:project`, so the Project fields were not independently read
afterward. The generation-2 acquisition and prior accepted facts remain
unchanged, and no worker was dispatched.

## TASK-002: Read-Only Control Inventory

Status: complete for the requested repository-level inventory as of
2026-10-01. No settings were changed. Organization-level ruleset settings were
not accessible from the current GitHub session. This is unknown, not a blocker
to implementation or repository-level configuration; only an effective rule
that denies the approved state write would block activation.

### Repository Rules

The active `protect-main` ruleset targets only `main` and has an empty bypass
list. It requires a pull request, the `Test` status check, and an up-to-date
branch; it also blocks force pushes, deletions, and merge commits. Stale review
approvals are dismissed after new commits. The configured required-approval
count is zero, so this ruleset does not itself enforce an approving review.
Branch creation and update restrictions are not enabled.

The ruleset therefore does not require a `factory-state` exception or weaken
`main`'s rules to target a separate state branch. Organization-level rules could
not be inspected, which is not evidence that none exist and is not a prerequisite
for implementation. The installed App has repository Contents read/write
access, so a state writer can be tested without adding a `main` bypass. Before
activation, verify the effective repository-level policy and write operation;
if GitHub denies it under an enforced rule, pause and resolve that concrete
conflict. No state branch or writer was created by this inventory.

### Project Fields

Project 1, `AVM Intelligence Factory`, is private. All fields configured in
`config/github.yaml` were found. GitHub's built-in `Status` field is single
select and is addressed by name in the settings UI; its options are Backlog,
Queued, Analyzing, Validating, Publishing, Done, Needs review, Failed, and
Blocked. Custom field IDs and select options are:

| Field | ID | Type or options |
| --- | --- | --- |
| Module ID | `419510286` | Text |
| Domain | `419510288` | Text |
| Source version | `419510289` | Text |
| Source SHA | `419510290` | Text |
| IaCLens version | `419510291` | Text |
| Run URL | `419510292` | Text |
| Published commit | `419510294` | Text |
| Last analyzed | `419510295` | Date |
| Work type | `419510352` | Implementation, Module analysis, Remediation, Operations |
| Module type | `419510353` | res, ptn, utl |
| Outcome | `419510368` | Clean, Findings, Unassessed, Infrastructure error |

### Environments And Delivery Gates

| Environment | Protection observed | Deployment branches |
| --- | --- | --- |
| `data-publisher` | No required reviewer or wait timer; administrator bypass allowed; writer App ID/private-key secrets are configured. Secret values were not accessed. | `main` only |
| `pages-publisher` | Ledermayer is a required reviewer; self-review is not prevented; one-minute wait; administrator bypass is not allowed. | `main` only |
| `github-pages` | No required reviewer or wait timer; administrator bypass allowed. | `main` only |

The `fleet-plan.yml` Pages job uses `pages-publisher`. The separate
`publish-pages.yml` workflow uses `data-publisher` and deploys via GitHub Pages;
the `github-pages` environment itself has no reviewer requirement. This is a
material difference between the two current deployment paths. No environment
or approval setting was changed.

The installed `app-avm-intelligence` permission screenshots supplied by
Ledermayer show repository Contents read/write, Issues read/write, and required
Metadata read-only access, plus organization Projects read/write access. The
projection code's `MintToken` scopes tokens to the `avm-intelligence`
repository and requests only Issues write, Organization Projects write, and
Metadata read; it rejects a token that includes Contents access. The pilot,
fleet, and accepted-data projection paths use this reduced token. The wider
installation grant is therefore not inherited by these projection tokens.

Organization-level branch rules could not be verified: the organization rules
settings page returned 404 in the current session. Treat that as unknown, not
as evidence that no organization rules exist.

### Unattended-Policy Feasibility

Data-only automation can be scoped without adding a `main` bypass: the current
ruleset requires PRs and the `Test` check, while not requiring an approving
review. The App grants and reduced projection-token behavior are known, but
organization-level branch rules remain unknown. The divergent Pages environment
paths also prevent treating unattended publication as consistently gated.
TASK-042 must resolve these conditions through an explicitly approved, narrow
policy; this assessment authorizes no auto-merge, permission change, state
write, collection, or deploy.

## TASK-003: Frozen Baseline Evidence

Status: verified 2026-10-01. These local counts and hashes describe the checked-
out canonical dataset, not the deployed site's module or quality coverage.

### Canonical Data And Local Reproduction

The checked-out `main` and `origin/main` both resolve to
`908ee877c0ff379b5552fa9558f43aa1233d74bb`. The complete discovered and
candidate ID lists remain in `data/discovery.json` and `data/candidate.json` at
that commit. Their file hashes are respectively
`4f79f6ddc041310a91ca8d836ec27262b3a547f7675acf8e1d5059ddfb612ffe` and
`c7c3e809d6e91376656e87f4e0b10b6dfe969e329364bc55232d5d655b472917`.

The baseline has no `data/source-pins/` history. Candidate entries retain source
SHA and fingerprint, while legacy requests retain source SHA/fingerprint but
not the resolved tag and release identity. Exact prior `ResolvedSource` records
cannot be reconstructed from those inputs; no historical tag is inferred. The
TASK-007 workflow seeds prior pins when present and retains newly resolved pins
in accepted data for future moved-tag checks. Tag moves predating that history
remain unverified.

Discovery generation 1 contains 232 active modules; candidate generation 1
contains 145 reports. SHA-256 of the sorted full active-discovery ID set is
`0af0ce8903064db9f0f7d9cf7755317b9c8783096a5a49449017adc6c451d58f`; the
sorted candidate ID set is `974a62d6718d022e6c82554c3e2efc8a2ff5bf2d9e50f1bf6ab6285e2535becf`.
The sorted module-to-report membership digest is
`badba1c10f90f642f9e06c9d11dbddc818ac712b907ed24189e5ef9d2e021021`; SHA-256
of the sorted per-report SHA-256 listing is
`e3edf07cf953ac197f527cbfa7fc6e7cb1b0d5d7b8ba7d3145cc11f7bd704b3e`.

The 145 reports contain 1,012 units: 1,010 unknown classifications and 6,064
checks (990 pass, 24 fail, 5,050 unchecked). A fresh site build from this
canonical `main` produced data digest
`f06dd87700455c2f6c1c85e4ac6ffec4bcf03e6f908eacb595024315052337e5` and site
digest `6c406a6d9b2e71c483844bb3384a0988e4c2a6da94ef54aed2bc4edd46a97125` in
temporary storage; no separate demo snapshot was retained.

### First Cohort And Deployment

PR #28 merged accepted commit `efdd161535b8152e686c13c9785098bc0b0d15ec`.
Its ten module IDs are `avm-res-apimanagement-service`,
`avm-res-authorization-roleassignment`,
`avm-res-compute-capacityreservationgroup`, `avm-res-compute-disk`,
`avm-res-compute-virtualmachine`,
`avm-res-compute-virtualmachinescaleset`,
`avm-res-managedidentity-userassignedidentity`,
`avm-res-network-applicationgateway`, `avm-res-network-networkinterface`, and
`avm-res-network-virtualnetwork`. The publication receipt above was independently
reproduced from this commit with exact data/site digest matches.

The board check is intentionally recorded as a mismatch, not a successful
reconciliation: Project Pages completion run `36875075805` reports success, but
its workflow SHA is `4bbe923da41e8cc37f254adb62bf4bb7764c6411`; Issue #37 and its
Project item remain `Publishing` with no published commit. Preserve this
counterexample for TASK-009, TASK-025, and TASK-028; the historical run status
must not be rewritten as proof of Done.

## F-014: Bare SemVer release produces an invalid source pin

Status: local fix implemented; protected-`main` merge pending

Clean-start discovery run `37003682029` validated the official catalogs but
failed while persisting the discovery transaction for
`avm-ptn-aks-production`. GitHub's latest stable release uses the unprefixed
tag `0.5.0`. `selectStableRelease` accepts that tag through `stableVersion`, but
`ResolveLatest` used `semver.Canonical` directly and emitted an empty version;
the source-pin validator correctly rejected it. The failed state transaction
was atomic: `factory-state` remained at its original empty snapshot.

Resolution prepared locally: both release canonicalization and alias
verification now use `stableVersion`; a resolver regression test confirms the
unprefixed tag produces `v0.5.0`. The focused resolver tests, full Go suite,
`go vet`, and editor diagnostics pass. Do not rerun v2 discovery from `main`
until this fix is merged under the existing protected-`main` process.

## F-016: Legacy Report Removal Leaves Dangling Manifests

Status: resolved locally by offline native rebuild; review/merge pending; no production incident asserted

On 2026-10-06, the local worktree had deletions for all 145 flat legacy reports
under `data/modules/`. Structured reads confirmed that `data/candidate.json`
still references those 145 missing reports and `data/dataset.json` still hashes
the same 145 missing records. The legacy site reader loads each candidate report;
deleting only the reports therefore leaves an invalid legacy candidate and a
dataset manifest that does not describe the tree. These counts describe the
local uncommitted worktree, not an accepted or deployed dataset.

The current `v2_proposal_validation.yml` requires any proposal/data change to be
a data-only PR with an exact typed envelope. Mixing report removal with agent
guides or README changes would fail that contract. No gate was bypassed, data
accepted, state written, or deployment attempted.

Ledermayer requested documentation first, then removal of all v1 artifacts.
TASK-043 owns the follow-up inventory and validated cleanup of legacy candidate
references and dataset digests, preserving native records and receipt identities.
Documentation PR #215 merged at `12d42dc` without report deletion. The removal
inventory and implementation proposal are now recorded in the
[retirement record](../plan/refactor-avm-intelligence-v1-retirement-1.md).

After explicit offline implementation approval on 2026-10-06, the 145 flat
reports, 145 local requests, legacy candidate and discovery records were removed
from the current worktree. The native builder rebuilt indexes twice into fresh
local directories; both outputs were byte-identical. All 237 surviving native
source-record SHA-256 values and all 232 catalog modules were preserved. The
installed dataset has 472 verified record digests and ID
`dataset-bd84ed9b0db3fe69fe0e26786df440e6a6905428a70531466949bd4f30fd8444`.
`TestRepositoryDatasetIsNativeAndRebuildable` now checks the repository tree
against complete native output without hard-coded fleet totals. No workflow was
dispatched, accepted remote tree changed, or state branch written.

## F-017: V1 Retirement Crosses Shared Native Contracts

Status: native contract isolation verified locally; review/merge pending; inventory baseline `12d42dcfbd9c2f413435f203c98ef5a50ef5a173`

The [native worker](../internal/worker/worker.go) accepts `queue.Request` and
validates its `Work`, lease, owner and fence. That type shares
[request.go](../internal/queue/request.go) with legacy local-file operations;
removing the whole file/package would break native work. Native discovery also
uses `catalog.AcceptSnapshot` and `source.Resolver`, and
[FromReducedStatus](../internal/github/reduced_projection.go) depends on the
shared `Projection`/marker/Issue-summary model in the legacy-mixed projection file.

[Publication manifests](../internal/publish/manifest.go) contain both legacy
candidate-based `Create` and native `CreateIndexed`. The current Pages workflow
still invokes the legacy site/manifest CLI, while the accepted baseline has no
native presentation snapshot; replacing one flag is not proof of a usable
native publication path. The six-kind proposal contract also rejects catalog
pointer removal and does not own the flat legacy report paths.

The structured baseline inventory has 292 untyped legacy JSON records (145
reports, 145 requests, candidate and discovery) and 473 typed native records,
including 232 module metadata/index pairs and two facts/provenance records.
There are no accepted analyses, selections, provider snapshots or presentation
snapshots in that baseline. Numeric schema/capability versions are not
architectural-v1 classifications.

TASK-043 owns the [phased retirement proposal](../plan/refactor-avm-intelligence-v1-retirement-1.md):
isolate native contracts/callers first, preserve native fixture semantics,
then use a separately reviewed exact-base maintenance validator and data PR.
The initial workflow-driven maintenance proposal was superseded by Ledermayer's
offline-only direction. Local implementation removed the external analyzer,
producer pins, pilot/fleet commands, file queue, report projection/rendering,
normalized schema/corpus, migration adapter and six old workflow files. Native
request IDs/serialization, claims, fences, sealing, source resolver, tree-copy
utilities, typed proposals, reduced projection and indexed publication remain.
The full offline Go suite passes. The inventory's classification of
`internal/queue/dispatch.go` was corrected: it contains native dispatch and is
retained with its dispatcher tests. Generic helpers formerly housed in migration
and legacy tests were relocated rather than lost. No runtime activation occurred.

Final local gates pass with module networking disabled: formatting, module
verification, full `go test ./... -count=1`, `go vet ./...`, native workflow and
schema tests, and race tests for queue/state/worker/GitHub projection. Native
schema rules/IDs/refs are unchanged; only the obsolete reader-description text
was removed. All 17 remaining Markdown files and 137 local links/anchors passed
validation. Retired names remain only in explicit negative tests and immutable
historical records, not callable producers or current accepted data.

Editor validation found a new router complexity advisory, resolved locally by
isolating token minting; the native router now has no diagnostics. The existing
`collectProposalWorkflowEvidence` test-helper complexity advisory (17 versus
15) was verified byte-for-byte unchanged from HEAD and left outside retirement
scope. It is not a Go compilation/test/vet failure; optional refactoring must
preserve all native trust-boundary assertions.

## F-018: Combined Retirement PR Is Outside The Typed Proposal Gate

Status: source-proven review constraint; effective required-check configuration not reverified offline

[Proposal validation](../.github/workflows/factory-proposal-validation.yml) runs on
`pull_request_target` and checks out trusted event-base code. A `data/` change
activates its six-kind envelope path, which rejects non-data changed paths and
requires exact proposal-owned changes. The local retirement deliberately changes
code, config, workflows, docs and data together; it is not a normal six-kind
producer proposal and will be rejected by that validation path as written.

Changing candidate code or this workflow in the same PR cannot establish trusted
base support for a retirement exception. The historical control inventory required
`Test` and did not establish an active required proposal check; that does not prove
today's effective rules. Do not describe this as a demonstrated repository policy
denial or disable the gate, broaden permissions, create an unattended exception,
or claim merge readiness without resolving the actual review/check requirement.

TASK-043 records this separately from the completed local cleanup. Ledermayer
requested a single finished local change rather than cleanup workflows; no
maintenance envelope/schema/workflow was added, no remote setting changed, and
no PR/workflow was dispatched during the offline implementation.

## F-019: Board Progress And Pages Publication Are Not Wired

Status: source-proven gap (2026-10-06); no live incident claimed

- **Only discovery projects to the board.** The only writer of projection
  intents is the discovery path in
  [discovery_projection.go](../cmd/factory/discovery_projection.go). It is
  called by `factory-discovery`, `manual-discovery` and
  `manual-projection-recovery`, and it emits only Backlog, Blocked, Needs review
  or Retired.
- **Later transitions are invisible.** Dispatch claim, worker bind and seal,
  evidence acceptance and recovery update `factory-state` but write no intents.
  Queued, Analyzing, Validating, Publishing, Done and Failed are therefore never
  projected.
- **The reducer is unused.** `queue.ReduceModuleStatus` has no non-test caller.
- **No Pages path.** No workflow performs assessment wake-up, selection export,
  presentation export or Pages deployment. None writes a deployment receipt.
  Done cannot be reached.

The [factory operations guide](factory-operations.md) shows these steps as
dashed, not-wired steps. The follow-up is owned by TASK-043 (wiring), TASK-034
(presentation) and TASK-028 (live validation). No code, workflow or board state
was changed by this finding.

## F-020: Scheduled Dispatch Does Not Yet Project Or Publish

Status: source-proven boundary (2026-10-07); no live dispatch claimed

- `factory-dispatch` claims only the oldest ready durable request and starts
  the existing manual worker. It does not accept evidence, wake assessment,
  export a selection, or deploy Pages.
- `manual-dispatch` now accepts one exact request ID or one canonical module
  ID. A module claim cannot select another module, but the board status remains
  unchanged because dispatch still writes no projection intent.
- Local queue and workflow-contract tests verify selection and shell syntax.
  They do not prove a scheduled run, claim, worker handoff, or publication.

Live verification belongs to TASK-044. Projection and publication remain
F-019, TASK-043, and TASK-028.

## F-021: First Scheduled Dispatch Reread The Full State Tree

Status: observed incident (2026-10-07), run
[37578553338](https://github.com/softwareone-public/avm-intelligence/actions/runs/37578553338)

- The claim at `69e6e72` read head `35b3dc2` twice. Each read downloaded 1,128
  blobs and took about 3m32s. The second head was unchanged.
- The expected-head commit failed after 407ms without a `create_tree` event or
  classified API diagnostic. No request was claimed and no worker started.
- The local correction commits module-layout claims through the declared module
  aggregate and reuses a verified snapshot after one live-head check. Blob
  contents remain cached across a real head change. Local tests do not prove
  the next scheduled run.

## F-022: Dispatch Still Downloads The Initial State Tree

Status: observed incident (2026-10-07), run
[37583177815](https://github.com/softwareone-public/avm-intelligence/actions/runs/37583177815)

- At `3f1ea67`, the first snapshot read took 3m36s and downloaded 1,066 of
  1,206 blobs. The second read reused 1,204 blobs and completed in 1.5s.
- The claim then failed in about 300ms with an unclassified `*errors.errorString`.
  No state-write event was logged, no request was claimed, and no worker started.
- The follow-up seeds immutable blobs from a trusted `factory-state` checkout
  before the first API read. It does not prove the next live claim because the
  exact unclassified failure remains hidden.
