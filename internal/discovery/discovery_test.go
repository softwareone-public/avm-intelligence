package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/github"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
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

	result, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, Options{
		AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer(),
	})
	if err == nil {
		t.Fatal("first unresolved module did not stop catalog discovery")
	}
	assertReleaseLessCatalogTransaction(t, backend, result, len(candidate.Modules))
}

func TestPersistSourceFailureRecordsRetryableModuleWorkBeforeRequest(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	moduleID := "avm-res-resources-resourcegroup"
	backend := newDiscoveryMemoryBackend()
	var sawClaim bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		workPath, pathErr := identity.StateDiscoveryWorkPath(moduleID)
		if pathErr != nil {
			t.Error(pathErr)
		} else if record, exists := backend.snapshot.Records[workPath]; exists {
			var work state.DiscoveryWork
			if json.Unmarshal(record.Data, &work) == nil && work.State == state.DiscoveryWorkReady && work.OwnerRunID == discoveryTestProducer().RunID && work.LeaseExpiresAt.After(time.Now()) {
				sawClaim = true
			}
		}
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	options := Options{
		AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1",
		Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{moduleID}, Producer: discoveryTestProducer(),
	}
	_, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), discoveryTestSnapshot(now), options)
	if err == nil {
		t.Fatal("source resolution failure did not stop discovery")
	}
	if !sawClaim {
		t.Fatal("source resolver ran before the fenced module work claim was durable")
	}
	workPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	workRecord, exists := backend.snapshot.Records[workPath]
	if !exists || workRecord.Kind != "discovery_work" {
		t.Fatalf("source failure did not persist module work at %s", workPath)
	}
	var work state.DiscoveryWork
	if err = json.Unmarshal(workRecord.Data, &work); err != nil {
		t.Fatal(err)
	}
	if work.State != state.DiscoveryWorkRetryable || work.FailureCategory != "source_unavailable" || work.RetryAt.IsZero() {
		t.Fatalf("source failure work = %+v, want retryable source_unavailable with a retry time", work)
	}
	if requests := moduleRequests(backend.snapshot, moduleID); len(requests) != 0 {
		t.Fatalf("source failure created %d acquisition requests before a source pin existed", len(requests))
	}
}

func TestStableModuleOperationIdentityDistinguishesForceGeneration(t *testing.T) {
	moduleID := "avm-res-resources-resourcegroup"
	accepted, _, err := catalog.AcceptSnapshot(nil, discoveryTestSnapshot(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)), time.Date(2026, 10, 9, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	var record catalog.ModuleRecord
	for _, candidate := range accepted.Modules {
		if candidate.Module.ID == moduleID {
			record = candidate
		}
	}
	options := Options{ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}}
	observationID := "discovery-observation-" + strings.Repeat("a", 64)
	ordinary, ordinaryFingerprint, err := stableModuleOperationIdentity(state.NewSnapshot(), moduleID, observationID, record, accepted.Generation, options, false)
	if err != nil {
		t.Fatal(err)
	}
	forced, forcedFingerprint, err := stableModuleOperationIdentity(state.NewSnapshot(), moduleID, observationID, record, accepted.Generation, options, true)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary == forced || ordinaryFingerprint != forcedFingerprint {
		t.Fatal("explicit force generation did not distinguish the stable module operation")
	}
}

func discoveryTestProducer() projection.WorkflowProvenance {
	return projection.WorkflowProvenance{WorkflowID: "factory-discovery-test", Commit: strings.Repeat("a", 40), RunID: "discovery-test-run", RunAttempt: 1}
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
		_, err := Persist(context.Background(), source.Resolver{APIBase: server.URL, Token: "private-token-sentinel"}, testTransactionCoordinator(backend), discoveryTestSnapshot(now), Options{
			AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer(),
		})
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
	backend := newDiscoveryMemoryBackend()
	_, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), discoveryTestSnapshot(now), Options{
		AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer(),
	})
	if err == nil {
		t.Fatal("unresolved source module did not stop discovery")
	}
	if strings.Contains(output.String(), "private-body-sentinel") || !strings.Contains(output.String(), "source resolution unavailable") {
		t.Fatal("source failure logs missing or contain raw diagnostics")
	}
}

func assertDiscoveryProgress(t *testing.T, output string) {
	t.Helper()
	for _, expected := range []string{"module_id=avm-res-", "phase=source_resolution", "position=", "source resolution unavailable", "discovery module prepared"} {
		if expected == "module_id=avm-res-" {
			if !strings.Contains(output, expected) && !strings.Contains(output, "module_id=avm-ptn-") {
				t.Errorf("missing discovery progress %q", expected)
			}
			continue
		}
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
	options := Options{AcceptedAt: startedAt.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer()}
	first, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options)
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
	forced, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, Options{
		AcceptedAt: options.AcceptedAt, ExtractorContract: options.ExtractorContract,
		Capabilities: options.Capabilities, ForceModuleIDs: []string{moduleID}, Producer: discoveryTestProducer(),
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
	result, err := Cycle(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options, func(ctx context.Context, module ModuleCycle) (string, error) {
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
	_, err := Cycle(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options, func(ctx context.Context, module ModuleCycle) (string, error) {
		return module.Head, fmt.Errorf("fixture delivery failure")
	})
	if err == nil || countRecordsWithKind(backend.snapshot, "request") != 1 {
		t.Fatal("failure did not retain exactly the first committed module")
	}
	candidate.FetchedAt = now.Add(2 * time.Minute)
	options.AcceptedAt = now.Add(3 * time.Minute)
	var completed []string
	result, err := Cycle(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options, func(ctx context.Context, module ModuleCycle) (string, error) {
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

func TestPersistReceiptReplayDoesNotResolveSourceOrChangeRequestAndPin(t *testing.T) {
	now := time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	moduleID := "avm-res-resources-resourcegroup"
	var sourceReads int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		sourceReads++
		if strings.HasSuffix(request.URL.Path, "/releases") {
			_, _ = response.Write([]byte(`[{"tag_name":"v1.0.0","html_url":"https://example.invalid/v1.0.0","published_at":"2026-01-01T00:00:00Z"}]`))
			return
		}
		_, _ = fmt.Fprintf(response, `{"object":{"type":"commit","sha":%q}}`, strings.Repeat("a", 40))
	}))
	defer server.Close()
	backend := newDiscoveryMemoryBackend()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{moduleID}, Producer: discoveryTestProducer()}
	firstResult, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstResult.CompletedModuleIDs) != 1 {
		t.Fatalf("first module commit was not retained in result: %v", firstResult.CompletedModuleIDs)
	}
	operationID, desiredFingerprint, receiptResult := moduleOperationReceiptForModule(t, backend.snapshot, moduleID)
	assertHandoffEventsInReceipt(t, backend.snapshot, moduleID, operationID, receiptResult.Events)
	advanceModuleRequestAndPin(t, backend, moduleID, now.Add(4*time.Minute))
	advancedSnapshot := cloneSnapshot(backend.snapshot)
	replayed, found, err := replayModuleOperation(context.Background(), testTransactionCoordinator(backend), advancedSnapshot, moduleID, operationID, desiredFingerprint)
	if err != nil || !found || replayed.OperationID != operationID {
		t.Fatalf("historical receipt replay after request/pin advance = %+v, found=%t, err=%v", replayed, found, err)
	}
	changedEvents := append([]state.Event(nil), receiptResult.Events...)
	data := changedEvents[0].Record.Data
	changedEvents[0].Record.Data = append(append([]byte(nil), data[:len(data)-1]...), []byte(`,"tampered":true}`)...)
	operationRunner := testTransactionCoordinator(backend).(operationTransactionRunner)
	_, err = operationRunner.RunOperationTransaction(context.Background(), moduleID, operationID, changedEvents, func(state.Snapshot) (state.TransactionDecision, error) {
		t.Fatal("changed same-ID replay reran discovery decision")
		return state.TransactionDecision{}, nil
	}, func(state.Snapshot) (state.TransactionResolution, error) {
		return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
	})
	if !errors.Is(err, state.ErrTransactionIdentityConflict) {
		t.Fatalf("changed same-ID proposal error = %v, want identity conflict", err)
	}
	requestRecords, pinRecords := moduleRecordBytes(backend.snapshot, moduleID)
	requestCount := countRecordsWithKind(backend.snapshot, "request")
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options); err != nil {
		t.Fatal(err)
	}
	requestsAfter, pinsAfter := moduleRecordBytes(backend.snapshot, moduleID)
	if requestCount != countRecordsWithKind(backend.snapshot, "request") || !bytes.Equal(requestRecords, requestsAfter) || !bytes.Equal(pinRecords, pinsAfter) || sourceReads != 3 {
		t.Fatalf("receipt replay changed request/pin or re-read source: reads=%d", sourceReads)
	}
}

