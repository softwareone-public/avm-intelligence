# AVM Intelligence

AVM Intelligence is a Go factory that discovers Azure Verified Modules, extracts
static facts from approved source revisions, assesses independent dimensions,
and publishes reviewed evidence. The selected v2 runtime uses compiled Go
libraries without executing IaCLens, Terraform, providers, or upstream code;
mandatory AI and dynamically loaded checks are excluded.

The JSON-only `factory-state` branch is the durable queue and lifecycle
authority. Accepted data and verified receipts govern evidence, selection, and
publication. GitHub Issues and Projects are human-facing projections, never
scheduling or approval inputs. Discovery, acquisition, assessment, acceptance,
selection export, board projection, and Pages deployment are separate stages.

The native cutover is in progress, not complete. See
[decisions](docs/decisions.md) for approved boundaries,
[TODOs](docs/TODOS.md) for implementation and rollout gates, and
[findings](docs/FINDINGS.md) for historical incidents and verified evidence.

## Current Status

Updated 2026-10-06 from recorded checkpoints through 2026-10-05 and the
2026-10-06 retirement direction; this summary does not constitute a fresh
verification of remote state.

| Area | Recorded progress and remaining boundary |
| --- | --- |
| Catalog and discovery | The generation-1 typed catalog, dataset manifest, module metadata, and indexes were accepted for 232 modules. Nightly full-catalog discovery and manual targeting are deployed; discovery never dispatches workers. |
| Native acquisition | Targeted facts-only acquisition for `avm-ptn-aca-lza-hosting-environment` completed compute, trusted sealing, evidence review/merge, and receipt import. Generation 2 is `facts_accepted` following PR #208 and import run `37227201589`. |
| Assessment and selection | Native dimension checks and receipt/selection contracts have local fixture coverage. The targeted cutover checkpoint has no assessment request, native analysis, selection, or deployment; accepted exact-version provider snapshots and policy/context wake-up wiring remain pending. |
| Board projection | Bounded Projection v2 and shared lifecycle reduction have fixture coverage. Targeted Backlog recovery succeeded in run `37219371339`; this does not establish fleet-wide convergence or historical first-cohort Done. |
| Provider intelligence | Exact-version catalog collection and conditional caching merged in PR #213; the manual artifact-only collection workflow merged in PR #214. Review-export implementation is on a separate unmerged branch. No live provider collection or acceptance is recorded; receipt import, health, invalidation, and assessment wake-up remain open. |
| Portal | Eight static surfaces render from accepted typed records, with local candidate browser checks. Integrated history/selectors, reciprocal evidence, module observations, branding approval, and full accessibility verification remain open; local rendering is not deployment evidence. |

Complete five-kind extraction, bounded AzureRM 4.x/5.x bootstrap, integrated
one-module then ten-module delivery, recovery/observability, and final product
acceptance remain required. See TASK-040, TASK-041, TASK-043, TASK-028, and Phase 6
in the [task register](docs/TODOS.md).

## Delivery And Safety

