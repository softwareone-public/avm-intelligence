package site

import (
	"os"
	"strings"
	"testing"

	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
)

func TestWriteEscapesEvidenceAndPreservesStatus(t *testing.T) {
	t.Parallel()
	report := iaclens.Report{
		SchemaVersion: iaclens.SupportedSchemaVersion, AdapterVersion: iaclens.AdapterVersion,
		Provenance:  iaclens.Provenance{ModuleID: "avm-<demo>", SourceSHA: strings.Repeat("a", 40), CLIVersion: "0.1.0", CLICommit: strings.Repeat("b", 40), AdapterVersion: iaclens.AdapterVersion},
		RulesetName: "factory", RulesetDigest: strings.Repeat("c", 64), SourceDigest: strings.Repeat("d", 64),
		Units: []iaclens.Unit{{
			Path: ".", Role: "component", Classification: iaclens.Classification{Kind: "module", Method: "override", Reason: "fixture"},
			Checks: []iaclens.Check{{ID: "input-descriptions", Engine: "go", Status: "unchecked", Severity: "warning", Message: "<script>alert(1)</script>"}},
		}},
	}
	path := t.TempDir() + "/index.html"
	if err := Write(path, report, "v0.4.0"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	if strings.Contains(page, "<script>alert") || !strings.Contains(page, "unchecked") || !strings.Contains(page, "avm-&lt;demo&gt;") {
		t.Fatalf("page did not preserve safe evidence:\n%s", page)
	}
}
