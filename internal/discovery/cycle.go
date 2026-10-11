package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

// ModuleCycle identifies one committed module transition awaiting board delivery.
type ModuleCycle struct {
	ModuleID           string
	Head               string
	Snapshot           state.Snapshot
	Accepted           catalog.AcceptedState
	ProjectionRequired bool
	Position           int
	Total              int
	Skipped            bool
}

// ModuleDelivery returns only after delivery is durably acknowledged or fails.
type ModuleDelivery func(context.Context, ModuleCycle) (string, error)

// ModuleSkipCheck proves current delivery is acknowledged using the supplied snapshot only.
type ModuleSkipCheck func(context.Context, ModuleCycle) (bool, error)

// CycleError preserves overall scope and completed work without leaking raw diagnostics.
type CycleError struct {
	ModuleID  string
	Position  int
	Total     int
	Completed int
	Cause     error
}

func (failure *CycleError) Error() string {
	if failure.Cause == nil {
		return "manual discovery module cycle stopped"
	}
	return fmt.Sprintf("manual discovery module cycle stopped: %v", failure.Cause)
}
func (failure *CycleError) Unwrap() error { return failure.Cause }

// CatalogProposalReference selects one current immutable outbox for a catalog generation.
type CatalogProposalReference struct {
	SchemaVersion int    `json:"schema_version"`
	ProposalKind  string `json:"proposal_kind"`
	Generation    uint64 `json:"generation"`
	ProposalID    string `json:"proposal_id"`
}

const (
	CatalogProposalReferencePath = "state/exports/catalog-current.json"
	CatalogProposalReferenceKind = "catalog_reference"
)

const (
	resourceCycleRank = iota
	utilityCycleRank
	patternCycleRank
)

type moduleCycleRunner struct {
	resolver       source.Resolver
	backend        state.Backend
	coordinator    state.TransactionCoordinator
	options        Options
	deliver        ModuleDelivery
	continuation   *Continuation
	skipCheck      ModuleSkipCheck
	result         *Result
	beforeMutation func(context.Context, string) (string, error)
}

// cycleModuleReceiptResult preserves the exact module write set needed to
// replay a committed discovery cycle after the continuation cursor lags it.
type cycleModuleReceiptResult struct {
	Resolved cycleModuleResolutionReceipt `json:"resolved"`
	Events   []state.Event                `json:"events"`
}

// cycleModuleResolutionReceipt uses exported fields because operation results
// are JSON encoded in durable state. moduleResolutionResult is intentionally
// package-private and encoding/json would silently omit all of its fields.
type cycleModuleResolutionReceipt struct {
	PinRef        *CatalogPinRef `json:"pin_ref,omitempty"`
	PinsChanged   bool           `json:"pins_changed,omitempty"`
	ProjectionIDs []string       `json:"projection_ids,omitempty"`
	Events        []state.Event  `json:"events,omitempty"`
	FailureState  string         `json:"failure_state,omitempty"`
	FailureClass  string         `json:"failure_class,omitempty"`
}

func cycleModuleResolutionToReceipt(resolved moduleResolutionResult) cycleModuleResolutionReceipt {
	return cycleModuleResolutionReceipt{
		PinRef: resolved.pinRef, PinsChanged: resolved.pinsChanged,
		ProjectionIDs: append([]string(nil), resolved.projectionIDs...),
		Events:        append([]state.Event(nil), resolved.events...),
		FailureState:  resolved.failureState, FailureClass: resolved.failureClass,
	}
}

func (receipt cycleModuleResolutionReceipt) result() moduleResolutionResult {
	return moduleResolutionResult{
		pinRef: receipt.PinRef, pinsChanged: receipt.PinsChanged,
		projectionIDs: append([]string(nil), receipt.ProjectionIDs...),
		events:        append([]state.Event(nil), receipt.Events...),
		failureState:  receipt.FailureState, failureClass: receipt.FailureClass,
	}
}

