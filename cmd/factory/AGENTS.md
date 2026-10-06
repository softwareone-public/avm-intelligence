# Factory CLI Guide

Follow the [root guide](../../AGENTS.md) and
[Go instructions](../../.github/instructions/go-development.instructions.md).

- Keep argument parsing, mode selection, and orchestration here; place domain
  decisions and typed validation in the owning `internal` package.
- Validate mutually exclusive modes, required identities, exact targets, and
  output/data/cache path isolation before network calls or filesystem writes.
  Follow [provider collection](provider_collection.go) and its
  [tests](provider_collection_test.go) for side-effect ordering.
- Preserve existing flags and historical readers unless the approved task
  explicitly changes them. A command that collects, dispatches, accepts, writes
  state, or deploys is not a local validation command.
- Reuse neighboring CLI tests with temporary directories and synthetic inputs.
  Workflow changes also need tests of executable shell and provenance filters,
  not just assertions that YAML contains a string; see
  [provider workflow tests](provider_workflow_test.go).
- Start with the touched test or `go test ./cmd/factory -count=1` from the repo
  root. For workflow contracts use
  `go test ./cmd/factory -run Workflow -count=1`; ensure `bash` and `jq` are
  available and report skipped checks rather than claiming full coverage.