package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPilotAssessmentWakeScopeIsExactlyApprovedCohort(t *testing.T) {
	dataRoot := filepath.Join("..", "..", "data")
	moduleNames := strings.Join([]string{
		"avm-res-apimanagement-service",
		"avm-res-authorization-roleassignment",
		"avm-res-compute-capacityreservationgroup",
		"avm-res-compute-disk",
		"avm-res-compute-virtualmachine",
		"avm-res-compute-virtualmachinescaleset",
		"avm-res-managedidentity-userassignedidentity",
		"avm-res-network-applicationgateway",
		"avm-res-network-networkinterface",
		"avm-res-network-virtualnetwork",
	}, ",")
	got, err := validatePilotAssessmentModuleScope(dataRoot, moduleNames)
	if err != nil {
		t.Fatalf("validate exact approved cohort: %v", err)
	}
	if len(got) != len(approvedProviderFactsScopeModules) {
		t.Fatalf("approved pilot module count = %d, want %d", len(got), len(approvedProviderFactsScopeModules))
	}
	for _, invalid := range []string{strings.TrimSuffix(moduleNames, ",avm-res-network-virtualnetwork"), moduleNames + ",avm-res-utl-sku-finder"} {
		if _, err := validatePilotAssessmentModuleScope(dataRoot, invalid); err == nil {
			t.Errorf("assessment wake accepted non-exact scope %q", invalid)
		}
	}
}

func TestPilotAssessmentWakeProducerBindsExactWorkflowRun(t *testing.T) {
	const acceptedCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	producer, err := workflowProducer(acceptedCommit, "9911-2", "manual-assessment-wake")
	if err != nil {
		t.Fatalf("build assessment wake provenance: %v", err)
	}
	if producer.WorkflowID != "manual-assessment-wake" || producer.Commit != acceptedCommit || producer.RunID != "9911" || producer.RunAttempt != 2 {
		t.Fatalf("assessment wake provenance = %+v", producer)
	}
	if _, err := workflowProducer(acceptedCommit, "0-1", "manual-assessment-wake"); err == nil {
		t.Fatal("assessment wake accepted a zero workflow run")
	}
}

func TestPilotAssessmentWakeRejectsPublicationOptions(t *testing.T) {
	err := run([]string{
		"--wake-pilot-assessments",
		"--assessment-wake-accepted-commit", strings.Repeat("a", 40),
		"--assessment-wake-producer-run", "9911-1",
		"--assessment-wake-module-names", "pilot",
		"--publication-time", "2026-10-12T00:00:00Z",
		"--publication-manifest", "site/publication.json",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported for pilot assessment wake") {
		t.Fatalf("assessment wake accepted a publication capability: %v", err)
	}
}

func TestManualAssessmentWakeWorkflowIsStateOnlyAndExactScope(t *testing.T) {
	path := filepath.Join("..", "..", ".github", "workflows", "manual-assessment-wake.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Name        string         `yaml:"name"`
		On          map[string]any `yaml:"on"`
		Permissions map[string]any `yaml:"permissions"`
		Jobs        map[string]struct {
			If          string            `yaml:"if"`
			Permissions map[string]string `yaml:"permissions"`
			Environment string            `yaml:"environment"`
			Steps       []map[string]any  `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	if workflow.Name != "manual-assessment-wake" || len(workflow.On) != 1 || workflow.On["workflow_dispatch"] == nil {
		t.Fatalf("workflow trigger must be manual-only: name=%q triggers=%#v", workflow.Name, workflow.On)
	}
	if len(workflow.Permissions) != 0 || len(workflow.Jobs) != 1 {
		t.Fatalf("workflow must have no global permissions and one state-only job: %#v", workflow)
	}
	job := workflow.Jobs["wake"]
	if job.If != "github.ref == 'refs/heads/main'" || job.Environment != "data-publisher" || len(job.Permissions) != 1 || job.Permissions["contents"] != "read" {
		t.Fatalf("unexpected job ref, environment, or permissions: %#v", job)
	}
	var hasCommitFence, hasStateCheckout, hasWakeCommand bool
	for _, step := range job.Steps {
		uses, _ := step["uses"].(string)
		command, _ := step["run"].(string)
		if strings.HasPrefix(uses, "actions/checkout@") {
			with, _ := step["with"].(map[string]any)
			if with["repository"] != nil {
				hasStateCheckout = with["ref"] == "factory-state" && with["path"] == "work/state" && with["persist-credentials"] == false
			}
		}
		hasCommitFence = hasCommitFence || strings.Contains(command, `gh api "repos/$GITHUB_REPOSITORY/git/ref/heads/main"`) && strings.Contains(command, `test "$remote_main" = "$ACCEPTED_COMMIT"`)
		hasWakeCommand = hasWakeCommand || strings.Contains(command, "--wake-pilot-assessments") && strings.Contains(command, `--assessment-wake-module-names "$MODULE_NAMES"`)
		if command != "" {
			if strings.Contains(command, "${{ inputs.") {
				t.Fatal("workflow input must pass through a quoted environment variable")
			}
			check := exec.Command("bash", "-n")
			check.Stdin = strings.NewReader(command)
			if output, err := check.CombinedOutput(); err != nil {
				t.Fatalf("workflow shell syntax in %q: %v\n%s", step["name"], err, output)
			}
		}
	}
	if !hasCommitFence || !hasStateCheckout || !hasWakeCommand {
		t.Fatalf("workflow omitted exact commit fence, state-only checkout, or wake operation: %#v", job.Steps)
	}
	for _, forbidden := range []string{"pages", "id-token", "actions/deploy-pages", "upload-pages-artifact", "--prepare-indexed-site-publication", "--authorize-indexed-site-publication"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Errorf("queue-only workflow contains publication capability %q", forbidden)
		}
	}
}
