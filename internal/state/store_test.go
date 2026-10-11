package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
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

func TestRequestDeletionEventRequiresExactCurrentRecordAndReplays(t *testing.T) {
	seed := requestEvent("request-seed", `{"module_id":"avm-res-demo","status":"queued"}`, "", 0)
	initial, _, err := applyEvent(NewSnapshot(), seed)
	if err != nil {
		t.Fatalf("seed request: %v", err)
	}
	deletion, err := NewRequestDeletionEvent(seed.RecordPath, seed.Record)
	if err != nil {
		t.Fatalf("create exact request deletion: %v", err)
	}
	deleted, changed, err := applyEvent(initial, deletion)
	if err != nil || !changed {
		t.Fatalf("delete exact request: changed=%t err=%v", changed, err)
	}
	if _, exists := deleted.Records[seed.RecordPath]; exists {
		t.Fatal("deleted request remains in the snapshot")
	}
	if _, exists := deleted.AppliedEvents[deletion.ID]; !exists {
		t.Fatal("request deletion has no durable event identity")
	}
	replayed, changed, err := applyEvent(deleted, deletion)
	if err != nil || changed || replayed.Generation != deleted.Generation {
		t.Fatalf("replay deletion: changed=%t generation=%d err=%v", changed, replayed.Generation, err)
	}
}

