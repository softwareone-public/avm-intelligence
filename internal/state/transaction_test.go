package state

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDecisionCoordinatorRerunsWholeDecisionAfterContention(t *testing.T) {
	origin := newMemoryBackend(NewSnapshot())
	backend := &coordinatorTestBackend{memoryBackend: origin, publishKinds: []PublicationOutcomeKind{PublicationRejected, PublicationCommitted}}
	backend.onRejected = func() {
		competitor := requestEvent("competing-operation", `{"status":"committed"}`, "", 0)
		if _, _, err := ApplyEvent(context.Background(), origin, competitor); err != nil {
			t.Fatalf("publish competing transaction: %v", err)
		}
	}
	decisionCalls := 0
	waits := 0
	coordinatorNow := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	coordinator := DecisionCoordinator{Backend: backend, Now: coordinatorNow, ReplayValidate: func(Snapshot) error { return nil }, Timing: TransactionTiming{
		Jitter: func(maximum time.Duration) time.Duration { return 50 * time.Millisecond },
		Sleep: func(ctx context.Context, delay time.Duration) error {
			waits++
			if delay != 150*time.Millisecond {
				t.Fatalf("retry wait = %s, want 150ms", delay)
			}
			return ctx.Err()
		},
	}}
	decide := func(snapshot Snapshot) (TransactionDecision, error) {
		decisionCalls++
		assertCompetingWriteVisibility(t, snapshot, decisionCalls)
		return transactionTestDecision(snapshot, "operation-2", decisionCalls), nil
	}
	resolve := func(snapshot Snapshot) (TransactionResolution, error) {
		if _, found := snapshot.AppliedEvents["operation-2"]; !found {
			return TransactionResolution{}, nil
		}
		return TransactionResolution{Found: true, Value: "result-2"}, nil
	}
	result, err := coordinator.RunTransaction(context.Background(), "operation-2", decide, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if decisionCalls != 2 || backend.publishCalls != 2 || waits != 1 || !result.Changed || coordinatorNow.IsZero() {
		t.Fatalf("coordinator result=%+v decisions=%d publishes=%d waits=%d", result, decisionCalls, backend.publishCalls, waits)
	}
	if result.Value != "result-2" {
		t.Fatalf("committed result = %#v, want resolver-independent proposed result", result.Value)
	}
}

func assertCompetingWriteVisibility(t *testing.T, snapshot Snapshot, decision int) {
	t.Helper()
	observed := appliedEventDigestExists(snapshot, "competing-operation")
	if decision == 1 && observed {
		t.Fatal("first decision unexpectedly observed the competing write")
	}
	if decision == 2 && !observed {
		t.Fatal("retry did not read the fresh snapshot after contention")
	}
}

func TestDecisionCoordinatorExhaustionUsesThreeTotalDecisionsAndTwoWaits(t *testing.T) {
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(NewSnapshot()), publishKinds: []PublicationOutcomeKind{PublicationRejected, PublicationRejected, PublicationRejected}}
	waits := 0
	coordinator := DecisionCoordinator{Backend: backend, ReplayValidate: func(Snapshot) error { return nil }, Timing: TransactionTiming{
		Jitter: func(time.Duration) time.Duration { return 0 },
		Sleep:  func(context.Context, time.Duration) error { waits++; return nil },
	}}
	decisions := 0
	_, err := coordinator.RunTransaction(context.Background(), "op-exhaust", func(snapshot Snapshot) (TransactionDecision, error) {
		decisions++
		return transactionTestDecision(snapshot, "op-exhaust", decisions), nil
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		return TransactionResolution{Found: true, Value: "original"}, nil
	})
	if !errors.Is(err, ErrTransactionConflict) || decisions != transactionAttempts || backend.publishCalls != transactionAttempts || waits != transactionAttempts-1 {
		t.Fatalf("exhaustion err=%v decisions=%d publishes=%d waits=%d", err, decisions, backend.publishCalls, waits)
	}
}

