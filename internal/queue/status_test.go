package queue

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestLifecycleIntentDoesNotChangeDiscoveryGeneration(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-lifecycle-independent", 'a', 1, now)
	backend := seedDispatchBackend(t, request)
	discoveryPath := "state/projection/intents/" + request.ModuleID + ".json"
	intent := map[string]any{
		"schema_version":     1,
		"module_id":          request.ModuleID,
		"desired_generation": 9,
		"status":             "backlog",
		"source":             map[string]string{"commit": request.SourceSHA},
	}
	event, err := stateRecordEvent(discoveryPath, "projection_intent", intent, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.ApplyEventsAtHead(t.Context(), backend, backend.head, []state.Event{event}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.MigrateModuleLayout(t.Context(), backend); err != nil {
		t.Fatal(err)
	}
	_, before, err := state.ReadSnapshot(t.Context(), backend)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimRequestWithLifecycle(t.Context(), newQueueTestCoordinator(backend), request.ID, "dispatcher", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	prepareIntent := func(current Request) {
		t.Helper()
		head, snapshot, err := state.ReadSnapshot(t.Context(), backend)
		if err != nil {
			t.Fatal(err)
		}
		events, err := AcquisitionLifecycleEvents(snapshot, current, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) > 0 {
			if _, _, err := state.ApplyEventsAtHead(t.Context(), backend, head, events); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, emittedSnapshot, err := state.ReadSnapshot(t.Context(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := emittedSnapshot.Records[lifecycleIntentPrefix+request.ModuleID+".json"]; !exists {
		t.Fatal("opt-in claim did not atomically emit lifecycle intent")
	}
	_, claimedSnapshot, err := state.ReadSnapshot(t.Context(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, current, err := ReadCurrentLifecycleIntent(claimedSnapshot, request.ModuleID); err != nil || !current {
		t.Fatalf("claimed lifecycle was not current: %t %v", current, err)
	}
	assertIntent := func(expected string, generation uint64) {
		t.Helper()
		_, snapshot, err := state.ReadSnapshot(t.Context(), backend)
		if err != nil {
			t.Fatal(err)
		}
		if string(snapshot.Records[discoveryPath].Data) != string(before.Records[discoveryPath].Data) {
			t.Fatal("work progress changed discovery generation or intent")
		}
		var intent LifecycleIntent
		if err := json.Unmarshal(snapshot.Records[lifecycleIntentPrefix+request.ModuleID+".json"].Data, &intent); err != nil {
			t.Fatal(err)
		}
		if intent.Status != expected || intent.Generation != generation || intent.DesiredGeneration != 1 || intent.RequestID != request.ID {
			t.Fatalf("lifecycle intent = %+v", intent)
		}
	}
	assertIntent(ModuleStatusQueued, 1)
	bound, err := BindActionsRunWithLifecycle(t.Context(), backend, claim.Attempt.ID, "worker", 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, runningSnapshot, err := state.ReadSnapshot(t.Context(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, current, err := ReadCurrentLifecycleIntent(runningSnapshot, request.ModuleID); err != nil || !current {
		t.Fatalf("opt-in binding did not commit current Running work: %t %v", current, err)
	}
	prepareIntent(bound.Request)
	assertIntent(ModuleStatusAnalyzing, 2)
	if _, err := BindActionsRun(t.Context(), backend, claim.Attempt.ID, "worker", 1, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	prepareIntent(bound.Request)
	assertIntent(ModuleStatusAnalyzing, 2)
}

func TestDefaultDispatchDoesNotProduceLifecycleRecords(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-lifecycle-default", 'a', 1, now)
	backend := seedDispatchBackend(t, request)
	event, err := stateRecordEvent("state/projection/intents/"+request.ModuleID+".json", "projection_intent", map[string]any{
		"schema_version": 1, "module_id": request.ModuleID, "desired_generation": 1, "status": "backlog",
		"source": map[string]string{"commit": request.SourceSHA},
	}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.ApplyEventsAtHead(t.Context(), backend, backend.head, []state.Event{event}); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimRequest(t.Context(), newQueueTestCoordinator(backend), request.ID, "dispatcher", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BindActionsRun(t.Context(), backend, claim.Attempt.ID, "worker", 1, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := state.ReadSnapshot(t.Context(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := snapshot.Records[lifecycleIntentPrefix+request.ModuleID+".json"]; exists {
		t.Fatal("default dispatch produced a new lifecycle record kind")
	}
}

func TestAcquisitionLifecycleLeavesMixedAndNewerWorkUntouched(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-lifecycle-context", 'a', 1, now)
	for _, other := range []Request{
		dispatchAssessment(t, request.ModuleID, "azurerm/4", 1, now),
		dispatchAcquisition(t, request.ModuleID, 'b', 2, now),
	} {
		backend := seedDispatchBackend(t, request, other)
		_, snapshot, err := state.ReadSnapshot(t.Context(), backend)
		if err != nil {
			t.Fatal(err)
		}
		discoveryData, err := json.Marshal(map[string]any{"schema_version": 1, "module_id": request.ModuleID, "status": "backlog", "source": map[string]string{"commit": request.SourceSHA}})
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Records["state/projection/intents/"+request.ModuleID+".json"] = state.Record{Kind: "projection_intent", SchemaVersion: 1, Data: discoveryData}
		events, err := AcquisitionLifecycleEvents(snapshot, request, nil, now)
		if err != nil || len(events) != 0 {
			t.Fatalf("acquisition controlled mixed/newer work: %v %v", events, err)
		}
	}
}

func TestAcquisitionLifecyclePreservesProtectedDiscovery(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-lifecycle-protected", 'a', 1, now)
	for _, status := range []string{"blocked", "needs_review", "retired", "unknown"} {
		backend := seedDispatchBackend(t, request)
		_, snapshot, err := state.ReadSnapshot(t.Context(), backend)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(map[string]any{"schema_version": 1, "module_id": request.ModuleID, "status": status, "source": map[string]string{"commit": request.SourceSHA}})
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Records["state/projection/intents/"+request.ModuleID+".json"] = state.Record{Kind: "projection_intent", SchemaVersion: 1, Data: data}
		events, err := AcquisitionLifecycleEvents(snapshot, request, nil, now)
		if err != nil || len(events) != 0 {
			t.Fatalf("protected %s created lifecycle work: %v %v", status, events, err)
		}
	}
}

func TestLifecycleIntentValidationRejectsIncompleteAndInventedIdentity(t *testing.T) {
	request := dispatchAcquisition(t, "avm-res-lifecycle-validation", 'a', 1, time.Now().UTC())
	valid := LifecycleIntent{SchemaVersion: 1, ModuleID: request.ModuleID, Generation: 1, RequestID: request.ID, DesiredGeneration: 1, InputFingerprint: request.Fingerprint, SourceSHA: request.SourceSHA, Status: ModuleStatusQueued, UpdatedAt: request.CreatedAt}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*LifecycleIntent){
		func(intent *LifecycleIntent) { intent.RequestID = "invented" },
		func(intent *LifecycleIntent) { intent.DesiredGeneration = 0 },
		func(intent *LifecycleIntent) { intent.Generation = 0 },
		func(intent *LifecycleIntent) { intent.Status = ModuleStatusDone },
		func(intent *LifecycleIntent) { intent.UpdatedAt = time.Time{} },
		func(intent *LifecycleIntent) { intent.SourceSHA = "invalid" },
	} {
		invalid := valid
		mutate(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid lifecycle intent passed: %+v", invalid)
		}
	}
}

func TestRequestContextPreservesAcquisitionProgress(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	backend := seedDispatchBackend(t, dispatchAcquisition(t, "avm-res-status-progress", 'a', 1, now))
	claim, err := ClaimNext(t.Context(), newQueueTestCoordinator(backend), "dispatcher", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindActionsRun(t.Context(), backend, claim.Attempt.ID, "worker", 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		request Request
		status  string
	}{{claim.Request, ModuleStatusQueued}, {bound.Request, ModuleStatusAnalyzing}} {
		context, err := ContextFromRequest(test.request)
		if err != nil {
			t.Fatal(err)
		}
		status, err := ReduceModuleStatus(ModuleStatusInput{ModuleID: test.request.ModuleID, Lifecycle: ModuleLifecycleActive, Contexts: []ModuleContext{context}})
		if err != nil || status.Status != test.status || status.SourceSHA != test.request.SourceSHA {
			t.Fatalf("request lifecycle = %+v, %v; want %s", status, err, test.status)
		}
	}
}

func TestRequestContextKeepsAcceptanceSeparateFromDeployment(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	request := assessmentRequest(t, "azurerm/4", 1)
	claimed, err := ClaimModuleRequest([]Request{request}, request.ID, "worker", now, now.Add(time.Minute), 1)
	if err != nil {
		t.Fatal(err)
	}
	completion := SealedCompletion{
		ID: "completion-" + strings.Repeat("e", 64), RequestID: request.ID, InputFingerprint: request.Fingerprint,
		DesiredGeneration: 1, Fence: 1, ArtifactDigest: strings.Repeat("d", 64), ValidationID: "validator", SealedAt: now,
	}
	sealed, err := SealCompletion(claimed[0], "worker", 1, completion)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := AcceptCompletion(sealed, completion.ID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		request Request
		status  string
	}{{sealed, ModuleStatusNeedsReview}, {accepted, ModuleStatusPublishing}} {
		context, err := ContextFromRequest(test.request)
		if err != nil {
			t.Fatal(err)
		}
		status, err := ReduceModuleStatus(ModuleStatusInput{ModuleID: request.ModuleID, Lifecycle: ModuleLifecycleActive, Contexts: []ModuleContext{context}})
		if err != nil || status.Status != test.status {
			t.Fatalf("acceptance lifecycle = %+v, %v; want %s", status, err, test.status)
		}
	}
}

func TestContextsFromRequestsPreservesNewDesiredAndOlderActiveWork(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	older := assessmentRequest(t, "azurerm/4", 1)
	claimed, err := ClaimModuleRequest([]Request{older}, older.ID, "worker", now, now.Add(time.Minute), 1)
	if err != nil {
		t.Fatal(err)
	}
	newer := assessmentRequestWithVersion(t, "azurerm/4", 2, "4.11.0")
	other := assessmentRequest(t, "azurerm/5", 1)
	required := []TrackState{
		{ContextID: "azurerm/4", Track: "azurerm/4", Applicability: ApplicabilityApplicable, Candidate: CandidateEligible},
		{ContextID: "azurerm/5", Track: "azurerm/5", Applicability: ApplicabilityApplicable, Candidate: CandidateEligible},
	}
	contexts, err := ContextsFromRequests(older.ModuleID, required, []Request{other, newer, claimed[0]})
	if err != nil {
		t.Fatal(err)
	}
	if contexts[0].DesiredGeneration != 2 || contexts[0].ActiveGeneration != 1 || contexts[0].ActivePhase != ModuleStatusAnalyzing {
		t.Fatalf("new desired work erased older active ownership: %+v", contexts[0])
	}
	status, err := ReduceModuleStatus(ModuleStatusInput{ModuleID: older.ModuleID, Lifecycle: ModuleLifecycleActive, Contexts: contexts})
	if err != nil || status.Status != ModuleStatusBacklog || status.DesiredGeneration != 2 || status.ActiveGeneration != 1 {
		t.Fatalf("mixed lifecycle status = %+v, %v", status, err)
	}
	if _, err := ContextsFromRequests(older.ModuleID, required, []Request{newer}); err == nil {
		t.Fatal("missing required context was ignored")
	}
}

func TestContextsFromRequestsResolvesPolicyAndRejectsConflictingDuplicates(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	request := assessmentRequest(t, "azurerm/4", 1)
	required := []TrackState{
		{ContextID: "azurerm/4", Track: "azurerm/4", Applicability: ApplicabilityApplicable, Candidate: CandidateEligible},
		{ContextID: "azurerm/5", Track: "azurerm/5", Applicability: ApplicabilityNotApplicable, Candidate: CandidateIneligible},
	}
	contexts, err := ContextsFromRequests(request.ModuleID, required, []Request{request, request})
	if err != nil || len(contexts) != 1 || contexts[0].State.ContextID != "azurerm/4" {
		t.Fatalf("inactive context was required: %+v, %v", contexts, err)
	}
	required[0].Applicability = ApplicabilityNotApplicable
	required[0].Candidate = CandidateIneligible
	acquisition := dispatchAcquisition(t, request.ModuleID, 'a', 1, now)
	contexts, err = ContextsFromRequests(request.ModuleID, required, []Request{acquisition})
	if err != nil || len(contexts) != 1 || contexts[0].State.ContextID != LatestSourceContext {
		t.Fatalf("latest-source fallback did not use acquisition: %+v, %v", contexts, err)
	}
	required[0].Applicability = ApplicabilityApplicable
	required[0].Candidate = CandidateEligible
	claimed, err := ClaimModuleRequest([]Request{request}, request.ID, "worker-a", now, now.Add(time.Minute), 1)
	if err != nil {
		t.Fatal(err)
	}
	conflict := cloneRequest(claimed[0])
	conflict.OwnerRunID = "worker-b"
	conflict.Work.Lease.Owner = "worker-b"
	for _, pair := range [][]Request{{claimed[0], conflict}, {conflict, claimed[0]}, {request, claimed[0]}, {claimed[0], request}} {
		if _, err := ContextsFromRequests(request.ModuleID, required, pair); err == nil {
			t.Fatal("conflicting duplicate requests were ordered instead of rejected")
		}
	}
}

func TestRequestContextRejectsSupersededWorkAndHonorsReview(t *testing.T) {
	request := assessmentRequest(t, "azurerm/4", 1)
	request.Status = StatusSuperseded
	if _, err := ContextFromRequest(request); err == nil {
		t.Fatal("superseded work controlled module lifecycle")
	}
	request.Status = StatusQueued
	request.Work.RetryClass = RetryNeedsReview
	context, err := ContextFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	status, err := ReduceModuleStatus(ModuleStatusInput{ModuleID: request.ModuleID, Lifecycle: ModuleLifecycleActive, Contexts: []ModuleContext{context}})
	if err != nil || status.Status != ModuleStatusNeedsReview {
		t.Fatalf("protected request status = %+v, %v", status, err)
	}
}

func TestReduceModuleStatusUsesMixedTrackPrecedence(t *testing.T) {
	blocked := moduleContext("azurerm/4", ApplicabilityApplicable, CandidateIneligible)
	blocked.BlockedReason = "provider snapshot unavailable"
	publishing := moduleContext("azurerm/5", ApplicabilityApplicable, CandidateEligible)
	publishing.DesiredPhase = ModuleStatusPublishing

	status, err := ReduceModuleStatus(ModuleStatusInput{ModuleID: "avm-res-status-test", Lifecycle: ModuleLifecycleActive, Contexts: []ModuleContext{publishing, blocked}})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != ModuleStatusBlocked || status.ControllingContext != "azurerm/4" {
		t.Fatalf("mixed Blocked/Publishing reduction = %+v", status)
	}

	blocked.BlockedReason = ""
	blocked.State.Candidate = CandidateEligible
	blocked.DesiredPhase = ModuleStatusBacklog
	publishing.DesiredPhase = ModuleStatusAnalyzing
	status, err = ReduceModuleStatus(ModuleStatusInput{ModuleID: "avm-res-status-test", Lifecycle: ModuleLifecycleActive, Contexts: []ModuleContext{publishing, blocked}})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != ModuleStatusBacklog || status.ControllingContext != "azurerm/4" {
		t.Fatalf("mixed Backlog/Analyzing reduction = %+v", status)
	}
}

func TestReduceModuleStatusDoneRequiresExactCurrentDeploymentMembership(t *testing.T) {
	context := moduleContext("azurerm/4", ApplicabilityApplicable, CandidateEligible)
	context.DesiredGeneration = 9
	context.DesiredFingerprint = "fingerprint-a"
	context.SelectedGeneration = 9
	context.SelectedFingerprint = "fingerprint-a"
	context.AnalysisID = "analysis-a"
	context.SelectionReceiptID = "selection-a"
	context.DeployedAnalysisID = "analysis-a"
	context.DeployedSelectionID = "selection-a"
	context.DesiredPhase = ModuleStatusPublishing
	input := ModuleStatusInput{
		ModuleID: "avm-res-status-test", Lifecycle: ModuleLifecycleActive,
		DeploymentGeneration: 4, Contexts: []ModuleContext{context},
	}
	status, err := ReduceModuleStatus(input)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != ModuleStatusDone {
		t.Fatalf("fully matched deployment status = %+v, want Done", status)
	}

	input.Contexts[0].DeployedSelectionID = "selection-old"
	status, err = ReduceModuleStatus(input)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != ModuleStatusPublishing {
		t.Fatalf("mismatched selection receipt status = %+v, want Publishing", status)
	}
	input.Contexts[0].DesiredGeneration++
	input.Contexts[0].DesiredPhase = ModuleStatusBacklog
	input.Contexts[0].ActivePhase = ModuleStatusAnalyzing
	input.Contexts[0].ActiveGeneration = 9
	status, err = ReduceModuleStatus(input)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != ModuleStatusBacklog || status.ActiveGeneration != 9 {
		t.Fatalf("new desired generation lost active work: %+v", status)
	}
}

func TestReduceModuleStatusHandlesLifecycleReviewAndNoApplicableFallback(t *testing.T) {
	retired, err := ReduceModuleStatus(ModuleStatusInput{ModuleID: "avm-res-status-test", Lifecycle: ModuleLifecycleRetired})
	if err != nil {
		t.Fatal(err)
	}
	if retired.Status != ModuleStatusRetired {
		t.Fatalf("retired module reduction = %+v", retired)
	}

	needsReview := moduleContext("azurerm/4", ApplicabilityApplicable, CandidateEligible)
	needsReview.NeedsReview = true
	blocked := moduleContext("azurerm/5", ApplicabilityApplicable, CandidateIneligible)
	blocked.BlockedReason = "missing input"
	status, err := ReduceModuleStatus(ModuleStatusInput{ModuleID: "avm-res-status-test", Lifecycle: ModuleLifecycleActive, Contexts: []ModuleContext{blocked, needsReview}})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != ModuleStatusNeedsReview {
		t.Fatalf("Needs review must outrank Blocked: %+v", status)
	}

	allNA := moduleContext("azurerm/4", ApplicabilityNotApplicable, CandidateIneligible)
	status, err = ReduceModuleStatus(ModuleStatusInput{
		ModuleID: "avm-res-status-test", Lifecycle: ModuleLifecycleActive,
		Contexts: []ModuleContext{allNA}, LatestSourceRunnable: true, LatestSourceVersion: "v1.2.3", LatestSourceSHA: "source-sha",
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != ModuleStatusBacklog || status.ControllingContext != LatestSourceContext || status.SourceVersion != "v1.2.3" {
		t.Fatalf("proven no-track fallback = %+v", status)
	}

	empty, err := ReduceModuleStatus(ModuleStatusInput{ModuleID: "avm-res-status-test", Lifecycle: ModuleLifecycleActive})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Status != ModuleStatusBlocked {
		t.Fatalf("empty unresolved context status = %+v, want Blocked", empty)
	}
}

func moduleContext(contextID string, applicability Applicability, candidate CandidateState) ModuleContext {
	return ModuleContext{
		State:             TrackState{ContextID: contextID, Track: contextID, Applicability: applicability, Candidate: candidate},
		DesiredGeneration: 1, DesiredFingerprint: "fingerprint-a",
	}
}
