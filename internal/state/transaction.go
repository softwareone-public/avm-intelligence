package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

const transactionAttempts = 3

const (
	transactionRetryBase   = 100 * time.Millisecond
	transactionRetryJitter = 200 * time.Millisecond
)

var ErrTransactionConflict = errors.New("state transaction conflicted with concurrent publication")

// TransactionTiming injects decision-loop time and jitter for deterministic tests.
type TransactionTiming struct {
	Now    func() time.Time
	Sleep  func(context.Context, time.Duration) error
	Jitter func(time.Duration) time.Duration
}

// DecisionCoordinator owns the single bounded whole-decision retry loop.
// It requires the Git publication contract and never merges or replays a stale candidate.
type DecisionCoordinator struct {
	Backend GitBackend
	Timing  TransactionTiming
	Now     time.Time
}

func (coordinator DecisionCoordinator) Read(ctx context.Context) (string, Snapshot, error) {
	return coordinator.Backend.Read(ctx)
}

// RunTransaction fetches a fresh snapshot for each total attempt, reruns the pure
// decision, and reconciles uncertain publication before returning or retrying.
func (coordinator DecisionCoordinator) RunTransaction(ctx context.Context, operationID string, decide TransactionDecisionFunc, resolve TransactionResultResolver) (TransactionResult, error) {
	return coordinator.runTransaction(ctx, operationID, nil, decide, resolve)
}

// RunOperationTransaction persists an immutable module-owned receipt and
// verifies its complete historical event proof before replaying the result.
// expectedEvents are fixed caller intent and must not be recomputed from
// advanced mutable records during replay.
func (coordinator DecisionCoordinator) RunOperationTransaction(ctx context.Context, moduleID, operationID string, expectedEvents []Event, decide TransactionDecisionFunc, resolve TransactionResultResolver) (TransactionResult, error) {
	if _, err := identity.OperationalModuleRoot(moduleID); err != nil {
		return TransactionResult{}, err
	}
	if expectedEvents == nil {
		return TransactionResult{}, fmt.Errorf("expected operation write set is required")
	}
	return coordinator.runTransaction(ctx, operationID, &operationContract{moduleID: moduleID, expectedEvents: expectedEvents}, decide, resolve)
}

type operationContract struct {
	moduleID       string
	expectedEvents []Event
}

func (coordinator DecisionCoordinator) runTransaction(ctx context.Context, operationID string, contract *operationContract, decide TransactionDecisionFunc, resolve TransactionResultResolver) (TransactionResult, error) {
	if err := validateCoordinator(coordinator, operationID, decide, resolve); err != nil {
		return TransactionResult{}, err
	}
	for attempt := 1; attempt <= transactionAttempts; attempt++ {
		result, retry, err := coordinator.runAttempt(ctx, operationID, attempt, contract, decide, resolve)
		if err != nil {
			return TransactionResult{}, err
		}
		if !retry {
			return result, nil
		}
		if attempt == transactionAttempts {
			return TransactionResult{}, ErrTransactionConflict
		}
		if err = coordinator.wait(ctx); err != nil {
			return TransactionResult{}, err
		}
	}
	return TransactionResult{}, ErrTransactionConflict
}

func validateCoordinator(coordinator DecisionCoordinator, operationID string, decide TransactionDecisionFunc, resolve TransactionResultResolver) error {
	if coordinator.Backend == nil || operationID == "" || decide == nil || resolve == nil {
		return fmt.Errorf("transaction backend, stable operation ID, decision, and result resolver are required")
	}
	return nil
}

