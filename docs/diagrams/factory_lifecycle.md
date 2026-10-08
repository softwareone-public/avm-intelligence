# Factory Board, Workflows And Branches

[Open the editable swimlane diagram](factory_lifecycle.drawio).

The diagram shows the **designed end-state factory**, including independent
Terraform provider/core metadata collection, receipt reconciliation, assessment,
selection, presentation, publication, projection and recovery. All workflow
nodes are rectangular. Ten existing statuses plus proposed **Acquiring** are shown;
branch lanes remain; no
manual workflow nodes are shown. Existing factory filenames identify current
entry points; descriptive planned-stage labels are not invented workflow names.

Based on Ledermayer's whiteboard sketch, with board statuses across the top,
workflow stages below, and `factory-state`, review branches and `main` at the
bottom. Source snapshot: 2026-10-07. Existing
[manual workflow connections](manual_workflows.md) remain a more detailed view
of exact handoffs.

## Reading The Diagram

Each status card represents the **same recurring module Issue** at a different
point in its lifecycle, not a newly created Issue. Factory-owned summaries,
labels and Project fields are projections of durable state; human notes and
comments are preserved. Board columns never trigger dispatch or authorize work.

Solid connectors show implemented state or artifact dependencies, not automatic
workflow handoffs. Dashed amber boxes and dashed connectors represent intended
factory integration, not working automation; some underlying components already
exist. The dashed scheduler-to-worker edge describes end-state transport, not a
current direct call from `factory-dispatch` to `factory-worker`. Workflow
alignment beneath a status is conceptual; it does not claim that current callers
emit every lifecycle transition or that the live board has every option enabled.

## Status Contract

| Board status | Meaning and durable request mapping |
| --- | --- |
| Backlog | Desired work remains; request `queued` maps here before claiming. |
| Queued | Exact request claimed with a lease and fence; request `claimed`. |
| Acquiring (proposed) | Running `acquire_source`: fetch pinned module source and parse HCL/JSON into static facts. Not implemented as a reducer or live Project status. |
| Analyzing | Designed distinction: running `assess` evaluates accepted facts and exact inputs. Current code still maps both acquisition and assessment to Analyzing; the phase-specific split is proposed. |
| Validating | Supported lifecycle context phase; no corresponding request status. Worker completion and proposal validation are distinct checks, not proof this phase is projected today. |
| Needs review | Sealed evidence awaits acceptance (`awaiting_acceptance`), or a required context has an explicit review condition. |
| Blocked | Required inputs are missing or contexts cannot be resolved. `facts_accepted` alone awaits assessment inputs; it is not Done. |
| Failed | Retryable processing failure (`failed`); independent of an assessment result of `fail`. |
| Publishing | Accepted analysis (`accepted`) or a current context selection still awaiting verified deployment. |
| Done | Every required current context has exact verified selection/deployment membership. Old success cannot satisfy newer desired work. |
| Retired | Confirmed catalog retirement; history is retained. |

The reducer, not a linear arrow chain, decides the status. Retirement is handled
first; required-context review, blocking and retryable failure take precedence
over normal progress. Mixed tracks and newer generations can keep a module in
Backlog even while an older attempt runs. `superseded` requests cannot control
the module lifecycle. Quality results remain `pass`, `fail`, `unchecked` and
`not_applicable`, separate from execution, freshness and delivery.

Sources: [status reducer](../../internal/queue/status.go),
[request states](../../internal/queue/request.go),
[native Issue lifecycle](../issue-lifecycle.md).

## Compute Parallelism

Acquisition and assessment are separate requests handled by the same worker,
not one worker held open across acceptance. The explanatory sequence is Backlog
-> Queued -> Acquiring -> Needs review -> accepted facts -> Queued -> Analyzing
-> Needs review -> Publishing -> Done. Both Queued entries are the **same board
status**, reached for different requests. Missing required assessment inputs can
leave the module Blocked. Mixed contexts and newer generations still govern the
reducer; this is not an unconditional linear transition chain.

Provider/core collection remains independent. Collection activity must not move
module Issues into Acquiring or Analyzing just because metadata is being fetched.

