package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"gopkg.in/yaml.v3"
)

const (
	CommandPause         = "pause"
	CommandResume        = "resume"
	CommandRetry         = "retry"
	CommandRefresh       = "refresh"
	CommandGlobalDisable = "global_disable"
	CommandGlobalEnable  = "global_enable"

	operationsRecordSchemaVersion = 1
	globalControlPath             = "state/operations/global.json"
)

var (
	ErrUnauthorizedOperator   = fmt.Errorf("operator is not authorized by the reviewed policy")
	ErrOperationConflict      = fmt.Errorf("operation ID was reused with different command content")
	ErrGlobalDispatchDisabled = fmt.Errorf("global dispatch is disabled")

	operatorLoginPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	operationIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,79}$`)
)

// OperatorPolicy is loaded from reviewed configuration and authorizes command
// actors independently of Issues, Projects, and workflow input text.
type OperatorPolicy struct {
	SchemaVersion      int      `yaml:"schema_version" json:"schema_version"`
	AllowedOperators   []string `yaml:"allowed_operators" json:"allowed_operators"`
	AllowGlobalDisable bool     `yaml:"allow_global_disable" json:"allow_global_disable"`
}

// OperationalCommand is an authenticated, reason-bearing control event.
type OperationalCommand struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Actor    string    `json:"actor"`
	ModuleID string    `json:"module_id,omitempty"`
	Reason   string    `json:"reason"`
	IssuedAt time.Time `json:"issued_at"`
}

// ModuleControl stores durable module pause state and its latest command.
type ModuleControl struct {
	SchemaVersion int       `json:"schema_version"`
	ModuleID      string    `json:"module_id"`
	Generation    uint64    `json:"generation"`
	Paused        bool      `json:"paused"`
	PauseReason   string    `json:"pause_reason,omitempty"`
	LastCommandID string    `json:"last_command_id"`
	LastReason    string    `json:"last_reason"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// GlobalControl is the durable scheduler kill switch.
