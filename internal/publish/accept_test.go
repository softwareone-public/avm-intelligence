package publish

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
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

func TestApplyModuleDeltaPreservesUntouchedAcceptedModule(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	candidateRoot := filepath.Join(root, "candidate")
	dataRoot := filepath.Join(root, "data")
	mergedDataRoot := filepath.Join(root, "merged-data")
	requestsRoot := filepath.Join(dataRoot, "requests")
	for _, directory := range []string{
		filepath.Join(candidateRoot, "modules"),
		filepath.Join(dataRoot, "modules"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	acceptedRequests := make(map[string]queue.Request)
	baseCandidateModules := make([]CandidateModule, 0, 2)
	for moduleID, sourceChar := range map[string]string{
		"avm-res-existing-a": "a",
		"avm-res-existing-b": "b",
	} {
		request, err := queue.New(candidateInput(moduleID, strings.Repeat(sourceChar, 40)), now)
		if err != nil {
			t.Fatal(err)
		}
		request.Status = queue.StatusAccepted
		request.LastAcceptedFingerprint = request.Fingerprint
		request.LastAcceptedSourceSHA = request.SourceSHA
		acceptedRequests[moduleID] = request
		if err = queue.Save(requestsRoot, request); err != nil {
			t.Fatal(err)
		}
		writeJSONFile(t, filepath.Join(dataRoot, "modules", moduleID+".json"), candidateReport(request))
		baseCandidateModules = append(baseCandidateModules, CandidateModule{
			ModuleID: moduleID, SourceSHA: request.SourceSHA, Fingerprint: request.Fingerprint,
			ReportPath: "modules/" + moduleID + ".json",
		})
	}
	sort.Slice(baseCandidateModules, func(left, right int) bool {
		return baseCandidateModules[left].ModuleID < baseCandidateModules[right].ModuleID
	})
	writeJSONFile(t, filepath.Join(dataRoot, candidateIndexName), Candidate{
		SchemaVersion: CandidateSchemaVersion, CatalogGeneration: 1,
		BuiltAt: now, Modules: baseCandidateModules,
	})

	untouchedModuleID := "avm-res-existing-b"
	untouchedReportPath := filepath.Join(dataRoot, "modules", untouchedModuleID+".json")
	untouchedRequestPath := filepath.Join(requestsRoot, untouchedModuleID+".json")
	untouchedReportBefore, err := os.ReadFile(untouchedReportPath)
	if err != nil {
		t.Fatal(err)
	}
	untouchedRequestBefore, err := os.ReadFile(untouchedRequestPath)
	if err != nil {
		t.Fatal(err)
	}

	updatedRequest, err := queue.New(candidateInput("avm-res-existing-a", strings.Repeat("c", 40)), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	updatedRequest.Status = queue.StatusAccepted
	updatedRequest.LastAcceptedFingerprint = updatedRequest.Fingerprint
	updatedRequest.LastAcceptedSourceSHA = updatedRequest.SourceSHA
	nextRequestsRoot := filepath.Join(root, "next-requests")
	if err = queue.Save(nextRequestsRoot, updatedRequest); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(candidateRoot, "modules", updatedRequest.ModuleID+".json"), candidateReport(updatedRequest))
	candidate := Candidate{
		SchemaVersion:     CandidateSchemaVersion,
		CatalogGeneration: 2,
		BuiltAt:           now.Add(time.Hour),
		Modules: []CandidateModule{{
			ModuleID: updatedRequest.ModuleID, SourceSHA: updatedRequest.SourceSHA,
			Fingerprint: updatedRequest.Fingerprint, ReportPath: "modules/" + updatedRequest.ModuleID + ".json",
		}},
	}
	writeJSONFile(t, filepath.Join(candidateRoot, candidateIndexName), candidate)
	discoveryPath := filepath.Join(root, "discovery.json")
	if err = catalog.SaveAccepted(discoveryPath, acceptedStateForTest(updatedRequest.ModuleID)); err != nil {
		t.Fatal(err)
	}

	if err = ApplyModuleDelta(candidateRoot, discoveryPath, nextRequestsRoot, dataRoot, mergedDataRoot); err != nil {
		t.Fatal(err)
	}
	untouchedReportAfter, err := os.ReadFile(filepath.Join(mergedDataRoot, "modules", untouchedModuleID+".json"))
	if err != nil {
		t.Fatalf("untouched accepted report was not preserved: %v", err)
	}
	if !bytes.Equal(untouchedReportBefore, untouchedReportAfter) {
		t.Fatal("untouched accepted report changed")
	}
	untouchedRequestAfter, err := os.ReadFile(filepath.Join(mergedDataRoot, "requests", untouchedModuleID+".json"))
	if err != nil {
		t.Fatalf("untouched accepted request was not preserved: %v", err)
	}
	if !bytes.Equal(untouchedRequestBefore, untouchedRequestAfter) {
		t.Fatal("untouched accepted request changed")
	}
	mergedCandidate, err := LoadCandidate(mergedDataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(mergedCandidate.Modules) != 2 ||
		mergedCandidate.Modules[0].ModuleID != updatedRequest.ModuleID ||
		mergedCandidate.Modules[0].SourceSHA != updatedRequest.SourceSHA ||
		mergedCandidate.Modules[1].ModuleID != untouchedModuleID {
		t.Fatalf("merged candidate membership = %#v", mergedCandidate.Modules)
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