func TestRequestDeletionEventRejectsChangedOrMissingRecord(t *testing.T) {
	seed := requestEvent("request-seed-for-delete", `{"module_id":"avm-res-demo","status":"queued"}`, "", 0)
	initial, _, err := applyEvent(NewSnapshot(), seed)
	if err != nil {
		t.Fatal(err)
	}
	deletion, err := NewRequestDeletionEvent(seed.RecordPath, seed.Record)
	if err != nil {
		t.Fatal(err)
	}
	changedRecord := requestEvent("request-replacement", `{"module_id":"avm-res-demo","status":"superseded"}`, "", 0)
	changedSnapshot, _, err := applyEvent(initial, changedRecord)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = applyEvent(changedSnapshot, deletion); !errors.Is(err, ErrRecordChanged) {
		t.Fatalf("delete changed request error = %v, want ErrRecordChanged", err)
	}
	if _, _, err = applyEvent(NewSnapshot(), deletion); !errors.Is(err, ErrRecordChanged) {
		t.Fatalf("delete missing request error = %v, want ErrRecordChanged", err)
	}
	other := Event{ID: "delete-non-request", RecordPath: "state/exports/old.json", Record: Record{Kind: "export", SchemaVersion: 1, Data: json.RawMessage(`{"id":"old"}`)}, Delete: true}
	if _, _, err = applyEvent(initial, other); err == nil {
		t.Fatal("request deletion event accepted a non-request record")
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

func TestOperationReceiptConstructorAndValidation(t *testing.T) {
	moduleID := "avm-utl-sku-finder"
	operationID := "discovery:module:stable-operation-1"
	request := Event{
		ID: "request-event-1", RecordPath: "state/requests/request-1.json",
		Record: Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"module_id":"avm-utl-sku-finder","status":"queued"}`)},
	}
	result := json.RawMessage(`{"request_id":"request-1","queued":true}`)
	receiptEvent, err := NewOperationReceipt(moduleID, operationID, result, []Event{request})
	if err != nil {
		t.Fatal(err)
	}
	if receiptEvent.ID != operationID || receiptEvent.Record.Kind != "operation_receipt" || receiptEvent.RecordPath != "state/operations/receipts/"+moduleID+"/"+operationID+".json" {
		t.Fatalf("receipt event identity/path = %+v", receiptEvent)
	}
	if err = validateRecord(receiptEvent.RecordPath, receiptEvent.Record); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	var receipt OperationReceipt
	if err = json.Unmarshal(receiptEvent.Record.Data, &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.WriteSet) != 1 || receipt.WriteSet[0].EventID != request.ID || receipt.WriteSet[0].RecordPath != request.RecordPath {
		t.Fatalf("receipt write set = %+v", receipt.WriteSet)
	}

	tests := []struct {
		name   string
		mutate func(*OperationReceipt)
	}{
		{name: "wrong operation path", mutate: func(receipt *OperationReceipt) { receipt.OperationID = "discovery:module:stable-operation-2" }},
		{name: "changed result", mutate: func(receipt *OperationReceipt) { receipt.Result = json.RawMessage(`{"queued":false}`) }},
		{name: "unsorted or duplicate identity", mutate: func(receipt *OperationReceipt) { receipt.WriteSet = append(receipt.WriteSet, receipt.WriteSet[0]) }},
		{name: "cross-owner record", mutate: func(receipt *OperationReceipt) {
			receipt.WriteSet[0].RecordPath = "state/discovery/catalog.json"
			receipt.WriteSet[0].RecordKind = "discovery_catalog"
		}},
		{name: "operation ID in write set", mutate: func(receipt *OperationReceipt) { receipt.WriteSet[0].EventID = receipt.OperationID }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := receipt
			changed.WriteSet = append([]OperationWriteSetEntry(nil), receipt.WriteSet...)
			test.mutate(&changed)
			data, marshalErr := json.Marshal(changed)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := validateRecord(receiptEvent.RecordPath, Record{Kind: "operation_receipt", SchemaVersion: 1, Data: data}); err == nil {
				t.Fatal("invalid receipt was accepted")
			}
		})
	}
}

func TestDiscoveryWorkValidatesOwnerFenceRetryAndReceipt(t *testing.T) {
	moduleID := "avm-utl-sku-finder"
	operationID := "discovery:module:sku-finder-work-1"
	fingerprint := strings.Repeat("a", 64)
	logicalPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	base := DiscoveryWork{
		SchemaVersion: 1, ModuleID: moduleID, DesiredFingerprint: fingerprint,
		OperationID: operationID, State: DiscoveryWorkReady, Attempt: 1,
		OwnerGeneration: 1, FailureCategory: "none",
	}
	if err = base.Validate(logicalPath); err != nil {
		t.Fatalf("valid discovery work rejected: %v", err)
	}
	retryable := base
	retryable.State = DiscoveryWorkRetryable
	retryable.FailureCategory = "source_unavailable"
	retryable.RetryAt = time.Now().UTC().Add(time.Minute)
	if err = retryable.Validate(logicalPath); err != nil {
		t.Fatalf("valid retryable discovery work rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*DiscoveryWork)
	}{
		{name: "owner path mismatch", mutate: func(work *DiscoveryWork) { work.ModuleID = "avm-res-other" }},
		{name: "invalid state", mutate: func(work *DiscoveryWork) { work.State = "running-from-board" }},
		{name: "unsafe failure", mutate: func(work *DiscoveryWork) { work.FailureCategory = "raw:secret:error" }},
		{name: "missing retry time", mutate: func(work *DiscoveryWork) { work.State = DiscoveryWorkRetryable; work.FailureCategory = "rate_limited" }},
		{name: "retry on ready state", mutate: func(work *DiscoveryWork) { work.RetryAt = time.Now().UTC().Add(time.Minute) }},
		{name: "owner without lease", mutate: func(work *DiscoveryWork) { work.OwnerRunID = "run-1" }},
		{name: "lease without owner", mutate: func(work *DiscoveryWork) { work.LeaseExpiresAt = time.Now().UTC().Add(time.Minute) }},
		{name: "foreign receipt", mutate: func(work *DiscoveryWork) {
			work.ReceiptPath = "state/modules/res/avm-res-other/operations/receipts/op.json"
		}},
		{name: "zero fence generation", mutate: func(work *DiscoveryWork) { work.OwnerGeneration = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := base
			test.mutate(&invalid)
			if err := invalid.Validate(logicalPath); err == nil {
				t.Fatal("invalid discovery work was accepted")
			}
		})
	}
	owned := base
	owned.ReceiptPath = "state/modules/utl/" + moduleID + "/operations/receipts/" + operationID + ".json"
	if err = owned.Validate(logicalPath); err != nil {
		t.Fatalf("valid owner-scoped receipt reference rejected: %v", err)
	}
}

func TestDiscoveryWorkPathAndKindAreValidatedBySnapshot(t *testing.T) {
	moduleID := "avm-utl-sku-finder"
	logicalPath, err := identity.StateDiscoveryWorkPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	work := DiscoveryWork{SchemaVersion: 1, ModuleID: moduleID, DesiredFingerprint: strings.Repeat("a", 64), OperationID: "discovery:module:snapshot-work-1", State: DiscoveryWorkReady, Attempt: 1, OwnerGeneration: 1, FailureCategory: "none"}
	record, err := DiscoveryWorkRecord(work)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := NewSnapshot()
	snapshot.Records[logicalPath] = record
	if err = snapshot.Validate(); err != nil {
		t.Fatalf("valid discovery work record rejected by snapshot: %v", err)
	}
	snapshot.Records[logicalPath] = Record{Kind: "request", SchemaVersion: 1, Data: record.Data}
	if err = snapshot.Validate(); err == nil {
		t.Fatal("discovery-work path accepted the wrong record kind")
	}
}

type projectionLifecycleFixture struct {
	moduleID            string
	projection          projection.ProjectionIdentity
	handoff             projection.Handoff
	handoffPath         string
	acknowledgement     Record
	acknowledgementPath string
	release             projection.BarrierRelease
	releasePath         string
	records             map[string]Record
	now                 time.Time
}

func newProjectionLifecycleFixture(t *testing.T) projectionLifecycleFixture {
	t.Helper()
	moduleID := "avm-res-projection-records"
	intent, err := projection.NewDesiredIntent(moduleID, 4, "transition-4", "queued", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	projectionIdentity, err := intent.Identity()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	handoff, err := projection.NewHandoff(projection.Handoff{
		ModuleID: moduleID, Generation: intent.Generation, TransitionID: intent.TransitionID,
		InputFingerprint: stateTestRecordID(t, "input-fingerprint"),
		OperationID:      "discovery:avm-res-projection-records:g4",
		Producer: projection.WorkflowProvenance{
			WorkflowID: "discovery.yml", Commit: strings.Repeat("a", 40), RunID: "run-4", RunAttempt: 1,
		},
		ConsumerStage: "acquire_source", Projection: projectionIdentity,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	acknowledgement := projection.Acknowledgement{
		SchemaVersion: projection.SchemaVersion, Projection: projectionIdentity,
		Status: projection.AcknowledgementDone, AcknowledgedAt: now,
	}
	deadline := now.Add(-projection.BarrierTimeout)
	release := projection.BarrierRelease{
		SchemaVersion: projection.SchemaVersion, Projection: projectionIdentity,
		Status: projection.BarrierReleased, Deadline: deadline, ReleasedAt: now,
	}
	handoffPath, err := identity.StateProjectionHandoffPath(moduleID, handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	acknowledgementPath, err := identity.StateProjectionAcknowledgementPath(moduleID, projectionIdentity.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	releasePath, err := identity.StateProjectionBarrierReleasePath(moduleID, projectionIdentity.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]Record{
		handoffPath:         projectionStateRecord(t, "stage_handoff", handoff),
		acknowledgementPath: projectionStateRecord(t, "projection_acknowledgement", acknowledgement),
		releasePath:         projectionStateRecord(t, "projection_barrier_release", release),
	}
	return projectionLifecycleFixture{
		moduleID: moduleID, projection: projectionIdentity, handoff: handoff, handoffPath: handoffPath,
		acknowledgement: records[acknowledgementPath], acknowledgementPath: acknowledgementPath,
		release: release, releasePath: releasePath, records: records, now: now,
	}
}

func TestProjectionLifecycleRecordsValidatePathsKindsAndModuleOwners(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	snapshot := NewSnapshot()
	for logicalPath, record := range fixture.records {
		if err := validateRecord(logicalPath, record); err != nil {
			t.Fatalf("valid %s record rejected: %v", record.Kind, err)
		}
		snapshot.Records[logicalPath] = record
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("valid projection lifecycle records rejected by snapshot: %v", err)
	}
	for logicalPath, record := range fixture.records {
		if _, _, err := applyEvent(snapshot, Event{ID: "projection-fixture-" + record.Kind, RecordPath: logicalPath, Record: record}); err != nil {
			t.Fatalf("identical %s handoff/ack/release content was rejected: %v", record.Kind, err)
		}
	}
	assertImmutableHandoff(t, snapshot, fixture)
	assertProjectionModuleRoundTrip(t, snapshot, fixture)
}

func assertImmutableHandoff(t *testing.T, snapshot Snapshot, fixture projectionLifecycleFixture) {
	t.Helper()
	changedHandoff := fixture.handoff
	changedHandoff.Producer.RunID = "run-replacement"
	changedHandoff.Producer.RunAttempt++
	changedRecord := projectionStateRecord(t, "stage_handoff", changedHandoff)
	if _, _, err := applyEvent(snapshot, Event{ID: "projection-fixture-handoff-replacement", RecordPath: fixture.handoffPath, Record: changedRecord}); err == nil {
		t.Fatal("changed immutable handoff content replaced an existing handoff ID")
	}
}

func assertProjectionModuleRoundTrip(t *testing.T, snapshot Snapshot, fixture projectionLifecycleFixture) {
	t.Helper()
	backend := newMemoryBackend(snapshot)
	if _, err := MigrateModuleLayout(context.Background(), backend); err != nil {
		t.Fatalf("module-v2 migration rejected projection lifecycle records: %v", err)
	}
	_, moduleSnapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	moduleFiles, err := encodeSnapshotFiles(moduleSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot, err := identity.OperationalModuleRoot(fixture.moduleID)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range fixture.records {
		logicalPath := projectionRecordLogicalPath(t, fixture, record.Kind)
		_, physicalPath, resolveErr := resolveRecordOwner(logicalPath, record)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		if !strings.HasPrefix(physicalPath, moduleRoot+"/projection/") {
			t.Fatalf("record %s resolved outside module projection owner: %q", logicalPath, physicalPath)
		}
		if _, exists := moduleFiles[physicalPath]; !exists {
			t.Fatalf("module-v2 encoding omitted %s at %s", logicalPath, physicalPath)
		}
	}
	assertModuleLayoutRoundtrip(t, moduleSnapshot)
}

func projectionRecordLogicalPath(t *testing.T, fixture projectionLifecycleFixture, kind string) string {
	t.Helper()
	for logicalPath, record := range fixture.records {
		if record.Kind == kind {
			return logicalPath
		}
	}
	t.Fatalf("fixture has no %s record", kind)
	return ""
}

func TestProjectionLifecycleRecordsRejectInvalidIdentityAndStatus(t *testing.T) {
	fixture := newProjectionLifecycleFixture(t)
	for _, test := range []struct {
		name   string
		path   string
		record Record
	}{
		{name: "wrong kind", path: fixture.handoffPath, record: fixture.acknowledgement},
		{name: "handoff ID path mismatch", path: strings.Replace(fixture.handoffPath, fixture.handoff.ID, stateTestRecordID(t, "stage-handoff"), 1), record: fixture.records[fixture.handoffPath]},
		{name: "cross-module acknowledgement", path: identityMustAcknowledgementPath(t, "avm-res-other-module", fixture.projection.IntentID), record: fixture.acknowledgement},
		{name: "release cannot claim success", path: fixture.releasePath, record: projectionStateRecord(t, "projection_barrier_release", projection.BarrierRelease{
			SchemaVersion: projection.SchemaVersion, Projection: fixture.projection,
			Status: projection.AcknowledgementDone, Deadline: fixture.release.Deadline, ReleasedAt: fixture.now,
		})},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRecord(test.path, test.record); err == nil {
				t.Fatal("invalid projection lifecycle record was accepted")
			}
		})
	}
}

func projectionStateRecord(t *testing.T, kind string, value any) Record {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return Record{Kind: kind, SchemaVersion: projection.SchemaVersion, Data: data}
}

func stateTestRecordID(t *testing.T, kind string) string {
	t.Helper()
	id, err := identity.ID(kind, 1, map[string]string{"fixture": "projection-lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func identityMustAcknowledgementPath(t *testing.T, moduleID, intentID string) string {
	t.Helper()
	path, err := identity.StateProjectionAcknowledgementPath(moduleID, intentID)
	if err != nil {
		t.Fatal(err)
	}
	return path
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
