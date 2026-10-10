# Dimensions And Checks

This guide documents the current AVM Intelligence collection and assessment
path, compares it with `templatespec-metadata`, and gives the repository
workflow for adding or moving compiled checks. The comparison is architectural:
AVM Intelligence does not copy TemplateSpec's YAML marker storage, Python
runner loading, or score aggregation.

## System Flow

```text
Pinned source revision
        |
        v
internal/terraform.ExtractFacts
        |
        v
Immutable facts-v3 record + capability coverage
        |
        +--> compiled dimension/check registry
        |       |
        |       v
        |    independent dimension results
        |
        +--> source/provider snapshot inputs
                |
                v
             dependent dimensions only
```

Facts collection is source-bound and syntax-only. Assessment consumes accepted
facts and optional immutable snapshots; it does not read Terraform source,
invoke Terraform/providers, or make network requests. Each dimension result
binds its definition, facts, unit, classification, and only the snapshots and
target versions the dimension actually uses. Missing optional inputs produce
`unchecked` with `missing_input`; they do not erase completed sibling dimensions.

Before invoking a check, the engine gates it on the union of its dimension's
and check's required facts capabilities. A failed gate records an unavailable
check result instead of running an evaluator with incomplete facts.

The entry points are `internal/terraform/extract.go`,
`internal/assessment/assessment.go`, and the offline flags in
`cmd/factory/main.go`.

## Collection: Terraform Metadata

### TemplateSpec Implementation

In `templatespec-metadata/scripts/scrape_module.py`,
`scrape_terraform_metadata(module_dir)` is the source-level scrape function:

1. `read_tf` concatenates sorted, direct-child `*.tf` files. It does not recurse
   into nested directories and does not read `.tf.json` files.
2. `strip_comments` drops lines whose trimmed text begins with `#` or `//`.
   It does not lex Terraform strings or remove inline comments.
3. Regex and brace-counting helpers extract `required_version`, provider
   `source`/`version_constraint`, resource/data/ephemeral/action labels,
   namespaced provider functions, module names, variable names, and output
   names. `group_by_provider` groups resource-like names by a naming-prefix
   convention; it does not resolve provider bindings from Terraform syntax.
4. The result is a mutable dictionary with `checked_at`, `status`, `errors`,
   `terraform_constraints`, symbol inventories, module names, variables, and
   outputs. `status` is `pass` for any nonempty uncommented input and
   `unchecked` for empty input; parse limitations do not independently change
   that status.
5. `update_module_scrape` first calls building-block discovery, then runs the
   scrape once per discovered block directory. `_apply_entity_block` locks the
   module YAML, migrates markers, ensures/replaces the entity-specific block,
   and writes the file. A scrape failure for a later dimension does not roll
   back earlier dimension writes.

The scrape registry in `scripts/scrape/registry.py` describes dimensions,
YAML keys, entity types, marker names, and runner names. The current
`scrape_module.py` CLI still dispatches its supported dimensions with hard-coded
branches; the scrape registry is not yet its runtime dispatcher. The analysis
registry is active: `analyze_module.py` validates selected names, imports each
registered runner, passes a `BuildingBlockContext`, and persists one analysis
marker per entity and dimension. The default scrape registry currently lists
`terraform-metadata`, `repository-status`, and `project-status`; the CLI also
handles `building-block-inventory` and `pipeline-inventory` through direct
branches rather than registry entries.

These are useful separation and inventory patterns, not parsing behavior to
copy. Regex parsing can miss indented/multiline declarations, confuse strings
and comments with syntax, and infer provider ownership from names. The AVM
extractor uses the maintained HCL parser and explicit syntax nodes instead.

### AVM Intelligence Equivalent

`terraform.ExtractFacts` is the AVM collection boundary. It receives the
immutable module ID/SHA and explicit `UnitInput` paths/files; callers, not the
parser, choose root, child-component, and example units. It then:

1. Validates module/SHA identity, unit roles, safe relative paths, and the
   explicit file list. Source files must be regular files without symlink path
   components.
2. Parses `.tf` with HCL native syntax and `.tf.json` with HCL's JSON parser.
   It never evaluates expressions. It extracts static provider requirements
   and version constraints, provider aliases/bindings, resource/data/ephemeral/
   action use, nested core and provider-qualified HCL function calls, module
   source/version/provider mappings, variable/output description presence,
   Terraform backend/cloud/`required_version` declarations, and exact
   provider selections from explicitly discovered `.terraform.lock.hcl` files.
