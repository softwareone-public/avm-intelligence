package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/contextset"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
)

const (
	selectionRecordSchemaVersion = 1
	selectionSchemaVersion       = 3
	selectionRecordType          = "selection_receipt"
	selectionExportRecordType    = "selection_export_intent"
	selectionStatePrefix         = "state/selections/"
	selectionExportPrefix        = "state/exports/"
	selectionCurrentSuffix       = "/current.json"
	selectionExportOperation     = "selection.export"
	selectionStateEventKind      = "state-event"
	selectionExportImportKind    = "selection-export-import"
	ExportPending                = "pending"
	ExportMerged                 = "merged"
)

var (
	ErrSelectionStale          = errors.New("selection does not match current desired work")
	ErrSelectionIneligible     = errors.New("selection is not currently eligible")
	ErrSelectionNotFound       = errors.New("current selection was not found")
	ErrSelectionStateMissing   = errors.New("selection state is missing; recovery review is required")
	ErrSelectionBarrierPending = errors.New("selection predecessor projection barrier is pending")
	selectionModulePattern     = regexp.MustCompile(`^avm-(res|ptn|utl)-[a-z0-9]+(?:-[a-z0-9]+)*$`)
	selectionReleasePattern    = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	selectionSHA1Pattern       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	selectionSHA256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// SelectionInput identifies one accepted assessment whose sealed completion
// may become the selected result for its module and context.
type SelectionInput struct {
	ModuleID            string
	ContextID           string
	RequestID           string
	CompletionID        string
	AcceptanceHandoffID string
	AnalysisID          string
	EvidenceCommit      string
	SelectedAt          time.Time
	Producer            projection.WorkflowProvenance
}

// DeselectRetiredInput creates a higher-sequence null selection only after
// durable discovery confirms the module is retired.
type DeselectRetiredInput struct {
	ModuleID          string
	AVMReleaseVersion string
	ContextID         string
	DesiredGeneration uint64
	InputFingerprint  string
	EvidenceCommit    string
	SelectedAt        time.Time
}

// SelectionReceipt is the immutable operational decision exported to the
// canonical dataset. CompletionID binds the decision to trusted worker output.
type SelectionReceipt struct {
	SchemaVersion     int       `json:"schema_version"`
	RecordType        string    `json:"record_type"`
	ID                string    `json:"selection_id"`
	ModuleID          string    `json:"module_id"`
	AVMReleaseVersion string    `json:"avm_release_version"`
	ContextID         string    `json:"context_id"`
	Sequence          uint64    `json:"sequence"`
	DesiredGeneration uint64    `json:"desired_generation"`
	SourceSHA         *string   `json:"source_sha"`
	FactsID           *string   `json:"facts_id"`
	InputFingerprint  string    `json:"input_fingerprint"`
	AnalysisID        *string   `json:"analysis_id"`
	CompletionID      *string   `json:"completion_id"`
	EvidenceCommit    string    `json:"evidence_commit"`
	SelectedAt        time.Time `json:"selected_at"`
}

// SelectionPointerRecord is the mutable per-context pointer and sequence
// high-water mark. Receipt history remains append-only beside this record.
type SelectionPointerRecord struct {
	SchemaVersion     int       `json:"schema_version"`
	ModuleID          string    `json:"module_id"`
	AVMReleaseVersion string    `json:"avm_release_version"`
	ContextID         string    `json:"context_id"`
	Sequence          uint64    `json:"sequence"`
	SelectionID       string    `json:"selection_id"`
	RequestID         string    `json:"request_id"`
	CompletionID      string    `json:"completion_id"`
	DesiredGeneration uint64    `json:"desired_generation"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// SelectionExport is a deterministic outbox entry. It is created in the same
// state transaction as its receipt and is reconciled after canonical merge.
type SelectionExport struct {
	SchemaVersion int              `json:"schema_version"`
	RecordType    string           `json:"record_type"`
	ID            string           `json:"export_id"`
	SelectionID   string           `json:"selection_id"`
	ModuleID      string           `json:"module_id"`
	ContextID     string           `json:"context_id"`
	Status        string           `json:"status"`
	Receipt       SelectionReceipt `json:"receipt"`
	CreatedAt     time.Time        `json:"created_at"`
	MergedCommit  string           `json:"merged_commit,omitempty"`
	MergedAt      time.Time        `json:"merged_at,omitempty"`
}

// SelectionResult returns the durable state head and the receipt/outbox pair
// committed by SelectAccepted.
type SelectionResult struct {
	Head    string
	Receipt SelectionReceipt
	Export  SelectionExport
	Changed bool
}

type selectionOperationResult struct {
	Selection SelectionResult `json:"selection"`
	Events    []Event         `json:"events"`
}

type selectionRequest struct {
	ID                      string                `json:"id"`
	ModuleID                string                `json:"module_id"`
	SourceSHA               string                `json:"source_sha"`
	Fingerprint             string                `json:"fingerprint"`
	Status                  string                `json:"status"`
	LastAcceptedFingerprint string                `json:"last_accepted_fingerprint,omitempty"`
	LastAcceptedSourceSHA   string                `json:"last_accepted_source_sha,omitempty"`
	Work                    *selectionWorkRequest `json:"work,omitempty"`
}

type selectionWorkRequest struct {
	Phase             string                   `json:"phase"`
	AVMReleaseVersion string                   `json:"avm_release_version,omitempty"`
	ContextID         string                   `json:"context_id,omitempty"`
	Track             string                   `json:"track,omitempty"`
	DesiredGeneration uint64                   `json:"desired_generation"`
	FactsID           string                   `json:"facts_id,omitempty"`
	DefinitionIDs     []string                 `json:"definition_ids,omitempty"`
	SnapshotIDs       []string                 `json:"snapshot_ids,omitempty"`
	Applicability     string                   `json:"applicability"`
	Candidate         string                   `json:"candidate"`
	Fence             uint64                   `json:"fence"`
	Completion        *selectionCompletionWork `json:"completion,omitempty"`
}

type selectionCompletionWork struct {
	ID                string    `json:"id"`
	RequestID         string    `json:"request_id"`
	InputFingerprint  string    `json:"input_fingerprint"`
	DesiredGeneration uint64    `json:"desired_generation"`
	Fence             uint64    `json:"fence"`
	ArtifactDigest    string    `json:"artifact_digest"`
	ValidationID      string    `json:"validation_id"`
	SealedAt          time.Time `json:"sealed_at"`
}

type selectionIdentity struct {
	SchemaVersion     int       `json:"schema_version"`
	RecordType        string    `json:"record_type"`
	ModuleID          string    `json:"module_id"`
	AVMReleaseVersion string    `json:"avm_release_version"`
	ContextID         string    `json:"context_id"`
	Sequence          uint64    `json:"sequence"`
	DesiredGeneration uint64    `json:"desired_generation"`
	SourceSHA         *string   `json:"source_sha"`
	FactsID           *string   `json:"facts_id"`
	InputFingerprint  string    `json:"input_fingerprint"`
	AnalysisID        *string   `json:"analysis_id"`
	EvidenceCommit    string    `json:"evidence_commit"`
	SelectedAt        time.Time `json:"selected_at"`
}

type selectionEventIdentity struct {
	Path   string `json:"path"`
	Record Record `json:"record"`
}

type selectionSourcePin struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

// SelectAccepted validates the current catalog and assessment request at one
// snapshot, then commits the receipt, context pointer, and export outbox as one
// expected-head transaction. It deliberately does not inspect leases in other
// contexts for the same module.
func SelectAccepted(ctx context.Context, coordinator TransactionCoordinator, input SelectionInput) (SelectionResult, error) {
	if coordinator == nil {
		return SelectionResult{}, fmt.Errorf("state transaction coordinator is required")
	}
	if err := input.Producer.Validate(); err != nil {
		return SelectionResult{}, fmt.Errorf("selection producer provenance is invalid: %w", err)
	}
	operationCoordinator, ok := coordinator.(interface {
		RunOperationTransaction(context.Context, string, string, []Event, TransactionDecisionFunc, TransactionResultResolver) (TransactionResult, error)
	})
	if !ok {
		return SelectionResult{}, fmt.Errorf("selection requires an operation-receipt coordinator")
	}
	operationID, err := selectionOperationID("select", input.ModuleID, input.ContextID, input.RequestID, input.CompletionID, input.SelectedAt)
	if err != nil {
		return SelectionResult{}, err
	}
	operationPath, err := identity.StateOperationReceiptPath(input.ModuleID, operationID)
	if err != nil {
		return SelectionResult{}, err
	}
	head, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return SelectionResult{}, err
	}
	var expected []Event
	if _, present := snapshot.Records[operationPath]; present {
		recorded, exists := operationReceiptRecord(snapshot, input.ModuleID, operationID)
		if !exists || recorded.Validate(operationPath) != nil || verifyReceiptEvent(snapshot, operationID, operationPath) != nil {
			return SelectionResult{}, ErrTransactionIdentityConflict
		}
		var previous selectionOperationResult
		if json.Unmarshal(recorded.Result, &previous) != nil || previous.Selection.Receipt.Validate() != nil || len(previous.Events) < 5 || len(previous.Events) > 6 || !sameSelectionRequest(previous.Selection.Receipt, input) || verifyReceiptWriteSet(snapshot, recorded, previous.Events) != nil {
			return SelectionResult{}, ErrTransactionIdentityConflict
		}
		expected = previous.Events
	} else {
		decision, buildErr := selectAcceptedDecision(snapshot, input, operationID)
		if buildErr != nil {
			return SelectionResult{}, buildErr
		}
		if decision.Noop {
			previous, ok := decision.Result.(SelectionResult)
			if !ok || previous.Receipt.Validate() != nil || previous.Receipt.ModuleID != input.ModuleID || previous.Receipt.ContextID != input.ContextID || previous.Receipt.CompletionID == nil || *previous.Receipt.CompletionID != input.CompletionID {
				return SelectionResult{}, ErrSelectionStateMissing
			}
			previous.Head = head
			previous.Changed = false
			return previous, nil
		}
		expected = decision.Events
	}
	result, err := operationCoordinator.RunOperationTransaction(ctx, input.ModuleID, operationID, expected, func(current Snapshot) (TransactionDecision, error) {
		decision, buildErr := selectAcceptedDecision(current, input, operationID)
		if buildErr != nil {
			return TransactionDecision{}, buildErr
		}
		if decision.Noop || !sameEventSet(decision.Events, expected) {
			return TransactionDecision{}, ErrTransactionIdentityConflict
		}
		decision.Result = selectionOperationResult{Selection: decision.Result.(SelectionResult), Events: expected}
		return decision, nil
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		return resolveSelectionResult(snapshot, input)
	})
	if err != nil {
		return SelectionResult{}, err
	}
	resolved, ok := result.Value.(SelectionResult)
	if !ok {
		switch committed := result.Value.(type) {
		case selectionOperationResult:
			resolved = committed.Selection
		case json.RawMessage:
			var replay selectionOperationResult
			if json.Unmarshal(committed, &replay) != nil {
				return SelectionResult{}, fmt.Errorf("committed selection result has an invalid type")
			}
			resolved = replay.Selection
		default:
			return SelectionResult{}, fmt.Errorf("committed selection result has an invalid type")
		}
	}
	resolved.Head, resolved.Changed = result.Head, result.Changed
	return resolved, nil
}

func selectAcceptedDecision(snapshot Snapshot, input SelectionInput, operationID string) (TransactionDecision, error) {
	if err := validateActiveCatalogModule(snapshot, input.ModuleID); err != nil {
		return TransactionDecision{}, err
	}
	request, err := validateSelectionInput(snapshot, input)
	if err != nil {
		return TransactionDecision{}, fmt.Errorf("validate accepted selection input: %w", err)
	}
	pointerPath := selectionPointerPath(input.ModuleID, request.Work.AVMReleaseVersion, input.ContextID)
	sequence, retryResult, err := selectionSequence(snapshot, pointerPath, "", input, request)
	if err != nil {
		return TransactionDecision{}, err
	}
	if retryResult != nil {
		return TransactionDecision{Noop: true, Result: *retryResult}, nil
	}
	receipt, err := buildSelectionReceipt(input, request, sequence)
	if err != nil {
		return TransactionDecision{}, err
	}
	if err = receipt.Validate(); err != nil {
		return TransactionDecision{}, err
	}
	selectionPath := selectionStatePrefix + input.ModuleID + "/" + input.ContextID + "/" + receipt.ID + ".json"
	export := SelectionExport{SchemaVersion: selectionRecordSchemaVersion, RecordType: selectionExportRecordType, ID: receipt.ID, SelectionID: receipt.ID, ModuleID: input.ModuleID, ContextID: input.ContextID, Status: ExportPending, Receipt: receipt, CreatedAt: input.SelectedAt.UTC()}
	pointer := SelectionPointerRecord{SchemaVersion: selectionRecordSchemaVersion, ModuleID: input.ModuleID, AVMReleaseVersion: receipt.AVMReleaseVersion, ContextID: input.ContextID, Sequence: sequence, SelectionID: receipt.ID, RequestID: input.RequestID, CompletionID: input.CompletionID, DesiredGeneration: request.Work.DesiredGeneration, UpdatedAt: input.SelectedAt.UTC()}
	events, err := selectionEvents(selectionPath, pointerPath, receipt, pointer, export)
	if err != nil {
		return TransactionDecision{}, err
	}
	projectionEvents, err := selectionProjectionEvents(snapshot, request, receipt, operationID, input)
	if err != nil {
		return TransactionDecision{}, fmt.Errorf("build selected module projection: %w", err)
	}
	events = append(events, projectionEvents...)
	if releaseEvent, released, releaseErr := selectionTimeoutBarrierReleaseEvent(snapshot, input); releaseErr != nil {
		return TransactionDecision{}, fmt.Errorf("build exact selection predecessor timeout release: %w", releaseErr)
	} else if released {
		events = append(events, releaseEvent)
	}
	return TransactionDecision{ReadSet: TransactionReadSet{Owners: []string{input.ModuleID}}, Events: events, Result: SelectionResult{Receipt: receipt, Export: export, Changed: true}, Candidate: CandidateOptions{Subject: "state(selection): select accepted analysis", Provenance: GitProvenance{Operation: selectionExportOperation, Event: operationID, Module: input.ModuleID, Request: input.RequestID}}}, nil
}

func selectionTimeoutBarrierReleaseEvent(snapshot Snapshot, input SelectionInput) (Event, bool, error) {
	handoffPath, err := identity.StateProjectionHandoffPath(input.ModuleID, input.AcceptanceHandoffID)
	if err != nil {
		return Event{}, false, err
	}
	record, exists := snapshot.Records[handoffPath]
	if !exists || record.Kind != "stage_handoff" || record.SchemaVersion != 1 {
		return Event{}, false, ErrSelectionIneligible
	}
	var handoff projection.Handoff
	if json.Unmarshal(record.Data, &handoff) != nil || handoff.Validate() != nil || handoff.ID != input.AcceptanceHandoffID || handoff.ModuleID != input.ModuleID || handoff.ConsumerStage != "selection" {
		return Event{}, false, ErrSelectionIneligible
	}
	acknowledgementPath, err := identity.StateProjectionAcknowledgementPath(input.ModuleID, handoff.Projection.IntentID)
	if err != nil {
		return Event{}, false, err
	}
	if acknowledgementRecord, present := snapshot.Records[acknowledgementPath]; present && acknowledgementRecord.Kind == "projection_acknowledgement" {
		var acknowledgement projection.Acknowledgement
		if json.Unmarshal(acknowledgementRecord.Data, &acknowledgement) == nil && acknowledgement.Validate(handoff.Projection, input.SelectedAt) == nil {
			return Event{}, false, nil
		}
	}
	releasePath, err := identity.StateProjectionBarrierReleasePath(input.ModuleID, handoff.Projection.IntentID)
	if err != nil {
		return Event{}, false, err
	}
	if releaseRecord, present := snapshot.Records[releasePath]; present {
		var release projection.BarrierRelease
		if releaseRecord.Kind != "projection_barrier_release" || json.Unmarshal(releaseRecord.Data, &release) != nil || release.Validate(handoff.Projection, handoff.BarrierDeadline, input.SelectedAt) != nil {
			return Event{}, false, ErrSelectionIneligible
		}
		return Event{}, false, nil
	}
	if input.SelectedAt.Before(handoff.BarrierDeadline) {
		return Event{}, false, nil
	}
	release := projection.BarrierRelease{
		SchemaVersion: projection.SchemaVersion, Projection: handoff.Projection,
		Status: projection.BarrierReleased, Deadline: handoff.BarrierDeadline,
		ReleasedAt: input.SelectedAt.UTC(),
	}
	if err := release.Validate(handoff.Projection, handoff.BarrierDeadline, input.SelectedAt); err != nil {
		return Event{}, false, err
	}
	event, err := evidenceStateEvent(releasePath, "projection_barrier_release", release)
	if err != nil {
		return Event{}, false, err
	}
	return event, true, nil
}

func sameSelectionRequest(receipt SelectionReceipt, input SelectionInput) bool {
	return receipt.ModuleID == input.ModuleID && receipt.ContextID == input.ContextID && receipt.CompletionID != nil && *receipt.CompletionID == input.CompletionID && receipt.AnalysisID != nil && *receipt.AnalysisID == input.AnalysisID && receipt.EvidenceCommit == input.EvidenceCommit
}

func selectionProjectionEvents(snapshot Snapshot, request selectionRequest, receipt SelectionReceipt, operationID string, input SelectionInput) ([]Event, error) {
	slotPath, err := identity.StateLifecycleSlotPath(input.ModuleID)
	if err != nil {
		return nil, err
	}
	var slot struct {
		SchemaVersion     int    `json:"schema_version"`
		ModuleID          string `json:"module_id"`
		ReleaseVersion    string `json:"release_version"`
		DesiredGeneration uint64 `json:"desired_generation"`
		Status            string `json:"status"`
	}
	if record, exists := snapshot.Records[slotPath]; !exists || record.Kind != "lifecycle_slot" || record.SchemaVersion != 1 || json.Unmarshal(record.Data, &slot) != nil || slot.SchemaVersion != 1 || slot.ModuleID != input.ModuleID || slot.ReleaseVersion != receipt.AVMReleaseVersion || slot.Status != "active" || slot.DesiredGeneration == 0 {
		return nil, ErrSelectionIneligible
	}
	set, err := acceptedRequiredContextSet(snapshot, request)
	if err != nil {
		return nil, err
	}
	selected, required, unresolved, err := selectedContextCoverage(snapshot, set, receipt)
	if err != nil {
		return nil, err
	}
	status, reason, err := selectionLifecycleStatus(snapshot, set, receipt, selected, required, unresolved)
	if err != nil {
		return nil, err
	}
	transitionID := "selection:" + receipt.ID
	coverageState := "complete"
	if selected < required || unresolved != 0 {
		coverageState = "partial"
	}
	projectionGeneration, err := ModuleProjectionGeneration(snapshot, input.ModuleID, slot.DesiredGeneration)
	if err != nil {
		return nil, err
	}
	fields := []projection.OwnedField{
		{Name: "avm_release_version", Value: receipt.AVMReleaseVersion},
		{Name: "source_version", Value: receipt.AVMReleaseVersion},
		{Name: "source_sha", Value: request.SourceSHA},
		{Name: "context_id", Value: receipt.ContextID},
		{Name: "selection_id", Value: receipt.ID},
		{Name: "selected_contexts", Value: fmt.Sprint(selected)},
		{Name: "required_contexts", Value: fmt.Sprint(required)},
		{Name: "unresolved_contexts", Value: fmt.Sprint(unresolved)},
		{Name: "coverage_state", Value: coverageState},
		{Name: "status_reason", Value: reason},
	}
	intent, err := projection.NewDesiredIntent(input.ModuleID, projectionGeneration, transitionID, status, fields, nil)
	if err != nil {
		return nil, err
	}
	intentPath, err := identity.StateProjectionIntentPath(input.ModuleID)
	if err != nil {
		return nil, err
	}
	if record, exists := snapshot.Records[intentPath]; exists {
		var current projection.DesiredIntent
		if record.Kind != "projection_intent" || json.Unmarshal(record.Data, &current) != nil || current.ValidatePath(intentPath) != nil || current.Generation > intent.Generation {
			return nil, ErrSelectionStale
		}
	}
	intentData, err := json.Marshal(intent)
	if err != nil {
		return nil, err
	}
	intentEvent := Event{ID: intent.IntentID, RecordPath: intentPath, Record: Record{Kind: "projection_intent", SchemaVersion: projection.SchemaVersion, Data: intentData}}
	projectionIdentity, err := intent.Identity()
	if err != nil {
		return nil, err
	}
	fingerprint, err := identity.ID("input-fingerprint", 1, request.Fingerprint)
	if err != nil {
		return nil, err
	}
	handoff, err := projection.NewHandoff(projection.Handoff{
		ModuleID: input.ModuleID, Generation: slot.DesiredGeneration, TransitionID: transitionID,
		InputFingerprint: fingerprint, OperationID: operationID, AcceptedFactsID: request.Work.FactsID,
		Producer: input.Producer, ConsumerStage: "publication", Projection: projectionIdentity,
	}, input.SelectedAt)
	if err != nil {
		return nil, err
	}
	handoffPath, err := identity.StateProjectionHandoffPath(input.ModuleID, handoff.ID)
	if err != nil {
		return nil, err
	}
	handoffData, err := json.Marshal(handoff)
	if err != nil {
		return nil, err
	}
	return []Event{intentEvent, {ID: handoff.ID, RecordPath: handoffPath, Record: Record{Kind: "stage_handoff", SchemaVersion: projection.SchemaVersion, Data: handoffData}}}, nil
}

// selectionLifecycleStatus reduces the complete accepted context set together
// with current requests. A selection must not hide another required context
// that is still queued, running, or awaiting review.
func selectionLifecycleStatus(snapshot Snapshot, set contextset.Set, receipt SelectionReceipt, selected, required, unresolved int) (string, string, error) {
	priority := map[string]int{"needs-review": 0, "blocked": 1, "failed": 2, "backlog": 3, "analyzing": 4, "publishing": 5}
	status, reason := "publishing", "accepted selections are ready for site publication"
	consider := func(candidate, candidateReason string) {
		if priority[candidate] < priority[status] {
			status, reason = candidate, candidateReason
		}
	}
	for _, requiredContext := range set.Contexts {
		state := requiredContext.State
		if state.Applicability == contextset.ApplicabilityNotApplicable || state.Candidate == contextset.CandidateIneligible {
			continue
		}
		if state.Applicability == contextset.ApplicabilityUnresolved || state.Candidate == contextset.CandidateUnresolved {
			consider("blocked", "required assessment context has unresolved accepted inputs")
			continue
		}
		request, err := latestCurrentAssessment(snapshot, receipt.ModuleID, receipt.AVMReleaseVersion, state.ContextID)
		if errors.Is(err, ErrDeploymentNotCurrent) {
			consider("backlog", "required assessment context has no current request")
			continue
		}
		if err != nil {
			return "", "", err
		}
		switch request.Status {
		case "queued":
			consider("backlog", "required assessment context is queued")
		case "claimed", "running":
			consider("analyzing", "required assessment context is active")
		case "awaiting_acceptance":
			consider("needs-review", "required assessment context is awaiting evidence acceptance")
		case "failed":
			consider("failed", "required assessment context has a retryable failure")
		case "facts_accepted":
			consider("blocked", "accepted facts are waiting for assessment inputs")
		case "accepted":
			if state.ContextID == receipt.ContextID {
				continue
			}
			pointer, exists, pointerErr := readSelectionPointer(snapshot, selectionPointerPath(receipt.ModuleID, receipt.AVMReleaseVersion, state.ContextID))
			if pointerErr != nil {
				return "", "", pointerErr
			}
			if !exists || pointer.DesiredGeneration != request.Work.DesiredGeneration || pointer.RequestID != request.ID || pointer.AVMReleaseVersion != receipt.AVMReleaseVersion {
				consider("publishing", "accepted assessment context is waiting for selection")
			}
		default:
			return "", "", fmt.Errorf("required assessment context has unsupported request status %q", request.Status)
		}
	}
	if status == "publishing" && (selected < required || unresolved != 0) {
		reason = "partial accepted context coverage is ready for site publication"
	}
	return status, reason, nil
}

func selectedContextCoverage(snapshot Snapshot, set contextset.Set, current SelectionReceipt) (selected, required, unresolved int, err error) {
	for _, context := range set.Contexts {
		if context.State.Applicability == contextset.ApplicabilityNotApplicable || context.State.Candidate == contextset.CandidateIneligible {
			continue
		}
		required++
		if context.State.Applicability == contextset.ApplicabilityUnresolved || context.State.Candidate == contextset.CandidateUnresolved {
			unresolved++
			continue
		}
		if context.State.ContextID == current.ContextID {
			selected++
			continue
		}
		pointer, exists, readErr := readSelectionPointer(snapshot, selectionPointerPath(current.ModuleID, current.AVMReleaseVersion, context.State.ContextID))
		if readErr != nil {
			return 0, 0, 0, readErr
		}
		if !exists || pointer.AVMReleaseVersion != current.AVMReleaseVersion {
			continue
		}
		prior, readErr := readSelectionReceipt(snapshot, current.ModuleID, context.State.ContextID, pointer.SelectionID)
		if readErr != nil {
			return 0, 0, 0, readErr
		}
		request, readErr := latestCurrentAssessment(snapshot, current.ModuleID, current.AVMReleaseVersion, context.State.ContextID)
		if errors.Is(readErr, ErrDeploymentNotCurrent) {
			continue
		}
		if readErr != nil {
			return 0, 0, 0, readErr
		}
		if request.Status == "accepted" && request.ID == pointer.RequestID && request.Work.DesiredGeneration == pointer.DesiredGeneration && request.Fingerprint == prior.InputFingerprint && prior.AVMReleaseVersion == current.AVMReleaseVersion && prior.SourceSHA != nil && *prior.SourceSHA == set.SourceSHA && prior.FactsID != nil && *prior.FactsID == set.FactsID && prior.AnalysisID != nil {
			selected++
		}
	}
	return selected, required, unresolved, nil
}

func selectionOperationID(kind, moduleID, contextID, requestID, completionID string, at time.Time) (string, error) {
	return identity.ID("selection-operation", 1, struct {
		Kind         string    `json:"kind"`
		ModuleID     string    `json:"module_id"`
		ContextID    string    `json:"context_id"`
		RequestID    string    `json:"request_id"`
		CompletionID string    `json:"completion_id,omitempty"`
		At           time.Time `json:"at"`
	}{kind, moduleID, contextID, requestID, completionID, at.UTC()})
}

func resolveSelectionResult(snapshot Snapshot, input SelectionInput) (TransactionResolution, error) {
	receipt, export, found, err := findSelectionByOperation(snapshot, input)
	if err != nil || !found {
		return TransactionResolution{}, err
	}
	return TransactionResolution{Found: true, Value: SelectionResult{Receipt: receipt, Export: export}}, nil
}

func findSelectionByOperation(snapshot Snapshot, input SelectionInput) (SelectionReceipt, SelectionExport, bool, error) {
	for selectionPath, record := range snapshot.Records {
		if record.Kind != "selection" || !strings.HasPrefix(selectionPath, selectionStatePrefix) || strings.HasSuffix(selectionPath, selectionCurrentSuffix) {
			continue
		}
		var receipt SelectionReceipt
		if err := json.Unmarshal(record.Data, &receipt); err != nil {
			return SelectionReceipt{}, SelectionExport{}, false, err
		}
		if err := receipt.Validate(); err != nil {
			return SelectionReceipt{}, SelectionExport{}, false, err
		}
		if receipt.ModuleID != input.ModuleID || receipt.ContextID != input.ContextID || receipt.CompletionID == nil || *receipt.CompletionID != input.CompletionID {
			continue
		}
		if receipt.AnalysisID == nil || *receipt.AnalysisID != input.AnalysisID || receipt.EvidenceCommit != input.EvidenceCommit {
			return SelectionReceipt{}, SelectionExport{}, false, ErrTransactionIdentityConflict
		}
		exportRecord, exists := snapshot.Records[selectionExportPrefix+receipt.ID+".json"]
		if !exists || exportRecord.Kind != "export" {
			return SelectionReceipt{}, SelectionExport{}, false, ErrSelectionStateMissing
		}
		var export SelectionExport
		if err := json.Unmarshal(exportRecord.Data, &export); err != nil {
			return SelectionReceipt{}, SelectionExport{}, false, err
		}
		if err := validateSelectionExport(export); err != nil {
			return SelectionReceipt{}, SelectionExport{}, false, err
		}
		return receipt, export, true, nil
	}
	return SelectionReceipt{}, SelectionExport{}, false, nil
}

func selectionSequence(snapshot Snapshot, pointerPath, head string, input SelectionInput, request selectionRequest) (uint64, *SelectionResult, error) {
	previous, exists, err := readSelectionPointer(snapshot, pointerPath)
	if err != nil || !exists {
		return 1, nil, err
	}
	if previous.RequestID == input.RequestID && previous.CompletionID == input.CompletionID {
		result, retryErr := existingSelectionResult(head, snapshot, previous, input)
		return 0, &result, retryErr
	}
	if request.Work.DesiredGeneration <= previous.DesiredGeneration {
		return 0, nil, ErrSelectionStale
	}
	if previous.Sequence == math.MaxUint64 {
		return 0, nil, fmt.Errorf("selection sequence is exhausted")
	}
	return previous.Sequence + 1, nil, nil
}

// DeselectRetired emits a higher-sequence null selection for a retired module.
// It never reuses or rewrites the accepted analysis history.
func DeselectRetired(ctx context.Context, coordinator TransactionCoordinator, input DeselectRetiredInput) (SelectionResult, error) {
	if err := validateDeselectRetiredInput(input); err != nil {
		return SelectionResult{}, err
	}
	if coordinator == nil {
		return SelectionResult{}, fmt.Errorf("state transaction coordinator is required")
	}
	operationID, err := selectionOperationID("deselect", input.ModuleID, input.ContextID, "", "", input.SelectedAt)
	if err != nil {
		return SelectionResult{}, err
	}
	result, err := coordinator.RunTransaction(ctx, operationID, func(snapshot Snapshot) (TransactionDecision, error) {
		return deselectRetiredDecision(snapshot, input, operationID)
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		return resolveDeselectResult(snapshot, input)
	})
	if err != nil {
		return SelectionResult{}, err
	}
	resolved, ok := result.Value.(SelectionResult)
	if !ok {
		return SelectionResult{}, fmt.Errorf("retirement deselection result has an invalid type")
	}
	resolved.Head, resolved.Changed = result.Head, result.Changed
	return resolved, nil
}

func deselectRetiredDecision(snapshot Snapshot, input DeselectRetiredInput, operationID string) (TransactionDecision, error) {
	if err := validateRetiredCatalogModule(snapshot, input.ModuleID); err != nil {
		return TransactionDecision{}, err
	}
	pointerPath := selectionPointerPath(input.ModuleID, input.AVMReleaseVersion, input.ContextID)
	previous, exists, err := readSelectionPointer(snapshot, pointerPath)
	if err != nil {
		return TransactionDecision{}, err
	}
	if !exists {
		return TransactionDecision{}, ErrSelectionNotFound
	}
	previousReceipt, err := readSelectionReceipt(snapshot, input.ModuleID, input.ContextID, previous.SelectionID)
	if err != nil {
		return TransactionDecision{}, err
	}
	if previousReceipt.AnalysisID == nil && sameDeselectRetiredRequest(previousReceipt, input) {
		existing, resultErr := existingDeselectionResult("", snapshot, previous, previousReceipt)
		return TransactionDecision{Noop: true, Result: existing}, resultErr
	}
	if input.DesiredGeneration <= previous.DesiredGeneration {
		return TransactionDecision{}, ErrSelectionStale
	}
	if previous.Sequence == math.MaxUint64 {
		return TransactionDecision{}, fmt.Errorf("selection sequence is exhausted")
	}
	receipt := SelectionReceipt{
		SchemaVersion: selectionSchemaVersion, RecordType: selectionRecordType,
		ModuleID: input.ModuleID, AVMReleaseVersion: input.AVMReleaseVersion, ContextID: input.ContextID,
		Sequence: previous.Sequence + 1, DesiredGeneration: input.DesiredGeneration,
		InputFingerprint: input.InputFingerprint, EvidenceCommit: input.EvidenceCommit,
		SelectedAt: input.SelectedAt.UTC(),
	}
	receipt.ID, err = identity.ID("selection", selectionSchemaVersion, selectionIdentity{
		SchemaVersion: receipt.SchemaVersion, RecordType: receipt.RecordType, ModuleID: receipt.ModuleID, AVMReleaseVersion: receipt.AVMReleaseVersion,
		ContextID: receipt.ContextID, Sequence: receipt.Sequence, DesiredGeneration: receipt.DesiredGeneration,
		SourceSHA: nil, FactsID: nil, InputFingerprint: receipt.InputFingerprint,
		AnalysisID: nil, EvidenceCommit: receipt.EvidenceCommit, SelectedAt: receipt.SelectedAt,
	})
	if err != nil {
		return TransactionDecision{}, err
	}
	if err = receipt.Validate(); err != nil {
		return TransactionDecision{}, err
	}
	pointer := SelectionPointerRecord{
		SchemaVersion: selectionRecordSchemaVersion, ModuleID: input.ModuleID, AVMReleaseVersion: input.AVMReleaseVersion,
		ContextID: input.ContextID, Sequence: receipt.Sequence, SelectionID: receipt.ID,
		DesiredGeneration: input.DesiredGeneration, UpdatedAt: input.SelectedAt.UTC(),
	}
	export := SelectionExport{
		SchemaVersion: selectionRecordSchemaVersion, RecordType: selectionExportRecordType,
		ID: receipt.ID, SelectionID: receipt.ID, ModuleID: input.ModuleID,
		ContextID: input.ContextID, Status: ExportPending, Receipt: receipt, CreatedAt: input.SelectedAt.UTC(),
	}
	receiptPath := selectionStatePrefix + input.ModuleID + "/" + input.ContextID + "/" + receipt.ID + ".json"
	events, err := selectionEvents(receiptPath, pointerPath, receipt, pointer, export)
	if err != nil {
		return TransactionDecision{}, err
	}
	for index := range events {
		if events[index].RecordPath == receiptPath {
			events[index].ID = operationID
			break
		}
	}
	return TransactionDecision{Events: events, Result: SelectionResult{Receipt: receipt, Export: export, Changed: true}, Candidate: CandidateOptions{Subject: "state(selection): deselect retired module", Provenance: GitProvenance{Operation: selectionExportOperation, Event: operationID, Module: input.ModuleID}}}, nil
}

func validateDeselectRetiredInput(input DeselectRetiredInput) error {
	if !selectionModulePattern.MatchString(input.ModuleID) || !selectionReleasePattern.MatchString(input.AVMReleaseVersion) || identity.ValidateRelativePath(input.ContextID, false) != nil || input.DesiredGeneration == 0 || !selectionSHA256Pattern.MatchString(input.InputFingerprint) || !selectionSHA1Pattern.MatchString(input.EvidenceCommit) || input.SelectedAt.IsZero() {
		return fmt.Errorf("retirement deselection identity is invalid")
	}
	return nil
}

func sameDeselectRetiredRequest(receipt SelectionReceipt, input DeselectRetiredInput) bool {
	return receipt.ModuleID == input.ModuleID && receipt.AVMReleaseVersion == input.AVMReleaseVersion && receipt.ContextID == input.ContextID && receipt.DesiredGeneration == input.DesiredGeneration && receipt.InputFingerprint == input.InputFingerprint && receipt.EvidenceCommit == input.EvidenceCommit
}

func resolveDeselectResult(snapshot Snapshot, input DeselectRetiredInput) (TransactionResolution, error) {
	pointer, exists, err := readSelectionPointer(snapshot, selectionPointerPath(input.ModuleID, input.AVMReleaseVersion, input.ContextID))
	if err != nil || !exists {
		return TransactionResolution{}, err
	}
	receipt, err := readSelectionReceipt(snapshot, input.ModuleID, input.ContextID, pointer.SelectionID)
	if err != nil {
		return TransactionResolution{}, err
	}
	result, err := existingDeselectionResult("", snapshot, pointer, receipt)
	return TransactionResolution{Found: err == nil, Value: result}, err
}

func validateRetiredCatalogModule(snapshot Snapshot, moduleID string) error {
	record, exists := snapshot.Records["state/discovery/catalog.json"]
	if !exists || record.Kind != "discovery_catalog" || record.SchemaVersion != 1 {
		return ErrSelectionIneligible
	}
	var durable struct {
		SchemaVersion int                   `json:"schema_version"`
		Accepted      catalog.AcceptedState `json:"accepted"`
	}
	if err := json.Unmarshal(record.Data, &durable); err != nil {
		return fmt.Errorf("decode accepted discovery catalog: %w", err)
	}
	if durable.SchemaVersion != 1 || durable.Accepted.Validate() != nil {
		return ErrSelectionIneligible
	}
	for _, module := range durable.Accepted.Modules {
		if module.Module.ID == moduleID && !module.Active {
			return nil
		}
		if module.Module.ID == moduleID {
			return ErrSelectionIneligible
		}
	}
	return ErrSelectionIneligible
}

func existingDeselectionResult(head string, snapshot Snapshot, pointer SelectionPointerRecord, receipt SelectionReceipt) (SelectionResult, error) {
	if err := receipt.Validate(); err != nil {
		return SelectionResult{}, err
	}
	exportPath := selectionExportPrefix + receipt.ID + ".json"
	record, exists := snapshot.Records[exportPath]
	if !exists || record.Kind != "export" || record.SchemaVersion != selectionRecordSchemaVersion {
		return SelectionResult{}, ErrSelectionStateMissing
	}
	var export SelectionExport
	if err := json.Unmarshal(record.Data, &export); err != nil {
		return SelectionResult{}, err
	}
	if err := validateSelectionExport(export); err != nil {
		return SelectionResult{}, err
	}
	if pointer.SelectionID != receipt.ID || export.SelectionID != receipt.ID {
		return SelectionResult{}, fmt.Errorf("retirement selection export does not match its receipt")
	}
	return SelectionResult{Head: head, Receipt: receipt, Export: export}, nil
}

// ImportSelectionReceipt marks an existing outbox entry merged after verifying
// the canonical receipt. It never selects again or advances the context sequence.
func ImportSelectionReceipt(ctx context.Context, coordinator TransactionCoordinator, receipt SelectionReceipt, mergedCommit string, importedAt time.Time) (SelectionResult, error) {
	if coordinator == nil || !selectionSHA1Pattern.MatchString(mergedCommit) || importedAt.IsZero() {
		return SelectionResult{}, fmt.Errorf("selection merge identity and import timestamp are required")
	}
	if err := receipt.Validate(); err != nil {
		return SelectionResult{}, err
	}
	operationID, err := identity.ID(selectionExportImportKind, 1, struct {
		SelectionID  string    `json:"selection_id"`
		MergedCommit string    `json:"merged_commit"`
		ImportedAt   time.Time `json:"imported_at"`
	}{receipt.ID, mergedCommit, importedAt.UTC()})
	if err != nil {
		return SelectionResult{}, err
	}
	result, err := coordinator.RunTransaction(ctx, operationID, func(snapshot Snapshot) (TransactionDecision, error) {
		return selectionExportImportDecision(snapshot, receipt, mergedCommit, importedAt, operationID)
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		return resolveSelectionExportImport(snapshot, receipt)
	})
	if err != nil {
		return SelectionResult{}, err
	}
	resolved, ok := result.Value.(SelectionResult)
	if !ok {
		return SelectionResult{}, fmt.Errorf("selection export import result has an invalid type")
	}
	resolved.Head, resolved.Changed = result.Head, result.Changed
	return resolved, nil
}

func selectionExportImportDecision(snapshot Snapshot, receipt SelectionReceipt, mergedCommit string, importedAt time.Time, operationID string) (TransactionDecision, error) {
	exportPath := selectionExportPrefix + receipt.ID + ".json"
	export, err := readSelectionExport(snapshot, exportPath, receipt)
	if err != nil {
		return TransactionDecision{}, err
	}
	if export.Status == ExportMerged {
		if err := validateMergedSelectionImport(export, mergedCommit, importedAt); err != nil {
			return TransactionDecision{}, err
		}
		return TransactionDecision{Noop: true, Result: SelectionResult{Receipt: export.Receipt, Export: export}}, nil
	}
	export.Status, export.MergedCommit, export.MergedAt = ExportMerged, mergedCommit, importedAt.UTC()
	event, err := selectionExportEvent(exportPath, export)
	if err != nil {
		return TransactionDecision{}, err
	}
	event.ID = operationID
	return TransactionDecision{Events: []Event{event}, Result: SelectionResult{Receipt: export.Receipt, Export: export, Changed: true}, Candidate: CandidateOptions{Subject: "state(selection): acknowledge merged export", Provenance: GitProvenance{Operation: selectionExportOperation, Event: operationID, Module: receipt.ModuleID}}}, nil
}

func validateMergedSelectionImport(export SelectionExport, mergedCommit string, importedAt time.Time) error {
	if export.MergedCommit != mergedCommit || !export.MergedAt.Equal(importedAt.UTC()) {
		return ErrTransactionIdentityConflict
	}
	return nil
}

func resolveSelectionExportImport(snapshot Snapshot, receipt SelectionReceipt) (TransactionResolution, error) {
	export, err := readSelectionExport(snapshot, selectionExportPrefix+receipt.ID+".json", receipt)
	if err != nil || export.Status != ExportMerged {
		return TransactionResolution{}, err
	}
	return TransactionResolution{Found: true, Value: SelectionResult{Receipt: export.Receipt, Export: export}}, nil
}

func readSelectionExport(snapshot Snapshot, exportPath string, receipt SelectionReceipt) (SelectionExport, error) {
	record, exists := snapshot.Records[exportPath]
	if !exists {
		return SelectionExport{}, ErrSelectionStateMissing
	}
	if record.Kind != "export" || record.SchemaVersion != selectionRecordSchemaVersion {
		return SelectionExport{}, fmt.Errorf("selection export has unsupported record identity")
	}
	var export SelectionExport
	if err := json.Unmarshal(record.Data, &export); err != nil {
		return SelectionExport{}, fmt.Errorf("decode selection export: %w", err)
	}
	if err := validateSelectionExport(export); err != nil {
		return SelectionExport{}, err
	}
	if export.SelectionID != receipt.ID || !sameSelectionReceipt(export.Receipt, receipt) {
		return SelectionExport{}, fmt.Errorf("imported selection receipt does not match its durable export")
	}
	if export.Status != ExportPending && export.Status != ExportMerged {
		return SelectionExport{}, fmt.Errorf("selection export has unsupported status %q", export.Status)
	}
	return export, nil
}

func selectionExportEvent(exportPath string, export SelectionExport) (Event, error) {
	data, err := json.Marshal(export)
	if err != nil {
		return Event{}, fmt.Errorf("encode merged selection export: %w", err)
	}
	record := Record{Kind: "export", SchemaVersion: selectionRecordSchemaVersion, Data: data}
	eventID, err := identity.ID(selectionStateEventKind, 1, selectionEventIdentity{Path: exportPath, Record: record})
	if err != nil {
		return Event{}, fmt.Errorf("identify merged selection export: %w", err)
	}
	return Event{ID: eventID, RecordPath: exportPath, Record: record}, nil
}

// PendingSelectionExports returns deterministic retry work without generating
// new receipts or analysis. A current pointer without its outbox is a review gate.
func PendingSelectionExports(snapshot Snapshot) ([]SelectionExport, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	exports, err := readSelectionExports(snapshot)
	if err != nil {
		return nil, err
	}
	if err = validateCurrentSelectionExports(snapshot, exports); err != nil {
		return nil, err
	}
	pending := make([]SelectionExport, 0, len(exports))
	for _, export := range exports {
		if export.Status == ExportPending {
			pending = append(pending, export)
		}
	}
	sort.Slice(pending, func(left, right int) bool { return pending[left].SelectionID < pending[right].SelectionID })
	return pending, nil
}

func readSelectionExports(snapshot Snapshot) (map[string]SelectionExport, error) {
	exports := make(map[string]SelectionExport)
	for recordPath, record := range snapshot.Records {
		if record.Kind != "export" {
			continue
		}
		var envelope struct {
			RecordType string `json:"record_type"`
		}
		if err := json.Unmarshal(record.Data, &envelope); err != nil {
			return nil, fmt.Errorf("decode export record %s: %w", recordPath, err)
		}
		if envelope.RecordType != selectionExportRecordType {
			continue
		}
		var export SelectionExport
		if err := json.Unmarshal(record.Data, &export); err != nil {
			return nil, fmt.Errorf("decode selection export %s: %w", recordPath, err)
		}
		if err := validateSelectionExport(export); err != nil {
			return nil, err
		}
		if recordPath != selectionExportPrefix+export.ID+".json" {
			return nil, fmt.Errorf("selection export %s is stored at a noncanonical path", export.ID)
		}
		exports[export.ID] = export
	}
	return exports, nil
}

func validateCurrentSelectionExports(snapshot Snapshot, exports map[string]SelectionExport) error {
	for recordPath, record := range snapshot.Records {
		if record.Kind != "selection" || !strings.HasSuffix(recordPath, selectionCurrentSuffix) {
			continue
		}
		pointer, exists, err := readSelectionPointer(snapshot, recordPath)
		if err != nil || !exists {
			return fmt.Errorf("validate current selection pointer %s: %w", recordPath, err)
		}
		if _, exists := exports[pointer.SelectionID]; !exists {
			return ErrSelectionStateMissing
		}
	}
	return nil
}

func validateSelectionExport(export SelectionExport) error {
	if export.SchemaVersion != selectionRecordSchemaVersion || export.RecordType != selectionExportRecordType || export.ID == "" || export.ID != export.SelectionID || export.ModuleID != export.Receipt.ModuleID || export.ContextID != export.Receipt.ContextID || export.SelectionID != export.Receipt.ID || export.CreatedAt.IsZero() {
		return fmt.Errorf("selection export identity is invalid")
	}
	if err := export.Receipt.Validate(); err != nil {
		return err
	}
	switch export.Status {
	case ExportPending:
		if export.MergedCommit != "" || !export.MergedAt.IsZero() {
			return fmt.Errorf("pending selection export contains merge receipt data")
		}
	case ExportMerged:
		if !selectionSHA1Pattern.MatchString(export.MergedCommit) || export.MergedAt.IsZero() {
			return fmt.Errorf("merged selection export has incomplete receipt data")
		}
	default:
		return fmt.Errorf("selection export status %q is invalid", export.Status)
	}
	return nil
}

func sameSelectionReceipt(left, right SelectionReceipt) bool {
	leftData, leftErr := json.Marshal(left)
	rightData, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftData) == string(rightData)
}

// Validate checks the canonical selection identity and the accepted evidence
// binding independently of the writer.
func (receipt SelectionReceipt) Validate() error {
	if receipt.SchemaVersion != selectionSchemaVersion || receipt.RecordType != selectionRecordType || !selectionModulePattern.MatchString(receipt.ModuleID) || identity.ValidateRelativePath(receipt.ContextID, false) != nil || receipt.Sequence == 0 || receipt.DesiredGeneration == 0 || !selectionSHA256Pattern.MatchString(receipt.InputFingerprint) || !selectionSHA1Pattern.MatchString(receipt.EvidenceCommit) || receipt.SelectedAt.IsZero() {
		return fmt.Errorf("selection receipt identity is invalid")
	}
	if !selectionReleasePattern.MatchString(receipt.AVMReleaseVersion) {
		return fmt.Errorf("selection receipt AVM release version is invalid")
	}
	if err := identity.ValidateRecordID("selection", receipt.ID); err != nil {
		return err
	}
	if receipt.SourceSHA == nil || receipt.FactsID == nil || receipt.AnalysisID == nil {
		if receipt.SourceSHA != nil || receipt.FactsID != nil || receipt.AnalysisID != nil || receipt.CompletionID != nil {
			return fmt.Errorf("selection receipt has partial evidence identity")
		}
	} else if !selectionSHA1Pattern.MatchString(*receipt.SourceSHA) || identity.ValidateRecordID("facts", *receipt.FactsID) != nil || identity.ValidateRecordID("analysis", *receipt.AnalysisID) != nil || receipt.CompletionID == nil || identity.ValidateRecordID("completion", *receipt.CompletionID) != nil {
		return fmt.Errorf("selection receipt evidence identity is incomplete")
	}
	expectedID, err := identity.ID("selection", selectionSchemaVersion, selectionIdentity{
		SchemaVersion: receipt.SchemaVersion, RecordType: receipt.RecordType, ModuleID: receipt.ModuleID, AVMReleaseVersion: receipt.AVMReleaseVersion,
		ContextID: receipt.ContextID, Sequence: receipt.Sequence, DesiredGeneration: receipt.DesiredGeneration,
		SourceSHA: receipt.SourceSHA, FactsID: receipt.FactsID, InputFingerprint: receipt.InputFingerprint,
		AnalysisID: receipt.AnalysisID, EvidenceCommit: receipt.EvidenceCommit, SelectedAt: receipt.SelectedAt,
	})
	if err != nil {
		return err
	}
	if receipt.ID != expectedID {
		return fmt.Errorf("selection receipt ID does not match its content")
	}
	return nil
}

func validateSelectionInput(snapshot Snapshot, input SelectionInput) (selectionRequest, error) {
	if !selectionModulePattern.MatchString(input.ModuleID) || identity.ValidateRelativePath(input.ContextID, false) != nil || input.RequestID == "" || input.SelectedAt.IsZero() || !selectionSHA1Pattern.MatchString(input.EvidenceCommit) {
		return selectionRequest{}, fmt.Errorf("selection input is incomplete or invalid")
	}
	if err := identity.ValidateRecordID("completion", input.CompletionID); err != nil {
		return selectionRequest{}, err
	}
	if err := identity.ValidateRecordID("analysis", input.AnalysisID); err != nil {
		return selectionRequest{}, err
	}
	if err := validateActiveCatalogModule(snapshot, input.ModuleID); err != nil {
		return selectionRequest{}, err
	}
	requestPath := "state/requests/" + input.RequestID + ".json"
	requestRecord, exists := snapshot.Records[requestPath]
	if !exists || requestRecord.Kind != "request" || requestRecord.SchemaVersion != 1 {
		return selectionRequest{}, ErrSelectionIneligible
	}
	var request selectionRequest
	if err := json.Unmarshal(requestRecord.Data, &request); err != nil {
		return selectionRequest{}, fmt.Errorf("decode selected request: %w", err)
	}
	if request.ID != input.RequestID || request.ModuleID != input.ModuleID || request.Status != "accepted" || request.Work == nil || request.Work.Phase != "assess" || request.Work.ContextID != input.ContextID || !selectionReleasePattern.MatchString(request.Work.AVMReleaseVersion) || request.Work.Candidate != "eligible" || request.Work.Applicability != "applicable" || request.Work.Completion == nil {
		return selectionRequest{}, ErrSelectionIneligible
	}
	if err := validateSelectionLifecycleSlot(snapshot, request); err != nil {
		return selectionRequest{}, err
	}
	completion := request.Work.Completion
	if request.Fingerprint == "" || request.LastAcceptedFingerprint != request.Fingerprint || request.LastAcceptedSourceSHA != request.SourceSHA || completion.ID != input.CompletionID || completion.RequestID != request.ID || completion.InputFingerprint != request.Fingerprint || completion.DesiredGeneration != request.Work.DesiredGeneration || completion.Fence != request.Work.Fence || completion.Fence == 0 || completion.ValidationID == "" || !selectionSHA256Pattern.MatchString(completion.ArtifactDigest) || completion.SealedAt.IsZero() || input.SelectedAt.Before(completion.SealedAt) {
		return selectionRequest{}, ErrSelectionStale
	}
	if !selectionSHA1Pattern.MatchString(request.SourceSHA) || identity.ValidateRecordID("facts", request.Work.FactsID) != nil || !selectionSHA256Pattern.MatchString(request.Fingerprint) {
		return selectionRequest{}, fmt.Errorf("accepted request evidence identity is invalid")
	}
	if err := rejectNewerDesiredRequest(snapshot, request); err != nil {
		return selectionRequest{}, err
	}
	if err := validateCurrentSourcePin(snapshot, request); err != nil {
		return selectionRequest{}, err
	}
	if err := validateImportedInputs(snapshot, request); err != nil {
		return selectionRequest{}, err
	}
	if err := validateAcceptedEvidenceReceipt(snapshot, request, input); err != nil {
		return selectionRequest{}, err
	}
	requiredContexts, err := acceptedRequiredContextSet(snapshot, request)
	if err != nil {
		return selectionRequest{}, err
	}
	if !requiredContextSetAllows(requiredContexts, input.ContextID) {
		return selectionRequest{}, ErrSelectionIneligible
	}
	if err := validateSelectionPredecessor(snapshot, request, input); err != nil {
		return selectionRequest{}, err
	}
	return request, nil
}

func requiredContextSetAllows(set contextset.Set, contextID string) bool {
	for _, required := range set.Contexts {
		if required.State.ContextID != contextID {
			continue
		}
		return required.State.Applicability == contextset.ApplicabilityApplicable && required.State.Candidate == contextset.CandidateEligible
	}
	return false
}

func validateSelectionPredecessor(snapshot Snapshot, request selectionRequest, input SelectionInput) error {
	if identity.ValidateRecordID("stage-handoff", input.AcceptanceHandoffID) != nil {
		return fmt.Errorf("selection requires the exact accepted-evidence handoff")
	}
	handoffPath, err := identity.StateProjectionHandoffPath(input.ModuleID, input.AcceptanceHandoffID)
	if err != nil {
		return err
	}
	handoffRecord, exists := snapshot.Records[handoffPath]
	if !exists || handoffRecord.Kind != "stage_handoff" || handoffRecord.SchemaVersion != 1 {
		return ErrSelectionIneligible
	}
	var handoff projection.Handoff
	fingerprint, fingerprintErr := identity.ID("input-fingerprint", 1, request.Fingerprint)
	if fingerprintErr != nil {
		return fingerprintErr
	}
	if err = json.Unmarshal(handoffRecord.Data, &handoff); err != nil || handoff.Validate() != nil || handoff.ID != input.AcceptanceHandoffID ||
		handoff.ModuleID != input.ModuleID || handoff.Generation != request.Work.DesiredGeneration || handoff.ConsumerStage != "selection" ||
		handoff.AcceptedFactsID != request.Work.FactsID || handoff.InputFingerprint != fingerprint ||
		handoff.OperationID == "" || !strings.HasPrefix(handoff.OperationID, "evidence-acceptance:") || handoff.Producer.WorkflowID != "manual-accept-evidence" {
		return ErrSelectionIneligible
	}
	operationPath, err := identity.StateOperationReceiptPath(input.ModuleID, handoff.OperationID)
	if err != nil {
		return err
	}
	operationRecord, exists := snapshot.Records[operationPath]
	if !exists || operationRecord.Kind != "operation_receipt" || operationRecord.SchemaVersion != 1 || !appliedOperationReceipt(snapshot, handoff.OperationID, operationPath, operationRecord) {
		return ErrSelectionIneligible
	}
	var operation OperationReceipt
	if json.Unmarshal(operationRecord.Data, &operation) != nil || operation.Validate(operationPath) != nil || operation.ModuleID != input.ModuleID || operation.OperationID != handoff.OperationID {
		return ErrSelectionIneligible
	}
	var result evidenceAcceptanceOperationResult
	if json.Unmarshal(operation.Result, &result) != nil || result.Acceptance.Receipt.Validate() != nil || len(result.Events) == 0 {
		return ErrSelectionIneligible
	}
	accepted := result.Acceptance.Receipt
	if accepted.ModuleID != input.ModuleID || accepted.ContextID != input.ContextID || accepted.RequestID != request.ID ||
		accepted.CompletionID != input.CompletionID || accepted.Phase != "assess" || accepted.DesiredGeneration != request.Work.DesiredGeneration ||
		accepted.FactsID != request.Work.FactsID || accepted.AnalysisID != input.AnalysisID || accepted.InputFingerprint != request.Fingerprint ||
		accepted.EvidenceCommit != input.EvidenceCommit || handoff.OperationID != "evidence-acceptance:"+accepted.ID {
		return ErrSelectionIneligible
	}
	proof := false
	acceptanceReceiptProof := false
	for _, event := range result.Events {
		switch {
		case event.RecordPath == handoffPath && event.Record.Kind == "stage_handoff":
			encoded, marshalErr := json.Marshal(handoff)
			if marshalErr != nil || !bytes.Equal(event.Record.Data, encoded) || operationWriteSetHasEvent(snapshot, operation, event) != nil {
				return ErrSelectionIneligible
			}
			proof = true
		case event.RecordPath == evidenceAcceptancePrefix+accepted.ID+".json" && event.Record.Kind == "publication":
			var committed EvidenceAcceptanceReceipt
			if json.Unmarshal(event.Record.Data, &committed) != nil || committed != accepted || operationWriteSetHasEvent(snapshot, operation, event) != nil {
				return ErrSelectionIneligible
			}
			acceptanceReceiptProof = true
		}
	}
	if !proof || !acceptanceReceiptProof {
		return ErrSelectionIneligible
	}
	acknowledgementPath, err := identity.StateProjectionAcknowledgementPath(input.ModuleID, handoff.Projection.IntentID)
	if err != nil {
		return err
	}
	if record, ok := snapshot.Records[acknowledgementPath]; ok && record.Kind == "projection_acknowledgement" {
		var acknowledgement projection.Acknowledgement
		if json.Unmarshal(record.Data, &acknowledgement) == nil && acknowledgement.Validate(handoff.Projection, input.SelectedAt) == nil {
			return nil
		}
	}
	releasePath, err := identity.StateProjectionBarrierReleasePath(input.ModuleID, handoff.Projection.IntentID)
	if err != nil {
		return err
	}
	if record, ok := snapshot.Records[releasePath]; ok && record.Kind == "projection_barrier_release" {
		var release projection.BarrierRelease
		if json.Unmarshal(record.Data, &release) == nil && release.Validate(handoff.Projection, handoff.BarrierDeadline, input.SelectedAt) == nil {
			return nil
		}
	}
	if !input.SelectedAt.Before(handoff.BarrierDeadline) {
		return nil
	}
	return fmt.Errorf("%w: exact acceptance projection acknowledgement or durable timeout release is required", ErrSelectionBarrierPending)
}

func appliedOperationReceipt(snapshot Snapshot, operationID, path string, record Record) bool {
	expected, err := identity.ID("state-event", 1, Event{ID: operationID, RecordPath: path, Record: record})
	if err != nil {
		return false
	}
	digest := strings.TrimPrefix(expected, "state-event-")
	if snapshot.AppliedEvents[operationID] == digest {
		return true
	}
	for _, aggregate := range snapshot.Aggregates {
		if aggregate.AppliedEvents[operationID] == digest {
			return true
		}
	}
	return false
}

func operationWriteSetHasEvent(snapshot Snapshot, receipt OperationReceipt, event Event) error {
	digest, err := transactionEventDigest(event)
	if err != nil {
		return err
	}
	for _, entry := range receipt.WriteSet {
		if entry.EventID == event.ID {
			appliedDigest, applied := appliedEventDigest(snapshot, event.ID)
			current, present := snapshot.Records[event.RecordPath]
			if applied && appliedDigest == digest && present && current.Kind == event.Record.Kind && bytes.Equal(current.Data, event.Record.Data) && entry.RecordPath == event.RecordPath && entry.RecordKind == event.Record.Kind && entry.Digest == digest {
				return nil
			}
			return ErrTransactionIdentityConflict
		}
	}
	return ErrTransactionIdentityConflict
}

func validateSelectionLifecycleSlot(snapshot Snapshot, request selectionRequest) error {
	path, err := identity.StateLifecycleSlotPath(request.ModuleID)
	if err != nil {
		return err
	}
	record, exists := snapshot.Records[path]
	if !exists || record.Kind != "lifecycle_slot" || record.SchemaVersion != 1 {
		return ErrSelectionIneligible
	}
	var slot struct {
		SchemaVersion     int    `json:"schema_version"`
		ModuleID          string `json:"module_id"`
		ReleaseVersion    string `json:"release_version"`
		DesiredGeneration uint64 `json:"desired_generation"`
		Status            string `json:"status"`
	}
	if err = json.Unmarshal(record.Data, &slot); err != nil || slot.SchemaVersion != 1 || slot.ModuleID != request.ModuleID || slot.Status != "active" || slot.DesiredGeneration == 0 {
		return ErrSelectionIneligible
	}
	if slot.ReleaseVersion != request.Work.AVMReleaseVersion {
		return ErrSelectionStale
	}
	if slot.DesiredGeneration < request.Work.DesiredGeneration {
		return ErrSelectionStale
	}
	return nil
}

func validateCurrentSourcePin(snapshot Snapshot, request selectionRequest) error {
	path := "state/discovery/source-pins/" + request.ModuleID + ".json"
	record, exists := snapshot.Records[path]
	if !exists {
		return nil
	}
	if record.Kind != "source_pins" || record.SchemaVersion != 1 {
		return fmt.Errorf("current source pin record identity is invalid")
	}
	var history struct {
		SchemaVersion   int                           `json:"schema_version"`
		ModuleID        string                        `json:"module_id"`
		OverallLatest   *selectionSourcePin           `json:"overall_latest"`
		ReleaseHistory  []selectionSourcePin          `json:"release_history"`
		TrackCandidates map[string]selectionSourcePin `json:"track_candidates"`
	}
	if err := json.Unmarshal(record.Data, &history); err != nil {
		return fmt.Errorf("decode current source pin history: %w", err)
	}
	if history.SchemaVersion != 1 || history.ModuleID != request.ModuleID {
		return fmt.Errorf("current source pin history does not match selected module")
	}
	for _, pin := range history.ReleaseHistory {
		if pin.Version == request.Work.AVMReleaseVersion {
			if pin.Commit != request.SourceSHA {
				return ErrSelectionStale
			}
			return nil
		}
	}
	if history.OverallLatest != nil && history.OverallLatest.Version == request.Work.AVMReleaseVersion && history.OverallLatest.Commit == request.SourceSHA {
		return nil
	}
	return ErrSelectionStale
}

func validateImportedInputs(snapshot Snapshot, request selectionRequest) error {
	moduleState, err := readAssessmentModuleState(snapshot, request.ModuleID)
	if err != nil {
		return err
	}
	contextState, exists := moduleState.Versions[request.Work.AVMReleaseVersion][request.Work.ContextID]
	if !exists {
		return nil
	}
	if request.Work.DesiredGeneration < contextState.DesiredGeneration {
		return ErrSelectionStale
	}
	var requiredContextPolicyID string
	for _, imported := range contextState.Inputs {
		var present bool
		if imported.Kind == InputProviderSnapshot {
			present = containsSelectionString(request.Work.SnapshotIDs, imported.InputID)
		} else if imported.Kind == InputAssessmentPolicy {
			if requiredContextPolicyID == "" {
				required, requiredErr := acceptedRequiredContextSet(snapshot, request)
				if requiredErr != nil {
					return requiredErr
				}
				requiredContextPolicyID = required.PolicyID
			}
			present = imported.InputID == requiredContextPolicyID
		}
		if !present {
			return ErrSelectionStale
		}
	}
	return nil
}

func containsSelectionString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func validateActiveCatalogModule(snapshot Snapshot, moduleID string) error {
	record, exists := snapshot.Records["state/discovery/catalog.json"]
	if !exists || record.Kind != "discovery_catalog" || record.SchemaVersion != 1 {
		return ErrSelectionIneligible
	}
	var durable struct {
		SchemaVersion int                   `json:"schema_version"`
		Accepted      catalog.AcceptedState `json:"accepted"`
	}
	if err := json.Unmarshal(record.Data, &durable); err != nil {
		return fmt.Errorf("decode accepted discovery catalog: %w", err)
	}
	if durable.SchemaVersion != 1 || durable.Accepted.Validate() != nil {
		return ErrSelectionIneligible
	}
	for _, module := range durable.Accepted.Modules {
		if module.Module.ID == moduleID {
			if !module.Active {
				return ErrSelectionIneligible
			}
			return nil
		}
	}
	return ErrSelectionIneligible
}

func rejectNewerDesiredRequest(snapshot Snapshot, selected selectionRequest) error {
	for recordPath, record := range snapshot.Records {
		if record.Kind != "request" || recordPath == "state/requests/"+selected.ID+".json" {
			continue
		}
		var candidate selectionRequest
		if err := json.Unmarshal(record.Data, &candidate); err != nil {
			return fmt.Errorf("decode request %s while checking selection freshness: %w", recordPath, err)
		}
		if candidate.ModuleID != selected.ModuleID || candidate.Work == nil || candidate.Work.Phase != "assess" || candidate.Work.ContextID != selected.Work.ContextID {
			continue
		}
		if candidate.Work.DesiredGeneration > selected.Work.DesiredGeneration {
			return ErrSelectionStale
		}
		if candidate.Work.DesiredGeneration == selected.Work.DesiredGeneration && candidate.Fingerprint != selected.Fingerprint {
			return fmt.Errorf("assessment context reused a desired generation with different inputs")
		}
	}
	return nil
}

func buildSelectionReceipt(input SelectionInput, request selectionRequest, sequence uint64) (SelectionReceipt, error) {
	sourceSHA, factsID, analysisID := request.SourceSHA, request.Work.FactsID, input.AnalysisID
	completionID := input.CompletionID
	receipt := SelectionReceipt{
		SchemaVersion: selectionSchemaVersion, RecordType: selectionRecordType,
		ModuleID: input.ModuleID, AVMReleaseVersion: request.Work.AVMReleaseVersion, ContextID: input.ContextID, Sequence: sequence,
		DesiredGeneration: request.Work.DesiredGeneration, SourceSHA: &sourceSHA, FactsID: &factsID,
		InputFingerprint: request.Fingerprint, AnalysisID: &analysisID,
		CompletionID: &completionID, EvidenceCommit: input.EvidenceCommit, SelectedAt: input.SelectedAt.UTC(),
	}
	var err error
	receipt.ID, err = identity.ID("selection", selectionSchemaVersion, selectionIdentity{
		SchemaVersion: receipt.SchemaVersion, RecordType: receipt.RecordType, ModuleID: receipt.ModuleID, AVMReleaseVersion: receipt.AVMReleaseVersion,
		ContextID: receipt.ContextID, Sequence: receipt.Sequence, DesiredGeneration: receipt.DesiredGeneration,
		SourceSHA: receipt.SourceSHA, FactsID: receipt.FactsID, InputFingerprint: receipt.InputFingerprint,
		AnalysisID: receipt.AnalysisID, EvidenceCommit: receipt.EvidenceCommit, SelectedAt: receipt.SelectedAt,
	})
	if err != nil {
		return SelectionReceipt{}, err
	}
	return receipt, nil
}

func selectionEvents(receiptPath, pointerPath string, receipt SelectionReceipt, pointer SelectionPointerRecord, export SelectionExport) ([]Event, error) {
	values := []struct {
		path string
		kind string
		data any
	}{
		{receiptPath, "selection", receipt},
		{pointerPath, "selection", pointer},
		{selectionExportPrefix + export.ID + ".json", "export", export},
	}
	events := make([]Event, 0, len(values))
	for _, value := range values {
		data, err := json.Marshal(value.data)
		if err != nil {
			return nil, fmt.Errorf("encode %s selection record: %w", value.kind, err)
		}
		record := Record{Kind: value.kind, SchemaVersion: selectionRecordSchemaVersion, Data: data}
		eventID, err := identity.ID(selectionStateEventKind, 1, selectionEventIdentity{Path: value.path, Record: record})
		if err != nil {
			return nil, fmt.Errorf("identify %s selection event: %w", value.kind, err)
		}
		events = append(events, Event{ID: eventID, RecordPath: value.path, Record: record})
	}
	return events, nil
}

func selectionPointerPath(moduleID, avmReleaseVersion, contextID string) string {
	return selectionStatePrefix + moduleID + "/versions/" + avmReleaseVersion + "/" + contextID + selectionCurrentSuffix
}

func readSelectionPointer(snapshot Snapshot, recordPath string) (SelectionPointerRecord, bool, error) {
	record, exists := snapshot.Records[recordPath]
	if !exists {
		return SelectionPointerRecord{}, false, nil
	}
	if record.Kind != "selection" || record.SchemaVersion != selectionRecordSchemaVersion {
		return SelectionPointerRecord{}, false, fmt.Errorf("selection pointer has unsupported record identity")
	}
	var pointer SelectionPointerRecord
	if err := json.Unmarshal(record.Data, &pointer); err != nil {
		return SelectionPointerRecord{}, false, fmt.Errorf("decode selection pointer: %w", err)
	}
	if pointer.SchemaVersion != selectionRecordSchemaVersion || pointer.ModuleID == "" || !selectionReleasePattern.MatchString(pointer.AVMReleaseVersion) || pointer.ContextID == "" || pointer.Sequence == 0 || pointer.SelectionID == "" || pointer.DesiredGeneration == 0 || pointer.UpdatedAt.IsZero() || (pointer.RequestID == "") != (pointer.CompletionID == "") {
		return SelectionPointerRecord{}, false, fmt.Errorf("selection pointer is incomplete")
	}
	if recordPath != selectionPointerPath(pointer.ModuleID, pointer.AVMReleaseVersion, pointer.ContextID) {
		return SelectionPointerRecord{}, false, fmt.Errorf("selection pointer is stored at a noncanonical release-scoped path")
	}
	if pointer.RequestID == "" {
		receipt, receiptErr := readSelectionReceipt(snapshot, pointer.ModuleID, pointer.ContextID, pointer.SelectionID)
		if receiptErr != nil || receipt.AVMReleaseVersion != pointer.AVMReleaseVersion || receipt.AnalysisID != nil || receipt.SourceSHA != nil || receipt.FactsID != nil || receipt.CompletionID != nil || receipt.Sequence != pointer.Sequence || receipt.DesiredGeneration != pointer.DesiredGeneration {
			return SelectionPointerRecord{}, false, fmt.Errorf("null selection pointer does not match its deselection receipt")
		}
	} else if identity.ValidateRecordID("completion", pointer.CompletionID) != nil {
		return SelectionPointerRecord{}, false, fmt.Errorf("selection pointer completion ID is invalid")
	}
	return pointer, true, nil
}

func existingSelectionResult(head string, snapshot Snapshot, pointer SelectionPointerRecord, input SelectionInput) (SelectionResult, error) {
	selectionPath := "state/selections/" + pointer.ModuleID + "/" + pointer.ContextID + "/" + pointer.SelectionID + ".json"
	selectionRecord, exists := snapshot.Records[selectionPath]
	if !exists || selectionRecord.Kind != "selection" || selectionRecord.SchemaVersion != selectionRecordSchemaVersion {
		return SelectionResult{}, fmt.Errorf("current selection receipt is missing; recovery review is required")
	}
	var receipt SelectionReceipt
	if err := json.Unmarshal(selectionRecord.Data, &receipt); err != nil {
		return SelectionResult{}, fmt.Errorf("decode current selection receipt: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return SelectionResult{}, err
	}
	if receipt.AVMReleaseVersion != pointer.AVMReleaseVersion || receipt.CompletionID == nil || *receipt.CompletionID != input.CompletionID || receipt.AnalysisID == nil || *receipt.AnalysisID != input.AnalysisID || receipt.EvidenceCommit != input.EvidenceCommit {
		return SelectionResult{}, fmt.Errorf("selection request was replayed with different accepted evidence")
	}
	exportPath := selectionExportPrefix + pointer.SelectionID + ".json"
	exportRecord, exists := snapshot.Records[exportPath]
	if !exists || exportRecord.Kind != "export" || exportRecord.SchemaVersion != selectionRecordSchemaVersion {
		return SelectionResult{}, fmt.Errorf("selection export intent is missing; recovery review is required")
	}
	var export SelectionExport
	if err := json.Unmarshal(exportRecord.Data, &export); err != nil {
		return SelectionResult{}, fmt.Errorf("decode current selection export: %w", err)
	}
	if export.SelectionID != receipt.ID || export.Receipt.ID != receipt.ID {
		return SelectionResult{}, fmt.Errorf("selection export intent does not match its receipt")
	}
	return SelectionResult{Head: head, Receipt: receipt, Export: export}, nil
}
