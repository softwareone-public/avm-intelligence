package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestSelectDispatchableRequestsUsesPhasePriorityAgeAndStableModuleID(t *testing.T) {
	baseTime := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	assessmentB := dispatchAssessment(t, "avm-res-b", "azurerm-4", 1, baseTime)
	assessmentA := dispatchAssessment(t, "avm-res-a", "azurerm-4", 1, baseTime)
	acquisition := dispatchAcquisition(t, "avm-res-z", 'e', 1, baseTime.Add(time.Minute))
	deferred := dispatchAssessment(t, "avm-res-c", "azurerm-4", 1, baseTime.Add(-time.Minute))
	deferred.Work.RetryClass = RetryTransient
	deferred.Work.RetryAt = baseTime.Add(time.Minute)
	deferred.UpdatedAt = baseTime
	if err := deferred.Validate(); err != nil {
		t.Fatal(err)
	}

	ordered, err := SelectDispatchableRequests([]Request{assessmentB, deferred, acquisition, assessmentA}, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{acquisition.ID, assessmentA.ID, assessmentB.ID}
	if len(ordered) != len(want) {
		t.Fatalf("dispatchable request count = %d, want %d", len(ordered), len(want))
	}
	for index, request := range ordered {
		if request.ID != want[index] {
			t.Errorf("dispatch order[%d] = %s, want %s", index, request.ID, want[index])
		}
	}
}

func TestClaimModuleRequestSelectsOnlyThatModulesOldestReadyWork(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	olderAssessment := dispatchAssessment(t, "avm-res-target", "azurerm-4", 1, now.Add(-time.Minute))
	newerAcquisition := dispatchAcquisition(t, "avm-res-target", 'b', 1, now)
	otherModule := dispatchAcquisition(t, "avm-res-other", 'c', 1, now.Add(-time.Hour))
	backend := seedDispatchBackend(t, olderAssessment, newerAcquisition, otherModule)

	claim, err := ClaimModule(context.Background(), backend, "avm-res-target", "manual-dispatcher", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Request.ID != newerAcquisition.ID || claim.Request.ModuleID != "avm-res-target" {
		t.Fatalf("module claim = %s/%s, want the target module's acquisition", claim.Request.ModuleID, claim.Request.ID)
	}
	if _, err = ClaimModule(context.Background(), backend, "avm-res-target", "manual-dispatcher", now, time.Minute); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("second module claim error = %v, want ErrNoDispatchableWork", err)
	}
	otherClaim, err := ClaimModule(context.Background(), backend, "avm-res-other", "manual-dispatcher", now, time.Minute)
	if err != nil || otherClaim.Request.ID != otherModule.ID {
		t.Fatalf("other module claim = %#v, err = %v", otherClaim.Request, err)
	}
	if _, err = ClaimModule(context.Background(), backend, "avm-res-missing", "manual-dispatcher", now, time.Minute); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("missing module claim error = %v, want ErrNoDispatchableWork", err)
	}
	if _, err = ClaimModule(context.Background(), backend, "not-a-module", "manual-dispatcher", now, time.Minute); err == nil {
		t.Fatal("malformed module ID was accepted")
	}
}

