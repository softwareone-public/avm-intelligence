package state

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

const (
	EvidenceProposalModule          = "module_evidence"
	evidenceAcceptanceRecordType    = "evidence_acceptance"
	evidenceAcceptanceSchemaVersion = 1
	evidenceAcceptancePrefix        = "state/publications/"
	evidenceAcceptanceKind          = "evidence-acceptance"
)

// EvidenceAcceptanceInput is supplied only after a trusted reconciler verifies
// the canonical module_evidence merge and its sealed completion.
type EvidenceAcceptanceInput struct {
	ProposalID        string    `json:"proposal_id"`
	ProposalKind      string    `json:"proposal_kind"`
	ModuleID          string    `json:"module_id"`
	ContextID         string    `json:"context_id,omitempty"`
	RequestID         string    `json:"request_id"`
	CompletionID      string    `json:"completion_id"`
	Phase             string    `json:"phase"`
	DesiredGeneration uint64    `json:"desired_generation"`
	SourceSHA         string    `json:"source_sha"`
	FactsID           string    `json:"facts_id"`
	AnalysisID        string    `json:"analysis_id,omitempty"`
	InputFingerprint  string    `json:"input_fingerprint"`
	ArtifactDigest    string    `json:"artifact_digest"`
	EvidenceCommit    string    `json:"evidence_commit"`
	AcceptedAt        time.Time `json:"accepted_at"`
}

// EvidenceAcceptanceReceipt records canonical evidence acceptance without
// changing the current selection pointer.
type EvidenceAcceptanceReceipt struct {
	SchemaVersion     int       `json:"schema_version"`
	RecordType        string    `json:"record_type"`
	ID                string    `json:"evidence_acceptance_id"`
	ProposalID        string    `json:"proposal_id"`
	ProposalKind      string    `json:"proposal_kind"`
	ModuleID          string    `json:"module_id"`
	ContextID         string    `json:"context_id,omitempty"`
	RequestID         string    `json:"request_id"`
	CompletionID      string    `json:"completion_id"`
	Phase             string    `json:"phase"`
	DesiredGeneration uint64    `json:"desired_generation"`
	SourceSHA         string    `json:"source_sha"`
	FactsID           string    `json:"facts_id"`
	AnalysisID        string    `json:"analysis_id,omitempty"`
	InputFingerprint  string    `json:"input_fingerprint"`
	ArtifactDigest    string    `json:"artifact_digest"`
	EvidenceCommit    string    `json:"evidence_commit"`
	AcceptedAt        time.Time `json:"accepted_at"`
}

// EvidenceAcceptanceResult returns the atomic receipt/request transition.
type EvidenceAcceptanceResult struct {
	Head    string
	Receipt EvidenceAcceptanceReceipt
	Changed bool
}

type evidenceAcceptanceIdentity struct {
	ProposalID        string `json:"proposal_id"`
	ProposalKind      string `json:"proposal_kind"`
	ModuleID          string `json:"module_id"`
	ContextID         string `json:"context_id,omitempty"`
	RequestID         string `json:"request_id"`
	CompletionID      string `json:"completion_id"`
	Phase             string `json:"phase"`
	DesiredGeneration uint64 `json:"desired_generation"`
	SourceSHA         string `json:"source_sha"`
	FactsID           string `json:"facts_id"`
	AnalysisID        string `json:"analysis_id,omitempty"`
	InputFingerprint  string `json:"input_fingerprint"`
	ArtifactDigest    string `json:"artifact_digest"`
	EvidenceCommit    string `json:"evidence_commit"`
}

