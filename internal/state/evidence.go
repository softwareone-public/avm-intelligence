package state

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/contextset"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
)

const (
	EvidenceProposalModule               = "module_evidence"
	evidenceAcceptanceRecordType         = "evidence_acceptance"
	evidenceAcceptanceSchemaVersion      = 2
	evidenceAcceptanceEventSchemaVersion = 1
	evidenceAcceptancePrefix             = "state/publications/"
	evidenceAcceptanceKind               = "evidence-acceptance"
)

// EvidenceAcceptanceInput is supplied only after a trusted reconciler verifies
// the canonical module_evidence merge and its sealed completion.
type EvidenceAcceptanceInput struct {
	ProposalID        string                        `json:"proposal_id"`
	ProposalKind      string                        `json:"proposal_kind"`
	ModuleID          string                        `json:"module_id"`
	ContextID         string                        `json:"context_id,omitempty"`
	RequestID         string                        `json:"request_id"`
	CompletionID      string                        `json:"completion_id"`
	Phase             string                        `json:"phase"`
	AVMReleaseVersion string                        `json:"avm_release_version"`
	DesiredGeneration uint64                        `json:"desired_generation"`
	SourceSHA         string                        `json:"source_sha"`
	FactsID           string                        `json:"facts_id"`
	AnalysisID        string                        `json:"analysis_id,omitempty"`
	InputFingerprint  string                        `json:"input_fingerprint"`
	ArtifactDigest    string                        `json:"artifact_digest"`
	EvidenceCommit    string                        `json:"evidence_commit"`
	AcceptedAt        time.Time                     `json:"accepted_at"`
	Producer          projection.WorkflowProvenance `json:"producer"`
	RequiredContexts  *contextset.Set               `json:"required_context_set,omitempty"`
}

// EvidenceAcceptanceReceipt records canonical evidence acceptance without
// changing the current selection pointer.
type EvidenceAcceptanceReceipt struct {
	SchemaVersion     int             `json:"schema_version"`
	RecordType        string          `json:"record_type"`
	ID                string          `json:"evidence_acceptance_id"`
	ProposalID        string          `json:"proposal_id"`
	ProposalKind      string          `json:"proposal_kind"`
	ModuleID          string          `json:"module_id"`
	ContextID         string          `json:"context_id,omitempty"`
	RequestID         string          `json:"request_id"`
	CompletionID      string          `json:"completion_id"`
	Phase             string          `json:"phase"`
	AVMReleaseVersion string          `json:"avm_release_version"`
	DesiredGeneration uint64          `json:"desired_generation"`
	SourceSHA         string          `json:"source_sha"`
	FactsID           string          `json:"facts_id"`
	AnalysisID        string          `json:"analysis_id,omitempty"`
	InputFingerprint  string          `json:"input_fingerprint"`
	ArtifactDigest    string          `json:"artifact_digest"`
	EvidenceCommit    string          `json:"evidence_commit"`
	AcceptedAt        time.Time       `json:"accepted_at"`
	RequiredContexts  *contextset.Set `json:"required_context_set,omitempty"`
}

