package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

const (
	AttemptClaimed    = "claimed"
	AttemptDispatched = "dispatched"
	AttemptReclaimed  = "reclaimed"
	AttemptCompleted  = "completed"
	stateRequestPath  = "state/requests/"
)

var (
	ErrNoDispatchableWork = errors.New("no dispatchable work")
	ErrAttemptNotFound    = errors.New("factory attempt not found")
	ErrAttemptConflict    = errors.New("factory attempt is already bound to another Actions run")
	ErrAttemptOwnership   = errors.New("factory attempt ownership or fence mismatch")
	ErrLeaseNotExpired    = errors.New("factory attempt lease has not expired")
)

// Attempt is the durable identity created before dispatch and bound to an
// Actions run afterward. It is not executable input.
type Attempt struct {
	SchemaVersion     int       `json:"schema_version"`
	ID                string    `json:"id"`
	RequestID         string    `json:"request_id"`
	ModuleID          string    `json:"module_id"`
	InputFingerprint  string    `json:"input_fingerprint"`
	SourceSHA         string    `json:"source_sha"`
	Phase             WorkPhase `json:"phase"`
	DesiredGeneration uint64    `json:"desired_generation"`
	Number            int       `json:"number"`
	Fence             uint64    `json:"fence"`
	Dispatcher        string    `json:"dispatcher"`
	Status            string    `json:"status"`
	RunID             string    `json:"run_id,omitempty"`
	RunAttempt        uint64    `json:"run_attempt,omitempty"`
	ClaimedAt         time.Time `json:"claimed_at"`
	BoundAt           time.Time `json:"bound_at,omitempty"`
	LeaseExpiresAt    time.Time `json:"lease_expires_at"`
	LastHeartbeatAt   time.Time `json:"last_heartbeat_at,omitempty"`
	RecoveredAt       time.Time `json:"recovered_at,omitempty"`
	CompletedAt       time.Time `json:"completed_at,omitempty"`
}

// Validate checks immutable request/fence identity and lifecycle-specific
// Actions binding and timestamps before an attempt is read or committed.
func (attempt Attempt) Validate() error {
	if err := validateAttemptIdentity(attempt); err != nil {
		return err
	}
	if err := validateAttemptLifecycle(attempt); err != nil {
		return err
	}
	return validateAttemptHeartbeat(attempt)
}

func validateAttemptIdentity(attempt Attempt) error {
	if attempt.SchemaVersion != 1 || attempt.Number < 1 || attempt.Fence == 0 || strings.TrimSpace(attempt.Dispatcher) == "" || attempt.ClaimedAt.IsZero() || !attempt.LeaseExpiresAt.After(attempt.ClaimedAt) {
		return fmt.Errorf("factory attempt metadata is invalid")
	}
	if identity.ValidateRecordID("attempt", attempt.ID) != nil || validateSourceIdentity(attempt.ModuleID, attempt.SourceSHA) != nil || !isLowerHexDigest(attempt.InputFingerprint) {
		return fmt.Errorf("factory attempt identity is invalid")
	}
	if attempt.RequestID != requestID(attempt.ModuleID, attempt.InputFingerprint) || attempt.DesiredGeneration == 0 || (attempt.Phase != PhaseAcquireSource && attempt.Phase != PhaseAssess) {
		return fmt.Errorf("factory attempt input contract is invalid")
	}
	return nil
}

func validateAttemptLifecycle(attempt Attempt) error {
	switch attempt.Status {
	case AttemptClaimed:
		if attempt.RunID != "" || attempt.RunAttempt != 0 || !attempt.BoundAt.IsZero() || !attempt.RecoveredAt.IsZero() || !attempt.CompletedAt.IsZero() {
			return fmt.Errorf("pre-dispatch attempt has post-dispatch fields")
		}
	case AttemptDispatched:
		if attempt.RunID == "" || attempt.RunAttempt == 0 || attempt.BoundAt.IsZero() || !attempt.RecoveredAt.IsZero() || !attempt.CompletedAt.IsZero() {
			return fmt.Errorf("dispatched attempt has invalid Actions binding")
		}
	case AttemptReclaimed:
		if attempt.RecoveredAt.IsZero() || !attempt.CompletedAt.IsZero() || (attempt.RunID == "" && (attempt.RunAttempt != 0 || !attempt.BoundAt.IsZero())) {
			return fmt.Errorf("reclaimed attempt lacks valid recovery identity")
		}
	case AttemptCompleted:
		if attempt.RunID == "" || attempt.RunAttempt == 0 || attempt.BoundAt.IsZero() || attempt.CompletedAt.IsZero() {
			return fmt.Errorf("completed attempt lacks a bound Actions run")
		}
	default:
		return fmt.Errorf("invalid factory attempt status %q", attempt.Status)
	}
	return nil
}