func TestDecisionCoordinatorReconcilesUnknownFinalAttempt(t *testing.T) {
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(NewSnapshot()), publishKinds: []PublicationOutcomeKind{PublicationRejected, PublicationRejected, PublicationUnknown}}
	waits := 0
	decisions := 0
	coordinator := DecisionCoordinator{Backend: backend, ReplayValidate: func(Snapshot) error { return nil }, Timing: TransactionTiming{
		Jitter: func(time.Duration) time.Duration { return 0 },
		Sleep:  func(context.Context, time.Duration) error { waits++; return nil },
	}}
	_, err := coordinator.RunTransaction(context.Background(), "op-final-unknown", func(snapshot Snapshot) (TransactionDecision, error) {
		decisions++
		return transactionTestDecision(snapshot, "op-final-unknown", decisions), nil
	}, func(Snapshot) (TransactionResolution, error) { return TransactionResolution{}, nil })
	var unknown *TransactionOutcomeUnknownError
	if !errors.As(err, &unknown) || decisions != transactionAttempts || backend.publishCalls != transactionAttempts || waits != transactionAttempts-1 {
		t.Fatalf("final unknown err=%v decisions=%d publishes=%d waits=%d", err, decisions, backend.publishCalls, waits)
	}
}

func TestDecisionCoordinatorResolvesCommittedOperationOnFinalUnknown(t *testing.T) {
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(NewSnapshot()), publishKinds: []PublicationOutcomeKind{PublicationRejected, PublicationRejected, PublicationUnknown}}
	backend.unknownCommits = true
	waits := 0
	decisions := 0
	result, err := (DecisionCoordinator{Backend: backend, ReplayValidate: func(Snapshot) error { return nil }, Timing: TransactionTiming{
		Jitter: func(time.Duration) time.Duration { return 0 },
		Sleep:  func(context.Context, time.Duration) error { waits++; return nil },
	}}).RunTransaction(context.Background(), "op-final-commit", func(snapshot Snapshot) (TransactionDecision, error) {
		decisions++
		return transactionTestDecision(snapshot, "op-final-commit", decisions), nil
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		if appliedEventDigestExists(snapshot, "op-final-commit") {
			return TransactionResolution{Found: true, Value: "original-final-result"}, nil
		}
		return TransactionResolution{}, nil
	})
	if err != nil || result.Value != "original-final-result" || !result.Changed || decisions != transactionAttempts || backend.publishCalls != transactionAttempts || waits != transactionAttempts-1 || backend.memoryBackend.commitCount != 1 {
		t.Fatalf("final unknown replay result=%+v err=%v decisions=%d publishes=%d waits=%d durable=%d", result, err, decisions, backend.publishCalls, waits, backend.memoryBackend.commitCount)
	}
}

func TestDecisionCoordinatorConfirmedCommitDoesNotReadAgain(t *testing.T) {
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(NewSnapshot())}
	readsBefore := backend.memoryBackend.readCount
	result, err := (DecisionCoordinator{Backend: backend, ReplayValidate: func(Snapshot) error { return nil }}).RunTransaction(context.Background(), "op-confirmed", func(snapshot Snapshot) (TransactionDecision, error) {
		return transactionTestDecision(snapshot, "op-confirmed", 1), nil
	}, func(Snapshot) (TransactionResolution, error) {
		return TransactionResolution{}, nil
	})
	if err != nil || result.Value != "result-1" || backend.memoryBackend.readCount != readsBefore+1 {
		t.Fatalf("confirmed commit=%+v err=%v reads=%d, want one initial read", result, err, backend.memoryBackend.readCount-readsBefore)
	}
}

func TestDecisionCoordinatorRejectsMismatchedOperationID(t *testing.T) {
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(NewSnapshot())}
	_, err := (DecisionCoordinator{Backend: backend, ReplayValidate: func(Snapshot) error { return nil }}).RunTransaction(context.Background(), "stable-operation", func(snapshot Snapshot) (TransactionDecision, error) {
		return transactionTestDecision(snapshot, "different-event", 1), nil
	}, func(Snapshot) (TransactionResolution, error) { return TransactionResolution{}, nil })
	if err == nil || backend.buildCalls != 0 || backend.publishCalls != 0 {
		t.Fatalf("mismatched operation ID err=%v builds=%d publishes=%d", err, backend.buildCalls, backend.publishCalls)
	}
}