// AcceptedFactsContextSets returns only facts-acceptance receipts whose exact
// operation receipt and historical write set still verify. Assessment planning
// uses these immutable sets to retain eligible and deferred tracks.
func AcceptedFactsContextSets(snapshot Snapshot, moduleID string) ([]EvidenceAcceptanceReceipt, error) {
	receipts := make([]EvidenceAcceptanceReceipt, 0)
	for recordPath, record := range snapshot.Records {
		if record.Kind != "publication" {
			continue
		}
		var header struct {
			SchemaVersion int    `json:"schema_version"`
			RecordType    string `json:"record_type"`
		}
		if err := json.Unmarshal(record.Data, &header); err != nil {
			return nil, fmt.Errorf("decode publication receipt %s: %w", recordPath, err)
		}
		if header.RecordType != evidenceAcceptanceRecordType {
			continue
		}
		if header.SchemaVersion == 1 {
			// Historical pattern facts receipts predate version-pinned context
			// sets and cannot participate in current assessment planning.
			continue
		}
		if header.SchemaVersion != evidenceAcceptanceSchemaVersion {
			return nil, fmt.Errorf("facts acceptance receipt uses an unsupported schema")
		}
		var receipt EvidenceAcceptanceReceipt
		if err := json.Unmarshal(record.Data, &receipt); err != nil || receipt.Validate() != nil {
			return nil, fmt.Errorf("facts acceptance receipt is invalid")
		}
		if receipt.Phase != "acquire_source" || receipt.ProposalKind != EvidenceProposalModule || (moduleID != "" && receipt.ModuleID != moduleID) {
			continue
		}
		if recordPath != evidenceAcceptancePrefix+receipt.ID+".json" || receipt.RequiredContexts == nil {
			return nil, fmt.Errorf("facts acceptance receipt path or required context set is invalid")
		}
		if err := verifyAcceptedFactsReceipt(snapshot, receipt, record); err != nil {
			return nil, err
		}
		contexts, contextErr := assessmentContextSetForFactsReceipt(snapshot, receipt)
		if contextErr != nil {
			return nil, contextErr
		}
		receipt.RequiredContexts = &contexts
		receipts = append(receipts, receipt)
	}
	sort.Slice(receipts, func(left, right int) bool {
		if receipts[left].ModuleID != receipts[right].ModuleID {
			return receipts[left].ModuleID < receipts[right].ModuleID
		}
		if receipts[left].AVMReleaseVersion != receipts[right].AVMReleaseVersion {
			return receipts[left].AVMReleaseVersion < receipts[right].AVMReleaseVersion
		}
		if receipts[left].DesiredGeneration != receipts[right].DesiredGeneration {
			return receipts[left].DesiredGeneration < receipts[right].DesiredGeneration
		}
		return receipts[left].ID < receipts[right].ID
	})
	return receipts, nil
}

// CurrentSourcePinMatches reports whether a release/source pair is still the
// durable discovery pin. Missing pin history is not proof of currentness.
func CurrentSourcePinMatches(snapshot Snapshot, moduleID, release, sourceSHA string) (bool, error) {
	if !selectionModulePattern.MatchString(moduleID) || !selectionReleasePattern.MatchString(release) || !selectionSHA1Pattern.MatchString(sourceSHA) {
		return false, fmt.Errorf("source pin query identity is invalid")
	}
	path := "state/discovery/source-pins/" + moduleID + ".json"
	record, exists := snapshot.Records[path]
	if !exists {
		return false, nil
	}
	if record.Kind != "source_pins" || record.SchemaVersion != 1 {
		return false, fmt.Errorf("current source pin record identity is invalid")
	}
	var history struct {
		SchemaVersion  int                  `json:"schema_version"`
		ModuleID       string               `json:"module_id"`
		OverallLatest  *selectionSourcePin  `json:"overall_latest"`
		ReleaseHistory []selectionSourcePin `json:"release_history"`
	}
	if err := json.Unmarshal(record.Data, &history); err != nil {
		return false, fmt.Errorf("decode current source pin history: %w", err)
	}
	if history.SchemaVersion != 1 || history.ModuleID != moduleID {
		return false, fmt.Errorf("current source pin history does not match module")
	}
	for _, pin := range history.ReleaseHistory {
		if pin.Version == release {
			return pin.Commit == sourceSHA, nil
		}
	}
	if history.OverallLatest != nil && history.OverallLatest.Version == release {
		return history.OverallLatest.Commit == sourceSHA, nil
	}
	return false, nil
}

// EvidenceAcceptanceResult returns the atomic receipt/request transition.
type EvidenceAcceptanceResult struct {
	Head    string
	Receipt EvidenceAcceptanceReceipt
	Changed bool
}

type evidenceAcceptanceOperationResult struct {
	Acceptance EvidenceAcceptanceResult `json:"acceptance"`
	Events     []Event                  `json:"events"`
}

