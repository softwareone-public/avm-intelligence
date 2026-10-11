package github

import (
	"strconv"
	"testing"
	"time"
)

func TestEffectAdmissionLeavesHighCostIntentUnclaimedForCheaperNeighbor(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	cheap := costFixtureIntent(t, "avm-res-cheaper", 0, now)
	expensive := costFixtureIntent(t, "avm-res-expensive", 6, now)
	budget := &ProjectionBudget{
		RESTRemaining: 40, RESTReserve: 10, GraphQLRemaining: 70, GraphQLReserve: 10,
		SearchRemaining: 40, SearchReserve: 1, SecondaryRemaining: 150, SecondaryReserve: 40,
	}

	selected, cost, admitted, err := FirstAdmissibleProjectionIntent([]ProjectionIntentRecord{expensive, cheap}, budget, 40*time.Minute)
	if err != nil || !admitted {
		t.Fatalf("effect admission = admitted %t error %v", admitted, err)
	}
	if selected.ModuleID != cheap.ModuleID {
		t.Fatalf("selected %s, want cheaper neighbor %s", selected.ModuleID, cheap.ModuleID)
	}
	expensiveCost, err := EstimateProjectionEffectCost(expensive)
	if err != nil {
		t.Fatal(err)
	}
	if budget.AdmitEffect(expensiveCost, 40*time.Minute) {
		t.Fatalf("expensive intent unexpectedly fits remaining budget: %+v", expensiveCost)
	}
	if cost.SecondaryRequests >= expensiveCost.SecondaryRequests {
		t.Fatalf("selected cost %+v is not cheaper than expensive cost %+v", cost, expensiveCost)
	}
}

func TestColdProjectSnapshotPaginationIsReservedInAdmissionCost(t *testing.T) {
	intent := costFixtureIntent(t, "avm-res-project-pages", 0, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	cost, err := EstimateProjectionEffectCost(intent)
	if err != nil {
		t.Fatal(err)
	}
	if cost.GraphQLRequests < maxProjectionProjectItemPages+13 {
		t.Fatalf("cold-cache cost %d does not reserve up to %d Project item pages", cost.GraphQLRequests, maxProjectionProjectItemPages)
	}
	budget := &ProjectionBudget{
		RESTRemaining: 100, RESTReserve: 0, GraphQLRemaining: cost.GraphQLRequests - 1, GraphQLReserve: 0,
		SearchRemaining: 100, SearchReserve: 0, SecondaryRemaining: 1000, SecondaryReserve: 0,
	}
	if budget.AdmitEffect(cost, time.Hour) {
		t.Fatalf("admission ignored the cold-cache Project pagination reserve: %+v", cost)
	}
}

func TestEvidenceCostCannotFitPastProjectionLeaseAdmissionWindow(t *testing.T) {
	intent := costFixtureIntent(t, "avm-res-slow-evidence", 60, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	cost, err := EstimateProjectionEffectCost(intent)
	if err != nil {
		t.Fatal(err)
	}
	const leaseAdmissionWindow = 40 * time.Minute
	if cost.EstimatedDuration <= leaseAdmissionWindow {
		t.Fatalf("fixture cost %s does not exceed the claim lease admission window", cost.EstimatedDuration)
	}
	budget := &ProjectionBudget{
		RESTRemaining: 1000, GraphQLRemaining: 1000, SearchRemaining: 1000, SecondaryRemaining: 10000,
	}
	if budget.AdmitEffect(cost, leaseAdmissionWindow) {
		t.Fatalf("slow evidence effect was admitted for %s inside %s", cost.EstimatedDuration, leaseAdmissionWindow)
	}
}

func TestEvidenceCommentPageCeilingIsReservedInAdmissionCost(t *testing.T) {
	intent := costFixtureIntent(t, "avm-res-evidence-pages", 1, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	cost, err := EstimateProjectionEffectCost(intent)
	if err != nil {
		t.Fatal(err)
	}
	if cost.RESTRequests < 12+maxEvidenceCommentPages+1 {
		t.Fatalf("evidence cost reserves %d REST calls, want at least %d", cost.RESTRequests, 12+maxEvidenceCommentPages+1)
	}
}

func TestManagedLabelCleanupCeilingIsReservedInAdmissionCost(t *testing.T) {
	intent := costFixtureIntent(t, "avm-res-label-cleanup", 0, time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	cost, err := EstimateProjectionEffectCost(intent)
	if err != nil {
		t.Fatal(err)
	}
	if cost.RESTRequests < 12+maxManagedLabelCleanupPerIssue || cost.Mutations < 4+maxManagedLabelCleanupPerIssue {
		t.Fatalf("projection cost does not reserve the managed-label cleanup ceiling: %+v", cost)
	}
}

func costFixtureIntent(t *testing.T, moduleID string, evidenceCount int, now time.Time) ProjectionIntentRecord {
	t.Helper()
	intent, err := NewProjectionIntent(projectionForModule(moduleID), 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	for index := range evidenceCount {
		evidence := RunEvidence{
			ModuleID: moduleID, Phase: EvidencePhaseProjectionRecovery,
			RunID: "1-" + strconv.Itoa(index+1), RunURL: "https://github.com/owner/repository/actions/runs/1",
			SourceVersion: "1.0.0", SourceSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", StateHead: "head-0",
			Status: StatusBacklog, Outcome: EvidenceOutcomePending,
		}
		id, err := evidence.ID()
		if err != nil {
			t.Fatal(err)
		}
		if intent.PendingEvidence == nil {
			intent.PendingEvidence = make(map[string]RunEvidence)
		}
		intent.PendingEvidence[id] = evidence
	}
	if err := intent.Validate(); err != nil {
		t.Fatalf("validate cost fixture: %v", err)
	}
	return intent
}