// NewEvidenceAcceptanceReceipt validates a reconciled merge receipt and assigns
// its stable ID before any operational request transition is attempted.
func NewEvidenceAcceptanceReceipt(input EvidenceAcceptanceInput) (EvidenceAcceptanceReceipt, error) {
	if err := validateEvidenceAcceptanceInput(input); err != nil {
		return EvidenceAcceptanceReceipt{}, err
	}
	identityValue := evidenceAcceptanceIdentity{
		ProposalID: input.ProposalID, ProposalKind: input.ProposalKind, ModuleID: input.ModuleID,
		ContextID: input.ContextID, RequestID: input.RequestID, CompletionID: input.CompletionID,
		Phase: input.Phase, DesiredGeneration: input.DesiredGeneration, SourceSHA: input.SourceSHA,
		FactsID: input.FactsID, AnalysisID: input.AnalysisID, InputFingerprint: input.InputFingerprint,
		ArtifactDigest: input.ArtifactDigest, EvidenceCommit: input.EvidenceCommit,
	}
	receiptID, err := identity.ID(evidenceAcceptanceKind, evidenceAcceptanceSchemaVersion, identityValue)
	if err != nil {
		return EvidenceAcceptanceReceipt{}, err
	}
	return EvidenceAcceptanceReceipt{
		SchemaVersion: evidenceAcceptanceSchemaVersion, RecordType: evidenceAcceptanceRecordType, ID: receiptID,
		ProposalID: input.ProposalID, ProposalKind: input.ProposalKind, ModuleID: input.ModuleID,
		ContextID: input.ContextID, RequestID: input.RequestID, CompletionID: input.CompletionID,
		Phase: input.Phase, DesiredGeneration: input.DesiredGeneration, SourceSHA: input.SourceSHA,
		FactsID: input.FactsID, AnalysisID: input.AnalysisID, InputFingerprint: input.InputFingerprint,
		ArtifactDigest: input.ArtifactDigest, EvidenceCommit: input.EvidenceCommit, AcceptedAt: input.AcceptedAt.UTC(),
	}, nil
}

// ImportEvidenceReceipt atomically records the verified merge and advances
// request acceptance. It never selects analysis or changes a selection pointer.
func ImportEvidenceReceipt(ctx context.Context, coordinator TransactionCoordinator, input EvidenceAcceptanceInput) (EvidenceAcceptanceResult, error) {
	receipt, err := NewEvidenceAcceptanceReceipt(input)
	if err != nil {
		return EvidenceAcceptanceResult{}, err
	}
	if coordinator == nil {
		return EvidenceAcceptanceResult{}, fmt.Errorf("state transaction coordinator is required")
	}
	operationID := "evidence-acceptance:" + receipt.ID
	result, err := coordinator.RunTransaction(ctx, operationID, func(snapshot Snapshot) (TransactionDecision, error) {
		return evidenceAcceptanceDecision(snapshot, input, receipt)
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		return resolveEvidenceAcceptance(snapshot, receipt)
	})
	if err != nil {
		return EvidenceAcceptanceResult{}, err
	}
	accepted, ok := result.Value.(EvidenceAcceptanceResult)
	if !ok {
		return EvidenceAcceptanceResult{}, fmt.Errorf("committed evidence acceptance result has an invalid type")
	}
	accepted.Head, accepted.Changed = result.Head, result.Changed
	return accepted, nil
}