type evidenceAcceptanceIdentity struct {
	ProposalID           string `json:"proposal_id"`
	ProposalKind         string `json:"proposal_kind"`
	ModuleID             string `json:"module_id"`
	ContextID            string `json:"context_id,omitempty"`
	RequestID            string `json:"request_id"`
	CompletionID         string `json:"completion_id"`
	Phase                string `json:"phase"`
	AVMReleaseVersion    string `json:"avm_release_version"`
	DesiredGeneration    uint64 `json:"desired_generation"`
	SourceSHA            string `json:"source_sha"`
	FactsID              string `json:"facts_id"`
	AnalysisID           string `json:"analysis_id,omitempty"`
	InputFingerprint     string `json:"input_fingerprint"`
	ArtifactDigest       string `json:"artifact_digest"`
	EvidenceCommit       string `json:"evidence_commit"`
	RequiredContextSetID string `json:"required_context_set_id,omitempty"`
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
		Phase: input.Phase, AVMReleaseVersion: input.AVMReleaseVersion, DesiredGeneration: input.DesiredGeneration, SourceSHA: input.SourceSHA,
		FactsID: input.FactsID, AnalysisID: input.AnalysisID, InputFingerprint: input.InputFingerprint,
		ArtifactDigest: input.ArtifactDigest, EvidenceCommit: input.EvidenceCommit,
	}
	if input.RequiredContexts != nil {
		identityValue.RequiredContextSetID = input.RequiredContexts.ID
	}
	receiptID, err := identity.ID(evidenceAcceptanceKind, evidenceAcceptanceSchemaVersion, identityValue)
	if err != nil {
		return EvidenceAcceptanceReceipt{}, err
	}
	return EvidenceAcceptanceReceipt{
		SchemaVersion: evidenceAcceptanceSchemaVersion, RecordType: evidenceAcceptanceRecordType, ID: receiptID,
		ProposalID: input.ProposalID, ProposalKind: input.ProposalKind, ModuleID: input.ModuleID,
		ContextID: input.ContextID, RequestID: input.RequestID, CompletionID: input.CompletionID,
		Phase: input.Phase, AVMReleaseVersion: input.AVMReleaseVersion, DesiredGeneration: input.DesiredGeneration, SourceSHA: input.SourceSHA,
		FactsID: input.FactsID, AnalysisID: input.AnalysisID, InputFingerprint: input.InputFingerprint,
		ArtifactDigest: input.ArtifactDigest, EvidenceCommit: input.EvidenceCommit, AcceptedAt: input.AcceptedAt.UTC(),
		RequiredContexts: cloneRequiredContexts(input.RequiredContexts),
	}, nil
}