func moduleOperationReceiptForModule(t *testing.T, snapshot state.Snapshot, moduleID string) (string, string, moduleOperationReceiptResult) {
	t.Helper()
	workPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	var work state.DiscoveryWork
	if err = json.Unmarshal(snapshot.Records[workPath].Data, &work); err != nil {
		t.Fatal(err)
	}
	receiptPath, err := identity.StateOperationReceiptPath(moduleID, work.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var receipt state.OperationReceipt
	if err = json.Unmarshal(snapshot.Records[receiptPath].Data, &receipt); err != nil {
		t.Fatal(err)
	}
	var result moduleOperationReceiptResult
	if err = json.Unmarshal(receipt.Result, &result); err != nil {
		t.Fatal(err)
	}
	return work.OperationID, work.DesiredFingerprint, result
}

func assertHandoffEventsInReceipt(t *testing.T, snapshot state.Snapshot, moduleID, operationID string, events []state.Event) {
	t.Helper()
	var hasIntent, hasHandoff bool
	for _, event := range events {
		switch event.Record.Kind {
		case "projection_intent":
			hasIntent = true
		case "stage_handoff":
			hasHandoff = true
		}
	}
	if !hasIntent || !hasHandoff {
		t.Fatalf("operation %s write set missing handoff intent/events: intent=%t handoff=%t", operationID, hasIntent, hasHandoff)
	}
	receiptPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		t.Fatal(err)
	}
	var receipt state.OperationReceipt
	if err = json.Unmarshal(snapshot.Records[receiptPath].Data, &receipt); err != nil {
		t.Fatal(err)
	}
	if err = validateReceiptWriteSet(receipt, events); err != nil {
		t.Fatalf("handoff records are not part of the committed receipt write set: %v", err)
	}
	workPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := snapshot.Records[workPath]; !exists {
		t.Fatal("discovery work event missing from the same module transaction")
	}
}

func advanceModuleRequestAndPin(t *testing.T, backend *memoryBackend, moduleID string, at time.Time) {
	t.Helper()
	var requestPath string
	var request queue.Request
	for path, record := range backend.snapshot.Records {
		if record.Kind == "request" {
			var candidate queue.Request
			if json.Unmarshal(record.Data, &candidate) == nil && candidate.ModuleID == moduleID {
				requestPath, request = path, candidate
			}
		}
	}
	if requestPath == "" {
		t.Fatal("module request missing before advance")
	}
	request.UpdatedAt = at.UTC()
	requestEvent, err := newStateEvent(requestPath, "request", request)
	if err != nil {
		t.Fatal(err)
	}
	pinPath := discoverySourcePinPath + moduleID + ".json"
	var history PinHistory
	if err = json.Unmarshal(backend.snapshot.Records[pinPath].Data, &history); err != nil {
		t.Fatal(err)
	}
	history.OverallLatest.ResolvedAt = at.UTC()
	for index := range history.ReleaseHistory {
		if history.ReleaseHistory[index].Commit == history.OverallLatest.Commit {
			history.ReleaseHistory[index].ResolvedAt = at.UTC()
		}
	}
	pinEvent, err := newStateEvent(pinPath, "source_pins", history)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = state.ApplyEventsAtHead(context.Background(), backend, backend.head, []state.Event{requestEvent, pinEvent}); err != nil {
		t.Fatalf("advance current request/pin records: %v", err)
	}
}

func TestPersistModuleTransactionConflictLeavesRequestAndPinIdentityUnchanged(t *testing.T) {
	now := time.Date(2026, 10, 9, 15, 45, 0, 0, time.UTC)
	moduleID := "avm-res-resources-resourcegroup"
	backend := newDiscoveryMemoryBackend()
	server := newScopedDiscoveryServer(t, moduleID)
	defer server.Close()
	options := Options{
		AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1",
		Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{moduleID}, Producer: discoveryTestProducer(),
	}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), discoveryTestSnapshot(now), options); err != nil {
		t.Fatal(err)
	}
	requestBefore, pinBefore := moduleRecordBytes(backend.snapshot, moduleID)
	options.AcceptedAt = now.Add(3 * time.Minute)
	options.ForceModuleIDs = []string{moduleID}
	_, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, rejectingOperationCoordinator{TransactionCoordinator: testTransactionCoordinator(backend)}, discoveryTestSnapshot(now.Add(2*time.Minute)), options)
	if err == nil || !errors.Is(err, state.ErrTransactionIdentityConflict) {
		t.Fatalf("injected module transaction conflict = %v", err)
	}
	requestAfter, pinAfter := moduleRecordBytes(backend.snapshot, moduleID)
	if !bytes.Equal(requestBefore, requestAfter) || !bytes.Equal(pinBefore, pinAfter) {
		t.Fatal("module transaction conflict changed an existing acquisition request or source pin")
	}
}

func TestPersistReplayOfRetryableModuleDoesNotRepeatSourceLookup(t *testing.T) {
	now := time.Date(2026, 10, 9, 15, 15, 0, 0, time.UTC)
	moduleID := "avm-res-resources-resourcegroup"
	var sourceReads int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		sourceReads++
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	backend := newDiscoveryMemoryBackend()
	candidate := discoveryTestSnapshot(now)
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{moduleID}}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options); err == nil {
		t.Fatal("source failure did not stop discovery")
	}
	workPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	var failed state.DiscoveryWork
	if err = json.Unmarshal(backend.snapshot.Records[workPath].Data, &failed); err != nil {
		t.Fatal(err)
	}
	if failed.State != state.DiscoveryWorkRetryable || failed.OperationID == "" {
		t.Fatalf("failed operation was not durably retryable: %+v", failed)
	}
	options.AcceptedAt = now.Add(time.Minute)
	failed.RetryAt = time.Now().UTC().Add(-time.Second)
	failedRecord, recordErr := state.DiscoveryWorkRecord(failed)
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	if _, _, err = state.ApplyEventsAtHead(context.Background(), backend, backend.head, []state.Event{{ID: "test:retry-due", RecordPath: workPath, Record: failedRecord}}); err != nil {
		t.Fatal(err)
	}
	if _, err = Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options); err == nil {
		t.Fatal("second source failure did not stop discovery")
	} else if !strings.Contains(err.Error(), "stopped with durable state retryable") {
		t.Fatalf("retry failed before classified source resolution: %v", err)
	}
	var retried state.DiscoveryWork
	if err = json.Unmarshal(backend.snapshot.Records[workPath].Data, &retried); err != nil {
		t.Fatal(err)
	}
	if sourceReads != 2 || retried.OperationID != failed.OperationID || retried.DesiredFingerprint != failed.DesiredFingerprint || retried.OwnerGeneration <= failed.OwnerGeneration || countRecordsWithKind(backend.snapshot, "request") != 0 {
		t.Fatalf("retry changed logical operation or did not resolve once: reads=%d before=%+v after=%+v", sourceReads, failed, retried)
	}
}

func TestPersistFailureLeavesOnlyEarlierModulesClaimed(t *testing.T) {
	for _, failedPosition := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("position-%d", failedPosition), func(t *testing.T) {
			now := time.Now().UTC().Add(-time.Hour)
			candidate := discoveryTestSnapshot(now)
			requested := []string{"avm-res-resources-resourcegroup", "avm-res-resources-storage", "avm-utl-test", "avm-ptn-test"}
			accepted, _, err := catalog.AcceptSnapshot(nil, candidate, now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			selection, err := catalog.NewAcceptedSelection(accepted.Generation, requested, catalog.ManualExactScope)
			if err != nil {
				t.Fatal(err)
			}
			ordered := orderedSelectedModuleIDs(accepted, selection)
			failedModule := ordered[failedPosition-1]
			backend := newDiscoveryMemoryBackend()
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if strings.Contains(request.URL.Path, failedModule) {
					response.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if strings.HasSuffix(request.URL.Path, "/releases") {
					_, _ = response.Write([]byte(`[{"tag_name":"v1.0.0","html_url":"https://example.invalid/v1.0.0","published_at":"2026-01-01T00:00:00Z"}]`))
					return
				}
				_, _ = fmt.Fprintf(response, `{"object":{"type":"commit","sha":%q}}`, strings.Repeat("a", 40))
			}))
			defer server.Close()
			options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: requested, Producer: discoveryTestProducer()}
			result, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options)
			if err == nil {
				t.Fatal("first source failure did not stop selected module processing")
			}
			if got := len(result.CompletedModuleIDs); got != failedPosition-1 {
				t.Fatalf("completed prefix length=%d, want %d (%v)", got, failedPosition-1, result.CompletedModuleIDs)
			}
			for index, moduleID := range ordered {
				workPath, pathErr := identity.StateDiscoveryWorkPath(moduleID)
				if pathErr != nil {
					t.Fatal(pathErr)
				}
				_, claimed := backend.snapshot.Records[workPath]
				if (index < failedPosition) != claimed {
					t.Fatalf("module %s claim existence=%t, failed position=%d", moduleID, claimed, failedPosition)
				}
				if index < failedPosition-1 {
					continue
				}
				if index > failedPosition-1 {
					continue
				}
				var work state.DiscoveryWork
				if err = json.Unmarshal(backend.snapshot.Records[workPath].Data, &work); err != nil {
					t.Fatal(err)
				}
				if index == failedPosition-1 {
					if work.State != state.DiscoveryWorkRetryable {
						t.Fatalf("failed module work state=%s, want retryable", work.State)
					}
					continue
				}
				t.Fatalf("later module %s received work before earlier failure stopped discovery", moduleID)
			}
		})
	}
}

