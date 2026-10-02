package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

const (
	recordSchemaVersion  = 1
	projectionIntentPath = "state/projection/intents/"
	IntentBacklog        = "backlog"
	IntentBlocked        = "blocked"
	IntentNeedsReview    = "needs_review"
	IntentRetired        = "retired"
)

// Options pins the reviewed extractor contract for source-acquisition work.
type Options struct {
	AcceptedAt        time.Time
	ExtractorContract string
	Capabilities      []string
}

// Result describes the accepted local transaction, not a canonical data merge
// or production workflow activation.
type Result struct {
	Head          string
	Accepted      catalog.AcceptedState
	Delta         catalog.Delta
	ObservationID string
	ProposalID    string
}

// DurableCatalogState is the latest accepted catalog reducer state.
type DurableCatalogState struct {
	SchemaVersion int                   `json:"schema_version"`
	Accepted      catalog.AcceptedState `json:"accepted"`
}

// Observation preserves the full accepted input and reducer result regardless
// of whether any source analysis succeeds.
type Observation struct {
	SchemaVersion      int              `json:"schema_version"`
	Candidate          catalog.Snapshot `json:"candidate"`
	AcceptedAt         time.Time        `json:"accepted_at"`
	AcceptedGeneration uint64           `json:"accepted_generation"`
	Delta              catalog.Delta    `json:"delta"`
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
func Persist(ctx context.Context, resolver source.Resolver, backend state.Backend, candidate catalog.Snapshot, options Options) (Result, error) {
	if options.AcceptedAt.IsZero() || strings.TrimSpace(options.ExtractorContract) == "" || len(options.Capabilities) == 0 {
		return Result{}, fmt.Errorf("discovery timestamp and reviewed extractor contract are required")
	}
	head, current, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return Result{}, err
	}
	previous, err := readDurableCatalog(current)
	if err != nil {
		return Result{}, err
	}
	accepted, delta, err := catalog.AcceptSnapshot(previous, candidate, options.AcceptedAt)
	if err != nil {
		return Result{}, err
	}
	observation := Observation{
		SchemaVersion: recordSchemaVersion, Candidate: candidate,
		AcceptedAt: options.AcceptedAt.UTC(), AcceptedGeneration: accepted.Generation, Delta: delta,
	}
	observationID, err := identity.ID("discovery-observation", recordSchemaVersion, observation)
	if err != nil {
		return Result{}, fmt.Errorf("identify discovery observation: %w", err)
	}
	events := make([]state.Event, 0, len(accepted.Modules)*4+3)
	observationPath := "state/discovery/observations/" + observationID + ".json"
	observationEvent, err := newStateEvent(observationPath, "discovery_observation", observation)
	if err != nil {
		return Result{}, err
	}
	events = append(events, observationEvent)
	if previous == nil || !delta.NoChange {
		catalogRecord := DurableCatalogState{SchemaVersion: recordSchemaVersion, Accepted: accepted}
		event, eventErr := newStateEvent("state/discovery/catalog.json", "discovery_catalog", catalogRecord)
		if eventErr != nil {
			return Result{}, eventErr
		}
		events = append(events, event)
	}
	pinRefs, pinsChanged, moduleEvents, err := resolveAndRecordModules(ctx, resolver, current, accepted, delta, options)
	if err != nil {
		return Result{}, err
	}
	events = append(events, moduleEvents...)
	proposalID := ""
	if !delta.NoChange || pinsChanged {
		proposal := newCatalogProposal(accepted, observationID, pinRefs, options.AcceptedAt)
		proposalID, err = identity.ID("catalog-proposal", recordSchemaVersion, proposal)
		if err != nil {
			return Result{}, fmt.Errorf("identify catalog proposal: %w", err)
		}
		event, eventErr := newStateEvent("state/exports/"+proposalID+".json", "export", proposal)
		if eventErr != nil {
			return Result{}, eventErr
		}
		events = append(events, event)
	}
	newHead, _, err := state.ApplyEventsAtHead(ctx, backend, head, events)
	if err != nil {
		return Result{}, err
	}
	return Result{Head: newHead, Accepted: accepted, Delta: delta, ObservationID: observationID, ProposalID: proposalID}, nil
}

