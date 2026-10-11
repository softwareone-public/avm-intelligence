package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

// AssessmentWakeCommit is the durable queue result for one module's eligible
// assessment contexts. Deferred contexts stay in the result for later review.
type AssessmentWakeCommit struct {
	Head     string                   `json:"head"`
	Requests []Request                `json:"requests"`
	Deferred []AssessmentWakeDeferred `json:"deferred"`
	Changed  bool                     `json:"changed"`
}

type assessmentWakeOperationResult struct {
	Commit   AssessmentWakeCommit          `json:"commit"`
	Events   []state.Event                 `json:"events"`
	Producer projection.WorkflowProvenance `json:"producer"`
}

type assessmentWakeOperationIdentity struct {
	ModuleID       string                   `json:"module_id"`
	AcceptedCommit string                   `json:"accepted_commit"`
	Requests       []wakeRequestID          `json:"requests"`
	Deferred       []AssessmentWakeDeferred `json:"deferred"`
}

type wakeRequestID struct {
	ID                string `json:"request_id"`
	Fingerprint       string `json:"input_fingerprint"`
	DesiredGeneration uint64 `json:"desired_generation"`
}

// EnqueueAssessmentWake atomically persists a module's exact eligible requests
// with an immutable operation receipt. It never dispatches the requests.
func EnqueueAssessmentWake(ctx context.Context, coordinator state.TransactionCoordinator, producer projection.WorkflowProvenance, acceptedCommit string, requests []Request, deferred []AssessmentWakeDeferred, now time.Time) (AssessmentWakeCommit, error) {
	if coordinator == nil || producer.Validate() != nil || producer.Commit != acceptedCommit || identity.ValidateSHA1(acceptedCommit) != nil || now.IsZero() || len(requests) == 0 {
		return AssessmentWakeCommit{}, fmt.Errorf("assessment wake requires exact accepted commit, producer, requests and time")
	}
	ordered := cloneRequests(requests)
	for index, request := range ordered {
		if request.Work == nil {
			return AssessmentWakeCommit{}, fmt.Errorf("assessment wake request %d has no work contract", index)
		}
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].Work.ContextID != ordered[right].Work.ContextID {
			return ordered[left].Work.ContextID < ordered[right].Work.ContextID
		}
		return ordered[left].ID < ordered[right].ID
	})
	moduleID := ordered[0].ModuleID
	requestIDs := make([]wakeRequestID, 0, len(ordered))
	seenContexts := make(map[string]struct{}, len(ordered))
	for index, request := range ordered {
		if err := request.Validate(); err != nil || request.Work == nil || request.Work.Phase != PhaseAssess || request.Status != StatusQueued || request.ModuleID != moduleID {
			return AssessmentWakeCommit{}, fmt.Errorf("assessment wake request %d is invalid or belongs to another module", index)
		}
		contextID := request.Work.AVMReleaseVersion + "\x00" + request.Work.ContextID
		if _, duplicate := seenContexts[contextID]; duplicate {
			return AssessmentWakeCommit{}, fmt.Errorf("assessment wake repeats a module-release context")
		}
		seenContexts[contextID] = struct{}{}
		requestIDs = append(requestIDs, wakeRequestID{ID: request.ID, Fingerprint: request.Fingerprint, DesiredGeneration: request.Work.DesiredGeneration})
	}
	if err := producer.Validate(); err != nil {
		return AssessmentWakeCommit{}, err
	}
	orderedDeferred := cloneAssessmentWakeDeferred(deferred)
	seenDeferred := make(map[string]struct{}, len(orderedDeferred))
	for _, item := range orderedDeferred {
		if item.ModuleID != moduleID || !avmReleaseVersionPattern.MatchString(item.AVMReleaseVersion) || item.Reason == "" || identity.ValidateRelativePath(item.ContextID, false) != nil {
			return AssessmentWakeCommit{}, fmt.Errorf("assessment wake deferred context is invalid")
		}
		key := item.AVMReleaseVersion + "\x00" + item.ContextID
		if _, duplicate := seenDeferred[key]; duplicate {
			return AssessmentWakeCommit{}, fmt.Errorf("assessment wake repeats a deferred module-release context")
		}
		seenDeferred[key] = struct{}{}
	}
	sort.Slice(orderedDeferred, func(left, right int) bool {
		if orderedDeferred[left].ModuleID != orderedDeferred[right].ModuleID {
			return orderedDeferred[left].ModuleID < orderedDeferred[right].ModuleID
		}
		if orderedDeferred[left].AVMReleaseVersion != orderedDeferred[right].AVMReleaseVersion {
			return orderedDeferred[left].AVMReleaseVersion < orderedDeferred[right].AVMReleaseVersion
		}
		return orderedDeferred[left].ContextID < orderedDeferred[right].ContextID
	})
	operationID, err := identity.ID("assessment-wake", 1, assessmentWakeOperationIdentity{
		ModuleID: moduleID, AcceptedCommit: acceptedCommit, Requests: requestIDs, Deferred: orderedDeferred,
	})
	if err != nil {
		return AssessmentWakeCommit{}, err
	}
	operationPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		return AssessmentWakeCommit{}, err
	}
	head, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return AssessmentWakeCommit{}, err
	}
	if head == "" {
		return AssessmentWakeCommit{}, fmt.Errorf("state branch head is required")
	}
	var expected []state.Event
	if record, exists := snapshot.Records[operationPath]; exists {
		if record.Kind != "operation_receipt" || record.SchemaVersion != 1 {
			return AssessmentWakeCommit{}, state.ErrTransactionIdentityConflict
		}
		var receipt state.OperationReceipt
		if json.Unmarshal(record.Data, &receipt) != nil || receipt.Validate(operationPath) != nil || receipt.OperationID != operationID || receipt.ModuleID != moduleID {
			return AssessmentWakeCommit{}, state.ErrTransactionIdentityConflict
		}
		var previous assessmentWakeOperationResult
		if json.Unmarshal(receipt.Result, &previous) != nil || len(previous.Events) == 0 || !wakeRequestsMatch(previous.Commit.Requests, ordered) || !reflect.DeepEqual(previous.Commit.Deferred, orderedDeferred) || !sameWakeProducer(previous.Producer, producer) {
			return AssessmentWakeCommit{}, state.ErrTransactionIdentityConflict
		}
		expected = previous.Events
	} else {
		expected, _, _, err = assessmentWakeEvents(snapshot, ordered)
		if err != nil {
			return AssessmentWakeCommit{}, err
		}
		if len(expected) == 0 {
			return AssessmentWakeCommit{Head: head, Requests: existingWakeRequests(snapshot, ordered), Deferred: orderedDeferred}, nil
		}
	}
	operationCoordinator, ok := coordinator.(interface {
		RunOperationTransaction(context.Context, string, string, []state.Event, state.TransactionDecisionFunc, state.TransactionResultResolver) (state.TransactionResult, error)
	})
	if !ok {
		return AssessmentWakeCommit{}, fmt.Errorf("assessment wake requires an operation-receipt coordinator")
	}
	result, err := operationCoordinator.RunOperationTransaction(ctx, moduleID, operationID, expected, func(current state.Snapshot) (state.TransactionDecision, error) {
		events, _, committedRequests, buildErr := assessmentWakeEvents(current, ordered)
		if buildErr != nil {
			return state.TransactionDecision{}, buildErr
		}
		if len(events) == 0 || !sameWakeEvents(events, expected) {
			return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
		}
		commit := AssessmentWakeCommit{Requests: committedRequests, Deferred: orderedDeferred, Changed: true}
		return state.TransactionDecision{
			Events:    events,
			Result:    assessmentWakeOperationResult{Commit: commit, Events: events, Producer: producer},
			Candidate: state.CandidateOptions{Subject: "queue: enqueue accepted assessment contexts", Provenance: state.GitProvenance{Operation: "assessment.wake", Event: operationID, Module: moduleID}},
		}, nil
	}, func(current state.Snapshot) (state.TransactionResolution, error) {
		record, exists := current.Records[operationPath]
		if !exists || record.Kind != "operation_receipt" {
			return state.TransactionResolution{}, nil
		}
		var receipt state.OperationReceipt
		var previous assessmentWakeOperationResult
		if json.Unmarshal(record.Data, &receipt) != nil || receipt.Validate(operationPath) != nil || json.Unmarshal(receipt.Result, &previous) != nil || !wakeRequestsMatch(previous.Commit.Requests, ordered) || !reflect.DeepEqual(previous.Commit.Deferred, orderedDeferred) || !sameWakeProducer(previous.Producer, producer) {
			return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
		}
		return state.TransactionResolution{Found: true, Value: previous}, nil
	})
	if err != nil {
		return AssessmentWakeCommit{}, err
	}
	committed, err := assessmentWakeResult(result.Value)
	if err != nil {
		return AssessmentWakeCommit{}, err
	}
	committed.Head = result.Head
	committed.Changed = result.Changed
	return committed, nil
}

