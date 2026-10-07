package discovery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

// pacingEscapeBackend intercepts exact cursor transaction shapes so a pacing
// wall on routine checkpoint saves can be proven to park instead of failing,
// while every other state transition behaves normally.
type pacingEscapeBackend struct {
	*memoryBackend
	intercept func(cursor Continuation) bool
	walls     int
	deferred  time.Time
}

func (backend *pacingEscapeBackend) Commit(ctx context.Context, expectedHead string, snapshot state.Snapshot) (string, error) {
	for _, record := range snapshot.Records {
		if record.Kind != "discovery_cycle" {
			continue
		}
		var cursor Continuation
		if err := json.Unmarshal(record.Data, &cursor); err != nil {
			return "", err
		}
		if backend.intercept != nil && backend.intercept(cursor) {
			backend.walls++
			return "", &ratelimit.DeferredError{Resource: "rest", RetryAt: backend.deferred, Reason: "local_hourly_mutations"}
		}
	}
	return backend.memoryBackend.Commit(ctx, expectedHead, snapshot)
}

func TestContinuationOwnerClaimDeferralParksInsteadOfFailing(t *testing.T) {
	now := time.Now().UTC()
	backend := &pacingEscapeBackend{
		memoryBackend: newDiscoveryMemoryBackend(),
		deferred:      now.Add(2 * time.Minute),
		intercept: func(cursor Continuation) bool {
			return cursor.Status == "running" && !cursor.PendingDelivery && cursor.Next == 0
		},
	}
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}}
	result, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, discoveryTestSnapshot(now), options, func(context.Context, ModuleCycle) (string, error) {
		t.Fatal("deferring claim still delivered a module")
		return "", nil
	}, "claim-defer", "", time.Time{})
	if err != nil || !result.Deferred || !result.DeferralSaved || !result.ContinuationDurable || result.ContinuationID == "" {
		t.Fatalf("owner claim deferral was not a durable wait: %+v %v", result, err)
	}
	if !result.RetryAt.Equal(backend.deferred) || result.WaitReason != "local_hourly_mutations" {
		t.Fatalf("claim park lost the pacing deadline: %+v", result)
	}
	cursor, err := ReadContinuation(backend.snapshot, result.ContinuationID)
	if err != nil || cursor.Status != "deferred" || cursor.OwnerGeneration != 1 || !cursor.RetryAt.Equal(backend.deferred) || !cursor.LeaseExpiresAt.IsZero() {
		t.Fatalf("claim park wrote wrong durable cursor: %+v %v", cursor, err)
	}
}

func TestContinuationDoubleWallOnClaimStillProvesDurableDeferral(t *testing.T) {
	now := time.Now().UTC()
	backend := &pacingEscapeBackend{memoryBackend: newDiscoveryMemoryBackend(), deferred: now.Add(3 * time.Minute)}
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}}
	deliverNone := func(context.Context, ModuleCycle) (string, error) {
		t.Fatal("parked segment delivered a module")
		return "", nil
	}
	// Segment one parks at the bounded stop so the stored cursor is a clean
	// deferral with no pending delivery, then its deadline is already past.
	first, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, discoveryTestSnapshot(now), options, deliverNone, "double-wall", "", time.Now().UTC().Add(-time.Second))
	if err != nil || !first.Deferred || !first.DeferralSaved {
		t.Fatalf("setup park failed: deferred=%v saved=%v err=%v", first.Deferred, first.DeferralSaved, err)
	}
	storedCursor, err := ReadContinuation(backend.snapshot, first.ContinuationID)
	if err != nil {
		t.Fatal(err)
	}
	staleRetryAt := storedCursor.RetryAt
	// Now both the owner claim and its parking retry hit the local wall.
	backend.intercept = func(cursor Continuation) bool {
		return cursor.Status == "deferred" || cursor.Status == "running" && !cursor.PendingDelivery && cursor.Next == 0
	}
	second, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, discoveryTestSnapshot(now), options, deliverNone, "double-wall", first.ContinuationID, time.Time{})
	if err != nil {
		t.Fatalf("double wall failed instead of waiting: %v", err)
	}
	if !second.Deferred || !second.DeferralSaved || !second.ContinuationDurable || second.ContinuationID != first.ContinuationID {
		t.Fatalf("double wall lost the durable proof: deferred=%v saved=%v durable=%v", second.Deferred, second.DeferralSaved, second.ContinuationDurable)
	}
	if !second.RetryAt.Equal(backend.deferred) || !second.RetryAt.After(staleRetryAt) {
		t.Fatalf("double wall reported a stale deadline: stored %v retry %v", staleRetryAt, second.RetryAt)
	}
	// The durable record is untouched by the failed claim: still the first
	// segment's saved deferral.
	cursor, err := ReadContinuation(backend.snapshot, first.ContinuationID)
	if err != nil || cursor.Status != "deferred" || cursor.OwnerGeneration != 1 || !cursor.RetryAt.Equal(staleRetryAt) {
		t.Fatalf("double wall mutated the stored cursor: %+v %v", cursor, err)
	}
}