// Cycle records shared catalog bookkeeping once, then completes each selected
// module's source/state/delivery cycle before touching the next module.
func Cycle(ctx context.Context, resolver source.Resolver, coordinator state.TransactionCoordinator, candidate catalog.Snapshot, options Options, deliver ModuleDelivery) (Result, error) {
	if options.NewModulesOnly || len(options.SelectedModuleIDs) == 0 || deliver == nil {
		return Result{}, fmt.Errorf("manual module cycle requires explicit selection and a delivery callback")
	}
	options.observeOnly = true
	reader, ok := coordinator.(state.Backend)
	if !ok {
		return Result{}, fmt.Errorf("manual module cycle requires the verified read backend")
	}
	result, err := Persist(ctx, resolver, coordinator, candidate, options)
	if err != nil {
		return result, err
	}
	ordered := orderedCycleModules(result.Accepted, result.SelectedModuleIDs)
	result.SelectedModuleIDs = ordered
	runner := moduleCycleRunner{resolver: resolver, backend: reader, coordinator: coordinator, options: options, deliver: deliver, result: &result}
	for position, moduleID := range ordered {
		module := ModuleCycle{ModuleID: moduleID, Position: position + 1, Total: len(ordered)}
		head, projected, cycleErr := runner.complete(ctx, result, module)
		if head != "" {
			result.Head = head
		}
		if projected {
			result.ProjectionModuleIDs = append(result.ProjectionModuleIDs, moduleID)
		}
		if cycleErr != nil {
			return result, &CycleError{ModuleID: moduleID, Position: position + 1, Total: len(ordered), Completed: len(result.CompletedModuleIDs), Cause: cycleErr}
		}
		result.CompletedModuleIDs = append(result.CompletedModuleIDs, moduleID)
	}
	return result, nil
}

func (runner moduleCycleRunner) complete(ctx context.Context, observed Result, target ModuleCycle) (string, bool, error) {
	ctx, finish := progress.Start(ctx, "discovery", "module_cycle", slog.String("module_id", target.ModuleID), slog.Int("position", target.Position), slog.Int("total", target.Total))
	defer finish(false)
	module, err := runner.persist(ctx, observed, target)
	if err != nil {
		return "", false, err
	}
	if module.Skipped {
		runner.result.SkippedModuleIDs = append(runner.result.SkippedModuleIDs, module.ModuleID)
		progress.Logger(ctx).InfoContext(ctx, "module discovery skipped", "reason", "unchanged source and acknowledged delivery", "state_head", module.Head)
		finish(true)
		return module.Head, false, nil
	}
	head, err := runner.deliver(ctx, module)
	if err != nil {
		return head, module.ProjectionRequired, err
	}
	if head == "" {
		return module.Head, module.ProjectionRequired, fmt.Errorf("module delivery acknowledgement has no state head")
	}
	progress.Logger(ctx).InfoContext(ctx, "module cycle completed", "state_head", head)
	if runner.result != nil {
		runner.result.DeliveredModuleIDs = appendCycleModuleID(runner.result.DeliveredModuleIDs, module.ModuleID)
	}
	finish(true)
	return head, module.ProjectionRequired, nil
}