func validateAttemptHeartbeat(attempt Attempt) error {
	if attempt.LastHeartbeatAt.IsZero() {
		return nil
	}
	if (attempt.Status != AttemptDispatched && attempt.Status != AttemptReclaimed) || attempt.BoundAt.IsZero() || !attempt.LastHeartbeatAt.After(attempt.BoundAt) || attempt.LastHeartbeatAt.After(attempt.LeaseExpiresAt) {
		return fmt.Errorf("factory attempt heartbeat is invalid")
	}
	return nil
}

// DispatchClaim is the durable claim returned to the caller before it invokes
// GitHub Actions.
type DispatchClaim struct {
	Head    string  `json:"head"`
	Request Request `json:"request"`
	Attempt Attempt `json:"attempt"`
}

// ActionsObservation is returned by a trusted Actions lookup. Unknown is
// deliberately distinct from absent and never releases ownership.
type ActionsObservation struct {
	State      string `json:"state"`
	RunID      string `json:"run_id,omitempty"`
	RunAttempt uint64 `json:"run_attempt,omitempty"`
}

// RecoveryResult reports a durable attempt/request pair after reconciliation.
type RecoveryResult struct {
	Head    string  `json:"head"`
	Attempt Attempt `json:"attempt"`
	Request Request `json:"request"`
	Changed bool    `json:"changed"`
}

// InspectAttempt returns the current durable attempt/request pair without
// mutating state, allowing a recovery workflow to query the bound Actions run.
func InspectAttempt(ctx context.Context, backend state.Backend, attemptID string) (RecoveryResult, error) {
	head, _, attempt, request, err := readAttemptAndRequest(ctx, backend, attemptID)
	if err != nil {
		return RecoveryResult{}, err
	}
	return RecoveryResult{Head: head, Attempt: attempt, Request: request}, nil
}

// SelectDispatchableRequests returns ready, unclaimed v2 work in deterministic
// source-first order, then by age and stable module/context identity.
func SelectDispatchableRequests(requests []Request, now time.Time) ([]Request, error) {
	if now.IsZero() {
		return nil, fmt.Errorf("dispatch selection time is required")
	}
	ready := make([]Request, 0, len(requests))
	for index, request := range requests {
		if err := request.Validate(); err != nil {
			return nil, fmt.Errorf("invalid request at index %d: %w", index, err)
		}
		if request.Work == nil || request.Status != StatusQueued || !retryReady(*request.Work, now) {
			continue
		}
		ready = append(ready, cloneRequest(request))
	}
	sort.Slice(ready, func(left, right int) bool {
		return dispatchRequestLess(ready[left], ready[right])
	})
	return ready, nil
}

func retryReady(work WorkContract, now time.Time) bool {
	switch work.RetryClass {
	case RetryNone:
		return true
	case RetryTransient, RetryRateLimited:
		return work.RetryAt.IsZero() || !work.RetryAt.After(now)
	default:
		return false
	}
}

