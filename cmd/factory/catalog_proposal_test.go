package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"gopkg.in/yaml.v3"
)

func TestCatalogIndexEntriesPreserveAcceptedLifecycle(t *testing.T) {
	accepted := catalog.AcceptedState{
		Generation: 1,
		Modules: []catalog.ModuleRecord{
			{Module: catalog.Module{ID: "avm-res-catalog-a", Type: catalog.Resource, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-catalog-a", DisplayName: "Catalog A", Description: "A"}, Active: true, LastSeenGeneration: 1},
			{Module: catalog.Module{ID: "avm-ptn-catalog-b", Type: catalog.Pattern, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-ptn-catalog-b.git", DisplayName: "Catalog B", Description: "B"}, Active: false, MissingObservations: 1, LastSeenGeneration: 1},
		},
	}

	entries, err := catalogIndexEntries(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ModuleID != "avm-ptn-catalog-b" || entries[0].Lifecycle != "retired" || entries[1].ModuleID != "avm-res-catalog-a" || entries[1].Lifecycle != "active" {
		t.Fatalf("catalog entries = %+v", entries)
	}
	if entries[0].Repository != "Azure/terraform-azurerm-avm-ptn-catalog-b" {
		t.Fatalf("normalized repository = %q", entries[0].Repository)
	}
	metadata, err := catalogModuleMetadata(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 2 || metadata[0].ModuleID != "avm-ptn-catalog-b" || metadata[0].ModuleType != "ptn" || metadata[0].Lifecycle != "retired" || metadata[0].MissingObservations != 1 || metadata[1].ModuleID != "avm-res-catalog-a" || metadata[1].ModuleType != "res" || metadata[1].Lifecycle != "active" || metadata[1].DisplayName != "Catalog A" {
		t.Fatalf("catalog metadata = %+v", metadata)
	}
}

func TestRepositoryIdentityRejectsNonGitHubURLs(t *testing.T) {
	for _, value := range []string{
		"http://github.com/Azure/repo",
		"https://user@github.com/Azure/repo",
		"https://github.com/Azure/repo?fork=true",
		"https://example.com/Azure/repo",
		"https://github.com/Azure/repo/tree/main",
	} {
		if _, err := repositoryIdentity(value); err == nil {
			t.Errorf("repositoryIdentity(%q) accepted an unsafe or noncanonical URL", value)
		}
	}
}

func TestCurrentCatalogOutboxRequiresAcceptedGeneration(t *testing.T) {
	outbox := discovery.CatalogProposal{
		SchemaVersion: 1, ProposalKind: "catalog", Generation: 1,
		ObservationID: "discovery-observation-" + strings.Repeat("a", 64),
		ModuleIDs:     []string{"avm-res-catalog-a"}, CreatedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
	proposalID, err := identity.ID("catalog-proposal", 1, outbox)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(outbox)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.Snapshot{Records: map[string]state.Record{
		"state/exports/" + proposalID + ".json": {Kind: "export", SchemaVersion: 1, Data: data},
	}}
	if _, err = currentCatalogOutbox(snapshot, 1); err != nil {
		t.Fatalf("current outbox was rejected: %v", err)
	}
	if _, err = currentCatalogOutbox(snapshot, 2); err == nil {
		t.Fatal("outbox for a different accepted generation was selected")
	}
}

func TestBuildCatalogProposalRequiresFactoryStateBranch(t *testing.T) {
	err := run([]string{
		"--build-catalog-proposal",
		"--state-branch", "main",
		"--catalog-proposal-base", strings.Repeat("a", 40),
		"--catalog-proposal-out", filepath.Join(t.TempDir(), "proposal"),
	})
	if err == nil || !strings.Contains(err.Error(), "factory-state") {
		t.Fatalf("wrong state branch error = %v", err)
	}
}

func TestBuildModuleEvidenceProposalRequiresTrustedInputs(t *testing.T) {
	err := run([]string{"--build-module-evidence-proposal"})
	if err == nil || !strings.Contains(err.Error(), "worker launch") {
		t.Fatalf("wrong missing-input error = %v", err)
	}
}

func TestBuildModuleEvidenceProposalUsesDedicatedValidationFlag(t *testing.T) {
	err := run([]string{
		"--build-module-evidence-proposal",
		"--worker-launch-input", "missing-launch.json",
		"--module-evidence-artifact", "missing-evidence.json",
		"--module-evidence-proposal-base", strings.Repeat("a", 40),
		"--module-evidence-proposal-out", filepath.Join(t.TempDir(), "proposal"),
		"--module-evidence-validation-id", "module-evidence-validator.v1",
	})
	if err == nil || !strings.Contains(err.Error(), "read trusted worker launch artifact") {
		t.Fatalf("proposal validation flag was intercepted by worker sealing: %v", err)
	}
}

func TestBuildModuleEvidenceProposalValidatesReplayTimestamp(t *testing.T) {
	err := run([]string{
		"--build-module-evidence-proposal",
		"--worker-launch-input", "missing-launch.json",
		"--module-evidence-artifact", "missing-evidence.json",
		"--module-evidence-proposal-base", strings.Repeat("a", 40),
		"--module-evidence-proposal-out", filepath.Join(t.TempDir(), "proposal"),
		"--module-evidence-validation-id", "module-evidence-validator.v1",
		"--module-evidence-proposal-created-at", "not-a-timestamp",
	})
	if err == nil || !strings.Contains(err.Error(), "parse module evidence proposal timestamp") {
		t.Fatalf("invalid replay timestamp error = %v", err)
	}
}

func TestImportEvidenceReceiptRequiresFactoryStateAndInput(t *testing.T) {
	err := run([]string{"--import-evidence-receipt", "--state-branch", "main", "--evidence-acceptance-input", "receipt.json"})
	if err == nil || !strings.Contains(err.Error(), "factory-state") {
		t.Fatalf("wrong state branch error = %v", err)
	}
	err = run([]string{"--import-evidence-receipt", "--state-branch", "factory-state"})
	if err == nil || !strings.Contains(err.Error(), "evidence-acceptance-input") {
		t.Fatalf("wrong missing input error = %v", err)
	}
}

func TestV2CatalogExportWorkflowIsManualAndReviewOnly(t *testing.T) {
	steps := catalogExportWorkflowSteps(t)
	buildsProposal := workflowRunsContain(steps, "--build-catalog-proposal")
	copiesCompleteData := workflowRunsContain(steps, "cp -R work/v2/catalog-proposal/data/. data/")
	pushesReviewBranch := workflowRunsContain(steps, "git push --no-verify") && workflowRunsContain(steps, "factory/v2-catalog-")
	createsPullRequest := workflowRunsContain(steps, "gh pr create")
	launchesDispatcher := workflowRunsContain(steps, "v2_dispatch.yml")
	if !buildsProposal || !copiesCompleteData || !pushesReviewBranch || createsPullRequest || launchesDispatcher {
		t.Fatalf("catalog workflow must copy the complete proposal and prepare only a review branch: builds=%t copiesData=%t pushes=%t createsPR=%t dispatches=%t", buildsProposal, copiesCompleteData, pushesReviewBranch, createsPullRequest, launchesDispatcher)
	}
}

func TestV2WorkerPreparesOnlySealedEvidenceReviewBranch(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "v2_worker.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse worker workflow: %v", err)
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("worker workflow has no jobs")
	}
	job, ok := jobs["prepare-evidence-proposal"].(map[string]any)
	if !ok || job["environment"] != "data-publisher" {
		t.Fatal("evidence proposal job must use the data-publisher environment")
	}
	needs, ok := job["needs"].([]any)
	if !ok || len(needs) != 2 || needs[0] != "prepare" || needs[1] != "validate-and-seal" {
		t.Fatalf("evidence proposal job must wait for the trusted seal: %#v", job["needs"])
	}
	permissions, ok := job["permissions"].(map[string]any)
	if !ok || len(permissions) != 1 || permissions["contents"] != "write" {
		t.Fatalf("evidence proposal job permissions are unexpected: %#v", job["permissions"])
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("evidence proposal job has no steps")
	}
	buildsProposal := workflowRunsContain(steps, "--build-module-evidence-proposal")
	usesDedicatedValidationFlag := workflowRunsContain(steps, "--module-evidence-validation-id")
	checksDeclaredPaths := workflowRunsContain(steps, "test \"$actual_paths\" = \"$expected_paths\"")
	pushesReviewBranch := workflowRunsContain(steps, "factory/v2-evidence-") && workflowRunsContain(steps, "git push --no-verify")
	createsPullRequest := workflowRunsContain(steps, "gh pr create")
	if !buildsProposal || !usesDedicatedValidationFlag || !checksDeclaredPaths || !pushesReviewBranch || createsPullRequest {
		t.Fatalf("worker workflow must prepare only a proposal-declared review branch: builds=%t validatorFlag=%t checksPaths=%t pushes=%t createsPR=%t", buildsProposal, usesDedicatedValidationFlag, checksDeclaredPaths, pushesReviewBranch, createsPullRequest)
	}
}

func TestV2EvidenceAcceptanceWorkflowVerifiesBeforeStateImport(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "v2_accept_evidence.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse evidence acceptance workflow: %v", err)
	}
	on, ok := document["on"].(map[string]any)
	if !ok || on["workflow_dispatch"] == nil || on["pull_request"] != nil || on["push"] != nil {
		t.Fatal("evidence acceptance must be manually dispatched after merge")
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("evidence acceptance workflow has no jobs")
	}
	job, ok := jobs["import"].(map[string]any)
	if !ok || job["environment"] != "data-publisher" {
		t.Fatal("evidence acceptance must use the data-publisher environment")
	}
	permissions, ok := job["permissions"].(map[string]any)
	if !ok || len(permissions) != 2 || permissions["contents"] != "read" || permissions["actions"] != "read" {
		t.Fatalf("evidence acceptance permissions are unexpected: %#v", job["permissions"])
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("evidence acceptance job has no steps")
	}
	checkoutHasReadCredential := false
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			continue
		}
		uses, ok := step["uses"].(string)
		if !ok || !strings.HasPrefix(uses, "actions/checkout@") {
			continue
		}
		with, ok := step["with"].(map[string]any)
		checkoutHasReadCredential = ok && with["persist-credentials"] == true
	}
	checksMergedAncestry := workflowRunsContain(steps, "git merge-base --is-ancestor")
	checksSuccessfulWorkerJobs := workflowRunsContain(steps, "/actions/runs/$run_id/jobs") && workflowRunsContain(steps, "Compute without state credentials") && workflowRunsContain(steps, "Trusted artifact validation and completion seal")
	requiresWholeRunSuccess := workflowRunsContain(steps, ".conclusion == \"success\" and")
	rebuildsArtifactProposal := workflowRunsContain(steps, "--build-module-evidence-proposal")
	usesDedicatedValidationFlag := workflowRunsContain(steps, "--module-evidence-validation-id")
	usesMergedDatasetTimestamp := workflowRunsContain(steps, ".created_at") && workflowRunsContain(steps, "--module-evidence-proposal-created-at")
	validatesMergedCandidate := workflowRunsContain(steps, "--apply-proposal") && workflowRunsContain(steps, "diff -qr work/v2/rebuilt-data work/v2/merged/data")
	importsReceipt := workflowRunsContain(steps, "--import-evidence-receipt")
	writesRepository := workflowRunsContain(steps, "git push") || workflowRunsContain(steps, "gh pr create")
	if !checkoutHasReadCredential || !checksMergedAncestry || !checksSuccessfulWorkerJobs || requiresWholeRunSuccess || !rebuildsArtifactProposal || !usesDedicatedValidationFlag || !usesMergedDatasetTimestamp || !validatesMergedCandidate || !importsReceipt || writesRepository {
		t.Fatalf("evidence acceptance must fetch merged history with read-only credentials and verify before state import: readCredential=%t ancestry=%t workerJobs=%t wholeRunSuccess=%t rebuild=%t validatorFlag=%t replayTimestamp=%t candidate=%t import=%t repositoryWrite=%t", checkoutHasReadCredential, checksMergedAncestry, checksSuccessfulWorkerJobs, requiresWholeRunSuccess, rebuildsArtifactProposal, usesDedicatedValidationFlag, usesMergedDatasetTimestamp, validatesMergedCandidate, importsReceipt, writesRepository)
	}
}

func TestLegacyProjectAcceptedWorkflowIsManualOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "project-accepted.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse legacy project-accepted workflow: %v", err)
	}
	on, ok := document["on"].(map[string]any)
	_, manualOnly := on["workflow_dispatch"]
	_, pushTrigger := on["push"]
	if !ok || !manualOnly || pushTrigger {
		t.Fatal("legacy project-accepted must not run automatically on V2 data PRs")
	}
}

