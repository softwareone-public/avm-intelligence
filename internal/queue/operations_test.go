package queue

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestOperationalCommandsAreAuthorizedDurableAndIdempotent(t *testing.T) {
	now := time.Date(2026, 10, 2, 21, 0, 0, 0, time.UTC)
	moduleA := "avm-res-ops-a"
	moduleB := "avm-res-ops-b"
	requestA := dispatchAcquisition(t, moduleA, 'a', 1, now)
	requestB := dispatchAcquisition(t, moduleB, 'b', 1, now.Add(time.Second))
	backend := seedOperationalBackend(t, now, requestA, requestB)
	policy := OperatorPolicy{SchemaVersion: 1, AllowedOperators: []string{"Ledermayer"}, AllowGlobalDisable: true}
	assertCommandAuthorization(t, backend, policy, moduleA, now)
	assertModulePauseResume(t, backend, policy, moduleA, moduleB, now)
	assertGlobalKillSwitch(t, backend, policy, now)
}

func assertCommandAuthorization(t *testing.T, backend state.Backend, policy OperatorPolicy, moduleID string, now time.Time) {
	t.Helper()
	unauthorized := OperationalCommand{ID: "op-unauthorized", Kind: CommandPause, Actor: "stranger", ModuleID: moduleID, Reason: "maintenance", IssuedAt: now}
	if _, err := ApplyOperationalCommand(context.Background(), backend, policy, unauthorized); !errors.Is(err, ErrUnauthorizedOperator) {
		t.Fatalf("unauthorized command error = %v, want ErrUnauthorizedOperator", err)
	}
	unknownModule := OperationalCommand{ID: "op-unknown-module", Kind: CommandPause, Actor: "Ledermayer", ModuleID: "avm-res-not-accepted", Reason: "maintenance", IssuedAt: now}
	if _, err := ApplyOperationalCommand(context.Background(), backend, policy, unknownModule); err == nil {
		t.Fatal("command for a module outside the accepted catalog was authorized")
	}
}

func assertModulePauseResume(t *testing.T, backend state.Backend, policy OperatorPolicy, moduleA, moduleB string, now time.Time) {
	t.Helper()
	pause := OperationalCommand{ID: "op-pause-a", Kind: CommandPause, Actor: "Ledermayer", ModuleID: moduleA, Reason: "maintenance window", IssuedAt: now}
	if _, err := ApplyOperationalCommand(context.Background(), backend, policy, pause); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimNext(context.Background(), backend, "dispatcher-1", now.Add(2*time.Second), time.Minute)
	if err != nil || claim.Request.ModuleID != moduleB {
		t.Fatalf("claim while one module is paused = %#v, err = %v", claim, err)
	}

	resume := OperationalCommand{ID: "op-resume-a", Kind: CommandResume, Actor: "Ledermayer", ModuleID: moduleA, Reason: "maintenance complete", IssuedAt: now.Add(3 * time.Second)}
	firstWake, err := ApplyOperationalCommand(context.Background(), backend, policy, resume)
	if err != nil || !firstWake.Changed || firstWake.WakeupID == "" {
		t.Fatalf("resume = %#v, err = %v", firstWake, err)
	}
	replayWake, err := ApplyOperationalCommand(context.Background(), backend, policy, resume)
	if err != nil || replayWake.Changed || replayWake.WakeupID != firstWake.WakeupID || replayWake.Head != firstWake.Head {
		t.Fatalf("resume replay was not idempotent: %#v, err = %v", replayWake, err)
	}
	conflictingReplay := resume
	conflictingReplay.Reason = "different command with reused ID"
	if _, err = ApplyOperationalCommand(context.Background(), backend, policy, conflictingReplay); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("conflicting command replay error = %v, want ErrOperationConflict", err)
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if countOperationRecords(snapshot, "operational_wakeup") != 1 {
		t.Fatalf("resume wakeups = %d, want exactly one", countOperationRecords(snapshot, "operational_wakeup"))
	}
}

func assertGlobalKillSwitch(t *testing.T, backend state.Backend, policy OperatorPolicy, now time.Time) {
	t.Helper()
	disable := OperationalCommand{ID: "op-global-disable", Kind: CommandGlobalDisable, Actor: "Ledermayer", Reason: "maintenance", IssuedAt: now.Add(4 * time.Second)}
	if _, err := ApplyOperationalCommand(context.Background(), backend, policy, disable); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimNext(context.Background(), backend, "dispatcher-2", now.Add(5*time.Second), time.Minute); !errors.Is(err, ErrGlobalDispatchDisabled) {
		t.Fatalf("global disable claim error = %v, want ErrGlobalDispatchDisabled", err)
	}
	enable := OperationalCommand{ID: "op-global-enable", Kind: CommandGlobalEnable, Actor: "Ledermayer", Reason: "maintenance complete", IssuedAt: now.Add(6 * time.Second)}
	if _, err := ApplyOperationalCommand(context.Background(), backend, policy, enable); err != nil {
		t.Fatal(err)
	}
	if countOperationRecordsMustRead(t, backend, "operational_wakeup") != 2 {
		t.Fatal("resume and global enable did not create independent durable wakeups")
	}
}

