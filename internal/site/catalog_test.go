package site

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
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
