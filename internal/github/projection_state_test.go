package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	neutralprojection "github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestLifecycleProjectionRejectsStaleClaimAndAcknowledgement(t *testing.T) {
	now := time.Now().UTC()
	request, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: "avm-res-lifecycle-delivery", SourceSHA: strings.Repeat("a", 40), DesiredGeneration: 1,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	context, err := queue.ContextFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	status, err := queue.ReduceModuleStatus(queue.ModuleStatusInput{ModuleID: request.ModuleID, Lifecycle: queue.ModuleLifecycleActive, Contexts: []queue.ModuleContext{context}})
	if err != nil {
		t.Fatal(err)
	}
	backend := newProjectionMemoryBackend()
	put := func(path, kind string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		backend.snapshot.Records[path] = state.Record{Kind: kind, SchemaVersion: 1, Data: data}
	}
	requestPath := "state/requests/" + request.ID + ".json"
	put(requestPath, "request", request)
	put("state/projection/intents/"+request.ModuleID+".json", "projection_intent", map[string]any{"schema_version": 1, "module_id": request.ModuleID, "desired_generation": 1, "status": "backlog", "source": map[string]string{"commit": request.SourceSHA}})
	put("state/projection/lifecycle/"+request.ModuleID+".json", "projection_lifecycle", queue.LifecycleIntent{
		SchemaVersion: 1, ModuleID: request.ModuleID, Generation: 1, RequestID: request.ID,
		DesiredGeneration: 1, InputFingerprint: request.Fingerprint, SourceSHA: request.SourceSHA,
		Status: status.Status, Reason: status.Reason, UpdatedAt: now,
	})
	projection := projectionForModule(request.ModuleID)
	projection.Status = status.Status
	projection.SourceSHA = request.SourceSHA
	projection.IssueBody = projection.body()
	intent, err := NewProjectionIntent(projection, 1, ProjectionPriorityActive, now)
	if err != nil {
		t.Fatal(err)
	}
	intent.LifecycleFingerprint, err = LifecycleProjectionFingerprint(backend.snapshot, request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	put(projectionDeliveryPrefix+request.ModuleID+".json", "projection_delivery", intent)
	request.Status = queue.StatusFailed
	put(requestPath, "request", request)
	beforeHead := backend.head
	beforeDelivery := append([]byte(nil), backend.snapshot.Records[projectionDeliveryPrefix+request.ModuleID+".json"].Data...)
	_, evidenceErr := QueueRunEvidenceBatch(t.Context(), projectionCoordinator(backend), "owner", "repository", []RunEvidence{{
		ModuleID: request.ModuleID, Phase: EvidencePhaseProjectionRecovery, RunID: "1-1",
		RunURL: "https://github.com/owner/repository/actions/runs/1", StateHead: backend.head,
		SourceSHA: request.SourceSHA, Status: status.Status, Outcome: EvidenceOutcomePending,
	}}, now)
	if !errors.Is(evidenceErr, ErrProjectionFence) || backend.head != beforeHead || !bytes.Equal(beforeDelivery, backend.snapshot.Records[projectionDeliveryPrefix+request.ModuleID+".json"].Data) {
		t.Fatalf("stale lifecycle changed run evidence: %v", evidenceErr)
	}
	if _, err := ClaimProjectionBatch(t.Context(), projectionCoordinator(backend), "projection-run", now, time.Minute, 1); !errors.Is(err, ErrProjectionFence) {
		t.Fatalf("stale lifecycle claimed: %v", err)
	}
	request.Status = queue.StatusQueued
	put(requestPath, "request", request)
	claim, err := ClaimProjectionBatch(t.Context(), projectionCoordinator(backend), "projection-run", now, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	request.Status = queue.StatusFailed
	put(requestPath, "request", request)
	networkCalls := 0
	client := Client{Token: "fixture", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		networkCalls++
		return nil, errors.New("unexpected stale projection request")
	})}}
	_, _, projectErr := client.projectClaimedIntent(t.Context(), backend, projectionCoordinator(backend), backend.head, claim, ProjectionRunOptions{Owner: "owner", Repository: "repository", ProjectNumber: 1, RetryDelay: time.Minute}, claim.Intents[0])
	if !errors.Is(projectErr, ErrProjectionFence) || networkCalls != 0 {
		t.Fatalf("stale lifecycle reached remote mutation: calls=%d error=%v", networkCalls, projectErr)
	}
	_, batchErr := client.ProjectClaimedBatch(t.Context(), backend, projectionCoordinator(backend), claim, ProjectionRunOptions{Owner: "owner", Repository: "repository", ProjectNumber: 1, RetryDelay: time.Minute})
	if !errors.Is(batchErr, ErrProjectionFence) || networkCalls != 0 {
		t.Fatalf("stale lifecycle reached batch budget bootstrap: calls=%d error=%v", networkCalls, batchErr)
	}
	_, err = RecordProjectionOutcome(t.Context(), projectionCoordinator(backend), ProjectionOutcomeInput{
		ClaimID: claim.Batch.ID, ModuleID: request.ModuleID, RunID: "projection-run", Generation: claim.Intents[0].Generation, Fence: claim.Intents[0].Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: now.Add(time.Second)},
	})
	if !errors.Is(err, ErrProjectionFence) {
		t.Fatalf("stale lifecycle acknowledged: %v", err)
	}
}

func TestProjectionRetryMetadataSurvivesEquivalentGenerationAndClearsOnSuccess(t *testing.T) {
	now := time.Now().UTC()
	request := ratelimit.Request{Resource: "rest", Points: 5, Mutation: true}
	existing := ProjectionIntentRecord{Generation: 1, Fingerprint: "same", DeliveryState: ProjectionRetryable, RetryAt: now.Add(5 * time.Minute), RetryReason: "local_hourly_mutations", RetryRequest: &request}
	updated := newerEquivalentProjectionIntent(existing, ProjectionIntentRecord{Generation: 2, Fingerprint: "same"})
	if updated.RetryReason != existing.RetryReason || updated.RetryRequest == nil || !updated.RetryAt.Equal(existing.RetryAt) {
		t.Fatal("equivalent generation lost retry classification")
	}
	if err := applyProjectionOutcome(&updated, ProjectionOutcome{Success: true, Binding: &ProjectionBinding{}}); err != nil || updated.RetryRequest != nil || updated.RetryReason != "" {
		t.Fatal("success retained stale retry classification")
	}
}

func TestProjectionRetryRequestRejectsCleanupBypass(t *testing.T) {
	intent := ProjectionIntentRecord{RetryRequest: &ratelimit.Request{Resource: "rest", Points: 5, Mutation: true, Checkpoint: true}}
	if err := intent.Validate(); err == nil || !strings.Contains(err.Error(), "retry request") {
		t.Fatalf("retry costs allowed cleanup bypass: %v", err)
	}
}

