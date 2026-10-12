package main

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/softwareone-public/avm-intelligence/internal/provider"
	"gopkg.in/yaml.v3"
)

func TestManualDiscoveryWorkflowSummaryDistinguishesSkippedAndDeferred(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatal("jq is required for discovery summary fixtures")
	}
	_, _, steps := providerWorkflowDocumentFor(t, "manual-discovery.yml", "persist")
	assertProviderWorkflowScriptsParse(t, steps)
	var script string
	for _, value := range steps {
		step := value.(map[string]any)
		if step["name"] == "Report durable continuation" {
			script, _ = step["run"].(string)
		}
	}
	if script == "" {
		t.Fatal("discovery summary step is missing")
	}
	for _, scenario := range []string{"complete", "deferred", "legacy"} {
		t.Run(scenario, func(t *testing.T) { assertDiscoverySummaryFixture(t, script, scenario) })
	}
}

func assertDiscoverySummaryFixture(t *testing.T, script, scenario string) {
	t.Helper()
	result := map[string]any{"selected_module_ids": []string{"one", "two"}, "completed_module_ids": []string{"one"}, "checked_module_ids": []string{"one", "two"}, "skipped_module_ids": []string{"one"}, "changed_module_ids": []string{"two"}}
	wantStatus := "Discovery completed"
	wantCounts := "Release checks: 2. Unchanged acknowledged skips: 1. Changed sources: 1. Remaining cycles: 1"
	switch scenario {
	case "complete":
		result["continuation_complete"] = true
	case "deferred":
		result["deferred"] = true
		result["continuation_id"] = "fixture-continuation"
		result["retry_at"] = "fixture-deadline"
		wantStatus = "Discovery deferred"
	case "legacy":
		result = map[string]any{}
		wantStatus = "Discovery stopped"
		wantCounts = "Release checks: 0. Unchanged acknowledged skips: 0. Changed sources: 0. Remaining cycles: 0"
	}
	root := t.TempDir()
	work := filepath.Join(root, "work", "v2")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(work, "discovery-result.json"), result); err != nil {
		t.Fatal(err)
	}
	summary := filepath.Join(root, "summary")
	command := exec.Command("bash", "-c", script)
	command.Dir = root
	command.Env = append(os.Environ(), "GITHUB_STEP_SUMMARY="+summary)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("summary script failed: %v\n%s", err, output)
	}
	output, err := os.ReadFile(summary)
	if err != nil || !strings.Contains(string(output), wantStatus) || !strings.Contains(string(output), wantCounts) {
		t.Fatalf("summary status/counts incorrect: %s %v", output, err)
	}
}

func TestManualDiscoveryWorkflowAutomaticallyResumesExactCycle(t *testing.T) {
	_, job, steps := providerWorkflowDocumentFor(t, "manual-discovery.yml", "persist")
	if job["timeout-minutes"] != 360 {
		t.Fatal("automatic operation must retain a bounded six-hour job")
	}
	assertProviderWorkflowScriptsParse(t, steps)
	var script string
	for _, value := range steps {
		step := value.(map[string]any)
		if step["name"] == "Complete selected module discovery cycles" {
			script, _ = step["run"].(string)
		}
	}
	if script == "" {
		t.Fatal("cycle script missing")
	}
	for _, scenario := range []string{"complete", "failed", "invalid", "unsaved", "deadline"} {
		t.Run(scenario, func(t *testing.T) { assertAutomaticCycleFixture(t, script, scenario) })
	}
}

