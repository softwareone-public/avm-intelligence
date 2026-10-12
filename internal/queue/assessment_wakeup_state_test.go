package queue

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestEnqueueAssessmentWakeWritesReplayableRequestsWithoutDispatch(t *testing.T) {
	now := time.Date(2026, 10, 10, 20, 0, 0, 0, time.UTC)
	request, err := NewAssessmentRequest(AssessmentInput{
		ModuleID: "avm-res-wake-replay", SourceSHA: strings.Repeat("a", 40), AVMReleaseVersion: "v1.2.3",
		ContextID: "registry.terraform.io/hashicorp/azurerm/4", Track: "registry.terraform.io/hashicorp/azurerm/4",
		DesiredGeneration: 3, FactsID: "facts-" + strings.Repeat("b", 64),
		DefinitionIDs:  []string{"definition-" + strings.Repeat("c", 64)},
		SnapshotIDs:    []string{"snapshot-" + strings.Repeat("d", 64)},
		TargetVersions: map[string]string{"registry.terraform.io/hashicorp/azurerm": "4.10.0"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	deferred := []AssessmentWakeDeferred{{ModuleID: request.ModuleID, AVMReleaseVersion: "v1.2.3", ContextID: "registry.terraform.io/hashicorp/azapi/2", Reason: "accepted provider snapshot is unavailable"}}
	backend := &dispatcherMemoryBackend{head: "dispatch-head-0", snapshot: state.NewSnapshot()}
	seedAssessmentWakeProjectionIntent(t, &backend.snapshot, request)
	coordinator := &queueReceiptTestCoordinator{backend: backend}
	producer := projection.WorkflowProvenance{WorkflowID: "manual-site-publish", Commit: strings.Repeat("e", 40), RunID: "wake-fixture", RunAttempt: 1}
	first, err := EnqueueAssessmentWake(context.Background(), coordinator, producer, producer.Commit, []Request{request}, deferred, now)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || len(first.Requests) != 1 || first.Requests[0].ID != request.ID || len(first.Deferred) != 1 {
		t.Fatalf("first wake commit = %+v, want one queued request and retained deferred context", first)
	}
	_, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := loadRequest(snapshot, request.ID)
	if err != nil || stored.Status != StatusQueued {
		t.Fatalf("stored request = %+v, error = %v; request must remain queued", stored, err)
	}
	if len(snapshot.Records) == 0 {
		t.Fatal("assessment wake did not persist request records")
	}
	slot, slotExists, err := lifecycleSlotFromSnapshot(snapshot, request.ModuleID)
	if err != nil || !slotExists || slot.Status != "active" || slot.ReleaseVersion != request.Work.AVMReleaseVersion || slot.RequestID != request.ID {
		t.Fatalf("assessment wake lifecycle slot = %+v exists=%t err=%v", slot, slotExists, err)
	}
	handoff, found, err := findConsumerHandoff(snapshot, stored)
	if err != nil || !found || handoff.ConsumerStage != "assessment" || handoff.OperationID != coordinator.operationID || handoff.AcceptedFactsID != request.Work.FactsID || !verifiedPredecessorReceipt(snapshot, handoff) {
		t.Fatalf("assessment wake handoff = %+v found=%t err=%v; expected exact receipt-backed assessment consumer", handoff, found, err)
	}
	var receiptFound bool
	for _, record := range snapshot.Records {
		if record.Kind == "operation_receipt" {
			receiptFound = true
		}
	}
	if !receiptFound {
		t.Fatal("assessment wake did not persist an operation receipt")
	}
	advanced := first.Requests[0]
	advanced.Status = StatusSuperseded
	advanced.UpdatedAt = now.Add(time.Minute)
	encoded, err := json.Marshal(advanced)
	if err != nil {
		t.Fatal(err)
	}
	advancedEvent := state.Event{ID: "advance-wake-fixture", RecordPath: stateRequestPath + request.ID + ".json", Record: state.Record{Kind: "request", SchemaVersion: 1, Data: encoded}}
	currentHead, _, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = state.ApplyEventsAtHead(context.Background(), backend, currentHead, []state.Event{advancedEvent}); err != nil {
		t.Fatalf("advance queued request after wake: %v", err)
	}
	retryProducer := producer
	retryProducer.RunID = "wake-fixture-retry"
	retryProducer.RunAttempt = 2
	second, err := EnqueueAssessmentWake(context.Background(), coordinator, retryProducer, producer.Commit, []Request{request}, deferred, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed || len(second.Requests) != 1 || second.Requests[0].ID != request.ID || coordinator.operationCalls != 2 {
		t.Fatalf("replayed wake = %+v, operation calls = %d; want immutable replay without another state change", second, coordinator.operationCalls)
	}
	_, advancedSnapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	advanced, err = loadRequest(advancedSnapshot, request.ID)
	if err != nil || advanced.Status != StatusSuperseded {
		t.Fatalf("replayed wake overwrote advanced request = %+v, error = %v", advanced, err)
	}
	if _, err = identity.StateOperationReceiptPath(request.ModuleID, coordinator.operationID); err != nil {
		t.Fatalf("operation receipt identity is invalid: %v", err)
	}
	wrongProducer := retryProducer
	wrongProducer.WorkflowID = "unreviewed-workflow"
	if _, err = EnqueueAssessmentWake(context.Background(), coordinator, wrongProducer, producer.Commit, []Request{request}, deferred, now.Add(2*time.Minute)); err == nil {
		t.Fatal("wake operation replay accepted a different producer workflow")
	}
}

func TestAssessmentWakeIgnoresUnrelatedVersionlessHistory(t *testing.T) {
	now := time.Date(2026, 10, 11, 14, 0, 0, 0, time.UTC)
	desired := dispatchAssessment(t, "avm-res-wake-target", "azurerm-4", 1, now)
	unrelated := dispatchAcquisition(t, "avm-res-wake-unrelated", 'b', 1, now)
	unrelated.Work.AVMReleaseVersion = ""
	unrelated.Fingerprint = strings.Repeat("f", 64)
	unrelated.ID = requestID(unrelated.ModuleID, unrelated.Fingerprint)
	encoded, err := json.Marshal(unrelated)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.NewSnapshot()
	snapshot.Records[stateRequestPath+unrelated.ID+".json"] = state.Record{Kind: "request", SchemaVersion: 1, Data: encoded}
	seedAssessmentWakeProjectionIntent(t, &snapshot, desired)

	operationID, err := identity.ID("assessment-wake", 3, map[string]string{"module_id": desired.ModuleID})
	if err != nil {
		t.Fatal(err)
	}
	producer := projection.WorkflowProvenance{WorkflowID: "manual-assessment-wake", Commit: strings.Repeat("e", 40), RunID: "wake-fixture", RunAttempt: 1}
	events, _, committed, err := assessmentWakeEvents(snapshot, []Request{desired}, operationID, producer, now)
	if err != nil || len(events) == 0 || len(committed) != 1 || committed[0].ID != desired.ID {
		t.Fatalf("target assessment wake was blocked by unrelated versionless history: events=%d committed=%d err=%v", len(events), len(committed), err)
	}
}

func TestEnqueueAssessmentWakePreservesAndProvesExistingHigherGenerationSlot(t *testing.T) {
	now := time.Date(2026, 10, 12, 13, 20, 0, 0, time.UTC)
	request, err := NewAssessmentRequest(AssessmentInput{
		ModuleID: "avm-res-wake-existing-slot", SourceSHA: strings.Repeat("a", 40), AVMReleaseVersion: "v1.2.3",
		ContextID: "registry.terraform.io/hashicorp/azurerm/3", Track: "registry.terraform.io/hashicorp/azurerm/3",
		DesiredGeneration: 3, FactsID: "facts-" + strings.Repeat("b", 64),
		DefinitionIDs:  []string{"definition-" + strings.Repeat("c", 64)},
		SnapshotIDs:    []string{"snapshot-" + strings.Repeat("d", 64)},
		TargetVersions: map[string]string{"registry.terraform.io/hashicorp/azurerm": "3.117.1"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	backend := &dispatcherMemoryBackend{head: "dispatch-head-existing-slot", snapshot: state.NewSnapshot()}
	seedAssessmentWakeProjectionIntent(t, &backend.snapshot, request)
	acquisition := dispatchAcquisition(t, request.ModuleID, 'a', 1, now.Add(-time.Hour))
	acquisition.Work.AVMReleaseVersion = request.Work.AVMReleaseVersion
	acquisition.Fingerprint, err = workFingerprint(acquisition.ModuleID, acquisition.SourceSHA, *acquisition.Work)
	if err != nil {
		t.Fatal(err)
	}
	acquisition.ID = requestID(acquisition.ModuleID, acquisition.Fingerprint)
	slot := LifecycleSlot{SchemaVersion: 1, ModuleID: request.ModuleID, ReleaseVersion: request.Work.AVMReleaseVersion, DesiredGeneration: 5, RequestID: acquisition.ID, AcquiredAt: now.Add(-time.Hour), Status: "active"}
	data, err := json.Marshal(slot)
	if err != nil {
		t.Fatal(err)
	}
	slotPath, err := LifecycleSlotPath(request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[slotPath] = state.Record{Kind: "lifecycle_slot", SchemaVersion: 1, Data: data}
	producer := projection.WorkflowProvenance{WorkflowID: "manual-assessment-wake", Commit: strings.Repeat("e", 40), RunID: "wake-existing-slot", RunAttempt: 1}
	coordinator := &queueReceiptTestCoordinator{backend: backend}
	if _, err = EnqueueAssessmentWake(context.Background(), coordinator, producer, producer.Commit, []Request{request}, nil, now); err != nil {
		t.Fatalf("enqueue assessment with an existing higher-generation slot: %v", err)
	}
	_, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	currentSlot, exists, err := lifecycleSlotFromSnapshot(snapshot, request.ModuleID)
	if err != nil || !exists || currentSlot.DesiredGeneration != 5 || currentSlot.ReleaseVersion != request.Work.AVMReleaseVersion {
		t.Fatalf("wake changed independent module slot generation: %+v exists=%t err=%v", currentSlot, exists, err)
	}
	stored, err := loadRequest(snapshot, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	handoff, found, err := findConsumerHandoff(snapshot, stored)
	if err != nil || !found || !verifiedPredecessorReceipt(snapshot, handoff) {
		t.Fatalf("existing-slot wake handoff lacks exact receipt proof: %+v found=%t err=%v", handoff, found, err)
	}
}

func seedAssessmentWakeProjectionIntent(t *testing.T, snapshot *state.Snapshot, request Request) {
	t.Helper()
	intent, err := projection.NewDesiredIntent(request.ModuleID, request.Work.DesiredGeneration, "evidence-acceptance:assessment-wake-fixture", "facts-accepted", []projection.OwnedField{
		{Name: "avm_release_version", Value: request.Work.AVMReleaseVersion},
		{Name: "phase", Value: "acquire_source"},
		{Name: "request_id", Value: request.ID},
		{Name: "source_sha", Value: request.SourceSHA},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path, err := identity.StateProjectionIntentPath(request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[path] = state.Record{Kind: "projection_intent", SchemaVersion: projection.SchemaVersion, Data: data}
}