func TestRetryAndRefreshCommandsCreateSeparateWakeups(t *testing.T) {
	now := time.Date(2026, 10, 2, 22, 0, 0, 0, time.UTC)
	moduleID := "avm-res-ops-a"
	backend := seedOperationalBackend(t, now, dispatchAcquisition(t, moduleID, 'a', 1, now))
	policy := OperatorPolicy{SchemaVersion: 1, AllowedOperators: []string{"Ledermayer"}}
	commands := []OperationalCommand{
		{ID: "op-retry", Kind: CommandRetry, Actor: "Ledermayer", ModuleID: moduleID, Reason: "retry after upstream recovery", IssuedAt: now.Add(time.Second)},
		{ID: "op-refresh", Kind: CommandRefresh, Actor: "Ledermayer", ModuleID: moduleID, Reason: "refresh source metadata", IssuedAt: now.Add(2 * time.Second)},
	}
	for _, command := range commands {
		if _, err := ApplyOperationalCommand(context.Background(), backend, policy, command); err != nil {
			t.Fatal(err)
		}
	}
	if countOperationRecordsMustRead(t, backend, "operational_wakeup") != 2 {
		t.Fatal("retry and refresh did not persist separate durable wakeups")
	}
}

func TestReviewedOperationsPolicyLoads(t *testing.T) {
	policy, err := LoadOperatorPolicy("../../config/operations.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !policy.authorizes("ledermayer") || !policy.AllowGlobalDisable {
		t.Fatalf("loaded operations policy = %#v", policy)
	}
}

func TestInvalidOperationPolicyAndCommandIdentityFailClosed(t *testing.T) {
	policy := OperatorPolicy{SchemaVersion: 1, AllowedOperators: []string{"Ledermayer"}}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := OperationalCommand{ID: "../escape", Kind: CommandPause, Actor: "Ledermayer", ModuleID: "avm-res-ops-a", Reason: "reason", IssuedAt: time.Now().UTC()}
	if err := invalid.Validate(); err == nil {
		t.Fatal("path-like operation identity was accepted")
	}
	noReason := OperationalCommand{ID: "op-no-reason", Kind: CommandPause, Actor: "Ledermayer", ModuleID: "avm-res-ops-a", IssuedAt: time.Now().UTC()}
	if err := noReason.Validate(); err == nil {
		t.Fatal("operator command without a reason was accepted")
	}
}

func seedOperationalBackend(t *testing.T, now time.Time, requests ...Request) *dispatcherMemoryBackend {
	t.Helper()
	backend := seedDispatchBackend(t, requests...)
	modules := []catalog.Module{
		{ID: "avm-res-ops-a", Type: catalog.Resource, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-ops-a", DisplayName: "Ops A", Description: "Resource A"},
		{ID: "avm-res-ops-b", Type: catalog.Resource, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-ops-b", DisplayName: "Ops B", Description: "Resource B"},
		{ID: "avm-ptn-ops-pattern", Type: catalog.Pattern, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-avm-ptn-ops-pattern", DisplayName: "Ops Pattern", Description: "Pattern"},
		{ID: "avm-utl-ops-utility", Type: catalog.Utility, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-avm-utl-ops-utility", DisplayName: "Ops Utility", Description: "Utility"},
	}
	accepted := catalog.AcceptedState{
		SchemaVersion: catalog.AcceptedStateSchemaVersion, Generation: 1,
		AcceptedAt: now.Add(time.Second), LastFetchedAt: now,
		Feeds: []catalog.FeedSnapshot{
			{Kind: catalog.Resource, URL: catalog.OfficialSources[0].URL, SHA256: strings.Repeat("a", 64), ModuleCount: 2},
			{Kind: catalog.Pattern, URL: catalog.OfficialSources[1].URL, SHA256: strings.Repeat("b", 64), ModuleCount: 1},
			{Kind: catalog.Utility, URL: catalog.OfficialSources[2].URL, SHA256: strings.Repeat("c", 64), ModuleCount: 1},
		},
	}
	for _, module := range modules {
		accepted.Modules = append(accepted.Modules, catalog.ModuleRecord{Module: module, Active: true, LastSeenGeneration: 1})
	}
	data, err := json.Marshal(struct {
		SchemaVersion int                   `json:"schema_version"`
		Accepted      catalog.AcceptedState `json:"accepted"`
	}{SchemaVersion: 1, Accepted: accepted})
	if err != nil {
		t.Fatal(err)
	}
	event := state.Event{
		ID: "seed-operation-catalog", RecordPath: "state/discovery/catalog.json",
		Record: state.Record{Kind: "discovery_catalog", SchemaVersion: 1, Data: data},
	}
	if _, _, err = state.ApplyEvent(context.Background(), backend, event); err != nil {
		t.Fatal(err)
	}
	return backend
}

func countOperationRecords(snapshot state.Snapshot, kind string) int {
	count := 0
	for _, record := range snapshot.Records {
		if record.Kind == kind {
			count++
		}
	}
	return count
}

func countOperationRecordsMustRead(t *testing.T, backend state.Backend, kind string) int {
	t.Helper()
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	return countOperationRecords(snapshot, kind)
}