3. Omits values and arbitrary expressions. Description checks receive only a
   `description_present` boolean. Terraform settings record the setting kind
   and backend type, never the configured value. Dynamic/unsafe facts lower
   capability coverage instead of being guessed.
4. Builds a deterministic `facts-<sha256>` identity and content digest under
   the standalone strict facts-v3 schema. Native facts are not appended to
   module YAML. Source facts and native analysis results have separate identities.

The v3 symbol contract lives in `schemas/factory-facts-v3.schema.json`. V2
factory data remains frozen for historical readers. HCL function calls are
syntax-traversed without evaluating their arguments; provider-qualified calls
are stored with full provider addresses. Terraform JSON ephemeral/action blocks
are extracted, but function-call traversal for JSON expressions remains partial.
Unknown top-level blocks, unsupported provider-function identities, missing
lockfiles, and lockfiles missing statically required providers never count as
complete coverage. Lock selections are a separate `provider_lock` fact and are
not conflated with declared constraints or inferred consumer installations.

## Current Assessment Registry

The registry is compiled into `internal/assessment`. Definitions name their
scope, ordered check IDs, required capabilities, policy ID, and evidence schema
version. Checks name their dimension, applicability rule, required
capabilities, severity, version, and a compiled implementation ID. No scripts,
plugins, or external policy file can add executable checks at runtime.

| Dimension | Unit scope | Checks | Inputs |
|---|---|---|---|
| `provider-requirements` | Repository units | `declared-provider-sources` | Provider-requirements facts |
| `static-facts-coverage` | Components | `static-facts-inventory` | Additional-kind and lock-selection coverage |
| `provider-type-catalog` | Repository units | `provider-type-inventory` | All provider-backed type facts and matching per-kind snapshots |
| `provider-currency` | Repository units | `provider-version-constraint` | Provider constraints, exact accepted snapshots, and context target versions |
| `interface-documentation` | Components | `input-descriptions`, `output-descriptions` | Description-presence facts |
| `module-ownership` | Components | `module-state-is-caller-owned`, `module-cloud-is-caller-owned` | Terraform setting declarations and module classification |
| `deployment-core-version` | Examples | `deployment-core-version` | Terraform setting declarations and deployment classification |

Classification is deliberately conservative. Root/modules/examples paths
establish unit role and ownership, not code kind. A module/deployment kind
requires an explicit override, except an example can be classified as a
deployment from a backend/cloud declaration. Unknown classification remains
`unchecked/missing_input` for classification-dependent checks. CLI overrides
are a strict JSON map from unit path to `module`, `deployment`, or `unknown`;
the classification is included in the result input fingerprint.

## Check Inventory

| Check | Applicability and input | Result semantics | Evidence |
|---|---|---|---|
| `declared-provider-sources` | Every unit; provider-requirements capability | `pass` when a provider requirement is declared; otherwise `fail`. Incomplete requirements are `unchecked`. Provider-free cases may need explicit policy customization; no consumer installation is inferred. | Requirement declaration location/name; usage locations when present and available |
| `static-facts-inventory` | Every component; additional-kind and lock-selection coverage | `pass` only when both capabilities are complete; otherwise `unchecked`. A missing lockfile is not proof that no provider version is selected. | No evidence entries yet; the result message explains the capability gap |
| `provider-type-inventory` | Units with provider-backed resource/data/ephemeral/action/function use; provider snapshot by full address and kind | `pass` when complete per-kind inventories contain used types; `fail` only when complete coverage omits a type; missing/partial snapshots are `unchecked`. Core functions are `not_applicable` to provider inventory. | Source location, kind, type, and provider address |
| `provider-version-constraint` | Units with provider requirements; exact accepted target vector | `pass` when each accepted target snapshot version satisfies its declared constraint; `fail` when a selected target violates it; missing target/snapshot is `unchecked`. It does not infer consumer installation from example lockfiles. | Provider declaration and exact target version |
| `input-descriptions` | Reviewed module classification; interface metadata | `pass` when every variable has a nonempty description; `fail` when any is absent/empty; unavailable description metadata is `unchecked`; no variables is `not_applicable`. | Variable declaration path/line/name |
| `output-descriptions` | Reviewed module classification; interface metadata | Same semantics as input descriptions for outputs. | Output declaration path/line/name |
| `module-state-is-caller-owned` | Reviewed module classification; Terraform settings | `fail` if `terraform.backend` is declared; otherwise `pass`. | Backend declaration path/line/type |
| `module-cloud-is-caller-owned` | Reviewed module classification; Terraform settings | `fail` if `terraform.cloud` is declared; otherwise `pass`. | Cloud declaration path/line |
| `deployment-core-version` | Reviewed deployment classification; Terraform settings | `pass` if `required_version` is declared; otherwise `fail`. Unknown kind is `unchecked`. | `required_version` declaration path/line, never the constraint text |

