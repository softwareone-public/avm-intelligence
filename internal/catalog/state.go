package catalog

import (
	"encoding/hex"

	"fmt"

	"sort"
	"time"
)

const (
	// AcceptedStateSchemaVersion tracks the durable discovery state contract.
	AcceptedStateSchemaVersion = 1
	// MaxFeedShrinkPercent is the review threshold for one accepted observation.
	MaxFeedShrinkPercent = 10
	// RequiredMissingObservations avoids retiring modules on one transient omission.
	RequiredMissingObservations = 2
)

// ModuleRecord retains catalog history after a module disappears from a feed.
type ModuleRecord struct {
	Module              Module `json:"module"`
	Active              bool   `json:"active"`
	MissingObservations int    `json:"missing_observations"`
	LastSeenGeneration  uint64 `json:"last_seen_generation"`
}

// AcceptedState is control state, separate from published catalog data.
type AcceptedState struct {
	SchemaVersion int            `json:"schema_version"`
	Generation    uint64         `json:"generation"`
	AcceptedAt    time.Time      `json:"accepted_at"`
	LastFetchedAt time.Time      `json:"last_fetched_at"`
	Feeds         []FeedSnapshot `json:"feeds"`
	Modules       []ModuleRecord `json:"modules"`
}

// Delta summarizes changes between accepted discovery generations.
type Delta struct {
	Added          []string `json:"added,omitempty"`
	Changed        []string `json:"changed,omitempty"`
	PendingRemoval []string `json:"pending_removal,omitempty"`
	Retired        []string `json:"retired,omitempty"`
	ChangedFeeds   []Kind   `json:"changed_feeds,omitempty"`
	NoChange       bool     `json:"no_change,omitempty"`
}

// AcceptSnapshot validates a candidate and advances durable control state. A
// suspicious feed shrink is rejected. Missing modules remain active after their
// first clean observation and become inactive only after the second.
func AcceptSnapshot(previous *AcceptedState, candidate Snapshot, acceptedAt time.Time) (AcceptedState, Delta, error) {
	if err := validateAcceptance(candidate, acceptedAt); err != nil {
		return AcceptedState{}, Delta{}, err
	}
	if previous == nil {
		return initialState(candidate, acceptedAt), initialDelta(candidate), nil
	}
	if err := validatePrior(previous, candidate); err != nil {
		return AcceptedState{}, Delta{}, err
	}
	if err := rejectSuspiciousShrink(previous, candidate); err != nil {
		return AcceptedState{}, Delta{}, err
	}
	state, delta := advanceState(previous, candidate, acceptedAt)
	if deltaEmpty(delta) {
		return cloneState(*previous), Delta{NoChange: true}, nil
	}
	return state, delta, nil
}

func validateAcceptance(candidate Snapshot, acceptedAt time.Time) error {
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("candidate catalog: %w", err)
	}
	if acceptedAt.IsZero() {
		return fmt.Errorf("accepted timestamp is required")
	}
	if acceptedAt.Before(candidate.FetchedAt) {
		return fmt.Errorf("acceptance timestamp predates candidate discovery")
	}
	return nil
}

func validatePrior(previous *AcceptedState, candidate Snapshot) error {
	if err := previous.Validate(); err != nil {
		return fmt.Errorf("accepted discovery state: %w", err)
	}
	if !candidate.FetchedAt.After(previous.LastFetchedAt) {
		return fmt.Errorf("candidate observation is not newer than accepted discovery state")
	}
	return nil
}

func rejectSuspiciousShrink(previous *AcceptedState, candidate Snapshot) error {
	previousFeedCounts := feedCounts(previous.Feeds)
	candidateFeedCounts := feedCounts(candidate.Feeds)
	for _, source := range OfficialSources {
		oldCount, newCount := previousFeedCounts[source.Kind], candidateFeedCounts[source.Kind]
		if oldCount > newCount && oldCount-newCount > max(1, oldCount*MaxFeedShrinkPercent/100) {
			return fmt.Errorf("suspicious %s catalog shrink from %d to %d modules; review required", source.Kind, oldCount, newCount)
		}
	}
	return nil
}

