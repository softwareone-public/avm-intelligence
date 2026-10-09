package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

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
		BatchID: claim.Batch.ID, ModuleID: request.ModuleID, RunID: "projection-run", Generation: claim.Intents[0].Generation, Fence: claim.Intents[0].Fence,
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
		intent.LeaseOwner, intent.BatchID, intent.Fence = "active-owner", "active-batch", 1
		intent.LeaseExpiresAt = now.Add(time.Hour)
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
	claimed, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-empty", time.Now().UTC(), time.Minute, 25)
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

func TestProjectionBatchOrdersRetriesBoundsClaimsAndCheckpoints(t *testing.T) {
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
	claimed, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-claim-1", now, time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed.Batch.ModuleIDs) != 2 || claimed.Batch.ModuleIDs[0] != "avm-res-beta" || claimed.Batch.ModuleIDs[1] != "avm-res-alpha" {
		t.Fatalf("claimed module order = %#v", claimed.Batch.ModuleIDs)
	}
	first := claimed.Intents[0]
	completed, err := RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		BatchID: claimed.Batch.ID, ModuleID: first.ModuleID, RunID: "run-claim-1", Generation: first.Generation, Fence: first.Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: now.Add(10 * time.Second), Binding: projectionTestBinding(first, now.Add(10*time.Second))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Batch.Cursor != 1 || completed.Batch.Status != ProjectionBatchRunning || completed.Intent.DeliveryState != ProjectionProjected || completed.Intent.LastAcknowledgedFingerprint != first.Fingerprint {
		t.Fatalf("partial projection checkpoint = %+v", completed)
	}
	second := claimed.Intents[1]
	retryAt := now.Add(20 * time.Minute)
	retry, err := RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		BatchID: claimed.Batch.ID, ModuleID: second.ModuleID, RunID: "run-claim-1", Generation: second.Generation, Fence: second.Fence,
		Outcome: ProjectionOutcome{ErrorClass: ProjectionErrorRateLimited, RetryAt: retryAt, CompletedAt: now.Add(10 * time.Second)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if retry.Batch.Cursor != 2 || retry.Batch.Status != ProjectionBatchComplete || retry.Intent.DeliveryState != ProjectionRetryable || !retry.Intent.RetryAt.Equal(retryAt) {
		t.Fatalf("retry checkpoint = %+v", retry)
	}
}

func TestProjectionCheckpointsRenewBatchLease(t *testing.T) {
	backend := newProjectionMemoryBackend()
	startedAt := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	moduleIDs := []string{"avm-res-lease-a", "avm-res-lease-b"}
	for _, moduleID := range moduleIDs {
		intent, err := NewProjectionIntent(projectionForModule(moduleID), 1, ProjectionPriorityChanged, startedAt)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
		if err != nil {
			t.Fatal(err)
		}
		backend.head = saved.Head
	}
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-lease", startedAt, time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	first := claim.Intents[0]
	if _, err = RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		BatchID: claim.Batch.ID, ModuleID: first.ModuleID, RunID: claim.Batch.RunID,
		Generation: first.Generation, Fence: first.Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: startedAt.Add(61 * time.Second), Binding: projectionTestBinding(first, startedAt.Add(61*time.Second))},
	}); err == nil {
		t.Fatal("expired projection lease acknowledged without a renewal")
	}
	checkpointAt := startedAt.Add(50 * time.Second)
	_, err = CheckpointProjectionSnapshot(context.Background(), projectionCoordinator(backend), ProjectionSnapshotCheckpoint{
		BatchID: claim.Batch.ID, RunID: claim.Batch.RunID, Cursors: map[string]string{"issues_page": "complete"}, CheckpointedAt: checkpointAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstCompletedAt := startedAt.Add(100 * time.Second)
	firstResult, err := RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		BatchID: claim.Batch.ID, ModuleID: first.ModuleID, RunID: claim.Batch.RunID,
		Generation: first.Generation, Fence: first.Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: firstCompletedAt, Binding: projectionTestBinding(first, firstCompletedAt)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !firstResult.Batch.LeaseExpiresAt.Equal(firstCompletedAt.Add(time.Minute)) {
		t.Fatalf("lease after first outcome = %s, want %s", firstResult.Batch.LeaseExpiresAt, firstCompletedAt.Add(time.Minute))
	}
	second := claim.Intents[1]
	secondCompletedAt := firstCompletedAt.Add(50 * time.Second)
	completed, err := RecordProjectionOutcome(context.Background(), projectionCoordinator(backend), ProjectionOutcomeInput{
		BatchID: claim.Batch.ID, ModuleID: second.ModuleID, RunID: claim.Batch.RunID,
		Generation: second.Generation, Fence: second.Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: secondCompletedAt, Binding: projectionTestBinding(second, secondCompletedAt)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Batch.Status != ProjectionBatchComplete || !completed.Batch.LeaseExpiresAt.IsZero() {
		t.Fatalf("completed batch retains lease: %+v", completed.Batch)
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
		BatchID: claimed.Batch.ID, ModuleID: old.ModuleID, RunID: "run-old", Generation: old.Generation, Fence: old.Fence,
		Outcome: ProjectionOutcome{Success: true, CompletedAt: now.Add(10 * time.Second), Binding: projectionTestBinding(old, now.Add(10*time.Second))},
	}); err == nil {
		t.Fatal("stale projection generation acknowledged after desired intent changed")
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
			return jsonResponse(`{"items":[]}`)
		})},
	}
	result, err := client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), claim, ProjectionRunOptions{
		Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Partial || result.Completed != 0 || result.Batch.Cursor != 1 || calls != 0 {
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
	for _, moduleID := range []string{"avm-res-a-fails", "avm-res-b-succeeds"} {
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
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-independent", now, time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	transport := &projectionBatchTransport{failFirst: true}
	client := Client{
		Token:      "token",
		Budget:     &ProjectionBudget{RESTRemaining: 100, GraphQLRemaining: 100, SearchRemaining: 100, SecondaryRemaining: 100},
		HTTPClient: &http.Client{Transport: transport},
	}
	result, err := client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), claim, ProjectionRunOptions{
		Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Completed != 1 || len(result.Errors) != 1 || result.Batch.Cursor != 2 || transport.created != 2 {
		t.Fatalf("independent batch result=%+v created=%d", result, transport.created)
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
	if first.DeliveryState != ProjectionNeedsReview || second.DeliveryState != ProjectionProjected {
		t.Fatalf("independent delivery states first=%s second=%s", first.DeliveryState, second.DeliveryState)
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
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "run-cache", now, time.Minute, 2)
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
	if result.Completed != 2 || transport.projectMetadataReads != 1 || transport.fieldMetadataReads != 1 || transport.issueSnapshotReads != 1 || !transport.projectItemFieldUnionFragment || result.Batch.SnapshotCursors["issues_page"] != "complete" || result.Batch.SnapshotCursors["project_items"] != "complete" {
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
		return jsonResponse(`{"items":[]}`)
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
		RunID: "run-target-only", Now: now.Add(time.Second), Lease: time.Minute, Limit: 25,
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