func dispatchRequestLess(left, right Request) bool {
	leftPriority := dispatchPhasePriority(left.Work.Phase)
	rightPriority := dispatchPhasePriority(right.Work.Phase)
	if leftPriority != rightPriority {
		return leftPriority < rightPriority
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	if left.ModuleID != right.ModuleID {
		return left.ModuleID < right.ModuleID
	}
	if left.Work.ContextID != right.Work.ContextID {
		return left.Work.ContextID < right.Work.ContextID
	}
	if left.Work.Track != right.Work.Track {
		return left.Work.Track < right.Work.Track
	}
	return left.ID < right.ID
}

func dispatchPhasePriority(phase WorkPhase) int {
	if phase == PhaseAcquireSource {
		return 0
	}
	return 1
}

// ClaimNext records a module claim, fencing token, and attempt before the
// caller dispatches a workflow. A competing expected-head writer wins at most
// one claim; the caller retries after ErrHeadMoved.
func ClaimNext(ctx context.Context, backend state.Backend, dispatcher string, now time.Time, leaseDuration time.Duration) (DispatchClaim, error) {
	if backend == nil || strings.TrimSpace(dispatcher) == "" || now.IsZero() || leaseDuration <= 0 {
		return DispatchClaim{}, fmt.Errorf("state backend, dispatcher, claim time, and positive lease duration are required")
	}
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return DispatchClaim{}, err
	}
	requests, err := loadVersionedRequests(snapshot)
	if err != nil {
		return DispatchClaim{}, err
	}
	globalEnabled, err := GlobalDispatchEnabled(snapshot)
	if err != nil {
		return DispatchClaim{}, err
	}
	if !globalEnabled {
		return DispatchClaim{}, ErrGlobalDispatchDisabled
	}
	ready, err := SelectDispatchableRequests(requests, now)
	if err != nil {
		return DispatchClaim{}, err
	}
	for _, request := range ready {
		paused, pauseErr := ModuleDispatchPaused(snapshot, request.ModuleID)
		if pauseErr != nil {
			return DispatchClaim{}, pauseErr
		}
		if paused {
			continue
		}
		if moduleHasActiveClaim(requests, request.ModuleID) {
			continue
		}
		claim, claimErr := claimSelectedRequest(ctx, claimInput{backend: backend, head: head, snapshot: snapshot, request: request, dispatcher: dispatcher, now: now, leaseDuration: leaseDuration})
		if claimErr != nil {
			return DispatchClaim{}, claimErr
		}
		return claim, nil
	}
	return DispatchClaim{}, ErrNoDispatchableWork
}

// ClaimRequest claims only the named queued request. It never substitutes a
// different request when the target is absent, paused, superseded, or not due.
func ClaimRequest(ctx context.Context, backend state.Backend, requestID, dispatcher string, now time.Time, leaseDuration time.Duration) (DispatchClaim, error) {
	if backend == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(dispatcher) == "" || now.IsZero() || leaseDuration <= 0 {
		return DispatchClaim{}, fmt.Errorf("state backend, request ID, dispatcher, claim time, and positive lease duration are required")
	}
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return DispatchClaim{}, err
	}
	requests, err := loadVersionedRequests(snapshot)
	if err != nil {
		return DispatchClaim{}, err
	}
	globalEnabled, err := GlobalDispatchEnabled(snapshot)
	if err != nil {
		return DispatchClaim{}, err
	}
	if !globalEnabled {
		return DispatchClaim{}, ErrGlobalDispatchDisabled
	}
	selected := -1
	for index, request := range requests {
		if request.ID == requestID {
			selected = index
			break
		}
	}
	if selected < 0 {
		return DispatchClaim{}, ErrNoDispatchableWork
	}
	request := requests[selected]
	if request.Status != StatusQueued || !retryReady(*request.Work, now) || hasNewerDesiredRequest(requests, selected) || moduleHasActiveClaim(requests, request.ModuleID) {
		return DispatchClaim{}, ErrNoDispatchableWork
	}
	paused, err := ModuleDispatchPaused(snapshot, request.ModuleID)
	if err != nil {
		return DispatchClaim{}, err
	}
	if paused {
		return DispatchClaim{}, ErrNoDispatchableWork
	}
	return claimSelectedRequest(ctx, claimInput{
		backend: backend, head: head, snapshot: snapshot, request: request,
		dispatcher: dispatcher, now: now, leaseDuration: leaseDuration,
	})
}