func TestDiscoveryWorkflowsPersistStateOnlyIntents(t *testing.T) {
	for _, workflowName := range []string{"manual-discovery.yml", "factory-discovery.yml"} {
		t.Run(workflowName, func(t *testing.T) {
			document, job, steps := providerWorkflowDocumentFor(t, workflowName, "persist")
			assertProviderWorkflowScriptsParse(t, steps)
			if document["name"] != strings.TrimSuffix(workflowName, ".yml") {
				t.Fatalf("unexpected workflow name: %v", document["name"])
			}
			if job["environment"] != "data-publisher" {
				t.Fatal("discovery must retain the data-publisher environment")
			}
			jobEnv, ok := job["env"].(map[string]any)
			if !ok {
				t.Fatal("discovery job environment is missing")
			}
			_, hasRepositoryToken := jobEnv["GITHUB_TOKEN"]
			if workflowName == "manual-discovery.yml" && hasRepositoryToken {
				t.Fatal("manual discovery must not send the workflow repository token to public upstream source reads")
			}
			if workflowName == "factory-discovery.yml" && !hasRepositoryToken {
				t.Fatal("factory discovery's existing source authentication remains unchanged during manual-first validation")
			}
			if workflowRunsContain(steps, "--project-discovery-modules") ||
				workflowRunsContain(steps, "--project-discovery-run-url") ||
				workflowRunsContain(steps, "--project-discovery-run-id") ||
				workflowRunsContain(steps, "projection-recovery") ||
				workflowRunsContain(steps, "--mint-projection-token") {
				t.Fatal("discovery workflow contains projection or recovery follow-on work")
			}
			if !workflowRunsContain(steps, "--mint-state-token") {
				t.Fatal("discovery must retain its state-only token mint")
			}
			if workflowStepCount(steps, "Mint state-only installation token") != 1 {
				t.Fatal("discovery must mint exactly one state-only installation token")
			}
		})
	}

	_, _, manual := providerWorkflowDocumentFor(t, "manual-discovery.yml", "persist")
	if !workflowRunsContain(manual, "--persist-discovery work/v2/candidate.json") ||
		!workflowRunsContain(manual, "--discovery-module-cycle") ||
		!workflowRunsContain(manual, "--discovery-paced") ||
		!workflowRunsContain(manual, `--discovery-run-id "$GITHUB_RUN_ID"`) ||
		!workflowRunsContain(manual, "--discovery-pacing-state work/v2/pacing-session.json") ||
		!workflowRunsContain(manual, "--discovery-continuation \"$CONTINUATION_ID\"") ||
		!workflowRunsContain(manual, "--state-checkout-root work/state") ||
		!workflowRunsContain(manual, "--state-branch factory-state") ||
		!workflowRunsContain(manual, "--discovery-result-out work/v2/discovery-result.json") {
		t.Fatal("manual discovery must preserve paced, resumable state-only discovery")
	}
	_, _, factory := providerWorkflowDocumentFor(t, "factory-discovery.yml", "persist")
	if !workflowRunsContain(factory, "--persist-discovery work/v2/candidate.json") ||
		workflowRunsContain(factory, "--discovery-new-modules-only") ||
		workflowRunsContain(factory, "--discovery-run-id") ||
		!workflowRunsContain(factory, "--state-checkout-root work/state") ||
		!workflowRunsContain(factory, "--state-branch factory-state") ||
		!workflowRunsContain(factory, "--discovery-result-out work/v2/discovery-result.json") {
		t.Fatal("nightly factory discovery must persist release checks for all active modules")
	}
	for _, workflowName := range []string{"manual-discovery.yml", "factory-discovery.yml"} {
		_, _, steps := providerWorkflowDocumentFor(t, workflowName, "persist")
		if workflowStepCount(steps, "Discover and validate official catalogs") != 1 {
			t.Fatalf("%s must retain catalog discovery", workflowName)
		}
	}
}

func TestTrustedStateWorkflowsDoNotGenerateCallerTreeListings(t *testing.T) {
	for _, workflowName := range []string{
		"manual-discovery.yml",
		"factory-worker.yml",
		"manual-dispatch.yml",
		"factory-dispatch.yml",
		"manual-dispatch-validation.yml",
	} {
		t.Run(workflowName, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", workflowName))
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]any
			if err = yaml.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			jobs, _ := document["jobs"].(map[string]any)
			if len(jobs) == 0 {
				t.Fatal("trusted workflow has no jobs")
			}
			for jobName, value := range jobs {
				job, _ := value.(map[string]any)
				steps, _ := job["steps"].([]any)
				assertProviderWorkflowScriptsParse(t, steps)
				for _, stepValue := range steps {
					step, _ := stepValue.(map[string]any)
					run, _ := step["run"].(string)
					if strings.Contains(run, "ls-tree") || strings.Contains(run, "state-tree") {
						t.Fatalf("job %s step %s derives a caller-owned state tree listing", jobName, step["name"])
					}
				}
			}
		})
	}
}

