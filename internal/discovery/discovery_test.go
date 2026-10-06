package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/github"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestPersistEmitsCatalogProposalWithoutStableSources(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/releases") {
			t.Errorf("unexpected request %s", request.URL.Path)
			http.NotFound(response, request)
			return
		}
		_, _ = response.Write([]byte(`[]`))
	}))
	defer server.Close()
	backend := newDiscoveryMemoryBackend()

	result, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, Options{
		AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertReleaseLessCatalogTransaction(t, backend, result, len(candidate.Modules))
}

func TestDiscoveryProgressPreservesDurableState(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = response.Write([]byte(`[]`))
	}))
	defer server.Close()
	var baseline []byte
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelError} {
		var output bytes.Buffer
		slog.SetDefault(slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: level})))
		backend := newDiscoveryMemoryBackend()
		_, err := Persist(context.Background(), source.Resolver{APIBase: server.URL, Token: "private-token-sentinel"}, backend, discoveryTestSnapshot(now), Options{
			AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
		})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(backend.snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if baseline == nil {
			baseline = encoded
		} else if !bytes.Equal(baseline, encoded) {
			t.Fatal("logging changed durable discovery evidence")
		}
		if strings.Contains(output.String(), "private-token-sentinel") {
			t.Fatal("source token leaked to logs")
		}
		if level == slog.LevelInfo {
			assertDiscoveryProgress(t, output.String())
		}
	}
}

func TestDiscoveryProgressDoesNotLogAPIDiagnostics(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusForbidden)
		_, _ = response.Write([]byte(`{"message":"private-body-sentinel"}`))
	}))
	defer server.Close()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	_, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, newDiscoveryMemoryBackend(), discoveryTestSnapshot(now), Options{
		AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "private-body-sentinel") || !strings.Contains(output.String(), "source resolution unavailable") {
		t.Fatal("source failure logs missing or contain raw diagnostics")
	}
}

func assertDiscoveryProgress(t *testing.T, output string) {
	t.Helper()
	for _, expected := range []string{"module_id=avm-res-", "phase=source_resolution", "position=", "source resolution unavailable", "discovery module prepared", "discovery transaction committed", "state committed"} {
		if !strings.Contains(output, expected) {
			t.Errorf("missing discovery progress %q", expected)
		}
	}
}

func TestPersistForcedModuleRequeuesUnchangedSourceOnlyForTarget(t *testing.T) {
	startedAt := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(startedAt)
	moduleID := "avm-res-resources-resourcegroup"
	targetRepository := "Azure/terraform-azurerm-avm-res-resources-resourcegroup"
	const stableCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var mu sync.Mutex
	releaseCalls := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		path := request.URL.Path
		if strings.HasSuffix(path, "/releases") {
			repository := strings.TrimSuffix(strings.TrimPrefix(path, "/repos/"), "/releases")
			mu.Lock()
			releaseCalls[repository]++
			mu.Unlock()
			_, _ = fmt.Fprintf(response, `[{"tag_name":"v1.0.0","html_url":"https://github.com/%s/releases/tag/v1.0.0","published_at":"2026-01-01T00:00:00Z"}]`, repository)
			return
		}
		if strings.Contains(path, "/git/ref/tags/") {
			_, _ = fmt.Fprintf(response, `{"object":{"type":"commit","sha":%q}}`, stableCommit)
			return
		}
		t.Errorf("unexpected request %s", request.URL)
		http.NotFound(response, request)
	}))
	defer server.Close()
	backend := newDiscoveryMemoryBackend()
	options := Options{AcceptedAt: startedAt.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}}
	first, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ProjectionModuleIDs) != len(candidate.Modules) {
		t.Fatalf("initial projection targets=%v, want all newly pinned modules", first.ProjectionModuleIDs)
	}
	acceptedRequest := acceptDiscoveryTestFacts(t, backend, moduleID, startedAt.Add(2*time.Minute))
	mu.Lock()
	beforeTargetCalls := releaseCalls[targetRepository]
	beforeOtherCalls := make(map[string]int, len(releaseCalls))
	for repository, count := range releaseCalls {
		beforeOtherCalls[repository] = count
	}
	mu.Unlock()
	candidate.FetchedAt = startedAt.Add(3 * time.Minute)
	options.AcceptedAt = startedAt.Add(4 * time.Minute)
	forced, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, Options{
		AcceptedAt: options.AcceptedAt, ExtractorContract: options.ExtractorContract,
		Capabilities: options.Capabilities, ForceModuleIDs: []string{moduleID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !forced.Delta.NoChange || len(forced.ProjectionModuleIDs) != 1 || forced.ProjectionModuleIDs[0] != moduleID {
		t.Fatalf("forced no-change discovery = delta %#v, projection modules %v", forced.Delta, forced.ProjectionModuleIDs)
	}
	mu.Lock()
	if releaseCalls[targetRepository] != beforeTargetCalls+1 {
		t.Errorf("target release lookups = %d, want %d", releaseCalls[targetRepository], beforeTargetCalls+1)
	}
	for repository, count := range beforeOtherCalls {
		if repository != targetRepository && releaseCalls[repository] != count {
			t.Errorf("unselected repository %s was resolved again: got %d lookups, want %d", repository, releaseCalls[repository], count)
		}
	}
	mu.Unlock()
	assertAcquisitionRequestGenerations(t, backend.snapshot, moduleID, 2)
	requests := moduleRequests(backend.snapshot, moduleID)
	var acceptedStatus, queuedStatus string
	for _, request := range requests {
		if request.Work.DesiredGeneration == acceptedRequest.Work.DesiredGeneration {
			acceptedStatus = request.Status
		}
		if request.Work.DesiredGeneration > acceptedRequest.Work.DesiredGeneration {
			queuedStatus = request.Status
		}
	}
	if acceptedStatus != queue.StatusFactsAccepted || queuedStatus != queue.StatusQueued {
		t.Fatalf("forced requeue statuses accepted=%q queued=%q, want accepted history and new queued request", acceptedStatus, queuedStatus)
	}
	var intent ProjectionIntent
	if err = json.Unmarshal(backend.snapshot.Records[projectionIntentPath+moduleID+".json"].Data, &intent); err != nil {
		t.Fatal(err)
	}
	if intent.Status != IntentBacklog || intent.DesiredGeneration < 2 {
		t.Fatalf("forced discovery intent = %#v, want backlog at a bumped generation", intent)
	}
}

func TestManualCycleStopsBeforeResolvingNextModuleOnDeliveryFailure(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	first := "avm-res-resources-resourcegroup"
	backend := newDiscoveryMemoryBackend()
	server := newScopedDiscoveryServer(t, first)
	defer server.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{first, "avm-res-resources-storage"}}
	result, err := Cycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, options, func(ctx context.Context, module ModuleCycle) (string, error) {
		if module.ModuleID != first || module.Head != backend.head || countRecordsWithKind(backend.snapshot, "request") != 1 {
			t.Fatal("delivery was invoked before singleton source state was committed")
		}
		if _, exists := backend.snapshot.Records[projectionIntentPath+"avm-res-resources-storage.json"]; exists {
			t.Fatal("later module was processed before the first delivery")
		}
		return module.Head, fmt.Errorf("fixture delivery failure")
	})
	if err == nil || len(result.CompletedModuleIDs) != 0 || countRecordsWithKind(backend.snapshot, "request") != 1 {
		t.Fatalf("cycle did not retain only its failed target: result=%+v error=%v", result, err)
	}
}