func (runner moduleCycleRunner) persist(ctx context.Context, observed Result, target ModuleCycle) (ModuleCycle, error) {
	moduleID := target.ModuleID
	options := runner.options
	head, current, err := state.ReadSnapshot(ctx, runner.backend)
	if err != nil {
		return ModuleCycle{}, err
	}
	accepted, err := readDurableCatalog(current)
	if err != nil {
		return ModuleCycle{}, err
	}
	if accepted == nil || accepted.Generation != observed.Accepted.Generation {
		return ModuleCycle{}, fmt.Errorf("catalog changed during the manual module cycle")
	}
	if runner.continuation != nil {
		owner, ownerErr := ReadContinuation(current, runner.continuation.ID)
		if ownerErr != nil || owner.OwnerRunID != runner.continuation.OwnerRunID || owner.OwnerGeneration != runner.continuation.OwnerGeneration || !time.Now().Before(owner.LeaseExpiresAt) {
			return ModuleCycle{}, state.ErrStaleFence
		}
		if owner.Next < runner.continuation.Next && head != runner.continuation.Result.Head {
			return ModuleCycle{}, state.ErrHeadMoved
		}
	}
	if runner.continuation != nil && runner.continuation.PendingDelivery {
		return ModuleCycle{ModuleID: moduleID, Head: head, Snapshot: current, Accepted: *accepted, Position: target.Position, Total: target.Total}, nil
	}
	input := moduleResolutionInput{
		resolver: runner.resolver, snapshot: current, generation: accepted.Generation, delta: observed.Delta,
		options: options, selected: map[string]struct{}{moduleID: {}}, forced: map[string]struct{}{}, restricted: true,
	}
	if containsModule(options.ForceModuleIDs, moduleID) {
		input.forced[moduleID] = struct{}{}
	}
	preparedBase := current
	record := cycleModuleRecord(*accepted, moduleID)
	if !record.Active || record.Module.ID != moduleID {
		return ModuleCycle{}, fmt.Errorf("selected module is no longer active in the observed catalog")
	}
	operationID, err := cycleModuleOperationID(runner.continuation, options, moduleID, accepted.Generation)
	if err != nil {
		return ModuleCycle{}, err
	}
	var resolved moduleResolutionResult
	var events []state.Event
	replayed := false
	if runner.continuation != nil {
		if receiptResult, found, receiptErr := replayCycleModuleOperation(ctx, runner.coordinator, current, moduleID, operationID); receiptErr != nil {
			return ModuleCycle{}, receiptErr
		} else if found {
			resolved, events, replayed = receiptResult.Resolved.result(), receiptResult.Events, true
		}
	}
	if !replayed {
		resolved, err = resolveSelectedModule(ctx, input, record, target.Position, target.Total)
		if err != nil {
			return ModuleCycle{}, err
		}
		events = append([]state.Event(nil), resolved.events...)
	}
	runner.recordSourceCheck(moduleID, resolved.pinsChanged)
	module := ModuleCycle{ModuleID: moduleID, Head: head, Snapshot: current, Accepted: *accepted, Position: target.Position, Total: target.Total}
	module.Skipped, err = runner.canSkip(ctx, module, resolved)
	if err != nil {
		return ModuleCycle{}, err
	}
	if module.Skipped {
		return module, nil
	}
	if runner.beforeMutation != nil {
		flushedHead, flushErr := runner.beforeMutation(ctx, head)
		if flushErr != nil {
			return ModuleCycle{}, flushErr
		}
		if flushedHead != head {
			head, current, err = state.ReadSnapshot(ctx, runner.backend)
			if err != nil {
				return ModuleCycle{}, err
			}
			if head != flushedHead {
				return ModuleCycle{}, state.ErrHeadMoved
			}
			preparedBase = current
		}
	}
	if len(events) > 0 {
		if !replayed {
			operationRunner, ok := runner.coordinator.(operationTransactionRunner)
			if !ok {
				return ModuleCycle{}, fmt.Errorf("manual discovery requires receipt-backed module transactions")
			}
			events, _, _, _, err = appendSuccessfulModuleHandoffs(events, preparedBase, moduleID, accepted.Generation, operationID, operationID, state.DiscoveryWorkCompleted, resolved, options)
			if err != nil {
				return ModuleCycle{}, err
			}
			receiptResult := cycleModuleReceiptResult{Resolved: cycleModuleResolutionToReceipt(resolved), Events: events}
			var committed state.TransactionResult
			committed, err = operationRunner.RunOperationTransaction(ctx, moduleID, operationID, events, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
				latest, readErr := readDurableCatalog(snapshot)
				if readErr != nil || latest == nil || latest.Generation != observed.Accepted.Generation {
					if readErr != nil {
						return state.TransactionDecision{}, readErr
					}
					return state.TransactionDecision{}, state.ErrHeadMoved
				}
				if runner.continuation != nil {
					owner, ownerErr := ReadContinuation(snapshot, runner.continuation.ID)
					if ownerErr != nil || owner.OwnerRunID != runner.continuation.OwnerRunID || owner.OwnerGeneration != runner.continuation.OwnerGeneration || !time.Now().Before(owner.LeaseExpiresAt) {
						return state.TransactionDecision{}, state.ErrStaleFence
					}
				}
				for _, event := range events {
					before, beforeExists := preparedBase.Records[event.RecordPath]
					now, nowExists := snapshot.Records[event.RecordPath]
					if beforeExists != nowExists || (beforeExists && !reflect.DeepEqual(before, now)) {
						return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
					}
				}
				return state.TransactionDecision{ReadSet: state.TransactionReadSet{Owners: []string{moduleID}}, Events: events, Result: receiptResult, Candidate: state.CandidateOptions{Subject: "state(discovery): persist module handoff", Provenance: state.GitProvenance{Operation: "discovery.persist", Event: operationID, Module: moduleID}}}, nil
			}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
				latest, readErr := readDurableCatalog(snapshot)
				if readErr != nil || latest == nil || latest.Generation != observed.Accepted.Generation {
					if readErr != nil {
						return state.TransactionResolution{}, readErr
					}
					return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
				}
				return resolveCycleModuleReceipt(snapshot, moduleID, operationID, receiptResult)
			})
			if err != nil {
				return ModuleCycle{}, err
			}
			receiptResult, err = decodeCycleModuleReceiptResult(committed.Value)
			if err != nil {
				return ModuleCycle{}, err
			}
			resolved, events = receiptResult.Resolved.result(), receiptResult.Events
			head = committed.Head
		}
		progress.Logger(ctx).InfoContext(ctx, "module discovery committed", "state_head", head, "events", len(events), "receipt_backed", true)
	} else {
		progress.Logger(ctx).InfoContext(ctx, "module discovery commit skipped", "reason", "unchanged source and lifecycle", "state_head", head)
	}
	if resolved.pinsChanged {
		_, current, err = runner.backend.Read(ctx)
		if err != nil {
			return ModuleCycle{}, err
		}
		proposalEvents, proposalErr := cycleCatalogProposal(current, observed, options)
		if proposalErr != nil {
			return ModuleCycle{}, proposalErr
		}
		head, err = persistCycleCatalogProposal(ctx, runner.coordinator, observed.Accepted.Generation, proposalEvents)
		if err != nil {
			return ModuleCycle{}, err
		}
	}
	if runner.continuation != nil {
		updated := *runner.continuation
		updated.PendingDelivery = true
		updated.LeaseExpiresAt = time.Now().UTC().Add(cycleLease)
		head, err = saveContinuation(ctx, runner.backend, runner.coordinator, head, updated)
		if err != nil {
			return ModuleCycle{}, err
		}
		*runner.continuation = updated
	}
	_, current, err = runner.backend.Read(ctx)
	if err != nil {
		return ModuleCycle{}, err
	}
	return ModuleCycle{ModuleID: moduleID, Head: head, Snapshot: current, Accepted: *accepted, ProjectionRequired: len(resolved.projectionIDs) > 0, Position: target.Position, Total: target.Total}, nil
}

