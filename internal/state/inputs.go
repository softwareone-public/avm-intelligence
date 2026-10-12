package state

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/contextset"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
)

const (
	InputProviderSnapshot    = "provider_snapshot"
	InputAssessmentPolicy    = "assessment_policy"
	AssessmentPolicyInputKey = "assessment-policy"
	assessmentInputsPrefix   = "state/inputs/"
	assessmentModulesPrefix  = "state/modules/"
	assessmentImportKind     = "input-import"
)

// AssessmentInputTarget scopes an imported input to one module and context.
type AssessmentInputTarget struct {
	ModuleID          string `json:"module_id"`
	AVMReleaseVersion string `json:"avm_release_version"`
	ContextID         string `json:"context_id"`
}

// AssessmentInputImport declares a newly accepted provider snapshot or
// assessment policy and the contexts whose future selections it invalidates.
type AssessmentInputImport struct {
	Kind           string                        `json:"kind"`
	InputKey       string                        `json:"input_key"`
	InputID        string                        `json:"input_id"`
	Targets        []AssessmentInputTarget       `json:"targets"`
	AcceptedCommit string                        `json:"accepted_commit"`
	Producer       projection.WorkflowProvenance `json:"producer"`
	ImportedAt     time.Time                     `json:"imported_at"`
}

// AssessmentInputReference identifies the currently accepted input for one
// provider address or for the shared assessment policy.
type AssessmentInputReference struct {
	Kind     string `json:"kind"`
	InputKey string `json:"input_key"`
	InputID  string `json:"input_id"`
}

// AssessmentContextState stores the generation high-water mark and current
// imported inputs for one assessment context.
type AssessmentContextState struct {
	DesiredGeneration uint64                     `json:"desired_generation"`
	Inputs            []AssessmentInputReference `json:"inputs"`
}

// AssessmentModuleState contains input invalidation state independent from
// compute leases and selection history.
type AssessmentModuleState struct {
	SchemaVersion int                                          `json:"schema_version"`
	ModuleID      string                                       `json:"module_id"`
	Versions      map[string]map[string]AssessmentContextState `json:"versions"`
}

// AssessmentInputImportRecord is the immutable receipt for one input import.
type AssessmentInputImportRecord struct {
	SchemaVersion  int                               `json:"schema_version"`
	RecordType     string                            `json:"record_type"`
	ID             string                            `json:"input_import_id"`
	Kind           string                            `json:"kind"`
	InputKey       string                            `json:"input_key"`
	InputID        string                            `json:"input_id"`
	AcceptedCommit string                            `json:"accepted_commit"`
	Producer       projection.WorkflowProvenance     `json:"producer"`
	Targets        []AssessmentInputTargetGeneration `json:"targets"`
	WriteSet       []OperationWriteSetEntry          `json:"write_set"`
	ImportedAt     time.Time                         `json:"imported_at"`
}

// AssessmentInputTargetGeneration records the generation advanced for one
// target context by an imported input.
type AssessmentInputTargetGeneration struct {
	ModuleID          string `json:"module_id"`
	AVMReleaseVersion string `json:"avm_release_version"`
	ContextID         string `json:"context_id"`
	DesiredGeneration uint64 `json:"desired_generation"`
}

// AssessmentInputImportResult returns the atomic state commit and generation
// assignments. Replaying the same import returns Changed=false.
type AssessmentInputImportResult struct {
	Head        string                      `json:"head"`
	Import      AssessmentInputImportRecord `json:"import"`
	Generations map[string]uint64           `json:"generations"`
	Changed     bool                        `json:"changed"`
}

type assessmentInputImportIdentity struct {
	Kind             string                  `json:"kind"`
	InputKey         string                  `json:"input_key"`
	InputID          string                  `json:"input_id"`
	Targets          []AssessmentInputTarget `json:"targets"`
	AcceptedCommit   string                  `json:"accepted_commit"`
	ProducerWorkflow string                  `json:"producer_workflow"`
	ProducerCommit   string                  `json:"producer_commit"`
}

