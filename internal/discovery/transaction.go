package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

const (
	recordSchemaVersion      = 1
	projectionIntentPath     = "state/projection/intents/"
	discoveryObservationPath = "state/discovery/observations/"
	discoverySourcePinPath   = "state/discovery/source-pins/"
	discoveryModuleOperation = "discovery:module:"
	discoveryClaimLease      = 6 * time.Hour
	IntentBacklog            = "backlog"
	IntentBlocked            = "blocked"
	IntentNeedsReview        = "needs_review"
	IntentRetired            = "retired"
)

// Options pins the reviewed extractor contract for source-acquisition work.
type Options struct {
	AcceptedAt        time.Time
	ExtractorContract string
	Capabilities      []string
	SelectedModuleIDs []string
	ForceModuleIDs    []string
	NewModulesOnly    bool
	Producer          projection.WorkflowProvenance
	observeOnly       bool
}

// Result describes the accepted local transaction, not a canonical data merge
// or production workflow activation.
type Result struct {
	ContinuationComplete bool      `json:"continuation_complete,omitempty"`
	ContinuationDurable  bool      `json:"continuation_durable,omitempty"`
	DeferralSaved        bool      `json:"deferral_saved,omitempty"`
	ContinuationID       string    `json:"continuation_id,omitempty"`
	Deferred             bool      `json:"deferred,omitempty"`
	RetryAt              time.Time `json:"retry_at,omitempty"`
	WaitReason           string    `json:"wait_reason,omitempty"`
	Head                 string
	Accepted             catalog.AcceptedState
	Delta                catalog.Delta
	ObservationID        string
	ProposalID           string
	ProjectionModuleIDs  []string `json:"projection_module_ids,omitempty"`
	SelectedModuleIDs    []string `json:"selected_module_ids,omitempty"`
	CompletedModuleIDs   []string `json:"completed_module_ids,omitempty"`
	CheckedModuleIDs     []string `json:"checked_module_ids,omitempty"`
	SkippedModuleIDs     []string `json:"skipped_module_ids,omitempty"`
	ChangedModuleIDs     []string `json:"changed_module_ids,omitempty"`
	DeliveredModuleIDs   []string `json:"delivered_module_ids,omitempty"`
}

// DurableCatalogState is the latest accepted catalog reducer state.
type DurableCatalogState struct {
	SchemaVersion int                   `json:"schema_version"`
	Accepted      catalog.AcceptedState `json:"accepted"`
}

// Observation preserves the full accepted input and reducer result regardless
// of whether any source analysis succeeds.
type Observation struct {
	SchemaVersion      int                       `json:"schema_version"`
	Candidate          catalog.Snapshot          `json:"candidate"`
	AcceptedAt         time.Time                 `json:"accepted_at"`
	AcceptedGeneration uint64                    `json:"accepted_generation"`
	Delta              catalog.Delta             `json:"delta"`
	Selection          catalog.AcceptedSelection `json:"selection"`
}

// PinHistory keeps overall latest source independent from track candidates.
type PinHistory struct {
	SchemaVersion   int                              `json:"schema_version"`
	ModuleID        string                           `json:"module_id"`
	OverallLatest   *source.ResolvedSource           `json:"overall_latest,omitempty"`
	ReleaseHistory  []source.ResolvedSource          `json:"release_history"`
	TrackCandidates map[string]source.ResolvedSource `json:"track_candidates"`
}

// ProjectionIntent is durable desired board state, not a board write request.
type ProjectionIntent struct {
	SchemaVersion     int                    `json:"schema_version"`
	ModuleID          string                 `json:"module_id"`
	DesiredGeneration uint64                 `json:"desired_generation"`
	Status            string                 `json:"status"`
	Reason            string                 `json:"reason,omitempty"`
	Source            *source.ResolvedSource `json:"source,omitempty"`
	UpdatedAt         time.Time              `json:"updated_at"`
}

// CatalogProposal is an independent outbox record for a canonical catalog
// proposal; it does not depend on successful analysis.
type CatalogProposal struct {
	SchemaVersion int             `json:"schema_version"`
	ProposalKind  string          `json:"proposal_kind"`
	Generation    uint64          `json:"generation"`
	ObservationID string          `json:"observation_id"`
	ModuleIDs     []string        `json:"module_ids"`
	SourcePins    []CatalogPinRef `json:"source_pins"`
	CreatedAt     time.Time       `json:"created_at"`
}

// CatalogPinRef identifies an overall source pin without replacing track state.
type CatalogPinRef struct {
	ModuleID string `json:"module_id"`
	Version  string `json:"version"`
	Tag      string `json:"tag"`
	Commit   string `json:"commit"`
}

// Persist atomically accepts a catalog observation, source-pin outcomes,
// acquisition requests, projection intents, and any catalog proposal.
func Persist(ctx context.Context, resolver source.Resolver, coordinator state.TransactionCoordinator, candidate catalog.Snapshot, options Options) (Result, error) {
	if err := validateDiscoveryOptions(options); err != nil {
		return Result{}, err
	}
	if coordinator == nil {
		return Result{}, fmt.Errorf("state transaction coordinator is required")
	}
	ctx, finish := progress.Start(ctx, "discovery", "persist")
	defer finish(false)
	headSnapshot, err := readCoordinatorSnapshot(ctx, coordinator)
	if err != nil {
		return Result{}, err
	}
	previous, err := readDurableCatalog(headSnapshot)
	if err != nil {
		return Result{}, err
	}
	var accepted catalog.AcceptedState
	var delta catalog.Delta
	retryObservation, retryFound, err := findRetryObservation(headSnapshot, candidate, options.AcceptedAt)
	if err != nil {
		return Result{}, err
	}
	if retryFound {
		if previous == nil || previous.Generation != retryObservation.AcceptedGeneration {
			return Result{}, state.ErrTransactionIdentityConflict
		}
		accepted, delta = *previous, retryObservation.Delta
	} else {
		accepted, delta, err = catalog.AcceptSnapshot(previous, candidate, options.AcceptedAt)
		if err != nil {
			return Result{}, err
		}
	}
	preparation, err := preparePersistence(headSnapshot, previous, accepted, delta, candidate, options)
	if err != nil {
		return Result{}, err
	}
	selectedModuleIDs, forceModuleIDs, preparedOptions := preparation.selectedIDs, preparation.forcedIDs, preparation.prepared
	selection := preparation.selection
	expectedCatalogGeneration := preparation.accepted.Generation
	observation, observationID, proposalID := preparation.observation, preparation.observationID, preparation.proposalID
	var projectionModuleIDs []string
	operationID := "discovery:" + observationID
	preparedEvents := preparation.events
	var result state.TransactionResult
	if preparation.retryObservation != nil {
		result = state.TransactionResult{Value: Result{
			Accepted: accepted, Delta: delta, ObservationID: observationID, ProposalID: proposalID,
			SelectedModuleIDs: selectedModuleIDs,
		}}
	} else {
		for index := range preparedEvents {
			if preparedEvents[index].RecordPath == discoveryObservationPath+observationID+".json" {
				preparedEvents[index].ID = operationID
				break
			}
		}
		result, err = coordinator.RunTransaction(ctx, operationID, func(current state.Snapshot) (state.TransactionDecision, error) {
			currentAccepted, readErr := readDurableCatalog(current)
			if readErr != nil {
				return state.TransactionDecision{}, readErr
			}
			if accepted.Generation != expectedCatalogGeneration || !reflect.DeepEqual(currentAccepted, previous) {
				return state.TransactionDecision{}, state.ErrHeadMoved
			}
			currentObservation, observationExists, observationErr := readDiscoveryObservation(current, observationID)
			if observationErr != nil {
				return state.TransactionDecision{}, observationErr
			}
			if observationExists {
				if !reflect.DeepEqual(currentObservation, observation) {
					return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
				}
				return state.TransactionDecision{Noop: true, Result: Result{Accepted: accepted, Delta: delta, ObservationID: observationID, ProposalID: proposalID, ProjectionModuleIDs: projectionModuleIDs, SelectedModuleIDs: selectedModuleIDs}}, nil
			}
			events := append([]state.Event(nil), preparedEvents...)
			for _, event := range events {
				before, beforeExists := headSnapshot.Records[event.RecordPath]
				now, nowExists := current.Records[event.RecordPath]
				if beforeExists != nowExists || (beforeExists && !reflect.DeepEqual(before, now)) {
					return state.TransactionDecision{}, state.ErrHeadMoved
				}
			}
			var eventErr error
			events, eventErr = appendAcceptedCatalogEvent(events, currentAccepted, accepted, delta)
			if eventErr != nil {
				return state.TransactionDecision{}, eventErr
			}
			return state.TransactionDecision{Events: events, Result: Result{Accepted: accepted, Delta: delta, ObservationID: observationID, ProposalID: proposalID, ProjectionModuleIDs: projectionModuleIDs, SelectedModuleIDs: selectedModuleIDs}, Candidate: state.CandidateOptions{Subject: "state(discovery): accept catalog enrollment", Provenance: state.GitProvenance{Operation: "discovery.persist", Event: operationID}}}, nil
		}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
			observation, exists, readErr := readDiscoveryObservation(snapshot, observationID)
			if readErr != nil || !exists {
				return state.TransactionResolution{}, readErr
			}
			accepted, readErr := readDurableCatalog(snapshot)
			if readErr != nil || accepted == nil || accepted.Generation != observation.AcceptedGeneration || !reflect.DeepEqual(observation.Candidate, candidate) || !observation.AcceptedAt.Equal(options.AcceptedAt.UTC()) {
				if readErr != nil {
					return state.TransactionResolution{}, readErr
				}
				return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
			}
			return state.TransactionResolution{Found: true, Value: Result{Accepted: *accepted, Delta: observation.Delta, ObservationID: observationID, SelectedModuleIDs: selectedModuleIDs, ProposalID: proposalID, ProjectionModuleIDs: projectionModuleIDs}}, nil
		})
		if err != nil {
			return Result{}, err
		}
	}
	completed, ok := result.Value.(Result)
	if !ok {
		return Result{}, fmt.Errorf("discovery transaction result has an invalid type")
	}
	completed.Head = result.Head
	moduleBase, err := readCoordinatorSnapshot(ctx, coordinator)
	if err != nil {
		return completed, err
	}
	selection = moduleSelection(moduleBase, accepted.Generation, preparation.selection, options)
	completed.SelectedModuleIDs = selection.ModuleIDs()
	var pinsChanged bool
	var moduleErr error
	if !options.observeOnly {
		var processedIDs []string
		processedIDs, pinsChanged, projectionModuleIDs, moduleErr = processAcceptedModules(ctx, moduleProcessor{
			resolver: resolver, coordinator: coordinator, accepted: accepted, delta: delta,
			selection: moduleSelection(moduleBase, accepted.Generation, selection, options), options: preparedOptions, forced: forceModuleIDs,
			observationID: observationID,
		})
		completed.CompletedModuleIDs = processedIDs
	}
	completed.ProjectionModuleIDs = projectionModuleIDs
	if moduleErr != nil {
		if head, _, headErr := coordinator.Read(ctx); headErr == nil {
			completed.Head = head
		}
		return completed, moduleErr
	}
	current, readErr := readCoordinatorSnapshot(ctx, coordinator)
	if readErr != nil {
		return completed, readErr
	}
	pinRefs, readErr := catalogPinReferences(current, accepted, nil)
	if readErr != nil {
		return completed, readErr
	}
	if pinsChanged || !catalogProposalMatches(current, accepted, pinRefs) {
		proposalID, err = persistCatalogProposal(ctx, coordinator, accepted, observationID, pinRefs, options.AcceptedAt)
		if err != nil {
			return completed, err
		}
		completed.ProposalID = proposalID
	}
	progress.Logger(ctx).InfoContext(ctx, "discovery transaction committed", "state_head", completed.Head, "projection_modules", len(completed.ProjectionModuleIDs), "catalog_proposal", completed.ProposalID != "")
	finish(true)
	return completed, nil
}

func persistCatalogProposal(ctx context.Context, coordinator state.TransactionCoordinator, accepted catalog.AcceptedState, observationID string, pins []CatalogPinRef, createdAt time.Time) (string, error) {
	proposal := newCatalogProposal(accepted, observationID, pins, createdAt)
	proposalID, events, err := catalogProposalEvents(proposal)
	if err != nil {
		return "", err
	}
	operationID := events[0].ID
	_, err = coordinator.RunTransaction(ctx, operationID, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
		current, readErr := readDurableCatalog(snapshot)
		if readErr != nil || current == nil || current.Generation != accepted.Generation {
			if readErr != nil {
				return state.TransactionDecision{}, readErr
			}
			return state.TransactionDecision{}, state.ErrHeadMoved
		}
		return state.TransactionDecision{Events: events, Result: proposalID, Candidate: state.CandidateOptions{Subject: "state(discovery): refresh catalog proposal", Provenance: state.GitProvenance{Operation: "discovery.catalog_proposal", Event: operationID}}}, nil
	}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
		record, exists := snapshot.Records["state/exports/"+proposalID+".json"]
		if !exists || record.Kind != "export" {
			return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
		}
		return state.TransactionResolution{Found: true, Value: proposalID}, nil
	})
	return proposalID, err
}