func (coordinator DecisionCoordinator) runAttempt(ctx context.Context, operationID string, attempt int, contract *operationContract, decide TransactionDecisionFunc, resolve TransactionResultResolver) (TransactionResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return TransactionResult{}, false, err
	}
	head, snapshot, err := coordinator.Backend.Read(ctx)
	if err != nil {
		return TransactionResult{}, false, fmt.Errorf("read state snapshot: %w", err)
	}
	if head == "" {
		return TransactionResult{}, false, fmt.Errorf("state branch head is required")
	}
	snapshot = normalizeSnapshot(snapshot)
	if err = snapshot.Validate(); err != nil {
		return TransactionResult{}, false, fmt.Errorf("validate state snapshot at %s: %w", head, err)
	}
	replayed, found, err := coordinator.tryResolveDurableReplay(head, snapshot, operationID, contract, resolve)
	if err != nil {
		return TransactionResult{}, false, err
	}
	if found {
		return replayed, false, nil
	}
	if contract != nil {
		operationDecide := decide
		decide = func(snapshot Snapshot) (TransactionDecision, error) {
			decision, err := operationDecide(snapshot)
			if err != nil {
				return TransactionDecision{}, err
			}
			decision, err = prepareOperationDecision(operationID, contract, decision)
			if err != nil {
				return TransactionDecision{}, err
			}
			decision.operation = contract
			return decision, nil
		}
	}
	return coordinator.continueAttempt(ctx, operationID, attempt, head, snapshot, decide, resolve)
}

func (coordinator DecisionCoordinator) continueAttempt(ctx context.Context, operationID string, attempt int, head string, snapshot Snapshot, decide TransactionDecisionFunc, resolve TransactionResultResolver) (TransactionResult, bool, error) {
	decision, err := decide(cloneSnapshot(snapshot))
	if err != nil {
		return TransactionResult{}, false, fmt.Errorf("decide state transaction: %w", err)
	}
	return coordinator.prepareAndPublish(ctx, operationID, attempt, head, snapshot, decision, resolve)
}

func (coordinator DecisionCoordinator) tryResolveDurableReplay(head string, snapshot Snapshot, operationID string, contract *operationContract, resolve TransactionResultResolver) (TransactionResult, bool, error) {
	if !durableOperationPresent(snapshot, operationID) {
		return TransactionResult{}, false, nil
	}
	if contract != nil {
		receipt, exists := operationReceiptRecord(snapshot, contract.moduleID, operationID)
		if !exists {
			return TransactionResult{}, false, ErrTransactionIdentityConflict
		}
		if err := verifyOperationReceipt(snapshot, contract.moduleID, operationID, contract.expectedEvents); err != nil {
			return TransactionResult{}, false, err
		}
		return TransactionResult{Head: head, Value: append(json.RawMessage(nil), receipt.Result...)}, true, nil
	}
	result, retry, err := coordinator.resolveDurableReplay(head, snapshot, operationID, resolve)
	return result, !retry, err
}

func (coordinator DecisionCoordinator) resolveDurableReplay(head string, snapshot Snapshot, operationID string, resolve TransactionResultResolver) (TransactionResult, bool, error) {
	resolution, err := resolveDurableOperation(snapshot, operationID, resolve)
	if err != nil {
		return TransactionResult{}, false, err
	}
	return TransactionResult{Head: head, Value: resolution.Value, Changed: false}, false, nil
}

func (coordinator DecisionCoordinator) prepareAndPublish(ctx context.Context, operationID string, attempt int, head string, snapshot Snapshot, decision TransactionDecision, resolve TransactionResultResolver) (TransactionResult, bool, error) {
	if !decision.Noop {
		if err := validateOperationIdentity(operationID, decision); err != nil {
			return TransactionResult{}, false, err
		}
	}
	if decision.Noop {
		if len(decision.Events) != 0 {
			return TransactionResult{}, false, fmt.Errorf("no-op transaction decision must not contain events")
		}
		return TransactionResult{Head: head, Value: decision.Result, Changed: false}, false, nil
	}
	if len(decision.Events) == 0 {
		if decision.Noop {
			return TransactionResult{Head: head, Value: decision.Result, Changed: false}, false, nil
		}
		return TransactionResult{}, false, fmt.Errorf("mutating transaction decision must contain events")
	}
	if decision.Noop {
		return TransactionResult{}, false, fmt.Errorf("no-op transaction decision must not contain events")
	}
	return coordinator.reduceAndPublish(ctx, operationID, attempt, head, snapshot, decision, resolve)
}

