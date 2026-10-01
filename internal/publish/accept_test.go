package publish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
)

func TestAcceptCandidateSwapsDataAndControlState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	candidateRoot := filepath.Join(root, "candidate")
	if err := os.MkdirAll(filepath.Join(candidateRoot, "modules"), 0o755); err != nil {
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
	requestRoot := filepath.Join(root, "requests")
	if err = queue.Save(requestRoot, request); err != nil {
		t.Fatal(err)
	}
	report := candidateReport(request)
	writeJSONFile(t, filepath.Join(candidateRoot, "modules", request.ModuleID+".json"), report)
	candidate := Candidate{SchemaVersion: CandidateSchemaVersion, CatalogGeneration: 2, BuiltAt: now, Modules: []CandidateModule{{ModuleID: request.ModuleID, SourceSHA: request.SourceSHA, Fingerprint: request.Fingerprint, ReportPath: "modules/" + request.ModuleID + ".json"}}}
	writeJSONFile(t, filepath.Join(candidateRoot, "candidate.json"), candidate)
	discoveryPath := filepath.Join(root, "discovery.json")
	if err = catalog.SaveAccepted(discoveryPath, acceptedStateForTest(request.ModuleID)); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(root, "data")
	if err = AcceptCandidate(candidateRoot, discoveryPath, requestRoot, dataRoot); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dataRoot, "modules", request.ModuleID+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dataRoot, "discovery.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dataRoot, "requests", request.ModuleID+".json")); err != nil {
		t.Fatal(err)
	}
}

func acceptedStateForTest(moduleID string) catalog.AcceptedState {
	return catalog.AcceptedState{
		SchemaVersion: catalog.AcceptedStateSchemaVersion,
		Generation:    2,
		AcceptedAt:    nowForTest(), LastFetchedAt: nowForTest().Add(-time.Hour),
		Feeds: []catalog.FeedSnapshot{
			{Kind: catalog.Resource, URL: catalog.OfficialSources[0].URL, SHA256: strings.Repeat("a", 64), ModuleCount: 1},
			{Kind: catalog.Pattern, URL: catalog.OfficialSources[1].URL, SHA256: strings.Repeat("b", 64), ModuleCount: 1},
			{Kind: catalog.Utility, URL: catalog.OfficialSources[2].URL, SHA256: strings.Repeat("c", 64), ModuleCount: 1},
		},
		Modules: []catalog.ModuleRecord{
			{Module: catalog.Module{ID: moduleID, Type: catalog.Resource, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-avm-res-demo", RegistryURL: "https://registry.terraform.io/modules/Azure/" + moduleID, DisplayName: "Demo", Description: "Demo"}, Active: true, LastSeenGeneration: 2},
			{Module: catalog.Module{ID: "avm-ptn-demo", Type: catalog.Pattern, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-avm-ptn-demo", DisplayName: "Pattern", Description: "Pattern"}, Active: true, LastSeenGeneration: 2},
			{Module: catalog.Module{ID: "avm-utl-demo", Type: catalog.Utility, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-avm-utl-demo", DisplayName: "Utility", Description: "Utility"}, Active: true, LastSeenGeneration: 2},
		},
	}
}

func nowForTest() time.Time {
	return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
}