func cycleModuleOperationID(continuation *Continuation, options Options, moduleID string, generation uint64) (string, error) {
	cycleID := "run:" + options.Producer.RunID
	if continuation != nil {
		cycleID = continuation.ID
	} else if cycleID == "run:" {
		cycleID = "accepted-at:" + options.AcceptedAt.UTC().Format(time.RFC3339Nano)
	}
	return identity.ID("discovery-cycle-module", 1, struct {
		CycleID    string `json:"cycle_id"`
		ModuleID   string `json:"module_id"`
		Generation uint64 `json:"generation"`
	}{cycleID, moduleID, generation})
}

func decodeCycleModuleReceiptResult(value any) (cycleModuleReceiptResult, error) {
	var result cycleModuleReceiptResult
	encoded, err := json.Marshal(value)
	if err != nil {
		return result, fmt.Errorf("encode discovery module receipt result: %w", err)
	}
	if err = json.Unmarshal(encoded, &result); err != nil {
		return result, fmt.Errorf("decode discovery module receipt result: %w", err)
	}
	if result.Events == nil {
		return result, fmt.Errorf("discovery module receipt has no replay write set")
	}
	return result, nil
}

func replayCycleModuleOperation(ctx context.Context, coordinator state.TransactionCoordinator, snapshot state.Snapshot, moduleID, operationID string) (cycleModuleReceiptResult, bool, error) {
	receiptPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		return cycleModuleReceiptResult{}, false, err
	}
	record, found := snapshot.Records[receiptPath]
	if !found {
		return cycleModuleReceiptResult{}, false, nil
	}
	if record.Kind != "operation_receipt" || record.SchemaVersion != 1 {
		return cycleModuleReceiptResult{}, false, state.ErrTransactionIdentityConflict
	}
	var receipt state.OperationReceipt
	if err = json.Unmarshal(record.Data, &receipt); err != nil || receipt.ModuleID != moduleID || receipt.OperationID != operationID {
		return cycleModuleReceiptResult{}, false, state.ErrTransactionIdentityConflict
	}
	if err = receipt.Validate(receiptPath); err != nil {
		return cycleModuleReceiptResult{}, false, state.ErrTransactionIdentityConflict
	}
	receiptResult, err := decodeCycleModuleReceiptResult(receipt.Result)
	if err != nil {
		return cycleModuleReceiptResult{}, false, state.ErrTransactionIdentityConflict
	}
	operationRunner, ok := coordinator.(operationTransactionRunner)
	if !ok {
		return cycleModuleReceiptResult{}, false, fmt.Errorf("manual discovery requires receipt-backed module transactions")
	}
	_, err = operationRunner.RunOperationTransaction(ctx, moduleID, operationID, receiptResult.Events, func(state.Snapshot) (state.TransactionDecision, error) {
		return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
	}, func(current state.Snapshot) (state.TransactionResolution, error) {
		return resolveCycleModuleReceipt(current, moduleID, operationID, receiptResult)
	})
	if err != nil {
		return cycleModuleReceiptResult{}, false, err
	}
	return receiptResult, true, nil
}

