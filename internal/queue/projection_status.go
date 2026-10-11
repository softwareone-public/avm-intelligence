package queue

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/softwareone-public/avm-intelligence/internal/state"
)

// ReduceActiveModuleProjectionStatus reduces the release held by a module's
// lifecycle slot. Newer queued releases are retained as work but cannot
// replace the status of the active release. The bool is false when no active
// slot exists and the caller should reduce the queued/latest release instead.
func ReduceActiveModuleProjectionStatus(snapshot state.Snapshot, moduleID string, pending []Request) (ModuleStatus, bool, error) {
	slot, exists, err := lifecycleSlotFromSnapshot(snapshot, moduleID)
	if err != nil || !exists || slot.Status != "active" {
		return ModuleStatus{}, false, err
	}
	requests, err := loadVersionedRequestsForModule(snapshot, moduleID)
	if err != nil {
		return ModuleStatus{}, false, err
	}
	byID := make(map[string]Request, len(requests)+len(pending))
	for _, request := range requests {
		if request.ModuleID == moduleID {
			byID[request.ID] = request
		}
	}
	for _, request := range pending {
		if request.ModuleID != moduleID || request.Validate() != nil {
			return ModuleStatus{}, false, fmt.Errorf("pending module projection request is invalid")
		}
		byID[request.ID] = request
	}
	requests = requests[:0]
	for _, request := range byID {
		requests = append(requests, request)
	}
	sort.Slice(requests, func(left, right int) bool {
		if requests[left].Work.AVMReleaseVersion != requests[right].Work.AVMReleaseVersion {
			return compareAVMReleases(requests[left].Work.AVMReleaseVersion, requests[right].Work.AVMReleaseVersion) < 0
		}
		if requests[left].Work.DesiredGeneration != requests[right].Work.DesiredGeneration {
			return requests[left].Work.DesiredGeneration < requests[right].Work.DesiredGeneration
		}
		return requests[left].ID < requests[right].ID
	})

	slotRequest, found := byID[slot.RequestID]
	if !found || slotRequest.ModuleID != moduleID || slotRequest.Work.AVMReleaseVersion != slot.ReleaseVersion {
		return ModuleStatus{}, false, fmt.Errorf("active lifecycle slot request is missing or mismatched")
	}
	receipts, err := state.AcceptedFactsContextSets(snapshot, moduleID)
	if err != nil {
		return ModuleStatus{}, false, fmt.Errorf("read accepted context sets for active module slot: %w", err)
	}
	var accepted *state.EvidenceAcceptanceReceipt
	for index := range receipts {
		receipt := &receipts[index]
		if receipt.AVMReleaseVersion != slot.ReleaseVersion || receipt.SourceSHA != slotRequest.SourceSHA || receipt.RequiredContexts == nil {
			continue
		}
		if accepted == nil || receipt.DesiredGeneration > accepted.DesiredGeneration {
			accepted = receipt
		} else if receipt.DesiredGeneration == accepted.DesiredGeneration && (receipt.FactsID != accepted.FactsID || receipt.RequiredContexts.ID != accepted.RequiredContexts.ID) {
			return ModuleStatus{}, false, fmt.Errorf("active release has conflicting accepted facts context sets")
		}
	}

	if accepted == nil {
		context, contextErr := ContextFromRequest(slotRequest)
		if contextErr != nil {
			return ModuleStatus{}, false, contextErr
		}
		context.SourceVersion = slot.ReleaseVersion
		status, reduceErr := ReduceModuleStatus(ModuleStatusInput{
			ModuleID: moduleID, Lifecycle: ModuleLifecycleActive, Contexts: []ModuleContext{context},
			LatestSourceVersion: slot.ReleaseVersion, LatestSourceSHA: slotRequest.SourceSHA,
		})
		return status, true, reduceErr
	}
	set := accepted.RequiredContexts
	if err := set.Validate(); err != nil || set.ModuleID != moduleID || set.AVMReleaseVersion != slot.ReleaseVersion || set.SourceSHA != slotRequest.SourceSHA || set.FactsID != accepted.FactsID {
		return ModuleStatus{}, false, fmt.Errorf("active release accepted context set identity is invalid")
	}
	tracks := make([]TrackState, 0, len(set.Contexts))
	for _, configured := range set.Contexts {
		tracks = append(tracks, TrackState{
			ContextID: configured.State.ContextID, Track: configured.State.Track,
			Applicability: Applicability(configured.State.Applicability), Candidate: CandidateState(configured.State.Candidate),
		})
	}
	required, err := RequiredContexts(tracks)
	if err != nil {
		return ModuleStatus{}, false, err
	}
	inputState, err := state.ReadAssessmentModuleState(snapshot, moduleID)
	if err != nil {
		return ModuleStatus{}, false, err
	}
	contexts := make([]ModuleContext, 0, len(required))
	for _, track := range required {
		if track.ContextID == LatestSourceContext {
			context, contextErr := ContextFromRequest(slotRequest)
			if contextErr != nil {
				return ModuleStatus{}, false, contextErr
			}
			context.State = track
			context.SourceVersion = slot.ReleaseVersion
			contexts = append(contexts, context)
			continue
		}
		var desired, active *Request
		for index := range requests {
			request := &requests[index]
			if request.ModuleID != moduleID || request.Status == StatusSuperseded || request.Work.Phase != PhaseAssess || request.Work.AVMReleaseVersion != slot.ReleaseVersion || request.Work.ContextID != track.ContextID || request.Work.FactsID != accepted.FactsID || request.SourceSHA != accepted.SourceSHA {
				continue
			}
			if desired == nil || request.Work.DesiredGeneration > desired.Work.DesiredGeneration {
				desired = request
			} else if request.Work.DesiredGeneration == desired.Work.DesiredGeneration && request.ID != desired.ID {
				return ModuleStatus{}, false, fmt.Errorf("active release context has ambiguous desired requests")
			}
			if request.Status == StatusClaimed || request.Status == StatusRunning {
				if active != nil && active.ID != request.ID {
					return ModuleStatus{}, false, fmt.Errorf("active release context has multiple owners")
				}
				active = request
			}
		}
		if desired == nil {
			contextGeneration := inputState.Versions[slot.ReleaseVersion][track.ContextID].DesiredGeneration
			if contextGeneration == 0 {
				contextGeneration = accepted.DesiredGeneration
			}
			context := ModuleContext{
				State: track, DesiredGeneration: contextGeneration, SourceVersion: slot.ReleaseVersion,
				SourceSHA: accepted.SourceSHA, BlockedReason: "required assessment context has no request from exact accepted inputs",
			}
			contexts = append(contexts, context)
			continue
		}
		context, contextErr := ContextFromRequest(*desired)
		if contextErr != nil {
			return ModuleStatus{}, false, contextErr
		}
		context.State = track
		context.SourceVersion = slot.ReleaseVersion
		if active != nil {
			activeContext, activeErr := ContextFromRequest(*active)
			if activeErr != nil {
				return ModuleStatus{}, false, activeErr
			}
			context.ActiveGeneration, context.ActivePhase = activeContext.ActiveGeneration, activeContext.ActivePhase
		}
		contexts = append(contexts, context)
	}
	status, err := ReduceModuleStatus(ModuleStatusInput{
		ModuleID: moduleID, Lifecycle: ModuleLifecycleActive, Contexts: contexts,
		LatestSourceVersion: slot.ReleaseVersion, LatestSourceSHA: slotRequest.SourceSHA,
	})
	return status, true, err
}

