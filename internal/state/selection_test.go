package state_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestSelectAcceptedAtomicallyPersistsReceiptAndExportDespiteOtherTrackLease(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	input := selectionInput(request, completionID)

	result, err := state.SelectAccepted(context.Background(), backend, "head-0", input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Sequence != 1 || result.Receipt.DesiredGeneration != request.Work.DesiredGeneration {
		t.Fatalf("selection receipt = %+v, want first sequence at current desired generation", result.Receipt)
	}
	if result.Export.SelectionID != result.Receipt.ID || result.Export.Status != state.ExportPending {
		t.Fatalf("selection export = %+v, want pending export for the committed receipt", result.Export)
	}
	if backend.commitCount != 1 {
		t.Fatalf("selection committed %d state transactions, want one atomic transaction", backend.commitCount)
	}

	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	selectionRecords, exportRecords := 0, 0
	for _, record := range snapshot.Records {
		switch record.Kind {
		case "selection":
			selectionRecords++
		case "export":
			exportRecords++
		}
	}
	if selectionRecords != 2 || exportRecords != 1 {
		t.Fatalf("committed selection records=%d exports=%d, want two selection records and one export intent", selectionRecords, exportRecords)
	}
	pending, err := state.PendingSelectionExports(snapshot)
	if err != nil || len(pending) != 1 || pending[0].SelectionID != result.Receipt.ID {
		t.Fatalf("pending exports = %+v, error %v, want the committed selection", pending, err)
	}
	delete(snapshot.Records, "state/exports/"+result.Receipt.ID+".json")
	if _, err = state.PendingSelectionExports(snapshot); !errors.Is(err, state.ErrSelectionStateMissing) {
		t.Fatalf("current selection without an export error = %v, want recovery review", err)
	}
}

func TestSelectAcceptedAdvancesSequenceForNewerContextGeneration(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	first, err := state.SelectAccepted(context.Background(), backend, backend.head, selectionInput(request, completionID))
	if err != nil {
		t.Fatal(err)
	}

	newer := acceptedAssessmentRequestWithFacts(t, request.ModuleID, request.Work.ContextID, request.Work.DesiredGeneration+1, request.UpdatedAt.Add(3*time.Minute), "selection-updated")
	if err = addRequest(backend, newer); err != nil {
		t.Fatal(err)
	}
	seedEvidenceAcceptanceReceipt(t, &backend.snapshot, newer)
	second, err := state.SelectAccepted(context.Background(), backend, backend.head, selectionInput(newer, newer.Work.Completion.ID))
	if err != nil {
		t.Fatal(err)
	}
	if second.Receipt.Sequence != first.Receipt.Sequence+1 || second.Receipt.DesiredGeneration != newer.Work.DesiredGeneration {
		t.Fatalf("second selection = %+v, want next sequence for newer context generation", second.Receipt)
	}
}

func TestImportEvidenceReceiptAcceptsHistoryWithoutSelectingUntilCAS(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	pending := request
	pending.Status = queue.StatusAwaitingAcceptance
	pending.LastAcceptedFingerprint = ""
	pending.LastAcceptedSourceSHA = ""
	if err := addRequest(backend, pending); err != nil {
		t.Fatal(err)
	}
	for recordPath, record := range backend.snapshot.Records {
		if record.Kind != "publication" {
			continue
		}
		var header struct {
			RecordType string `json:"record_type"`
		}
		if err := json.Unmarshal(record.Data, &header); err == nil && header.RecordType == "evidence_acceptance" {
			delete(backend.snapshot.Records, recordPath)
		}
	}
	proposalID := selectionTestID(t, "proposal", struct {
		RequestID string `json:"request_id"`
	}{request.ID})
	input := state.EvidenceAcceptanceInput{
		ProposalID: proposalID, ProposalKind: "module_evidence", ModuleID: request.ModuleID,
		ContextID: request.Work.ContextID, RequestID: request.ID, CompletionID: completionID,
		Phase: string(request.Work.Phase), DesiredGeneration: request.Work.DesiredGeneration,
		SourceSHA: request.SourceSHA, FactsID: request.Work.FactsID,
		AnalysisID: "analysis-" + strings.Repeat("d", 64), InputFingerprint: request.Fingerprint,
		ArtifactDigest: request.Work.Completion.ArtifactDigest, EvidenceCommit: strings.Repeat("e", 40),
		AcceptedAt: request.UpdatedAt,
	}
	head, _ := readSelectionTestSnapshot(t, backend)
	accepted, err := state.ImportEvidenceReceipt(context.Background(), backend, head, input)
	if err != nil {
		t.Fatal(err)
	}
	assertEvidenceImportRetryIsIdempotent(t, backend, accepted, input)
	_, snapshot := readSelectionTestSnapshot(t, backend)
	requestRecord := snapshot.Records["state/requests/"+request.ID+".json"]
	var stored queue.Request
	if err = json.Unmarshal(requestRecord.Data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status != queue.StatusAccepted || stored.LastAcceptedFingerprint != request.Fingerprint || accepted.Receipt.EvidenceCommit != input.EvidenceCommit {
		t.Fatalf("evidence import status=%s receipt=%+v", stored.Status, accepted.Receipt)
	}
	if _, exists := snapshot.Records[selectionPointerPathForTest(request.ModuleID, request.Work.ContextID)]; exists {
		t.Fatal("evidence merge advanced current selection before SelectAccepted CAS")
	}
	selected, err := state.SelectAccepted(context.Background(), backend, accepted.Head, selectionInput(request, completionID))
	if err != nil {
		t.Fatalf("selection after accepted evidence receipt: %v", err)
	}
	if selected.Receipt.AnalysisID == nil || *selected.Receipt.AnalysisID != accepted.Receipt.AnalysisID {
		t.Fatalf("selected analysis does not match imported evidence: %+v", selected.Receipt)
	}
}

func assertEvidenceImportRetryIsIdempotent(t *testing.T, backend *selectionBackend, accepted state.EvidenceAcceptanceResult, input state.EvidenceAcceptanceInput) {
	t.Helper()
	retried, err := state.ImportEvidenceReceipt(context.Background(), backend, accepted.Head, input)
	if err != nil {
		t.Fatalf("duplicate evidence import was not idempotent: %v", err)
	}
	if retried.Changed || retried.Head != accepted.Head || retried.Receipt.ID != accepted.Receipt.ID {
		t.Fatalf("duplicate evidence import changed state: first=%+v retry=%+v", accepted, retried)
	}
}

func TestSelectAcceptedRejectsRetiredModule(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	retireCatalogModule(t, backend, request.ModuleID)
	if _, err := state.SelectAccepted(context.Background(), backend, backend.head, selectionInput(request, completionID)); !errors.Is(err, state.ErrSelectionIneligible) {
		t.Fatalf("retired module selection error = %v, want ErrSelectionIneligible", err)
	}
	if backend.commitCount != 0 {
		t.Fatalf("retired module selection committed state; commits=%d", backend.commitCount)
	}
}

func TestSelectAcceptedMatchesItsTrackPinWithoutCrossTrackLeaseOrPinFencing(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	seedTrackPinHistory(t, backend, request.ModuleID, map[string]string{
		request.Work.ContextID: request.SourceSHA,
		"azurerm/5":            strings.Repeat("c", 40),
	}, strings.Repeat("c", 40))
	if _, err := state.SelectAccepted(context.Background(), backend, backend.head, selectionInput(request, completionID)); err != nil {
		t.Fatalf("v5 pin incorrectly fenced v4 selection: %v", err)
	}

	staleBackend, staleRequest, staleCompletionID := selectionFixture(t, false)
	seedTrackPinHistory(t, staleBackend, staleRequest.ModuleID, map[string]string{
		staleRequest.Work.ContextID: strings.Repeat("c", 40),
		"azurerm/5":                 staleRequest.SourceSHA,
	}, staleRequest.SourceSHA)
	if _, err := state.SelectAccepted(context.Background(), staleBackend, staleBackend.head, selectionInput(staleRequest, staleCompletionID)); !errors.Is(err, state.ErrSelectionStale) {
		t.Fatalf("selection with changed current track pin = %v, want ErrSelectionStale", err)
	}
}

func TestDeselectRetiredWritesHigherSequenceNullReceiptAndRecoversLostAck(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	selected, err := state.SelectAccepted(context.Background(), backend, backend.head, selectionInput(request, completionID))
	if err != nil {
		t.Fatal(err)
	}
	retireCatalogModule(t, backend, request.ModuleID)
	input := state.DeselectRetiredInput{
		ModuleID: request.ModuleID, ContextID: request.Work.ContextID,
		DesiredGeneration: request.Work.DesiredGeneration + 1,
		InputFingerprint:  strings.Repeat("d", 64), EvidenceCommit: strings.Repeat("e", 40),
		SelectedAt: request.UpdatedAt.Add(3 * time.Minute),
	}
	backend.failAfterCommit = true
	if _, err = state.DeselectRetired(context.Background(), backend, backend.head, input); !errors.Is(err, errSelectionAcknowledgmentLost) {
		t.Fatalf("first deselection error = %v, want lost acknowledgment", err)
	}
	head, snapshot := readSelectionTestSnapshot(t, backend)
	recovered, err := state.DeselectRetired(context.Background(), backend, head, input)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Changed || recovered.Receipt.Sequence != selected.Receipt.Sequence+1 || recovered.Receipt.AnalysisID != nil || recovered.Receipt.SourceSHA != nil || recovered.Receipt.FactsID != nil {
		t.Fatalf("recovered deselection = %+v, want same higher-sequence null receipt", recovered)
	}
	pending, err := state.PendingSelectionExports(snapshot)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending selection exports=%d error=%v, want original and retirement exports", len(pending), err)
	}
	if _, err = state.SelectAccepted(context.Background(), backend, head, selectionInput(request, completionID)); !errors.Is(err, state.ErrSelectionIneligible) {
		t.Fatalf("late assessment selection after retirement = %v, want ErrSelectionIneligible", err)
	}
}

func TestProviderSnapshotImportAllowsNoDependentModules(t *testing.T) {
	backend, request, _ := selectionFixture(t, false)
	snapshotID := selectionTestID(t, "snapshot", struct {
		Fixture string `json:"fixture"`
	}{"provider-without-dependents"})
	head, _ := readSelectionTestSnapshot(t, backend)
	result := importSelectionTestInput(t, backend, head, state.AssessmentInputImport{
		Kind: state.InputProviderSnapshot, InputKey: "registry.terraform.io/hashicorp/azurerm",
		InputID: snapshotID, ImportedAt: request.UpdatedAt.Add(time.Minute),
	})
	if !result.Changed || len(result.Generations) != 0 || backend.commitCount != 1 {
		t.Fatalf("zero-dependent provider import = %+v commits=%d, want one independent import receipt", result, backend.commitCount)
	}
}

func TestSelectAcceptedRejectsSupersededDesiredGeneration(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	newer, err := queue.NewAssessmentRequest(queue.AssessmentInput{
		ModuleID: request.ModuleID, SourceSHA: request.SourceSHA, ContextID: request.Work.ContextID,
		Track: request.Work.Track, DesiredGeneration: request.Work.DesiredGeneration + 1,
		FactsID: request.Work.FactsID, DefinitionIDs: request.Work.DefinitionIDs,
		SnapshotIDs: request.Work.SnapshotIDs, TargetVersions: request.Work.TargetVersions,
	}, request.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = addRequest(backend, newer); err != nil {
		t.Fatal(err)
	}

	if _, err = state.SelectAccepted(context.Background(), backend, backend.head, selectionInput(request, completionID)); err == nil {
		t.Fatal("selection of an older desired generation succeeded")
	}
	if backend.commitCount != 0 {
		t.Fatalf("stale selection committed %d state transactions, want none", backend.commitCount)
	}
}

func TestSelectAcceptedRecoversLostAcknowledgmentAndImportDoesNotReselect(t *testing.T) {
	backend, request, completionID := selectionFixture(t, true)
	input := selectionInput(request, completionID)

	if _, err := state.SelectAccepted(context.Background(), backend, "head-0", input); !errors.Is(err, errSelectionAcknowledgmentLost) {
		t.Fatalf("first selection error = %v, want lost acknowledgment", err)
	}
	head, _, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := state.SelectAccepted(context.Background(), backend, head, input)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Changed || retried.Receipt.Sequence != 1 || backend.commitCount != 1 {
		t.Fatalf("selection retry = changed %t sequence %d commits %d, want unchanged sequence 1 and one commit", retried.Changed, retried.Receipt.Sequence, backend.commitCount)
	}

	mergedCommit := strings.Repeat("f", 40)
	imported, err := state.ImportSelectionReceipt(context.Background(), backend, head, retried.Receipt, mergedCommit, request.UpdatedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !imported.Changed || imported.Receipt.ID != retried.Receipt.ID || imported.Receipt.Sequence != retried.Receipt.Sequence || imported.Export.Status != state.ExportMerged {
		t.Fatalf("imported selection = %+v, want same selection marked merged", imported)
	}
	if backend.commitCount != 2 {
		t.Fatalf("receipt import committed %d times, want one additional state transaction", backend.commitCount)
	}
	if _, err = state.ImportSelectionReceipt(context.Background(), backend, imported.Head, retried.Receipt, strings.Repeat("1", 40), request.UpdatedAt.Add(3*time.Minute)); err != nil {
		t.Fatalf("duplicate receipt import: %v", err)
	}
	if backend.commitCount != 2 {
		t.Fatalf("duplicate receipt import changed state; commits=%d", backend.commitCount)
	}

	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := state.PendingSelectionExports(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending selection exports = %d, want none after import", len(pending))
	}
}

func TestProviderInputImportAdvancesContextGenerationAndFencesOlderSelection(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	snapshotID := selectionTestID(t, "snapshot", struct {
		Fixture string `json:"fixture"`
	}{"provider-refresh"})
	head, _ := readSelectionTestSnapshot(t, backend)
	imported := importSelectionTestInput(t, backend, head, state.AssessmentInputImport{
		Kind: state.InputProviderSnapshot, InputKey: "registry.terraform.io/hashicorp/azurerm",
		InputID: snapshotID, Targets: []state.AssessmentInputTarget{{ModuleID: request.ModuleID, ContextID: request.Work.ContextID}},
		ImportedAt: request.UpdatedAt.Add(time.Minute),
	})
	if !imported.Changed || imported.Generations[request.ModuleID+"/"+request.Work.ContextID] != request.Work.DesiredGeneration+1 {
		t.Fatalf("provider input import = %+v, want one generation advance for its context", imported)
	}
	providerReplay := state.AssessmentInputImport{
		Kind: state.InputProviderSnapshot, InputKey: "registry.terraform.io/hashicorp/azurerm",
		InputID: snapshotID, Targets: []state.AssessmentInputTarget{{ModuleID: request.ModuleID, ContextID: request.Work.ContextID}},
		ImportedAt: request.UpdatedAt.Add(2 * time.Minute),
	}
	replayed := importSelectionTestInput(t, backend, imported.Head, providerReplay)
	if replayed.Changed || replayed.Generations[request.ModuleID+"/"+request.Work.ContextID] != request.Work.DesiredGeneration+1 {
		t.Fatalf("duplicate provider import = %+v, want original generation without a commit", replayed)
	}
	policyID := selectionTestID(t, "definition", struct {
		Fixture string `json:"fixture"`
	}{"new-policy"})
	policyImport := importSelectionTestInput(t, backend, replayed.Head, state.AssessmentInputImport{
		Kind: state.InputAssessmentPolicy, InputKey: state.AssessmentPolicyInputKey,
		InputID: policyID, Targets: []state.AssessmentInputTarget{{ModuleID: request.ModuleID, ContextID: request.Work.ContextID}},
		ImportedAt: request.UpdatedAt.Add(3 * time.Minute),
	})
	if policyImport.Generations[request.ModuleID+"/"+request.Work.ContextID] != request.Work.DesiredGeneration+2 {
		t.Fatalf("policy import generation = %+v, want a second context generation advance", policyImport.Generations)
	}

	if _, err := state.SelectAccepted(context.Background(), backend, policyImport.Head, selectionInput(request, completionID)); !errors.Is(err, state.ErrSelectionStale) {
		t.Fatalf("selection after provider invalidation = %v, want ErrSelectionStale", err)
	}
	if backend.commitCount != 2 {
		t.Fatalf("stale selection changed state; commits=%d", backend.commitCount)
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	moduleRecord := snapshot.Records["state/modules/"+request.ModuleID+".json"]
	var moduleState state.AssessmentModuleState
	if err := json.Unmarshal(moduleRecord.Data, &moduleState); err != nil {
		t.Fatal(err)
	}
	if moduleState.Contexts[request.Work.ContextID].DesiredGeneration != request.Work.DesiredGeneration+2 || len(moduleState.Contexts[request.Work.ContextID].Inputs) != 2 {
		t.Fatalf("durable context state = %+v, want provider and policy inputs at generation %d", moduleState.Contexts[request.Work.ContextID], request.Work.DesiredGeneration+2)
	}
}

func selectionTestID(t *testing.T, kind string, value any) string {
	t.Helper()
	id, err := identity.ID(kind, 1, value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func readSelectionTestSnapshot(t *testing.T, backend state.Backend) (string, state.Snapshot) {
	t.Helper()
	head, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	return head, snapshot
}

func importSelectionTestInput(t *testing.T, backend state.Backend, head string, input state.AssessmentInputImport) state.AssessmentInputImportResult {
	t.Helper()
	result, err := state.ImportAssessmentInputs(context.Background(), backend, head, input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func selectionFixture(t *testing.T, loseAcknowledgment bool) (*selectionBackend, queue.Request, string) {
	t.Helper()

	selectedAt := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	accepted := catalog.AcceptedState{
		SchemaVersion: catalog.AcceptedStateSchemaVersion,
		Generation:    1,
		AcceptedAt:    selectedAt,
		LastFetchedAt: selectedAt,
		Modules: []catalog.ModuleRecord{
			{Module: catalog.Module{ID: "avm-res-selection-test", Type: catalog.Resource, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-selection-test", DisplayName: "Selection test", Description: "Selection fixture"}, Active: true, LastSeenGeneration: 1},
			{Module: catalog.Module{ID: "avm-ptn-selection-test", Type: catalog.Pattern, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-ptn-selection-test", DisplayName: "Pattern fixture", Description: "Selection fixture"}, Active: true, LastSeenGeneration: 1},
			{Module: catalog.Module{ID: "avm-utl-selection-test", Type: catalog.Utility, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-utl-selection-test", DisplayName: "Utility fixture", Description: "Selection fixture"}, Active: true, LastSeenGeneration: 1},
		},
	}
	for _, source := range catalog.OfficialSources {
		accepted.Feeds = append(accepted.Feeds, catalog.FeedSnapshot{Kind: source.Kind, URL: source.URL, SHA256: strings.Repeat("a", 64), ModuleCount: 1})
	}
	if err := accepted.Validate(); err != nil {
		t.Fatalf("validate fixture catalog: %v", err)
	}
	catalogRecord, err := json.Marshal(struct {
		SchemaVersion int                   `json:"schema_version"`
		Accepted      catalog.AcceptedState `json:"accepted"`
	}{SchemaVersion: 1, Accepted: accepted})
	if err != nil {
		t.Fatal(err)
	}

	request := acceptedAssessmentRequest(t, "avm-res-selection-test", "azurerm/4", 4, selectedAt)
	otherTrack, err := queue.NewAssessmentRequest(queue.AssessmentInput{
		ModuleID: request.ModuleID, SourceSHA: request.SourceSHA, ContextID: "azurerm/5", Track: "5.x",
		DesiredGeneration: 8, FactsID: request.Work.FactsID, DefinitionIDs: request.Work.DefinitionIDs,
	}, selectedAt)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := queue.ClaimModuleRequest([]queue.Request{otherTrack}, otherTrack.ID, "actions-run-55", selectedAt, selectedAt.Add(time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}

	snapshot := state.NewSnapshot()
	snapshot.Records["state/discovery/catalog.json"] = state.Record{Kind: "discovery_catalog", SchemaVersion: 1, Data: catalogRecord}
	addRequestToSnapshot(t, &snapshot, request)
	seedEvidenceAcceptanceReceipt(t, &snapshot, request)
	addRequestToSnapshot(t, &snapshot, claimed[0])
	return &selectionBackend{head: "head-0", snapshot: snapshot, failAfterCommit: loseAcknowledgment}, request, request.Work.Completion.ID
}

func acceptedAssessmentRequest(t *testing.T, moduleID, contextID string, generation uint64, at time.Time) queue.Request {
	return acceptedAssessmentRequestWithFacts(t, moduleID, contextID, generation, at, "selection")
}

func acceptedAssessmentRequestWithFacts(t *testing.T, moduleID, contextID string, generation uint64, at time.Time, fixture string) queue.Request {
	t.Helper()
	factsID, err := identity.ID("facts", 1, struct {
		Fixture string `json:"fixture"`
	}{fixture})
	if err != nil {
		t.Fatal(err)
	}
	definitionID, err := identity.ID("definition", 1, struct {
		Fixture string `json:"fixture"`
	}{"selection"})
	if err != nil {
		t.Fatal(err)
	}
	request, err := queue.NewAssessmentRequest(queue.AssessmentInput{
		ModuleID: moduleID, SourceSHA: strings.Repeat("b", 40), ContextID: contextID,
		Track: "4.x", DesiredGeneration: generation, FactsID: factsID,
		DefinitionIDs: []string{definitionID},
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := queue.ClaimModuleRequest([]queue.Request{request}, request.ID, "actions-run-44", at, at.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	completionID, err := identity.ID("completion", 1, struct {
		RequestID string `json:"request_id"`
	}{request.ID})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := queue.SealCompletion(claimed[0], "actions-run-44", 1, queue.SealedCompletion{
		ID: completionID, RequestID: request.ID, InputFingerprint: request.Fingerprint,
		DesiredGeneration: generation, Fence: 1, ArtifactDigest: strings.Repeat("c", 64),
		ValidationID: "fixture-validator-v1", SealedAt: at.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := queue.AcceptCompletion(sealed, completionID, at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func selectionInput(request queue.Request, completionID string) state.SelectionInput {
	return state.SelectionInput{
		ModuleID: request.ModuleID, ContextID: request.Work.ContextID, RequestID: request.ID,
		CompletionID: completionID, AnalysisID: "analysis-" + strings.Repeat("d", 64),
		EvidenceCommit: strings.Repeat("e", 40), SelectedAt: request.UpdatedAt.Add(time.Minute),
	}
}

func addRequest(backend *selectionBackend, request queue.Request) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	backend.snapshot.Records["state/requests/"+request.ID+".json"] = state.Record{Kind: "request", SchemaVersion: 1, Data: encoded}
	return nil
}

func retireCatalogModule(t *testing.T, backend *selectionBackend, moduleID string) {
	t.Helper()
	record := backend.snapshot.Records["state/discovery/catalog.json"]
	var durable struct {
		SchemaVersion int                   `json:"schema_version"`
		Accepted      catalog.AcceptedState `json:"accepted"`
	}
	if err := json.Unmarshal(record.Data, &durable); err != nil {
		t.Fatal(err)
	}
	for index := range durable.Accepted.Modules {
		if durable.Accepted.Modules[index].Module.ID == moduleID {
			durable.Accepted.Modules[index].Active = false
			durable.Accepted.Modules[index].MissingObservations = catalog.RequiredMissingObservations
		}
	}
	encoded, err := json.Marshal(durable)
	if err != nil {
		t.Fatal(err)
	}
	record.Data = encoded
	backend.snapshot.Records["state/discovery/catalog.json"] = record
}

func selectionPointerPathForTest(moduleID, contextID string) string {
	return "state/selections/" + moduleID + "/" + contextID + "/current.json"
}

func seedTrackPinHistory(t *testing.T, backend *selectionBackend, moduleID string, tracks map[string]string, latest string) {
	t.Helper()
	type pin struct {
		Commit string `json:"commit"`
	}
	var overall *pin
	if latest != "" {
		overall = &pin{Commit: latest}
	}
	candidates := make(map[string]pin, len(tracks))
	for contextID, commit := range tracks {
		candidates[contextID] = pin{Commit: commit}
	}
	encoded, err := json.Marshal(struct {
		SchemaVersion   int            `json:"schema_version"`
		ModuleID        string         `json:"module_id"`
		OverallLatest   *pin           `json:"overall_latest"`
		TrackCandidates map[string]pin `json:"track_candidates"`
	}{SchemaVersion: 1, ModuleID: moduleID, OverallLatest: overall, TrackCandidates: candidates})
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records["state/discovery/source-pins/"+moduleID+".json"] = state.Record{Kind: "source_pins", SchemaVersion: 1, Data: encoded}
}

func addRequestToSnapshot(t *testing.T, snapshot *state.Snapshot, request queue.Request) {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records["state/requests/"+request.ID+".json"] = state.Record{Kind: "request", SchemaVersion: 1, Data: encoded}
}

func seedEvidenceAcceptanceReceipt(t *testing.T, snapshot *state.Snapshot, request queue.Request) {
	t.Helper()
	analysisID := "analysis-" + strings.Repeat("d", 64)
	proposalID := selectionTestID(t, "proposal", struct {
		RequestID string `json:"request_id"`
	}{request.ID})
	receipt, err := state.NewEvidenceAcceptanceReceipt(state.EvidenceAcceptanceInput{
		ProposalID: proposalID, ProposalKind: state.EvidenceProposalModule,
		ModuleID: request.ModuleID, ContextID: request.Work.ContextID, RequestID: request.ID,
		CompletionID: request.Work.Completion.ID, Phase: string(request.Work.Phase),
		DesiredGeneration: request.Work.DesiredGeneration, SourceSHA: request.SourceSHA,
		FactsID: request.Work.FactsID, AnalysisID: analysisID, InputFingerprint: request.Fingerprint,
		ArtifactDigest: request.Work.Completion.ArtifactDigest, EvidenceCommit: strings.Repeat("e", 40),
		AcceptedAt: request.UpdatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records["state/publications/"+receipt.ID+".json"] = state.Record{Kind: "publication", SchemaVersion: 1, Data: data}
}

type selectionBackend struct {
	head            string
	snapshot        state.Snapshot
	commitCount     int
	failAfterCommit bool
}

func (backend *selectionBackend) Read(ctx context.Context) (string, state.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return "", state.Snapshot{}, err
	}
	return backend.head, cloneSelectionSnapshot(backend.snapshot), nil
}

func (backend *selectionBackend) Commit(ctx context.Context, expectedHead string, snapshot state.Snapshot) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if backend.head != expectedHead {
		return "", state.ErrHeadMoved
	}
	backend.commitCount++
	backend.head = fmt.Sprintf("head-%d", backend.commitCount)
	backend.snapshot = cloneSelectionSnapshot(snapshot)
	if backend.failAfterCommit {
		backend.failAfterCommit = false
		return "", errSelectionAcknowledgmentLost
	}
	return backend.head, nil
}

func cloneSelectionSnapshot(snapshot state.Snapshot) state.Snapshot {
	clone := snapshot
	clone.AppliedEvents = make(map[string]string, len(snapshot.AppliedEvents))
	for eventID, digest := range snapshot.AppliedEvents {
		clone.AppliedEvents[eventID] = digest
	}
	clone.FencingTokens = make(map[string]uint64, len(snapshot.FencingTokens))
	for key, token := range snapshot.FencingTokens {
		clone.FencingTokens[key] = token
	}
	clone.Records = make(map[string]state.Record, len(snapshot.Records))
	for recordPath, record := range snapshot.Records {
		record.Data = append(json.RawMessage(nil), record.Data...)
		clone.Records[recordPath] = record
	}
	return clone
}

var errSelectionAcknowledgmentLost = errors.New("selection committed but acknowledgment was lost")
