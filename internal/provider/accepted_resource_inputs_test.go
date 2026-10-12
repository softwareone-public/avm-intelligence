package provider

import (
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
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

func TestLoadAcceptedResourceIntelligenceInputsVerifiesExactRecordsAndStubs(t *testing.T) {
	root, facts, snapshot, policy := acceptedResourceIntelligenceTree(t)
	reference := AcceptedResourceSnapshotReference{
		ProviderAddress: snapshot.ProviderAddress, ProviderVersion: snapshot.ProviderVersion, SnapshotID: snapshot.SnapshotID,
	}
	loaded, err := LoadAcceptedResourceIntelligenceInputs(root, facts.ModuleID, facts.SourceSHA, facts.FactsID, policy, []AcceptedResourceSnapshotReference{reference})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.FactsScope) != 1 || loaded.FactsScope[0].FactsID != facts.FactsID || len(loaded.Snapshots) != 1 {
		t.Fatalf("loaded accepted inputs = facts scope %d, snapshots %d", len(loaded.FactsScope), len(loaded.Snapshots))
	}
	if len(loaded.FactReferences) != 1 || loaded.FactReferences[0].SourceSHA != facts.SourceSHA || loaded.FactReferences[0].FactsDigest != facts.ContentDigest {
		t.Fatalf("loaded exact facts proposal reference = %+v", loaded.FactReferences)
	}
	if _, err = LoadAcceptedResourceIntelligenceInputsForScope(root, []AcceptedFactsReference{
		{ModuleID: facts.ModuleID, SourceSHA: facts.SourceSHA, FactsID: facts.FactsID},
		{ModuleID: facts.ModuleID, SourceSHA: facts.SourceSHA, FactsID: facts.FactsID},
	}, policy, []AcceptedResourceSnapshotReference{reference}); err == nil {
		t.Fatal("resource intelligence loader accepted a repeated module in its facts scope")
	}
	acceptedSnapshot := loaded.Snapshots[0]
	if acceptedSnapshot.SnapshotID != snapshot.SnapshotID || acceptedSnapshot.StubDigest == "" ||
		len(acceptedSnapshot.Resources) != 1 || acceptedSnapshot.Resources[0].Name != "azurerm_resource_group" {
		t.Fatalf("loaded accepted provider stubs = %#v", acceptedSnapshot)
	}

	wrong := reference
	wrong.SnapshotID = "snapshot-" + strings.Repeat("f", 64)
	if _, err = LoadAcceptedResourceIntelligenceInputs(root, facts.ModuleID, facts.SourceSHA, facts.FactsID, policy, []AcceptedResourceSnapshotReference{wrong}); err == nil {
		t.Fatal("loader accepted a provider snapshot that is not in the accepted index")
	}
	if _, err = LoadAcceptedResourceIntelligenceInputs(root, facts.ModuleID, facts.SourceSHA, "facts-"+strings.Repeat("f", 64), policy, []AcceptedResourceSnapshotReference{reference}); err == nil {
		t.Fatal("loader accepted a facts identity that is not in the accepted tree")
	}
	loaded.FactsScope[0].SourceSHA = strings.Repeat("c", 40)
	collected := loaded.Snapshots[0]
	collected.Resources = []ResourceIntelligenceResource{{
		Kind: "resource", Name: "azurerm_resource_group", Sources: []ResourceIntelligenceSource{{
			Kind: "github_release", URL: "https://github.com/hashicorp/terraform-provider-azurerm/releases/tag/v4.2.0",
			Version: "4.2.0", VersionBasis: "release_tag", ContentDigest: digestText("release"), Body: "release",
			ObservedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
		}},
	}}
	if _, err = NewResourceIntelligenceProposal(policy, loaded, []ResourceIntelligenceSnapshot{collected}, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("proposal accepted input facts changed after trusted loading")
	}
}

func TestLoadAcceptedProviderSnapshotRequiresExactIndexedIdentity(t *testing.T) {
	root, _, snapshot, _ := acceptedResourceIntelligenceTree(t)
	loaded, err := LoadAcceptedProviderSnapshot(root, snapshot.ProviderAddress, snapshot.ProviderVersion, snapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ProviderAddress != snapshot.ProviderAddress || loaded.ProviderVersion != snapshot.ProviderVersion || loaded.SnapshotID != snapshot.SnapshotID {
		t.Fatalf("loaded provider snapshot = %+v, want exact accepted identity", loaded)
	}
	if _, err = LoadAcceptedProviderSnapshot(root, snapshot.ProviderAddress, snapshot.ProviderVersion, "snapshot-"+strings.Repeat("f", 64)); err == nil {
		t.Fatal("provider snapshot loader accepted an ID absent from the validated provider index")
	}
}

func TestLoadAcceptedResourceIntelligenceInputsRejectsChangedAcceptedSnapshot(t *testing.T) {
	root, facts, snapshot, policy := acceptedResourceIntelligenceTree(t)
	manifestPath, err := identity.ProviderSnapshotManifestPath(snapshot.ProviderAddress, snapshot.ProviderVersion, snapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	manifestFile := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(manifestPath, "data/")))
	data, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}
	var changed assessment.ProviderSnapshot
	if err = json.Unmarshal(data, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Entries[0].DocumentationRef = "https://example.com/changed"
	data, err = json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifestFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reference := AcceptedResourceSnapshotReference{
		ProviderAddress: snapshot.ProviderAddress, ProviderVersion: snapshot.ProviderVersion, SnapshotID: snapshot.SnapshotID,
	}
	if _, err = LoadAcceptedResourceIntelligenceInputs(root, facts.ModuleID, facts.SourceSHA, facts.FactsID, policy, []AcceptedResourceSnapshotReference{reference}); err == nil {
		t.Fatal("loader accepted a provider stub changed after its indexed digest was created")
	}
}

