package queue

import (
	"fmt"
	"sort"
)

const (
	ModuleLifecycleActive   = "active"
	ModuleLifecycleRetired  = "retired"
	ModuleStatusBacklog     = "Backlog"
	ModuleStatusQueued      = "Queued"
	ModuleStatusAnalyzing   = "Analyzing"
	ModuleStatusValidating  = "Validating"
	ModuleStatusPublishing  = "Publishing"
	ModuleStatusDone        = "Done"
	ModuleStatusFailed      = "Failed"
	ModuleStatusBlocked     = "Blocked"
	ModuleStatusNeedsReview = "Needs review"
	ModuleStatusRetired     = "Retired"
)

// ModuleContext carries desired and active work separately from accepted and
// deployed pointers. Only the desired generation can satisfy current Done.
type ModuleContext struct {
	State               TrackState
	DesiredGeneration   uint64
	ActiveGeneration    uint64
	DesiredPhase        string
	ActivePhase         string
	RunnableAcquisition bool
	BlockedReason       string
	RetryableFailure    bool
	NeedsReview         bool
	SourceVersion       string
	SourceSHA           string
	DesiredFingerprint  string
	SelectedGeneration  uint64
	SelectedFingerprint string
	AnalysisID          string
	SelectionReceiptID  string
	DeployedAnalysisID  string
	DeployedSelectionID string
}

// ModuleStatusInput is the canonicalized lifecycle input for one module.
type ModuleStatusInput struct {
	ModuleID             string
	Lifecycle            string
	Contexts             []ModuleContext
	DeploymentGeneration uint64
	PublishedCommit      string
	LatestSourceRunnable bool
	LatestSourceVersion  string
	LatestSourceSHA      string
}

// ModuleStatus is the shared projection truth for one module.
type ModuleStatus struct {
	ModuleID             string
	Status               string
	Reason               string
	ControllingContext   string
	SourceVersion        string
	SourceSHA            string
	LatestSourceVersion  string
	LatestSourceSHA      string
	DesiredGeneration    uint64
	ActiveGeneration     uint64
	DeploymentGeneration uint64
	PublishedCommit      string
	RequiredContexts     []string
}

type classifiedContext struct {
	context ModuleContext
	blocked string
	phase   string
}

// ReduceModuleStatus applies the operating-model lifecycle truth table without
// consulting Project state or conflating delivery with quality.
func ReduceModuleStatus(input ModuleStatusInput) (ModuleStatus, error) {
	if !moduleIDPattern.MatchString(input.ModuleID) {
		return ModuleStatus{}, fmt.Errorf("invalid module ID %q", input.ModuleID)
	}
	base := newModuleStatus(input)
	if input.Lifecycle == ModuleLifecycleRetired {
		base.Status = ModuleStatusRetired
		base.Reason = "catalog retirement is confirmed"
		return base, nil
	}
	if input.Lifecycle != ModuleLifecycleActive {
		return ModuleStatus{}, fmt.Errorf("invalid module lifecycle %q", input.Lifecycle)
	}
	contexts, err := requiredModuleContexts(input, &base)
	if err != nil {
		if len(input.Contexts) == 0 {
			base.Status = ModuleStatusBlocked
			base.Reason = "required contexts are unresolved"
			return base, nil
		}
		return ModuleStatus{}, err
	}
	return reduceClassifiedContexts(base, contexts, input.DeploymentGeneration), nil
}

// ContextFromRequest translates durable work into lifecycle input without
// treating accepted facts or analyses as a deployment receipt.
func ContextFromRequest(request Request) (ModuleContext, error) {
	if err := request.Validate(); err != nil {
		return ModuleContext{}, err
	}
	work := request.Work
	contextID, track := work.ContextID, work.Track
	if work.Phase == PhaseAcquireSource {
		contextID, track = LatestSourceContext, LatestSourceContext
	}
	context := ModuleContext{
		State:             TrackState{ContextID: contextID, Track: track, Applicability: work.Applicability, Candidate: work.Candidate},
		DesiredGeneration: work.DesiredGeneration, DesiredFingerprint: request.Fingerprint,
		SourceSHA: request.SourceSHA, RunnableAcquisition: work.Phase == PhaseAcquireSource,
	}
	if work.Phase == PhaseAcquireSource {
		context.State.Applicability = ApplicabilityApplicable
		context.State.Candidate = CandidateEligible
	}
	switch request.Status {
	case StatusQueued:
		context.DesiredPhase = ModuleStatusBacklog
	case StatusClaimed:
		context.ActiveGeneration, context.ActivePhase = work.DesiredGeneration, ModuleStatusQueued
	case StatusRunning:
		context.ActiveGeneration, context.ActivePhase = work.DesiredGeneration, ModuleStatusAnalyzing
	case StatusAwaitingAcceptance:
		context.NeedsReview = true
	case StatusFailed:
		context.RetryableFailure = true
	case StatusFactsAccepted:
		context.RunnableAcquisition = false
		context.BlockedReason = "accepted facts await assessment inputs"
	case StatusAccepted:
		context.DesiredPhase = ModuleStatusPublishing
	case StatusSuperseded:
		return ModuleContext{}, fmt.Errorf("superseded request cannot control module lifecycle")
	}
	switch work.RetryClass {
	case RetryBlocked:
		context.BlockedReason = "required work inputs are unavailable"
	case RetryNeedsReview:
		context.NeedsReview = true
	}
	return context, nil
}