type persistPreparation struct {
	headSnapshot     state.Snapshot
	previous         *catalog.AcceptedState
	accepted         catalog.AcceptedState
	delta            catalog.Delta
	selection        catalog.AcceptedSelection
	selectedIDs      []string
	forcedIDs        []string
	prepared         Options
	observationID    string
	retryObservation *Observation
	proposalID       string
	observation      Observation
	events           []state.Event
}

func preparePersistence(headSnapshot state.Snapshot, previous *catalog.AcceptedState, accepted catalog.AcceptedState, delta catalog.Delta, candidate catalog.Snapshot, options Options) (persistPreparation, error) {
	if previous != nil {
		if observation, found, err := findRetryObservation(headSnapshot, candidate, options.AcceptedAt); err != nil {
			return persistPreparation{}, err
		} else if found {
			selection, selected, forced, prepared, retryErr := prepareRetrySelection(headSnapshot, observation, options)
			if retryErr != nil {
				return persistPreparation{}, retryErr
			}
			proposalID := currentProposalID(headSnapshot, observation.AcceptedGeneration)
			return persistPreparation{
				headSnapshot: headSnapshot, previous: previous, accepted: accepted, delta: observation.Delta,
				selection: selection, selectedIDs: selected, forcedIDs: forced, prepared: prepared,
				observationID: observationIDFor(observation), retryObservation: &observation, proposalID: proposalID,
			}, nil
		}
	}
	selection, err := acceptedDiscoverySelection(headSnapshot, accepted, delta, options)
	if err != nil {
		return persistPreparation{}, err
	}
	prepared := options
	if options.NewModulesOnly {
		prepared.SelectedModuleIDs = missingAcceptedFactoryModules(headSnapshot, selection)
	}
	selectedIDs, forcedIDs, err := discoverySelection(accepted, prepared)
	if err != nil {
		return persistPreparation{}, err
	}
	if options.NewModulesOnly && len(selection.ModuleIDs()) == 0 {
		selectedIDs = nil
	}
	prepared.SelectedModuleIDs = selectedIDs
	observation := Observation{SchemaVersion: recordSchemaVersion, Candidate: candidate, AcceptedAt: options.AcceptedAt.UTC(), AcceptedGeneration: accepted.Generation, Delta: delta, Selection: selection}
	observationID, err := identity.ID("discovery-observation", recordSchemaVersion, observation)
	if err != nil {
		return persistPreparation{}, fmt.Errorf("identify discovery observation: %w", err)
	}
	events, proposalID, err := prepareCatalogEvents(headSnapshot, previous, accepted, delta, observationID, observation, options.AcceptedAt)
	if err != nil {
		return persistPreparation{}, err
	}
	return persistPreparation{
		headSnapshot: headSnapshot, previous: previous, accepted: accepted, delta: delta,
		selection: selection, selectedIDs: selectedIDs, forcedIDs: forcedIDs, prepared: prepared,
		observationID: observationID, proposalID: proposalID, observation: observation, events: events,
	}, nil
}

func findRetryObservation(snapshot state.Snapshot, candidate catalog.Snapshot, acceptedAt time.Time) (Observation, bool, error) {
	var matching *Observation
	for _, record := range snapshot.Records {
		if record.Kind != "discovery_observation" || record.SchemaVersion != recordSchemaVersion {
			continue
		}
		var observation Observation
		if err := json.Unmarshal(record.Data, &observation); err != nil {
			return Observation{}, false, state.ErrTransactionIdentityConflict
		}
		if !reflect.DeepEqual(observation.Candidate, candidate) || !observation.AcceptedAt.Equal(acceptedAt.UTC()) {
			continue
		}
		if observation.Selection.SchemaVersion() == 0 || observation.Selection.Validate() != nil {
			return Observation{}, false, state.ErrTransactionIdentityConflict
		}
		if matching != nil {
			return Observation{}, false, state.ErrTransactionIdentityConflict
		}
		matchedObservation := observation
		matching = &matchedObservation
	}
	if matching == nil {
		return Observation{}, false, nil
	}
	return *matching, true, nil
}

func prepareRetrySelection(snapshot state.Snapshot, observation Observation, options Options) (catalog.AcceptedSelection, []string, []string, Options, error) {
	selected := observation.Selection.ModuleIDs()
	if options.NewModulesOnly {
		selected = missingAcceptedFactoryModulesForObservation(snapshot, observation)
	} else if len(options.SelectedModuleIDs) > 0 {
		requested, err := identity.SortedSet(options.SelectedModuleIDs)
		if err != nil || !reflect.DeepEqual(requested, observation.Selection.ModuleIDs()) {
			return catalog.AcceptedSelection{}, nil, nil, Options{}, state.ErrTransactionIdentityConflict
		}
	}
	forced, err := validateForceModuleIDs(catalog.AcceptedState{Modules: activeModulesFromSelection(observation.Selection)}, options.ForceModuleIDs)
	if err != nil {
		return catalog.AcceptedSelection{}, nil, nil, Options{}, err
	}
	prepared := options
	prepared.SelectedModuleIDs = selected
	if len(forced) > 0 && !sameModuleSet(forced, selected) {
		return catalog.AcceptedSelection{}, nil, nil, Options{}, fmt.Errorf("forced discovery module is outside retried observation selection")
	}
	return observation.Selection, selected, forced, prepared, nil
}

func missingAcceptedFactoryModulesForObservation(snapshot state.Snapshot, observation Observation) []string {
	completed := make(map[string]struct{})
	for _, record := range snapshot.Records {
		if record.Kind != "discovery_work" {
			continue
		}
		var work state.DiscoveryWork
		if json.Unmarshal(record.Data, &work) == nil && work.State == state.DiscoveryWorkCompleted && work.ModuleID != "" {
			completed[work.ModuleID] = struct{}{}
		}
	}
	selected := make([]string, 0)
	for _, moduleID := range observation.Selection.ModuleIDs() {
		if _, exists := completed[moduleID]; !exists {
			selected = append(selected, moduleID)
		}
	}
	return selected
}

func activeModulesFromSelection(selection catalog.AcceptedSelection) []catalog.ModuleRecord {
	modules := make([]catalog.ModuleRecord, 0, len(selection.ModuleIDs()))
	for _, moduleID := range selection.ModuleIDs() {
		kind := catalog.Kind(strings.Split(moduleID, "-")[1])
		modules = append(modules, catalog.ModuleRecord{Module: catalog.Module{ID: moduleID, Type: kind}, Active: true, LastSeenGeneration: selection.CatalogGeneration()})
	}
	return modules
}

func sameModuleSet(left, right []string) bool {
	leftSet, rightSet := moduleIDSet(left), moduleIDSet(right)
	if len(leftSet) != len(rightSet) {
		return false
	}
	for moduleID := range leftSet {
		if _, exists := rightSet[moduleID]; !exists {
			return false
		}
	}
	return true
}

func observationIDFor(observation Observation) string {
	operationID, _ := identity.ID("discovery-observation", recordSchemaVersion, observation)
	return operationID
}

func currentProposalID(snapshot state.Snapshot, generation uint64) string {
	var reference CatalogProposalReference
	record, exists := snapshot.Records[CatalogProposalReferencePath]
	if !exists || record.Kind != "export" || json.Unmarshal(record.Data, &reference) != nil || reference.Generation != generation {
		return ""
	}
	return reference.ProposalID
}

func prepareCatalogEvents(headSnapshot state.Snapshot, previous *catalog.AcceptedState, accepted catalog.AcceptedState, delta catalog.Delta, observationID string, observation Observation, at time.Time) ([]state.Event, string, error) {
	observationEvent, err := newStateEvent(discoveryObservationPath+observationID+".json", "discovery_observation", observation)
	if err != nil {
		return nil, "", err
	}
	events, err := appendAcceptedCatalogEvent([]state.Event{observationEvent}, previous, accepted, delta)
	if err != nil {
		return nil, "", err
	}
	if delta.NoChange {
		return events, "", nil
	}
	pins, err := catalogPinReferences(headSnapshot, accepted, nil)
	if err != nil {
		return nil, "", err
	}
	proposalID, proposalEvents, err := catalogProposalEvents(newCatalogProposal(accepted, observationID, pins, at))
	if err != nil {
		return nil, "", err
	}
	return append(events, proposalEvents...), proposalID, nil
}

func catalogProposalMatches(snapshot state.Snapshot, accepted catalog.AcceptedState, pins []CatalogPinRef) bool {
	referenceRecord, exists := snapshot.Records[CatalogProposalReferencePath]
	if !exists || referenceRecord.Kind != "export" {
		return false
	}
	var reference CatalogProposalReference
	if json.Unmarshal(referenceRecord.Data, &reference) != nil || reference.Generation != accepted.Generation || reference.ProposalID == "" {
		return false
	}
	proposalRecord, exists := snapshot.Records["state/exports/"+reference.ProposalID+".json"]
	if !exists || proposalRecord.Kind != "export" {
		return false
	}
	var proposal CatalogProposal
	if json.Unmarshal(proposalRecord.Data, &proposal) != nil {
		return false
	}
	want := newCatalogProposal(accepted, proposal.ObservationID, pins, proposal.CreatedAt)
	return reflect.DeepEqual(proposal, want)
}

type operationTransactionRunner interface {
	RunOperationTransaction(context.Context, string, string, []state.Event, state.TransactionDecisionFunc, state.TransactionResultResolver) (state.TransactionResult, error)
}

type moduleOperationIdentity struct {
	ModuleID           string   `json:"module_id"`
	ObservationID      string   `json:"observation_id"`
	OperationKind      string   `json:"operation_kind"`
	DesiredGeneration  uint64   `json:"desired_generation"`
	DesiredFingerprint string   `json:"desired_fingerprint"`
	ExtractorContract  string   `json:"extractor_contract"`
	Capabilities       []string `json:"capabilities"`
	ForceGeneration    uint64   `json:"force_generation"`
}

type moduleOperationResult struct {
	ModuleID     string   `json:"module_id"`
	OperationID  string   `json:"operation_id"`
	State        string   `json:"state"`
	RequestIDs   []string `json:"request_ids,omitempty"`
	ProjectionID string   `json:"projection_intent_id,omitempty"`
	HandoffIDs   []string `json:"handoff_ids,omitempty"`
}

type moduleOperationReceiptResult struct {
	Result             moduleOperationResult `json:"result"`
	DesiredFingerprint string                `json:"desired_fingerprint"`
	Events             []state.Event         `json:"events"`
}

type moduleOperationInput struct {
	coordinator       state.TransactionCoordinator
	prepared          state.Snapshot
	catalogGeneration uint64
	module            catalog.ModuleRecord
	moduleID          string
	observationID     string
	selection         catalog.AcceptedSelection
	resolved          moduleResolutionResult
	work              state.DiscoveryWork
	options           Options
}

func stableModuleOperationIdentity(snapshot state.Snapshot, moduleID, observationID string, record catalog.ModuleRecord, generation uint64, options Options, forced bool) (string, string, error) {
	capabilities, err := identity.SortedSet(options.Capabilities)
	if err != nil {
		return "", "", err
	}
	if observationID == "" {
		observationID = latestDiscoveryObservationID(snapshot, generation)
		if observationID == "" {
			return "", "", fmt.Errorf("discovery observation identity is missing for generation %d", generation)
		}
	}
	desiredFingerprint, err := identity.ID("discovery-desired-input", recordSchemaVersion, struct {
		Generation uint64               `json:"generation"`
		Module     catalog.ModuleRecord `json:"module"`
	}{generation, record})
	if err != nil {
		return "", "", err
	}
	desiredDigest := strings.TrimPrefix(desiredFingerprint, "discovery-desired-input-")
	forceGenerationValue := forceGeneration(generation, forced)
	operationFingerprint, err := identity.ID("discovery-operation-input", recordSchemaVersion, moduleOperationIdentity{
		ModuleID: moduleID, ObservationID: observationID, OperationKind: "discovery", DesiredGeneration: generation,
		DesiredFingerprint: desiredDigest, ExtractorContract: options.ExtractorContract,
		Capabilities: capabilities, ForceGeneration: forceGenerationValue,
	})
	if err != nil {
		return "", "", err
	}
	return discoveryModuleOperation + strings.TrimPrefix(operationFingerprint, "discovery-operation-input-"), desiredDigest, nil
}

