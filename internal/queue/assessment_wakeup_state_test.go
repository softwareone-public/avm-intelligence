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
