# Factory Board Operations

Owner: Ledermayer.

The AVM Intelligence Factory Project is a projection for human triage and
status visibility. It is not the scheduler, request store, accepted dataset, or
publication authority.

Project: [AVM Intelligence Factory](https://github.com/orgs/softwareone-public/projects/1)

## Views

| View | Purpose | Primary filter |
| --- | --- | --- |
| Factory board | Review all factory and module work by status. | All work types |
| Catalog | Scan the accepted module cohort. | Work type = Module analysis |
| Needs attention | Triage blocked, failed, and review-required work. | Status = Blocked, Failed, or Needs review |
| Delivery roadmap | Track product implementation. | Work type = Implementation |

## Module Issue Contract

One active module has one recurring Issue identified by a stable hidden module
marker. A new source release or analysis attempt updates that Issue; it does not
create a new Issue. Retired modules close their recurring Issue only after the
catalog retirement rule is satisfied.

The Issue and Project item are projections of durable request state and accepted
results. A card move does not authorize work. Human notes remain outside the
factory-owned summary block.

The [factory discovery workflow](../.github/workflows/factory-discovery.yml)
retains nightly full-catalog discovery and pre-dispatch projection for source
changes. Its separate [manual counterpart](../.github/workflows/manual-discovery.yml)
uses RES/PTN/UTL checkboxes, `force`, and `module_names` CSV. Explicit canonical
IDs narrow the checked types; an empty list selects those types. Ordinary runs
do not refresh unchanged acquisition work. Force may create a selected durable
queued acquisition generation while displaying Backlog until a dispatcher claims
it. Neither discovery entry point dispatches workers, and the durable queue
remains authoritative. Complete catalog/retirement bookkeeping remains global;
manual selection narrows source resolution, not accepted catalog membership.

V2 reuses the same recurring module Issue across analysis cycles. Each durable
module/context desired generation has one factory-owned cycle comment identified
by a stable hidden cycle marker; retries update that cycle's comment rather than
creating duplicates. The comment records the source SHA, run, outcome, and links
to verified evidence, selection-export, and deployment receipts as they become
available. Current status stays in the Issue summary and Project fields; comments
preserve prior cycle footprints. Never edit human comments or create an Issue per
cycle.

## Status Mapping

| Board status | Meaning |
| --- | --- |
| Backlog | Known work awaiting dispatcher pickup or manual retry; a durable request may already exist. The durable queue, not the card, determines eligibility. |
| Queued | Durable request is ready for a worker. |
| Analyzing | A worker run owns the request. |
| Validating | Artifacts are being checked before acceptance. |
| Publishing | An accepted commit is awaiting Pages deployment. |
| Done | Every current required context has the exact accepted analysis/selection membership in the active verified deployment generation. |
| Needs review | Human decision is required. |
| Failed | Infrastructure or processing failure is retryable. |
| Blocked | Required source, release, permission, or policy input is unavailable. |

## Native Projection And Recovery

The bounded projector consumes durable native lifecycle intents. It verifies
the factory summary, owned labels and configured Project fields before writing
an acknowledgement; one module's failure does not invalidate another's evidence.
API budgets, leases, generation fences and per-module checkpoints remain active.

Current fields include source version/SHA, last analyzed, outcome, run URL and
verified published commit. The factory summary identifies the native factory,
not an external analyzer. Retirement removes the old analyzer field from managed
writes but does not delete or change remote Project settings.

Board-only recovery uses the exact-module
[projection recovery workflow](../.github/workflows/manual-projection-recovery.yml)
and [recovery skill](../.github/skills/v2-projection-recovery/SKILL.md).
It never discovers, enqueues, reanalyzes, accepts or deploys data. Use immutable
accepted records and receipts, not stale cards, as evidence. The historical
publication/board mismatch remains documented in
[F-001](FINDINGS.md#f-001-deployment-receipt-matches-board-reconciliation-does-not).

Recovery uses the same five module inputs. Normal recovery retains existing
delivery fingerprints and skips acknowledged convergence. `force` requests a
new drift check of acknowledged delivery without replacing lifecycle evidence,
preempting an active lease, clearing review state, or bypassing retry backoff.
For incident repair, always supply the approved exact module list rather than
inferring a wider cohort. New behavior is validated manually before reviewed
promotion into autonomous factory workflows; no additional scheduling is implied.