func latestDiscoveryObservationID(snapshot state.Snapshot, generation uint64) string {
	var selected string
	for recordPath, record := range snapshot.Records {
		if record.Kind != "discovery_observation" || record.SchemaVersion != recordSchemaVersion {
			continue
		}
		var observation Observation
		if json.Unmarshal(record.Data, &observation) != nil || observation.AcceptedGeneration != generation {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(recordPath, discoveryObservationPath), ".json")
		if selected == "" || id > selected {
			selected = id
		}
	}
	return selected
}

func replayModuleOperation(ctx context.Context, coordinator state.TransactionCoordinator, snapshot state.Snapshot, moduleID, operationID, desiredFingerprint string) (moduleOperationResult, bool, error) {
	reader, ok := coordinator.(state.GitReader)
	operationRunner, operationOK := coordinator.(operationTransactionRunner)
	if !ok || !operationOK {
		return moduleOperationResult{}, false, nil
	}
	receiptPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		return moduleOperationResult{}, false, err
	}
	receiptRecord, exists := snapshot.Records[receiptPath]
	if !exists || receiptRecord.Kind != "operation_receipt" {
		return moduleOperationResult{}, false, nil
	}
	var receipt state.OperationReceipt
	if err = json.Unmarshal(receiptRecord.Data, &receipt); err != nil || receipt.ModuleID != moduleID || receipt.OperationID != operationID || receipt.Validate(receiptPath) != nil {
		return moduleOperationResult{}, false, state.ErrTransactionIdentityConflict
	}
	var receiptResult moduleOperationReceiptResult
	if err = json.Unmarshal(receipt.Result, &receiptResult); err != nil || receiptResult.Result.ModuleID != moduleID || receiptResult.Result.OperationID != operationID || receiptResult.DesiredFingerprint != desiredFingerprint {
		return moduleOperationResult{}, false, state.ErrTransactionIdentityConflict
	}
	if err = validateReceiptWriteSet(receipt, receiptResult.Events); err != nil {
		return moduleOperationResult{}, false, err
	}
	if err = validateReceiptWorkEvent(moduleID, operationID, desiredFingerprint, receiptResult.Events); err != nil {
		return moduleOperationResult{}, false, err
	}
	if _, _, err = reader.Read(ctx); err != nil {
		return moduleOperationResult{}, false, err
	}
	replayed, err := operationRunner.RunOperationTransaction(ctx, moduleID, operationID, receiptResult.Events, func(state.Snapshot) (state.TransactionDecision, error) {
		return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
	}, func(state.Snapshot) (state.TransactionResolution, error) {
		return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
	})
	if err != nil {
		return moduleOperationResult{}, false, err
	}
	if replayed.Value == nil {
		return moduleOperationResult{}, false, state.ErrTransactionIdentityConflict
	}
	var replayedReceiptResult moduleOperationReceiptResult
	if raw, ok := replayed.Value.(json.RawMessage); ok {
		err = json.Unmarshal(raw, &replayedReceiptResult)
	} else {
		encoded, marshalErr := json.Marshal(replayed.Value)
		if marshalErr != nil {
			return moduleOperationResult{}, false, marshalErr
		}
		err = json.Unmarshal(encoded, &replayedReceiptResult)
	}
	if err != nil || !reflect.DeepEqual(replayedReceiptResult, receiptResult) {
		return moduleOperationResult{}, false, state.ErrTransactionIdentityConflict
	}
	if receiptResult.Result.ModuleID != moduleID || receiptResult.Result.State == "" {
		return moduleOperationResult{}, false, state.ErrTransactionIdentityConflict
	}
	return receiptResult.Result, true, nil
}

func validateReceiptWriteSet(receipt state.OperationReceipt, events []state.Event) error {
	receiptEvent, err := state.NewOperationReceipt(receipt.ModuleID, receipt.OperationID, receipt.Result, events)
	if err != nil {
		return state.ErrTransactionIdentityConflict
	}
	var expected state.OperationReceipt
	if json.Unmarshal(receiptEvent.Record.Data, &expected) != nil || !reflect.DeepEqual(receipt.WriteSet, expected.WriteSet) {
		return state.ErrTransactionIdentityConflict
	}
	return nil
}

func validateReceiptWorkEvent(moduleID, operationID, desiredFingerprint string, events []state.Event) error {
	workPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		return err
	}
	receiptPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		return err
	}
	receiptPath, err = identity.OperationalRecordPath(receiptPath, "operation_receipt", moduleID)
	if err != nil {
		return err
	}
	for _, event := range events {
		if event.RecordPath != workPath || event.Record.Kind != "discovery_work" {
			continue
		}
		var work state.DiscoveryWork
		if json.Unmarshal(event.Record.Data, &work) == nil && work.ModuleID == moduleID && work.OperationID == operationID && work.DesiredFingerprint == desiredFingerprint && work.ReceiptPath == receiptPath {
			return nil
		}
	}
	return state.ErrTransactionIdentityConflict
}

func forceGeneration(generation uint64, forced bool) uint64 {
	if forced {
		return generation
	}
	return 0
}

func selectionForObservation(snapshot state.Snapshot, generation uint64, fallback catalog.AcceptedSelection) catalog.AcceptedSelection {
	if fallback.Provenance() != catalog.FactoryNewOnly {
		return fallback
	}
	for _, record := range snapshot.Records {
		if record.Kind != "discovery_observation" || record.SchemaVersion != recordSchemaVersion {
			continue
		}
		var observation Observation
		if json.Unmarshal(record.Data, &observation) == nil && observation.AcceptedGeneration == generation && observation.Selection.SchemaVersion() != 0 && observation.Selection.Provenance() == catalog.FactoryNewOnly && observation.Selection.Validate() == nil {
			return observation.Selection
		}
	}
	return fallback
}

func moduleSelection(snapshot state.Snapshot, generation uint64, fallback catalog.AcceptedSelection, options Options) catalog.AcceptedSelection {
	if options.NewModulesOnly {
		return selectionForObservation(snapshot, generation, fallback)
	}
	return fallback
}

type moduleProcessor struct {
	resolver      source.Resolver
	coordinator   state.TransactionCoordinator
	accepted      catalog.AcceptedState
	delta         catalog.Delta
	selection     catalog.AcceptedSelection
	options       Options
	forced        []string
	observationID string
}

func processAcceptedModules(ctx context.Context, processor moduleProcessor) ([]string, bool, []string, error) {
	orderedIDs := orderedSelectedModuleIDs(processor.accepted, processor.selection)
	moduleByID := make(map[string]catalog.ModuleRecord, len(processor.accepted.Modules))
	for _, module := range processor.accepted.Modules {
		moduleByID[module.Module.ID] = module
	}
	selected := moduleIDSet(orderedIDs)
	forceSet := moduleIDSet(processor.forced)
	processorState := moduleProcessingState{total: len(orderedIDs)}
	for _, moduleID := range orderedIDs {
		record := moduleByID[moduleID]
		if err := processSelectedModule(ctx, processor, record, moduleID, &processorState, selected, forceSet); err != nil {
			return processorState.completed, processorState.pinsChanged, processorState.projections, err
		}
	}
	return processorState.completed, processorState.pinsChanged, processorState.projections, nil
}

type moduleProcessingState struct {
	position    int
	total       int
	pinsChanged bool
	completed   []string
	projections []string
}

func orderedSelectedModuleIDs(accepted catalog.AcceptedState, selection catalog.AcceptedSelection) []string {
	modules := append([]catalog.ModuleRecord(nil), accepted.Modules...)
	sort.Slice(modules, func(left, right int) bool { return modules[left].Module.ID < modules[right].Module.ID })
	selected := moduleIDSet(selection.ModuleIDs())
	ordered := make([]string, 0, len(selected))
	for _, record := range modules {
		if _, exists := selected[record.Module.ID]; exists {
			ordered = append(ordered, record.Module.ID)
		}
	}
	return ordered
}

func moduleIDSet(moduleIDs []string) map[string]struct{} {
	set := make(map[string]struct{}, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		set[moduleID] = struct{}{}
	}
	return set
}

func processSelectedModule(ctx context.Context, processor moduleProcessor, record catalog.ModuleRecord, moduleID string, progressState *moduleProcessingState, selected, forceSet map[string]struct{}) error {
	coordinator, resolver := processor.coordinator, processor.resolver
	accepted, delta, selection := processor.accepted, processor.delta, processor.selection
	options := processor.options
	retired := !record.Active && containsModule(delta.Retired, moduleID)
	progressState.position++
	position := progressState.position
	if err := ctx.Err(); err != nil {
		return err
	}
	snapshot, err := readCoordinatorSnapshot(ctx, coordinator)
	if err != nil {
		return err
	}
	if err = validateCurrentCatalog(snapshot, accepted.Generation); err != nil {
		return err
	}
	selection = moduleSelection(snapshot, accepted.Generation, selection, options)
	if options.NewModulesOnly && (len(selection.ModuleIDs()) == 0 || !containsModule(selection.ModuleIDs(), moduleID)) {
		return nil
	}
	if _, chosen := selected[moduleID]; !chosen && (options.observeOnly || options.NewModulesOnly || !retired) {
		return nil
	}
	_, forced := forceSet[moduleID]
	operationID, desiredFingerprint, err := stableModuleOperationIdentity(snapshot, moduleID, processor.observationID, record, accepted.Generation, options, forced)
	if err != nil {
		return err
	}
	if replayed, found, replayErr := replayModuleOperation(ctx, coordinator, snapshot, moduleID, operationID, desiredFingerprint); replayErr != nil {
		return replayErr
	} else if found {
		if replayed.State != state.DiscoveryWorkCompleted {
			return fmt.Errorf("discovery module %s stopped with durable state %s", moduleID, replayed.State)
		}
		progressState.completed = append(progressState.completed, moduleID)
		return nil
	}
	claim, err := claimModuleWork(ctx, coordinator, snapshot, moduleID, operationID, desiredFingerprint, options)
	if err != nil {
		return err
	}
	snapshot, err = readCoordinatorSnapshot(ctx, coordinator)
	if err != nil {
		return err
	}
	workPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		return err
	}
	claimedRecord, exists := snapshot.Records[workPath]
	claimedData, marshalErr := json.Marshal(claim)
	if marshalErr != nil || !exists || claimedRecord.Kind != "discovery_work" || !bytes.Equal(claimedRecord.Data, claimedData) || snapshot.FencingTokens[moduleID] != claim.OwnerGeneration {
		return state.ErrStaleFence
	}
	moduleResult, err := prepareModuleResult(ctx, resolver, snapshot, accepted.Generation, record, moduleID, position, progressState.total, options, selected, forceSet, retired)
	if err != nil {
		return err
	}
	if moduleResult.failureState != "" {
		if err = checkpointModuleFailure(ctx, coordinator, snapshot, moduleID, desiredFingerprint, claim, moduleResult, options); err != nil {
			return err
		}
		return fmt.Errorf("discovery module %s stopped with durable state %s", moduleID, moduleResult.failureState)
	}
	progressState.pinsChanged = progressState.pinsChanged || moduleResult.pinsChanged
	committed, err := commitModuleOperation(ctx, moduleOperationInput{
		coordinator: coordinator, prepared: snapshot, catalogGeneration: accepted.Generation,
		module: record, moduleID: moduleID, observationID: processor.observationID,
		selection: selection, resolved: moduleResult, work: claim, options: options,
	})
	if err != nil {
		return err
	}
	if committed.State == state.DiscoveryWorkRetryable || committed.State == state.DiscoveryWorkNeedsReview || committed.State == state.DiscoveryWorkOutcomeUnknown {
		return fmt.Errorf("discovery module %s stopped with durable state %s", moduleID, committed.State)
	}
	if len(moduleResult.projectionIDs) > 0 {
		progressState.projections = append(progressState.projections, moduleID)
	}
	progressState.completed = append(progressState.completed, moduleID)
	progress.Logger(ctx).InfoContext(ctx, "discovery module transaction committed", "module_id", moduleID, "position", position, "total", progressState.total, "operation_id", committed.OperationID)
	return nil
}

