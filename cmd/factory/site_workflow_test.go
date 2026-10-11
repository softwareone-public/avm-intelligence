package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"gopkg.in/yaml.v3"
)

func TestDeploymentFanoutTargetsDeduplicateContextsAndPreserveReleases(t *testing.T) {
	membership := []index.ModuleMembership{
		{ModuleID: "avm-res-alpha", AVMReleaseVersion: "v1.0.0", ContextID: "registry.terraform.io/hashicorp/azurerm/4"},
		{ModuleID: "avm-res-alpha", AVMReleaseVersion: "v1.0.0", ContextID: "registry.terraform.io/hashicorp/azurerm/5"},
		{ModuleID: "avm-res-alpha", AVMReleaseVersion: "v2.0.0", ContextID: "registry.terraform.io/hashicorp/azurerm/4"},
		{ModuleID: "avm-res-beta", AVMReleaseVersion: "v1.0.0", ContextID: "registry.terraform.io/hashicorp/azurerm/4"},
	}
	want := []moduleReleaseTarget{
		{moduleID: "avm-res-alpha", release: "v1.0.0"},
		{moduleID: "avm-res-alpha", release: "v2.0.0"},
		{moduleID: "avm-res-beta", release: "v1.0.0"},
	}
	got := deploymentFanoutTargets(membership)
	if len(got) != len(want) {
		t.Fatalf("fan-out targets = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("fan-out target %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestSiteAssessmentPlanningDoesNotInventRequestsWithoutAcceptanceReceipts(t *testing.T) {
	dataRoot := filepath.Join("..", "..", "data")
	cycle, err := planAcceptedSiteAssessments(dataRoot, state.NewSnapshot(), time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC), []string{"avm-res-apimanagement-service"})
	if err != nil {
		t.Fatalf("plan partial accepted site without lifecycle receipts: %v", err)
	}
	if len(cycle.Requests) != 0 || len(cycle.Deferred) != 0 {
		t.Fatalf("site assessment cycle without accepted facts receipts = %+v, want no invented requests or deferred work", cycle)
	}
}

func TestSiteAssessmentScopeRequiresExactActiveCatalogModules(t *testing.T) {
	dataRoot := filepath.Join("..", "..", "data")
	got, err := validateSiteAssessmentModuleScope(dataRoot, "avm-res-network-virtualnetwork,avm-res-apimanagement-service")
	if err != nil {
		t.Fatalf("validate exact active pilot modules: %v", err)
	}
	want := []string{"avm-res-apimanagement-service", "avm-res-network-virtualnetwork"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("validated scope = %v, want %v", got, want)
	}
	for _, invalid := range []string{"", "avm-res-unknown-module", "avm-res-apimanagement-service,avm-res-apimanagement-service"} {
		if _, err := validateSiteAssessmentModuleScope(dataRoot, invalid); err == nil {
			t.Errorf("invalid site assessment scope %q was accepted", invalid)
		}
	}
}

func TestAcceptedSelectionReconciliationSkipsUnmergedOutboxes(t *testing.T) {
	dataRoot := filepath.Join("..", "..", "data")
	coordinator := &selectionReconciliationCoordinator{snapshot: state.NewSnapshot()}
	if err := importAcceptedSelectionExports(context.Background(), coordinator, dataRoot, strings.Repeat("a", 40), time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC), []string{"avm-res-apimanagement-service"}); err != nil {
		t.Fatal(err)
	}
	if coordinator.transactions != 0 {
		t.Fatalf("selection reconciliation wrote state without an accepted receipt: %d transactions", coordinator.transactions)
	}
}

type selectionReconciliationCoordinator struct {
	snapshot     state.Snapshot
	transactions int
}

func (coordinator *selectionReconciliationCoordinator) Read(context.Context) (string, state.Snapshot, error) {
	return strings.Repeat("a", 40), coordinator.snapshot, nil
}

func (coordinator *selectionReconciliationCoordinator) RunTransaction(context.Context, string, state.TransactionDecisionFunc, state.TransactionResultResolver) (state.TransactionResult, error) {
	coordinator.transactions++
	return state.TransactionResult{}, nil
}

type sitePublishJob struct {
	If          string            `yaml:"if"`
	Needs       any               `yaml:"needs"`
	Outputs     map[string]string `yaml:"outputs"`
	Env         map[string]string `yaml:"env"`
	Permissions map[string]string `yaml:"permissions"`
	Environment any               `yaml:"environment"`
	Steps       []map[string]any  `yaml:"steps"`
}

func TestManualSitePublishWorkflowBindsOneAcceptedSiteToDeploymentReceipt(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "manual-site-publish.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On          map[string]any            `yaml:"on"`
		Permissions map[string]any            `yaml:"permissions"`
		Jobs        map[string]sitePublishJob `yaml:"jobs"`
	}
	if err = yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse site publication workflow: %v", err)
	}
	if len(workflow.On) != 1 {
		t.Fatalf("site publisher must have only workflow_dispatch: %#v", workflow.On)
	}
	if _, exists := workflow.On["workflow_dispatch"]; !exists {
		t.Fatalf("site publisher is not manual-only: %#v", workflow.On)
	}
	dispatch, ok := workflow.On["workflow_dispatch"].(map[string]any)
	inputs, inputsOK := dispatch["inputs"].(map[string]any)
	moduleNames, scopeOK := inputs["module_names"].(map[string]any)
	if !ok || !inputsOK || !scopeOK || moduleNames["required"] != true || moduleNames["type"] != "string" {
		t.Fatalf("site publisher must require an exact module scope for assessment side effects: %#v", workflow.On["workflow_dispatch"])
	}
	if len(workflow.Permissions) != 0 || len(workflow.Jobs) != 4 {
		t.Fatalf("workflow permissions must be empty and four ordered lifecycle jobs are required: %#v", workflow)
	}
	for _, required := range []string{
		"cancel-in-progress: false",
		"PAGES_DEPLOYMENT_ID: ${{ needs.deploy.outputs.pages_deployment_id }}",
		"--publication-manifest combined-site/publication.json",
	} {
		if !strings.Contains(string(data), required) {
			t.Errorf("workflow is missing exact deployment handoff %q", required)
		}
	}
	const mainOnly = "github.ref == 'refs/heads/main'"
	prepare, ok := workflow.Jobs["prepare"]
	if !ok || prepare.If != mainOnly || siteEnvironmentName(prepare.Environment) != "data-publisher" || len(prepare.Permissions) != 1 || prepare.Permissions["contents"] != "read" {
		t.Fatalf("preparation must be main-only behind data-publisher with read-only workflow token: %#v", prepare)
	}
	if prepare.Env["MODULE_NAMES"] != "${{ inputs.module_names }}" {
		t.Fatalf("exact assessment module scope must be passed through the environment: %#v", prepare.Env)
	}
	authorize, ok := workflow.Jobs["authorize"]
	if !ok || authorize.If != mainOnly || authorize.Needs != "prepare" || siteEnvironmentName(authorize.Environment) != "data-publisher" || len(authorize.Permissions) != 1 || authorize.Permissions["contents"] != "read" {
		t.Fatalf("authorization must follow preparation behind data-publisher: %#v", authorize)
	}
	deploy, ok := workflow.Jobs["deploy"]
	if !ok || deploy.If != mainOnly || deploy.Needs != "authorize" || siteEnvironmentName(deploy.Environment) != "github-pages" {
		t.Fatalf("Pages deployment must follow authorization in the Pages environment: %#v", deploy)
	}
	if deploy.Outputs["pages_deployment_id"] != "${{ github.sha }}" {
		t.Fatalf("Pages receipt key must use the action's build version because the pinned action exposes no deployment_id output: %#v", deploy.Outputs)
	}
	if len(deploy.Permissions) != 2 || deploy.Permissions["pages"] != "write" || deploy.Permissions["id-token"] != "write" {
		t.Fatalf("deploy permissions exceed Pages and OIDC needs: %#v", deploy.Permissions)
	}
	importReceipt, ok := workflow.Jobs["import-receipt"]
	if !ok || importReceipt.If != mainOnly || siteEnvironmentName(importReceipt.Environment) != "data-publisher" || len(importReceipt.Permissions) != 1 || importReceipt.Permissions["contents"] != "read" {
		t.Fatalf("receipt import must be main-only behind data-publisher: %#v", importReceipt)
	}
	needs, ok := importReceipt.Needs.([]any)
	if !ok || len(needs) != 2 || needs[0] != "prepare" || needs[1] != "deploy" {
		t.Fatalf("receipt import must wait for preparation and successful Pages deployment: %#v", importReceipt.Needs)
	}

	allJobs := []sitePublishJob{prepare, authorize, deploy, importReceipt}
	for _, job := range allJobs {
		for _, step := range job.Steps {
			uses, _ := step["uses"].(string)
			if strings.HasPrefix(uses, "actions/") && !regexp.MustCompile(`^actions/[a-z-]+@[0-9a-f]{40}$`).MatchString(uses) {
				t.Errorf("action is not pinned to a full commit SHA: %s", uses)
			}
		}
	}
	mainRefChecks := map[string]bool{
		"Verify exact accepted main commit":             false,
		"Verify accepted commit is still current main":  false,
		"Verify deployed artifact targets current main": false,
	}
	for _, job := range []sitePublishJob{prepare, authorize, importReceipt} {
		for _, step := range job.Steps {
			name, _ := step["name"].(string)
			if _, required := mainRefChecks[name]; !required {
				continue
			}
			env, _ := step["env"].(map[string]any)
			command, _ := step["run"].(string)
			if env["GH_TOKEN"] != "${{ github.token }}" ||
				!strings.Contains(command, `gh api "repos/$GITHUB_REPOSITORY/git/ref/heads/main"`) ||
				!strings.Contains(command, `test "$remote_main" = "$ACCEPTED_COMMIT"`) ||
				strings.Contains(command, "git fetch --no-tags origin main") {
				t.Errorf("site main-ref fence %q must use the authenticated read-only API without Git fetch: env=%#v command=%q", name, env, command)
			}
			mainRefChecks[name] = true
		}
	}
	for name, found := range mainRefChecks {
		if !found {
			t.Errorf("site publication workflow is missing required main-ref fence %q", name)
		}
	}

	var hasExactCheckout, hasGoSetup, hasPrepare, hasAssessmentScope, hasPagesArtifact, hasVerificationArtifact bool
	for _, step := range prepare.Steps {
		uses, _ := step["uses"].(string)
		with, _ := step["with"].(map[string]any)
		switch {
		case strings.HasPrefix(uses, "actions/checkout@"):
			if with["repository"] == nil {
				hasExactCheckout = true
			}
			if with["repository"] == nil && (with["ref"] != "${{ github.sha }}" || with["persist-credentials"] != false) {
				t.Errorf("checkout must use exact dispatch commit without persisted credentials: %#v", with)
			}
		case strings.HasPrefix(uses, "actions/setup-go@"):
			hasGoSetup = true
			if with["go-version-file"] != "go.mod" {
				t.Errorf("Go setup must follow go.mod: %#v", with)
			}
		case strings.HasPrefix(uses, "actions/upload-pages-artifact@"):
			hasPagesArtifact = with["path"] == "site"
		case strings.HasPrefix(uses, "actions/upload-artifact@"):
			hasVerificationArtifact = with["name"] == "combined-site-input" && with["path"] == "site"
		}
		command, _ := step["run"].(string)
		hasPrepare = hasPrepare || strings.Contains(command, "--prepare-indexed-site-publication")
		hasAssessmentScope = hasAssessmentScope || strings.Contains(command, `--publication-module-names "$MODULE_NAMES"`)
	}
	if !hasExactCheckout || !hasGoSetup || !hasPrepare || !hasAssessmentScope || !hasPagesArtifact || !hasVerificationArtifact {
		t.Fatalf("prepare job is missing exact build inputs, state generation, or artifact outputs: %#v", prepare.Steps)
	}

	var hasAuthorization, hasReceiptImport, hasPagesDeployment bool
	for _, step := range authorize.Steps {
		command, _ := step["run"].(string)
		hasAuthorization = hasAuthorization || strings.Contains(command, "--authorize-indexed-site-publication")
	}
	for _, step := range importReceipt.Steps {
		command, _ := step["run"].(string)
		hasReceiptImport = hasReceiptImport || strings.Contains(command, "--import-indexed-site-deployment")
	}
	for _, step := range deploy.Steps {
		uses, _ := step["uses"].(string)
		hasPagesDeployment = hasPagesDeployment || strings.HasPrefix(uses, "actions/deploy-pages@")
	}
	if !hasAuthorization || !hasReceiptImport || !hasPagesDeployment {
		t.Fatal("workflow must authorize before Pages deployment and import exact deployment membership afterward")
	}
	for _, forbidden := range []string{"--process-work", "--dispatch-operation", "--collect-provider", "--import-evidence-receipt", "gh workflow run"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("site publication workflow contains unrelated factory operation %q", forbidden)
		}
	}
}

func siteEnvironmentName(environment any) string {
	switch value := environment.(type) {
	case string:
		return value
	case map[string]any:
		name, _ := value["name"].(string)
		return name
	default:
		return ""
	}
}