func TestPreSendCancellationPersistsAsDueRetry(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	backend, _ := projectionReplayTestBackend(t, ProjectionPending, now)
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "cancel-run", now, time.Minute, 1)
	if err != nil || len(claim.Intents) != 1 {
		t.Fatalf("claim retry fixture: claim=%+v err=%v", claim, err)
	}
	intent := claim.Intents[0]
	retryAt := now.Add(2 * time.Minute)
	result, err := RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		ClaimID: claim.Batch.ID, ModuleID: intent.ModuleID, RunID: claim.Batch.RunID,
		Generation: intent.Generation, Fence: intent.Fence,
		Outcome: ProjectionOutcome{ErrorClass: classifyProjectionError(&ProjectionPreSendError{Cause: context.DeadlineExceeded}), RetryAt: retryAt, CompletedAt: now.Add(time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Intent.DeliveryState != ProjectionRetryable || result.Intent.LastErrorClass != ProjectionErrorTransient || !result.Intent.RetryAt.Equal(retryAt) {
		t.Fatalf("pre-send cancellation outcome = %+v; want transient retry at %s", result.Intent, retryAt)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if due, err := DueProjectionModuleIDs(snapshot, now.Add(time.Minute)); err != nil || len(due) != 0 {
		t.Fatalf("cancelled delivery due before retry time: due=%v err=%v", due, err)
	}
	if due, err := DueProjectionModuleIDs(snapshot, retryAt); err != nil || !reflect.DeepEqual(due, []string{intent.ModuleID}) {
		t.Fatalf("cancelled delivery not due on later tick: due=%v err=%v", due, err)
	}
}

func TestReplayAcknowledgedProjectionsPreservesOwnershipAndRetries(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, deliveryState := range []string{ProjectionProjected, ProjectionPending, ProjectionProjecting, ProjectionRetryable, ProjectionNeedsReview} {
		t.Run(deliveryState, func(t *testing.T) {
			assertAcknowledgedProjectionReplay(t, deliveryState, now)
		})
	}
}

func assertAcknowledgedProjectionReplay(t *testing.T, deliveryState string, now time.Time) {
	t.Helper()
	backend, intent := projectionReplayTestBackend(t, deliveryState, now)
	path := projectionDeliveryPrefix + intent.ModuleID + ".json"
	encoded := backend.snapshot.Records[path].Data
	if _, err := ReplayAcknowledgedProjections(context.Background(), projectionCoordinator(backend), []string{intent.ModuleID}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	updated, _, err := readProjectionIntent(backend.snapshot, path)
	if err != nil {
		t.Fatal(err)
	}
	if deliveryState != ProjectionProjected {
		if backend.commits != 0 || !bytes.Equal(encoded, backend.snapshot.Records[path].Data) {
			t.Fatal("replay changed active ownership, pending delivery, review or retry state")
		}
		return
	}
	if updated.DeliveryState != ProjectionPending || updated.Generation != intent.Generation || updated.Fingerprint != intent.Fingerprint || updated.LastAcknowledgedFingerprint != intent.Fingerprint {
		t.Fatalf("replay changed lifecycle identity: %+v", updated)
	}
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "replay-run", now.Add(time.Minute), time.Minute, 1)
	if err != nil || len(claim.Intents) != 1 {
		t.Fatalf("replay was not claimable: %+v %v", claim, err)
	}
}

func projectionReplayTestBackend(t *testing.T, deliveryState string, now time.Time) (*projectionMemoryBackend, ProjectionIntentRecord) {
	t.Helper()
	backend := newProjectionMemoryBackend()
	intent, err := NewProjectionIntent(projectionForModule("avm-res-replay"), 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	intent.DeliveryState = deliveryState
	intent.LastAcknowledgedFingerprint = intent.Fingerprint
	if deliveryState == ProjectionProjecting {
		intent.LeaseOwner, intent.ClaimID, intent.Fence = "active-owner", "active-batch", 1
		intent.LeaseExpiresAt, intent.LeaseDuration = now.Add(time.Hour), time.Hour
	}
	if deliveryState == ProjectionRetryable {
		intent.RetryAt, intent.LastErrorClass = now.Add(time.Hour), ProjectionErrorRateLimited
	}
	if deliveryState == ProjectionNeedsReview {
		intent.LastErrorClass = ProjectionErrorNeedsReview
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[projectionDeliveryPrefix+intent.ModuleID+".json"] = state.Record{Kind: "projection_delivery", SchemaVersion: 1, Data: encoded}
	return backend, intent
}

func TestClaimProjectionBatchWithNoIntentsMakesNoStateOrGitHubWork(t *testing.T) {
	backend := newProjectionMemoryBackend()
	claimed, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-empty", time.Now().UTC(), time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Changed || len(claimed.Batch.ModuleIDs) != 0 || backend.commits != 0 {
		t.Fatalf("empty projection claim = %+v commits=%d, want no batch and no state write", claimed, backend.commits)
	}
	client := Client{Token: "token", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		t.Fatal("GitHub was called for an empty projection batch")
		return nil, nil
	})}}
	if _, err = client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), claimed, ProjectionRunOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectionClaimsOrderRetriesAndPersistBudgetPerModule(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, fixture := range []struct {
		module   string
		priority ProjectionPriority
	}{
		{"avm-res-zeta", ProjectionPriorityCleanup},
		{"avm-res-alpha", ProjectionPriorityChanged},
		{"avm-res-beta", ProjectionPriorityRetry},
	} {
		projection := projectionForModule(fixture.module)
		intent, err := NewProjectionIntent(projection, 1, fixture.priority, now)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
		if err != nil {
			t.Fatal(err)
		}
		backend.head = saved.Head
	}
	admittedBudget := BudgetSnapshot{RESTRemaining: 1200, GraphQLRemaining: 400, SearchRemaining: 80, SecondaryRemaining: 150, RESTCalls: 1}
	claimed, err := ClaimProjectionBatchForModules(context.Background(), projectionCoordinator(backend), ProjectionClaimOptions{
		RunID: "run-claim-1", Now: now, Lease: time.Minute, Limit: 1, Budget: admittedBudget,
	}, []string{"avm-res-zeta", "avm-res-alpha", "avm-res-beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed.Intents) != 1 || claimed.Intents[0].ModuleID != "avm-res-beta" || claimed.Intents[0].Budget != admittedBudget {
		t.Fatalf("claim must select only the highest-priority due module and persist its budget: %+v", claimed)
	}
	_, claimedSnapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	for path, record := range claimedSnapshot.Records {
		if record.Kind == "projection_batch" || strings.Contains(path, "/projection/batches/") {
			t.Fatalf("module claim created forbidden durable batch state at %s", path)
		}
	}
	first := claimed.Intents[0]
	completed, err := RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		ClaimID: claimed.Batch.ID, ModuleID: first.ModuleID, RunID: "run-claim-1", Generation: first.Generation, Fence: first.Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: now.Add(10 * time.Second), Binding: projectionTestBinding(first, now.Add(10*time.Second))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Intent.DeliveryState != ProjectionProjected || completed.Intent.LastAcknowledgedFingerprint != first.Fingerprint {
		t.Fatalf("module projection checkpoint = %+v", completed)
	}
	secondClaim, err := ClaimProjectionBatchForModules(context.Background(), projectionCoordinator(backend), ProjectionClaimOptions{
		RunID: "run-claim-2", Now: now.Add(11 * time.Second), Lease: time.Minute, Limit: 1,
	}, []string{"avm-res-zeta", "avm-res-alpha"})
	if err != nil || len(secondClaim.Intents) != 1 || secondClaim.Intents[0].ModuleID != "avm-res-alpha" {
		t.Fatalf("second independent claim=%+v error=%v", secondClaim, err)
	}
	second := secondClaim.Intents[0]
	retryAt := now.Add(20 * time.Minute)
	retry, err := RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		ClaimID: secondClaim.Batch.ID, ModuleID: second.ModuleID, RunID: "run-claim-2", Generation: second.Generation, Fence: second.Fence,
		Outcome: ProjectionOutcome{ErrorClass: ProjectionErrorRateLimited, RetryAt: retryAt, CompletedAt: now.Add(20 * time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if retry.Batch.Status != ProjectionBatchComplete || retry.Intent.DeliveryState != ProjectionRetryable || !retry.Intent.RetryAt.Equal(retryAt) {
		t.Fatalf("retry checkpoint = %+v", retry)
	}
}

func TestSingleModuleClaimsContinueInPriorityOrderWithoutReclaimingCompletedWork(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	fixtures := []struct {
		moduleID string
		priority ProjectionPriority
	}{
		{moduleID: "avm-res-cleanup", priority: ProjectionPriorityCleanup},
		{moduleID: "avm-res-active", priority: ProjectionPriorityActive},
		{moduleID: "avm-res-changed", priority: ProjectionPriorityChanged},
	}
	for _, fixture := range fixtures {
		intent, err := NewProjectionIntent(projectionForModule(fixture.moduleID), 1, fixture.priority, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent); err != nil {
			t.Fatal(err)
		}
	}

	remaining := []string{"avm-res-cleanup", "avm-res-active", "avm-res-changed"}
	for position, wantModule := range []string{"avm-res-changed", "avm-res-active", "avm-res-cleanup"} {
		claim, err := ClaimProjectionBatchForModules(context.Background(), projectionCoordinator(backend), ProjectionClaimOptions{
			RunID: fmt.Sprintf("single-module-run-%d", position+1), Now: now.Add(time.Duration(position+1) * time.Second), Lease: time.Minute, Limit: 1,
		}, remaining)
		if err != nil {
			t.Fatalf("claim module position %d: %v", position+1, err)
		}
		if len(claim.Intents) != 1 || claim.Intents[0].ModuleID != wantModule {
			t.Fatalf("claim position %d = %#v, want only %s", position+1, claim.Intents, wantModule)
		}
		intent := claim.Intents[0]
		at := now.Add(time.Duration(position+2) * time.Second)
		if _, err = RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
			ClaimID: claim.Batch.ID, ModuleID: intent.ModuleID, RunID: claim.Batch.RunID,
			Generation: intent.Generation, Fence: intent.Fence,
			Outcome: ProjectionOutcome{Success: true, CompletedAt: at, Binding: projectionTestBinding(intent, at)},
		}); err != nil {
			t.Fatalf("complete module %s: %v", intent.ModuleID, err)
		}
		remaining = removeTestProjectionModule(remaining, wantModule)
	}
}

func removeTestProjectionModule(moduleIDs []string, target string) []string {
	remaining := make([]string, 0, len(moduleIDs)-1)
	for _, moduleID := range moduleIDs {
		if moduleID != target {
			remaining = append(remaining, moduleID)
		}
	}
	return remaining
}

func TestDueProjectionIntentsIsReadOnlyAndScoped(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	projection := projectionForModule("avm-res-due-one")
	intent, err := NewProjectionIntent(projection, 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
	if err != nil {
		t.Fatal(err)
	}
	backend.head = saved.Head
	beforeHead, beforeSnapshot, err := projectionCoordinator(backend).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	due, err := DueProjectionIntents(context.Background(), projectionCoordinator(backend), now, []string{"avm-res-due-one", "avm-res-due-two"})
	if err != nil || due != 1 {
		t.Fatalf("scoped due projection count = %d, error=%v, want 1", due, err)
	}
	untargeted, err := DueProjectionIntents(context.Background(), projectionCoordinator(backend), now, []string{"avm-res-due-two"})
	if err != nil || untargeted != 0 {
		t.Fatalf("untargeted due projection count = %d, error=%v, want 0", untargeted, err)
	}
	afterHead, afterSnapshot, err := projectionCoordinator(backend).Read(context.Background())
	if err != nil || afterHead != beforeHead || len(afterSnapshot.Records) != len(beforeSnapshot.Records) {
		t.Fatalf("due preflight wrote state: head %q -> %q, error=%v", beforeHead, afterHead, err)
	}
}

func TestDueProjectionModuleIDsSelectsOnlyCurrentDueWork(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for _, fixture := range []struct {
		moduleID string
		state    string
		retryAt  time.Time
	}{
		{moduleID: "avm-res-due-pending", state: ProjectionPending},
		{moduleID: "avm-res-due-retry", state: ProjectionRetryable, retryAt: now.Add(-time.Second)},
		{moduleID: "avm-res-future-retry", state: ProjectionRetryable, retryAt: now.Add(time.Hour)},
		{moduleID: "avm-res-already-projected", state: ProjectionProjected},
	} {
		intent, err := NewProjectionIntent(projectionForModule(fixture.moduleID), 1, ProjectionPriorityChanged, now.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		intent.DeliveryState = fixture.state
		if fixture.state == ProjectionRetryable {
			intent.RetryAt, intent.LastErrorClass = fixture.retryAt, ProjectionErrorTransient
		}
		if fixture.state == ProjectionProjected {
			intent.LastAcknowledgedFingerprint = intent.Fingerprint
		}
		saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
		if err != nil {
			t.Fatal(err)
		}
		backend.head = saved.Head
	}
	_, snapshot, err := projectionCoordinator(backend).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids, err := DueProjectionModuleIDs(snapshot, now)
	want := []string{"avm-res-due-retry", "avm-res-due-pending"}
	if err != nil || !reflect.DeepEqual(ids, want) {
		t.Fatalf("due projection IDs=%v error=%v, want %v", ids, err, want)
	}
	nextTick, err := DueProjectionModuleIDs(snapshot, now.Add(time.Hour))
	wantNextTick := []string{"avm-res-due-retry", "avm-res-future-retry", "avm-res-due-pending"}
	if err != nil || !reflect.DeepEqual(nextTick, wantNextTick) {
		t.Fatalf("next-tick projection IDs=%v error=%v, want %v", nextTick, err, wantNextTick)
	}
}

func TestProjectionCheckpointRenewsOnlyTheClaimedModuleLease(t *testing.T) {
	backend := newProjectionMemoryBackend()
	startedAt := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	moduleID := "avm-res-lease-a"
	intent, err := NewProjectionIntent(projectionForModule(moduleID), 1, ProjectionPriorityChanged, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-lease", startedAt, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	first := claim.Intents[0]
	if _, err = RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		ClaimID: claim.Batch.ID, ModuleID: first.ModuleID, RunID: claim.Batch.RunID,
		Generation: first.Generation, Fence: first.Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: startedAt.Add(61 * time.Second), Binding: projectionTestBinding(first, startedAt.Add(61*time.Second))},
	}); err == nil {
		t.Fatal("expired projection lease acknowledged without a renewal")
	}
	checkpointAt := startedAt.Add(50 * time.Second)
	_, err = CheckpointProjectionSnapshot(context.Background(), projectionCoordinator(backend), ProjectionSnapshotCheckpoint{
		ClaimID: claim.Batch.ID, RunID: claim.Batch.RunID, ModuleID: moduleID, Cursors: map[string]string{"issues_page": "complete"}, CheckpointedAt: checkpointAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, checkpointed, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	stored, exists, err := readProjectionIntent(checkpointed, projectionDeliveryPrefix+moduleID+".json")
	if err != nil || !exists || stored.SnapshotCursors["issues_page"] != "complete" || !stored.LeaseExpiresAt.Equal(checkpointAt.Add(time.Minute)) {
		t.Fatalf("per-module checkpoint did not persist cursor and lease: intent=%+v exists=%t err=%v", stored, exists, err)
	}
	firstCompletedAt := startedAt.Add(100 * time.Second)
	firstResult, err := RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		ClaimID: claim.Batch.ID, ModuleID: first.ModuleID, RunID: claim.Batch.RunID,
		Generation: first.Generation, Fence: first.Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: firstCompletedAt, Binding: projectionTestBinding(first, firstCompletedAt)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !firstResult.Intent.LeaseExpiresAt.IsZero() || firstResult.Intent.DeliveryState != ProjectionProjected {
		t.Fatalf("completed module retained lease: %+v", firstResult.Intent)
	}
}

func TestProjectionIntentGenerationFencesLateRemoteWrites(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	projection := projectionForModule("avm-res-fenced")
	intent, err := NewProjectionIntent(projection, 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-old", now, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	changed := projection
	changed.SourceSHA = strings.Repeat("b", 40)
	changed.IssueBody = changed.body()
	newIntent, err := NewProjectionIntent(changed, 2, ProjectionPriorityChanged, now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, err = SaveProjectionIntent(context.Background(), projectionCoordinator(backend), newIntent)
	if err != nil {
		t.Fatal(err)
	}
	old := claimed.Intents[0]
	if _, err = RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		ClaimID: claimed.Batch.ID, ModuleID: old.ModuleID, RunID: "run-old", Generation: old.Generation, Fence: old.Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: now.Add(10 * time.Second), Binding: projectionTestBinding(old, now.Add(10*time.Second))},
	}); err == nil {
		t.Fatal("stale projection generation acknowledged after desired intent changed")
	}
}

func TestSuccessfulProjectionAcknowledgesExactNeutralHandoff(t *testing.T) {
	backend := newProjectionMemoryBackend()
	moduleID := "avm-res-neutral-ack"
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	desired, err := neutralprojection.NewDesiredIntent(moduleID, 7, "dispatch:request-1", "queued", []neutralprojection.OwnedField{{Name: "request_id", Value: "request-1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	neutralID, err := desired.Identity()
	if err != nil {
		t.Fatal(err)
	}
	intentPath, err := identity.StateProjectionIntentPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	desiredData, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[intentPath] = state.Record{Kind: "projection_intent", SchemaVersion: neutralprojection.SchemaVersion, Data: desiredData}

	delivery, err := NewProjectionIntent(projectionForModule(moduleID), 1, ProjectionPriorityActive, now)
	if err != nil {
		t.Fatal(err)
	}
	delivery.NeutralProjection = &neutralID
	if _, err = SaveProjectionIntent(context.Background(), projectionCoordinator(backend), delivery); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "neutral-ack-run", now, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	claimed := claim.Intents[0]
	completedAt := now.Add(time.Second)
	if _, err = RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		ClaimID: claim.Batch.ID, ModuleID: moduleID, RunID: claim.Batch.RunID,
		Generation: claimed.Generation, Fence: claimed.Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: completedAt, Binding: projectionTestBinding(claimed, completedAt)},
	}); err != nil {
		t.Fatal(err)
	}
	ackPath, err := identity.StateProjectionAcknowledgementPath(moduleID, neutralID.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	ackRecord, exists := backend.snapshot.Records[ackPath]
	var acknowledgement neutralprojection.Acknowledgement
	if !exists || ackRecord.Kind != "projection_acknowledgement" || json.Unmarshal(ackRecord.Data, &acknowledgement) != nil || acknowledgement.Validate(neutralID, completedAt) != nil {
		t.Fatalf("successful delivery did not persist the exact neutral acknowledgement: exists=%t record=%+v ack=%+v", exists, ackRecord, acknowledgement)
	}

	nextDesired, err := neutralprojection.NewDesiredIntent(moduleID, 8, "dispatch:request-2", "analyzing", []neutralprojection.OwnedField{{Name: "request_id", Value: "request-2"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	nextNeutralID, err := nextDesired.Identity()
	if err != nil {
		t.Fatal(err)
	}
	nextDesiredData, err := json.Marshal(nextDesired)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[intentPath] = state.Record{Kind: "projection_intent", SchemaVersion: neutralprojection.SchemaVersion, Data: nextDesiredData}
	nextDelivery := claimed
	nextDelivery.Generation = 2
	nextDelivery.NeutralProjection = &nextNeutralID
	nextDelivery.UpdatedAt = completedAt.Add(time.Second)
	if _, err = SaveProjectionIntent(context.Background(), projectionCoordinator(backend), nextDelivery); err != nil {
		t.Fatal(err)
	}
	updated, exists, err := readProjectionIntent(backend.snapshot, projectionDeliveryPrefix+moduleID+".json")
	if err != nil || !exists || updated.DeliveryState != ProjectionPending {
		t.Fatalf("new neutral handoff with unchanged board content must be re-read: intent=%+v exists=%t err=%v", updated, exists, err)
	}
	nextClaim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "neutral-ack-run-2", completedAt.Add(2*time.Second), time.Minute, 1)
	if err != nil || len(nextClaim.Intents) != 1 {
		t.Fatalf("claim newer neutral handoff: claim=%+v err=%v", nextClaim, err)
	}
	nextCompletedAt := completedAt.Add(3 * time.Second)
	if _, err = RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		ClaimID: nextClaim.Batch.ID, ModuleID: moduleID, RunID: nextClaim.Batch.RunID,
		Generation: nextClaim.Intents[0].Generation, Fence: nextClaim.Intents[0].Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: nextCompletedAt, Binding: projectionTestBinding(nextClaim.Intents[0], nextCompletedAt)},
	}); err != nil {
		t.Fatal(err)
	}
	nextAckPath, err := identity.StateProjectionAcknowledgementPath(moduleID, nextNeutralID.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	nextAckRecord, exists := backend.snapshot.Records[nextAckPath]
	var nextAcknowledgement neutralprojection.Acknowledgement
	if !exists || nextAckRecord.Kind != "projection_acknowledgement" || json.Unmarshal(nextAckRecord.Data, &nextAcknowledgement) != nil || nextAcknowledgement.Validate(nextNeutralID, nextCompletedAt) != nil {
		t.Fatalf("newer neutral handoff was not acknowledged exactly: exists=%t record=%+v ack=%+v", exists, nextAckRecord, nextAcknowledgement)
	}
}

func TestNeutralProjectionIsRevalidatedBeforeRemoteEffects(t *testing.T) {
	backend := newProjectionMemoryBackend()
	moduleID := "avm-res-neutral-fence"
	now := time.Date(2026, 10, 11, 13, 0, 0, 0, time.UTC)
	desired, err := neutralprojection.NewDesiredIntent(moduleID, 1, "dispatch:request-1", "queued", []neutralprojection.OwnedField{{Name: "request_id", Value: "request-1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	neutralID, err := desired.Identity()
	if err != nil {
		t.Fatal(err)
	}
	intentPath, err := identity.StateProjectionIntentPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	desiredData, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[intentPath] = state.Record{Kind: "projection_intent", SchemaVersion: neutralprojection.SchemaVersion, Data: desiredData}
	delivery, err := NewProjectionIntent(projectionForModule(moduleID), 1, ProjectionPriorityActive, now)
	if err != nil {
		t.Fatal(err)
	}
	delivery.NeutralProjection = &neutralID
	if _, err = SaveProjectionIntent(context.Background(), projectionCoordinator(backend), delivery); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "neutral-fence-run", now, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := neutralprojection.NewDesiredIntent(moduleID, 2, "dispatch:request-2", "analyzing", []neutralprojection.OwnedField{{Name: "request_id", Value: "request-2"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	newerData, err := json.Marshal(newer)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[intentPath] = state.Record{Kind: "projection_intent", SchemaVersion: neutralprojection.SchemaVersion, Data: newerData}
	calls := 0
	client := Client{
		Token: "fixture", Budget: &ProjectionBudget{RESTRemaining: 100, GraphQLRemaining: 100, SearchRemaining: 100, SecondaryRemaining: 100},
		HTTPClient: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(`{"total_count":0,"incomplete_results":false,"items":[]}`)
		})},
	}
	_, err = client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), claim, ProjectionRunOptions{
		Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute,
	})
	if err == nil || calls != 0 {
		t.Fatalf("stale neutral delivery was not fenced before remote effects: err=%v calls=%d", err, calls)
	}
}

func TestNeutralProjectionChangeDuringReadFencesNextMutation(t *testing.T) {
	backend := newProjectionMemoryBackend()
	moduleID := "avm-res-neutral-midread"
	now := time.Date(2026, 10, 11, 14, 0, 0, 0, time.UTC)
	desired, err := neutralprojection.NewDesiredIntent(moduleID, 1, "dispatch:request-1", "queued", []neutralprojection.OwnedField{{Name: "request_id", Value: "request-1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	neutralID, err := desired.Identity()
	if err != nil {
		t.Fatal(err)
	}
	intentPath, err := identity.StateProjectionIntentPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	desiredData, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[intentPath] = state.Record{Kind: "projection_intent", SchemaVersion: neutralprojection.SchemaVersion, Data: desiredData}
	delivery, err := NewProjectionIntent(projectionForModule(moduleID), 1, ProjectionPriorityActive, now)
	if err != nil {
		t.Fatal(err)
	}
	delivery.NeutralProjection = &neutralID
	if _, err = SaveProjectionIntent(context.Background(), projectionCoordinator(backend), delivery); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "neutral-midread-run", now, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := neutralprojection.NewDesiredIntent(moduleID, 2, "dispatch:request-2", "analyzing", []neutralprojection.OwnedField{{Name: "request_id", Value: "request-2"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	newerData, err := json.Marshal(newer)
	if err != nil {
		t.Fatal(err)
	}
	mutatedState, mutations := false, 0
	transport := roundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/search/issues") && !mutatedState {
			mutatedState = true
			backend.snapshot.Records[intentPath] = state.Record{Kind: "projection_intent", SchemaVersion: neutralprojection.SchemaVersion, Data: newerData}
			return jsonResponse(`{"total_count":0,"incomplete_results":false,"items":[]}`)
		}
		if request.Method != http.MethodGet {
			mutations++
		}
		return jsonResponse(`{}`)
	})
	client := Client{
		Token: "fixture", Budget: &ProjectionBudget{RESTRemaining: 1000, GraphQLRemaining: 1000, SearchRemaining: 100, SecondaryRemaining: 1000},
		HTTPClient: &http.Client{Transport: transport},
	}
	result, err := client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), claim, ProjectionRunOptions{
		Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute,
	})
	if !errors.Is(err, ErrProjectionFence) || !mutatedState || mutations != 0 || result.Completed != 0 {
		t.Fatalf("stale neutral intent passed the mutation fence: result=%+v err=%v read=%t mutations=%d", result, err, mutatedState, mutations)
	}
}

func TestExpiredProjectionOwnerRemainsParkedUntilReconciliation(t *testing.T) {
	backend := newProjectionMemoryBackend()
	startedAt := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	intent, err := NewProjectionIntent(projectionForModule("avm-res-expired-owner"), 1, ProjectionPriorityChanged, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
	if err != nil {
		t.Fatal(err)
	}
	backend.head = saved.Head
	oldClaim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-old-owner", startedAt, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	newClaim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-recovery", startedAt.Add(time.Minute), time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(newClaim.Intents) != 0 {
		t.Fatalf("expired owner was reclaimed without reconciliation: %+v", newClaim)
	}
	old := oldClaim.Intents[0]
	_, err = RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		ClaimID: oldClaim.Batch.ID, ModuleID: old.ModuleID, RunID: oldClaim.Batch.RunID,
		Generation: old.Generation, Fence: old.Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: startedAt.Add(2 * time.Minute), Binding: projectionTestBinding(old, startedAt.Add(2*time.Minute))},
	})
	if !errors.Is(err, ErrProjectionFence) {
		t.Fatalf("expired owner callback error = %v, want ErrProjectionFence", err)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	stored, exists, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+old.ModuleID+".json")
	if err != nil || !exists || stored.DeliveryState != ProjectionProjecting || stored.LeaseOwner != oldClaim.Batch.RunID {
		t.Fatalf("expired owner was cleared without verified recovery: intent=%+v exists=%t err=%v", stored, exists, err)
	}
}

func TestNewerDesiredProjectionPreservesUnknownInFlightOwner(t *testing.T) {
	backend := newProjectionMemoryBackend()
	startedAt := time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC)
	projection := projectionForModule("avm-res-newer-during-write")
	intent, err := NewProjectionIntent(projection, 1, ProjectionPriorityChanged, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
	if err != nil {
		t.Fatal(err)
	}
	backend.head = saved.Head
	oldClaim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-old-status", startedAt, time.Minute, 1)
	if err != nil || len(oldClaim.Intents) != 1 {
		t.Fatalf("initial projection claim = %+v, err=%v", oldClaim, err)
	}
	projection.Status = "Analyzing"
	newIntent, err := NewProjectionIntent(projection, 2, ProjectionPriorityChanged, startedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), newIntent)
	if err != nil {
		t.Fatal(err)
	}
	backend.head = updated.Head
	if len(updated.Intents) != 1 || updated.Intents[0].Generation != 2 || updated.Intents[0].DeliveryState != ProjectionProjecting || updated.Intents[0].LeaseOwner != oldClaim.Batch.RunID || updated.Intents[0].ClaimID != oldClaim.Batch.ID || updated.Intents[0].Fence != oldClaim.Intents[0].Fence {
		t.Fatalf("new desired projection did not preserve and fence the unknown owner: %+v", updated.Intents)
	}
	newClaim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-new-status", startedAt.Add(2*time.Minute), time.Minute, 1)
	if err != nil || len(newClaim.Intents) != 0 {
		t.Fatalf("new desired projection started before owner reconciliation: claim=%+v err=%v", newClaim, err)
	}
	_, err = RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		ClaimID: oldClaim.Batch.ID, ModuleID: projection.ModuleID, RunID: oldClaim.Batch.RunID,
		Generation: oldClaim.Intents[0].Generation, Fence: oldClaim.Intents[0].Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: startedAt.Add(30 * time.Second), Binding: projectionTestBinding(oldClaim.Intents[0], startedAt.Add(30*time.Second))},
	})
	if !errors.Is(err, ErrProjectionFence) {
		t.Fatalf("old owner outcome after desired status change = %v, want ErrProjectionFence", err)
	}
}