func evidenceAcceptanceDecision(snapshot Snapshot, input EvidenceAcceptanceInput, receipt EvidenceAcceptanceReceipt) (TransactionDecision, error) {
	receiptPath := evidenceAcceptancePrefix + receipt.ID + ".json"
	if record, exists := snapshot.Records[receiptPath]; exists {
		var previous EvidenceAcceptanceReceipt
		if record.Kind != "publication" || record.SchemaVersion != evidenceAcceptanceSchemaVersion || json.Unmarshal(record.Data, &previous) != nil || previous.Validate() != nil || previous.identity() != receipt.identity() {
			return TransactionDecision{}, fmt.Errorf("existing evidence receipt conflicts with its identity")
		}
		return TransactionDecision{Noop: true, Result: EvidenceAcceptanceResult{Receipt: previous}}, nil
	}
	requestPath := "state/requests/" + input.RequestID + ".json"
	requestRecord, exists := snapshot.Records[requestPath]
	if !exists || requestRecord.Kind != "request" || requestRecord.SchemaVersion != 1 {
		return TransactionDecision{}, ErrSelectionIneligible
	}
	var request selectionRequest
	if err := json.Unmarshal(requestRecord.Data, &request); err != nil {
		return TransactionDecision{}, fmt.Errorf("decode evidence request: %w", err)
	}
	if err := validateEvidenceRequest(request, input); err != nil {
		return TransactionDecision{}, err
	}
	updatedRequest, err := acceptEvidenceRequest(requestRecord.Data, input)
	if err != nil {
		return TransactionDecision{}, err
	}
	requestEvent, err := evidenceStateEvent(requestPath, "request", updatedRequest)
	if err != nil {
		return TransactionDecision{}, err
	}
	receiptEvent, err := evidenceStateEvent(receiptPath, "publication", receipt)
	if err != nil {
		return TransactionDecision{}, err
	}
	operationID := "evidence-acceptance:" + receipt.ID
	requestEvent.ID = operationID
	return TransactionDecision{
		ReadSet:   TransactionReadSet{Owners: []string{input.ModuleID}},
		Events:    []Event{requestEvent, receiptEvent},
		Result:    EvidenceAcceptanceResult{Receipt: receipt, Changed: true},
		Candidate: CandidateOptions{Subject: "state(evidence): accept merged receipt", Provenance: GitProvenance{Operation: "evidence.accept", Event: operationID, Module: input.ModuleID, Request: input.RequestID}},
	}, nil
}

func resolveEvidenceAcceptance(snapshot Snapshot, receipt EvidenceAcceptanceReceipt) (TransactionResolution, error) {
	record, exists := snapshot.Records[evidenceAcceptancePrefix+receipt.ID+".json"]
	if !exists || record.Kind != "publication" {
		return TransactionResolution{}, nil
	}
	var durable EvidenceAcceptanceReceipt
	if err := json.Unmarshal(record.Data, &durable); err != nil {
		return TransactionResolution{}, err
	}
	if err := durable.Validate(); err != nil || durable.identity() != receipt.identity() || !durable.AcceptedAt.Equal(receipt.AcceptedAt) {
		return TransactionResolution{}, ErrTransactionIdentityConflict
	}
	return TransactionResolution{Found: true, Value: EvidenceAcceptanceResult{Receipt: durable}}, nil
}

func validateAcceptedEvidenceReceipt(snapshot Snapshot, request selectionRequest, input SelectionInput) error {
	found := false
	for recordPath, record := range snapshot.Records {
		if record.Kind != "publication" {
			continue
		}
		var header struct {
			RecordType string `json:"record_type"`
		}
		if err := json.Unmarshal(record.Data, &header); err != nil {
			return fmt.Errorf("decode publication receipt %s: %w", recordPath, err)
		}
		if header.RecordType != evidenceAcceptanceRecordType {
			continue
		}
		var receipt EvidenceAcceptanceReceipt
		if err := json.Unmarshal(record.Data, &receipt); err != nil {
			return fmt.Errorf("decode evidence acceptance receipt %s: %w", recordPath, err)
		}
		if err := receipt.Validate(); err != nil {
			return err
		}
		matches := receipt.ProposalKind == EvidenceProposalModule && receipt.ModuleID == input.ModuleID && receipt.ContextID == input.ContextID && receipt.RequestID == request.ID && receipt.CompletionID == input.CompletionID && receipt.Phase == "assess" && receipt.DesiredGeneration == request.Work.DesiredGeneration && receipt.SourceSHA == request.SourceSHA && receipt.FactsID == request.Work.FactsID && receipt.AnalysisID == input.AnalysisID && receipt.InputFingerprint == request.Fingerprint && receipt.EvidenceCommit == input.EvidenceCommit && !receipt.AcceptedAt.After(input.SelectedAt)
		if !matches {
			continue
		}
		if recordPath != evidenceAcceptancePrefix+receipt.ID+".json" || found {
			return fmt.Errorf("selection has duplicate or noncanonical evidence acceptance receipts")
		}
		found = true
	}
	if !found {
		return ErrSelectionIneligible
	}
	return nil
}

