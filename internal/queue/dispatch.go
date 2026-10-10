package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

const (
	AttemptClaimed         = "claimed"
	AttemptDispatched      = "dispatched"
	AttemptReclaimed       = "reclaimed"
	AttemptCompleted       = "completed"
	stateRequestPath       = "state/requests/"
	dispatchClaimOperation = "dispatch.claim"
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
	OperationID       string    `json:"operation_id,omitempty"`
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
func ClaimNext(ctx context.Context, coordinator state.TransactionCoordinator, dispatcher string, now time.Time, leaseDuration time.Duration) (DispatchClaim, error) {
	return ClaimNextWithHandoff(ctx, coordinator, dispatcher, now, leaseDuration, nil)
}

// ClaimNextWithHandoff claims the oldest ready request and atomically writes
// its projection handoff when trusted producer provenance is supplied.
func ClaimNextWithHandoff(ctx context.Context, coordinator state.TransactionCoordinator, dispatcher string, now time.Time, leaseDuration time.Duration, producer *projection.WorkflowProvenance) (DispatchClaim, error) {
	return claimNext(ctx, coordinator, dispatcher, now, leaseDuration, producer)
}

func claimNext(ctx context.Context, coordinator state.TransactionCoordinator, dispatcher string, now time.Time, leaseDuration time.Duration, producer *projection.WorkflowProvenance) (DispatchClaim, error) {
	if coordinator == nil || strings.TrimSpace(dispatcher) == "" || now.IsZero() || leaseDuration <= 0 {
		return DispatchClaim{}, fmt.Errorf("state transaction coordinator, dispatcher, claim time, and positive lease duration are required")
	}
	operationID, err := dispatchOperationID("next", dispatcher, "", now)
	if err != nil {
		return DispatchClaim{}, err
	}
	return runClaimTransactionWithProducer(ctx, coordinator, operationID, dispatcher, now, leaseDuration, false, producer, func(snapshot state.Snapshot) (Request, error) {
		requests, loadErr := loadVersionedRequests(snapshot)
		if loadErr != nil {
			return Request{}, loadErr
		}
		globalEnabled, controlErr := GlobalDispatchEnabled(snapshot)
		if controlErr != nil {
			return Request{}, controlErr
		}
		if !globalEnabled {
			return Request{}, ErrGlobalDispatchDisabled
		}
		ready, selectErr := SelectDispatchableRequests(requests, now)
		if selectErr != nil {
			return Request{}, selectErr
		}
		for _, request := range ready {
			paused, pauseErr := ModuleDispatchPaused(snapshot, request.ModuleID)
			if pauseErr != nil {
				return Request{}, pauseErr
			}
			if paused || moduleHasActiveClaim(requests, request.ModuleID) {
				continue
			}
			eligible, slotErr := slotAllowsRequestInQueue(snapshot, request, requests)
			if slotErr != nil {
				return Request{}, slotErr
			}
			if !eligible {
				continue
			}
			barrierAllows, _, barrierErr := evaluateProjectionBarrier(snapshot, request, now)
			if barrierErr != nil {
				return Request{}, barrierErr
			}
			if barrierAllows {
				return request, nil
			}
		}
		return Request{}, ErrNoDispatchableWork
	})
}

// ClaimRequest claims only the named queued request. It never substitutes a
// different request when the target is absent, paused, superseded, or not due.
func ClaimRequest(ctx context.Context, coordinator state.TransactionCoordinator, requestID, dispatcher string, now time.Time, leaseDuration time.Duration) (DispatchClaim, error) {
	return claimDurableRequest(ctx, coordinator, requestID, dispatcher, now, leaseDuration, false)
}

// ClaimRequestWithHandoff is the provenance-bearing variant used by reviewed
// dispatcher workflows. Nil provenance preserves the legacy call contract.
func ClaimRequestWithHandoff(ctx context.Context, coordinator state.TransactionCoordinator, requestID, dispatcher string, now time.Time, leaseDuration time.Duration, producer *projection.WorkflowProvenance) (DispatchClaim, error) {
	return claimDurableRequestWithProducer(ctx, coordinator, requestID, dispatcher, now, leaseDuration, producer)
}

// ClaimRequestWithLifecycle emits acquisition progress only for an explicitly
// approved reader-compatible caller. Default dispatch retains its old record set.
func ClaimRequestWithLifecycle(ctx context.Context, coordinator state.TransactionCoordinator, requestID, dispatcher string, now time.Time, leaseDuration time.Duration) (DispatchClaim, error) {
	return claimDurableRequest(ctx, coordinator, requestID, dispatcher, now, leaseDuration, true)
}

func claimDurableRequest(ctx context.Context, coordinator state.TransactionCoordinator, requestID, dispatcher string, now time.Time, leaseDuration time.Duration, lifecycle bool) (DispatchClaim, error) {
	if coordinator == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(dispatcher) == "" || now.IsZero() || leaseDuration <= 0 {
		return DispatchClaim{}, fmt.Errorf("state transaction coordinator, request ID, dispatcher, claim time, and positive lease duration are required")
	}
	operationID, err := dispatchOperationID("request", dispatcher, requestID, now)
	if err != nil {
		return DispatchClaim{}, err
	}
	return runClaimTransactionWithProducer(ctx, coordinator, operationID, dispatcher, now, leaseDuration, lifecycle, nil, func(snapshot state.Snapshot) (Request, error) {
		requests, loadErr := loadVersionedRequests(snapshot)
		if loadErr != nil {
			return Request{}, loadErr
		}
		globalEnabled, controlErr := GlobalDispatchEnabled(snapshot)
		if controlErr != nil {
			return Request{}, controlErr
		}
		if !globalEnabled {
			return Request{}, ErrGlobalDispatchDisabled
		}
		for index, request := range requests {
			if request.ID != requestID {
				continue
			}
			if request.Status != StatusQueued || request.Work == nil || !retryReady(*request.Work, now) || hasNewerDesiredRequest(requests, index) || moduleHasActiveClaim(requests, request.ModuleID) {
				return Request{}, ErrNoDispatchableWork
			}
			paused, pauseErr := ModuleDispatchPaused(snapshot, request.ModuleID)
			if pauseErr != nil {
				return Request{}, pauseErr
			}
			if paused {
				return Request{}, ErrNoDispatchableWork
			}
			eligible, slotErr := slotAllowsRequestInQueue(snapshot, request, requests)
			if slotErr != nil {
				return Request{}, slotErr
			}
			if !eligible {
				return Request{}, ErrNoDispatchableWork
			}
			barrierAllows, _, barrierErr := evaluateProjectionBarrier(snapshot, request, now)
			if barrierErr != nil {
				return Request{}, barrierErr
			}
			if !barrierAllows {
				return Request{}, ErrNoDispatchableWork
			}
			return request, nil
		}
		return Request{}, ErrNoDispatchableWork
	})
}

// ClaimModule claims the oldest ready queued request for one exact module.
// It never substitutes another module when that module is absent, paused,
// superseded, retry-delayed, or already claimed.
func ClaimModule(ctx context.Context, coordinator state.TransactionCoordinator, moduleID, dispatcher string, now time.Time, leaseDuration time.Duration) (DispatchClaim, error) {
	return ClaimModuleWithHandoff(ctx, coordinator, moduleID, dispatcher, now, leaseDuration, nil)
}

// ClaimModuleWithHandoff claims one exact module and atomically writes its
// projection handoff when trusted producer provenance is supplied.
func ClaimModuleWithHandoff(ctx context.Context, coordinator state.TransactionCoordinator, moduleID, dispatcher string, now time.Time, leaseDuration time.Duration, producer *projection.WorkflowProvenance) (DispatchClaim, error) {
	return claimModule(ctx, coordinator, moduleID, dispatcher, now, leaseDuration, producer)
}

func claimModule(ctx context.Context, coordinator state.TransactionCoordinator, moduleID, dispatcher string, now time.Time, leaseDuration time.Duration, producer *projection.WorkflowProvenance) (DispatchClaim, error) {
	if coordinator == nil || !moduleIDPattern.MatchString(moduleID) || strings.TrimSpace(dispatcher) == "" || now.IsZero() || leaseDuration <= 0 {
		return DispatchClaim{}, fmt.Errorf("state transaction coordinator, canonical module ID, dispatcher, claim time, and positive lease duration are required")
	}
	operationID, err := dispatchOperationID("module", dispatcher, moduleID, now)
	if err != nil {
		return DispatchClaim{}, err
	}
	return runClaimTransactionWithProducer(ctx, coordinator, operationID, dispatcher, now, leaseDuration, false, producer, func(snapshot state.Snapshot) (Request, error) {
		requests, loadErr := loadVersionedRequests(snapshot)
		if loadErr != nil {
			return Request{}, loadErr
		}
		globalEnabled, controlErr := GlobalDispatchEnabled(snapshot)
		if controlErr != nil {
			return Request{}, controlErr
		}
		if !globalEnabled {
			return Request{}, ErrGlobalDispatchDisabled
		}
		if moduleHasActiveClaim(requests, moduleID) {
			return Request{}, ErrNoDispatchableWork
		}
		paused, pauseErr := ModuleDispatchPaused(snapshot, moduleID)
		if pauseErr != nil {
			return Request{}, pauseErr
		}
		if paused {
			return Request{}, ErrNoDispatchableWork
		}
		ready := make([]Request, 0, 1)
		for index, request := range requests {
			if request.ModuleID == moduleID && request.Work != nil && request.Status == StatusQueued && retryReady(*request.Work, now) && !hasNewerDesiredRequest(requests, index) {
				eligible, slotErr := slotAllowsRequestInQueue(snapshot, request, requests)
				if slotErr != nil {
					return Request{}, slotErr
				}
				if !eligible {
					continue
				}
				ready = append(ready, cloneRequest(request))
			}
		}
		if len(ready) == 0 {
			return Request{}, ErrNoDispatchableWork
		}
		sort.Slice(ready, func(left, right int) bool { return dispatchRequestLess(ready[left], ready[right]) })
		for _, request := range ready {
			barrierAllows, _, barrierErr := evaluateProjectionBarrier(snapshot, request, now)
			if barrierErr != nil {
				return Request{}, barrierErr
			}
			if barrierAllows {
				return request, nil
			}
		}
		return Request{}, ErrNoDispatchableWork
	})
}

// evaluateProjectionBarrier fails closed without a matching durable handoff.
// The operation receipt is checked before either projection success or timeout
// release can authorize the consumer.
func evaluateProjectionBarrier(snapshot state.Snapshot, request Request, now time.Time) (bool, *projection.BarrierRelease, error) {
	handoff, found, err := findConsumerHandoff(snapshot, request)
	if err != nil {
		return false, nil, err
	}
	if !found {
		return false, nil, nil
	}
	if !verifiedPredecessorReceipt(snapshot, handoff) {
		return false, nil, nil
	}
	return evaluateHandoffProjection(snapshot, handoff, now)
}

func evaluateHandoffProjection(snapshot state.Snapshot, handoff projection.Handoff, now time.Time) (bool, *projection.BarrierRelease, error) {
	required := handoff.Projection
	acknowledgementPath, err := identity.StateProjectionAcknowledgementPath(handoff.ModuleID, required.IntentID)
	if err != nil {
		return false, nil, err
	}
	if record, exists := snapshot.Records[acknowledgementPath]; exists && record.Kind == "projection_acknowledgement" {
		var acknowledgement projection.Acknowledgement
		if json.Unmarshal(record.Data, &acknowledgement) == nil && acknowledgement.Validate(required, now) == nil {
			return true, nil, nil
		}
	}

	releasePath, err := identity.StateProjectionBarrierReleasePath(handoff.ModuleID, required.IntentID)
	if err != nil {
		return false, nil, err
	}
	if record, exists := snapshot.Records[releasePath]; exists {
		if record.Kind != "projection_barrier_release" {
			return false, nil, nil
		}
		var release projection.BarrierRelease
		if json.Unmarshal(record.Data, &release) != nil || release.Validate(required, handoff.BarrierDeadline, now) != nil {
			return false, nil, nil
		}
		return true, nil, nil
	}
	if now.Before(handoff.BarrierDeadline) {
		return false, nil, nil
	}
	release := projection.BarrierRelease{
		SchemaVersion: projection.SchemaVersion, Projection: required,
		Status: projection.BarrierReleased, Deadline: handoff.BarrierDeadline,
		ReleasedAt: now.UTC(),
	}
	if err = release.Validate(required, handoff.BarrierDeadline, now); err != nil {
		return false, nil, err
	}
	return true, &release, nil
}

func findConsumerHandoff(snapshot state.Snapshot, request Request) (projection.Handoff, bool, error) {
	wantedStage := string(request.Work.Phase)
	if request.Work.Phase == PhaseAssess {
		wantedStage = "assessment"
	}
	fingerprint, err := identity.ID("input-fingerprint", 1, request.Fingerprint)
	if err != nil {
		return projection.Handoff{}, false, err
	}
	var matched projection.Handoff
	found := false
	for recordPath, record := range snapshot.Records {
		if record.Kind != "stage_handoff" {
			continue
		}
		var handoff projection.Handoff
		if err := json.Unmarshal(record.Data, &handoff); err != nil {
			return projection.Handoff{}, false, fmt.Errorf("decode stage handoff %s: %w", recordPath, err)
		}
		if err := handoff.Validate(); err != nil {
			return projection.Handoff{}, false, fmt.Errorf("validate stage handoff %s: %w", recordPath, err)
		}
		wantPath, err := identity.StateProjectionHandoffPath(handoff.ModuleID, handoff.ID)
		if err != nil || recordPath != wantPath {
			return projection.Handoff{}, false, fmt.Errorf("stage handoff is stored at an invalid path")
		}
		if handoff.ModuleID != request.ModuleID || handoff.Generation != request.Work.DesiredGeneration || handoff.ConsumerStage != wantedStage || handoff.InputFingerprint != fingerprint {
			continue
		}
		acceptedFactsID := request.Work.AcceptedFactsID
		if request.Work.Phase == PhaseAssess {
			acceptedFactsID = request.Work.FactsID
		}
		if acceptedFactsID != "" && handoff.AcceptedFactsID != acceptedFactsID {
			continue
		}
		if found {
			return projection.Handoff{}, false, fmt.Errorf("multiple stage handoffs match queued request %s", request.ID)
		}
		matched, found = handoff, true
	}
	return matched, found, nil
}

func verifiedPredecessorReceipt(snapshot state.Snapshot, handoff projection.Handoff) bool {
	receiptPath, err := identity.StateOperationReceiptPath(handoff.ModuleID, handoff.OperationID)
	if err != nil {
		return false
	}
	record, exists := snapshot.Records[receiptPath]
	if !exists || record.Kind != "operation_receipt" || record.SchemaVersion != 1 || !appliedReceiptMatches(snapshot, handoff.OperationID, receiptPath, record) {
		return false
	}
	var receipt state.OperationReceipt
	if json.Unmarshal(record.Data, &receipt) != nil || receipt.Validate(receiptPath) != nil || receipt.ModuleID != handoff.ModuleID || receipt.OperationID != handoff.OperationID || len(receipt.WriteSet) == 0 {
		return false
	}
	return verifyPredecessorWriteSet(snapshot, handoff, receipt)
}

func appliedReceiptMatches(snapshot state.Snapshot, operationID, recordPath string, record state.Record) bool {
	digest, applied := appliedEventDigest(snapshot, operationID)
	if !applied {
		return false
	}
	expected, err := identity.ID("state-event", 1, state.Event{ID: operationID, RecordPath: recordPath, Record: record})
	return err == nil && digest == strings.TrimPrefix(expected, "state-event-")
}

func verifyPredecessorWriteSet(snapshot state.Snapshot, handoff projection.Handoff, receipt state.OperationReceipt) bool {
	handoffPath, err := identity.StateProjectionHandoffPath(handoff.ModuleID, handoff.ID)
	if err != nil {
		return false
	}
	verifiedHandoff, verifiedCompletion := false, false
	for _, entry := range receipt.WriteSet {
		digest, applied := appliedEventDigest(snapshot, entry.EventID)
		if !applied || digest != entry.Digest {
			return false
		}
		if entry.RecordPath == handoffPath && entry.RecordKind == "stage_handoff" {
			current := snapshot.Records[entry.RecordPath]
			handoffData, marshalErr := json.Marshal(handoff)
			if marshalErr != nil || current.Kind != entry.RecordKind || !bytes.Equal(current.Data, handoffData) {
				return false
			}
			verifiedHandoff = true
		} else {
			verifiedCompletion = true
		}
	}
	return verifiedHandoff && verifiedCompletion
}

func appliedEventDigest(snapshot state.Snapshot, eventID string) (string, bool) {
	if digest, exists := snapshot.AppliedEvents[eventID]; exists {
		return digest, true
	}
	for _, aggregate := range snapshot.Aggregates {
		if digest, exists := aggregate.AppliedEvents[eventID]; exists {
			return digest, true
		}
	}
	return "", false
}

func claimDurableRequestWithProducer(ctx context.Context, coordinator state.TransactionCoordinator, requestID, dispatcher string, now time.Time, leaseDuration time.Duration, producer *projection.WorkflowProvenance) (DispatchClaim, error) {
	if coordinator == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(dispatcher) == "" || now.IsZero() || leaseDuration <= 0 {
		return DispatchClaim{}, fmt.Errorf("state transaction coordinator, request ID, dispatcher, claim time, and positive lease duration are required")
	}
	operationID, err := dispatchOperationID("request", dispatcher, requestID, now)
	if err != nil {
		return DispatchClaim{}, err
	}
	return runClaimTransactionWithProducer(ctx, coordinator, operationID, dispatcher, now, leaseDuration, false, producer, func(snapshot state.Snapshot) (Request, error) {
		requests, loadErr := loadVersionedRequests(snapshot)
		if loadErr != nil {
			return Request{}, loadErr
		}
		globalEnabled, controlErr := GlobalDispatchEnabled(snapshot)
		if controlErr != nil {
			return Request{}, controlErr
		}
		if !globalEnabled {
			return Request{}, ErrGlobalDispatchDisabled
		}
		for index, request := range requests {
			if request.ID != requestID {
				continue
			}
			if !requestReadyToClaim(requests, index, request, now) || moduleHasActiveClaim(requests, request.ModuleID) {
				return Request{}, ErrNoDispatchableWork
			}
			return selectUnpausedRequest(snapshot, request, requests, now)
		}
		return Request{}, ErrNoDispatchableWork
	})
}

func requestReadyToClaim(requests []Request, index int, request Request, now time.Time) bool {
	return request.Status == StatusQueued && request.Work != nil && retryReady(*request.Work, now) && !hasNewerDesiredRequest(requests, index)
}

func selectUnpausedRequest(snapshot state.Snapshot, request Request, requests []Request, now time.Time) (Request, error) {
	paused, err := ModuleDispatchPaused(snapshot, request.ModuleID)
	if err != nil {
		return Request{}, err
	}
	if paused {
		return Request{}, ErrNoDispatchableWork
	}
	eligible, err := slotAllowsRequestInQueue(snapshot, request, requests)
	if err != nil {
		return Request{}, err
	}
	if !eligible {
		return Request{}, ErrNoDispatchableWork
	}
	allowed, _, err := evaluateProjectionBarrier(snapshot, request, now)
	if err != nil {
		return Request{}, err
	}
	if !allowed {
		return Request{}, ErrNoDispatchableWork
	}
	return request, nil
}

func dispatchOperationID(kind, dispatcher, target string, now time.Time) (string, error) {
	return identity.ID("dispatch-operation", 1, struct {
		Kind       string    `json:"kind"`
		Dispatcher string    `json:"dispatcher"`
		Target     string    `json:"target,omitempty"`
		IssuedAt   time.Time `json:"issued_at"`
	}{kind, dispatcher, target, now.UTC()})
}

func runClaimTransaction(ctx context.Context, coordinator state.TransactionCoordinator, operationID, dispatcher string, now time.Time, leaseDuration time.Duration, lifecycle bool, selectRequest func(state.Snapshot) (Request, error)) (DispatchClaim, error) {
	return runClaimTransactionWithOptions(ctx, coordinator, claimTransactionOptions{operationID: operationID, dispatcher: dispatcher, now: now, leaseDuration: leaseDuration, lifecycle: lifecycle, selectRequest: selectRequest})
}

func runClaimTransactionWithProducer(ctx context.Context, coordinator state.TransactionCoordinator, operationID, dispatcher string, now time.Time, leaseDuration time.Duration, lifecycle bool, producer *projection.WorkflowProvenance, selectRequest func(state.Snapshot) (Request, error)) (DispatchClaim, error) {
	return runClaimTransactionWithOptions(ctx, coordinator, claimTransactionOptions{operationID: operationID, dispatcher: dispatcher, now: now, leaseDuration: leaseDuration, lifecycle: lifecycle, producer: producer, selectRequest: selectRequest})
}

type claimTransactionOptions struct {
	operationID   string
	dispatcher    string
	now           time.Time
	leaseDuration time.Duration
	lifecycle     bool
	producer      *projection.WorkflowProvenance
	selectRequest func(state.Snapshot) (Request, error)
}

func runClaimTransactionWithOptions(ctx context.Context, coordinator state.TransactionCoordinator, options claimTransactionOptions) (DispatchClaim, error) {
	operationID, dispatcher, now, leaseDuration, lifecycle, producer, selectRequest := options.operationID, options.dispatcher, options.now, options.leaseDuration, options.lifecycle, options.producer, options.selectRequest
	if producer != nil {
		return runClaimOperationTransaction(ctx, coordinator, operationID, dispatcher, now, leaseDuration, lifecycle, *producer, selectRequest)
	}
	var proposed DispatchClaim
	result, err := coordinator.RunTransaction(ctx, operationID, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
		request, selectErr := selectRequest(snapshot)
		if selectErr != nil {
			return state.TransactionDecision{}, selectErr
		}
		if request.ID == "" {
			return state.TransactionDecision{Noop: true}, nil
		}
		decision, claim, buildErr := buildClaimDecision(snapshot, request, dispatcher, now, leaseDuration, lifecycle, operationID)
		if buildErr != nil {
			return state.TransactionDecision{}, buildErr
		}
		decision.Events[0].ID = operationID
		if producer != nil {
			decision, buildErr = appendClaimProjectionHandoff(snapshot, request, *producer, operationID, decision, now)
			if buildErr != nil {
				return state.TransactionDecision{}, buildErr
			}
		}
		proposed = claim
		return decision, nil
	}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
		claim, found, resolveErr := resolveDispatchClaim(snapshot, operationID)
		return state.TransactionResolution{Found: found, Value: claim}, resolveErr
	})
	if err != nil {
		if errors.Is(err, state.ErrTransactionConflict) {
			return DispatchClaim{}, state.ErrHeadMoved
		}
		return DispatchClaim{}, err
	}
	if resolved, ok := result.Value.(DispatchClaim); ok && resolved.Attempt.ID != "" {
		resolved.Head = result.Head
		return resolved, nil
	}
	if proposed.Attempt.ID != "" {
		proposed.Head = result.Head
		return proposed, nil
	}
	return DispatchClaim{Head: result.Head}, nil
}