func TestLoadAcceptedModuleFactsUsesExactAcceptedDataTree(t *testing.T) {
	root, facts, _, _ := acceptedResourceIntelligenceTree(t)
	loaded, err := LoadAcceptedModuleFacts(root, facts.ModuleID, facts.SourceSHA, facts.FactsID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.FactsID != facts.FactsID || loaded.ModuleID != facts.ModuleID || loaded.SourceSHA != facts.SourceSHA {
		t.Fatalf("loaded facts identity = %s/%s/%s, want %s/%s/%s", loaded.ModuleID, loaded.SourceSHA, loaded.FactsID, facts.ModuleID, facts.SourceSHA, facts.FactsID)
	}
	if _, err = LoadAcceptedModuleFacts(root, facts.ModuleID, facts.SourceSHA, "facts-"+strings.Repeat("f", 64)); err == nil {
		t.Fatal("missing accepted facts record was accepted")
	}
}

func TestLoadAcceptedProviderTypeScopeUnionsExactFactsAndRejectsDuplicates(t *testing.T) {
	root, facts, _, policy := acceptedResourceIntelligenceTree(t)
	reference := AcceptedFactsReference{ModuleID: facts.ModuleID, SourceSHA: facts.SourceSHA, FactsID: facts.FactsID}
	scope, err := LoadAcceptedProviderTypeScope(root, []AcceptedFactsReference{reference}, policy, policy.Providers[0].Address)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope) != 1 || scope[0] != (ProviderTypeReference{Kind: "resource", Name: "azurerm_resource_group"}) {
		t.Fatalf("accepted provider type scope = %#v", scope)
	}
	if _, err = LoadAcceptedProviderTypeScope(root, []AcceptedFactsReference{reference, reference}, policy, policy.Providers[0].Address); err == nil {
		t.Fatal("provider scope accepted a duplicate exact facts reference")
	}
	if _, err = LoadAcceptedProviderTypeScope(root, []AcceptedFactsReference{reference}, policy, "registry.terraform.io/hashicorp/random"); err == nil {
		t.Fatal("provider scope accepted an address outside reviewed policy")
	}
}

func TestProviderTypesFromFactsIncludesEveryProviderSpecificSymbolKind(t *testing.T) {
	const address = "registry.terraform.io/hashicorp/azurerm"
	facts := terraform.Facts{Units: []terraform.Unit{{Symbols: []terraform.FactSymbol{
		{Kind: "resource", Name: "azurerm_resource_group.main", ProviderAddress: address},
		{Kind: "data", Name: "azurerm_client_config.current", ProviderAddress: address},
		{Kind: "ephemeral", Name: "azurerm_secret.value", ProviderAddress: address},
		{Kind: "action", Name: "azurerm_run_command.execute", ProviderAddress: address},
		{Kind: "function", Name: "command_id", ProviderAddress: address},
		{Kind: "function", Name: "upper", ProviderAddress: ""},
		{Kind: "variable", Name: "name", ProviderAddress: address},
	}}}}
	want := []ProviderTypeReference{
		{Kind: "action", Name: "azurerm_run_command"},
		{Kind: "datasource", Name: "azurerm_client_config"},
		{Kind: "ephemeral", Name: "azurerm_secret"},
		{Kind: "function", Name: "command_id"},
		{Kind: "resource", Name: "azurerm_resource_group"},
	}
	if got := providerTypesFromFacts(facts, address); !equalProviderTypeReferences(got, want) {
		t.Fatalf("provider type scope = %#v, want %#v", got, want)
	}
}

func equalProviderTypeReferences(left, right []ProviderTypeReference) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func acceptedResourceIntelligenceTree(t *testing.T) (string, terraform.Facts, assessment.ProviderSnapshot, CollectionPolicy) {
	return acceptedResourceIntelligenceTreeForModule(t, "avm-res-example", strings.Repeat("a", 40))
}