// ProviderSnapshotAssessmentTargets finds accepted module-version contexts
// whose durable facts-acceptance receipt requires this provider major. Every
// receipt must retain its operation proof; imported provider data alone cannot
// invent or broaden a context set.
func ProviderSnapshotAssessmentTargets(snapshot Snapshot, providerAddress string, providerMajor uint64) ([]AssessmentInputTarget, error) {
	if identity.ValidateRelativePath(providerAddress, false) != nil || len(strings.Split(providerAddress, "/")) != 3 {
		return nil, fmt.Errorf("provider snapshot target identity is invalid")
	}
	targets := make([]AssessmentInputTarget, 0)
	seenReceipts := make(map[string]struct{})
	seenTargets := make(map[string]struct{})
	for recordPath, record := range snapshot.Records {
		if record.Kind != "publication" {
			continue
		}
		var header struct {
			RecordType string `json:"record_type"`
		}
		if err := json.Unmarshal(record.Data, &header); err != nil {
			return nil, fmt.Errorf("decode publication receipt %s: %w", recordPath, err)
		}
		if header.RecordType != evidenceAcceptanceRecordType {
			continue
		}
		var receipt EvidenceAcceptanceReceipt
		if err := json.Unmarshal(record.Data, &receipt); err != nil || receipt.Validate() != nil {
			return nil, fmt.Errorf("provider target facts acceptance receipt is invalid")
		}
		if receipt.Phase != "acquire_source" || receipt.ProposalKind != EvidenceProposalModule {
			continue
		}
		if recordPath != evidenceAcceptancePrefix+receipt.ID+".json" || receipt.RequiredContexts == nil {
			return nil, fmt.Errorf("provider target facts acceptance receipt is noncanonical or lacks required contexts")
		}
		if _, duplicate := seenReceipts[receipt.ID]; duplicate {
			return nil, fmt.Errorf("provider target facts acceptance receipt is duplicated")
		}
		seenReceipts[receipt.ID] = struct{}{}
		if err := verifyAcceptedFactsReceipt(snapshot, receipt, record); err != nil {
			return nil, err
		}
		receiptTargets, targetErr := providerSnapshotTargetsForReceipt(receipt, providerAddress, providerMajor)
		if targetErr != nil {
			return nil, targetErr
		}
		for _, target := range receiptTargets {
			key := target.ModuleID + "\x00" + target.AVMReleaseVersion + "\x00" + target.ContextID
			if _, duplicate := seenTargets[key]; duplicate {
				return nil, fmt.Errorf("provider target context has duplicate accepted facts receipts")
			}
			seenTargets[key] = struct{}{}
			targets = append(targets, target)
		}
	}
	sort.Slice(targets, func(left, right int) bool {
		leftKey := targets[left].ModuleID + "\x00" + targets[left].AVMReleaseVersion + "\x00" + targets[left].ContextID
		rightKey := targets[right].ModuleID + "\x00" + targets[right].AVMReleaseVersion + "\x00" + targets[right].ContextID
		return leftKey < rightKey
	})
	return targets, nil
}

func providerSnapshotTargetsForReceipt(receipt EvidenceAcceptanceReceipt, providerAddress string, providerMajor uint64) ([]AssessmentInputTarget, error) {
	set := receipt.RequiredContexts
	if set == nil || set.Validate() != nil || set.ModuleID != receipt.ModuleID || set.SourceSHA != receipt.SourceSHA ||
		set.AVMReleaseVersion != receipt.AVMReleaseVersion || set.FactsID != receipt.FactsID {
		return nil, fmt.Errorf("provider target required-context set does not match accepted facts")
	}
	targets := make([]AssessmentInputTarget, 0)
	for _, context := range set.Contexts {
		if context.ProviderAddress != providerAddress || context.ProviderMajor != providerMajor ||
			context.State.Applicability != contextset.ApplicabilityApplicable || context.State.Candidate != contextset.CandidateEligible {
			continue
		}
		targets = append(targets, AssessmentInputTarget{
			ModuleID: receipt.ModuleID, AVMReleaseVersion: receipt.AVMReleaseVersion, ContextID: context.State.ContextID,
		})
	}
	return targets, nil
}