// Validate checks the durable evidence acceptance identity and its two phases.
func (receipt EvidenceAcceptanceReceipt) Validate() error {
	input := EvidenceAcceptanceInput{
		ProposalID: receipt.ProposalID, ProposalKind: receipt.ProposalKind, ModuleID: receipt.ModuleID,
		ContextID: receipt.ContextID, RequestID: receipt.RequestID, CompletionID: receipt.CompletionID,
		Phase: receipt.Phase, DesiredGeneration: receipt.DesiredGeneration, SourceSHA: receipt.SourceSHA,
		FactsID: receipt.FactsID, AnalysisID: receipt.AnalysisID, InputFingerprint: receipt.InputFingerprint,
		ArtifactDigest: receipt.ArtifactDigest, EvidenceCommit: receipt.EvidenceCommit, AcceptedAt: receipt.AcceptedAt,
	}
	if receipt.SchemaVersion != evidenceAcceptanceSchemaVersion || receipt.RecordType != evidenceAcceptanceRecordType || validateEvidenceAcceptanceInput(input) != nil {
		return fmt.Errorf("evidence acceptance receipt is invalid")
	}
	if err := identity.ValidateRecordID(evidenceAcceptanceKind, receipt.ID); err != nil {
		return err
	}
	expectedID, err := evidenceAcceptanceID(receipt.identity())
	if err != nil || receipt.ID != expectedID {
		return fmt.Errorf("evidence acceptance ID does not match merged content")
	}
	return nil
}

type evidenceRequestDocument map[string]json.RawMessage

func validateEvidenceRequest(request selectionRequest, input EvidenceAcceptanceInput) error {
	if request.ID != input.RequestID || request.ModuleID != input.ModuleID || request.Status != "awaiting_acceptance" || request.Work == nil || request.Work.Phase != input.Phase || request.Work.DesiredGeneration != input.DesiredGeneration || request.SourceSHA != input.SourceSHA || request.Fingerprint != input.InputFingerprint || request.Work.Completion == nil {
		return ErrSelectionStale
	}
	completion := request.Work.Completion
	if completion.ID != input.CompletionID || completion.RequestID != request.ID || completion.InputFingerprint != request.Fingerprint || completion.DesiredGeneration != request.Work.DesiredGeneration || completion.ArtifactDigest != input.ArtifactDigest || completion.SealedAt.IsZero() || input.AcceptedAt.Before(completion.SealedAt) {
		return ErrSelectionStale
	}
	if input.Phase == "assess" {
		if request.Work.ContextID != input.ContextID || request.Work.FactsID != input.FactsID || identity.ValidateRecordID("analysis", input.AnalysisID) != nil {
			return ErrSelectionIneligible
		}
	} else if input.ContextID != "" || input.AnalysisID != "" || request.Work.Phase != "acquire_source" {
		return ErrSelectionIneligible
	}
	return nil
}

func acceptEvidenceRequest(data []byte, input EvidenceAcceptanceInput) ([]byte, error) {
	var document evidenceRequestDocument
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return nil, fmt.Errorf("decode evidence request document")
	}
	status := "accepted"
	if input.Phase == "acquire_source" {
		status = "facts_accepted"
	}
	if err := setRequestField(document, "status", status); err != nil {
		return nil, err
	}
	if err := setRequestField(document, "updated_at", input.AcceptedAt.UTC()); err != nil {
		return nil, err
	}
	if input.Phase == "assess" {
		if err := setRequestField(document, "last_accepted_fingerprint", input.InputFingerprint); err != nil {
			return nil, err
		}
		if err := setRequestField(document, "last_accepted_source_sha", input.SourceSHA); err != nil {
			return nil, err
		}
	} else {
		var work evidenceRequestDocument
		if err := json.Unmarshal(document["work"], &work); err != nil || work == nil {
			return nil, fmt.Errorf("decode acquisition request work")
		}
		encodedFacts, err := json.Marshal(input.FactsID)
		if err != nil {
			return nil, err
		}
		work["accepted_facts_id"] = encodedFacts
		encodedWork, err := json.Marshal(work)
		if err != nil {
			return nil, err
		}
		document["work"] = encodedWork
	}
	return json.Marshal(document)
}

