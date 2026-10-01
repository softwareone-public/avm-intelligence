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

## F-002: Candidate data is not yet canonical

Status: open

PR19 validates plan/request/report identity and assembles all successful worker
artifacts into a complete candidate data tree. No publisher currently accepts,
commits, or reconciles that candidate against current canonical state. The Pages
workflow therefore still cannot consume worker output directly and does not
claim a published result.

Next action: implement single-writer candidate acceptance, control-state
persistence, and candidate conflict handling before enabling unattended publication.
