# Findings

## F-001: Live Pages publication has not been verified yet

Status: open

The approval-gated Pages workflow verifies an exact accepted commit, builds the
evidence-first site from accepted `data/`, and refuses to continue if the data
tree or generated output is missing. The first ten-module cohort is now
canonical, but Pages has not yet been deployed and verified against it.

Next action: run the approved fleet writer and Pages workflow against a complete
public-data cohort, then verify the deployed commit and rendered pages.

## F-002: Accepted bundle is canonical; publication remains separate

Status: resolved for the first cohort

PR19 validates plan/request/report identity and assembles all successful worker
artifacts into a complete candidate data tree. PR20 atomically bundles that tree
with accepted discovery and durable requests. PR21/PR27 submit accepted data
through the repository's pull-request rule. PR22 adds the static site build and
PR23 connects the accepted commit to a separate `pages-publisher` approval.
PR28 merged the first ten-module accepted bundle at `efdd161`.

Next action: deploy and verify Pages for the accepted commit, then reconcile
the corresponding board projections.

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

## F-007: Fleet board projection is not automated

Status: open

The first ten-module cohort is canonical in `data/`, but the fleet workflow does
not yet create or reconcile one recurring Issue and Project item per accepted
module. The Project is therefore useful for product work and pilot history, but
it is not yet a complete view of fleet analysis state.

Next action: implement the projection worker using the stable module marker and
the field/status mapping in `docs/board-operations.md`. Keep the board
non-canonical and preserve human Issue notes.
