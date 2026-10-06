---
goal: "Retire the former runtime and data locally while preserving native contracts"
version: "2.0"
date_created: "2026-10-06"
last_updated: "2026-10-06"
owner: "Ledermayer"
status: "In progress"
tags: [refactor, retirement, native, offline]
---

# Introduction

![Status: In progress](https://img.shields.io/badge/status-In%20progress-yellow)

Ledermayer approved complete offline implementation on 2026-10-06: remove and
change the current tree locally so the next reviewed change contains no former
runtime, workflows, producer configuration, report readers or data. This replaces
the initial proposed maintenance envelope/schema/workflow and multi-PR cleanup.
No such maintenance mechanism was implemented. Local validation does not establish
a merged commit, active deployment or completed native product.

TASK-043 remains the authoritative owning task. `STEP-*` and `CHECK-*` are local
execution identifiers, not new v2 task/test IDs. Inventory used main at `12d42dc`;
the original worktree's provider-export code and documentation changes were
preserved. No commit, push, new PR, workflow dispatch or remote setting change
was performed during this implementation.

## 1. Requirements & Constraints

- **REQ-001**: Remove external analyzer/toolchain/pilot execution and pins, old fleet/file-queue APIs, report adapters/renderers, compatibility-only schema/fixtures/migration output, six old workflows, and 292 legacy JSON records.
- **REQ-002**: Preserve native request IDs, serialized native inputs, CAS/fences, source pins, facts/provenance, independent assessment/selection contracts, stable Issues and indexed deployment receipts.
- **REQ-003**: Regenerate all derived native files locally with existing Go builders; preserve source bytes, catalog membership, generator/time metadata and explicit null selections.
- **SEC-001**: Perform no live collection, dispatch, state acceptance, deployment, permission change or protected-branch bypass. Future PR review/merge retains its actual effective controls.
- **CON-001**: Native schemas/capabilities numbered `v1`, schema-version-1 native artifacts, public module version tags and preserved native fingerprint wire keys are not architectural-v1 runtime and must not be deleted by a text search.
- **CON-002**: Keep factual history attributable to immutable Git commits; remove actionable old operating recipes and current-tree dangling links rather than rewriting incidents as native successes.
- **CON-003**: Keep the six production proposal kinds and trust boundaries unchanged. Record the mixed-PR review constraint, not an unreviewed exception or false merge-readiness claim.
- **GUD-001**: Validate each removal slice with focused offline tests. Preserve required native test helpers and reviewed status/severity/evidence expectations before deleting their former definitions.

## 2. Implementation Steps

### Implementation Phase 1

- **GOAL-001**: Complete local native-only code/config/workflow retirement.

| Task | Description | Completed | Date |
| --- | --- | --- | --- |
| STEP-001 | Route the CLI through `cmd/factory/native.go`, reject retired/mixed modes, and isolate reduced token minting from removed producer config. Preserve native command handlers and provider-export work. | Yes | 2026-10-06 |
| STEP-002 | Remove file queue APIs/pending slots, require native Work, preserve IDs/statuses/leases/sealing and retain `internal/queue/dispatch.go`: the initial inventory incorrectly classified this native file. Preserve former shared test helpers. | Yes | 2026-10-06 |
| STEP-003 | Remove external analyzer/toolchain and pilot/fleet source wrappers, report-based publishing/projection/site/migration adapters and compatibility schema/fixtures. Preserve pure catalog/source, accepted-tree helpers, native typed proposals and indexed publication. | Yes | 2026-10-06 |
| STEP-004 | Retain reviewed check status/severity/evidence records in producer-free `testdata/assessment/native-checks.json`; reject retired receipt formats and preserve human-summary boundary checks. Replace analyzer field writes with native factory summary naming without remote Project changes. | Yes | 2026-10-06 |
| STEP-005 | Remove old module/project/fleet-plan/project-accepted/project-pages-complete/publish-pages workflows. Keep native workflows and change CI/Go-guide smoke checks to `--list-dimensions`. | Yes | 2026-10-06 |

Native publisher activation remains separate. There is no accepted presentation
snapshot or native deployment receipt in the current data; unavailable inputs
must fail closed, not revive a removed reader or certify Done.

### Implementation Phase 2

- **GOAL-002**: Complete offline legacy-data removal and native rebuild.

| Task | Description | Completed | Date |
| --- | --- | --- | --- |
| STEP-006 | Remove 145 flat reports, 145 local requests, candidate and discovery files; verify native record shapes before removing control files. Preserve the original report deletions rather than restoring them. | Yes | 2026-10-06 |
| STEP-007 | Enable direct native-tree input on `--build-indexes`; rebuild twice into fresh external local roots with preserved base metadata/time, compare bytes, and install generated files only. | Yes | 2026-10-06 |
| STEP-008 | Verify 237 unchanged native source hashes, 232 catalog members, 472 manifest digests and no invented analyses/selections. Add `TestRepositoryDatasetIsNativeAndRebuildable` without fixed fleet counts. | Yes | 2026-10-06 |

Verified dataset ID:
`dataset-bd84ed9b0db3fe69fe0e26786df440e6a6905428a70531466949bd4f30fd8444`.
The dataset has 473 typed files including the manifest; the manifest omits its
own hash and covers the other 472 files. These are local results, not a remote
acceptance or publication receipt.

### Implementation Phase 3

- **GOAL-003**: Complete local documentation/gates, then resolve review readiness.

| Task | Description | Completed | Date |
| --- | --- | --- | --- |
| STEP-009 | Update README, native lifecycle/board/check guides, decisions, findings and TODOs; retain historical links at immutable commits and document corrected native/shared-file classifications. | Yes | 2026-10-06 |
| STEP-010 | Pass full offline tests, native schema/workflow checks, formatting, module verification, vet and affected race tests. Verify no external producer imports/download recipes or current-data dangling references remain. | Yes | 2026-10-06 |
| STEP-011 | Determine the effective review handling for one combined retirement PR. The current trusted-base proposal path rejects mixed code/data and cannot acquire support from candidate code in that same PR. Keep this boundary intact until reviewed handling is explicitly resolved. | No | Pending |

No new cleanup workflow, maintenance schema, envelope or autonomous data-writer
path is part of these steps. TASK-043, TASK-028, TASK-042 and product acceptance
remain open independently of local runtime retirement.

## 3. Alternatives

- **ALT-001**: Workflow-driven maintenance envelope/schema and a separate cleanup run. Rejected by Ledermayer; not implemented.
- **ALT-002**: Report-only deletion or manual hash editing. Rejected; complete native rebuild/preservation checks were used locally.
- **ALT-003**: Delete whole mixed packages based on age or name. Rejected; native dispatch, request/projection models, source resolver and tree utilities were preserved and tested.
- **ALT-004**: Disable/broaden trusted proposal validation or claim the mixed PR will merge without reviewing effective controls. Rejected; F-018 records the unresolved constraint.

## 4. Dependencies

- **DEP-001**: Native compiled dependencies already cached for offline execution; no new package or analyzer installation was performed.
- **DEP-002**: Protected-main review and effective check requirements before the next PR can be called merge-ready. No remote permissions/environment policy was inspected or changed during retirement.
- **DEP-003**: Native assessment/provider inputs, selections, presentation export and integrated publication still need their separate live/acceptance gates. Local removal is not activation.

## 5. Files

- **FILE-001**: Native CLI/router/path utilities and tests under `cmd/factory/`; native-only queue, catalog/source, projection, publishing, site and identity changes under `internal/`.
- **FILE-002**: Retired external runtime/config/schema/test/migration files and six old workflow files; native workflows remain.
- **FILE-003**: Native `data/` after removal of 292 old JSON records; deterministic generated index/dataset changes only, with all 237 source records unchanged.
- **FILE-004**: Root/nested guides, scoped Go instructions, README, decisions, findings, TODOs, native lifecycle/check/board documentation and this execution record.

## 6. Testing

- **CHECK-001**: Focused native CLI regressions reject retired modes, mixed modes and unrelated producer config; token validation is credential-free.
- **CHECK-002**: Queue/worker/state/discovery tests preserve claims, leases, fences, sealing, facts acceptance and native request identity; missing Work is rejected.
- **CHECK-003**: Native check fixtures preserve deterministic statuses, severities and evidence without normalization or executable producer dependencies.
- **CHECK-004**: Native projection tests preserve human notes, exact markers, malformed-summary rejection, budgeting/checkpoints and generation behavior. Indexed receipt tests reject old format, wrong producer/generation and changed dataset.
- **CHECK-005**: All generated bytes match two offline rebuilds; all manifest digests resolve, native source hashes remain identical and no old control/report paths exist. Repository regression rebuilds current data without permanent expected fleet totals.
- **CHECK-006**: Native workflow YAML/shell/provenance tests pass; retired workflow files are absent. No workflow is actually dispatched as validation.
- **CHECK-007**: Required final local gates are formatting, `go mod verify`, `go test ./... -count=1`, `go vet ./...`, native dimension listing, native schema tests and affected queue/state race checks, with module networking disabled.
- **CHECK-008**: Markdown frontmatter/links/anchors are valid; missing current-tree historical paths link to immutable commits. Literal native contract versions or negative regression strings are explicit exceptions to a text-only retirement scan.

## 7. Risks & Assumptions

- **RISK-001**: Native helpers shared files with removed code/tests. Compiler and focused checks exposed and repaired those boundaries; native `dispatch.go` must never be treated as an old fleet writer.
- **RISK-002**: Removing the old publisher does not implement its integrated native replacement. Keep this capability unavailable until its exact inputs/receipts are accepted.
- **RISK-003**: Trusted-base proposal validation will reject the combined change as written; effective required-check enforcement is unknown offline. No bypass or candidate-self-authorization is acceptable.
- **ASSUMPTION-001**: The 232-module counts describe the current local baseline, not permanent expected totals; automated gates derive membership from native records.
- **ASSUMPTION-002**: Existing worktree/provider-export changes remain local and unstaged; no remote state, deployment, permissions or active worker owners were reverified.

## 8. Related Specifications / Further Reading

- [Owning task and rollout gates](../docs/TODOS.md)
- [Offline retirement authority](../docs/decisions.md)
- [Local preservation/isolation and review constraint](../docs/FINDINGS.md)
- [Native lifecycle](../docs/issue-lifecycle.md)
- [Native dimensions/checks](../docs/dimensions-and-checks.md)
- [Go development gates](../.github/instructions/go-development.instructions.md)