func TestManualCycleSourceResolverDoesNotUseRepositoryToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "workflow-repository-token")
	if resolver := manualCycleSourceResolver(); resolver.Token != "" {
		t.Fatalf("manual cycle source token = %q, want unauthenticated public read", resolver.Token)
	}
}

func workflowStepCount(steps []any, name string) int {
	count := 0
	for _, value := range steps {
		step, _ := value.(map[string]any)
		if step["name"] == name {
			count++
		}
	}
	return count
}

func assertAutomaticCycleFixture(t *testing.T, script, scenario string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "work", "v2"), 0755); err != nil {
		t.Fatal(err)
	}
	id := "discovery-cycle-" + strings.Repeat("a", 64)
	preamble := `
fake_now=100
calls=0
date() { if [[ "${1:-}" == -d ]]; then if [[ "$scenario" == deadline ]]; then printf '999999\n'; else printf '160\n'; fi; else printf '%s\n' "$fake_now"; fi; }
sleep() { fake_now=$((fake_now+$1)); }
go() {
  if [[ "$*" == *--mint-state-token* ]]; then printf 'fixture-token\n'; return; fi
  calls=$((calls+1))
  if [[ "$scenario" == failed ]]; then return 7; fi
  if (( calls == 1 )); then
    if [[ "$scenario" == invalid ]]; then printf '{"deferred":true,"continuation_durable":false}' > work/v2/discovery-result.json; return; fi
	if [[ "$scenario" == unsaved ]]; then printf '{"deferred":true,"continuation_durable":true,"deferral_saved":false}' > work/v2/discovery-result.json; return; fi
	printf '{"deferred":true,"continuation_durable":true,"deferral_saved":true,"continuation_id":"%s","retry_at":"fixture-time"}' "$expected_id" > work/v2/discovery-result.json
  else
    [[ "$CONTINUATION_ID" == "$expected_id" && "$FORCE" == false && -z "$MODULE_NAMES" && "$fake_now" -ge 160 ]] || return 8
    [[ "$*" == *"--discovery-pacing-state work/v2/pacing-session.json"* ]] || return 9
    printf '{"continuation_complete":true}' > work/v2/discovery-result.json
  fi
}
`
	command := exec.Command("bash", "-c", preamble+script+"\ntest \"$calls\" -eq 2\n")
	command.Dir = root
	command.Env = append(os.Environ(), "scenario="+scenario, "expected_id="+id, "CONTINUATION_ID=", "MODULE_NAMES=avm-res-one", "FORCE=true", "MODULE_TYPES=res", "GITHUB_REPOSITORY=softwareone-public/avm-intelligence", "GITHUB_RUN_ID=1", "GITHUB_RUN_ATTEMPT=1")
	output, err := command.CombinedOutput()
	if (err == nil) != (scenario == "complete") {
		t.Fatalf("automatic continuation fixture %s: %v\n%s", scenario, err, output)
	}
}

func TestV2ProviderCollectionWorkflowIsManualAndReadOnly(t *testing.T) {
	document, job, steps := providerWorkflowDocument(t)
	triggers, ok := document["on"].(map[string]any)
	if !ok || len(triggers) != 1 || triggers["workflow_dispatch"] == nil {
		t.Fatal("provider collection must be manually dispatched only")
	}
	permissions, ok := document["permissions"].(map[string]any)
	if !ok || len(permissions) != 0 {
		t.Fatal("provider collection must have no global permissions")
	}
	jobPermissions, ok := job["permissions"].(map[string]any)
	if !ok || len(jobPermissions) != 2 || jobPermissions["contents"] != "read" || jobPermissions["actions"] != "read" {
		t.Fatalf("unexpected collection permissions: %#v", job["permissions"])
	}
	if job["environment"] != "data-publisher" {
		t.Fatal("collection must preserve the publisher approval environment")
	}
	for _, forbidden := range []string{"git push", "--mint-state-token", "--import-evidence-receipt", "--dispatch-operation", "--assess-facts", "--process-work", "--build-site", "--build-indexed-site", "gh workflow run", "gh pr", "terraform init"} {
		if workflowRunsContain(steps, forbidden) {
			t.Errorf("proposal-only workflow contains forbidden operation %q", forbidden)
		}
	}
	assertProviderWorkflowPinnedActions(t, steps, false, "${{ inputs.accepted_base }}")
}

