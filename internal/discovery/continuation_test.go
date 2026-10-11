package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestContinuationResumesDeliveryWithoutForcedSourceReplay(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	selected := []string{"avm-res-resources-resourcegroup", "avm-res-resources-storage"}
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: selected, ForceModuleIDs: selected, Producer: discoveryTestProducer()}
	result, err := ContinueCycle(ctx, source.Resolver{APIBase: server.URL}, backend, testTransactionCoordinator(backend), discoveryTestSnapshot(now), options, func(ctx context.Context, module ModuleCycle) (string, error) {
		if module.Position == 2 {
			return module.Head, &ratelimit.DeferredError{Resource: "rest", RetryAt: now.Add(-time.Second)}
		}
		return acknowledgeCycleTestModule(t, ctx, backend, module, now.Add(time.Duration(module.Position)*time.Second)), nil
	}, "run-1", "", time.Time{})
	if err != nil || !result.Deferred || len(result.CompletedModuleIDs) != 1 {
		t.Fatalf("cycle did not park: %+v %v", result, err)
	}
	requestsBefore := countRecordsWithKind(backend.snapshot, "request")
	observationsBefore := countRecordsWithKind(backend.snapshot, "discovery_observation")
	cursor, err := ReadContinuation(backend.snapshot, result.ContinuationID)
	if err != nil || !cursor.PendingDelivery || cursor.Next != 1 {
		t.Fatalf("pending phase lost: %+v %v", cursor, err)
	}
	resumed, err := ContinueCycle(ctx, source.Resolver{}, backend, testTransactionCoordinator(backend), catalog.Snapshot{}, options, func(ctx context.Context, module ModuleCycle) (string, error) {
		if module.Position != 2 {
			t.Fatal("completed module was replayed")
		}
		return acknowledgeCycleTestModule(t, ctx, backend, module, time.Now().UTC().Add(time.Second)), nil
	}, "run-2", result.ContinuationID, time.Time{})
	if err != nil || resumed.Deferred || len(resumed.CompletedModuleIDs) != 2 {
		t.Fatalf("resume failed: %+v %v", resumed, err)
	}
	if countRecordsWithKind(backend.snapshot, "request") != requestsBefore || countRecordsWithKind(backend.snapshot, "discovery_observation") != observationsBefore {
		t.Fatal("resume repeated forced source work or catalog observation")
	}
}

// checkpointCountingBackend distinguishes cursor advancement from intermediate pending writes.
type checkpointCountingBackend struct {
	*memoryBackend
	checkpoints int
	pending     int
}

func (backend *checkpointCountingBackend) Commit(ctx context.Context, head string, snapshot state.Snapshot) (string, error) {
	for path, record := range snapshot.Records {
		if record.Kind != "discovery_cycle" || string(record.Data) == string(backend.snapshot.Records[path].Data) {
			continue
		}
		var cursor Continuation
		if err := json.Unmarshal(record.Data, &cursor); err != nil {
			return "", err
		}
		backend.checkpoints++
		if cursor.PendingDelivery {
			backend.pending++
		}
	}
	return backend.memoryBackend.Commit(ctx, head, snapshot)
}

// skipCycleFixture seeds existing source work and measures only the subsequent cycle.
type skipCycleFixture struct {
	backend       *checkpointCountingBackend
	options       Options
	candidate     catalog.Snapshot
	resolver      source.Resolver
	releaseChecks int
	deliveries    int
	skipChecks    int
}