func TestDiscoveryClaimRejectsStaleOwnerGeneration(t *testing.T) {
	moduleID := "avm-res-resources-resourcegroup"
	now := time.Now().UTC().Add(-time.Hour)
	candidate := discoveryTestSnapshot(now)
	backend := newDiscoveryMemoryBackend()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{moduleID}, Producer: discoveryTestProducer()}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options); err == nil {
		t.Fatal("initial source failure did not establish retryable work")
	}
	workPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	var work state.DiscoveryWork
	if err = json.Unmarshal(backend.snapshot.Records[workPath].Data, &work); err != nil {
		t.Fatal(err)
	}
	work.OwnerGeneration++
	work.OwnerRunID = "successor-owner"
	work.LeaseExpiresAt = time.Now().UTC().Add(time.Hour)
	work.RetryAt = time.Time{}
	work.State, work.FailureCategory = state.DiscoveryWorkReady, "none"
	record, err := state.DiscoveryWorkRecord(work)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = state.ApplyEventsAtHead(context.Background(), backend, backend.head, []state.Event{{ID: "test:successor-claim", RecordPath: workPath, Record: record, FenceKey: moduleID, FenceToken: work.OwnerGeneration}}); err != nil {
		t.Fatal(err)
	}
	stale := work
	stale.OwnerGeneration--
	stale.OwnerRunID = "stale-owner"
	stale.LeaseExpiresAt = time.Now().UTC().Add(time.Hour)
	staleRecord, err := state.DiscoveryWorkRecord(stale)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = state.ApplyEventsAtHead(context.Background(), backend, backend.head, []state.Event{{ID: "test:stale-owner", RecordPath: workPath, Record: staleRecord, FenceKey: moduleID, FenceToken: stale.OwnerGeneration}})
	if !errors.Is(err, state.ErrStaleFence) {
		t.Fatalf("stale owner publication error=%v, want stale fence", err)
	}
}

func moduleRecordBytes(snapshot state.Snapshot, moduleID string) ([]byte, []byte) {
	var requestBytes, pinBytes []byte
	for _, record := range snapshot.Records {
		if record.Kind == "request" {
			var request queue.Request
			if json.Unmarshal(record.Data, &request) == nil && request.ModuleID == moduleID {
				requestBytes = append(requestBytes, record.Data...)
			}
		}
		if record.Kind == "source_pins" {
			var history PinHistory
			if json.Unmarshal(record.Data, &history) == nil && history.ModuleID == moduleID {
				pinBytes = append(pinBytes, record.Data...)
			}
		}
	}
	return requestBytes, pinBytes
}