func (coordinator DecisionCoordinator) reduceAndPublish(ctx context.Context, operationID string, attempt int, head string, snapshot Snapshot, decision TransactionDecision, resolve TransactionResultResolver) (TransactionResult, bool, error) {
	candidateSnapshot, err := reduceTransactionEvents(snapshot, decision.Events)
	if err != nil {
		return TransactionResult{}, false, err
	}
	if reflect.DeepEqual(candidateSnapshot, snapshot) {
		return TransactionResult{Head: head, Value: decision.Result, Changed: false}, false, nil
	}
	decision.Candidate.ParentHead = head
	decision.Candidate.Snapshot = candidateSnapshot
	if decision.Candidate.Now.IsZero() {
		decision.Candidate.Now = coordinator.Now
	}
	candidate, err := coordinator.Backend.BuildCandidate(ctx, decision.Candidate)
	if err != nil {
		return TransactionResult{}, false, err
	}
	if candidate.CandidateID == "" {
		return TransactionResult{Head: head, Value: decision.Result, Changed: false}, false, nil
	}
	return coordinator.publishDecision(ctx, operationID, attempt, head, decision, candidate, resolve)
}

func validateOperationIdentity(operationID string, decision TransactionDecision) error {
	for _, event := range decision.Events {
		if event.ID == operationID {
			return nil
		}
	}
	return fmt.Errorf("mutating transaction must contain its stable operation ID %q", operationID)
}

func prepareOperationDecision(operationID string, contract *operationContract, decision TransactionDecision) (TransactionDecision, error) {
	if decision.Noop {
		return TransactionDecision{}, fmt.Errorf("operation receipt transaction cannot be a no-op")
	}
	if !sameEventSet(decision.Events, contract.expectedEvents) {
		return TransactionDecision{}, ErrTransactionIdentityConflict
	}
	result, err := json.Marshal(decision.Result)
	if err != nil {
		return TransactionDecision{}, fmt.Errorf("encode immutable operation result: %w", err)
	}
	receiptEvent, err := NewOperationReceipt(contract.moduleID, operationID, result, contract.expectedEvents)
	if err != nil {
		return TransactionDecision{}, err
	}
	for _, event := range contract.expectedEvents {
		owner, _, ownerErr := resolveRecordOwner(event.RecordPath, event.Record)
		if ownerErr != nil || owner != mustModuleOwner(contract.moduleID) {
			return TransactionDecision{}, fmt.Errorf("operation write set is not exclusively owned by its module")
		}
	}
	decision.Events = append(append([]Event(nil), contract.expectedEvents...), receiptEvent)
	return decision, nil
}

func sameEventSet(left, right []Event) bool {
	if len(left) != len(right) {
		return false
	}
	leftDigests := make(map[string]string, len(left))
	for _, event := range left {
		digest, err := transactionEventDigest(event)
		if err != nil {
			return false
		}
		if _, exists := leftDigests[event.ID]; exists {
			return false
		}
		leftDigests[event.ID] = digest
	}
	for _, event := range right {
		digest, err := transactionEventDigest(event)
		if err != nil || leftDigests[event.ID] != digest {
			return false
		}
		delete(leftDigests, event.ID)
	}
	return len(leftDigests) == 0
}

func mustModuleOwner(moduleID string) string {
	owner, _ := identity.OperationalModuleRoot(moduleID)
	return owner
}

func operationReceiptRecord(snapshot Snapshot, moduleID, operationID string) (OperationReceipt, bool) {
	logicalPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		return OperationReceipt{}, false
	}
	record, exists := snapshot.Records[logicalPath]
	if !exists || record.Kind != "operation_receipt" || record.SchemaVersion != 1 {
		return OperationReceipt{}, false
	}
	var receipt OperationReceipt
	if err = json.Unmarshal(record.Data, &receipt); err != nil {
		return OperationReceipt{}, false
	}
	return receipt, true
}

