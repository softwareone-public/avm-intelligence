package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/contextset"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/provider"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

func TestSiteAssessmentPlanningQueuesOnlyExactAcceptedFactsAndInputs(t *testing.T) {
	const moduleID = "avm-res-site-wake"
	sourceSHA, acceptedCommit := strings.Repeat("a", 40), strings.Repeat("b", 40)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	dataRoot, facts, snapshot, policy := siteAssessmentAcceptedTree(t, moduleID, sourceSHA, now)
	required, err := provider.DeriveRequiredAssessmentContexts(facts, "v1.0.0", policy)
	if err != nil || len(required.Contexts) != 2 {
		t.Fatalf("required assessment contexts = %+v, error=%v", required, err)
	}
	providerAddress := required.Contexts[0].ProviderAddress
	providerMajor := uint64(4)
	selectedContextID := providerAddress + "/4"
	if required.Contexts[0].ProviderMajor != providerMajor {
		t.Fatalf("fixture provider contexts are unexpected: %+v", required.Contexts)
	}
	coordinator := &siteAssessmentFixtureCoordinator{head: "fixture-head", snapshot: state.NewSnapshot()}
	seedSiteFactsAcceptance(t, &coordinator.snapshot, moduleID, sourceSHA, acceptedCommit, facts, required, now)
	seedSiteSourcePin(t, &coordinator.snapshot, moduleID, sourceSHA)
	seedSiteActiveCatalog(t, &coordinator.snapshot, moduleID, now)
	targets, err := state.ProviderSnapshotAssessmentTargets(coordinator.snapshot, providerAddress, providerMajor)
	if err != nil || len(targets) != 1 {
		t.Fatalf("receipt-backed provider targets = %+v, error=%v", targets, err)
	}
	producer := projection.WorkflowProvenance{WorkflowID: "manual-import-provider-snapshot", Commit: acceptedCommit, RunID: "9911", RunAttempt: 1}
	if _, err = state.ImportAssessmentInputs(context.Background(), coordinator, state.AssessmentInputImport{
		Kind: state.InputProviderSnapshot, InputKey: providerAddress, InputID: snapshot.SnapshotID,
		Targets: targets, AcceptedCommit: acceptedCommit, Producer: producer, ImportedAt: now,
	}); err != nil {
		t.Fatalf("import exact provider snapshot: %v", err)
	}
	policyID, err := provider.AssessmentTrackPolicyID(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.ImportAssessmentInputs(context.Background(), coordinator, state.AssessmentInputImport{
		Kind: state.InputAssessmentPolicy, InputKey: state.AssessmentPolicyInputKey, InputID: policyID,
		Targets: targets, AcceptedCommit: acceptedCommit, Producer: producer, ImportedAt: now,
	}); err != nil {
		t.Fatalf("import exact reviewed assessment policy: %v", err)
	}
	cycle, err := planAcceptedSiteAssessments(dataRoot, coordinator.snapshot, now.Add(time.Minute), []string{moduleID})
	if err != nil {
		t.Fatalf("plan from exact accepted tree and durable receipts: %v", err)
	}
	if len(cycle.Requests) != 1 || len(cycle.Deferred) != 1 || cycle.Deferred[0].ContextID != providerAddress+"/5" {
		t.Fatalf("accepted site assessment plan = %+v, want one exact request and the unaccepted major-5 context deferred", cycle)
	}
	request := cycle.Requests[0]
	if request.ModuleID != moduleID || request.SourceSHA != sourceSHA || request.Work == nil ||
		request.Work.AVMReleaseVersion != "v1.0.0" || request.Work.FactsID != facts.FactsID ||
		request.Work.ContextID != selectedContextID || len(request.Work.SnapshotIDs) != 1 || request.Work.SnapshotIDs[0] != snapshot.SnapshotID {
		t.Fatalf("assessment request lost accepted input provenance: %+v", request)
	}

	backend := &workerTestBackend{head: "site-wake-head", snapshot: coordinator.snapshot}
	wakeCoordinator := workerTestCoordinator{backend: backend}
	if err := queueAcceptedSiteAssessments(context.Background(), wakeCoordinator, dataRoot, acceptedCommit, "9911-1", now.Add(2*time.Minute), []string{moduleID}); err != nil {
		t.Fatalf("persist exact site-cycle assessment wake: %v", err)
	}
	requestPath := "state/requests/" + request.ID + ".json"
	var queued queue.Request
	if json.Unmarshal(backend.snapshot.Records[requestPath].Data, &queued) != nil || queued.ID != request.ID || queued.Status != queue.StatusQueued || queued.Work.AVMReleaseVersion != "v1.0.0" || queued.Work.ContextID != selectedContextID {
		t.Fatalf("durable site-cycle request = %+v, want the exact eligible release/context request queued", queued)
	}
	if len(backend.snapshot.Records) != len(coordinator.snapshot.Records)+2 {
		t.Fatalf("site-cycle wake wrote unexpected records: before=%d after=%d", len(coordinator.snapshot.Records), len(backend.snapshot.Records))
	}
	var wakeReceipt state.OperationReceipt
	for _, record := range backend.snapshot.Records {
		if record.Kind != "operation_receipt" {
			continue
		}
		if json.Unmarshal(record.Data, &wakeReceipt) == nil && strings.HasPrefix(wakeReceipt.OperationID, "assessment-wake-") {
			break
		}
	}
	if wakeReceipt.OperationID == "" {
		t.Fatal("site-cycle assessment wake did not persist its immutable operation receipt")
	}
	var wakeResult struct {
		Commit struct {
			Requests []queue.Request                `json:"requests"`
			Deferred []queue.AssessmentWakeDeferred `json:"deferred"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(wakeReceipt.Result, &wakeResult); err != nil {
		t.Fatalf("decode durable assessment wake result: %v", err)
	}
	if len(wakeResult.Commit.Requests) != 1 || len(wakeResult.Commit.Deferred) != 1 || wakeResult.Commit.Deferred[0].ContextID != providerAddress+"/5" {
		t.Fatalf("durable wake result = %+v, want exact queued request and deferred major-5 context", wakeResult.Commit)
	}
}

func TestSiteAssessmentPlanningExcludesAcceptedModulesOutsidePilotScope(t *testing.T) {
	const moduleID = "avm-res-site-wake"
	sourceSHA := strings.Repeat("a", 40)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	dataRoot, facts, _, policy := siteAssessmentAcceptedTree(t, moduleID, sourceSHA, now)
	required, err := provider.DeriveRequiredAssessmentContexts(facts, "v1.0.0", policy)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.NewSnapshot()
	seedSiteFactsAcceptance(t, &snapshot, moduleID, sourceSHA, strings.Repeat("b", 40), facts, required, now)
	seedSiteSourcePin(t, &snapshot, moduleID, sourceSHA)
	seedSiteActiveCatalog(t, &snapshot, moduleID, now)

	cycle, err := planAcceptedSiteAssessments(dataRoot, snapshot, now.Add(time.Minute), []string{"avm-res-outside-pilot"})
	if err != nil {
		t.Fatalf("plan exact pilot scope: %v", err)
	}
	if len(cycle.Requests) != 0 || len(cycle.Deferred) != 0 {
		t.Fatalf("site cycle planned work outside pilot scope: %+v", cycle)
	}
	if _, err = planAcceptedSiteAssessments(dataRoot, snapshot, now.Add(time.Minute), nil); err == nil {
		t.Fatal("site cycle accepted an empty module scope")
	}
}

type siteAssessmentFixtureCoordinator struct {
	head     string
	snapshot state.Snapshot
}

func (coordinator *siteAssessmentFixtureCoordinator) Read(context.Context) (string, state.Snapshot, error) {
	return coordinator.head, coordinator.snapshot, nil
}

func (coordinator *siteAssessmentFixtureCoordinator) RunTransaction(ctx context.Context, _ string, decide state.TransactionDecisionFunc, _ state.TransactionResultResolver) (state.TransactionResult, error) {
	decision, err := decide(coordinator.snapshot)
	if err != nil {
		return state.TransactionResult{}, err
	}
	if decision.Noop {
		return state.TransactionResult{Head: coordinator.head, Value: decision.Result}, nil
	}
	for _, event := range decision.Events {
		coordinator.snapshot.Records[event.RecordPath] = event.Record
		digest, digestErr := identity.ID("state-event", 1, event)
		if digestErr != nil {
			return state.TransactionResult{}, digestErr
		}
		coordinator.snapshot.AppliedEvents[event.ID] = strings.TrimPrefix(digest, "state-event-")
	}
	coordinator.head += "-next"
	return state.TransactionResult{Head: coordinator.head, Value: decision.Result, Changed: len(decision.Events) > 0}, nil
}

func siteAssessmentAcceptedTree(t *testing.T, moduleID, sourceSHA string, now time.Time) (string, terraform.Facts, assessment.ProviderSnapshot, provider.CollectionPolicy) {
	t.Helper()
	sourceDir := t.TempDir()
	configuration := `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}
resource "azurerm_resource_group" "main" {}`
	if err := os.WriteFile(filepath.Join(sourceDir, "main.tf"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: sourceDir, ModuleID: moduleID, SourceSHA: sourceSHA, ObservedAt: now,
		Units: []terraform.UnitInput{{Path: ".", Role: terraform.RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := provider.LoadCollectionPolicy(filepath.Join("..", "..", "config", "providers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	address := policy.Providers[0].Address
	providerSnapshot := assessment.ProviderSnapshot{
		SchemaVersion: 2, RecordType: "provider_snapshot", ProviderAddress: address, ProviderVersion: "4.2.0",
		Coverage: assessment.SnapshotCoverage{Capabilities: map[string]string{"resource": "complete"}, Complete: true, ObservedAt: now.Format(time.RFC3339)},
		Entries:  []assessment.ProviderEntry{{Kind: "resource", Name: "azurerm_resource_group", DocumentationRef: "https://registry.terraform.io/providers/hashicorp/azurerm/4.2.0/docs/resources/resource_group", ContentDigest: strings.Repeat("c", 64)}},
		Producer: assessment.SnapshotProducer{ID: "fixture-collector", Version: "1.0.0", ContractVersion: 1}, SourceDigest: strings.Repeat("d", 64), ObservedAt: now.Format(time.RFC3339),
	}
	providerSnapshot.SnapshotID, err = identity.ID("snapshot", providerSnapshot.SchemaVersion, providerSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	metadataPath, _ := identity.ModuleMetadataPath(moduleID)
	sourcePath, _ := identity.ModuleSourcePath(moduleID, sourceSHA)
	factsPath, _ := identity.ModuleRecordPath(moduleID, sourceSHA, "facts", facts.FactsID)
	snapshotPath, _ := identity.ProviderSnapshotManifestPath(address, providerSnapshot.ProviderVersion, providerSnapshot.SnapshotID)
	metadata := mustSiteFixtureJSON(t, struct {
		SchemaVersion      int    `json:"schema_version"`
		RecordType         string `json:"record_type"`
		ModuleID           string `json:"module_id"`
		ModuleType         string `json:"module_type"`
		Status             string `json:"status"`
		Repository         string `json:"repository"`
		Lifecycle          string `json:"lifecycle"`
		DisplayName        string `json:"display_name"`
		Description        string `json:"description"`
		LastSeenGeneration uint64 `json:"last_seen_generation"`
	}{2, "module_metadata", moduleID, "res", "Available", "Azure/terraform-azurerm-avm-res-site-wake", "active", "Site wake fixture", "Accepted site planner fixture", 1})
	repository := "Azure/terraform-azurerm-avm-res-site-wake"
	sourceRecord := mustSiteFixtureJSON(t, struct {
		SchemaVersion int    `json:"schema_version"`
		RecordType    string `json:"record_type"`
		ModuleID      string `json:"module_id"`
		Repository    string `json:"repository"`
		SourceSHA     string `json:"source_sha"`
		Immutable     bool   `json:"immutable"`
	}{1, "source_revision", moduleID, repository, sourceSHA, true})
	factsBytes := mustSiteFixtureJSON(t, facts)
	snapshotBytes := mustSiteFixtureJSON(t, providerSnapshot)
	factRecord, err := index.FactRecordFromTypedFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	records := index.Records{
		CatalogGeneration: 1, CreatedAt: now,
		Generator: index.Producer{ID: "fixture-indexer", Version: "1.0.0", ContractVersion: 1},
		RecordDigests: map[string]string{
			metadataPath: siteFixtureDigest(metadata), sourcePath: siteFixtureDigest(sourceRecord),
			factsPath: siteFixtureDigest(factsBytes), snapshotPath: siteFixtureDigest(snapshotBytes),
		},
		Modules:           []index.ModuleRecord{{ModuleID: moduleID, Repository: repository, Lifecycle: "active"}},
		SourceRevisions:   []index.SourceRevisionRecord{{ModuleID: moduleID, Repository: repository, SourceSHA: sourceSHA, Immutable: true}},
		Facts:             []index.FactRecord{factRecord},
		ProviderSnapshots: []index.ProviderSnapshotRecord{{ID: providerSnapshot.SnapshotID, Address: address, Version: providerSnapshot.ProviderVersion}},
	}
	indexes, err := index.Build(records)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := indexes.Files()
	if err != nil {
		t.Fatal(err)
	}
	dataRoot := t.TempDir()
	writeSiteFixtureRecord(t, dataRoot, metadataPath, metadata)
	writeSiteFixtureRecord(t, dataRoot, sourcePath, sourceRecord)
	writeSiteFixtureRecord(t, dataRoot, factsPath, factsBytes)
	writeSiteFixtureRecord(t, dataRoot, snapshotPath, snapshotBytes)
	for logicalPath, contents := range generated {
		writeSiteFixtureRecord(t, dataRoot, logicalPath, contents)
	}
	return dataRoot, facts, providerSnapshot, policy
}

func seedSiteFactsAcceptance(t *testing.T, snapshot *state.Snapshot, moduleID, sourceSHA, acceptedCommit string, facts terraform.Facts, required contextset.Set, now time.Time) {
	t.Helper()
	request, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{ModuleID: moduleID, SourceSHA: sourceSHA, DesiredGeneration: 1, ExtractorContract: "factory-hcl-v1", Capabilities: []string{terraform.CapabilityProviderRequirements}}, now)
	if err != nil {
		t.Fatal(err)
	}
	input := state.EvidenceAcceptanceInput{
		ProposalID: "proposal-" + strings.Repeat("1", 64), ProposalKind: state.EvidenceProposalModule,
		ModuleID: moduleID, RequestID: request.ID, CompletionID: "completion-" + strings.Repeat("2", 64),
		Phase: "acquire_source", AVMReleaseVersion: "v1.0.0", DesiredGeneration: 1, SourceSHA: sourceSHA,
		FactsID: facts.FactsID, InputFingerprint: strings.Repeat("3", 64), ArtifactDigest: strings.Repeat("4", 64),
		EvidenceCommit: acceptedCommit, AcceptedAt: now,
		Producer:         projection.WorkflowProvenance{WorkflowID: "manual-accept-evidence", Commit: acceptedCommit, RunID: "8811", RunAttempt: 1},
		RequiredContexts: &required,
	}
	receipt, err := state.NewEvidenceAcceptanceReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "evidence-acceptance:" + receipt.ID
	intent, err := projection.NewDesiredIntent(moduleID, 1, operationID, "accepted", []projection.OwnedField{{Name: "phase", Value: "acquire_source"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	requestBytes := mustSiteFixtureJSON(t, request)
	receiptBytes := mustSiteFixtureJSON(t, receipt)
	intentBytes := mustSiteFixtureJSON(t, intent)
	events := []state.Event{
		{ID: "accepted-request:" + request.ID, RecordPath: "state/requests/" + request.ID + ".json", Record: state.Record{Kind: "request", SchemaVersion: 1, Data: requestBytes}},
		{ID: "accepted-evidence:" + receipt.ID, RecordPath: "state/publications/" + receipt.ID + ".json", Record: state.Record{Kind: "publication", SchemaVersion: 1, Data: receiptBytes}},
		{ID: intent.IntentID, RecordPath: "state/projection/intents/" + moduleID + ".json", Record: state.Record{Kind: "projection_intent", SchemaVersion: 1, Data: intentBytes}},
	}
	result := mustSiteFixtureJSON(t, struct {
		Acceptance state.EvidenceAcceptanceResult `json:"acceptance"`
		Events     []state.Event                  `json:"events"`
	}{Acceptance: state.EvidenceAcceptanceResult{Receipt: receipt, Changed: true}, Events: events})
	operation, err := state.NewOperationReceipt(moduleID, operationID, result, events)
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, operation)
	for _, event := range events {
		snapshot.Records[event.RecordPath] = event.Record
		digest, digestErr := identity.ID("state-event", 1, event)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		snapshot.AppliedEvents[event.ID] = strings.TrimPrefix(digest, "state-event-")
	}
}

func seedSiteSourcePin(t *testing.T, snapshot *state.Snapshot, moduleID, sourceSHA string) {
	t.Helper()
	path, err := identity.StateSourcePinPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	data := mustSiteFixtureJSON(t, struct {
		SchemaVersion  int    `json:"schema_version"`
		ModuleID       string `json:"module_id"`
		ReleaseHistory []struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"release_history"`
	}{1, moduleID, []struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}{{"v1.0.0", sourceSHA}}})
	snapshot.Records[path] = state.Record{Kind: "source_pins", SchemaVersion: 1, Data: data}
}

