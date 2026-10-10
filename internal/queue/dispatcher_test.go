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

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
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
	seedProjectionBarrier(t, backend, newerAcquisition, now, true, false)

	coordinator := newQueueTestCoordinator(backend)
	claim, err := ClaimModule(context.Background(), coordinator, "avm-res-target", "manual-dispatcher", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Request.ID != newerAcquisition.ID || claim.Request.ModuleID != "avm-res-target" {
		t.Fatalf("module claim = %s/%s, want the target module's acquisition", claim.Request.ModuleID, claim.Request.ID)
	}
	_, beforeReplay, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := ClaimModule(context.Background(), coordinator, "avm-res-target", "manual-dispatcher", now, time.Minute)
	if err != nil || replayed.Attempt.ID != claim.Attempt.ID || replayed.Request.ID != claim.Request.ID {
		t.Fatalf("same-operation claim replay = %#v, err = %v; want original claim %s", replayed, err, claim.Attempt.ID)
	}
	_, afterReplay, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if afterReplay.Generation != beforeReplay.Generation {
		t.Fatalf("same-operation replay advanced generation from %d to %d", beforeReplay.Generation, afterReplay.Generation)
	}
	otherClaim, err := ClaimModule(context.Background(), coordinator, "avm-res-other", "manual-dispatcher", now, time.Minute)
	if err != nil || otherClaim.Request.ID != otherModule.ID {
		t.Fatalf("other module claim = %#v, err = %v", otherClaim.Request, err)
	}
	if _, err = ClaimModule(context.Background(), coordinator, "avm-res-missing", "manual-dispatcher", now, time.Minute); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("missing module claim error = %v, want ErrNoDispatchableWork", err)
	}
	if _, err = ClaimModule(context.Background(), coordinator, "not-a-module", "manual-dispatcher", now, time.Minute); err == nil {
		t.Fatal("malformed module ID was accepted")
	}
}

func TestDispatchClaimAcquiresReleaseSlotAndQueuesNewerVersion(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	current := dispatchAcquisition(t, "avm-res-slot", 'a', 1, now)
	queued := dispatchAcquisition(t, "avm-res-slot", 'b', 2, now.Add(time.Second))
	queued.Work.AVMReleaseVersion = "2.0.0"
	queued.Fingerprint, _ = workFingerprint(queued.ModuleID, queued.SourceSHA, *queued.Work)
	queued.ID = requestID(queued.ModuleID, queued.Fingerprint)
	if err := queued.Validate(); err != nil {
		t.Fatal(err)
	}
	backend := seedDispatchBackend(t, current, queued)
	claim, err := ClaimModule(context.Background(), newQueueTestCoordinator(backend), current.ModuleID, "slot-dispatcher", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	slot, exists, err := lifecycleSlotFromSnapshot(snapshot, current.ModuleID)
	if err != nil || !exists || slot.ReleaseVersion != "1.0.0" || slot.RequestID != claim.Request.ID {
		t.Fatalf("acquired lifecycle slot = %+v, exists=%t, err=%v", slot, exists, err)
	}
	newGeneration := cloneRequest(current)
	newGeneration.Work.DesiredGeneration++
	allowed, err := slotAllowsRequest(snapshot, newGeneration)
	if err != nil || !allowed {
		t.Fatalf("accepted same-release provider-input generation could not advance held slot: allowed=%t err=%v", allowed, err)
	}
	advancedEvent, err := lifecycleSlotEvent(snapshot, newGeneration, "selection-generation-advance", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("prepare slot generation advance: %v", err)
	}
	var advanced LifecycleSlot
	if err = json.Unmarshal(advancedEvent.Record.Data, &advanced); err != nil || advanced.ReleaseVersion != "1.0.0" || advanced.DesiredGeneration != newGeneration.Work.DesiredGeneration {
		t.Fatalf("slot generation advance = %+v err=%v", advanced, err)
	}
	staleGeneration := cloneRequest(current)
	staleGeneration.Work.DesiredGeneration--
	if allowed, err = slotAllowsRequest(snapshot, staleGeneration); err != nil || allowed {
		t.Fatalf("older desired generation remained dispatchable: allowed=%t err=%v", allowed, err)
	}
	if _, err = ClaimModule(context.Background(), newQueueTestCoordinator(backend), current.ModuleID, "slot-dispatcher-next", now.Add(2*time.Minute), time.Minute); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("new release dispatched while slot is held: %v", err)
	}
}

func TestClaimModuleEnforcesOldestQueuedAVMReleaseBeforeFirstSlot(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	older := dispatchAcquisition(t, "avm-res-release-order", 'a', 1, now.Add(time.Minute))
	newer := dispatchAcquisition(t, "avm-res-release-order", 'b', 2, now)
	older.Work.AVMReleaseVersion = "1.2.0"
	older.Fingerprint, _ = workFingerprint(older.ModuleID, older.SourceSHA, *older.Work)
	older.ID = requestID(older.ModuleID, older.Fingerprint)
	newer.Work.AVMReleaseVersion = "1.10.0"
	newer.Fingerprint, _ = workFingerprint(newer.ModuleID, newer.SourceSHA, *newer.Work)
	newer.ID = requestID(newer.ModuleID, newer.Fingerprint)
	for _, request := range []Request{older, newer} {
		if err := request.Validate(); err != nil {
			t.Fatalf("invalid queued release %s: %v", request.Work.AVMReleaseVersion, err)
		}
	}
	backend := seedDispatchBackend(t, newer, older)
	claim, err := ClaimModule(context.Background(), newQueueTestCoordinator(backend), older.ModuleID, "release-order-dispatcher", now.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Request.ID != older.ID {
		t.Fatalf("first release claim = %s (%s), want oldest release %s", claim.Request.ID, claim.Request.Work.AVMReleaseVersion, older.ID)
	}

	delayedOlder := cloneRequest(older)
	delayedOlder.Status = StatusQueued
	delayedOlder.Work.RetryClass = RetryTransient
	delayedOlder.Work.RetryAt = now.Add(time.Hour)
	delayedOlder.UpdatedAt = now.Add(3 * time.Minute)
	if err = delayedOlder.Validate(); err != nil {
		t.Fatal(err)
	}
	backend = seedDispatchBackend(t, newer, delayedOlder)
	if _, err = ClaimModule(context.Background(), newQueueTestCoordinator(backend), older.ModuleID, "release-order-retry", now.Add(4*time.Minute), time.Minute); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("newer release claimed while oldest release awaits retry: %v", err)
	}
}

