package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/contextset"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

func TestRequiredContextSetAllowsOnlyApplicableEligibleContexts(t *testing.T) {
	policyID, err := identity.ID("provider-policy", 1, "selection-contextset-test")
	if err != nil {
		t.Fatal(err)
	}
	contexts := []contextset.Context{
		{State: contextset.ContextState{ContextID: "azurerm/4", Track: "4.x", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible}},
		{State: contextset.ContextState{ContextID: "azurerm/5", Track: "5.x", Applicability: contextset.ApplicabilityUnresolved, Candidate: contextset.CandidateUnresolved}, Reason: "provider snapshot is not accepted"},
	}
	set, err := contextset.New("avm-res-contextset", strings.Repeat("a", 40), "v1.0.0", "facts-"+strings.Repeat("b", 64), policyID, false, contexts)
	if err != nil {
		t.Fatal(err)
	}
	if !requiredContextSetAllows(set, "azurerm/4") {
		t.Fatal("applicable eligible context was not selectable")
	}
	if requiredContextSetAllows(set, "azurerm/5") {
		t.Fatal("unresolved provider context was selectable")
	}
	if requiredContextSetAllows(set, "azurerm/6") {
		t.Fatal("context outside the accepted set was selectable")
	}
}

func TestSelectedContextCoverageRetainsUnresolvedRequiredTrack(t *testing.T) {
	set := contextset.Set{Contexts: []contextset.Context{
		{State: contextset.ContextState{ContextID: "azurerm/4", Track: "4.x", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible}},
		{State: contextset.ContextState{ContextID: "azurerm/5", Track: "5.x", Applicability: contextset.ApplicabilityUnresolved, Candidate: contextset.CandidateUnresolved}, Reason: "accepted provider snapshot is missing"},
	}}
	receipt := SelectionReceipt{ModuleID: "avm-res-contextset", AVMReleaseVersion: "v1.0.0", ContextID: "azurerm/4"}
	selected, required, unresolved, err := selectedContextCoverage(NewSnapshot(), set, receipt)
	if err != nil || selected != 1 || required != 2 || unresolved != 1 {
		t.Fatalf("coverage selected=%d required=%d unresolved=%d error=%v, want 1/2/1 without error", selected, required, unresolved, err)
	}
}

func TestSelectionLifecycleStatusKeepsOtherRequiredQueuedContextInBacklog(t *testing.T) {
	moduleID, release := "avm-res-contextset", "v1.0.0"
	set := contextset.Set{Contexts: []contextset.Context{
		{State: contextset.ContextState{ContextID: "azurerm/4", Track: "4.x", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible}},
		{State: contextset.ContextState{ContextID: "azurerm/5", Track: "5.x", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible}},
	}}
	snapshot := NewSnapshot()
	for _, request := range []selectionRequest{
		{ID: "accepted-v4", ModuleID: moduleID, Status: "accepted", Work: &selectionWorkRequest{Phase: "assess", AVMReleaseVersion: release, ContextID: "azurerm/4", DesiredGeneration: 1}},
		{ID: "queued-v5", ModuleID: moduleID, Status: "queued", Work: &selectionWorkRequest{Phase: "assess", AVMReleaseVersion: release, ContextID: "azurerm/5", DesiredGeneration: 1}},
	} {
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Records["state/requests/"+request.ID+".json"] = Record{Kind: "request", SchemaVersion: 1, Data: encoded}
	}
	status, reason, err := selectionLifecycleStatus(snapshot, set, SelectionReceipt{ModuleID: moduleID, AVMReleaseVersion: release, ContextID: "azurerm/4"}, 1, 2, 0)
	if err != nil || status != "backlog" || reason != "required assessment context is queued" {
		t.Fatalf("selection lifecycle status = %q (%q), error=%v; want queued required context to remain Backlog", status, reason, err)
	}
}

func TestSelectedContextCoverageFailsClosedOnMissingPriorReceipt(t *testing.T) {
	set := contextset.Set{Contexts: []contextset.Context{
		{State: contextset.ContextState{ContextID: "azurerm/4", Track: "4.x", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible}},
		{State: contextset.ContextState{ContextID: "azurerm/5", Track: "5.x", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible}},
	}}
	current := SelectionReceipt{ModuleID: "avm-res-contextset", AVMReleaseVersion: "v1.0.0", ContextID: "azurerm/4"}
	priorPointer := SelectionPointerRecord{
		SchemaVersion: selectionRecordSchemaVersion, ModuleID: current.ModuleID,
		AVMReleaseVersion: current.AVMReleaseVersion, ContextID: "azurerm/5", Sequence: 1,
		SelectionID: "selection-" + strings.Repeat("a", 64), RequestID: "request-id",
		CompletionID: "completion-" + strings.Repeat("b", 64), DesiredGeneration: 1,
		UpdatedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
	}
	encoded, err := json.Marshal(priorPointer)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := NewSnapshot()
	snapshot.Records[selectionPointerPath(current.ModuleID, current.AVMReleaseVersion, priorPointer.ContextID)] = Record{Kind: "selection", SchemaVersion: selectionRecordSchemaVersion, Data: encoded}
	if _, _, _, err = selectedContextCoverage(snapshot, set, current); err == nil {
		t.Fatal("selection coverage ignored a prior pointer whose receipt is missing")
	}
}