func resolveCycleModuleReceipt(snapshot state.Snapshot, moduleID, operationID string, expected cycleModuleReceiptResult) (state.TransactionResolution, error) {
	receiptPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		return state.TransactionResolution{}, err
	}
	record, found := snapshot.Records[receiptPath]
	if !found {
		return state.TransactionResolution{}, nil
	}
	if record.Kind != "operation_receipt" || record.SchemaVersion != 1 {
		return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
	}
	var receipt state.OperationReceipt
	if err = json.Unmarshal(record.Data, &receipt); err != nil || receipt.ModuleID != moduleID || receipt.OperationID != operationID || receipt.Validate(receiptPath) != nil {
		return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
	}
	actual, err := decodeCycleModuleReceiptResult(receipt.Result)
	if err != nil || !reflect.DeepEqual(actual, expected) || validateReceiptWriteSet(receipt, actual.Events) != nil {
		return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
	}
	return state.TransactionResolution{Found: true, Value: actual}, nil
}

func persistCycleCatalogProposal(ctx context.Context, coordinator state.TransactionCoordinator, generation uint64, events []state.Event) (string, error) {
	if len(events) != 2 || events[0].RecordPath != "state/exports/"+proposalIDFromEvent(events[0])+".json" || events[1].RecordPath != CatalogProposalReferencePath {
		return "", fmt.Errorf("catalog proposal write set is invalid")
	}
	proposalID := proposalIDFromEvent(events[0])
	if proposalID == "" || events[0].Record.Kind != "export" || events[0].Record.SchemaVersion != recordSchemaVersion || events[1].Record.Kind != "export" || events[1].Record.SchemaVersion != recordSchemaVersion {
		return "", fmt.Errorf("catalog proposal records are invalid")
	}
	var proposal CatalogProposal
	if err := json.Unmarshal(events[0].Record.Data, &proposal); err != nil || proposal.Generation != generation {
		return "", fmt.Errorf("catalog proposal generation is invalid")
	}
	computedProposalID, err := identity.ID("catalog-proposal", recordSchemaVersion, proposal)
	if err != nil || computedProposalID != proposalID {
		return "", fmt.Errorf("catalog proposal identity is invalid")
	}
	var reference CatalogProposalReference
	if err := json.Unmarshal(events[1].Record.Data, &reference); err != nil || reference.SchemaVersion != recordSchemaVersion || reference.ProposalKind != CatalogProposalReferenceKind || reference.Generation != generation || reference.ProposalID != proposalID {
		return "", fmt.Errorf("catalog proposal reference does not match its outbox")
	}
	operationID := events[0].ID
	result, err := coordinator.RunTransaction(ctx, operationID, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
		accepted, readErr := readDurableCatalog(snapshot)
		if readErr != nil || accepted == nil || accepted.Generation != generation {
			if readErr != nil {
				return state.TransactionDecision{}, readErr
			}
			return state.TransactionDecision{}, state.ErrHeadMoved
		}
		currentPins, pinErr := catalogPinReferences(snapshot, *accepted, nil)
		if pinErr != nil {
			return state.TransactionDecision{}, pinErr
		}
		if !reflect.DeepEqual(proposal.SourcePins, currentPins) {
			return state.TransactionDecision{}, state.ErrHeadMoved
		}
		if previous, exists := snapshot.Records[events[0].RecordPath]; exists && !reflect.DeepEqual(previous, events[0].Record) {
			return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
		}
		if previous, exists := snapshot.Records[CatalogProposalReferencePath]; exists && !reflect.DeepEqual(previous, events[1].Record) {
			if err := validateReplaceableCatalogReference(snapshot, previous, generation); err != nil {
				return state.TransactionDecision{}, err
			}
		}
		return state.TransactionDecision{Events: events, Result: operationID, Candidate: state.CandidateOptions{Subject: "state(discovery): persist catalog proposal", Provenance: state.GitProvenance{Operation: "discovery.persist", Event: operationID}}}, nil
	}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
		accepted, readErr := readDurableCatalog(snapshot)
		if readErr != nil || accepted == nil || accepted.Generation != generation {
			if readErr != nil {
				return state.TransactionResolution{}, readErr
			}
			return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
		}
		if previous, exists := snapshot.Records[events[0].RecordPath]; !exists || !reflect.DeepEqual(previous, events[0].Record) {
			return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
		}
		return state.TransactionResolution{Found: true, Value: operationID}, nil
	})
	if err != nil {
		return "", err
	}
	return result.Head, nil
}

