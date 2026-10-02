package state

import (
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
	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

const (
	selectionRecordSchemaVersion = 1
	selectionSchemaVersion       = 2
	selectionRecordType          = "selection_receipt"
	selectionExportRecordType    = "selection_export_intent"
	selectionStatePrefix         = "state/selections/"
	selectionExportPrefix        = "state/exports/"
	ExportPending                = "pending"
	ExportMerged                 = "merged"
)

var (
	ErrSelectionStale        = errors.New("selection does not match current desired work")
	ErrSelectionIneligible   = errors.New("selection is not currently eligible")
	ErrSelectionNotFound     = errors.New("current selection was not found")
	ErrSelectionStateMissing = errors.New("selection state is missing; recovery review is required")
	selectionModulePattern   = regexp.MustCompile(`^avm-(res|ptn|utl)-[a-z0-9]+(?:-[a-z0-9]+)*$`)
	selectionSHA1Pattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	selectionSHA256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// SelectionInput identifies one accepted assessment whose sealed completion
// may become the selected result for its module and context.
type SelectionInput struct {
	ModuleID       string
	ContextID      string
	RequestID      string
	CompletionID   string
	AnalysisID     string
	EvidenceCommit string
	SelectedAt     time.Time
}

// DeselectRetiredInput creates a higher-sequence null selection only after
// durable discovery confirms the module is retired.
type DeselectRetiredInput struct {
	ModuleID          string
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
	Commit string `json:"commit"`
}

// SelectAccepted validates the current catalog and assessment request at one
// snapshot, then commits the receipt, context pointer, and export outbox as one
// expected-head transaction. It deliberately does not inspect leases in other
// contexts for the same module.
func SelectAccepted(ctx context.Context, backend Backend, expectedHead string, input SelectionInput) (SelectionResult, error) {
	if expectedHead == "" {
		return SelectionResult{}, fmt.Errorf("expected state head is required")
	}
	head, snapshot, err := ReadSnapshot(ctx, backend)
	if err != nil {
		return SelectionResult{}, err
	}
	if head != expectedHead {
		return SelectionResult{}, ErrHeadMoved
	}
	request, err := validateSelectionInput(snapshot, input)
	if err != nil {
		return SelectionResult{}, err
	}
	pointerPath := selectionPointerPath(input.ModuleID, input.ContextID)
	sequence, retryResult, err := selectionSequence(snapshot, pointerPath, head, input, request)
	if err != nil {
		return SelectionResult{}, err
	}
	if retryResult != nil {
		return *retryResult, nil
	}
	receipt, err := buildSelectionReceipt(input, request, sequence)
	if err != nil {
		return SelectionResult{}, err
	}
	if err = receipt.Validate(); err != nil {
		return SelectionResult{}, err
	}
	selectionPath := selectionStatePrefix + input.ModuleID + "/" + input.ContextID + "/" + receipt.ID + ".json"
	export := SelectionExport{
		SchemaVersion: selectionRecordSchemaVersion, RecordType: selectionExportRecordType,
		ID: receipt.ID, SelectionID: receipt.ID, ModuleID: input.ModuleID,
		ContextID: input.ContextID, Status: ExportPending, Receipt: receipt,
		CreatedAt: input.SelectedAt.UTC(),
	}
	pointer := SelectionPointerRecord{
		SchemaVersion: selectionRecordSchemaVersion, ModuleID: input.ModuleID,
		ContextID: input.ContextID, Sequence: sequence, SelectionID: receipt.ID,
		RequestID: input.RequestID, CompletionID: input.CompletionID,
		DesiredGeneration: request.Work.DesiredGeneration, UpdatedAt: input.SelectedAt.UTC(),
	}
	events, err := selectionEvents(selectionPath, pointerPath, receipt, pointer, export)
	if err != nil {
		return SelectionResult{}, err
	}
	newHead, _, err := ApplyEventsAtHead(ctx, backend, expectedHead, events)
	if err != nil {
		return SelectionResult{}, err
	}
	return SelectionResult{Head: newHead, Receipt: receipt, Export: export, Changed: true}, nil
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
func DeselectRetired(ctx context.Context, backend Backend, expectedHead string, input DeselectRetiredInput) (SelectionResult, error) {
	if err := validateDeselectRetiredInput(expectedHead, input); err != nil {
		return SelectionResult{}, err
	}
	head, snapshot, err := ReadSnapshot(ctx, backend)
	if err != nil {
		return SelectionResult{}, err
	}
	if head != expectedHead {
		return SelectionResult{}, ErrHeadMoved
	}
	if err = validateRetiredCatalogModule(snapshot, input.ModuleID); err != nil {
		return SelectionResult{}, err
	}
	pointerPath := selectionPointerPath(input.ModuleID, input.ContextID)
	previous, exists, err := readSelectionPointer(snapshot, pointerPath)
	if err != nil {
		return SelectionResult{}, err
	}
	if !exists {
		return SelectionResult{}, ErrSelectionNotFound
	}
	previousReceipt, err := readSelectionReceipt(snapshot, input.ModuleID, input.ContextID, previous.SelectionID)
	if err != nil {
		return SelectionResult{}, err
	}
	if previousReceipt.AnalysisID == nil && sameDeselectRetiredRequest(previousReceipt, input) {
		return existingDeselectionResult(head, snapshot, previous, previousReceipt)
	}
	if input.DesiredGeneration <= previous.DesiredGeneration {
		return SelectionResult{}, ErrSelectionStale
	}
	if previous.Sequence == math.MaxUint64 {
		return SelectionResult{}, fmt.Errorf("selection sequence is exhausted")
	}
	receipt := SelectionReceipt{
		SchemaVersion: selectionSchemaVersion, RecordType: selectionRecordType,
		ModuleID: input.ModuleID, ContextID: input.ContextID,
		Sequence: previous.Sequence + 1, DesiredGeneration: input.DesiredGeneration,
		InputFingerprint: input.InputFingerprint, EvidenceCommit: input.EvidenceCommit,
		SelectedAt: input.SelectedAt.UTC(),
	}
	receipt.ID, err = identity.ID("selection", selectionSchemaVersion, selectionIdentity{
		SchemaVersion: receipt.SchemaVersion, RecordType: receipt.RecordType, ModuleID: receipt.ModuleID,
		ContextID: receipt.ContextID, Sequence: receipt.Sequence, DesiredGeneration: receipt.DesiredGeneration,
		SourceSHA: nil, FactsID: nil, InputFingerprint: receipt.InputFingerprint,
		AnalysisID: nil, EvidenceCommit: receipt.EvidenceCommit, SelectedAt: receipt.SelectedAt,
	})
	if err != nil {
		return SelectionResult{}, err
	}
	if err = receipt.Validate(); err != nil {
		return SelectionResult{}, err
	}
	pointer := SelectionPointerRecord{
		SchemaVersion: selectionRecordSchemaVersion, ModuleID: input.ModuleID,
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
		return SelectionResult{}, err
	}
	newHead, _, err := ApplyEventsAtHead(ctx, backend, expectedHead, events)
	if err != nil {
		return SelectionResult{}, err
	}
	return SelectionResult{Head: newHead, Receipt: receipt, Export: export, Changed: true}, nil
}

func validateDeselectRetiredInput(expectedHead string, input DeselectRetiredInput) error {
	if expectedHead == "" || !selectionModulePattern.MatchString(input.ModuleID) || identity.ValidateRelativePath(input.ContextID, false) != nil || input.DesiredGeneration == 0 || !selectionSHA256Pattern.MatchString(input.InputFingerprint) || !selectionSHA1Pattern.MatchString(input.EvidenceCommit) || input.SelectedAt.IsZero() {
		return fmt.Errorf("retirement deselection identity is invalid")
	}
	return nil
}

func sameDeselectRetiredRequest(receipt SelectionReceipt, input DeselectRetiredInput) bool {
	return receipt.ModuleID == input.ModuleID && receipt.ContextID == input.ContextID && receipt.DesiredGeneration == input.DesiredGeneration && receipt.InputFingerprint == input.InputFingerprint && receipt.EvidenceCommit == input.EvidenceCommit
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
func ImportSelectionReceipt(ctx context.Context, backend Backend, expectedHead string, receipt SelectionReceipt, mergedCommit string, importedAt time.Time) (SelectionResult, error) {
	if expectedHead == "" || !selectionSHA1Pattern.MatchString(mergedCommit) || importedAt.IsZero() {
		return SelectionResult{}, fmt.Errorf("selection merge identity and import timestamp are required")
	}
	if err := receipt.Validate(); err != nil {
		return SelectionResult{}, err
	}
	head, snapshot, err := ReadSnapshot(ctx, backend)
	if err != nil {
		return SelectionResult{}, err
	}
	if head != expectedHead {
		return SelectionResult{}, ErrHeadMoved
	}
	exportPath := selectionExportPrefix + receipt.ID + ".json"
	export, err := readSelectionExport(snapshot, exportPath, receipt)
	if err != nil {
		return SelectionResult{}, err
	}
	if export.Status == ExportMerged {
		return SelectionResult{Head: head, Receipt: export.Receipt, Export: export}, nil
	}
	export.Status = ExportMerged
	export.MergedCommit = mergedCommit
	export.MergedAt = importedAt.UTC()
	return commitSelectionExport(ctx, backend, expectedHead, exportPath, export)
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

func commitSelectionExport(ctx context.Context, backend Backend, expectedHead, exportPath string, export SelectionExport) (SelectionResult, error) {
	data, err := json.Marshal(export)
	if err != nil {
		return SelectionResult{}, fmt.Errorf("encode merged selection export: %w", err)
	}
	eventRecord := Record{Kind: "export", SchemaVersion: selectionRecordSchemaVersion, Data: data}
	eventID, err := identity.ID("state-event", 1, selectionEventIdentity{Path: exportPath, Record: eventRecord})
	if err != nil {
		return SelectionResult{}, fmt.Errorf("identify merged selection export: %w", err)
	}
	newHead, _, err := ApplyEventsAtHead(ctx, backend, expectedHead, []Event{{ID: eventID, RecordPath: exportPath, Record: eventRecord}})
	if err != nil {
		return SelectionResult{}, err
	}
	return SelectionResult{Head: newHead, Receipt: export.Receipt, Export: export, Changed: true}, nil
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
		if record.Kind != "selection" || !strings.HasSuffix(recordPath, "/current.json") {
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
		SchemaVersion: receipt.SchemaVersion, RecordType: receipt.RecordType, ModuleID: receipt.ModuleID,
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
	if request.ID != input.RequestID || request.ModuleID != input.ModuleID || request.Status != "accepted" || request.Work == nil || request.Work.Phase != "assess" || request.Work.ContextID != input.ContextID || request.Work.Candidate != "eligible" || request.Work.Applicability != "applicable" || request.Work.Completion == nil {
		return selectionRequest{}, ErrSelectionIneligible
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
	return request, nil
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
		TrackCandidates map[string]selectionSourcePin `json:"track_candidates"`
	}
	if err := json.Unmarshal(record.Data, &history); err != nil {
		return fmt.Errorf("decode current source pin history: %w", err)
	}
	if history.SchemaVersion != 1 || history.ModuleID != request.ModuleID {
		return fmt.Errorf("current source pin history does not match selected module")
	}
	if request.Work.ContextID == "latest-source" {
		if history.OverallLatest != nil && history.OverallLatest.Commit != request.SourceSHA {
			return ErrSelectionStale
		}
		return nil
	}
	for _, contextID := range []string{request.Work.ContextID, request.Work.Track} {
		if pin, found := history.TrackCandidates[contextID]; found && pin.Commit != request.SourceSHA {
			return ErrSelectionStale
		}
	}
	return nil
}

func validateImportedInputs(snapshot Snapshot, request selectionRequest) error {
	moduleState, err := readAssessmentModuleState(snapshot, request.ModuleID)
	if err != nil {
		return err
	}
	contextState, exists := moduleState.Contexts[request.Work.ContextID]
	if !exists {
		return nil
	}
	if request.Work.DesiredGeneration < contextState.DesiredGeneration {
		return ErrSelectionStale
	}
	for _, imported := range contextState.Inputs {
		var present bool
		if imported.Kind == InputProviderSnapshot {
			present = containsSelectionString(request.Work.SnapshotIDs, imported.InputID)
		} else if imported.Kind == InputAssessmentPolicy {
			present = containsSelectionString(request.Work.DefinitionIDs, imported.InputID)
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
		ModuleID: input.ModuleID, ContextID: input.ContextID, Sequence: sequence,
		DesiredGeneration: request.Work.DesiredGeneration, SourceSHA: &sourceSHA, FactsID: &factsID,
		InputFingerprint: request.Fingerprint, AnalysisID: &analysisID,
		CompletionID: &completionID, EvidenceCommit: input.EvidenceCommit, SelectedAt: input.SelectedAt.UTC(),
	}
	var err error
	receipt.ID, err = identity.ID("selection", selectionSchemaVersion, selectionIdentity{
		SchemaVersion: receipt.SchemaVersion, RecordType: receipt.RecordType, ModuleID: receipt.ModuleID,
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
		eventID, err := identity.ID("state-event", 1, selectionEventIdentity{Path: value.path, Record: record})
		if err != nil {
			return nil, fmt.Errorf("identify %s selection event: %w", value.kind, err)
		}
		events = append(events, Event{ID: eventID, RecordPath: value.path, Record: record})
	}
	return events, nil
}

func selectionPointerPath(moduleID, contextID string) string {
	return selectionStatePrefix + moduleID + "/" + contextID + "/current.json"
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
	if pointer.SchemaVersion != selectionRecordSchemaVersion || pointer.ModuleID == "" || pointer.ContextID == "" || pointer.Sequence == 0 || pointer.SelectionID == "" || pointer.DesiredGeneration == 0 || pointer.UpdatedAt.IsZero() || (pointer.RequestID == "") != (pointer.CompletionID == "") {
		return SelectionPointerRecord{}, false, fmt.Errorf("selection pointer is incomplete")
	}
	if pointer.RequestID == "" {
		receipt, receiptErr := readSelectionReceipt(snapshot, pointer.ModuleID, pointer.ContextID, pointer.SelectionID)
		if receiptErr != nil || receipt.AnalysisID != nil || receipt.SourceSHA != nil || receipt.FactsID != nil || receipt.CompletionID != nil || receipt.Sequence != pointer.Sequence || receipt.DesiredGeneration != pointer.DesiredGeneration {
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
	if receipt.CompletionID == nil || *receipt.CompletionID != input.CompletionID || receipt.AnalysisID == nil || *receipt.AnalysisID != input.AnalysisID || receipt.EvidenceCommit != input.EvidenceCommit {
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
