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
			{Module: catalog.Module{ID: "avm-res-catalog-a", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-catalog-a"}, Active: true},
			{Module: catalog.Module{ID: "avm-ptn-catalog-b", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-ptn-catalog-b.git"}, Active: false},
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

func TestV2CatalogExportWorkflowIsManualAndReviewOnly(t *testing.T) {
	steps := catalogExportWorkflowSteps(t)
	buildsProposal := workflowRunsContain(steps, "--build-catalog-proposal")
	pushesReviewBranch := workflowRunsContain(steps, "git push --no-verify") && workflowRunsContain(steps, "factory/v2-catalog-")
	createsPullRequest := workflowRunsContain(steps, "gh pr create")
	launchesDispatcher := workflowRunsContain(steps, "v2_dispatch.yml")
	if !buildsProposal || !pushesReviewBranch || createsPullRequest || launchesDispatcher {
		t.Fatalf("catalog workflow must prepare only a review branch: builds=%t pushes=%t createsPR=%t dispatches=%t", buildsProposal, pushesReviewBranch, createsPullRequest, launchesDispatcher)
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