// ClaimModule claims the oldest ready queued request for one exact module.
// It never substitutes another module when that module is absent, paused,
// superseded, retry-delayed, or already claimed.
func ClaimModule(ctx context.Context, backend state.Backend, moduleID, dispatcher string, now time.Time, leaseDuration time.Duration) (DispatchClaim, error) {
	if backend == nil || !moduleIDPattern.MatchString(moduleID) || strings.TrimSpace(dispatcher) == "" || now.IsZero() || leaseDuration <= 0 {
		return DispatchClaim{}, fmt.Errorf("state backend, canonical module ID, dispatcher, claim time, and positive lease duration are required")
	}
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return DispatchClaim{}, err
	}
	requests, err := loadVersionedRequests(snapshot)
	if err != nil {
		return DispatchClaim{}, err
	}
	globalEnabled, err := GlobalDispatchEnabled(snapshot)
	if err != nil {
		return DispatchClaim{}, err
	}
	if !globalEnabled || moduleHasActiveClaim(requests, moduleID) {
		if !globalEnabled {
			return DispatchClaim{}, ErrGlobalDispatchDisabled
		}
		return DispatchClaim{}, ErrNoDispatchableWork
	}
	paused, err := ModuleDispatchPaused(snapshot, moduleID)
	if err != nil {
		return DispatchClaim{}, err
	}
	if paused {
		return DispatchClaim{}, ErrNoDispatchableWork
	}
	ready := make([]Request, 0, 1)
	for index, request := range requests {
		if request.ModuleID != moduleID || request.Work == nil || request.Status != StatusQueued || !retryReady(*request.Work, now) || hasNewerDesiredRequest(requests, index) {
			continue
		}
		ready = append(ready, cloneRequest(request))
	}
	if len(ready) == 0 {
		return DispatchClaim{}, ErrNoDispatchableWork
	}
	sort.Slice(ready, func(left, right int) bool { return dispatchRequestLess(ready[left], ready[right]) })
	return claimSelectedRequest(ctx, claimInput{
		backend: backend, head: head, snapshot: snapshot, request: ready[0],
		dispatcher: dispatcher, now: now, leaseDuration: leaseDuration,
	})
}

type claimInput struct {
	backend       state.Backend
	head          string
	snapshot      state.Snapshot
	request       Request
	dispatcher    string
	now           time.Time
	leaseDuration time.Duration
}

func claimSelectedRequest(ctx context.Context, input claimInput) (DispatchClaim, error) {
	backend, head, snapshot, request := input.backend, input.head, input.snapshot, input.request
	dispatcher, now, leaseDuration := input.dispatcher, input.now, input.leaseDuration
	fence := snapshot.FencingTokens[request.ModuleID]
	if fence == math.MaxUint64 {
		return DispatchClaim{}, fmt.Errorf("module %s exhausted its fencing-token space", request.ModuleID)
	}
	fence++
	attemptID, err := identity.ID("attempt", 1, struct {
		RequestID string `json:"request_id"`
		Fence     uint64 `json:"fence"`
	}{request.ID, fence})
	if err != nil {
		return DispatchClaim{}, fmt.Errorf("identify factory attempt: %w", err)
	}
	attemptNumber, err := countRequestAttempts(snapshot, request.ID)
	if err != nil {
		return DispatchClaim{}, err
	}
	attempt := Attempt{
		SchemaVersion: 1, ID: attemptID, RequestID: request.ID, ModuleID: request.ModuleID,
		InputFingerprint: request.Fingerprint, SourceSHA: request.SourceSHA, Phase: request.Work.Phase,
		DesiredGeneration: request.Work.DesiredGeneration, Number: attemptNumber + 1,
		Fence: fence, Dispatcher: dispatcher, Status: AttemptClaimed, ClaimedAt: now.UTC(),
		LeaseExpiresAt: now.Add(leaseDuration).UTC(),
	}
	if err = attempt.Validate(); err != nil {
		return DispatchClaim{}, err
	}
	claimed := cloneRequest(request)
	claimed.Status = StatusClaimed
	claimed.OwnerRunID = ""
	claimed.UpdatedAt = now.UTC()
	claimed.Work.AttemptID = attempt.ID
	claimed.Work.Fence = fence
	claimed.Work.Lease = &ComputeLease{Owner: attempt.ID, ExpiresAt: attempt.LeaseExpiresAt, Fence: fence}
	if err = claimed.Validate(); err != nil {
		return DispatchClaim{}, fmt.Errorf("validate dispatch claim: %w", err)
	}
	requestEvent, err := stateRecordEvent(stateRequestPath+claimed.ID+".json", "request", claimed, "", 0)
	if err != nil {
		return DispatchClaim{}, err
	}
	attemptEvent, err := stateRecordEvent(dispatchAttemptPath(attempt), "attempt", attempt, claimed.ModuleID, fence)
	if err != nil {
		return DispatchClaim{}, err
	}
	newHead, _, err := state.ApplyEventsAtHead(ctx, backend, head, []state.Event{requestEvent, attemptEvent})
	if err != nil {
		return DispatchClaim{}, err
	}
	return DispatchClaim{Head: newHead, Request: claimed, Attempt: attempt}, nil
}

