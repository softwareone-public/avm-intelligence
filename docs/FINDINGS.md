# Findings

## F-001: Live Pages publication is verified; board reconciliation is pending

Status: resolved for the first cohort

The approval-gated Pages workflow verifies an exact accepted commit, builds the
evidence-first site from accepted `data/`, and refuses to continue if the data
tree or generated output is missing. The first ten-module cohort is canonical
and has been deployed successfully at
https://ideal-adventure-62gqzpm.pages.github.io/.

Next action: verify the in-progress Pages-complete Project reconciliation run
`36875075805` for the first cohort.

## F-002: Accepted bundle is canonical; publication remains separate

Status: resolved for the first cohort

PR19 validates plan/request/report identity and assembles all successful worker
artifacts into a complete candidate data tree. PR20 atomically bundles that tree
with accepted discovery and durable requests. PR21/PR27 submit accepted data
through the repository's pull-request rule. PR22 adds the static site build and
PR23 connects the accepted commit to a separate `pages-publisher` approval.
PR28 merged the first ten-module accepted bundle at `efdd161`.

Next action: verify the Pages-complete board projection and reconcile blocked
and retired module states.

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