func TestExpiredProjectionOwnerCannotStartAnotherRemoteMutation(t *testing.T) {
	backend := newProjectionMemoryBackend()
	startedAt := time.Now().UTC()
	intent, err := NewProjectionIntent(projectionForModule("avm-res-expired-write"), 1, ProjectionPriorityChanged, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
	if err != nil {
		t.Fatal(err)
	}
	backend.head = saved.Head
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-expired-write", startedAt, time.Millisecond, 1)
	if err != nil {
		t.Fatal(err)
	}
	var searches, mutations int
	client := Client{Token: "fixture-token", Pacer: ratelimit.New(), HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/search/issues" {
			searches++
			return jsonResponse(`{"total_count":0,"incomplete_results":false,"items":[]}`)
		}
		mutations++
		return jsonResponse(`{"number":7,"node_id":"ISSUE","html_url":"https://github.com/softwareone-public/avm-intelligence/issues/7"}`)
	})}}
	time.Sleep(2 * time.Millisecond)
	_, err = client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), claim, ProjectionRunOptions{
		Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute,
		BudgetPolicy: ProjectionBudgetPolicy{SecondaryReserve: 1}, InitialBudget: &ProjectionBudget{RESTRemaining: 100, GraphQLRemaining: 100, SearchRemaining: 100, SecondaryRemaining: 100},
	})
	if !errors.Is(err, ErrProjectionFence) {
		t.Fatalf("expired projection run error = %v, want ErrProjectionFence", err)
	}
	if searches != 1 || mutations != 0 {
		t.Fatalf("expired owner made %d searches and %d mutations, want one read and no writes", searches, mutations)
	}
}

