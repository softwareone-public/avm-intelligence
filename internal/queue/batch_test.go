package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/source"
)

func TestPlanAcceptedCatalogQueuesResolvedActiveModules(t *testing.T) {
	t.Parallel()
	state := acceptedTestState(t, 1)
	pins := testPins(state, 2)
	requestsDir := t.TempDir()
	contract := testContract()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	plan, err := PlanAcceptedCatalog(state, pins, contract, requestsDir, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 3 || len(plan.Batches) != 1 || len(plan.Batches[0].ModuleIDs) != 2 {
		t.Fatalf("plan counts: items=%d batches=%#v", len(plan.Items), plan.Batches)
	}
	var queued, blocked int
	for _, item := range plan.Items {
		switch item.Action {
		case PlanQueued:
			queued++
		case PlanBlocked:
			blocked++
		}
	}
	if queued != 2 || blocked != 1 {
		t.Fatalf("queued=%d blocked=%d items=%#v", queued, blocked, plan.Items)
	}

	second, err := PlanAcceptedCatalog(state, pins, contract, requestsDir, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Batches) != 1 || len(second.Batches[0].ModuleIDs) != 2 {
		t.Fatalf("queued work was lost during reconciliation: %#v", second.Batches)
	}
	for _, item := range second.Items {
		if item.Action != PlanCoalesced && item.Action != PlanBlocked {
			t.Errorf("unchanged action = %s for %s", item.Action, item.ModuleID)
		}
	}
}

func TestPlanAcceptedCatalogReusesAcceptedFingerprint(t *testing.T) {
	t.Parallel()
	state := acceptedTestState(t, 1)
	pins := testPins(state, 3)
	requestsDir := t.TempDir()
	contract := testContract()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first, err := PlanAcceptedCatalog(state, pins, contract, requestsDir, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range first.Items {
		request, err := Load(requestsDir, item.ModuleID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Start(requestsDir, request.ModuleID, request.Fingerprint, "run-"+request.ModuleID, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err = Finish(requestsDir, request.ModuleID, request.Fingerprint, "run-"+request.ModuleID, true, now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	second, err := PlanAcceptedCatalog(state, pins, contract, requestsDir, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Batches) != 0 {
		t.Fatalf("accepted fingerprints were scheduled again: %#v", second.Batches)
	}
	for _, item := range second.Items {
		if item.Action != PlanReused {
			t.Errorf("accepted action = %s for %s", item.Action, item.ModuleID)
		}
	}
}

func TestPlanAcceptedCatalogSplitsDispatchableWork(t *testing.T) {
	t.Parallel()
	state := acceptedTestState(t, MaxBatchModules-1)
	pins := testPins(state, MaxBatchModules+1)
	plan, err := PlanAcceptedCatalog(state, pins, testContract(), t.TempDir(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Batches) != 2 || len(plan.Batches[0].ModuleIDs) != MaxBatchModules || len(plan.Batches[1].ModuleIDs) != 1 {
		t.Fatalf("dispatch batches = %#v", plan.Batches)
	}
}

func TestPlanRejectsSourcePinForRetiredOrUnknownModule(t *testing.T) {
	t.Parallel()
	state := acceptedTestState(t, 1)
	pin := source.ResolvedSource{ModuleID: "avm-res-not-accepted"}
	if _, err := PlanAcceptedCatalog(state, []source.ResolvedSource{pin}, testContract(), t.TempDir(), time.Now().UTC()); err == nil {
		t.Fatal("pin for unknown module was accepted")
	}
}

func TestPlanBlocksMismatchedSourceRepositoryPerModule(t *testing.T) {
	t.Parallel()
	state := acceptedTestState(t, 1)
	pins := testPins(state, 1)
	pins[0].Repository = "Azure/unrelated-repository"
	plan, err := PlanAcceptedCatalog(state, pins, testContract(), t.TempDir(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 3 || plan.Items[0].Action != PlanBlocked {
		t.Fatalf("mismatched source pin should be module-blocked: %#v", plan.Items)
	}
	if len(plan.Batches) != 0 {
		t.Fatalf("blocked source pin was scheduled: %#v", plan.Batches)
	}
}

func acceptedTestState(t *testing.T, resourceCount int) catalog.AcceptedState {
	t.Helper()
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	modules := make([]catalog.Module, 0, resourceCount+2)
	for index := 0; index < resourceCount; index++ {
		id := fmt.Sprintf("avm-res-test-resource-%03d", index)
		modules = append(modules, catalog.Module{
			ID: id, Type: catalog.Resource, Status: "Available", DisplayName: "Test Resource", Description: "test resource",
			RepositoryURL: "https://github.com/Azure/terraform-azurerm-" + id,
			RegistryURL:   "https://registry.terraform.io/modules/Azure/" + id,
		})
	}
	modules = append(modules,
		catalog.Module{ID: "avm-ptn-test-pattern", Type: catalog.Pattern, Status: "Proposed", DisplayName: "Test Pattern", Description: "test pattern", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-ptn-test-pattern", RegistryURL: "https://registry.terraform.io/modules/Azure/avm-ptn-test-pattern"},
		catalog.Module{ID: "avm-utl-test-utility", Type: catalog.Utility, Status: "Available", DisplayName: "Test Utility", Description: "test utility", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-utl-test-utility", RegistryURL: "https://registry.terraform.io/modules/Azure/avm-utl-test-utility"},
	)
	counts := map[catalog.Kind]int{}
	for _, module := range modules {
		counts[module.Type]++
	}
	state := catalog.AcceptedState{
		SchemaVersion: catalog.AcceptedStateSchemaVersion, Generation: 1, AcceptedAt: now, LastFetchedAt: now,
	}
	for _, feed := range catalog.OfficialSources {
		digest := sha256.Sum256([]byte(feed.Kind + " test snapshot"))
		state.Feeds = append(state.Feeds, catalog.FeedSnapshot{Kind: feed.Kind, URL: feed.URL, SHA256: hex.EncodeToString(digest[:]), ModuleCount: counts[feed.Kind]})
	}
	for _, module := range modules {
		state.Modules = append(state.Modules, catalog.ModuleRecord{Module: module, Active: true, LastSeenGeneration: 1})
	}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	return state
}

func testPins(state catalog.AcceptedState, pinLimit int) []source.ResolvedSource {
	pins := make([]source.ResolvedSource, 0, pinLimit)
	for index, record := range state.Modules {
		if index >= pinLimit {
			break
		}
		module := record.Module
		ownerRepo := strings.TrimPrefix(module.RepositoryURL, "https://github.com/")
		commit := strings.Repeat("a", 40)
		pins = append(pins, source.ResolvedSource{
			ModuleID: module.ID, Repository: ownerRepo, Version: "v1.0.0", Tag: "v1.0.0", Commit: commit,
			ArchiveURL: "https://github.com/" + ownerRepo + "/archive/" + commit + ".tar.gz",
			ReleaseURL: "https://github.com/" + ownerRepo + "/releases/tag/v1.0.0",
			ResolvedAt: time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC),
		})
	}
	return pins
}

func testContract() AnalysisContract {
	return AnalysisContract{
		CLIVersion: "0.1.0", CLICommit: strings.Repeat("b", 40), BinaryDigest: strings.Repeat("c", 64),
		PolicyDigest: strings.Repeat("d", 64), ReportSchemaVersion: 2, AdapterVersion: 1,
	}
}