func newModuleStatus(input ModuleStatusInput) ModuleStatus {
	return ModuleStatus{
		ModuleID: input.ModuleID, LatestSourceVersion: input.LatestSourceVersion,
		LatestSourceSHA: input.LatestSourceSHA, DeploymentGeneration: input.DeploymentGeneration,
		PublishedCommit: input.PublishedCommit,
	}
}

func requiredModuleContexts(input ModuleStatusInput, base *ModuleStatus) ([]classifiedContext, error) {
	tracks := make([]TrackState, 0, len(input.Contexts))
	contextByID := make(map[string]ModuleContext, len(input.Contexts))
	for _, context := range input.Contexts {
		tracks = append(tracks, context.State)
		if _, exists := contextByID[context.State.ContextID]; exists {
			return nil, fmt.Errorf("module status repeats context %q", context.State.ContextID)
		}
		contextByID[context.State.ContextID] = context
	}
	required, err := RequiredContexts(tracks)
	if err != nil {
		return nil, err
	}
	if len(required) == 1 && required[0].ContextID == LatestSourceContext {
		if _, exists := contextByID[LatestSourceContext]; !exists {
			contextByID[LatestSourceContext] = latestSourceContext(input)
		}
	}
	return classifyRequiredContexts(required, contextByID, base, input.DeploymentGeneration)
}

func classifyRequiredContexts(required []TrackState, contextByID map[string]ModuleContext, base *ModuleStatus, deploymentGeneration uint64) ([]classifiedContext, error) {
	contexts := make([]classifiedContext, 0, len(required))
	for _, track := range required {
		context, exists := contextByID[track.ContextID]
		if !exists {
			return nil, fmt.Errorf("required context %q has no lifecycle state", track.ContextID)
		}
		context.State = track
		classified, classifyErr := classifyModuleContext(context)
		if classifyErr != nil {
			return nil, fmt.Errorf("context %s: %w", track.ContextID, classifyErr)
		}
		if classified.phase == ModuleStatusPublishing && selectionAndDeploymentCurrent(context, deploymentGeneration) {
			classified.phase = ""
		}
		contexts = append(contexts, classified)
		base.RequiredContexts = append(base.RequiredContexts, track.ContextID)
	}
	sort.Slice(contexts, func(left, right int) bool {
		return contexts[left].context.State.ContextID < contexts[right].context.State.ContextID
	})
	return contexts, nil
}

func reduceClassifiedContexts(base ModuleStatus, contexts []classifiedContext, deploymentGeneration uint64) ModuleStatus {
	if selected, found := firstContext(contexts, func(value classifiedContext) bool { return value.context.NeedsReview }); found {
		return moduleStatusForContext(base, selected, ModuleStatusNeedsReview, "a required context needs review")
	}
	if selected, found := firstContext(contexts, func(value classifiedContext) bool { return value.blocked != "" }); found {
		return moduleStatusForContext(base, selected, ModuleStatusBlocked, selected.blocked)
	}
	if selected, found := firstContext(contexts, func(value classifiedContext) bool { return value.context.RetryableFailure }); found {
		return moduleStatusForContext(base, selected, ModuleStatusFailed, "a required context has retryable processing failure")
	}
	if selected, found := earliestOutstandingContext(contexts); found {
		return moduleStatusForContext(base, selected, selected.phase, "required context work is outstanding")
	}
	for _, context := range contexts {
		if !selectionAndDeploymentCurrent(context.context, deploymentGeneration) {
			phase := ModuleStatusBacklog
			if context.context.SelectedGeneration == context.context.DesiredGeneration && context.context.SelectionReceiptID != "" {
				phase = ModuleStatusPublishing
			}
			return moduleStatusForContext(base, context, phase, "current selection or deployment membership is incomplete")
		}
	}
	if len(contexts) == 0 {
		base.Status = ModuleStatusBlocked
		base.Reason = "no required context is available"
		return base
	}
	base.Status = ModuleStatusDone
	base.ControllingContext = contexts[0].context.State.ContextID
	copyContextSource(&base, contexts[0].context)
	base.DesiredGeneration = contexts[0].context.DesiredGeneration
	base.ActiveGeneration = contexts[0].context.ActiveGeneration
	return base
}

