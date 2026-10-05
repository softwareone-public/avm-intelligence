package main

import (
	"encoding/hex"
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
	assertProviderWorkflowPinnedActions(t, steps)
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
		`.path == ".github/workflows/v2_provider_collect.yml"`,
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
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "v2_provider_collect.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok || len(jobs) != 1 {
		t.Fatal("provider workflow must have exactly one collection job")
	}
	job, ok := jobs["collect"].(map[string]any)
	if !ok {
		t.Fatal("provider collection job is missing")
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("provider collection steps are missing")
	}
	return document, job, steps
}

func assertProviderWorkflowPinnedActions(t *testing.T, steps []any) {
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
			if !ok || options["persist-credentials"] != false || options["ref"] != "${{ inputs.accepted_base }}" {
				t.Fatal("provider checkout must use the exact input base without persisted credentials")
			}
		}
	}
}