// ImportEvidenceReceipt atomically records the verified merge, request acceptance,
// and projection intent. Assessment acceptance also creates its selection handoff.
// Facts acceptance does not create a module-queue collector stage: provider-owned
// resource collection and acceptance are independent inputs to site-cycle planning.
func ImportEvidenceReceipt(ctx context.Context, coordinator TransactionCoordinator, input EvidenceAcceptanceInput) (EvidenceAcceptanceResult, error) {
	if err := input.Producer.Validate(); err != nil {
		return EvidenceAcceptanceResult{}, fmt.Errorf("evidence acceptance producer provenance is invalid: %w", err)
	}
	receipt, err := NewEvidenceAcceptanceReceipt(input)
	if err != nil {
		return EvidenceAcceptanceResult{}, err
	}
	if coordinator == nil {
		return EvidenceAcceptanceResult{}, fmt.Errorf("state transaction coordinator is required")
	}
	operationCoordinator, ok := coordinator.(interface {
		RunOperationTransaction(context.Context, string, string, []Event, TransactionDecisionFunc, TransactionResultResolver) (TransactionResult, error)
	})
	if !ok {
		return EvidenceAcceptanceResult{}, fmt.Errorf("evidence acceptance requires an operation-receipt coordinator")
	}
	operationID := "evidence-acceptance:" + receipt.ID
	operationPath, err := identity.StateOperationReceiptPath(input.ModuleID, operationID)
	if err != nil {
		return EvidenceAcceptanceResult{}, err
	}
	_, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return EvidenceAcceptanceResult{}, err
	}
	var expected []Event
	if _, present := snapshot.Records[operationPath]; present {
		recorded, exists := operationReceiptRecord(snapshot, input.ModuleID, operationID)
		if !exists {
			return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
		}
		if recorded.Validate(operationPath) != nil {
			return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
		}
		// The acceptance identity deliberately excludes retry-specific timestamps
		// and workflow run provenance. Reuse the original accepted time so replay
		// returns the immutable committed result and any selection handoff.
		var accepted EvidenceAcceptanceReceipt
		acceptanceRecord, present := snapshot.Records[evidenceAcceptancePrefix+receipt.ID+".json"]
		if !present || acceptanceRecord.Kind != "publication" || json.Unmarshal(acceptanceRecord.Data, &accepted) != nil || accepted.Validate() != nil || accepted.identity() != receipt.identity() {
			return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
		}
		receipt = accepted
		var previous evidenceAcceptanceOperationResult
		wantHandoff := input.Phase == "assess"
		wantEventCount := 3
		if wantHandoff {
			wantEventCount++
		}
		if err := json.Unmarshal(recorded.Result, &previous); err != nil || !sameEvidenceAcceptanceReceipt(previous.Acceptance.Receipt, receipt) || len(previous.Events) != wantEventCount {
			return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
		}
		if verifyReceiptWriteSet(snapshot, recorded, previous.Events) != nil || verifyReceiptEvent(snapshot, operationID, operationPath) != nil {
			return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
		}
		var handoff projection.Handoff
		handoffCount := 0
		for _, event := range previous.Events {
			if event.Record.Kind != "stage_handoff" {
				continue
			}
			handoffCount++
			if json.Unmarshal(event.Record.Data, &handoff) != nil || handoff.Validate() != nil || handoff.OperationID != operationID || handoff.Producer.WorkflowID != input.Producer.WorkflowID || !handoff.CreatedAt.Equal(accepted.AcceptedAt) {
				return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
			}
		}
		if (wantHandoff && handoffCount != 1) || (!wantHandoff && handoffCount != 0) {
			return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
		}
		intentCount := 0
		for _, event := range previous.Events {
			if event.Record.Kind != "projection_intent" {
				continue
			}
			var intent projection.DesiredIntent
			if json.Unmarshal(event.Record.Data, &intent) != nil || intent.ValidatePath(event.RecordPath) != nil || intent.TransitionID != operationID || intent.ModuleID != input.ModuleID || intent.Generation < input.DesiredGeneration {
				return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
			}
			if wantHandoff {
				intentIdentity, identityErr := intent.Identity()
				if identityErr != nil || handoff.Projection != intentIdentity {
					return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
				}
			}
			intentCount++
		}
		if intentCount != 1 {
			return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
		}
		expected = previous.Events
	} else {
		if _, present := snapshot.Records[evidenceAcceptancePrefix+receipt.ID+".json"]; present {
			return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
		}
		decision, buildErr := evidenceAcceptanceDecision(snapshot, input, receipt)
		if buildErr != nil {
			return EvidenceAcceptanceResult{}, buildErr
		}
		expected = decision.Events
	}
	result, err := operationCoordinator.RunOperationTransaction(ctx, input.ModuleID, operationID, expected, func(current Snapshot) (TransactionDecision, error) {
		decision, buildErr := evidenceAcceptanceDecision(current, input, receipt)
		if buildErr != nil {
			return TransactionDecision{}, buildErr
		}
		if !sameEventSet(decision.Events, expected) {
			return TransactionDecision{}, ErrTransactionIdentityConflict
		}
		decision.Result = evidenceAcceptanceOperationResult{Acceptance: decision.Result.(EvidenceAcceptanceResult), Events: expected}
		return decision, nil
	}, func(current Snapshot) (TransactionResolution, error) {
		return resolveEvidenceAcceptance(current, receipt)
	})
	if err != nil {
		return EvidenceAcceptanceResult{}, err
	}
	var committed evidenceAcceptanceOperationResult
	switch value := result.Value.(type) {
	case evidenceAcceptanceOperationResult:
		committed = value
	case json.RawMessage:
		if err := json.Unmarshal(value, &committed); err != nil {
			return EvidenceAcceptanceResult{}, fmt.Errorf("decode acceptance operation result: %w", err)
		}
	default:
		return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
	}
	if !sameEvidenceAcceptanceReceipt(committed.Acceptance.Receipt, receipt) || !sameEventSet(committed.Events, expected) {
		return EvidenceAcceptanceResult{}, ErrTransactionIdentityConflict
	}
	accepted := committed.Acceptance
	accepted.Head, accepted.Changed = result.Head, result.Changed
	return accepted, nil
}