// BindActionsRun binds one actual workflow run to a pre-dispatch attempt. A
// retry of the same binding is idempotent; a second run cannot take ownership.
func BindActionsRun(ctx context.Context, backend state.Backend, attemptID, runID string, runAttempt uint64, boundAt time.Time) (RecoveryResult, error) {
	if strings.TrimSpace(runID) == "" || runAttempt == 0 || boundAt.IsZero() {
		return RecoveryResult{}, fmt.Errorf("Actions run ID, attempt number, and bind time are required")
	}
	head, snapshot, attempt, request, err := readAttemptAndRequest(ctx, backend, attemptID)
	if err != nil {
		return RecoveryResult{}, err
	}
	if (attempt.Status == AttemptDispatched || attempt.Status == AttemptCompleted) && attempt.RunID == runID && attempt.RunAttempt == runAttempt {
		return RecoveryResult{Head: head, Attempt: attempt, Request: request}, nil
	}
	if attempt.Status == AttemptDispatched {
		return RecoveryResult{}, ErrAttemptConflict
	}
	if attempt.Status != AttemptClaimed || request.Status != StatusClaimed || request.Work.AttemptID != attempt.ID || request.Work.Fence != attempt.Fence || request.Work.Lease == nil || request.Work.Lease.Owner != attempt.ID {
		return RecoveryResult{}, ErrAttemptOwnership
	}
	updatedAttempt := attempt
	updatedAttempt.Status = AttemptDispatched
	updatedAttempt.RunID = runID
	updatedAttempt.RunAttempt = runAttempt
	updatedAttempt.BoundAt = boundAt.UTC()
	updatedRequest := cloneRequest(request)
	updatedRequest.Status = StatusRunning
	updatedRequest.OwnerRunID = runID
	updatedRequest.UpdatedAt = boundAt.UTC()
	updatedRequest.Work.Lease.Owner = runID
	if err = updatedRequest.Validate(); err != nil {
		return RecoveryResult{}, err
	}
	requestEvent, err := stateRecordEvent(stateRequestPath+updatedRequest.ID+".json", "request", updatedRequest, "", 0)
	if err != nil {
		return RecoveryResult{}, err
	}
	attemptEvent, err := stateRecordEvent(dispatchAttemptPath(updatedAttempt), "attempt", updatedAttempt, "", 0)
	if err != nil {
		return RecoveryResult{}, err
	}
	newHead, _, err := state.ApplyEventsAtHead(ctx, backend, head, []state.Event{requestEvent, attemptEvent})
	if err != nil {
		return RecoveryResult{}, err
	}
	_ = snapshot
	return RecoveryResult{Head: newHead, Attempt: updatedAttempt, Request: updatedRequest, Changed: true}, nil
}