func verifyOperationReceipt(snapshot Snapshot, moduleID, operationID string, expectedEvents []Event) error {
	logicalPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		return err
	}
	receipt, exists := operationReceiptRecord(snapshot, moduleID, operationID)
	if !exists || receipt.Validate(logicalPath) != nil {
		return ErrTransactionIdentityConflict
	}
	if err := verifyReceiptEvent(snapshot, operationID, logicalPath); err != nil {
		return err
	}
	return verifyReceiptWriteSet(snapshot, receipt, expectedEvents)
}

func verifyReceiptEvent(snapshot Snapshot, operationID, logicalPath string) error {
	receiptEvent := Event{ID: operationID, RecordPath: logicalPath, Record: snapshot.Records[logicalPath]}
	receiptDigest, err := transactionEventDigest(receiptEvent)
	if err != nil {
		return ErrTransactionIdentityConflict
	}
	if historical, applied := appliedEventDigest(snapshot, operationID); !applied || historical != receiptDigest {
		return ErrTransactionIdentityConflict
	}
	return nil
}

func verifyReceiptWriteSet(snapshot Snapshot, receipt OperationReceipt, expectedEvents []Event) error {
	want := make(map[string]OperationWriteSetEntry, len(expectedEvents))
	for _, event := range expectedEvents {
		digest, digestErr := transactionEventDigest(event)
		if digestErr != nil {
			return ErrTransactionIdentityConflict
		}
		want[event.ID] = OperationWriteSetEntry{EventID: event.ID, RecordPath: event.RecordPath, RecordKind: event.Record.Kind, Digest: digest}
	}
	if len(want) != len(receipt.WriteSet) {
		return ErrTransactionIdentityConflict
	}
	for _, entry := range receipt.WriteSet {
		expected, found := want[entry.EventID]
		if !found || expected != entry {
			return ErrTransactionIdentityConflict
		}
		historical, applied := appliedEventDigest(snapshot, entry.EventID)
		if !applied || historical != entry.Digest {
			return ErrTransactionIdentityConflict
		}
		delete(want, entry.EventID)
	}
	if len(want) != 0 {
		return ErrTransactionIdentityConflict
	}
	return nil
}

func transactionEventDigest(event Event) (string, error) {
	updated, _, err := applyEvent(NewSnapshot(), event)
	if err != nil {
		return "", err
	}
	digest, exists := updated.AppliedEvents[event.ID]
	if !exists {
		for _, aggregate := range updated.Aggregates {
			if aggregateDigest, found := aggregate.AppliedEvents[event.ID]; found {
				digest = aggregateDigest
				break
			}
		}
	}
	if digest == "" {
		return "", ErrTransactionIdentityConflict
	}
	return digest, nil
}