func seedSiteActiveCatalog(t *testing.T, snapshot *state.Snapshot, moduleID string, now time.Time) {
	t.Helper()
	modules := []catalog.Module{
		{ID: moduleID, Type: catalog.Resource, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-site-wake", DisplayName: "Site wake", Description: "Fixture"},
		{ID: "avm-ptn-site-wake", Type: catalog.Pattern, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-ptn-site-wake", DisplayName: "Pattern", Description: "Fixture"},
		{ID: "avm-utl-site-wake", Type: catalog.Utility, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-utl-site-wake", DisplayName: "Utility", Description: "Fixture"},
	}
	feeds := make([]catalog.FeedSnapshot, 0, len(catalog.OfficialSources))
	for _, source := range catalog.OfficialSources {
		feeds = append(feeds, catalog.FeedSnapshot{Kind: source.Kind, URL: source.URL, SHA256: strings.Repeat("a", 64), ModuleCount: 1})
	}
	candidate := catalog.Snapshot{SchemaVersion: catalog.SnapshotSchemaVersion, FetchedAt: now, Feeds: feeds, Modules: modules}
	accepted, _, err := catalog.AcceptSnapshot(nil, candidate, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	data := mustSiteFixtureJSON(t, struct {
		SchemaVersion int                   `json:"schema_version"`
		Accepted      catalog.AcceptedState `json:"accepted"`
	}{1, accepted})
	snapshot.Records["state/discovery/catalog.json"] = state.Record{Kind: "discovery_catalog", SchemaVersion: 1, Data: data}
}

func mustSiteFixtureJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func siteFixtureDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func writeSiteFixtureRecord(t *testing.T, root, logicalPath string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(logicalPath, "data/")))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