func TestManualCycleAcknowledgesEachModuleBeforeNextAndRetriesWithoutDuplicateRequests(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	selected := []string{"avm-ptn-test", "avm-utl-test", "avm-res-resources-storage", "avm-res-resources-resourcegroup"}
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: selected}
	_, err := Cycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, options, func(ctx context.Context, module ModuleCycle) (string, error) {
		return module.Head, fmt.Errorf("fixture delivery failure")
	})
	if err == nil || countRecordsWithKind(backend.snapshot, "request") != 1 {
		t.Fatal("failure did not retain exactly the first committed module")
	}
	candidate.FetchedAt = now.Add(2 * time.Minute)
	options.AcceptedAt = now.Add(3 * time.Minute)
	var completed []string
	result, err := Cycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, options, func(ctx context.Context, module ModuleCycle) (string, error) {
		if countRecordsWithKind(backend.snapshot, "request") != module.Position || countRecordsWithKind(backend.snapshot, "projection_delivery") != module.Position-1 {
			t.Fatal("next module advanced before the previous module acknowledgement")
		}
		head := acknowledgeCycleTestModule(t, ctx, backend, module, options.AcceptedAt.Add(time.Duration(module.Position)*time.Second))
		completed = append(completed, module.ModuleID)
		return head, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "avm-res-resources-resourcegroup,avm-res-resources-storage,avm-utl-test,avm-ptn-test"
	if strings.Join(completed, ",") != want || strings.Join(result.CompletedModuleIDs, ",") != want || countRecordsWithKind(backend.snapshot, "discovery_observation") != 2 {
		t.Fatalf("cycle order or shared observation count changed: %+v", result)
	}
	if countRecordsWithKind(backend.snapshot, "request") != len(selected) {
		t.Fatal("retry duplicated acquisition work")
	}
}