type operationTransactionCoordinator interface {
	RunOperationTransaction(context.Context, string, string, []state.Event, state.TransactionDecisionFunc, state.TransactionResultResolver) (state.TransactionResult, error)
}

func runClaimOperationTransaction(ctx context.Context, coordinator state.TransactionCoordinator, operationID, dispatcher string, now time.Time, leaseDuration time.Duration, lifecycle bool, producer projection.WorkflowProvenance, selectRequest func(state.Snapshot) (Request, error)) (DispatchClaim, error) {
	receiptCoordinator, ok := coordinator.(operationTransactionCoordinator)
	if !ok {
		return DispatchClaim{}, fmt.Errorf("provenance-bearing dispatch claims require an operation-receipt coordinator")
	}
	moduleID := ""
	var selectedRequest Request
	var initialClaim DispatchClaim
	var expectedEvents []state.Event
	head, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return DispatchClaim{}, err
	}
	if head == "" {
		return DispatchClaim{}, fmt.Errorf("state branch head is required")
	}
	if eventApplied(snapshot, operationID) {
		initialClaim, expectedEvents, err = replayClaimOperation(snapshot, operationID, now)
	} else {
		request, selectErr := selectRequest(snapshot)
		if selectErr != nil {
			return DispatchClaim{}, selectErr
		}
		if request.ID == "" {
			return DispatchClaim{Head: head}, nil
		}
		moduleID = request.ModuleID
		selectedRequest = cloneRequest(request)
		decision, claim, buildErr := buildClaimDecision(snapshot, request, dispatcher, now, leaseDuration, lifecycle, operationID)
		if buildErr != nil {
			return DispatchClaim{}, buildErr
		}
		expectedEvents, err = prepareClaimOperationEvents(snapshot, request, claim.Request, producer, operationID, decision, now)
		if err == nil {
			initialClaim = claim
		}
	}
	if err != nil {
		return DispatchClaim{}, err
	}
	if moduleID == "" {
		moduleID = initialClaim.Request.ModuleID
	}
	if moduleID == "" || len(expectedEvents) == 0 {
		return DispatchClaim{}, state.ErrTransactionIdentityConflict
	}
	decide := func(current state.Snapshot) (state.TransactionDecision, error) {
		request, selectErr := selectRequest(current)
		if selectErr != nil {
			return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
		}
		if request.ID == "" || request.ModuleID != moduleID || !sameClaimRequest(request, selectedRequest) {
			return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
		}
		decision, claim, buildErr := buildClaimDecision(current, request, dispatcher, now, leaseDuration, lifecycle, operationID)
		if buildErr != nil {
			return state.TransactionDecision{}, buildErr
		}
		events, buildErr := prepareClaimOperationEvents(current, request, claim.Request, producer, operationID, decision, now)
		if buildErr != nil {
			return state.TransactionDecision{}, buildErr
		}
		if !sameClaimEventSet(events, expectedEvents) || !sameClaimValue(claim, initialClaim) {
			return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
		}
		decision.Events = events
		decision.Result = claim
		return decision, nil
	}
	result, err := receiptCoordinator.RunOperationTransaction(ctx, moduleID, operationID, expectedEvents, decide, func(current state.Snapshot) (state.TransactionResolution, error) {
		claim, found, resolveErr := resolveDispatchClaim(current, operationID)
		return state.TransactionResolution{Found: found, Value: claim}, resolveErr
	})
	if err != nil {
		if errors.Is(err, state.ErrTransactionConflict) {
			return DispatchClaim{}, state.ErrHeadMoved
		}
		return DispatchClaim{}, err
	}
	resolved, err := dispatchClaimResult(result.Value)
	if err != nil {
		return DispatchClaim{}, err
	}
	resolved.Head = result.Head
	return resolved, nil
}