func appliedEventDigest(snapshot Snapshot, eventID string) (string, bool) {
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

func (coordinator DecisionCoordinator) publishDecision(ctx context.Context, operationID string, attempt int, head string, decision TransactionDecision, candidate GitCandidate, resolve TransactionResultResolver) (TransactionResult, bool, error) {
	publication, publishErr := coordinator.Backend.Publish(ctx, candidate)
	switch publication.Kind {
	case PublicationCommitted:
		if publication.CandidateID != candidate.CandidateID || publication.Head == "" || publication.Head == head {
			return TransactionResult{}, false, fmt.Errorf("committed publication did not advance the verified head")
		}
		return TransactionResult{Head: publication.Head, Value: decision.Result, Changed: true}, false, nil
	case PublicationRejected:
		if publishErr != nil && !errors.Is(publishErr, ErrHeadMoved) && !isTypedContention(publishErr) {
			return TransactionResult{}, false, publishErr
		}
		if attempt == transactionAttempts {
			return TransactionResult{}, false, ErrTransactionConflict
		}
		return TransactionResult{}, true, nil
	case PublicationUnknown:
		return coordinator.reconcileUnknown(ctx, operationID, attempt, decision, candidate, resolve, publishErr)
	default:
		return TransactionResult{}, false, fmt.Errorf("Git backend returned an invalid publication outcome")
	}
}

// selectTransactionResult prefers the resolver's reconstructed result. A nil
// result indicates that the resolver recovered only the durable identity.
func selectTransactionResult(resolved, proposed any) any {
	if resolved == nil {
		return proposed
	}
	return resolved
}

func (coordinator DecisionCoordinator) reconcileUnknown(ctx context.Context, operationID string, attempt int, decision TransactionDecision, candidate GitCandidate, resolve TransactionResultResolver, publishErr error) (TransactionResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return unknownTransactionResult(operationID, candidate.CandidateID, err)
	}
	head, snapshot, err := coordinator.Backend.Read(ctx)
	if err != nil {
		return unknownTransactionResult(operationID, candidate.CandidateID, errors.Join(publishErr, err))
	}
	if head == "" {
		return unknownTransactionResult(operationID, candidate.CandidateID, errors.Join(publishErr, fmt.Errorf("state branch head is required")))
	}
	if snapshot.SchemaVersion == 0 && snapshot.Generation == 0 && snapshot.Records == nil {
		snapshot = NewSnapshot()
	}
	if snapshot.AppliedEvents == nil && snapshot.Generation > 0 {
		snapshot.AppliedEvents = map[string]string{}
	}
	if err = snapshot.Validate(); err != nil {
		return unknownTransactionResult(operationID, candidate.CandidateID, errors.Join(publishErr, err))
	}
	if _, exists := appliedEventDigest(snapshot, operationID); exists && decision.operation == nil {
		return coordinator.resolveReconciledOperation(head, snapshot, operationID, decision, candidate, resolve)
	}
	if result, found, reconcileErr := coordinator.tryResolveUnknownOperation(head, snapshot, operationID, decision, candidate, resolve); found || reconcileErr != nil {
		return result, false, reconcileErr
	}
	accepted, proofErr := coordinator.proveDescendant(ctx, candidate.CandidateID, head)
	if proofErr != nil {
		return unknownTransactionResult(operationID, candidate.CandidateID, errors.Join(publishErr, proofErr))
	}
	if accepted {
		return unknownTransactionResult(operationID, candidate.CandidateID, publishErr)
	}
	return coordinator.retryIfParentAdvanced(ctx, operationID, attempt, candidate, head, publishErr)
}

func (coordinator DecisionCoordinator) tryResolveUnknownOperation(head string, snapshot Snapshot, operationID string, decision TransactionDecision, candidate GitCandidate, resolve TransactionResultResolver) (TransactionResult, bool, error) {
	if decision.operation != nil {
		receipt, exists := operationReceiptRecord(snapshot, decision.operation.moduleID, operationID)
		if exists {
			if err := verifyOperationReceipt(snapshot, decision.operation.moduleID, operationID, decision.operation.expectedEvents); err != nil {
				return TransactionResult{}, true, err
			}
			return TransactionResult{Head: head, Value: append(json.RawMessage(nil), receipt.Result...), Changed: head != candidate.ParentHead}, true, nil
		}
	}
	if durableOperationPresent(snapshot, operationID) {
		return coordinator.resolveReconciledOperation(head, snapshot, operationID, decision, candidate, resolve)
	}
	return TransactionResult{}, false, nil
}

func unknownTransactionResult(operationID, candidateID string, cause error) (TransactionResult, bool, error) {
	return TransactionResult{}, false, &TransactionOutcomeUnknownError{OperationID: operationID, CandidateID: candidateID, Cause: cause}
}

func (coordinator DecisionCoordinator) resolveReconciledOperation(head string, snapshot Snapshot, operationID string, decision TransactionDecision, candidate GitCandidate, resolve TransactionResultResolver) (TransactionResult, bool, error) {
	value, err := resolveDurableResult(snapshot, operationID, decision.Result, resolve)
	if err != nil {
		return TransactionResult{}, false, err
	}
	return TransactionResult{Head: head, Value: value, Changed: head != candidate.ParentHead}, false, nil
}