func TestPersistRejectsCrossOwnerModuleWriteSet(t *testing.T) {
	moduleID := "avm-res-resources-resourcegroup"
	otherModuleID := "avm-res-resources-storage"
	foreignRequest, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: otherModuleID, SourceSHA: strings.Repeat("a", 40), DesiredGeneration: 1,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, time.Date(2026, 10, 9, 15, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	foreignEvent, err := newStateEvent("state/requests/"+foreignRequest.ID+".json", "request", foreignRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkOnlyModuleWrites(moduleID, []state.Event{foreignEvent}); err == nil {
		t.Fatal("cross-owner operation write set passed discovery ownership validation")
	}
}

func TestManualCyclePopulatesMigratedModuleLayoutWithoutDuplicateWork(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	backend := newDiscoveryMemoryBackend()
	if _, err := state.MigrateModuleLayout(ctx, backend); err != nil {
		t.Fatal(err)
	}
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	candidate := discoveryTestSnapshot(now)
	selected := []string{"avm-res-resources-resourcegroup", "avm-res-resources-storage"}
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: selected}
	for run := 0; run < 2; run++ {
		candidate.FetchedAt = now.Add(time.Duration(run) * time.Hour)
		options.AcceptedAt = candidate.FetchedAt.Add(time.Minute)
		result, err := Cycle(ctx, source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options, func(ctx context.Context, module ModuleCycle) (string, error) {
			owner, _ := identity.OperationalModuleRoot(module.ModuleID)
			if _, exists := module.Snapshot.Aggregates[owner]; !exists {
				t.Fatal("singleton module has no transaction metadata")
			}
			if run > 0 {
				if module.ProjectionRequired {
					t.Fatal("unchanged migrated cycle requested new lifecycle delivery")
				}
				return module.Head, nil
			}
			return acknowledgeCycleTestModule(t, ctx, backend, module, options.AcceptedAt.Add(time.Duration(module.Position)*time.Second)), nil
		})
		if err != nil || len(result.CompletedModuleIDs) != len(selected) {
			t.Fatalf("migrated manual cycle failed: %v", err)
		}
	}
	if countRecordsWithKind(backend.snapshot, "request") != len(selected) || len(backend.snapshot.Aggregates) != len(selected) {
		t.Fatal("migrated cycles duplicated work or expanded module scope")
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
	_, err = github.SaveProjectionIntents(ctx, testTransactionCoordinator(backend), []github.ProjectionIntentRecord{delivery})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := github.ClaimProjectionBatchForModules(ctx, testTransactionCoordinator(backend), github.ProjectionClaimOptions{RunID: "cycle-test", Now: now, Lease: time.Minute, Limit: 1}, []string{module.ModuleID})
	if err != nil || len(claim.Intents) != 1 {
		t.Fatalf("singleton claim failed: %+v %v", claim, err)
	}
	outcome, err := github.RecordProjectionOutcome(ctx, testTransactionCoordinator(backend), github.ProjectionOutcomeInput{
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
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}, Producer: discoveryTestProducer()}
	_, err := Cycle(context.Background(), source.Resolver{APIBase: firstServer.URL}, testTransactionCoordinator(backend), candidate, options, func(ctx context.Context, module ModuleCycle) (string, error) {
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
	result, err := Persist(context.Background(), source.Resolver{APIBase: nextServer.URL}, testTransactionCoordinator(backend), candidate, options)
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

func TestSkuFinderReceiptOperationReplaysLiveRequestAndPinFixture(t *testing.T) {
	fixture := newSkuFinderReceiptFixture(t)
	coordinator := state.DecisionCoordinator{Backend: fixture.backend}
	committed, err := coordinator.RunOperationTransaction(context.Background(), fixture.moduleID, fixture.operationID, fixture.events, fixture.decision, fixture.resolve)
	if err != nil || !committed.Changed {
		t.Fatalf("receipt operation result=%+v err=%v", committed, err)
	}
	fixture.assertCommittedReceipt(t)
	fixture.assertReplayDoesNotDuplicate(t, coordinator)
}

type skuFinderReceiptFixture struct {
	moduleID    string
	requestID   string
	operationID string
	requestData []byte
	pinPath     string
	requestPath string
	events      []state.Event
	decision    state.TransactionDecisionFunc
	resolve     state.TransactionResultResolver
	backend     *discoveryReceiptTestBackend
}

func newSkuFinderReceiptFixture(t *testing.T) skuFinderReceiptFixture {
	t.Helper()
	fixture := skuFinderReceiptFixture{
		moduleID: "avm-utl-sku-finder", requestID: "avm-utl-sku-finder-3671cb5053264385bf22c73fb89c35220d9b89ceb87c2cd30374307388a46c1b",
		operationID: "discovery:module:sku-finder-fixture-1",
	}
	acceptedAt := time.Date(2026, 10, 9, 6, 4, 6, 879495286, time.UTC)
	fixture.pinPath = "state/discovery/source-pins/" + fixture.moduleID + ".json"
	fixture.requestPath = "state/requests/" + fixture.requestID + ".json"
	history := skuFinderSourceHistory(fixture.moduleID)
	request := skuFinderQueuedRequest(fixture.moduleID, fixture.requestID, acceptedAt)
	pinData, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	fixture.requestData, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.events = []state.Event{
		mustDiscoveryEvent(t, fixture.pinPath, "source_pins", history),
		mustDiscoveryEvent(t, fixture.requestPath, "request", request),
	}
	snapshot := state.NewSnapshot()
	snapshot.LayoutVersion = identity.ModuleLayout
	snapshot.LayoutMigrationBase = strings.Repeat("a", 40)
	snapshot.Aggregates = map[string]state.AggregateTransaction{}
	owner, err := identity.OperationalModuleRoot(fixture.moduleID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Aggregates[owner] = state.AggregateTransaction{AppliedEvents: map[string]string{}, FencingTokens: map[string]uint64{}, Generation: 5}
	snapshot.Records[fixture.pinPath] = state.Record{Kind: "source_pins", SchemaVersion: 1, Data: pinData}
	snapshot.Records[fixture.requestPath] = state.Record{Kind: "request", SchemaVersion: 1, Data: fixture.requestData}
	snapshot.Generation = 5
	fixture.backend = &discoveryReceiptTestBackend{snapshot: snapshot, head: "fixture-live-base-2335460b"}
	fixture.decision = func(current state.Snapshot) (state.TransactionDecision, error) {
		if record, exists := current.Records[fixture.requestPath]; !exists || !bytes.Equal(record.Data, fixture.requestData) {
			return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
		}
		return state.TransactionDecision{Events: fixture.events, Result: skuFinderOperationResult(fixture.requestID)}, nil
	}
	fixture.resolve = func(current state.Snapshot) (state.TransactionResolution, error) {
		if _, exists := current.Records["state/operations/receipts/"+fixture.moduleID+"/"+fixture.operationID+".json"]; !exists {
			return state.TransactionResolution{}, nil
		}
		return state.TransactionResolution{Found: true, Value: skuFinderOperationResult(fixture.requestID)}, nil
	}
	return fixture
}

func skuFinderSourceHistory(moduleID string) PinHistory {
	pin := source.ResolvedSource{
		ModuleID: moduleID, Repository: "Azure/terraform-azapi-avm-utl-sku-finder", Version: "v0.3.0", Tag: "v0.3.0",
		Commit:     "4b6c302921ec88e7ebdcddc599ddffc080bde76b",
		ArchiveURL: "https://github.com/Azure/terraform-azapi-avm-utl-sku-finder/archive/4b6c302921ec88e7ebdcddc599ddffc080bde76b.tar.gz",
		ReleaseURL: "https://github.com/Azure/terraform-azapi-avm-utl-sku-finder/releases/tag/v0.3.0",
		Aliases:    []source.TagObservation{{Tag: "v0.3.0", Commit: "4b6c302921ec88e7ebdcddc599ddffc080bde76b"}},
		ResolvedAt: time.Date(2026, 10, 2, 17, 24, 5, 15376421, time.UTC),
	}
	return PinHistory{SchemaVersion: 1, ModuleID: moduleID, OverallLatest: &pin, ReleaseHistory: []source.ResolvedSource{pin}, TrackCandidates: map[string]source.ResolvedSource{}}
}

func skuFinderQueuedRequest(moduleID, requestID string, createdAt time.Time) queue.Request {
	return queue.Request{
		ID: requestID, ModuleID: moduleID, SourceSHA: "4b6c302921ec88e7ebdcddc599ddffc080bde76b", Fingerprint: "3671cb5053264385bf22c73fb89c35220d9b89ceb87c2cd30374307388a46c1b",
		Status: "queued", Attempt: 1, Work: &queue.WorkContract{Phase: "acquire_source", DesiredGeneration: 1, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1", "terraform-json-v1"}, Applicability: "unresolved", Candidate: "unresolved", RetryClass: "none"},
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}

func mustDiscoveryEvent(t *testing.T, recordPath, kind string, value any) state.Event {
	t.Helper()
	event, err := newStateEvent(recordPath, kind, value)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func skuFinderOperationResult(requestID string) json.RawMessage {
	return json.RawMessage(`{"request_id":"` + requestID + `","source_sha":"4b6c302921ec88e7ebdcddc599ddffc080bde76b"}`)
}

func (fixture skuFinderReceiptFixture) assertCommittedReceipt(t *testing.T) {
	t.Helper()
	owner, err := identity.OperationalModuleRoot(fixture.moduleID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := fixture.backend.snapshot.Records["state/operations/receipts/"+fixture.moduleID+"/"+fixture.operationID+".json"]; !exists {
		t.Fatal("module-owned receipt missing after operation commit")
	}
	if fixture.backend.snapshot.Aggregates[owner].Generation != 8 {
		t.Fatalf("module aggregate generation=%d, want five historical plus two data events and receipt", fixture.backend.snapshot.Aggregates[owner].Generation)
	}
	if _, _, err := state.ReadSnapshot(context.Background(), fixture.backend); err != nil {
		t.Fatalf("receipt-bearing snapshot failed validation: %v", err)
	}
	if _, committedFailedEvent := fixture.backend.snapshot.AppliedEvents["discovery-module-transaction-never-committed"]; committedFailedEvent {
		t.Fatal("fixture falsely represents the failed historical operation as committed")
	}
}

func (fixture skuFinderReceiptFixture) assertReplayDoesNotDuplicate(t *testing.T, coordinator state.DecisionCoordinator) {
	t.Helper()
	beforePin := append([]byte(nil), fixture.backend.snapshot.Records[fixture.pinPath].Data...)
	beforeRequest := append([]byte(nil), fixture.backend.snapshot.Records[fixture.requestPath].Data...)
	replayed, err := coordinator.RunOperationTransaction(context.Background(), fixture.moduleID, fixture.operationID, fixture.events, func(state.Snapshot) (state.TransactionDecision, error) {
		t.Fatal("replay reran discovery decision")
		return state.TransactionDecision{}, nil
	}, fixture.resolve)
	if err != nil || replayed.Changed || replayed.Value == nil {
		t.Fatalf("receipt replay=%+v err=%v", replayed, err)
	}
	if !bytes.Equal(beforePin, fixture.backend.snapshot.Records[fixture.pinPath].Data) || !bytes.Equal(beforeRequest, fixture.backend.snapshot.Records[fixture.requestPath].Data) {
		t.Fatal("receipt replay duplicated source pin or request mutation")
	}
}

type discoveryReceiptTestBackend struct {
	mu       sync.Mutex
	head     string
	snapshot state.Snapshot
}

func (backend *discoveryReceiptTestBackend) BuildCandidate(ctx context.Context, options state.CandidateOptions) (state.GitCandidate, error) {
	return state.GitCandidate{ParentHead: options.ParentHead, CandidateID: "fixture-candidate", Snapshot: cloneSnapshot(options.Snapshot)}, nil
}

func (backend *discoveryReceiptTestBackend) Read(ctx context.Context) (string, state.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return "", state.Snapshot{}, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.head, cloneSnapshot(backend.snapshot), nil
}

func (backend *discoveryReceiptTestBackend) Publish(ctx context.Context, candidate state.GitCandidate) (state.PublicationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return state.PublicationOutcome{Kind: state.PublicationRejected, CandidateID: candidate.CandidateID}, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.head != candidate.ParentHead {
		return state.PublicationOutcome{Kind: state.PublicationRejected, Head: backend.head, CandidateID: candidate.CandidateID}, state.ErrHeadMoved
	}
	backend.head = "fixture-receipt-committed"
	backend.snapshot = cloneSnapshot(candidate.Snapshot)
	return state.PublicationOutcome{Kind: state.PublicationCommitted, Head: backend.head, CandidateID: candidate.CandidateID}, nil
}

func TestFactoryDiscoveryEmptyNewScopeDoesNotResolveExistingModules(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer()}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options); err != nil {
		t.Fatal(err)
	}
	options.NewModulesOnly = true
	options.SelectedModuleIDs = nil
	options.ForceModuleIDs = nil
	before := cloneSnapshot(backend.snapshot)
	forbidden := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		t.Error("empty new-module scope resolved an existing source")
		response.WriteHeader(http.StatusForbidden)
	}))
	defer forbidden.Close()
	candidate.FetchedAt = now.Add(2 * time.Minute)
	options.AcceptedAt = now.Add(3 * time.Minute)
	result, err := Persist(context.Background(), source.Resolver{APIBase: forbidden.URL}, testTransactionCoordinator(backend), candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SelectedModuleIDs) != 0 || len(result.ProjectionModuleIDs) != 0 {
		t.Fatal("empty factory scope broadened to existing modules")
	}
	assertModuleRecordsPreserved(t, before, backend.snapshot)
}

func TestFactoryDiscoveryChecksExistingModulesWhenCatalogIDsDoNotChange(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	newID := "avm-res-resources-storage"
	full := discoveryTestSnapshot(now)
	seed := removeDiscoveryModule(full, newID, now)
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer()}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), seed, options); err != nil {
		t.Fatal(err)
	}
	before := cloneSnapshot(backend.snapshot)
	versionServer := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer versionServer.Close()
	full.FetchedAt = now.Add(2 * time.Minute)
	options.AcceptedAt = now.Add(3 * time.Minute)
	result, err := Persist(context.Background(), source.Resolver{APIBase: versionServer.URL}, testTransactionCoordinator(backend), full, options)
	if err != nil {
		t.Fatal(err)
	}
	var expected []string
	for _, module := range full.Modules {
		expected = append(expected, module.ID)
	}
	sort.Strings(expected)
	if !containsModule(result.Delta.Added, newID) || strings.Join(result.SelectedModuleIDs, ",") != strings.Join(expected, ",") {
		t.Fatalf("nightly factory scan did not revisit all active IDs: selected=%v want=%v delta=%#v", result.SelectedModuleIDs, expected, result.Delta)
	}
	selection, err := acceptedDiscoverySelection(backend.snapshot, result.Accepted, result.Delta, options)
	if err != nil || selection.Provenance() != catalog.FactoryAllActive || strings.Join(selection.ModuleIDs(), ",") != strings.Join(expected, ",") {
		t.Fatalf("nightly scan membership provenance=%q modules=%v err=%v", selection.Provenance(), selection.ModuleIDs(), err)
	}
	assertModuleRecordsPreserved(t, before, backend.snapshot)
	assertAcquisitionRequestGenerations(t, backend.snapshot, newID, 1)
}

func TestFactoryDiscoveryQueuesNewReleasesForKnownModulesOnNoCatalogDelta(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	backend := newDiscoveryMemoryBackend()
	baseline := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer()}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: baseline.URL}, testTransactionCoordinator(backend), candidate, options); err != nil {
		baseline.Close()
		t.Fatal(err)
	}
	baseline.Close()

	versions := newReleaseSetServer(t, map[string]string{
		"v1.0.0": strings.Repeat("a", 40),
		"v1.1.0": strings.Repeat("b", 40),
	})
	defer versions.Close()
	candidate.FetchedAt = now.Add(2 * time.Minute)
	options.AcceptedAt = now.Add(3 * time.Minute)
	result, err := Persist(context.Background(), source.Resolver{APIBase: versions.URL}, testTransactionCoordinator(backend), candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Delta.NoChange {
		t.Fatalf("unchanged catalog unexpectedly changed: %#v", result.Delta)
	}
	var expected []string
	for _, module := range candidate.Modules {
		expected = append(expected, module.ID)
	}
	sort.Strings(expected)
	if strings.Join(result.SelectedModuleIDs, ",") != strings.Join(expected, ",") {
		t.Fatalf("release scan selected %v, want all active IDs %v", result.SelectedModuleIDs, expected)
	}
	for _, moduleID := range expected {
		requests := moduleRequests(backend.snapshot, moduleID)
		if len(requests) != 2 {
			t.Fatalf("module %s has %d requests after its newer release, want baseline and new release", moduleID, len(requests))
		}
		versions := map[string]bool{}
		for _, request := range requests {
			versions[request.Work.AVMReleaseVersion] = true
		}
		if !versions["v1.0.0"] || !versions["v1.1.0"] {
			t.Fatalf("module %s requests did not preserve both versions: %v", moduleID, versions)
		}
	}
}