func replayClaimOperation(snapshot state.Snapshot, operationID string, now time.Time) (DispatchClaim, []state.Event, error) {
	var receipt state.OperationReceipt
	var moduleID string
	for recordPath, record := range snapshot.Records {
		if record.Kind != "operation_receipt" {
			continue
		}
		var candidate state.OperationReceipt
		if json.Unmarshal(record.Data, &candidate) == nil && candidate.OperationID == operationID {
			moduleID = candidate.ModuleID
			receiptPath, pathErr := identity.StateOperationReceiptPath(moduleID, operationID)
			if pathErr != nil || recordPath != receiptPath || candidate.Validate(receiptPath) != nil {
				return DispatchClaim{}, nil, state.ErrTransactionIdentityConflict
			}
			receipt = candidate
			break
		}
	}
	if moduleID == "" {
		return DispatchClaim{}, nil, state.ErrTransactionIdentityConflict
	}
	if err := verifyRecordedClaimOperation(snapshot, moduleID, operationID, receipt); err != nil {
		return DispatchClaim{}, nil, err
	}
	claim, err := dispatchClaimResult(json.RawMessage(receipt.Result))
	if err != nil || claim.Attempt.OperationID != operationID || claim.Attempt.ModuleID != moduleID || claim.Request.ModuleID != moduleID {
		return DispatchClaim{}, nil, state.ErrTransactionIdentityConflict
	}
	requestEvent, err := stateRecordEvent(stateRequestPath+claim.Request.ID+".json", "request", claim.Request, "", 0)
	if err != nil {
		return DispatchClaim{}, nil, err
	}
	attemptEvent, err := stateRecordEvent(dispatchAttemptPath(claim.Attempt), "attempt", claim.Attempt, claim.Request.ModuleID, claim.Attempt.Fence)
	if err != nil {
		return DispatchClaim{}, nil, err
	}
	producer, err := recordedClaimProducer(snapshot, operationID, moduleID)
	if err != nil {
		return DispatchClaim{}, nil, err
	}
	decision := state.TransactionDecision{Events: []state.Event{requestEvent, attemptEvent}, Result: claim}
	events, err := appendClaimProjectionHandoff(snapshot, claim.Request, producer, operationID, decision, claim.Attempt.ClaimedAt)
	if err != nil {
		return DispatchClaim{}, nil, err
	}
	for _, entry := range receipt.WriteSet {
		if entry.RecordKind != "projection_barrier_release" {
			continue
		}
		releaseRecord, found := snapshot.Records[entry.RecordPath]
		if !found || releaseRecord.Kind != entry.RecordKind {
			return DispatchClaim{}, nil, state.ErrTransactionIdentityConflict
		}
		events.Events = append(events.Events, state.Event{ID: entry.EventID, RecordPath: entry.RecordPath, Record: releaseRecord})
	}
	return claim, events.Events, nil
}

