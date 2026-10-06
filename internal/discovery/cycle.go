package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
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
}

// ModuleDelivery returns only after delivery is durably acknowledged or fails.
type ModuleDelivery func(context.Context, ModuleCycle) (string, error)

// CycleError preserves overall scope and completed work without leaking raw diagnostics.
type CycleError struct {
	ModuleID  string
	Position  int
	Total     int
	Completed int
	Cause     error
}

func (failure *CycleError) Error() string { return "manual discovery module cycle stopped" }
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
	resolver     source.Resolver
	backend      state.Backend
	options      Options
	deliver      ModuleDelivery
	continuation *Continuation
}

// Cycle records shared catalog bookkeeping once, then completes each selected
// module's source/state/delivery cycle before touching the next module.
func Cycle(ctx context.Context, resolver source.Resolver, backend state.Backend, candidate catalog.Snapshot, options Options, deliver ModuleDelivery) (Result, error) {
	if options.NewModulesOnly || len(options.SelectedModuleIDs) == 0 || deliver == nil {
		return Result{}, fmt.Errorf("manual module cycle requires explicit selection and a delivery callback")
	}
	options.observeOnly = true
	result, err := Persist(ctx, resolver, backend, candidate, options)
	if err != nil {
		return result, err
	}
	ordered := orderedCycleModules(result.Accepted, result.SelectedModuleIDs)
	result.SelectedModuleIDs = ordered
	runner := moduleCycleRunner{resolver: resolver, backend: backend, options: options, deliver: deliver}
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
	head, err := runner.deliver(ctx, module)
	if err != nil {
		return head, module.ProjectionRequired, err
	}
	if head == "" {
		return module.Head, module.ProjectionRequired, fmt.Errorf("module delivery acknowledgement has no state head")
	}
	progress.Logger(ctx).InfoContext(ctx, "module cycle completed", "state_head", head)
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
	record := cycleModuleRecord(*accepted, moduleID)
	if !record.Active || record.Module.ID != moduleID {
		return ModuleCycle{}, fmt.Errorf("selected module is no longer active in the observed catalog")
	}
	resolved, err := resolveSelectedModule(ctx, input, record, target.Position, target.Total)
	if err != nil {
		return ModuleCycle{}, err
	}
	events := resolved.events
	if resolved.pinsChanged {
		proposalEvents, proposalErr := cycleCatalogProposal(current, observed, resolved.pinRef, options)
		if proposalErr != nil {
			return ModuleCycle{}, proposalErr
		}
		events = append(events, proposalEvents...)
	}
	if runner.continuation != nil {
		updated := *runner.continuation
		updated.PendingDelivery = true
		updated.LeaseExpiresAt = time.Now().UTC().Add(cycleLease)
		event, eventErr := continuationEvent(updated)
		if eventErr != nil {
			return ModuleCycle{}, eventErr
		}
		events = append(events, event)
	}
	if len(events) > 0 {
		if current.LayoutVersion == identity.ModuleLayout {
			owner, ownerErr := identity.OperationalModuleRoot(moduleID)
			if ownerErr != nil {
				return ModuleCycle{}, ownerErr
			}
			sharedPaths := []string{}
			if resolved.pinsChanged {
				for logicalPath, record := range current.Records {
					if record.Kind == "source_pins" {
						sharedPaths = append(sharedPaths, logicalPath)
					}
				}
			}
			head, current, err = state.ApplyAggregateDelta(ctx, runner.backend, state.AggregateDelta{ExpectedHead: head, Base: current, ReadOwners: []string{owner}, ReadPaths: sharedPaths, Events: events})
		} else {
			head, current, err = state.ApplyEventsAtHead(ctx, runner.backend, head, events)
		}
		if err != nil {
			return ModuleCycle{}, err
		}
		progress.Logger(ctx).InfoContext(ctx, "module discovery committed", "state_head", head, "events", len(events))
	} else {
		progress.Logger(ctx).InfoContext(ctx, "module discovery commit skipped", "reason", "unchanged source and lifecycle", "state_head", head)
	}
	if runner.continuation != nil {
		runner.continuation.PendingDelivery = true
	}
	return ModuleCycle{ModuleID: moduleID, Head: head, Snapshot: current, Accepted: *accepted, ProjectionRequired: len(resolved.projectionIDs) > 0, Position: target.Position, Total: target.Total}, nil
}

func cycleCatalogProposal(snapshot state.Snapshot, observed Result, pin *CatalogPinRef, options Options) ([]state.Event, error) {
	pins, err := catalogPinReferences(snapshot, observed.Accepted, pin)
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