func proposalIDFromEvent(event state.Event) string {
	const prefix = "state/exports/"
	const suffix = ".json"
	if !strings.HasPrefix(event.RecordPath, prefix) || !strings.HasSuffix(event.RecordPath, suffix) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(event.RecordPath, prefix), suffix)
}

func validateReplaceableCatalogReference(snapshot state.Snapshot, record state.Record, generation uint64) error {
	if record.Kind != "export" || record.SchemaVersion != recordSchemaVersion {
		return state.ErrTransactionIdentityConflict
	}
	var reference CatalogProposalReference
	if err := json.Unmarshal(record.Data, &reference); err != nil || reference.SchemaVersion != recordSchemaVersion || reference.ProposalKind != CatalogProposalReferenceKind || reference.Generation != generation || reference.ProposalID == "" {
		return state.ErrTransactionIdentityConflict
	}
	proposalRecord, exists := snapshot.Records["state/exports/"+reference.ProposalID+".json"]
	if !exists || proposalRecord.Kind != "export" || proposalRecord.SchemaVersion != recordSchemaVersion {
		return state.ErrTransactionIdentityConflict
	}
	var proposal CatalogProposal
	if err := json.Unmarshal(proposalRecord.Data, &proposal); err != nil || proposal.Generation != generation {
		return state.ErrTransactionIdentityConflict
	}
	proposalID, err := identity.ID("catalog-proposal", recordSchemaVersion, proposal)
	if err != nil || proposalID != reference.ProposalID {
		return state.ErrTransactionIdentityConflict
	}
	return nil
}

// recordSourceCheck tracks actual source resolution separately from delivery retries.
func (runner moduleCycleRunner) recordSourceCheck(moduleID string, changed bool) {
	if runner.result == nil {
		return
	}
	runner.result.CheckedModuleIDs = appendCycleModuleID(runner.result.CheckedModuleIDs, moduleID)
	if changed {
		runner.result.ChangedModuleIDs = appendCycleModuleID(runner.result.ChangedModuleIDs, moduleID)
	}
}