func TestV2ProviderCollectionWorkflowFencesBaseAndCache(t *testing.T) {
	_, job, steps := providerWorkflowDocument(t)
	env, ok := job["env"].(map[string]any)
	if !ok || env["FACTS_SCOPE"] != "${{ inputs.facts_scope }}" {
		t.Fatal("provider collection must pass the exact facts scope through an environment variable")
	}
	for _, required := range []string{
		`test "$GITHUB_REF" = refs/heads/main`,
		`test "$ACCEPTED_BASE" = "$GITHUB_SHA"`,
		`test "$(git rev-parse HEAD)" = "$ACCEPTED_BASE"`,
		`gh api "repos/$GITHUB_REPOSITORY/git/ref/heads/main"`,
		`test "$remote_main" = "$ACCEPTED_BASE"`,
		`.event == "workflow_dispatch" and .head_branch == "main"`,
		`.head_sha == $commit and .repository.full_name == $repository`,
		`.path == ".github/workflows/manual-provider-collect.yml"`,
		`--name "v2-provider-cache-${CACHE_RUN_ID}-${cache_attempt}"`,
		`. == $expected[0]`,
		`sha256sum config/providers.yaml`,
		`printf '%s' "$FACTS_SCOPE" > work/v2/provider-facts-scope.json`,
		`scope_digest:$scope_digest`,
		`--provider-facts-scope work/v2/provider-facts-scope.json`,
		`--provider-version "$PROVIDER_VERSION"`,
		`--provider-revision "$PROVIDER_REVISION"`,
		`--provider-collector-commit "$GITHUB_SHA"`,
		`--provider-proposal-base "$ACCEPTED_BASE"`,
		`--provider-source-generation "$SOURCE_GENERATION"`,
		`--provider-observed-at "$OBSERVED_AT"`,
		`--provider-cache-root work/v2/provider-cache`,
		`--proposal-data-out work/v2/provider-candidate`,
		`git diff --exit-code`,
	} {
		if !workflowRunsContain(steps, required) {
			t.Errorf("provider workflow omitted required guard or pinned option %q", required)
		}
	}
}

func TestV2ProviderCollectionWorkflowScriptsParse(t *testing.T) {
	_, _, steps := providerWorkflowDocument(t)
	assertProviderWorkflowScriptsParse(t, steps)
}

func assertProviderWorkflowScriptsParse(t *testing.T, steps []any) {
	t.Helper()
	for _, value := range steps {
		step := value.(map[string]any)
		script, _ := step["run"].(string)
		if script == "" {
			continue
		}
		if strings.Contains(script, "${{ inputs.") {
			t.Fatal("workflow inputs must be passed through quoted environment variables, not interpolated into shell code")
		}
		command := exec.Command("bash", "-n")
		command.Stdin = strings.NewReader(script)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("workflow step %v has invalid shell syntax: %v\n%s", step["name"], err, output)
		}
	}
}

