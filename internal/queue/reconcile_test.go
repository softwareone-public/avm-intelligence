package queue

import (
	"strings"
	"testing"
	"time"
)

func TestReconcileFleetRecoversPendingRequestIntoBatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	input := reconcileInput("avm-res-demo", strings.Repeat("a", 40))
	if _, _, err := Register(dir, input, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(dir, input.ModuleID, mustFingerprint(t, input), "run-1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	newInput := input
	newInput.SourceSHA = strings.Repeat("b", 40)
	if _, _, err := Register(dir, newInput, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	plan, err := ReconcileFleet(dir, []RunObservation{{ModuleID: input.ModuleID, State: RunCompleted}}, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 1 || plan.Items[0].Action != ReconcileRecovered || plan.Items[0].Status != StatusQueued {
		t.Fatalf("reconciliation items = %#v", plan.Items)
	}
	if len(plan.Batches) != 1 || len(plan.Batches[0].ModuleIDs) != 1 || plan.Batches[0].ModuleIDs[0] != input.ModuleID {
		t.Fatalf("reconciliation batches = %#v", plan.Batches)
	}
}

func TestReconcileFleetRequiresExplicitStateForRunningRequest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	input := reconcileInput("avm-res-demo", strings.Repeat("a", 40))
	if _, _, err := Register(dir, input, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(dir, input.ModuleID, mustFingerprint(t, input), "run-1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileFleet(dir, nil, now.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "missing run observation") {
		t.Fatalf("missing observation error = %v", err)
	}
}

func reconcileInput(moduleID, sourceSHA string) AnalysisInput {
	return AnalysisInput{
		ModuleID: moduleID, SourceSHA: sourceSHA, CLIVersion: "0.1.0", CLICommit: strings.Repeat("b", 40),
		BinaryDigest: strings.Repeat("c", 64), PolicyDigest: strings.Repeat("d", 64), ReportSchemaVersion: 2, AdapterVersion: 1,
	}
}

func mustFingerprint(t *testing.T, input AnalysisInput) string {
	t.Helper()
	fingerprint, err := input.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}