func (coordinator DecisionCoordinator) retryIfParentAdvanced(ctx context.Context, operationID string, attempt int, candidate GitCandidate, head string, publishErr error) (TransactionResult, bool, error) {
	advanced, err := coordinator.proveDescendant(ctx, candidate.ParentHead, head)
	if err != nil {
		return unknownTransactionResult(operationID, candidate.CandidateID, errors.Join(publishErr, err))
	}
	if !advanced {
		return unknownTransactionResult(operationID, candidate.CandidateID, publishErr)
	}
	if attempt == transactionAttempts {
		return TransactionResult{}, false, ErrTransactionConflict
	}
	return TransactionResult{}, true, nil
}

func resolveDurableResult(snapshot Snapshot, operationID string, proposed any, resolve TransactionResultResolver) (any, error) {
	resolution, err := resolveDurableOperation(snapshot, operationID, resolve)
	if err != nil {
		return nil, err
	}
	if !resolution.Found {
		return nil, fmt.Errorf("durable operation is not present")
	}
	return selectTransactionResult(resolution.Value, proposed), nil
}

func (coordinator DecisionCoordinator) proveDescendant(ctx context.Context, ancestor, descendant string) (bool, error) {
	prover, ok := coordinator.Backend.(interface {
		ProveAncestor(context.Context, string, string) (bool, error)
	})
	if !ok {
		return false, ErrGitObjectProofUnavailable
	}
	return prover.ProveAncestor(ctx, ancestor, descendant)
}

func (coordinator DecisionCoordinator) buildCandidate(ctx context.Context, options CandidateOptions) (GitCandidate, error) {
	return coordinator.Backend.BuildCandidate(ctx, options)
}

func (coordinator DecisionCoordinator) wait(ctx context.Context) error {
	delay := transactionRetryBase
	if coordinator.Timing.Jitter != nil {
		delay += coordinator.Timing.Jitter(transactionRetryJitter)
	} else {
		delay += time.Duration(rand.Int64N(int64(transactionRetryJitter)))
	}
	if coordinator.Timing.Sleep != nil {
		return coordinator.Timing.Sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func resolveDurableOperation(snapshot Snapshot, operationID string, resolve TransactionResultResolver) (TransactionResolution, error) {
	digest, found := snapshot.AppliedEvents[operationID]
	if !found {
		for _, aggregate := range snapshot.Aggregates {
			if value, exists := aggregate.AppliedEvents[operationID]; exists {
				digest = value
				found = true
				break
			}
		}
	}
	if !found {
		return TransactionResolution{}, nil
	}
	if digest == "" {
		return TransactionResolution{}, ErrTransactionIdentityConflict
	}
	value, err := resolve(snapshot)
	if err != nil {
		return TransactionResolution{}, fmt.Errorf("resolve durable transaction result: %w", err)
	}
	if !value.Found {
		return TransactionResolution{}, fmt.Errorf("durable transaction identity has no resolvable result")
	}
	return value, nil
}

func durableOperationPresent(snapshot Snapshot, operationID string) bool {
	if _, found := snapshot.AppliedEvents[operationID]; found {
		return true
	}
	for _, aggregate := range snapshot.Aggregates {
		if _, found := aggregate.AppliedEvents[operationID]; found {
			return true
		}
	}
	return false
}

func reduceTransactionEvents(snapshot Snapshot, events []Event) (Snapshot, error) {
	if len(events) == 0 {
		return Snapshot{}, fmt.Errorf("transaction event set must not be empty")
	}
	next := cloneSnapshot(snapshot)
	for index, event := range events {
		updated, _, err := applyEvent(next, event)
		if err != nil {
			return Snapshot{}, fmt.Errorf("apply transaction event %d: %w", index, err)
		}
		next = updated
	}
	return next, nil
}

func isTypedContention(err error) bool {
	var commandError *GitCommandError
	return errors.As(err, &commandError) && commandError.Category == GitFailureContention
}

var _ TransactionCoordinator = DecisionCoordinator{}