func TestV2ProviderCollectionWorkflowChoicesMatchPolicy(t *testing.T) {
	document, _, _ := providerWorkflowDocument(t)
	triggers := document["on"].(map[string]any)
	dispatch := triggers["workflow_dispatch"].(map[string]any)
	inputs := dispatch["inputs"].(map[string]any)
	for _, name := range []string{"accepted_base", "provider_address", "provider_version", "provider_revision", "source_generation", "observed_at"} {
		input, ok := inputs[name].(map[string]any)
		if !ok || input["required"] != true {
			t.Fatalf("missing required explicit workflow input %s", name)
		}
	}
	address := inputs["provider_address"].(map[string]any)
	choices, ok := address["options"].([]any)
	if !ok || address["type"] != "choice" {
		t.Fatal("provider address must be restricted to reviewed choices")
	}
	policy, err := provider.LoadCollectionPolicy(filepath.Join("..", "..", "config", "providers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != len(policy.Providers) {
		t.Fatal("provider workflow choices differ from the reviewed policy")
	}
	for position, allowed := range policy.Providers {
		if choices[position] != allowed.Address {
			t.Fatalf("workflow provider choice %v differs from %s", choices[position], allowed.Address)
		}
	}
}

func TestV2ProviderResourceIntelligenceWorkflowIsManualProposalOnly(t *testing.T) {
	document, job, steps := providerWorkflowDocumentFor(t, "manual-provider-resource-intelligence.yml", "collect")
	triggers, ok := document["on"].(map[string]any)
	if !ok || len(triggers) != 1 || triggers["workflow_dispatch"] == nil {
		t.Fatal("resource intelligence collection must be manually dispatched only")
	}
	permissions, ok := document["permissions"].(map[string]any)
	if !ok || len(permissions) != 0 {
		t.Fatal("resource intelligence workflow must have no global permissions")
	}
	jobPermissions, ok := job["permissions"].(map[string]any)
	if !ok || len(jobPermissions) != 1 || jobPermissions["contents"] != "read" {
		t.Fatalf("unexpected resource intelligence permissions: %#v", job["permissions"])
	}
	if job["environment"] != "data-publisher" {
		t.Fatal("resource intelligence workflow must preserve the publisher approval environment")
	}
	for _, forbidden := range []string{"git push", "--mint-state-token", "--import-evidence-receipt", "--dispatch-operation", "--assess-facts", "--process-work", "--build-site", "--build-indexed-site", "gh workflow run", "gh pr", "terraform init"} {
		if workflowRunsContain(steps, forbidden) {
			t.Errorf("resource intelligence workflow contains forbidden operation %q", forbidden)
		}
	}
	assertProviderWorkflowPinnedActions(t, steps, false, "${{ inputs.accepted_base }}")
	assertProviderWorkflowScriptsParse(t, steps)
}

func TestV2ProviderResourceIntelligenceWorkflowPinsInputsAndProposalBase(t *testing.T) {
	document, _, steps := providerWorkflowDocumentFor(t, "manual-provider-resource-intelligence.yml", "collect")
	inputs := document["on"].(map[string]any)["workflow_dispatch"].(map[string]any)["inputs"].(map[string]any)
	for _, name := range []string{"accepted_base", "module_id", "source_sha", "facts_id", "provider_address", "provider_version", "provider_snapshot_id", "source_generation", "observed_at"} {
		input, ok := inputs[name].(map[string]any)
		if !ok || input["required"] != true {
			t.Fatalf("missing exact resource intelligence input %s", name)
		}
	}
	address := inputs["provider_address"].(map[string]any)
	choices, ok := address["options"].([]any)
	if !ok || address["type"] != "choice" {
		t.Fatal("resource intelligence provider address must use reviewed policy choices")
	}
	policy, err := provider.LoadCollectionPolicy(filepath.Join("..", "..", "config", "providers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != len(policy.Providers) {
		t.Fatal("resource intelligence provider choices differ from the reviewed policy")
	}
	for position, allowed := range policy.Providers {
		if choices[position] != allowed.Address {
			t.Fatalf("resource intelligence provider choice %v differs from %s", choices[position], allowed.Address)
		}
	}
	for _, required := range []string{
		`test "$GITHUB_REF" = refs/heads/main`,
		`test "$ACCEPTED_BASE" = "$GITHUB_SHA"`,
		`gh api "repos/$GITHUB_REPOSITORY/git/ref/heads/main"`,
		`--resource-module-id "$MODULE_ID"`,
		`--resource-source-sha "$SOURCE_SHA"`,
		`--resource-facts-id "$FACTS_ID"`,
		`--resource-provider-snapshot-id "$PROVIDER_SNAPSHOT_ID"`,
		`--provider-collector-commit "$GITHUB_SHA"`,
		`--provider-proposal-base "$ACCEPTED_BASE"`,
		`--provider-source-generation "$SOURCE_GENERATION"`,
		`--provider-cache-root work/v2/provider-resource-cache`,
		`--proposal-data-out work/v2/provider-candidate`,
		`git diff --exit-code`,
		`No accepted data, factory state, dispatch, or deployment was changed.`,
	} {
		if !workflowRunsContain(steps, required) {
			t.Errorf("resource intelligence workflow omitted exact-input or read-only guard %q", required)
		}
	}
}

func TestManualProviderSnapshotInputImportIsExactAndStateOnly(t *testing.T) {
	document, job, steps := providerWorkflowDocumentFor(t, "manual-import-provider-snapshot.yml", "import")
	triggers, ok := document["on"].(map[string]any)
	if !ok || len(triggers) != 1 || triggers["workflow_dispatch"] == nil {
		t.Fatal("provider snapshot input import must be manually dispatched only")
	}
	permissions, ok := document["permissions"].(map[string]any)
	if !ok || len(permissions) != 0 {
		t.Fatal("provider snapshot input import must have no global permissions")
	}
	jobPermissions, ok := job["permissions"].(map[string]any)
	if !ok || len(jobPermissions) != 1 || jobPermissions["contents"] != "read" {
		t.Fatalf("provider snapshot input import has unexpected permissions: %#v", job["permissions"])
	}
	if job["environment"] != "data-publisher" {
		t.Fatal("provider snapshot input import must preserve the publisher approval environment")
	}
	inputs := triggers["workflow_dispatch"].(map[string]any)["inputs"].(map[string]any)
	for _, name := range []string{"accepted_commit", "provider_address", "provider_version", "snapshot_id"} {
		input, exists := inputs[name].(map[string]any)
		if !exists || input["required"] != true {
			t.Fatalf("provider snapshot input import lacks exact required input %q", name)
		}
	}
	for _, required := range []string{
		`test "$GITHUB_REF" = refs/heads/main`,
		`gh api "repos/$GITHUB_REPOSITORY/git/ref/heads/main"`,
		`test "$remote_main" = "$ACCEPTED_COMMIT"`,
		`mkdir -p work/v2`,
		`--mint-state-token`,
		`--import-provider-snapshot-inputs`,
		`--provider-snapshot-input-data-root data`,
		`--state-branch factory-state`,
	} {
		if !workflowRunsContain(steps, required) {
			t.Errorf("provider snapshot input import omitted required exact-state guard %q", required)
		}
	}
	var importScript string
	for _, value := range steps {
		step := value.(map[string]any)
		if step["name"] == "Import exact provider snapshot generations" {
			importScript, _ = step["run"].(string)
			break
		}
	}
	mkdirPosition := strings.Index(importScript, "mkdir -p work/v2")
	outputPosition := strings.Index(importScript, "> work/v2/provider-snapshot-input-import.json")
	if importScript == "" || mkdirPosition < 0 || outputPosition < 0 || mkdirPosition > outputPosition {
		t.Fatal("provider input import must create its temporary parent directory before writing its import record")
	}
	if workflowRunsContain(steps, "git fetch --no-tags origin main") {
		t.Fatal("provider snapshot input import must not fetch through a credential-free checkout")
	}
	for _, forbidden := range []string{"--dispatch-operation", "--process-work", "--assess-facts", "--build-site", "--build-indexed-site", "gh workflow run", "gh pr", "git push", "--force"} {
		if workflowRunsContain(steps, forbidden) {
			t.Errorf("provider snapshot input import contains forbidden operation %q", forbidden)
		}
	}
	assertManualProviderInputImportCheckouts(t, steps)
	assertProviderWorkflowScriptsParse(t, steps)
}

func assertManualProviderInputImportCheckouts(t *testing.T, steps []any) {
	t.Helper()
	checkouts := make([]map[string]any, 0, 2)
	for _, value := range steps {
		step, ok := value.(map[string]any)
		if !ok {
			t.Fatal("invalid provider input import workflow step")
		}
		uses, _ := step["uses"].(string)
		if uses == "" {
			continue
		}
		_, commit, pinned := strings.Cut(uses, "@")
		if !pinned || len(commit) != 40 {
			t.Fatalf("provider input import action is not commit-pinned: %s", uses)
		}
		if _, err := hex.DecodeString(commit); err != nil {
			t.Fatalf("provider input import action has invalid commit: %s", uses)
		}
		if strings.HasPrefix(uses, "actions/checkout@") {
			checkouts = append(checkouts, step)
		}
	}
	if len(checkouts) != 2 {
		t.Fatalf("provider input import has %d checkout steps, want exact data and state checkouts", len(checkouts))
	}
	dataOptions, ok := checkouts[0]["with"].(map[string]any)
	if !ok || dataOptions["ref"] != "${{ inputs.accepted_commit }}" || dataOptions["fetch-depth"] != 0 || dataOptions["persist-credentials"] != false {
		t.Fatalf("provider data checkout is not pinned to exact accepted commit: %#v", checkouts[0]["with"])
	}
	stateOptions, ok := checkouts[1]["with"].(map[string]any)
	if !ok || stateOptions["ref"] != "factory-state" || stateOptions["path"] != "work/state" || stateOptions["persist-credentials"] != false || stateOptions["token"] != "${{ steps.state-token.outputs.token }}" {
		t.Fatalf("provider state checkout is not isolated and explicit: %#v", checkouts[1]["with"])
	}
}

func providerWorkflowDocument(t *testing.T) (map[string]any, map[string]any, []any) {
	t.Helper()
	return providerWorkflowDocumentFor(t, "manual-provider-collect.yml", "collect")
}

func providerWorkflowDocumentFor(t *testing.T, workflowName, jobName string) (map[string]any, map[string]any, []any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", workflowName))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok || len(jobs) != 1 {
		t.Fatal("provider workflow must have exactly one job")
	}
	job, ok := jobs[jobName].(map[string]any)
	if !ok {
		t.Fatalf("provider workflow job %s is missing", jobName)
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("provider collection steps are missing")
	}
	return document, job, steps
}

func TestV2ProviderExportWorkflowIsManualAndReviewOnly(t *testing.T) {
	document, job, steps := providerWorkflowDocumentFor(t, "manual-provider-export.yml", "export")
	triggers, ok := document["on"].(map[string]any)
	if !ok || len(triggers) != 1 || triggers["workflow_dispatch"] == nil {
		t.Fatal("provider export must be manually dispatched only")
	}
	permissions, ok := document["permissions"].(map[string]any)
	if !ok || len(permissions) != 0 {
		t.Fatal("provider export must have no global permissions")
	}
	jobPermissions, ok := job["permissions"].(map[string]any)
	if !ok || len(jobPermissions) != 2 || jobPermissions["contents"] != "write" || jobPermissions["actions"] != "read" {
		t.Fatalf("unexpected provider export permissions: %#v", job["permissions"])
	}
	if job["environment"] != "data-publisher" {
		t.Fatal("provider export must preserve the publisher approval environment")
	}
	for _, forbidden := range []string{"--mint-state-token", "--collect-provider-catalog", "--import-evidence-receipt", "--dispatch-operation", "--assess-facts", "--process-work", "--build-site", "--build-indexed-site", "gh workflow run", "gh pr", "push origin main", "--force"} {
		if workflowRunsContain(steps, forbidden) {
			t.Errorf("review-only export contains forbidden operation %q", forbidden)
		}
	}
	assertProviderWorkflowPinnedActions(t, steps, true, "main")
	assertProviderWorkflowScriptsParse(t, steps)
}

func TestV2ProviderExportWorkflowVerifiesArtifactAndStagedPaths(t *testing.T) {
	_, _, steps := providerWorkflowDocumentFor(t, "manual-provider-export.yml", "export")
	checkout, ok := steps[0].(map[string]any)
	if !ok {
		t.Fatal("provider export checkout step is missing")
	}
	checkoutWith, ok := checkout["with"].(map[string]any)
	if !ok || checkoutWith["ref"] != "main" {
		t.Fatal("provider export must check out the dispatch's main ref before verifying the exact accepted SHA")
	}
	for _, required := range []string{
		`test "$ACCEPTED_BASE" = "$GITHUB_SHA"`,
		`test "$(git rev-parse HEAD)" = "$ACCEPTED_BASE"`,
		`test "$remote_main" = "$ACCEPTED_BASE"`,
		`.status == "completed" and .conclusion == "success"`,
		`.event == "workflow_dispatch" and .head_branch == "main"`,
		`.head_sha == $base and .repository.full_name == $repository`,
		`.path == ".github/workflows/manual-provider-collect.yml"`,
		`--name "v2-provider-proposal-${COLLECTION_RUN_ID}-${COLLECTION_ATTEMPT}"`,
		`.kind == "provider_snapshot" and .expected_base == $base`,
		`--proposal-data-out work/v2/provider-candidate`,
		`git diff --cached --no-renames --name-only -z`,
		`--proposal-changed-paths work/v2/provider-actual-paths`,
		`git archive "$ACCEPTED_BASE" data`,
		`diff -qr work/v2/provider-candidate work/v2/provider-verified-candidate`,
		`branch="factory/v2-provider-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"`,
		`git push --set-upstream origin "$branch"`,
	} {
		if !workflowRunsContain(steps, required) {
			t.Errorf("provider export omitted required guard %q", required)
		}
	}
}

func TestV2ProviderExportWorkflowRejectsUntrustedCollectionRuns(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required to execute the workflow's collection-run filter")
	}
	filter := providerExportCollectionRunFilter(t)
	const base = "f4fefc3c7e111f77723de59408a277e980732455"
	const repository = "softwareone-public/avm-intelligence"
	for _, name := range []string{"valid", "historical-path", "resource-intelligence", "lookalike-path", "failed", "unfinished", "foreign-repository", "wrong-workflow", "pull-request", "feature-branch", "stale-base", "missing-attempt", "string-attempt", "fractional-attempt"} {
		t.Run(name, func(t *testing.T) {
			run := map[string]any{
				"status": "completed", "conclusion": "success", "event": "workflow_dispatch",
				"head_branch": "main", "head_sha": base, "run_attempt": 1,
				"path":       ".github/workflows/manual-provider-collect.yml",
				"repository": map[string]any{"full_name": repository},
			}
			mutateProviderCollectionRunFixture(run, name)
			data, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command("jq", "-e", "--arg", "repository", repository, "--arg", "base", base, filter)
			command.Stdin = strings.NewReader(string(data))
			output, err := command.CombinedOutput()
			if (err == nil) != (name == "valid" || name == "historical-path" || name == "resource-intelligence") {
				t.Fatalf("collection run %s filter error=%v output=%s", name, err, output)
			}
		})
	}
}