func advanceState(previous *AcceptedState, candidate Snapshot, acceptedAt time.Time) (AcceptedState, Delta) {
	state := cloneState(*previous)
	state.Generation++
	state.AcceptedAt = acceptedAt.UTC()
	state.LastFetchedAt = candidate.FetchedAt.UTC()
	state.Feeds = append([]FeedSnapshot(nil), candidate.Feeds...)
	delta := Delta{}
	candidateByID := make(map[string]Module, len(candidate.Modules))
	for _, module := range candidate.Modules {
		candidateByID[module.ID] = module
	}
	existing := make(map[string]struct{}, len(state.Modules))
	for index := range state.Modules {
		record := &state.Modules[index]
		id := record.Module.ID
		existing[id] = struct{}{}
		module, present := candidateByID[id]
		updateRecord(record, module, present, state.Generation, &delta)
		if present {
			delete(candidateByID, id)
		}
	}
	for _, module := range candidate.Modules {
		if _, exists := existing[module.ID]; exists {
			continue
		}
		state.Modules = append(state.Modules, ModuleRecord{Module: module, Active: true, LastSeenGeneration: state.Generation})
		delta.Added = append(delta.Added, module.ID)
	}
	oldDigests := feedDigests(previous.Feeds)
	for _, feed := range candidate.Feeds {
		if oldDigests[feed.Kind] != feed.SHA256 {
			delta.ChangedFeeds = append(delta.ChangedFeeds, feed.Kind)
		}
	}
	sortState(&state, &delta)
	return state, delta
}

func updateRecord(record *ModuleRecord, module Module, present bool, generation uint64, delta *Delta) {
	if !present {
		if record.Active && record.MissingObservations < RequiredMissingObservations {
			record.MissingObservations++
			if record.MissingObservations >= RequiredMissingObservations {
				record.Active = false
				delta.Retired = append(delta.Retired, record.Module.ID)
			} else {
				delta.PendingRemoval = append(delta.PendingRemoval, record.Module.ID)
			}
		}
		return
	}
	if !modulesEqual(record.Module, module) {
		delta.Changed = append(delta.Changed, module.ID)
	}
	record.Module = module
	record.Active = true
	record.MissingObservations = 0
	record.LastSeenGeneration = generation
}

func deltaEmpty(delta Delta) bool {
	return len(delta.Added)+len(delta.Changed)+len(delta.PendingRemoval)+len(delta.Retired)+len(delta.ChangedFeeds) == 0
}

// Validate checks the accepted state independently of its writer.
func (s AcceptedState) Validate() error {
	if err := validateStateMetadata(s); err != nil {
		return err
	}
	lastSeenCounts, err := validateStateRecords(s)
	if err != nil {
		return err
	}
	return validateStateFeeds(s.Feeds, lastSeenCounts)
}

func validateStateMetadata(state AcceptedState) error {
	if state.SchemaVersion != AcceptedStateSchemaVersion || state.Generation == 0 || state.AcceptedAt.IsZero() || state.LastFetchedAt.IsZero() {
		return fmt.Errorf("accepted discovery state metadata is invalid")
	}
	if state.LastFetchedAt.After(state.AcceptedAt) {
		return fmt.Errorf("accepted discovery state timestamps are inconsistent")
	}
	if len(state.Feeds) != len(OfficialSources) || len(state.Modules) == 0 {
		return fmt.Errorf("accepted discovery state is incomplete")
	}
	return nil
}

func validateStateRecords(state AcceptedState) (map[Kind]int, error) {
	seen := map[string]struct{}{}
	lastSeenCounts := map[Kind]int{}
	for _, record := range state.Modules {
		if err := record.Module.Validate(); err != nil {
			return nil, err
		}
		if record.MissingObservations < 0 || record.MissingObservations > RequiredMissingObservations || (record.Active && record.MissingObservations >= RequiredMissingObservations) || record.LastSeenGeneration == 0 || record.LastSeenGeneration > state.Generation {
			return nil, fmt.Errorf("accepted module state is invalid for %s", record.Module.ID)
		}
		if _, exists := seen[record.Module.ID]; exists {
			return nil, fmt.Errorf("accepted discovery state repeats module %s", record.Module.ID)
		}
		seen[record.Module.ID] = struct{}{}
		if record.LastSeenGeneration == state.Generation {
			lastSeenCounts[record.Module.Type]++
		}
		if !record.Active && record.MissingObservations < RequiredMissingObservations {
			return nil, fmt.Errorf("inactive module %s lacks confirmed-removal observations", record.Module.ID)
		}
	}
	return lastSeenCounts, nil
}

