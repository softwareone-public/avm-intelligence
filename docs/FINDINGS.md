# Findings

## Source-Proven V2 Gaps (Not Incident Claims)

These behaviors were verified in the checked-out factory source at baseline
`main` commit `908ee877c0ff379b5552fa9558f43aa1233d74bb`. They describe
implementation risks, not claims that each consequence occurred in production.
Run-specific events and receipts are kept separately in F-001 through F-013.

| Area | Source-proven behavior and evidence | Follow-up |
| --- | --- | --- |
| Preservation | [`Assemble`](../internal/publish/candidate.go) builds from dispatched modules; [`replaceAcceptedBundle`](../internal/publish/accept.go) stages that candidate as a replacement; [`fleet-plan.yml`](../.github/workflows/fleet-plan.yml) installs it with `rsync --delete`. A targeted accepted cohort can therefore omit unrelated canonical reports. No production deletion is asserted. | TASK-005, TASK-006, TASK-023 |
| Durable state | [`fleet-plan.yml`](../.github/workflows/fleet-plan.yml) starts discovery/planning in a fresh run workspace without loading prior accepted discovery, requests, or source pins. [`queue.Start`](../internal/queue/request.go) claims through local files, not a cross-workspace CAS. Multiple Actions workspaces therefore do not share durable ownership. | TASK-007, TASK-011, TASK-013, TASK-014 |
| Ownership and acceptance | Fleet assembly depends on the worker matrix succeeding; successful module artifacts cannot independently advance past a failed sibling. Worker completion is also distinct from reviewed canonical acceptance. Selection filters must not turn omitted modules into blocked work. | TASK-007, TASK-022, TASK-024, TASK-027 |
| Normalization and assessment | [`Normalize`](../internal/iaclens/normalize.go) retains a symbol count but not typed provider requirements or symbol bindings. The current reports therefore cannot establish zero provider usage or broad assessment coverage from missing facts; the local unknown/unchecked counts are measurements, not explanations. | TASK-008, TASK-016, TASK-017, TASK-018, TASK-040 |
| Facts schema versioning | Frozen [`factory-data-v2`](../schemas/factory-data-v2.schema.json) remains strict and unchanged. New [`factory-facts-v3`](../schemas/factory-facts-v3.schema.json) provides typed provider constraints and multi-provider child mappings; consumers must select the matching facts schema version and preserve v2 historical reads. | TASK-017, TASK-018, TASK-019, TASK-021 |
| Projection | At baseline, [`client.go`](../internal/github/client.go) could skip from an Issue summary without verifying Project fields/labels. TASK-010 now writes a bot-owned acknowledgement only after all per-module surfaces succeed and includes the published commit in summary/field sync; external drift audits and durable repair intents remain Projection v2 work. Current projection paths still omit blocked/retired modules. Run F-013 separately records the observed rate-limit incident. | TASK-010, TASK-026, TASK-027 |
| Deployment identity | [`project-pages-complete.yml`](../.github/workflows/project-pages-complete.yml) takes `workflow_run.head_sha` as the published commit instead of validating the deployment's accepted-commit receipt. F-001 records the verified deployed receipt and Issue #37 counterexample. | TASK-009, TASK-025, TASK-028 |
| Portal coverage | [`site/catalog.go`](../internal/site/catalog.go) builds from candidate reports rather than the complete discovered catalog, so catalog-only, blocked, and retired modules and the required history/provider/activity views are not represented by the current site. | TASK-034, TASK-035, TASK-036 |

## Evidence Status

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
