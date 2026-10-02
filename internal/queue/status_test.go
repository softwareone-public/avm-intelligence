package queue

import "testing"

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