func TestDiscoveryProjectionMapsDurableBacklogIntent(t *testing.T) {
	module := catalog.Module{ID: "avm-res-discovery-test", Type: catalog.Resource, DisplayName: "Discovery test"}
	projection, _, err := projectionForDiscoveryIntent(module, discovery.ProjectionIntent{
		SchemaVersion: 1, ModuleID: module.ID, DesiredGeneration: 2, Status: discovery.IntentBacklog,
	}, "https://github.com/softwareone-public/avm-intelligence/actions/runs/123")
	if err != nil {
		t.Fatal(err)
	}
	if projection.Status != "Backlog" || projection.Outcome != "Unassessed" || projection.ModuleID != module.ID {
		t.Fatalf("discovery projection = %+v, want Backlog/Unassessed for %s", projection, module.ID)
	}
}

func TestV2DiscoveryWorkflowSchedulesAndProjectsOnlyDiscoveryTargets(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "v2_discovery.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse v2 discovery workflow: %v", err)
	}
	on, ok := document["on"].(map[string]any)
	if !ok || on["schedule"] == nil || on["workflow_dispatch"] == nil {
		t.Fatal("v2 discovery must support nightly and manual runs")
	}
	dispatch, ok := on["workflow_dispatch"].(map[string]any)
	inputs, inputsOK := dispatch["inputs"].(map[string]any)
	moduleInput, moduleOK := inputs["discover_modules"].(map[string]any)
	if !ok || !inputsOK || !moduleOK || moduleInput["type"] != "string" || moduleInput["required"] != false {
		t.Fatalf("manual discover_modules input is missing or malformed: %#v", dispatch["inputs"])
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("v2 discovery workflow has no jobs")
	}
	job, ok := jobs["persist"].(map[string]any)
	if !ok {
		t.Fatal("v2 discovery workflow has no persist job")
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("v2 discovery job has no steps")
	}
	passesTargets := workflowRunsContain(steps, "--discover-modules \"$DISCOVER_MODULES\"")
	writesResult := workflowRunsContain(steps, "--discovery-result-out work/v2/discovery-result.json")
	projectsTargets := workflowRunsContain(steps, "--project-discovery-modules \"$module_ids\"")
	requiresManualList := workflowRunsContain(steps, "Manual discovery requires one or more comma-separated module IDs")
	dispatchesWorker := workflowRunsContain(steps, "v2_worker.yml") || workflowRunsContain(steps, "--dispatch-operation claim")
	if !passesTargets || !writesResult || !projectsTargets || !requiresManualList || dispatchesWorker {
		t.Fatalf("discovery workflow must resolve listed modules, project their Issues, and not dispatch workers: passesTargets=%t writesResult=%t projects=%t requiresList=%t dispatchesWorker=%t", passesTargets, writesResult, projectsTargets, requiresManualList, dispatchesWorker)
	}
	legacyData, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "project-accepted.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err = yaml.Unmarshal(legacyData, &legacy); err != nil {
		t.Fatalf("parse legacy projector workflow: %v", err)
	}
	legacyOn, ok := legacy["on"].(map[string]any)
	_, manualOnly := legacyOn["workflow_dispatch"]
	_, pushTrigger := legacyOn["push"]
	if !ok || !manualOnly || pushTrigger {
		t.Fatal("legacy project-accepted must remain manual-only and not react to V2 data PRs")
	}
}

