# Factory Tasks

Owner: Ledermayer.

- [x] Record sponsorship, event scope, public-data policy, and the IaCLens `v0.1.0` pin.
- [x] Add the fixture-only factory skeleton and read-only CI.
- [x] Prove the single public AVM pilot transaction: analysis run `36847787791`, Issue #8, and Project item with source provenance. Keep its state `Publishing` / `Unassessed` until publication succeeds.
- [x] Upsert the pilot Issue and Project item with a reduced installation token; preserve human Issue notes on retries.
- [x] Resolve active accepted catalog modules to the highest published stable SemVer commit; reject moved tags and conflicting `v`/unprefixed aliases.
- [ ] Extend durable requests and reconciliation from the pilot to fleet work. Accepted discovery state is persisted separately at `data/discovery.json`; large per-feed shrinkage requires review, and missing modules retire only after two successful observations.
- [ ] Add exact-commit publication and Pages deployment after a separate approval.
- [ ] Capture approved SoftwareOne brand assets before portal styling.
- [ ] Run a bounded semantic sample only after budget and public-input approval.
