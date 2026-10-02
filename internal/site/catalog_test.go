package site

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
)

func TestBuildAcceptedCatalogEscapesEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(dataRoot, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := iaclens.Report{
		SchemaVersion: iaclens.SupportedSchemaVersion, AdapterVersion: iaclens.AdapterVersion,
		Provenance:  iaclens.Provenance{ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("a", 40), CLIVersion: "0.1.0", CLICommit: strings.Repeat("b", 40), AdapterVersion: 1},
		RulesetName: "factory", RulesetDigest: strings.Repeat("c", 64), SourceDigest: strings.Repeat("d", 64),
		Units: []iaclens.Unit{{Path: ".", Role: "component", Classification: iacLensClassification(), Checks: []iaclens.Check{{ID: "test", Engine: "go", Status: "unchecked", Message: "<script>alert(1)</script>"}}}},
	}
	writeSiteJSON(t, filepath.Join(dataRoot, "modules", "avm-res-demo.json"), report)
	candidate := publish.Candidate{
		SchemaVersion: publish.CandidateSchemaVersion, CatalogGeneration: 3, BuiltAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		Modules: []publish.CandidateModule{{ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("a", 40), Fingerprint: strings.Repeat("e", 64), ReportPath: "modules/avm-res-demo.json"}},
	}
	writeSiteJSON(t, filepath.Join(dataRoot, "candidate.json"), candidate)
	out := filepath.Join(root, "site")
	if err := Build(dataRoot, out); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "avm-res-demo") || !strings.Contains(string(index), "modules/avm-res-demo.html") {
		t.Fatalf("index missing module link: %s", index)
	}
	page, err := os.ReadFile(filepath.Join(out, "modules", "avm-res-demo.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(page), "<script>alert") || !strings.Contains(string(page), "&lt;script&gt;") || !strings.Contains(string(page), "unchecked") {
		t.Fatalf("module page did not safely preserve evidence: %s", page)
	}
}

func TestCompareLegacyAndIndexedCatalogPreservesIDsBytesAndCheckEvidence(t *testing.T) {
	root := t.TempDir()
	legacyRoot := filepath.Join(root, "legacy")
	if err := os.MkdirAll(filepath.Join(legacyRoot, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	moduleID := "avm-res-demo"
	sourceSHA := strings.Repeat("a", 40)
	report := iaclens.Report{
		SchemaVersion: iaclens.SupportedSchemaVersion, AdapterVersion: iaclens.AdapterVersion,
		Provenance:  iaclens.Provenance{ModuleID: moduleID, SourceSHA: sourceSHA, CLIVersion: "0.1.0", CLICommit: strings.Repeat("b", 40), AdapterVersion: 1},
		RulesetName: "factory", RulesetDigest: strings.Repeat("c", 64), SourceDigest: strings.Repeat("d", 64),
		Units: []iaclens.Unit{{
			Path: ".", Role: "component", Classification: iacLensClassification(),
			Checks: []iaclens.Check{{ID: "input-descriptions", Engine: "go", Status: "unchecked", Severity: "warning", Message: "not assessed", Evidence: []string{"variables.tf:3"}}},
		}},
	}
	reportBytes, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(legacyRoot, "modules", moduleID+".json")
	if err = os.WriteFile(reportPath, reportBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	builtAt := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	candidate := publish.Candidate{
		SchemaVersion: publish.CandidateSchemaVersion, CatalogGeneration: 3, BuiltAt: builtAt,
		Modules: []publish.CandidateModule{{ModuleID: moduleID, SourceSHA: sourceSHA, Fingerprint: strings.Repeat("e", 64), ReportPath: "modules/" + moduleID + ".json"}},
	}
	writeSiteJSON(t, filepath.Join(legacyRoot, "candidate.json"), candidate)
	metadataPath, err := identity.ModuleMetadataPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	sourcePath, err := identity.ModuleSourcePath(moduleID, sourceSHA)
	if err != nil {
		t.Fatal(err)
	}
	reportDigest := sha256.Sum256(reportBytes)
	acceptedRecords := index.Records{
		CatalogGeneration: 3, CreatedAt: builtAt, Generator: index.Producer{ID: "factory", Version: "2.0.0", ContractVersion: 1},
		RecordDigests: map[string]string{
			metadataPath: strings.Repeat("1", 64), sourcePath: strings.Repeat("2", 64),
			"data/modules/" + moduleID + ".json": hex.EncodeToString(reportDigest[:]),
		},
		Modules:         []index.ModuleRecord{{ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-demo", Lifecycle: "active"}},
		SourceRevisions: []index.SourceRevisionRecord{{ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-demo", SourceSHA: sourceSHA, Immutable: true}},
	}
	indexes, err := index.Build(acceptedRecords)
	if err != nil {
		t.Fatal(err)
	}
	indexedRoot := filepath.Join(root, "indexed")
	writeIndexFiles(t, indexedRoot, indexes)
	readiness, err := CompareLegacyAndIndex(legacyRoot, indexedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.LegacyModuleCount != 1 || readiness.IndexModuleCount != 1 || len(readiness.Modules) != 1 {
		t.Fatalf("legacy/index catalog counts differ: %#v", readiness)
	}
	evidence := readiness.Modules[0]
	if evidence.ModuleID != moduleID || evidence.SourceSHA != sourceSHA || evidence.LegacyReportSHA256 != hex.EncodeToString(reportDigest[:]) || evidence.CheckCount != 1 || evidence.CheckEvidenceSHA256 == "" {
		t.Fatalf("legacy evidence was not retained in readiness view: %#v", evidence)
	}
	if err = os.WriteFile(reportPath, append(reportBytes, ' '), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = CompareLegacyAndIndex(legacyRoot, indexedRoot); err == nil {
		t.Fatal("changed legacy report bytes passed index readiness")
	}
}

func writeIndexFiles(t *testing.T, root string, indexes index.Indexes) {
	t.Helper()
	files, err := indexes.Files()
	if err != nil {
		t.Fatal(err)
	}
	for relativePath, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(relativePath))
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func iacLensClassification() iaclens.Classification {
	return iaclens.Classification{Kind: "module", Method: "override", Reason: "fixture"}
}

func writeSiteJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