// Heartbeat extends an active attempt lease only for its current bound run and
// fence. Expired or stale owners cannot revive themselves.
func Heartbeat(ctx context.Context, backend state.Backend, attemptID, runID string, fence uint64, now, expiresAt time.Time) (RecoveryResult, error) {
	if now.IsZero() || !expiresAt.After(now) {
		return RecoveryResult{}, fmt.Errorf("heartbeat time and future lease expiry are required")
	}
	head, _, attempt, request, err := readAttemptAndRequest(ctx, backend, attemptID)
	if err != nil {
		return RecoveryResult{}, err
	}
	if attempt.Status != AttemptDispatched || attempt.RunID != runID || attempt.Fence != fence || request.Status != StatusRunning || request.OwnerRunID != runID || request.Work.AttemptID != attempt.ID || request.Work.Lease == nil || request.Work.Lease.Fence != fence {
		return RecoveryResult{}, ErrAttemptOwnership
	}
	if !attempt.LeaseExpiresAt.After(now) {
		return RecoveryResult{}, ErrLeaseNotExpired
	}
	if !expiresAt.After(attempt.LeaseExpiresAt) || (!attempt.LastHeartbeatAt.IsZero() && !now.After(attempt.LastHeartbeatAt)) {
		return RecoveryResult{}, fmt.Errorf("heartbeat must monotonically extend lease and heartbeat time")
	}
	updatedAttempt := attempt
	updatedAttempt.LeaseExpiresAt = expiresAt.UTC()
	updatedAttempt.LastHeartbeatAt = now.UTC()
	updatedRequest := cloneRequest(request)
	updatedRequest.Work.Lease.ExpiresAt = expiresAt.UTC()
	updatedRequest.UpdatedAt = now.UTC()
	requestEvent, err := stateRecordEvent(stateRequestPath+updatedRequest.ID+".json", "request", updatedRequest, "", 0)
	if err != nil {
		return RecoveryResult{}, err
	}
	attemptEvent, err := stateRecordEvent(dispatchAttemptPath(updatedAttempt), "attempt", updatedAttempt, "", 0)
	if err != nil {
		return RecoveryResult{}, err
	}
	newHead, _, err := state.ApplyEventsAtHead(ctx, backend, head, []state.Event{requestEvent, attemptEvent})
	if err != nil {
		return RecoveryResult{}, err
	}
	return RecoveryResult{Head: newHead, Attempt: updatedAttempt, Request: updatedRequest, Changed: true}, nil
}

// RecoverAttempt acts only on explicit Actions observations. Unknown lookup
// preserves the owner; confirmed absence can release an expired attempt.
func RecoverAttempt(ctx context.Context, backend state.Backend, attemptID string, now time.Time, observation ActionsObservation) (RecoveryResult, error) {
	if now.IsZero() || !validRunObservation(observation.State) {
		return RecoveryResult{}, fmt.Errorf("recovery time and valid Actions state are required")
	}
	head, _, attempt, request, err := readAttemptAndRequest(ctx, backend, attemptID)
	if err != nil {
		return RecoveryResult{}, err
	}
	if observation.State == RunUnknown {
		return RecoveryResult{Head: head, Attempt: attempt, Request: request}, nil
	}
	if observation.State == RunActive {
		if attempt.Status == AttemptClaimed {
			if observation.RunID == "" {
				return RecoveryResult{Head: head, Attempt: attempt, Request: request}, nil
			}
			return BindActionsRun(ctx, backend, attempt.ID, observation.RunID, observation.RunAttempt, now)
		}
		if attempt.Status == AttemptDispatched && (observation.RunID == "" || observation.RunID == attempt.RunID) {
			return RecoveryResult{Head: head, Attempt: attempt, Request: request}, nil
		}
		return RecoveryResult{}, ErrAttemptConflict
	}
	if observation.State == RunAbsent {
		return reclaimConfirmedAbsent(ctx, backend, head, attempt, request, now)
	}
	return finishObservedRun(ctx, backend, head, attempt, request, now, observation)
}

func reclaimConfirmedAbsent(ctx context.Context, backend state.Backend, head string, attempt Attempt, request Request, now time.Time) (RecoveryResult, error) {
	if attempt.Status == AttemptReclaimed {
		return RecoveryResult{Head: head, Attempt: attempt, Request: request}, nil
	}
	if !attempt.LeaseExpiresAt.Before(now) || !claimStillMatches(attempt, request) {
		return RecoveryResult{Head: head, Attempt: attempt, Request: request}, ErrLeaseNotExpired
	}
	updatedAttempt := attempt
	updatedAttempt.Status = AttemptReclaimed
	updatedAttempt.RecoveredAt = now.UTC()
	updatedRequest := releaseRequestForRetry(request, now)
	return commitAttemptAndRequest(ctx, backend, head, updatedAttempt, updatedRequest)
}