func recordedClaimProducer(snapshot state.Snapshot, operationID, moduleID string) (projection.WorkflowProvenance, error) {
	for _, record := range snapshot.Records {
		if record.Kind != "stage_handoff" {
			continue
		}
		var handoff projection.Handoff
		if err := json.Unmarshal(record.Data, &handoff); err != nil {
			return projection.WorkflowProvenance{}, err
		}
		if handoff.ModuleID == moduleID && handoff.OperationID == operationID {
			return handoff.Producer, nil
		}
	}
	return projection.WorkflowProvenance{}, state.ErrTransactionIdentityConflict
}

func verifyRecordedClaimOperation(snapshot state.Snapshot, moduleID, operationID string, receipt state.OperationReceipt) error {
	if receipt.ModuleID != moduleID || receipt.OperationID != operationID {
		return state.ErrTransactionIdentityConflict
	}
	receiptPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil || !appliedReceiptMatches(snapshot, operationID, receiptPath, snapshot.Records[receiptPath]) {
		return state.ErrTransactionIdentityConflict
	}
	for _, entry := range receipt.WriteSet {
		digest, applied := appliedEventDigest(snapshot, entry.EventID)
		if !applied || digest != entry.Digest {
			return state.ErrTransactionIdentityConflict
		}
	}
	return nil
}