`module-purpose` is not implemented or claimed by the compiled registry.
AVM Intelligence does not compute a dimension score or load external checks.

The offline facts v3 model includes only presence/kind facts needed by the
compiled checks: `description_present` for variables/outputs, `setting` plus
backend type for `required_version`, `backend`, and `cloud`, separate provider
constraint and lock-selection fields, and source locations for typed provider
usage. It does not retain description text, lock hashes, backend configuration,
or output expressions.

## Native Evidence Contracts

`site.BuildIndexed` renders accepted typed records. If no presentation snapshot
has been accepted, it derives a temporary catalog-first view at the accepted
dataset timestamp without writing data or presentation history; the site labels
that view as derived. Once present, an accepted presentation snapshot remains
validated against exact catalog membership. The builder does not read
normalized reports or operational state.
`github.FromReducedStatus` consumes the shared native lifecycle reducer;
publication requires exact current analysis/selection/deployment membership.

The reviewed [native check fixture](../testdata/assessment/native-checks.json)
contains explicit statuses, severities and source-location expectations without
producer binaries, raw output or compatibility parsing. Native check evidence
can add declaration names/columns; provider requirement locations are checked at
file level. Provider-free behavior remains an explicit policy caveat, not an
inferred pass or consumer compatibility claim.

## CLI Workflow

```bash
go run ./cmd/factory --list-dimensions
go run ./cmd/factory --list-checks interface-documentation
go run ./cmd/factory \
  --assess-facts facts.json \
  --dimension-ids interface-documentation,module-ownership \
  --classifications reviewed-unit-kinds.json \
  --assessment-out dimension-results.json
```

`--provider-snapshots` optionally accepts a strict JSON array of v2
`provider_snapshot` records. With no `--dimension-ids`, all compiled dimensions
are selected. Results are emitted to stdout unless `--assessment-out` is set.
These commands are offline and do not persist records or activate a producer.

## Add Or Move A Check

1. **Confirm source facts exist.** If the check needs new data, add a typed
   facts-v3 field and extractor conformance test first. Never parse Terraform
   again inside an evaluator. Store only the minimum fact needed; source values
   and expressions must remain absent unless separately approved.
2. **Add a versioned definition.** Add a stable check ID, version, dimension,
   applicability, required capabilities, evidence schema version, severity,
   and a compiled implementation ID in `DefaultRegistry`.
3. **Register the compiled evaluator.** Add the implementation to
   `compiledEvaluators`. It consumes only supplied facts/snapshots and returns
   a `CheckResult`; missing prerequisites are `unchecked/missing_input`, not
   `fail`.
4. **Update the dimension membership.** A check has one owning dimension. To
   move it, remove the ID from the old definition and add it to the new one.
   Keep the check ID/version if semantics are unchanged; increment its version
   if meaning or evidence changes. Dimension-definition identity changes when
   scope, required capabilities, policy, or membership changes.
5. **Add reviewed tests.** Cover pass, fail, not-applicable, missing input,
   execution error, deterministic identity/reuse, and reviewed native fixture parity
   where semantics are unchanged. Add a schema-shape test for any new record
   fields.
6. **Verify list/select behavior.** Confirm `--list-dimensions`,
   `--list-checks`, and `--assess-facts --dimension-ids ...` expose the updated
   registry while leaving unselected dimensions and historical records
   untouched.

The TemplateSpec comparison is recorded in terms of current source behavior;
it is not a requirement to introduce YAML markers, weighted scores, dynamic
runner imports, or network checks into the AVM factory.