func finishObservedRun(ctx context.Context, backend state.Backend, head string, attempt Attempt, request Request, now time.Time, observation ActionsObservation) (RecoveryResult, error) {
	if attempt.Status == AttemptCompleted {
		return RecoveryResult{Head: head, Attempt: attempt, Request: request}, nil
	}
	if attempt.Status == AttemptClaimed {
		if observation.RunID == "" || observation.RunAttempt == 0 {
			return RecoveryResult{}, ErrAttemptOwnership
		}
		if _, err := BindActionsRun(ctx, backend, attempt.ID, observation.RunID, observation.RunAttempt, now); err != nil {
			return RecoveryResult{}, err
		}
		return RecoverAttempt(ctx, backend, attempt.ID, now, observation)
	}
	if attempt.Status != AttemptDispatched || observation.RunID != attempt.RunID {
		return RecoveryResult{}, ErrAttemptOwnership
	}
	if observation.RunAttempt != attempt.RunAttempt {
		return RecoveryResult{}, ErrAttemptOwnership
	}
	updatedAttempt := attempt
	updatedAttempt.Status = AttemptCompleted
	updatedAttempt.CompletedAt = now.UTC()
	updatedRequest := request
	if request.Status == StatusRunning && request.Work.Completion == nil {
		updatedRequest = releaseRequestForRetry(request, now)
	}
	return commitAttemptAndRequest(ctx, backend, head, updatedAttempt, updatedRequest)
}

func releaseRequestForRetry(request Request, now time.Time) Request {
	updated := cloneRequest(request)
	updated.Status = StatusQueued
	updated.OwnerRunID = ""
	updated.UpdatedAt = now.UTC()
	updated.Work.AttemptID = ""
	updated.Work.Lease = nil
	updated.Work.RetryClass = RetryTransient
	updated.Work.RetryAt = now.UTC()
	return updated
}

func commitAttemptAndRequest(ctx context.Context, backend state.Backend, head string, attempt Attempt, request Request) (RecoveryResult, error) {
	if err := request.Validate(); err != nil {
		return RecoveryResult{}, err
	}
	requestEvent, err := stateRecordEvent("state/requests/"+request.ID+".json", "request", request, "", 0)
	if err != nil {
		return RecoveryResult{}, err
	}
	attemptEvent, err := stateRecordEvent(dispatchAttemptPath(attempt), "attempt", attempt, "", 0)
	if err != nil {
		return RecoveryResult{}, err
	}
	newHead, _, err := state.ApplyEventsAtHead(ctx, backend, head, []state.Event{requestEvent, attemptEvent})
	if err != nil {
		return RecoveryResult{}, err
	}
	return RecoveryResult{Head: newHead, Attempt: attempt, Request: request, Changed: true}, nil
}

func readAttemptAndRequest(ctx context.Context, backend state.Backend, attemptID string) (string, state.Snapshot, Attempt, Request, error) {
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return "", state.Snapshot{}, Attempt{}, Request{}, err
	}
	attempt, exists, loadErr := loadAttempt(snapshot, attemptID)
	if loadErr != nil {
		return "", state.Snapshot{}, Attempt{}, Request{}, loadErr
	}
	if !exists {
		return "", state.Snapshot{}, Attempt{}, Request{}, ErrAttemptNotFound
	}
	requestRecord, exists := snapshot.Records[stateRequestPath+attempt.RequestID+".json"]
	if !exists || requestRecord.Kind != "request" {
		return "", state.Snapshot{}, Attempt{}, Request{}, fmt.Errorf("request record for attempt %s is missing", attemptID)
	}
	var request Request
	if err = json.Unmarshal(requestRecord.Data, &request); err != nil {
		return "", state.Snapshot{}, Attempt{}, Request{}, fmt.Errorf("decode request for attempt %s: %w", attemptID, err)
	}
	if err = request.Validate(); err != nil {
		return "", state.Snapshot{}, Attempt{}, Request{}, err
	}
	return head, snapshot, attempt, request, nil
}

