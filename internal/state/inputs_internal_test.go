package state

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/contextset"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
)

func TestProviderSnapshotTargetsForReceiptUsesExactEligibleProviderMajor(t *testing.T) {
	moduleID := "avm-res-targets-test"
	sourceSHA := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	factsID := "facts-" + strings.Repeat("a", 64)
	policyID, err := identity.ID("provider-policy", 1, struct {
		Fixture string `json:"fixture"`
	}{"provider-snapshot-targets"})
	if err != nil {
		t.Fatal(err)
	}
	address := "registry.terraform.io/hashicorp/azurerm"
	contexts := []contextset.Context{
		{State: contextset.ContextState{ContextID: address + "/4", Track: address + "/4", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible}, ProviderAddress: address, ProviderMajor: 4},
		{State: contextset.ContextState{ContextID: address + "/5", Track: address + "/5", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible}, ProviderAddress: address, ProviderMajor: 5},
	}
	set, err := contextset.New(moduleID, sourceSHA, "v1.2.3", factsID, policyID, true, contexts)
	if err != nil {
		t.Fatal(err)
	}
	receipt := EvidenceAcceptanceReceipt{ModuleID: moduleID, SourceSHA: sourceSHA, AVMReleaseVersion: "v1.2.3", FactsID: factsID, RequiredContexts: &set}
	targets, err := providerSnapshotTargetsForReceipt(receipt, address, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := AssessmentInputTarget{ModuleID: moduleID, AVMReleaseVersion: "v1.2.3", ContextID: address + "/4"}
	if len(targets) != 1 || targets[0] != want {
		t.Fatalf("targets = %+v, want only exact provider-major target %+v", targets, want)
	}
}

func TestAssessmentPolicyInputUsesRequiredContextPolicyIdentity(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	policyID, err := identity.ID("provider-policy", 1, struct {
		Fixture string `json:"fixture"`
	}{"assessment-policy-input"})
	if err != nil {
		t.Fatal(err)
	}
	input := AssessmentInputImport{
		Kind: InputAssessmentPolicy, InputKey: AssessmentPolicyInputKey, InputID: policyID,
		Targets:        []AssessmentInputTarget{{ModuleID: "avm-res-policy-input-test", AVMReleaseVersion: "v1.2.3", ContextID: "registry.terraform.io/hashicorp/azurerm/4"}},
		AcceptedCommit: strings.Repeat("a", 40),
		Producer:       projection.WorkflowProvenance{WorkflowID: "manual-site-publish", Commit: strings.Repeat("a", 40), RunID: "policy-input-fixture", RunAttempt: 1},
		ImportedAt:     now,
	}
	if err = validateAssessmentInputImport(input); err != nil {
		t.Fatalf("validate provider-policy input identity: %v", err)
	}
	input.InputID = strings.Replace(policyID, "provider-policy-", "definition-", 1)
	if err = validateAssessmentInputImport(input); err == nil {
		t.Fatal("definition identity was accepted as a required-context policy")
	}
}

func TestReadSelectionReceiptRejectsUnappliedSelectionHistory(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	sourceSHA := strings.Repeat("b", 40)
	factsID := "facts-" + strings.Repeat("a", 64)
	analysisID := "analysis-" + strings.Repeat("c", 64)
	completionID := "completion-" + strings.Repeat("d", 64)
	inputFingerprint := strings.Repeat("e", 64)
	receipt := SelectionReceipt{
		SchemaVersion: selectionSchemaVersion, RecordType: selectionRecordType,
		ModuleID: "avm-res-receipt-proof", AVMReleaseVersion: "v1.2.3", ContextID: "registry.terraform.io/hashicorp/azurerm/4",
		Sequence: 1, DesiredGeneration: 1, SourceSHA: &sourceSHA, FactsID: &factsID,
		AnalysisID: &analysisID, CompletionID: &completionID, InputFingerprint: inputFingerprint,
		EvidenceCommit: strings.Repeat("f", 40), SelectedAt: now,
	}
	var err error
	receipt.ID, err = identity.ID("selection", selectionSchemaVersion, selectionIdentity{
		SchemaVersion: receipt.SchemaVersion, RecordType: receipt.RecordType, ModuleID: receipt.ModuleID,
		AVMReleaseVersion: receipt.AVMReleaseVersion, ContextID: receipt.ContextID, Sequence: receipt.Sequence,
		DesiredGeneration: receipt.DesiredGeneration, SourceSHA: receipt.SourceSHA, FactsID: receipt.FactsID,
		AnalysisID: receipt.AnalysisID, InputFingerprint: receipt.InputFingerprint,
		EvidenceCommit: receipt.EvidenceCommit, SelectedAt: receipt.SelectedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	path := selectionStatePrefix + receipt.ModuleID + "/" + receipt.ContextID + "/" + receipt.ID + ".json"
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := NewSnapshot()
	snapshot.Records[path] = Record{Kind: "selection", SchemaVersion: selectionRecordSchemaVersion, Data: encoded}
	if _, err = readSelectionReceipt(snapshot, receipt.ModuleID, receipt.ContextID, receipt.ID); !errors.Is(err, ErrTransactionIdentityConflict) {
		t.Fatalf("unapplied selection receipt error = %v, want transaction identity conflict", err)
	}
}