func resolveAndRecordModules(ctx context.Context, resolver source.Resolver, current state.Snapshot, accepted catalog.AcceptedState, delta catalog.Delta, options Options) ([]CatalogPinRef, bool, []state.Event, error) {
	moduleRecords := append([]catalog.ModuleRecord(nil), accepted.Modules...)
	sort.Slice(moduleRecords, func(left, right int) bool { return moduleRecords[left].Module.ID < moduleRecords[right].Module.ID })
	var pinRefs []CatalogPinRef
	var events []state.Event
	pinsChanged := false
	for _, moduleRecord := range moduleRecords {
		if err := ctx.Err(); err != nil {
			return nil, false, nil, err
		}
		module := moduleRecord.Module
		if !moduleRecord.Active {
			retirementEvents, eventErr := persistRetirement(current, moduleRecord, delta, accepted.Generation, options.AcceptedAt)
			if eventErr != nil {
				return nil, false, nil, eventErr
			}
			events = append(events, retirementEvents...)
			continue
		}
		history, err := readPinHistory(current, module.ID)
		if err != nil {
			return nil, false, nil, err
		}
		var previousPin *source.ResolvedSource
		if history.OverallLatest != nil {
			previous := *history.OverallLatest
			previousPin = &previous
		}
		pin, resolveErr := resolver.ResolveLatest(ctx, module, previousPin)
		if resolveErr != nil {
			intentEvents, intentErr := persistResolutionIntent(current, module.ID, accepted.Generation, history.OverallLatest, resolveErr, options.AcceptedAt)
			if intentErr != nil {
				return nil, false, nil, intentErr
			}
			events = append(events, intentEvents...)
			continue
		}
		pin.ResolvedAt = options.AcceptedAt.UTC()
		updatedHistory, pinChanged, err := addPinObservation(history, module.ID, pin)
		if err != nil {
			return nil, false, nil, err
		}
		if pinChanged {
			event, eventErr := newStateEvent("state/discovery/source-pins/"+module.ID+".json", "source_pins", updatedHistory)
			if eventErr != nil {
				return nil, false, nil, eventErr
			}
			events = append(events, event)
			pinsChanged = true
		}
		currentPin := pin
		if updatedHistory.OverallLatest != nil {
			currentPin = *updatedHistory.OverallLatest
		}
		pinRefs = append(pinRefs, CatalogPinRef{ModuleID: module.ID, Version: currentPin.Version, Tag: currentPin.Tag, Commit: currentPin.Commit})
		intent, intentChanged, intentErr := buildProjectionIntent(current, module.ID, accepted.Generation, IntentBacklog, "source facts are not yet accepted for assessment", &currentPin, options.AcceptedAt)
		if intentErr != nil {
			return nil, false, nil, intentErr
		}
		requestEvents, requestErr := acquisitionEvents(current, module.ID, currentPin, intent.DesiredGeneration, options)
		if requestErr != nil {
			return nil, false, nil, requestErr
		}
		events = append(events, requestEvents...)
		if intentChanged {
			intentEvent, eventErr := newStateEvent(projectionIntentPath+module.ID+".json", "projection_intent", intent)
			if eventErr != nil {
				return nil, false, nil, eventErr
			}
			events = append(events, intentEvent)
		}
	}
	sort.Slice(pinRefs, func(left, right int) bool { return pinRefs[left].ModuleID < pinRefs[right].ModuleID })
	return pinRefs, pinsChanged, events, nil
}

func persistRetirement(snapshot state.Snapshot, module catalog.ModuleRecord, delta catalog.Delta, generation uint64, at time.Time) ([]state.Event, error) {
	if !containsModule(delta.Retired, module.Module.ID) {
		return nil, nil
	}
	return persistIntent(snapshot, module.Module.ID, generation, IntentRetired, "confirmed absent in two successful observations", nil, at)
}

func persistResolutionIntent(snapshot state.Snapshot, moduleID string, generation uint64, previous *source.ResolvedSource, resolveErr error, at time.Time) ([]state.Event, error) {
	status, reason := classifyResolutionError(resolveErr)
	return persistIntent(snapshot, moduleID, generation, status, reason, previous, at)
}

func persistIntent(snapshot state.Snapshot, moduleID string, generation uint64, status, reason string, pin *source.ResolvedSource, at time.Time) ([]state.Event, error) {
	intent, changed, err := buildProjectionIntent(snapshot, moduleID, generation, status, reason, pin, at)
	if err != nil || !changed {
		return nil, err
	}
	event, err := newStateEvent(projectionIntentPath+moduleID+".json", "projection_intent", intent)
	if err != nil {
		return nil, err
	}
	return []state.Event{event}, nil
}

func acquisitionEvents(snapshot state.Snapshot, moduleID string, pin source.ResolvedSource, generation uint64, options Options) ([]state.Event, error) {
	desired, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: moduleID, SourceSHA: pin.Commit, DesiredGeneration: generation,
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
		event, eventErr := newStateEvent("state/requests/"+request.ID+".json", "request", request)
		if eventErr != nil {
			return nil, eventErr
		}
		events = append(events, event)
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

func buildProjectionIntent(snapshot state.Snapshot, moduleID string, generation uint64, status, reason string, pin *source.ResolvedSource, updatedAt time.Time) (ProjectionIntent, bool, error) {
	path := projectionIntentPath + moduleID + ".json"
	previousRecord, exists := snapshot.Records[path]
	var previous *ProjectionIntent
	if exists {
		if previousRecord.Kind != "projection_intent" {
			return ProjectionIntent{}, false, fmt.Errorf("projection intent has wrong record kind for %s", moduleID)
		}
		var decoded ProjectionIntent
		if err := json.Unmarshal(previousRecord.Data, &decoded); err != nil {
			return ProjectionIntent{}, false, fmt.Errorf("decode projection intent for %s: %w", moduleID, err)
		}
		previous = &decoded
	}
	if previous != nil && previous.Status == status && previous.Reason == reason && sameOptionalPin(previous.Source, pin) {
		return *previous, false, nil
	}
	if previous != nil && generation <= previous.DesiredGeneration {
		generation = previous.DesiredGeneration + 1
	}
	intent := ProjectionIntent{
		SchemaVersion: recordSchemaVersion, ModuleID: moduleID, DesiredGeneration: generation,
		Status: status, Reason: reason, Source: cloneOptionalPin(pin), UpdatedAt: updatedAt.UTC(),
	}
	return intent, true, nil
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
