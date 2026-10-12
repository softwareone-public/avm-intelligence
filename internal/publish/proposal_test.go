package publish

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
	"github.com/softwareone-public/avm-intelligence/internal/worker"
)

func TestCatalogOutboxBuildsIndependentProposalWithoutWorkerEvidence(t *testing.T) {
	moduleID := "avm-res-proposal-demo"
	base := strings.Repeat("a", 40)
	baseRoot, proposalRoot := t.TempDir(), t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate")
	oldIndex := index.CatalogIndex{
		SchemaVersion: 1, RecordType: "catalog_index",
		CatalogGeneration: 1,
		Modules:           []index.CatalogModuleEntry{{ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-proposal-demo", Lifecycle: "active"}},
	}
	oldIndex.IndexID = proposalTestIndexID(t, "catalog-index", oldIndex)
	newIndex := oldIndex
	newIndex.CatalogGeneration = 2
	newIndex.IndexID = proposalTestIndexID(t, "catalog-index", newIndex)
	oldBytes, err := json.Marshal(oldIndex)
	if err != nil {
		t.Fatal(err)
	}
	newBytes, err := json.Marshal(newIndex)
	if err != nil {
		t.Fatal(err)
	}
	writeAcceptedDataPayload(t, baseRoot, "data/catalog.json", oldBytes)
	writeProposalPayload(t, proposalRoot, "data/catalog.json", newBytes)
	outbox := discovery.CatalogProposal{
		SchemaVersion: 1, ProposalKind: "catalog", Generation: 2,
		ObservationID: "discovery-observation-" + strings.Repeat("3", 64),
		ModuleIDs:     []string{moduleID}, CreatedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
	changes := []ProposalChange{{
		Path: "data/catalog.json", Operation: "set_pointer", Digest: digestBytes(newBytes), RecordID: newIndex.IndexID,
	}}
	expectedPointer := oldIndex.IndexID
	proposal, err := BuildCatalogProposal(proposalRoot, outbox, base, &expectedPointer, changes)
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	if err = ApplyProposal(proposalRoot, base, baseRoot, outputRoot); err != nil {
		t.Fatal(err)
	}
	accepted, err := os.ReadFile(filepath.Join(outputRoot, "catalog.json"))
	if err != nil || !bytes.Equal(accepted, newBytes) {
		t.Fatalf("catalog-only proposal was not accepted: read err = %v", err)
	}
}

func TestCatalogIndexProposalUsesExactDiscoveryMembership(t *testing.T) {
	base := strings.Repeat("a", 40)
	proposalRoot := t.TempDir()
	baseRoot := t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate")
	moduleIDs := []string{"avm-ptn-catalog-b", "avm-res-catalog-a"}
	modules := []index.CatalogModuleEntry{
		{ModuleID: moduleIDs[1], Repository: "Azure/terraform-azurerm-avm-res-catalog-a", Lifecycle: "active"},
		{ModuleID: moduleIDs[0], Repository: "Azure/terraform-azurerm-avm-ptn-catalog-b", Lifecycle: "retired"},
	}
	currentCatalog := index.CatalogIndex{
		SchemaVersion: 1, RecordType: "catalog_index", CatalogGeneration: 1,
		Modules: []index.CatalogModuleEntry{
			{ModuleID: moduleIDs[0], Repository: modules[1].Repository, Lifecycle: "retired"},
			{ModuleID: moduleIDs[1], Repository: modules[0].Repository, Lifecycle: "active"},
		},
	}
	currentCatalog.IndexID = proposalTestIndexID(t, "catalog-index", currentCatalog)
	writeAcceptedDataPayload(t, baseRoot, catalogPointerPath, mustMarshalProposalTest(t, currentCatalog))
	outbox := discovery.CatalogProposal{
		SchemaVersion: 1, ProposalKind: "catalog", Generation: 1,
		ObservationID: "discovery-observation-" + strings.Repeat("3", 64),
		ModuleIDs:     moduleIDs, CreatedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
	metadata := []CatalogModuleMetadata{
		{SchemaVersion: 2, RecordType: "module_metadata", ModuleID: moduleIDs[0], ModuleType: "ptn", Status: "Available", Repository: modules[1].Repository, Lifecycle: "retired", DisplayName: "Catalog B", Description: "Catalog B", MissingObservations: 1, LastSeenGeneration: 1},
		{SchemaVersion: 2, RecordType: "module_metadata", ModuleID: moduleIDs[1], ModuleType: "res", Status: "Available", Repository: modules[0].Repository, Lifecycle: "active", DisplayName: "Catalog A", Description: "Catalog A", LastSeenGeneration: 1},
	}
	expectedPointer := currentCatalog.IndexID
	proposal, err := BuildCatalogIndexProposal(CatalogIndexProposalInput{
		ProposalRoot: proposalRoot, BaseDataRoot: baseRoot, Outbox: outbox,
		Generation: 1, Modules: modules, Metadata: metadata,
		ExpectedBase: base, ExpectedPointer: &expectedPointer,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	changedPaths := append(append([]string(nil), proposal.Paths...), proposalFilename)
	if err = ApplyProposalPatch(proposalRoot, base, baseRoot, outputRoot, changedPaths); err != nil {
		t.Fatal(err)
	}
	accepted, err := os.ReadFile(filepath.Join(outputRoot, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalogIndex index.CatalogIndex
	if err = json.Unmarshal(accepted, &catalogIndex); err != nil {
		t.Fatal(err)
	}
	if catalogIndex.CatalogGeneration != 1 || len(catalogIndex.Modules) != len(moduleIDs) || catalogIndex.Modules[0].ModuleID != moduleIDs[0] || catalogIndex.Modules[1].ModuleID != moduleIDs[1] {
		t.Fatalf("accepted catalog does not match discovery membership: %+v", catalogIndex)
	}
	if _, err = os.Stat(filepath.Join(outputRoot, "dataset.json")); err != nil {
		t.Fatalf("catalog bootstrap did not write the first typed dataset: %v", err)
	}
	for _, moduleID := range moduleIDs {
		metadataPath, pathErr := identity.ModuleMetadataPath(moduleID)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		if _, err = os.Stat(filepath.Join(outputRoot, filepath.FromSlash(strings.TrimPrefix(metadataPath, "data/")))); err != nil {
			t.Fatalf("catalog bootstrap omitted metadata for %s: %v", moduleID, err)
		}
	}
	if len(proposal.Changes) <= len(moduleIDs) {
		t.Fatalf("catalog bootstrap proposal lacks metadata and generated indexes: %+v", proposal.Changes)
	}
}

func TestCatalogProposalRejectsMetadataOutsideOutboxMembership(t *testing.T) {
	proposalRoot := t.TempDir()
	base := strings.Repeat("a", 40)
	moduleID := "avm-res-catalog-member"
	indexRecord := index.CatalogIndex{
		SchemaVersion: 1, RecordType: "catalog_index", CatalogGeneration: 2,
		Modules: []index.CatalogModuleEntry{{ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-catalog-member", Lifecycle: "active"}},
	}
	indexRecord.IndexID = proposalTestIndexID(t, "catalog-index", indexRecord)
	indexData := mustMarshalProposalTest(t, indexRecord)
	writeProposalPayload(t, proposalRoot, catalogPointerPath, indexData)
	metadata := CatalogModuleMetadata{
		SchemaVersion: 2, RecordType: "module_metadata", ModuleID: "avm-res-catalog-outsider",
		ModuleType: "res", Status: "Available", Repository: "Azure/terraform-azurerm-avm-res-catalog-outsider",
		Lifecycle: "active", DisplayName: "Outsider", Description: "Outsider", LastSeenGeneration: 2,
	}
	metadataPath, err := identity.ModuleMetadataPath(metadata.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	metadataData := mustMarshalProposalTest(t, metadata)
	writeProposalPayload(t, proposalRoot, metadataPath, metadataData)
	outbox := discovery.CatalogProposal{
		SchemaVersion: 1, ProposalKind: "catalog", Generation: 2,
		ObservationID: "discovery-observation-" + strings.Repeat("4", 64),
		ModuleIDs:     []string{moduleID}, CreatedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
	changes := []ProposalChange{
		{Path: catalogPointerPath, Operation: "set_pointer", Digest: digestBytes(indexData), RecordID: indexRecord.IndexID},
		{Path: metadataPath, Operation: "set_pointer", Digest: digestBytes(metadataData)},
	}
	if _, err = BuildCatalogProposal(proposalRoot, outbox, base, nil, changes, map[string]string{metadataPath: "absent"}); err == nil {
		t.Fatal("catalog proposal accepted module metadata outside discovery membership")
	}
}

func mustMarshalProposalTest(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCatalogModulePointerRequiresMatchingBasePrecondition(t *testing.T) {
	moduleID := "avm-res-proposal-demo"
	base := strings.Repeat("a", 40)
	baseRoot, proposalRoot := t.TempDir(), t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate")
	metadataPath, err := identity.ModuleMetadataPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	metadataRecord := CatalogModuleMetadata{
		SchemaVersion: 2, RecordType: "module_metadata", ModuleID: moduleID, ModuleType: "res",
		Status: "Available", Repository: "Azure/terraform-azurerm-avm-res-proposal-demo",
		Lifecycle: "active", DisplayName: "Proposal Demo", Description: "Proposal Demo", LastSeenGeneration: 1,
	}
	oldMetadata := mustMarshalProposalTest(t, metadataRecord)
	metadataRecord.Lifecycle = "retired"
	metadataRecord.MissingObservations = 2
	newMetadata := mustMarshalProposalTest(t, metadataRecord)
	writeAcceptedDataPayload(t, baseRoot, metadataPath, oldMetadata)
	writeProposalPayload(t, proposalRoot, metadataPath, newMetadata)
	oldCatalog := index.CatalogIndex{
		SchemaVersion: 1, RecordType: "catalog_index", CatalogGeneration: 1,
		Modules: []index.CatalogModuleEntry{{ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-proposal-demo", Lifecycle: "active"}},
	}
	oldCatalog.IndexID = proposalTestIndexID(t, "catalog-index", oldCatalog)
	newCatalog := index.CatalogIndex{
		SchemaVersion: 1, RecordType: "catalog_index", CatalogGeneration: 2,
		Modules: []index.CatalogModuleEntry{{ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-proposal-demo", Lifecycle: "retired"}},
	}
	newCatalog.IndexID = proposalTestIndexID(t, "catalog-index", newCatalog)
	oldCatalogData, _ := json.Marshal(oldCatalog)
	newCatalogData, _ := json.Marshal(newCatalog)
	writeAcceptedDataPayload(t, baseRoot, catalogPointerPath, oldCatalogData)
	writeProposalPayload(t, proposalRoot, catalogPointerPath, newCatalogData)
	outbox := discovery.CatalogProposal{
		SchemaVersion: 1, ProposalKind: "catalog", Generation: 2,
		ObservationID: "discovery-observation-" + strings.Repeat("4", 64),
		ModuleIDs:     []string{moduleID}, CreatedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
	changes := []ProposalChange{
		{Path: catalogPointerPath, Operation: "set_pointer", Digest: digestBytes(newCatalogData), RecordID: newCatalog.IndexID},
		{Path: metadataPath, Operation: "set_pointer", Digest: digestBytes(newMetadata)},
	}
	expectedPointer := oldCatalog.IndexID
	preconditions := map[string]string{metadataPath: digestBytes(oldMetadata)}
	proposal, err := BuildCatalogProposal(proposalRoot, outbox, base, &expectedPointer, changes, preconditions)
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	if err = ApplyProposal(proposalRoot, base, baseRoot, outputRoot); err != nil {
		t.Fatal(err)
	}
	accepted, err := os.ReadFile(filepath.Join(outputRoot, filepath.FromSlash(strings.TrimPrefix(metadataPath, "data/"))))
	if err != nil || !bytes.Equal(accepted, newMetadata) {
		t.Fatalf("catalog metadata update was not applied: read err = %v", err)
	}

	staleRoot := t.TempDir()
	writeProposalPayload(t, staleRoot, catalogPointerPath, newCatalogData)
	writeProposalPayload(t, staleRoot, metadataPath, newMetadata)
	staleOutput := filepath.Join(t.TempDir(), "candidate")
	staleProposal, err := BuildCatalogProposal(staleRoot, outbox, base, &expectedPointer, changes, map[string]string{metadataPath: strings.Repeat("0", 64)})
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, staleRoot, staleProposal)
	if err = ApplyProposal(staleRoot, base, baseRoot, staleOutput); err == nil {
		t.Fatal("catalog metadata update with a stale per-module precondition was accepted")
	}
}

func TestApplyProposalRebuildsGeneratedIndexesFromMergedTypedData(t *testing.T) {
	t.Helper()
	moduleID := "avm-res-rebuild-demo"
	repository := "Azure/terraform-azurerm-avm-res-rebuild-demo"
	sourceSHA := strings.Repeat("a", 40)
	base := strings.Repeat("b", 40)
	createdAt := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	baseRoot, proposalRoot := t.TempDir(), t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate-data")
	metadataPath, err := identity.ModuleMetadataPath(moduleID)
	requireProposalTestNoError(t, err)
	sourcePath, err := identity.ModuleSourcePath(moduleID, sourceSHA)
	requireProposalTestNoError(t, err)
	metadataRecord := CatalogModuleMetadata{
		SchemaVersion: 2, RecordType: "module_metadata", ModuleID: moduleID, ModuleType: "res",
		Status: "Available", Repository: repository, Lifecycle: "active",
		DisplayName: "Rebuild Demo", Description: "Rebuild Demo", LastSeenGeneration: 1,
	}
	oldMetadata := mustMarshalProposalTest(t, metadataRecord)
	metadataRecord.Lifecycle = "retired"
	metadataRecord.MissingObservations = 2
	newMetadata := mustMarshalProposalTest(t, metadataRecord)
	sourceRecord := []byte(`{"schema_version":2,"record_type":"source_revision","module_id":"` + moduleID + `","repository":"` + repository + `","source_sha":"` + sourceSHA + `","immutable":true}`)
	records := index.Records{
		CatalogGeneration: 1, CreatedAt: createdAt,
		Generator: index.Producer{ID: "fixture-generator", Version: "1.0.0", ContractVersion: 1},
		RecordDigests: map[string]string{
			metadataPath: digestBytes(oldMetadata), sourcePath: digestBytes(sourceRecord),
		},
		Modules:         []index.ModuleRecord{{ModuleID: moduleID, Repository: repository, Lifecycle: "active"}},
		SourceRevisions: []index.SourceRevisionRecord{{ModuleID: moduleID, Repository: repository, SourceSHA: sourceSHA, Immutable: true}},
	}
	writeAcceptedDataPayload(t, baseRoot, metadataPath, oldMetadata)
	writeAcceptedDataPayload(t, baseRoot, sourcePath, sourceRecord)
	writeIndexFixtureFiles(t, baseRoot, records)
	currentCatalogData, err := os.ReadFile(filepath.Join(baseRoot, "catalog.json"))
	requireProposalTestNoError(t, err)
	var currentCatalog index.CatalogIndex
	requireProposalTestNoError(t, json.Unmarshal(currentCatalogData, &currentCatalog))

	nextRecords := records
	nextRecords.CatalogGeneration = 2
	nextRecords.CreatedAt = createdAt
	nextRecords.Modules = []index.ModuleRecord{{ModuleID: moduleID, Repository: repository, Lifecycle: "retired"}}
	nextRecords.RecordDigests[metadataPath] = digestBytes(newMetadata)
	nextCatalogIndexes, err := index.Build(nextRecords)
	requireProposalTestNoError(t, err)
	nextFiles, err := nextCatalogIndexes.Files()
	requireProposalTestNoError(t, err)
	nextCatalogData := nextFiles[catalogPointerPath]
	if len(nextCatalogData) == 0 {
		t.Fatal("rebuilt proposal fixture lacks catalog index")
	}
	changes := changedGeneratedIndexProposalChanges(t, baseRoot, proposalRoot, nextFiles)
	writeProposalPayload(t, proposalRoot, metadataPath, newMetadata)
	outbox := discovery.CatalogProposal{
		SchemaVersion: 1, ProposalKind: "catalog", Generation: 2,
		ObservationID: "discovery-observation-" + strings.Repeat("c", 64),
		ModuleIDs:     []string{moduleID}, CreatedAt: createdAt.Add(time.Hour),
	}
	changes = append(changes, ProposalChange{Path: metadataPath, Operation: "set_pointer", Digest: digestBytes(newMetadata)})
	expectedPointer := currentCatalog.IndexID
	proposal, err := BuildCatalogProposal(proposalRoot, outbox, base, &expectedPointer, changes, map[string]string{metadataPath: digestBytes(oldMetadata)})
	requireProposalTestNoError(t, err)
	writeProposalEnvelope(t, proposalRoot, proposal)
	incompleteRoot, incompleteProposal := proposalWithoutChangePath(t, proposalRoot, proposal, datasetManifestPath)
	incompleteOutput := filepath.Join(t.TempDir(), "incomplete-candidate")
	incompleteChangedPaths := append(append([]string(nil), incompleteProposal.Paths...), proposalFilename)
	if err = ApplyProposalPatch(incompleteRoot, strings.Repeat("d", 40), baseRoot, incompleteOutput, incompleteChangedPaths); err == nil {
		t.Fatal("rebased proposal omitting its regenerated dataset manifest was accepted")
	}
	if _, err = os.Stat(incompleteOutput); !os.IsNotExist(err) {
		t.Fatalf("incomplete rebase created output: %v", err)
	}
	changedPaths := append(append([]string(nil), proposal.Paths...), proposalFilename)
	requireProposalTestNoError(t, ApplyProposalPatch(proposalRoot, strings.Repeat("d", 40), baseRoot, outputRoot, changedPaths))
	for _, relativePath := range []string{
		"catalog.json", "dataset.json", filepath.Join("modules", "res", moduleID, "index.json"),
	} {
		_, err = os.Stat(filepath.Join(outputRoot, relativePath))
		requireProposalTestNoError(t, err)
	}
	if _, err = os.Stat(filepath.Join(outputRoot, "data")); !os.IsNotExist(err) {
		t.Fatalf("applied candidate nested a second data directory: %v", err)
	}
	updatedMetadata, err := os.ReadFile(filepath.Join(outputRoot, filepath.FromSlash(strings.TrimPrefix(metadataPath, "data/"))))
	requireProposalTestNoError(t, err)
	if !bytes.Equal(updatedMetadata, newMetadata) {
		t.Fatalf("merged typed record was not preserved: read err = %v", err)
	}
}

func requireProposalTestNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func writeIndexFixtureFiles(t *testing.T, dataRoot string, records index.Records) {
	t.Helper()
	indexes, err := index.Build(records)
	if err != nil {
		t.Fatal(err)
	}
	files, err := indexes.Files()
	if err != nil {
		t.Fatal(err)
	}
	for canonicalPath, content := range files {
		writeAcceptedDataPayload(t, dataRoot, canonicalPath, content)
	}
}

func changedGeneratedIndexProposalChanges(t *testing.T, baseRoot, proposalRoot string, files map[string][]byte) []ProposalChange {
	t.Helper()
	changes := make([]ProposalChange, 0)
	for canonicalPath, content := range files {
		relativePath := strings.TrimPrefix(canonicalPath, "data/")
		baseContent, err := os.ReadFile(filepath.Join(baseRoot, filepath.FromSlash(relativePath)))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err == nil && bytes.Equal(baseContent, content) {
			continue
		}
		writeProposalPayload(t, proposalRoot, canonicalPath, content)
		var record struct {
			IndexID   string `json:"index_id"`
			DatasetID string `json:"dataset_id"`
		}
		if err = json.Unmarshal(content, &record); err != nil {
			t.Fatal(err)
		}
		recordID := record.IndexID
		if recordID == "" {
			recordID = record.DatasetID
		}
		changes = append(changes, ProposalChange{Path: canonicalPath, Operation: "set_pointer", Digest: digestBytes(content), RecordID: recordID})
	}
	return changes
}

func proposalWithoutChangePath(t *testing.T, sourceRoot string, proposal Proposal, omittedPath string) (string, Proposal) {
	t.Helper()
	proposalRoot := t.TempDir()
	changes := make([]ProposalChange, 0, len(proposal.Changes))
	for _, change := range proposal.Changes {
		if change.Path == omittedPath {
			continue
		}
		data, err := readProposalPayload(sourceRoot, change.Path)
		if err != nil {
			t.Fatal(err)
		}
		writeProposalPayload(t, proposalRoot, change.Path, data)
		changes = append(changes, change)
	}
	proposal.Changes = changes
	proposal.Paths = proposalChangePaths(changes)
	proposal, err := IdentifyProposal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	return proposalRoot, proposal
}

func TestProviderObservationAndPresentationProposalsAdvanceTheirPointers(t *testing.T) {
	t.Run("provider snapshot", testProviderSnapshotProposal)
	t.Run("module observation", testModuleObservationProposal)
	t.Run("presentation", testPresentationProposal)
}

func testProviderSnapshotProposal(t *testing.T) {
	base := strings.Repeat("a", 40)
	address, version := "registry.terraform.io/hashicorp/azurerm", "4.0.0"
	indexPath, err := identity.ProviderIndexPath(address)
	if err != nil {
		t.Fatal(err)
	}
	baseRoot, proposalRoot := t.TempDir(), t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate")
	snapshot := assessment.ProviderSnapshot{
		SchemaVersion: 2, RecordType: "provider_snapshot",
		ProviderAddress: address, ProviderVersion: version,
		Coverage:     assessment.SnapshotCoverage{Capabilities: map[string]string{"resource": "complete"}, Complete: true, ObservedAt: "2026-10-02T12:00:00Z"},
		Entries:      []assessment.ProviderEntry{{Kind: "resource", Name: "azurerm_resource_group"}},
		Producer:     assessment.SnapshotProducer{ID: "fixture", Version: "1.0.0", ContractVersion: 1},
		SourceDigest: strings.Repeat("c", 64), ObservedAt: "2026-10-02T12:00:00Z",
	}
	snapshot.SnapshotID, err = identity.ID("snapshot", snapshot.SchemaVersion, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshotID := snapshot.SnapshotID
	snapshotPath, err := identity.ProviderSnapshotManifestPath(address, version, snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	oldIndex := index.ProviderIndex{SchemaVersion: 1, RecordType: "provider_index", ProviderAddress: address, Snapshots: []index.ProviderSnapshotRecord{}}
	oldIndex.IndexID = proposalTestIndexID(t, "provider-index", oldIndex)
	newIndex := index.ProviderIndex{SchemaVersion: 1, RecordType: "provider_index", ProviderAddress: address, Snapshots: []index.ProviderSnapshotRecord{{ID: snapshotID, Address: address, Version: version}}}
	newIndex.IndexID = proposalTestIndexID(t, "provider-index", newIndex)
	oldBytes, _ := json.Marshal(oldIndex)
	indexBytes, _ := json.Marshal(newIndex)
	snapshotBytes, _ := json.Marshal(snapshot)
	writeAcceptedDataPayload(t, baseRoot, indexPath, oldBytes)
	writeProposalPayload(t, proposalRoot, indexPath, indexBytes)
	writeProposalPayload(t, proposalRoot, snapshotPath, snapshotBytes)
	changes := []ProposalChange{
		{Path: snapshotPath, Operation: "add_immutable", Digest: digestBytes(snapshotBytes), RecordID: snapshotID},
		{Path: indexPath, Operation: "set_pointer", Digest: digestBytes(indexBytes), RecordID: newIndex.IndexID},
	}
	expectedPointer := oldIndex.IndexID
	proposal, err := IdentifyProposal(Proposal{
		Kind: ProposalProviderSnapshot, Subject: address + "@" + version, SourceGeneration: 2,
		ExpectedBase: base, ExpectedPointer: &expectedPointer,
		Producer: ProposalProducer{ID: expectedProposalProducer(ProposalProviderSnapshot), Version: "1.0.0", ContractVersion: 1},
		Paths:    proposalChangePaths(changes), Changes: changes,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	if err = ApplyProposal(proposalRoot, base, baseRoot, outputRoot); err != nil {
		t.Fatal(err)
	}
}

func TestBuildProviderSnapshotProposalAppliesWithZeroConsumers(t *testing.T) {
	const address = "registry.terraform.io/hashicorp/azurerm"
	const version = "4.0.0"
	base := strings.Repeat("a", 40)
	baseRoot, proposalRoot := t.TempDir(), t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate")
	indexPath, err := identity.ProviderIndexPath(address)
	if err != nil {
		t.Fatal(err)
	}
	currentIndex := index.ProviderIndex{
		SchemaVersion: 1, RecordType: "provider_index", ProviderAddress: address,
		Snapshots: []index.ProviderSnapshotRecord{},
	}
	currentIndex.IndexID = proposalTestIndexID(t, "provider-index", currentIndex)
	writeAcceptedDataRecord(t, baseRoot, indexPath, currentIndex)

	snapshot, err := identifyProviderSnapshot(assessment.ProviderSnapshot{
		SchemaVersion: 2, RecordType: "provider_snapshot", ProviderAddress: address, ProviderVersion: version,
		Coverage: assessment.SnapshotCoverage{
			Capabilities: map[string]string{"resource": "partial"}, Complete: false,
			ObservedAt: "2026-10-04T12:00:00Z",
		},
		Entries:      []assessment.ProviderEntry{{Kind: "resource", Name: "azurerm_resource_group"}},
		Producer:     assessment.SnapshotProducer{ID: "avm-intelligence-provider", Version: "1.0.0", ContractVersion: 1},
		SourceDigest: strings.Repeat("c", 64), ObservedAt: "2026-10-04T12:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := BuildProviderSnapshotProposal(ProviderSnapshotProposalInput{
		ProposalRoot: proposalRoot, BaseDataRoot: baseRoot, ExpectedBase: base, SourceGeneration: 1,
		Snapshot: snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.ExpectedPointer == nil || *proposal.ExpectedPointer != currentIndex.IndexID {
		t.Fatalf("expected pointer = %v, want %q", proposal.ExpectedPointer, currentIndex.IndexID)
	}
	if err = ApplyProposal(proposalRoot, base, baseRoot, outputRoot); err != nil {
		t.Fatal(err)
	}

	var acceptedIndex index.ProviderIndex
	acceptedIndexPath := filepath.Join(outputRoot, filepath.FromSlash(strings.TrimPrefix(indexPath, "data/")))
	acceptedIndexData, err := os.ReadFile(acceptedIndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(acceptedIndexData, &acceptedIndex); err != nil {
		t.Fatal(err)
	}
	if len(acceptedIndex.Snapshots) != 1 || acceptedIndex.Snapshots[0].Address != address || acceptedIndex.Snapshots[0].Version != version {
		t.Fatalf("accepted provider index = %#v, want one exact provider/version snapshot", acceptedIndex)
	}
	snapshotPath, err := identity.ProviderSnapshotManifestPath(address, version, acceptedIndex.Snapshots[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotRelativePath, err := dataRelativePath(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshotData, err := os.ReadFile(filepath.Join(outputRoot, filepath.FromSlash(snapshotRelativePath)))
	if err != nil {
		t.Fatal(err)
	}
	var acceptedSnapshot assessment.ProviderSnapshot
	if err = json.Unmarshal(snapshotData, &acceptedSnapshot); err != nil {
		t.Fatal(err)
	}
	if acceptedSnapshot.Coverage.Complete || acceptedSnapshot.Coverage.Capabilities["resource"] != "partial" {
		t.Fatalf("accepted snapshot coverage = %#v, want partial", acceptedSnapshot.Coverage)
	}
}

func TestIdentifyProviderSnapshotRejectsIncompleteCompleteCoverage(t *testing.T) {
	validSnapshot := assessment.ProviderSnapshot{
		SchemaVersion: 2, RecordType: "provider_snapshot",
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", ProviderVersion: "4.0.0",
		Coverage: assessment.SnapshotCoverage{
			Capabilities: map[string]string{"resource": "complete"}, Complete: true,
			ObservedAt: "2026-10-04T12:00:00Z",
		},
		Entries:      []assessment.ProviderEntry{{Kind: "resource", Name: "azurerm_resource_group"}},
		Producer:     assessment.SnapshotProducer{ID: "avm-intelligence-provider", Version: "1.0.0", ContractVersion: 1},
		SourceDigest: strings.Repeat("c", 64), ObservedAt: "2026-10-04T12:00:00Z",
	}
	tests := map[string]func(*assessment.ProviderSnapshot){
		"no capability coverage": func(snapshot *assessment.ProviderSnapshot) {
			snapshot.Coverage.Capabilities = map[string]string{}
		},
		"partial capability": func(snapshot *assessment.ProviderSnapshot) {
			snapshot.Coverage.Capabilities["resource"] = "partial"
		},
		"empty inventory": func(snapshot *assessment.ProviderSnapshot) {
			snapshot.Entries = nil
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			snapshot := validSnapshot
			snapshot.Coverage.Capabilities = map[string]string{"resource": "complete"}
			snapshot.Entries = append([]assessment.ProviderEntry(nil), validSnapshot.Entries...)
			mutate(&snapshot)
			if _, err := identifyProviderSnapshot(snapshot); err == nil {
				t.Fatal("incomplete inventory was accepted as complete")
			}
		})
	}
}

func TestIdentifyProviderSnapshotVerifiesProvidedContentID(t *testing.T) {
	snapshot := assessment.ProviderSnapshot{
		SchemaVersion: 2, RecordType: "provider_snapshot",
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", ProviderVersion: "4.0.0",
		Coverage: assessment.SnapshotCoverage{
			Capabilities: map[string]string{"resource": "partial"}, Complete: false,
			ObservedAt: "2026-10-04T12:00:00Z",
		},
		Entries:      []assessment.ProviderEntry{{Kind: "resource", Name: "azurerm_resource_group"}},
		Producer:     assessment.SnapshotProducer{ID: "avm-intelligence-provider", Version: "1.0.0", ContractVersion: 1},
		SourceDigest: strings.Repeat("c", 64), ObservedAt: "2026-10-04T12:00:00Z",
	}
	identified, err := identifyProviderSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := identifyProviderSnapshot(identified)
	if err != nil || replayed.SnapshotID != identified.SnapshotID {
		t.Fatalf("re-identify supplied snapshot ID %q: got %q, error %v", identified.SnapshotID, replayed.SnapshotID, err)
	}
	identified.SnapshotID = "snapshot-" + strings.Repeat("f", 64)
	if _, err = identifyProviderSnapshot(identified); err == nil {
		t.Fatal("mismatched supplied provider snapshot ID was accepted")
	}
}

func testModuleObservationProposal(t *testing.T) {
	base := strings.Repeat("a", 40)
	moduleID, kind := "avm-res-proposal-demo", "issues"
	oldID := "observation-" + strings.Repeat("1", 64)
	newID := "observation-" + strings.Repeat("2", 64)
	indexPath := "data/observations/modules/" + moduleID + "/index.json"
	observationPath, err := identity.ModuleObservationPath(moduleID, newID)
	if err != nil {
		t.Fatal(err)
	}
	baseRoot, proposalRoot := t.TempDir(), t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate")
	oldPointer := oldID
	oldIndex := moduleObservationIndex{IndexID: "module-observation-index-" + strings.Repeat("3", 64), SchemaVersion: 1, RecordType: "module_observation_index", ModuleID: moduleID, Kind: kind, Generation: 1, ObservationIDs: []string{oldID}, CurrentID: &oldPointer}
	newPointer := newID
	newIndex := moduleObservationIndex{IndexID: "module-observation-index-" + strings.Repeat("4", 64), SchemaVersion: 1, RecordType: "module_observation_index", ModuleID: moduleID, Kind: kind, Generation: 2, ObservationIDs: []string{oldID, newID}, CurrentID: &newPointer}
	observation := moduleObservationRecord{
		SchemaVersion: 2, RecordType: "module_observation", ObservationID: newID,
		ModuleID: moduleID, Kind: kind, Generation: 2, SourceSHA: strings.Repeat("b", 40),
		SourceDigest: strings.Repeat("c", 64),
		Producer:     ProposalProducer{ID: expectedProposalProducer(ProposalModuleObservation), Version: "1.0.0", ContractVersion: 1},
		Coverage:     "complete", ObservedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
	indexOldBytes, _ := json.Marshal(oldIndex)
	indexNewBytes, _ := json.Marshal(newIndex)
	observationBytes, _ := json.Marshal(observation)
	writeAcceptedDataPayload(t, baseRoot, indexPath, indexOldBytes)
	writeProposalPayload(t, proposalRoot, indexPath, indexNewBytes)
	writeProposalPayload(t, proposalRoot, observationPath, observationBytes)
	changes := []ProposalChange{
		{Path: observationPath, Operation: "add_immutable", Digest: digestBytes(observationBytes), RecordID: newID},
		{Path: indexPath, Operation: "set_pointer", Digest: digestBytes(indexNewBytes), RecordID: newIndex.IndexID},
	}
	expectedPointer := oldID
	proposal, err := IdentifyProposal(Proposal{
		Kind: ProposalModuleObservation, Subject: moduleID + "@" + kind, SourceGeneration: 2,
		ExpectedBase: base, ExpectedPointer: &expectedPointer,
		Producer: ProposalProducer{ID: expectedProposalProducer(ProposalModuleObservation), Version: "1.0.0", ContractVersion: 1},
		Paths:    proposalChangePaths(changes), Changes: changes,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	if err = ApplyProposal(proposalRoot, base, baseRoot, outputRoot); err != nil {
		t.Fatal(err)
	}
}

func testPresentationProposal(t *testing.T) {
	base := strings.Repeat("a", 40)
	oldID := "presentation-" + strings.Repeat("1", 64)
	newID := "presentation-" + strings.Repeat("2", 64)
	indexPath := identity.PresentationIndexPath()
	snapshotPath, err := identity.PresentationPath(newID)
	if err != nil {
		t.Fatal(err)
	}
	baseRoot, proposalRoot := t.TempDir(), t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate")
	oldIndex := index.PresentationIndex{SchemaVersion: 1, RecordType: "presentation_index", Snapshots: []index.PresentationSnapshotRecord{{ID: oldID, Generation: 1, AsOf: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), SelectionReceiptIDs: []string{}}}, CurrentID: &oldID}
	oldIndex.IndexID = proposalTestIndexID(t, "presentation-index", oldIndex)
	newIndex := index.PresentationIndex{SchemaVersion: 1, RecordType: "presentation_index", Snapshots: append(append([]index.PresentationSnapshotRecord(nil), oldIndex.Snapshots...), index.PresentationSnapshotRecord{ID: newID, Generation: 2, AsOf: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), SelectionReceiptIDs: []string{}}), CurrentID: &newID}
	newIndex.IndexID = proposalTestIndexID(t, "presentation-index", newIndex)
	snapshotBytes := []byte(`{"schema_version":2,"record_type":"presentation_snapshot","presentation_id":"` + newID + `","generation":2,"as_of":"2026-10-02T12:00:00Z","modules":[{"module_id":"avm-res-proposal-demo","lifecycle":"active","coverage":{"capabilities":{},"complete":false,"observed_at":"2026-10-02T12:00:00Z"}}],"collector_health":[]}`)
	indexOldBytes, _ := json.Marshal(oldIndex)
	indexNewBytes, _ := json.Marshal(newIndex)
	writeAcceptedDataPayload(t, baseRoot, indexPath, indexOldBytes)
	writeProposalPayload(t, proposalRoot, indexPath, indexNewBytes)
	writeProposalPayload(t, proposalRoot, snapshotPath, snapshotBytes)
	changes := []ProposalChange{
		{Path: snapshotPath, Operation: "add_immutable", Digest: digestBytes(snapshotBytes), RecordID: newID},
		{Path: indexPath, Operation: "set_pointer", Digest: digestBytes(indexNewBytes), RecordID: newIndex.IndexID},
	}
	expectedPointer := oldID
	proposal, err := IdentifyProposal(Proposal{
		Kind: ProposalPresentation, Subject: "presentation", SourceGeneration: 2,
		ExpectedBase: base, ExpectedPointer: &expectedPointer,
		Producer: ProposalProducer{ID: expectedProposalProducer(ProposalPresentation), Version: "1.0.0", ContractVersion: 1},
		Paths:    proposalChangePaths(changes), Changes: changes,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	if err = ApplyProposal(proposalRoot, base, baseRoot, outputRoot); err != nil {
		t.Fatal(err)
	}
}

func TestBuildPresentationProposalAppliesAndAdvancesAsOfHistory(t *testing.T) {
	baseDataRoot, firstProposalRoot := t.TempDir(), t.TempDir()
	firstOutputRoot := filepath.Join(t.TempDir(), "accepted")
	firstAsOf := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	modules := []PresentationModule{{
		ModuleID: "avm-res-presentation-demo", Lifecycle: "active", SelectedReceiptIDs: []string{},
		Coverage: json.RawMessage(`{"capabilities":{},"complete":false,"observed_at":"2026-10-04T12:00:00Z","reason":"facts unavailable"}`),
	}}
	first, err := BuildPresentationProposal(PresentationProposalInput{
		ProposalRoot: firstProposalRoot, BaseDataRoot: baseDataRoot,
		ExpectedBase: strings.Repeat("a", 40), AsOf: firstAsOf, Modules: modules,
		CollectorHealth: []PresentationCollectorHealth{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ExpectedPointer != nil {
		t.Fatalf("initial presentation pointer = %v, want nil", first.ExpectedPointer)
	}
	firstSnapshotID := presentationSnapshotChangeID(t, first)
	if err = ApplyProposal(firstProposalRoot, first.ExpectedBase, baseDataRoot, firstOutputRoot); err != nil {
		t.Fatal(err)
	}

	secondProposalRoot := t.TempDir()
	secondOutputRoot := filepath.Join(t.TempDir(), "accepted")
	second, err := BuildPresentationProposal(PresentationProposalInput{
		ProposalRoot: secondProposalRoot, BaseDataRoot: firstOutputRoot,
		ExpectedBase: strings.Repeat("b", 40), AsOf: firstAsOf.Add(24 * time.Hour), Modules: modules,
		CollectorHealth: []PresentationCollectorHealth{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ExpectedPointer == nil || *second.ExpectedPointer != firstSnapshotID {
		t.Fatalf("second presentation pointer = %v, want first snapshot ID %q", second.ExpectedPointer, firstSnapshotID)
	}
	secondSnapshotID := presentationSnapshotChangeID(t, second)
	if err = ApplyProposal(secondProposalRoot, second.ExpectedBase, firstOutputRoot, secondOutputRoot); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(secondOutputRoot, "presentation", "index.json")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var acceptedIndex index.PresentationIndex
	if err = json.Unmarshal(indexData, &acceptedIndex); err != nil {
		t.Fatal(err)
	}
	if len(acceptedIndex.Snapshots) != 2 || acceptedIndex.Snapshots[1].Generation != 2 || acceptedIndex.CurrentID == nil || *acceptedIndex.CurrentID != secondSnapshotID {
		t.Fatalf("accepted presentation history = %#v", acceptedIndex)
	}
}

func presentationSnapshotChangeID(t *testing.T, proposal Proposal) string {
	t.Helper()
	for _, change := range proposal.Changes {
		if change.Operation == "add_immutable" {
			return change.RecordID
		}
	}
	t.Fatal("presentation proposal has no immutable snapshot change")
	return ""
}

func TestApplyModuleEvidenceAddsFactsAndHistoryWithoutSelectionChanges(t *testing.T) {
	moduleID := "avm-res-proposal-demo"
	sourceSHA := strings.Repeat("b", 40)
	base := strings.Repeat("a", 40)
	sourceRoot := t.TempDir()
	configuration := `terraform {
	required_providers {
		azurerm = {
			source = "hashicorp/azurerm"
		}
	}
}`
	if err := os.WriteFile(filepath.Join(sourceRoot, "main.tf"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	units, err := terraform.DiscoverUnits(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: sourceRoot, ModuleID: moduleID, SourceSHA: sourceSHA,
		ObservedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC), Units: units,
	})
	if err != nil {
		t.Fatal(err)
	}
	baseRoot, proposalRoot := t.TempDir(), t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate")
	sourcePath, err := identity.ModuleSourcePath(moduleID, sourceSHA)
	if err != nil {
		t.Fatal(err)
	}
	factsPath, err := identity.ModuleRecordPath(moduleID, sourceSHA, "facts", facts.FactsID)
	if err != nil {
		t.Fatal(err)
	}
	moduleIndexPath, err := identity.ModuleIndexPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	sourceRecord, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		RecordType    string `json:"record_type"`
		ModuleID      string `json:"module_id"`
		SourceSHA     string `json:"source_sha"`
		Immutable     bool   `json:"immutable"`
	}{1, "source_revision", moduleID, sourceSHA, true})
	if err != nil {
		t.Fatal(err)
	}
	factsRecord, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	moduleIndex := index.ModuleIndex{
		SchemaVersion: 1, RecordType: "module_index",
		ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-proposal-demo", Lifecycle: "active",
		SourceSHAs: []string{sourceSHA}, FactsIDs: []string{facts.FactsID},
		SelectionIDs: []string{}, SelectionHistory: []index.SelectionPointer{}, CurrentSelections: []index.SelectionPointer{},
	}
	moduleIndex.IndexID = proposalTestIndexID(t, "module-index", moduleIndex)
	indexRecord, err := json.Marshal(moduleIndex)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{sourcePath: sourceRecord, factsPath: factsRecord} {
		writeProposalPayload(t, proposalRoot, path, content)
	}
	writeProposalPayload(t, proposalRoot, moduleIndexPath, indexRecord)
	changes := []ProposalChange{
		{Path: sourcePath, Operation: "add_immutable", Digest: digestBytes(sourceRecord)},
		{Path: factsPath, Operation: "add_immutable", Digest: digestBytes(factsRecord), RecordID: facts.FactsID},
		{Path: moduleIndexPath, Operation: "set_pointer", Digest: digestBytes(indexRecord), RecordID: moduleIndex.IndexID},
	}
	proposal, err := IdentifyProposal(Proposal{
		Kind: ProposalModuleEvidence, Subject: moduleID + "@" + sourceSHA, SourceGeneration: 1,
		ExpectedBase: base, Producer: ProposalProducer{ID: expectedProposalProducer(ProposalModuleEvidence), Version: "2.0.0", ContractVersion: 1},
		Paths: proposalChangePaths(changes), Changes: changes,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	if err = ApplyModuleEvidenceProposal(proposalRoot, base, baseRoot, outputRoot); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(outputRoot, filepath.FromSlash(strings.TrimPrefix(factsPath, "data/")))); err != nil {
		t.Fatalf("facts history was not accepted: %v", err)
	}
}

func TestBuildModuleEvidenceProposalForSealedAcquisition(t *testing.T) {
	moduleID := "avm-res-evidence-builder"
	repository := "Azure/terraform-azurerm-avm-res-evidence-builder"
	sourceSHA := strings.Repeat("b", 40)
	base := strings.Repeat("a", 40)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	baseRoot, proposalRoot := t.TempDir(), t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate")

	metadata := CatalogModuleMetadata{
		SchemaVersion: 2, RecordType: "module_metadata", ModuleID: moduleID, ModuleType: "res",
		Status: "Available", Repository: repository, Lifecycle: "active",
		DisplayName: "Evidence Builder", Description: "Evidence Builder", LastSeenGeneration: 1,
	}
	metadataPath, err := identity.ModuleMetadataPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	metadataData := mustMarshalProposalTest(t, metadata)
	records := index.Records{
		CatalogGeneration: 1, CreatedAt: now,
		Generator:     index.Producer{ID: "fixture-generator", Version: "1.0.0", ContractVersion: 1},
		RecordDigests: map[string]string{metadataPath: digestBytes(metadataData)},
		Modules:       []index.ModuleRecord{{ModuleID: moduleID, Repository: repository, Lifecycle: "active"}},
	}
	writeAcceptedDataPayload(t, baseRoot, metadataPath, metadataData)
	writeIndexFixtureFiles(t, baseRoot, records)

	sourceRoot := t.TempDir()
	writeProposalTestSource(t, sourceRoot, "main.tf", `terraform {
  required_providers {
    azurerm = { source = "hashicorp/azurerm" }
  }
}
resource "azurerm_resource_group" "main" {}`)
	request, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: moduleID, SourceSHA: sourceSHA, DesiredGeneration: 1,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{terraform.CapabilityProviderRequirements},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	const owner = "worker-run-1"
	claimed, err := queue.ClaimModuleRequest([]queue.Request{request}, request.ID, owner, now, now.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	workerInput := worker.Input{
		Request: claimed[0], Owner: owner, Fence: 1, SourceRoot: sourceRoot, ObservedAt: now,
		Producer: worker.Producer{ID: "avm-intelligence-factory", Version: "2.0.0", ContractVersion: 1},
	}
	workerInput.Request.Work.Capabilities = append([]string(nil), request.Work.Capabilities...)
	artifact, err := worker.Process(workerInput)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := BuildModuleEvidenceProposal(ModuleEvidenceProposalInput{
		ProposalRoot: proposalRoot, BaseDataRoot: baseRoot, ExpectedBase: base, Repository: repository,
		WorkerInput: workerInput, Artifact: artifact, ValidationID: "module-evidence-validator.v1", SealedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	changedPaths := append(append([]string(nil), proposal.Paths...), proposalFilename)
	if err = ApplyProposalPatch(proposalRoot, base, baseRoot, outputRoot, changedPaths); err != nil {
		t.Fatal(err)
	}
	accepted, err := index.ReadRecords(outputRoot, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted.SourceRevisions) != 1 || len(accepted.Facts) != 1 || len(accepted.Provenance) != 1 || len(accepted.Analyses) != 0 || len(accepted.Selections) != 0 {
		t.Fatalf("accepted source evidence has unexpected native records or selection: revisions=%d facts=%d provenance=%d analyses=%d selections=%d", len(accepted.SourceRevisions), len(accepted.Facts), len(accepted.Provenance), len(accepted.Analyses), len(accepted.Selections))
	}
	registry, err := assessment.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definitionID := ""
	var selectedDefinition assessment.DimensionDefinition
	for _, definition := range registry.ListDimensions() {
		if definition.DimensionID == assessment.DimensionInterfaceDocumentation {
			definitionID = definition.DefinitionID
			selectedDefinition = definition
		}
	}
	assessmentRequest, err := queue.NewAssessmentRequest(queue.AssessmentInput{
		ModuleID: moduleID, SourceSHA: sourceSHA, ContextID: "azurerm/4", Track: "azurerm/4", DesiredGeneration: 1,
		FactsID: artifact.FactsID, DefinitionIDs: []string{definitionID},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	assessmentClaim, err := queue.ClaimModuleRequest([]queue.Request{assessmentRequest}, assessmentRequest.ID, owner, now, now.Add(time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	assessmentInput := worker.Input{Request: assessmentClaim[0], Owner: owner, Fence: 2, Facts: artifact.Facts, ObservedAt: now, Producer: workerInput.Producer}
	analysisArtifact, err := worker.Process(assessmentInput)
	if err != nil {
		t.Fatal(err)
	}
	changedResult := analysisArtifact.DimensionResults[0]
	changedResult.Checks = append([]assessment.CheckResult(nil), changedResult.Checks...)
	changedResult.Checks[0].Status = assessment.StatusPass
	changedResultData := mustMarshalProposalTest(t, changedResult)
	if err := validateModuleEvidenceRecordContent("dimension_result", changedResult.DimensionResultID, changedResultData, moduleID, sourceSHA); err == nil {
		t.Fatal("acceptance trusted altered dimension content under old identity")
	}
	changedAnalysis := *analysisArtifact.Analysis
	changedAnalysis.Producer.Version = "unreviewed"
	if err := validateModuleEvidenceRecordContent("analysis", changedAnalysis.AnalysisID, mustMarshalProposalTest(t, changedAnalysis), moduleID, sourceSHA); err == nil {
		t.Fatal("acceptance trusted altered analysis producer under old identity")
	}
	analysisProposalRoot := t.TempDir()
	analysisProposal, err := BuildModuleEvidenceProposal(ModuleEvidenceProposalInput{
		ProposalRoot: analysisProposalRoot, BaseDataRoot: outputRoot, ExpectedBase: base, Repository: repository,
		WorkerInput: assessmentInput, Artifact: analysisArtifact, ValidationID: "module-evidence-validator.v1", SealedAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	definitionPath, err := identity.DefinitionPath(definitionID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsProposalString(analysisProposal.Paths, definitionPath) {
		t.Fatalf("analysis proposal omitted the exact compiled definition: %v", analysisProposal.Paths)
	}
	changedDefinition := selectedDefinition
	changedDefinition.Version++
	if err := validateModuleEvidenceRecordContent("dimension_definition", changedDefinition.DefinitionID, mustMarshalProposalTest(t, changedDefinition), moduleID, sourceSHA); err == nil {
		t.Fatal("proposal acceptance trusted an altered compiled dimension definition")
	}
	writeProposalEnvelope(t, analysisProposalRoot, analysisProposal)
	analysisOutput := filepath.Join(t.TempDir(), "analysis-candidate")
	if err := ApplyProposalPatch(analysisProposalRoot, base, outputRoot, analysisOutput, append(append([]string(nil), analysisProposal.Paths...), proposalFilename)); err != nil {
		t.Fatal(err)
	}
	analysisRecords, err := index.ReadRecords(analysisOutput, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(analysisRecords.Analyses) != 1 || len(analysisRecords.DimensionResults) != 1 || len(analysisRecords.Definitions) != 1 || len(analysisRecords.Facts) != 1 || len(analysisRecords.Provenance) != 2 || len(analysisRecords.Selections) != 0 {
		t.Fatalf("analysis acceptance did not preserve facts/selection boundary: %+v", analysisRecords)
	}
	sharedDefinitionRoot := filepath.Join(t.TempDir(), "accepted-shared-definition")
	if err := copyAcceptedTree(outputRoot, sharedDefinitionRoot); err != nil {
		t.Fatal(err)
	}
	definitionData, err := json.MarshalIndent(selectedDefinition, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeAcceptedDataPayload(t, sharedDefinitionRoot, definitionPath, definitionData)
	sharedRecords := accepted
	sharedRecords.Definitions = append(sharedRecords.Definitions, selectedDefinition)
	sharedRecords.RecordDigests[definitionPath] = digestBytes(definitionData)
	writeIndexFixtureFiles(t, sharedDefinitionRoot, sharedRecords)
	sharedProposalRoot := t.TempDir()
	sharedProposal, err := BuildModuleEvidenceProposal(ModuleEvidenceProposalInput{
		ProposalRoot: sharedProposalRoot, BaseDataRoot: sharedDefinitionRoot, ExpectedBase: base, Repository: repository,
		WorkerInput: assessmentInput, Artifact: analysisArtifact, ValidationID: "module-evidence-validator.v1", SealedAt: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if containsProposalString(sharedProposal.Paths, definitionPath) {
		t.Fatal("analysis proposal repeated an already accepted shared definition")
	}
	writeProposalEnvelope(t, sharedProposalRoot, sharedProposal)
	sharedOutput := filepath.Join(t.TempDir(), "shared-definition-candidate")
	if err := ApplyProposalPatch(sharedProposalRoot, base, sharedDefinitionRoot, sharedOutput, append(append([]string(nil), sharedProposal.Paths...), proposalFilename)); err != nil {
		t.Fatal(err)
	}
	sharedAnalysisRecords, err := index.ReadRecords(sharedOutput, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sharedAnalysisRecords.Definitions) != 1 {
		t.Fatalf("analysis using an accepted shared definition lost its typed reference: %d definitions", len(sharedAnalysisRecords.Definitions))
	}
	analysisPath, err := identity.ModuleRecordPath(moduleID, sourceSHA, "analysis", analysisArtifact.Analysis.AnalysisID)
	if err != nil {
		t.Fatal(err)
	}
	originalAnalysis, err := readOptionalDataFile(analysisOutput, analysisPath)
	if err != nil {
		t.Fatal(err)
	}
	assessmentInput.ObservedAt = now.Add(time.Minute)
	recomputed, err := worker.Process(assessmentInput)
	if err != nil {
		t.Fatal(err)
	}
	retryRoot := t.TempDir()
	retryProposal, err := BuildModuleEvidenceProposal(ModuleEvidenceProposalInput{
		ProposalRoot: retryRoot, BaseDataRoot: analysisOutput, ExpectedBase: base, Repository: repository,
		WorkerInput: assessmentInput, Artifact: recomputed, ValidationID: "module-evidence-validator.v1", SealedAt: now.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, retryRoot, retryProposal)
	retryOutput := filepath.Join(t.TempDir(), "retry-candidate")
	if err := ApplyProposalPatch(retryRoot, base, analysisOutput, retryOutput, append(append([]string(nil), retryProposal.Paths...), proposalFilename)); err != nil {
		t.Fatal(err)
	}
	reusedAnalysis, err := readOptionalDataFile(retryOutput, analysisPath)
	if err != nil || !bytes.Equal(originalAnalysis, reusedAnalysis) {
		t.Fatal("recomputation replaced accepted analysis bytes")
	}
}

func writeProposalTestSource(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestModuleDeltaProposalAdapterRejectsNonEvidenceProposal(t *testing.T) {
	base := strings.Repeat("a", 40)
	presentationID := "presentation-" + strings.Repeat("b", 64)
	recordPath, err := identity.PresentationPath(presentationID)
	if err != nil {
		t.Fatal(err)
	}
	proposalRoot := t.TempDir()
	baseRoot := t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate")
	payload := []byte(`{"record_type":"presentation_snapshot","presentation_id":"` + presentationID + `"}`)
	writeProposalPayload(t, proposalRoot, recordPath, payload)
	proposal := proposalFixture(ProposalPresentation, "presentation", base, recordPath, payload)
	proposal.Changes[0].RecordID = presentationID
	proposal, err = IdentifyProposal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	if err = ApplyModuleEvidenceProposal(proposalRoot, base, baseRoot, outputRoot); err == nil {
		t.Fatal("module evidence adapter accepted a presentation proposal")
	}
	if _, err = os.Stat(outputRoot); !os.IsNotExist(err) {
		t.Fatalf("rejected non-evidence proposal created output: %v", err)
	}
}

func TestApplySelectionExportRequiresExpectedPointerAndIncreasingSequence(t *testing.T) {
	t.Run("valid next selection", func(t *testing.T) {
		proposalRoot, baseRoot, outputRoot, proposal := selectionProposalFixture(t, 0, 1)
		if err := ApplyProposal(proposalRoot, proposal.ExpectedBase, baseRoot, outputRoot); err != nil {
			t.Fatal(err)
		}
		unchanged, err := os.ReadFile(filepath.Join(outputRoot, "unrelated", "retain.json"))
		if err != nil || string(unchanged) != "keep" {
			t.Fatalf("selection export changed unrelated accepted content: read err = %v", err)
		}
	})
	t.Run("stale pointer", func(t *testing.T) {
		proposalRoot, baseRoot, outputRoot, proposal := selectionProposalFixture(t, 0, 1)
		stalePointer := "selection-" + strings.Repeat("9", 64)
		proposal.ExpectedPointer = &stalePointer
		proposal, err := IdentifyProposal(proposal)
		if err != nil {
			t.Fatal(err)
		}
		writeProposalEnvelope(t, proposalRoot, proposal)
		if err = ApplyProposal(proposalRoot, proposal.ExpectedBase, baseRoot, outputRoot); err == nil {
			t.Fatal("selection export with a stale expected pointer was accepted")
		}
		if _, err = os.Stat(outputRoot); !os.IsNotExist(err) {
			t.Fatalf("stale selection proposal created output: %v", err)
		}
	})
	t.Run("non-increasing sequence", func(t *testing.T) {
		proposalRoot, baseRoot, outputRoot, proposal := selectionProposalFixture(t, 1, 1)
		if err := ApplyProposal(proposalRoot, proposal.ExpectedBase, baseRoot, outputRoot); err == nil {
			t.Fatal("selection export did not advance its current sequence")
		}
		if _, err := os.Stat(outputRoot); !os.IsNotExist(err) {
			t.Fatalf("stale sequence proposal created output: %v", err)
		}
	})
}

func TestApplySelectionExportKeepsAVMReleasesIndependent(t *testing.T) {
	proposalRoot, baseRoot, outputRoot, proposal := selectionProposalFixtureForReleases(t, "v1.0.0", 1, "v2.0.0", 1)
	if err := ApplyProposal(proposalRoot, proposal.ExpectedBase, baseRoot, outputRoot); err != nil {
		t.Fatal(err)
	}
	moduleIndexPath, err := identity.ModuleIndexPath("avm-res-proposal-demo")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(outputRoot, filepath.FromSlash(strings.TrimPrefix(moduleIndexPath, "data/"))))
	if err != nil {
		t.Fatal(err)
	}
	var module index.ModuleIndex
	if err = json.Unmarshal(data, &module); err != nil {
		t.Fatal(err)
	}
	if len(module.CurrentSelections) != 2 || module.CurrentSelections[0].AVMReleaseVersion != "v1.0.0" || module.CurrentSelections[1].AVMReleaseVersion != "v2.0.0" || module.CurrentSelections[0].Sequence != 1 || module.CurrentSelections[1].Sequence != 1 {
		t.Fatalf("selection export did not preserve independent release histories: %#v", module.CurrentSelections)
	}
}

func TestBuildSelectionExportProposalFromPendingOutbox(t *testing.T) {
	acceptedDataRoot := t.TempDir()
	if err := copyAcceptedTree(filepath.Join("..", "..", "data"), acceptedDataRoot); err != nil {
		t.Fatal(err)
	}
	const moduleID = "avm-res-insights-privatelinkscope"
	selectedAt := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	receipt := state.SelectionReceipt{
		SchemaVersion: 3, RecordType: "selection_receipt", ModuleID: moduleID,
		AVMReleaseVersion: "v1.0.0", ContextID: "azurerm-4", Sequence: 1,
		DesiredGeneration: 1, InputFingerprint: strings.Repeat("a", 64),
		EvidenceCommit: strings.Repeat("b", 40), SelectedAt: selectedAt,
	}
	receipt.ID = selectionStateReceiptID(t, receipt)
	if err := receipt.Validate(); err != nil {
		t.Fatalf("test selection receipt invalid: %v", err)
	}
	export := state.SelectionExport{
		SchemaVersion: 1, RecordType: "selection_export_intent", ID: receipt.ID,
		SelectionID: receipt.ID, ModuleID: moduleID, ContextID: receipt.ContextID,
		Status: state.ExportPending, Receipt: receipt, CreatedAt: selectedAt,
	}
	artifacts, err := BuildSelectionExportProposal(acceptedDataRoot, strings.Repeat("c", 40), export)
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.Proposal.Kind != ProposalSelectionExport || artifacts.Proposal.Subject != moduleID+"/v1.0.0/azurerm-4" {
		t.Fatalf("proposal identity = %+v", artifacts.Proposal)
	}
	if len(artifacts.Payloads) != 4 {
		t.Fatalf("selection proposal payload count = %d, want receipt plus three generated indexes", len(artifacts.Payloads))
	}
	proposalRoot := t.TempDir()
	for path, content := range artifacts.Payloads {
		writeProposalPayload(t, proposalRoot, path, content)
	}
	writeProposalEnvelope(t, proposalRoot, artifacts.Proposal)
	outputRoot := filepath.Join(t.TempDir(), "candidate")
	if err = ApplyProposal(proposalRoot, artifacts.Proposal.ExpectedBase, acceptedDataRoot, outputRoot); err != nil {
		t.Fatal(err)
	}
}

func selectionStateReceiptID(t *testing.T, receipt state.SelectionReceipt) string {
	t.Helper()
	id, err := identity.ID("selection", receipt.SchemaVersion, struct {
		SchemaVersion     int       `json:"schema_version"`
		RecordType        string    `json:"record_type"`
		ModuleID          string    `json:"module_id"`
		AVMReleaseVersion string    `json:"avm_release_version"`
		ContextID         string    `json:"context_id"`
		Sequence          uint64    `json:"sequence"`
		DesiredGeneration uint64    `json:"desired_generation"`
		SourceSHA         *string   `json:"source_sha"`
		FactsID           *string   `json:"facts_id"`
		InputFingerprint  string    `json:"input_fingerprint"`
		AnalysisID        *string   `json:"analysis_id"`
		EvidenceCommit    string    `json:"evidence_commit"`
		SelectedAt        time.Time `json:"selected_at"`
	}{
		receipt.SchemaVersion, receipt.RecordType, receipt.ModuleID, receipt.AVMReleaseVersion,
		receipt.ContextID, receipt.Sequence, receipt.DesiredGeneration, receipt.SourceSHA,
		receipt.FactsID, receipt.InputFingerprint, receipt.AnalysisID, receipt.EvidenceCommit, receipt.SelectedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestApplySelectionExportPreservesPriorTrackHistory(t *testing.T) {
	proposalRoot, baseRoot, outputRoot, proposal := selectionProposalFixtureForReleases(t, "v1.0.0", 1, "v2.0.0", 1)
	trackPath, err := identity.TrackIndexPath("azurerm-4")
	if err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(proposalRoot, filepath.FromSlash(trackPath))
	data, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatal(err)
	}
	var track index.TrackIndex
	if err = json.Unmarshal(data, &track); err != nil {
		t.Fatal(err)
	}
	track.Selections = track.Selections[1:]
	track.TargetVectors = track.TargetVectors[1:]
	data = mustMarshalProposal(t, track)
	if err = os.WriteFile(payloadPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	for position := range proposal.Changes {
		if proposal.Changes[position].Path == trackPath {
			proposal.Changes[position].Digest = digestBytes(data)
			proposal.Changes[position].RecordID = track.IndexID
		}
	}
	proposal, err = IdentifyProposal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	if err = ApplyProposal(proposalRoot, proposal.ExpectedBase, baseRoot, outputRoot); err == nil {
		t.Fatal("selection export dropped prior track history")
	}
	if _, err = os.Stat(outputRoot); !os.IsNotExist(err) {
		t.Fatalf("rejected selection proposal created output: %v", err)
	}
}

func selectionProposalFixture(t *testing.T, currentSequence, nextSequence uint64) (string, string, string, Proposal) {
	return selectionProposalFixtureForReleases(t, "v1.0.0", currentSequence, "v1.0.0", nextSequence)
}

func selectionProposalFixtureForReleases(t *testing.T, currentRelease string, currentSequence uint64, nextRelease string, nextSequence uint64) (string, string, string, Proposal) {
	t.Helper()
	const moduleID = "avm-res-proposal-demo"
	const contextID = "azurerm-4"
	base := strings.Repeat("a", 40)
	sourceSHA := strings.Repeat("b", 40)
	factsID := "facts-" + strings.Repeat("c", 64)
	analysisID := "analysis-" + strings.Repeat("d", 64)
	currentSelectionID := "selection-" + strings.Repeat("e", 64)
	inputFingerprint := strings.Repeat("f", 64)
	selectedAt := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	baseRoot, proposalRoot := t.TempDir(), t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate")
	writeAcceptedDataPayload(t, baseRoot, "data/unrelated/retain.json", []byte("keep"))
	moduleIndexPath, err := identity.ModuleIndexPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	trackIndexPath, err := identity.TrackIndexPath(contextID)
	if err != nil {
		t.Fatal(err)
	}
	selectionPointer := func(id string, release string, sequence uint64) index.SelectionPointer {
		analysis := analysisID
		source := sourceSHA
		facts := factsID
		return index.SelectionPointer{
			ModuleID: moduleID, AVMReleaseVersion: release, ContextID: contextID, Sequence: sequence, DesiredGeneration: 3,
			SelectionID: id, AnalysisID: &analysis, SourceSHA: &source, FactsID: &facts,
			InputFingerprint: inputFingerprint, SelectedAt: selectedAt, ProviderTargets: []index.ProviderTarget{},
		}
	}
	currentModule := index.ModuleIndex{
		SchemaVersion: 1, RecordType: "module_index", ModuleID: moduleID,
		Repository: "Azure/terraform-azurerm-avm-res-proposal-demo", Lifecycle: "active",
		SourceSHAs: []string{sourceSHA}, FactsIDs: []string{factsID}, AnalysisIDs: []string{analysisID},
		SelectionIDs: []string{}, SelectionHistory: []index.SelectionPointer{}, CurrentSelections: []index.SelectionPointer{},
	}
	currentTrack := index.TrackIndex{
		SchemaVersion: 1, RecordType: "track_index", ContextID: contextID,
		ModuleIDs: []string{}, Selections: []index.SelectionPointer{}, TargetVectors: []index.TrackTargetVector{},
	}
	if currentSequence > 0 {
		previous := selectionPointer(currentSelectionID, currentRelease, currentSequence)
		currentModule.SelectionIDs = []string{currentSelectionID}
		currentModule.SelectionHistory = []index.SelectionPointer{previous}
		currentModule.CurrentSelections = []index.SelectionPointer{previous}
		currentTrack.ModuleIDs = []string{moduleID}
		currentTrack.Selections = []index.SelectionPointer{previous}
		currentTrack.TargetVectors = []index.TrackTargetVector{{
			ModuleID: moduleID, AVMReleaseVersion: currentRelease, SelectionID: currentSelectionID, Sequence: currentSequence,
			AnalysisID: previous.AnalysisID, ProviderTargets: []index.ProviderTarget{},
		}}
	}
	currentModule.IndexID = proposalTestIndexID(t, "module-index", currentModule)
	currentTrack.IndexID = proposalTestIndexID(t, "track-index", currentTrack)
	writeAcceptedDataRecord(t, baseRoot, moduleIndexPath, currentModule)
	writeAcceptedDataRecord(t, baseRoot, trackIndexPath, currentTrack)
	for _, record := range []struct{ kind, id string }{{"facts", factsID}, {"analysis", analysisID}} {
		path, pathErr := identity.ModuleRecordPath(moduleID, sourceSHA, record.kind, record.id)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		writeAcceptedDataPayload(t, baseRoot, path, []byte(`{"record_type":"`+record.kind+`"}`))
	}
	sourcePath, err := identity.ModuleSourcePath(moduleID, sourceSHA)
	if err != nil {
		t.Fatal(err)
	}
	writeAcceptedDataPayload(t, baseRoot, sourcePath, []byte(`{"record_type":"source_revision"}`))

	receipt := selectionExportReceipt{
		SchemaVersion: 3, RecordType: "selection_receipt", ModuleID: moduleID, AVMReleaseVersion: nextRelease, ContextID: contextID,
		Sequence: nextSequence, DesiredGeneration: 3, SourceSHA: stringPointer(sourceSHA),
		FactsID: stringPointer(factsID), InputFingerprint: inputFingerprint,
		AnalysisID: stringPointer(analysisID), EvidenceCommit: base, SelectedAt: selectedAt,
	}
	receipt.ID, err = selectionReceiptID(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath, err := identity.SelectionPath(moduleID, contextID, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	newPointer := selectionPointer(receipt.ID, receipt.AVMReleaseVersion, receipt.Sequence)
	nextModule := currentModule
	nextModule.SelectionIDs = append(append([]string(nil), currentModule.SelectionIDs...), receipt.ID)
	nextModule.SelectionIDs, err = identity.SortedSet(nextModule.SelectionIDs)
	if err != nil {
		t.Fatal(err)
	}
	nextModule.SelectionHistory = append(append([]index.SelectionPointer(nil), currentModule.SelectionHistory...), newPointer)
	nextModule.CurrentSelections = append(append([]index.SelectionPointer(nil), currentModule.CurrentSelections...), newPointer)
	nextTrack := currentTrack
	if !containsProposalString(nextTrack.ModuleIDs, moduleID) {
		nextTrack.ModuleIDs = append(nextTrack.ModuleIDs, moduleID)
	}
	nextTrack.Selections = append(append([]index.SelectionPointer(nil), currentTrack.Selections...), newPointer)
	nextTrack.TargetVectors = append(append([]index.TrackTargetVector(nil), currentTrack.TargetVectors...), index.TrackTargetVector{
		ModuleID: moduleID, AVMReleaseVersion: receipt.AVMReleaseVersion, SelectionID: receipt.ID, Sequence: receipt.Sequence,
		AnalysisID: receipt.AnalysisID, ProviderTargets: []index.ProviderTarget{},
	})
	nextModule.IndexID = proposalTestIndexID(t, "module-index", nextModule)
	nextTrack.IndexID = proposalTestIndexID(t, "track-index", nextTrack)
	writeProposalPayload(t, proposalRoot, receiptPath, mustMarshalProposal(t, receipt))
	writeProposalPayload(t, proposalRoot, moduleIndexPath, mustMarshalProposal(t, nextModule))
	writeProposalPayload(t, proposalRoot, trackIndexPath, mustMarshalProposal(t, nextTrack))
	changes := []ProposalChange{
		{Path: receiptPath, Operation: "add_immutable", Digest: digestBytes(mustMarshalProposal(t, receipt)), RecordID: receipt.ID},
		{Path: moduleIndexPath, Operation: "set_pointer", Digest: digestBytes(mustMarshalProposal(t, nextModule)), RecordID: nextModule.IndexID},
		{Path: trackIndexPath, Operation: "set_pointer", Digest: digestBytes(mustMarshalProposal(t, nextTrack)), RecordID: nextTrack.IndexID},
	}
	expectedPointer := (*string)(nil)
	if currentSequence > 0 && currentRelease == nextRelease {
		expectedPointer = stringPointer(currentSelectionID)
	}
	proposal, err := IdentifyProposal(Proposal{
		Kind: ProposalSelectionExport, Subject: moduleID + "/" + nextRelease + "/" + contextID, SourceGeneration: 3,
		ExpectedBase: base, ExpectedPointer: expectedPointer,
		Producer: ProposalProducer{ID: expectedProposalProducer(ProposalSelectionExport), Version: "1.0.0", ContractVersion: 1},
		Paths:    []string{receiptPath, moduleIndexPath, trackIndexPath}, Changes: changes,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	return proposalRoot, baseRoot, outputRoot, proposal
}

func writeProposalRecord(t *testing.T, root, recordPath string, value any) {
	t.Helper()
	writeProposalPayload(t, root, recordPath, mustMarshalProposal(t, value))
}

func mustMarshalProposal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func proposalTestIndexID(t *testing.T, recordKind string, record any) string {
	t.Helper()
	id, err := proposalIndexID(recordKind, record)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func stringPointer(value string) *string {
	return &value
}

func TestApplyProposalRejectsStaleBaseWithoutChangingOutput(t *testing.T) {
	base := strings.Repeat("a", 40)
	proposalRoot := t.TempDir()
	baseRoot := t.TempDir()
	outputRoot := filepath.Join(t.TempDir(), "candidate")
	outputFile := filepath.Join(outputRoot, "keep.json")
	original := []byte("preserve candidate")
	if err := os.MkdirAll(filepath.Dir(outputFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputFile, original, 0o600); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"record_type":"catalog"}`)
	writeProposalPayload(t, proposalRoot, "data/catalog.json", payload)
	proposal := proposalFixture(ProposalCatalog, "catalog", base, "data/catalog.json", payload)
	writeProposalEnvelope(t, proposalRoot, proposal)
	if err := ApplyProposal(proposalRoot, strings.Repeat("b", 40), baseRoot, outputRoot); err == nil {
		t.Fatal("proposal built against a stale base was accepted")
	}
	unchanged, err := os.ReadFile(outputFile)
	if err != nil || !bytes.Equal(unchanged, original) {
		t.Fatalf("stale proposal changed output: read err = %v", err)
	}
}

func TestValidateProposalRejectsCrossKindPaths(t *testing.T) {
	data := []byte(`{"record_type":"presentation_snapshot"}`)
	proposal := proposalFixture(ProposalModuleEvidence, "avm-res-proposal-demo@"+strings.Repeat("a", 40), strings.Repeat("a", 40), "data/presentation/presentation-"+strings.Repeat("b", 64)+".json", data)
	if err := validateProposal(proposal); err == nil {
		t.Fatal("module evidence proposal was allowed to write a presentation path")
	}
}

func TestIdentifyProposalCanonicalizesPathAndChangeOrder(t *testing.T) {
	base := strings.Repeat("a", 40)
	first, err := IdentifyProposal(Proposal{
		Kind: ProposalPresentation, Subject: "presentation", SourceGeneration: 1, ExpectedBase: base,
		Producer: ProposalProducer{ID: expectedProposalProducer(ProposalPresentation), Version: "1.0.0", ContractVersion: 1},
		Paths:    []string{"data/presentation/b.json", "data/presentation/a.json"},
		Changes: []ProposalChange{
			{Path: "data/presentation/b.json", Operation: "add_immutable", Digest: strings.Repeat("b", 64)},
			{Path: "data/presentation/a.json", Operation: "add_immutable", Digest: strings.Repeat("a", 64)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := IdentifyProposal(Proposal{
		Kind: ProposalPresentation, Subject: "presentation", SourceGeneration: 1, ExpectedBase: base,
		Producer: ProposalProducer{ID: expectedProposalProducer(ProposalPresentation), Version: "1.0.0", ContractVersion: 1},
		Paths:    []string{"data/presentation/a.json", "data/presentation/b.json"},
		Changes: []ProposalChange{
			{Path: "data/presentation/a.json", Operation: "add_immutable", Digest: strings.Repeat("a", 64)},
			{Path: "data/presentation/b.json", Operation: "add_immutable", Digest: strings.Repeat("b", 64)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ProposalID != second.ProposalID || first.ContentDigest != second.ContentDigest {
		t.Fatalf("equivalent proposal sets have different identities: %#v / %#v", first, second)
	}
}

func TestValidateProposalPatchRequiresExactChangedPathSet(t *testing.T) {
	base := strings.Repeat("a", 40)
	proposalRoot := t.TempDir()
	presentationID := "presentation-" + strings.Repeat("c", 64)
	recordPath, err := identity.PresentationPath(presentationID)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"record_type":"presentation_snapshot","presentation_id":"` + presentationID + `"}`)
	proposal := proposalFixture(ProposalPresentation, "presentation", base, recordPath, payload)
	proposal.Changes[0].RecordID = presentationID
	proposal, err = IdentifyProposal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	writeProposalEnvelope(t, proposalRoot, proposal)
	if err = ValidateProposalPatch(proposalRoot, []string{"proposal.json", recordPath}); err != nil {
		t.Fatalf("exact proposal patch rejected: %v", err)
	}
	if err = ValidateProposalPatch(proposalRoot, []string{"proposal.json", recordPath, "README.md"}); err == nil {
		t.Fatal("proposal patch with an unrelated code/documentation file was accepted")
	}
	if err = ValidateProposalPatch(proposalRoot, []string{"proposal.json"}); err == nil {
		t.Fatal("proposal patch missing its declared payload path was accepted")
	}
}

func proposalFixture(kind ProposalKind, subject, base, recordPath string, data []byte) Proposal {
	proposal, err := IdentifyProposal(Proposal{
		Kind: kind, Subject: subject, SourceGeneration: 1, ExpectedBase: base,
		Producer:        ProposalProducer{ID: expectedProposalProducer(kind), Version: "1.0.0", ContractVersion: 1},
		ExpectedPointer: nil, Paths: []string{recordPath},
		Changes: []ProposalChange{{Path: recordPath, Operation: "add_immutable", Digest: digestBytes(data)}},
	})
	if err != nil {
		panic(err)
	}
	return proposal
}

func writeProposalPayload(t *testing.T, root, recordPath string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(recordPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeAcceptedDataPayload(t *testing.T, root, canonicalPath string, data []byte) {
	t.Helper()
	relativePath, err := dataRelativePath(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	writeProposalPayload(t, root, relativePath, data)
}

func writeAcceptedDataRecord(t *testing.T, root, canonicalPath string, value any) {
	t.Helper()
	writeAcceptedDataPayload(t, root, canonicalPath, mustMarshalProposal(t, value))
}

func writeProposalEnvelope(t *testing.T, root string, proposal Proposal) {
	t.Helper()
	data, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "proposal.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