func TestLifecycleSlotRejectsUnreleasedPrereleaseVersion(t *testing.T) {
	for _, version := range []string{"v1.0.0-rc.1", "1.0.0-beta"} {
		slot := LifecycleSlot{
			SchemaVersion: 1, ModuleID: "avm-res-prerelease", ReleaseVersion: version,
			DesiredGeneration: 1, RequestID: "avm-res-prerelease-" + repeatDigest('a'), AcquiredAt: time.Now().UTC(), Status: "active",
		}
		if err := slot.Validate(); err == nil {
			t.Errorf("prerelease %q was accepted as a released AVM version", version)
		}
	}
}

func TestReleasedLifecycleSlotBlocksOlderAVMRelease(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	receiptID, err := identity.ID("deployment-receipt", 1, map[string]string{"run": "published-v2"})
	if err != nil {
		t.Fatal(err)
	}
	moduleID := "avm-res-released-slot"
	slot := LifecycleSlot{
		SchemaVersion: 1, ModuleID: moduleID, ReleaseVersion: "2.0.0", DesiredGeneration: 3,
		RequestID: moduleID + "-" + repeatDigest('a'), AcquiredAt: now.Add(-time.Hour), Status: "released",
		ReleaseProof: &LifecycleSlotReleaseProof{DeploymentReceiptID: receiptID, MembershipDigest: repeatDigest('b'), VerifiedAt: now.Add(-time.Minute)},
	}
	data, err := json.Marshal(slot)
	if err != nil {
		t.Fatal(err)
	}
	path, err := LifecycleSlotPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.Snapshot{Records: map[string]state.Record{path: {Kind: "lifecycle_slot", SchemaVersion: 1, Data: data}}}
	request := dispatchAcquisition(t, moduleID, 'a', 1, now)
	request.Work.AVMReleaseVersion = "1.9.0"
	request.Fingerprint, _ = workFingerprint(request.ModuleID, request.SourceSHA, *request.Work)
	request.ID = requestID(request.ModuleID, request.Fingerprint)
	if err = request.Validate(); err != nil {
		t.Fatal(err)
	}
	if allowed, slotErr := slotAllowsRequest(snapshot, request); slotErr != nil || allowed {
		t.Fatalf("older release remained dispatchable after v2 publication: allowed=%t err=%v", allowed, slotErr)
	}
	request.Work.AVMReleaseVersion = "2.0.0"
	request.Work.DesiredGeneration = 3
	request.Fingerprint, _ = workFingerprint(request.ModuleID, request.SourceSHA, *request.Work)
	request.ID = requestID(request.ModuleID, request.Fingerprint)
	if allowed, slotErr := slotAllowsRequest(snapshot, request); slotErr != nil || !allowed {
		t.Fatalf("published release's current generation was blocked: allowed=%t err=%v", allowed, slotErr)
	}
	request.Work.AVMReleaseVersion = "2.1.0"
	request.Work.DesiredGeneration = 1
	request.Fingerprint, _ = workFingerprint(request.ModuleID, request.SourceSHA, *request.Work)
	request.ID = requestID(request.ModuleID, request.Fingerprint)
	if allowed, slotErr := slotAllowsRequest(snapshot, request); slotErr != nil || !allowed {
		t.Fatalf("newer queued release was blocked after publication: allowed=%t err=%v", allowed, slotErr)
	}
}

func TestClaimModuleWithHandoffCommitsReceiptIntentAndHandoffAtomically(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-claim-handoff", 'e', 1, now)
	backend := seedDispatchBackend(t, request)
	coordinator := &queueReceiptTestCoordinator{backend: backend}
	producer := dispatchTestProvenance()

	claim, err := ClaimModuleWithHandoff(context.Background(), coordinator, request.ModuleID, "dispatcher-handoff", now, time.Minute, &producer)
	if err != nil {
		t.Fatal(err)
	}
	if coordinator.operationCalls != 1 || len(coordinator.expectedEvents) != 5 {
		t.Fatalf("receipt transaction calls/events = %d/%d, want one transaction with request, attempt, lifecycle slot, intent, and handoff", coordinator.operationCalls, len(coordinator.expectedEvents))
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	assertClaimHandoffReceipt(t, snapshot, claim, coordinator.operationID, producer)
	replayed, err := ClaimModuleWithHandoff(context.Background(), coordinator, request.ModuleID, "dispatcher-handoff", now, time.Minute, &producer)
	if err != nil || replayed.Attempt.ID != claim.Attempt.ID || replayed.Request.ID != claim.Request.ID {
		t.Fatalf("receipt-backed claim replay = %#v, err = %v", replayed, err)
	}
}

func TestClaimWithHandoffNoWorkOrRejectedSelectionWritesNoHandoff(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	queued := dispatchAcquisition(t, "avm-res-claim-no-work", 'f', 1, now)
	backend := seedDispatchBackend(t, queued)
	coordinator := &queueReceiptTestCoordinator{backend: backend}
	producer := dispatchTestProvenance()

	if _, err := ClaimModuleWithHandoff(context.Background(), coordinator, "avm-res-claim-missing", "dispatcher-handoff", now, time.Minute, &producer); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("missing module claim error = %v, want ErrNoDispatchableWork", err)
	}
	if _, err := ClaimRequestWithHandoff(context.Background(), coordinator, "missing-request", "dispatcher-handoff", now, time.Minute, &producer); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("missing request claim error = %v, want ErrNoDispatchableWork", err)
	}
	if coordinator.operationCalls != 0 {
		t.Fatalf("no-work/rejected claims entered receipt transaction %d times", coordinator.operationCalls)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := dispatchOperationID("module", "dispatcher-handoff", "avm-res-claim-missing", now)
	if err != nil {
		t.Fatal(err)
	}
	assertNoClaimHandoff(t, snapshot, queued.ModuleID, operationID)
}