func claimModuleWork(ctx context.Context, coordinator state.TransactionCoordinator, prepared state.Snapshot, moduleID, operationID, desiredFingerprint string, options Options) (state.DiscoveryWork, error) {
	workPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		return state.DiscoveryWork{}, err
	}
	prior, exists := prepared.Records[workPath]
	var previous state.DiscoveryWork
	if exists {
		if prior.Kind != "discovery_work" || json.Unmarshal(prior.Data, &previous) != nil || previous.ModuleID != moduleID {
			return state.DiscoveryWork{}, state.ErrTransactionIdentityConflict
		}
		if (previous.OperationID != operationID || previous.DesiredFingerprint != desiredFingerprint) && previous.State != state.DiscoveryWorkCompleted {
			return state.DiscoveryWork{}, state.ErrTransactionIdentityConflict
		}
		if previous.State == state.DiscoveryWorkNeedsReview {
			return state.DiscoveryWork{}, fmt.Errorf("discovery module %s has terminal durable work state %s", moduleID, previous.State)
		}
		if previous.State == state.DiscoveryWorkOutcomeUnknown {
			return state.DiscoveryWork{}, fmt.Errorf("discovery module %s has unknown durable work outcome", moduleID)
		}
		now := time.Now().UTC()
		if previous.OwnerRunID != "" && now.Before(previous.LeaseExpiresAt) {
			return state.DiscoveryWork{}, fmt.Errorf("discovery module %s is owned by an unexpired work claim", moduleID)
		}
		if previous.State == state.DiscoveryWorkRetryable && now.Before(previous.RetryAt) {
			return state.DiscoveryWork{}, fmt.Errorf("discovery module %s is not due for retry", moduleID)
		}
	}
	ownerGeneration := prepared.FencingTokens[moduleID]
	if exists && previous.OwnerGeneration > ownerGeneration {
		ownerGeneration = previous.OwnerGeneration
	}
	if ownerGeneration == ^uint64(0) {
		return state.DiscoveryWork{}, fmt.Errorf("discovery module %s owner generation is exhausted", moduleID)
	}
	ownerRunID := options.Producer.RunID
	if ownerRunID == "" {
		ownerRunID = operationID
	}
	work := state.DiscoveryWork{
		SchemaVersion: 1, ModuleID: moduleID, DesiredFingerprint: desiredFingerprint, OperationID: operationID,
		State: state.DiscoveryWorkReady, Attempt: 1, OwnerRunID: ownerRunID,
		OwnerGeneration: ownerGeneration + 1, LeaseExpiresAt: options.AcceptedAt.UTC().Add(discoveryClaimLease), FailureCategory: "none",
	}
	if exists {
		work.Attempt = previous.Attempt + 1
	}
	record, err := state.DiscoveryWorkRecord(work)
	if err != nil {
		return state.DiscoveryWork{}, err
	}
	event := state.Event{ID: "discovery:claim:" + strings.TrimPrefix(operationID, discoveryModuleOperation) + ":" + fmt.Sprint(work.OwnerGeneration), RecordPath: workPath, Record: record, FenceKey: moduleID, FenceToken: work.OwnerGeneration}
	if err = checkOnlyModuleWrites(moduleID, []state.Event{event}); err != nil {
		return state.DiscoveryWork{}, err
	}
	_, err = coordinator.RunTransaction(ctx, event.ID, func(current state.Snapshot) (state.TransactionDecision, error) {
		currentWork, currentExists := current.Records[workPath]
		if currentExists != exists || (exists && (!reflect.DeepEqual(currentWork, prior) || currentWork.Kind != "discovery_work")) {
			return state.TransactionDecision{}, state.ErrStaleFence
		}
		if current.FencingTokens[moduleID] != prepared.FencingTokens[moduleID] {
			return state.TransactionDecision{}, state.ErrStaleFence
		}
		if !exists && current.FencingTokens[moduleID] != 0 {
			return state.TransactionDecision{}, state.ErrStaleFence
		}
		return state.TransactionDecision{Events: []state.Event{event}, Result: work, ReadSet: state.TransactionReadSet{Owners: []string{moduleID}}, Candidate: state.CandidateOptions{Subject: "state(discovery): claim module work", Provenance: state.GitProvenance{Operation: "discovery.module_claim", Event: event.ID, Module: moduleID}}}, nil
	}, func(current state.Snapshot) (state.TransactionResolution, error) {
		currentWork, found := current.Records[workPath]
		if !found || currentWork.Kind != "discovery_work" || !bytes.Equal(currentWork.Data, record.Data) || current.FencingTokens[moduleID] != work.OwnerGeneration {
			return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
		}
		return state.TransactionResolution{Found: true, Value: work}, nil
	})
	if err != nil {
		return state.DiscoveryWork{}, err
	}
	return work, nil
}

func checkpointModuleFailure(ctx context.Context, coordinator state.TransactionCoordinator, prepared state.Snapshot, moduleID, desiredFingerprint string, claim state.DiscoveryWork, resolved moduleResolutionResult, options Options) error {
	workPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		return err
	}
	work := claim
	work.State, work.FailureCategory = resolved.failureState, resolved.failureClass
	work.LeaseExpiresAt = time.Time{}
	work.OwnerRunID = ""
	if work.State == state.DiscoveryWorkRetryable {
		work.RetryAt = options.AcceptedAt.UTC().Add(5 * time.Minute)
	}
	workRecord, err := state.DiscoveryWorkRecord(work)
	if err != nil {
		return err
	}
	events := append([]state.Event(nil), resolved.events...)
	workEvent := state.Event{ID: "discovery:failure:" + strings.TrimPrefix(claim.OperationID, discoveryModuleOperation) + ":" + fmt.Sprint(claim.OwnerGeneration), RecordPath: workPath, Record: workRecord, FenceKey: moduleID, FenceToken: claim.OwnerGeneration}
	events = append(events, workEvent)
	if err = checkOnlyModuleWrites(moduleID, events); err != nil {
		return err
	}
	_, err = coordinator.RunTransaction(ctx, workEvent.ID, func(current state.Snapshot) (state.TransactionDecision, error) {
		currentWork, found := current.Records[workPath]
		claimRecord, recordErr := state.DiscoveryWorkRecord(claim)
		if recordErr != nil || !found || currentWork.Kind != "discovery_work" || !bytes.Equal(currentWork.Data, claimRecord.Data) || current.FencingTokens[moduleID] != claim.OwnerGeneration {
			return state.TransactionDecision{}, state.ErrStaleFence
		}
		preparedWork, preparedExists := prepared.Records[workPath]
		if !preparedExists || preparedWork.Kind != "discovery_work" || !bytes.Equal(preparedWork.Data, claimRecord.Data) {
			return state.TransactionDecision{}, state.ErrStaleFence
		}
		return state.TransactionDecision{Events: events, Result: work, ReadSet: state.TransactionReadSet{Owners: []string{moduleID}}, Candidate: state.CandidateOptions{Subject: "state(discovery): classify module work", Provenance: state.GitProvenance{Operation: "discovery.module_failure", Event: workEvent.ID, Module: moduleID}}}, nil
	}, func(current state.Snapshot) (state.TransactionResolution, error) {
		currentWork, found := current.Records[workPath]
		if !found || currentWork.Kind != "discovery_work" || !bytes.Equal(currentWork.Data, workRecord.Data) || current.FencingTokens[moduleID] != claim.OwnerGeneration {
			return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
		}
		return state.TransactionResolution{Found: true, Value: work}, nil
	})
	return err
}

func validateCurrentCatalog(snapshot state.Snapshot, generation uint64) error {
	latest, err := readDurableCatalog(snapshot)
	if err != nil {
		return err
	}
	if latest == nil || latest.Generation != generation {
		return state.ErrHeadMoved
	}
	return nil
}

func prepareModuleResult(ctx context.Context, resolver source.Resolver, snapshot state.Snapshot, generation uint64, record catalog.ModuleRecord, moduleID string, position, total int, options Options, selected, forced map[string]struct{}, retired bool) (moduleResolutionResult, error) {
	if retired {
		events, err := persistRetirement(snapshot, record, catalog.Delta{Retired: []string{moduleID}}, generation, options.AcceptedAt)
		return moduleResolutionResult{events: events, projectionIDs: retirementProjectionIDs(events, moduleID)}, err
	}
	input := moduleResolutionInput{resolver: resolver, snapshot: snapshot, generation: generation, options: options, selected: selected, forced: forced, restricted: !options.NewModulesOnly}
	return resolveSelectedModule(ctx, input, record, position, total)
}

func retirementProjectionIDs(events []state.Event, moduleID string) []string {
	if len(events) == 0 {
		return nil
	}
	return []string{moduleID}
}

func commitModuleOperation(ctx context.Context, input moduleOperationInput) (moduleOperationResult, error) {
	coordinator, prepared, catalogGeneration := input.coordinator, input.prepared, input.catalogGeneration
	module, moduleID := input.module, input.moduleID
	resolved, options := input.resolved, input.options
	operationRunner, ok := coordinator.(operationTransactionRunner)
	if !ok {
		return moduleOperationResult{}, fmt.Errorf("discovery requires receipt-backed operation transactions")
	}
	workPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		return moduleOperationResult{}, err
	}
	forced := containsModule(options.ForceModuleIDs, moduleID)
	operationID, desiredFingerprint, err := stableModuleOperationIdentity(prepared, moduleID, input.observationID, module, catalogGeneration, options, forced)
	if err != nil {
		return moduleOperationResult{}, err
	}
	if input.work.OperationID != operationID || input.work.DesiredFingerprint != desiredFingerprint {
		return moduleOperationResult{}, state.ErrTransactionIdentityConflict
	}
	workState := state.DiscoveryWorkCompleted
	failureCategory := "none"
	retryAt := time.Time{}
	if resolved.failureState != "" {
		workState, failureCategory = resolved.failureState, resolved.failureClass
		if workState == state.DiscoveryWorkRetryable {
			retryAt = options.AcceptedAt.UTC().Add(5 * time.Minute)
		}
	}
	desiredGeneration := catalogGeneration
	if resolved.pinRef != nil {
		desiredGeneration, err = nextModuleWorkGeneration(prepared, moduleID, catalogGeneration, forced)
		if err != nil {
			return moduleOperationResult{}, err
		}
	}
	receiptPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		return moduleOperationResult{}, err
	}
	receiptPath, err = identity.OperationalRecordPath(receiptPath, "operation_receipt", moduleID)
	if err != nil {
		return moduleOperationResult{}, err
	}
	work := input.work
	work.ReceiptPath, work.State, work.RetryAt, work.FailureCategory = receiptPath, workState, retryAt, failureCategory
	work.OwnerRunID, work.LeaseExpiresAt = "", time.Time{}
	if _, err = state.DiscoveryWorkRecord(work); err != nil {
		return moduleOperationResult{}, err
	}
	expectedEvents := append([]state.Event(nil), resolved.events...)
	workEvent, err := newStateEvent(workPath, "discovery_work", work)
	if err != nil {
		return moduleOperationResult{}, err
	}
	workEvent.FenceKey, workEvent.FenceToken = moduleID, input.work.OwnerGeneration
	expectedEvents = append(expectedEvents, workEvent)
	expectedEvents, projectionIntentID, requestIDs, handoffIDs, err := appendSuccessfulModuleHandoffs(expectedEvents, prepared, moduleID, desiredGeneration, desiredFingerprint, operationID, workState, resolved, options)
	if err != nil {
		return moduleOperationResult{}, err
	}
	result := moduleOperationResult{ModuleID: moduleID, OperationID: operationID, State: workState, RequestIDs: requestIDs, ProjectionID: projectionIntentID, HandoffIDs: handoffIDs}
	if err = prepareModuleEvents(moduleID, expectedEvents); err != nil {
		return moduleOperationResult{}, err
	}
	return executeModuleOperation(ctx, input, operationRunner, workPath, expectedEvents, result, desiredFingerprint)
}

func appendSuccessfulModuleHandoffs(events []state.Event, snapshot state.Snapshot, moduleID string, generation uint64, inputFingerprint, operationID, workState string, resolved moduleResolutionResult, options Options) ([]state.Event, string, []string, []string, error) {
	if workState != state.DiscoveryWorkCompleted {
		return events, "", nil, nil, nil
	}
	history, err := readPinHistory(snapshot, moduleID)
	if err != nil {
		return events, "", nil, nil, err
	}
	var currentPin source.ResolvedSource
	if resolved.pinRef != nil {
		currentPin = resolvedSourceFromResult(snapshot, moduleID, resolved)
	} else if history.OverallLatest != nil {
		currentPin = *history.OverallLatest
	}
	var queued []queue.Request
	for _, event := range events {
		if !event.Delete && event.Record.Kind == "request" {
			var request queue.Request
			if json.Unmarshal(event.Record.Data, &request) == nil && request.ModuleID == moduleID && request.Status == queue.StatusQueued {
				queued = append(queued, request)
			}
		}
	}
	if len(resolved.projectionIDs) == 0 || currentPin.ModuleID == "" || len(queued) == 0 {
		return events, "", nil, nil, nil
	}
	requestIDs := make([]string, 0, len(queued))
	handoffIDs := make([]string, 0, len(queued))
	var projectionID string
	for index, request := range queued {
		intentID, handoffID, handoffErr := appendDiscoveryHandoffEvents(&events, snapshot, moduleID, request.Work.DesiredGeneration, request.Fingerprint, request.Work.AcceptedFactsID, operationID, currentPin, queued, options, index == 0)
		if handoffErr != nil {
			return nil, "", nil, nil, handoffErr
		}
		if projectionID == "" {
			projectionID = intentID
		} else if intentID != projectionID {
			return nil, "", nil, nil, state.ErrTransactionIdentityConflict
		}
		if handoffID != "" {
			requestIDs = append(requestIDs, request.ID)
			handoffIDs = append(handoffIDs, handoffID)
		}
	}
	return events, projectionID, requestIDs, handoffIDs, nil
}

