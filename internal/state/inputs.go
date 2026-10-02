package state

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
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
	ModuleID  string `json:"module_id"`
	ContextID string `json:"context_id"`
}

// AssessmentInputImport declares a newly accepted provider snapshot or
// assessment policy and the contexts whose future selections it invalidates.
type AssessmentInputImport struct {
	Kind       string                  `json:"kind"`
	InputKey   string                  `json:"input_key"`
	InputID    string                  `json:"input_id"`
	Targets    []AssessmentInputTarget `json:"targets"`
	ImportedAt time.Time               `json:"imported_at"`
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
	SchemaVersion int                               `json:"schema_version"`
	ModuleID      string                            `json:"module_id"`
	Contexts      map[string]AssessmentContextState `json:"contexts"`
}

// AssessmentInputImportRecord is the immutable receipt for one input import.
type AssessmentInputImportRecord struct {
	SchemaVersion int                               `json:"schema_version"`
	RecordType    string                            `json:"record_type"`
	ID            string                            `json:"input_import_id"`
	Kind          string                            `json:"kind"`
	InputKey      string                            `json:"input_key"`
	InputID       string                            `json:"input_id"`
	Targets       []AssessmentInputTargetGeneration `json:"targets"`
	ImportedAt    time.Time                         `json:"imported_at"`
}

// AssessmentInputTargetGeneration records the generation advanced for one
// target context by an imported input.
type AssessmentInputTargetGeneration struct {
	ModuleID          string `json:"module_id"`
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
	Kind     string                  `json:"kind"`
	InputKey string                  `json:"input_key"`
	InputID  string                  `json:"input_id"`
	Targets  []AssessmentInputTarget `json:"targets"`
}

// ImportAssessmentInputs advances each targeted context once in the same CAS
// transaction that records the immutable import receipt. It does not enqueue
// work; downstream dispatch consumes the durable generation and input IDs.
func ImportAssessmentInputs(ctx context.Context, backend Backend, expectedHead string, input AssessmentInputImport) (AssessmentInputImportResult, error) {
	if err := validateAssessmentInputImport(input); err != nil {
		return AssessmentInputImportResult{}, err
	}
	head, snapshot, err := readExpectedAssessmentSnapshot(ctx, backend, expectedHead)
	if err != nil {
		return AssessmentInputImportResult{}, err
	}
	input.ImportedAt = input.ImportedAt.UTC()
	importID, err := identity.ID(assessmentImportKind, 1, assessmentInputImportIdentity{
		Kind: input.Kind, InputKey: input.InputKey, InputID: input.InputID,
		Targets: input.Targets,
	})
	if err != nil {
		return AssessmentInputImportResult{}, err
	}
	importPath := assessmentInputsPrefix + importID + ".json"
	previous, found, err := readAssessmentInputImport(snapshot, importPath, importID)
	if err != nil {
		return AssessmentInputImportResult{}, err
	}
	if found {
		return AssessmentInputImportResult{Head: head, Import: previous, Generations: importGenerations(previous), Changed: false}, nil
	}
	states, targetGenerations, err := advanceAssessmentTargets(snapshot, input)
	if err != nil {
		return AssessmentInputImportResult{}, err
	}
	importRecord := AssessmentInputImportRecord{
		SchemaVersion: 1, RecordType: "assessment_input_import", ID: importID,
		Kind: input.Kind, InputKey: input.InputKey, InputID: input.InputID,
		Targets: targetGenerations, ImportedAt: input.ImportedAt,
	}
	events, err := assessmentInputEvents(importPath, importRecord, states)
	if err != nil {
		return AssessmentInputImportResult{}, err
	}
	newHead, _, err := ApplyEventsAtHead(ctx, backend, expectedHead, events)
	if err != nil {
		return AssessmentInputImportResult{}, err
	}
	return AssessmentInputImportResult{Head: newHead, Import: importRecord, Generations: importGenerations(importRecord), Changed: true}, nil
}

func readExpectedAssessmentSnapshot(ctx context.Context, backend Backend, expectedHead string) (string, Snapshot, error) {
	if expectedHead == "" {
		return "", Snapshot{}, fmt.Errorf("expected state head is required")
	}
	head, snapshot, err := ReadSnapshot(ctx, backend)
	if err != nil {
		return "", Snapshot{}, err
	}
	if head != expectedHead {
		return "", Snapshot{}, ErrHeadMoved
	}
	return head, snapshot, nil
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
		contextState := moduleState.Contexts[target.ContextID]
		latestRequestGeneration, err := latestAssessmentGeneration(snapshot, target.ModuleID, target.ContextID)
		if err != nil {
			return nil, nil, err
		}
		baseGeneration := max(contextState.DesiredGeneration, latestRequestGeneration)
		if baseGeneration == math.MaxUint64 {
			return nil, nil, fmt.Errorf("assessment generation is exhausted for %s/%s", target.ModuleID, target.ContextID)
		}
		contextState.DesiredGeneration = baseGeneration + 1
		contextState.Inputs = replaceAssessmentInput(contextState.Inputs, AssessmentInputReference{Kind: input.Kind, InputKey: input.InputKey, InputID: input.InputID})
		moduleState.Contexts[target.ContextID] = contextState
		states[target.ModuleID] = moduleState
		targetGenerations = append(targetGenerations, AssessmentInputTargetGeneration{ModuleID: target.ModuleID, ContextID: target.ContextID, DesiredGeneration: contextState.DesiredGeneration})
	}
	return states, targetGenerations, nil
}

