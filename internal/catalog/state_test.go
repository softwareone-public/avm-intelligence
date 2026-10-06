package catalog

import (
	"fmt"

	"strings"
	"testing"
	"time"
)

func TestAcceptSnapshotRetiresAfterTwoSuccessfulObservations(t *testing.T) {
	t.Parallel()
	firstTime := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	all := stateTestModules(12)
	initialCandidate := stateTestSnapshot(all, firstTime)
	state, delta, err := AcceptSnapshot(nil, initialCandidate, firstTime.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if state.Generation != 1 || len(delta.Added) != len(all) {
		t.Fatalf("initial state=%d added=%d", state.Generation, len(delta.Added))
	}

	missingID := all[0].ID
	withoutFirst := append([]Module(nil), all[1:]...)
	secondCandidate := stateTestSnapshot(withoutFirst, firstTime.Add(time.Minute))
	state, delta, err = AcceptSnapshot(&state, secondCandidate, firstTime.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	record := findRecord(t, state, missingID)
	if !record.Active || record.MissingObservations != 1 || !contains(delta.PendingRemoval, missingID) {
		t.Fatalf("first absence was not pending: record=%#v delta=%#v", record, delta)
	}

	thirdCandidate := stateTestSnapshot(withoutFirst, firstTime.Add(2*time.Minute))
	state, delta, err = AcceptSnapshot(&state, thirdCandidate, firstTime.Add(2*time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	record = findRecord(t, state, missingID)
	if record.Active || record.MissingObservations != RequiredMissingObservations || !contains(delta.Retired, missingID) {
		t.Fatalf("second absence did not retire module: record=%#v delta=%#v", record, delta)
	}
}

func TestAcceptSnapshotRejectsSuspiciousShrink(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	previous, _, err := AcceptSnapshot(nil, stateTestSnapshot(stateTestModules(12), now), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	candidate := stateTestSnapshot(stateTestModules(10), now.Add(time.Minute))
	if _, _, err = AcceptSnapshot(&previous, candidate, now.Add(time.Minute+time.Second)); err == nil || !strings.Contains(err.Error(), "suspicious res catalog shrink") {
		t.Fatalf("shrink error = %v", err)
	}
}

func TestAcceptSnapshotNoOpDoesNotAdvanceGeneration(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	candidate := stateTestSnapshot(stateTestModules(3), now)
	previous, _, err := AcceptSnapshot(nil, candidate, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	next := stateTestSnapshot(stateTestModules(3), now.Add(time.Minute))
	state, delta, err := AcceptSnapshot(&previous, next, now.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !delta.NoChange || state.Generation != previous.Generation {
		t.Fatalf("no-op changed state: generation %d -> %d, delta=%#v", previous.Generation, state.Generation, delta)
	}
}

func stateTestModules(resourceCount int) []Module {
	modules := make([]Module, 0, resourceCount+2)
	for index := 0; index < resourceCount; index++ {
		id := fmt.Sprintf("avm-res-test-resource-%02d", index)
		modules = append(modules, Module{
			ID: id, Type: Resource, Status: "Available", DisplayName: "Test Resource", Description: "test resource",
			RepositoryURL: "https://github.com/Azure/terraform-azurerm-" + id,
			RegistryURL:   "https://registry.terraform.io/modules/Azure/" + id,
		})
	}
	modules = append(modules,
		Module{ID: "avm-ptn-test-pattern", Type: Pattern, Status: "Proposed", DisplayName: "Test Pattern", Description: "test pattern", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-ptn-test-pattern", RegistryURL: "https://registry.terraform.io/modules/Azure/avm-ptn-test-pattern"},
		Module{ID: "avm-utl-test-utility", Type: Utility, Status: "Available", DisplayName: "Test Utility", Description: "test utility", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-utl-test-utility", RegistryURL: "https://registry.terraform.io/modules/Azure/avm-utl-test-utility"},
	)
	return modules
}

func stateTestSnapshot(modules []Module, fetchedAt time.Time) Snapshot {
	feeds := make(map[Kind][]byte, len(OfficialSources))
	for _, source := range OfficialSources {
		var ids []string
		for _, module := range modules {
			if module.Type == source.Kind {
				ids = append(ids, module.ID)
			}
		}
		feeds[source.Kind] = []byte(strings.Join(ids, "\n"))
	}
	return buildSnapshot(feeds, modules, fetchedAt)
}

func findRecord(t *testing.T, state AcceptedState, id string) ModuleRecord {
	t.Helper()
	for _, record := range state.Modules {
		if record.Module.ID == id {
			return record
		}
	}
	t.Fatalf("module record %s not found", id)
	return ModuleRecord{}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