func newSkipCycleFixture(t *testing.T, newRelease bool) *skipCycleFixture {
	t.Helper()
	now := time.Now().UTC()
	fixture := &skipCycleFixture{backend: &checkpointCountingBackend{memoryBackend: newDiscoveryMemoryBackend()},
		options: Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup", "avm-res-resources-storage"}, Producer: discoveryTestProducer()}, candidate: discoveryTestSnapshot(now)}
	versionServer := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	t.Cleanup(versionServer.Close)
	if _, err := Cycle(context.Background(), source.Resolver{APIBase: versionServer.URL}, discoveryTestCoordinator{fixture.backend}, fixture.candidate, fixture.options, fixture.deliver); err != nil {
		t.Fatal(err)
	}
	fixture.deliveries = 0
	if newRelease {
		versionServer = newVersionServer(t, "v2.0.0", strings.Repeat("b", 40), "v1.0.0", strings.Repeat("a", 40))
		t.Cleanup(versionServer.Close)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/releases") {
			fixture.releaseChecks++
		}
		versionServer.Config.Handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	fixture.resolver = source.Resolver{APIBase: server.URL}
	fixture.candidate.FetchedAt = now.Add(time.Minute)
	fixture.options.AcceptedAt = fixture.candidate.FetchedAt
	return fixture
}

func (fixture *skipCycleFixture) deliver(_ context.Context, module ModuleCycle) (string, error) {
	fixture.deliveries++
	return module.Head, nil
}

func (fixture *skipCycleFixture) skip(_ context.Context, module ModuleCycle) (bool, error) {
	fixture.skipChecks++
	if module.Head != fixture.backend.head || len(module.Snapshot.Records) == 0 {
		return false, errors.New("skip did not receive the live source-check snapshot")
	}
	return true, nil
}

func (fixture *skipCycleFixture) assertComplete(t *testing.T, result Result, err error) {
	t.Helper()
	total := len(fixture.options.SelectedModuleIDs)
	if err != nil || !result.ContinuationComplete || len(result.CompletedModuleIDs) != total || fixture.releaseChecks != total || len(result.CheckedModuleIDs) != total {
		t.Fatalf("cycle lost source checks or completion: %+v checks=%d error=%v", result, fixture.releaseChecks, err)
	}
	cursor, err := ReadContinuation(fixture.backend.snapshot, result.ContinuationID)
	if err != nil || cursor.PendingDelivery || cursor.Next != total || !cursor.LeaseExpiresAt.IsZero() {
		t.Fatalf("terminal cursor is inconsistent: %+v %v", cursor, err)
	}
}

func TestContinuationUnchangedSkipRetainsReleaseChecksAndSingleCursorWrite(t *testing.T) {
	fixture := newSkipCycleFixture(t, false)
	result, err := ContinueCycle(context.Background(), fixture.resolver, fixture.backend, discoveryTestCoordinator{fixture.backend}, fixture.candidate, fixture.options, fixture.deliver, "skip-run", "", time.Time{}, fixture.skip)
	fixture.assertComplete(t, result, err)
	total := len(fixture.options.SelectedModuleIDs)
	if fixture.deliveries != 0 || fixture.skipChecks != total || fixture.backend.pending != 0 || fixture.backend.checkpoints != 2 || len(result.SkippedModuleIDs) != total || len(result.ChangedModuleIDs) != 0 {
		t.Fatalf("unchanged work was repeated: fixture=%+v result=%+v", fixture, result)
	}
}

type discoveryDecisionBackend struct {
	*memoryBackend
	nextCandidate int
}

func (backend *discoveryDecisionBackend) BuildCandidate(ctx context.Context, options state.CandidateOptions) (state.GitCandidate, error) {
	if err := ctx.Err(); err != nil {
		return state.GitCandidate{}, err
	}
	if options.ParentHead != backend.head {
		return state.GitCandidate{}, state.ErrHeadMoved
	}
	if err := options.Snapshot.Validate(); err != nil {
		return state.GitCandidate{}, err
	}
	backend.nextCandidate++
	candidateID := fmt.Sprintf("candidate-%d", backend.nextCandidate)
	return state.GitCandidate{ParentHead: options.ParentHead, CandidateID: candidateID, Snapshot: cloneSnapshot(options.Snapshot)}, nil
}

func (backend *discoveryDecisionBackend) Publish(ctx context.Context, candidate state.GitCandidate) (state.PublicationOutcome, error) {
	if candidate.ParentHead != backend.head {
		return state.PublicationOutcome{Kind: state.PublicationRejected}, state.ErrHeadMoved
	}
	head, err := backend.Commit(ctx, candidate.ParentHead, candidate.Snapshot)
	if err != nil {
		return state.PublicationOutcome{Kind: state.PublicationRejected}, err
	}
	return state.PublicationOutcome{Kind: state.PublicationCommitted, Head: head, CandidateID: candidate.CandidateID}, nil
}

func TestContinuationUnchangedSourceResumesThroughDecisionCoordinator(t *testing.T) {
	fixture := newSkipCycleFixture(t, false)
	backend := &discoveryDecisionBackend{memoryBackend: newDiscoveryMemoryBackend()}
	if _, err := state.MigrateModuleLayout(context.Background(), backend); err != nil {
		t.Fatalf("migrate fixture state: %v", err)
	}
	coordinator := state.DecisionCoordinator{Backend: backend, Timing: state.TransactionTiming{Sleep: func(context.Context, time.Duration) error { return nil }, Jitter: func(time.Duration) time.Duration { return 0 }}}
	initial, err := Cycle(context.Background(), fixture.resolver, coordinator, fixture.candidate, fixture.options, fixture.deliver)
	if err != nil || len(initial.CompletedModuleIDs) != len(fixture.options.SelectedModuleIDs) {
		t.Fatalf("initial module cycle failed through decision coordinator: %+v %v", initial, err)
	}
	parked, err := ContinueCycle(context.Background(), fixture.resolver, backend, coordinator, fixture.candidate, fixture.options, fixture.deliver, "decision-park-run", "", time.Now().Add(-time.Second))
	if err != nil || !parked.Deferred || !parked.ContinuationDurable {
		t.Fatalf("cycle did not park before module processing: %+v %v", parked, err)
	}
	result, err := ContinueCycle(context.Background(), fixture.resolver, backend, coordinator, catalog.Snapshot{}, fixture.options, fixture.deliver, "decision-resume-run", parked.ContinuationID, time.Time{})
	if err != nil || !result.ContinuationComplete || len(result.CompletedModuleIDs) != len(fixture.options.SelectedModuleIDs) {
		t.Fatalf("unchanged source continuation did not complete: %+v %v", result, err)
	}
}

func TestContinuationRepairsVersionlessCurrentRequestThroughDecisionCoordinator(t *testing.T) {
	now := time.Now().UTC()
	candidate := discoveryTestSnapshot(now)
	module := candidate.Modules[0]
	otherModule := candidate.Modules[1]
	module.ID = "avm-res-apimanagement-service"
	module.RepositoryURL = "https://github.com/Azure/terraform-azurerm-avm-res-apimanagement-service"
	module.RegistryURL = "https://registry.terraform.io/modules/Azure/avm-res-apimanagement-service"
	candidate.Modules[0] = module
	server := newVersionServer(t, "v0.1.0", "1bb1067ee2e5ce085f84b099fac4159e57fba15f", "", "")
	defer server.Close()
	backend := &discoveryDecisionBackend{memoryBackend: newDiscoveryMemoryBackend()}
	if _, err := state.MigrateModuleLayout(context.Background(), backend); err != nil {
		t.Fatalf("migrate fixture state: %v", err)
	}
	coordinator := state.DecisionCoordinator{Backend: backend, Timing: state.TransactionTiming{
		Sleep: func(context.Context, time.Duration) error { return nil }, Jitter: func(time.Duration) time.Duration { return 0 },
	}}
	options := Options{
		AcceptedAt: now, SelectedModuleIDs: []string{otherModule.ID},
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer(),
	}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, coordinator, candidate, options); err != nil {
		t.Fatalf("persist full catalog: %v", err)
	}
	commit := "1bb1067ee2e5ce085f84b099fac4159e57fba15f"
	pin := source.ResolvedSource{
		ModuleID: module.ID, Repository: strings.TrimPrefix(module.RepositoryURL, "https://github.com/"),
		Version: "v0.1.0", Tag: "v0.1.0", Commit: commit,
		ArchiveURL: module.RepositoryURL + "/archive/" + commit + ".tar.gz",
		ReleaseURL: module.RepositoryURL + "/releases/tag/v0.1.0", ResolvedAt: now,
	}
	history := PinHistory{SchemaVersion: recordSchemaVersion, ModuleID: module.ID, OverallLatest: &pin, ReleaseHistory: []source.ResolvedSource{pin}, TrackCandidates: map[string]source.ResolvedSource{}}
	historyPath, err := identity.StateSourcePinPath(module.ID)
	if err != nil {
		t.Fatal(err)
	}
	historyEvent, err := newStateEvent(historyPath, "source_pins", history)
	if err != nil {
		t.Fatal(err)
	}
	seedEvents := []state.Event{historyEvent}
	for generation := uint64(1); generation <= 4; generation++ {
		request, requestErr := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
			ModuleID: module.ID, SourceSHA: commit, DesiredGeneration: generation, RefreshGeneration: generation,
			ExtractorContract: options.ExtractorContract, Capabilities: options.Capabilities,
		}, now.Add(time.Duration(generation)*time.Minute))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		// Historical requests used a fingerprint shape that no longer validates
		// against today's version-bound acquisition contract.
		request.Fingerprint = fmt.Sprintf("%064x", generation)
		request.ID = module.ID + "-" + request.Fingerprint
		if generation < 4 {
			request.Status = queue.StatusSuperseded
		}
		requestPath := "state/requests/" + request.ID + ".json"
		requestEvent, eventErr := newStateEvent(requestPath, "request", request)
		if eventErr != nil {
			t.Fatal(eventErr)
		}
		seedEvents = append(seedEvents, requestEvent)
	}
	_, seededSnapshot, err := coordinator.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	intent, changed, err := buildProjectionIntent(seededSnapshot, projectionIntentInput{
		ModuleID: module.ID, Generation: 4, Status: IntentBacklog,
		Reason: "source facts are not yet accepted for assessment", Source: &pin, UpdatedAt: now,
	})
	if err != nil || !changed {
		t.Fatalf("build historical backlog intent: changed=%v err=%v", changed, err)
	}
	intentEvent, err := newStateEvent(projectionIntentPath+module.ID+".json", "projection_intent", intent)
	if err != nil {
		t.Fatal(err)
	}
	seedEvents = append(seedEvents, intentEvent)
	// Seed the historical versionless shape through a real module transaction.
	// Direct snapshot edits bypass module-v2 aggregate ownership and make the
	// production coordinator correctly reject the fixture before discovery runs.
	operationID := "discovery:module:legacy-current-request-fixture"
	if _, err := coordinator.RunOperationTransaction(context.Background(), module.ID, operationID, seedEvents, func(state.Snapshot) (state.TransactionDecision, error) {
		return state.TransactionDecision{
			Events: seedEvents, Result: "legacy-current-request",
			Candidate: state.CandidateOptions{Subject: "test: seed versionless current request", Provenance: state.GitProvenance{Operation: "test.discovery", Event: operationID, Module: module.ID}},
		}, nil
	}, func(state.Snapshot) (state.TransactionResolution, error) {
		return state.TransactionResolution{}, nil
	}); err != nil {
		t.Fatalf("seed versionless current request through module transaction: %v", err)
	}
	// Unrelated retained queue history must not block this module's handoff.
	// The live factory-state snapshot contains requests from many modules, and
	// this target's module transaction owns only its own request aggregate.
	unrelated, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: "avm-res-unrelated-history", SourceSHA: commit, DesiredGeneration: 1,
		ExtractorContract: options.ExtractorContract, Capabilities: options.Capabilities,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	unrelated.Work.AVMReleaseVersion = ""
	unrelated.Fingerprint = fmt.Sprintf("%064x", 99)
	unrelated.ID = unrelated.ModuleID + "-" + unrelated.Fingerprint
	unrelatedPath := "state/requests/" + unrelated.ID + ".json"
	unrelatedEvent, err := newStateEvent(unrelatedPath, "request", unrelated)
	if err != nil {
		t.Fatal(err)
	}
	seedUnrelatedID, err := identity.ID("test-unrelated-request", 1, unrelated.ID)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedEvent.ID = seedUnrelatedID
	if _, err := coordinator.RunTransaction(context.Background(), seedUnrelatedID, func(state.Snapshot) (state.TransactionDecision, error) {
		return state.TransactionDecision{
			Events: []state.Event{unrelatedEvent}, Result: "unrelated-versionless-request",
			Candidate: state.CandidateOptions{Subject: "test: retain unrelated versionless request", Provenance: state.GitProvenance{Operation: "discovery.persist", Event: seedUnrelatedID}},
		}, nil
	}, func(state.Snapshot) (state.TransactionResolution, error) {
		return state.TransactionResolution{}, nil
	}); err != nil {
		t.Fatalf("seed unrelated versionless request: %v", err)
	}

	candidate.FetchedAt = now.Add(2 * time.Minute)
	options.AcceptedAt = now.Add(3 * time.Minute)
	options.SelectedModuleIDs = []string{module.ID}
	result, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, coordinator, candidate, options, func(_ context.Context, moduleCycle ModuleCycle) (string, error) {
		return moduleCycle.Head, nil
	}, "versionless-repair-run", "", time.Time{})
	if err != nil {
		t.Fatalf("continue exact current-release repair: %v", err)
	}
	if !result.ContinuationComplete || len(result.CheckedModuleIDs) != 1 || len(result.CompletedModuleIDs) != 1 {
		t.Fatalf("repair cycle did not complete exactly one module: %+v", result)
	}
	requests := moduleRequests(backend.snapshot, module.ID)
	if len(requests) != 1 {
		t.Fatalf("module requests = %d, want only the new versioned request after obsolete unstarted records are removed", len(requests))
	}
	var versioned queue.Request
	for _, request := range requests {
		if request.Work != nil && request.Work.AVMReleaseVersion == pin.Version && request.SourceSHA == pin.Commit {
			versioned = request
		} else {
			t.Fatalf("unexpected request remains after versionless repair: %+v", request)
		}
	}
	if versioned.Work == nil || versioned.Status != queue.StatusQueued || versioned.Work.AVMReleaseVersion != pin.Version || versioned.SourceSHA != pin.Commit || versioned.Work.RefreshGeneration != 0 {
		t.Fatalf("versioned request = %+v, want exact current release without force", versioned)
	}
	var handoffFound bool
	var handoffOperationID string
	for _, record := range backend.snapshot.Records {
		if record.Kind != "stage_handoff" {
			continue
		}
		var handoff struct {
			ModuleID         string `json:"module_id"`
			InputFingerprint string `json:"input_fingerprint"`
			OperationID      string `json:"operation_id"`
		}
		if err := json.Unmarshal(record.Data, &handoff); err != nil {
			t.Fatal(err)
		}
		fingerprint, fingerprintErr := identity.ID("input-fingerprint", 1, versioned.Fingerprint)
		if fingerprintErr != nil {
			t.Fatal(fingerprintErr)
		}
		if handoff.ModuleID == module.ID && handoff.InputFingerprint == fingerprint {
			handoffFound = true
			handoffOperationID = handoff.OperationID
		}
	}
	if !handoffFound {
		t.Fatal("versioned current-release request has no receipt-backed acquisition handoff")
	}
	receiptPath, err := identity.StateOperationReceiptPath(module.ID, handoffOperationID)
	if err != nil {
		t.Fatal(err)
	}
	var receipt state.OperationReceipt
	if err := json.Unmarshal(backend.snapshot.Records[receiptPath].Data, &receipt); err != nil {
		t.Fatalf("decode handoff operation receipt: %v", err)
	}
	if err := receipt.Validate(receiptPath); err != nil {
		t.Fatalf("validate handoff operation receipt: %v", err)
	}
	var operationResult cycleModuleReceiptResult
	if err := json.Unmarshal(receipt.Result, &operationResult); err != nil {
		t.Fatalf("decode handoff operation result: %v", err)
	}
	if err := validateReceiptWriteSet(receipt, operationResult.Events); err != nil {
		t.Fatalf("handoff is not in the immutable receipt write set: %v", err)
	}
	deleted := 0
	for _, event := range operationResult.Events {
		if !event.Delete {
			continue
		}
		deleted++
		if event.Record.Kind != "request" {
			t.Fatalf("unexpected obsolete request deletion event: %+v", event)
		}
		if _, exists := backend.snapshot.Records[event.RecordPath]; exists {
			t.Fatalf("deleted request %s remains in current state", event.RecordPath)
		}
		if digest, exists := backend.snapshot.AppliedEvents[event.ID]; !exists || digest == "" {
			t.Fatalf("deletion event %s has no durable replay proof", event.ID)
		}
	}
	if deleted != 4 {
		t.Fatalf("receipt deletion events = %d, want all four obsolete unstarted requests", deleted)
	}
}