func evidenceAcceptanceDecision(snapshot Snapshot, input EvidenceAcceptanceInput, receipt EvidenceAcceptanceReceipt) (TransactionDecision, error) {
	receiptPath := evidenceAcceptancePrefix + receipt.ID + ".json"
	if _, exists := snapshot.Records[receiptPath]; exists {
		return TransactionDecision{}, ErrTransactionIdentityConflict
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
	transitionID := operationID
	fields := []projection.OwnedField{
		{Name: "avm_release_version", Value: input.AVMReleaseVersion},
		{Name: "source_version", Value: input.AVMReleaseVersion},
		{Name: "source_sha", Value: input.SourceSHA},
		{Name: "phase", Value: input.Phase},
		{Name: "request_id", Value: input.RequestID},
	}
	status := "facts-accepted"
	if input.Phase == "assess" {
		status = "accepted"
		fields = append(fields, projection.OwnedField{Name: "context_id", Value: input.ContextID})
	}
	projectionGeneration, err := ModuleProjectionGeneration(snapshot, input.ModuleID, input.DesiredGeneration)
	if err != nil {
		return TransactionDecision{}, err
	}
	intent, err := projection.NewDesiredIntent(input.ModuleID, projectionGeneration, transitionID, status, fields, nil)
	if err != nil {
		return TransactionDecision{}, err
	}
	intentPath, err := identity.StateProjectionIntentPath(input.ModuleID)
	if err != nil {
		return TransactionDecision{}, err
	}
	intentEvent, err := evidenceStateEvent(intentPath, "projection_intent", intent)
	if err != nil {
		return TransactionDecision{}, err
	}
	projectionIdentity, err := intent.Identity()
	if err != nil {
		return TransactionDecision{}, err
	}
	events := []Event{requestEvent, receiptEvent, intentEvent}
	if input.Phase == "assess" {
		inputFingerprint, fingerprintErr := identity.ID("input-fingerprint", 1, input.InputFingerprint)
		if fingerprintErr != nil {
			return TransactionDecision{}, fingerprintErr
		}
		handoff, handoffErr := projection.NewHandoff(projection.Handoff{
			ModuleID: input.ModuleID, Generation: input.DesiredGeneration, TransitionID: transitionID,
			InputFingerprint: inputFingerprint, OperationID: operationID, Producer: input.Producer,
			ConsumerStage: "selection", Projection: projectionIdentity, AcceptedFactsID: input.FactsID,
		}, input.AcceptedAt)
		if handoffErr != nil {
			return TransactionDecision{}, handoffErr
		}
		handoffPath, pathErr := identity.StateProjectionHandoffPath(input.ModuleID, handoff.ID)
		if pathErr != nil {
			return TransactionDecision{}, pathErr
		}
		handoffEvent, eventErr := evidenceStateEvent(handoffPath, "stage_handoff", handoff)
		if eventErr != nil {
			return TransactionDecision{}, eventErr
		}
		handoffEvent.ID = handoff.ID
		if existing, present := snapshot.Records[handoffPath]; present && (existing.Kind != handoffEvent.Record.Kind || existing.SchemaVersion != handoffEvent.Record.SchemaVersion || !bytes.Equal(existing.Data, handoffEvent.Record.Data)) {
			return TransactionDecision{}, ErrTransactionIdentityConflict
		}
		events = append(events, handoffEvent)
	}
	if existing, present := snapshot.Records[intentPath]; present && existing.Kind == "projection_intent" {
		var current projection.DesiredIntent
		if json.Unmarshal(existing.Data, &current) != nil || current.ValidatePath(intentPath) != nil || current.Generation > intent.Generation {
			return TransactionDecision{}, ErrSelectionStale
		}
	}
	for recordPath, record := range snapshot.Records {
		if record.Kind != "request" || recordPath == requestPath {
			continue
		}
		var other selectionRequest
		if json.Unmarshal(record.Data, &other) != nil {
			return TransactionDecision{}, ErrSelectionIneligible
		}
		if other.ModuleID == input.ModuleID && other.Work != nil && other.Work.Phase == input.Phase && other.Work.ContextID == input.ContextID && other.Work.DesiredGeneration > input.DesiredGeneration {
			return TransactionDecision{}, ErrSelectionStale
		}
	}
	return TransactionDecision{
		ReadSet:   TransactionReadSet{Owners: []string{input.ModuleID}},
		Events:    events,
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
	if err := durable.Validate(); err != nil || durable.identity() != receipt.identity() {
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
		matches := receipt.ProposalKind == EvidenceProposalModule && receipt.ModuleID == input.ModuleID && receipt.AVMReleaseVersion == request.Work.AVMReleaseVersion && receipt.ContextID == input.ContextID && receipt.RequestID == request.ID && receipt.CompletionID == input.CompletionID && receipt.Phase == "assess" && receipt.DesiredGeneration == request.Work.DesiredGeneration && receipt.SourceSHA == request.SourceSHA && receipt.FactsID == request.Work.FactsID && receipt.AnalysisID == input.AnalysisID && receipt.InputFingerprint == request.Fingerprint && receipt.EvidenceCommit == input.EvidenceCommit && !receipt.AcceptedAt.After(input.SelectedAt)
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

func acceptedRequiredContextSet(snapshot Snapshot, request selectionRequest) (contextset.Set, error) {
	if request.Work == nil || request.Work.Phase != "assess" {
		return contextset.Set{}, ErrSelectionIneligible
	}
	var found *EvidenceAcceptanceReceipt
	for recordPath, record := range snapshot.Records {
		if record.Kind != "publication" {
			continue
		}
		var header struct {
			RecordType string `json:"record_type"`
		}
		if json.Unmarshal(record.Data, &header) != nil || header.RecordType != evidenceAcceptanceRecordType {
			continue
		}
		var receipt EvidenceAcceptanceReceipt
		if json.Unmarshal(record.Data, &receipt) != nil || receipt.Validate() != nil {
			return contextset.Set{}, ErrTransactionIdentityConflict
		}
		if receipt.Phase != "acquire_source" || receipt.ModuleID != request.ModuleID || receipt.SourceSHA != request.SourceSHA ||
			receipt.AVMReleaseVersion != request.Work.AVMReleaseVersion || receipt.FactsID != request.Work.FactsID {
			continue
		}
		if recordPath != evidenceAcceptancePrefix+receipt.ID+".json" || found != nil || receipt.RequiredContexts == nil {
			return contextset.Set{}, fmt.Errorf("facts acceptance receipt is noncanonical, duplicated, or missing its context set: %w", ErrTransactionIdentityConflict)
		}
		operationID := "evidence-acceptance:" + receipt.ID
		operationPath, pathErr := identity.StateOperationReceiptPath(request.ModuleID, operationID)
		if pathErr != nil {
			return contextset.Set{}, pathErr
		}
		operation, exists := operationReceiptRecord(snapshot, request.ModuleID, operationID)
		if !exists || operation.Validate(operationPath) != nil || verifyReceiptEvent(snapshot, operationID, operationPath) != nil {
			return contextset.Set{}, fmt.Errorf("facts acceptance operation receipt is invalid: %w", ErrTransactionIdentityConflict)
		}
		var result evidenceAcceptanceOperationResult
		if json.Unmarshal(operation.Result, &result) != nil {
			return contextset.Set{}, fmt.Errorf("facts acceptance result cannot be decoded: %w", ErrTransactionIdentityConflict)
		}
		if !sameEvidenceAcceptanceReceipt(result.Acceptance.Receipt, receipt) || len(result.Events) != 3 {
			return contextset.Set{}, fmt.Errorf("facts acceptance result identity or event count is invalid: %w", ErrTransactionIdentityConflict)
		}
		if verifyReceiptWriteSet(snapshot, operation, result.Events) != nil {
			for _, event := range result.Events {
				digest, digestErr := transactionEventDigest(event)
				historical, applied := appliedEventDigest(snapshot, event.ID)
				if digestErr != nil || !applied || historical != digest {
					return contextset.Set{}, fmt.Errorf("facts acceptance event %q lacks matching history (digest_error=%t applied=%t digest_match=%t): %w", event.ID, digestErr != nil, applied, historical == digest, ErrTransactionIdentityConflict)
				}
			}
			return contextset.Set{}, fmt.Errorf("facts acceptance write-set entries do not match the result: %w", ErrTransactionIdentityConflict)
		}
		receiptWriteProven := false
		for _, event := range result.Events {
			if event.RecordPath == recordPath && event.Record.Kind == "publication" && operationWriteSetHasEvent(snapshot, operation, event) == nil {
				receiptWriteProven = bytes.Equal(event.Record.Data, record.Data)
			}
		}
		if !receiptWriteProven {
			return contextset.Set{}, fmt.Errorf("facts acceptance receipt event does not match its operation proof: %w", ErrTransactionIdentityConflict)
		}
		copy := receipt
		found = &copy
	}
	if found == nil || found.RequiredContexts == nil {
		return contextset.Set{}, ErrSelectionIneligible
	}
	set, contextErr := assessmentContextSetForFactsReceipt(snapshot, *found)
	if contextErr != nil {
		return contextset.Set{}, contextErr
	}
	if err := set.Validate(); err != nil || set.ModuleID != request.ModuleID || set.SourceSHA != request.SourceSHA ||
		set.AVMReleaseVersion != request.Work.AVMReleaseVersion || set.FactsID != request.Work.FactsID {
		return contextset.Set{}, ErrSelectionIneligible
	}
	return set, nil
}

// Validate checks the durable evidence acceptance identity and its two phases.
func (receipt EvidenceAcceptanceReceipt) Validate() error {
	input := EvidenceAcceptanceInput{
		ProposalID: receipt.ProposalID, ProposalKind: receipt.ProposalKind, ModuleID: receipt.ModuleID,
		ContextID: receipt.ContextID, RequestID: receipt.RequestID, CompletionID: receipt.CompletionID,
		Phase: receipt.Phase, DesiredGeneration: receipt.DesiredGeneration, SourceSHA: receipt.SourceSHA,
		AVMReleaseVersion: receipt.AVMReleaseVersion,
		FactsID:           receipt.FactsID, AnalysisID: receipt.AnalysisID, InputFingerprint: receipt.InputFingerprint,
		ArtifactDigest: receipt.ArtifactDigest, EvidenceCommit: receipt.EvidenceCommit, AcceptedAt: receipt.AcceptedAt,
		RequiredContexts: cloneRequiredContexts(receipt.RequiredContexts),
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
	if request.ID != input.RequestID || request.ModuleID != input.ModuleID || request.Status != "awaiting_acceptance" || request.Work == nil || request.Work.Phase != input.Phase || request.Work.AVMReleaseVersion != input.AVMReleaseVersion || request.Work.DesiredGeneration != input.DesiredGeneration || request.SourceSHA != input.SourceSHA || request.Fingerprint != input.InputFingerprint || request.Work.Completion == nil {
		return ErrSelectionStale
	}
	completion := request.Work.Completion
	if completion.ID != input.CompletionID || completion.RequestID != request.ID || completion.InputFingerprint != request.Fingerprint || completion.DesiredGeneration != request.Work.DesiredGeneration || completion.ArtifactDigest != input.ArtifactDigest || completion.SealedAt.IsZero() || input.AcceptedAt.Before(completion.SealedAt) {
		return ErrSelectionStale
	}
	if input.Phase == "assess" {
		if request.Work.ContextID != input.ContextID || request.Work.FactsID != input.FactsID || identity.ValidateRecordID("analysis", input.AnalysisID) != nil || input.RequiredContexts != nil {
			return ErrSelectionIneligible
		}
	} else if input.ContextID != "" || input.AnalysisID != "" || request.Work.Phase != "acquire_source" || request.Work.AVMReleaseVersion != input.AVMReleaseVersion {
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
	if identity.ValidateRecordID("proposal", input.ProposalID) != nil || input.ProposalKind != EvidenceProposalModule || !selectionModulePattern.MatchString(input.ModuleID) || input.RequestID == "" || identity.ValidateRecordID("completion", input.CompletionID) != nil || input.DesiredGeneration == 0 || !selectionSHA1Pattern.MatchString(input.SourceSHA) || identity.ValidateRecordID("facts", input.FactsID) != nil || !selectionSHA256Pattern.MatchString(input.InputFingerprint) || !selectionSHA256Pattern.MatchString(input.ArtifactDigest) || !selectionSHA1Pattern.MatchString(input.EvidenceCommit) || input.AcceptedAt.IsZero() || !selectionReleasePattern.MatchString(input.AVMReleaseVersion) {
		return fmt.Errorf("evidence acceptance input is incomplete or invalid")
	}
	switch input.Phase {
	case "assess":
		if identity.ValidateRelativePath(input.ContextID, false) != nil || identity.ValidateRecordID("analysis", input.AnalysisID) != nil || input.RequiredContexts != nil {
			return fmt.Errorf("assessment evidence identity is invalid")
		}
	case "acquire_source":
		if input.ContextID != "" || input.AnalysisID != "" || input.RequiredContexts == nil {
			return fmt.Errorf("facts-only evidence requires a context set and must omit context and analysis IDs")
		}
		if err := input.RequiredContexts.Validate(); err != nil || input.RequiredContexts.ModuleID != input.ModuleID || input.RequiredContexts.SourceSHA != input.SourceSHA || input.RequiredContexts.AVMReleaseVersion != input.AVMReleaseVersion || input.RequiredContexts.FactsID != input.FactsID {
			return fmt.Errorf("facts-only required context set does not match the accepted facts identity")
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
	record := Record{Kind: kind, SchemaVersion: evidenceAcceptanceEventSchemaVersion, Data: data}
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
		AVMReleaseVersion: receipt.AVMReleaseVersion,
		FactsID:           receipt.FactsID, AnalysisID: receipt.AnalysisID, InputFingerprint: receipt.InputFingerprint,
		ArtifactDigest: receipt.ArtifactDigest, EvidenceCommit: receipt.EvidenceCommit,
		RequiredContextSetID: requiredContextSetID(receipt.RequiredContexts),
	}
}

func cloneRequiredContexts(contexts *contextset.Set) *contextset.Set {
	if contexts == nil {
		return nil
	}
	cloned := *contexts
	cloned.Contexts = append([]contextset.Context(nil), contexts.Contexts...)
	return &cloned
}

func requiredContextSetID(contexts *contextset.Set) string {
	if contexts == nil {
		return ""
	}
	return contexts.ID
}

func sameEvidenceAcceptanceReceipt(left, right EvidenceAcceptanceReceipt) bool {
	return left.identity() == right.identity() && left.AcceptedAt.Equal(right.AcceptedAt)
}
