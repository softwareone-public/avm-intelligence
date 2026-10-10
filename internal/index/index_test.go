package index

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

func TestBuildSelectsCurrentAnalysisFromReceiptSequence(t *testing.T) {
	records, fixture := selectionIndexFixture(t)
	indexes, err := Build(records)
	if err != nil {
		t.Fatal(err)
	}
	if len(indexes.Modules) != 1 || len(indexes.Modules[0].CurrentSelections) != 1 {
		t.Fatalf("module current selections = %#v, want one context", indexes.Modules)
	}
	selected := indexes.Modules[0].CurrentSelections[0]
	if selected.AnalysisID == nil || *selected.AnalysisID != fixture.selectedAnalysisID || selected.SelectionID != fixture.selectedSelectionID || selected.Sequence != 2 {
		t.Fatalf("selected pointer = %#v, want sequence-2 receipt regardless of analysis ID order", selected)
	}
	moduleIndex := indexes.Modules[0]
	if len(moduleIndex.SourceSHAs) != 1 || moduleIndex.SourceSHAs[0] != fixture.sourceSHA || len(moduleIndex.FactsIDs) != 1 || moduleIndex.FactsIDs[0] != fixture.factsID {
		t.Fatalf("module revision/facts history = %#v", moduleIndex)
	}
	if len(moduleIndex.DefinitionIDs) != 1 || moduleIndex.DefinitionIDs[0] != fixture.definitionID || len(moduleIndex.DimensionResultIDs) != 1 || moduleIndex.DimensionResultIDs[0] != fixture.dimensionResultID || len(moduleIndex.AnalysisIDs) != 2 {
		t.Fatalf("module assessment history = %#v", moduleIndex)
	}
	if len(moduleIndex.SelectionIDs) != 2 || moduleIndex.SelectionIDs[0] != "selection-"+strings.Repeat("f", 64) || moduleIndex.SelectionIDs[1] != fixture.selectedSelectionID {
		t.Fatalf("module selection IDs are not in receipt sequence order: %#v", moduleIndex.SelectionIDs)
	}
	if len(indexes.Tracks) != 1 || len(indexes.Tracks[0].Selections) != 2 || indexes.Tracks[0].Selections[1].Sequence != 2 {
		t.Fatalf("track selection history = %#v", indexes.Tracks)
	}
}

func TestBuildRejectsGappedSelectionHistory(t *testing.T) {
	records, _ := selectionIndexFixture(t)
	records.Selections[1].Sequence = 3
	if _, err := Build(records); err == nil {
		t.Fatal("selection sequence gap was accepted")
	}
}

