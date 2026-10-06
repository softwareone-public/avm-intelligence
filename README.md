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

The runtime and current data tree are native-only. Integrated delivery and
product acceptance are still in progress. See
[decisions](docs/decisions.md) for approved boundaries,
[TODOs](docs/TODOS.md) for implementation and rollout gates, and
[findings](docs/FINDINGS.md) for historical incidents and verified evidence.

## Current Status

Updated 2026-10-06. Runtime retirement and dataset regeneration were performed
and tested locally; remote activation and deployment were not reverified.

| Area | Recorded progress and remaining boundary |
| --- | --- |
| Catalog and discovery | The generation-1 typed catalog, dataset manifest, module metadata, and indexes were accepted for 232 modules. Nightly full-catalog discovery and manual targeting are deployed; discovery never dispatches workers. |
| Native acquisition | Targeted facts-only acquisition for `avm-ptn-aca-lza-hosting-environment` completed compute, trusted sealing, evidence review/merge, and receipt import. Generation 2 is `facts_accepted` following PR #208 and import run `37227201589`. |
| Assessment and selection | Native dimension checks and receipt/selection contracts have local fixture coverage. The targeted cutover checkpoint has no assessment request, native analysis, selection, or deployment; accepted exact-version provider snapshots and policy/context wake-up wiring remain pending. |
| Board projection | Bounded Projection v2 and shared lifecycle reduction have fixture coverage. Targeted Backlog recovery succeeded in run `37219371339`; this does not establish fleet-wide convergence or historical first-cohort Done. |
| Provider intelligence | Exact-version catalog collection and conditional caching merged in PR #213; the artifact-only collection workflow merged in PR #214. Review-export code is present in this checkout but not recorded as merged. Receipt import, health, invalidation, and assessment wake-up remain open; no live collection was performed during retirement. |
| Portal | Eight static surfaces render from accepted typed records, with local candidate browser checks. Integrated history/selectors, reciprocal evidence, module observations, branding approval, and full accessibility verification remain open; local rendering is not deployment evidence. |

Complete five-kind extraction, bounded AzureRM 4.x/5.x bootstrap, integrated
one-module then ten-module delivery, recovery/observability, and final product
acceptance remain required. See TASK-040, TASK-041, TASK-043, TASK-028, and Phase 6
in the [task register](docs/TODOS.md).

## Delivery And Safety

The external analyzer, pilot/fleet runtime, file-based queue, report readers,
compatibility schema and old publisher/projector workflows have been removed.
The 145 report files, 145 local requests, candidate and discovery files are no
longer in the current data tree. Immutable Git history retains past evidence;
none was relabeled as native facts, analyses or selections.

The offline rebuild preserves all 237 native source records byte-for-byte and
all 232 catalog modules. The complete native dataset has 472 verified record
digests and ID
`dataset-bd84ed9b0db3fe69fe0e26786df440e6a6905428a70531466949bd4f30fd8444`.
No assessment, selection or deployment was invented during this change.

The v2 manual dispatcher requires an exact request ID. Workers cannot write
accepted data: trusted sealing, reviewed evidence PRs, merge-receipt import,
and separate reviewed selection exports retain their own boundaries. Broad
compute scheduling remains off pending TASK-028.

Native publication contracts bind exact accepted data, generation and membership;
workflow success alone cannot establish Done. The old Pages producer has been
removed; an integrated native Pages path and an accepted presentation snapshot
remain required before publication. Protected
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
go run ./cmd/factory --list-dimensions
go run ./cmd/factory --list-checks interface-documentation
```

See [AGENTS.md](AGENTS.md) for formatting, focused checks, and agent
guidance, and [dimensions and checks](docs/dimensions-and-checks.md) for offline
assessment commands and evidence semantics. Unit tests use fixtures rather than
live credentials; operational commands are not local validation checks.

Rebuild native indexes without source collection or state writes into a fresh
directory outside accepted data:

```sh
go run ./cmd/factory --build-indexes \
	--data-root data --indexes-out work/native-index-candidate
```

This stages derived files only; it does not overwrite accepted data or publish.
The indexed portal requires an accepted presentation snapshot and fails closed
when that input is unavailable. Retired commands are rejected rather than used
as fallback readers.

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

After a successful collection, the manual
[V2 provider proposal export workflow](.github/workflows/v2_provider_export.yml)
accepts the same exact current-main `accepted_base` and the `collection_run_id`.
It verifies the successful manual producer run in this repository and downloads
only its run-attempt-specific proposal artifact. The typed applier validates
the proposal and actual staged path set against the exact base before the
exporter prepares a `factory/v2-provider-*` review branch. Main is rechecked
before the branch push; moved bases and missing/expired artifacts fail closed.
Open a data PR from that branch for review. Export does not merge the PR,
import acceptance into `factory-state`, dispatch assessment, or publish Pages.

## Evidence And Operations

The [board operations guide](docs/board-operations.md) describes native lifecycle
projection and exact Done membership. Past deployments and incidents remain in
[findings](docs/FINDINGS.md); they are historical evidence, not proof that a site
serves this dataset or that native delivery is complete.