func TestInFlightIssueCreateCannotBeFollowedByStaleProjectionMutation(t *testing.T) {
	backend := newProjectionMemoryBackend()
	startedAt := time.Now().UTC()
	projection := projectionForModule("avm-res-delayed-write")
	intent, err := NewProjectionIntent(projection, 1, ProjectionPriorityChanged, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
	if err != nil {
		t.Fatal(err)
	}
	backend.head = saved.Head
	lease := 150 * time.Millisecond
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-delayed-write", startedAt, lease, 1)
	if err != nil {
		t.Fatal(err)
	}
	transport := &delayedIssueCreateTransport{reached: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-transport.release:
		default:
			close(transport.release)
		}
	})
	cache := &ProjectionMetadataCache{}
	cache.setIssueIndex("softwareone-public/avm-intelligence", map[string][]Issue{})
	cache.setProject("softwareone-public:1", "PROJECT")
	cache.setProjectFields("PROJECT", map[string]fieldRef{})
	cache.setProjectItems("PROJECT", map[int]ProjectItemSnapshot{})
	for _, label := range Labels(projection) {
		cache.setRepoLabel("softwareone-public/avm-intelligence", label, true)
	}
	client := Client{
		Token: "fixture-token", Pacer: ratelimit.New(), MetadataCache: cache,
		HTTPClient: &http.Client{Transport: transport},
	}
	type runResult struct {
		err error
	}
	finished := make(chan runResult, 1)
	go func() {
		_, runErr := client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), claim, ProjectionRunOptions{
			Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute,
			BudgetPolicy: ProjectionBudgetPolicy{SecondaryReserve: 1}, InitialBudget: &ProjectionBudget{RESTRemaining: 100, GraphQLRemaining: 100, SearchRemaining: 100, SecondaryRemaining: 100},
		})
		finished <- runResult{err: runErr}
	}()
	select {
	case <-transport.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("Issue create did not reach the delayed transport")
	}
	time.Sleep(lease + 20*time.Millisecond)
	recovered, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-delayed-recovery", startedAt.Add(lease), time.Minute, 1)
	if err != nil {
		t.Fatalf("check due projection while Issue create is in flight: %v", err)
	}
	if len(recovered.Intents) != 0 {
		t.Fatalf("new owner claimed module while prior Issue create was in flight: %+v", recovered)
	}
	close(transport.release)
	select {
	case result := <-finished:
		if !errors.Is(result.err, ErrProjectionFence) {
			t.Fatalf("stale owner result = %v, want ErrProjectionFence", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale owner did not finish after its in-flight write returned")
	}
	if got := transport.issueCreates.Load(); got != 1 {
		t.Fatalf("in-flight Issue creates = %d, want exactly one", got)
	}
	if got := transport.labelWrites.Load(); got != 0 {
		t.Fatalf("stale owner started %d follow-up label writes after its lease expired", got)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	stored, exists, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+projection.ModuleID+".json")
	if err != nil || !exists || stored.DeliveryState != ProjectionProjecting || stored.LeaseOwner != claim.Batch.RunID {
		t.Fatalf("in-flight owner was retired without verified reconciliation: intent=%+v exists=%t err=%v", stored, exists, err)
	}
}

func TestExpiredProjectionOwnerRequiresExactTerminalAttemptBeforeRecovery(t *testing.T) {
	startedAt := time.Now().UTC().Truncate(time.Second)
	backend := newProjectionMemoryBackend()
	intent, err := NewProjectionIntent(projectionForModule("avm-res-owner-recovery"), 1, ProjectionPriorityChanged, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = SaveProjectionIntent(t.Context(), projectionCoordinator(backend), intent); err != nil {
		t.Fatal(err)
	}
	ownerRunID, ownerRunAttempt := uint64(987654321), uint64(2)
	claim, err := ClaimProjectionBatchForModules(t.Context(), projectionCoordinator(backend), ProjectionClaimOptions{
		RunID: "987654321-2", OwnerRunID: ownerRunID, OwnerRunAttempt: ownerRunAttempt,
		Now: startedAt, Lease: time.Minute, Limit: 1,
	}, []string{intent.ModuleID})
	if err != nil {
		t.Fatal(err)
	}
	if len(claim.Intents) != 1 || claim.Intents[0].OwnerRunID != ownerRunID || claim.Intents[0].OwnerRunAttempt != ownerRunAttempt {
		t.Fatalf("claim did not persist exact owner attempt: %+v", claim.Intents)
	}
	recoveryAt := startedAt.Add(time.Minute)
	owners, err := ExpiredProjectionOwners(backend.snapshot, recoveryAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0].ClaimID != claim.Batch.ID || owners[0].RunID != ownerRunID || owners[0].RunAttempt != ownerRunAttempt {
		t.Fatalf("expired owner candidates = %+v", owners)
	}
	observation := ProjectionOwnerObservation{Repository: "softwareone-public/avm-intelligence", WorkflowPath: ".github/workflows/factory-projection.yml", HeadBranch: "main", RunID: ownerRunID, RunAttempt: ownerRunAttempt, Status: "in_progress", CurrentRunAttempt: ownerRunAttempt, CurrentRunStatus: "completed"}
	if _, err := RecoverProjectionOwner(t.Context(), projectionCoordinator(backend), owners[0], observation, recoveryAt); err == nil {
		t.Fatal("active Actions attempt was allowed to release its owner")
	}
	observation.Status, observation.Conclusion = "completed", "failure"
	observation.CurrentRunStatus = "in_progress"
	if _, err := RecoverProjectionOwner(t.Context(), projectionCoordinator(backend), owners[0], observation, recoveryAt); err == nil {
		t.Fatal("active latest Actions attempt was allowed to release an older owner")
	}
	observation.CurrentRunStatus = "completed"
	observation.RunAttempt++
	if _, err := RecoverProjectionOwner(t.Context(), projectionCoordinator(backend), owners[0], observation, recoveryAt); err == nil {
		t.Fatal("different Actions attempt was allowed to release its owner")
	}
	observation.RunAttempt--
	recovered, err := RecoverProjectionOwner(t.Context(), projectionCoordinator(backend), owners[0], observation, recoveryAt)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.DeliveryState != ProjectionNeedsReview || recovered.ClaimID != "" || recovered.LeaseOwner != "" || recovered.OwnerRunID != 0 || recovered.OwnerRunAttempt != 0 || recovered.Fence != claim.Intents[0].Fence+1 || recovered.RetryReason != projectionRetryOwnerEffectUnknown {
		t.Fatalf("recovery did not fence and park the owner with an unresolved remote effect: %+v", recovered)
	}
	if got, err := DueProjectionModuleIDs(backend.snapshot, recoveryAt); err != nil || len(got) != 0 {
		t.Fatalf("recovered delivery with unknown remote effect must stay parked: modules=%v error=%v", got, err)
	}
	newerProjection := projectionForModule(intent.ModuleID)
	newerProjection.Version = "v2.0.0"
	newer, err := NewProjectionIntent(newerProjection, intent.Generation+1, ProjectionPriorityChanged, recoveryAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = SaveProjectionIntent(t.Context(), projectionCoordinator(backend), newer); err != nil {
		t.Fatal(err)
	}
	_, fresh, err := state.ReadSnapshot(t.Context(), backend)
	if err != nil {
		t.Fatal(err)
	}
	parked, exists, err := readProjectionIntent(fresh, projectionDeliveryPrefix+intent.ModuleID+".json")
	if err != nil || !exists || parked.DeliveryState != ProjectionNeedsReview || parked.RetryReason != projectionRetryOwnerEffectUnknown || parked.Projection.Version != "v2.0.0" || parked.UnknownEffect == nil || parked.UnknownEffect.Projection.Version != intent.Projection.Version || parked.UnknownEffect.Fingerprint != intent.Fingerprint {
		t.Fatalf("new desired content cleared unresolved remote-effect hold: intent=%+v exists=%t err=%v", parked, exists, err)
	}
	if got, err := DueProjectionModuleIDs(fresh, recoveryAt.Add(time.Hour)); err != nil || len(got) != 0 {
		t.Fatalf("newer desired content made unresolved remote effect due: modules=%v error=%v", got, err)
	}
}

func TestUnknownProjectionEffectAdoptsOnlyExactReadback(t *testing.T) {
	probe := projectionForModule("avm-res-readback")
	mismatchCases := []string{"exact", "issue-title", "issue-marker", "issue-summary", "managed-labels", "missing-project-item", "mismatched-project-item"}
	for fieldName := range desiredProjectFieldValues(probe) {
		mismatchCases = append(mismatchCases, "project-field:"+fieldName)
	}
	for _, mismatch := range mismatchCases {
		t.Run(mismatch, func(t *testing.T) {
			startedAt := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
			backend := newProjectionMemoryBackend()
			desired := projectionForModule("avm-res-readback")
			desired.IssueTitle = "Module analysis: " + desired.ModuleID
			desired.IssueBody = desired.body()
			intent, err := NewProjectionIntent(desired, 1, ProjectionPriorityChanged, startedAt)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = SaveProjectionIntent(t.Context(), projectionCoordinator(backend), intent); err != nil {
				t.Fatal(err)
			}
			claim, err := ClaimProjectionBatchForModules(t.Context(), projectionCoordinator(backend), ProjectionClaimOptions{
				RunID: "987654321-1", OwnerRunID: 987654321, OwnerRunAttempt: 1,
				Now: startedAt, Lease: time.Minute, Limit: 1,
			}, []string{desired.ModuleID})
			if err != nil {
				t.Fatal(err)
			}
			owner := ProjectionOwner{ModuleID: desired.ModuleID, ClaimID: claim.Batch.ID, Fence: claim.Intents[0].Fence,
				RunID: 987654321, RunAttempt: 1, LeaseExpiresAt: claim.Intents[0].LeaseExpiresAt}
			observation := ProjectionOwnerObservation{Repository: "softwareone-public/avm-intelligence", WorkflowPath: ".github/workflows/factory-projection.yml", HeadBranch: "main",
				RunID: owner.RunID, RunAttempt: owner.RunAttempt, Status: "completed", Conclusion: "failure", CurrentRunAttempt: owner.RunAttempt, CurrentRunStatus: "completed"}
			if _, err = RecoverProjectionOwner(t.Context(), projectionCoordinator(backend), owner, observation, owner.LeaseExpiresAt); err != nil {
				t.Fatal(err)
			}
			_, snapshot, err := state.ReadSnapshot(t.Context(), backend)
			if err != nil {
				t.Fatal(err)
			}
			target, found, err := UnknownProjectionEffectForModule(snapshot, desired.ModuleID)
			if err != nil || !found || target.Fingerprint != intent.Fingerprint {
				t.Fatalf("unknown projection target = %+v, found=%t error=%v", target, found, err)
			}
			var mutations int
			client := Client{Token: "read-only-token", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
				var body []byte
				if request.Body != nil {
					body, _ = io.ReadAll(request.Body)
				}
				if request.Method != http.MethodGet && request.URL.Path != "/graphql" || strings.Contains(string(body), "mutation") {
					mutations++
				}
				switch request.URL.Path {
				case "/search/issues":
					issueBody, bodyErr := fleetProjectionAcknowledgement(desired.IssueBody)
					if bodyErr != nil {
						return nil, bodyErr
					}
					if mismatch == "issue-summary" {
						issueBody = strings.Replace(issueBody, "- Status: `"+desired.Status+"`", "- Status: `Blocked`", 1)
					}
					if mismatch == "issue-marker" {
						issueBody = strings.Replace(issueBody, Marker+desired.ModuleID, Marker+desired.ModuleID+"-other", 1)
					}
					title := desired.IssueTitle
					if mismatch == "issue-title" {
						title += " changed"
					}
					labels := Labels(desired)
					if mismatch == "managed-labels" && len(labels) > 0 {
						labels = labels[1:]
					}
					items, marshalErr := json.Marshal(map[string]any{"total_count": 1, "incomplete_results": false, "items": []map[string]any{{
						"number": 7, "node_id": "ISSUE", "html_url": "https://github.com/softwareone-public/avm-intelligence/issues/7",
						"title": title, "body": issueBody, "labels": labelsJSON(labels),
					}}})
					if marshalErr != nil {
						return nil, marshalErr
					}
					return jsonResponse(string(items))
				case "/graphql":
					var payload struct {
						Query string `json:"query"`
					}
					if err := json.Unmarshal(body, &payload); err != nil {
						return nil, err
					}
					switch {
					case strings.Contains(payload.Query, "projectV2(number"):
						return jsonResponse(`{"data":{"organization":{"projectV2":{"id":"PROJECT","title":"AVM Intelligence Factory"}}}}`)
					case strings.Contains(payload.Query, "fields(first:50)"):
						values := desiredProjectFieldValues(desired)
						fields := make([]map[string]any, 0, len(values))
						for field, value := range values {
							fields = append(fields, map[string]any{"id": "field-" + field, "name": field, "options": []map[string]string{{"id": "option-" + field, "name": value}}})
						}
						encoded, marshalErr := json.Marshal(map[string]any{"data": map[string]any{"node": map[string]any{"fields": map[string]any{"nodes": fields}}}})
						if marshalErr != nil {
							return nil, marshalErr
						}
						return jsonResponse(string(encoded))
					case strings.Contains(payload.Query, "items(first:100"):
						values := desiredProjectFieldValues(desired)
						if strings.HasPrefix(mismatch, "project-field:") {
							values[strings.TrimPrefix(mismatch, "project-field:")] = "unexpected value"
						}
						fieldValues := make([]map[string]any, 0, len(values))
						for field, value := range values {
							fieldValues = append(fieldValues, map[string]any{"field": map[string]string{"name": field}, "name": value})
						}
						nodes := []map[string]any{{"id": "ITEM", "content": map[string]int{"number": 7}, "fieldValues": map[string]any{"nodes": fieldValues}}}
						if mismatch == "missing-project-item" {
							nodes = []map[string]any{}
						}
						if mismatch == "mismatched-project-item" {
							nodes[0]["content"] = map[string]int{"number": 8}
						}
						encoded, marshalErr := json.Marshal(map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{
							"nodes":    nodes,
							"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
						}}}})
						if marshalErr != nil {
							return nil, marshalErr
						}
						return jsonResponse(string(encoded))
					default:
						return nil, fmt.Errorf("unexpected projection readback query")
					}
				default:
					return nil, fmt.Errorf("unexpected projection readback path %s", request.URL.Path)
				}
			})}}
			readback, exact, err := client.ReadbackFleet(t.Context(), "softwareone-public", "avm-intelligence", 1, target)
			if err != nil {
				t.Fatal(err)
			}
			if exact != (mismatch == "exact") {
				t.Fatalf("readback exact=%t, case=%s", exact, mismatch)
			}
			if mutations != 0 {
				t.Fatalf("readback attempted %d remote mutations", mutations)
			}
			if mismatch != "exact" {
				_, parked, readErr := UnknownProjectionEffectForModule(backend.snapshot, desired.ModuleID)
				if readErr != nil || !parked {
					t.Fatalf("mixed readback cleared the hold: found=%t error=%v", parked, readErr)
				}
				return
			}
			if _, forgedErr := AdoptProjectionReadback(t.Context(), projectionCoordinator(backend), ProjectionReadback{}, startedAt.Add(2*time.Minute)); forgedErr == nil {
				t.Fatal("caller-created empty readback unexpectedly authorized adoption")
			}
			adopted, err := AdoptProjectionReadback(t.Context(), projectionCoordinator(backend), readback, startedAt.Add(2*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if adopted.DeliveryState != ProjectionProjected || adopted.LastAcknowledgedFingerprint != target.Fingerprint || adopted.UnknownEffect != nil {
				t.Fatalf("exact readback was not adopted: %+v", adopted)
			}
		})
	}
}