func prepareClaimOperationEvents(snapshot state.Snapshot, request, claim Request, producer projection.WorkflowProvenance, operationID string, decision state.TransactionDecision, now time.Time) ([]state.Event, error) {
	if len(decision.Events) < 2 {
		return nil, state.ErrTransactionIdentityConflict
	}
	requestEvent, err := stateRecordEvent(decision.Events[0].RecordPath, decision.Events[0].Record.Kind, claim, "", 0)
	if err != nil {
		return nil, err
	}
	decision.Events[0] = requestEvent
	decision, err = appendClaimProjectionHandoff(snapshot, request, producer, operationID, decision, now)
	if err != nil {
		return nil, err
	}
	return decision.Events, nil
}

func sameClaimRequest(left, right Request) bool {
	leftData, leftErr := json.Marshal(left)
	rightData, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftData, rightData)
}

func sameClaimEventSet(left, right []state.Event) bool {
	if len(left) != len(right) {
		return false
	}
	leftEvents := make(map[string][]byte, len(left))
	for _, event := range left {
		encoded, err := json.Marshal(event)
		if err != nil || event.ID == "" {
			return false
		}
		if _, exists := leftEvents[event.ID]; exists {
			return false
		}
		leftEvents[event.ID] = encoded
	}
	for _, event := range right {
		encoded, err := json.Marshal(event)
		if err != nil || !bytes.Equal(leftEvents[event.ID], encoded) {
			return false
		}
		delete(leftEvents, event.ID)
	}
	return len(leftEvents) == 0
}

func sameClaimValue(left, right DispatchClaim) bool {
	leftData, leftErr := json.Marshal(left)
	rightData, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftData, rightData)
}

func dispatchClaimResult(value any) (DispatchClaim, error) {
	switch claim := value.(type) {
	case DispatchClaim:
		return claim, nil
	case json.RawMessage:
		var decoded DispatchClaim
		if err := json.Unmarshal(claim, &decoded); err != nil {
			return DispatchClaim{}, fmt.Errorf("decode immutable dispatch claim result: %w", err)
		}
		return decoded, nil
	default:
		return DispatchClaim{}, fmt.Errorf("committed dispatch claim result has an invalid type")
	}
}

func appendClaimProjectionHandoff(snapshot state.Snapshot, request Request, producer projection.WorkflowProvenance, operationID string, decision state.TransactionDecision, now time.Time) (state.TransactionDecision, error) {
	if request.Work == nil {
		return state.TransactionDecision{}, fmt.Errorf("claimed request has no work contract")
	}
	projectionStatus := "queued"
	consumerStage := "acquire_source"
	if request.Work.Phase == PhaseAssess {
		projectionStatus = "analyzing"
		consumerStage = "assessment"
	}
	transitionID := "dispatch:" + operationID
	intent, err := projection.NewDesiredIntent(request.ModuleID, request.Work.DesiredGeneration, transitionID, projectionStatus, []projection.OwnedField{{Name: "phase", Value: string(request.Work.Phase)}, {Name: "request_id", Value: request.ID}}, nil)
	if err != nil {
		return state.TransactionDecision{}, err
	}
	intentPath, err := identity.StateProjectionIntentPath(request.ModuleID)
	if err != nil {
		return state.TransactionDecision{}, err
	}
	intentEvent, err := stateRecordEvent(intentPath, "projection_intent", intent, "", 0)
	if err != nil {
		return state.TransactionDecision{}, err
	}
	intentIdentity, err := intent.Identity()
	if err != nil {
		return state.TransactionDecision{}, err
	}
	inputFingerprint, err := identity.ID("input-fingerprint", 1, request.Fingerprint)
	if err != nil {
		return state.TransactionDecision{}, err
	}
	handoff, err := projection.NewHandoff(projection.Handoff{
		ModuleID: request.ModuleID, Generation: request.Work.DesiredGeneration, TransitionID: transitionID,
		InputFingerprint: inputFingerprint, OperationID: operationID, Producer: producer,
		ConsumerStage: consumerStage, Projection: intentIdentity,
	}, now)
	if err != nil {
		return state.TransactionDecision{}, err
	}
	handoffPath, err := identity.StateProjectionHandoffPath(request.ModuleID, handoff.ID)
	if err != nil {
		return state.TransactionDecision{}, err
	}
	handoffData, err := json.Marshal(handoff)
	if err != nil {
		return state.TransactionDecision{}, err
	}
	handoffEvent := state.Event{ID: handoff.ID, RecordPath: handoffPath, Record: state.Record{Kind: "stage_handoff", SchemaVersion: 1, Data: handoffData}}
	writeSet := append(append([]state.Event(nil), decision.Events...), intentEvent, handoffEvent)
	updated := decision
	updated.Events = writeSet
	return updated, nil
}