// canSkip requires a fenced continuation completion and no source or lifecycle changes.
func (runner moduleCycleRunner) canSkip(ctx context.Context, module ModuleCycle, resolved moduleResolutionResult) (bool, error) {
	if runner.continuation == nil || runner.skipCheck == nil || len(resolved.events) != 0 || resolved.pinsChanged || len(resolved.projectionIDs) != 0 || containsModule(runner.options.ForceModuleIDs, module.ModuleID) {
		return false, nil
	}
	return runner.skipCheck(ctx, module)
}

// appendCycleModuleID keeps retrying source checks from duplicating result membership.
func appendCycleModuleID(moduleIDs []string, moduleID string) []string {
	if containsModule(moduleIDs, moduleID) {
		return moduleIDs
	}
	return append(moduleIDs, moduleID)
}

func cycleCatalogProposal(snapshot state.Snapshot, observed Result, options Options) ([]state.Event, error) {
	pins, err := catalogPinReferences(snapshot, observed.Accepted, nil)
	if err != nil {
		return nil, err
	}
	proposal := newCatalogProposal(observed.Accepted, observed.ObservationID, pins, options.AcceptedAt)
	_, events, err := catalogProposalEvents(proposal)
	return events, err
}

// catalogProposalEvents advances immutable outbox selection for every discovery writer.
func catalogProposalEvents(proposal CatalogProposal) (string, []state.Event, error) {
	proposalID, err := identity.ID("catalog-proposal", recordSchemaVersion, proposal)
	if err != nil {
		return "", nil, err
	}
	outbox, err := newStateEvent("state/exports/"+proposalID+".json", "export", proposal)
	if err != nil {
		return "", nil, err
	}
	reference, err := newStateEvent(CatalogProposalReferencePath, "export", CatalogProposalReference{
		SchemaVersion: recordSchemaVersion, ProposalKind: CatalogProposalReferenceKind, Generation: proposal.Generation, ProposalID: proposalID,
	})
	if err != nil {
		return "", nil, err
	}
	return proposalID, []state.Event{outbox, reference}, nil
}

func catalogPinReferences(snapshot state.Snapshot, accepted catalog.AcceptedState, replacement *CatalogPinRef) ([]CatalogPinRef, error) {
	pins := make([]CatalogPinRef, 0, len(accepted.Modules))
	for _, record := range accepted.Modules {
		if !record.Active {
			continue
		}
		if replacement != nil && record.Module.ID == replacement.ModuleID {
			pins = append(pins, *replacement)
			continue
		}
		history, err := readPinHistory(snapshot, record.Module.ID)
		if err != nil {
			return nil, err
		}
		if reference := projectionPinReference(record.Module.ID, history).pinRef; reference != nil {
			pins = append(pins, *reference)
		}
	}
	sort.Slice(pins, func(left, right int) bool { return pins[left].ModuleID < pins[right].ModuleID })
	return pins, nil
}

func cycleModuleRecord(accepted catalog.AcceptedState, moduleID string) catalog.ModuleRecord {
	for _, record := range accepted.Modules {
		if record.Module.ID == moduleID {
			return record
		}
	}
	return catalog.ModuleRecord{}
}

func orderedCycleModules(accepted catalog.AcceptedState, selected []string) []string {
	rank := map[catalog.Kind]int{catalog.Resource: resourceCycleRank, catalog.Utility: utilityCycleRank, catalog.Pattern: patternCycleRank}
	kinds := make(map[string]catalog.Kind, len(accepted.Modules))
	for _, record := range accepted.Modules {
		kinds[record.Module.ID] = record.Module.Type
	}
	ordered := append([]string(nil), selected...)
	sort.Slice(ordered, func(left, right int) bool {
		leftRank, rightRank := rank[kinds[ordered[left]]], rank[kinds[ordered[right]]]
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return ordered[left] < ordered[right]
	})
	return ordered
}
