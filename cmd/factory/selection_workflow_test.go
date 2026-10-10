package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestManualSelectionExportWorkflowPreparesOnlyAnExactReviewedProposal(t *testing.T) {
	document, job, steps := providerWorkflowDocumentFor(t, "manual-selection-export.yml", "export")
	assertProviderWorkflowScriptsParse(t, steps)
	triggers, ok := document["on"].(map[string]any)
	if !ok || len(triggers) != 1 {
		t.Fatalf("selection export triggers = %#v, want workflow_dispatch only", document["on"])
	}
	dispatch, ok := triggers["workflow_dispatch"].(map[string]any)
	if !ok {
		t.Fatal("selection export workflow must be manually dispatched")
	}
	inputs, ok := dispatch["inputs"].(map[string]any)
	if !ok {
		t.Fatalf("selection export inputs = %#v", dispatch["inputs"])
	}
	for _, name := range []string{"accepted_base", "selection_id", "module_id"} {
		input, exists := inputs[name].(map[string]any)
		if !exists || input["required"] != true || input["type"] != "string" {
			t.Errorf("selection export input %s must be a required string: %#v", name, inputs[name])
		}
	}
	if job["if"] != "github.ref == 'refs/heads/main'" || workflowEnvironmentName(job["environment"]) != "data-publisher" {
		t.Fatalf("selection export must run only from protected main behind data-publisher: %#v", job)
	}
	permissions, ok := job["permissions"].(map[string]any)
	if !ok || len(permissions) != 1 || permissions["contents"] != "write" {
		t.Fatalf("selection export permissions must be contents:write only: %#v", job["permissions"])
	}
	usesPattern := regexp.MustCompile(`^[^\s]+@[0-9a-f]{40}$`)
	for _, value := range steps {
		step := value.(map[string]any)
		if uses, exists := step["uses"].(string); exists && !usesPattern.MatchString(uses) {
			t.Errorf("action is not pinned to a full commit SHA: %s", uses)
		}
	}
	for _, required := range []string{
		"--build-selection-export-proposal",
		"--selection-export-id \"$SELECTION_ID\"",
		"--selection-export-module-id \"$MODULE_ID\"",
		"--apply-proposal",
		"git archive \"$ACCEPTED_BASE\" data",
		"git push --set-upstream origin \"$branch\"",
	} {
		if !workflowStepsContainScript(steps, required) {
			t.Errorf("selection export workflow is missing exact step behavior %q", required)
		}
	}
}

func TestManualSelectionExportPathFilterProducesExactNULDelimitedStageList(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatal("jq is required for selection export path-filter fixtures")
	}
	fixture := filepath.Join(t.TempDir(), "proposal.json")
	data, err := json.Marshal(map[string]any{"paths": []string{"data/tracks/azurerm-4/index.json", "data/selections/avm-res-example/azurerm-4/selection-abc.json"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(fixture, data, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("jq", "-j", `[.paths[] , "proposal.json"] | sort[] | . + "\u0000"`, fixture)
	actual, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"data/selections/avm-res-example/azurerm-4/selection-abc.json",
		"data/tracks/azurerm-4/index.json",
		"proposal.json",
		"",
	}, "\x00")
	if string(actual) != want {
		t.Fatalf("proposal path filter output = %q, want %q", actual, want)
	}
}

func workflowEnvironmentName(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case map[string]any:
		name, _ := value["name"].(string)
		return name
	default:
		return ""
	}
}

func workflowStepsContainScript(steps []any, want string) bool {
	for _, value := range steps {
		step := value.(map[string]any)
		if script, ok := step["run"].(string); ok && regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(script) {
			return true
		}
	}
	return false
}