func prepareModuleEvents(moduleID string, events []state.Event) error {
	if err := checkOnlyModuleWrites(moduleID, events); err != nil {
		return err
	}
	return nil
}

func executeModuleOperation(ctx context.Context, input moduleOperationInput, operationRunner operationTransactionRunner, workPath string, expectedEvents []state.Event, result moduleOperationResult, desiredFingerprint string) (moduleOperationResult, error) {
	moduleID, operationID := input.moduleID, result.OperationID
	if err := validateReceiptWorkEvent(moduleID, operationID, desiredFingerprint, expectedEvents); err != nil {
		return moduleOperationResult{}, err
	}
	receiptResult := moduleOperationReceiptResult{
		Result: result, DesiredFingerprint: desiredFingerprint, Events: append([]state.Event(nil), expectedEvents...),
	}
	resultJSON, err := json.Marshal(receiptResult)
	if err != nil {
		return moduleOperationResult{}, err
	}
	resultResolver := func(state.Snapshot) (state.TransactionResolution, error) {
		if validateReceiptWorkEvent(moduleID, operationID, desiredFingerprint, receiptResult.Events) != nil {
			return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
		}
		return state.TransactionResolution{Found: true, Value: resultJSON}, nil
	}
	transactionResult, err := operationRunner.RunOperationTransaction(ctx, moduleID, operationID, expectedEvents, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
		currentAccepted, readErr := readDurableCatalog(snapshot)
		if readErr != nil || currentAccepted == nil || currentAccepted.Generation != input.catalogGeneration {
			if readErr != nil {
				return state.TransactionDecision{}, readErr
			}
			return state.TransactionDecision{}, state.ErrHeadMoved
		}
		for _, event := range expectedEvents {
			before, beforeExists := input.prepared.Records[event.RecordPath]
			now, nowExists := snapshot.Records[event.RecordPath]
			if beforeExists != nowExists || (beforeExists && !reflect.DeepEqual(before, now)) {
				return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
			}
		}
		currentWork, found := snapshot.Records[workPath]
		claimRecord, claimErr := state.DiscoveryWorkRecord(input.work)
		if claimErr != nil || !found || currentWork.Kind != "discovery_work" || !bytes.Equal(currentWork.Data, claimRecord.Data) || snapshot.FencingTokens[moduleID] != input.work.OwnerGeneration {
			return state.TransactionDecision{}, state.ErrStaleFence
		}
		return state.TransactionDecision{ReadSet: state.TransactionReadSet{Owners: []string{moduleID}}, Events: expectedEvents, Result: receiptResult, Candidate: state.CandidateOptions{Subject: "state(discovery): persist module lifecycle", Provenance: state.GitProvenance{Operation: "discovery.module", Event: operationID, Module: moduleID}}}, nil
	}, resultResolver)
	if err != nil {
		if errors.Is(err, state.ErrTransactionIdentityConflict) || errors.Is(err, state.ErrTransactionConflict) {
			return moduleOperationResult{}, fmt.Errorf("discovery module %s transaction conflict: %w", moduleID, err)
		}
		return moduleOperationResult{}, err
	}
	if transactionResult.Value != nil {
		if replayed, ok := transactionResult.Value.(moduleOperationReceiptResult); ok {
			if reflect.DeepEqual(replayed, receiptResult) {
				return replayed.Result, nil
			}
			return moduleOperationResult{}, state.ErrTransactionIdentityConflict
		}
		if raw, ok := transactionResult.Value.(json.RawMessage); ok {
			if json.Unmarshal(raw, &receiptResult) == nil && reflect.DeepEqual(receiptResult, moduleOperationReceiptResult{
				Result: result, DesiredFingerprint: desiredFingerprint, Events: expectedEvents,
			}) {
				return receiptResult.Result, nil
			}
		}
	}
	return result, nil
}

func appendDiscoveryHandoffEvents(events *[]state.Event, snapshot state.Snapshot, moduleID string, generation uint64, inputFingerprint, acceptedFactsID, operationID string, pin source.ResolvedSource, pending []queue.Request, options Options, includeIntent bool) (string, string, error) {
	transitionID := "discovery:" + strings.TrimPrefix(operationID, discoveryModuleOperation)
	intentGeneration := generation
	if prior, exists := snapshot.Records[projectionIntentPath+moduleID+".json"]; exists && prior.Kind == "projection_intent" {
		var previous projection.DesiredIntent
		if err := json.Unmarshal(prior.Data, &previous); err == nil && previous.ValidatePath(projectionIntentPath+moduleID+".json") == nil {
			if intentGeneration < previous.Generation {
				intentGeneration = previous.Generation
			}
		} else {
			// Older discovery status records remain the current source of lifecycle
			// status until their producer is converted to the neutral contract.
			var legacy ProjectionIntent
			if json.Unmarshal(prior.Data, &legacy) != nil || prior.SchemaVersion != recordSchemaVersion || legacy.ModuleID != moduleID || legacy.DesiredGeneration == 0 || legacy.Status == "" {
				return "", "", state.ErrSelectionStale
			}
			if intentGeneration < legacy.DesiredGeneration {
				intentGeneration = legacy.DesiredGeneration
			}
		}
	}
	status := "backlog"
	statusFields := []projection.OwnedField{{Name: "avm_release_version", Value: pin.Version}, {Name: "source_version", Value: pin.Version}, {Name: "source_sha", Value: pin.Commit}}
	statusSnapshot, err := snapshotAfterPlannedRequestDeletions(snapshot, *events)
	if err != nil {
		return "", "", err
	}
	if reduced, controlling, reduceErr := queue.ReduceModuleProjectionStatus(statusSnapshot, moduleID, pending); reduceErr != nil {
		return "", "", fmt.Errorf("reduce controlling module status for discovery handoff: %w", reduceErr)
	} else if controlling {
		status = queue.NeutralStatus(reduced.Status)
		if status == "" || reduced.SourceVersion == "" || reduced.SourceSHA == "" {
			return "", "", fmt.Errorf("active module status lacks a neutral projection identity")
		}
		statusFields = []projection.OwnedField{
			{Name: "active_release_version", Value: reduced.SourceVersion},
			{Name: "avm_release_version", Value: reduced.SourceVersion},
			{Name: "source_version", Value: reduced.SourceVersion},
			{Name: "source_sha", Value: reduced.SourceSHA},
		}
		if reduced.Reason != "" {
			statusFields = append(statusFields, projection.OwnedField{Name: "status_reason", Value: reduced.Reason})
		}
	}
	intent, err := projection.NewDesiredIntent(moduleID, intentGeneration, transitionID, status, statusFields, nil)
	if err != nil {
		return "", "", err
	}
	if options.Producer.WorkflowID == "" || options.Producer.Commit == "" || options.Producer.RunID == "" || options.Producer.RunAttempt == 0 {
		return "", "", fmt.Errorf("discovery producer provenance is required for a stage handoff")
	}
	intentPath, err := identity.StateProjectionIntentPath(moduleID)
	if err != nil {
		return "", "", err
	}
	intentEvent, err := newStateEvent(intentPath, "projection_intent", intent)
	if err != nil {
		return "", "", err
	}
	if previous, exists := snapshot.Records[intentPath]; exists && previous.Kind == intentEvent.Record.Kind && bytes.Equal(previous.Data, intentEvent.Record.Data) {
		return intent.IntentID, "", nil
	}
	if includeIntent {
		*events = append(*events, intentEvent)
	}
	identityTuple, err := intent.Identity()
	if err != nil {
		return "", "", err
	}
	inputFingerprintID, err := identity.ID("input-fingerprint", recordSchemaVersion, inputFingerprint)
	if err != nil {
		return "", "", err
	}
	handoff, err := projection.NewHandoff(projection.Handoff{
		ModuleID: moduleID, Generation: generation, TransitionID: transitionID,
		InputFingerprint: inputFingerprintID, OperationID: operationID, AcceptedFactsID: acceptedFactsID, Producer: options.Producer,
		ConsumerStage: "acquire_source", Projection: identityTuple,
	}, options.AcceptedAt)
	if err != nil {
		return "", "", err
	}
	handoffPath, err := identity.StateProjectionHandoffPath(moduleID, handoff.ID)
	if err != nil {
		return "", "", err
	}
	handoffEvent, err := newStateEvent(handoffPath, "stage_handoff", handoff)
	if err != nil {
		return "", "", err
	}
	*events = append(*events, handoffEvent)
	return intent.IntentID, handoff.ID, nil
}

func snapshotAfterPlannedRequestDeletions(snapshot state.Snapshot, events []state.Event) (state.Snapshot, error) {
	updated := snapshot
	recordsCopied := false
	for _, event := range events {
		if !event.Delete {
			continue
		}
		current, exists := snapshot.Records[event.RecordPath]
		if !exists || !reflect.DeepEqual(current, event.Record) || event.Record.Kind != "request" {
			return state.Snapshot{}, state.ErrRecordChanged
		}
		if !recordsCopied {
			updated.Records = make(map[string]state.Record, len(snapshot.Records))
			for recordPath, record := range snapshot.Records {
				updated.Records[recordPath] = record
			}
			recordsCopied = true
		}
		delete(updated.Records, event.RecordPath)
	}
	return updated, nil
}

