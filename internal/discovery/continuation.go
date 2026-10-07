package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

const (
	cycleLease             = 10 * time.Minute
	skipCheckpointBatch    = 16
	skipCheckpointInterval = time.Minute
)

// Continuation retains exact authorized scope and source/delivery position across bounded manual runs.
type Continuation struct {
	SchemaVersion   int       `json:"schema_version"`
	ID              string    `json:"id"`
	RunID           string    `json:"run_id"`
	OwnerRunID      string    `json:"owner_run_id"`
	OwnerGeneration uint64    `json:"owner_generation"`
	LeaseExpiresAt  time.Time `json:"lease_expires_at"`
	Status          string    `json:"status"`
	RetryAt         time.Time `json:"retry_at"`
	Options         Options   `json:"options"`
	Result          Result    `json:"result"`
	Next            int       `json:"next"`
	PendingDelivery bool      `json:"pending_delivery"`
}

func continuationPath(id string) (string, error) {
	if err := identity.ValidateRecordID("discovery-cycle", id); err != nil {
		return "", err
	}
	return "state/discovery/cycles/" + id + ".json", nil
}

// ReadContinuation validates one exact durable cycle, never resolves a new scope from selectors.
func ReadContinuation(snapshot state.Snapshot, id string) (Continuation, error) {
	path, err := continuationPath(id)
	if err != nil {
		return Continuation{}, err
	}
	record, found := snapshot.Records[path]
	if !found || record.Kind != "discovery_cycle" {
		return Continuation{}, fmt.Errorf("discovery continuation is missing")
	}
	var cursor Continuation
	if err = json.Unmarshal(record.Data, &cursor); err != nil {
		return cursor, err
	}
	if cursor.SchemaVersion != 1 || cursor.ID != id || cursor.RunID == "" || cursor.OwnerRunID == "" || cursor.OwnerGeneration == 0 || len(cursor.Result.SelectedModuleIDs) == 0 || cursor.Next < 0 || cursor.Next > len(cursor.Result.SelectedModuleIDs) || len(cursor.Result.CompletedModuleIDs) != cursor.Next {
		return cursor, fmt.Errorf("discovery continuation metadata is invalid")
	}
	if cursor.Status != "running" && cursor.Status != "deferred" && cursor.Status != "complete" {
		return cursor, fmt.Errorf("discovery continuation state is invalid")
	}
	if err := validateContinuationScope(cursor); err != nil {
		return cursor, err
	}
	return cursor, nil
}

func validateContinuationScope(cursor Continuation) error {
	seen := map[string]bool{}
	for position, moduleID := range cursor.Result.SelectedModuleIDs {
		if _, err := identity.OperationalModuleRoot(moduleID); err != nil || seen[moduleID] {
			return fmt.Errorf("continuation scope is invalid or duplicated")
		}
		seen[moduleID] = true
		if position < cursor.Next && cursor.Result.CompletedModuleIDs[position] != moduleID {
			return fmt.Errorf("continuation completed prefix is inconsistent")
		}
	}
	if len(cursor.Options.SelectedModuleIDs) != len(seen) {
		return fmt.Errorf("continuation scope and options disagree")
	}
	for _, moduleID := range cursor.Options.SelectedModuleIDs {
		if !seen[moduleID] {
			return fmt.Errorf("continuation options broaden scope")
		}
		delete(seen, moduleID)
	}
	if len(seen) != 0 {
		return fmt.Errorf("continuation options omit or duplicate targets")
	}
	for _, moduleID := range cursor.Result.SelectedModuleIDs {
		seen[moduleID] = true
	}
	for _, moduleID := range cursor.Options.ForceModuleIDs {
		if !seen[moduleID] {
			return fmt.Errorf("continuation force target is outside scope")
		}
	}
	if cursor.Status == "complete" && (cursor.Next != len(seen) || cursor.PendingDelivery || !cursor.LeaseExpiresAt.IsZero()) {
		return fmt.Errorf("continuation terminal state is inconsistent")
	}
	if cursor.Status == "running" && cursor.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("continuation owner lease is missing")
	}
	return nil
}

func continuationEvent(cursor Continuation) (state.Event, error) {
	path, err := continuationPath(cursor.ID)
	if err != nil {
		return state.Event{}, err
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return state.Event{}, err
	}
	record := state.Record{Kind: "discovery_cycle", SchemaVersion: 1, Data: data}
	id, err := identity.ID("state-event", 1, record)
	if err != nil {
		return state.Event{}, err
	}
	return state.Event{ID: id, RecordPath: path, Record: record, FenceKey: "cycle:" + cursor.ID[len("discovery-cycle-"):], FenceToken: cursor.OwnerGeneration}, nil
}

