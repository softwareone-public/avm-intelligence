# Workflow Guide

Follow the [root guide](../../AGENTS.md), [decisions](../../docs/decisions.md),
and the applicable task's rollout gates in [TODOs](../../docs/TODOS.md).

- Pin actions to reviewed full commit SHAs. Use `go-version-file: go.mod` and
  mirror the [CI gates](ci.yml), not an independently hard-coded Go version.
- Keep workflow/job permissions explicit and least-privilege. State-writer
  Contents access and reduced projection permissions are separate contracts.
  Preserve environment approvals and protected-main PR delivery.
- Run reviewed trusted code, never code from `factory-state` or an untrusted
  artifact. Separate credential-free compute from trusted validation/sealing.
- Validate repository, workflow, event, branch, commit, run, and attempt before
  consuming artifacts. Fence exact data/source targets and recheck mutable refs
  before remote writes. Successful workflow status alone is not an acceptance
  or publication receipt.
- Pass untrusted expressions through quoted environment variables, not shell
  interpolation. Retain path/symlink checks and bounded artifact handling.
- Keep discovery, compute, acceptance, selection export, projection, and Pages
  independent. Do not add triggers or broaden a manual target without approval;
  scheduling and unattended delivery retain their explicit rollout gates.
- Add or update neighboring tests in [cmd/factory](../../cmd/factory/AGENTS.md).
  Run `go test ./cmd/factory -run Workflow -count=1` from the repository root,
  including YAML parsing, `bash -n`, and fixture-based filters where applicable.
  Report missing tooling/skips; do not invent a CI lint command.
- Never dispatch a workflow as a local check. Record approved live outcomes
  separately from local tests, including exact runs, commits, and receipts.