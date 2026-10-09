package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type headCountingBackend struct {
	*memoryBackend
	reads    int
	checkErr error
}

func (backend *headCountingBackend) Head(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return backend.head, backend.checkErr
}

func (backend *headCountingBackend) Read(ctx context.Context) (string, Snapshot, error) {
	backend.reads++
	return backend.memoryBackend.Read(ctx)
}

func TestVerifiedReadCacheChecksHeadAndIsolatesSnapshots(t *testing.T) {
	ctx := context.Background()
	origin := &headCountingBackend{memoryBackend: newMemoryBackend(NewSnapshot())}
	cache := &VerifiedReadBackend{Backend: origin}
	_, snapshot, err := cache.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.FencingTokens["private"] = 99
	_, reused, err := cache.Read(ctx)
	if err != nil || origin.reads != 1 || reused.FencingTokens["private"] != 0 {
		t.Fatal("cache reused mutable caller snapshot or reloaded stable head")
	}
	origin.head = "changed-head"
	if _, _, err := cache.Read(ctx); err != nil || origin.reads != 2 {
		t.Fatal("cache did not reload changed head")
	}
	origin.checkErr = errors.New("head fixture failure")
	if _, _, err := cache.Read(ctx); err == nil {
		t.Fatal("cache bypassed live head failure")
	}
}

func TestTransactionContractCarriesDecisionScopeAndPublicationOutcomes(t *testing.T) {
	base := NewSnapshot()
	decision := TransactionDecisionFunc(func(snapshot Snapshot) (TransactionDecision, error) {
		if snapshot.Generation != base.Generation {
			t.Fatalf("decision snapshot generation = %d, want %d", snapshot.Generation, base.Generation)
		}
		return TransactionDecision{
			ReadSet: TransactionReadSet{Owners: []string{"state/global", "state/modules/avm-res-demo"}, Paths: []string{"state/operations/global.json"}},
			Events:  []Event{requestEvent("decision-event", `{"status":"queued"}`, "module/avm-res-demo", 1)},
			Result:  "proposed-result",
		}, nil
	})
	proposed, err := decision(cloneSnapshot(base))
	if err != nil {
		t.Fatal(err)
	}
	if len(proposed.ReadSet.Owners) != 2 || len(proposed.ReadSet.Paths) != 1 || len(proposed.Events) != 1 || proposed.Result != "proposed-result" || proposed.Noop {
		t.Fatalf("decision contract lost declared scope or proposal: %+v", proposed)
	}
	if !(TransactionDecision{Noop: true}).Noop {
		t.Fatal("no-op decision cannot be represented")
	}

	resolver := TransactionResultResolver(func(snapshot Snapshot) (TransactionResolution, error) {
		if _, exists := snapshot.AppliedEvents["decision-event"]; !exists {
			return TransactionResolution{}, fmt.Errorf("durable operation is missing")
		}
		return TransactionResolution{Found: true, Value: "original-result"}, nil
	})
	committed := cloneSnapshot(base)
	committed.AppliedEvents["decision-event"] = "durable-digest"
	resolved, err := resolver(committed)
	if err != nil || !resolved.Found || resolved.Value != "original-result" {
		t.Fatalf("resolver result = %+v, error = %v", resolved, err)
	}

	for _, kind := range []PublicationOutcomeKind{PublicationCommitted, PublicationRejected, PublicationUnknown} {
		outcome := PublicationOutcome{Kind: kind, Head: "head-1", CandidateID: "candidate-1"}
		if outcome.Kind != kind || outcome.Head == "" || outcome.CandidateID == "" {
			t.Fatalf("publication outcome contract lost classification or identity: %+v", outcome)
		}
	}
}

