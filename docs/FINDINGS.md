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
site build, but no result is canonical until the workflows run successfully.

Next action: execute the approved writer against a real complete worker run and
verify the resulting commit before Pages deployment.