func TestClaimWithHandoffFailsClosedWhenSelectionChanges(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-claim-changing", 'a', 1, now)
	backend := seedDispatchBackend(t, request)
	coordinator := &changingSelectionCoordinator{backend: backend}
	producer := dispatchTestProvenance()

	if _, err := ClaimModuleWithHandoff(context.Background(), coordinator, request.ModuleID, "dispatcher-handoff", now, time.Minute, &producer); !errors.Is(err, state.ErrTransactionIdentityConflict) {
		t.Fatalf("claim after selection changed error = %v, want ErrTransactionIdentityConflict", err)
	}
	if !coordinator.callbackInvoked {
		t.Fatal("operation transaction did not revalidate the selected request")
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := dispatchOperationID("module", "dispatcher-handoff", request.ModuleID, now)
	if err != nil {
		t.Fatal(err)
	}
	assertNoClaimHandoff(t, snapshot, request.ModuleID, operationID)
}

func dispatchTestProvenance() projection.WorkflowProvenance {
	return projection.WorkflowProvenance{WorkflowID: "manual-dispatch", Commit: repeat('a'), RunID: "dispatch-run", RunAttempt: 1}
}

func assertClaimHandoffReceipt(t *testing.T, snapshot state.Snapshot, claim DispatchClaim, operationID string, producer projection.WorkflowProvenance) {
	t.Helper()
	receiptPath, err := identity.StateOperationReceiptPath(claim.Request.ModuleID, operationID)
	if err != nil {
		t.Fatal(err)
	}
	receiptRecord, exists := snapshot.Records[receiptPath]
	if !exists || receiptRecord.Kind != "operation_receipt" {
		t.Fatalf("claim receipt record = %#v, exists=%v", receiptRecord, exists)
	}
	var receipt state.OperationReceipt
	if err = json.Unmarshal(receiptRecord.Data, &receipt); err != nil {
		t.Fatal(err)
	}
	if err = receipt.Validate(receiptPath); err != nil {
		t.Fatalf("validate claim receipt: %v", err)
	}
	if len(receipt.WriteSet) != 5 || !queueEventApplied(snapshot, operationID) {
		t.Fatalf("claim receipt write set/event = %d/%v, want five data records and applied receipt", len(receipt.WriteSet), queueEventApplied(snapshot, operationID))
	}
	if snapshot.Records[stateRequestPath+claim.Request.ID+".json"].Kind != "request" || snapshot.Records[dispatchAttemptPath(claim.Attempt)].Kind != "attempt" {
		t.Fatal("claim request and attempt were not committed with the receipt")
	}
	var handoff projection.Handoff
	for _, record := range snapshot.Records {
		if record.Kind == "stage_handoff" && json.Unmarshal(record.Data, &handoff) == nil && handoff.OperationID == operationID {
			break
		}
	}
	if handoff.ID == "" || handoff.ModuleID != claim.Request.ModuleID || handoff.Producer != producer {
		t.Fatalf("claim handoff = %+v, want module-owned handoff with producer provenance", handoff)
	}
	intentPath, err := identity.StateProjectionIntentPath(claim.Request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Records[intentPath].Kind != "projection_intent" {
		t.Fatal("claim projection intent was not committed")
	}
	for _, entry := range receipt.WriteSet {
		if entry.EventID == operationID {
			t.Fatal("receipt operation ID was reused as a data-record event identity")
		}
	}
}

func assertNoClaimHandoff(t *testing.T, snapshot state.Snapshot, moduleID, operationID string) {
	t.Helper()
	for _, record := range snapshot.Records {
		if record.Kind != "stage_handoff" {
			continue
		}
		var handoff projection.Handoff
		if json.Unmarshal(record.Data, &handoff) == nil && handoff.ModuleID == moduleID && handoff.OperationID == operationID {
			t.Fatalf("unexpected handoff for module %s", moduleID)
		}
	}
}

type queueReceiptTestCoordinator struct {
	backend        *dispatcherMemoryBackend
	operationCalls int
	operationID    string
	expectedEvents []state.Event
}

func (coordinator *queueReceiptTestCoordinator) Read(ctx context.Context) (string, state.Snapshot, error) {
	return coordinator.backend.Read(ctx)
}

func (coordinator *queueReceiptTestCoordinator) RunTransaction(context.Context, string, state.TransactionDecisionFunc, state.TransactionResultResolver) (state.TransactionResult, error) {
	return state.TransactionResult{}, errors.New("provenance claim used generic transaction")
}

func (coordinator *queueReceiptTestCoordinator) RunOperationTransaction(ctx context.Context, moduleID, operationID string, expectedEvents []state.Event, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	coordinator.operationCalls++
	coordinator.operationID = operationID
	coordinator.expectedEvents = append([]state.Event(nil), expectedEvents...)
	head, snapshot, err := coordinator.backend.Read(ctx)
	if err != nil {
		return state.TransactionResult{}, err
	}
	if queueEventApplied(snapshot, operationID) {
		resolution, resolveErr := resolve(snapshot)
		if resolveErr != nil {
			return state.TransactionResult{}, resolveErr
		}
		if !resolution.Found {
			return state.TransactionResult{}, state.ErrTransactionIdentityConflict
		}
		return state.TransactionResult{Head: head, Value: resolution.Value}, nil
	}
	decision, err := decide(snapshot)
	if err != nil {
		return state.TransactionResult{}, err
	}
	if !sameClaimEventSet(decision.Events, expectedEvents) {
		return state.TransactionResult{}, state.ErrTransactionIdentityConflict
	}
	result, err := json.Marshal(decision.Result)
	if err != nil {
		return state.TransactionResult{}, err
	}
	receiptEvent, err := state.NewOperationReceipt(moduleID, operationID, result, expectedEvents)
	if err != nil {
		return state.TransactionResult{}, err
	}
	committedHead, _, err := state.ApplyEventsAtHead(ctx, coordinator.backend, head, append(expectedEvents, receiptEvent))
	if err != nil {
		return state.TransactionResult{}, err
	}
	resolved, err := resolve(func() state.Snapshot {
		_, current, readErr := coordinator.backend.Read(ctx)
		if readErr != nil {
			return state.Snapshot{}
		}
		return current
	}())
	if err != nil {
		return state.TransactionResult{}, err
	}
	if !resolved.Found {
		return state.TransactionResult{}, errors.New("committed claim did not resolve")
	}
	return state.TransactionResult{Head: committedHead, Value: resolved.Value, Changed: true}, nil
}

type changingSelectionCoordinator struct {
	backend         *dispatcherMemoryBackend
	callbackInvoked bool
}

func (coordinator *changingSelectionCoordinator) Read(ctx context.Context) (string, state.Snapshot, error) {
	return coordinator.backend.Read(ctx)
}

func (coordinator *changingSelectionCoordinator) RunTransaction(context.Context, string, state.TransactionDecisionFunc, state.TransactionResultResolver) (state.TransactionResult, error) {
	return state.TransactionResult{}, errors.New("provenance claim used generic transaction")
}

func (coordinator *changingSelectionCoordinator) RunOperationTransaction(ctx context.Context, _, _ string, _ []state.Event, decide state.TransactionDecisionFunc, _ state.TransactionResultResolver) (state.TransactionResult, error) {
	_, snapshot, err := coordinator.backend.Read(ctx)
	if err != nil {
		return state.TransactionResult{}, err
	}
	for path, record := range snapshot.Records {
		if record.Kind != "request" {
			continue
		}
		var request Request
		if err = json.Unmarshal(record.Data, &request); err != nil {
			return state.TransactionResult{}, err
		}
		request.UpdatedAt = request.UpdatedAt.Add(time.Second)
		encoded, marshalErr := json.Marshal(request)
		if marshalErr != nil {
			return state.TransactionResult{}, marshalErr
		}
		record.Data = encoded
		snapshot.Records[path] = record
	}
	coordinator.callbackInvoked = true
	_, err = decide(snapshot)
	return state.TransactionResult{}, err
}

func TestClaimRequestRequiresExactProjectionAcknowledgement(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-barrier-ack", 'a', 2, now)
	backend := seedDispatchBackend(t, request)
	handoff := seedProjectionBarrier(t, backend, request, now, true, false)

	claim, err := ClaimRequest(context.Background(), newQueueTestCoordinator(backend), request.ID, "barrier-dispatcher", now.Add(time.Minute), time.Minute)
	if err != nil || claim.Request.ID != request.ID {
		t.Fatalf("claim with exact acknowledgement = %#v, %v", claim, err)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	releasePath, err := identity.StateProjectionBarrierReleasePath(request.ModuleID, handoff.Projection.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := snapshot.Records[releasePath]; exists {
		t.Fatal("exact acknowledgement created an unnecessary timeout release")
	}
}

func TestClaimRequestStaleAcknowledgementWaitsForFixedDeadlineAndWritesSeparateRelease(t *testing.T) {
	createdAt := time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-barrier-deadline", 'b', 2, createdAt)
	backend := seedDispatchBackend(t, request)
	handoff := seedProjectionBarrier(t, backend, request, createdAt, true, true)
	coordinator := newQueueTestCoordinator(backend)
	beforeDeadline := handoff.BarrierDeadline.Add(-time.Nanosecond)

	if _, err := ClaimRequest(context.Background(), coordinator, request.ID, "barrier-dispatcher", beforeDeadline, time.Minute); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("stale acknowledgement before deadline error = %v, want ErrNoDispatchableWork", err)
	}
	if _, err := ClaimRequest(context.Background(), coordinator, request.ID, "barrier-dispatcher", handoff.BarrierDeadline, time.Minute); err != nil {
		t.Fatalf("claim at fixed deadline: %v", err)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	releasePath, err := identity.StateProjectionBarrierReleasePath(request.ModuleID, handoff.Projection.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	record, exists := snapshot.Records[releasePath]
	if !exists || record.Kind != "projection_barrier_release" {
		t.Fatalf("deadline release record = %#v, exists=%v", record, exists)
	}
	var release projection.BarrierRelease
	if err = json.Unmarshal(record.Data, &release); err != nil {
		t.Fatal(err)
	}
	if release.Status != projection.BarrierReleased || release.Deadline != handoff.BarrierDeadline || release.ReleasedAt != handoff.BarrierDeadline || release.Status == projection.AcknowledgementDone {
		t.Fatalf("release changed its fixed deadline or claims delivery: %+v", release)
	}
	acknowledgementPath, err := identity.StateProjectionAcknowledgementPath(request.ModuleID, handoff.Projection.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	var acknowledgement projection.Acknowledgement
	if err = json.Unmarshal(snapshot.Records[acknowledgementPath].Data, &acknowledgement); err != nil {
		t.Fatal(err)
	}
	if acknowledgement.Projection == release.Projection {
		t.Fatal("release modified the stale acknowledgement into a success")
	}
}

func TestClaimRequestBlocksUnknownPredecessorReceipt(t *testing.T) {
	now := time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-barrier-unknown", 'c', 2, now)
	backend := seedDispatchBackend(t, request)
	seedProjectionBarrier(t, backend, request, now.Add(-projection.BarrierTimeout), false, false)
	deleteReceiptForTest(t, backend, request.ModuleID)
	if _, err := ClaimRequest(context.Background(), newQueueTestCoordinator(backend), request.ID, "barrier-dispatcher", now, time.Minute); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("claim without canonical predecessor receipt error = %v, want ErrNoDispatchableWork", err)
	}
}

func TestClaimRequestDoesNotReleaseBarrierWithActiveModuleOwner(t *testing.T) {
	now := time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	owner := dispatchAcquisition(t, "avm-res-barrier-owner", 'd', 1, now)
	backend := seedDispatchBackend(t, owner)
	if _, err := ClaimRequest(context.Background(), newQueueTestCoordinator(backend), owner.ID, "existing-dispatcher", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	queued := dispatchAssessment(t, owner.ModuleID, "azurerm-4", 2, now.Add(time.Second))
	queuedEvent := requestSeedEvent(t, queued)
	if _, _, err := state.ApplyEventsAtHead(context.Background(), backend, backend.head, []state.Event{queuedEvent}); err != nil {
		t.Fatal(err)
	}
	operationID, err := identity.ID("operation", 1, struct {
		RequestID string `json:"request_id"`
	}{queued.ID})
	if err != nil {
		t.Fatal(err)
	}
	handoff, events := newSeedProjectionHandoff(t, queued, now.Add(-projection.BarrierTimeout), operationID)
	if _, _, err = state.ApplyEventsAtHead(context.Background(), backend, backend.head, events); err != nil {
		t.Fatal(err)
	}
	if _, err = ClaimRequest(context.Background(), newQueueTestCoordinator(backend), queued.ID, "barrier-dispatcher", now, time.Minute); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("claim despite active module owner error = %v, want ErrNoDispatchableWork", err)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	releasePath, err := identity.StateProjectionBarrierReleasePath(owner.ModuleID, handoff.Projection.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := snapshot.Records[releasePath]; exists {
		t.Fatal("active owner caused a barrier release before a claim")
	}
}

func TestClaimRequestWithoutMatchingHandoffRemainsBlocked(t *testing.T) {
	now := time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-barrier-no-handoff", 'f', 1, now)
	backend := seedDispatchBackend(t, request)
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	for path, record := range snapshot.Records {
		if record.Kind == "stage_handoff" {
			delete(snapshot.Records, path)
		}
	}
	backend.snapshot = snapshot
	if _, err := ClaimRequest(context.Background(), newQueueTestCoordinator(backend), request.ID, "barrier-dispatcher", now, time.Minute); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("claim without matching handoff error = %v, want ErrNoDispatchableWork", err)
	}
}

func TestConsumerHandoffMatchesExactReleaseInputWhenGenerationsRepeat(t *testing.T) {
	now := time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC)
	first := dispatchAcquisition(t, "avm-res-handoff-version", 'a', 1, now)
	second := dispatchAcquisition(t, "avm-res-handoff-version", 'b', 1, now.Add(time.Second))
	first.Work.AVMReleaseVersion = "1.0.0"
	first.Fingerprint, _ = workFingerprint(first.ModuleID, first.SourceSHA, *first.Work)
	first.ID = requestID(first.ModuleID, first.Fingerprint)
	second.Work.AVMReleaseVersion = "2.0.0"
	second.Fingerprint, _ = workFingerprint(second.ModuleID, second.SourceSHA, *second.Work)
	second.ID = requestID(second.ModuleID, second.Fingerprint)
	backend := seedDispatchBackend(t, first, second)
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	handoff, found, err := findConsumerHandoff(snapshot, second)
	if err != nil || !found || handoff.InputFingerprint == "" {
		t.Fatalf("exact second-version handoff found=%t value=%+v err=%v", found, handoff, err)
	}
	wantFingerprint, err := identity.ID("input-fingerprint", 1, second.Fingerprint)
	if err != nil || handoff.InputFingerprint != wantFingerprint {
		t.Fatalf("matched handoff fingerprint=%s want=%s err=%v", handoff.InputFingerprint, wantFingerprint, err)
	}
}

func seedProjectionBarrier(t *testing.T, backend *dispatcherMemoryBackend, request Request, createdAt time.Time, withAcknowledgement, staleAcknowledgement bool) projection.Handoff {
	t.Helper()
	if existing, found := findSeededProjectionHandoff(t, backend, request); found {
		if staleAcknowledgement {
			seedAcknowledgement(t, backend, request.ModuleID, existing.Projection, createdAt, true)
		}
		return existing
	}
	handoff, events := newSeedProjectionHandoff(t, request, createdAt, "")
	if withAcknowledgement {
		events = append(events, seededAcknowledgementEvent(t, request.ModuleID, handoff.Projection, createdAt, false))
	}
	if _, _, err := state.ApplyEventsAtHead(context.Background(), backend, backend.head, events); err != nil {
		t.Fatalf("seed projection barrier records: %v", err)
	}
	return handoff
}

func findSeededProjectionHandoff(t *testing.T, backend *dispatcherMemoryBackend, request Request) (projection.Handoff, bool) {
	t.Helper()
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range snapshot.Records {
		if record.Kind != "stage_handoff" {
			continue
		}
		var existing projection.Handoff
		wantedStage := "acquire_source"
		if request.Work.Phase == PhaseAssess {
			wantedStage = "assessment"
		}
		fingerprint, err := identity.ID("input-fingerprint", 1, request.Fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(record.Data, &existing) == nil && existing.ModuleID == request.ModuleID && existing.Generation == request.Work.DesiredGeneration && existing.ConsumerStage == wantedStage && existing.InputFingerprint == fingerprint {
			return existing, true
		}
	}
	return projection.Handoff{}, false
}

func newSeedProjectionHandoff(t *testing.T, request Request, createdAt time.Time, operationID string) (projection.Handoff, []state.Event) {
	t.Helper()
	intent, err := projection.NewDesiredIntent(request.ModuleID, request.Work.DesiredGeneration, "transition-"+request.ID, "queued", []projection.OwnedField{{Name: "status", Value: "Queued"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	required, err := intent.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if operationID == "" {
		operationID, err = identity.ID("operation", 1, struct {
			RequestID string `json:"request_id"`
		}{request.ID})
		if err != nil {
			t.Fatal(err)
		}
	}
	inputFingerprint, err := identity.ID("input-fingerprint", 1, request.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	consumerStage := "acquire_source"
	if request.Work.Phase == PhaseAssess {
		consumerStage = "assessment"
	}
	handoff, err := projection.NewHandoff(projection.Handoff{
		ModuleID: request.ModuleID, Generation: request.Work.DesiredGeneration,
		TransitionID: required.TransitionID, InputFingerprint: inputFingerprint,
		OperationID: operationID, AcceptedFactsID: request.Work.FactsID,
		Producer:      projection.WorkflowProvenance{WorkflowID: "producer", Commit: repeat('f'), RunID: "producer-run", RunAttempt: 1},
		ConsumerStage: consumerStage, Projection: required,
	}, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	completionEvent, err := stateRecordEvent("state/projection/intents/"+request.ModuleID+".json", "projection_intent", map[string]any{"module_id": request.ModuleID, "status": "completed"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	handoffPath, err := identity.StateProjectionHandoffPath(request.ModuleID, handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	handoffEvent, err := stateRecordEvent(handoffPath, "stage_handoff", handoff, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	events := []state.Event{completionEvent, handoffEvent}
	receiptEvent, err := state.NewOperationReceipt(request.ModuleID, operationID, json.RawMessage(`{"completed":true}`), events)
	if err != nil {
		t.Fatal(err)
	}
	return handoff, append(events, receiptEvent)
}

func seedAcknowledgement(t *testing.T, backend *dispatcherMemoryBackend, moduleID string, required projection.ProjectionIdentity, acknowledgedAt time.Time, stale bool) {
	t.Helper()
	event := seededAcknowledgementEvent(t, moduleID, required, acknowledgedAt, stale)
	if _, _, err := state.ApplyEventsAtHead(context.Background(), backend, backend.head, []state.Event{event}); err != nil {
		t.Fatal(err)
	}
}

func seededAcknowledgementEvent(t *testing.T, moduleID string, required projection.ProjectionIdentity, acknowledgedAt time.Time, stale bool) state.Event {
	t.Helper()
	acknowledgement := projection.Acknowledgement{SchemaVersion: projection.SchemaVersion, Projection: required, Status: projection.AcknowledgementDone, AcknowledgedAt: acknowledgedAt}
	if stale {
		acknowledgement.Projection.TransitionID += "-stale"
	}
	ackPath, err := identity.StateProjectionAcknowledgementPath(moduleID, required.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	event, err := stateRecordEvent(ackPath, "projection_acknowledgement", acknowledgement, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func deleteReceiptForTest(t *testing.T, backend *dispatcherMemoryBackend, moduleID string) {
	t.Helper()
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	for recordPath, record := range snapshot.Records {
		if record.Kind != "operation_receipt" {
			continue
		}
		var receipt state.OperationReceipt
		if json.Unmarshal(record.Data, &receipt) == nil && receipt.ModuleID == moduleID {
			delete(snapshot.Records, recordPath)
			backend.mu.Lock()
			backend.snapshot = snapshot
			backend.mu.Unlock()
			return
		}
	}
	t.Fatal("test fixture has no predecessor receipt to remove")
}

func requestSeedEvent(t *testing.T, request Request) state.Event {
	t.Helper()
	event, err := stateRecordEvent(stateRequestPath+request.ID+".json", "request", request, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestVerifiedReadBackendReusesSnapshotAfterCheckingHead(t *testing.T) {
	now := time.Date(2026, 10, 7, 13, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-verified-claim", 'd', 1, now)
	origin := seedDispatchBackend(t, request)
	if _, err := state.MigrateModuleLayout(context.Background(), origin); err != nil {
		t.Fatal(err)
	}
	origin.reads = 0
	verified := &state.VerifiedReadBackend{Backend: origin}
	if _, _, err := state.ReadSnapshot(context.Background(), verified); err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.ReadSnapshot(context.Background(), verified); err != nil {
		t.Fatal(err)
	}
	if origin.reads != 1 {
		t.Fatalf("verified snapshot content reads = %d, want one after fresh-head check", origin.reads)
	}
}

func newQueueTestCoordinator(backend state.Backend) state.TransactionCoordinator {
	return &queueTestCoordinator{backend: backend}
}

func queueEventApplied(snapshot state.Snapshot, eventID string) bool {
	if _, exists := snapshot.AppliedEvents[eventID]; exists {
		return true
	}
	for _, aggregate := range snapshot.Aggregates {
		if _, exists := aggregate.AppliedEvents[eventID]; exists {
			return true
		}
	}
	return false
}

type queueTestCoordinator struct {
	backend state.Backend
}

func (coordinator *queueTestCoordinator) Read(ctx context.Context) (string, state.Snapshot, error) {
	return coordinator.backend.Read(ctx)
}

func (coordinator *queueTestCoordinator) RunTransaction(ctx context.Context, operationID string, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	for attempt := 0; attempt < 3; attempt++ {
		head, snapshot, err := coordinator.backend.Read(ctx)
		if err != nil {
			return state.TransactionResult{}, err
		}
		if queueEventApplied(snapshot, operationID) {
			resolution, resolveErr := resolve(snapshot)
			if resolveErr != nil {
				return state.TransactionResult{}, resolveErr
			}
			if !resolution.Found {
				return state.TransactionResult{}, fmt.Errorf("durable operation did not resolve")
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
		updated, _, applyErr := state.ApplyEventsAtHead(ctx, coordinator.backend, head, decision.Events)
		if errors.Is(applyErr, state.ErrHeadMoved) && attempt < 2 {
			continue
		}
		if applyErr != nil {
			return state.TransactionResult{}, applyErr
		}
		return state.TransactionResult{Head: updated, Value: decision.Result, Changed: true}, nil
	}
	return state.TransactionResult{}, state.ErrTransactionConflict
}

func TestBindActionsRunRetriesUnrelatedModuleLayoutHeadMovement(t *testing.T) {
	now := time.Date(2026, 10, 7, 14, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-binding-retry", 'd', 1, now)
	unrelated := dispatchAcquisition(t, "avm-res-binding-other", 'e', 1, now)
	origin := seedDispatchBackend(t, request, unrelated)
	if _, err := state.MigrateModuleLayout(context.Background(), origin); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimRequest(context.Background(), newQueueTestCoordinator(origin), request.ID, "dispatcher-run", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	backend := &bindingHeadMovementBackend{dispatcherMemoryBackend: origin, beforeCommit: func() {
		if _, err := ClaimModule(context.Background(), newQueueTestCoordinator(origin), unrelated.ModuleID, "other-dispatcher", now, time.Minute); err != nil {
			t.Fatal(err)
		}
	}}
	bound, err := BindActionsRun(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "worker-run", 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !backend.moved || bound.Attempt.RunID != "worker-run" || bound.Request.OwnerRunID != "worker-run" {
		t.Fatalf("binding was not recomputed after unrelated head movement: %#v", bound)
	}
	_, after, err := state.ReadSnapshot(context.Background(), origin)
	if err != nil {
		t.Fatal(err)
	}
	if after.FencingTokens[unrelated.ModuleID] != 1 {
		t.Fatal("binding lost the intervening unrelated module fence")
	}
	var other Request
	if err := json.Unmarshal(after.Records[stateRequestPath+unrelated.ID+".json"].Data, &other); err != nil {
		t.Fatal(err)
	}
	if other.Status != StatusClaimed || other.Work.Lease == nil {
		t.Fatal("binding overwrote the unrelated module claim")
	}
	var attempt Attempt
	if err := json.Unmarshal(after.Records[dispatchAttemptPath(claim.Attempt)].Data, &attempt); err != nil {
		t.Fatal(err)
	}
	if attempt.Status != AttemptDispatched || attempt.RunID != "worker-run" {
		t.Fatalf("binding did not commit after recomputation: %+v", attempt)
	}
}

type bindingHeadMovementBackend struct {
	*dispatcherMemoryBackend
	moved        bool
	beforeCommit func()
}

func (backend *bindingHeadMovementBackend) Commit(ctx context.Context, expectedHead string, snapshot state.Snapshot) (string, error) {
	if !backend.moved {
		backend.moved = true
		backend.beforeCommit()
		return "", state.ErrHeadMoved
	}
	return backend.dispatcherMemoryBackend.Commit(ctx, expectedHead, snapshot)
}

func TestBindActionsRunRejectsConcurrentModuleOwner(t *testing.T) {
	now := time.Date(2026, 10, 7, 14, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-binding-conflict", 'd', 1, now)
	origin := seedDispatchBackend(t, request)
	if _, err := state.MigrateModuleLayout(context.Background(), origin); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimNext(context.Background(), newQueueTestCoordinator(origin), "dispatcher-run", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	backend := &bindingHeadMovementBackend{dispatcherMemoryBackend: origin, beforeCommit: func() {
		if _, err := BindActionsRun(context.Background(), newQueueTestCoordinator(origin), claim.Attempt.ID, "winning-run", 1, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := BindActionsRun(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "losing-run", 1, now.Add(time.Second)); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("competing binding error = %v, want durable ownership conflict", err)
	}
	for _, owner := range []struct {
		runID      string
		runAttempt uint64
	}{{"losing-run", 1}, {"winning-run", 2}} {
		if _, err := BindActionsRun(context.Background(), newQueueTestCoordinator(origin), claim.Attempt.ID, owner.runID, owner.runAttempt, now.Add(2*time.Second)); !errors.Is(err, ErrAttemptConflict) {
			t.Fatalf("duplicate owner error = %v", err)
		}
	}
	inspected, err := InspectAttempt(context.Background(), origin, claim.Attempt.ID)
	if err != nil || inspected.Attempt.RunID != "winning-run" || inspected.Request.OwnerRunID != "winning-run" {
		t.Fatalf("winning ownership changed: %#v, %v", inspected, err)
	}
}

func TestBindActionsRunRecoversLostAcknowledgementOnModuleLayout(t *testing.T) {
	now := time.Date(2026, 10, 7, 14, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-binding-ack", 'd', 1, now)
	origin := seedDispatchBackend(t, request)
	if _, err := state.MigrateModuleLayout(context.Background(), origin); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimNext(context.Background(), newQueueTestCoordinator(origin), "dispatcher-run", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	backend := &bindingLostAckBackend{dispatcherMemoryBackend: origin}
	boundAt := now.Add(time.Second)
	if _, err := BindActionsRun(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "worker-run", 1, boundAt); err == nil {
		t.Fatal("lost acknowledgement was not surfaced")
	}
	head, before, err := state.ReadSnapshot(context.Background(), origin)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := BindActionsRun(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "worker-run", 1, now.Add(2*time.Second))
	if err != nil || retry.Changed || retry.Head != head || !retry.Attempt.BoundAt.Equal(boundAt) {
		t.Fatalf("lost acknowledgement retry = %#v, %v", retry, err)
	}
	_, after, err := state.ReadSnapshot(context.Background(), origin)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("binding retry changed the accepted binding snapshot")
	}
}

type bindingLostAckBackend struct {
	*dispatcherMemoryBackend
	lost bool
}

func (backend *bindingLostAckBackend) Commit(ctx context.Context, expectedHead string, snapshot state.Snapshot) (string, error) {
	head, err := backend.dispatcherMemoryBackend.Commit(ctx, expectedHead, snapshot)
	if err == nil && !backend.lost {
		backend.lost = true
		return "", errors.New("binding acknowledgement lost")
	}
	return head, err
}

func TestClaimNextPersistsAttemptBeforeDispatchAndUnknownLookupKeepsClaim(t *testing.T) {
	now := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
	acquisition := dispatchAcquisition(t, "avm-res-claim", 'a', 1, now)
	assessment := dispatchAssessment(t, "avm-res-claim", "azurerm-4", 1, now)
	backend := seedDispatchBackend(t, acquisition, assessment)

	claim, err := ClaimNext(context.Background(), newQueueTestCoordinator(backend), "dispatcher-run-1", now, time.Minute)
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
	if _, err = ClaimNext(context.Background(), newQueueTestCoordinator(backend), "dispatcher-run-2", now, time.Minute); !errors.Is(err, ErrNoDispatchableWork) {
		t.Fatalf("second same-module claim error = %v, want ErrNoDispatchableWork", err)
	}

	result, err := RecoverAttempt(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, now.Add(2*time.Minute), ActionsObservation{State: RunUnknown})
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

	claim, err := ClaimRequest(context.Background(), newQueueTestCoordinator(backend), target.ID, "dispatcher-target-only", now, time.Minute)
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
	claim, err := ClaimNext(context.Background(), newQueueTestCoordinator(backend), "dispatcher-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindActionsRun(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "actions-run-inspect", 1, now.Add(time.Second))
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
	claim, err := ClaimNext(context.Background(), newQueueTestCoordinator(backend), "dispatcher-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	bound, err := BindActionsRun(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "actions-run-1", 1, now.Add(time.Second))
	if err != nil || !bound.Changed || bound.Attempt.Status != AttemptDispatched || bound.Request.Status != StatusRunning {
		t.Fatalf("bound run = %#v, err = %v", bound, err)
	}
	retry, err := BindActionsRun(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "actions-run-1", 1, now.Add(2*time.Second))
	if err != nil || retry.Changed || retry.Head != bound.Head {
		t.Fatalf("same-run bind was not idempotent: %#v, err = %v", retry, err)
	}
	if _, err = BindActionsRun(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "actions-run-duplicate", 1, now.Add(3*time.Second)); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("duplicate run bind error = %v, want ErrAttemptConflict", err)
	}
}

func TestHeartbeatAndConfirmedAbsentRecoveryPreserveFenceHighWater(t *testing.T) {
	now := time.Date(2026, 10, 2, 16, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-recover", 'a', 1, now)
	backend := seedDispatchBackend(t, request)
	claim, err := ClaimNext(context.Background(), newQueueTestCoordinator(backend), "dispatcher-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindActionsRun(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "actions-run-1", 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	heartbeat, err := Heartbeat(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "actions-run-1", claim.Attempt.Fence, now.Add(20*time.Second), now.Add(2*time.Minute))
	if err != nil || !heartbeat.Changed || !heartbeat.Attempt.LeaseExpiresAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("heartbeat = %#v, err = %v", heartbeat, err)
	}
	if _, err = Heartbeat(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "actions-run-1", claim.Attempt.Fence+1, now.Add(30*time.Second), now.Add(3*time.Minute)); !errors.Is(err, ErrAttemptOwnership) {
		t.Fatalf("stale heartbeat error = %v, want ErrAttemptOwnership", err)
	}

	unknown, err := RecoverAttempt(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, now.Add(3*time.Minute), ActionsObservation{State: RunUnknown})
	if err != nil || unknown.Changed || unknown.Request.Status != StatusRunning {
		t.Fatalf("unknown recovery released run: %#v, err = %v", unknown, err)
	}
	absent, err := RecoverAttempt(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, now.Add(3*time.Minute), ActionsObservation{State: RunAbsent})
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
	secondClaim, err := ClaimNext(context.Background(), newQueueTestCoordinator(backend), "dispatcher-2", now.Add(3*time.Minute), time.Minute)
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
			_, err := ClaimNext(context.Background(), newQueueTestCoordinator(backend), dispatcher, now, time.Minute)
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
	claim, err := ClaimNext(context.Background(), newQueueTestCoordinator(backend), "dispatcher-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = BindActionsRun(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "actions-run-completed", 1, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverAttempt(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, now.Add(2*time.Second), ActionsObservation{
		State: RunCompleted, RunID: "actions-run-completed", RunAttempt: 1,
	})
	if err != nil || !recovered.Changed || recovered.Attempt.Status != AttemptCompleted || recovered.Request.Status != StatusQueued || recovered.Request.Work.RetryClass != RetryTransient {
		t.Fatalf("completed run recovery = %#v, err = %v", recovered, err)
	}
	replay, err := BindActionsRun(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, "actions-run-completed", 1, now.Add(3*time.Second))
	if err != nil || replay.Changed || replay.Head != recovered.Head {
		t.Fatalf("completed run bind replay = %#v, err = %v", replay, err)
	}
}

func TestCompletedRunFoundBeforeBindingIsRecoveredByAttemptIdentity(t *testing.T) {
	now := time.Date(2026, 10, 2, 19, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-bind-crash", 'a', 1, now)
	backend := seedDispatchBackend(t, request)
	claim, err := ClaimNext(context.Background(), newQueueTestCoordinator(backend), "dispatcher-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverAttempt(context.Background(), newQueueTestCoordinator(backend), claim.Attempt.ID, now.Add(2*time.Second), ActionsObservation{
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
		ModuleID: moduleID, SourceSHA: repeat(sourceByte), AVMReleaseVersion: "1.0.0", DesiredGeneration: generation,
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
		ModuleID: moduleID, SourceSHA: repeat('a'), AVMReleaseVersion: "1.0.0", ContextID: contextID, Track: contextID,
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
	for _, request := range requests {
		seedProjectionBarrier(t, backend, request, request.CreatedAt, true, false)
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
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		panic(err)
	}
	var cloned state.Snapshot
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		panic(err)
	}
	return cloned
}
