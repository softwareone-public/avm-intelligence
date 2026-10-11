package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/contextset"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

func TestSelectedContextCoverageRequiresCurrentRequestIdentity(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	moduleID := "avm-res-selection-coverage"
	sourceSHA := strings.Repeat("a", 40)
	factsID, err := identity.ID("facts", 1, map[string]string{"fixture": "accepted facts"})
	if err != nil {
		t.Fatal(err)
	}
	policyID, err := identity.ID("provider-policy", 1, map[string]string{"fixture": "accepted policy"})
	if err != nil {
		t.Fatal(err)
	}
	set, err := contextset.New(moduleID, sourceSHA, "v1.0.0", factsID, policyID, true, []contextset.Context{
		{State: contextset.ContextState{ContextID: "azurerm/4", Track: "4.x", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible}},
		{State: contextset.ContextState{ContextID: "azurerm/5", Track: "5.x", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestID, requestFingerprint := "current-request", strings.Repeat("d", 64)

	analysisID, err := identity.ID("analysis", 1, map[string]string{"fixture": "analysis"})
	if err != nil {
		t.Fatal(err)
	}
	completionID, err := identity.ID("completion", 1, map[string]string{"fixture": "completion"})
	if err != nil {
		t.Fatal(err)
	}
	currentReceipt := SelectionReceipt{ModuleID: moduleID, AVMReleaseVersion: "v1.0.0", ContextID: "azurerm/4"}
	prior := SelectionReceipt{
		SchemaVersion: selectionSchemaVersion, RecordType: selectionRecordType, ModuleID: moduleID,
		AVMReleaseVersion: "v1.0.0", ContextID: "azurerm/5", Sequence: 1, DesiredGeneration: 2,
		SourceSHA: &sourceSHA, FactsID: &factsID, InputFingerprint: requestFingerprint, AnalysisID: &analysisID,
		CompletionID: &completionID, EvidenceCommit: strings.Repeat("c", 40), SelectedAt: now,
	}
	prior.ID, err = identity.ID("selection", selectionSchemaVersion, selectionIdentity{
		SchemaVersion: prior.SchemaVersion, RecordType: prior.RecordType, ModuleID: prior.ModuleID,
		AVMReleaseVersion: prior.AVMReleaseVersion, ContextID: prior.ContextID, Sequence: prior.Sequence,
		DesiredGeneration: prior.DesiredGeneration, SourceSHA: prior.SourceSHA, FactsID: prior.FactsID,
		InputFingerprint: prior.InputFingerprint, AnalysisID: prior.AnalysisID, EvidenceCommit: prior.EvidenceCommit, SelectedAt: prior.SelectedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = prior.Validate(); err != nil {
		t.Fatal(err)
	}
	priorPath := selectionStatePrefix + moduleID + "/azurerm/5/" + prior.ID + ".json"
	priorRecordData, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	priorRecord := Record{Kind: "selection", SchemaVersion: selectionRecordSchemaVersion, Data: priorRecordData}
	eventID, err := identity.ID(selectionStateEventKind, 1, selectionEventIdentity{Path: priorPath, Record: priorRecord})
	if err != nil {
		t.Fatal(err)
	}
	eventDigest, err := transactionEventDigest(Event{ID: eventID, RecordPath: priorPath, Record: priorRecord})
	if err != nil {
		t.Fatal(err)
	}
	requestData, err := json.Marshal(map[string]any{
		"id": requestID, "module_id": moduleID, "source_sha": sourceSHA, "fingerprint": requestFingerprint, "status": "accepted",
		"work": map[string]any{"phase": "assess", "avm_release_version": "v1.0.0", "context_id": "azurerm/5", "desired_generation": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	pointer := SelectionPointerRecord{
		SchemaVersion: selectionRecordSchemaVersion, ModuleID: moduleID, AVMReleaseVersion: "v1.0.0", ContextID: "azurerm/5",
		Sequence: 1, SelectionID: prior.ID, RequestID: "stale-request-id", CompletionID: completionID,
		DesiredGeneration: 2, UpdatedAt: now,
	}
	pointerData, err := json.Marshal(pointer)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := NewSnapshot()
	snapshot.Records[priorPath] = priorRecord
	snapshot.Records[selectionPointerPath(moduleID, "v1.0.0", "azurerm/5")] = Record{Kind: "selection", SchemaVersion: selectionRecordSchemaVersion, Data: pointerData}
	snapshot.Records["state/requests/"+requestID+".json"] = Record{Kind: "request", SchemaVersion: 1, Data: requestData}
	snapshot.AppliedEvents[eventID] = strings.TrimPrefix(eventDigest, "state-event-")

	selected, required, unresolved, err := selectedContextCoverage(snapshot, set, currentReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if selected != 1 || required != 2 || unresolved != 0 {
		t.Fatalf("coverage counted a selection from a different request: selected=%d required=%d unresolved=%d", selected, required, unresolved)
	}
}