func buildClaimDecision(snapshot state.Snapshot, request Request, dispatcher string, now time.Time, leaseDuration time.Duration, lifecycle bool, operationID string) (state.TransactionDecision, DispatchClaim, error) {
	slotEvent, err := lifecycleSlotEvent(snapshot, request, operationID, now)
	if err != nil {
		return state.TransactionDecision{}, DispatchClaim{}, err
	}
	barrierAllows, barrierRelease, err := evaluateProjectionBarrier(snapshot, request, now)
	if err != nil {
		return state.TransactionDecision{}, DispatchClaim{}, err
	}
	if !barrierAllows {
		return state.TransactionDecision{}, DispatchClaim{}, ErrNoDispatchableWork
	}
	fence := snapshot.FencingTokens[request.ModuleID]
	if fence == math.MaxUint64 {
		return state.TransactionDecision{}, DispatchClaim{}, fmt.Errorf("module %s exhausted its fencing-token space", request.ModuleID)
	}
	fence++
	attemptID, err := identity.ID("attempt", 1, struct {
		RequestID string `json:"request_id"`
		Fence     uint64 `json:"fence"`
	}{request.ID, fence})
	if err != nil {
		return state.TransactionDecision{}, DispatchClaim{}, fmt.Errorf("identify factory attempt: %w", err)
	}
	attemptNumber, err := countRequestAttempts(snapshot, request.ID)
	if err != nil {
		return state.TransactionDecision{}, DispatchClaim{}, err
	}
	attempt := Attempt{
		SchemaVersion: 1, ID: attemptID, RequestID: request.ID, ModuleID: request.ModuleID,
		OperationID: operationID, InputFingerprint: request.Fingerprint, SourceSHA: request.SourceSHA,
		Phase: request.Work.Phase, DesiredGeneration: request.Work.DesiredGeneration, Number: attemptNumber + 1,
		Fence: fence, Dispatcher: dispatcher, Status: AttemptClaimed, ClaimedAt: now.UTC(), LeaseExpiresAt: now.Add(leaseDuration).UTC(),
	}
	if err = attempt.Validate(); err != nil {
		return state.TransactionDecision{}, DispatchClaim{}, err
	}
	claimed := cloneRequest(request)
	claimed.Status = StatusClaimed
	claimed.OwnerRunID = ""
	claimed.UpdatedAt = now.UTC()
	claimed.Work.AttemptID = attempt.ID
	claimed.Work.Fence = fence
	claimed.Work.Lease = &ComputeLease{Owner: attempt.ID, ExpiresAt: attempt.LeaseExpiresAt, Fence: fence}
	if err = claimed.Validate(); err != nil {
		return state.TransactionDecision{}, DispatchClaim{}, fmt.Errorf("validate dispatch claim: %w", err)
	}
	requestEvent, err := stateRecordEvent(stateRequestPath+claimed.ID+".json", "request", claimed, "", 0)
	if err != nil {
		return state.TransactionDecision{}, DispatchClaim{}, err
	}
	requestEvent.ID = operationID
	attemptEvent, err := stateRecordEvent(dispatchAttemptPath(attempt), "attempt", attempt, claimed.ModuleID, fence)
	if err != nil {
		return state.TransactionDecision{}, DispatchClaim{}, err
	}
	events := []state.Event{requestEvent, attemptEvent, slotEvent}
	if lifecycle {
		events, err = AcquisitionLifecycleEvents(snapshot, claimed, events, now)
		if err != nil {
			return state.TransactionDecision{}, DispatchClaim{}, err
		}
	}
	if barrierRelease != nil {
		releasePath, pathErr := identity.StateProjectionBarrierReleasePath(request.ModuleID, barrierRelease.Projection.IntentID)
		if pathErr != nil {
			return state.TransactionDecision{}, DispatchClaim{}, pathErr
		}
		releaseEvent, eventErr := stateRecordEvent(releasePath, "projection_barrier_release", *barrierRelease, "", 0)
		if eventErr != nil {
			return state.TransactionDecision{}, DispatchClaim{}, eventErr
		}
		events = append(events, releaseEvent)
	}
	claim := DispatchClaim{Request: claimed, Attempt: attempt}
	decision := state.TransactionDecision{
		ReadSet: state.TransactionReadSet{Owners: []string{request.ModuleID}}, Events: events, Result: claim,
		Candidate: state.CandidateOptions{Subject: "state(dispatch): claim request", Provenance: state.GitProvenance{Operation: dispatchClaimOperation, Event: operationID, Module: request.ModuleID, Request: request.ID, Attempt: attempt.ID, Fence: fmt.Sprint(fence)}},
	}
	return decision, claim, nil
}

func resolveDispatchClaim(snapshot state.Snapshot, operationID string) (DispatchClaim, bool, error) {
	if !eventApplied(snapshot, operationID) {
		return DispatchClaim{}, false, nil
	}
	for _, record := range snapshot.Records {
		if record.Kind != "attempt" {
			continue
		}
		var attempt Attempt
		if err := json.Unmarshal(record.Data, &attempt); err != nil {
			return DispatchClaim{}, false, fmt.Errorf("decode durable dispatch attempt: %w", err)
		}
		if attempt.OperationID != operationID {
			continue
		}
		requestRecord, exists := snapshot.Records[stateRequestPath+attempt.RequestID+".json"]
		if !exists || requestRecord.Kind != "request" {
			return DispatchClaim{}, false, fmt.Errorf("durable dispatch attempt has no matching request")
		}
		var request Request
		if err := json.Unmarshal(requestRecord.Data, &request); err != nil {
			return DispatchClaim{}, false, fmt.Errorf("decode durable dispatch request: %w", err)
		}
		return DispatchClaim{Request: request, Attempt: attempt}, true, nil
	}
	return DispatchClaim{}, false, nil
}

func eventApplied(snapshot state.Snapshot, eventID string) bool {
	if _, exists := snapshot.AppliedEvents[eventID]; exists {
		return true
	}
	for _, aggregate := range snapshot.Aggregates {
		if _, exists := aggregate.AppliedEvents[eventID]; exists {
			return true
		}
	}
	return false
}

// BindActionsRun binds one actual workflow run to a pre-dispatch attempt. A
// retry of the same binding is idempotent; a second run cannot take ownership.
func BindActionsRun(ctx context.Context, coordinator state.TransactionCoordinator, attemptID, runID string, runAttempt uint64, boundAt time.Time) (RecoveryResult, error) {
	return bindActionsRunWithCoordinator(ctx, coordinator, attemptID, runID, runAttempt, boundAt, false)
}

// BindActionsRunWithLifecycle atomically records acquisition progress for an
// explicitly approved caller without enabling new record production by default.
func BindActionsRunWithLifecycle(ctx context.Context, coordinator state.TransactionCoordinator, attemptID, runID string, runAttempt uint64, boundAt time.Time) (RecoveryResult, error) {
	return bindActionsRunWithCoordinator(ctx, coordinator, attemptID, runID, runAttempt, boundAt, true)
}