func TestFactoryDiscoveryRecoversPartialReleaseEnrollmentOnEmptyCatalogDelta(t *testing.T) {
	now := time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	backend := newDiscoveryMemoryBackend()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer()}
	baseline := newReleaseSetServer(t, map[string]string{"v1.0.0": strings.Repeat("a", 40)})
	if _, err := Persist(context.Background(), source.Resolver{APIBase: baseline.URL}, testTransactionCoordinator(backend), candidate, options); err != nil {
		baseline.Close()
		t.Fatal(err)
	}
	baseline.Close()

	versions := map[string]string{
		"v1.0.0": strings.Repeat("a", 40),
		"v1.1.0": strings.Repeat("b", 40),
		"v1.2.0": strings.Repeat("c", 40),
	}
	releases := newReleaseSetServer(t, versions)
	defer releases.Close()
	candidate.FetchedAt = now.Add(2 * time.Minute)
	options.AcceptedAt = now.Add(3 * time.Minute)
	crashingCoordinator := &discoveryCrashCoordinator{TransactionCoordinator: testTransactionCoordinator(backend), failAt: 4}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: releases.URL}, crashingCoordinator, candidate, options); err == nil {
		t.Fatal("injected interruption did not stop after a partial release scan")
	}
	if crashingCoordinator.calls != 4 {
		t.Fatalf("interrupted transaction calls=%d, want catalog commit, one module commit, then one failed module operation", crashingCoordinator.calls)
	}
	var partiallyAdvanced int
	for _, module := range candidate.Modules {
		requests := moduleRequests(backend.snapshot, module.ID)
		if len(requests) == 3 {
			partiallyAdvanced++
		} else if len(requests) != 1 {
			t.Fatalf("module %s has %d requests after interruption, want baseline or complete release enrollment", module.ID, len(requests))
		}
	}
	if partiallyAdvanced != 1 {
		t.Fatalf("partially enrolled module count=%d, want exactly one", partiallyAdvanced)
	}

	candidate.FetchedAt = now.Add(4 * time.Minute)
	options.AcceptedAt = now.Add(5 * time.Minute)
	recovered, err := Persist(context.Background(), source.Resolver{APIBase: releases.URL}, testTransactionCoordinator(backend), candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Delta.NoChange || len(recovered.CompletedModuleIDs) != len(candidate.Modules) {
		t.Fatalf("empty-delta recovery=%+v, want unchanged catalog and every active module processed", recovered)
	}
	for _, module := range candidate.Modules {
		requests := moduleRequests(backend.snapshot, module.ID)
		if got := len(requests); got != 3 {
			t.Fatalf("module %s has %d requests after recovery, want baseline and both releases", module.ID, got)
		}
		if got := strings.Join(sortedRequestVersions(requests), ","); got != "v1.0.0,v1.1.0,v1.2.0" {
			t.Fatalf("module %s release requests = %s, want all stable versions since baseline", module.ID, got)
		}
	}
}

func TestFactoryDiscoveryRecoversAcceptedSelectionAfterCatalogCommit(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	newID := "avm-res-resources-storage"
	full := discoveryTestSnapshot(now)
	seed := removeDiscoveryModule(full, newID, now)
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, NewModulesOnly: true, Producer: discoveryTestProducer()}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), seed, options); err != nil {
		t.Fatal(err)
	}
	seedRequestCount := countRecordsWithKind(backend.snapshot, "request")

	full = discoveryTestSnapshot(now.Add(2 * time.Minute))
	options.AcceptedAt = now.Add(3 * time.Minute)
	crashingCoordinator := &discoveryCrashCoordinator{TransactionCoordinator: testTransactionCoordinator(backend), failAt: 2}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, crashingCoordinator, full, options); err == nil {
		t.Fatal("module transaction failure was not returned")
	}
	if crashingCoordinator.calls != 2 || countRecordsWithKind(backend.snapshot, "discovery_observation") != 2 {
		t.Fatalf("catalog acceptance did not commit independently: transactions=%d observations=%d", crashingCoordinator.calls, countRecordsWithKind(backend.snapshot, "discovery_observation"))
	}
	accepted, err := readDurableCatalog(backend.snapshot)
	if err != nil || accepted == nil {
		t.Fatalf("accepted catalog missing after module crash: accepted=%v err=%v", accepted, err)
	}
	selection, err := acceptedDiscoverySelection(backend.snapshot, *accepted, catalog.Delta{}, options)
	if err != nil || strings.Join(selection.ModuleIDs(), ",") != newID {
		t.Fatalf("accepted immutable enrollment missing after module crash: selection=%v err=%v", selection.ModuleIDs(), err)
	}
	if _, exists := backend.snapshot.Records["state/discovery/source-pins/"+newID+".json"]; exists {
		t.Fatal("crashed module transaction unexpectedly persisted its source pin")
	}

	full.FetchedAt = now.Add(4 * time.Minute)
	options.AcceptedAt = now.Add(5 * time.Minute)
	resolver := source.Resolver{APIBase: server.URL}
	result, err := Persist(context.Background(), resolver, testTransactionCoordinator(backend), full, options)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Delta.NoChange || strings.Join(result.SelectedModuleIDs, ",") != newID {
		t.Fatalf("empty-delta retry lost accepted enrollment: delta=%#v selected=%v", result.Delta, result.SelectedModuleIDs)
	}
	if countRecordsWithKind(backend.snapshot, "request") != seedRequestCount+1 {
		t.Fatalf("recovered module request count = %d, want one new request after %d seed requests", countRecordsWithKind(backend.snapshot, "request"), seedRequestCount)
	}
}