func loadAttempt(snapshot state.Snapshot, attemptID string) (Attempt, bool, error) {
	var matched Attempt
	found := false
	for recordPath, record := range snapshot.Records {
		if record.Kind != "attempt" {
			continue
		}
		var attempt Attempt
		if err := json.Unmarshal(record.Data, &attempt); err != nil {
			return Attempt{}, false, fmt.Errorf("decode factory attempt %s: %w", recordPath, err)
		}
		if err := attempt.Validate(); err != nil {
			return Attempt{}, false, fmt.Errorf("validate factory attempt at %s: %w", recordPath, err)
		}
		if recordPath != dispatchAttemptPath(attempt) {
			return Attempt{}, false, fmt.Errorf("factory attempt %s is stored at an invalid path", attempt.ID)
		}
		if attempt.ID == attemptID {
			if found {
				return Attempt{}, false, fmt.Errorf("duplicate factory attempt ID %s", attemptID)
			}
			matched = attempt
			found = true
		}
	}
	return matched, found, nil
}

func loadVersionedRequests(snapshot state.Snapshot) ([]Request, error) {
	requests := make([]Request, 0)
	for recordPath, record := range snapshot.Records {
		if record.Kind != "request" {
			continue
		}
		var request Request
		if err := json.Unmarshal(record.Data, &request); err != nil {
			return nil, fmt.Errorf("decode request %s: %w", recordPath, err)
		}
		if err := request.Validate(); err != nil {
			return nil, fmt.Errorf("validate request %s: %w", recordPath, err)
		}
		if recordPath != stateRequestPath+request.ID+".json" {
			return nil, fmt.Errorf("request %s is stored at an invalid path", request.ID)
		}
		if request.Work != nil {
			requests = append(requests, request)
		}
	}
	return requests, nil
}

func moduleHasActiveClaim(requests []Request, moduleID string) bool {
	for _, request := range requests {
		if request.ModuleID == moduleID && (request.Status == StatusClaimed || request.Status == StatusRunning) {
			return true
		}
	}
	return false
}

func countRequestAttempts(snapshot state.Snapshot, requestID string) (int, error) {
	count := 0
	for recordPath, record := range snapshot.Records {
		if record.Kind != "attempt" {
			continue
		}
		var attempt Attempt
		if err := json.Unmarshal(record.Data, &attempt); err != nil {
			return 0, fmt.Errorf("decode factory attempt %s: %w", recordPath, err)
		}
		if err := attempt.Validate(); err != nil {
			return 0, fmt.Errorf("validate factory attempt at %s: %w", recordPath, err)
		}
		if recordPath != dispatchAttemptPath(attempt) {
			return 0, fmt.Errorf("factory attempt %s is stored at an invalid path", attempt.ID)
		}
		if attempt.RequestID == requestID {
			count++
		}
	}
	return count, nil
}

func attemptPath(attempt Attempt) string {
	return dispatchAttemptPath(attempt)
}

func dispatchAttemptPath(attempt Attempt) string {
	return "state/attempts/" + attempt.RequestID + "/" + attempt.ID + ".json"
}

func claimStillMatches(attempt Attempt, request Request) bool {
	if request.Work == nil || request.Work.AttemptID != attempt.ID || request.Work.Fence != attempt.Fence || request.Work.Lease == nil || request.Work.Lease.Fence != attempt.Fence {
		return false
	}
	if attempt.Status == AttemptClaimed {
		return request.Status == StatusClaimed && request.Work.Lease.Owner == attempt.ID
	}
	return attempt.Status == AttemptDispatched && request.Status == StatusRunning && request.OwnerRunID == attempt.RunID && request.Work.Lease.Owner == attempt.RunID
}

func validRunObservation(value string) bool {
	switch value {
	case RunUnknown, RunActive, RunAbsent, RunCompleted:
		return true
	default:
		return false
	}
}

func stateRecordEvent(recordPath, kind string, value any, fenceKey string, fenceToken uint64) (state.Event, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return state.Event{}, fmt.Errorf("encode state %s record: %w", kind, err)
	}
	record := state.Record{Kind: kind, SchemaVersion: 1, Data: data}
	eventID, err := identity.ID("state-event", 1, struct {
		Path   string       `json:"path"`
		Record state.Record `json:"record"`
	}{recordPath, record})
	if err != nil {
		return state.Event{}, err
	}
	return state.Event{ID: eventID, FenceKey: fenceKey, FenceToken: fenceToken, RecordPath: recordPath, Record: record}, nil
}