func TestV2ProjectionRecoveryWorkflowIsManualAndProjectionOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "v2_projection_recovery.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse v2 projection recovery workflow: %v", err)
	}
	on, ok := document["on"].(map[string]any)
	dispatch, dispatchOK := on["workflow_dispatch"].(map[string]any)
	inputs, inputsOK := dispatch["inputs"].(map[string]any)
	moduleInput, moduleOK := inputs["module_ids"].(map[string]any)
	if !ok || !dispatchOK || !inputsOK || !moduleOK || moduleInput["type"] != "string" || moduleInput["required"] != true || on["schedule"] != nil || on["push"] != nil {
		t.Fatalf("projection recovery must require a manual module_ids input and have no automatic trigger: %#v", on)
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("projection recovery workflow has no jobs")
	}
	job, ok := jobs["project"].(map[string]any)
	if !ok || job["environment"] != "data-publisher" {
		t.Fatal("projection recovery must use the data-publisher environment")
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("projection recovery job has no steps")
	}
	projectsSelectedModules := workflowRunsContain(steps, "--project-discovery-modules \"$MODULE_IDS\"")
	usesStateToken := workflowRunsContain(steps, "--mint-state-token")
	performsDiscovery := workflowRunsContain(steps, "--discover-catalog") || workflowRunsContain(steps, "--persist-discovery") || workflowRunsContain(steps, "--discover-modules")
	dispatchesWorkers := workflowRunsContain(steps, "v2_worker.yml") || workflowRunsContain(steps, "--dispatch-operation claim")
	if !projectsSelectedModules || !usesStateToken || performsDiscovery || dispatchesWorkers {
		t.Fatalf("recovery must only project selected modules: projects=%t stateToken=%t discovery=%t workerDispatch=%t", projectsSelectedModules, usesStateToken, performsDiscovery, dispatchesWorkers)
	}
}

