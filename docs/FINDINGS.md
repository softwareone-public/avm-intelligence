# Findings

## F-001: Publication inputs are not present yet

Status: open

The approval-gated Pages workflow verifies an exact accepted commit and refuses
to continue unless that commit contains both `data/` and generated `site/`
trees. The repository does not yet contain an accepted dataset or site builder,
so a manual publication attempt must fail closed. This is an expected product
boundary for PR18, not evidence that Pages is ready for production.

Next action: accept validated worker artifacts into a complete dataset and add
the approved site generator before requesting a Pages deployment.

## F-002: Accepted bundle is not yet canonical

Status: open

PR19 validates plan/request/report identity and assembles all successful worker
artifacts into a complete candidate data tree. PR20 atomically bundles that tree
with accepted discovery and durable requests. PR21 adds an approval-gated,
data-only, non-force writer with a current-HEAD check; until that workflow is
run successfully, the bundle remains an artifact and no result is canonical.

Next action: execute the approved writer against a real complete worker run and
verify the resulting commit before Pages deployment.