func acknowledgeCycleTestModule(t *testing.T, ctx context.Context, backend *memoryBackend, module ModuleCycle, now time.Time) string {
	t.Helper()
	var intent ProjectionIntent
	if err := json.Unmarshal(module.Snapshot.Records[projectionIntentPath+module.ModuleID+".json"].Data, &intent); err != nil {
		t.Fatal(err)
	}
	projection, err := github.FromReducedStatus(queue.ModuleStatus{ModuleID: module.ModuleID, Status: queue.ModuleStatusBacklog, SourceVersion: intent.Source.Version, SourceSHA: intent.Source.Commit}, github.ReducedProjectionInput{ModuleType: string(cycleModuleRecord(module.Accepted, module.ModuleID).Module.Type), RunURL: "https://github.com/example/run"})
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := github.NewProjectionIntent(projection, intent.DesiredGeneration, github.ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := github.SaveProjectionIntents(ctx, backend, module.Head, []github.ProjectionIntentRecord{delivery})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := github.ClaimProjectionBatchForModules(ctx, backend, github.ProjectionClaimOptions{ExpectedHead: saved.Head, RunID: "cycle-test", Now: now, Lease: time.Minute, Limit: 1}, []string{module.ModuleID})
	if err != nil || len(claim.Intents) != 1 {
		t.Fatalf("singleton claim failed: %+v %v", claim, err)
	}
	outcome, err := github.RecordProjectionOutcome(ctx, backend, claim.Head, github.ProjectionOutcomeInput{
		BatchID: claim.Batch.ID, ModuleID: module.ModuleID, RunID: "cycle-test", Generation: delivery.Generation, Fence: claim.Intents[0].Fence,
		Outcome: github.ProjectionOutcome{Success: true, CompletedAt: now.Add(time.Second), Binding: &github.ProjectionBinding{
			SchemaVersion: 1, ModuleID: module.ModuleID, IssueNumber: module.Position,
			IssueNodeID: "issue-" + module.ModuleID, IssueURL: fmt.Sprintf("https://github.com/example/issues/%d", module.Position),
			ProjectItemID: "item-" + module.ModuleID, Generation: delivery.Generation, UpdatedAt: now.Add(time.Second),
		}},
	})
	if err != nil || outcome.Intent.DeliveryState != github.ProjectionProjected {
		t.Fatalf("module acknowledgement failed: %+v %v", outcome, err)
	}
	return outcome.Head
}

func TestCatalogReferenceTracksNonCyclePinUpdateAfterCycle(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	backend := newDiscoveryMemoryBackend()
	firstServer := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer firstServer.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}}
	_, err := Cycle(context.Background(), source.Resolver{APIBase: firstServer.URL}, backend, candidate, options, func(ctx context.Context, module ModuleCycle) (string, error) {
		return acknowledgeCycleTestModule(t, ctx, backend, module, now.Add(2*time.Minute)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var previous CatalogProposalReference
	if err := json.Unmarshal(backend.snapshot.Records[CatalogProposalReferencePath].Data, &previous); err != nil {
		t.Fatal(err)
	}
	nextServer := newVersionServer(t, "v2.0.0", strings.Repeat("b", 40), "v1.0.0", strings.Repeat("a", 40))
	defer nextServer.Close()
	candidate.FetchedAt = now.Add(3 * time.Minute)
	options.AcceptedAt = now.Add(4 * time.Minute)
	result, err := Persist(context.Background(), source.Resolver{APIBase: nextServer.URL}, backend, candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	var current CatalogProposalReference
	if err := json.Unmarshal(backend.snapshot.Records[CatalogProposalReferencePath].Data, &current); err != nil {
		t.Fatal(err)
	}
	if current.Generation != previous.Generation || current.ProposalID != result.ProposalID || current.ProposalID == previous.ProposalID {
		t.Fatalf("non-cycle writer left a stale outbox reference: previous=%+v current=%+v", previous, current)
	}
	if _, retained := backend.snapshot.Records["state/exports/"+previous.ProposalID+".json"]; !retained {
		t.Fatal("non-cycle writer deleted the earlier immutable cycle outbox")
	}
}

func TestCycleOrderUsesResourceUtilityPatternThenID(t *testing.T) {
	accepted := catalog.AcceptedState{Modules: []catalog.ModuleRecord{
		{Module: catalog.Module{ID: "avm-ptn-z", Type: catalog.Pattern}},
		{Module: catalog.Module{ID: "avm-utl-z", Type: catalog.Utility}},
		{Module: catalog.Module{ID: "avm-res-z", Type: catalog.Resource}},
		{Module: catalog.Module{ID: "avm-res-a", Type: catalog.Resource}},
	}}
	ordered := orderedCycleModules(accepted, []string{"avm-ptn-z", "avm-res-z", "avm-utl-z", "avm-res-a"})
	if strings.Join(ordered, ",") != "avm-res-a,avm-res-z,avm-utl-z,avm-ptn-z" {
		t.Fatalf("cycle order=%v", ordered)
	}
}

func TestFactoryDiscoveryEmptyNewScopeDoesNotResolveExistingModules(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, NewModulesOnly: true}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, options); err != nil {
		t.Fatal(err)
	}
	before := cloneSnapshot(backend.snapshot)
	forbidden := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		t.Error("empty new-module scope resolved an existing source")
		response.WriteHeader(http.StatusForbidden)
	}))
	defer forbidden.Close()
	candidate.FetchedAt = now.Add(2 * time.Minute)
	options.AcceptedAt = now.Add(3 * time.Minute)
	result, err := Persist(context.Background(), source.Resolver{APIBase: forbidden.URL}, backend, candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SelectedModuleIDs) != 0 || len(result.ProjectionModuleIDs) != 0 {
		t.Fatal("empty factory scope broadened to existing modules")
	}
	assertModuleRecordsPreserved(t, before, backend.snapshot)
}

