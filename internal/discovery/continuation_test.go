package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestContinuationResumesDeliveryWithoutForcedSourceReplay(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	backend := newDiscoveryMemoryBackend()
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	selected := []string{"avm-res-resources-resourcegroup", "avm-res-resources-storage"}
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: selected, ForceModuleIDs: selected}
	result, err := ContinueCycle(ctx, source.Resolver{APIBase: server.URL}, backend, discoveryTestSnapshot(now), options, func(ctx context.Context, module ModuleCycle) (string, error) {
		if module.Position == 2 {
			return module.Head, &ratelimit.DeferredError{Resource: "rest", RetryAt: now.Add(-time.Second)}
		}
		return acknowledgeCycleTestModule(t, ctx, backend, module, now.Add(time.Duration(module.Position)*time.Second)), nil
	}, "run-1", "", time.Time{})
	if err != nil || !result.Deferred || len(result.CompletedModuleIDs) != 1 {
		t.Fatalf("cycle did not park: %+v %v", result, err)
	}
	requestsBefore := countRecordsWithKind(backend.snapshot, "request")
	observationsBefore := countRecordsWithKind(backend.snapshot, "discovery_observation")
	cursor, err := ReadContinuation(backend.snapshot, result.ContinuationID)
	if err != nil || !cursor.PendingDelivery || cursor.Next != 1 {
		t.Fatalf("pending phase lost: %+v %v", cursor, err)
	}
	resumed, err := ContinueCycle(ctx, source.Resolver{}, backend, catalog.Snapshot{}, options, func(ctx context.Context, module ModuleCycle) (string, error) {
		if module.Position != 2 {
			t.Fatal("completed module was replayed")
		}
		return acknowledgeCycleTestModule(t, ctx, backend, module, time.Now().UTC().Add(time.Second)), nil
	}, "run-2", result.ContinuationID, time.Time{})
	if err != nil || resumed.Deferred || len(resumed.CompletedModuleIDs) != 2 {
		t.Fatalf("resume failed: %+v %v", resumed, err)
	}
	if countRecordsWithKind(backend.snapshot, "request") != requestsBefore || countRecordsWithKind(backend.snapshot, "discovery_observation") != observationsBefore {
		t.Fatal("resume repeated forced source work or catalog observation")
	}
}

func TestContinuationDeadlineParksBeforeLaterModule(t *testing.T) {
	now := time.Now().UTC()
	backend := newDiscoveryMemoryBackend()
	selected := []string{"avm-res-resources-resourcegroup"}
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: selected}
	result, err := ContinueCycle(context.Background(), source.Resolver{}, backend, discoveryTestSnapshot(now), options, func(context.Context, ModuleCycle) (string, error) {
		t.Fatal("deadline invoked module delivery")
		return "", nil
	}, "bounded-run", "", now.Add(-time.Second))
	if err != nil || !result.Deferred || countRecordsWithKind(backend.snapshot, "request") != 0 {
		t.Fatalf("deadline did not park cleanly: %+v %v", result, err)
	}
}

func TestContinuationRejectsCorruptFrozenScope(t *testing.T) {
	now := time.Now().UTC()
	base := Continuation{SchemaVersion: 1, ID: "discovery-cycle-" + strings.Repeat("a", 64), RunID: "run", OwnerRunID: "run", OwnerGeneration: 1, Status: "running", LeaseExpiresAt: now.Add(time.Minute), Options: Options{SelectedModuleIDs: []string{"avm-res-one", "avm-res-two"}}, Result: Result{SelectedModuleIDs: []string{"avm-res-one", "avm-res-two"}, CompletedModuleIDs: []string{"avm-res-one"}}, Next: 1}
	for _, mutation := range []string{"duplicate", "prefix", "options", "force", "terminal"} {
		cursor := base
		cursor.Result.SelectedModuleIDs = append([]string(nil), base.Result.SelectedModuleIDs...)
		cursor.Options.SelectedModuleIDs = append([]string(nil), base.Options.SelectedModuleIDs...)
		switch mutation {
		case "duplicate":
			cursor.Result.SelectedModuleIDs[1] = "avm-res-one"
		case "prefix":
			cursor.Result.CompletedModuleIDs = []string{"avm-res-two"}
		case "options":
			cursor.Options.SelectedModuleIDs[1] = "avm-res-one"
		case "force":
			cursor.Options.ForceModuleIDs = []string{"avm-res-other"}
		case "terminal":
			cursor.Status = "complete"
		}
		if err := validateContinuationScope(cursor); err == nil {
			t.Fatalf("accepted corrupt continuation: %s", mutation)
		}
	}
}

type parkFailureBackend struct{ *memoryBackend }

func (backend parkFailureBackend) Commit(ctx context.Context, head string, snapshot state.Snapshot) (string, error) {
	for _, record := range snapshot.Records {
		if record.Kind == "discovery_cycle" {
			var cursor Continuation
			_ = json.Unmarshal(record.Data, &cursor)
			if cursor.Status == "deferred" {
				return "", errors.New("checkpoint fixture failure")
			}
		}
	}
	return backend.memoryBackend.Commit(ctx, head, snapshot)
}

func TestContinuationFailedParkingRetainsOriginalCause(t *testing.T) {
	now := time.Now().UTC()
	backend := parkFailureBackend{newDiscoveryMemoryBackend()}
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}}
	origin := errors.New("origin fixture error")
	result, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, discoveryTestSnapshot(now), options, func(context.Context, ModuleCycle) (string, error) {
		return backend.head, errors.Join(origin, &ratelimit.DeferredError{Resource: "rest", RetryAt: now.Add(time.Hour)})
	}, "failed-park", "", time.Time{})
	if !errors.Is(err, origin) || result.Deferred || !result.ContinuationDurable || result.ContinuationID == "" {
		t.Fatalf("parking lost failure/durability: %+v %v", result, err)
	}
}

func TestContinuationFailedDeadlineParkingRetainsTimeout(t *testing.T) {
	now := time.Now().UTC()
	backend := parkFailureBackend{newDiscoveryMemoryBackend()}
	server := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	defer server.Close()
	options := Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup"}}
	result, err := ContinueCycle(context.Background(), source.Resolver{APIBase: server.URL}, backend, discoveryTestSnapshot(now), options, func(context.Context, ModuleCycle) (string, error) { return backend.head, context.DeadlineExceeded }, "deadline-park", "", time.Time{})
	if !errors.Is(err, context.DeadlineExceeded) || result.Deferred || !result.ContinuationDurable || result.ContinuationID == "" {
		t.Fatalf("deadline parking lost timeout/durability: %+v %v", result, err)
	}
}