func TestFactoryDiscoveryRecoversOnlyMissingModulesAfterIntermediateCrash(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	firstID := "avm-res-resources-database"
	secondID := "avm-res-resources-network"
	full := discoveryTestSnapshot(now)
	newModule := full.Modules[1]
	newModule.ID = secondID
	newModule.RepositoryURL = "https://github.com/Azure/terraform-azurerm-avm-res-resources-network"
	newModule.RegistryURL = "https://registry.terraform.io/modules/Azure/avm-res-resources-network"
	full.Modules = append(full.Modules, newModule)
	newModule.ID = firstID
	newModule.RepositoryURL = "https://github.com/Azure/terraform-azurerm-avm-res-resources-database"
	newModule.RegistryURL = "https://registry.terraform.io/modules/Azure/avm-res-resources-database"
	full.Modules = append(full.Modules, newModule)
	full = refreshDiscoveryFeedCounts(full, now)
	seedSnapshot := discoveryTestSnapshot(now)
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, NewModulesOnly: true, Producer: discoveryTestProducer()}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), seedSnapshot, options); err != nil {
		t.Fatal(err)
	}
	full.FetchedAt = now.Add(2 * time.Minute)
	options.AcceptedAt = now.Add(3 * time.Minute)
	crashingCoordinator := &discoveryCrashCoordinator{TransactionCoordinator: testTransactionCoordinator(backend), failAt: 4}
	interrupted, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, crashingCoordinator, full, options)
	if err == nil {
		t.Fatal("intermediate module transaction failure was not returned")
	}
	if len(interrupted.CompletedModuleIDs) != 0 && len(interrupted.CompletedModuleIDs) != 1 {
		t.Fatalf("invalid retained module completion set: %v", interrupted.CompletedModuleIDs)
	}
	requestCountAtCrash := countRecordsWithKind(backend.snapshot, "request")
	accepted, err := readDurableCatalog(backend.snapshot)
	if err != nil || accepted == nil {
		t.Fatalf("accepted catalog unavailable after intermediate crash: %v", err)
	}
	acceptedSelection, err := acceptedDiscoverySelection(backend.snapshot, *accepted, catalog.Delta{}, options)
	if err != nil || len(acceptedSelection.ModuleIDs()) != 2 || !containsModule(acceptedSelection.ModuleIDs(), firstID) || !containsModule(acceptedSelection.ModuleIDs(), secondID) {
		t.Fatalf("intermediate crash enrollment=%v err=%v", acceptedSelection.ModuleIDs(), err)
	}
	var completedIDs, missingIDs []string
	for _, moduleID := range acceptedSelection.ModuleIDs() {
		if _, exists := backend.snapshot.Records["state/discovery/source-pins/"+moduleID+".json"]; exists {
			completedIDs = append(completedIDs, moduleID)
		} else {
			missingIDs = append(missingIDs, moduleID)
		}
	}
	if len(completedIDs) != 1 || completedIDs[0] != firstID || len(missingIDs) != 1 || missingIDs[0] != secondID || crashingCoordinator.calls < 3 {
		t.Fatalf("intermediate crash state completed=%v missing=%v transactions=%d", completedIDs, missingIDs, crashingCoordinator.calls)
	}
	firstWorkPath, err := identity.StateDiscoveryWorkPath(firstID)
	if err != nil {
		t.Fatal(err)
	}
	secondWorkPath, err := identity.StateDiscoveryWorkPath(secondID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := backend.snapshot.Records[firstWorkPath]; !exists {
		t.Fatal("prior module work/receipt commit was not retained")
	}
	if _, exists := backend.snapshot.Records[secondWorkPath]; exists {
		t.Fatal("later module was marked started after the preceding module failure")
	}

	full.FetchedAt = now.Add(4 * time.Minute)
	options.AcceptedAt = now.Add(5 * time.Minute)
	result, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), full, options)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Delta.NoChange || len(result.CompletedModuleIDs) != 2 || strings.Join(result.SelectedModuleIDs, ",") != strings.Join(acceptedSelection.ModuleIDs(), ",") {
		t.Fatalf("accepted factory membership recovery selected=%v completed=%v delta=%#v", result.SelectedModuleIDs, result.CompletedModuleIDs, result.Delta)
	}
	if countRecordsWithKind(backend.snapshot, "request") != requestCountAtCrash+1 {
		t.Fatalf("module recovery request count = %d, want one additional request after the crash count %d", countRecordsWithKind(backend.snapshot, "request"), requestCountAtCrash)
	}
}

func TestManualCyclePersistsExactSelectionProvenance(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	selected := []string{"avm-res-resources-storage", "avm-ptn-test"}
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: selected, Producer: discoveryTestProducer()}
	_, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range backend.snapshot.Records {
		if record.Kind != "discovery_observation" {
			continue
		}
		var observation Observation
		if err := json.Unmarshal(record.Data, &observation); err != nil {
			t.Fatal(err)
		}
		if observation.Selection.Provenance() != catalog.ManualExactScope || strings.Join(observation.Selection.ModuleIDs(), ",") != "avm-ptn-test,avm-res-resources-storage" {
			t.Fatalf("manual enrollment lost exact selection provenance: %#v", observation.Selection)
		}
		return
	}
	t.Fatal("manual discovery did not persist its immutable selection")
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
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer()}
	if mode == "manual" {
		options.SelectedModuleIDs = []string{"avm-res-resources-resourcegroup"}
	} else {
		options.NewModulesOnly = true
	}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options); err != nil {
		t.Fatal(err)
	}
	retiringID := "avm-res-resources-storage"
	path := projectionIntentPath + retiringID + ".json"
	before := append([]byte(nil), backend.snapshot.Records[path].Data...)
	for observation := 1; observation <= 2; observation++ {
		candidate = removeDiscoveryModule(candidate, retiringID, now.Add(time.Duration(observation*2)*time.Minute))
		options.AcceptedAt = candidate.FetchedAt.Add(time.Minute)
		if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(before, backend.snapshot.Records[path].Data) {
		t.Fatal("catalog retirement wrote an out-of-scope module intent")
	}
}

func TestFactoryDiscoveryFailureLeavesLaterMembershipUnclaimed(t *testing.T) {
	now := time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	firstID := "avm-res-resources-resourcegroup"
	laterID := "avm-res-resources-storage"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "resourcegroup") {
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/releases") {
			_, _ = response.Write([]byte(`[ {"tag_name":"v1.0.0","html_url":"https://example.invalid/v1.0.0","published_at":"2026-01-01T00:00:00Z"} ]`))
			return
		}
		_, _ = fmt.Fprintf(response, `{"object":{"type":"commit","sha":%q}}`, strings.Repeat("a", 40))
	}))
	defer server.Close()
	backend := newDiscoveryMemoryBackend()
	_, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, Options{
		AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
		NewModulesOnly: true, Producer: discoveryTestProducer(),
	})
	if err == nil {
		t.Fatal("first unresolved factory module did not stop discovery")
	}
	for _, moduleID := range []string{firstID, laterID} {
		workPath, pathErr := identity.StateDiscoveryWorkPath(moduleID)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		_, exists := backend.snapshot.Records[workPath]
		if moduleID == firstID && !exists {
			t.Fatal("failed first module lacks durable discovery work")
		}
		if moduleID == laterID && exists {
			t.Fatal("later accepted module was claimed after the first unresolved failure")
		}
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
	options := Options{AcceptedAt: startedAt.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer()}
	first, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	firstRequestCount := countRecordsWithKind(backend.snapshot, "request")
	if firstRequestCount != len(candidate.Modules) || len(first.ProjectionModuleIDs) != len(candidate.Modules) {
		t.Fatalf("initial discovery requests=%d projections=%v, want one request and projection per module", firstRequestCount, first.ProjectionModuleIDs)
	}
	candidate.FetchedAt = startedAt.Add(2 * time.Minute)
	options.AcceptedAt = startedAt.Add(3 * time.Minute)
	second, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Delta.NoChange || second.ProjectionModuleIDs != nil || countRecordsWithKind(backend.snapshot, "request") != firstRequestCount {
		t.Fatalf("unchanged nightly discovery added work: delta=%#v projectionIDs=%v requestCount=%d", second.Delta, second.ProjectionModuleIDs, countRecordsWithKind(backend.snapshot, "request"))
	}
}