func catalogExportWorkflowSteps(t *testing.T) []any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "v2_catalog_export.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse catalog export workflow: %v", err)
	}
	on, ok := document["on"].(map[string]any)
	if !ok {
		t.Fatal("catalog export workflow has no trigger map")
	}
	_, dispatchesManually := on["workflow_dispatch"]
	_, triggersOnPullRequest := on["pull_request"]
	_, triggersOnPush := on["push"]
	if !ok || !dispatchesManually || triggersOnPullRequest || triggersOnPush {
		t.Fatal("catalog export must be manually dispatched and must not run on untrusted PR or push events")
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("catalog export workflow has no jobs")
	}
	job, ok := jobs["export"].(map[string]any)
	if !ok || job["environment"] != "data-publisher" {
		t.Fatal("catalog export must use the data-publisher environment")
	}
	permissions, ok := job["permissions"].(map[string]any)
	if !ok || len(permissions) != 1 || permissions["contents"] != "write" {
		t.Fatalf("catalog export permissions are insufficient or unexpected: %#v", job["permissions"])
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("catalog export workflow has no steps")
	}
	return steps
}

func workflowRunsContain(steps []any, expected string) bool {
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			continue
		}
		run, _ := step["run"].(string)
		if strings.Contains(run, expected) {
			return true
		}
	}
	return false
}
