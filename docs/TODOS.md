# Factory Tasks

Owner: Ledermayer.

- [x] Record sponsorship, event scope, public-data policy, and the IaCLens `v0.1.0` pin.
- [x] Add the fixture-only factory skeleton and read-only CI.
- [x] Prove the single public AVM pilot transaction: analysis run `36847787791`, Issue #8, and Project item with source provenance. Keep its state `Publishing` / `Unassessed` until publication succeeds.
- [x] Upsert the pilot Issue and Project item with a reduced installation token; preserve human Issue notes on retries.
- [x] Resolve active accepted catalog modules to the highest published stable SemVer commit; reject moved tags and conflicting `v`/unprefixed aliases.
- [x] Add durable request state with complete analysis fingerprints, unchanged-work coalescing, failed-attempt retries, one newer pending input per active module, run ownership, and lost-run reconciliation.
- [x] Build a deterministic planner from accepted catalog state and resolved pins; register per-module work and emit dispatch batches capped at 100 while keeping unresolved/retired modules explicit.
- [x] Resolve immutable release pins for every active accepted module in stable ID order, fail closed on any unresolved module, and preserve prior pins for moved-tag verification.
- [x] Reconcile durable fleet requests from explicit run states, promote pending work, and emit bounded recovery batches without inferring absent runs.
- [x] Orchestrate scheduled dry-run plans and authorized manual fleet workers. Discovery state remains separate from published data; suspicious shrinkage requires review and modules retire only after two successful observations.
- [x] Add exact-commit publication manifests and an approval-gated Pages deployment workflow; deployment fails closed until accepted data and generated site trees exist.
- [x] Validate and assemble all successful worker artifacts into a complete candidate data tree with plan, request, provenance, duplicate, and completeness checks.
- [x] Atomically bundle validated candidate data, accepted discovery state, and durable requests in the single aggregation job.
- [x] Commit the accepted bundle to canonical Git state with current-HEAD conflict detection, data-only staging, and non-force updates.
- [x] Build an evidence-first static catalog and module detail site from accepted data without aggregate scores or unassessed claims.
- [x] Connect the canonical accepted commit to a separately approved exact-SHA Pages deployment.
- [x] Keep accepted modules without a published stable release explicit and blocked instead of aborting the fleet plan.
- [x] Add manual resource/pattern/utility module filters and short-lived per-worker artifacts; retain accepted bundles longer for review.
- [x] Add optional manual module-ID selection for fast targeted analysis runs.
- [ ] Capture approved SoftwareOne brand assets before portal styling.
- [ ] Run a bounded semantic sample only after budget and public-input approval.
