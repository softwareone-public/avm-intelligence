# Domain Package Guide

Follow the [root guide](../AGENTS.md) and
[Go instructions](../.github/instructions/go-development.instructions.md).
Read the [decisions](../docs/decisions.md) before changing lifecycle contracts.

## Ownership

- `state` owns expected-head transactions, event replay, durable records, and
  fences. `queue` owns requests, claims, recovery, selection, and the shared
  lifecycle reducer. Never infer authority from an Issue or Project field.
- `catalog`, `discovery`, and `source` own observations and immutable source
  pins; discovery must remain separate from dispatch.
- `publish` owns typed proposals, preservation, acceptance, and publication
  verification. Use existing appliers; never replace a complete accepted tree
  with a targeted cohort or manually rewrite accepted history.
- `identity` and `index` own canonical IDs, safe paths, typed references, and
  deterministic indexes. Reuse their helpers instead of constructing IDs or
  paths ad hoc; indexes are derived from accepted records.
- `terraform` extracts syntax-only facts; `assessment` owns compiled checks and
  independent dimensions. Read the [checks guide](../docs/dimensions-and-checks.md).
  Do not execute Terraform, providers, upstream code, or arbitrary configuration.
- `provider` owns allowlisted, bounded, pinned public metadata collection;
  missing coverage stays unavailable, never an inferred pass.
- `github` owns non-canonical projection and reduced tokens; `site` renders
  accepted evidence and sanitized as-of state, not raw control records.

## Logging

- Follow the [shared logging policy](../AGENTS.md#logging) and Go instructions.
  Use the factory-configured `log/slog` logger; domain packages must not choose
  output streams or configure handlers. Include module/request/phase IDs where
  available, meaningful boundaries and durations, and safe skip/retry reasons.
- Keep detailed checks at `DEBUG`, progress at `INFO`, and incomplete/recoverable
  execution at `WARN`. Assessment outcomes remain attributes. Propagate errors
  to the CLI for the single terminal `ERROR`; do not dump source, credentials,
  whole evidence records or parser/API bodies.
- Do not add telemetry to persisted records or identity calculations. Verify
  evidence bytes and semantics remain unchanged across severity settings.

## Local checks

- Reuse tests in the owning package, `httptest`, and `t.TempDir`; require no live
  credentials. Preserve unrelated records byte-for-byte in proposal tests.
- Test stale fences, lost acknowledgements, exact memberships, and idempotency
  when touching durable transitions. Run race tests for touched state/queue
  concurrency paths after focused behavior tests.
- Keep `pass`, `fail`, `unchecked`, and `not_applicable` distinct from execution
  failure, coverage, freshness, and delivery status. Missing facts are not proof
  of absence; failed optional inputs must not erase completed dimensions.
- Log findings and follow-ups in the central registers linked by the root guide,
  not package-local duplicate lists.