func TestClaimNextUsesVerifiedSnapshotWithoutSecondContentRead(t *testing.T) {
	now := time.Date(2026, 10, 7, 13, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-verified-claim", 'd', 1, now)
	origin := seedDispatchBackend(t, request)
	if _, err := state.MigrateModuleLayout(context.Background(), origin); err != nil {
		t.Fatal(err)
	}
	origin.reads = 0
	verified := &state.VerifiedReadBackend{Backend: origin}
	claim, err := ClaimNext(context.Background(), verified, "dispatcher-run", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Request.ID != request.ID || origin.reads != 1 {
		t.Fatalf("claim reads = %d, request = %s; want one content read", origin.reads, claim.Request.ID)
	}
}

func TestClaimNextPersistsAttemptBeforeDispatchAndUnknownLookupKeepsClaim(t *testing.T) {
	now := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
	acquisition := dispatchAcquisition(t, "avm-res-claim", 'a', 1, now)
	assessment := dispatchAssessment(t, "avm-res-claim", "azurerm-4", 1, now)
	backend := seedDispatchBackend(t, acquisition, assessment)

	claim, err := ClaimNext(context.Background(), backend, "dispatcher-run-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Request.ID != acquisition.ID || claim.Attempt.ID == "" || claim.Attempt.Status != AttemptClaimed || claim.Attempt.RunID != "" {
		t.Fatalf("claim = %#v, want persisted pre-dispatch acquisition attempt", claim)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FencingTokens[acquisition.ModuleID] != 1 || snapshot.Records[attemptPath(claim.Attempt)].Kind != "attempt" {
		t.Fatal("claim did not atomically persist attempt and fencing high-water mark")
	}
	if _, err = ClaimNext(context.Background(), backend, "dispatcher-run-2", now, time.Minute); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("second same-module claim error = %v, want ErrNoDispatchableWork", err)
	}

	result, err := RecoverAttempt(context.Background(), backend, claim.Attempt.ID, now.Add(2*time.Minute), ActionsObservation{State: RunUnknown})
	if err != nil || result.Changed {
		t.Fatalf("unknown run recovery = %#v, err = %v", result, err)
	}
	_, afterUnknown, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if afterUnknown.FencingTokens[acquisition.ModuleID] != 1 || afterUnknown.Records[attemptPath(claim.Attempt)].Kind != "attempt" {
		t.Fatal("unknown lookup released or altered the active claim")
	}
}

func TestClaimRequestPersistsOnlyNamedRequest(t *testing.T) {
	now := time.Date(2026, 10, 4, 18, 0, 0, 0, time.UTC)
	unrelated := dispatchAcquisition(t, "avm-res-a-unrelated", 'a', 1, now)
	target := dispatchAcquisition(t, "avm-res-z-target", 'b', 1, now)
	backend := seedDispatchBackend(t, unrelated, target)
	_, before, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedPath := stateRequestPath + unrelated.ID + ".json"
	unrelatedBefore := append([]byte(nil), before.Records[unrelatedPath].Data...)

	claim, err := ClaimRequest(context.Background(), backend, target.ID, "dispatcher-target-only", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Request.ID != target.ID || claim.Request.Status != StatusClaimed {
		t.Fatalf("targeted claim = %#v, want only request %s claimed", claim.Request, target.ID)
	}
	_, after, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Records[unrelatedPath].Data; !bytes.Equal(got, unrelatedBefore) {
		t.Fatalf("unrelated request changed during targeted claim: got %s, want %s", got, unrelatedBefore)
	}
	for _, record := range after.Records {
		if record.Kind != "attempt" {
			continue
		}
		var attempt Attempt
		if err = json.Unmarshal(record.Data, &attempt); err != nil {
			t.Fatal(err)
		}
		if attempt.RequestID != target.ID {
			t.Fatalf("targeted claim created unrelated attempt %s for request %s", attempt.ID, attempt.RequestID)
		}
	}
}

func TestInspectAttemptReadsCurrentBindingWithoutMutation(t *testing.T) {
	now := time.Date(2026, 10, 2, 14, 30, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-inspect", 'a', 1, now)
	backend := seedDispatchBackend(t, request)
	claim, err := ClaimNext(context.Background(), backend, "dispatcher-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindActionsRun(context.Background(), backend, claim.Attempt.ID, "actions-run-inspect", 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	inspected, err := InspectAttempt(context.Background(), backend, claim.Attempt.ID)
	if err != nil || inspected.Changed || inspected.Head != bound.Head || inspected.Attempt.RunID != "actions-run-inspect" {
		t.Fatalf("inspection = %#v, err = %v", inspected, err)
	}
}

func TestBindActionsRunIsIdempotentAndRejectsDuplicateOwner(t *testing.T) {
	now := time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-bind", 'a', 1, now)
	backend := seedDispatchBackend(t, request)
	claim, err := ClaimNext(context.Background(), backend, "dispatcher-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	bound, err := BindActionsRun(context.Background(), backend, claim.Attempt.ID, "actions-run-1", 1, now.Add(time.Second))
	if err != nil || !bound.Changed || bound.Attempt.Status != AttemptDispatched || bound.Request.Status != StatusRunning {
		t.Fatalf("bound run = %#v, err = %v", bound, err)
	}
	retry, err := BindActionsRun(context.Background(), backend, claim.Attempt.ID, "actions-run-1", 1, now.Add(2*time.Second))
	if err != nil || retry.Changed || retry.Head != bound.Head {
		t.Fatalf("same-run bind was not idempotent: %#v, err = %v", retry, err)
	}
	if _, err = BindActionsRun(context.Background(), backend, claim.Attempt.ID, "actions-run-duplicate", 1, now.Add(3*time.Second)); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("duplicate run bind error = %v, want ErrAttemptConflict", err)
	}
}

func TestHeartbeatAndConfirmedAbsentRecoveryPreserveFenceHighWater(t *testing.T) {
	now := time.Date(2026, 10, 2, 16, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-recover", 'a', 1, now)
	backend := seedDispatchBackend(t, request)
	claim, err := ClaimNext(context.Background(), backend, "dispatcher-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindActionsRun(context.Background(), backend, claim.Attempt.ID, "actions-run-1", 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err := Heartbeat(context.Background(), backend, claim.Attempt.ID, "actions-run-1", claim.Attempt.Fence, now.Add(20*time.Second), now.Add(2*time.Minute))
	if err != nil || !heartbeat.Changed || !heartbeat.Attempt.LeaseExpiresAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("heartbeat = %#v, err = %v", heartbeat, err)
	}
	if _, err = Heartbeat(context.Background(), backend, claim.Attempt.ID, "actions-run-1", claim.Attempt.Fence+1, now.Add(30*time.Second), now.Add(3*time.Minute)); !errors.Is(err, ErrAttemptOwnership) {
		t.Fatalf("stale heartbeat error = %v, want ErrAttemptOwnership", err)
	}

	unknown, err := RecoverAttempt(context.Background(), backend, claim.Attempt.ID, now.Add(3*time.Minute), ActionsObservation{State: RunUnknown})
	if err != nil || unknown.Changed || unknown.Request.Status != StatusRunning {
		t.Fatalf("unknown recovery released run: %#v, err = %v", unknown, err)
	}
	absent, err := RecoverAttempt(context.Background(), backend, claim.Attempt.ID, now.Add(3*time.Minute), ActionsObservation{State: RunAbsent})
	if err != nil || !absent.Changed || absent.Attempt.Status != AttemptReclaimed || absent.Request.Status != StatusQueued || absent.Request.Work.Fence != claim.Attempt.Fence {
		t.Fatalf("confirmed absent recovery = %#v, err = %v", absent, err)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.FencingTokens[request.ModuleID] != claim.Attempt.Fence {
		t.Fatalf("fencing high-water mark = %d, want %d", snapshot.FencingTokens[request.ModuleID], claim.Attempt.Fence)
	}
	secondClaim, err := ClaimNext(context.Background(), backend, "dispatcher-2", now.Add(3*time.Minute), time.Minute)
	if err != nil || secondClaim.Attempt.Fence != claim.Attempt.Fence+1 {
		t.Fatalf("reclaimed work did not advance fence: %#v, err = %v", secondClaim, err)
	}
	_ = bound
}

func TestCompetingDispatchersPersistAtMostOneModuleClaim(t *testing.T) {
	now := time.Date(2026, 10, 2, 17, 0, 0, 0, time.UTC)
	requests := []Request{
		dispatchAcquisition(t, "avm-res-race", 'a', 1, now),
		dispatchAssessment(t, "avm-res-race", "azurerm-4", 1, now.Add(time.Second)),
	}
	backend := seedDispatchBackend(t, requests...)
	claimErrors := raceDispatchers(backend, now)
	assertSingleSuccessfulClaim(t, claimErrors)
	assertOnePersistedModuleClaim(t, backend)
}

func raceDispatchers(backend *dispatcherMemoryBackend, now time.Time) []error {
	var waitGroup sync.WaitGroup
	results := make(chan error, 2)
	for _, dispatcher := range []string{"dispatcher-a", "dispatcher-b"} {
		dispatcher := dispatcher
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := ClaimNext(context.Background(), backend, dispatcher, now, time.Minute)
			results <- err
		}()
	}
	waitGroup.Wait()
	close(results)
	claimErrors := make([]error, 0, 2)
	for err := range results {
		claimErrors = append(claimErrors, err)
	}
	return claimErrors
}

func assertSingleSuccessfulClaim(t *testing.T, claimErrors []error) {
	t.Helper()
	claimed := 0
	for _, err := range claimErrors {
		if err == nil {
			claimed++
			continue
		}
		if !errors.Is(err, state.ErrHeadMoved) && !errors.Is(err, ErrNoDispatchableWork) {
			t.Fatalf("unexpected competing claim error: %v", err)
		}
	}
	if claimed != 1 {
		t.Fatalf("successful claims = %d, want exactly one", claimed)
	}
}

func assertOnePersistedModuleClaim(t *testing.T, backend *dispatcherMemoryBackend) {
	t.Helper()
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	activeClaims := 0
	for _, record := range snapshot.Records {
		if record.Kind != "request" {
			continue
		}
		var request Request
		if err = json.Unmarshal(record.Data, &request); err != nil {
			t.Fatal(err)
		}
		if request.Status == StatusClaimed || request.Status == StatusRunning {
			activeClaims++
		}
	}
	if activeClaims != 1 {
		t.Fatalf("persisted active claims = %d, want one", activeClaims)
	}
}

func TestCompletedActionsRunWithoutSealedCompletionIsRequeued(t *testing.T) {
	now := time.Date(2026, 10, 2, 18, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-completed", 'a', 1, now)
	backend := seedDispatchBackend(t, request)
	claim, err := ClaimNext(context.Background(), backend, "dispatcher-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = BindActionsRun(context.Background(), backend, claim.Attempt.ID, "actions-run-completed", 1, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverAttempt(context.Background(), backend, claim.Attempt.ID, now.Add(2*time.Second), ActionsObservation{
		State: RunCompleted, RunID: "actions-run-completed", RunAttempt: 1,
	})
	if err != nil || !recovered.Changed || recovered.Attempt.Status != AttemptCompleted || recovered.Request.Status != StatusQueued || recovered.Request.Work.RetryClass != RetryTransient {
		t.Fatalf("completed run recovery = %#v, err = %v", recovered, err)
	}
	replay, err := BindActionsRun(context.Background(), backend, claim.Attempt.ID, "actions-run-completed", 1, now.Add(3*time.Second))
	if err != nil || replay.Changed || replay.Head != recovered.Head {
		t.Fatalf("completed run bind replay = %#v, err = %v", replay, err)
	}
}

func TestCompletedRunFoundBeforeBindingIsRecoveredByAttemptIdentity(t *testing.T) {
	now := time.Date(2026, 10, 2, 19, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-bind-crash", 'a', 1, now)
	backend := seedDispatchBackend(t, request)
	claim, err := ClaimNext(context.Background(), backend, "dispatcher-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverAttempt(context.Background(), backend, claim.Attempt.ID, now.Add(2*time.Second), ActionsObservation{
		State: RunCompleted, RunID: "actions-run-after-crash", RunAttempt: 1,
	})
	if err != nil || !recovered.Changed || recovered.Attempt.Status != AttemptCompleted || recovered.Attempt.RunID != "actions-run-after-crash" || recovered.Request.Status != StatusQueued {
		t.Fatalf("post-crash completed run recovery = %#v, err = %v", recovered, err)
	}
}

func dispatchAssessment(t *testing.T, moduleID, contextID string, generation uint64, createdAt time.Time) Request {
	t.Helper()
	return assessmentRequestForModule(t, moduleID, contextID, generation, createdAt)
}

func dispatchAcquisition(t *testing.T, moduleID string, sourceByte byte, generation uint64, createdAt time.Time) Request {
	t.Helper()
	request, err := NewAcquireSourceRequest(AcquisitionInput{
		ModuleID: moduleID, SourceSHA: repeat(sourceByte), DesiredGeneration: generation,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func assessmentRequestForModule(t *testing.T, moduleID, contextID string, generation uint64, createdAt time.Time) Request {
	t.Helper()
	request, err := NewAssessmentRequest(AssessmentInput{
		ModuleID: moduleID, SourceSHA: repeat('a'), ContextID: contextID, Track: contextID,
		DesiredGeneration: generation, FactsID: "facts-" + repeatDigest('b'),
		DefinitionIDs: []string{"definition-" + repeatDigest('c')},
	}, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

type dispatcherMemoryBackend struct {
	mu       sync.Mutex
	head     string
	snapshot state.Snapshot
	reads    int
	commits  int
}

func seedDispatchBackend(t *testing.T, requests ...Request) *dispatcherMemoryBackend {
	t.Helper()
	backend := &dispatcherMemoryBackend{head: "dispatch-head-0", snapshot: state.NewSnapshot()}
	events := make([]state.Event, 0, len(requests))
	for _, request := range requests {
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, state.Event{
			ID: "seed-" + request.ID, RecordPath: "state/requests/" + request.ID + ".json",
			Record: state.Record{Kind: "request", SchemaVersion: 1, Data: encoded},
		})
	}
	if _, _, err := state.ApplyEventsAtHead(context.Background(), backend, backend.head, events); err != nil {
		t.Fatal(err)
	}
	return backend
}

func (backend *dispatcherMemoryBackend) Head(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.head, nil
}

func (backend *dispatcherMemoryBackend) Read(ctx context.Context) (string, state.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return "", state.Snapshot{}, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.commits++
	backend.reads++
	return backend.head, cloneDispatchSnapshot(backend.snapshot), nil
}

func (backend *dispatcherMemoryBackend) Commit(ctx context.Context, expectedHead string, snapshot state.Snapshot) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.head != expectedHead {
		return "", state.ErrHeadMoved
	}
	backend.head = fmt.Sprintf("dispatch-head-%s", expectedHead[len("dispatch-head-"):]) + "-next"
	backend.snapshot = cloneDispatchSnapshot(snapshot)
	return backend.head, nil
}

func cloneDispatchSnapshot(snapshot state.Snapshot) state.Snapshot {
	cloned := state.NewSnapshot()
	cloned.Generation = snapshot.Generation
	for key, value := range snapshot.AppliedEvents {
		cloned.AppliedEvents[key] = value
	}
	for key, value := range snapshot.FencingTokens {
		cloned.FencingTokens[key] = value
	}
	for key, value := range snapshot.Records {
		value.Data = append(json.RawMessage(nil), value.Data...)
		cloned.Records[key] = value
	}
	return cloned
}