type failReceiptCursorCheckpointBackend struct {
	*discoveryDecisionBackend
	failed bool
}

func (backend *failReceiptCursorCheckpointBackend) BuildCandidate(ctx context.Context, options state.CandidateOptions) (state.GitCandidate, error) {
	if !backend.failed && options.Subject == "state(discovery): checkpoint continuation" {
		for _, record := range options.Snapshot.Records {
			if record.Kind == "stage_handoff" {
				backend.failed = true
				return state.GitCandidate{}, fmt.Errorf("fixture continuation checkpoint failure")
			}
		}
	}
	return backend.discoveryDecisionBackend.BuildCandidate(ctx, options)
}

func TestContinuationReplaysReceiptBeforeSourceResolutionWhenCursorLags(t *testing.T) {
	now := time.Now().UTC()
	fixture := discoveryTestSnapshot(now)
	moduleID := fixture.Modules[0].ID
	moduleIDs := []string{moduleID}
	versionServer := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer versionServer.Close()
	var sourceReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sourceReads.Add(1)
		versionServer.Config.Handler.ServeHTTP(writer, request)
	}))
	defer server.Close()
	baseBackend := &discoveryDecisionBackend{memoryBackend: newDiscoveryMemoryBackend()}
	if _, err := state.MigrateModuleLayout(context.Background(), baseBackend); err != nil {
		t.Fatal(err)
	}
	backend := &failReceiptCursorCheckpointBackend{discoveryDecisionBackend: baseBackend}
	coordinator := state.DecisionCoordinator{Backend: backend, Timing: state.TransactionTiming{
		Sleep: func(context.Context, time.Duration) error { return nil }, Jitter: func(time.Duration) time.Duration { return 0 },
	}}
	options := Options{
		AcceptedAt: now, SelectedModuleIDs: moduleIDs, ExtractorContract: "factory-hcl-v1",
		Capabilities: []string{"native-hcl-v1"}, Producer: discoveryTestProducer(),
	}
	deliveries := 0
	_, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, coordinator, fixture, options, func(_ context.Context, module ModuleCycle) (string, error) {
		deliveries++
		return module.Head, nil
	}, "receipt-lag-first-run", "", time.Time{})
	if err == nil || !backend.failed || deliveries != 0 {
		t.Fatalf("checkpoint failure did not stop after module receipt: failed=%v deliveries=%d err=%v", backend.failed, deliveries, err)
	}
	_, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var cursorID string
	for path, record := range snapshot.Records {
		if record.Kind != "discovery_cycle" {
			continue
		}
		var candidate Continuation
		if err := json.Unmarshal(record.Data, &candidate); err != nil {
			t.Fatal(err)
		}
		if candidate.OwnerRunID == "receipt-lag-first-run" {
			cursorID = candidate.ID
			if path != "state/discovery/cycles/"+cursorID+".json" {
				t.Fatalf("continuation path does not match cursor ID: %s", path)
			}
		}
	}
	if cursorID == "" {
		t.Fatal("continuation cursor missing after receipt commit")
	}
	cursor, err := ReadContinuation(snapshot, cursorID)
	if err != nil || cursor.Next != 0 || cursor.PendingDelivery {
		t.Fatalf("failed checkpoint advanced the cycle: %+v %v", cursor, err)
	}
	operationID, err := cycleModuleOperationID(&cursor, cursor.Options, moduleID, cursor.Result.Accepted.Generation)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := snapshot.Records[receiptPath]; !exists {
		t.Fatal("module receipt was not durable before the cursor checkpoint")
	}
	readsBeforeResume := sourceReads.Load()

	// Expire only this synthetic fixture owner through a normal global state
	// transaction so the resume exercises the same lease admission contract.
	cursor.LeaseExpiresAt = time.Now().UTC().Add(-time.Second)
	expiredEvent, err := continuationEvent(cursor)
	if err != nil {
		t.Fatal(err)
	}
	expireOperationID := "test:expire-receipt-lag-cycle"
	expiredEvent.ID = expireOperationID
	if _, err := coordinator.RunTransaction(context.Background(), expireOperationID, func(current state.Snapshot) (state.TransactionDecision, error) {
		owner, ownerErr := ReadContinuation(current, cursorID)
		if ownerErr != nil || owner.OwnerRunID != cursor.OwnerRunID || owner.OwnerGeneration != cursor.OwnerGeneration {
			return state.TransactionDecision{}, state.ErrStaleFence
		}
		return state.TransactionDecision{Events: []state.Event{expiredEvent}, Result: cursor, Candidate: state.CandidateOptions{Subject: "test: expire fixture continuation", Provenance: state.GitProvenance{Operation: "test.discovery", Event: expireOperationID}}}, nil
	}, func(state.Snapshot) (state.TransactionResolution, error) {
		return state.TransactionResolution{Found: true, Value: cursor}, nil
	}); err != nil {
		t.Fatalf("expire fixture cursor lease: %v", err)
	}

	result, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, coordinator, catalog.Snapshot{}, options, func(_ context.Context, module ModuleCycle) (string, error) {
		deliveries++
		if !module.ProjectionRequired {
			t.Fatal("receipt replay lost the module projection requirement")
		}
		referenceRecord, exists := module.Snapshot.Records[CatalogProposalReferencePath]
		if !exists || referenceRecord.Kind != "export" {
			t.Fatal("receipt replay omitted the durable catalog proposal reference")
		}
		var reference CatalogProposalReference
		if err := json.Unmarshal(referenceRecord.Data, &reference); err != nil || reference.Generation != module.Accepted.Generation || reference.ProposalID == "" {
			t.Fatalf("catalog proposal reference = %+v, decode error = %v", reference, err)
		}
		return module.Head, nil
	}, "receipt-lag-resume-run", cursorID, time.Time{})
	if err != nil || !result.ContinuationComplete || deliveries != 1 {
		t.Fatalf("receipt replay did not finish the saved module: result=%+v deliveries=%d err=%v", result, deliveries, err)
	}
	if sourceReads.Load() != readsBeforeResume {
		t.Fatalf("receipt replay repeated source resolution: before=%d after=%d", readsBeforeResume, sourceReads.Load())
	}
}