func TestExactOldReadbackLeavesNewerProjectionPending(t *testing.T) {
	startedAt := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	backend := newProjectionMemoryBackend()
	oldProjection := projectionForModule("avm-res-readback-superseded")
	oldProjection.IssueTitle = "Module analysis: " + oldProjection.ModuleID
	oldProjection.IssueBody = oldProjection.body()
	oldIntent, err := NewProjectionIntent(oldProjection, 1, ProjectionPriorityChanged, startedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = SaveProjectionIntent(t.Context(), projectionCoordinator(backend), oldIntent); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimProjectionBatchForModules(t.Context(), projectionCoordinator(backend), ProjectionClaimOptions{
		RunID: "987654322-1", OwnerRunID: 987654322, OwnerRunAttempt: 1,
		Now: startedAt, Lease: time.Minute, Limit: 1,
	}, []string{oldProjection.ModuleID})
	if err != nil {
		t.Fatal(err)
	}
	owner := ProjectionOwner{ModuleID: oldProjection.ModuleID, ClaimID: claim.Batch.ID, Fence: claim.Intents[0].Fence,
		RunID: 987654322, RunAttempt: 1, LeaseExpiresAt: claim.Intents[0].LeaseExpiresAt}
	observation := ProjectionOwnerObservation{Repository: "softwareone-public/avm-intelligence", WorkflowPath: ".github/workflows/factory-projection.yml", HeadBranch: "main",
		RunID: owner.RunID, RunAttempt: owner.RunAttempt, Status: "completed", Conclusion: "failure", CurrentRunAttempt: owner.RunAttempt, CurrentRunStatus: "completed"}
	if _, err = RecoverProjectionOwner(t.Context(), projectionCoordinator(backend), owner, observation, owner.LeaseExpiresAt); err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := state.ReadSnapshot(t.Context(), backend)
	if err != nil {
		t.Fatal(err)
	}
	target, found, err := UnknownProjectionEffectForModule(snapshot, oldProjection.ModuleID)
	if err != nil || !found {
		t.Fatalf("unknown projection target = %+v, found=%t error=%v", target, found, err)
	}
	newerProjection := oldProjection
	newerProjection.Version = "v2.0.0"
	newerProjection.IssueBody = newerProjection.body()
	newerIntent, err := NewProjectionIntent(newerProjection, 2, ProjectionPriorityChanged, startedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = SaveProjectionIntent(t.Context(), projectionCoordinator(backend), newerIntent); err != nil {
		t.Fatal(err)
	}
	readback := ProjectionReadback{target: target, binding: ProjectionBinding{
		ModuleID: target.ModuleID, Generation: target.Generation,
		IssueNumber: 7, IssueNodeID: "ISSUE", IssueURL: "https://github.com/softwareone-public/avm-intelligence/issues/7", ProjectItemID: "ITEM",
	}}
	adopted, err := AdoptProjectionReadback(t.Context(), projectionCoordinator(backend), readback, startedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if adopted.DeliveryState != ProjectionPending || adopted.Fingerprint != newerIntent.Fingerprint || adopted.LastAcknowledgedFingerprint != oldIntent.Fingerprint || adopted.UnknownEffect != nil {
		t.Fatalf("old effect adoption overwrote or held the newer projection: %+v", adopted)
	}
}

func labelsJSON(labels []string) []map[string]string {
	result := make([]map[string]string, 0, len(labels))
	for _, label := range labels {
		result = append(result, map[string]string{"name": label})
	}
	return result
}

func TestEquivalentProjectionIntentPreservesUnknownOwnerEffectHold(t *testing.T) {
	now := time.Now().UTC()
	intent, err := NewProjectionIntent(projectionForModule("avm-res-owner-equivalent"), 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	existing := intent
	existing.DeliveryState = ProjectionNeedsReview
	existing.Fence = 4
	existing.LastAcknowledgedFingerprint = intent.Fingerprint
	existing.LastErrorClass = ProjectionErrorNeedsReview
	existing.RetryReason = projectionRetryOwnerEffectUnknown
	newer := intent
	newer.Generation = 2
	newer.UpdatedAt = now.Add(time.Minute)
	updated := newerEquivalentProjectionIntent(existing, newer)
	if updated.DeliveryState != ProjectionNeedsReview || updated.RetryReason != projectionRetryOwnerEffectUnknown || updated.LastErrorClass != ProjectionErrorNeedsReview || updated.Fence != existing.Fence+1 {
		t.Fatalf("equivalent intent cleared the unknown remote-effect hold: %+v", updated)
	}
}

type delayedIssueCreateTransport struct {
	reached      chan struct{}
	release      chan struct{}
	mu           sync.Mutex
	issueBody    string
	issueCreates atomic.Int32
	issueUpdates atomic.Int32
	labelWrites  atomic.Int32
}

func (transport *delayedIssueCreateTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Path == "/search/issues" {
		transport.mu.Lock()
		body := transport.issueBody
		transport.mu.Unlock()
		items, err := json.Marshal(map[string]any{"total_count": 1, "incomplete_results": false, "items": []map[string]any{{
			"number": 7, "node_id": "ISSUE", "html_url": "https://github.com/softwareone-public/avm-intelligence/issues/7", "body": body,
		}}})
		if err != nil {
			return nil, err
		}
		return jsonResponse(string(items))
	}
	if request.URL.Path == "/graphql" {
		var payload struct {
			Query     string `json:"query"`
			Variables struct {
				Body string `json:"body"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		switch {
		case strings.Contains(payload.Query, "updateIssue(input"):
			transport.mu.Lock()
			transport.issueBody = payload.Variables.Body
			transport.mu.Unlock()
			transport.issueUpdates.Add(1)
			return jsonResponse(`{"data":{"updateIssue":{"issue":{"number":7}}}}`)
		case strings.Contains(payload.Query, "projectV2(number"):
			return jsonResponse(`{"data":{"organization":{"projectV2":{"id":"PROJECT","title":"AVM Intelligence Factory"}}}}`)
		case strings.Contains(payload.Query, "addProjectV2ItemById"):
			return jsonResponse(`{"data":{"addProjectV2ItemById":{"item":{"id":"ITEM"}}}}`)
		case strings.Contains(payload.Query, "updateProjectV2ItemFieldValue"):
			return jsonResponse(`{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"ITEM"}}}}`)
		case strings.Contains(payload.Query, "fields(first:50)"):
			return jsonResponse(fleetProjectFieldsJSON)
		default:
			return nil, fmt.Errorf("unexpected delayed projection GraphQL query")
		}
	}
	switch {
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/issues"):
		transport.issueCreates.Add(1)
		var payload struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		transport.mu.Lock()
		transport.issueBody = payload.Body
		transport.mu.Unlock()
		close(transport.reached)
		<-transport.release
		return jsonResponse(`{"number":7,"node_id":"ISSUE","html_url":"https://github.com/softwareone-public/avm-intelligence/issues/7"}`)
	case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/issues/7/labels"):
		return jsonResponse(`[]`)
	case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/labels/"):
		return jsonResponse(`{"name":"existing"}`)
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/issues/7/labels"):
		transport.labelWrites.Add(1)
		return jsonResponse(`[]`)
	default:
		return nil, fmt.Errorf("unexpected delayed projection request %s %s", request.Method, request.URL.Path)
	}
}

func TestProjectionRunnerCheckpointsBudgetPauseWithoutGitHubCalls(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Now().UTC()
	intent, err := NewProjectionIntent(projectionForModule("avm-res-budget"), 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-budget", now, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := Client{
		Token: "token", Budget: &ProjectionBudget{SearchRemaining: 0, SecondaryRemaining: 10},
		HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(`{"total_count":0,"incomplete_results":false,"items":[]}`)
		})},
	}
	result, err := client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), claim, ProjectionRunOptions{
		Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Partial || result.Completed != 0 || calls != 0 {
		t.Fatalf("budget pause result=%+v calls=%d, want persisted partial checkpoint before network", result, calls)
	}
	if !errors.Is(result.Cause, ErrProjectionBudgetExhausted) || errors.Is(result.Cause, ErrGitHubRateLimited) || result.RetryAt.IsZero() {
		t.Fatalf("local budget cause or retry checkpoint lost: %+v", result)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	stored, exists, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+intent.ModuleID+".json")
	if err != nil || !exists || stored.DeliveryState != ProjectionRetryable || stored.LastErrorClass != ProjectionErrorBudget || stored.RetryAt.IsZero() {
		t.Fatalf("budget-paused intent=%+v exists=%t error=%v", stored, exists, err)
	}
}

func TestProjectionRunnerKeepsModuleFailuresIndependent(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Now().UTC()
	for _, moduleID := range []string{"avm-res-a-fails", "avm-res-b-succeeds", "avm-res-c-unstarted"} {
		projection := projectionForModule(moduleID)
		if moduleID == "avm-res-b-succeeds" {
			projection.Status = StatusPublishing
			projection.IssueBody = projection.body()
		}
		intent, err := NewProjectionIntent(projection, 1, ProjectionPriorityChanged, now)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
		if err != nil {
			t.Fatal(err)
		}
		backend.head = saved.Head
	}
	firstClaim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-independent-a", now, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	transport := &projectionBatchTransport{failFirst: true}
	client := Client{
		Token:      "token",
		Budget:     &ProjectionBudget{RESTRemaining: 100, GraphQLRemaining: 100, SearchRemaining: 100, SecondaryRemaining: 100},
		HTTPClient: &http.Client{Transport: transport},
	}
	result, err := client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), firstClaim, ProjectionRunOptions{
		Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Completed != 0 || len(result.Errors) != 1 || transport.created != 1 {
		t.Fatalf("first independent module result=%+v created=%d", result, transport.created)
	}
	secondClaim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-independent-b", now.Add(time.Second), time.Minute, 1)
	if err != nil || len(secondClaim.Intents) != 1 || secondClaim.Intents[0].ModuleID != "avm-res-b-succeeds" {
		t.Fatalf("second independent module claim=%+v err=%v", secondClaim, err)
	}
	transport.failFirst = false
	secondResult, err := client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), secondClaim, ProjectionRunOptions{
		Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute,
	})
	if err != nil || secondResult.Completed != 1 || transport.created != 2 {
		t.Fatalf("neighbor module did not proceed after failure: result=%+v created=%d err=%v", secondResult, transport.created, err)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+"avm-res-a-fails.json")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+"avm-res-b-succeeds.json")
	if err != nil {
		t.Fatal(err)
	}
	third, _, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+"avm-res-c-unstarted.json")
	if err != nil {
		t.Fatal(err)
	}
	if first.DeliveryState != ProjectionNeedsReview || second.DeliveryState != ProjectionProjected {
		t.Fatalf("independent delivery states first=%s second=%s", first.DeliveryState, second.DeliveryState)
	}
	if third.DeliveryState != ProjectionPending || third.Attempt != 0 {
		t.Fatalf("unstarted module delivery = %+v, want unchanged pending intent", third)
	}
	if _, exists := snapshot.Records[projectionBindingPrefix+"avm-res-b-succeeds.json"]; !exists {
		t.Fatal("successful module projection did not persist its remote binding")
	}
}

func TestProjectionRunnerReadsProjectMetadataOncePerBatch(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Now().UTC()
	for _, moduleID := range []string{"avm-res-cache-a", "avm-res-cache-b"} {
		projection := projectionForModule(moduleID)
		projection.Status = StatusPublishing
		projection.IssueBody = projection.body()
		intent, err := NewProjectionIntent(projection, 1, ProjectionPriorityChanged, now)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
		if err != nil {
			t.Fatal(err)
		}
		backend.head = saved.Head
	}
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-cache", now, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	transport := &projectionBatchTransport{}
	client := Client{
		Token: "token", Budget: &ProjectionBudget{RESTRemaining: 200, GraphQLRemaining: 200, SearchRemaining: 100, SecondaryRemaining: 500},
		HTTPClient: &http.Client{Transport: transport},
	}
	result, err := client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), claim, ProjectionRunOptions{
		Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	stored, exists, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+claim.Intents[0].ModuleID+".json")
	if err != nil || !exists {
		t.Fatalf("read projected module checkpoint: exists=%t err=%v", exists, err)
	}
	if result.Completed != 1 || transport.projectMetadataReads != 1 || transport.fieldMetadataReads != 1 || transport.issueSnapshotReads != 1 || !transport.projectItemFieldUnionFragment || stored.SnapshotCursors["issues_page"] != "complete" || stored.SnapshotCursors["project_items"] != "complete" {
		t.Fatalf("batch result=%+v metadata reads project=%d fields=%d", result, transport.projectMetadataReads, transport.fieldMetadataReads)
	}
}

type projectionMemoryBackend struct {
	head     string
	snapshot state.Snapshot
	commits  int
}

type projectionTestCoordinator struct {
	state.Backend
}

func (coordinator projectionTestCoordinator) Read(ctx context.Context) (string, state.Snapshot, error) {
	return coordinator.Backend.Read(ctx)
}

func projectionCoordinator(backend state.Backend) state.TransactionCoordinator {
	return projectionTestCoordinator{Backend: backend}
}

func (coordinator projectionTestCoordinator) RunTransaction(ctx context.Context, operationID string, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	for attempt := 0; attempt < 3; attempt++ {
		head, snapshot, err := state.ReadSnapshot(ctx, coordinator.Backend)
		if err != nil {
			return state.TransactionResult{}, err
		}
		if projectionOperationApplied(snapshot, operationID) {
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

func projectionOperationApplied(snapshot state.Snapshot, operationID string) bool {
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

type projectionBatchTransport struct {
	created                       int
	failFirst                     bool
	projectMetadataReads          int
	fieldMetadataReads            int
	issueSnapshotReads            int
	projectItemFieldUnionFragment bool
}

func (transport *projectionBatchTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Path == "/search/issues" {
		transport.issueSnapshotReads++
		return jsonResponse(`{"total_count":0,"incomplete_results":false,"items":[]}`)
	}
	if request.Method == http.MethodPost && request.URL.Path == "/repos/softwareone-public/avm-intelligence/issues" {
		transport.created++
		if transport.failFirst && transport.created == 1 {
			return statusResponse(http.StatusUnprocessableEntity, `{"message":"invalid issue fixture"}`), nil
		}
		return jsonResponse(`{"number":7,"node_id":"ISSUE","html_url":"https://github.com/softwareone-public/avm-intelligence/issues/7"}`)
	}
	if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/issues/7/labels") {
		return jsonResponse(`[]`)
	}
	if request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/labels/") {
		return jsonResponse(`{"name":"existing"}`)
	}
	if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/issues/7/labels") {
		return jsonResponse(`[]`)
	}
	if request.URL.Path == "/graphql" {
		var payload struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		switch {
		case strings.Contains(payload.Query, "projectV2(number"):
			transport.projectMetadataReads++
			return jsonResponse(`{"data":{"organization":{"projectV2":{"id":"PROJECT","title":"AVM Intelligence Factory"}}}}`)
		case strings.Contains(payload.Query, "addProjectV2ItemById"):
			return jsonResponse(`{"data":{"addProjectV2ItemById":{"item":{"id":"ITEM"}}}}`)
		case strings.Contains(payload.Query, "fields(first:50)"):
			transport.fieldMetadataReads++
			return jsonResponse(fleetProjectFieldsJSON)
		case strings.Contains(payload.Query, "items(first:100,after:$after)"):
			const fieldNameFragment = "field{... on ProjectV2FieldCommon{name}}"
			if strings.Count(payload.Query, fieldNameFragment) != 3 {
				return nil, fmt.Errorf("ProjectV2FieldConfiguration.name must be selected through ProjectV2FieldCommon")
			}
			transport.projectItemFieldUnionFragment = true
			return jsonResponse(`{"data":{"node":{"items":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}}`)
		default:
			return jsonResponse(`{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"ITEM"}},"updateIssue":{"issue":{"number":7}}}}`)
		}
	}
	return nil, fmt.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
}

func TestProjectionIntentBatchSaveAndModuleScopedClaim(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	moduleIDs := []string{"avm-res-target", "avm-res-unrelated"}
	intents := make([]ProjectionIntentRecord, 0, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		intent, err := NewProjectionIntent(projectionForModule(moduleID), 1, ProjectionPriorityChanged, now)
		if err != nil {
			t.Fatal(err)
		}
		intents = append(intents, intent)
	}
	saved, err := SaveProjectionIntents(context.Background(), projectionCoordinator(backend), intents)
	if err != nil || !saved.Changed || len(saved.Intents) != len(moduleIDs) {
		t.Fatalf("batch projection intent save = %#v, err=%v", saved, err)
	}
	claim, err := ClaimProjectionBatchForModules(context.Background(), projectionCoordinator(backend), ProjectionClaimOptions{
		RunID: "run-target-only", Now: now.Add(time.Second), Lease: time.Minute, Limit: 1,
	}, []string{"avm-res-target"})
	if err != nil {
		t.Fatal(err)
	}
	if len(claim.Batch.ModuleIDs) != 1 || claim.Batch.ModuleIDs[0] != "avm-res-target" {
		t.Fatalf("module-scoped claim = %v, want only avm-res-target", claim.Batch.ModuleIDs)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	unrelated, exists, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+"avm-res-unrelated.json")
	if err != nil || !exists || unrelated.DeliveryState != ProjectionPending {
		t.Fatalf("unrelated projection intent = %#v exists=%t err=%v, want pending", unrelated, exists, err)
	}
}

func TestSaveProjectionIntentRejectsNeutralIdentityAdvancedAfterRead(t *testing.T) {
	ctx := context.Background()
	backend := newProjectionMemoryBackend()
	moduleID := "avm-res-projection-save-race"
	oldDesired, err := neutralprojection.NewDesiredIntent(moduleID, 1, "dispatch:old", "queued", []neutralprojection.OwnedField{{Name: "avm_release_version", Value: "v1.0.0"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	newDesired, err := neutralprojection.NewDesiredIntent(moduleID, 2, "dispatch:new", "analyzing", []neutralprojection.OwnedField{{Name: "avm_release_version", Value: "v2.0.0"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldData, err := json.Marshal(oldDesired)
	if err != nil {
		t.Fatal(err)
	}
	newData, err := json.Marshal(newDesired)
	if err != nil {
		t.Fatal(err)
	}
	neutralPath, err := identity.StateProjectionIntentPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[neutralPath] = state.Record{Kind: "projection_intent", SchemaVersion: neutralprojection.SchemaVersion, Data: oldData}
	oldIdentity, err := oldDesired.Identity()
	if err != nil {
		t.Fatal(err)
	}
	intent, err := NewProjectionIntent(projectionForModule(moduleID), 1, ProjectionPriorityActive, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	intent.NeutralProjection = &oldIdentity
	coordinator := &projectionIntentAdvancingCoordinator{
		TransactionCoordinator: projectionCoordinator(backend), backend: backend,
		advance: state.Event{ID: "advance-neutral-projection", RecordPath: neutralPath, Record: state.Record{Kind: "projection_intent", SchemaVersion: neutralprojection.SchemaVersion, Data: newData}},
	}
	if _, err := SaveProjectionIntent(ctx, coordinator, intent); !errors.Is(err, ErrProjectionFence) {
		t.Fatalf("stale neutral intent save error=%v, want projection fence", err)
	}
	if _, exists := backend.snapshot.Records[projectionDeliveryPrefix+moduleID+".json"]; exists {
		t.Fatal("stale neutral delivery was committed after the producer advanced")
	}
}

func TestSaveProjectionIntentRejectsActiveSlotStatusChangedAfterRead(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	moduleID := "avm-res-projection-active-slot-race"
	request, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: moduleID, SourceSHA: strings.Repeat("a", 40), AVMReleaseVersion: "1.0.0",
		DesiredGeneration: 1, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	neutral, err := neutralprojection.NewDesiredIntent(moduleID, 1, "dispatch:held-release", "backlog", []neutralprojection.OwnedField{
		{Name: "avm_release_version", Value: "1.0.0"},
		{Name: "source_version", Value: "1.0.0"},
		{Name: "source_sha", Value: strings.Repeat("a", 40)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	neutralData, err := json.Marshal(neutral)
	if err != nil {
		t.Fatal(err)
	}
	neutralPath, err := identity.StateProjectionIntentPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	slotPath, err := queue.LifecycleSlotPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	backend := newProjectionMemoryBackend()
	put := func(path, kind string, value any) {
		t.Helper()
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		backend.snapshot.Records[path] = state.Record{Kind: kind, SchemaVersion: 1, Data: data}
	}
	put("state/requests/"+request.ID+".json", "request", request)
	put(slotPath, "lifecycle_slot", queue.LifecycleSlot{
		SchemaVersion: 1, ModuleID: moduleID, ReleaseVersion: "1.0.0", DesiredGeneration: 1,
		RequestID: request.ID, AcquiredAt: now, Status: "active",
	})
	backend.snapshot.Records[neutralPath] = state.Record{Kind: "projection_intent", SchemaVersion: neutralprojection.SchemaVersion, Data: neutralData}
	neutralIdentity, err := neutral.Identity()
	if err != nil {
		t.Fatal(err)
	}
	projection := projectionForModule(moduleID)
	projection.Version, projection.SourceSHA, projection.Status = "1.0.0", request.SourceSHA, queue.ModuleStatusBacklog
	projection.IssueBody = projection.body()
	delivery, err := NewProjectionIntent(projection, 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	delivery.NeutralProjection = &neutralIdentity

	activeRequest := request
	activeRequest.Status, activeRequest.OwnerRunID = queue.StatusRunning, "worker-active"
	activeWork := *request.Work
	activeRequest.Work = &activeWork
	activeRequest.Work.AttemptID, activeRequest.Work.Fence = "attempt-active", 1
	activeRequest.Work.Lease = &queue.ComputeLease{Owner: activeRequest.OwnerRunID, Fence: 1, ExpiresAt: now.Add(time.Hour)}
	if err = activeRequest.Validate(); err != nil {
		t.Fatalf("validate advanced running request fixture: %v", err)
	}
	requestPath := "state/requests/" + request.ID + ".json"
	activeData, err := json.Marshal(activeRequest)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &projectionIntentAdvancingCoordinator{
		TransactionCoordinator: projectionCoordinator(backend), backend: backend,
		advance: state.Event{ID: "advance-held-slot-status", RecordPath: requestPath, Record: state.Record{Kind: "request", SchemaVersion: 1, Data: activeData}},
	}
	if _, err = SaveProjectionIntent(ctx, coordinator, delivery); !errors.Is(err, ErrProjectionFence) {
		t.Fatalf("save after active-slot status advanced = %v, want projection fence", err)
	}
	if _, exists := backend.snapshot.Records[projectionDeliveryPrefix+moduleID+".json"]; exists {
		t.Fatal("delivery built from stale Backlog reduction was committed")
	}
}

func TestSaveProjectionIntentRejectsOlderQueuedReleaseAddedAfterRead(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 11, 13, 0, 0, 0, time.UTC)
	moduleID := "avm-res-projection-queued-release-race"
	newer, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: moduleID, SourceSHA: strings.Repeat("b", 40), AVMReleaseVersion: "1.10.0",
		DesiredGeneration: 2, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	older, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: moduleID, SourceSHA: strings.Repeat("a", 40), AVMReleaseVersion: "1.2.0",
		DesiredGeneration: 1, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	neutral, err := neutralprojection.NewDesiredIntent(moduleID, 3, "discovery:newer-release", "backlog", []neutralprojection.OwnedField{
		{Name: "avm_release_version", Value: "1.10.0"},
		{Name: "source_version", Value: "1.10.0"},
		{Name: "source_sha", Value: newer.SourceSHA},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	neutralData, err := json.Marshal(neutral)
	if err != nil {
		t.Fatal(err)
	}
	neutralPath, err := identity.StateProjectionIntentPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	newerData, err := json.Marshal(newer)
	if err != nil {
		t.Fatal(err)
	}
	backend := newProjectionMemoryBackend()
	backend.snapshot.Records["state/requests/"+newer.ID+".json"] = state.Record{Kind: "request", SchemaVersion: 1, Data: newerData}
	backend.snapshot.Records[neutralPath] = state.Record{Kind: "projection_intent", SchemaVersion: neutralprojection.SchemaVersion, Data: neutralData}
	projection := projectionForModule(moduleID)
	projection.Version, projection.SourceSHA, projection.Status = "1.10.0", newer.SourceSHA, StatusBacklog
	projection.IssueBody = projection.body()
	delivery, err := NewProjectionIntent(projection, 3, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	neutralIdentity, err := neutral.Identity()
	if err != nil {
		t.Fatal(err)
	}
	delivery.NeutralProjection = &neutralIdentity
	olderData, err := json.Marshal(older)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &projectionIntentAdvancingCoordinator{
		TransactionCoordinator: projectionCoordinator(backend), backend: backend,
		advance: state.Event{ID: "add-older-waiting-release", RecordPath: "state/requests/" + older.ID + ".json", Record: state.Record{Kind: "request", SchemaVersion: 1, Data: olderData}},
	}
	if _, err = SaveProjectionIntent(ctx, coordinator, delivery); !errors.Is(err, ErrProjectionFence) {
		t.Fatalf("save after oldest queued release changed = %v, want projection fence", err)
	}
	if _, exists := backend.snapshot.Records[projectionDeliveryPrefix+moduleID+".json"]; exists {
		t.Fatal("delivery for newer release was committed after an older release entered the backlog")
	}
}

type projectionIntentAdvancingCoordinator struct {
	state.TransactionCoordinator
	backend  *projectionMemoryBackend
	advance  state.Event
	advanced bool
}

func (coordinator *projectionIntentAdvancingCoordinator) Read(ctx context.Context) (string, state.Snapshot, error) {
	return coordinator.backend.Read(ctx)
}

func (coordinator *projectionIntentAdvancingCoordinator) RunTransaction(ctx context.Context, operationID string, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	if !coordinator.advanced {
		coordinator.advanced = true
		if _, _, err := state.ApplyEventsAtHead(ctx, coordinator.backend, coordinator.backend.head, []state.Event{coordinator.advance}); err != nil {
			return state.TransactionResult{}, err
		}
	}
	return coordinator.TransactionCoordinator.RunTransaction(ctx, operationID, decide, resolve)
}

func newProjectionMemoryBackend() *projectionMemoryBackend {
	return &projectionMemoryBackend{head: "head-0", snapshot: state.NewSnapshot()}
}

func (backend *projectionMemoryBackend) Read(ctx context.Context) (string, state.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return "", state.Snapshot{}, err
	}
	return backend.head, cloneProjectionSnapshot(backend.snapshot), nil
}

func (backend *projectionMemoryBackend) Commit(ctx context.Context, expectedHead string, snapshot state.Snapshot) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if backend.head != expectedHead {
		return "", state.ErrHeadMoved
	}
	backend.commits++
	backend.head = fmt.Sprintf("head-%d", backend.commits)
	backend.snapshot = cloneProjectionSnapshot(snapshot)
	return backend.head, nil
}

func cloneProjectionSnapshot(snapshot state.Snapshot) state.Snapshot {
	clone := snapshot
	clone.AppliedEvents = make(map[string]string, len(snapshot.AppliedEvents))
	for key, value := range snapshot.AppliedEvents {
		clone.AppliedEvents[key] = value
	}
	clone.FencingTokens = make(map[string]uint64, len(snapshot.FencingTokens))
	for key, value := range snapshot.FencingTokens {
		clone.FencingTokens[key] = value
	}
	clone.Records = make(map[string]state.Record, len(snapshot.Records))
	for path, record := range snapshot.Records {
		record.Data = append([]byte(nil), record.Data...)
		clone.Records[path] = record
	}
	return clone
}

func projectionForModule(moduleID string) Projection {
	projection := Projection{
		ModuleID: moduleID, ModuleType: "res", Domain: "compute", Version: "v1.0.0",
		SourceSHA: strings.Repeat("a", 40), FactoryVersion: "2.0.0", Status: StatusBacklog,
		Outcome: OutcomeUnassessed, WorkType: WorkTypeModuleAnalysis,
	}
	projection.IssueTitle = projection.WorkType + ": " + moduleID
	projection.IssueBody = projection.body()
	return projection
}

func projectionTestBinding(intent ProjectionIntentRecord, at time.Time) *ProjectionBinding {
	return &ProjectionBinding{
		SchemaVersion: projectionStateSchemaVersion, ModuleID: intent.ModuleID,
		IssueNumber: 7, IssueNodeID: "ISSUE-" + intent.ModuleID,
		IssueURL:      "https://github.com/softwareone-public/avm-intelligence/issues/7",
		ProjectItemID: "ITEM-" + intent.ModuleID, Generation: intent.Generation, UpdatedAt: at,
	}
}