func TestApplyEventIsIdempotentAndRejectsChangedReplay(t *testing.T) {
	backend := newMemoryBackend(NewSnapshot())
	event := requestEvent("event-1", `{"status":"queued"}`, "", 0)

	head, snapshot, err := ApplyEvent(context.Background(), backend, event)
	if err != nil {
		t.Fatal(err)
	}
	if head != "head-1" || snapshot.Generation != 1 {
		t.Fatalf("first apply = (%q, generation %d), want (head-1, 1)", head, snapshot.Generation)
	}

	retryHead, retrySnapshot, err := ApplyEvent(context.Background(), backend, event)
	if err != nil {
		t.Fatal(err)
	}
	if retryHead != head || retrySnapshot.Generation != snapshot.Generation {
		t.Fatalf("idempotent retry changed head or generation: (%q, %d)", retryHead, retrySnapshot.Generation)
	}

	changed := requestEvent("event-1", `{"status":"failed"}`, "", 0)
	if _, _, err = ApplyEvent(context.Background(), backend, changed); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("changed replay error = %v, want ErrEventConflict", err)
	}
}

func TestApplyEventRejectsStaleFence(t *testing.T) {
	snapshot := NewSnapshot()
	snapshot.FencingTokens["module-a"] = 4
	backend := newMemoryBackend(snapshot)
	event := requestEvent("event-stale", `{"status":"running"}`, "module-a", 3)

	if _, _, err := ApplyEvent(context.Background(), backend, event); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale event error = %v, want ErrStaleFence", err)
	}
	if backend.commitCount != 0 {
		t.Fatalf("stale event made %d commits, want none", backend.commitCount)
	}
}

func TestExpectedHeadAllowsOnlyOneConcurrentCommit(t *testing.T) {
	backend := newMemoryBackend(NewSnapshot())
	_, base, err := ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}

	var waitGroup sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			next := cloneSnapshot(base)
			next.Generation = 1
			_, commitErr := CommitExpectedHead(context.Background(), backend, "head-0", next)
			results <- commitErr
		}()
	}
	waitGroup.Wait()
	close(results)

	committed := 0
	conflicted := 0
	for commitErr := range results {
		switch {
		case commitErr == nil:
			committed++
		case errors.Is(commitErr, ErrHeadMoved):
			conflicted++
		default:
			t.Fatalf("unexpected commit error: %v", commitErr)
		}
	}
	if committed != 1 || conflicted != 1 {
		t.Fatalf("commits = %d, conflicts = %d, want one each", committed, conflicted)
	}
}

func TestApplyEventRecoversWhenCommitAcknowledgmentIsLost(t *testing.T) {
	backend := newMemoryBackend(NewSnapshot())
	backend.failAfterCommit = true
	event := requestEvent("event-ack-lost", `{"status":"queued"}`, "", 0)

	if _, _, err := ApplyEvent(context.Background(), backend, event); !errors.Is(err, errAcknowledgmentLost) {
		t.Fatalf("first apply error = %v, want lost acknowledgment", err)
	}
	head, snapshot, err := ApplyEvent(context.Background(), backend, event)
	if err != nil {
		t.Fatal(err)
	}
	if head != "head-1" || snapshot.Generation != 1 || backend.commitCount != 1 {
		t.Fatalf("retry duplicated committed event: head=%q generation=%d commits=%d", head, snapshot.Generation, backend.commitCount)
	}
}