func acceptedResourceIntelligenceTreeForModule(t *testing.T, moduleID, sourceSHA string) (string, terraform.Facts, assessment.ProviderSnapshot, CollectionPolicy) {
	t.Helper()
	facts, _, policy := resourceIntelligenceFixtureForModule(t, moduleID, sourceSHA)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	address := policy.Providers[0].Address
	snapshot := assessment.ProviderSnapshot{
		SchemaVersion: 2, RecordType: "provider_snapshot", ProviderAddress: address, ProviderVersion: "4.2.0",
		Coverage:     assessment.SnapshotCoverage{Capabilities: map[string]string{"resource": "complete"}, Complete: true, ObservedAt: now.Format(time.RFC3339)},
		Entries:      []assessment.ProviderEntry{{Kind: "resource", Name: "azurerm_resource_group", DocumentationRef: "https://registry.terraform.io/providers/hashicorp/azurerm/4.2.0/docs/resources/resource_group", ContentDigest: strings.Repeat("a", 64)}},
		Producer:     assessment.SnapshotProducer{ID: "fixture-collector", Version: "1.0.0", ContractVersion: 1},
		SourceDigest: strings.Repeat("b", 64), ObservedAt: now.Format(time.RFC3339),
	}
	var err error
	snapshot.SnapshotID, err = identity.ID("snapshot", snapshot.SchemaVersion, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	factsPath, err := identity.ModuleRecordPath(facts.ModuleID, facts.SourceSHA, "facts", facts.FactsID)
	if err != nil {
		t.Fatal(err)
	}
	factsBytes, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	snapshotPath, err := identity.ProviderSnapshotManifestPath(address, snapshot.ProviderVersion, snapshot.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	repository := "Azure/terraform-azurerm-" + moduleID
	metadataPath, err := identity.ModuleMetadataPath(facts.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	metadataBytes, err := json.Marshal(struct {
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
	}{2, "module_metadata", facts.ModuleID, "res", "Available", repository, "active", "Example", "Accepted input fixture", 1})
	if err != nil {
		t.Fatal(err)
	}
	sourcePath, err := identity.ModuleSourcePath(facts.ModuleID, facts.SourceSHA)
	if err != nil {
		t.Fatal(err)
	}
	sourceBytes, err := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		RecordType    string `json:"record_type"`
		ModuleID      string `json:"module_id"`
		Repository    string `json:"repository"`
		SourceSHA     string `json:"source_sha"`
		Immutable     bool   `json:"immutable"`
	}{1, "source_revision", facts.ModuleID, repository, facts.SourceSHA, true})
	if err != nil {
		t.Fatal(err)
	}
	factRecord, err := index.FactRecordFromTypedFacts(facts)
	if err != nil {
		t.Fatal(err)
	}
	records := index.Records{
		CatalogGeneration: 1, CreatedAt: now,
		Generator: index.Producer{ID: "fixture-indexer", Version: "1.0.0", ContractVersion: 1},
		RecordDigests: map[string]string{
			metadataPath: acceptedRecordDigest(metadataBytes), sourcePath: acceptedRecordDigest(sourceBytes),
			factsPath: acceptedRecordDigest(factsBytes), snapshotPath: acceptedRecordDigest(snapshotBytes),
		},
		Modules:           []index.ModuleRecord{{ModuleID: facts.ModuleID, Repository: repository, Lifecycle: "active"}},
		SourceRevisions:   []index.SourceRevisionRecord{{ModuleID: facts.ModuleID, Repository: repository, SourceSHA: facts.SourceSHA, Immutable: true}},
		Facts:             []index.FactRecord{factRecord},
		ProviderSnapshots: []index.ProviderSnapshotRecord{{ID: snapshot.SnapshotID, Address: address, Version: snapshot.ProviderVersion}},
	}
	indexes, err := index.Build(records)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := indexes.Files()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeAcceptedRecord(t, root, metadataPath, metadataBytes)
	writeAcceptedRecord(t, root, sourcePath, sourceBytes)
	writeAcceptedRecord(t, root, factsPath, factsBytes)
	writeAcceptedRecord(t, root, snapshotPath, snapshotBytes)
	for logicalPath, contents := range generated {
		writeAcceptedRecord(t, root, logicalPath, contents)
	}
	return root, facts, snapshot, policy
}

func TestProviderTypeIsExplicitlyAbsentRequiresExactScopedCoverage(t *testing.T) {
	scoped := []assessment.ProviderTypeCoverage{
		{Kind: "resource", Name: "azurerm_ai_services", Status: assessment.ProviderTypeAbsent},
		{Kind: "resource", Name: "azurerm_resource_group", Status: assessment.ProviderTypePresent},
		{Kind: "datasource", Name: "azurerm_client_config", Status: assessment.ProviderTypeUnavailable},
	}
	tests := []struct {
		kind string
		name string
		want bool
	}{
		{kind: "resource", name: "azurerm_ai_services", want: true},
		{kind: "resource", name: "azurerm_resource_group", want: false},
		{kind: "datasource", name: "azurerm_client_config", want: false},
		{kind: "resource", name: "azurerm_missing", want: false},
	}
	for _, test := range tests {
		t.Run(test.kind+"/"+test.name, func(t *testing.T) {
			if got := providerTypeIsExplicitlyAbsent(scoped, test.kind, test.name); got != test.want {
				t.Fatalf("providerTypeIsExplicitlyAbsent() = %t, want %t", got, test.want)
			}
		})
	}
}

func writeAcceptedRecord(t *testing.T, root, logicalPath string, contents []byte) {
	t.Helper()
	filePath := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(logicalPath, "data/")))
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func acceptedRecordDigest(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}
