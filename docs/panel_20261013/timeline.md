# Terraform Metadata Project Timeline

Updated 2026-10-12. This is a historical narrative of four iterations of the
same idea: treat a fleet of Terraform modules as data, extract what the source
declares, and turn that into something a platform team can review. Dates come
from each repository's git history. A commit date is not proof that a live
workflow, acceptance, or publication succeeded.

The idea stayed stable. The runtime did not.

## Where it started

In early May 2026 I was asked to evaluate Azure Verified Modules, and the ways
a customer could adopt them instead of keeping a Terraform module base of their
own. That raised the question the rest of this timeline is spent answering:
how good are those modules, actually? They are published as separate
repositories, so "good" is not a page in the catalog. It means currency, test
coverage, the required interface, pinned providers, and whether a platform
team can see the fleet without cloning it by hand. avm-metadata was the first
attempt to measure that. Each later iteration kept the question and replaced
the way it was answered.

| Iteration | Repository | Span | What it was |
| --- | --- | --- | --- |
| 1 | [Crayon-HU/avm-metadata](https://github.com/Crayon-HU/avm-metadata) | 2026-05-12 to 2026-05-19 | Public, now archived. Left behind when the work moved to softwareone-public. |
| 2 | private-modules-metadata | 2026-06-03 to 2026-09-15 | Same pipeline for a private Azure DevOps fleet: 147 modules across Azure, AWS, and GCP, plus git and Azure DevOps metadata. |
| 3 | [Ledermayer/iaclens](https://github.com/Ledermayer/iaclens) | 2026-09-27 to 2026-09-30 | Public sidequest. Go parser plus TypeSafe System One / Jev classifier. |
| 4 | [softwareone-public/avm-intelligence](https://github.com/softwareone-public/avm-intelligence) | 2026-09-28 to present | Go factory. Compiled facts and checks; the model path was dropped. |

```text
Early May 2026    Evaluation task
                  Can customers adopt AVM instead of maintaining their own
                  Terraform module base? How good are the modules?

May 12–19 2026    avm-metadata
                  Python catalog, regex scrape, seven analysis dimensions,
                  GitHub Pages portal. Demoed, then archived.

Jun–Sep 2026      private-modules-metadata
                  147 Azure, AWS, and GCP modules on Azure DevOps. Terraform
                  plus git and host metadata. Dimensions bundle company
                  standards, not only industry practice.

Sep 15 2026       Jev
                  TypeSafe announces System One models. A classifier that
                  returns a choice and a probability, not a paragraph.

Sep 27–30 2026    iaclens
                  Sidequest. Go parses HCL. Jev classifies module vs
                  deployment and answers enabled semantic checks.
                  v0.1.0 on 2026-09-30.

Sep 28 2026       softwareone-public
                  Moved into the new org and started setting up the
                  environment. avm-intelligence is created there.
Oct 1             v1 fleet: discovery, workers, accepted data, Pages, board
Oct 1–2           v2 approved; phases 0–4 land as one production path
Oct 4–6           native facts accepted; IaCLens and the old runtime retired
Oct 7–9           sealed evidence, Git state transport, projection split
Oct 10–12         partial portal, ten-module provider inputs, 70 assessments queued
```

## 1. avm-metadata — 2026-05-12 to 2026-05-19

The evaluation could not be done from the AVM catalog page. Each module is its
own repository, so the first artifact was a workspace that could sync the
fleet and score it. Public archive: https://github.com/Crayon-HU/avm-metadata.
Nine commits. The first commit is 2026-05-12. Setup and the GitHub Pages portal
landed the next day. Provider-currency analysis and a demo script landed on
2026-05-19, and that is the last commit.

The repository holds no Terraform of its own. `./avm.sh` is a thin wrapper
over Python. It syncs the official AVM index CSVs into one YAML file per
module, clones the upstream `terraform-azurerm-avm-*` repositories into a
VS Code multi-root workspace, and analyzes them. The demo script, written for
a Microsoft Budapest session, framed the problem as a platform team tracking
148 separate module repositories: currency, test coverage, required AVM
interface variables, and pinned provider versions.

What shipped in that week:

- Catalog sync from the upstream AVM CSVs, with `Available` as the default
  filter and an opt-in for `Proposed`.
- Clone, fetch, status, branch, stash, and cleanup across the workspace.
- Seven analysis dimensions, including test coverage, documentation quality,
  and provider currency. Provider currency did not need a clone.
- A static portal (`scripts/generate_site.py`, Observable Plot) with a catalog,
  quality scoreboard, provider-currency findings, a resource treemap, known
  issues, and a 90-day activity feed.
- Copilot skills (`/avm-sync`, `/avm-check`) that called the same scripts.
  There was no model-only path.

The ideas list already named the next product: sync findings to GitHub Issues
and a Project board (Triage, Bulk Fix, Agent Fix, Blocked, Done). That idea
is the board projection that avm-intelligence later built. The analysis itself
was still a local Python scrape, not a durable queue.

The repository is a public archive because of an organization change. The work
had been started under Crayon-HU. On 2026-09-28 we moved to a new public
organization, softwareone-public, and continued there. Crayon-HU kept this first
iteration readable and stopped taking changes. The archive is the record of
where this started, not a second home for it.

## 2. private-modules-metadata — 2026-06-03 to 2026-09-15

Private Azure DevOps repository for a client module fleet. The organization,
project, and URL are omitted. Thirty-one commits, from a README on 2026-06-03
to the last ticket-linking fix on 2026-09-15. It is the longest-lived iteration
and the one that had to work against a real private fleet. The dates below are
the days the feature first appears in git, the same way the factory dates are
recorded. A commit is the feature landing, not proof that a pipeline run
succeeded.

I started the same solution for one customer whose modules we already manage,
this time against Azure DevOps. The catalog holds 147 modules: 86 Azure, 28
AWS, 26 GCP, and 8 that are none of those three (shared, data-platform, and
other). One module is classified under more than one platform. Instead of
public AVM CSVs, sync discovers `terraform-*` repositories, writes
`data/modules/<repo>.yaml`, and needs the client network plus an authenticated
CLI session.

### 2026-06-03 — README

The first commit is the README. Nothing else is in the tree yet.

### 2026-06-03 to 2026-08-11 — spare time

Git has nothing between these two dates. The work was happening anyway. The
catalog sync, the scrape and analysis registries, the ten analysis runners, the
building-block discovery, the resource index, the report command, the portal,
and the bash entry point were all built in spare time, outside the repository
history. 2026-08-11 is the day that work was committed, not the day it was
started.

### 2026-08-11 — workspace, CLI, and the dimension system

The spare-time work lands as the initial commit, and the metadata skill lands
the same day. This is the day the shape of the tool enters the repository:
catalog sync, repo management, the scrape and analysis registries, the resource
index, the report command, the static portal, and the building-block discovery
that classifies repository shapes. All ten analysis runners are in this commit.

The people working in that fleet use both macOS and Windows, so the Python
scripts are not the interface. The bash entry point lands with the init. It
exposes sync, setup, clone, scrape, analyze, index, report, and the portal,
and each command dispatches to one Python file. There is no second
implementation behind it.

### 2026-08-24 — PowerShell entry point, and a fresh scrape

The PowerShell entry point lands, with the same commands and the same flags as
the bash script. A Windows user and a Mac user now run the same pipeline, and
neither of them has to know which Python file does the work. The same day the
scraped module data is refreshed.

### 2026-08-25 — provider status

Provider collection is split into its own scrape: release notes, open issues,
and the optional MCP probe. This is the collector that provider health and
provider concurrency read. Without it, those checks stay `unchecked`.

### 2026-08-26 to 2026-08-27 — index stubs and the upgrade graph

The resource stubs are repaired and a full index run is recorded. The Azure
DevOps pipeline that will later file the work is added on 2026-08-26. On
2026-08-27 the upgrade graph, the per-module upgrade report, and the ticket
writer land. The graph, the priority rules, and the ticket phases described
below start here.

### 2026-09-10 — fresh collection

The module data is scraped again. No new collector; the existing ones are run
over the fleet.

### 2026-09-14 to 2026-09-15 — tickets, priorities, and links

The pipeline learns to inventory the parent ticket and to create or update one
subtask per module. Priority stops being a single value: modules are spread
across the five levels. On 2026-09-15 example dependencies join the graph, the
dry run covers the create, update, and link phases, and the link types are
resolved from the tracker instead of being assumed. The last commit, the same
day, fixes the ticket description. That is the end of this repository's
history.

Collection is not limited to Terraform code. A scrape dimension has its own
collector, its own scope, and its own marker block. It records metadata and a
collector status. It does not score the module. The checks that decide pass or
fail live in the analysis dimensions and read what the scrapes left behind.
Six collectors cover the fleet:

- `terraform-metadata`, once per building block. Reads the block's `*.tf` files
  after stripping comment lines and inventories the Terraform surface:
  required version, providers and their constraints, managed resources, data
  sources, ephemeral resources, actions, provider functions, module calls,
  variables, and outputs. Composition calls stay separate from example calls.
  This is the input for provider health and provider concurrency. It is not a
  verdict, and it is not a parser: it
  does not lex strings, recurse into nested modules, or resolve provider aliases.
- `building-block-inventory`, once per repository. AVM made this unnecessary:
  one repository held one Terraform module at its root, so the catalog entry
  and the module were the same thing. This fleet does not. Discovery walks the
  clone for `anchor.module` files and classifies the repository into one of
  four shapes:
  - `single-block` — one anchor at the root and no internal modules. This is
    the AVM shape, and the only one the first iteration had to handle.
  - `multi-block` — more than one anchor. A repository can carry several
    public modules, each in its own directory, and the root itself may not be
    one of them. A key-vault repository, for example, is five sibling modules
    rather than a root plus children. Each anchor becomes its own building
    block, with its own scrape and its own analysis.
  - `parent-with-submodules` — one public anchor, plus directories under
    `modules/` that have Terraform files but no anchor. Those are internal.
    They are recorded and scraped with their parent; they are not catalog
    entries and they are not scored as if they were callable on their own. A
    front-door repository is this shape: one public module, four internals.
  - `mixed` — several public anchors and internal submodules in the same
    repository. The public blocks are scored on their own, and the internals
    stay attached to the repository rather than being promoted.
  A clone with no anchor still produces one block, marked as a catalog
  fallback, so a repository that has not adopted the anchor file is visible
  instead of disappearing. The analysis runner iterates the discovered blocks.
  It never assumes that a repository path is a module path.
- `repository-status`, once per repository. Reads the local clone for branches
  and tags, and queries Azure DevOps for branch policy, build validation,
  status checks, required reviewers, and repository security. The collector
  status is the worst of the git read and the Azure DevOps read. DevOps
  governance checks this block and stays `unchecked` when the scrape has not
  run.
- `pipeline-inventory`, once per repository. Finds pipeline YAML in the clone
  and classifies each file by kind: execute, test, semantic release, tag,
  checkov, tfsec, or unknown. Pipeline compliance, examples and test coverage,
  security hardening, and documentation and release quality all look here
  instead of re-scanning the tree.
- `project-status`, once per workspace. Records the project-level default-branch
  policy and the build-definition inventory, so a rule that is inherited by
  every repository is collected once rather than copied into 147 module files.
- `provider-status`, once per workspace. From the providers that
  `terraform-metadata` already found, it fetches release notes and open issues
  and writes them onto the resource stubs the index created. It counts
  providers, stubs, enriched resources, and releases processed. Provider health
  and provider concurrency then ask whether a pinned version is current and
  whether a resource type has a breaking change or a critical issue since it
  was last reviewed. Those checks stay `unchecked` when this scrape is missing
  or stale.

Analysis is the other half. Each dimension has its own runner, its own weight,
and its own marker block, written once per building block. A check returns
`pass`, `fail`, `partial`, `missing`, `info`, `unchecked`, or `not_applicable`,
with evidence and the standard it enforces. Eligible dimensions roll those up
into one weighted score. A network check that cannot reach Azure DevOps stays
`unchecked` and drops out of the score instead of failing the module. The ten
dimensions, 56 checks in all:

- `repository-layout`, weight 4, local. The files a module is expected to have:
  a README, a changelog, a terraform-docs config, an `examples/` directory, a
  pipeline directory, and at least one of the canonical `.tf` files. This is
  the hygiene bar, not a judgment of the Terraform itself.
- `interface-compliance`, weight 4, local. Reads the `.tf` files. Requires
  `variables.tf` and `outputs.tf`, a description on every variable and output,
  and at least one `validation` block. A module another team is expected to
  call has to document its contract.
- `pipeline-compliance`, weight 4, network. Reads the pipeline inventory.
  Requires a local pipeline, an execute pipeline, and a test pipeline, then
  asks Azure DevOps whether a build definition exists for the execute file.
  That last check is `unchecked` when the network is excluded. The standard
  here is the team's release path, not a public Terraform convention.
- `examples-test-coverage`, weight 3, local. Requires `examples/` with at least
  one `.tf` file and example variables, plus a terraform-test pipeline from the
  inventory. An example that cannot be executed by the pipeline does not count
  as coverage.
- `security-hardening`, weight 4, local. Scans the tree for secret-shaped
  values and reports only the path and the pattern name, never the value. Also
  requires a checkov pipeline and a checkov or tfsec config. The secret check
  is the one marked critical.
- `provider-health`, weight 3, network. Reads declared providers and module
  sources. Requires a `required_providers` block, a source on every provider,
  no upper-bound constraint, and a minimum version per provider: azurerm at
  least 3, aws and google at least 4, with floors for azapi, databricks, and
  the smaller utility providers. Git module sources must be pinned to a ref.
  Two checks need `provider-status`: whether the provider exists in the
  internal proxy, which stays `unchecked` until that probe exists, and whether
  the pin meets the latest known release, which stays `unchecked` when the
  project scrape is missing or older than 48 hours.
- `doc-release-quality`, weight 2, local. README and changelog again, plus a
  semantic-release pipeline and a tag pipeline. The repeated file checks are
  deliberate: layout asks whether the file exists, this dimension asks whether
  the release automation keeps it current.
- `devops-governance`, weight 3, network. Reads `repository-status` and does
  not query Azure DevOps itself. Three checks: the default-branch policy is
  known, repository security is known, and required reviewers are known. All
  three stay `unchecked` until that scrape has run. Governance is a fact about
  the host, so it cannot be answered from `.tf` files.
- `module-taxonomy`, weight 1, local. The lightest dimension on purpose.
  Requires structured anchor metadata, at least one platform, a domain that is
  not `uncategorized`, and a usage role. It keeps the catalog classifiable. It
  is not a quality gate, which is what the weight says.
- `provider-concurrency`, weight 3, network, not score-eligible. For each
  resource, data source, function, ephemeral, and action in
  `terraform-metadata`, it loads the matching stub and compares provider
  releases and open issues with the last time a person reviewed that symbol.
  Pending breaking changes and open critical issues fail. Enhancements and
  high issues are informational unless policy says otherwise. A missing stub or
  an unknown review date is `unchecked`, not a failure. Currency findings stay
  visible without moving the headline score.

That split is why the system exists. A fleet this size cannot be a list of
one-off scripts. Adding a check means registering it under a dimension, not
inventing a new command. A dimension can encode the company standard — the way
this team writes, pipelines, reviews, and releases modules — separately from an
industry best practice. Those rules are checks because the team decided they
are, not because a generic Terraform guide says so.

Storage is the important design choice. Generated YAML uses marker blocks so
a tool can replace its own section without touching hand-written enrichment:

- `# BEGIN CATALOG` — Azure DevOps repository metadata, owned by `sync`.
- `# BEGIN SCRAPE:*` — one transaction per collector, at building-block,
  repository, or project scope, as listed above.
- `enrichment:` — preserved by tooling.
- `legacy.analysis_module_quality` — archived scores after the live analysis
  was removed pending a later re-analysis.

An AzureRM v5 upgrade is one thing this data can answer, not the reason the
data was collected. The scrapes and the analysis describe the fleet. What a
team does with that description is a later question, and a major provider bump
is one of them: across 147 modules it is an ordering problem. From 2026-09-14
an Azure DevOps pipeline plans that order and files it as work, in three
phases, with a dry run as the default. The same records could feed a portal, a
review, or a different upgrade. This pipeline is one consumer.

The graph is built from the module calls that `terraform-metadata` already
separated. A composition call is a hard dependency. A call that only appears
inside `examples/` is an example dependency. Both are resolved back to catalog
names, cycles are detected, and the rest is sorted so a module is upgraded
before the modules that call it.

Priority is not a person's guess and it is not the analysis score copied into a
ticket. A module that many others depend on ranks higher. A module inside a
cycle is at least High, because there is no safe end of that loop to start
from. A breaking change or a critical finding from the provider analysis lifts
the priority one more step, up to Highest. A module nobody depends on, with
nothing breaking, stays Lowest.

The pipeline then inventories the parent, creates or updates one subtask per
module, and links those subtasks with the same graph. A composition edge
becomes a blocks link. An example edge becomes a required-for link. The link
type is resolved from the tracker at runtime rather than assumed. Analysis
decides how urgent the work is. The dependency graph decides what it has to
wait for.

The shape classifier was the part worth keeping, and the part that did not
yet work without help. It depends on an `anchor.module` file that this fleet
agreed to commit. A repository that has not committed one falls back to a
single guessed block. The next iteration takes the same problem — a directory
of Terraform is not the same thing as a repository — and answers it from the
code, with no anchor file and no convention that is local to one customer.

avm-intelligence later kept the dimension/check split and the status
vocabulary. It did not keep YAML markers, dynamic runner loading, score
weights, or the regex scraper. The comparison is written up in
[dimensions and checks](../dimensions-and-checks.md).

## Story time — 2026-09-15, Jev

Twelve days before the iaclens experiment, TypeSafe came out of two years in
stealth and published [Introducing System One Models & Jev](https://www.typesafe.ai/blog/introducing-system-one-models-and-jev).
The post is by Diogo Almeida, who had worked on the research behind ChatGPT.
Hacker News carried it the same day. The claim was not another chat model. It
was that chat had been the wrong interface for software.

The models everyone had been calling through an API were trained to produce
text a person prefers. Reinforcement learning from human feedback is good at
that, and it is also why the text sounds sure of itself when it is not. Ask a
chat model whether a directory is a module or a deployment and it writes a
paragraph. The paragraph can contain the right word, the wrong word, a
refusal, or a shape of JSON you did not ask for. Your code then has to parse
the paragraph and pretend the parse is a decision. That is a person-shaped
interface with a program stuck on the end of it.

System One, named for the fast intuitive half of Kahneman's *Thinking, Fast
and Slow*, is trained the other way. Reinforcement learning for calibrated
decisions does not reward a pleasing sentence. It rewards a probability that
matches how often the answer is actually right. A 0.8 should be right about
four times in five, across many predictions. It is not a promise about any one
of them. Jev is the first public model of that class, and the one iaclens
pinned was `jev-1.13.0`. It takes a state and a set of questions whose legal
answers you wrote down first. It returns the choice, a probability for every
legal answer, and a confidence. It does not return an explanation, and it
cannot return an answer outside the list, which is the sense in which TypeSafe
says it cannot hallucinate. Being unable to invent a fourth category is not
the same as being right about the three you gave it.

What that changes is where the judgment is allowed to sit. A generative model
is a collaborator: you read it, or you wrap it in retries until the JSON
happens to validate. A classifier with a calibrated probability is a function
you can branch on. The interesting part of iaclens is not that it called a
model. It is that the call has the same shape as the checks around it. Go
decides what it can decide. Jev is asked one closed question about one
directory. The probability comes back, and the threshold in the ruleset — not
the model — says whether the answer is allowed to count. Under 0.8 it counts
as unknown, and the checks that needed a kind stay unchecked. The model never
gets to talk the program out of that.

TypeSafe's own numbers, from that post, are 70 to 500 milliseconds a call and
$42 per billion input tokens, against multi-second frontier chat models at
dollars per million. Those figures are theirs. What mattered for this work is
smaller. A decision that is cheap enough and closed enough can be one step in
a pipeline instead of a session with a person. The factory later decided not
to take that step. The story of why it was worth trying is this date.

## 3. iaclens — 2026-09-27 to 2026-09-30

Public sidequest: https://github.com/Ledermayer/iaclens. Thirteen commits,
four tags, four days. `v0.1.0-rc.1` on 2026-09-27, `v0.1.0-rc.2` on
2026-09-28, `v0.1.0-rc.3` and `v0.1.0` on 2026-09-30.

The repository-shape problem from the private fleet is the problem this repo
was built to solve, without an anchor file. The scanner walks every directory
that contains `.tf` or `.tf.json` and treats each one as its own unit. A
repository is no longer assumed to be one module. Two facts are decided
separately for every directory:

- Role is structural. A directory under a configured example directory, `examples`
  by default, is an example. Its owner is the nearest parent directory that is
  not itself an example, or an explicit path assignment in the ruleset. An
  example keeps its own checks, and it does not count as another component.
  This replaces `parent-with-submodules`: the supporting code stays attached
  to the module it serves instead of being promoted to a catalog entry.
- Kind is intent. `module`, `deployment`, or `unknown`. Configured signals
  decide first, in Go: a `terraform.backend` or `terraform.cloud` block means
  deployment, and two signals that disagree mean `unknown` rather than a guess.
  A path override or `--kind` decides a directory the operator already knows.
  Only a directory with no conclusive signal is a classification question for
  Jev. Below the configured confidence and probability, both 0.8 by default,
  the kind stays `unknown` and the checks that depend on it stay `unchecked`.

The repository summary then falls out of the units. Examples are excluded from
the component counts. One kind across the remaining directories is that kind.
More than one kind is `mixed`. Any unresolved directory makes the
classification incomplete. `mixed` here is a result, not a shape someone had to
name in advance, and it does not need a marker file in the repository being
studied.

Go parses `.tf` and `.tf.json` and evaluates the exact checks. Anything the
rules cannot decide goes to TypeSafe's System One API as a Jev choice question.

The client posts to `https://api.typesafe.ai/v1/systemone`
(`--provider typesafe`) or to LLM Gateway at the same path
(`--provider llmgateway`). The request is the System One contract announced on
2026-09-15: a state, and questions whose criteria are the only legal answers.
The response has to come back with a choice inside those criteria, a
probability for every criterion, and a confidence in `[0, 1]`, with the
probabilities summing to one. The shipped ruleset asks one question per
unresolved directory — `module`, `deployment`, or `unknown` — and tells the
model to treat the source as data rather than instructions. The call happens
only in `--mode jev`. `--mode prepare` writes the request and calls nothing.
The 0.8 thresholds are the program's half of the contract: a probability the
model emits is not, by itself, a decision the program accepts.

RC1 proved the loop on the AVM resource-group module (18 files, 5
directories) through LLM Gateway. RC2 replaced the schema-v1 domain/role
questions with this configurable classifier and published schema 2. `v0.1.0`
documented installation, the report contract, and privacy limits. It is
pre-1.0 and explicitly not a certification.

Deliberate boundaries, carried forward even after the model was dropped:

- Terraform source only. No git history, no Azure DevOps policy, no provider release
  feeds, no pipeline compliance.
- The parser keeps expressions and does not evaluate them, download modules,
  or read tfvars, stacks, tests, plans, or state.
- Unknown stays unknown. A missing classification does not unlock code checks.
- Reports record the ruleset digest and the actual model response. "No
  findings" is not a plan or a security result.

## 4. avm-intelligence — 2026-09-28 to 2026-10-12

https://github.com/softwareone-public/avm-intelligence. Two hundred thirty-one
commits by 2026-10-12. This is the iteration that turned the workspace into a
factory: discovery, acquisition, assessment, acceptance, selection, board
projection, and publication are separate stages, and GitHub Issues are a
projection of durable state rather than the queue.

### The idea — a dark factory

The first three iterations all ran on a person's machine. A script cloned the
modules, scraped them, analyzed them, and wrote a report or a YAML file. That
works for one afternoon and for one customer fleet. It does not survive the
person closing the laptop. AVM has a few hundred modules, each with its own
repository and its own release cadence, and the question from May was how a
platform team keeps up without maintaining the modules themselves.

The bet on 2026-09-28 was to move the fleet off the laptop and onto GitHub,
and to let it run when nobody is watching. Discovery, analysis, and publication
become workflows in the repository rather than commands typed at a shell.
GitHub Actions is the machine that does the work. The results have two faces
and neither of them is the authority. GitHub Pages serves the intelligence
portal: the catalog, the module, the evidence, the provider, rendered from one
accepted commit. GitHub Projects is the monitoring board: one card per module,
showing where that module is in its life, from discovered to analyzed to
published. A person looks at the board to see what happened. The board does
not decide what happens next. That decision stays in durable state, so a
Projects outage delays the view and does not lose the work.

Self-sustaining means a nightly discovery finds a new release, records it, and
the rest of the pipeline carries that one module through analysis and
publication without a new command. It does not mean unsupervised. Workers do
not write accepted data. A reviewed pull request still stands between a result
and the dataset, and a deployment receipt still stands between the dataset and
a card that says Done. The dark-factory goal, named in the v2 projection
design, is unattended delivery of data. It was never permission to skip review.

### 2026-09-28 — the new organization

We moved into softwareone-public, the new organization, and started setting up
the environment: the repository, the identity that would later write state, and
the project that would hold the board. The first commit is that day, one day
after iaclens began. This is also why
[Crayon-HU/avm-metadata](https://github.com/Crayon-HU/avm-metadata) is a public
archive. The first iteration had been built in the previous organization, in
public. Once the work continued here, that repository was left readable and
closed to new commits. Archiving it did not publish this one. The organization
name describes where the work lives, and the repository stayed internal.
Nothing in the factory runs yet. The fleet work starts on 2026-10-01.

### Three designs, and the failures between them

There is no v3 architecture. The `v3` in this repository is a schema version:
facts are recorded as facts-v3, and a provider snapshot stores each source
document once and lets resource stubs point at it, which is snapshot schema v3.
Those are record formats. The designs that changed the factory are v1, v2, and
the corrections v2 grew once it met a real run. The
[v1 roadmap](../../../plan/architecture-iaclens-avm-intelligence-1.md) is
historical as of 2026-10-01. The
[v2 operating model](../../../plan/design-avm-intelligence-operating-model-2.md)
governs.

**v1 — a released analyzer, driven by the factory.** Written 2026-09-30, and
superseded the next day. The factory discovers AVM catalogs, pins a source
SHA, and shells out to a released IaCLens binary for parsing and checks. The
factory does not own a parser. Four workers run, results land as files, and a
fleet transaction accepts a cohort into the data branch. Issues and Projects
show the work. Pages publishes one accepted commit. It is the laptop pipeline
lifted onto Actions, with the sidequest's CLI as the engine.

It broke in specific ways, found by reading the code rather than by a
production incident:

- Accepting a cohort replaced the dataset. The candidate was built from the
  modules in that dispatch, copied into an empty directory, and swapped in
  with `rsync --delete`. A later one-module run could remove every other
  module from the catalog. Git history could recover the bytes. It could not
  keep the current catalog complete.
- A failed module stopped its siblings. The assemble job required the whole
  worker matrix to succeed, so one module's failure withheld everyone else's
  finished results.
- The queue was a file. A worker claimed a request by renaming a file in its
  own workspace. Two Actions runners do not share a disk, so the rename was
  not a lock. Workflow concurrency was being asked to do the job of a
  transaction.
- Normalization threw away the facts. The adapter kept a symbol count and the
  check results, and dropped the provider requirements and the typed symbols.
  A report could not tell "this module uses no provider" from "we did not
  record the providers."
- The board lied by omission. Projection skipped an Issue whose body text
  looked unchanged, without checking the Project fields. A successful Issue
  write followed by a failed Project write stayed broken. And the job that
  marked a module Done checked out a different commit than the job that
  published the site, so a green workflow was not evidence of what was
  deployed.
- Discovery planned into an empty directory and did not load the previous
  pins, so it could not tell a moved tag from a new module.

**v2 — one module, owned facts, a board that only reflects.** Approved
2026-10-01. Each of those failures gets a structural answer. A module revision
is the unit of work, and accepting it patches that module into the existing
dataset instead of replacing the dataset. The factory extracts facts itself,
in Go, and retires the IaCLens binary; the model path goes with it, because a
judgment the factory cannot replay is not evidence. Provider requirements
survive as their own record, and a missing fact stays unknown rather than
becoming a zero. Requests live in a durable store with compare-and-swap and a
fence, so a stale runner cannot overwrite a newer generation. Discovery writes
the desired state and a projection step writes the Issue and the Project card.
Done requires a receipt that names the exact accepted commit, the data digest,
and the modules in that deployment. The
[delivery plan](../../../plan/refactor-avm-intelligence-delivery-2.md) orders
the work. The
[projection design](../../../plan/board-projection-v2.md) is one subsystem of
it, written after the first full projection tried to reconcile 145 modules in
one job and hit the GitHub App rate limit.

**What v2 had to survive, 2026-10-06 to 2026-10-10.** The design was right and
the first machinery was not, and each correction is a response to a run rather
than a new version:

- The state transport was REST, and it re-downloaded the tree. Discovery run
  `37723675785` fetched 1,336 blobs twice, about three and a half minutes each
  time, and then spent three seconds on the commit. On 2026-10-08 the
  transport plan replaced that with Git: fetch the branch, read one snapshot,
  publish one small commit per operation, and on conflict fetch again and
  decide again. No custom locking protocol and no force push. PR #309 merged
  it on 2026-10-09. The first three live attempts still failed before any
  write, on a checkout path and a refspec the validator rejected. The fourth,
  run `37890050530`, committed catalog generation 3 and correctly did nothing
  for a module whose source had not changed.
- Discovery was still one batch with a cursor, and it wrote the board itself.
  A six-module run stopped at module five because the operation ID on the
  transaction did not match the ID of any event inside it, and resuming
  repeated the same failure. The
  [projection plan](../plan/refactor-independent-module-projection-1.md) of
  2026-10-09 makes each module's discovery durable on its own, and makes one
  scheduled workflow the only writer of Issues and Projects. A projection
  failure no longer replays the discovery that already committed.
- The old runtime was still in the tree, and the clean-start rule said its
  reports could not be relabeled as native facts. On 2026-10-06 the v1
  retirement removed the analyzer, the file queue, the report readers, and the
  145 legacy reports locally, and rebuilt the derived indexes from the native
  records. History stays in git. It is not a code path anymore.
- Evidence had nowhere to go. A worker run prepared a branch and never opened
  the pull request. The
  [delivery plan](../plan/feature-native-pipeline-delivery-1.md) connects the
  pieces: facts are accepted before assessment, provider snapshots arrive on
  their own, and assessment wakes only when both inputs exist. Each result
  crosses a reviewed pull request, and selection is a separate one.

What remains unfinished is the point of the current checkpoint. The ten-module
pilot has accepted facts and provider inputs, and seventy assessment requests
are queued. The workers have not run. Selection and publication have not run.
v2 describes a factory that can finish a module without a person driving it.
The runs so far prove pieces of that, one module at a time, and not the whole
loop.

### 2026-10-01 — v1 fleet in a day

Forty-three commits. The fixture-only skeleton (PR #1) was followed the same
day by a single-module pilot, catalog discovery, release-to-commit resolution,
durable fleet requests, opt-in workers, candidate assembly, accepted data,
and an exact-commit Pages site. PR #28 accepted the first cohort: 10 offline
resource-module analyses (4 compute, 3 network, 3 platform/management) with
semantic analysis disabled. Pages was verified at
`https://ideal-adventure-62gqzpm.pages.github.io/` from workflow `36874979207`.
Board projection ran, and the findings register records that a successful
projection run did not establish convergence (F-001). By the evening, PR #170
proposed the v2 board redesign.

### 2026-10-01 to 2026-10-02 — v2 chosen

Ledermayer approved the v2 operating model on 2026-10-01. The runtime may use
reviewed, pinned Go libraries. IaCLens, Python, Terraform and provider
binaries, mandatory AI, and dynamically loaded checks are excluded. Terraform
source is not executed. Phase 0 closed the same day (PR #172), Phase 1
containment landed as PR #173, and phases 2–4 landed on 2026-10-02
(PRs #174–#176), including the v2 worker and typed proposal acceptance. The
v2 catalog was bootstrapped twice that day (PRs #179 and #195) and the
reviewed evidence handoff was added (PR #196).

This is the break with iteration 3. Jev classification and the semantic
`module-purpose` check stay in history as evidence of what the corpus
contained. They are not part of the runtime. Path conventions and compiled
checks decide unit role. Exact checks that iaclens already evaluated in Go
are the checks that survived.

### 2026-10-03 to 2026-10-06 — native facts, then retirement of the old path

Targeted v2 discovery was scheduled on 2026-10-03 (PR #202). On 2026-10-04
the first native acquisition was accepted (PR #208) and dispatch was scoped
to an exact request ID (PR #207), so discovery can no longer start workers.
Provider collection policy, snapshot-gated assessment planning, and the
artifact-only collection workflow followed on 2026-10-04 and 2026-10-05
(PRs #210–#214). No live provider collection was run as part of that merge.

On 2026-10-02 a clean start had already been authorized: old reports must not
be imported as native facts. On 2026-10-06, after PR #215, the former
runtime, its workflows, compatibility readers, and data tree were removed
locally. Native source records were kept byte-for-byte; derived indexes were
rebuilt in Go. The same day set the workflow split that is still in force:
`factory-*` runs without a manual dispatch, `manual-*` is dispatch-only, and
a behavior is proven manually before it is promoted. Manual discovery was
corrected to walk one module at a time (PR #222) and to persist exact
continuations (PR #232).

### 2026-10-07 to 2026-10-09 — evidence sealing and Git state

The queue gained independent per-module lifecycle intents (PR #290) and
opt-in atomic acquisition transitions (PR #294). Evidence is sealed by the
worker, exported to a review branch, and opened as an exact-head review
(PRs #296–#300). Workers do not write accepted data.

On 2026-10-08 the state transport plan was approved and the Git command
transport, verified reads, isolated candidates, and verified publication
landed (PRs #304–#308). PR #309 integrated it on 2026-10-09. `factory-state`
is a JSON-only branch written with expected-head, fenced transactions. The
same day, PR #315 planned the split that discovery still uses: discovery
commits state, and a separate projection workflow is the only Issue and
Project writer.

### 2026-10-10 to 2026-10-12 — partial portal and the ten-module pilot

PR #316 published the catalog-first partial portal on 2026-10-10. The data
baseline is the current stable version of each active module, not a backfill
of older releases. Nightly discovery records newly observed stable versions
and leaves them queued; it does not dispatch assessment.

2026-10-11 was worker and discovery hardening: current-release binding,
receipt-backed pacing, anonymous upstream reads, and scoped credentials
(PRs #321–#332). 2026-10-12 was the provider and assessment-wake push, 87
commits. Reviewed provider snapshots were staged, resource-intelligence
proposals were accepted, and PRs #384–#394 fixed traversal, page and byte
limits, rate-limit handling, and the normalized source-document pool. PR #412
added the exact-scope assessment wake. PR #418 fixed the wake validator
rejecting provider major version 0, which had excluded the valid `modtm/0`
lane.

Checkpoint at the end of 2026-10-12, from the task register: all ten pilot
facts receipts are accepted, and exact-used-type provider stub snapshots are
accepted for the 12 eligible provider-version pairs. All 70 provider contexts
have exact accepted inputs. Wake run `38250302770` committed 70 assessment
requests and deferred none. Those requests are queued. Workers, selection,
and publication have not run. The approved ten-module pilot is not complete.

## What each iteration changed

| Question | avm-metadata | private-modules-metadata | iaclens | avm-intelligence |
| --- | --- | --- | --- | --- |
| Whose modules? | Public AVM | 147 private modules on Azure DevOps: 86 Azure, 28 AWS, 26 GCP | Any one Terraform repo | Public AVM, pinned releases |
| Where does metadata live? | One YAML per module | Marker blocks in one YAML per module | A report for one run | Accepted records plus `factory-state` |
| How is Terraform read? | Regex over comment-stripped `.tf` | Same scrape, per building block | Go HCL parser | Go facts extraction, syntax only |
| Who judges intent? | Dimension scripts and scores | Ten dimensions, company standard plus industry practice | Jev choice classifier, or unknown | Compiled checks; unknown stays unchecked |
| What does a person see? | Local portal, demoed once | Reports and work items | A schema-v2 report | Issues, Project, and Pages as projections |

The archived Crayon repository is where the product was named, and it was
archived because the work moved on 2026-09-28. The private repo proved the pipeline
on a fleet that had to be synced, scored, and filed as work. The sidequest
showed that a closed-vocabulary classifier can answer the question a regex
cannot, and also showed the cost: a model call in the analysis path, source
sent outward, and a judgment that cannot be replayed without the same model.
The factory kept the fleet, the dimensions, and the exact checks, and left
the model behind the decision recorded on 2026-10-01.