func assessmentInputEvents(importPath string, importRecord AssessmentInputImportRecord, states map[string]AssessmentModuleState) ([]Event, error) {
	importEvent, err := assessmentInputEvent(importPath, "input_import", importRecord)
	if err != nil {
		return nil, err
	}
	events := []Event{importEvent}
	moduleIDs := make([]string, 0, len(states))
	for moduleID := range states {
		moduleIDs = append(moduleIDs, moduleID)
	}
	sort.Strings(moduleIDs)
	for _, moduleID := range moduleIDs {
		moduleState := states[moduleID]
		if err = moduleState.Validate(); err != nil {
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
	if moduleState.SchemaVersion != 1 || !selectionModulePattern.MatchString(moduleState.ModuleID) || moduleState.Contexts == nil {
		return fmt.Errorf("assessment module state identity is invalid")
	}
	for contextID, contextState := range moduleState.Contexts {
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
	return nil
}

func (record AssessmentInputImportRecord) Validate() error {
	if record.SchemaVersion != 1 || record.RecordType != "assessment_input_import" || record.ImportedAt.IsZero() {
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
		if !selectionModulePattern.MatchString(target.ModuleID) || identity.ValidateRelativePath(target.ContextID, false) != nil || target.DesiredGeneration == 0 {
			return fmt.Errorf("assessment input import target generation is invalid")
		}
		key := target.ModuleID + "\x00" + target.ContextID
		if previous != "" && key <= previous {
			return fmt.Errorf("assessment input import targets are not canonical")
		}
		previous = key
	}
	targets := make([]AssessmentInputTarget, 0, len(record.Targets))
	for _, target := range record.Targets {
		targets = append(targets, AssessmentInputTarget{ModuleID: target.ModuleID, ContextID: target.ContextID})
	}
	expectedID, err := identity.ID(assessmentImportKind, 1, assessmentInputImportIdentity{
		Kind: record.Kind, InputKey: record.InputKey, InputID: record.InputID, Targets: targets,
	})
	if err != nil || record.ID != expectedID {
		return fmt.Errorf("assessment input import ID does not match its content")
	}
	return nil
}

func validateAssessmentInputImport(input AssessmentInputImport) error {
	if input.ImportedAt.IsZero() {
		return fmt.Errorf("assessment input import timestamp is required")
	}
	if err := validateAssessmentInputReference(AssessmentInputReference{Kind: input.Kind, InputKey: input.InputKey, InputID: input.InputID}); err != nil {
		return err
	}
	previous := ""
	for _, target := range input.Targets {
		if !selectionModulePattern.MatchString(target.ModuleID) || identity.ValidateRelativePath(target.ContextID, false) != nil {
			return fmt.Errorf("assessment input import target is invalid")
		}
		key := target.ModuleID + "\x00" + target.ContextID
		if previous != "" && key <= previous {
			return fmt.Errorf("assessment input import targets must be unique and sorted")
		}
		previous = key
	}
	return nil
}

func validateAssessmentInputReference(input AssessmentInputReference) error {
	switch input.Kind {
	case InputProviderSnapshot:
		if identity.ValidateRecordID("snapshot", input.InputID) != nil || identity.ValidateRelativePath(input.InputKey, false) != nil || len(strings.Split(input.InputKey, "/")) != 3 {
			return fmt.Errorf("provider input identity is invalid")
		}
	case InputAssessmentPolicy:
		if input.InputKey != AssessmentPolicyInputKey || identity.ValidateRecordID("definition", input.InputID) != nil {
			return fmt.Errorf("assessment policy input identity is invalid")
		}
	default:
		return fmt.Errorf("unsupported assessment input kind %q", input.Kind)
	}
	return nil
}

func readAssessmentModuleState(snapshot Snapshot, moduleID string) (AssessmentModuleState, error) {
	moduleState := AssessmentModuleState{SchemaVersion: 1, ModuleID: moduleID, Contexts: map[string]AssessmentContextState{}}
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

func latestAssessmentGeneration(snapshot Snapshot, moduleID, contextID string) (uint64, error) {
	var latest uint64
	for recordPath, record := range snapshot.Records {
		if record.Kind != "request" {
			continue
		}
		var request selectionRequest
		if err := json.Unmarshal(record.Data, &request); err != nil {
			return 0, fmt.Errorf("decode request %s while importing assessment inputs: %w", recordPath, err)
		}
		if request.ModuleID == moduleID && request.Work != nil && request.Work.Phase == "assess" && request.Work.ContextID == contextID && request.Work.DesiredGeneration > latest {
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
		generations[target.ModuleID+"/"+target.ContextID] = target.DesiredGeneration
	}
	return generations
}
