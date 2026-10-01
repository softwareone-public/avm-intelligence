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
with accepted discovery and durable requests, but the bundle remains a workflow
artifact. No publisher currently commits or reconciles it against canonical Git
state. The Pages workflow therefore still cannot consume worker output directly
and does not claim a published result.

Next action: implement the canonical Git writer with current-HEAD checks,
conflict handling, and non-force-push updates before enabling unattended
publication.
