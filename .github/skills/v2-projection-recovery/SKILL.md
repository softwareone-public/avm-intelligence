---
name: v2-projection-recovery
description: 'Use when retrying AVM Intelligence V2 Issue or Project projection for existing durable lifecycle intents, especially after a projection workflow failure. Never use this to rerun discovery or dispatch workers.'
argument-hint: 'Canonical AVM module IDs to project, comma-separated'
---

# V2 Projection Recovery

Recover selected AVM Intelligence Issue/Project projections from existing
`factory-state` lifecycle intents. This is projection-only: it must not discover
catalogs, enqueue acquisition, alter accepted facts, or dispatch workers.

## Preconditions

1. Confirm the user requested recovery and has supplied exact canonical module
   IDs. Do not infer a wider cohort from an Issue, Project view, or failed run.
2. Verify the GraphQL/lease fix required for the failure is merged to `main` and
   `.github/workflows/v2_projection_recovery.yml` is active there.
3. Inspect each module's durable lifecycle and projection records on
   `factory-state`. Confirm the module is active and has a lifecycle intent.
   Stop if the lifecycle/catalog records are missing or conflict.
4. If delivery is already `projected` and its acknowledged fingerprint matches
   the desired fingerprint, report it as converged and do not dispatch again.
   If it is `projecting` with an unexpired lease, wait for that owner; do not
   start a competing run. For `needs-review`, identify and fix the underlying
   cause before retrying. Never retry a permanent error blindly.

## Recovery

1. Dispatch only the manual recovery workflow, using the explicit module list:

   ```sh
   gh workflow run .github/workflows/v2_projection_recovery.yml \
     --repo softwareone-public/avm-intelligence \
     --ref main \
     --field module_ids=avm-ptn-aca-lza-hosting-environment
   ```

   Replace the example with the approved module ID(s). Use the available
   authenticated GitHub workflow-dispatch integration when provided; use `gh`
   only when there is no suitable workflow tool. The workflow uses the approved
   `data-publisher` environment and mints its own reduced tokens. Never request,
   print, or copy secrets into chat.
2. Monitor that one run through completion. Do not launch duplicates while it is
   active. Inspect the completed `Project selected lifecycle intents` log on
   failure before deciding whether code or permissions need correction.
3. On success, read the module's
   `state/projection/delivery/<module-id>.json` record from `factory-state` and
   require `delivery_state: projected`, a matching acknowledged fingerprint,
   and no active lease. Confirm the run's actual Issue summary contains the
   exact module marker, desired lifecycle status, and projection acknowledgement
   marker; confirm the managed status label matches.
4. A successful durable projection checkpoint means the projector completed its
   Issue, configured Project-field, and label synchronization path. If a
   personal token lacks `read:project`, state that Project fields were not
   independently read; do not claim a direct board audit.
5. Confirm the targeted acquisition request and accepted facts are unchanged
   and that no worker run was dispatched. Update the owning incident entry in
   `docs/FINDINGS.md` and the checkpoint in `docs/TODOS.md` with run IDs, final
   delivery state, and any verification limits.

## Stop Conditions

- Do not use `v2_discovery.yml` for recovery: manual discovery may create another
  acquisition generation.
- Do not use `project-accepted.yml`: it projects the broad accepted cohort, not
  the selected lifecycle intents.
- Do not dispatch `v2_dispatch.yml`, `v2_worker.yml`, or any analysis or
  publication workflow as part of projection recovery.
- If the run fails, preserve the durable state and Issue history. Fix the exact
  error in a reviewed PR, wait for it to merge, then retry only the affected
  module IDs.

For lifecycle authority and rollout constraints, see
[`docs/decisions.md`](../../../docs/decisions.md),
[`docs/board-operations.md`](../../../docs/board-operations.md),
[`docs/FINDINGS.md`](../../../docs/FINDINGS.md), and
[`docs/TODOS.md`](../../../docs/TODOS.md).