func assessmentWakeEvents(snapshot state.Snapshot, desired []Request) ([]state.Event, []Request, []Request, error) {
	moduleIDs := make(map[string]struct{}, len(desired))
	for _, request := range desired {
		moduleIDs[request.ModuleID] = struct{}{}
	}
	existing, err := loadVersionedRequestsForModules(snapshot, moduleIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	updated := cloneRequests(existing)
	for _, request := range desired {
		updated, _, err = CoalesceRequests(updated, request)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	previous := make(map[string]Request, len(existing))
	for _, request := range existing {
		previous[request.ID] = request
	}
	changed := make([]Request, 0, len(desired))
	committed := make([]Request, 0, len(desired))
	desiredIDs := make(map[string]struct{}, len(desired))
	for _, request := range desired {
		desiredIDs[request.ID] = struct{}{}
	}
	for _, request := range updated {
		before, existed := previous[request.ID]
		if _, wanted := desiredIDs[request.ID]; wanted {
			committed = append(committed, request)
		}
		if !existed || !reflect.DeepEqual(before, request) {
			changed = append(changed, request)
		}
	}
	if len(committed) != len(desired) {
		return nil, nil, nil, state.ErrTransactionIdentityConflict
	}
	sort.Slice(committed, func(left, right int) bool {
		if committed[left].Work.AVMReleaseVersion != committed[right].Work.AVMReleaseVersion {
			return committed[left].Work.AVMReleaseVersion < committed[right].Work.AVMReleaseVersion
		}
		if committed[left].Work.ContextID != committed[right].Work.ContextID {
			return committed[left].Work.ContextID < committed[right].Work.ContextID
		}
		return committed[left].ID < committed[right].ID
	})
	sort.Slice(changed, func(left, right int) bool { return changed[left].ID < changed[right].ID })
	events := make([]state.Event, 0, len(changed))
	for _, request := range changed {
		event, eventErr := stateRecordEvent(stateRequestPath+request.ID+".json", "request", request, "", 0)
		if eventErr != nil {
			return nil, nil, nil, eventErr
		}
		events = append(events, event)
	}
	return events, changed, committed, nil
}

func existingWakeRequests(snapshot state.Snapshot, desired []Request) []Request {
	requests := make([]Request, 0, len(desired))
	for _, target := range desired {
		request, err := loadRequest(snapshot, target.ID)
		if err == nil {
			requests = append(requests, request)
		}
	}
	return requests
}

func assessmentWakeResult(value any) (AssessmentWakeCommit, error) {
	var operation assessmentWakeOperationResult
	switch result := value.(type) {
	case assessmentWakeOperationResult:
		operation = result
	case json.RawMessage:
		if err := json.Unmarshal(result, &operation); err != nil {
			return AssessmentWakeCommit{}, fmt.Errorf("decode assessment wake receipt: %w", err)
		}
	default:
		return AssessmentWakeCommit{}, state.ErrTransactionIdentityConflict
	}
	if len(operation.Events) == 0 {
		return AssessmentWakeCommit{}, state.ErrTransactionIdentityConflict
	}
	return operation.Commit, nil
}

func wakeRequestsMatch(left, right []Request) bool {
	if len(left) != len(right) {
		return false
	}
	leftByID := make(map[string]Request, len(left))
	for _, request := range left {
		leftByID[request.ID] = request
	}
	for _, request := range right {
		stored, exists := leftByID[request.ID]
		if !exists || stored.Fingerprint != request.Fingerprint || stored.ModuleID != request.ModuleID || stored.Work == nil || request.Work == nil ||
			stored.Work.DesiredGeneration != request.Work.DesiredGeneration || stored.Work.AVMReleaseVersion != request.Work.AVMReleaseVersion || stored.Work.ContextID != request.Work.ContextID {
			return false
		}
	}
	return true
}

func sameWakeEvents(left, right []state.Event) bool {
	if len(left) != len(right) {
		return false
	}
	leftDigests := make(map[string]string, len(left))
	for _, event := range left {
		digest, err := identity.ID("state-event", 1, event)
		if err != nil {
			return false
		}
		leftDigests[event.ID] = digest
	}
	for _, event := range right {
		digest, err := identity.ID("state-event", 1, event)
		if err != nil || leftDigests[event.ID] != digest {
			return false
		}
	}
	return true
}

func cloneAssessmentWakeDeferred(deferred []AssessmentWakeDeferred) []AssessmentWakeDeferred {
	return append([]AssessmentWakeDeferred(nil), deferred...)
}

func sameWakeProducer(original, retry projection.WorkflowProvenance) bool {
	return original.Validate() == nil && retry.Validate() == nil && original.WorkflowID == retry.WorkflowID && original.Commit == retry.Commit
}