func verifyAcceptedFactsReceipt(snapshot Snapshot, receipt EvidenceAcceptanceReceipt, record Record) error {
	operationID := "evidence-acceptance:" + receipt.ID
	operationPath, err := identity.StateOperationReceiptPath(receipt.ModuleID, operationID)
	if err != nil {
		return err
	}
	operation, exists := operationReceiptRecord(snapshot, receipt.ModuleID, operationID)
	if !exists || operation.Validate(operationPath) != nil || verifyReceiptEvent(snapshot, operationID, operationPath) != nil {
		return fmt.Errorf("provider target facts acceptance operation proof is missing or invalid")
	}
	var result evidenceAcceptanceOperationResult
	if json.Unmarshal(operation.Result, &result) != nil || !sameEvidenceAcceptanceReceipt(result.Acceptance.Receipt, receipt) || len(result.Events) != 3 {
		return fmt.Errorf("provider target facts acceptance operation result is invalid")
	}
	if err = verifyReceiptWriteSet(snapshot, operation, result.Events); err != nil {
		return fmt.Errorf("provider target facts acceptance write-set proof is invalid: %w", err)
	}
	for _, event := range result.Events {
		if event.RecordPath == evidenceAcceptancePrefix+receipt.ID+".json" && event.Record.Kind == "publication" && operationWriteSetHasEvent(snapshot, operation, event) == nil {
			if string(event.Record.Data) == string(record.Data) {
				return nil
			}
			return fmt.Errorf("provider target facts acceptance receipt differs from its operation proof")
		}
	}
	return fmt.Errorf("provider target facts acceptance operation does not prove its receipt")
}

// ImportAssessmentInputs advances each targeted context once in the same CAS
// transaction that records the immutable import receipt. It does not enqueue
// work; the site-cycle assessment planner consumes durable generations and
// exact accepted input IDs when it builds requests.
func ImportAssessmentInputs(ctx context.Context, coordinator TransactionCoordinator, input AssessmentInputImport) (AssessmentInputImportResult, error) {
	if err := validateAssessmentInputImport(input); err != nil {
		return AssessmentInputImportResult{}, err
	}
	if coordinator == nil {
		return AssessmentInputImportResult{}, fmt.Errorf("state transaction coordinator is required")
	}
	// Use one canonical representation for an import with no dependent modules.
	// The persisted receipt encodes its empty target list as [], so the stable ID
	// must use the same value rather than distinguishing nil from empty.
	if input.Targets == nil {
		input.Targets = []AssessmentInputTarget{}
	}
	input.ImportedAt = input.ImportedAt.UTC()
	importID, err := identity.ID(assessmentImportKind, 1, assessmentInputImportIdentity{
		Kind: input.Kind, InputKey: input.InputKey, InputID: input.InputID,
		Targets: input.Targets, AcceptedCommit: input.AcceptedCommit,
		ProducerWorkflow: input.Producer.WorkflowID, ProducerCommit: input.Producer.Commit,
	})
	if err != nil {
		return AssessmentInputImportResult{}, err
	}
	importPath := assessmentInputsPrefix + importID + ".json"
	operationID := "assessment-input:" + importID
	result, err := coordinator.RunTransaction(ctx, operationID, func(snapshot Snapshot) (TransactionDecision, error) {
		previous, found, readErr := readAssessmentInputImport(snapshot, importPath, importID)
		if readErr != nil {
			return TransactionDecision{}, readErr
		}
		if found {
			return TransactionDecision{Noop: true, Result: AssessmentInputImportResult{Import: previous, Generations: importGenerations(previous)}}, nil
		}
		states, targetGenerations, advanceErr := advanceAssessmentTargets(snapshot, input)
		if advanceErr != nil {
			return TransactionDecision{}, advanceErr
		}
		moduleEvents, eventErr := assessmentModuleEvents(states)
		if eventErr != nil {
			return TransactionDecision{}, eventErr
		}
		importRecord := AssessmentInputImportRecord{
			SchemaVersion: 2, RecordType: "assessment_input_import", ID: importID,
			Kind: input.Kind, InputKey: input.InputKey, InputID: input.InputID,
			AcceptedCommit: input.AcceptedCommit, Producer: input.Producer,
			Targets: targetGenerations, WriteSet: assessmentInputWriteSet(moduleEvents), ImportedAt: input.ImportedAt,
		}
		if err = importRecord.Validate(); err != nil {
			return TransactionDecision{}, err
		}
		importEvent, eventErr := assessmentInputEvent(importPath, "input_import", importRecord)
		if eventErr != nil {
			return TransactionDecision{}, eventErr
		}
		importEvent.ID = operationID
		events := append([]Event{importEvent}, moduleEvents...)
		return TransactionDecision{Events: events, Result: AssessmentInputImportResult{Import: importRecord, Generations: importGenerations(importRecord), Changed: true}, Candidate: CandidateOptions{Subject: "state(inputs): import assessment inputs", Provenance: GitProvenance{Operation: "evidence.accept", Event: operationID}}}, nil
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		previous, found, readErr := readAssessmentInputImport(snapshot, importPath, importID)
		return TransactionResolution{Found: found, Value: AssessmentInputImportResult{Import: previous, Generations: importGenerations(previous)}}, readErr
	})
	if err != nil {
		return AssessmentInputImportResult{}, err
	}
	resolved, ok := result.Value.(AssessmentInputImportResult)
	if !ok {
		return AssessmentInputImportResult{}, fmt.Errorf("assessment input import result has an invalid type")
	}
	resolved.Head, resolved.Changed = result.Head, result.Changed
	return resolved, nil
}