**Current implementation:** each dispatcher invocation claims one request.
[ClaimNext](../../internal/queue/dispatch.go#L215) excludes active claims for the
same module, but does not enforce a factory-wide compute limit. Worker Actions
concurrency is keyed by request ID, so different modules can run concurrently.
The designed initial single compute slot is not currently a globally enforced cap.

**Proposed configuration:** add `max_active_compute`, default **1**, enforced by
the durable dispatcher. Count both claimed and running acquisition/assessment
requests so pending launches reserve capacity. Check capacity and claim ownership
in the same expected-head transaction; concurrent dispatchers must not oversubscribe.
Keep one active request per module and fill available slots per bounded dispatch
invocation. Provider collection uses a separate budget outside this compute cap.
GitHub Actions concurrency alone is not a durable numeric worker limit.

For example, **3** permits up to three active compute attempts across different
modules. Validated completion sealing releases ownership before evidence review;
verified recovery can also release an attempt. Lease expiry alone or an unknown
run outcome must not free capacity. Assessment still requires accepted acquisition
facts and current inputs before it becomes eligible. Lowering the cap must not
cancel existing attempts; new claims wait until active usage falls below the cap.

Neither `max_active_compute` nor the Acquiring split is implemented by this
documentation/diagram change. Implementation needs contract, reducer, projection,
configuration and concurrent-claim tests, plus normal review and activation gates.

## End-State Stages

| Stage | Designed responsibility |
| --- | --- |
| Discovery | Catalog observation, immutable release/SHA pins, desired work and catalog proposal; never directly launches workers. Existing [factory-discovery](../../.github/workflows/factory-discovery.yml). |
| Scheduler and recovery | Eligible durable requests, one initial compute slot, exact claims, leases/fences, heartbeats, retry deadlines and verified terminal outcomes. Unknown ownership remains parked. Existing [factory-dispatch](../../.github/workflows/factory-dispatch.yml); end-state transport/recovery integration is planned. |
| Module compute | Separate acquisition and assessment requests. Acquisition extracts static source facts; assessment reads accepted facts and exact input snapshots without source checkout. Trusted validation seals completion before independent delivery. Existing reusable [factory-worker](../../.github/workflows/factory-worker.yml). |
| Provider/core collection | Independent scheduled, conditional public-metadata reads; immutable versioned snapshots and explicit coverage. No provider binaries or upstream execution. |
| Module observations | Sanitized, versioned issues/activity proposals; invalidate only dependent dimensions, never edit curation or policy. |
| Proposal delivery | One typed subject per proposal; no fleet barrier. Kinds: `catalog`, `module_evidence`, `provider_snapshot`, `module_observation`, `selection_export`, `presentation`. Rebuild from current accepted base and preserve unrelated data. |
| Merge gates | Trusted-base validation and protected-main review. Eligible unattended data-only policy must be implemented and effectively enforced before use; executable/configuration changes remain human-reviewed. Existing [factory-ci](../../.github/workflows/factory-ci.yml) and [factory-proposal-validation](../../.github/workflows/factory-proposal-validation.yml). |
| Receipt reconciliation | Exact actual merge commit, proposal and content identity; idempotent acceptance import and durable wake-ups; lost acknowledgement cannot duplicate delivery. |
| Assessment input reconciliation | Accepted facts, definitions, policy and provider/core/observation snapshots; atomic narrow invalidation and current-fingerprint assessment enqueue. |
| Selection and export | Current accepted analysis selected independently per context with monotonic receipts; separate selection PR verifies canonical pointer and sequence, without reselection on merge. |
| Presentation | Sanitized as-of operational snapshots triggered by meaningful lifecycle/data changes; independent proposal, not self-certification of deployment. |
| Publication | Exact accepted commit, complete dataset, serialized environment-approved Pages deployment and verified generation/digest/selection-membership receipt. |
| Projection and audit | Durable events and bounded delivery, per-module checkpoints and acknowledgement; periodic drift audit with cursor and budget reserve. Only exact current deployment membership can project Done. |

Existing filenames do not prove activation or end-to-end live success. Planned
stage labels describe the approved operating model, not implemented schedules.

## Terraform Collection

### Planned Collector Workflow

The diagram groups three task views of a proposed reusable **factory-collector**
under **Acquiring**. This expresses collection activity; it does not make provider
collection control a module's board status. No collector workflow file, schedule,
request schema or runtime mode is implemented by this diagram/documentation change.

| Mode | Source and retained evidence | Diagram colour |
| --- | --- | --- |
| `factory-collector:acquire_changelog` | Pinned provider changelog entries; evidenced type/version associations; `<kind>/<type>/changelog/<content-id>.json`. | Orange |
| `factory-collector:acquire_release` | Exact provider release metadata and notes, with independent provenance; `<kind>/<type>/releases/<content-id>.json`. | Red |
| `factory-collector:acquire_issue` | Bounded issue observations, pagination completeness and evidenced affected-version ranges; `<kind>/<type>/issues/<content-id>.json`. | Green |

Each run handles one exact durable collection request and fenced attempt: prepare
pinned inputs, collect without state-writer credentials, validate and seal through
trusted code, then deliver an independent provider proposal. Acceptance receipt
import, readiness reconciliation and dependent assessment wake-ups remain separate.
Provider requirements/requests are source-specific; shared ownership records serve
all modes rather than belonging exclusively to a single mode.

Collect provider-wide sources once and associate supported entries with types.
Unmatched entries remain provider-wide. Sources may complement or contradict each
other for the same release; retain immutable evidence independently, never silently
merge away provenance or infer applicability from a mention. Human `curation/`
updates are separately reviewed and are not an automated collector mode.

These three modes cover changelog, release and issue evidence, not the whole
provider capability inventory. Pinned documentation/source metadata and Terraform
core-function collection remain separate designed responsibilities. The layout
preserves direct per-type source folders without `sources/` or `records/` wrappers,
with version/snapshot manifests binding exact accepted evidence and coverage.

Provider collection runs independently of module compute, daily/conditional or
on an explicitly scoped refresh, with one provider per claim. Start with full
AzureRM and AzAPI addresses; cover AzureRM 4.x and 5.x separately. Collect latest
stable releases within approved active majors, exact versions needed by selected
contexts, and explicitly requested historical versions. Full-version backfill is
a separate bounded operation. Retain every snapshot referenced by accepted analysis.

Pinned public registry/release metadata, documentation/source metadata, release
notes and bounded issue observations provide knowledge for resources, datasources,
ephemerals, functions and actions. Enumerate all documented types for selected
versions, not just symbols already used by modules. Register newly observed
providers without inventing coverage or silently expanding the reviewed allowlist.

Each immutable manifest records full address/version/ref, collector identity,
digests, collection time, validators/ETags, per-kind coverage, pagination and
failure/truncation reasons. Coverage is `complete`, `partial`, `unsupported`,
`failed` or `not_collected`; zero entries imply known-empty only for proven
complete collection. Never use `latest` as an immutable identity or infer runtime
compatibility from documentation.

Terraform core functions have a separate allowlisted public-documentation catalog,
pinned by core version/ref/digest. Missing core context remains unavailable.
Neither collector runs Terraform, provider plugins or upstream code. Plugin-derived
schema collection requires separate design/security approval and is outside this
end-state's approved read-only collection contract.

## Delivery Boundaries

- `factory-state` owns queue and lifecycle truth. Expected-head transactions,
  leases and fences protect writes; Issues and Projects are never queue inputs.
- Workers seal completion before independent proposal delivery. Compute success
  and branch creation are not canonical acceptance. Protected-main review and
  deployment environment gates remain intact until an eligible policy is enforced.
- A merge into `main` and the subsequent verified evidence receipt import are
  separate operations. Facts acceptance does not imply analysis acceptance,
  selection or deployment.
- Provider receipt import and assessment input reconciliation remain unwired.
  Current assessment needs accepted facts, definitions, policy and exact provider
  snapshots; missing inputs cannot be guessed or treated as a pass.
- Selection export, presentation export, complete dataset publication and
  deployment receipt integration remain planned; no workflow names are invented.
- Queued/Analyzing intent adapters are opt-in and current workflow callers do not
  enable those writes. Projection flush delivers existing records rather than
  creating missing lifecycle work.

See the [delivery plan](../../plan/feature-native-pipeline-delivery-1.md),
[operations](../factory-operations.md), [decisions](../decisions.md), and
[task register](../TODOS.md) for remaining implementation and live gates.
The authoritative end-state collection, proposal and operating-default contracts
are in the parent workspace's `plan/design-avm-intelligence-operating-model-2.md`;
the diagram does not turn those designed stages into claims of current execution.