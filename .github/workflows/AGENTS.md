# Workflow Guide

Follow the [root guide](../../AGENTS.md), [decisions](../../docs/decisions.md),
and the applicable task's rollout gates in [TODOs](../../docs/TODOS.md).

- Follow the [GitHub operations standard](../instructions/github-operations.instructions.md):
  `factory-*` is autonomous without dispatch; `manual-*` is dispatch-only;
  `admin-*` covers non-factory administration, CI and PR/proposal validation.
  Develop and verify behavior manually before reviewed factory promotion.
  Preserve existing autonomous workload and rollout gates during development.
- Module-targeted manual workflows use `res`, `ptn`, `utl`, `force`, and
  `module_names`. Keep meaningful provider/global/exact-identity exceptions;
  selectors and force never replace safety identities or approvals.
- Prefer suitable GitHub/GitKraken tools; use authenticated `gh`/`git` fallbacks
  when tools are unavailable, failing or unreliable. Check uncertain prior write
  outcomes before retrying to avoid duplicates. Verify authentication rather than
  assuming it; user login happens directly in the terminal. Preserve all approvals.

- Pin actions to reviewed full commit SHAs. Use `go-version-file: go.mod` and
  mirror the [CI gates](factory-ci.yml), not an independently hard-coded Go version.
- Keep workflow/job permissions explicit and least-privilege. State-writer
  Contents access and reduced projection permissions are separate contracts.
  Preserve environment approvals and protected-main PR delivery.
- Run reviewed trusted code, never code from `factory-state` or an untrusted
  artifact. Separate credential-free compute from trusted validation/sealing.
- Every trusted `factory-*` or `manual-*` job that reads or writes `factory-state` must use a verified
  state checkout and seed the command's blob cache from it. Follow
  [factory-dispatch](factory-dispatch.yml): state-only App token, pinned checkout
  into `work/state`, explicit repository/ref, `fetch-depth: 1`, and
  `persist-credentials: false`; verify the origin repository with
  `GIT_NO_REPLACE_OBJECTS=1`, then produce `git ls-tree -r -z HEAD` and pass the
  checkout root/tree listing to the owning command. Verify blob identities and
  reject unsafe paths/symlinks before reuse. A checkout without command-side
  cache consumption is not sufficient. If the command lacks support, flag the
  gap and implement it within approved scope before claiming compliance; do not
  enable unrelated modes or broaden discovery scope to obtain caching.
- Reuse verified immutable blobs across repeated reads; retain live-head checks,
  expected-head transactions, fencing and lost-acknowledgement recovery. Fetch
  updates when needed; never pull/merge/rebase divergent operational state or
  force-push it. Checkout is transport, not queue authority or permission to run
  state-branch code. Jobs without state access, especially credential-free
  compute, must not receive state credentials or an unnecessary state checkout.
  The checkout requirement does not apply to `admin-*`; administration/PR checks
  must not acquire incidental state access or perform factory lifecycle operations.
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