func TestFactoryDiscoveryProcessesOnlyAddedCatalogIDs(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	newID := "avm-res-resources-storage"
	full := discoveryTestSnapshot(now)
	seed := removeDiscoveryModule(full, newID, now)
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, NewModulesOnly: true}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, seed, options); err != nil {
		t.Fatal(err)
	}
	before := cloneSnapshot(backend.snapshot)
	scoped := newScopedDiscoveryServer(t, newID)
	defer scoped.Close()
	full = discoveryTestSnapshot(now.Add(2 * time.Minute))
	options.AcceptedAt = now.Add(3 * time.Minute)
	result, err := Persist(context.Background(), source.Resolver{APIBase: scoped.URL}, backend, full, options)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.SelectedModuleIDs, ",") != newID || strings.Join(result.ProjectionModuleIDs, ",") != newID {
		t.Fatalf("factory scope broadened: selected=%v projections=%v", result.SelectedModuleIDs, result.ProjectionModuleIDs)
	}
	assertModuleRecordsPreserved(t, before, backend.snapshot)
	assertAcquisitionRequestGenerations(t, backend.snapshot, newID, 1)
}

func newScopedDiscoveryServer(t *testing.T, moduleID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.URL.Path, "terraform-azurerm-"+moduleID+"/") {
			t.Errorf("out-of-scope source request: %s", request.URL.Path)
			response.WriteHeader(http.StatusForbidden)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/releases") {
			_, _ = response.Write([]byte(`[{"tag_name":"v1.0.0","html_url":"https://example.invalid/v1.0.0","published_at":"2026-01-01T00:00:00Z"}]`))
			return
		}
		_, _ = fmt.Fprintf(response, `{"object":{"type":"commit","sha":%q}}`, strings.Repeat("a", 40))
	}))
}

func TestRestrictedDiscoveryDoesNotWriteUnselectedRetirement(t *testing.T) {
	for _, mode := range []string{"manual", "factory"} {
		t.Run(mode, func(t *testing.T) { assertRestrictedRetirement(t, mode) })
	}
}

func assertRestrictedRetirement(t *testing.T, mode string) {
	t.Helper()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, options); err != nil {
		t.Fatal(err)
	}
	retiringID := "avm-res-resources-storage"
	path := projectionIntentPath + retiringID + ".json"
	before := append([]byte(nil), backend.snapshot.Records[path].Data...)
	options.NewModulesOnly = mode == "factory"
	if mode == "manual" {
		options.SelectedModuleIDs = []string{"avm-res-resources-resourcegroup"}
	}
	for observation := 1; observation <= 2; observation++ {
		candidate = removeDiscoveryModule(candidate, retiringID, now.Add(time.Duration(observation*2)*time.Minute))
		options.AcceptedAt = candidate.FetchedAt.Add(time.Minute)
		if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, options); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(before, backend.snapshot.Records[path].Data) {
		t.Fatal("catalog retirement wrote an out-of-scope module intent")
	}
}

func assertModuleRecordsPreserved(t *testing.T, before, after state.Snapshot) {
	t.Helper()
	for path, record := range before.Records {
		if record.Kind == "source_pins" || record.Kind == "request" || record.Kind == "projection_intent" {
			if !bytes.Equal(record.Data, after.Records[path].Data) {
				t.Errorf("out-of-scope module record changed: %s", path)
			}
		}
	}
}

func TestPersistUnchangedNightlyPinsDoNotEnqueueNewAcquisitions(t *testing.T) {
	startedAt := time.Date(2026, 10, 3, 4, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(startedAt)
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	backend := newDiscoveryMemoryBackend()
	options := Options{AcceptedAt: startedAt.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}}
	first, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	firstRequestCount := countRecordsWithKind(backend.snapshot, "request")
	if firstRequestCount != len(candidate.Modules) || len(first.ProjectionModuleIDs) != len(candidate.Modules) {
		t.Fatalf("initial discovery requests=%d projections=%v, want one request and projection per module", firstRequestCount, first.ProjectionModuleIDs)
	}
	candidate.FetchedAt = startedAt.Add(2 * time.Minute)
	options.AcceptedAt = startedAt.Add(3 * time.Minute)
	second, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Delta.NoChange || second.ProjectionModuleIDs != nil || countRecordsWithKind(backend.snapshot, "request") != firstRequestCount {
		t.Fatalf("unchanged nightly discovery added work: delta=%#v projectionIDs=%v requestCount=%d", second.Delta, second.ProjectionModuleIDs, countRecordsWithKind(backend.snapshot, "request"))
	}
}

