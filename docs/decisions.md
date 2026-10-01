# AVM Intelligence Decisions

Owner: Ledermayer. These decisions describe the approved factory boundaries
and the current product milestone. They do not authorize public visibility,
semantic analysis, or autonomous remediation.

| Decision | Status | Record |
| --- | --- | --- |
| SoftwareOne sponsors AVM Intelligence. | Approved | Recorded by Ledermayer on 2026-10-01. |
| Repository, Project, and Pages remain internal until a separate public-content approval. | Approved | No visibility change is included in the factory skeleton. |
| First event date is 2026-10-13. | Approved | The event uses a reduced pilot, not full-catalog intelligence. |
| Event analysis baseline is offline IaCLens. Semantic analysis is preferred only after a bounded public sample and approved budget. | Approved | Semantic mode remains disabled. |
| Public data excludes private client source, reports, screenshots, organization identifiers, credentials, and private Git history. | Approved | Factory fixtures are synthetic. |
| Pilot cohort is one public AVM module before any fleet run. | Approved | No module is selected by this skeleton. |
| One organization GitHub App, `app-avm-intelligence`, is the approved identity. Jobs must mint reduced installation tokens. | Approved | The private key remains in the `data-publisher` environment and is not available to this PR. |
| Project `AVM Intelligence Factory` is the work-tracking projection. | Approved | https://github.com/orgs/softwareone-public/projects/1 |

## Consumer Pin

| Input | Value |
| --- | --- |
| IaCLens release | `v0.1.0` |
| Commit | `c9d714fd6cb7da88ffcb4b049f39480263cc2875` |
| Report schema | 2 |
| Ruleset format | 1 |
| Linux worker asset | `iaclens_0.1.0_linux_amd64.tar.gz` |
| Linux worker SHA-256 | `390ef9d188341c85d42e16adf61cbf6b91effeee9ec0af99c492ab50b08d6238` |

The factory treats `source_digest` as an opaque IaCLens inventory digest, not as
the upstream Git commit. It joins inventory and analysis units by path and
preserves `pass`, `fail`, `unchecked`, and `not_applicable` as distinct results.

## Product Milestone: First Accepted Fleet Cohort

| Item | Record |
| --- | --- |
| Date | 2026-10-01 |
| Workflow run | `36869616628` |
| Accepted pull request | [PR #28](https://github.com/softwareone-public/avm-intelligence/pull/28) |
| Accepted commit | `efdd161535b8152e686c13c9785098bc0b0d15ec` |
| Cohort | 10 offline resource-module analyses: 4 compute, 3 network, 3 platform/management |
| Semantic analysis | Disabled |
| Pages publication | Verified at `https://ideal-adventure-62gqzpm.pages.github.io/` |
| Pages workflow run | `36874979207` |
| Board projection | Pages-complete reconciliation run `36875075805` in progress |

This is a product milestone, not a certification claim. The accepted dataset is
canonical and reviewable; the Project board remains a human-facing projection,
and Pages publication remains a separate approved operation.

## Board Operating Decision

The `AVM Intelligence Factory` Project is the operational projection for human
triage. It is not the scheduler, durable request store, canonical dataset, or
publication authority. Module Issues use one stable hidden module marker and
remain open while the module is active. Status, source identity, outcome, run
URL, and publication commit are reconciled from durable state and accepted
results. Card moves do not authorize work, and human Issue notes are preserved.

The field/status mapping and next implementation slice are recorded in
[`docs/board-operations.md`](board-operations.md).