func latestSourceContext(input ModuleStatusInput) ModuleContext {
	context := ModuleContext{
		State:             TrackState{ContextID: LatestSourceContext, Track: LatestSourceContext, Applicability: ApplicabilityApplicable, Candidate: CandidateEligible},
		DesiredGeneration: 1, SourceVersion: input.LatestSourceVersion, SourceSHA: input.LatestSourceSHA,
	}
	if input.LatestSourceRunnable {
		context.DesiredPhase = ModuleStatusBacklog
	} else {
		context.BlockedReason = "latest source is unavailable"
	}
	return context
}

func classifyModuleContext(context ModuleContext) (classifiedContext, error) {
	if context.State.ContextID == "" || context.DesiredGeneration == 0 {
		return classifiedContext{}, fmt.Errorf("desired context identity or generation is missing")
	}
	phase, err := effectiveContextPhase(context)
	if err != nil {
		return classifiedContext{}, err
	}
	blocked := context.BlockedReason
	if blocked == "" && !context.RunnableAcquisition {
		switch {
		case context.State.Applicability == ApplicabilityUnresolved:
			blocked = "context applicability is unresolved"
		case context.State.Candidate == CandidateUnresolved:
			blocked = "context eligibility is unresolved"
		case context.State.Candidate == CandidateIneligible:
			blocked = "no eligible candidate is available"
		}
	}
	if context.RunnableAcquisition && phase == "" {
		phase = ModuleStatusBacklog
	}
	return classifiedContext{context: context, blocked: blocked, phase: phase}, nil
}

func effectiveContextPhase(context ModuleContext) (string, error) {
	if context.DesiredPhase != "" {
		if !validProjectionPhase(context.DesiredPhase) {
			return "", fmt.Errorf("invalid desired phase %q", context.DesiredPhase)
		}
		if context.DesiredGeneration > context.ActiveGeneration || context.ActivePhase == "" {
			return context.DesiredPhase, nil
		}
	}
	if context.ActivePhase != "" {
		if !validProjectionPhase(context.ActivePhase) {
			return "", fmt.Errorf("invalid active phase %q", context.ActivePhase)
		}
		return context.ActivePhase, nil
	}
	if context.DesiredPhase != "" {
		return context.DesiredPhase, nil
	}
	if !selectionCurrent(context) {
		return ModuleStatusBacklog, nil
	}
	if context.DeployedAnalysisID != context.AnalysisID || context.DeployedSelectionID != context.SelectionReceiptID {
		return ModuleStatusPublishing, nil
	}
	return "", nil
}

func validProjectionPhase(phase string) bool {
	switch phase {
	case ModuleStatusBacklog, ModuleStatusQueued, ModuleStatusAnalyzing, ModuleStatusValidating, ModuleStatusPublishing:
		return true
	default:
		return false
	}
}

func selectionAndDeploymentCurrent(context ModuleContext, deploymentGeneration uint64) bool {
	return selectionCurrent(context) && context.DeployedAnalysisID == context.AnalysisID && context.DeployedSelectionID == context.SelectionReceiptID && deploymentGeneration > 0
}

func selectionCurrent(context ModuleContext) bool {
	return context.DesiredGeneration != 0 && context.SelectedGeneration == context.DesiredGeneration && context.DesiredFingerprint != "" && context.SelectedFingerprint == context.DesiredFingerprint && context.AnalysisID != "" && context.SelectionReceiptID != ""
}

func firstContext(contexts []classifiedContext, predicate func(classifiedContext) bool) (classifiedContext, bool) {
	for _, context := range contexts {
		if predicate(context) {
			return context, true
		}
	}
	return classifiedContext{}, false
}

func earliestOutstandingContext(contexts []classifiedContext) (classifiedContext, bool) {
	selected := classifiedContext{}
	selectedRank := len(contextPhaseOrder)
	found := false
	for _, context := range contexts {
		rank := contextPhaseRank(context.phase)
		if rank < 0 {
			continue
		}
		if !found || rank < selectedRank || (rank == selectedRank && context.context.State.ContextID < selected.context.State.ContextID) {
			selected, selectedRank, found = context, rank, true
		}
	}
	return selected, found
}

var contextPhaseOrder = []string{ModuleStatusBacklog, ModuleStatusQueued, ModuleStatusAnalyzing, ModuleStatusValidating, ModuleStatusPublishing}

func contextPhaseRank(phase string) int {
	for position, candidate := range contextPhaseOrder {
		if phase == candidate {
			return position
		}
	}
	return -1
}

func moduleStatusForContext(base ModuleStatus, selected classifiedContext, status, reason string) ModuleStatus {
	base.Status = status
	base.Reason = reason
	base.ControllingContext = selected.context.State.ContextID
	base.DesiredGeneration = selected.context.DesiredGeneration
	base.ActiveGeneration = selected.context.ActiveGeneration
	copyContextSource(&base, selected.context)
	return base
}

func copyContextSource(status *ModuleStatus, context ModuleContext) {
	status.SourceVersion = context.SourceVersion
	status.SourceSHA = context.SourceSHA
}