func TestContinuationSkipNeverSuppressesNewReleaseOrForce(t *testing.T) {
	for _, scenario := range []string{"new_release", "force"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newSkipCycleFixture(t, scenario == "new_release")
			if scenario == "force" {
				fixture.options.ForceModuleIDs = fixture.options.SelectedModuleIDs
			}
			result, err := ContinueCycle(context.Background(), fixture.resolver, fixture.backend, discoveryTestCoordinator{fixture.backend}, fixture.candidate, fixture.options, fixture.deliver, "changed-run", "", time.Time{}, fixture.skip)
			fixture.assertComplete(t, result, err)
			total := len(fixture.options.SelectedModuleIDs)
			if fixture.skipChecks != 0 || fixture.deliveries != total || fixture.backend.pending != total || len(result.SkippedModuleIDs) != 0 {
				t.Fatalf("new or forced source skipped: %+v", result)
			}
			if scenario == "new_release" && len(result.ChangedModuleIDs) != total {
				t.Fatal("new releases were not counted")
			}
		})
	}
}

func TestContinuationSkipDoesNotReplayPendingSource(t *testing.T) {
	fixture := newSkipCycleFixture(t, false)
	deliver := func(ctx context.Context, module ModuleCycle) (string, error) {
		head, err := fixture.deliver(ctx, module)
		if fixture.deliveries == 1 {
			return head, &ratelimit.DeferredError{Resource: "fixture", RetryAt: time.Now().Add(-time.Second)}
		}
		return head, err
	}
	skip := func(context.Context, ModuleCycle) (bool, error) { fixture.skipChecks++; return false, nil }
	result, err := ContinueCycle(context.Background(), fixture.resolver, fixture.backend, discoveryTestCoordinator{fixture.backend}, fixture.candidate, fixture.options, deliver, "pending-run", "", time.Time{}, skip)
	if err != nil || !result.Deferred {
		t.Fatalf("pending delivery did not park: %+v %v", result, err)
	}
	checksBefore := fixture.releaseChecks
	result, err = ContinueCycle(context.Background(), fixture.resolver, fixture.backend, discoveryTestCoordinator{fixture.backend}, catalog.Snapshot{}, fixture.options, deliver, "resume-run", result.ContinuationID, time.Time{}, skip)
	fixture.assertComplete(t, result, err)
	if fixture.releaseChecks != checksBefore+1 || fixture.skipChecks != len(fixture.options.SelectedModuleIDs) {
		t.Fatal("pending resume repeated source resolution or offered pending work to skip")
	}
}