func readAssessmentInputImport(snapshot Snapshot, recordPath, importID string) (AssessmentInputImportRecord, bool, error) {
	record, exists := snapshot.Records[recordPath]
	if !exists {
		return AssessmentInputImportRecord{}, false, nil
	}
	if record.Kind != "input_import" || record.SchemaVersion != 1 {
		return AssessmentInputImportRecord{}, false, fmt.Errorf("existing assessment input import has invalid state")
	}
	var previous AssessmentInputImportRecord
	if err := json.Unmarshal(record.Data, &previous); err != nil {
		return AssessmentInputImportRecord{}, false, fmt.Errorf("decode existing assessment input import: %w", err)
	}
	if err := previous.Validate(); err != nil || previous.ID != importID {
		return AssessmentInputImportRecord{}, false, fmt.Errorf("existing assessment input import conflicts with content identity")
	}
	operationID := "assessment-input:" + importID
	operationEvent := Event{ID: operationID, RecordPath: recordPath, Record: record}
	digest, err := transactionEventDigest(operationEvent)
	if err != nil {
		return AssessmentInputImportRecord{}, false, ErrTransactionIdentityConflict
	}
	if historical, applied := appliedEventDigest(snapshot, operationID); !applied || historical != digest {
		return AssessmentInputImportRecord{}, false, ErrTransactionIdentityConflict
	}
	for _, entry := range previous.WriteSet {
		historical, applied := appliedEventDigest(snapshot, entry.EventID)
		if !applied || historical != entry.Digest {
			return AssessmentInputImportRecord{}, false, ErrTransactionIdentityConflict
		}
	}
	return previous, true, nil
}