func withoutLegacyProjectionIntent(events []state.Event) []state.Event {
	filtered := make([]state.Event, 0, len(events))
	for _, event := range events {
		if event.Record.Kind != "projection_intent" {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func resolvedSourceFromResult(snapshot state.Snapshot, moduleID string, result moduleResolutionResult) source.ResolvedSource {
	history, err := readPinHistory(snapshot, moduleID)
	if err == nil && history.OverallLatest != nil {
		return *history.OverallLatest
	}
	for _, event := range result.events {
		if event.Record.Kind == "source_pins" {
			var pinHistory PinHistory
			if json.Unmarshal(event.Record.Data, &pinHistory) == nil && pinHistory.OverallLatest != nil {
				return *pinHistory.OverallLatest
			}
		}
	}
	return source.ResolvedSource{}
}

func checkOnlyModuleWrites(moduleID string, events []state.Event) error {
	for _, event := range events {
		physicalPath, err := identity.OperationalRecordPath(event.RecordPath, event.Record.Kind, moduleID)
		if err != nil || !strings.HasPrefix(physicalPath, "state/modules/") || !strings.Contains(physicalPath, "/"+moduleID+"/") {
			return fmt.Errorf("discovery module write set crosses its owner")
		}
		var owner struct {
			ModuleID string `json:"module_id"`
		}
		if json.Unmarshal(event.Record.Data, &owner) != nil || owner.ModuleID != moduleID {
			return fmt.Errorf("discovery module write set crosses its owner")
		}
	}
	return nil
}

func acceptedDiscoverySelection(snapshot state.Snapshot, accepted catalog.AcceptedState, delta catalog.Delta, options Options) (catalog.AcceptedSelection, error) {
	provenance := catalog.ManualExactScope
	moduleIDs := options.SelectedModuleIDs
	if len(moduleIDs) == 0 && len(options.ForceModuleIDs) > 0 {
		moduleIDs = options.ForceModuleIDs
	}
	if len(moduleIDs) == 0 && len(options.ForceModuleIDs) == 0 && !options.NewModulesOnly {
		provenance = catalog.FactoryAllActive
	}
	if options.NewModulesOnly {
		provenance = catalog.FactoryNewOnly
		for _, record := range accepted.Modules {
			if record.Active && containsModule(delta.Added, record.Module.ID) {
				moduleIDs = append(moduleIDs, record.Module.ID)
			}
		}
		recovered, err := findFactorySelection(snapshot, accepted.Generation)
		if err != nil {
			return catalog.AcceptedSelection{}, err
		}
		if recovered != nil {
			return *recovered, nil
		}
		return catalog.NewAcceptedSelection(accepted.Generation, moduleIDs, provenance)
	}
	if !options.NewModulesOnly && len(moduleIDs) == 0 && len(options.ForceModuleIDs) == 0 {
		for _, record := range accepted.Modules {
			if record.Active {
				moduleIDs = append(moduleIDs, record.Module.ID)
			}
		}
	}
	return catalog.NewAcceptedSelection(accepted.Generation, moduleIDs, provenance)
}

func findFactorySelection(snapshot state.Snapshot, generation uint64) (*catalog.AcceptedSelection, error) {
	var recovered *catalog.AcceptedSelection
	for _, record := range snapshot.Records {
		if record.Kind != "discovery_observation" || record.SchemaVersion != recordSchemaVersion {
			continue
		}
		var observation Observation
		if err := json.Unmarshal(record.Data, &observation); err != nil {
			return nil, fmt.Errorf("decode accepted discovery observation: %w", err)
		}
		if observation.AcceptedGeneration != generation || observation.Selection.SchemaVersion() == 0 {
			continue
		}
		if err := observation.Selection.Validate(); err != nil {
			return nil, state.ErrTransactionIdentityConflict
		}
		if observation.Selection.CatalogGeneration() != generation || observation.Selection.Provenance() != catalog.FactoryNewOnly {
			continue
		}
		if recovered != nil && recovered.ID() != observation.Selection.ID() {
			return nil, state.ErrTransactionIdentityConflict
		}
		selectionValue := observation.Selection
		recovered = &selectionValue
	}
	return recovered, nil
}

func missingAcceptedFactoryModules(snapshot state.Snapshot, selection catalog.AcceptedSelection) []string {
	missing := make([]string, 0, len(selection.ModuleIDs()))
	for _, moduleID := range selection.ModuleIDs() {
		if !hasDurableDiscoveryOperation(snapshot, moduleID, selection.CatalogGeneration()) {
			missing = append(missing, moduleID)
		}
	}
	return missing
}

func hasDurableDiscoveryOperation(snapshot state.Snapshot, moduleID string, generation uint64) bool {
	workPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		return false
	}
	record, exists := snapshot.Records[workPath]
	if !exists || record.Kind != "discovery_work" {
		return false
	}
	var work state.DiscoveryWork
	if json.Unmarshal(record.Data, &work) != nil || work.ModuleID != moduleID || work.State != state.DiscoveryWorkCompleted {
		return false
	}
	return strings.Contains(work.OperationID, discoveryModuleOperation) && work.DesiredFingerprint != "" && generation > 0
}

func hasDurableDiscoveryModuleResult(snapshot state.Snapshot, moduleID string) bool {
	for _, path := range []string{
		"state/discovery/source-pins/" + moduleID + ".json",
		projectionIntentPath + moduleID + ".json",
	} {
		if _, exists := snapshot.Records[path]; exists {
			return true
		}
	}
	for _, record := range snapshot.Records {
		if record.Kind != "request" {
			continue
		}
		var request queue.Request
		if err := json.Unmarshal(record.Data, &request); err == nil && request.ModuleID == moduleID {
			return true
		}
	}
	return false
}

// validateDiscoveryOptions rejects conflicting scope before any backend or source read.
func validateDiscoveryOptions(options Options) error {
	if options.AcceptedAt.IsZero() || strings.TrimSpace(options.ExtractorContract) == "" || len(options.Capabilities) == 0 {
		return fmt.Errorf("discovery timestamp and reviewed extractor contract are required")
	}
	if options.NewModulesOnly && (len(options.SelectedModuleIDs) > 0 || len(options.ForceModuleIDs) > 0) {
		return fmt.Errorf("new-module discovery cannot be combined with explicit or forced scope")
	}
	return nil
}

func appendAcceptedCatalogEvent(events []state.Event, previous *catalog.AcceptedState, accepted catalog.AcceptedState, delta catalog.Delta) ([]state.Event, error) {
	if previous != nil && delta.NoChange {
		return events, nil
	}
	record := DurableCatalogState{SchemaVersion: recordSchemaVersion, Accepted: accepted}
	event, err := newStateEvent("state/discovery/catalog.json", "discovery_catalog", record)
	if err != nil {
		return nil, err
	}
	return append(events, event), nil
}

func readCoordinatorSnapshot(ctx context.Context, coordinator state.TransactionCoordinator) (state.Snapshot, error) {
	reader, ok := coordinator.(state.GitReader)
	if !ok {
		return state.Snapshot{}, fmt.Errorf("discovery preparation requires a verified state reader")
	}
	_, snapshot, err := reader.Read(ctx)
	return snapshot, err
}

func readDiscoveryObservation(snapshot state.Snapshot, observationID string) (Observation, bool, error) {
	path := discoveryObservationPath + observationID + ".json"
	record, exists := snapshot.Records[path]
	if !exists {
		return Observation{}, false, nil
	}
	if record.Kind != "discovery_observation" || record.SchemaVersion != recordSchemaVersion {
		return Observation{}, false, state.ErrTransactionIdentityConflict
	}
	var observation Observation
	if err := json.Unmarshal(record.Data, &observation); err != nil {
		return Observation{}, false, err
	}
	expectedID, err := identity.ID("discovery-observation", recordSchemaVersion, observation)
	if err != nil || expectedID != observationID {
		return Observation{}, false, state.ErrTransactionIdentityConflict
	}
	return observation, true, nil
}

type moduleResolutionInput struct {
	resolver   source.Resolver
	snapshot   state.Snapshot
	generation uint64
	delta      catalog.Delta
	options    Options
	selected   map[string]struct{}
	forced     map[string]struct{}
	restricted bool
}

type moduleResolutionResult struct {
	pinRef        *CatalogPinRef
	pinsChanged   bool
	projectionIDs []string
	events        []state.Event
	failureState  string
	failureClass  string
}

// resolutionModuleCount counts the active source-resolution scope, not catalog bookkeeping.
func resolutionModuleCount(records []catalog.ModuleRecord, selected []string, restricted bool) int {
	if restricted {
		return len(selected)
	}
	total := 0
	for _, record := range records {
		if record.Active {
			total++
		}
	}
	return total
}

// resolveSelectedModule reports preparation; durability is acknowledged only by Persist.
func resolveSelectedModule(ctx context.Context, input moduleResolutionInput, record catalog.ModuleRecord, position, total int) (moduleResolutionResult, error) {
	ctx, finish := progress.Start(ctx, "discovery", "module_prepare", slog.String("module_id", record.Module.ID), slog.Int("position", position), slog.Int("total", total))
	defer finish(false)
	result, err := resolveModuleRecord(ctx, input, record)
	if err != nil {
		return result, err
	}
	progress.Logger(ctx).InfoContext(ctx, "discovery module prepared", "events", len(result.events), "projection_required", len(result.projectionIDs) > 0)
	finish(true)
	return result, nil
}

func resolveModuleRecord(ctx context.Context, input moduleResolutionInput, record catalog.ModuleRecord) (moduleResolutionResult, error) {
	_, selected := input.selected[record.Module.ID]
	if !record.Active {
		if input.restricted && !selected {
			return moduleResolutionResult{}, nil
		}
		events, err := persistRetirement(input.snapshot, record, input.delta, input.generation, input.options.AcceptedAt)
		return moduleResolutionResult{events: events}, err
	}
	history, err := readPinHistory(input.snapshot, record.Module.ID)
	if err != nil {
		return moduleResolutionResult{}, err
	}
	_, forced := input.forced[record.Module.ID]
	if input.restricted && !selected {
		return projectionPinReference(record.Module.ID, history), nil
	}
	return resolveActiveModule(ctx, input, record.Module, history, forced)
}

func projectionPinReference(moduleID string, history PinHistory) moduleResolutionResult {
	if history.OverallLatest == nil {
		return moduleResolutionResult{}
	}
	pin := *history.OverallLatest
	return moduleResolutionResult{pinRef: &CatalogPinRef{ModuleID: moduleID, Version: pin.Version, Tag: pin.Tag, Commit: pin.Commit}}
}

func persistResolutionFailure(input moduleResolutionInput, moduleID string, history PinHistory, resolveErr error, forced bool) (moduleResolutionResult, error) {
	status, reason := classifyResolutionError(resolveErr)
	failureState, failureClass := state.DiscoveryWorkRetryable, "source_unavailable"
	if status == IntentNeedsReview {
		failureState, failureClass = state.DiscoveryWorkNeedsReview, "policy_rejected"
	}
	result := moduleResolutionResult{failureState: failureState, failureClass: failureClass, pinRef: projectionPinReference(moduleID, history).pinRef}
	intent, changed, err := buildProjectionIntent(input.snapshot, projectionIntentInput{
		ModuleID: moduleID, Generation: input.generation, Status: status, Reason: reason,
		Source: history.OverallLatest, UpdatedAt: input.options.AcceptedAt,
	})
	if err != nil {
		return moduleResolutionResult{}, err
	}
	if changed {
		event, eventErr := newStateEvent(projectionIntentPath+moduleID+".json", "projection_intent", intent)
		if eventErr != nil {
			return moduleResolutionResult{}, eventErr
		}
		result.events = append(result.events, event)
	}
	return result, nil
}

func resolveActiveModule(ctx context.Context, input moduleResolutionInput, module catalog.Module, history PinHistory, forced bool) (moduleResolutionResult, error) {
	ctx, finish := progress.Start(ctx, "discovery", "source_resolution", slog.String("module_id", module.ID))
	defer finish(false)
	var pins []source.ResolvedSource
	var err error
	if history.OverallLatest == nil {
		pin, resolveErr := input.resolver.ResolveLatest(ctx, module, nil)
		if resolveErr != nil {
			err = resolveErr
		} else {
			pins = []source.ResolvedSource{pin}
		}
	} else {
		pins, err = input.resolver.ResolveStableReleasesAfter(ctx, module, *history.OverallLatest)
		if err == nil && len(pins) == 0 && forced {
			pins = []source.ResolvedSource{*history.OverallLatest}
		}
	}
	if err != nil {
		status, _ := classifyResolutionError(err)
		progress.Logger(ctx).WarnContext(ctx, "source resolution unavailable", "status", status, "reason", resolutionLogReason(err))
		result, failureErr := persistResolutionFailure(input, module.ID, history, err, forced)
		if failureErr == nil && status == IntentNeedsReview {
			result.failureState, result.failureClass = state.DiscoveryWorkNeedsReview, "policy_rejected"
		}
		if failureErr == nil {
			progress.Logger(ctx).WarnContext(ctx, "source resolution classified", "state", result.failureState, "failure_category", result.failureClass)
		}
		return result, failureErr
	}
	if len(pins) == 0 {
		if history.OverallLatest != nil && hasQueuedUnversionedCurrentAcquisition(input.snapshot, module.ID, *history.OverallLatest) {
			result, repairErr := recordResolvedModules(ctx, input, module.ID, history, nil, false)
			if repairErr != nil {
				return moduleResolutionResult{}, repairErr
			}
			progress.Logger(ctx).InfoContext(ctx, "source release check complete", "release_count", 0, "source_changed", false, "forced", false, "acquisition_prepared", true, "reason", "versionless_current_request")
			finish(true)
			return result, nil
		}
		progress.Logger(ctx).InfoContext(ctx, "source release check complete", "release_count", 0, "source_changed", false, "forced", forced, "acquisition_prepared", false)
		finish(true)
		return projectionPinReference(module.ID, history), nil
	}
	for index := range pins {
		pins[index].ResolvedAt = input.options.AcceptedAt.UTC()
	}
	result, err := recordResolvedModules(ctx, input, module.ID, history, pins, forced)
	if err == nil {
		latest := pins[len(pins)-1]
		progress.Logger(ctx).InfoContext(ctx, "source releases resolved", "release_count", len(pins), "latest_version", latest.Version, "source_sha", latest.Commit, "source_changed", result.pinsChanged, "forced", forced, "acquisition_prepared", result.pinsChanged || history.OverallLatest == nil || forced)
		finish(true)
	}
	return result, err
}

func recordResolvedModules(ctx context.Context, input moduleResolutionInput, moduleID string, history PinHistory, pins []source.ResolvedSource, forced bool) (moduleResolutionResult, error) {
	queueVersionlessCurrent := len(pins) == 0 && history.OverallLatest != nil && hasQueuedUnversionedCurrentAcquisition(input.snapshot, moduleID, *history.OverallLatest)
	if len(pins) == 0 {
		if !queueVersionlessCurrent {
			return projectionPinReference(moduleID, history), nil
		}
		pins = []source.ResolvedSource{*history.OverallLatest}
	}
	updatedHistory := history
	pinsChanged := false
	var err error
	for _, pin := range pins {
		var changed bool
		updatedHistory, changed, err = addPinObservation(updatedHistory, moduleID, pin)
		if err != nil {
			return moduleResolutionResult{}, err
		}
		pinsChanged = pinsChanged || changed
	}
	currentPin := pins[len(pins)-1]
	result := moduleResolutionResult{pinsChanged: pinsChanged}
	if pinsChanged {
		if updatedHistory.OverallLatest != nil {
			currentPin = *updatedHistory.OverallLatest
		}
		event, eventErr := newStateEvent("state/discovery/source-pins/"+moduleID+".json", "source_pins", updatedHistory)
		if eventErr != nil {
			return moduleResolutionResult{}, eventErr
		}
		result.events = append(result.events, event)
	} else if history.OverallLatest != nil {
		currentPin = *history.OverallLatest
	}
	result.pinRef = &CatalogPinRef{ModuleID: moduleID, Version: currentPin.Version, Tag: currentPin.Tag, Commit: currentPin.Commit}
	queueAcquisition := pinsChanged || history.OverallLatest == nil || forced || queueVersionlessCurrent
	desiredGeneration := input.generation
	if queueAcquisition {
		desiredGeneration, err = nextModuleWorkGeneration(input.snapshot, moduleID, input.generation, forced)
		if err != nil {
			return moduleResolutionResult{}, err
		}
	}
	intent, intentChanged, err := buildProjectionIntent(input.snapshot, projectionIntentInput{
		ModuleID: moduleID, Generation: desiredGeneration, Status: IntentBacklog,
		Reason: "source facts are not yet accepted for assessment", Source: &currentPin,
		UpdatedAt: input.options.AcceptedAt, Force: forced,
	})
	if err != nil {
		return moduleResolutionResult{}, err
	}
	if queueAcquisition {
		queuedPins := pins
		if forced && !pinsChanged && history.OverallLatest != nil {
			queuedPins = []source.ResolvedSource{*history.OverallLatest}
		} else if queueVersionlessCurrent && len(queuedPins) == 0 && history.OverallLatest != nil {
			queuedPins = []source.ResolvedSource{*history.OverallLatest}
		}
		for index, pin := range queuedPins {
			refreshGeneration := uint64(0)
			if forced && !pinsChanged && index == len(queuedPins)-1 {
				refreshGeneration = desiredGeneration
			}
			coalesceSnapshot, filterErr := snapshotWithoutInvalidVersionlessAcquisitions(input.snapshot, moduleID)
			if filterErr != nil {
				return moduleResolutionResult{}, filterErr
			}
			requestEvents, requestErr := acquisitionEvents(ctx, coalesceSnapshot, moduleID, pin, desiredGeneration, refreshGeneration, input.options)
			if requestErr != nil {
				return moduleResolutionResult{}, requestErr
			}
			result.events = append(result.events, requestEvents...)
		}
		legacyEvents, legacyErr := repairVersionlessCurrentAcquisitions(input.snapshot, moduleID, currentPin, input.options.AcceptedAt)
		if legacyErr != nil {
			return moduleResolutionResult{}, legacyErr
		}
		result.events = append(result.events, legacyEvents...)
	} else {
		progress.Logger(ctx).InfoContext(ctx, "acquisition skipped", "reason", "unchanged source without force")
	}
	if intentChanged {
		intentEvent, eventErr := newStateEvent(projectionIntentPath+moduleID+".json", "projection_intent", intent)
		if eventErr != nil {
			return moduleResolutionResult{}, eventErr
		}
		result.events = append(result.events, intentEvent)
	}
	if pinsChanged || forced || queueVersionlessCurrent {
		result.projectionIDs = []string{moduleID}
	}
	return result, nil
}

// hasQueuedUnversionedCurrentAcquisition identifies the pre-versioned queued
// request shape that cannot acquire the module lifecycle slot. It triggers a
// repair only for an unstarted request pinned to the exact current source;
// completed, active, failed, and other-release work retain their existing state.
func hasQueuedUnversionedCurrentAcquisition(snapshot state.Snapshot, moduleID string, pin source.ResolvedSource) bool {
	legacyQueued := false
	for _, request := range moduleRequests(snapshot, moduleID) {
		if request.Work == nil || request.Work.Phase != queue.PhaseAcquireSource || request.SourceSHA != pin.Commit {
			continue
		}
		if request.Work.AVMReleaseVersion == pin.Version {
			return false
		}
		if isVersionlessCurrentRequest(request, moduleID, pin) && isUnstartedVersionlessRequest(request) && request.Status == queue.StatusQueued {
			legacyQueued = true
		}
	}
	return legacyQueued
}

func isVersionlessCurrentRequest(request queue.Request, moduleID string, pin source.ResolvedSource) bool {
	return request.ModuleID == moduleID && request.Work != nil && request.Work.Phase == queue.PhaseAcquireSource &&
		request.Work.AVMReleaseVersion == "" && request.SourceSHA == pin.Commit
}

func isUnstartedVersionlessRequest(request queue.Request) bool {
	if request.Status != queue.StatusQueued && request.Status != queue.StatusSuperseded {
		return false
	}
	if request.Attempt != 1 || request.OwnerRunID != "" || request.LastAcceptedFingerprint != "" || request.LastAcceptedSourceSHA != "" || request.Work == nil {
		return false
	}
	work := request.Work
	return work.AcceptedFactsID == "" && work.AttemptID == "" && work.Fence == 0 && work.Lease == nil && work.Completion == nil &&
		work.RetryClass == queue.RetryNone && work.RetryAt.IsZero()
}

func snapshotWithoutInvalidVersionlessAcquisitions(snapshot state.Snapshot, moduleID string) (state.Snapshot, error) {
	filtered := snapshot
	filtered.Records = make(map[string]state.Record, len(snapshot.Records))
	for recordPath, record := range snapshot.Records {
		filtered.Records[recordPath] = record
	}
	for _, request := range moduleRequests(snapshot, moduleID) {
		if request.ModuleID != moduleID || request.Work == nil || request.Work.Phase != queue.PhaseAcquireSource || request.Work.AVMReleaseVersion != "" ||
			!isUnstartedVersionlessRequest(request) || !errors.Is(request.Validate(), queue.ErrRequestFingerprintMismatch) {
			continue
		}
		recordPath, _, err := exactModuleRequestRecord(snapshot, request)
		if err != nil {
			return state.Snapshot{}, err
		}
		delete(filtered.Records, recordPath)
	}
	return filtered, nil
}

func repairVersionlessCurrentAcquisitions(snapshot state.Snapshot, moduleID string, pin source.ResolvedSource, at time.Time) ([]state.Event, error) {
	var events []state.Event
	for _, request := range moduleRequests(snapshot, moduleID) {
		if request.ModuleID != moduleID || request.Work == nil || request.Work.Phase != queue.PhaseAcquireSource || request.Work.AVMReleaseVersion != "" ||
			!isUnstartedVersionlessRequest(request) {
			continue
		}
		validationErr := request.Validate()
		if errors.Is(validationErr, queue.ErrRequestFingerprintMismatch) {
			recordPath, record, recordErr := exactModuleRequestRecord(snapshot, request)
			if recordErr != nil {
				return nil, recordErr
			}
			event, err := state.NewRequestDeletionEvent(recordPath, record)
			if err != nil {
				return nil, fmt.Errorf("delete obsolete versionless current acquisition %s: %w", request.ID, err)
			}
			events = append(events, event)
			continue
		}
		if validationErr != nil {
			if isVersionlessCurrentRequest(request, moduleID, pin) {
				return nil, fmt.Errorf("validate versionless current acquisition %s: %w", request.ID, validationErr)
			}
			continue
		}
		if !isVersionlessCurrentRequest(request, moduleID, pin) || request.Status != queue.StatusQueued {
			continue
		}
		request.Status = queue.StatusSuperseded
		request.UpdatedAt = at.UTC()
		event, err := newStateEvent("state/requests/"+request.ID+".json", "request", request)
		if err != nil {
			return nil, fmt.Errorf("supersede versionless current acquisition %s: %w", request.ID, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func exactModuleRequestRecord(snapshot state.Snapshot, request queue.Request) (string, state.Record, error) {
	recordPath := "state/requests/" + request.ID + ".json"
	record, exists := snapshot.Records[recordPath]
	if !exists || record.Kind != "request" {
		return "", state.Record{}, fmt.Errorf("obsolete request %s is missing its exact state record", request.ID)
	}
	var stored queue.Request
	if json.Unmarshal(record.Data, &stored) != nil || !reflect.DeepEqual(stored, request) {
		return "", state.Record{}, state.ErrTransactionIdentityConflict
	}
	return recordPath, record, nil
}

func persistRetirement(snapshot state.Snapshot, module catalog.ModuleRecord, delta catalog.Delta, generation uint64, at time.Time) ([]state.Event, error) {
	if !containsModule(delta.Retired, module.Module.ID) {
		return nil, nil
	}
	intent, changed, err := buildProjectionIntent(snapshot, projectionIntentInput{
		ModuleID: module.Module.ID, Generation: generation, Status: IntentRetired,
		Reason: "confirmed absent in two successful observations", UpdatedAt: at,
	})
	if err != nil || !changed {
		return nil, err
	}
	event, err := newStateEvent(projectionIntentPath+module.Module.ID+".json", "projection_intent", intent)
	if err != nil {
		return nil, err
	}
	return []state.Event{event}, nil
}

func persistResolutionIntent(snapshot state.Snapshot, moduleID string, generation uint64, previous *source.ResolvedSource, resolveErr error, at time.Time) ([]state.Event, error) {
	status, reason := classifyResolutionError(resolveErr)
	return persistIntent(snapshot, moduleID, generation, status, reason, previous, at)
}

func persistIntent(snapshot state.Snapshot, moduleID string, generation uint64, status, reason string, pin *source.ResolvedSource, at time.Time) ([]state.Event, error) {
	intent, changed, err := buildProjectionIntent(snapshot, projectionIntentInput{
		ModuleID: moduleID, Generation: generation, Status: status, Reason: reason, Source: pin, UpdatedAt: at,
	})
	if err != nil || !changed {
		return nil, err
	}
	event, err := newStateEvent(projectionIntentPath+moduleID+".json", "projection_intent", intent)
	if err != nil {
		return nil, err
	}
	return []state.Event{event}, nil
}

func acquisitionEvents(ctx context.Context, snapshot state.Snapshot, moduleID string, pin source.ResolvedSource, generation, refreshGeneration uint64, options Options) ([]state.Event, error) {
	desired, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: moduleID, SourceSHA: pin.Commit, AVMReleaseVersion: pin.Version, DesiredGeneration: generation, RefreshGeneration: refreshGeneration,
		ExtractorContract: options.ExtractorContract, Capabilities: options.Capabilities,
	}, options.AcceptedAt)
	if err != nil {
		return nil, fmt.Errorf("create source acquisition for %s: %w", moduleID, err)
	}
	coalesced, _, err := queue.CoalesceRequests(moduleRequests(snapshot, moduleID), desired)
	if err != nil {
		return nil, fmt.Errorf("coalesce source acquisition for %s: %w", moduleID, err)
	}
	events := make([]state.Event, 0, len(coalesced))
	for _, request := range coalesced {
		requestPath := "state/requests/" + request.ID + ".json"
		if existing, exists := snapshot.Records[requestPath]; exists {
			var previous queue.Request
			if existing.Kind != "request" || json.Unmarshal(existing.Data, &previous) != nil {
				return nil, fmt.Errorf("existing acquisition request %s is invalid", request.ID)
			}
			if reflect.DeepEqual(previous, request) {
				continue
			}
		}
		event, eventErr := newStateEvent(requestPath, "request", request)
		if eventErr != nil {
			return nil, eventErr
		}
		events = append(events, event)
		progress.Logger(ctx).InfoContext(ctx, "acquisition request prepared", "request_id", request.ID, "request_status", request.Status, "desired_generation", generation)
	}
	return events, nil
}

func moduleRequests(snapshot state.Snapshot, moduleID string) []queue.Request {
	requests := make([]queue.Request, 0)
	for _, record := range snapshot.Records {
		if record.Kind != "request" {
			continue
		}
		var request queue.Request
		if err := json.Unmarshal(record.Data, &request); err == nil && request.ModuleID == moduleID {
			requests = append(requests, request)
		}
	}
	sort.Slice(requests, func(left, right int) bool { return requests[left].ID < requests[right].ID })
	return requests
}

func readDurableCatalog(snapshot state.Snapshot) (*catalog.AcceptedState, error) {
	record, exists := snapshot.Records["state/discovery/catalog.json"]
	if !exists {
		return nil, nil
	}
	if record.Kind != "discovery_catalog" || record.SchemaVersion != recordSchemaVersion {
		return nil, fmt.Errorf("durable catalog record has unsupported identity")
	}
	var durable DurableCatalogState
	if err := json.Unmarshal(record.Data, &durable); err != nil {
		return nil, fmt.Errorf("decode durable catalog state: %w", err)
	}
	if durable.SchemaVersion != recordSchemaVersion {
		return nil, fmt.Errorf("unsupported durable catalog schema version %d", durable.SchemaVersion)
	}
	if err := durable.Accepted.Validate(); err != nil {
		return nil, fmt.Errorf("validate durable catalog state: %w", err)
	}
	return &durable.Accepted, nil
}

func readPinHistory(snapshot state.Snapshot, moduleID string) (PinHistory, error) {
	history := PinHistory{SchemaVersion: recordSchemaVersion, ModuleID: moduleID, ReleaseHistory: []source.ResolvedSource{}, TrackCandidates: map[string]source.ResolvedSource{}}
	record, exists := snapshot.Records["state/discovery/source-pins/"+moduleID+".json"]
	if !exists {
		return history, nil
	}
	if record.Kind != "source_pins" || record.SchemaVersion != recordSchemaVersion {
		return PinHistory{}, fmt.Errorf("source pin history has unsupported identity for %s", moduleID)
	}
	if err := json.Unmarshal(record.Data, &history); err != nil {
		return PinHistory{}, fmt.Errorf("decode source pin history for %s: %w", moduleID, err)
	}
	if err := history.Validate(moduleID); err != nil {
		return PinHistory{}, err
	}
	return history, nil
}

func (history PinHistory) Validate(moduleID string) error {
	if history.SchemaVersion != recordSchemaVersion || history.ModuleID != moduleID || history.TrackCandidates == nil {
		return fmt.Errorf("source pin history metadata is invalid for %s", moduleID)
	}
	for _, pin := range history.ReleaseHistory {
		if err := validateResolvedPin(moduleID, pin); err != nil {
			return err
		}
	}
	if history.OverallLatest != nil {
		if err := validateResolvedPin(moduleID, *history.OverallLatest); err != nil {
			return err
		}
	}
	for track, pin := range history.TrackCandidates {
		if strings.TrimSpace(track) == "" {
			return fmt.Errorf("source pin track key is empty for %s", moduleID)
		}
		if err := validateResolvedPin(moduleID, pin); err != nil {
			return err
		}
	}
	return nil
}

func validateResolvedPin(moduleID string, pin source.ResolvedSource) error {
	if pin.ModuleID != moduleID || pin.Repository == "" || pin.Version == "" || pin.Tag == "" || len(pin.Commit) != 40 || pin.ResolvedAt.IsZero() {
		return fmt.Errorf("source pin identity is invalid for %s", moduleID)
	}
	for _, alias := range pin.Aliases {
		if alias.Tag == "" || alias.Commit != pin.Commit {
			return fmt.Errorf("source pin alias is invalid for %s", moduleID)
		}
	}
	return nil
}

func addPinObservation(history PinHistory, moduleID string, pin source.ResolvedSource) (PinHistory, bool, error) {
	if err := validateResolvedPin(moduleID, pin); err != nil {
		return PinHistory{}, false, err
	}
	if history.TrackCandidates == nil {
		history.TrackCandidates = map[string]source.ResolvedSource{}
	}
	changed := history.OverallLatest == nil || !samePin(*history.OverallLatest, pin)
	if !pinObserved(history.ReleaseHistory, pin) {
		history.ReleaseHistory = append(history.ReleaseHistory, pin)
		changed = true
	}
	if changed {
		latest := pin
		history.OverallLatest = &latest
	}
	return history, changed, nil
}

func pinObserved(history []source.ResolvedSource, candidate source.ResolvedSource) bool {
	for _, existing := range history {
		if samePin(existing, candidate) {
			return true
		}
	}
	return false
}

func samePin(left, right source.ResolvedSource) bool {
	if left.ModuleID != right.ModuleID || left.Repository != right.Repository || left.Version != right.Version || left.Tag != right.Tag || left.Commit != right.Commit || left.ArchiveURL != right.ArchiveURL || left.ReleaseURL != right.ReleaseURL || len(left.Aliases) != len(right.Aliases) {
		return false
	}
	for index := range left.Aliases {
		if left.Aliases[index] != right.Aliases[index] {
			return false
		}
	}
	return true
}

// resolutionLogReason excludes upstream diagnostic text from operational logs.
func resolutionLogReason(err error) string {
	switch {
	case errors.Is(err, source.ErrMovedTag):
		return "accepted tag moved"
	case errors.Is(err, source.ErrNoStableRelease):
		return "no published stable release"
	default:
		return "source metadata unavailable or requires review"
	}
}

func classifyResolutionError(err error) (string, string) {
	switch {
	case errors.Is(err, source.ErrMovedTag), strings.Contains(err.Error(), "ambiguous stable release tags"), strings.Contains(err.Error(), "ambiguous release tags"):
		return IntentNeedsReview, err.Error()
	case errors.Is(err, source.ErrNoStableRelease):
		return IntentBlocked, "no published stable release"
	default:
		return IntentBlocked, err.Error()
	}
}

type projectionIntentInput struct {
	ModuleID   string
	Generation uint64
	Status     string
	Reason     string
	Source     *source.ResolvedSource
	UpdatedAt  time.Time
	Force      bool
}

func buildProjectionIntent(snapshot state.Snapshot, input projectionIntentInput) (ProjectionIntent, bool, error) {
	path := projectionIntentPath + input.ModuleID + ".json"
	previousRecord, exists := snapshot.Records[path]
	var previous *ProjectionIntent
	if exists {
		if previousRecord.Kind != "projection_intent" {
			return ProjectionIntent{}, false, fmt.Errorf("projection intent has wrong record kind for %s", input.ModuleID)
		}
		var decoded ProjectionIntent
		if err := json.Unmarshal(previousRecord.Data, &decoded); err != nil {
			return ProjectionIntent{}, false, fmt.Errorf("decode projection intent for %s: %w", input.ModuleID, err)
		}
		previous = &decoded
	}
	generation := input.Generation
	if previous != nil && previous.Status == input.Status && previous.Reason == input.Reason && sameOptionalPin(previous.Source, input.Source) && !input.Force {
		return *previous, false, nil
	}
	if previous != nil && generation <= previous.DesiredGeneration {
		generation = previous.DesiredGeneration + 1
	}
	intent := ProjectionIntent{
		SchemaVersion: recordSchemaVersion, ModuleID: input.ModuleID, DesiredGeneration: generation,
		Status: input.Status, Reason: input.Reason, Source: cloneOptionalPin(input.Source), UpdatedAt: input.UpdatedAt.UTC(),
	}
	return intent, true, nil
}

// discoverySelection validates independent selection and refresh scopes before resolution.
func discoverySelection(accepted catalog.AcceptedState, options Options) ([]string, []string, error) {
	forced, err := validateForceModuleIDs(accepted, options.ForceModuleIDs)
	if err != nil {
		return nil, nil, err
	}
	selected, err := validateForceModuleIDs(accepted, options.SelectedModuleIDs)
	if err != nil {
		return nil, nil, err
	}
	if len(selected) == 0 {
		selected = forced
	}
	for _, moduleID := range forced {
		if !containsModule(selected, moduleID) {
			return nil, nil, fmt.Errorf("forced module %s is outside the selected scope", moduleID)
		}
	}
	return selected, forced, nil
}

func validateForceModuleIDs(accepted catalog.AcceptedState, requested []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, nil
	}
	active := make(map[string]bool, len(accepted.Modules))
	for _, module := range accepted.Modules {
		active[module.Module.ID] = module.Active
	}
	selected := make(map[string]struct{}, len(requested))
	for _, value := range requested {
		moduleID := strings.TrimSpace(value)
		if moduleID == "" {
			return nil, fmt.Errorf("forced discovery module IDs must not be empty")
		}
		if !active[moduleID] {
			return nil, fmt.Errorf("forced discovery module %q is unknown or inactive", moduleID)
		}
		if _, duplicate := selected[moduleID]; duplicate {
			return nil, fmt.Errorf("forced discovery module %q is duplicated", moduleID)
		}
		selected[moduleID] = struct{}{}
	}
	moduleIDs := make([]string, 0, len(selected))
	for moduleID := range selected {
		moduleIDs = append(moduleIDs, moduleID)
	}
	sort.Strings(moduleIDs)
	return moduleIDs, nil
}

func appendUniqueModuleID(moduleIDs []string, moduleID string) []string {
	for _, existing := range moduleIDs {
		if existing == moduleID {
			return moduleIDs
		}
	}
	return append(moduleIDs, moduleID)
}

func nextModuleWorkGeneration(snapshot state.Snapshot, moduleID string, catalogGeneration uint64, force bool) (uint64, error) {
	if catalogGeneration == 0 {
		return 0, fmt.Errorf("discovery generation is required for %s", moduleID)
	}
	if force {
		moduleHighWater, err := moduleWorkHighWater(snapshot, moduleID)
		if err != nil {
			return 0, err
		}
		if moduleHighWater < catalogGeneration {
			moduleHighWater = catalogGeneration
		}
		return incrementModuleGeneration(moduleID, moduleHighWater)
	}
	moduleHighWater, err := moduleWorkHighWater(snapshot, moduleID)
	if err != nil {
		return 0, err
	}
	if moduleHighWater == 0 || catalogGeneration > moduleHighWater {
		return catalogGeneration, nil
	}
	return incrementModuleGeneration(moduleID, moduleHighWater)
}

func moduleWorkHighWater(snapshot state.Snapshot, moduleID string) (uint64, error) {
	moduleHighWater := uint64(0)
	for _, request := range moduleRequests(snapshot, moduleID) {
		if request.Work != nil && request.Work.DesiredGeneration > moduleHighWater {
			moduleHighWater = request.Work.DesiredGeneration
		}
	}
	if record, exists := snapshot.Records[projectionIntentPath+moduleID+".json"]; exists {
		if record.Kind != "projection_intent" || record.SchemaVersion != recordSchemaVersion {
			return 0, fmt.Errorf("discovery projection intent has invalid identity for %s", moduleID)
		}
		var intent ProjectionIntent
		if err := json.Unmarshal(record.Data, &intent); err != nil {
			return 0, fmt.Errorf("decode discovery projection intent for %s: %w", moduleID, err)
		}
		if intent.ModuleID != moduleID || intent.DesiredGeneration == 0 {
			return 0, fmt.Errorf("discovery projection intent is invalid for %s", moduleID)
		}
		if intent.DesiredGeneration > moduleHighWater {
			moduleHighWater = intent.DesiredGeneration
		}
	}
	return moduleHighWater, nil
}

func incrementModuleGeneration(moduleID string, generation uint64) (uint64, error) {
	if generation == ^uint64(0) {
		return 0, fmt.Errorf("module %s exhausted its desired generation space", moduleID)
	}
	return generation + 1, nil
}

func sameOptionalPin(left, right *source.ResolvedSource) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return samePin(*left, *right)
}

func cloneOptionalPin(pin *source.ResolvedSource) *source.ResolvedSource {
	if pin == nil {
		return nil
	}
	cloned := *pin
	cloned.Aliases = append([]source.TagObservation(nil), pin.Aliases...)
	return &cloned
}

func newCatalogProposal(accepted catalog.AcceptedState, observationID string, pins []CatalogPinRef, createdAt time.Time) CatalogProposal {
	moduleIDs := make([]string, 0, len(accepted.Modules))
	for _, module := range accepted.Modules {
		moduleIDs = append(moduleIDs, module.Module.ID)
	}
	sort.Strings(moduleIDs)
	sort.Slice(pins, func(left, right int) bool { return pins[left].ModuleID < pins[right].ModuleID })
	return CatalogProposal{
		SchemaVersion: recordSchemaVersion, ProposalKind: "catalog", Generation: accepted.Generation,
		ObservationID: observationID, ModuleIDs: moduleIDs, SourcePins: pins, CreatedAt: createdAt.UTC(),
	}
}

func newStateEvent(recordPath, kind string, value any) (state.Event, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return state.Event{}, fmt.Errorf("encode %s state record: %w", kind, err)
	}
	record := state.Record{Kind: kind, SchemaVersion: recordSchemaVersion, Data: data}
	eventID, err := identity.ID("state-event", recordSchemaVersion, struct {
		Path   string       `json:"path"`
		Record state.Record `json:"record"`
	}{recordPath, record})
	if err != nil {
		return state.Event{}, fmt.Errorf("identify %s state event: %w", kind, err)
	}
	return state.Event{ID: eventID, RecordPath: recordPath, Record: record}, nil
}

func containsModule(modules []string, moduleID string) bool {
	for _, candidate := range modules {
		if candidate == moduleID {
			return true
		}
	}
	return false
}