func TestContinuationSkipRejectsConcurrentHeadMovement(t *testing.T) {
	fixture := newSkipCycleFixture(t, false)
	skip := func(ctx context.Context, module ModuleCycle) (bool, error) {
		skipped, err := fixture.skip(ctx, module)
		fixture.backend.head = "concurrent-head"
		return skipped, err
	}
	result, err := ContinueCycle(context.Background(), fixture.resolver, fixture.backend, discoveryTestCoordinator{fixture.backend}, fixture.candidate, fixture.options, fixture.deliver, "stale-run", "", time.Time{}, skip)
	if !errors.Is(err, state.ErrHeadMoved) || fixture.deliveries != 0 {
		t.Fatalf("stale skip advanced: %+v %v", result, err)
	}
	cursor, err := ReadContinuation(fixture.backend.snapshot, result.ContinuationID)
	if err != nil || cursor.Next != 0 || cursor.PendingDelivery {
		t.Fatalf("failed skip changed durable cursor: %+v %v", cursor, err)
	}
}

func TestContinuationFlushesReadOnlySkipsBeforeDelivery(t *testing.T) {
	fixture := newSkipCycleFixture(t, false)
	skip := func(ctx context.Context, module ModuleCycle) (bool, error) {
		if module.Position == 1 {
			return fixture.skip(ctx, module)
		}
		return false, nil
	}
	deliver := func(ctx context.Context, module ModuleCycle) (string, error) {
		cursor, err := ReadContinuation(fixture.backend.snapshot, fixture.backendCycleID(t))
		if err != nil || cursor.Next != 1 || !cursor.PendingDelivery {
			t.Fatalf("delivery started before skip flush/pending checkpoint: %+v %v", cursor, err)
		}
		return fixture.deliver(ctx, module)
	}
	result, err := ContinueCycle(context.Background(), fixture.resolver, fixture.backend, discoveryTestCoordinator{fixture.backend}, fixture.candidate, fixture.options, deliver, "mixed-run", "", time.Time{}, skip)
	fixture.assertComplete(t, result, err)
	if fixture.deliveries != 1 || len(result.SkippedModuleIDs) != 1 || len(result.DeliveredModuleIDs) != 1 {
		t.Fatalf("mixed cycle counts incorrect: %+v", result)
	}
}