func saveContinuation(ctx context.Context, backend state.Backend, head string, cursor Continuation) (string, error) {
	event, err := continuationEvent(cursor)
	if err != nil {
		return "", err
	}
	newHead, _, err := state.ApplyEventsAtHead(ratelimit.CheckpointContext(ctx), backend, head, []state.Event{event})
	return newHead, err
}

// ContinueCycle records shared discovery once or resumes an exact cycle without replaying completed work.
func ContinueCycle(ctx context.Context, resolver source.Resolver, backend state.Backend, candidate catalog.Snapshot, options Options, deliver ModuleDelivery, runID, resumeID string, stopBefore time.Time, skipChecks ...ModuleSkipCheck) (Result, error) {
	if len(skipChecks) > 1 {
		return Result{}, fmt.Errorf("paced cycle accepts at most one skip check")
	}
	if runID == "" || deliver == nil {
		return Result{}, fmt.Errorf("paced cycle requires run identity and delivery")
	}
	backend = &state.VerifiedReadBackend{Backend: backend}
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return Result{}, err
	}
	id := resumeID
	if id == "" {
		id, err = identity.ID("discovery-cycle", 1, map[string]string{"run_id": runID})
		if err != nil {
			return Result{}, err
		}
	}
	path, err := continuationPath(id)
	if err != nil {
		return Result{}, err
	}
	var cursor Continuation
	if _, found := snapshot.Records[path]; found {
		cursor, err = ReadContinuation(snapshot, id)
		if err != nil {
			return Result{}, err
		}
		if cursor.Status == "complete" {
			cursor.Result.ContinuationDurable = true
			cursor.Result.ContinuationComplete = true
			cursor.Result.Head = head
			return cursor.Result, nil
		}
		now := time.Now().UTC()
		if cursor.Status == "running" && cursor.OwnerRunID != runID && now.Before(cursor.LeaseExpiresAt) {
			return Result{}, fmt.Errorf("discovery continuation has an active owner")
		}
		if now.Before(cursor.RetryAt) {
			result := cursor.Result
			result.ContinuationDurable = true
			result.DeferralSaved = true
			result.Head = head
			result.Deferred = true
			result.ContinuationID = id
			result.RetryAt = cursor.RetryAt
			return result, nil
		}
		if cursor.Options.ExtractorContract != options.ExtractorContract || !reflect.DeepEqual(cursor.Options.Capabilities, options.Capabilities) {
			return Result{}, fmt.Errorf("continuation extractor contract changed")
		}
		cursor.OwnerGeneration++
		cursor.OwnerRunID = runID
	} else {
		if resumeID != "" {
			return Result{}, fmt.Errorf("requested continuation does not exist")
		}
		if options.NewModulesOnly || len(options.SelectedModuleIDs) == 0 {
			return Result{}, fmt.Errorf("paced cycle requires exact manual scope")
		}
		options.observeOnly = true
		observed, persistErr := Persist(ctx, resolver, backend, candidate, options)
		if persistErr != nil {
			return observed, persistErr
		}
		head = observed.Head
		observed.SelectedModuleIDs = orderedCycleModules(observed.Accepted, observed.SelectedModuleIDs)
		observed.ContinuationID = id
		cursor = Continuation{SchemaVersion: 1, ID: id, RunID: runID, OwnerRunID: runID, OwnerGeneration: 1, Status: "running", Options: options, Result: observed}
	}
	cursor.Status = "running"
	cursor.RetryAt = time.Time{}
	cursor.Result.Deferred = false
	cursor.Result.DeferralSaved = false
	cursor.Result.RetryAt = time.Time{}
	cursor.Result.WaitReason = ""
	cursor.LeaseExpiresAt = time.Now().UTC().Add(cycleLease)
	head, err = saveContinuation(ctx, backend, head, cursor)
	if err != nil {
		return cursor.Result, err
	}
	cursor.Result.ContinuationDurable = true
	runner := moduleCycleRunner{resolver: resolver, backend: backend, options: cursor.Options, deliver: deliver, continuation: &cursor, result: &cursor.Result}
	dirtySkips := 0
	lastCheckpoint := time.Now()
	runner.beforeMutation = func(ctx context.Context, expectedHead string) (string, error) {
		if dirtySkips == 0 {
			return expectedHead, nil
		}
		cursor.Result.Head = expectedHead
		cursor.LeaseExpiresAt = time.Now().UTC().Add(cycleLease)
		flushedHead, flushErr := saveContinuation(ctx, backend, expectedHead, cursor)
		if flushErr == nil {
			head = flushedHead
			dirtySkips = 0
			lastCheckpoint = time.Now()
		}
		return flushedHead, flushErr
	}
	if len(skipChecks) == 1 {
		runner.skipCheck = skipChecks[0]
	}
	for cursor.Next < len(cursor.Result.SelectedModuleIDs) {
		if !stopBefore.IsZero() && !time.Now().Before(stopBefore) {
			return parkContinuation(ctx, backend, head, cursor, time.Now().UTC())
		}
		moduleID := cursor.Result.SelectedModuleIDs[cursor.Next]
		target := ModuleCycle{ModuleID: moduleID, Position: cursor.Next + 1, Total: len(cursor.Result.SelectedModuleIDs)}
		moduleDeadline := time.Now().Add(cycleLease - time.Minute)
		if !stopBefore.IsZero() && stopBefore.Before(moduleDeadline) {
			moduleDeadline = stopBefore
		}
		moduleCtx, moduleCancel := context.WithDeadline(ctx, moduleDeadline)
		moduleCtx = ratelimit.WithCleanup(moduleCtx, ctx)
		skipsBefore := len(cursor.Result.SkippedModuleIDs)
		moduleHead, projected, cycleErr := runner.complete(moduleCtx, cursor.Result, target)
		moduleCancel()
		if moduleHead != "" {
			head = moduleHead
		}
		if cycleErr != nil {
			if errors.Is(cycleErr, context.DeadlineExceeded) {
				result, parkErr := parkContinuation(ctx, backend, head, cursor, time.Now().UTC())
				if parkErr != nil {
					return result, errors.Join(cycleErr, parkErr)
				}
				return result, nil
			}
			var deferred *ratelimit.DeferredError
			if errors.As(cycleErr, &deferred) {
				cursor.Result.WaitReason = deferred.Reason
				result, parkErr := parkContinuation(ctx, backend, head, cursor, deferred.RetryAt)
				if parkErr != nil {
					return result, errors.Join(cycleErr, parkErr)
				}
				return result, nil
			}
			return cursor.Result, &CycleError{ModuleID: moduleID, Position: target.Position, Total: target.Total, Completed: cursor.Next, Cause: cycleErr}
		}
		if projected {
			cursor.Result.ProjectionModuleIDs = append(cursor.Result.ProjectionModuleIDs, moduleID)
		}
		cursor.Result.CompletedModuleIDs = append(cursor.Result.CompletedModuleIDs, moduleID)
		cursor.Next++
		cursor.PendingDelivery = false
		cursor.Result.Head = head
		if len(cursor.Result.SkippedModuleIDs) > skipsBefore {
			dirtySkips++
		}
		if dirtySkips > 0 && dirtySkips < skipCheckpointBatch && time.Since(lastCheckpoint) < skipCheckpointInterval && cursor.Next < len(cursor.Result.SelectedModuleIDs) {
			continue
		}
		cursor.LeaseExpiresAt = time.Now().UTC().Add(cycleLease)
		if cursor.Next == len(cursor.Result.SelectedModuleIDs) {
			cursor.Status = "complete"
			cursor.LeaseExpiresAt = time.Time{}
		}
		head, err = saveContinuation(ctx, backend, head, cursor)
		if err != nil {
			return cursor.Result, err
		}
		cursor.Result.ContinuationComplete = cursor.Status == "complete"
		dirtySkips = 0
		lastCheckpoint = time.Now()
	}
	cursor.Result.Head = head
	return cursor.Result, nil
}

func parkContinuation(ctx context.Context, backend state.Backend, head string, cursor Continuation, retryAt time.Time) (Result, error) {
	cursor.Status = "deferred"
	cursor.RetryAt = retryAt
	cursor.LeaseExpiresAt = time.Time{}
	cursor.Result.Deferred = true
	cursor.Result.DeferralSaved = true
	cursor.Result.RetryAt = retryAt
	cursor.Result.ContinuationID = cursor.ID
	parkingCtx, cancel := ratelimit.ParkingContext(ctx)
	defer cancel()
	newHead, err := saveContinuation(parkingCtx, backend, head, cursor)
	if err != nil {
		cursor.Result.Deferred = false
		cursor.Result.DeferralSaved = false
		return cursor.Result, err
	}
	cursor.Result.Head = newHead
	return cursor.Result, nil
}