// ReduceModuleProjectionStatus selects the currently controlling release for
// module-wide projection. An active lifecycle slot wins; otherwise the oldest
// queued source request that can next acquire the slot wins. If neither exists,
// callers may use the latest neutral handoff as the lifecycle fallback.
func ReduceModuleProjectionStatus(snapshot state.Snapshot, moduleID string, pending []Request) (ModuleStatus, bool, error) {
	active, held, err := ReduceActiveModuleProjectionStatus(snapshot, moduleID, pending)
	if err != nil || held {
		return active, held, err
	}
	requests, err := loadVersionedRequestsForModule(snapshot, moduleID)
	if err != nil {
		return ModuleStatus{}, false, err
	}
	byID := make(map[string]Request, len(requests)+len(pending))
	for _, request := range requests {
		if request.ModuleID == moduleID {
			byID[request.ID] = request
		}
	}
	for _, request := range pending {
		if request.ModuleID != moduleID || request.Validate() != nil {
			return ModuleStatus{}, false, fmt.Errorf("pending module projection request is invalid")
		}
		if existing, exists := byID[request.ID]; exists && !reflect.DeepEqual(existing, request) {
			return ModuleStatus{}, false, fmt.Errorf("pending module projection request conflicts with durable state")
		}
		byID[request.ID] = request
	}
	requests = requests[:0]
	for _, request := range byID {
		requests = append(requests, request)
	}
	sort.Slice(requests, func(left, right int) bool { return dispatchRequestLess(requests[left], requests[right]) })
	if moduleHasActiveClaim(requests, moduleID) {
		return ModuleStatus{}, false, fmt.Errorf("module has an active request without an active lifecycle slot")
	}
	for index, request := range requests {
		if request.ModuleID != moduleID || request.Status != StatusQueued || request.Work == nil || request.Work.Phase != PhaseAcquireSource || hasNewerDesiredRequest(requests, index) {
			continue
		}
		eligible, eligibilityErr := slotAllowsRequestInQueue(snapshot, request, requests)
		if eligibilityErr != nil {
			return ModuleStatus{}, false, eligibilityErr
		}
		if !eligible {
			continue
		}
		context, contextErr := ContextFromRequest(request)
		if contextErr != nil {
			return ModuleStatus{}, false, contextErr
		}
		context.State = TrackState{ContextID: LatestSourceContext, Track: LatestSourceContext, Applicability: ApplicabilityApplicable, Candidate: CandidateEligible}
		context.SourceVersion = request.Work.AVMReleaseVersion
		status, reduceErr := ReduceModuleStatus(ModuleStatusInput{
			ModuleID: moduleID, Lifecycle: ModuleLifecycleActive, Contexts: []ModuleContext{context},
			LatestSourceRunnable: true, LatestSourceVersion: request.Work.AVMReleaseVersion, LatestSourceSHA: request.SourceSHA,
		})
		return status, true, reduceErr
	}
	return ModuleStatus{}, false, nil
}

// NeutralStatus maps the canonical module reducer values into the neutral
// projection status vocabulary.
func NeutralStatus(status string) string {
	switch status {
	case ModuleStatusBacklog:
		return "backlog"
	case ModuleStatusQueued:
		return "queued"
	case ModuleStatusAnalyzing:
		return "analyzing"
	case ModuleStatusValidating:
		return "validating"
	case ModuleStatusPublishing:
		return "publishing"
	case ModuleStatusDone:
		return "done"
	case ModuleStatusFailed:
		return "failed"
	case ModuleStatusBlocked:
		return "blocked"
	case ModuleStatusNeedsReview:
		return "needs-review"
	case ModuleStatusRetired:
		return "retired"
	default:
		return ""
	}
}