func bindActionsRunWithCoordinator(ctx context.Context, coordinator state.TransactionCoordinator, attemptID, runID string, runAttempt uint64, boundAt time.Time, lifecycle bool) (RecoveryResult, error) {
	if coordinator == nil || strings.TrimSpace(runID) == "" || runAttempt == 0 || boundAt.IsZero() {
		return RecoveryResult{}, fmt.Errorf("Actions run ID, attempt number, and bind time are required")
	}
	operationID, err := identity.ID("bind-operation", 1, struct {
		AttemptID  string    `json:"attempt_id"`
		RunID      string    `json:"run_id"`
		RunAttempt uint64    `json:"run_attempt"`
		BoundAt    time.Time `json:"bound_at"`
	}{attemptID, runID, runAttempt, boundAt.UTC()})
	if err != nil {
		return RecoveryResult{}, err
	}
	result, err := coordinator.RunTransaction(ctx, operationID, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
		attempt, exists, loadErr := loadAttempt(snapshot, attemptID)
		if loadErr != nil || !exists {
			if loadErr != nil {
				return state.TransactionDecision{}, loadErr
			}
			return state.TransactionDecision{}, ErrAttemptNotFound
		}
		request, requestErr := loadRequest(snapshot, attempt.RequestID)
		if requestErr != nil {
			return state.TransactionDecision{}, requestErr
		}
		if (attempt.Status == AttemptDispatched || attempt.Status == AttemptCompleted) && attempt.RunID == runID && attempt.RunAttempt == runAttempt {
			return state.TransactionDecision{Noop: true, Result: RecoveryResult{Attempt: attempt, Request: request}}, nil
		}
		if attempt.Status == AttemptDispatched {
			return state.TransactionDecision{}, ErrAttemptConflict
		}
		if attempt.Status != AttemptClaimed || request.Status != StatusClaimed || request.Work.AttemptID != attempt.ID || request.Work.Fence != attempt.Fence || request.Work.Lease == nil || request.Work.Lease.Owner != attempt.ID {
			return state.TransactionDecision{}, ErrAttemptOwnership
		}
		updatedAttempt := attempt
		updatedAttempt.Status, updatedAttempt.RunID, updatedAttempt.RunAttempt, updatedAttempt.BoundAt = AttemptDispatched, runID, runAttempt, boundAt.UTC()
		updatedRequest := cloneRequest(request)
		updatedRequest.Status, updatedRequest.OwnerRunID, updatedRequest.UpdatedAt = StatusRunning, runID, boundAt.UTC()
		updatedRequest.Work.Lease.Owner = runID
		if validateErr := updatedRequest.Validate(); validateErr != nil {
			return state.TransactionDecision{}, validateErr
		}
		requestEvent, eventErr := stateRecordEvent(stateRequestPath+updatedRequest.ID+".json", "request", updatedRequest, "", 0)
		if eventErr != nil {
			return state.TransactionDecision{}, eventErr
		}
		attemptEvent, eventErr := stateRecordEvent(dispatchAttemptPath(updatedAttempt), "attempt", updatedAttempt, "", 0)
		if eventErr != nil {
			return state.TransactionDecision{}, eventErr
		}
		events := []state.Event{requestEvent, attemptEvent}
		if lifecycle {
			events, eventErr = AcquisitionLifecycleEvents(snapshot, updatedRequest, events, boundAt)
			if eventErr != nil {
				return state.TransactionDecision{}, eventErr
			}
		}
		events[0].ID = operationID
		return state.TransactionDecision{Events: events, Result: RecoveryResult{Attempt: updatedAttempt, Request: updatedRequest}, Candidate: state.CandidateOptions{Subject: "state(dispatch): bind Actions run", Provenance: state.GitProvenance{Operation: dispatchClaimOperation, Event: operationID, Module: request.ModuleID, Request: request.ID, Attempt: attempt.ID, Fence: fmt.Sprint(attempt.Fence)}}}, nil
	}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
		attempt, exists, loadErr := loadAttempt(snapshot, attemptID)
		if loadErr != nil || !exists {
			return state.TransactionResolution{}, loadErr
		}
		request, requestErr := loadRequest(snapshot, attempt.RequestID)
		return state.TransactionResolution{Found: requestErr == nil, Value: RecoveryResult{Attempt: attempt, Request: request}}, requestErr
	})
	if err != nil {
		return RecoveryResult{}, err
	}
	resolved, ok := result.Value.(RecoveryResult)
	if !ok {
		return RecoveryResult{}, fmt.Errorf("committed Actions binding result has an invalid type")
	}
	resolved.Head = result.Head
	resolved.Changed = result.Changed
	return resolved, nil
}

// Heartbeat extends an active attempt lease only for its current bound run and
// fence. Expired or stale owners cannot revive themselves.
func Heartbeat(ctx context.Context, coordinator state.TransactionCoordinator, attemptID, runID string, fence uint64, now, expiresAt time.Time) (RecoveryResult, error) {
	if coordinator == nil || now.IsZero() || !expiresAt.After(now) {
		return RecoveryResult{}, fmt.Errorf("heartbeat time and future lease expiry are required")
	}
	operationID, err := identity.ID("heartbeat-operation", 1, struct {
		AttemptID string    `json:"attempt_id"`
		RunID     string    `json:"run_id"`
		Fence     uint64    `json:"fence"`
		At        time.Time `json:"at"`
		ExpiresAt time.Time `json:"expires_at"`
	}{attemptID, runID, fence, now.UTC(), expiresAt.UTC()})
	if err != nil {
		return RecoveryResult{}, err
	}
	result, err := coordinator.RunTransaction(ctx, operationID, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
		attempt, exists, loadErr := loadAttempt(snapshot, attemptID)
		if loadErr != nil || !exists {
			if loadErr != nil {
				return state.TransactionDecision{}, loadErr
			}
			return state.TransactionDecision{}, ErrAttemptNotFound
		}
		request, loadErr := loadRequest(snapshot, attempt.RequestID)
		if loadErr != nil {
			return state.TransactionDecision{}, loadErr
		}
		if attempt.Status != AttemptDispatched || attempt.RunID != runID || attempt.Fence != fence || request.Status != StatusRunning || request.OwnerRunID != runID || request.Work.AttemptID != attempt.ID || request.Work.Lease == nil || request.Work.Lease.Fence != fence {
			return state.TransactionDecision{}, ErrAttemptOwnership
		}
		if !attempt.LeaseExpiresAt.After(now) {
			return state.TransactionDecision{}, ErrLeaseNotExpired
		}
		if !expiresAt.After(attempt.LeaseExpiresAt) || (!attempt.LastHeartbeatAt.IsZero() && !now.After(attempt.LastHeartbeatAt)) {
			return state.TransactionDecision{}, fmt.Errorf("heartbeat must monotonically extend lease and heartbeat time")
		}
		updatedAttempt := attempt
		updatedAttempt.LeaseExpiresAt, updatedAttempt.LastHeartbeatAt = expiresAt.UTC(), now.UTC()
		updatedRequest := cloneRequest(request)
		updatedRequest.Work.Lease.ExpiresAt, updatedRequest.UpdatedAt = expiresAt.UTC(), now.UTC()
		requestEvent, eventErr := stateRecordEvent(stateRequestPath+updatedRequest.ID+".json", "request", updatedRequest, "", 0)
		if eventErr != nil {
			return state.TransactionDecision{}, eventErr
		}
		attemptEvent, eventErr := stateRecordEvent(dispatchAttemptPath(updatedAttempt), "attempt", updatedAttempt, "", 0)
		if eventErr != nil {
			return state.TransactionDecision{}, eventErr
		}
		requestEvent.ID = operationID
		return state.TransactionDecision{Events: []state.Event{requestEvent, attemptEvent}, Result: RecoveryResult{Attempt: updatedAttempt, Request: updatedRequest}, Candidate: state.CandidateOptions{Subject: "state(dispatch): extend lease", Provenance: state.GitProvenance{Operation: dispatchClaimOperation, Event: operationID, Module: attempt.ModuleID, Request: attempt.RequestID, Attempt: attempt.ID, Fence: fmt.Sprint(fence)}}}, nil
	}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
		attempt, exists, loadErr := loadAttempt(snapshot, attemptID)
		if loadErr != nil || !exists {
			return state.TransactionResolution{}, loadErr
		}
		request, loadErr := loadRequest(snapshot, attempt.RequestID)
		return state.TransactionResolution{Found: loadErr == nil, Value: RecoveryResult{Attempt: attempt, Request: request}}, loadErr
	})
	if err != nil {
		return RecoveryResult{}, err
	}
	resolved, ok := result.Value.(RecoveryResult)
	if !ok {
		return RecoveryResult{}, fmt.Errorf("committed heartbeat result has an invalid type")
	}
	resolved.Head, resolved.Changed = result.Head, result.Changed
	return resolved, nil
}