func advanceAssessmentTargets(snapshot Snapshot, input AssessmentInputImport) (map[string]AssessmentModuleState, []AssessmentInputTargetGeneration, error) {
	states := make(map[string]AssessmentModuleState)
	targetGenerations := make([]AssessmentInputTargetGeneration, 0, len(input.Targets))
	for _, target := range input.Targets {
		moduleState, exists := states[target.ModuleID]
		if !exists {
			if err := validateActiveCatalogModule(snapshot, target.ModuleID); err != nil {
				return nil, nil, err
			}
			var err error
			moduleState, err = readAssessmentModuleState(snapshot, target.ModuleID)
			if err != nil {
				return nil, nil, err
			}
		}
		contexts := moduleState.Versions[target.AVMReleaseVersion]
		if contexts == nil {
			contexts = make(map[string]AssessmentContextState)
		}
		contextState := contexts[target.ContextID]
		latestRequestGeneration, err := latestAssessmentGeneration(snapshot, target.ModuleID, target.AVMReleaseVersion, target.ContextID)
		if err != nil {
			return nil, nil, err
		}
		baseGeneration := max(contextState.DesiredGeneration, latestRequestGeneration)
		updatedInputs := replaceAssessmentInput(contextState.Inputs, AssessmentInputReference{Kind: input.Kind, InputKey: input.InputKey, InputID: input.InputID})
		inputChanged := !sameAssessmentInputReferences(contextState.Inputs, updatedInputs)
		if inputChanged && baseGeneration == math.MaxUint64 {
			return nil, nil, fmt.Errorf("assessment generation is exhausted for %s/%s", target.ModuleID, target.ContextID)
		}
		contextState.DesiredGeneration = baseGeneration
		if inputChanged {
			contextState.DesiredGeneration++
		}
		contextState.Inputs = updatedInputs
		contexts[target.ContextID] = contextState
		moduleState.Versions[target.AVMReleaseVersion] = contexts
		states[target.ModuleID] = moduleState
		targetGenerations = append(targetGenerations, AssessmentInputTargetGeneration{ModuleID: target.ModuleID, AVMReleaseVersion: target.AVMReleaseVersion, ContextID: target.ContextID, DesiredGeneration: contextState.DesiredGeneration})
	}
	return states, targetGenerations, nil
}

func sameAssessmentInputReferences(left, right []AssessmentInputReference) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func assessmentModuleEvents(states map[string]AssessmentModuleState) ([]Event, error) {
	events := make([]Event, 0, len(states))
	moduleIDs := make([]string, 0, len(states))
	for moduleID := range states {
		moduleIDs = append(moduleIDs, moduleID)
	}
	sort.Strings(moduleIDs)
	for _, moduleID := range moduleIDs {
		moduleState := states[moduleID]
		if err := moduleState.Validate(); err != nil {
			return nil, err
		}
		event, eventErr := assessmentInputEvent(assessmentModulesPrefix+moduleID+".json", "module", moduleState)
		if eventErr != nil {
			return nil, eventErr
		}
		events = append(events, event)
	}
	return events, nil
}

// Validate checks persisted module generations and their current input set.
func (moduleState AssessmentModuleState) Validate() error {
	if moduleState.SchemaVersion != 2 || !selectionModulePattern.MatchString(moduleState.ModuleID) || moduleState.Versions == nil {
		return fmt.Errorf("assessment module state identity is invalid")
	}
	for release, contexts := range moduleState.Versions {
		if !selectionReleasePattern.MatchString(release) || contexts == nil {
			return fmt.Errorf("assessment AVM release state is invalid")
		}
		for contextID, contextState := range contexts {
			if identity.ValidateRelativePath(contextID, false) != nil || contextState.DesiredGeneration == 0 {
				return fmt.Errorf("assessment context generation is invalid")
			}
			previous := ""
			for _, input := range contextState.Inputs {
				if err := validateAssessmentInputReference(input); err != nil {
					return err
				}
				key := input.Kind + "\x00" + input.InputKey
				if previous != "" && key <= previous {
					return fmt.Errorf("assessment context inputs must be unique and sorted")
				}
				previous = key
			}
		}
	}
	return nil
}

