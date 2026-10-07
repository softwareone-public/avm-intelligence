package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

// checkpointCountingBackend distinguishes cursor advancement from intermediate pending writes.
type checkpointCountingBackend struct {
	*memoryBackend
	checkpoints int
	pending     int
}

func (backend *checkpointCountingBackend) Commit(ctx context.Context, head string, snapshot state.Snapshot) (string, error) {
	for path, record := range snapshot.Records {
		if record.Kind != "discovery_cycle" || string(record.Data) == string(backend.snapshot.Records[path].Data) {
			continue
		}
		var cursor Continuation
		if err := json.Unmarshal(record.Data, &cursor); err != nil {
			return "", err
		}
		backend.checkpoints++
		if cursor.PendingDelivery {
			backend.pending++
		}
	}
	return backend.memoryBackend.Commit(ctx, head, snapshot)
}

// skipCycleFixture seeds existing source work and measures only the subsequent cycle.
type skipCycleFixture struct {
	backend       *checkpointCountingBackend
	options       Options
	candidate     catalog.Snapshot
	resolver      source.Resolver
	releaseChecks int
	deliveries    int
	skipChecks    int
}

func newSkipCycleFixture(t *testing.T, newRelease bool) *skipCycleFixture {
	t.Helper()
	now := time.Now().UTC()
	fixture := &skipCycleFixture{backend: &checkpointCountingBackend{memoryBackend: newDiscoveryMemoryBackend()},
		options: Options{AcceptedAt: now, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}, SelectedModuleIDs: []string{"avm-res-resources-resourcegroup", "avm-res-resources-storage"}}, candidate: discoveryTestSnapshot(now)}
	versionServer := newVersionServer(t, "v1.0.0", strings.Repeat("a", 40), "", "")
	t.Cleanup(versionServer.Close)
	if _, err := Cycle(context.Background(), source.Resolver{APIBase: versionServer.URL}, fixture.backend, fixture.candidate, fixture.options, fixture.deliver); err != nil {
		t.Fatal(err)
	}
	fixture.deliveries = 0
	if newRelease {
		versionServer = newVersionServer(t, "v2.0.0", strings.Repeat("b", 40), "v1.0.0", strings.Repeat("a", 40))
		t.Cleanup(versionServer.Close)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/releases") {
			fixture.releaseChecks++
		}
		versionServer.Config.Handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	fixture.resolver = source.Resolver{APIBase: server.URL}
	fixture.candidate.FetchedAt = now.Add(time.Minute)
	fixture.options.AcceptedAt = fixture.candidate.FetchedAt
	return fixture
}

func (fixture *skipCycleFixture) deliver(_ context.Context, module ModuleCycle) (string, error) {
	fixture.deliveries++
	return module.Head, nil
}

func (fixture *skipCycleFixture) skip(_ context.Context, module ModuleCycle) (bool, error) {
	fixture.skipChecks++
	if module.Head != fixture.backend.head || len(module.Snapshot.Records) == 0 {
		return false, errors.New("skip did not receive the live source-check snapshot")
	}
	return true, nil
}

func (fixture *skipCycleFixture) assertComplete(t *testing.T, result Result, err error) {
	t.Helper()
	total := len(fixture.options.SelectedModuleIDs)
	if err != nil || !result.ContinuationComplete || len(result.CompletedModuleIDs) != total || fixture.releaseChecks != total || len(result.CheckedModuleIDs) != total {
		t.Fatalf("cycle lost source checks or completion: %+v checks=%d error=%v", result, fixture.releaseChecks, err)
	}
	cursor, err := ReadContinuation(fixture.backend.snapshot, result.ContinuationID)
	if err != nil || cursor.PendingDelivery || cursor.Next != total || !cursor.LeaseExpiresAt.IsZero() {
		t.Fatalf("terminal cursor is inconsistent: %+v %v", cursor, err)
	}
}

