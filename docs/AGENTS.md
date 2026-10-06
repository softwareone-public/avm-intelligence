# Documentation And Evidence Guide

Follow the [root guide](../AGENTS.md). Use [decisions](decisions.md) for approved
boundaries, [FINDINGS.md](FINDINGS.md) for findings/incidents, and
[TODOS.md](TODOS.md) for actionable tasks and verification gates.

- Record every substantive new finding in the findings register and every
  actionable follow-up in the TODO register. Update an existing matching entry
  instead of creating duplicate registers or conflicting task lists.
- Keep source-proven behavior separate from observed production incidents.
  Include affected paths or exact run/commit/receipt evidence, current status,
  and the owning task. Do not claim a consequence occurred without evidence.
- Preserve historical evidence when a fix lands; append its resolution and
  distinguish local tests, reviewed implementation, merged code, activation,
  and verified live postconditions.
- Reuse authoritative v2 `TASK-*` and `TEST-*` IDs. Synchronize changed task
  status with the parent delivery plan only when that update is in approved
  scope; flag discrepancies rather than inventing IDs or marking completion.
- Do not turn user authorization into proof of execution, a successful workflow
  into proof of acceptance/deployment, or a fixture gate into product acceptance.
- Link contracts and evidence rather than copying them. Keep provider/runtime
  compatibility claims within the actual extracted and accepted evidence.
- When verified conventions change, update the affected agent guide and
  [Go instructions](../.github/instructions/go-development.instructions.md)
  in the same scoped change. Temporary failures are not permanent conventions.