func TestDecisionCoordinatorRejectsChangedReplayWithoutValidator(t *testing.T) {
	event := requestEvent("op-missing-validator", `{"status":"queued"}`, "", 0)
	digest, err := transactionEventDigest(event)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := NewSnapshot()
	snapshot.AppliedEvents[event.ID] = digest
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(snapshot)}
	_, err = (DecisionCoordinator{Backend: backend}).RunTransaction(context.Background(), event.ID, func(Snapshot) (TransactionDecision, error) {
		return TransactionDecision{Events: []Event{event}}, nil
	}, func(Snapshot) (TransactionResolution, error) {
		return TransactionResolution{Found: true, Value: "replayed"}, nil
	})
	if err == nil || backend.buildCalls != 0 || backend.publishCalls != 0 {
		t.Fatalf("replay without validator err=%v builds=%d publishes=%d", err, backend.buildCalls, backend.publishCalls)
	}
}

func TestDecisionCoordinatorRejectsMalformedCommittedOutcome(t *testing.T) {
	for _, test := range []struct {
		name           string
		wrongCandidate bool
		invalidHead    bool
	}{
		{name: "candidate identity", wrongCandidate: true},
		{name: "non-advancing head", invalidHead: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(NewSnapshot()), wrongCandidate: test.wrongCandidate, invalidHead: test.invalidHead}
			_, err := (DecisionCoordinator{Backend: backend, ReplayValidate: func(Snapshot) error { return nil }}).RunTransaction(context.Background(), "op-invalid-commit", func(snapshot Snapshot) (TransactionDecision, error) {
				return transactionTestDecision(snapshot, "op-invalid-commit", 1), nil
			}, func(Snapshot) (TransactionResolution, error) { return TransactionResolution{}, nil })
			if err == nil || backend.publishCalls != 1 {
				t.Fatalf("malformed committed outcome err=%v publishes=%d", err, backend.publishCalls)
			}
		})
	}
}

func TestDecisionCoordinatorResolvesDurableReplayBeforeBuildingCandidate(t *testing.T) {
	snapshot := NewSnapshot()
	snapshot.AppliedEvents["op-replay"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(snapshot)}
	decisions := 0
	replayEvent := requestEvent("op-replay", `{"status":"queued"}`, "", 0)
	digest, err := transactionEventDigest(replayEvent)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.AppliedEvents["op-replay"] = digest
	backend = &coordinatorTestBackend{memoryBackend: newMemoryBackend(snapshot)}
	result, err := (DecisionCoordinator{Backend: backend, ReplayValidate: replayEventValidator(replayEvent)}).RunTransaction(context.Background(), "op-replay", func(snapshot Snapshot) (TransactionDecision, error) {
		decisions++
		return TransactionDecision{Events: []Event{replayEvent}}, nil
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		return TransactionResolution{Found: true, Value: "original"}, nil
	})
	if err != nil || result.Value != "original" || result.Changed || backend.buildCalls != 0 || backend.publishCalls != 0 || decisions != 0 {
		t.Fatalf("replay result=%+v err=%v decisions=%d builds=%d publishes=%d", result, err, decisions, backend.buildCalls, backend.publishCalls)
	}
}

func TestDecisionCoordinatorResolvesDurableReplayBeforeDecision(t *testing.T) {
	event := requestEvent("op-replay-first", `{"status":"queued"}`, "", 0)
	digest, err := transactionEventDigest(event)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := NewSnapshot()
	snapshot.AppliedEvents[event.ID] = digest
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(snapshot)}
	decisions := 0
	result, err := (DecisionCoordinator{Backend: backend, ReplayValidate: replayEventValidator(event)}).RunTransaction(context.Background(), event.ID, func(Snapshot) (TransactionDecision, error) {
		decisions++
		return TransactionDecision{}, errors.New("replay must not rerun eligibility decision")
	}, func(Snapshot) (TransactionResolution, error) {
		return TransactionResolution{Found: true, Value: "original-result"}, nil
	})
	if err != nil || result.Value != "original-result" || result.Changed || decisions != 0 || backend.buildCalls != 0 || backend.publishCalls != 0 {
		t.Fatalf("replay result=%+v err=%v decisions=%d builds=%d publishes=%d", result, err, decisions, backend.buildCalls, backend.publishCalls)
	}
}