func TestContinuationCheckpointDeferralParksCompletedProgress(t *testing.T) {
	now := time.Now().UTC()
	backend := &pacingEscapeBackend{
		memoryBackend: newDiscoveryMemoryBackend(),
		deferred:      now.Add(3 * time.Minute),
		intercept: func(cursor Continuation) bool {
			return cursor.Status == "running" && !cursor.PendingDelivery && cursor.Next == 1
		},
	}
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	selected := []string{"avm-res-resources-resourcegroup", "avm-res-resources-storage"}
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: selected, ForceModuleIDs: selected}
	result, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, discoveryTestSnapshot(now), options, func(ctx context.Context, module ModuleCycle) (string, error) {
		return acknowledgeCycleTestModule(t, ctx, backend.memoryBackend, module, time.Now().UTC().Add(time.Second)), nil
	}, "checkpoint-defer", "", time.Time{})
	if err != nil || !result.Deferred || !result.DeferralSaved || len(result.CompletedModuleIDs) != 1 {
		t.Fatalf("checkpoint deferral lost completed progress: %+v %v", result, err)
	}
	if backend.walls != 1 {
		t.Fatalf("unexpected intercept count: %+v", backend)
	}
	cursor, err := ReadContinuation(backend.snapshot, result.ContinuationID)
	if err != nil || cursor.Status != "deferred" || cursor.Next != 1 || !cursor.RetryAt.Equal(backend.deferred) {
		t.Fatalf("checkpoint park wrote wrong cursor: %+v %v", cursor, err)
	}
}

func TestContinuationCompleteMarkerSurvivesLocalWall(t *testing.T) {
	now := time.Now().UTC()
	backend := &pacingEscapeBackend{memoryBackend: newDiscoveryMemoryBackend(), deferred: now.Add(3 * time.Minute)}
	// Wall the terminal marker exactly once; the parking retry must re-commit
	// it without converting it to a deferred record.
	backend.intercept = func(cursor Continuation) bool {
		if cursor.Status != "complete" {
			return false
		}
		backend.walls++
		return backend.walls == 1
	}
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}}
	result, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, discoveryTestSnapshot(now), options, func(ctx context.Context, module ModuleCycle) (string, error) {
		return acknowledgeCycleTestModule(t, ctx, backend.memoryBackend, module, time.Now().UTC().Add(time.Second)), nil
	}, "complete-wall", "", time.Time{})
	if err != nil || !result.ContinuationComplete || result.Deferred || len(result.CompletedModuleIDs) != 1 {
		t.Fatalf("complete marker did not survive the wall: %+v %v", result, err)
	}
	cursor, err := ReadContinuation(backend.snapshot, result.ContinuationID)
	if err != nil || cursor.Status != "complete" || !cursor.LeaseExpiresAt.IsZero() {
		t.Fatalf("durable terminal state incorrect: %+v %v", cursor, err)
	}
}