func (record AssessmentInputImportRecord) Validate() error {
	if record.SchemaVersion != 2 || record.RecordType != "assessment_input_import" || record.ImportedAt.IsZero() || !selectionSHA1Pattern.MatchString(record.AcceptedCommit) || record.Producer.Validate() != nil || record.Producer.Commit != record.AcceptedCommit || record.WriteSet == nil {
		return fmt.Errorf("assessment input import record metadata is invalid")
	}
	if err := identity.ValidateRecordID(assessmentImportKind, record.ID); err != nil {
		return err
	}
	if err := validateAssessmentInputReference(AssessmentInputReference{Kind: record.Kind, InputKey: record.InputKey, InputID: record.InputID}); err != nil {
		return err
	}
	previous := ""
	for _, target := range record.Targets {
		if !selectionModulePattern.MatchString(target.ModuleID) || !selectionReleasePattern.MatchString(target.AVMReleaseVersion) || identity.ValidateRelativePath(target.ContextID, false) != nil || target.DesiredGeneration == 0 {
			return fmt.Errorf("assessment input import target generation is invalid")
		}
		key := target.ModuleID + "\x00" + target.AVMReleaseVersion + "\x00" + target.ContextID
		if previous != "" && key <= previous {
			return fmt.Errorf("assessment input import targets are not canonical")
		}
		previous = key
	}
	targets := make([]AssessmentInputTarget, 0, len(record.Targets))
	for _, target := range record.Targets {
		targets = append(targets, AssessmentInputTarget{ModuleID: target.ModuleID, AVMReleaseVersion: target.AVMReleaseVersion, ContextID: target.ContextID})
	}
	expectedID, err := identity.ID(assessmentImportKind, 1, assessmentInputImportIdentity{
		Kind: record.Kind, InputKey: record.InputKey, InputID: record.InputID, Targets: targets,
		AcceptedCommit: record.AcceptedCommit, ProducerWorkflow: record.Producer.WorkflowID, ProducerCommit: record.Producer.Commit,
	})
	if err != nil || record.ID != expectedID {
		return fmt.Errorf("assessment input import ID does not match its content")
	}
	moduleTargets := make(map[string]struct{})
	for _, target := range record.Targets {
		moduleTargets[target.ModuleID] = struct{}{}
	}
	previousEvent := ""
	writeModules := make(map[string]struct{})
	for _, entry := range record.WriteSet {
		if entry.EventID == "" || entry.EventID <= previousEvent || entry.RecordKind != "module" || len(entry.Digest) != 64 || !isLowerHexDigest(entry.Digest) {
			return fmt.Errorf("assessment input import write set is invalid")
		}
		previousEvent = entry.EventID
		moduleID := strings.TrimSuffix(strings.TrimPrefix(entry.RecordPath, assessmentModulesPrefix), ".json")
		if !selectionModulePattern.MatchString(moduleID) || entry.RecordPath != assessmentModulesPrefix+moduleID+".json" {
			return fmt.Errorf("assessment input import write set has a noncanonical module path")
		}
		if _, exists := moduleTargets[moduleID]; !exists {
			return fmt.Errorf("assessment input import writes an untargeted module")
		}
		if _, duplicate := writeModules[moduleID]; duplicate {
			return fmt.Errorf("assessment input import writes a module more than once")
		}
		writeModules[moduleID] = struct{}{}
	}
	if len(writeModules) != len(moduleTargets) {
		return fmt.Errorf("assessment input import write set omits a targeted module")
	}
	return nil
}