func TestTypedFactsProduceProviderTypeDependenciesByFullIdentity(t *testing.T) {
	root := t.TempDir()
	source := `resource "azurerm_shared" "main" {}
resource "azapi_shared" "main" {}
data "azurerm_shared" "lookup" {}
ephemeral "azurerm_shared" "secret" {}
action "azapi_shared" "execute" {}
output "provider_function" {
  value = provider::hashicorp::azurerm::shared_id()
}`
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: root, ModuleID: "avm-res-index-demo", SourceSHA: strings.Repeat("9", 40),
		ObservedAt: time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC),
		Units:      []terraform.UnitInput{{Path: ".", Role: terraform.RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	encodedFacts, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	records := Records{}
	if err = addTypedRecord(&records, encodedFacts); err != nil {
		t.Fatal(err)
	}
	if len(records.Facts) != 1 || len(records.Facts[0].ProviderTypeUsages) != 6 || len(records.Facts[0].ProviderUsageCoverage) != 1 {
		t.Fatalf("derived provider dependencies = %#v / %#v", records.Facts, records.Facts[0].ProviderUsageCoverage)
	}
	usageSet := make(map[ProviderTypeUsage]struct{}, len(records.Facts[0].ProviderTypeUsages))
	for _, usage := range records.Facts[0].ProviderTypeUsages {
		usageSet[usage] = struct{}{}
	}
	want := []ProviderTypeUsage{
		{ModuleID: facts.ModuleID, FactsID: facts.FactsID, UnitPath: ".", ProviderAddress: "registry.terraform.io/hashicorp/azurerm", Kind: "resource", Name: "azurerm_shared"},
		{ModuleID: facts.ModuleID, FactsID: facts.FactsID, UnitPath: ".", ProviderAddress: "registry.terraform.io/hashicorp/azapi", Kind: "resource", Name: "azapi_shared"},
		{ModuleID: facts.ModuleID, FactsID: facts.FactsID, UnitPath: ".", ProviderAddress: "registry.terraform.io/hashicorp/azurerm", Kind: "datasource", Name: "azurerm_shared"},
		{ModuleID: facts.ModuleID, FactsID: facts.FactsID, UnitPath: ".", ProviderAddress: "registry.terraform.io/hashicorp/azurerm", Kind: "ephemeral", Name: "azurerm_shared"},
		{ModuleID: facts.ModuleID, FactsID: facts.FactsID, UnitPath: ".", ProviderAddress: "registry.terraform.io/hashicorp/azapi", Kind: "action", Name: "azapi_shared"},
		{ModuleID: facts.ModuleID, FactsID: facts.FactsID, UnitPath: ".", ProviderAddress: "registry.terraform.io/hashicorp/azurerm", Kind: "function", Name: "shared_id"},
	}
	for _, dependency := range want {
		if _, found := usageSet[dependency]; !found {
			t.Errorf("provider type dependency missing: %#v", dependency)
		}
	}
	coverage := records.Facts[0].ProviderUsageCoverage[0]
	if coverage.Capabilities[terraform.CapabilityAdditionalKinds] != terraform.CoverageComplete || len(coverage.ProviderAddresses) != 2 {
		t.Fatalf("provider usage fallback coverage = %#v", coverage)
	}
	dependencyIndex := buildDimensionDependencies(
		map[string]assessment.DimensionDefinition{}, map[string]assessment.DimensionResult{},
		map[string]FactRecord{facts.FactsID: records.Facts[0]},
	)
	if len(dependencyIndex.ProviderTypeUsages) != len(want) || len(dependencyIndex.ProviderUsageCoverage) != 1 {
		t.Fatalf("generated dependency index omitted provider usage: %#v", dependencyIndex)
	}
	indexID, err := indexContentID("dimension-dependency-index", dependencyIndex)
	if err != nil {
		t.Fatal(err)
	}
	dependencyIndex.IndexID = indexID
	assertIndexIdentity(t, "dimension-dependency-index", dependencyIndex.IndexID, dependencyIndex)
}

func TestTypedFactsRetainPartialProviderUsageCoverage(t *testing.T) {
	root := t.TempDir()
	source := `{"resource":{"azurerm_shared":{"main":{}}},"ephemeral":{"azurerm_shared":{"secret":{}}}}`
	if err := os.WriteFile(filepath.Join(root, "main.tf.json"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: root, ModuleID: "avm-res-index-partial", SourceSHA: strings.Repeat("8", 40),
		ObservedAt: time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC),
		Units:      []terraform.UnitInput{{Path: ".", Role: terraform.RoleComponent, Files: []string{"main.tf.json"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if facts.Coverage.Capabilities[terraform.CapabilityAdditionalKinds] != terraform.CoveragePartial {
		t.Fatalf("fixture additional-kind coverage = %#v, want partial", facts.Coverage.Capabilities)
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	records := Records{}
	if err = addTypedRecord(&records, encoded); err != nil {
		t.Fatal(err)
	}
	if len(records.Facts) != 1 || len(records.Facts[0].ProviderUsageCoverage) != 1 ||
		records.Facts[0].ProviderUsageCoverage[0].Capabilities[terraform.CapabilityAdditionalKinds] != terraform.CoveragePartial {
		t.Fatalf("accepted index input lost partial provider usage coverage: %#v", records.Facts)
	}
}

func TestBuildRejectsDanglingDimensionResult(t *testing.T) {
	records, _ := selectionIndexFixture(t)
	records.Analyses[0].DimensionResultIDs = []string{"dimension-result-" + strings.Repeat("9", 64)}
	if _, err := Build(records); err == nil {
		t.Fatal("analysis with a dangling dimension result was accepted")
	}
}

func TestBuildPreservesExplicitVersionPointerAndHistoryOrder(t *testing.T) {
	records, _ := selectionIndexFixture(t)
	moduleID := records.Modules[0].ModuleID
	repository := records.Modules[0].Repository
	sourceSHA := records.SourceRevisions[0].SourceSHA
	olderID := "version-observation-" + strings.Repeat("1", 64)
	newerID := "version-observation-" + strings.Repeat("f", 64)
	observedAt := records.CreatedAt
	records.VersionObservations = []VersionObservationRecord{
		{ID: olderID, ModuleID: moduleID, Generation: 1, Repository: repository, Version: "4.0.0", Tags: []string{"v4.0.0"}, SourceSHA: sourceSHA, ObservedAt: observedAt},
		{ID: newerID, ModuleID: moduleID, Generation: 2, Repository: repository, Version: "5.0.0", Tags: []string{"v5.0.0"}, SourceSHA: sourceSHA, ObservedAt: observedAt.Add(time.Minute)},
	}
	records.VersionIndexes = []ModuleVersionsIndexRecord{{
		ModuleID: moduleID, Generation: 2, ObservationIDs: []string{olderID, newerID},
		OverallLatest: &VersionPointer{Version: "4.0.0", Tag: "v4.0.0", SourceSHA: sourceSHA, ObservationID: olderID},
	}}
	addFixtureRecordDigests(t, &records)
	indexes, err := Build(records)
	if err != nil {
		t.Fatal(err)
	}
	module := indexes.Modules[0]
	if module.VersionIndex == nil || module.VersionIndex.OverallLatest == nil || module.VersionIndex.OverallLatest.ObservationID != olderID {
		t.Fatalf("module index replaced explicit overall-latest pointer: %#v", module.VersionIndex)
	}
	if len(module.VersionObservationIDs) != 2 || module.VersionObservationIDs[0] != olderID || module.VersionObservationIDs[1] != newerID {
		t.Fatalf("module version history was reordered: %#v", module.VersionObservationIDs)
	}
}

func TestBuildRejectsMissingAcceptedRecordDigest(t *testing.T) {
	records, _ := selectionIndexFixture(t)
	delete(records.RecordDigests, "data/modules/res/avm-res-network-vnet/revisions/"+records.SourceRevisions[0].SourceSHA+"/facts/"+records.Facts[0].ID+".json")
	if _, err := Build(records); err == nil {
		t.Fatal("accepted facts without a source digest were included in the dataset")
	}
}

func TestBuildRejectsPresentationNewerThanDataset(t *testing.T) {
	records, fixture := selectionIndexFixture(t)
	records.CreatedAt = records.Selections[1].SelectedAt.Add(time.Minute)
	presentationID := "presentation-" + strings.Repeat("7", 64)
	records.Presentations = []PresentationSnapshotRecord{{
		ID: presentationID, Generation: 1, AsOf: records.CreatedAt.Add(time.Second),
		SelectionReceiptIDs: []string{fixture.selectedSelectionID},
	}}
	addFixtureRecordDigests(t, &records)
	if _, err := Build(records); err == nil {
		t.Fatal("presentation with a selection receipt newer than its as-of time was accepted")
	}
}

func TestBuildRejectsSelectionNewerThanDataset(t *testing.T) {
	records, _ := selectionIndexFixture(t)
	records.CreatedAt = records.Selections[1].SelectedAt.Add(-time.Second)
	if _, err := Build(records); err == nil {
		t.Fatal("dataset with a future selection receipt was accepted")
	}
}

func TestBuildIndexesUseFullProviderIdentityAndExactTargets(t *testing.T) {
	records, _ := selectionIndexFixture(t)
	address := "registry.terraform.io/hashicorp/azurerm"
	snapshotID := "snapshot-" + strings.Repeat("4", 64)
	target := ProviderTarget{Address: address, Version: "4.0.0", SnapshotID: snapshotID}
	records.ProviderSnapshots = []ProviderSnapshotRecord{{ID: snapshotID, Address: address, Version: target.Version}}
	records.DimensionResults[0].SnapshotIDs = []string{snapshotID}
	addFixtureRecordDigests(t, &records)
	for index := range records.Analyses {
		records.Analyses[index].ProviderTargets = []ProviderTarget{target}
	}
	indexes, err := Build(records)
	if err != nil {
		t.Fatal(err)
	}
	assertProviderProjection(t, indexes, address, target, snapshotID)

	records.Analyses[0].ProviderTargets[0].Address = "registry.terraform.io/other/azurerm"
	if _, err = Build(records); err == nil {
		t.Fatal("provider target with a mismatched full address was accepted")
	}
}

func assertProviderProjection(t *testing.T, indexes Indexes, address string, target ProviderTarget, snapshotID string) {
	t.Helper()
	if len(indexes.Providers) != 1 || indexes.Providers[0].ProviderAddress != address || len(indexes.Providers[0].Snapshots) != 1 {
		t.Fatalf("provider index lost full provider identity: %#v", indexes.Providers)
	}
	assertIndexIdentity(t, "provider-index", indexes.Providers[0].IndexID, indexes.Providers[0])
	if len(indexes.Tracks) != 1 || len(indexes.Tracks[0].TargetVectors) != 2 || indexes.Tracks[0].TargetVectors[1].ProviderTargets[0] != target {
		t.Fatalf("track index did not preserve exact provider target vectors: %#v", indexes.Tracks)
	}
	if indexes.Tracks[0].TargetVectors[1].AnalysisID == nil || *indexes.Tracks[0].TargetVectors[1].AnalysisID != *indexes.Tracks[0].Selections[1].AnalysisID {
		t.Fatalf("track target vector analysis does not match its receipt: %#v", indexes.Tracks[0].TargetVectors[1])
	}
	files, err := indexes.Files()
	if err != nil {
		t.Fatal(err)
	}
	providerPath, err := identity.ProviderIndexPath(address)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := files[providerPath]; !exists {
		t.Fatalf("provider index file missing at full-address path %s", providerPath)
	}
	if len(indexes.DimensionDependencies.Definitions) != 1 || len(indexes.DimensionDependencies.Definitions[0].DimensionResultIDs) != 1 {
		t.Fatalf("dimension dependency index lost its immutable result reference: %#v", indexes.DimensionDependencies)
	}
	dependency := indexes.DimensionDependencies.Definitions[0]
	if len(dependency.FactsIDs) != 1 || dependency.FactsIDs[0] != "facts-"+strings.Repeat("b", 64) || len(dependency.ProviderSnapshotIDs) != 1 || dependency.ProviderSnapshotIDs[0] != snapshotID {
		t.Fatalf("dimension dependency index lost shared facts or provider snapshot identity: %#v", dependency)
	}
}

func TestBuildPreservesDeselectionAsNullAnalysis(t *testing.T) {
	records, _ := selectionIndexFixture(t)
	receiptID := "selection-" + strings.Repeat("0", 64)
	deselectedAt := records.Selections[1].SelectedAt.Add(time.Minute)
	records.Selections = append(records.Selections, SelectionReceipt{
		ID: receiptID, ModuleID: records.Modules[0].ModuleID, AVMReleaseVersion: "v1.0.0", ContextID: "azurerm-4",
		Sequence: 3, DesiredGeneration: 3,
		InputFingerprint: strings.Repeat("0", 64), EvidenceCommit: strings.Repeat("3", 40), SelectedAt: deselectedAt,
	})
	addFixtureRecordDigests(t, &records)
	indexes, err := Build(records)
	if err != nil {
		t.Fatal(err)
	}
	current := indexes.Modules[0].CurrentSelections[0]
	if current.AnalysisID != nil || current.SourceSHA != nil || current.FactsID != nil || current.Sequence != 3 || current.SelectionID != receiptID {
		t.Fatalf("deselection was not retained as current explicit selection: %#v", current)
	}
	vector := indexes.Tracks[0].TargetVectors[2]
	if vector.AnalysisID != nil || len(vector.ProviderTargets) != 0 || vector.SelectionID != receiptID {
		t.Fatalf("deselection target vector is not explicit and empty: %#v", vector)
	}
}

func TestBuildDatasetMembershipKeepsAVMReleasesSeparate(t *testing.T) {
	records, _ := selectionIndexFixture(t)
	secondRelease := records.Selections[1]
	secondRelease.ID = "selection-" + strings.Repeat("e", 64)
	secondRelease.AVMReleaseVersion = "v2.0.0"
	secondRelease.Sequence = 1
	secondRelease.DesiredGeneration = 1
	secondRelease.SelectedAt = records.Selections[1].SelectedAt.Add(time.Minute)
	records.Selections = append(records.Selections, secondRelease)
	addFixtureRecordDigests(t, &records)

	indexes, err := Build(records)
	if err != nil {
		t.Fatal(err)
	}
	module := indexes.Modules[0]
	if len(module.CurrentSelections) != 2 || module.CurrentSelections[0].AVMReleaseVersion != "v1.0.0" || module.CurrentSelections[1].AVMReleaseVersion != "v2.0.0" {
		t.Fatalf("module current selections collapsed AVM releases: %#v", module.CurrentSelections)
	}
	if len(indexes.Dataset.ModuleMembership) != 2 || indexes.Dataset.ModuleMembership[0].AVMReleaseVersion != "v1.0.0" || indexes.Dataset.ModuleMembership[1].AVMReleaseVersion != "v2.0.0" {
		t.Fatalf("dataset deployment membership collapsed AVM releases: %#v", indexes.Dataset.ModuleMembership)
	}
}

func TestBuildPresentationIndexSortsHistoryByGeneration(t *testing.T) {
	older := PresentationSnapshotRecord{ID: "presentation-" + strings.Repeat("a", 64), Generation: 1, AsOf: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	newer := PresentationSnapshotRecord{ID: "presentation-" + strings.Repeat("b", 64), Generation: 2, AsOf: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	presentationIndex := buildPresentationIndex([]PresentationSnapshotRecord{newer, older})
	if len(presentationIndex.Snapshots) != 2 || presentationIndex.Snapshots[0].Generation != 1 || presentationIndex.Snapshots[1].Generation != 2 {
		t.Fatalf("presentation history order = %#v", presentationIndex.Snapshots)
	}
	if presentationIndex.CurrentID == nil || *presentationIndex.CurrentID != newer.ID {
		t.Fatalf("presentation current pointer = %v, want %s", presentationIndex.CurrentID, newer.ID)
	}
}

func TestBuildDatasetManifestExcludesItsOwnDigest(t *testing.T) {
	records, fixture := selectionIndexFixture(t)
	records.CreatedAt = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	records.Generator = Producer{ID: "factory", Version: "2.0.0", ContractVersion: 1}
	records.RecordDigests["data/accepted/facts.json"] = strings.Repeat("7", 64)
	records.RecordDigests["data/dataset.json"] = strings.Repeat("9", 64)
	presentationID := "presentation-" + strings.Repeat("6", 64)
	records.Presentations = []PresentationSnapshotRecord{{
		ID: presentationID, Generation: 1, AsOf: records.CreatedAt,
		SelectionReceiptIDs: []string{fixture.selectedSelectionID},
	}}
	addFixtureRecordDigests(t, &records)
	indexes, err := Build(records)
	if err != nil {
		t.Fatal(err)
	}
	assertDatasetManifest(t, indexes, presentationID)
	assertEmittedIndexDigests(t, indexes)
}

func assertDatasetManifest(t *testing.T, indexes Indexes, presentationID string) {
	t.Helper()
	if indexes.Dataset.DatasetID == "" || indexes.Dataset.RecordDigests["data/accepted/facts.json"] != strings.Repeat("7", 64) {
		t.Fatalf("dataset manifest omitted accepted record digests: %#v", indexes.Dataset)
	}
	datasetPayload := struct {
		CatalogGeneration uint64             `json:"catalog_generation"`
		RecordDigests     map[string]string  `json:"record_digests"`
		ModuleMembership  []ModuleMembership `json:"module_membership"`
	}{indexes.Dataset.CatalogGeneration, indexes.Dataset.RecordDigests, indexes.Dataset.ModuleMembership}
	expectedDatasetID, err := identity.ID("dataset", 3, datasetPayload)
	if err != nil {
		t.Fatal(err)
	}
	if indexes.Dataset.DatasetID != expectedDatasetID {
		t.Fatalf("dataset ID = %s, want content identity %s", indexes.Dataset.DatasetID, expectedDatasetID)
	}
	if _, exists := indexes.Dataset.RecordDigests["data/dataset.json"]; exists {
		t.Fatal("dataset manifest includes its own digest")
	}
	if indexes.Presentation.CurrentID == nil || *indexes.Presentation.CurrentID != presentationID || len(indexes.Presentation.Snapshots) != 1 {
		t.Fatalf("presentation index did not use explicit generation: %#v", indexes.Presentation)
	}
}

func assertEmittedIndexDigests(t *testing.T, indexes Indexes) {
	t.Helper()
	files, err := indexes.Files()
	if err != nil {
		t.Fatal(err)
	}
	datasetPath, err := identity.CanonicalDataPath("dataset")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := files[datasetPath]; !exists {
		t.Fatal("generated files omit the dataset manifest")
	}
	for recordPath, data := range files {
		if recordPath == datasetPath {
			continue
		}
		digest := sha256.Sum256(data)
		if indexes.Dataset.RecordDigests[recordPath] != hex.EncodeToString(digest[:]) {
			t.Fatalf("dataset digest for %s does not match emitted bytes", recordPath)
		}
	}
}

func TestBuildIndexIdentitiesIgnoreInputOrderAndCreationTime(t *testing.T) {
	records, _ := selectionIndexFixture(t)
	first, err := Build(records)
	if err != nil {
		t.Fatal(err)
	}
	reverseSlice(records.Modules)
	reverseSlice(records.SourceRevisions)
	reverseSlice(records.Facts)
	reverseSlice(records.Definitions)
	reverseSlice(records.DimensionResults)
	reverseSlice(records.Analyses)
	reverseSlice(records.Selections)
	records.CreatedAt = records.CreatedAt.Add(time.Hour)
	second, err := Build(records)
	if err != nil {
		t.Fatal(err)
	}
	if first.Dataset.DatasetID != second.Dataset.DatasetID || first.Catalog.IndexID != second.Catalog.IndexID || first.Modules[0].IndexID != second.Modules[0].IndexID || first.Tracks[0].IndexID != second.Tracks[0].IndexID {
		t.Fatalf("input ordering or creation time changed semantic IDs: first=%#v second=%#v", first, second)
	}
	assertIndexIdentity(t, "catalog-index", first.Catalog.IndexID, first.Catalog)
	assertIndexIdentity(t, "module-index", first.Modules[0].IndexID, first.Modules[0])
	assertIndexIdentity(t, "track-index", first.Tracks[0].IndexID, first.Tracks[0])
	assertIndexIdentity(t, "dimension-dependency-index", first.DimensionDependencies.IndexID, first.DimensionDependencies)
	assertIndexIdentity(t, "presentation-index", first.Presentation.IndexID, first.Presentation)
	firstFiles, err := first.Files()
	if err != nil {
		t.Fatal(err)
	}
	secondFiles, err := second.Files()
	if err != nil {
		t.Fatal(err)
	}
	for recordPath, firstData := range firstFiles {
		if recordPath == "data/dataset.json" {
			continue
		}
		if !bytes.Equal(firstData, secondFiles[recordPath]) {
			t.Fatalf("non-dataset index %s changed with input order or creation time", recordPath)
		}
	}
}

func assertIndexIdentity(t *testing.T, kind, actualID string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, "index_id")
	expectedID, err := identity.ID(kind, 1, payload)
	if err != nil {
		t.Fatal(err)
	}
	if actualID != expectedID {
		t.Fatalf("%s = %s, want payload identity %s", kind, actualID, expectedID)
	}
}

type selectionFixture struct {
	factsID             string
	definitionID        string
	dimensionResultID   string
	sourceSHA           string
	selectedAnalysisID  string
	selectedSelectionID string
}

func selectionIndexFixture(t *testing.T) (Records, selectionFixture) {
	t.Helper()
	moduleID := "avm-res-network-vnet"
	sourceSHA := strings.Repeat("a", 40)
	factsID := "facts-" + strings.Repeat("b", 64)
	definitionID := "definition-" + strings.Repeat("c", 64)
	dimensionResultID := "dimension-result-" + strings.Repeat("d", 64)
	selectedAnalysisID := "analysis-" + strings.Repeat("1", 64)
	otherAnalysisID := "analysis-" + strings.Repeat("f", 64)
	selectedFingerprint := strings.Repeat("1", 64)
	otherFingerprint := strings.Repeat("f", 64)
	selectedSelectionID := "selection-" + strings.Repeat("2", 64)
	sourceSHAPointer := &sourceSHA
	factsIDPointer := &factsID
	selectedAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	records := Records{
		CatalogGeneration: 4,
		CreatedAt:         time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC),
		Generator:         Producer{ID: "factory", Version: "2.0.0", ContractVersion: 1},
		RecordDigests:     map[string]string{"data/accepted/baseline.json": strings.Repeat("8", 64)},
		Modules: []ModuleRecord{{
			ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-network-vnet", Lifecycle: "active",
		}},
		SourceRevisions: []SourceRevisionRecord{{
			ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-network-vnet", SourceSHA: sourceSHA, Immutable: true,
		}},
		Facts: []FactRecord{{ID: factsID, ModuleID: moduleID, SourceSHA: sourceSHA, ContentDigest: strings.Repeat("e", 64)}},
		Definitions: []assessment.DimensionDefinition{{
			SchemaVersion: 2, RecordType: "dimension_definition", DefinitionID: definitionID,
			DimensionID: "provider-requirements", Version: 1, UnitScope: "repository",
			CheckIDs: []string{"declared-provider-sources"}, RequiredCapabilities: []string{"terraform.provider_requirements"},
			PolicyID: "definition-" + strings.Repeat("5", 64), EvidenceSchemaVersion: 1,
		}},
		DimensionResults: []assessment.DimensionResult{{
			SchemaVersion: 2, RecordType: "dimension_result", DimensionResultID: dimensionResultID,
			ModuleID: moduleID, SourceSHA: sourceSHA, FactsID: factsID,
			DefinitionID: definitionID, UnitPath: ".", InputFingerprint: strings.Repeat("9", 64), Freshness: "current",
		}},
		Analyses: []AnalysisRecord{
			{ID: otherAnalysisID, ModuleID: moduleID, SourceSHA: sourceSHA, FactsID: factsID,
				ContextID: "azurerm-4", DimensionResultIDs: []string{dimensionResultID}, InputFingerprint: otherFingerprint},
			{ID: selectedAnalysisID, ModuleID: moduleID, SourceSHA: sourceSHA, FactsID: factsID,
				ContextID: "azurerm-4", DimensionResultIDs: []string{dimensionResultID}, InputFingerprint: selectedFingerprint},
		},
		Selections: []SelectionReceipt{
			{ID: "selection-" + strings.Repeat("f", 64), ModuleID: moduleID, AVMReleaseVersion: "v1.0.0", ContextID: "azurerm-4", Sequence: 1, DesiredGeneration: 1,
				SourceSHA: sourceSHAPointer, FactsID: factsIDPointer, InputFingerprint: otherFingerprint, AnalysisID: &otherAnalysisID,
				EvidenceCommit: strings.Repeat("1", 40), SelectedAt: selectedAt.Add(-time.Minute)},
			{ID: selectedSelectionID, ModuleID: moduleID, AVMReleaseVersion: "v1.0.0", ContextID: "azurerm-4", Sequence: 2, DesiredGeneration: 2,
				SourceSHA: sourceSHAPointer, FactsID: factsIDPointer, InputFingerprint: selectedFingerprint, AnalysisID: &selectedAnalysisID,
				EvidenceCommit: strings.Repeat("2", 40), SelectedAt: selectedAt},
		},
	}
	addFixtureRecordDigests(t, &records)
	return records, selectionFixture{
		factsID: factsID, definitionID: definitionID, dimensionResultID: dimensionResultID,
		sourceSHA: sourceSHA, selectedAnalysisID: selectedAnalysisID, selectedSelectionID: selectedSelectionID,
	}
}

func reverseSlice[T any](values []T) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func addFixtureRecordDigests(t *testing.T, records *Records) {
	t.Helper()
	add := func(recordPath string, err error) {
		if err != nil {
			t.Fatal(err)
		}
		records.RecordDigests[recordPath] = strings.Repeat("8", 64)
	}
	for _, module := range records.Modules {
		add(identity.ModuleMetadataPath(module.ModuleID))
	}
	for _, revision := range records.SourceRevisions {
		add(identity.ModuleSourcePath(revision.ModuleID, revision.SourceSHA))
	}
	for _, fact := range records.Facts {
		add(identity.ModuleRecordPath(fact.ModuleID, fact.SourceSHA, "facts", fact.ID))
	}
	for _, definition := range records.Definitions {
		add(identity.DefinitionPath(definition.DefinitionID))
	}
	for _, result := range records.DimensionResults {
		add(identity.ModuleRecordPath(result.ModuleID, result.SourceSHA, "dimension-result", result.DimensionResultID))
	}
	for _, analysis := range records.Analyses {
		add(identity.ModuleRecordPath(analysis.ModuleID, analysis.SourceSHA, "analysis", analysis.ID))
	}
	for _, selection := range records.Selections {
		add(identity.SelectionPath(selection.ModuleID, selection.ContextID, selection.ID))
	}
	for _, observation := range records.VersionObservations {
		add(identity.ModuleVersionObservationPath(observation.ModuleID, observation.ID))
	}
	for _, versionIndex := range records.VersionIndexes {
		add(identity.ModuleVersionsPath(versionIndex.ModuleID))
	}
	for _, snapshot := range records.ProviderSnapshots {
		add(identity.ProviderSnapshotManifestPath(snapshot.Address, snapshot.Version, snapshot.ID))
	}
	for _, presentation := range records.Presentations {
		add(identity.PresentationPath(presentation.ID))
	}
}