func (fixture *skipCycleFixture) backendCycleID(t *testing.T) string {
	t.Helper()
	for _, record := range fixture.backend.snapshot.Records {
		if record.Kind == "discovery_cycle" {
			var cursor Continuation
			if err := json.Unmarshal(record.Data, &cursor); err != nil {
				t.Fatal(err)
			}
			return cursor.ID
		}
	}
	t.Fatal("cycle missing")
	return ""
}

func TestContinuationDeadlineParksBeforeLaterModule(t *testing.T) {
	now := time.Now().UTC()
	backend := newDiscoveryMemoryBackend()
	selected := []string{"avm-res-resources-resourcegroup"}
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: selected, Producer: discoveryTestProducer()}
	result, err := ContinueCycle(context.Background(), source.Resolver{}, backend, testTransactionCoordinator(backend), discoveryTestSnapshot(now), options, func(context.Context, ModuleCycle) (string, error) {
		t.Fatal("deadline invoked module delivery")
		return "", nil
	}, "bounded-run", "", now.Add(-time.Second))
	if err != nil || !result.Deferred || countRecordsWithKind(backend.snapshot, "request") != 0 {
		t.Fatalf("deadline did not park cleanly: %+v %v", result, err)
	}
}

func TestContinuationMissingColdResumeNeverObservesCatalog(t *testing.T) {
	backend := newDiscoveryMemoryBackend()
	id := "discovery-cycle-" + strings.Repeat("a", 64)
	_, err := ContinueCycle(context.Background(), source.Resolver{}, backend, testTransactionCoordinator(backend), catalog.Snapshot{}, Options{}, func(context.Context, ModuleCycle) (string, error) {
		t.Fatal("missing continuation attempted delivery")
		return "", nil
	}, "resume-run", id, time.Time{})
	if err == nil || countRecordsWithKind(backend.snapshot, "discovery_observation") != 0 || countRecordsWithKind(backend.snapshot, "request") != 0 {
		t.Fatalf("missing cold resume created new work: %v", err)
	}
}

