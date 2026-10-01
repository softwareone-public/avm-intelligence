package publish

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
)

func TestAssembleValidatesAndWritesWorkerArtifact(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	artifacts := filepath.Join(root, "artifacts", "fleet-analysis-avm-res-demo")
	if err := os.MkdirAll(filepath.Join(artifacts, "work", "fleet", "requests"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	input := candidateInput("avm-res-demo", strings.Repeat("a", 40))
	request, err := queue.New(input, now)
	if err != nil {
		t.Fatal(err)
	}
	request.Status = queue.StatusAccepted
	request.LastAcceptedFingerprint = request.Fingerprint
	request.LastAcceptedSourceSHA = request.SourceSHA
	writeJSONFile(t, filepath.Join(artifacts, "work", "fleet", "requests", request.ModuleID+".json"), request)
	report := candidateReport(request)
	writeJSONFile(t, filepath.Join(artifacts, "work", "fleet", "analysis.json"), report)
	plan := queue.BatchPlan{
		CatalogGeneration: 4,
		Items:             []queue.PlanItem{{ModuleID: request.ModuleID, Action: queue.PlanQueued, SourceSHA: request.SourceSHA, Fingerprint: request.Fingerprint}},
		Batches:           []queue.DispatchBatch{{Number: 1, ModuleIDs: []string{request.ModuleID}}},
	}
	out := filepath.Join(root, "data")
	candidate, err := Assemble(plan, filepath.Join(root, "artifacts"), out, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Modules) != 1 || candidate.Modules[0].ModuleID != request.ModuleID {
		t.Fatalf("candidate modules = %#v", candidate.Modules)
	}
	if _, err = os.Stat(filepath.Join(out, "modules", request.ModuleID+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestAssembleRejectsIncompleteWorkerArtifacts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	plan := queue.BatchPlan{
		CatalogGeneration: 1,
		Items:             []queue.PlanItem{{ModuleID: "avm-res-demo", Action: queue.PlanQueued}},
		Batches:           []queue.DispatchBatch{{Number: 1, ModuleIDs: []string{"avm-res-demo"}}},
	}
	if _, err := Assemble(plan, filepath.Join(root, "artifacts"), filepath.Join(root, "data"), time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("incomplete artifact error = %v", err)
	}
}

type invalidDeltaCase struct {
	name                  string
	status                string
	stalePlan             bool
	duplicateArtifact     bool
	conflictingProvenance bool
	wantError             string
}

func TestAssembleRejectsInvalidModuleDeltas(t *testing.T) {
	testCases := []invalidDeltaCase{
		{name: "failed", status: queue.StatusFailed, wantError: "not an accepted terminal request"},
		{name: "stale", status: queue.StatusAccepted, stalePlan: true, wantError: "does not match the plan"},
		{name: "duplicate", status: queue.StatusAccepted, duplicateArtifact: true, wantError: "duplicate worker artifact"},
		{name: "conflicting", status: queue.StatusAccepted, conflictingProvenance: true, wantError: "does not match request provenance"},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assertInvalidDeltaRejected(t, testCase)
		})
	}
}

func assertInvalidDeltaRejected(t *testing.T, testCase invalidDeltaCase) {
	t.Helper()
	root := t.TempDir()
	artifactsRoot := filepath.Join(root, "artifacts")
	moduleID := "avm-res-demo"
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	request, err := queue.New(candidateInput(moduleID, strings.Repeat("a", 40)), now)
	if err != nil {
		t.Fatal(err)
	}
	request.Status = testCase.status
	request.LastAcceptedFingerprint = request.Fingerprint
	request.LastAcceptedSourceSHA = request.SourceSHA
	report := candidateReport(request)
	if testCase.conflictingProvenance {
		report.Provenance.SourceSHA = strings.Repeat("b", 40)
	}
	writeWorkerArtifact(t, artifactsRoot, "fleet-analysis-first", request, report)
	if testCase.duplicateArtifact {
		writeWorkerArtifact(t, artifactsRoot, "fleet-analysis-second", request, report)
	}

	plan := invalidDeltaPlan(request, testCase.stalePlan)
	outputRoot := filepath.Join(root, "candidate")
	if _, err = Assemble(plan, artifactsRoot, outputRoot, now); err == nil || !strings.Contains(err.Error(), testCase.wantError) {
		t.Fatalf("invalid delta error = %v, want containing %q", err, testCase.wantError)
	}
	if _, err = os.Stat(filepath.Join(outputRoot, candidateIndexName)); !os.IsNotExist(err) {
		t.Fatalf("invalid delta wrote candidate index: %v", err)
	}
}

func writeWorkerArtifact(t *testing.T, artifactsRoot, artifactName string, request queue.Request, report iaclens.Report) {
	t.Helper()
	artifactRoot := filepath.Join(artifactsRoot, artifactName, "work", "fleet")
	if err := os.MkdirAll(filepath.Join(artifactRoot, "requests"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(artifactRoot, "requests", request.ModuleID+".json"), request)
	writeJSONFile(t, filepath.Join(artifactRoot, "analysis.json"), report)
}

func invalidDeltaPlan(request queue.Request, stale bool) queue.BatchPlan {
	planFingerprint := request.Fingerprint
	planSourceSHA := request.SourceSHA
	if stale {
		planFingerprint = strings.Repeat("e", 64)
		planSourceSHA = strings.Repeat("d", 40)
	}
	return queue.BatchPlan{
		CatalogGeneration: 1,
		Items: []queue.PlanItem{{
			ModuleID: request.ModuleID, Action: queue.PlanQueued,
			SourceSHA: planSourceSHA, Fingerprint: planFingerprint,
		}},
		Batches: []queue.DispatchBatch{{Number: 1, ModuleIDs: []string{request.ModuleID}}},
	}
}

func candidateInput(moduleID, sourceSHA string) queue.AnalysisInput {
	return queue.AnalysisInput{
		ModuleID: moduleID, SourceSHA: sourceSHA, CLIVersion: "0.1.0", CLICommit: strings.Repeat("b", 40),
		BinaryDigest: strings.Repeat("c", 64), PolicyDigest: strings.Repeat("d", 64), ReportSchemaVersion: 2, AdapterVersion: 1,
	}
}

func candidateReport(request queue.Request) iaclens.Report {
	return iaclens.Report{
		SchemaVersion: iaclens.SupportedSchemaVersion, AdapterVersion: iaclens.AdapterVersion,
		Provenance:  iaclens.Provenance{ModuleID: request.ModuleID, SourceSHA: request.SourceSHA, CLIVersion: "0.1.0", CLICommit: strings.Repeat("b", 40), AdapterVersion: 1},
		RulesetName: "factory", RulesetDigest: strings.Repeat("c", 64), SourceDigest: strings.Repeat("d", 64),
		Units: []iaclens.Unit{{Path: ".", Role: "component", Classification: iaclens.Classification{Kind: "module", Method: "override", Reason: "fixture"}, Checks: []iaclens.Check{{ID: "test", Engine: "go", Status: "pass"}}}},
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
