package queue

import (
	"strings"
	"testing"
	"time"
)

const (
	testSourceSHAHexLength = 40
	testDigestHexLength    = 64
)

func repeat(value byte) string {
	return strings.Repeat(string(value), testSourceSHAHexLength)
}

func repeatDigest(value byte) string {
	return strings.Repeat(string(value), testDigestHexLength)
}

func TestRequestRequiresNativeWork(t *testing.T) {
	request, err := NewAcquireSourceRequest(AcquisitionInput{
		ModuleID: "avm-res-demo", SourceSHA: repeat('a'), DesiredGeneration: 1,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err = request.Validate(); err != nil {
		t.Fatalf("native acquisition contract rejected: %v", err)
	}
	request.Work = nil
	if err = request.Validate(); err == nil || !strings.Contains(err.Error(), "native work contract") {
		t.Fatalf("unversioned local-file request was accepted: %v", err)
	}
}

func TestAcquireSourceDoesNotRequireFactsOrTrackApplicability(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	request, err := NewAcquireSourceRequest(AcquisitionInput{
		ModuleID: "avm-res-demo", SourceSHA: repeat('a'), DesiredGeneration: 1,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{"hcl", "terraform-json"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if request.Work.Phase != PhaseAcquireSource || request.Work.FactsID != "" || request.Work.ContextID != "" || request.Work.Track != "" {
		t.Fatalf("acquisition request requires future assessment facts/context: %#v", request.Work)
	}
	if request.Work.Applicability != ApplicabilityUnresolved || request.Work.Candidate != CandidateUnresolved {
		t.Fatalf("unknown applicability was not retained as unresolved: %#v", request.Work)
	}
}

func TestDifferentSourceAcquisitionsPreserveOlderTrackWork(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	older, err := NewAcquireSourceRequest(AcquisitionInput{
		ModuleID: "avm-res-demo", SourceSHA: repeat('a'), DesiredGeneration: 1,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := NewAcquireSourceRequest(AcquisitionInput{
		ModuleID: "avm-res-demo", SourceSHA: repeat('b'), DesiredGeneration: 2,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	coalesced, changed, err := CoalesceRequests([]Request{older}, newer)
	if err != nil || !changed || len(coalesced) != 2 || coalesced[0].Status != StatusQueued {
		t.Fatalf("source-scoped acquisition coalescing = %#v changed=%t err=%v", coalesced, changed, err)
	}
	if _, err = ClaimModuleRequest(coalesced, older.ID, "run-v4", now, now.Add(time.Minute), 1); err != nil {
		t.Fatalf("older source acquisition became unclaimable: %v", err)
	}
}

func TestForcedAcquisitionRefreshChangesIdentityWithoutChangingSource(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	regular, err := NewAcquireSourceRequest(AcquisitionInput{
		ModuleID: "avm-res-demo", SourceSHA: repeat('a'), DesiredGeneration: 1,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	forced, err := NewAcquireSourceRequest(AcquisitionInput{
		ModuleID: "avm-res-demo", SourceSHA: repeat('a'), DesiredGeneration: 2,
		RefreshGeneration: 2, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if regular.SourceSHA != forced.SourceSHA || regular.Fingerprint == forced.Fingerprint || regular.ID == forced.ID {
		t.Fatalf("forced refresh identity regular=%+v forced=%+v, want same source and distinct request identity", regular, forced)
	}
}

func TestAssessmentPinsAcceptedFactsAndInputs(t *testing.T) {
	request, err := NewAssessmentRequest(AssessmentInput{
		ModuleID: "avm-res-demo", SourceSHA: repeat('a'), ContextID: "azurerm-4", Track: "azurerm-4",
		DesiredGeneration: 2, FactsID: "facts-" + strings.Repeat("a", 64),
		DefinitionIDs:  []string{"definition-" + strings.Repeat("b", 64)},
		SnapshotIDs:    []string{"snapshot-" + strings.Repeat("c", 64)},
		TargetVersions: map[string]string{"azurerm": "4.10.0"},
	}, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if request.Work.Phase != PhaseAssess || request.Work.FactsID == "" || request.Work.ContextID != "azurerm-4" || request.Work.Track != "azurerm-4" {
		t.Fatalf("assessment request did not pin its context and facts: %#v", request.Work)
	}
	if request.Work.ExtractorContract != "" || len(request.Work.Capabilities) != 0 {
		t.Fatal("assessment request unexpectedly requires source extraction")
	}
}

func TestAcceptedFactsWakeIndependentAssessmentContexts(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	acquisition, err := NewAcquireSourceRequest(AcquisitionInput{
		ModuleID: "avm-res-demo", SourceSHA: repeat('a'), DesiredGeneration: 1,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{"hcl"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := ClaimModuleRequest([]Request{acquisition}, acquisition.ID, "run-acquire", now, now.Add(time.Minute), 1)
	if err != nil {
		t.Fatal(err)
	}
	completion := SealedCompletion{
		ID: "completion-" + strings.Repeat("e", 64), RequestID: requests[0].ID,
		InputFingerprint: requests[0].Fingerprint, DesiredGeneration: 1, Fence: 1,
		ArtifactDigest: strings.Repeat("d", 64), ValidationID: "validation-1", SealedAt: now.Add(time.Second),
	}
	sealed, err := SealCompletion(requests[0], "run-acquire", 1, completion)
	if err != nil {
		t.Fatal(err)
	}
	factsID := "facts-" + strings.Repeat("a", 64)
	acceptedFacts, err := AcceptFacts(sealed, completion.ID, factsID, now.Add(2*time.Second))
	if err != nil || acceptedFacts.Status != StatusFactsAccepted || acceptedFacts.Work.AcceptedFactsID != factsID || acceptedFacts.Status == StatusAccepted {
		t.Fatalf("accepted acquisition facts = %#v, err = %v", acceptedFacts, err)
	}

	assessmentInputs := []AssessmentInput{
		{ModuleID: acquisition.ModuleID, SourceSHA: acquisition.SourceSHA, ContextID: "azurerm-4", Track: "azurerm-4", DesiredGeneration: 1, DefinitionIDs: []string{"definition-" + strings.Repeat("b", 64)}},
		{ModuleID: acquisition.ModuleID, SourceSHA: acquisition.SourceSHA, ContextID: "azurerm-5", Track: "azurerm-5", DesiredGeneration: 1, DefinitionIDs: []string{"definition-" + strings.Repeat("b", 64)}},
	}
	assessments, err := WakeAssessmentRequests(factsID, assessmentInputs, now.Add(3*time.Second))
	if err != nil || len(assessments) != 2 {
		t.Fatalf("assessment wake-up = %#v, err = %v", assessments, err)
	}
	if assessments[0].Work.FactsID != factsID || assessments[1].Work.FactsID != factsID || assessments[0].ID == assessments[1].ID {
		t.Fatalf("accepted facts were not shared across distinct contexts: %#v", assessments)
	}
}

func TestPlanAssessmentWakeRequiresExactAcceptedSnapshots(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	factsID := "facts-" + strings.Repeat("a", 64)
	snapshotID := "snapshot-" + strings.Repeat("c", 64)
	providerAddress := "registry.terraform.io/hashicorp/azurerm"
	context := AssessmentWakeContext{
		State: TrackState{ContextID: "azurerm-4", Track: "azurerm-4", Applicability: ApplicabilityApplicable, Candidate: CandidateEligible},
		Input: AssessmentInput{
			ModuleID: "avm-res-demo", SourceSHA: repeat('b'), ContextID: "azurerm-4", Track: "azurerm-4",
			DesiredGeneration: 1, DefinitionIDs: []string{"definition-" + strings.Repeat("d", 64)},
			SnapshotIDs: []string{snapshotID}, TargetVersions: map[string]string{providerAddress: "4.10.0"},
		},
		PrimaryProviderAddress: providerAddress, PrimaryProviderMajor: 4,
	}

	plan, err := PlanAssessmentWake(factsID, "avm-res-demo", repeat('b'), []AssessmentWakeContext{context}, now)
	if err != nil || len(plan.Requests) != 0 || len(plan.Deferred) != 1 || plan.Deferred[0].ContextID != "azurerm-4" {
		t.Fatalf("missing accepted snapshot plan = %#v, err=%v; want deferred with no request", plan, err)
	}

	context.AcceptedSnapshots = []AcceptedProviderSnapshotPin{{ID: snapshotID, ProviderAddress: providerAddress, ProviderVersion: "4.10.0"}}
	plan, err = PlanAssessmentWake(factsID, "avm-res-demo", repeat('b'), []AssessmentWakeContext{context}, now)
	if err != nil || len(plan.Requests) != 1 || len(plan.Deferred) != 0 {
		t.Fatalf("accepted snapshot plan = %#v, err=%v; want one request", plan, err)
	}
	request := plan.Requests[0]
	if request.Work.FactsID != factsID || len(request.Work.SnapshotIDs) != 1 || request.Work.SnapshotIDs[0] != snapshotID || request.Work.TargetVersions[providerAddress] != "4.10.0" {
		t.Fatalf("prepared request did not pin exact accepted inputs: %+v", request.Work)
	}

	context.AcceptedSnapshots[0].ProviderVersion = "5.0.0"
	plan, err = PlanAssessmentWake(factsID, "avm-res-demo", repeat('b'), []AssessmentWakeContext{context}, now)
	if err != nil || len(plan.Requests) != 0 || len(plan.Deferred) != 1 {
		t.Fatalf("wrong-major snapshot plan = %#v, err=%v; want deferred with no request", plan, err)
	}
}

func TestPlanAssessmentWakeKeepsUnresolvedTracksParkedWithoutFallback(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	contexts := []AssessmentWakeContext{
		{State: TrackState{ContextID: "azurerm-4", Track: "azurerm-4", Applicability: ApplicabilityUnresolved, Candidate: CandidateUnresolved}},
		{State: TrackState{ContextID: "azurerm-5", Track: "azurerm-5", Applicability: ApplicabilityNotApplicable, Candidate: CandidateIneligible}},
	}
	plan, err := PlanAssessmentWake("facts-"+strings.Repeat("a", 64), "avm-res-demo", repeat('b'), contexts, now)
	if err != nil || len(plan.Requests) != 0 || len(plan.Deferred) != 2 {
		t.Fatalf("unresolved track plan = %#v, err=%v; want both contexts parked", plan, err)
	}
	for _, deferred := range plan.Deferred {
		if deferred.ContextID == LatestSourceContext {
			t.Fatal("planner invented a latest-source fallback context")
		}
	}
}

func TestModuleClaimIsExclusiveAndCompletionMustBeSealedBeforeAcceptance(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first := assessmentRequest(t, "azurerm-4", 1)
	second := assessmentRequest(t, "azurerm-5", 1)
	requests, err := ClaimModuleRequest([]Request{first, second}, first.ID, "run-1", now, now.Add(time.Minute), 1)
	if err != nil {
		t.Fatal(err)
	}
	if requests[0].Status != StatusRunning || requests[0].Work.Lease == nil || requests[0].Work.Lease.Fence != 1 {
		t.Fatalf("claim did not bind owner and fence: %#v", requests[0])
	}
	if _, err = ClaimModuleRequest(requests, second.ID, "run-2", now, now.Add(time.Minute), 2); err == nil {
		t.Fatal("a second context acquired a concurrent module compute claim")
	}

	completion := SealedCompletion{
		ID: "completion-" + strings.Repeat("e", 64), RequestID: requests[0].ID, InputFingerprint: requests[0].Fingerprint,
		DesiredGeneration: requests[0].Work.DesiredGeneration, Fence: requests[0].Work.Lease.Fence,
		ArtifactDigest: strings.Repeat("d", 64), ValidationID: "validation-1", SealedAt: now,
	}
	sealed, err := SealCompletion(requests[0], "run-1", 1, completion)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Status != StatusAwaitingAcceptance || sealed.Work.Lease != nil || sealed.Work.Completion == nil {
		t.Fatalf("completion did not seal before releasing compute: %#v", sealed)
	}
	accepted, err := AcceptCompletion(sealed, completion.ID, now.Add(time.Second))
	if err != nil || accepted.Status != StatusAccepted || accepted.Work.Completion.ID != completion.ID {
		t.Fatalf("accepted completion = %#v, err = %v", accepted, err)
	}
}

func TestCoalesceDesiredRequestsPerContextAndPreserveOtherContexts(t *testing.T) {
	older := assessmentRequest(t, "azurerm-4", 1)
	otherContext := assessmentRequest(t, "azurerm-5", 1)
	newer := assessmentRequestWithVersion(t, "azurerm-4", 2, "4.11.0")
	requests, changed, err := CoalesceRequests([]Request{older, otherContext}, newer)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || len(requests) != 3 || requests[0].Status != StatusSuperseded || requests[1].Status != StatusQueued || requests[2].ID != newer.ID {
		t.Fatalf("coalesced requests = %#v changed=%t", requests, changed)
	}
	claimTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if _, err = ClaimModuleRequest([]Request{older, newer}, older.ID, "run-old", claimTime, claimTime.Add(time.Minute), 1); err == nil {
		t.Fatal("older request was claimable while a newer context generation existed")
	}
	if _, _, err = CoalesceRequests(requests, older); err == nil {
		t.Fatal("older desired generation replaced newer work")
	}

	sameInputsNextGeneration := assessmentRequest(t, "azurerm-4", 2)
	coalesced, changed, err := CoalesceRequests([]Request{older}, sameInputsNextGeneration)
	if err != nil || !changed || len(coalesced) != 1 || coalesced[0].Work.DesiredGeneration != 2 {
		t.Fatalf("same-input generation update = %#v changed=%t err=%v", coalesced, changed, err)
	}
}

func TestLatestSourceFallbackRequiresProvenTrackNonApplicability(t *testing.T) {
	tracks := []TrackState{
		{ContextID: "azurerm-4", Track: "azurerm-4", Applicability: ApplicabilityNotApplicable, Candidate: CandidateUnresolved},
		{ContextID: "azurerm-5", Track: "azurerm-5", Applicability: ApplicabilityNotApplicable, Candidate: CandidateUnresolved},
	}
	contexts, err := RequiredContexts(tracks)
	if err != nil || len(contexts) != 1 || contexts[0].ContextID != LatestSourceContext {
		t.Fatalf("all-not-applicable fallback = %#v, err = %v", contexts, err)
	}

	tracks[1].Applicability = ApplicabilityUnresolved
	contexts, err = RequiredContexts(tracks)
	if err != nil || len(contexts) != 1 || contexts[0].Applicability != ApplicabilityUnresolved {
		t.Fatalf("unresolved contexts = %#v, err = %v", contexts, err)
	}
	for _, context := range contexts {
		if context.ContextID == LatestSourceContext {
			t.Fatal("unknown applicability incorrectly enabled latest-source fallback")
		}
	}
	if _, err = RequiredContexts(nil); err == nil {
		t.Fatal("empty track set was treated as vacuous applicability")
	}
}

func assessmentRequest(t *testing.T, contextID string, generation uint64) Request {
	return assessmentRequestWithVersion(t, contextID, generation, "4.10.0")
}

func assessmentRequestWithVersion(t *testing.T, contextID string, generation uint64, targetVersion string) Request {
	t.Helper()
	request, err := NewAssessmentRequest(AssessmentInput{
		ModuleID: "avm-res-demo", SourceSHA: repeat('a'), ContextID: contextID, Track: contextID,
		DesiredGeneration: generation, FactsID: "facts-" + strings.Repeat("a", 64),
		DefinitionIDs:  []string{"definition-" + strings.Repeat("b", 64)},
		TargetVersions: map[string]string{"azurerm": targetVersion},
	}, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return request
}