// RecoverAttempt acts only on explicit Actions observations. Unknown lookup
// preserves the owner; confirmed absence can release an expired attempt.
func RecoverAttempt(ctx context.Context, coordinator state.TransactionCoordinator, attemptID string, now time.Time, observation ActionsObservation) (RecoveryResult, error) {
	if coordinator == nil || now.IsZero() || !validRunObservation(observation.State) {
		return RecoveryResult{}, fmt.Errorf("recovery time and valid Actions state are required")
	}
	operationID, err := identity.ID("recovery-operation", 1, struct {
		AttemptID   string             `json:"attempt_id"`
		Now         time.Time          `json:"now"`
		Observation ActionsObservation `json:"observation"`
	}{attemptID, now.UTC(), observation})
	if err != nil {
		return RecoveryResult{}, err
	}
	result, err := coordinator.RunTransaction(ctx, operationID, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
		attempt, exists, loadErr := loadAttempt(snapshot, attemptID)
		if loadErr != nil || !exists {
			if loadErr != nil {
				return state.TransactionDecision{}, loadErr
			}
			return state.TransactionDecision{}, ErrAttemptNotFound
		}
		request, loadErr := loadRequest(snapshot, attempt.RequestID)
		if loadErr != nil {
			return state.TransactionDecision{}, loadErr
		}
		if observation.State == RunUnknown || (observation.State == RunActive && attempt.Status == AttemptDispatched && (observation.RunID == "" || observation.RunID == attempt.RunID)) || (observation.State == RunActive && attempt.Status == AttemptClaimed && observation.RunID == "") {
			return state.TransactionDecision{Noop: true, Result: RecoveryResult{Attempt: attempt, Request: request}}, nil
		}
		if observation.State == RunActive && attempt.Status == AttemptClaimed {
			if observation.RunID == "" || observation.RunAttempt == 0 {
				return state.TransactionDecision{}, ErrAttemptOwnership
			}
			return state.TransactionDecision{}, errRecoveryBindRequired
		}
		if observation.State == RunCompleted && attempt.Status == AttemptClaimed {
			if observation.RunID == "" || observation.RunAttempt == 0 {
				return state.TransactionDecision{}, ErrAttemptOwnership
			}
			return state.TransactionDecision{}, errRecoveryBindThenComplete
		}
		if observation.State == RunActive {
			return state.TransactionDecision{}, ErrAttemptConflict
		}
		updatedAttempt, updatedRequest, transitionErr := reduceRecoveryObservation(attempt, request, now, observation)
		if transitionErr != nil {
			return state.TransactionDecision{}, transitionErr
		}
		return recoveryDecision(updatedAttempt, updatedRequest, operationID)
	}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
		attempt, exists, loadErr := loadAttempt(snapshot, attemptID)
		if loadErr != nil || !exists {
			return state.TransactionResolution{}, loadErr
		}
		request, loadErr := loadRequest(snapshot, attempt.RequestID)
		return state.TransactionResolution{Found: loadErr == nil, Value: RecoveryResult{Attempt: attempt, Request: request}}, loadErr
	})
	if err != nil {
		if errors.Is(err, errRecoveryBindRequired) {
			return BindActionsRun(ctx, coordinator, attemptID, observation.RunID, observation.RunAttempt, now)
		}
		if errors.Is(err, errRecoveryBindThenComplete) {
			if _, bindErr := BindActionsRun(ctx, coordinator, attemptID, observation.RunID, observation.RunAttempt, now); bindErr != nil {
				return RecoveryResult{}, bindErr
			}
			return RecoverAttempt(ctx, coordinator, attemptID, now, observation)
		}
		return RecoveryResult{}, err
	}
	resolved, ok := result.Value.(RecoveryResult)
	if !ok {
		return RecoveryResult{}, fmt.Errorf("committed recovery result has an invalid type")
	}
	resolved.Head, resolved.Changed = result.Head, result.Changed
	return resolved, nil
}

var errRecoveryBindRequired = errors.New("recovery requires a separate run-binding transaction")
var errRecoveryBindThenComplete = errors.New("completed recovery requires a separate run-binding transaction")

func reduceRecoveryObservation(attempt Attempt, request Request, now time.Time, observation ActionsObservation) (Attempt, Request, error) {
	if observation.State == RunAbsent {
		if attempt.Status == AttemptReclaimed {
			return attempt, request, nil
		}
		if !attempt.LeaseExpiresAt.Before(now) || !claimStillMatches(attempt, request) {
			return Attempt{}, Request{}, ErrLeaseNotExpired
		}
		attempt.Status, attempt.RecoveredAt = AttemptReclaimed, now.UTC()
		return attempt, releaseRequestForRetry(request, now), nil
	}
	if attempt.Status == AttemptCompleted {
		return attempt, request, nil
	}
	if attempt.Status != AttemptDispatched || observation.RunID != attempt.RunID || observation.RunAttempt != attempt.RunAttempt {
		return Attempt{}, Request{}, ErrAttemptOwnership
	}
	attempt.Status, attempt.CompletedAt = AttemptCompleted, now.UTC()
	if request.Status == StatusRunning && request.Work.Completion == nil {
		request = releaseRequestForRetry(request, now)
	}
	return attempt, request, nil
}

func recoveryDecision(attempt Attempt, request Request, operationID string) (state.TransactionDecision, error) {
	requestEvent, err := stateRecordEvent(stateRequestPath+request.ID+".json", "request", request, "", 0)
	if err != nil {
		return state.TransactionDecision{}, err
	}
	attemptEvent, err := stateRecordEvent(dispatchAttemptPath(attempt), "attempt", attempt, "", 0)
	if err != nil {
		return state.TransactionDecision{}, err
	}
	requestEvent.ID = operationID
	return state.TransactionDecision{Events: []state.Event{requestEvent, attemptEvent}, Result: RecoveryResult{Attempt: attempt, Request: request}, Candidate: state.CandidateOptions{Subject: "state(dispatch): recover attempt", Provenance: state.GitProvenance{Operation: dispatchClaimOperation, Event: operationID, Module: request.ModuleID, Request: request.ID, Attempt: attempt.ID, Fence: fmt.Sprint(attempt.Fence)}}}, nil
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

func loadRequest(snapshot state.Snapshot, requestID string) (Request, error) {
	record, exists := snapshot.Records[stateRequestPath+requestID+".json"]
	if !exists || record.Kind != "request" {
		return Request{}, fmt.Errorf("request record for %s is missing", requestID)
	}
	var request Request
	if err := json.Unmarshal(record.Data, &request); err != nil {
		return Request{}, fmt.Errorf("decode request %s: %w", requestID, err)
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
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
