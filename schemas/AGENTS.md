# Schema Contract Guide

Follow the [root guide](../AGENTS.md) and
[contract decisions](../docs/decisions.md).

- Treat [factory-data-v2](factory-data-v2.schema.json) and historical
  [normalized-analysis-v1](normalized-analysis-v1.schema.json) as frozen
  contracts. Do not silently widen them for new producer output.
- New representations need explicit versioning, matching readers, and tested
  historical compatibility; [factory-facts-v3](factory-facts-v3.schema.json)
  is separate from the frozen v2 facts representation.
- Update owning Go records/validators, reference helpers, fixtures, and affected
  reader/proposal tests together. Preserve required fields, explicit nulls,
  absence meanings, strict properties, canonical paths, and typed references.
- Never relabel legacy evidence as native facts or invent missing provenance.
  Missing extraction coverage cannot become a complete empty inventory.
- From the repository root, start with
  `go test ./internal/identity ./internal/terraform ./internal/assessment -run Schema -count=1`,
  then run tests for affected consumers and the root review gates. Existing Go
  schema tests check references and selected shapes; do not describe them as
  comprehensive JSON-Schema validation.