func TestPersistSelectedSourceChangePreservesOtherModules(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	backend := newDiscoveryMemoryBackend()
	oldServer := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer oldServer.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: oldServer.URL}, backend, candidate, options); err != nil {
		t.Fatal(err)
	}
	moduleID := candidate.Modules[0].ID
	assertSelectedUnchangedDiscovery(t, backend, candidate, source.Resolver{APIBase: oldServer.URL}, options)
	unselected := make(map[string][]byte)
	for path, record := range backend.snapshot.Records {
		if record.Kind != "source_pins" && record.Kind != "request" && record.Kind != "projection_intent" {
			continue
		}
		var value map[string]any
		if err := json.Unmarshal(record.Data, &value); err != nil {
			t.Fatal(err)
		}
		if value["module_id"] != moduleID {
			unselected[path] = append([]byte(nil), record.Data...)
		}
	}
	newServer := newVersionServer(t, "v2.0.0", strings.Repeat("b", 40), "v1.0.0", strings.Repeat("a", 40))
	defer newServer.Close()
	candidate.FetchedAt = now.Add(2 * time.Minute)
	options.AcceptedAt = now.Add(3 * time.Minute)
	options.SelectedModuleIDs = []string{moduleID}
	result, err := Persist(context.Background(), source.Resolver{APIBase: newServer.URL}, backend, candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted.Modules) != len(candidate.Modules) || strings.Join(result.ProjectionModuleIDs, ",") != moduleID {
		t.Fatalf("selected source change altered catalog or projection scope: %+v", result)
	}
	assertAcquisitionRequestGenerations(t, backend.snapshot, moduleID, 2)
	for path, data := range unselected {
		if !bytes.Equal(data, backend.snapshot.Records[path].Data) {
			t.Fatalf("selected source change mutated unrelated record %s", path)
		}
	}
	commits := backend.commitCount
	options.ForceModuleIDs = []string{candidate.Modules[1].ID}
	if _, err = Persist(context.Background(), source.Resolver{APIBase: newServer.URL}, backend, candidate, options); err == nil || backend.commitCount != commits {
		t.Fatal("force outside selection must fail before committing")
	}
}

