# Factory Tasks

Owner: Ledermayer.

- [x] Record sponsorship, event scope, public-data policy, and the IaCLens `v0.1.0` pin.
- [x] Add the fixture-only factory skeleton and read-only CI.
- [x] Prove the single public AVM pilot transaction: analysis run `36847787791`, Issue #8, and Project item with source provenance. Keep its state `Publishing` / `Unassessed` until publication succeeds.
- [x] Upsert the pilot Issue and Project item with a reduced installation token; preserve human Issue notes on retries.
- [x] Resolve active accepted catalog modules to the highest published stable SemVer commit; reject moved tags and conflicting `v`/unprefixed aliases.
- [x] Add durable request state with complete analysis fingerprints, unchanged-work coalescing, failed-attempt retries, one newer pending input per active module, run ownership, and lost-run reconciliation.
- [ ] Connect accepted catalog state and immutable source pins to batch fleet planning/reconciliation. Discovery state remains separate at `data/discovery.json`; suspicious shrinkage requires review and modules retire only after two successful observations.
- [ ] Add exact-commit publication and Pages deployment after a separate approval.
- [ ] Capture approved SoftwareOne brand assets before portal styling.
- [ ] Run a bounded semantic sample only after budget and public-input approval.
