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
	assertProviderWorkflowPinnedActions(t, steps, false)
}

func TestV2ProviderCollectionWorkflowFencesBaseAndCache(t *testing.T) {
	_, _, steps := providerWorkflowDocument(t)
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
	assertProviderWorkflowPinnedActions(t, steps, true)
	assertProviderWorkflowScriptsParse(t, steps)
}

func TestV2ProviderExportWorkflowVerifiesArtifactAndStagedPaths(t *testing.T) {
	_, _, steps := providerWorkflowDocumentFor(t, "manual-provider-export.yml", "export")
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
	for _, name := range []string{"valid", "historical-path", "lookalike-path", "failed", "unfinished", "foreign-repository", "wrong-workflow", "pull-request", "feature-branch", "stale-base", "missing-attempt", "string-attempt", "fractional-attempt"} {
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
			if (err == nil) != (name == "valid" || name == "historical-path") {
				t.Fatalf("collection run %s filter error=%v output=%s", name, err, output)
			}
		})
	}
}

func mutateProviderCollectionRunFixture(run map[string]any, name string) {
	switch name {
	case "historical-path":
		run["path"] = ".github/workflows/v2_provider_collect.yml"
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

func assertProviderWorkflowPinnedActions(t *testing.T, steps []any, persistCredentials bool) {
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
			if !ok || options["persist-credentials"] != persistCredentials || options["ref"] != "${{ inputs.accepted_base }}" {
				t.Fatal("provider checkout must use the exact input base and approved credential setting")
			}
		}
	}
}