func validateAssessmentInputImport(input AssessmentInputImport) error {
	if input.ImportedAt.IsZero() || !selectionSHA1Pattern.MatchString(input.AcceptedCommit) {
		return fmt.Errorf("assessment input import timestamp and accepted commit are required")
	}
	if err := input.Producer.Validate(); err != nil || input.Producer.Commit != input.AcceptedCommit {
		return fmt.Errorf("assessment input import producer must prove its exact accepted commit")
	}
	if err := validateAssessmentInputReference(AssessmentInputReference{Kind: input.Kind, InputKey: input.InputKey, InputID: input.InputID}); err != nil {
		return err
	}
	previous := ""
	for _, target := range input.Targets {
		if !selectionModulePattern.MatchString(target.ModuleID) || !selectionReleasePattern.MatchString(target.AVMReleaseVersion) || identity.ValidateRelativePath(target.ContextID, false) != nil {
			return fmt.Errorf("assessment input import target is invalid")
		}
		key := target.ModuleID + "\x00" + target.AVMReleaseVersion + "\x00" + target.ContextID
		if previous != "" && key <= previous {
			return fmt.Errorf("assessment input import targets must be unique and sorted")
		}
		previous = key
	}
	return nil
}

func assessmentInputWriteSet(events []Event) []OperationWriteSetEntry {
	entries := make([]OperationWriteSetEntry, 0, len(events))
	for _, event := range events {
		digest, err := transactionEventDigest(event)
		if err != nil {
			return nil
		}
		entries = append(entries, OperationWriteSetEntry{EventID: event.ID, RecordPath: event.RecordPath, RecordKind: event.Record.Kind, Digest: digest})
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].EventID < entries[right].EventID })
	return entries
}

func validateAssessmentInputReference(input AssessmentInputReference) error {
	switch input.Kind {
	case InputProviderSnapshot:
		if identity.ValidateRecordID("snapshot", input.InputID) != nil || identity.ValidateRelativePath(input.InputKey, false) != nil || len(strings.Split(input.InputKey, "/")) != 3 {
			return fmt.Errorf("provider input identity is invalid")
		}
	case InputAssessmentPolicy:
		if input.InputKey != AssessmentPolicyInputKey || identity.ValidateRecordID("provider-policy", input.InputID) != nil {
			return fmt.Errorf("assessment policy input identity is invalid")
		}
	default:
		return fmt.Errorf("unsupported assessment input kind %q", input.Kind)
	}
	return nil
}

func readAssessmentModuleState(snapshot Snapshot, moduleID string) (AssessmentModuleState, error) {
	moduleState := AssessmentModuleState{SchemaVersion: 2, ModuleID: moduleID, Versions: map[string]map[string]AssessmentContextState{}}
	record, exists := snapshot.Records[assessmentModulesPrefix+moduleID+".json"]
	if !exists {
		return moduleState, nil
	}
	if record.Kind != "module" || record.SchemaVersion != 1 {
		return AssessmentModuleState{}, fmt.Errorf("assessment module state has unsupported record identity")
	}
	if err := json.Unmarshal(record.Data, &moduleState); err != nil {
		return AssessmentModuleState{}, fmt.Errorf("decode assessment module state: %w", err)
	}
	if err := moduleState.Validate(); err != nil || moduleState.ModuleID != moduleID {
		return AssessmentModuleState{}, fmt.Errorf("assessment module state is invalid for %s", moduleID)
	}
	return moduleState, nil
}

// ReadAssessmentModuleState returns the validated per-release input generations
// used by the site-cycle planner to form exact assessment requests.
func ReadAssessmentModuleState(snapshot Snapshot, moduleID string) (AssessmentModuleState, error) {
	if !selectionModulePattern.MatchString(moduleID) {
		return AssessmentModuleState{}, fmt.Errorf("assessment module identity is invalid")
	}
	return readAssessmentModuleState(snapshot, moduleID)
}

