package queue

import (
	"strings"
	"testing"
	"time"
)

func TestRequestContextPreservesAcquisitionProgress(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	backend := seedDispatchBackend(t, dispatchAcquisition(t, "avm-res-status-progress", 'a', 1, now))
	claim, err := ClaimNext(t.Context(), backend, "dispatcher", now, time.Minute)
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
