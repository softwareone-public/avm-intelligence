# AVM Intelligence Decisions

Owner: Ledermayer. These decisions authorize the factory skeleton only. They do
not authorize publication, public visibility, semantic analysis, or autonomous
remediation.

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