type GlobalControl struct {
	SchemaVersion int       `json:"schema_version"`
	Generation    uint64    `json:"generation"`
	Enabled       bool      `json:"enabled"`
	Reason        string    `json:"reason,omitempty"`
	LastCommandID string    `json:"last_command_id"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// OperationalWakeup is an immutable outbox intent consumed by discovery or
// dispatch independently of GitHub Issue/Project projection.
type OperationalWakeup struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	CommandID     string    `json:"command_id"`
	Kind          string    `json:"kind"`
	ModuleID      string    `json:"module_id,omitempty"`
	Reason        string    `json:"reason"`
	CreatedAt     time.Time `json:"created_at"`
}

// OperationResult reports the committed transaction and any generated wakeup.
type OperationResult struct {
	Head      string `json:"head"`
	CommandID string `json:"command_id"`
	WakeupID  string `json:"wakeup_id,omitempty"`
	Changed   bool   `json:"changed"`
}

// LoadOperatorPolicy strictly loads the reviewed command authorization policy.
func LoadOperatorPolicy(path string) (OperatorPolicy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return OperatorPolicy{}, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	var policy OperatorPolicy
	if err = decoder.Decode(&policy); err != nil {
		return OperatorPolicy{}, fmt.Errorf("parse operator policy: %w", err)
	}
	if err = policy.Validate(); err != nil {
		return OperatorPolicy{}, err
	}
	return policy, nil
}

// Validate checks that the reviewed operator allowlist is usable and unambiguous.
func (policy OperatorPolicy) Validate() error {
	if policy.SchemaVersion != operationsRecordSchemaVersion || len(policy.AllowedOperators) == 0 {
		return fmt.Errorf("operator policy metadata or allowlist is invalid")
	}
	seen := make(map[string]struct{}, len(policy.AllowedOperators))
	for _, operator := range policy.AllowedOperators {
		if !operatorLoginPattern.MatchString(operator) {
			return fmt.Errorf("invalid GitHub operator login %q", operator)
		}
		key := strings.ToLower(operator)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate GitHub operator login %q", operator)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// Validate checks a command's canonical identity, actor, reason, and target.
func (command OperationalCommand) Validate() error {
	if !operationIDPattern.MatchString(command.ID) || !operatorLoginPattern.MatchString(command.Actor) || strings.TrimSpace(command.Reason) == "" || len(command.Reason) > 512 || command.IssuedAt.IsZero() {
		return fmt.Errorf("operational command identity, actor, reason, or timestamp is invalid")
	}
	if command.RequiresModule() {
		if !moduleIDPattern.MatchString(command.ModuleID) {
			return fmt.Errorf("invalid operational command module ID %q", command.ModuleID)
		}
		return nil
	}
	if command.ModuleID != "" {
		return fmt.Errorf("global operational command must not include a module ID")
	}
	if !command.IsGlobal() {
		return fmt.Errorf("unknown operational command kind %q", command.Kind)
	}
	return nil
}

func (command OperationalCommand) RequiresModule() bool {
	switch command.Kind {
	case CommandPause, CommandResume, CommandRetry, CommandRefresh:
		return true
	default:
		return false
	}
}

func (command OperationalCommand) IsGlobal() bool {
	return command.Kind == CommandGlobalDisable || command.Kind == CommandGlobalEnable
}

// ApplyOperationalCommand validates operator and module authority, then
// atomically stores the command, control change, and idempotent wakeup intent.
func ApplyOperationalCommand(ctx context.Context, coordinator state.TransactionCoordinator, policy OperatorPolicy, command OperationalCommand) (OperationResult, error) {
	if coordinator == nil {
		return OperationResult{}, fmt.Errorf("state transaction coordinator is required")
	}
	if err := validateAuthorizedCommand(policy, command); err != nil {
		return OperationResult{}, err
	}
	operationID := "operation:" + command.ID + ":command"
	result, err := coordinator.RunTransaction(ctx, operationID, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
		if prior, found, priorErr := findPriorOperation(snapshot, "", command); priorErr != nil {
			return state.TransactionDecision{}, priorErr
		} else if found {
			return state.TransactionDecision{Noop: true, Result: prior}, nil
		}
		if command.RequiresModule() {
			if activeErr := validateActiveCatalogModule(snapshot, command.ModuleID); activeErr != nil {
				return state.TransactionDecision{}, activeErr
			}
		}
		events, wakeID, buildErr := buildOperationEvents(snapshot, command)
		if buildErr != nil {
			return state.TransactionDecision{}, buildErr
		}
		events[0].ID = operationID
		owners := []string{}
		if command.RequiresModule() {
			owners = append(owners, command.ModuleID)
		}
		return state.TransactionDecision{ReadSet: state.TransactionReadSet{Owners: owners}, Events: events, Result: OperationResult{CommandID: command.ID, WakeupID: wakeID, Changed: true}, Candidate: state.CandidateOptions{Subject: "state(operations): " + string(command.Kind), Provenance: state.GitProvenance{Operation: "dispatch.claim", Event: operationID, Module: command.ModuleID}}}, nil
	}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
		prior, found, priorErr := findPriorOperation(snapshot, "", command)
		return state.TransactionResolution{Found: found, Value: prior}, priorErr
	})
	if err != nil {
		return OperationResult{}, err
	}
	resolved, ok := result.Value.(OperationResult)
	if !ok {
		return OperationResult{}, fmt.Errorf("committed operational command result has an invalid type")
	}
	resolved.Head, resolved.Changed = result.Head, result.Changed
	if resolved.CommandID == "" {
		resolved.CommandID, resolved.WakeupID = command.ID, wakeupID(command)
	}
	return resolved, nil
}

func validateAuthorizedCommand(policy OperatorPolicy, command OperationalCommand) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if err := command.Validate(); err != nil {
		return err
	}
	if !policy.authorizes(command.Actor) {
		return ErrUnauthorizedOperator
	}
	if command.IsGlobal() && !policy.AllowGlobalDisable {
		return fmt.Errorf("global enable/disable commands are not approved by policy")
	}
	return nil
}

func findPriorOperation(snapshot state.Snapshot, head string, command OperationalCommand) (OperationResult, bool, error) {
	commandPath := "state/operations/commands/" + command.ID + ".json"
	if previous, exists := snapshot.Records[commandPath]; exists {
		if previous.Kind != "operational_command" {
			return OperationResult{}, false, fmt.Errorf("operation ID %s is stored with the wrong record kind", command.ID)
		}
		var recorded OperationalCommand
		if err := json.Unmarshal(previous.Data, &recorded); err != nil {
			return OperationResult{}, false, fmt.Errorf("decode prior operation %s: %w", command.ID, err)
		}
		if !sameCommandContent(recorded, command) {
			return OperationResult{}, false, ErrOperationConflict
		}
		return OperationResult{Head: head, CommandID: command.ID, WakeupID: wakeupID(command), Changed: false}, true, nil
	}
	return OperationResult{}, false, nil
}

func buildOperationEvents(snapshot state.Snapshot, command OperationalCommand) ([]state.Event, string, error) {
	updatedAt := command.IssuedAt.UTC()
	events := make([]state.Event, 0, 3)
	commandPath := "state/operations/commands/" + command.ID + ".json"
	commandEvent, err := operationStateEvent(commandPath, "operational_command", command, command.ID+":command", "", 0)
	if err != nil {
		return nil, "", err
	}
	events = append(events, commandEvent)
	if command.RequiresModule() {
		control, controlErr := readModuleControl(snapshot, command.ModuleID)
		if controlErr != nil {
			return nil, "", controlErr
		}
		applyModuleCommand(&control, command, updatedAt)
		controlEvent, eventErr := operationStateEvent(moduleControlPath(command.ModuleID), "module_control", control, command.ID+":module", "", 0)
		if eventErr != nil {
			return nil, "", eventErr
		}
		events = append(events, controlEvent)
	} else {
		control, controlErr := readGlobalControl(snapshot)
		if controlErr != nil {
			return nil, "", controlErr
		}
		applyGlobalCommand(&control, command, updatedAt)
		controlEvent, eventErr := operationStateEvent(globalControlPath, "global_control", control, command.ID+":global", "", 0)
		if eventErr != nil {
			return nil, "", eventErr
		}
		events = append(events, controlEvent)
	}
	wakeup := newWakeup(command, updatedAt)
	if wakeup != nil {
		wakeupEvent, eventErr := operationStateEvent(wakeupPath(command.ID), "operational_wakeup", wakeup, command.ID+":wakeup", "", 0)
		if eventErr != nil {
			return nil, "", eventErr
		}
		events = append(events, wakeupEvent)
	}
	return events, wakeupID(command), nil
}

func (policy OperatorPolicy) authorizes(actor string) bool {
	for _, allowed := range policy.AllowedOperators {
		if strings.EqualFold(allowed, actor) {
			return true
		}
	}
	return false
}

func (command OperationalCommand) IsWakeup() bool {
	switch command.Kind {
	case CommandResume, CommandRetry, CommandRefresh, CommandGlobalEnable:
		return true
	default:
		return false
	}
}

func validateActiveCatalogModule(snapshot state.Snapshot, moduleID string) error {
	record, exists := snapshot.Records["state/discovery/catalog.json"]
	if !exists || record.Kind != "discovery_catalog" {
		return fmt.Errorf("accepted catalog state is unavailable for operator command")
	}
	var durable struct {
		SchemaVersion int                   `json:"schema_version"`
		Accepted      catalog.AcceptedState `json:"accepted"`
	}
	if err := json.Unmarshal(record.Data, &durable); err != nil {
		return fmt.Errorf("decode accepted catalog for operator command: %w", err)
	}
	if durable.SchemaVersion != 1 {
		return fmt.Errorf("unsupported accepted catalog state version %d", durable.SchemaVersion)
	}
	if err := durable.Accepted.Validate(); err != nil {
		return fmt.Errorf("validate accepted catalog for operator command: %w", err)
	}
	for _, module := range durable.Accepted.Modules {
		if module.Module.ID == moduleID && module.Active {
			return nil
		}
	}
	return fmt.Errorf("module %s is not an active accepted catalog identity", moduleID)
}

func readModuleControl(snapshot state.Snapshot, moduleID string) (ModuleControl, error) {
	control := ModuleControl{SchemaVersion: 1, ModuleID: moduleID}
	record, exists := snapshot.Records[moduleControlPath(moduleID)]
	if !exists {
		return control, nil
	}
	if record.Kind != "module_control" {
		return ModuleControl{}, fmt.Errorf("module control %s has the wrong record kind", moduleID)
	}
	if err := json.Unmarshal(record.Data, &control); err != nil {
		return ModuleControl{}, fmt.Errorf("decode module control %s: %w", moduleID, err)
	}
	if control.SchemaVersion != 1 || control.ModuleID != moduleID || control.Generation == 0 {
		return ModuleControl{}, fmt.Errorf("module control %s is invalid", moduleID)
	}
	return control, nil
}

func readGlobalControl(snapshot state.Snapshot) (GlobalControl, error) {
	control := GlobalControl{SchemaVersion: 1, Enabled: true}
	record, exists := snapshot.Records[globalControlPath]
	if !exists {
		return control, nil
	}
	if record.Kind != "global_control" {
		return GlobalControl{}, fmt.Errorf("global control has the wrong record kind")
	}
	if err := json.Unmarshal(record.Data, &control); err != nil {
		return GlobalControl{}, fmt.Errorf("decode global control: %w", err)
	}
	if control.SchemaVersion != 1 || control.Generation == 0 {
		return GlobalControl{}, fmt.Errorf("global control is invalid")
	}
	return control, nil
}

func applyModuleCommand(control *ModuleControl, command OperationalCommand, now time.Time) {
	control.SchemaVersion = 1
	control.ModuleID = command.ModuleID
	control.Generation++
	control.LastCommandID = command.ID
	control.LastReason = command.Reason
	control.UpdatedAt = now
	switch command.Kind {
	case CommandPause:
		control.Paused = true
		control.PauseReason = command.Reason
	case CommandResume:
		control.Paused = false
		control.PauseReason = ""
	}
}

func applyGlobalCommand(control *GlobalControl, command OperationalCommand, now time.Time) {
	control.SchemaVersion = 1
	control.Generation++
	control.Enabled = command.Kind == CommandGlobalEnable
	control.Reason = command.Reason
	control.LastCommandID = command.ID
	control.UpdatedAt = now
}

func newWakeup(command OperationalCommand, now time.Time) *OperationalWakeup {
	if !command.IsWakeup() {
		return nil
	}
	kind := command.Kind
	return &OperationalWakeup{
		SchemaVersion: 1, ID: wakeupID(command), CommandID: command.ID,
		Kind: kind, ModuleID: command.ModuleID, Reason: command.Reason, CreatedAt: now,
	}
}

func sameCommandContent(left, right OperationalCommand) bool {
	return left.ID == right.ID && left.Kind == right.Kind && strings.EqualFold(left.Actor, right.Actor) && left.ModuleID == right.ModuleID && left.Reason == right.Reason
}

func moduleControlPath(moduleID string) string {
	return "state/operations/modules/" + moduleID + ".json"
}

func wakeupPath(commandID string) string {
	return "state/operations/wakeups/" + commandID + ".json"
}

func wakeupID(command OperationalCommand) string {
	if command.IsWakeup() {
		return "wakeup-" + command.ID
	}
	return ""
}

func operationStateEvent(recordPath, kind string, value any, eventSuffix, fenceKey string, fenceToken uint64) (state.Event, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return state.Event{}, fmt.Errorf("encode %s: %w", kind, err)
	}
	return state.Event{
		ID: "operation:" + eventSuffix, FenceKey: fenceKey, FenceToken: fenceToken,
		RecordPath: recordPath, Record: state.Record{Kind: kind, SchemaVersion: 1, Data: encoded},
	}, nil
}

// ModuleDispatchPaused reports whether an accepted module is paused.
func ModuleDispatchPaused(snapshot state.Snapshot, moduleID string) (bool, error) {
	control, err := readModuleControl(snapshot, moduleID)
	if err != nil {
		return false, err
	}
	return control.Paused, nil
}

// GlobalDispatchEnabled returns the persisted kill-switch state; absence means enabled.
func GlobalDispatchEnabled(snapshot state.Snapshot) (bool, error) {
	control, err := readGlobalControl(snapshot)
	if err != nil {
		return false, err
	}
	return control.Enabled, nil
}