func TestDecisionCoordinatorRejectsChangedDurableReplayPayload(t *testing.T) {
	original := requestEvent("op-changed", `{"status":"queued"}`, "", 0)
	digest, err := transactionEventDigest(original)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := NewSnapshot()
	snapshot.AppliedEvents[original.ID] = digest
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(snapshot)}
	changed := requestEvent(original.ID, `{"status":"failed"}`, "", 0)
	_, err = (DecisionCoordinator{Backend: backend, ReplayValidate: replayEventValidator(changed)}).RunTransaction(context.Background(), original.ID, func(Snapshot) (TransactionDecision, error) {
		return TransactionDecision{Events: []Event{changed}}, nil
	}, func(Snapshot) (TransactionResolution, error) {
		return TransactionResolution{Found: true, Value: "incorrect"}, nil
	})
	if !errors.Is(err, ErrTransactionIdentityConflict) || backend.buildCalls != 0 || backend.publishCalls != 0 {
		t.Fatalf("changed replay err=%v builds=%d publishes=%d", err, backend.buildCalls, backend.publishCalls)
	}
}

func TestDecisionCoordinatorRejectsUnresolvableDurableReplay(t *testing.T) {
	event := requestEvent("op-unresolved", `{"status":"queued"}`, "", 0)
	digest, err := transactionEventDigest(event)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := NewSnapshot()
	snapshot.AppliedEvents[event.ID] = digest
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(snapshot)}
	coordinator := DecisionCoordinator{Backend: backend, ReplayValidate: replayEventValidator(event)}
	_, err = coordinator.RunTransaction(context.Background(), event.ID, func(Snapshot) (TransactionDecision, error) {
		return TransactionDecision{Events: []Event{event}}, nil
	}, func(Snapshot) (TransactionResolution, error) {
		return TransactionResolution{}, nil
	})
	if err == nil || backend.buildCalls != 0 || backend.publishCalls != 0 {
		t.Fatalf("unresolvable durable replay err=%v builds=%d publishes=%d", err, backend.buildCalls, backend.publishCalls)
	}
}

func TestDecisionCoordinatorNoopAndUnknownOutcome(t *testing.T) {
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(NewSnapshot()), publishKinds: []PublicationOutcomeKind{PublicationUnknown}}
	coordinator := DecisionCoordinator{Backend: backend, ReplayValidate: func(Snapshot) error { return nil }}
	noop, err := coordinator.RunTransaction(context.Background(), "op-noop", func(Snapshot) (TransactionDecision, error) {
		return TransactionDecision{Noop: true, Result: "none"}, nil
	}, func(Snapshot) (TransactionResolution, error) { return TransactionResolution{}, nil })
	if err != nil || noop.Changed || noop.Value != "none" || backend.buildCalls != 0 {
		t.Fatalf("noop result=%+v err=%v builds=%d", noop, err, backend.buildCalls)
	}
	_, err = coordinator.RunTransaction(context.Background(), "op-unknown", func(snapshot Snapshot) (TransactionDecision, error) {
		return transactionTestDecision(snapshot, "op-unknown", 1), nil
	}, func(Snapshot) (TransactionResolution, error) { return TransactionResolution{}, nil })
	var unknown *TransactionOutcomeUnknownError
	if !errors.As(err, &unknown) || unknown.OperationID != "op-unknown" || unknown.CandidateID == "" {
		t.Fatalf("unknown publication error = %v", err)
	}
}