func TestContinuationRejectsCorruptFrozenScope(t *testing.T) {
	now := time.Now().UTC()
	base := Continuation{SchemaVersion: 1, ID: "discovery-cycle-" + strings.Repeat("a", 64), RunID: "run", OwnerRunID: "run", OwnerGeneration: 1, Status: "running", LeaseExpiresAt: now.Add(time.Minute), Options: Options{SelectedModuleIDs: []string{"avm-res-one", "avm-res-two"}}, Result: Result{SelectedModuleIDs: []string{"avm-res-one", "avm-res-two"}, CompletedModuleIDs: []string{"avm-res-one"}}, Next: 1}
	for _, mutation := range []string{"duplicate", "prefix", "options", "force", "terminal"} {
		cursor := base
		cursor.Result.SelectedModuleIDs = append([]string(nil), base.Result.SelectedModuleIDs...)
		cursor.Options.SelectedModuleIDs = append([]string(nil), base.Options.SelectedModuleIDs...)
		switch mutation {
		case "duplicate":
			cursor.Result.SelectedModuleIDs[1] = "avm-res-one"
		case "prefix":
			cursor.Result.CompletedModuleIDs = []string{"avm-res-two"}
		case "options":
			cursor.Options.SelectedModuleIDs[1] = "avm-res-one"
		case "force":
			cursor.Options.ForceModuleIDs = []string{"avm-res-other"}
		case "terminal":
			cursor.Status = "complete"
		}
		if err := validateContinuationScope(cursor); err == nil {
			t.Fatalf("accepted corrupt continuation: %s", mutation)
		}
	}
}

type parkFailureBackend struct{ *memoryBackend }