Legacy `Fleet plan` and `Pilot module` workflows are recorded as manually
disabled during clean-start cutover. Legacy evidence is not imported as native
facts, analyses, or selections. Ledermayer requested removal of all remaining
v1 artifacts after this documentation PR; that cleanup is a separate reviewed
change, not completed here. Its manifest and reader dependencies are tracked
under TASK-043 and [F-016](docs/FINDINGS.md#f-016-legacy-report-removal-leaves-dangling-manifests).

The v2 manual dispatcher requires an exact request ID. Workers cannot write
accepted data: trusted sealing, reviewed evidence PRs, merge-receipt import,
and separate reviewed selection exports retain their own boundaries. Broad
compute scheduling remains off pending TASK-028.

Pages publication targets an exact accepted commit and requires verified
deployment receipts; workflow success alone cannot establish Done. Protected
`main` PR delivery and existing environment protections remain in force.
Unattended delivery was authorized within the staged cutover, but TASK-042 must
implement and verify its narrow data-only policy before activation. Authorization
does not mean execution, and this is not yet a completed unattended factory.

## Development

Use the Go version declared in [go.mod](go.mod). From the repository root:

```sh
go mod verify
go test ./... -count=1
go vet ./...
go run ./cmd/factory --show-toolchain
```

The toolchain display reports the retained consumer contract, not proof of native
cutover. See [AGENTS.md](AGENTS.md) for formatting, focused checks, and agent
guidance, and [dimensions and checks](docs/dimensions-and-checks.md) for offline
assessment commands and evidence semantics. Unit tests use fixtures rather than
live credentials; operational commands are not local validation checks.

## Pinned Provider Collection

From a clean reviewed checkout, `--collect-provider-catalog` collects one
allowlisted public provider version into a reviewable snapshot proposal. Supply
the exact release version, matching provider source commit, positive collection
generation, and RFC3339 observation time explicitly; the command never selects
`latest`, accepts data, imports state, or starts assessment.

```sh
mkdir -p work/provider-cache
go run ./cmd/factory --collect-provider-catalog \
	--provider-policy config/providers.yaml \
	--provider-address registry.terraform.io/hashicorp/azurerm \
	--provider-version "$PROVIDER_VERSION" \
	--provider-revision "$PROVIDER_SOURCE_COMMIT" \
	--provider-collector-commit "$(git rev-parse HEAD)" \
	--provider-proposal-base "$(git rev-parse HEAD)" \
	--provider-source-generation "$PROVIDER_GENERATION" \
	--provider-observed-at "$OBSERVED_AT" \
	--provider-cache-root work/provider-cache \
	--provider-proposal-out work/provider-proposal \
	--data-root data
```

The cache directory must exist, the proposal directory must be fresh, and both
must be separate from accepted data and each other. The reviewed policy bounds
HTTP requests, pages, bytes, and wall time; release tags must match the supplied
source commit. Responses with validators can be reused conditionally across
runs. Review/apply/merge and receipt reconciliation remain separate gates;
this command is not a provider collection workflow or production activation.

The manual [V2 provider collection workflow](.github/workflows/v2_provider_collect.yml)
uses the existing `data-publisher` environment and read-only repository permissions.
Provide the exact current main commit plus provider version/revision, generation,
and observation time. It collects one provider, applies the resulting proposal
only to a temporary validation candidate, and uploads separate proposal and
public-response cache artifacts. It neither pushes a branch nor accepts data,
imports state, dispatches assessment, or deploys Pages.

An optional exact `cache_run_id` can restore a cache from a successful manual
run of that same workflow in this repository, using the same code commit,
policy digest, provider address, version, and revision. Artifact names include
the run ID and attempt; missing/expired/mismatched caches fail closed. Leave the
input empty for a cold collection. Artifacts expire after 30 days and are not
acceptance receipts; review/merge and receipt import still need their own stages.

## Historical Publication

The first ten-module legacy cohort was accepted at commit
`efdd161535b8152e686c13c9785098bc0b0d15ec` from workflow run `36869616628`.
Its Pages publication at https://ideal-adventure-62gqzpm.pages.github.io/ was
receipt-verified and locally reproduced. This is historical evidence, not proof
that the site serves the current v2 dataset.

The successful Pages-completion callback did not establish board convergence:
the recorded Issue #37 counterexample remained Publishing with no published
commit. See [F-001](docs/FINDINGS.md#f-001-deployment-receipt-matches-board-reconciliation-does-not)
for the deployment receipt and mismatch, and
[F-015](docs/FINDINGS.md#f-015-targeted-discovery-projection-outlived-its-lease)
for the separate successful targeted Backlog recovery.
The [board operations guide](docs/board-operations.md) describes the projection
contract; neither the board nor the historical publication certifies completion.