func assertSelectedUnchangedDiscovery(t *testing.T, backend *memoryBackend, candidate catalog.Snapshot, resolver source.Resolver, options Options) {
	t.Helper()
	moduleID := candidate.Modules[0].ID
	options.SelectedModuleIDs = []string{moduleID}
	options.AcceptedAt = options.AcceptedAt.Add(time.Second)
	candidate.FetchedAt = candidate.FetchedAt.Add(time.Second)
	ordinary, err := Persist(context.Background(), resolver, backend, candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if !ordinary.Delta.NoChange || len(ordinary.Accepted.Modules) != len(candidate.Modules) || len(ordinary.ProjectionModuleIDs) != 0 {
		t.Fatalf("ordinary targeted discovery changed unchanged work or catalog membership: %#v", ordinary)
	}
	assertAcquisitionRequestGenerations(t, backend.snapshot, moduleID, 1)
}

func acceptDiscoveryTestFacts(t *testing.T, backend *memoryBackend, moduleID string, now time.Time) queue.Request {
	t.Helper()
	requests := moduleRequests(backend.snapshot, moduleID)
	if len(requests) != 1 {
		t.Fatalf("initial module requests=%d, want one acquisition", len(requests))
	}
	const owner = "discovery-test-worker"
	claimed, err := queue.ClaimModuleRequest(requests, requests[0].ID, owner, now, now.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	running := claimed[0]
	completion := queue.SealedCompletion{
		ID: "completion-" + strings.Repeat("e", 64), RequestID: running.ID,
		InputFingerprint: running.Fingerprint, DesiredGeneration: running.Work.DesiredGeneration,
		Fence: running.Work.Fence, ArtifactDigest: strings.Repeat("f", 64),
		ValidationID: "module-evidence-validator.v1", SealedAt: now.Add(time.Minute),
	}
	sealed, err := queue.SealCompletion(running, owner, running.Work.Fence, completion)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := queue.AcceptFacts(sealed, completion.ID, "facts-"+strings.Repeat("d", 64), completion.SealedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(accepted)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records["state/requests/"+accepted.ID+".json"] = state.Record{Kind: "request", SchemaVersion: 1, Data: data}
	return accepted
}

func assertAcquisitionRequestGenerations(t *testing.T, snapshot state.Snapshot, moduleID string, count int) {
	t.Helper()
	generations := make([]uint64, 0, count)
	for _, record := range snapshot.Records {
		if record.Kind != "request" {
			continue
		}
		var request queue.Request
		if err := json.Unmarshal(record.Data, &request); err != nil {
			t.Fatal(err)
		}
		if request.ModuleID == moduleID && request.Work != nil && request.Work.Phase == queue.PhaseAcquireSource {
			generations = append(generations, request.Work.DesiredGeneration)
		}
	}
	if len(generations) != count {
		t.Fatalf("acquisition requests for %s = %d, want %d (generations %v)", moduleID, len(generations), count, generations)
	}
	sort.Slice(generations, func(left, right int) bool { return generations[left] < generations[right] })
	for index := range generations {
		if index > 0 && generations[index] <= generations[index-1] {
			t.Fatalf("acquisition generations are not strictly increasing: %v", generations)
		}
	}
}

func assertReleaseLessCatalogTransaction(t *testing.T, backend *memoryBackend, result Result, moduleCount int) {
	t.Helper()
	if result.Head != "head-1" || backend.commitCount != 1 {
		t.Fatalf("discovery transaction head=%q commits=%d, want one atomic commit", result.Head, backend.commitCount)
	}
	if _, exists := backend.snapshot.Records["state/discovery/catalog.json"]; !exists {
		t.Fatal("accepted discovery state was not persisted")
	}
	if countRecordsWithKind(backend.snapshot, "discovery_observation") != 1 {
		t.Fatal("full catalog observation was not persisted")
	}
	if countRecordsWithKind(backend.snapshot, "export") != 2 || result.ProposalID == "" {
		t.Fatal("catalog proposal was not queued without analysis output")
	}
	if countRecordsWithKind(backend.snapshot, "projection_intent") != moduleCount {
		t.Fatalf("blocked intents = %d, want %d", countRecordsWithKind(backend.snapshot, "projection_intent"), moduleCount)
	}
	for _, record := range backend.snapshot.Records {
		if record.Kind != "projection_intent" {
			continue
		}
		var intent ProjectionIntent
		if err := json.Unmarshal(record.Data, &intent); err != nil {
			t.Fatal(err)
		}
		if intent.Status != IntentBlocked {
			t.Fatalf("release-less module intent status = %q, want blocked", intent.Status)
		}
	}
}

func TestPersistNewOverallPinPreservesOlderTrackWork(t *testing.T) {
	start := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	candidateV4 := discoveryTestSnapshot(start)
	commitV4 := strings.Repeat("a", 40)
	serverV4 := newVersionServer(t, "v4.0.0", commitV4, "", "")
	backend := newDiscoveryMemoryBackend()
	options := Options{AcceptedAt: start.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}}
	first, err := Persist(context.Background(), source.Resolver{APIBase: serverV4.URL}, backend, candidateV4, options)
	serverV4.Close()
	if err != nil {
		t.Fatal(err)
	}
	moduleID := candidateV4.Modules[0].ID
	seededHead, assessmentID, pinPath := seedV4TrackAssessment(t, backend, first.Head, moduleID, commitV4, start)

	commitV5 := strings.Repeat("b", 40)
	serverV5 := newVersionServer(t, "v5.0.0", commitV5, "v4.0.0", commitV4)
	defer serverV5.Close()
	candidateV5 := discoveryTestSnapshot(start.Add(3 * time.Minute))
	options.AcceptedAt = start.Add(4 * time.Minute)
	second, err := Persist(context.Background(), source.Resolver{APIBase: serverV5.URL}, backend, candidateV5, options)
	if err != nil {
		t.Fatal(err)
	}
	assertV5PreservesV4(t, backend, second, v5PreservationExpectation{
		seededHead: seededHead, assessmentID: assessmentID, pinPath: pinPath,
		commitV4: commitV4, commitV5: commitV5,
	})
}

func seedV4TrackAssessment(t *testing.T, backend *memoryBackend, head, moduleID, commitV4 string, start time.Time) (string, string, string) {
	t.Helper()
	pinPath := "state/discovery/source-pins/" + moduleID + ".json"
	var history PinHistory
	if err := json.Unmarshal(backend.snapshot.Records[pinPath].Data, &history); err != nil {
		t.Fatal(err)
	}
	history.TrackCandidates["azurerm-4"] = *history.OverallLatest
	trackEvent, err := newStateEvent(pinPath, "source_pins", history)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := queue.NewAssessmentRequest(queue.AssessmentInput{
		ModuleID: moduleID, SourceSHA: commitV4, ContextID: "azurerm-4", Track: "azurerm-4",
		DesiredGeneration: 1, FactsID: "facts-" + strings.Repeat("d", 64),
		DefinitionIDs: []string{"definition-" + strings.Repeat("e", 64)},
	}, start.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	assessmentEvent, err := newStateEvent("state/requests/"+assessment.ID+".json", "request", assessment)
	if err != nil {
		t.Fatal(err)
	}
	seededHead, _, err := state.ApplyEventsAtHead(context.Background(), backend, head, []state.Event{trackEvent, assessmentEvent})
	if err != nil {
		t.Fatal(err)
	}
	return seededHead, assessment.ID, pinPath
}

type v5PreservationExpectation struct {
	seededHead   string
	assessmentID string
	pinPath      string
	commitV4     string
	commitV5     string
}

func assertV5PreservesV4(t *testing.T, backend *memoryBackend, second Result, expected v5PreservationExpectation) {
	t.Helper()
	if second.Head == expected.seededHead || second.ProposalID == "" || !second.Delta.NoChange {
		t.Fatalf("pin-only update = %#v, seeded head %s", second, expected.seededHead)
	}

	var updatedHistory PinHistory
	if err := json.Unmarshal(backend.snapshot.Records[expected.pinPath].Data, &updatedHistory); err != nil {
		t.Fatal(err)
	}
	if updatedHistory.OverallLatest == nil || updatedHistory.OverallLatest.Version != "v5.0.0" || len(updatedHistory.ReleaseHistory) != 2 {
		t.Fatalf("overall latest/release history = %#v", updatedHistory)
	}
	if updatedHistory.TrackCandidates["azurerm-4"].Commit != expected.commitV4 {
		t.Fatalf("v4 track candidate was overwritten: %#v", updatedHistory.TrackCandidates)
	}
	if _, exists := backend.snapshot.Records["state/requests/"+expected.assessmentID+".json"]; !exists {
		t.Fatal("pending v4 assessment was discarded by the v5 source update")
	}
	requestBySource := map[string]queue.Request{}
	for _, record := range backend.snapshot.Records {
		if record.Kind != "request" {
			continue
		}
		var request queue.Request
		if err := json.Unmarshal(record.Data, &request); err != nil {
			t.Fatal(err)
		}
		if request.Work != nil && request.Work.Phase == queue.PhaseAcquireSource {
			requestBySource[request.SourceSHA] = request
		}
	}
	if requestBySource[expected.commitV4].Status != queue.StatusQueued || requestBySource[expected.commitV5].Status != queue.StatusQueued {
		t.Fatalf("v4/v5 acquisition requests = %#v", requestBySource)
	}
}

func TestPersistMovedTagCreatesReviewIntentAndPreservesAcceptedPin(t *testing.T) {
	start := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(start)
	acceptedCommit := strings.Repeat("a", 40)
	firstServer := newVersionServer(t, "v4.0.0", acceptedCommit, "", "")
	backend := newDiscoveryMemoryBackend()
	options := Options{AcceptedAt: start.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}}
	first, err := Persist(context.Background(), source.Resolver{APIBase: firstServer.URL}, backend, candidate, options)
	firstServer.Close()
	if err != nil {
		t.Fatal(err)
	}

	movedCommit := strings.Repeat("b", 40)
	movedServer := newVersionServer(t, "v4.0.0", movedCommit, "", "")
	defer movedServer.Close()
	candidate.FetchedAt = start.Add(2 * time.Minute)
	options.AcceptedAt = start.Add(3 * time.Minute)
	second, err := Persist(context.Background(), source.Resolver{APIBase: movedServer.URL}, backend, candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Delta.NoChange || second.ProposalID != "" || second.Head == first.Head {
		t.Fatalf("moved-tag transaction = %#v, first head %s", second, first.Head)
	}

	moduleID := candidate.Modules[0].ID
	var history PinHistory
	if err = json.Unmarshal(backend.snapshot.Records["state/discovery/source-pins/"+moduleID+".json"].Data, &history); err != nil {
		t.Fatal(err)
	}
	if history.OverallLatest == nil || history.OverallLatest.Commit != acceptedCommit || len(history.ReleaseHistory) != 1 {
		t.Fatalf("moved tag overwrote accepted pin history: %#v", history)
	}
	var intent ProjectionIntent
	if err = json.Unmarshal(backend.snapshot.Records["state/projection/intents/"+moduleID+".json"].Data, &intent); err != nil {
		t.Fatal(err)
	}
	if intent.Status != IntentNeedsReview || !strings.Contains(intent.Reason, "moved accepted release tag") {
		t.Fatalf("moved-tag intent = %#v", intent)
	}
}

func TestPersistRetiresOnlyAfterTwoSuccessfulObservations(t *testing.T) {
	start := time.Date(2026, 10, 2, 16, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(start)
	retiringID := "avm-res-resources-storage"
	server := newVersionServer(t, "v1.0.0", strings.Repeat("c", 40), "", "")
	defer server.Close()
	backend := newDiscoveryMemoryBackend()
	options := Options{AcceptedAt: start.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, options); err != nil {
		t.Fatal(err)
	}

	withoutRetiring := removeDiscoveryModule(candidate, retiringID, start.Add(2*time.Minute))
	options.AcceptedAt = start.Add(3 * time.Minute)
	second, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, withoutRetiring, options)
	if err != nil {
		t.Fatal(err)
	}
	var pendingIntent ProjectionIntent
	if err = json.Unmarshal(backend.snapshot.Records["state/projection/intents/"+retiringID+".json"].Data, &pendingIntent); err != nil {
		t.Fatal(err)
	}
	if pendingIntent.Status == IntentRetired || !containsModule(second.Delta.PendingRemoval, retiringID) {
		t.Fatalf("first omission retired module: intent=%#v delta=%#v", pendingIntent, second.Delta)
	}

	withoutRetiring.FetchedAt = start.Add(4 * time.Minute)
	options.AcceptedAt = start.Add(5 * time.Minute)
	third, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, withoutRetiring, options)
	if err != nil {
		t.Fatal(err)
	}
	var retiredIntent ProjectionIntent
	if err = json.Unmarshal(backend.snapshot.Records["state/projection/intents/"+retiringID+".json"].Data, &retiredIntent); err != nil {
		t.Fatal(err)
	}
	if retiredIntent.Status != IntentRetired || !containsModule(third.Delta.Retired, retiringID) {
		t.Fatalf("second omission did not persist retirement: intent=%#v delta=%#v", retiredIntent, third.Delta)
	}
}

func newVersionServer(t *testing.T, currentVersion, currentCommit, previousVersion, previousCommit string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/releases") {
			_, _ = fmt.Fprintf(response, `[{"tag_name":%q,"html_url":"https://example.invalid/%s","published_at":"2026-01-01T00:00:00Z"}]`, currentVersion, currentVersion)
			return
		}
		if strings.Contains(request.URL.Path, "/git/ref/tags/") {
			tag := request.URL.Path[strings.LastIndex(request.URL.Path, "/git/ref/tags/")+len("/git/ref/tags/"):]
			if tag == currentVersion {
				_, _ = fmt.Fprintf(response, `{"object":{"type":"commit","sha":%q}}`, currentCommit)
				return
			}
			if tag == previousVersion && previousVersion != "" {
				_, _ = fmt.Fprintf(response, `{"object":{"type":"commit","sha":%q}}`, previousCommit)
				return
			}
			http.NotFound(response, request)
			return
		}
		t.Errorf("unexpected request %s", request.URL.Path)
		http.NotFound(response, request)
	}))
}