func TestDecisionCoordinatorReconcilesUnknownCommittedOperationWithoutDuplicate(t *testing.T) {
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(NewSnapshot()), publishKinds: []PublicationOutcomeKind{PublicationUnknown}}
	backend.unknownCommits = true
	decisions := 0
	event := requestEvent("op-lost-ack", `{"generation":1}`, "", 0)
	result, err := (DecisionCoordinator{Backend: backend, ReplayValidate: replayEventValidator(event)}).RunTransaction(context.Background(), "op-lost-ack", func(snapshot Snapshot) (TransactionDecision, error) {
		decisions++
		return TransactionDecision{Events: []Event{event}, Result: "proposed", Candidate: CandidateOptions{Subject: "state(test): persist decision", Provenance: GitProvenance{Operation: "dispatch.claim"}}}, nil
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		if appliedEventDigestExists(snapshot, "op-lost-ack") {
			return TransactionResolution{Found: true, Value: "original-commit"}, nil
		}
		return TransactionResolution{}, nil
	})
	if err != nil || result.Value != "original-commit" || backend.publishCalls != 1 || backend.memoryBackend.commitCount != 1 || decisions != 1 {
		t.Fatalf("unknown committed replay result=%+v err=%v decisions=%d publishes=%d durable=%d", result, err, decisions, backend.publishCalls, backend.memoryBackend.commitCount)
	}
}

func TestDecisionCoordinatorUnknownReplayRejectsChangedPayload(t *testing.T) {
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(NewSnapshot()), publishKinds: []PublicationOutcomeKind{PublicationUnknown}}
	backend.unknownCommits = true
	original := requestEvent("op-unknown-mismatch", `{"status":"queued"}`, "", 0)
	changed := requestEvent(original.ID, `{"status":"failed"}`, "", 0)
	_, err := (DecisionCoordinator{Backend: backend, ReplayValidate: replayEventValidator(changed)}).RunTransaction(context.Background(), original.ID, func(snapshot Snapshot) (TransactionDecision, error) {
		return TransactionDecision{Events: []Event{original}, Result: "proposed", Candidate: CandidateOptions{Subject: "state(test): persist decision", Provenance: GitProvenance{Operation: "dispatch.claim"}}}, nil
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		return TransactionResolution{Found: true, Value: "incorrect"}, nil
	})
	if !errors.Is(err, ErrTransactionIdentityConflict) || backend.publishCalls != 1 || backend.memoryBackend.commitCount != 1 {
		t.Fatalf("unknown changed-payload replay err=%v publishes=%d durable=%d", err, backend.publishCalls, backend.memoryBackend.commitCount)
	}
}

func TestDecisionCoordinatorCancellationAndDefiniteErrorsDoNotRetry(t *testing.T) {
	backend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(NewSnapshot()), publishKinds: []PublicationOutcomeKind{PublicationRejected}}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	decisions := 0
	_, err := (DecisionCoordinator{Backend: backend, ReplayValidate: func(Snapshot) error { return nil }}).RunTransaction(cancelled, "op-cancel", func(snapshot Snapshot) (TransactionDecision, error) {
		decisions++
		return transactionTestDecision(snapshot, "op-cancel", 1), nil
	}, func(Snapshot) (TransactionResolution, error) { return TransactionResolution{}, nil })
	if !errors.Is(err, context.Canceled) || decisions != 0 || backend.publishCalls != 0 {
		t.Fatalf("cancel error=%v decisions=%d publishes=%d", err, decisions, backend.publishCalls)
	}

	policyBackend := &coordinatorTestBackend{memoryBackend: newMemoryBackend(NewSnapshot()), publishKinds: []PublicationOutcomeKind{PublicationRejected}, publishErr: errors.New("protected policy")}
	policyDecisions := 0
	_, err = (DecisionCoordinator{Backend: policyBackend, ReplayValidate: func(Snapshot) error { return nil }}).RunTransaction(context.Background(), "op-policy", func(snapshot Snapshot) (TransactionDecision, error) {
		policyDecisions++
		return transactionTestDecision(snapshot, "op-policy", 1), nil
	}, func(Snapshot) (TransactionResolution, error) { return TransactionResolution{}, nil })
	if err == nil || policyDecisions != 1 || policyBackend.publishCalls != 1 {
		t.Fatalf("policy failure retried: err=%v decisions=%d publishes=%d", err, policyDecisions, policyBackend.publishCalls)
	}
}

type coordinatorTestBackend struct {
	memoryBackend  *memoryBackend
	publishKinds   []PublicationOutcomeKind
	publishErr     error
	onRejected     func()
	wrongCandidate bool
	invalidHead    bool
	buildCalls     int
	publishCalls   int
	unknownCommits bool
}