func TestContinuationReclaimOfUnmarkedCompleteCycleClosesWithoutDelivery(t *testing.T) {
	now := time.Now().UTC()
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}}
	result, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, discoveryTestSnapshot(now), options, func(ctx context.Context, module ModuleCycle) (string, error) {
		return acknowledgeCycleTestModule(t, ctx, backend, module, time.Now().UTC().Add(time.Second)), nil
	}, "unmarked-complete", "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate losing the terminal marker between commits so the record is
	// still running with every module completed.
	path, err := continuationPath(result.ContinuationID)
	if err != nil {
		t.Fatal(err)
	}
	var cursor Continuation
	if err := json.Unmarshal(backend.snapshot.Records[path].Data, &cursor); err != nil {
		t.Fatal(err)
	}
	cursor.Status = "running"
	cursor.LeaseExpiresAt = now.Add(-time.Minute)
	data, err := json.Marshal(cursor)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[path] = state.Record{Kind: "discovery_cycle", SchemaVersion: 1, Data: data}
	reopened, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, discoveryTestSnapshot(now), options, func(context.Context, ModuleCycle) (string, error) {
		t.Fatal("reopening a completed scope delivered modules")
		return "", nil
	}, "reopen-run", result.ContinuationID, time.Time{})
	if err != nil || !reopened.ContinuationComplete || reopened.Deferred {
		t.Fatalf("unmarked completion did not close: %+v %v", reopened, err)
	}
	closed, err := ReadContinuation(backend.snapshot, result.ContinuationID)
	if err != nil || closed.Status != "complete" {
		t.Fatalf("durable closure missing: %+v %v", closed, err)
	}
}

// seedProveCursor writes one exact continuation record for the proof tests.
func seedProveCursor(t *testing.T, status string) (*memoryBackend, string) {
	t.Helper()
	now := time.Now().UTC()
	backend := newDiscoveryMemoryBackend()
	cursor := Continuation{SchemaVersion: 1, ID: "discovery-cycle-" + strings.Repeat("b", 64), RunID: "prove-run", OwnerRunID: "prove-run", OwnerGeneration: 2, Status: status, Options: Options{SelectedModuleIDs: []string{"avm-res-one"}}, Result: Result{SelectedModuleIDs: []string{"avm-res-one"}}}
	switch status {
	case "deferred":
		cursor.RetryAt = now.Add(-10 * time.Minute)
	case "complete":
		cursor.Next, cursor.RetryAt = 1, time.Time{}
		cursor.Result.CompletedModuleIDs = []string{"avm-res-one"}
	case "running":
		cursor.LeaseExpiresAt = now.Add(time.Minute)
	}
	if _, err := saveContinuation(context.Background(), backend, backend.head, cursor); err != nil {
		t.Fatal(err)
	}
	return backend, cursor.ID
}

func TestProveDurableDeferralReportsSavedWait(t *testing.T) {
	future := time.Now().UTC().Add(4 * time.Minute)
	t.Run("deferred", func(t *testing.T) {
		backend, id := seedProveCursor(t, "deferred")
		result, ok, err := ProveDurableDeferral(context.Background(), backend, id, future)
		if !ok || err != nil || !result.Deferred || !result.DeferralSaved || result.ContinuationComplete {
			t.Fatalf("deferred proof incorrect: %+v ok=%t %v", result, ok, err)
		}
		if !result.RetryAt.Equal(future) {
			t.Fatalf("stale deadline not raised to the pacer floor: %v want %v", result.RetryAt, future)
		}
		assertProveIdentity(t, backend, id, result)
	})
	t.Run("complete", func(t *testing.T) {
		backend, id := seedProveCursor(t, "complete")
		result, ok, err := ProveDurableDeferral(context.Background(), backend, id, future)
		if !ok || err != nil || !result.ContinuationComplete || result.Deferred || !result.RetryAt.IsZero() {
			t.Fatalf("complete proof incorrect: %+v ok=%t %v", result, ok, err)
		}
		assertProveIdentity(t, backend, id, result)
	})
	t.Run("running", func(t *testing.T) {
		backend, id := seedProveCursor(t, "running")
		result, ok, err := ProveDurableDeferral(context.Background(), backend, id, future)
		if ok || err != nil || result.ContinuationID != "" {
			t.Fatalf("running cursor must not prove a wait: %+v ok=%t %v", result, ok, err)
		}
	})
}

func assertProveIdentity(t *testing.T, backend *memoryBackend, id string, result Result) {
	t.Helper()
	if result.ContinuationID != id || !result.ContinuationDurable || result.Head != backend.head {
		t.Fatalf("proof lost durable identity: %+v", result)
	}
}

func TestProveDurableDeferralRejectsMissingCursor(t *testing.T) {
	backend := newDiscoveryMemoryBackend()
	id := "discovery-cycle-" + strings.Repeat("c", 64)
	result, ok, err := ProveDurableDeferral(context.Background(), backend, id, time.Now().UTC().Add(time.Minute))
	if ok || err == nil || result.ContinuationID != "" {
		t.Fatalf("missing cursor claimed a proof: %+v %v ok=%t", result, err, ok)
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing cursor error unclear: %v", err)
	}
}