func discoveryTestSnapshot(fetchedAt time.Time) catalog.Snapshot {
	modules := []catalog.Module{
		{
			ID: "avm-res-resources-resourcegroup", Type: catalog.Resource, Status: "Available",
			DisplayName: "Resource Group", Description: "AVM resource module",
			RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-resources-resourcegroup",
			RegistryURL:   "https://registry.terraform.io/modules/Azure/avm-res-resources-resourcegroup",
		},
		{ID: "avm-res-resources-storage", Type: catalog.Resource, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-resources-storage", RegistryURL: "https://registry.terraform.io/modules/Azure/avm-res-resources-storage", DisplayName: "Storage", Description: "Storage module"},
		{ID: "avm-ptn-test", Type: catalog.Pattern, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-avm-ptn-test", RegistryURL: "https://registry.terraform.io/modules/Azure/avm-ptn-test", DisplayName: "Pattern", Description: "Pattern module"},
		{ID: "avm-utl-test", Type: catalog.Utility, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-avm-utl-test", RegistryURL: "https://registry.terraform.io/modules/Azure/avm-utl-test", DisplayName: "Utility", Description: "Utility module"},
	}
	feeds := make([]catalog.FeedSnapshot, 0, len(catalog.OfficialSources))
	for _, sourceInfo := range catalog.OfficialSources {
		count := 0
		for _, module := range modules {
			if module.Type == sourceInfo.Kind {
				count++
			}
		}
		digest := strings.Repeat("a", 64)
		if sourceInfo.Kind == catalog.Pattern {
			digest = strings.Repeat("b", 64)
		} else if sourceInfo.Kind == catalog.Utility {
			digest = strings.Repeat("c", 64)
		}
		feeds = append(feeds, catalog.FeedSnapshot{Kind: sourceInfo.Kind, URL: sourceInfo.URL, SHA256: digest, ModuleCount: count})
	}
	return catalog.Snapshot{SchemaVersion: catalog.SnapshotSchemaVersion, FetchedAt: fetchedAt, Feeds: feeds, Modules: modules}
}

func removeDiscoveryModule(snapshot catalog.Snapshot, moduleID string, fetchedAt time.Time) catalog.Snapshot {
	updated := snapshot
	updated.FetchedAt = fetchedAt
	updated.Modules = make([]catalog.Module, 0, len(snapshot.Modules)-1)
	counts := make(map[catalog.Kind]int)
	for _, module := range snapshot.Modules {
		if module.ID == moduleID {
			continue
		}
		updated.Modules = append(updated.Modules, module)
		counts[module.Type]++
	}
	for index := range updated.Feeds {
		updated.Feeds[index].ModuleCount = counts[updated.Feeds[index].Kind]
		if updated.Feeds[index].Kind == catalog.Resource {
			updated.Feeds[index].SHA256 = strings.Repeat("d", 64)
		}
	}
	return updated
}

func countRecordsWithKind(snapshot state.Snapshot, kind string) int {
	count := 0
	for _, record := range snapshot.Records {
		if record.Kind == kind {
			count++
		}
	}
	return count
}

type memoryBackend struct {
	mu          sync.Mutex
	head        string
	snapshot    state.Snapshot
	commitCount int
}

func newDiscoveryMemoryBackend() *memoryBackend {
	return &memoryBackend{head: "head-0", snapshot: state.NewSnapshot()}
}

func (backend *memoryBackend) Read(ctx context.Context) (string, state.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return "", state.Snapshot{}, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.head, cloneSnapshot(backend.snapshot), nil
}

func (backend *memoryBackend) Commit(ctx context.Context, expectedHead string, snapshot state.Snapshot) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.head != expectedHead {
		return "", state.ErrHeadMoved
	}
	backend.commitCount++
	backend.head = fmt.Sprintf("head-%d", backend.commitCount)
	backend.snapshot = cloneSnapshot(snapshot)
	return backend.head, nil
}

func cloneSnapshot(snapshot state.Snapshot) state.Snapshot {
	encoded, _ := json.Marshal(snapshot)
	var cloned state.Snapshot
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}