func TestPersistNightlyDiscoveryRetainsEveryStableReleaseSinceBaseline(t *testing.T) {
	startedAt := time.Date(2026, 10, 3, 4, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(startedAt)
	moduleID := "avm-res-resources-resourcegroup"
	options := Options{
		AcceptedAt: startedAt.Add(time.Minute), ExtractorContract: "factory-hcl-v1",
		Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{moduleID}, Producer: discoveryTestProducer(),
	}
	backend := newDiscoveryMemoryBackend()
	baselineServer := newReleaseSetServer(t, map[string]string{"v1.0.0": strings.Repeat("a", 40)})
	if _, err := Persist(context.Background(), source.Resolver{APIBase: baselineServer.URL}, testTransactionCoordinator(backend), candidate, options); err != nil {
		t.Fatal(err)
	}
	baselineServer.Close()

	candidate.FetchedAt = startedAt.Add(2 * time.Minute)
	options.AcceptedAt = startedAt.Add(3 * time.Minute)
	deltaServer := newReleaseSetServer(t, map[string]string{
		"v1.0.0": strings.Repeat("a", 40),
		"v1.1.0": strings.Repeat("b", 40),
		"v1.2.0": strings.Repeat("c", 40),
	})
	defer deltaServer.Close()
	if _, err := Persist(context.Background(), source.Resolver{APIBase: deltaServer.URL}, testTransactionCoordinator(backend), candidate, options); err != nil {
		t.Fatal(err)
	}

	requests := moduleRequests(backend.snapshot, moduleID)
	versions := make(map[string]queue.Request)
	for _, request := range requests {
		versions[request.Work.AVMReleaseVersion] = request
	}
	for _, version := range []string{"v1.0.0", "v1.1.0", "v1.2.0"} {
		if _, exists := versions[version]; !exists {
			t.Errorf("durable request missing AVM release %s; got %v", version, sortedRequestVersions(requests))
		}
	}
	if len(versions) != 3 {
		t.Fatalf("release requests = %v, want baseline and both intermediate releases", sortedRequestVersions(requests))
	}

	var history PinHistory
	record := backend.snapshot.Records["state/discovery/source-pins/"+moduleID+".json"]
	if err := json.Unmarshal(record.Data, &history); err != nil {
		t.Fatal(err)
	}
	if len(history.ReleaseHistory) != 3 || history.OverallLatest == nil || history.OverallLatest.Version != "v1.2.0" {
		t.Fatalf("durable release history = %+v, want three releases through v1.2.0", history)
	}
	handoffs := make(map[string]projection.Handoff)
	for _, record := range backend.snapshot.Records {
		if record.Kind != "stage_handoff" {
			continue
		}
		var handoff projection.Handoff
		if err := json.Unmarshal(record.Data, &handoff); err != nil {
			t.Fatal(err)
		}
		if handoff.ModuleID != moduleID {
			continue
		}
		handoffs[handoff.InputFingerprint] = handoff
	}
	if len(handoffs) != len(requests) {
		t.Fatalf("module stage handoffs=%d, requests=%d; every release request needs an exact handoff", len(handoffs), len(requests))
	}
	for _, request := range requests {
		fingerprint, err := identity.ID("input-fingerprint", 1, request.Fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		handoff, exists := handoffs[fingerprint]
		if !exists || handoff.Generation != request.Work.DesiredGeneration || handoff.ConsumerStage != "acquire_source" {
			t.Errorf("release %s request %s has no exact acquisition handoff: %+v", request.Work.AVMReleaseVersion, request.ID, handoff)
		}
	}
}

func sortedRequestVersions(requests []queue.Request) []string {
	versions := make([]string, 0, len(requests))
	for _, request := range requests {
		if request.Work != nil {
			versions = append(versions, request.Work.AVMReleaseVersion)
		}
	}
	sort.Strings(versions)
	return versions
}

func TestPersistSelectedSourceChangePreservesOtherModules(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	backend := newDiscoveryMemoryBackend()
	oldServer := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer oldServer.Close()
	options := Options{AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer()}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: oldServer.URL}, testTransactionCoordinator(backend), candidate, options); err != nil {
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
	options.ForceModuleIDs = []string{moduleID}
	result, err := Persist(context.Background(), source.Resolver{APIBase: newServer.URL}, testTransactionCoordinator(backend), candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted.Modules) != len(candidate.Modules) || len(result.ProjectionModuleIDs) != 1 || result.ProjectionModuleIDs[0] != moduleID {
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
	if _, err = Persist(context.Background(), source.Resolver{APIBase: newServer.URL}, testTransactionCoordinator(backend), candidate, options); err == nil || backend.commitCount != commits {
		t.Fatal("force outside selection must fail before committing")
	}
}

func assertSelectedUnchangedDiscovery(t *testing.T, backend *memoryBackend, candidate catalog.Snapshot, resolver source.Resolver, options Options) {
	t.Helper()
	moduleID := candidate.Modules[0].ID
	options.SelectedModuleIDs = []string{moduleID}
	secondObservation := candidate
	secondObservation.FetchedAt = candidate.FetchedAt.Add(2 * time.Minute)
	options.AcceptedAt = secondObservation.FetchedAt.Add(time.Minute)
	ordinary, err := Persist(context.Background(), resolver, testTransactionCoordinator(backend), secondObservation, options)
	if err != nil {
		t.Fatal(err)
	}
	if !ordinary.Delta.NoChange || len(ordinary.Accepted.Modules) != len(candidate.Modules) || len(ordinary.ProjectionModuleIDs) != 0 {
		t.Fatalf("ordinary targeted discovery changed unchanged work or catalog membership: %#v", ordinary)
	}
	if _, exists := backend.snapshot.Records["state/discovery/source-pins/"+moduleID+".json"]; !exists {
		t.Fatal("initial selected source pin missing")
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
	if result.Head != backend.head || backend.commitCount != 3 {
		t.Fatalf("discovery transaction head=%q commits=%d, want catalog acceptance, durable module claim, and classified failure commits", result.Head, backend.commitCount)
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
	if len(result.CompletedModuleIDs) != 0 {
		t.Fatalf("unresolved source modules reported complete: %v", result.CompletedModuleIDs)
	}
	if countRecordsWithKind(backend.snapshot, "discovery_work") != 1 {
		t.Fatalf("per-module processing continued past first failure: discovery work records=%d, want 1", countRecordsWithKind(backend.snapshot, "discovery_work"))
	}
	for _, record := range backend.snapshot.Records {
		if record.Kind != "discovery_work" {
			continue
		}
		var work state.DiscoveryWork
		if err := json.Unmarshal(record.Data, &work); err != nil {
			t.Fatal(err)
		}
		if work.State != state.DiscoveryWorkRetryable {
			t.Fatalf("discovery work state = %q, want retryable for unresolved source", work.State)
		}
	}
	if countRecordsWithKind(backend.snapshot, "projection_intent") != 1 {
		t.Fatalf("source failure should persist only the current module projection intent, got %d", countRecordsWithKind(backend.snapshot, "projection_intent"))
	}
}

func TestPersistNewOverallPinPreservesOlderTrackWork(t *testing.T) {
	start := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	candidateV4 := discoveryTestSnapshot(start)
	commitV4 := strings.Repeat("a", 40)
	serverV4 := newVersionServer(t, "v4.0.0", commitV4, "", "")
	backend := newDiscoveryMemoryBackend()
	options := Options{AcceptedAt: start.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer(), SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}}
	_, err := Persist(context.Background(), source.Resolver{APIBase: serverV4.URL}, testTransactionCoordinator(backend), candidateV4, options)
	serverV4.Close()
	if err != nil {
		t.Fatal(err)
	}
	moduleID := candidateV4.Modules[0].ID
	seededHead, assessmentID, pinPath := seedV4TrackAssessment(t, backend, backend.head, moduleID, commitV4, start)

	commitV5 := strings.Repeat("b", 40)
	serverV5 := newVersionServer(t, "v5.0.0", commitV5, "v4.0.0", commitV4)
	defer serverV5.Close()
	candidateV5 := discoveryTestSnapshot(start.Add(3 * time.Minute))
	options.AcceptedAt = start.Add(4 * time.Minute)
	second, err := Persist(context.Background(), source.Resolver{APIBase: serverV5.URL}, testTransactionCoordinator(backend), candidateV5, options)
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
	options := Options{AcceptedAt: start.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer(), SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}}
	_, err := Persist(context.Background(), source.Resolver{APIBase: firstServer.URL}, testTransactionCoordinator(backend), candidate, options)
	firstServer.Close()
	if err != nil {
		t.Fatal(err)
	}

	movedCommit := strings.Repeat("b", 40)
	movedServer := newVersionServer(t, "v4.0.0", movedCommit, "v4.0.0", movedCommit)
	defer movedServer.Close()
	candidate.FetchedAt = start.Add(2 * time.Minute)
	options.AcceptedAt = start.Add(3 * time.Minute)
	_, err = Persist(context.Background(), source.Resolver{APIBase: movedServer.URL}, testTransactionCoordinator(backend), candidate, options)
	if err == nil {
		t.Fatal("moved accepted source tag did not stop discovery for review")
	}
	if !strings.Contains(err.Error(), "avm-res-resources-resourcegroup") {
		t.Fatalf("moved-tag failure stopped at unexpected module: %v", err)
	}
	moduleID := "avm-res-resources-resourcegroup"
	workPath, pathErr := identity.StateDiscoveryWorkPath(moduleID)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	var work state.DiscoveryWork
	if json.Unmarshal(backend.snapshot.Records[workPath].Data, &work) != nil || work.State != state.DiscoveryWorkNeedsReview || work.FailureCategory != "policy_rejected" {
		t.Fatalf("moved tag discovery work = %+v, error=%v", work, err)
	}

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
	options := Options{AcceptedAt: start.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer()}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), candidate, options); err != nil {
		t.Fatal(err)
	}

	withoutRetiring := removeDiscoveryModule(candidate, retiringID, start.Add(2*time.Minute))
	options.AcceptedAt = start.Add(3 * time.Minute)
	second, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), withoutRetiring, options)
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
	third, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, testTransactionCoordinator(backend), withoutRetiring, options)
	if err != nil {
		t.Fatal(err)
	}
	var retiredIntent ProjectionIntent
	if err = json.Unmarshal(backend.snapshot.Records["state/projection/intents/"+retiringID+".json"].Data, &retiredIntent); err != nil {
		t.Fatal(err)
	}
	if !containsModule(third.Delta.Retired, retiringID) || retiredIntent.Status != IntentBacklog {
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

func newReleaseSetServer(t *testing.T, tags map[string]string) *httptest.Server {
	t.Helper()
	versions := make([]string, 0, len(tags))
	for version := range tags {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(left, right int) bool { return versions[left] > versions[right] })
	releases := make([]map[string]string, 0, len(versions))
	for _, version := range versions {
		releases = append(releases, map[string]string{
			"tag_name": version, "html_url": "https://example.invalid/" + version,
			"published_at": "2026-01-01T00:00:00Z",
		})
	}
	encoded, err := json.Marshal(releases)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/releases") {
			_, _ = response.Write(encoded)
			return
		}
		if strings.Contains(request.URL.Path, "/git/ref/tags/") {
			tag := request.URL.Path[strings.LastIndex(request.URL.Path, "/git/ref/tags/")+len("/git/ref/tags/"):]
			if commit, exists := tags[tag]; exists {
				_, _ = fmt.Fprintf(response, `{"object":{"type":"commit","sha":%q}}`, commit)
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
		{ID: "avm-res-resources-resourcegroup", Type: catalog.Resource, Status: "Available", DisplayName: "Resource Group", Description: "AVM resource module", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-resources-resourcegroup", RegistryURL: "https://registry.terraform.io/modules/Azure/avm-res-resources-resourcegroup"},
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
	updated.Modules = filterDiscoveryModules(snapshot.Modules, moduleID)
	return refreshDiscoveryFeedCounts(updated, fetchedAt)
}

func filterDiscoveryModules(modules []catalog.Module, excludedID string) []catalog.Module {
	filtered := make([]catalog.Module, 0, len(modules)-1)
	for _, module := range modules {
		if module.ID != excludedID {
			filtered = append(filtered, module)
		}
	}
	return filtered
}

func refreshDiscoveryFeedCounts(snapshot catalog.Snapshot, fetchedAt time.Time) catalog.Snapshot {
	updated := snapshot
	updated.FetchedAt = fetchedAt
	updated.Feeds = append([]catalog.FeedSnapshot(nil), snapshot.Feeds...)
	counts := make(map[catalog.Kind]int)
	for _, module := range snapshot.Modules {
		counts[module.Type]++
	}
	for index := range updated.Feeds {
		updated.Feeds[index].ModuleCount = counts[updated.Feeds[index].Kind]
		if updated.Feeds[index].ModuleCount == 0 {
			updated.Feeds[index].ModuleCount = 1
		}
		updated.Feeds[index].SHA256 = strings.Repeat(map[catalog.Kind]string{catalog.Resource: "d", catalog.Pattern: "e", catalog.Utility: "f"}[updated.Feeds[index].Kind], 64)
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

type discoveryTestCoordinator struct {
	state.Backend
}

type discoveryTestOperationCoordinator struct {
	discoveryTestCoordinator
}

type discoveryCrashCoordinator struct {
	state.TransactionCoordinator
	calls  int
	failAt int
}

type rejectingOperationCoordinator struct {
	state.TransactionCoordinator
}

func (coordinator rejectingOperationCoordinator) RunOperationTransaction(context.Context, string, string, []state.Event, state.TransactionDecisionFunc, state.TransactionResultResolver) (state.TransactionResult, error) {
	return state.TransactionResult{}, state.ErrTransactionIdentityConflict
}

func (coordinator *discoveryCrashCoordinator) beforeTransaction() error {
	coordinator.calls++
	if coordinator.calls == coordinator.failAt {
		return errors.New("injected discovery crash")
	}
	return nil
}

func (coordinator *discoveryCrashCoordinator) RunTransaction(ctx context.Context, operationID string, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	if err := coordinator.beforeTransaction(); err != nil {
		return state.TransactionResult{}, err
	}
	return coordinator.TransactionCoordinator.RunTransaction(ctx, operationID, decide, resolve)
}

func (coordinator *discoveryCrashCoordinator) RunOperationTransaction(ctx context.Context, moduleID, operationID string, events []state.Event, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	if err := coordinator.beforeTransaction(); err != nil {
		return state.TransactionResult{}, err
	}
	operationRunner, ok := coordinator.TransactionCoordinator.(operationTransactionRunner)
	if !ok {
		return state.TransactionResult{}, errors.New("wrapped coordinator lacks operation transactions")
	}
	return operationRunner.RunOperationTransaction(ctx, moduleID, operationID, events, decide, resolve)
}

func (coordinator discoveryTestCoordinator) Read(ctx context.Context) (string, state.Snapshot, error) {
	return coordinator.Backend.Read(ctx)
}

func (coordinator discoveryTestCoordinator) RunTransaction(ctx context.Context, operationID string, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	for attempt := 0; attempt < 3; attempt++ {
		head, snapshot, err := state.ReadSnapshot(ctx, coordinator.Backend)
		if err != nil {
			return state.TransactionResult{}, err
		}
		if discoveryEventApplied(snapshot, operationID) {
			resolution, resolveErr := resolve(snapshot)
			if resolveErr != nil || !resolution.Found {
				if resolveErr != nil {
					return state.TransactionResult{}, resolveErr
				}
				return state.TransactionResult{}, state.ErrTransactionIdentityConflict
			}
			return state.TransactionResult{Head: head, Value: resolution.Value}, nil
		}
		decision, err := decide(snapshot)
		if err != nil {
			return state.TransactionResult{}, err
		}
		if decision.Noop {
			return state.TransactionResult{Head: head, Value: decision.Result}, nil
		}
		updated, _, err := state.ApplyEventsAtHead(ctx, coordinator.Backend, head, decision.Events)
		if errors.Is(err, state.ErrHeadMoved) && attempt < 2 {
			continue
		}
		if err != nil {
			return state.TransactionResult{}, err
		}
		return state.TransactionResult{Head: updated, Value: decision.Result, Changed: true}, nil
	}
	return state.TransactionResult{}, state.ErrTransactionConflict
}

func (coordinator discoveryTestCoordinator) RunOperationTransaction(ctx context.Context, moduleID, operationID string, expectedEvents []state.Event, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	_, snapshot, err := state.ReadSnapshot(ctx, coordinator.Backend)
	if err != nil {
		return state.TransactionResult{}, err
	}
	if receiptPath, pathErr := identity.StateOperationReceiptPath(moduleID, operationID); pathErr == nil {
		if record, exists := snapshot.Records[receiptPath]; exists && record.Kind == "operation_receipt" {
			var receipt state.OperationReceipt
			if json.Unmarshal(record.Data, &receipt) != nil || receipt.ModuleID != moduleID || receipt.OperationID != operationID || !receiptMatchesExpectedEvents(snapshot, receipt, expectedEvents) {
				return state.TransactionResult{}, state.ErrTransactionIdentityConflict
			}
			return state.TransactionResult{Head: "receipt-replay", Value: append(json.RawMessage(nil), receipt.Result...)}, nil
		}
	}
	return coordinator.RunTransaction(ctx, operationID, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
		decision, err := decide(snapshot)
		if err != nil {
			return state.TransactionDecision{}, err
		}
		actualEvents := decision.Events
		if len(actualEvents) != len(expectedEvents) {
			return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
		}
		for _, event := range expectedEvents {
			matched := false
			for _, actual := range actualEvents {
				if actual.ID == event.ID && actual.RecordPath == event.RecordPath && actual.Record.Kind == event.Record.Kind && bytes.Equal(actual.Record.Data, event.Record.Data) {
					matched = true
					break
				}
			}
			if !matched {
				return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
			}
		}
		resultJSON, err := json.Marshal(decision.Result)
		if err != nil {
			return state.TransactionDecision{}, err
		}
		receipt, err := state.NewOperationReceipt(moduleID, operationID, resultJSON, expectedEvents)
		if err != nil {
			return state.TransactionDecision{}, err
		}
		decision.Events = append(append([]state.Event(nil), expectedEvents...), receipt)
		return decision, nil
	}, resolve)
}

func receiptMatchesExpectedEvents(snapshot state.Snapshot, receipt state.OperationReceipt, expected []state.Event) bool {
	if len(receipt.WriteSet) != len(expected) {
		return false
	}
	receiptEvent, err := state.NewOperationReceipt(receipt.ModuleID, receipt.OperationID, receipt.Result, expected)
	if err != nil {
		return false
	}
	var expectedReceipt state.OperationReceipt
	return json.Unmarshal(receiptEvent.Record.Data, &expectedReceipt) == nil && reflect.DeepEqual(receipt.WriteSet, expectedReceipt.WriteSet)
}

func sameTestEventSet(left, right []state.Event) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].RecordPath != right[index].RecordPath || left[index].Record.Kind != right[index].Record.Kind || !bytes.Equal(left[index].Record.Data, right[index].Record.Data) {
			return false
		}
	}
	return true
}

func (coordinator discoveryTestOperationCoordinator) RunTransaction(ctx context.Context, operationID string, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	return coordinator.discoveryTestCoordinator.RunTransaction(ctx, operationID, decide, resolve)
}

func (coordinator discoveryTestOperationCoordinator) RunOperationTransaction(ctx context.Context, moduleID, operationID string, expectedEvents []state.Event, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	return coordinator.discoveryTestCoordinator.RunOperationTransaction(ctx, moduleID, operationID, expectedEvents, decide, resolve)
}

func testTransactionCoordinator(backend state.Backend) state.TransactionCoordinator {
	return discoveryTestOperationCoordinator{discoveryTestCoordinator{Backend: backend}}
}

func discoveryEventApplied(snapshot state.Snapshot, operationID string) bool {
	if _, exists := snapshot.AppliedEvents[operationID]; exists {
		return true
	}
	for _, aggregate := range snapshot.Aggregates {
		if _, exists := aggregate.AppliedEvents[operationID]; exists {
			return true
		}
	}
	return false
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
