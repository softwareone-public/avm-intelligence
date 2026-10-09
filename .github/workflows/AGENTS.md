# Workflow Guide

Follow the [root guide](../../AGENTS.md), [decisions](../../docs/decisions.md),
and the applicable task's rollout gates in [TODOs](../../docs/TODOS.md).

- Follow the [GitHub operations standard](../instructions/github-operations.instructions.md):
  `factory-*` is autonomous without dispatch; `manual-*` is dispatch-only;
  `admin-*` covers non-factory administration, CI and PR/proposal validation.
  Develop and verify behavior manually before reviewed factory promotion.
  Preserve existing autonomous workload and rollout gates during development.
- Approval of a plan authorizes autonomous execution of its planned tasks; do not
  request per-task approval for routine implementation. Create a focused branch/PR
  as required by protected-main flow; enable/use auto-merge when eligible under
  existing repository policy, poll until merged or blocked, then verify and
  fast-forward local `main` before continuing. This does not authorize changing
  protection/required-check/auto-merge policy or bypassing separately named task,
  environment, manual-run, or live-operation gates.
- Module-targeted manual workflows use `res`, `ptn`, `utl`, `force`, and
  `module_names`. Keep meaningful provider/global/exact-identity exceptions;
  selectors and force never replace safety identities or approvals.
- Prefer authenticated `gh pr`/`gh api` for pull requests, not Playwright/browser
  automation, following the GitHub operations standard. For other operations,
  prefer suitable GitHub/GitKraken tools; use authenticated `gh`/`git` fallbacks
  when tools are unavailable, failing or unreliable. Check uncertain prior write
  outcomes before retrying to avoid duplicates. Verify authentication rather than
  assuming it; user login happens directly in the terminal. Preserve all approvals.

- Pin actions to reviewed full commit SHAs. Use `go-version-file: go.mod` and
  mirror the [CI gates](admin-ci.yml), not an independently hard-coded Go version.
- Keep workflow/job permissions explicit and least-privilege. State-writer
  Contents access and reduced projection permissions are separate contracts.
  Preserve environment approvals and protected-main PR delivery.
- Run reviewed trusted code, never code from `factory-state` or an untrusted
  artifact. Separate credential-free compute from trusted validation/sealing.
- Every trusted `factory-*` or `manual-*` job that reads or writes `factory-state`
  uses a separate verified depth-1 checkout in `work/state`, explicit repository
  and `factory-state` ref, state-only App token for checkout, and
  `persist-credentials: false`. Verify origin identity with
  `GIT_NO_REPLACE_OBJECTS=1`. Pass only `--state-branch factory-state` and
  `--state-checkout-root work/state`; `VerifiedGitBackend` derives and verifies
  the live tree internally. Do not shell-generate tree listings or pass transport
  state tokens through a process environment variable. The CLI's temporary credential provider
  mints operation-time credentials. Do not enable unrelated modes or broaden
  discovery scope to obtain state access.
- Reuse verified immutable Git objects across reads; retain live-head checks,
  coordinator-owned decision retries, fencing and lost-acknowledgement recovery.
  Fetch updates when needed; never pull/merge/rebase divergent operational state
  or force-push it. Checkout is transport, not queue authority or permission to
  run state-branch code. Jobs without state access, especially credential-free
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