func TestContinuationUnchangedSkipRetainsReleaseChecksAndSingleCursorWrite(t *testing.T) {
	fixture := newSkipCycleFixture(t, false)
	result, err := ContinueCycle(context.Background(), fixture.resolver, fixture.backend, fixture.candidate, fixture.options, fixture.deliver, "skip-run", "", time.Time{}, fixture.skip)
	fixture.assertComplete(t, result, err)
	total := len(fixture.options.SelectedModuleIDs)
	if fixture.deliveries != 0 || fixture.skipChecks != total || fixture.backend.pending != 0 || fixture.backend.checkpoints != total+1 || len(result.SkippedModuleIDs) != total || len(result.ChangedModuleIDs) != 0 {
		t.Fatalf("unchanged work was repeated: fixture=%+v result=%+v", fixture, result)
	}
}

func TestContinuationSkipNeverSuppressesNewReleaseOrForce(t *testing.T) {
	for _, scenario := range []string{"new_release", "force"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newSkipCycleFixture(t, scenario == "new_release")
			if scenario == "force" {
				fixture.options.ForceModuleIDs = fixture.options.SelectedModuleIDs
			}
			result, err := ContinueCycle(context.Background(), fixture.resolver, fixture.backend, fixture.candidate, fixture.options, fixture.deliver, "changed-run", "", time.Time{}, fixture.skip)
			fixture.assertComplete(t, result, err)
			total := len(fixture.options.SelectedModuleIDs)
			if fixture.skipChecks != 0 || fixture.deliveries != total || fixture.backend.pending != total || len(result.SkippedModuleIDs) != 0 {
				t.Fatalf("new or forced source skipped: %+v", result)
			}
			if scenario == "new_release" && len(result.ChangedModuleIDs) != total {
				t.Fatal("new releases were not counted")
			}
		})
	}
}

func TestContinuationSkipDoesNotReplayPendingSource(t *testing.T) {
	fixture := newSkipCycleFixture(t, false)
	deliver := func(ctx context.Context, module ModuleCycle) (string, error) {
		head, err := fixture.deliver(ctx, module)
		if fixture.deliveries == 1 {
			return head, &ratelimit.DeferredError{Resource: "fixture", RetryAt: time.Now().Add(-time.Second)}
		}
		return head, err
	}
	skip := func(context.Context, ModuleCycle) (bool, error) { fixture.skipChecks++; return false, nil }
	result, err := ContinueCycle(context.Background(), fixture.resolver, fixture.backend, fixture.candidate, fixture.options, deliver, "pending-run", "", time.Time{}, skip)
	if err != nil || !result.Deferred {
		t.Fatalf("pending delivery did not park: %+v %v", result, err)
	}
	checksBefore := fixture.releaseChecks
	result, err = ContinueCycle(context.Background(), fixture.resolver, fixture.backend, catalog.Snapshot{}, fixture.options, deliver, "resume-run", result.ContinuationID, time.Time{}, skip)
	fixture.assertComplete(t, result, err)
	if fixture.releaseChecks != checksBefore+1 || fixture.skipChecks != len(fixture.options.SelectedModuleIDs) {
		t.Fatal("pending resume repeated source resolution or offered pending work to skip")
	}
}

func TestContinuationSkipRejectsConcurrentHeadMovement(t *testing.T) {
	fixture := newSkipCycleFixture(t, false)
	skip := func(ctx context.Context, module ModuleCycle) (bool, error) {
		skipped, err := fixture.skip(ctx, module)
		fixture.backend.head = "concurrent-head"
		return skipped, err
	}
	result, err := ContinueCycle(context.Background(), fixture.resolver, fixture.backend, fixture.candidate, fixture.options, fixture.deliver, "stale-run", "", time.Time{}, skip)
	if !errors.Is(err, state.ErrHeadMoved) || fixture.deliveries != 0 {
		t.Fatalf("stale skip advanced: %+v %v", result, err)
	}
	cursor, err := ReadContinuation(fixture.backend.snapshot, result.ContinuationID)
	if err != nil || cursor.Next != 0 || cursor.PendingDelivery {
		t.Fatalf("failed skip changed durable cursor: %+v %v", cursor, err)
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
