package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
)

// AcceptedAssessmentSelectionCandidate identifies one current, receipt-backed
// assessment. ProjectionBarrierPending marks results still waiting on their
// predecessor projection barrier. SelectAccepted revalidates every field and
// proof in its expected-head transaction.
type AcceptedAssessmentSelectionCandidate struct {
	ModuleID                 string
	ContextID                string
	RequestID                string
	CompletionID             string
	AcceptanceHandoffID      string
	AnalysisID               string
	EvidenceCommit           string
	ProjectionBarrierPending bool
}

// AcceptedAssessmentSelectionCandidates returns current accepted assessments
// whose exact acceptance receipt, operation write set, and request are proven
// by this snapshot, including results still waiting on their projection barrier.
func AcceptedAssessmentSelectionCandidates(snapshot Snapshot, moduleID string, selectedAt time.Time) ([]AcceptedAssessmentSelectionCandidate, error) {
	if selectedAt.IsZero() {
		return nil, fmt.Errorf("selection candidate time is required")
	}
	if moduleID != "" && !selectionModulePattern.MatchString(moduleID) {
		return nil, fmt.Errorf("selection candidate module ID is invalid")
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	candidates := make([]AcceptedAssessmentSelectionCandidate, 0)
	seenRequests := make(map[string]struct{})
	for recordPath, record := range snapshot.Records {
		if record.Kind != "publication" {
			continue
		}
		var header struct {
			SchemaVersion int    `json:"schema_version"`
			RecordType    string `json:"record_type"`
		}
		if err := json.Unmarshal(record.Data, &header); err != nil {
			return nil, fmt.Errorf("decode evidence acceptance header %s: %w", recordPath, err)
		}
		if header.RecordType != evidenceAcceptanceRecordType {
			continue
		}
		if header.SchemaVersion == 1 {
			// Historical acquisition receipts predate version-pinned assessment
			// contexts and cannot produce selection candidates.
			continue
		}
		var accepted EvidenceAcceptanceReceipt
		if err := json.Unmarshal(record.Data, &accepted); err != nil || accepted.Validate() != nil {
			return nil, fmt.Errorf("invalid evidence acceptance receipt %s", recordPath)
		}
		if accepted.Phase != "assess" || moduleID != "" && accepted.ModuleID != moduleID {
			continue
		}
		if recordPath != evidenceAcceptancePrefix+accepted.ID+".json" {
			return nil, ErrTransactionIdentityConflict
		}
		operationID := "evidence-acceptance:" + accepted.ID
		operationPath, err := identity.StateOperationReceiptPath(accepted.ModuleID, operationID)
		if err != nil {
			return nil, err
		}
		operationRecord, exists := snapshot.Records[operationPath]
		if !exists || operationRecord.Kind != "operation_receipt" || operationRecord.SchemaVersion != 1 || verifyReceiptEvent(snapshot, operationID, operationPath) != nil {
			return nil, ErrSelectionIneligible
		}
		var operation OperationReceipt
		var result evidenceAcceptanceOperationResult
		if json.Unmarshal(operationRecord.Data, &operation) != nil || operation.Validate(operationPath) != nil ||
			json.Unmarshal(operation.Result, &result) != nil || !sameEvidenceAcceptanceReceipt(result.Acceptance.Receipt, accepted) ||
			verifyReceiptWriteSet(snapshot, operation, result.Events) != nil {
			return nil, ErrSelectionIneligible
		}
		var handoff projection.Handoff
		handoffCount := 0
		for _, event := range result.Events {
			if event.Record.Kind != "stage_handoff" {
				continue
			}
			var candidate projection.Handoff
			if json.Unmarshal(event.Record.Data, &candidate) != nil || candidate.Validate() != nil || candidate.OperationID != operationID {
				return nil, ErrSelectionIneligible
			}
			if candidate.ConsumerStage == "selection" {
				handoff, handoffCount = candidate, handoffCount+1
			}
		}
		if handoffCount != 1 {
			return nil, ErrSelectionIneligible
		}
		request, err := latestCurrentAssessment(snapshot, accepted.ModuleID, accepted.AVMReleaseVersion, accepted.ContextID)
		if errors.Is(err, ErrDeploymentNotCurrent) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if request.ID != accepted.RequestID || request.Status != "accepted" || request.Work == nil || request.Work.Completion == nil ||
			request.Work.Completion.ID != accepted.CompletionID || request.Work.Completion.ValidationID == "" ||
			request.Fingerprint != accepted.InputFingerprint || request.SourceSHA != accepted.SourceSHA ||
			request.Work.DesiredGeneration != accepted.DesiredGeneration || request.Work.FactsID != accepted.FactsID {
			continue
		}
		if err := validateSelectionLifecycleSlot(snapshot, request); errors.Is(err, ErrSelectionStale) || errors.Is(err, ErrSelectionIneligible) {
			continue
		} else if err != nil {
			return nil, err
		}
		candidate := AcceptedAssessmentSelectionCandidate{
			ModuleID: accepted.ModuleID, ContextID: accepted.ContextID, RequestID: accepted.RequestID,
			CompletionID: accepted.CompletionID, AcceptanceHandoffID: handoff.ID,
			AnalysisID: accepted.AnalysisID, EvidenceCommit: accepted.EvidenceCommit,
		}
		input := SelectionInput{
			ModuleID: candidate.ModuleID, ContextID: candidate.ContextID, RequestID: candidate.RequestID,
			CompletionID: candidate.CompletionID, AcceptanceHandoffID: candidate.AcceptanceHandoffID,
			AnalysisID: candidate.AnalysisID, EvidenceCommit: candidate.EvidenceCommit, SelectedAt: selectedAt,
		}
		if err := validateSelectionPredecessor(snapshot, request, input); err != nil {
			if errors.Is(err, ErrSelectionBarrierPending) {
				candidate.ProjectionBarrierPending = true
			} else {
				return nil, err
			}
		}
		if _, duplicate := seenRequests[candidate.ModuleID+"\x00"+candidate.RequestID]; duplicate {
			return nil, fmt.Errorf("multiple selection candidates reference one assessment request")
		}
		seenRequests[candidate.ModuleID+"\x00"+candidate.RequestID] = struct{}{}
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].ModuleID != candidates[right].ModuleID {
			return candidates[left].ModuleID < candidates[right].ModuleID
		}
		if candidates[left].ContextID != candidates[right].ContextID {
			return candidates[left].ContextID < candidates[right].ContextID
		}
		return candidates[left].RequestID < candidates[right].RequestID
	})
	return candidates, nil
}
