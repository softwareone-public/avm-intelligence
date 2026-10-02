package main

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
	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

type legacyMigrationPlanTestRecord struct {
	LegacyFingerprint string `json:"legacy_fingerprint"`
	LegacyReport      struct {
		ArchiveID         string `json:"archive_id"`
		ArchivePath       string `json:"archive_path"`
		ArchiveRecordType string `json:"archive_record_type"`
		SHA256            string `json:"sha256"`
		Provenance        struct {
			CLICommit string `json:"cli_commit"`
		} `json:"provenance"`
	} `json:"legacy_report"`
	SourceRevision struct {
		Status              string `json:"status"`
		Repository          string `json:"repository"`
		RepositorySource    string `json:"repository_source"`
		DiscoveryGeneration uint64 `json:"discovery_generation"`
	} `json:"source_revision"`
	Facts          migrationAvailability   `json:"facts"`
	ReleaseVersion migrationAvailability   `json:"release_version"`
	Analysis       migrationAnalysisStatus `json:"analysis"`
}

type legacyMigrationPlanTestDocument struct {
	Mode            string                          `json:"mode"`
	WritesPerformed bool                            `json:"writes_performed"`
	Modules         []legacyMigrationPlanTestRecord `json:"modules"`
}

func TestLegacyMigrationPlanPreservesEvidenceWithoutWritingAcceptedData(t *testing.T) {
	root := t.TempDir()
	moduleID := "avm-res-keyvault-vault"
	modulesDir := filepath.Join(root, "modules")
	if err := os.MkdirAll(modulesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	report := []byte(`{"schema_version":2,"adapter_version":1,"provenance":{"module_id":"avm-res-keyvault-vault","source_sha":"02719320e9d412b8c95eae8240f3812c0e75ed2c","cli_version":"0.1.0","cli_commit":"c9d714fd6cb7da88ffcb4b049f39480263cc2875","adapter_version":1},"ruleset_name":"legacy-policy","ruleset_digest":"55b982d670cb1e5d1e4381b5d4276ea46e553065e75ab72fb49b342e4b3b7da1","source_digest":"a2727af07c41dcae6686b9287023d8b2b438594d91f20726d690162cfd45af6a","units":[{"path":".","role":"component","classification":{"kind":"unknown","method":"unavailable","reason":"legacy"},"checks":[],"symbol_count":1}]}`)
	reportPath := filepath.Join(modulesDir, moduleID+".json")
	if err := os.WriteFile(reportPath, report, 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := `{"schema_version":1,"catalog_generation":1,"built_at":"2026-10-02T00:00:00Z","modules":[{"module_id":"avm-res-keyvault-vault","source_sha":"02719320e9d412b8c95eae8240f3812c0e75ed2c","fingerprint":"legacy-fingerprint","report_path":"modules/avm-res-keyvault-vault.json"}]}`
	if err := os.WriteFile(filepath.Join(root, "candidate.json"), []byte(candidate), 0o600); err != nil {
		t.Fatal(err)
	}
	writeMigrationDiscovery(t, root, 1, time.Date(2026, 10, 1, 14, 26, 24, 0, time.UTC))
	planPath := filepath.Join(t.TempDir(), "migration-plan.json")
	if err := run([]string{"--plan-legacy-migration", "--data-root", root, "--migration-out", planPath}); err != nil {
		t.Fatal(err)
	}
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyMigrationPlan(t, planBytes, report, moduleID)
	unchanged, err := os.ReadFile(reportPath)
	if err != nil || !bytes.Equal(unchanged, report) {
		t.Fatalf("legacy report changed during dry run: read err = %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "modules", "res", moduleID, "revisions")); !os.IsNotExist(err) {
		t.Fatalf("dry run created or observed a revision directory: stat err = %v", err)
	}
}

func assertLegacyMigrationPlan(t *testing.T, planBytes, report []byte, moduleID string) {
	t.Helper()
	var plan legacyMigrationPlanTestDocument
	if err := json.Unmarshal(planBytes, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.WritesPerformed || plan.Mode != "dry_run" || len(plan.Modules) != 1 {
		t.Fatalf("migration plan header is invalid: %#v", plan)
	}
	entry := plan.Modules[0]
	digest := sha256.Sum256(report)
	digestHex := hex.EncodeToString(digest[:])
	if entry.LegacyReport.SHA256 != digestHex || entry.LegacyFingerprint != "legacy-fingerprint" {
		t.Fatalf("legacy report identity was not preserved: %#v", entry)
	}
	if entry.LegacyReport.Provenance.CLICommit != "c9d714fd6cb7da88ffcb4b049f39480263cc2875" {
		t.Fatalf("legacy producer provenance was not retained: %#v", entry.LegacyReport.Provenance)
	}
	if entry.SourceRevision.Status != "proposed" || entry.SourceRevision.Repository != "Azure/terraform-azurerm-avm-res-keyvault-vault" || entry.SourceRevision.RepositorySource != "data/discovery.json" || entry.SourceRevision.DiscoveryGeneration != 1 {
		t.Fatalf("source revision did not use accepted discovery identity: %#v", entry.SourceRevision)
	}
	if entry.Facts.Status != "unavailable" || entry.ReleaseVersion.Status != "unavailable" || entry.Analysis.Status != "not_created" {
		t.Fatalf("migration inferred unavailable native inputs: %#v", entry)
	}
	if entry.LegacyReport.ArchiveID != "legacy-analysis-"+digestHex || entry.LegacyReport.ArchiveRecordType != "legacy_iaclens_report" {
		t.Fatalf("legacy archive identity is not byte-addressed: %#v", entry.LegacyReport)
	}
	wantArchivePath := filepath.ToSlash(filepath.Join("data", "modules", "res", moduleID, "revisions", "02719320e9d412b8c95eae8240f3812c0e75ed2c", "analyses", "legacy-analysis-"+digestHex+".json"))
	if entry.LegacyReport.ArchivePath != wantArchivePath {
		t.Fatalf("legacy archive path = %q, want %q", entry.LegacyReport.ArchivePath, wantArchivePath)
	}
}

func TestLegacyMigrationPlanRejectsOutputInsideAcceptedData(t *testing.T) {
	root := t.TempDir()
	if err := ensureMigrationOutputOutsideData(root, filepath.Join(root, "migration-plan.json")); err == nil {
		t.Fatal("migration output inside accepted data root was allowed")
	}
}

func TestMigrationRepositoryJoinRequiresMatchingPreexistingGeneration(t *testing.T) {
	candidateBuiltAt := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	discovery := migrationDiscoveryFixture(t, 2, candidateBuiltAt.Add(-time.Hour))
	if repositories := migrationRepositoriesFromAcceptedState(&discovery, 1, candidateBuiltAt); len(repositories) != 0 {
		t.Fatalf("mismatched generation supplied repository identity: %#v", repositories)
	}
	discovery = migrationDiscoveryFixture(t, 1, candidateBuiltAt.Add(time.Second))
	if repositories := migrationRepositoriesFromAcceptedState(&discovery, 1, candidateBuiltAt); len(repositories) != 0 {
		t.Fatalf("later discovery state supplied repository identity: %#v", repositories)
	}
}

func TestIndexesCLIWritesFreshCandidateOutsideAcceptedData(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "accepted-data")
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	moduleID := "avm-res-network-vnet"
	metadataPath, err := identity.ModuleMetadataPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	records := index.Records{
		CatalogGeneration: 1,
		CreatedAt:         time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		Generator:         index.Producer{ID: "factory", Version: "2.0.0", ContractVersion: 1},
		RecordDigests:     map[string]string{metadataPath: strings.Repeat("a", 64)},
		Modules: []index.ModuleRecord{{
			ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-network-vnet", Lifecycle: "active",
		}},
	}
	inputBytes, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(root, "accepted-records.json")
	if err = os.WriteFile(inputPath, inputBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(root, "work", "indexes")
	if err = run([]string{
		"--build-indexes", "--index-records", inputPath,
		"--data-root", dataRoot, "--indexes-out", outputRoot,
	}); err != nil {
		t.Fatal(err)
	}
	datasetPath, err := identity.CanonicalDataPath("dataset")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(outputRoot, filepath.FromSlash(datasetPath))); err != nil {
		t.Fatalf("generated candidate is missing dataset manifest: %v", err)
	}
	if entries, err := os.ReadDir(dataRoot); err != nil || len(entries) != 0 {
		t.Fatalf("index CLI changed accepted data root: entries=%v err=%v", entries, err)
	}
	if err = run([]string{
		"--build-indexes", "--index-records", inputPath,
		"--data-root", dataRoot, "--indexes-out", outputRoot,
	}); err == nil {
		t.Fatal("index CLI replaced an existing candidate output")
	}
}

func writeMigrationDiscovery(t *testing.T, root string, generation uint64, acceptedAt time.Time) {
	t.Helper()
	state := migrationDiscoveryFixture(t, generation, acceptedAt)
	if err := catalog.SaveAccepted(filepath.Join(root, "discovery.json"), state); err != nil {
		t.Fatal(err)
	}
}

func migrationDiscoveryFixture(t *testing.T, generation uint64, acceptedAt time.Time) catalog.AcceptedState {
	t.Helper()
	modules := []catalog.Module{
		{ID: "avm-res-keyvault-vault", Type: catalog.Resource, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-keyvault-vault", DisplayName: "Vault", Description: "Key Vault"},
		{ID: "avm-ptn-test-pattern", Type: catalog.Pattern, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-ptn-test-pattern", DisplayName: "Pattern", Description: "Test pattern"},
		{ID: "avm-utl-test-utility", Type: catalog.Utility, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-utl-test-utility", DisplayName: "Utility", Description: "Test utility"},
	}
	state := catalog.AcceptedState{
		SchemaVersion: catalog.AcceptedStateSchemaVersion,
		Generation:    generation,
		AcceptedAt:    acceptedAt,
		LastFetchedAt: acceptedAt.Add(-time.Minute),
	}
	for _, source := range catalog.OfficialSources {
		state.Feeds = append(state.Feeds, catalog.FeedSnapshot{
			Kind: source.Kind, URL: source.URL, SHA256: strings.Repeat("a", 64), ModuleCount: 1,
		})
	}
	for _, module := range modules {
		state.Modules = append(state.Modules, catalog.ModuleRecord{
			Module: module, Active: true, LastSeenGeneration: generation,
		})
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("migration discovery fixture: %v", err)
	}
	return state
}

func TestAssessmentCLIListsCompiledDimensionsAndChecks(t *testing.T) {
	var dimensionsJSON bytes.Buffer
	if err := listAssessmentDefinitions(&dimensionsJSON, ""); err != nil {
		t.Fatal(err)
	}
	var dimensions []assessment.DimensionDefinition
	if err := json.Unmarshal(dimensionsJSON.Bytes(), &dimensions); err != nil {
		t.Fatal(err)
	}
	if len(dimensions) != 6 {
		t.Fatalf("listed dimensions = %d, want 6", len(dimensions))
	}
	var checksJSON bytes.Buffer
	if err := listAssessmentDefinitions(&checksJSON, assessment.DimensionProviderRequirements); err != nil {
		t.Fatal(err)
	}
	var checks []assessment.CheckDefinition
	if err := json.Unmarshal(checksJSON.Bytes(), &checks); err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].CheckID != assessment.CheckDeclaredProviderSources {
		t.Fatalf("listed provider-requirements checks = %#v", checks)
	}
}

func TestAssessmentCLISelectsDimensionsFromFactsFile(t *testing.T) {
	root := t.TempDir()
	factsPath := writeAssessmentFactsFile(t, root)
	resultsPath := filepath.Join(root, "results.json")
	selection := assessment.DimensionProviderRequirements + "," + assessment.DimensionProviderCatalog
	if err := run([]string{"--assess-facts", factsPath, "--dimension-ids", selection, "--assessment-out", resultsPath}); err != nil {
		t.Fatal(err)
	}
	results := readAssessmentResults(t, resultsPath)
	if len(results) != 2 {
		t.Fatalf("selected dimensions produced %d results, want 2", len(results))
	}
	assertAssessmentResultStatuses(t, results, assessment.StatusUnchecked)

	snapshotPath := filepath.Join(root, "provider-snapshots.json")
	observedAt := "2026-10-02T23:00:00Z"
	snapshots := []assessment.ProviderSnapshot{{
		SchemaVersion: 2, RecordType: "provider_snapshot",
		SnapshotID:      "snapshot-" + strings.Repeat("b", 64),
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", ProviderVersion: "4.0.0",
		Coverage:     assessment.SnapshotCoverage{Capabilities: map[string]string{"resource": "complete"}, Complete: true, ObservedAt: observedAt},
		Entries:      []assessment.ProviderEntry{{Kind: "resource", Name: "azurerm_resource_group"}},
		Producer:     assessment.SnapshotProducer{ID: "fixture", Version: "1.0.0", ContractVersion: 1},
		SourceDigest: strings.Repeat("c", 64), ObservedAt: observedAt,
	}}
	snapshotData, err := json.Marshal(snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(snapshotPath, snapshotData, 0o600); err != nil {
		t.Fatal(err)
	}
	withSnapshotPath := filepath.Join(root, "results-with-snapshot.json")
	if err = run([]string{
		"--assess-facts", factsPath, "--dimension-ids", selection,
		"--provider-snapshots", snapshotPath, "--assessment-out", withSnapshotPath,
	}); err != nil {
		t.Fatal(err)
	}
	assertAssessmentResultStatuses(t, readAssessmentResults(t, withSnapshotPath), assessment.StatusPass)
}

func TestAssessmentCLIRequiresReviewedClassificationForModuleChecks(t *testing.T) {
	root := t.TempDir()
	factsPath := writeAssessmentFactsFile(t, root)
	dimension := assessment.DimensionInterfaceDocumentation
	unreviewedPath := filepath.Join(root, "unreviewed.json")
	if err := run([]string{"--assess-facts", factsPath, "--dimension-ids", dimension, "--assessment-out", unreviewedPath}); err != nil {
		t.Fatal(err)
	}
	unreviewed := readAssessmentResults(t, unreviewedPath)
	if len(unreviewed) != 1 {
		t.Fatalf("unreviewed dimension results = %#v", unreviewed)
	}
	for _, check := range unreviewed[0].Checks {
		if check.Status != assessment.StatusUnchecked || check.Execution != assessment.ExecutionMissingInput {
			t.Fatalf("unreviewed module check = %#v", check)
		}
	}

	classificationPath := filepath.Join(root, "classifications.json")
	if err := os.WriteFile(classificationPath, []byte(`{".":"module"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reviewedPath := filepath.Join(root, "reviewed.json")
	if err := run([]string{
		"--assess-facts", factsPath, "--dimension-ids", dimension,
		"--classifications", classificationPath, "--assessment-out", reviewedPath,
	}); err != nil {
		t.Fatal(err)
	}
	reviewed := readAssessmentResults(t, reviewedPath)
	if len(reviewed) != 1 || len(reviewed[0].Checks) != 2 {
		t.Fatalf("reviewed dimension results = %#v", reviewed)
	}
	for _, check := range reviewed[0].Checks {
		if check.Status != assessment.StatusNotApplicable || check.Execution != assessment.ExecutionCompleted {
			t.Fatalf("reviewed module check = %#v", check)
		}
	}
}

func writeAssessmentFactsFile(t *testing.T, root string) string {
	t.Helper()
	configuration := `terraform {
  required_providers {
    azurerm = {
      source = "hashicorp/azurerm"
    }
  }
}

resource "azurerm_resource_group" "main" {}`
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: root, ModuleID: "avm-res-cli-assessment", SourceSHA: strings.Repeat("a", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []terraform.UnitInput{{Path: ".", Role: terraform.RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	factsPath := filepath.Join(root, "facts.json")
	factsData, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(factsPath, factsData, 0o600); err != nil {
		t.Fatal(err)
	}
	return factsPath
}

func readAssessmentResults(t *testing.T, path string) []assessment.DimensionResult {
	t.Helper()
	resultsData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var results []assessment.DimensionResult
	if err = json.Unmarshal(resultsData, &results); err != nil {
		t.Fatal(err)
	}
	return results
}

func assertAssessmentResultStatuses(t *testing.T, results []assessment.DimensionResult, providerCatalogStatus string) {
	t.Helper()
	statuses := map[string]string{}
	registry, err := assessment.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		for _, definition := range registry.ListDimensions() {
			if definition.DefinitionID == result.DefinitionID {
				statuses[definition.DimensionID] = result.Checks[0].Status
			}
		}
	}
	if statuses[assessment.DimensionProviderRequirements] != assessment.StatusPass || statuses[assessment.DimensionProviderCatalog] != providerCatalogStatus {
		t.Fatalf("selected dimension statuses = %#v", statuses)
	}
}

func TestPersistDiscoveryRequiresExplicitStateBranch(t *testing.T) {
	err := run([]string{
		"--persist-discovery", "missing-candidate.json",
		"--extractor-contract", "factory-hcl-v1",
		"--extractor-capabilities", "native-hcl-v1",
	})
	if err == nil || !strings.Contains(err.Error(), "--state-branch is required") {
		t.Fatalf("missing state branch error = %v", err)
	}
}

func TestPersistDiscoveryRejectsMainBeforeReadingCredentialsOrCandidate(t *testing.T) {
	err := persistAVMCatalog("missing-candidate.json", "main", "factory-hcl-v1", "native-hcl-v1")
	if err == nil || !strings.Contains(err.Error(), `must be "factory-state"`) {
		t.Fatalf("main branch error = %v", err)
	}
}

func TestDispatchOperationRequiresFactoryStateBranch(t *testing.T) {
	err := run([]string{"--dispatch-operation", "claim", "--state-branch", "main"})
	if err == nil || !strings.Contains(err.Error(), `must be "factory-state"`) {
		t.Fatalf("unsafe dispatcher branch error = %v", err)
	}
}

func TestDispatchOperationRequiresDedicatedStateToken(t *testing.T) {
	t.Setenv("FACTORY_STATE_TOKEN", "")
	err := run([]string{"--dispatch-operation", "claim", "--state-branch", "factory-state"})
	if err == nil || !strings.Contains(err.Error(), "FACTORY_STATE_TOKEN is required") {
		t.Fatalf("missing dispatcher state token error = %v", err)
	}
}

func TestMintStateTokenRequiresAppCredentials(t *testing.T) {
	t.Setenv("AVM_WRITER_APP_ID", "")
	t.Setenv("AVM_WRITER_PRIVATE_KEY", "")
	t.Setenv("APP_ID", "")
	t.Setenv("APP_PRIVATE_KEY", "")
	err := run([]string{"--mint-state-token"})
	if err == nil || !strings.Contains(err.Error(), "GitHub App credentials are required") {
		t.Fatalf("missing App credentials error = %v", err)
	}
}