func validateStateFeeds(feeds []FeedSnapshot, lastSeenCounts map[Kind]int) error {
	feedByKind := map[Kind]FeedSnapshot{}
	for _, feed := range feeds {
		if _, exists := feedByKind[feed.Kind]; exists {
			return fmt.Errorf("accepted discovery state repeats feed %s", feed.Kind)
		}
		feedByKind[feed.Kind] = feed
	}
	for _, source := range OfficialSources {
		feed, exists := feedByKind[source.Kind]
		if !exists || feed.URL != source.URL || feed.ModuleCount != lastSeenCounts[source.Kind] || feed.ModuleCount == 0 {
			return fmt.Errorf("accepted state feed is invalid for %s", source.Kind)
		}
		if len(feed.SHA256) != 64 {
			return fmt.Errorf("accepted state feed digest is invalid for %s", source.Kind)
		}
		if _, err := hex.DecodeString(feed.SHA256); err != nil {
			return fmt.Errorf("accepted state feed digest is invalid for %s", source.Kind)
		}
	}
	return nil
}

func initialState(candidate Snapshot, acceptedAt time.Time) AcceptedState {
	state := AcceptedState{SchemaVersion: AcceptedStateSchemaVersion, Generation: 1, AcceptedAt: acceptedAt.UTC(), LastFetchedAt: candidate.FetchedAt.UTC(), Feeds: append([]FeedSnapshot(nil), candidate.Feeds...)}
	for _, module := range candidate.Modules {
		state.Modules = append(state.Modules, ModuleRecord{Module: module, Active: true, LastSeenGeneration: 1})
	}
	sortState(&state, nil)
	return state
}

func initialDelta(candidate Snapshot) Delta {
	delta := Delta{}
	for _, module := range candidate.Modules {
		delta.Added = append(delta.Added, module.ID)
	}
	for _, feed := range candidate.Feeds {
		delta.ChangedFeeds = append(delta.ChangedFeeds, feed.Kind)
	}
	sort.Strings(delta.Added)
	return delta
}

func modulesEqual(left, right Module) bool {
	return left == right
}

func feedCounts(feeds []FeedSnapshot) map[Kind]int {
	counts := make(map[Kind]int, len(feeds))
	for _, feed := range feeds {
		counts[feed.Kind] = feed.ModuleCount
	}
	return counts
}

func feedDigests(feeds []FeedSnapshot) map[Kind]string {
	digests := make(map[Kind]string, len(feeds))
	for _, feed := range feeds {
		digests[feed.Kind] = feed.SHA256
	}
	return digests
}

func findFeed(feeds []FeedSnapshot, kind Kind) (FeedSnapshot, bool) {
	for _, feed := range feeds {
		if feed.Kind == kind {
			return feed, true
		}
	}
	return FeedSnapshot{}, false
}

func cloneState(state AcceptedState) AcceptedState {
	state.Feeds = append([]FeedSnapshot(nil), state.Feeds...)
	state.Modules = append([]ModuleRecord(nil), state.Modules...)
	return state
}

func sortState(state *AcceptedState, delta *Delta) {
	sort.Slice(state.Modules, func(i, j int) bool { return state.Modules[i].Module.ID < state.Modules[j].Module.ID })
	if delta == nil {
		return
	}
	sort.Strings(delta.Added)
	sort.Strings(delta.Changed)
	sort.Strings(delta.PendingRemoval)
	sort.Strings(delta.Retired)
	sort.Slice(delta.ChangedFeeds, func(i, j int) bool { return delta.ChangedFeeds[i] < delta.ChangedFeeds[j] })
}