func TestApplyEventsCommitsDiscoveryRecordsAtomically(t *testing.T) {
	backend := newMemoryBackend(NewSnapshot())
	events := []Event{
		{
			ID: "discovery-1", RecordPath: "state/discovery/catalog.json",
			Record: Record{Kind: "discovery_catalog", SchemaVersion: 1, Data: json.RawMessage(`{"generation":1}`)},
		},
		{
			ID: "pin-1", RecordPath: "state/discovery/source-pins/avm-res-demo.json",
			Record: Record{Kind: "source_pins", SchemaVersion: 1, Data: json.RawMessage(`{"module_id":"avm-res-demo"}`)},
		},
		requestEvent("acquire-1", `{"phase":"acquire_source"}`, "", 0),
	}

	head, snapshot, err := ApplyEvents(context.Background(), backend, events)
	if err != nil {
		t.Fatal(err)
	}
	if head != "head-1" || backend.commitCount != 1 || snapshot.Generation != uint64(len(events)) || len(snapshot.Records) != len(events) {
		t.Fatalf("discovery batch head=%q commits=%d generation=%d records=%d", head, backend.commitCount, snapshot.Generation, len(snapshot.Records))
	}

	invalidBatch := []Event{events[0], {
		ID: "invalid-pin", RecordPath: "state/discovery/source-pins/avm-res-demo.json",
		Record: Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"module_id":"avm-res-demo"}`)},
	}}
	if _, _, err = ApplyEvents(context.Background(), backend, invalidBatch); err == nil {
		t.Fatal("invalid batch was committed")
	}
	if backend.commitCount != 1 || backend.snapshot.Generation != uint64(len(events)) {
		t.Fatalf("invalid batch changed durable state: commits=%d generation=%d", backend.commitCount, backend.snapshot.Generation)
	}
}

func TestApplyEventsExpectedHeadRejectsStaleDiscoveryPlan(t *testing.T) {
	backend := newMemoryBackend(NewSnapshot())
	if _, _, err := ApplyEvent(context.Background(), backend, requestEvent("concurrent-1", `{"status":"queued"}`, "", 0)); err != nil {
		t.Fatal(err)
	}
	event := Event{
		ID: "stale-discovery", RecordPath: "state/discovery/catalog.json",
		Record: Record{Kind: "discovery_catalog", SchemaVersion: 1, Data: json.RawMessage(`{"generation":1}`)},
	}
	if _, _, err := ApplyEventsAtHead(context.Background(), backend, "head-0", []Event{event}); !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("stale discovery commit error = %v, want ErrHeadMoved", err)
	}
	if backend.commitCount != 1 || backend.snapshot.Generation != 1 {
		t.Fatalf("stale discovery plan changed state: commits=%d generation=%d", backend.commitCount, backend.snapshot.Generation)
	}
}

func TestApplyEventRejectsUnsafePathAndMalformedRecord(t *testing.T) {
	backend := newMemoryBackend(NewSnapshot())
	unsafePath := requestEvent("event-path", `{"status":"queued"}`, "", 0)
	unsafePath.RecordPath = "state/../workflow.yml"
	if _, _, err := ApplyEvent(context.Background(), backend, unsafePath); err == nil {
		t.Fatal("unsafe state path was accepted")
	}

	malformed := requestEvent("event-json", `{"status":`, "", 0)
	if _, _, err := ApplyEvent(context.Background(), backend, malformed); err == nil {
		t.Fatal("malformed typed record was accepted")
	}
	trailingData := requestEvent("event-trailing", `{"status":"queued"} invalid`, "", 0)
	if _, _, err := ApplyEvent(context.Background(), backend, trailingData); err == nil {
		t.Fatal("trailing data after the JSON object was accepted")
	}
	if backend.commitCount != 0 {
		t.Fatalf("invalid events made %d commits, want none", backend.commitCount)
	}
}

func requestEvent(eventID, payload, fenceKey string, fenceToken uint64) Event {
	return Event{
		ID:         eventID,
		FenceKey:   fenceKey,
		FenceToken: fenceToken,
		RecordPath: "state/requests/request-1.json",
		Record: Record{
			Kind:          "request",
			SchemaVersion: 1,
			Data:          json.RawMessage(payload),
		},
	}
}

type memoryBackend struct {
	mu              sync.Mutex
	head            string
	snapshot        Snapshot
	readCount       int
	commitCount     int
	failAfterCommit bool
}

func newMemoryBackend(snapshot Snapshot) *memoryBackend {
	return &memoryBackend{head: "head-0", snapshot: cloneSnapshot(snapshot)}
}

func (backend *memoryBackend) Read(ctx context.Context) (string, Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return "", Snapshot{}, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.readCount++
	return backend.head, cloneSnapshot(backend.snapshot), nil
}

func (backend *memoryBackend) Commit(ctx context.Context, expectedHead string, snapshot Snapshot) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.head != expectedHead {
		return "", ErrHeadMoved
	}
	backend.commitCount++
	backend.head = fmt.Sprintf("head-%d", backend.commitCount)
	backend.snapshot = cloneSnapshot(snapshot)
	if backend.failAfterCommit {
		backend.failAfterCommit = false
		return "", errAcknowledgmentLost
	}
	return backend.head, nil
}

var errAcknowledgmentLost = errors.New("commit succeeded but acknowledgment was lost")
