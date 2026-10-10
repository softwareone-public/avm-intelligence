# Native Issue Lifecycle

The durable `factory-state` branch owns requests, desired generations, claims,
leases, fences, sealed completions, acceptance receipts and selections. The
Project and recurring module Issues are human-facing projections only.

## Current Pipeline

1. Official catalog observations and immutable release/tag/SHA pins create
   durable acquisition work and independent catalog proposals. Discovery does
   not dispatch a worker.
2. An exact-request dispatcher claims native work with expected-head writes and
   monotonic fences. Unknown owners remain parked for verified recovery.
3. Credential-free compute extracts static facts or assesses accepted facts.
   Trusted validation seals completion before releasing ownership; workers do
   not write accepted data.
4. Reviewed evidence merge receipts are imported independently. Facts acceptance
   is not analysis acceptance, selection or deployment.
5. Context-specific assessments require exact accepted provider/policy inputs.
   Selection and export require current input identities and monotonic receipts.
6. Native deployment receipts bind the complete dataset, generation and exact
   analysis/selection membership. Only current verified membership can become Done.

## Projection

One module keeps one recurring Issue with the exact hidden module marker.
Only the factory summary, owned labels and configured fields are managed;
human comments and notes are preserved. The shared lifecycle reducer handles
Backlog, Blocked, Failed, Needs review, active work, retirement and verified Done.
Mixed tracks and newer desired work cannot inherit an old publication success.

Projection uses bounded budgets, snapshots, leases, fences and per-module
checkpoints. The autonomous
[factory-projection](../.github/workflows/factory-projection.yml) workflow
polls durable intents every five minutes and is the only Issue/Project writer.
The schedule is a retry mechanism, not an SLA. Exact-module
[recovery](../.github/workflows/manual-projection-recovery.yml) prepares
state-only intents; [flush](../.github/workflows/manual-projection-flush.yml)
validates existing due intents. Both leave remote delivery to the scheduled
projector and preserve stored retry deadlines.

## Verification Boundary

The local retired-runtime change preserves 232 catalog modules and native source
records, but does not create assessments, selections or presentation snapshots.
Native publication integration and final rollout gates remain open. The
scheduled projector has not been activated. Previous report projection and
publication incidents are historical records in [FINDINGS.md](FINDINGS.md),
not evidence that native delivery is complete.

See [factory operations](factory-operations.md), [decisions](decisions.md), and
[TASK-043/TASK-028](TODOS.md) for authority and outstanding delivery gates.