func setRequestField(document evidenceRequestDocument, name string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	document[name] = encoded
	return nil
}

func validateEvidenceAcceptanceInput(input EvidenceAcceptanceInput) error {
	if identity.ValidateRecordID("proposal", input.ProposalID) != nil || input.ProposalKind != EvidenceProposalModule || !selectionModulePattern.MatchString(input.ModuleID) || input.RequestID == "" || identity.ValidateRecordID("completion", input.CompletionID) != nil || input.DesiredGeneration == 0 || !selectionSHA1Pattern.MatchString(input.SourceSHA) || identity.ValidateRecordID("facts", input.FactsID) != nil || !selectionSHA256Pattern.MatchString(input.InputFingerprint) || !selectionSHA256Pattern.MatchString(input.ArtifactDigest) || !selectionSHA1Pattern.MatchString(input.EvidenceCommit) || input.AcceptedAt.IsZero() {
		return fmt.Errorf("evidence acceptance input is incomplete or invalid")
	}
	switch input.Phase {
	case "assess":
		if identity.ValidateRelativePath(input.ContextID, false) != nil || identity.ValidateRecordID("analysis", input.AnalysisID) != nil {
			return fmt.Errorf("assessment evidence identity is invalid")
		}
	case "acquire_source":
		if input.ContextID != "" || input.AnalysisID != "" {
			return fmt.Errorf("facts-only evidence must omit context and analysis IDs")
		}
	default:
		return fmt.Errorf("unsupported evidence acceptance phase %q", input.Phase)
	}
	return nil
}

func evidenceAcceptanceID(value evidenceAcceptanceIdentity) (string, error) {
	return identity.ID(evidenceAcceptanceKind, evidenceAcceptanceSchemaVersion, value)
}

func evidenceStateEvent(recordPath, kind string, value any) (Event, error) {
	var data []byte
	var err error
	switch raw := value.(type) {
	case json.RawMessage:
		data = append([]byte(nil), raw...)
	case []byte:
		data = append([]byte(nil), raw...)
	default:
		data, err = json.Marshal(value)
	}
	if err != nil {
		return Event{}, fmt.Errorf("encode %s receipt: %w", kind, err)
	}
	record := Record{Kind: kind, SchemaVersion: evidenceAcceptanceSchemaVersion, Data: data}
	eventID, err := identity.ID("state-event", 1, selectionEventIdentity{Path: recordPath, Record: record})
	if err != nil {
		return Event{}, err
	}
	return Event{ID: eventID, RecordPath: recordPath, Record: record}, nil
}

func (receipt EvidenceAcceptanceReceipt) identity() evidenceAcceptanceIdentity {
	return evidenceAcceptanceIdentity{
		ProposalID: receipt.ProposalID, ProposalKind: receipt.ProposalKind, ModuleID: receipt.ModuleID,
		ContextID: receipt.ContextID, RequestID: receipt.RequestID, CompletionID: receipt.CompletionID,
		Phase: receipt.Phase, DesiredGeneration: receipt.DesiredGeneration, SourceSHA: receipt.SourceSHA,
		FactsID: receipt.FactsID, AnalysisID: receipt.AnalysisID, InputFingerprint: receipt.InputFingerprint,
		ArtifactDigest: receipt.ArtifactDigest, EvidenceCommit: receipt.EvidenceCommit,
	}
}