func mutateProviderCollectionRunFixture(run map[string]any, name string) {
	switch name {
	case "historical-path":
		run["path"] = ".github/workflows/v2_provider_collect.yml"
	case "resource-intelligence":
		run["path"] = ".github/workflows/manual-provider-resource-intelligence.yml"
	case "lookalike-path":
		run["path"] = ".github/workflows/untrusted-manual-provider-collect.yml"
	case "failed":
		run["conclusion"] = "failure"
	case "unfinished":
		run["status"] = "in_progress"
	case "foreign-repository":
		run["repository"] = map[string]any{"full_name": "foreign/repository"}
	case "wrong-workflow":
		run["path"] = ".github/workflows/manual-worker.yml"
	case "pull-request":
		run["event"] = "pull_request"
	case "feature-branch":
		run["head_branch"] = "feature/other"
	case "stale-base":
		run["head_sha"] = strings.Repeat("a", 40)
	case "missing-attempt":
		delete(run, "run_attempt")
	case "string-attempt":
		run["run_attempt"] = "1"
	case "fractional-attempt":
		run["run_attempt"] = 1.5
	}
}

func providerExportCollectionRunFilter(t *testing.T) string {
	t.Helper()
	_, _, steps := providerWorkflowDocumentFor(t, "manual-provider-export.yml", "export")
	for _, value := range steps {
		step := value.(map[string]any)
		if step["name"] != "Verify trusted successful collection run" {
			continue
		}
		script, _ := step["run"].(string)
		_, remaining, found := strings.Cut(script, `--arg base "$ACCEPTED_BASE" '`)
		if !found {
			t.Fatal("collection run filter start was not found")
		}
		filter, _, found := strings.Cut(remaining, "' work/v2/collection-run.json")
		if !found {
			t.Fatal("collection run filter end was not found")
		}
		return filter
	}
	t.Fatal("trusted collection verification step is missing")
	return ""
}

func assertProviderWorkflowPinnedActions(t *testing.T, steps []any, persistCredentials bool, checkoutRef string) {
	t.Helper()
	for _, value := range steps {
		step, ok := value.(map[string]any)
		if !ok {
			t.Fatal("invalid provider workflow step")
		}
		uses, _ := step["uses"].(string)
		if uses == "" {
			continue
		}
		action, commit, found := strings.Cut(uses, "@")
		if !found || len(commit) != 40 {
			t.Fatalf("action is not commit-pinned: %s", uses)
		}
		if _, err := hex.DecodeString(commit); err != nil {
			t.Fatalf("invalid action commit: %s", uses)
		}
		if action == "actions/checkout" {
			options, ok := step["with"].(map[string]any)
			if !ok || options["persist-credentials"] != persistCredentials || options["ref"] != checkoutRef {
				t.Fatal("provider checkout must use the expected ref and approved credential setting")
			}
		}
	}
}
