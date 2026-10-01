# AVM Intelligence

AVM Intelligence discovers Azure Verified Modules, analyzes approved source
revisions with the released [IaCLens](https://github.com/Ledermayer/iaclens)
CLI, and publishes reviewed intelligence. The factory now includes deterministic
fleet planning, validated candidate acceptance, and an evidence-first static site;
full SoftwareOne branding and live publication remain gated.

The current pin is IaCLens `v0.1.0`, report schema 2. The skeleton validates
that pin and normalizes reports without GitHub write credentials, publication,
or semantic analysis. See [docs/decisions.md](docs/decisions.md) for the approved
event scope and [docs/TODOS.md](docs/TODOS.md) for the remaining factory work.

```sh
go test ./...
go run ./cmd/factory --show-toolchain
```

The scheduled `Fleet plan` workflow performs a read-only dry run: it discovers catalogs,
resolves immutable source pins, builds bounded request batches, and uploads the plan as
an artifact. A manual run can opt into analysis workers with `dispatch=true`;
scheduled runs never dispatch workers. Manual runs also select resource, pattern, and
utility modules independently. Worker artifacts are transient and retained for two
days; accepted bundles remain available longer for review and are not reused as inputs
to a different run. For fast tests, provide a comma-separated `module_ids` list in
manual dispatch; the list is applied in addition to the module-type checkboxes.

The `Publish Pages` workflow checks out an exact accepted commit, builds the site from
accepted data, creates a provenance manifest, and deploys only after the `data-publisher`
environment approval.

The fleet writer submits accepted data through a pull request; it does not bypass the
repository's pull-request rule to push directly to `main`.

## Current Milestone

The first accepted fleet cohort is canonical at commit `efdd161` from workflow run
`36869616628`: ten offline resource-module analyses covering four compute, three
network, and three platform/management modules. Pages is now deployed at
https://ideal-adventure-62gqzpm.pages.github.io/. The Pages-complete Project
reconciliation is running; blocked/retired lifecycle reconciliation remains.
See [board-operations.md](docs/board-operations.md) for the operating model.