func TestParkingRetriesOnAdvancedHeadAndReportsDurableDeferral(t *testing.T) {
	now := time.Now().UTC()
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}, Producer: discoveryTestProducer()}
	result, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, testTransactionCoordinator(backend), discoveryTestSnapshot(now), options, func(context.Context, ModuleCycle) (string, error) {
		expectedHead := backend.head
		backend.head = "concurrent-writer-head"
		return expectedHead, &ratelimit.DeferredError{Resource: "rest", RetryAt: now.Add(time.Minute)}
	}, "parking-stale", "", time.Time{})
	if err != nil || !result.DeferralSaved || !result.Deferred || !result.ContinuationDurable {
		t.Fatalf("parking did not retry and save durable deferral: %+v %v", result, err)
	}
	_, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := ReadContinuation(snapshot, result.ContinuationID)
	if err != nil || cursor.Status != "deferred" || cursor.OwnerGeneration != 1 {
		t.Fatalf("deferred continuation was not durable after head movement: %+v %v", cursor, err)
	}
}

type parkingCapacityBackend struct {
	*memoryBackend
	pacer         *ratelimit.Pacer
	parkingWrites int
}

func (backend *parkingCapacityBackend) Commit(ctx context.Context, head string, snapshot state.Snapshot) (string, error) {
	for _, record := range snapshot.Records {
		if record.Kind != "discovery_cycle" {
			continue
		}
		var cursor Continuation
		if err := json.Unmarshal(record.Data, &cursor); err != nil {
			return "", err
		}
		if cursor.Status != "deferred" {
			continue
		}
		for range 3 {
			if err := backend.pacer.Before(ctx, ratelimit.Request{Resource: "rest", Points: 5, Mutation: true}); err != nil {
				return "", err
			}
			backend.parkingWrites++
		}
	}
	return backend.memoryBackend.Commit(ctx, head, snapshot)
}

func TestContinuationParkingRetainsCapacityAfterOrdinaryCheckpointDefers(t *testing.T) {
	now := time.Now().UTC()
	backend := &parkingCapacityBackend{memoryBackend: newDiscoveryMemoryBackend(), pacer: ratelimit.New()}
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}, Producer: discoveryTestProducer()}
	result, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, testTransactionCoordinator(backend), discoveryTestSnapshot(now), options, func(ctx context.Context, module ModuleCycle) (string, error) {
		backend.pacer.Seed("rest", 11, 5000, time.Now().Add(time.Hour))
		return module.Head, backend.pacer.Before(ratelimit.CheckpointContext(ctx), ratelimit.Request{Resource: "rest", Points: 5, Mutation: true})
	}, "parking-capacity", "", time.Time{})
	if err != nil || !result.Deferred || !result.DeferralSaved || backend.parkingWrites != 3 {
		t.Fatalf("parking did not persist after checkpoint rejection: %+v writes=%d error=%v", result, backend.parkingWrites, err)
	}
	cursor, err := ReadContinuation(backend.snapshot, result.ContinuationID)
	if err != nil || cursor.Status != "deferred" || !cursor.PendingDelivery || cursor.Next != 0 || !cursor.Result.DeferralSaved {
		t.Fatalf("parking lost pending phase or durable proof: %+v %v", cursor, err)
	}
}

func (backend parkFailureBackend) Commit(ctx context.Context, head string, snapshot state.Snapshot) (string, error) {
	for _, record := range snapshot.Records {
		if record.Kind == "discovery_cycle" {
			var cursor Continuation
			_ = json.Unmarshal(record.Data, &cursor)
			if cursor.Status == "deferred" {
				return "", errors.New("checkpoint fixture failure")
			}
		}
	}
	return backend.memoryBackend.Commit(ctx, head, snapshot)
}

func TestContinuationFailedParkingRetainsOriginalCause(t *testing.T) {
	now := time.Now().UTC()
	backend := parkFailureBackend{newDiscoveryMemoryBackend()}
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}, Producer: discoveryTestProducer()}
	origin := errors.New("origin fixture error")
	result, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, testTransactionCoordinator(backend), discoveryTestSnapshot(now), options, func(context.Context, ModuleCycle) (string, error) {
		return backend.head, errors.Join(origin, &ratelimit.DeferredError{Resource: "rest", RetryAt: now.Add(time.Hour)})
	}, "failed-park", "", time.Time{})
	if !errors.Is(err, origin) || result.Deferred || result.DeferralSaved || !result.ContinuationDurable || result.ContinuationID == "" {
		t.Fatalf("parking lost failure/durability: %+v %v", result, err)
	}
	var parking *ParkingError
	if !errors.As(err, &parking) || errors.Is(parking.Cause, origin) {
		t.Fatalf("parking error did not isolate checkpoint failure: %v", err)
	}
}

func TestContinuationFailedDeadlineParkingRetainsTimeout(t *testing.T) {
	now := time.Now().UTC()
	backend := parkFailureBackend{newDiscoveryMemoryBackend()}
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}, Producer: discoveryTestProducer()}
	result, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, testTransactionCoordinator(backend), discoveryTestSnapshot(now), options, func(context.Context, ModuleCycle) (string, error) { return backend.head, context.DeadlineExceeded }, "deadline-park", "", time.Time{})
	if !errors.Is(err, context.DeadlineExceeded) || result.Deferred || !result.ContinuationDurable || result.ContinuationID == "" {
		t.Fatalf("deadline parking lost timeout/durability: %+v %v", result, err)
	}
}