func (backend *coordinatorTestBackend) Read(ctx context.Context) (string, Snapshot, error) {
	return backend.memoryBackend.Read(ctx)
}

func (backend *coordinatorTestBackend) Head(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	backend.memoryBackend.mu.Lock()
	defer backend.memoryBackend.mu.Unlock()
	return backend.memoryBackend.head, nil
}

func (backend *coordinatorTestBackend) Commit(ctx context.Context, head string, snapshot Snapshot) (string, error) {
	return backend.memoryBackend.Commit(ctx, head, snapshot)
}

func (backend *coordinatorTestBackend) BuildCandidate(ctx context.Context, options CandidateOptions) (GitCandidate, error) {
	backend.buildCalls++
	if err := ctx.Err(); err != nil {
		return GitCandidate{}, err
	}
	return GitCandidate{ParentHead: options.ParentHead, CandidateID: fmt.Sprintf("%040x", backend.buildCalls), TreeID: fmt.Sprintf("%040x", backend.buildCalls+100), Snapshot: cloneSnapshot(options.Snapshot)}, nil
}

func (backend *coordinatorTestBackend) Publish(ctx context.Context, candidate GitCandidate) (PublicationOutcome, error) {
	backend.publishCalls++
	index := backend.publishCalls - 1
	kind := PublicationCommitted
	if index < len(backend.publishKinds) {
		kind = backend.publishKinds[index]
	}
	if backend.publishErr != nil {
		return PublicationOutcome{Kind: kind, CandidateID: candidate.CandidateID}, backend.publishErr
	}
	switch kind {
	case PublicationCommitted:
		head, err := backend.memoryBackend.Commit(ctx, candidate.ParentHead, candidate.Snapshot)
		if err != nil {
			return PublicationOutcome{Kind: PublicationRejected, Head: backend.memoryBackend.head, CandidateID: candidate.CandidateID}, err
		}
		if backend.wrongCandidate {
			return PublicationOutcome{Kind: PublicationCommitted, Head: head, CandidateID: fmt.Sprintf("%040x", backend.buildCalls+999)}, nil
		}
		if backend.invalidHead {
			return PublicationOutcome{Kind: PublicationCommitted, Head: candidate.ParentHead, CandidateID: candidate.CandidateID}, nil
		}
		return PublicationOutcome{Kind: PublicationCommitted, Head: head, CandidateID: candidate.CandidateID}, nil
	default:
		if kind == PublicationRejected && backend.onRejected != nil {
			backend.onRejected()
		}
		if kind == PublicationUnknown && backend.unknownCommits {
			if _, err := backend.memoryBackend.Commit(ctx, candidate.ParentHead, candidate.Snapshot); err != nil {
				return PublicationOutcome{Kind: PublicationUnknown, CandidateID: candidate.CandidateID}, err
			}
		}
		return PublicationOutcome{Kind: kind, CandidateID: candidate.CandidateID}, ErrHeadMoved
	}
}

func transactionTestDecision(snapshot Snapshot, eventID string, generation int) TransactionDecision {
	return TransactionDecision{
		Events:    []Event{requestEvent(eventID, fmt.Sprintf(`{"generation":%d}`, generation), "", 0)},
		Result:    fmt.Sprintf("result-%d", generation),
		Candidate: CandidateOptions{Subject: "state(test): persist decision", Provenance: GitProvenance{Operation: "dispatch.claim"}},
	}
}

func replayEventValidator(event Event) TransactionReplayValidator {
	return func(snapshot Snapshot) error {
		expected, err := transactionEventDigest(event)
		if err != nil {
			return err
		}
		if actual, exists := appliedEventDigest(snapshot, event.ID); exists && actual != expected {
			return ErrTransactionIdentityConflict
		}
		return nil
	}
}

func appliedEventDigestExists(snapshot Snapshot, eventID string) bool {
	if _, found := snapshot.AppliedEvents[eventID]; found {
		return true
	}
	for _, aggregate := range snapshot.Aggregates {
		if _, found := aggregate.AppliedEvents[eventID]; found {
			return true
		}
	}
	return false
}
