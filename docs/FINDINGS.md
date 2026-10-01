# Findings

## F-001: Live publication has not been verified yet

Status: open

The approval-gated Pages workflow verifies an exact accepted commit, builds the
evidence-first site from accepted `data/`, and refuses to continue if the data
tree or generated output is missing. The repository now contains the site
builder, but no complete live fleet run and Pages deployment have been verified.

Next action: run the approved fleet writer and Pages workflow against a complete
public-data cohort, then verify the deployed commit and rendered pages.

## F-002: Accepted bundle is not yet canonical

Status: open

PR19 validates plan/request/report identity and assembles all successful worker
artifacts into a complete candidate data tree. PR20 atomically bundles that tree
with accepted discovery and durable requests. PR21 adds an approval-gated,
data-only, non-force writer with a current-HEAD check. PR22 adds the static
site build. PR23 connects the accepted commit to a separate `pages-publisher`
approval and exact-SHA deployment, but no live result has been verified yet.

Next action: execute the approved fleet writer and Pages deployment against a
real complete public-data run, then verify the deployed commit and rendered
pages.

## F-003: Release-less modules aborted fleet resolution

Status: resolved in PR24

The first live fleet attempt failed during source resolution because many
accepted AVM modules have no published stable SemVer release. The resolver
treated that expected module-level condition as a systemic failure, preventing
the planner from marking those modules blocked.

Resolution: release-less modules are now omitted from the pin set so the
planner records them as blocked; API failures, invalid release data, ambiguous
aliases, and moved accepted tags remain fatal. Rerun the fleet workflow after
PR24 merges.