// AssessmentContextInputs verifies that every current input reference is
// backed by an immutable import receipt and its recorded transaction history.
func AssessmentContextInputs(snapshot Snapshot, moduleID, release, contextID string) (AssessmentContextState, error) {
	moduleState, err := readAssessmentModuleState(snapshot, moduleID)
	if err != nil {
		return AssessmentContextState{}, err
	}
	contextState, exists := moduleState.Versions[release][contextID]
	if !exists {
		return AssessmentContextState{}, nil
	}
	for _, reference := range contextState.Inputs {
		proven := false
		for recordPath, record := range snapshot.Records {
			if record.Kind != "input_import" {
				continue
			}
			var importRecord AssessmentInputImportRecord
			if json.Unmarshal(record.Data, &importRecord) != nil || importRecord.Validate() != nil {
				return AssessmentContextState{}, fmt.Errorf("assessment input import receipt is invalid")
			}
			if importRecord.Kind != reference.Kind || importRecord.InputKey != reference.InputKey || importRecord.InputID != reference.InputID {
				continue
			}
			if recordPath != assessmentInputsPrefix+importRecord.ID+".json" {
				return AssessmentContextState{}, ErrTransactionIdentityConflict
			}
			if _, found, readErr := readAssessmentInputImport(snapshot, recordPath, importRecord.ID); readErr != nil || !found {
				return AssessmentContextState{}, ErrTransactionIdentityConflict
			}
			for _, target := range importRecord.Targets {
				if target.ModuleID == moduleID && target.AVMReleaseVersion == release && target.ContextID == contextID && target.DesiredGeneration <= contextState.DesiredGeneration {
					proven = true
					break
				}
			}
			if proven {
				break
			}
		}
		if !proven {
			return AssessmentContextState{}, fmt.Errorf("assessment context input lacks an exact import receipt")
		}
	}
	return contextState, nil
}

func latestAssessmentGeneration(snapshot Snapshot, moduleID, avmReleaseVersion, contextID string) (uint64, error) {
	var latest uint64
	for recordPath, record := range snapshot.Records {
		if record.Kind != "request" {
			continue
		}
		var request selectionRequest
		if err := json.Unmarshal(record.Data, &request); err != nil {
			return 0, fmt.Errorf("decode request %s while importing assessment inputs: %w", recordPath, err)
		}
		if request.ModuleID == moduleID && request.Work != nil && request.Work.Phase == "assess" && request.Work.AVMReleaseVersion == avmReleaseVersion && request.Work.ContextID == contextID && request.Work.DesiredGeneration > latest {
			latest = request.Work.DesiredGeneration
		}
	}
	return latest, nil
}

func replaceAssessmentInput(inputs []AssessmentInputReference, updated AssessmentInputReference) []AssessmentInputReference {
	result := make([]AssessmentInputReference, 0, len(inputs)+1)
	replaced := false
	for _, input := range inputs {
		if input.Kind == updated.Kind && input.InputKey == updated.InputKey {
			if !replaced {
				result = append(result, updated)
				replaced = true
			}
			continue
		}
		result = append(result, input)
	}
	if !replaced {
		result = append(result, updated)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Kind+"\x00"+result[left].InputKey < result[right].Kind+"\x00"+result[right].InputKey
	})
	return result
}

func assessmentInputEvent(recordPath, kind string, value any) (Event, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return Event{}, fmt.Errorf("encode %s assessment input record: %w", kind, err)
	}
	record := Record{Kind: kind, SchemaVersion: 1, Data: data}
	eventID, err := identity.ID("state-event", 1, selectionEventIdentity{Path: recordPath, Record: record})
	if err != nil {
		return Event{}, fmt.Errorf("identify %s assessment input event: %w", kind, err)
	}
	return Event{ID: eventID, RecordPath: recordPath, Record: record}, nil
}

func importGenerations(record AssessmentInputImportRecord) map[string]uint64 {
	generations := make(map[string]uint64, len(record.Targets))
	for _, target := range record.Targets {
		generations[target.ModuleID+"/"+target.AVMReleaseVersion+"/"+target.ContextID] = target.DesiredGeneration
	}
	return generations
}

func sameAssessmentTargets(previous []AssessmentInputTargetGeneration, requested []AssessmentInputTarget) bool {
	if len(previous) != len(requested) {
		return false
	}
	for index, target := range requested {
		if previous[index].ModuleID != target.ModuleID || previous[index].AVMReleaseVersion != target.AVMReleaseVersion || previous[index].ContextID != target.ContextID {
			return false
		}
	}
	return true
}
