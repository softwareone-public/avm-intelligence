package state_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/contextset"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/provider"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

func TestSelectAcceptedAtomicallyPersistsReceiptAndExportDespiteOtherTrackLease(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	input := selectionInput(request, completionID)

	result, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, input)
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
	intentPath, err := identity.StateProjectionIntentPath(request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	var intent projection.DesiredIntent
	if json.Unmarshal(snapshot.Records[intentPath].Data, &intent) != nil || intent.ValidatePath(intentPath) != nil || intent.Status != "publishing" || intent.Generation != 4 {
		t.Fatalf("selection projection intent = %+v", intent)
	}
	if !containsOwnedField(intent.OwnedFields, "selected_contexts", "1") || !containsOwnedField(intent.OwnedFields, "required_contexts", "1") || !containsOwnedField(intent.OwnedFields, "unresolved_contexts", "0") || !containsOwnedField(intent.OwnedFields, "coverage_state", "complete") {
		t.Fatalf("selection intent omitted exact partial coverage: %+v", intent.OwnedFields)
	}
	handoffCount := 0
	for _, record := range snapshot.Records {
		if record.Kind != "stage_handoff" {
			continue
		}
		var handoff projection.Handoff
		if json.Unmarshal(record.Data, &handoff) != nil || handoff.Validate() != nil {
			t.Fatal("selection wrote an invalid stage handoff")
		}
		if handoff.ConsumerStage == "publication" && handoff.Projection.IntentID == intent.IntentID {
			handoffCount++
			if handoff.AcceptedFactsID != request.Work.FactsID || handoff.Producer != input.Producer {
				t.Fatalf("selection handoff lost exact facts or producer provenance: %+v", handoff)
			}
		}
	}
	if handoffCount != 1 {
		t.Fatalf("selection publication handoffs=%d, want one", handoffCount)
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

func TestSelectionHandoffKeepsModuleProjectionGenerationAheadOfReleaseWork(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	projectionPath, err := identity.StateProjectionIntentPath(request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	moduleGeneration := request.Work.DesiredGeneration + 1
	newerIntent, err := projection.NewDesiredIntent(request.ModuleID, moduleGeneration, "discovery:newer-release", "analyzing", []projection.OwnedField{
		{Name: "active_release_version", Value: request.Work.AVMReleaseVersion},
		{Name: "source_sha", Value: request.SourceSHA},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(newerIntent)
	if err != nil {
		t.Fatal(err)
	}
	head, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	path, err := identity.StateProjectionIntentPath(request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = state.ApplyEventsAtHead(context.Background(), backend, head, []state.Event{{
		ID: "seed-newer-module-projection", RecordPath: path,
		Record: state.Record{Kind: "projection_intent", SchemaVersion: projection.SchemaVersion, Data: data},
	}}); err != nil {
		t.Fatal(err)
	}

	_, err = state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID))
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot, err = state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	var stored projection.DesiredIntent
	if err = json.Unmarshal(snapshot.Records[projectionPath].Data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Generation != moduleGeneration || stored.Status != "publishing" {
		t.Fatalf("selection projection = generation %d status %q, want generation %d publishing", stored.Generation, stored.Status, moduleGeneration)
	}
	var found bool
	for _, record := range snapshot.Records {
		if record.Kind != "stage_handoff" {
			continue
		}
		var handoff projection.Handoff
		if json.Unmarshal(record.Data, &handoff) != nil || handoff.ConsumerStage != "publication" || handoff.Projection.IntentID != stored.IntentID {
			continue
		}
		found = true
		if handoff.Generation != request.Work.DesiredGeneration || handoff.Projection.Generation != moduleGeneration || handoff.Projection.IntentID != stored.IntentID {
			t.Fatalf("selection handoff mixed identities: work generation=%d projection=%+v", handoff.Generation, handoff.Projection)
		}
	}
	if !found {
		t.Fatal("selection handoff was not persisted")
	}
}

func TestAcceptedFactsContextSetsAreReceiptBackedAndReleasePinned(t *testing.T) {
	backend, request, _ := selectionFixture(t, false)
	receipts, err := state.AcceptedFactsContextSets(backend.snapshot, request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].FactsID != request.Work.FactsID || receipts[0].RequiredContexts == nil || receipts[0].RequiredContexts.ModuleID != request.ModuleID {
		t.Fatalf("accepted facts context sets = %+v, want the operation-proven fixture receipt", receipts)
	}
	matched, err := state.CurrentSourcePinMatches(backend.snapshot, request.ModuleID, request.Work.AVMReleaseVersion, request.SourceSHA)
	if err != nil || matched {
		t.Fatalf("missing source pin matched = %t, error = %v; missing history must not prove currentness", matched, err)
	}
	pinPath, err := identity.StateSourcePinPath(request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	pinData, err := json.Marshal(struct {
		SchemaVersion  int    `json:"schema_version"`
		ModuleID       string `json:"module_id"`
		ReleaseHistory []struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"release_history"`
	}{SchemaVersion: 1, ModuleID: request.ModuleID, ReleaseHistory: []struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}{{Version: request.Work.AVMReleaseVersion, Commit: request.SourceSHA}}})
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[pinPath] = state.Record{Kind: "source_pins", SchemaVersion: 1, Data: pinData}
	matched, err = state.CurrentSourcePinMatches(backend.snapshot, request.ModuleID, request.Work.AVMReleaseVersion, request.SourceSHA)
	if err != nil || !matched {
		t.Fatalf("exact source pin matched = %t, error = %v", matched, err)
	}
	matched, err = state.CurrentSourcePinMatches(backend.snapshot, request.ModuleID, request.Work.AVMReleaseVersion, strings.Repeat("f", 40))
	if err != nil || matched {
		t.Fatalf("different source pin matched = %t, error = %v", matched, err)
	}
}

func containsOwnedField(fields []projection.OwnedField, name, value string) bool {
	for _, field := range fields {
		if field.Name == name && field.Value == value {
			return true
		}
	}
	return false
}

func TestSelectAcceptedAdvancesSequenceForNewerContextGeneration(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	first, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID))
	if err != nil {
		t.Fatal(err)
	}

	newer := acceptedAssessmentRequestWithFacts(t, request.ModuleID, request.Work.ContextID, request.Work.DesiredGeneration+1, request.UpdatedAt.Add(3*time.Minute), "selection-updated")
	if err = addRequest(backend, newer); err != nil {
		t.Fatal(err)
	}
	seedEvidenceAcceptanceReceipt(t, &backend.snapshot, newer)
	second, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(newer, newer.Work.Completion.ID))
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
	requestPath := "state/requests/" + request.ID + ".json"
	var preserved map[string]json.RawMessage
	if err := json.Unmarshal(backend.snapshot.Records[requestPath].Data, &preserved); err != nil {
		t.Fatal(err)
	}
	preserved["human_review_note"] = json.RawMessage(`"retain this note"`)
	encoded, err := json.Marshal(preserved)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[requestPath] = state.Record{Kind: "request", SchemaVersion: 1, Data: encoded}
	removeSeededEvidenceAcceptance(t, &backend.snapshot, request)
	proposalID := selectionTestID(t, "proposal", struct {
		RequestID string `json:"request_id"`
	}{request.ID})
	input := state.EvidenceAcceptanceInput{
		ProposalID: proposalID, ProposalKind: "module_evidence", ModuleID: request.ModuleID,
		ContextID: request.Work.ContextID, RequestID: request.ID, CompletionID: completionID,
		Phase: string(request.Work.Phase), AVMReleaseVersion: request.Work.AVMReleaseVersion, DesiredGeneration: request.Work.DesiredGeneration,
		SourceSHA: request.SourceSHA, FactsID: request.Work.FactsID,
		AnalysisID: "analysis-" + strings.Repeat("d", 64), InputFingerprint: request.Fingerprint,
		ArtifactDigest: request.Work.Completion.ArtifactDigest, EvidenceCommit: strings.Repeat("e", 40),
		AcceptedAt: request.UpdatedAt,
		Producer:   projection.WorkflowProvenance{WorkflowID: "manual-accept-evidence", Commit: strings.Repeat("a", 40), RunID: "test-run", RunAttempt: 1},
	}
	_, _ = readSelectionTestSnapshot(t, backend)
	accepted, err := state.ImportEvidenceReceipt(context.Background(), selectionTestCoordinator{backend: backend}, input)
	if err != nil {
		t.Fatal(err)
	}
	assertEvidenceImportRetryIsIdempotent(t, backend, accepted, input)
	_, snapshot := readSelectionTestSnapshot(t, backend)
	requestRecord := snapshot.Records[requestPath]
	var stored queue.Request
	if err = json.Unmarshal(requestRecord.Data, &stored); err != nil {
		t.Fatal(err)
	}
	var updated map[string]json.RawMessage
	if err := json.Unmarshal(requestRecord.Data, &updated); err != nil || string(updated["human_review_note"]) != string(preserved["human_review_note"]) {
		t.Fatalf("acceptance changed preserved review note: %v", err)
	}
	if stored.Status != queue.StatusAccepted || stored.LastAcceptedFingerprint != request.Fingerprint || accepted.Receipt.EvidenceCommit != input.EvidenceCommit {
		t.Fatalf("evidence import status=%s receipt=%+v", stored.Status, accepted.Receipt)
	}
	assertAcceptanceProjection(t, snapshot, input, accepted.Receipt.ID, "accepted", "selection")
	if _, exists := snapshot.Records[selectionPointerPathForTest(request.ModuleID, request.Work.ContextID)]; exists {
		t.Fatal("evidence merge advanced current selection before SelectAccepted CAS")
	}
	selectedInput := selectionInput(request, completionID)
	var selectedHandoff projection.Handoff
	for _, record := range snapshot.Records {
		if record.Kind != "stage_handoff" {
			continue
		}
		var handoff projection.Handoff
		if json.Unmarshal(record.Data, &handoff) == nil && handoff.OperationID == "evidence-acceptance:"+accepted.Receipt.ID {
			selectedInput.AcceptanceHandoffID = handoff.ID
			selectedHandoff = handoff
			break
		}
	}
	acknowledgementPath, err := identity.StateProjectionAcknowledgementPath(request.ModuleID, selectedHandoff.Projection.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	acknowledgement, err := json.Marshal(projection.Acknowledgement{
		SchemaVersion: projection.SchemaVersion, Projection: selectedHandoff.Projection,
		Status: projection.AcknowledgementDone, AcknowledgedAt: input.AcceptedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[acknowledgementPath] = state.Record{Kind: "projection_acknowledgement", SchemaVersion: projection.SchemaVersion, Data: acknowledgement}
	selected, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectedInput)
	if err != nil {
		t.Fatalf("selection after accepted evidence receipt: %v", err)
	}
	if selected.Receipt.AnalysisID == nil || *selected.Receipt.AnalysisID != accepted.Receipt.AnalysisID {
		t.Fatalf("selected analysis does not match imported evidence: %+v", selected.Receipt)
	}
}

func removeSeededEvidenceAcceptance(t *testing.T, snapshot *state.Snapshot, request queue.Request) {
	t.Helper()
	removedOperationID := ""
	for recordPath, record := range snapshot.Records {
		if record.Kind != "publication" {
			continue
		}
		var receipt state.EvidenceAcceptanceReceipt
		if json.Unmarshal(record.Data, &receipt) != nil || receipt.RecordType != "evidence_acceptance" || receipt.RequestID != request.ID {
			continue
		}
		operationID := "evidence-acceptance:" + receipt.ID
		removedOperationID = operationID
		operationPath, err := identity.StateOperationReceiptPath(request.ModuleID, operationID)
		if err != nil {
			t.Fatal(err)
		}
		var operation state.OperationReceipt
		if record, exists := snapshot.Records[operationPath]; exists && json.Unmarshal(record.Data, &operation) == nil {
			for _, entry := range operation.WriteSet {
				delete(snapshot.AppliedEvents, entry.EventID)
			}
		}
		delete(snapshot.AppliedEvents, operationID)
		delete(snapshot.Records, operationPath)
		delete(snapshot.Records, recordPath)
	}
	for recordPath, record := range snapshot.Records {
		if record.Kind != "stage_handoff" {
			continue
		}
		var handoff projection.Handoff
		if json.Unmarshal(record.Data, &handoff) != nil || handoff.ModuleID != request.ModuleID || handoff.OperationID != removedOperationID {
			continue
		}
		delete(snapshot.Records, recordPath)
		delete(snapshot.AppliedEvents, handoff.ID)
	}
	delete(snapshot.Records, "state/projection/intents/"+request.ModuleID+".json")
}

func assertAcceptanceProjection(t *testing.T, snapshot state.Snapshot, input state.EvidenceAcceptanceInput, receiptID, status, consumer string) {
	t.Helper()
	operationID := "evidence-acceptance:" + receiptID
	intentPath, err := identity.StateProjectionIntentPath(input.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	var intent projection.DesiredIntent
	if err := json.Unmarshal(snapshot.Records[intentPath].Data, &intent); err != nil || intent.ValidatePath(intentPath) != nil {
		t.Fatalf("accepted projection intent invalid: %+v, %v", intent, err)
	}
	if intent.Status != status || intent.Generation < input.DesiredGeneration || intent.TransitionID != operationID {
		t.Fatalf("accepted projection intent = %+v", intent)
	}
	identityTuple, err := intent.Identity()
	if err != nil {
		t.Fatal(err)
	}
	var handoffs int
	for recordPath, record := range snapshot.Records {
		if record.Kind != "stage_handoff" {
			continue
		}
		var handoff projection.Handoff
		if err := json.Unmarshal(record.Data, &handoff); err != nil || handoff.Validate() != nil {
			t.Fatalf("invalid handoff %s: %v", recordPath, err)
		}
		if handoff.OperationID != operationID {
			continue
		}
		handoffs++
		fingerprint, err := identity.ID("input-fingerprint", 1, input.InputFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		expectedPath, err := identity.StateProjectionHandoffPath(input.ModuleID, handoff.ID)
		if err != nil || recordPath != expectedPath || handoff.Generation != input.DesiredGeneration || handoff.Projection != identityTuple || handoff.InputFingerprint != fingerprint || handoff.ConsumerStage != consumer || handoff.Producer != input.Producer || !handoff.BarrierDeadline.Equal(input.AcceptedAt.Add(projection.BarrierTimeout)) {
			t.Fatalf("acceptance handoff tuple mismatch: %+v", handoff)
		}
	}
	wantHandoffs := 1
	if consumer == "" {
		wantHandoffs = 0
	}
	if handoffs != wantHandoffs {
		t.Fatalf("acceptance handoffs=%d, want %d", handoffs, wantHandoffs)
	}
	receiptPath, err := identity.StateOperationReceiptPath(input.ModuleID, operationID)
	if err != nil {
		t.Fatal(err)
	}
	var receipt state.OperationReceipt
	wantWrites := 3
	if consumer != "" {
		wantWrites++
	}
	if err := json.Unmarshal(snapshot.Records[receiptPath].Data, &receipt); err != nil || receipt.Validate(receiptPath) != nil || len(receipt.WriteSet) != wantWrites {
		t.Fatalf("acceptance operation proof invalid: %+v, %v", receipt, err)
	}
}

func assertEvidenceImportRetryIsIdempotent(t *testing.T, backend *selectionBackend, accepted state.EvidenceAcceptanceResult, input state.EvidenceAcceptanceInput) {
	t.Helper()
	retried, err := state.ImportEvidenceReceipt(context.Background(), selectionTestCoordinator{backend: backend}, input)
	if err != nil {
		t.Fatalf("duplicate evidence import was not idempotent: %v", err)
	}
	if retried.Changed || retried.Head != accepted.Head || retried.Receipt.ID != accepted.Receipt.ID {
		t.Fatalf("duplicate evidence import changed state: first=%+v retry=%+v", accepted, retried)
	}
}

func TestImportFactsAcceptanceDoesNotInventAssessmentInputs(t *testing.T) {
	backend, _, _ := selectionFixture(t, false)
	now := time.Date(2026, 10, 2, 13, 0, 0, 0, time.UTC)
	request, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: "avm-res-selection-test", SourceSHA: strings.Repeat("b", 40), AVMReleaseVersion: "v1.0.0", DesiredGeneration: 6,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{"hcl"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := queue.ClaimModuleRequest([]queue.Request{request}, request.ID, "run-acquire", now, now.Add(time.Hour), 3)
	if err != nil {
		t.Fatal(err)
	}
	completionID := selectionTestID(t, "completion", request.ID)
	sealed, err := queue.SealCompletion(claimed[0], "run-acquire", 3, queue.SealedCompletion{
		ID: completionID, RequestID: request.ID, InputFingerprint: request.Fingerprint,
		DesiredGeneration: 6, Fence: 3, ArtifactDigest: strings.Repeat("c", 64),
		ValidationID: "fixture", SealedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := addRequest(backend, sealed); err != nil {
		t.Fatal(err)
	}
	facts, requiredContexts := acceptedFactsContextSet(t, request.ModuleID, request.SourceSHA, request.Work.AVMReleaseVersion)
	factsID := facts.FactsID
	input := state.EvidenceAcceptanceInput{
		ProposalID: selectionTestID(t, "proposal", request.ID), ProposalKind: state.EvidenceProposalModule,
		ModuleID: request.ModuleID, RequestID: request.ID, CompletionID: completionID,
		Phase: string(queue.PhaseAcquireSource), AVMReleaseVersion: request.Work.AVMReleaseVersion, DesiredGeneration: 6, SourceSHA: request.SourceSHA,
		FactsID: factsID, InputFingerprint: request.Fingerprint, ArtifactDigest: sealed.Work.Completion.ArtifactDigest,
		EvidenceCommit: strings.Repeat("e", 40), AcceptedAt: now.Add(2 * time.Minute),
		RequiredContexts: &requiredContexts,
		Producer:         projection.WorkflowProvenance{WorkflowID: "manual-accept-evidence", Commit: strings.Repeat("a", 40), RunID: "test-run", RunAttempt: 1},
	}
	baselineRequests := 0
	for _, record := range backend.snapshot.Records {
		if record.Kind == "request" {
			baselineRequests++
		}
	}
	accepted, err := state.ImportEvidenceReceipt(context.Background(), selectionTestCoordinator{backend: backend}, input)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Receipt.RequiredContexts == nil || accepted.Receipt.RequiredContexts.ID != requiredContexts.ID || accepted.Receipt.AVMReleaseVersion != request.Work.AVMReleaseVersion {
		t.Fatalf("facts acceptance receipt did not persist its exact release context set: %+v", accepted.Receipt)
	}
	changedSet := *input.RequiredContexts
	changedSet.AVMReleaseVersion = "v2.0.0"
	changedAcceptance := input
	changedAcceptance.RequiredContexts = &changedSet
	if _, err := state.NewEvidenceAcceptanceReceipt(changedAcceptance); err == nil {
		t.Fatal("facts acceptance accepted a context set rebound to another AVM release")
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	assertAcceptanceProjection(t, snapshot, input, accepted.Receipt.ID, "facts-accepted", "")
	var durableAcceptance state.EvidenceAcceptanceReceipt
	if err := json.Unmarshal(snapshot.Records["state/publications/"+accepted.Receipt.ID+".json"].Data, &durableAcceptance); err != nil || durableAcceptance.RequiredContexts == nil || durableAcceptance.RequiredContexts.ID != requiredContexts.ID {
		t.Fatalf("durable facts acceptance omitted required context set: %+v, error %v", durableAcceptance, err)
	}
	requestCount := 0
	for _, record := range snapshot.Records {
		if record.Kind == "request" {
			requestCount++
		}
	}
	if requestCount != baselineRequests {
		t.Fatalf("facts acceptance created %d extra requests", requestCount-baselineRequests)
	}
	var stored queue.Request
	if err := json.Unmarshal(snapshot.Records["state/requests/"+request.ID+".json"].Data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status != queue.StatusFactsAccepted || stored.Work.AcceptedFactsID != factsID || stored.Work.SnapshotIDs != nil {
		t.Fatalf("facts acceptance invented inputs: %+v", stored)
	}
	assertEvidenceImportRetryIsIdempotent(t, backend, accepted, input)
	unknown := input
	unknown.ProposalID = selectionTestID(t, "proposal", "unknown-proposal")
	if _, err := state.ImportEvidenceReceipt(context.Background(), selectionTestCoordinator{backend: backend}, unknown); !errors.Is(err, state.ErrSelectionStale) {
		t.Fatalf("unknown acceptance input error = %v, want stale request", err)
	}
	changedContent := input
	changedContent.ArtifactDigest = strings.Repeat("f", 64)
	if _, err := state.ImportEvidenceReceipt(context.Background(), selectionTestCoordinator{backend: backend}, changedContent); !errors.Is(err, state.ErrSelectionStale) {
		t.Fatalf("changed evidence error = %v, want stale request", err)
	}
	changed := input
	changed.Producer.RunID = "different-run"
	changed.Producer.Commit = strings.Repeat("b", 40)
	changed.AcceptedAt = input.AcceptedAt.Add(time.Hour)
	replayed, err := state.ImportEvidenceReceipt(context.Background(), selectionTestCoordinator{backend: backend}, changed)
	if err != nil || replayed.Changed || replayed.Receipt.ID != accepted.Receipt.ID || !replayed.Receipt.AcceptedAt.Equal(accepted.Receipt.AcceptedAt) {
		t.Fatalf("retry with new run/time did not replay original acceptance: %+v %v", replayed, err)
	}
	invalidProvenance := changed
	invalidProvenance.Producer.RunID = ""
	if _, err = state.ImportEvidenceReceipt(context.Background(), selectionTestCoordinator{backend: backend}, invalidProvenance); err == nil {
		t.Fatal("acceptance replay accepted incomplete retry producer provenance")
	}
}

type acceptanceGitBackend struct {
	*selectionBackend
	builds int
}

func (backend *acceptanceGitBackend) BuildCandidate(ctx context.Context, options state.CandidateOptions) (state.GitCandidate, error) {
	if err := ctx.Err(); err != nil {
		return state.GitCandidate{}, err
	}
	backend.builds++
	return state.GitCandidate{ParentHead: options.ParentHead, CandidateID: fmt.Sprintf("%040x", backend.builds), TreeID: fmt.Sprintf("%040x", backend.builds+100), Snapshot: cloneSelectionSnapshot(options.Snapshot)}, nil
}

func (backend *acceptanceGitBackend) Publish(ctx context.Context, candidate state.GitCandidate) (state.PublicationOutcome, error) {
	head, err := backend.selectionBackend.Commit(ctx, candidate.ParentHead, candidate.Snapshot)
	if err != nil {
		return state.PublicationOutcome{Kind: state.PublicationUnknown, CandidateID: candidate.CandidateID}, err
	}
	return state.PublicationOutcome{Kind: state.PublicationCommitted, Head: head, CandidateID: candidate.CandidateID}, nil
}

func TestEvidenceAcceptanceProductionCoordinatorReplay(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	request.Status = queue.StatusAwaitingAcceptance
	request.LastAcceptedFingerprint = ""
	request.LastAcceptedSourceSHA = ""
	if err := addRequest(backend, request); err != nil {
		t.Fatal(err)
	}
	for recordPath, record := range backend.snapshot.Records {
		if record.Kind == "publication" {
			delete(backend.snapshot.Records, recordPath)
		}
	}
	newerIntent, err := projection.NewDesiredIntent(request.ModuleID, request.Work.DesiredGeneration+1, "discovery:newer-release", "analyzing", []projection.OwnedField{{Name: "source_sha", Value: request.SourceSHA}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	newerIntentData, err := json.Marshal(newerIntent)
	if err != nil {
		t.Fatal(err)
	}
	projectionPath, err := identity.StateProjectionIntentPath(request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[projectionPath] = state.Record{Kind: "projection_intent", SchemaVersion: projection.SchemaVersion, Data: newerIntentData}
	input := state.EvidenceAcceptanceInput{
		ProposalID: selectionTestID(t, "proposal", request.ID), ProposalKind: state.EvidenceProposalModule,
		ModuleID: request.ModuleID, ContextID: request.Work.ContextID, RequestID: request.ID,
		CompletionID: completionID, Phase: string(queue.PhaseAssess), DesiredGeneration: request.Work.DesiredGeneration,
		AVMReleaseVersion: request.Work.AVMReleaseVersion,
		SourceSHA:         request.SourceSHA, FactsID: request.Work.FactsID, AnalysisID: "analysis-" + strings.Repeat("d", 64),
		InputFingerprint: request.Fingerprint, ArtifactDigest: request.Work.Completion.ArtifactDigest,
		EvidenceCommit: strings.Repeat("e", 40), AcceptedAt: request.UpdatedAt,
		Producer: projection.WorkflowProvenance{WorkflowID: "manual-accept-evidence", Commit: strings.Repeat("a", 40), RunID: "test-run", RunAttempt: 1},
	}
	gitBackend := &acceptanceGitBackend{selectionBackend: backend}
	coordinator := state.DecisionCoordinator{Backend: gitBackend}
	first, err := state.ImportEvidenceReceipt(context.Background(), coordinator, input)
	if err != nil || !first.Changed {
		t.Fatalf("commit acceptance: %+v %v", first, err)
	}
	_, acceptedSnapshot := readSelectionTestSnapshot(t, backend)
	assertAcceptanceProjection(t, acceptedSnapshot, input, first.Receipt.ID, "accepted", "selection")
	laterIntent, err := projection.NewDesiredIntent(request.ModuleID, request.Work.DesiredGeneration+2, "selection:later-transition", "publishing", []projection.OwnedField{{Name: "source_sha", Value: request.SourceSHA}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	laterIntentData, err := json.Marshal(laterIntent)
	if err != nil {
		t.Fatal(err)
	}
	currentHead, _, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = state.ApplyEventsAtHead(context.Background(), backend, currentHead, []state.Event{{
		ID: "seed-later-neutral-status", RecordPath: projectionPath,
		Record: state.Record{Kind: "projection_intent", SchemaVersion: projection.SchemaVersion, Data: laterIntentData},
	}}); err != nil {
		t.Fatal(err)
	}
	second, err := state.ImportEvidenceReceipt(context.Background(), coordinator, input)
	if err != nil || second.Changed || second.Receipt != first.Receipt || backend.commitCount != 2 {
		t.Fatalf("receipt-backed replay: %+v %v, commits=%d", second, err, backend.commitCount)
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	var preserved projection.DesiredIntent
	if json.Unmarshal(snapshot.Records[projectionPath].Data, &preserved) != nil || preserved.IntentID != laterIntent.IntentID {
		t.Fatalf("acceptance replay overwrote the later module status: %+v", preserved)
	}
	changed := input
	changed.Producer.RunID = "different-run"
	changed.Producer.Commit = strings.Repeat("b", 40)
	changed.AcceptedAt = input.AcceptedAt.Add(time.Hour)
	replayed, err := state.ImportEvidenceReceipt(context.Background(), coordinator, changed)
	if err != nil || replayed.Changed || replayed.Receipt != first.Receipt {
		t.Fatalf("retry with new run/time did not replay original acceptance: %+v %v", replayed, err)
	}
	broken := backend.snapshot.Records
	operationPath, err := identity.StateOperationReceiptPath(request.ModuleID, "evidence-acceptance:"+first.Receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	receiptRecord := broken[operationPath]
	delete(broken, operationPath)
	if _, err := state.ImportEvidenceReceipt(context.Background(), coordinator, input); !errors.Is(err, state.ErrTransactionIdentityConflict) {
		t.Fatalf("orphaned publication receipt replay: %v", err)
	}
	broken[operationPath] = receiptRecord
}

func TestSelectAcceptedRejectsRetiredModule(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	retireCatalogModule(t, backend, request.ModuleID)
	if _, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID)); !errors.Is(err, state.ErrSelectionIneligible) {
		t.Fatalf("retired module selection error = %v, want ErrSelectionIneligible", err)
	}
	if backend.commitCount != 0 {
		t.Fatalf("retired module selection committed state; commits=%d", backend.commitCount)
	}
}

func TestSelectAcceptedRequiresExactAcceptanceHandoffAndProjectionBarrier(t *testing.T) {
	for _, scenario := range []string{"wrong handoff", "missing acknowledgement", "wrong acknowledgement tuple"} {
		t.Run(scenario, func(t *testing.T) {
			backend, request, completionID := selectionFixture(t, false)
			input := selectionInput(request, completionID)
			switch scenario {
			case "wrong handoff":
				input.AcceptanceHandoffID = "stage-handoff-" + strings.Repeat("f", 64)
			default:
				handoffPath, err := identity.StateProjectionHandoffPath(input.ModuleID, input.AcceptanceHandoffID)
				if err != nil {
					t.Fatal(err)
				}
				var handoff projection.Handoff
				if err = json.Unmarshal(backend.snapshot.Records[handoffPath].Data, &handoff); err != nil {
					t.Fatal(err)
				}
				ackPath, err := identity.StateProjectionAcknowledgementPath(input.ModuleID, handoff.Projection.IntentID)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "missing acknowledgement" {
					delete(backend.snapshot.Records, ackPath)
				} else {
					wrong := handoff.Projection
					wrong.Generation++
					encoded, marshalErr := json.Marshal(projection.Acknowledgement{SchemaVersion: projection.SchemaVersion, Projection: wrong, Status: projection.AcknowledgementDone, AcknowledgedAt: input.SelectedAt})
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					backend.snapshot.Records[ackPath] = state.Record{Kind: "projection_acknowledgement", SchemaVersion: 1, Data: encoded}
				}
			}
			if _, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, input); err == nil {
				t.Fatal("selection accepted without the exact predecessor proof")
			}
			if backend.commitCount != 0 {
				t.Fatalf("invalid predecessor proof committed %d transactions", backend.commitCount)
			}
		})
	}
}

func TestSelectAcceptedAllowsExactDurableBarrierRelease(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	input := selectionInput(request, completionID)
	handoffPath, err := identity.StateProjectionHandoffPath(input.ModuleID, input.AcceptanceHandoffID)
	if err != nil {
		t.Fatal(err)
	}
	var handoff projection.Handoff
	if err = json.Unmarshal(backend.snapshot.Records[handoffPath].Data, &handoff); err != nil {
		t.Fatal(err)
	}
	ackPath, err := identity.StateProjectionAcknowledgementPath(input.ModuleID, handoff.Projection.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	delete(backend.snapshot.Records, ackPath)
	release := projection.BarrierRelease{
		SchemaVersion: projection.SchemaVersion, Projection: handoff.Projection,
		Status: projection.BarrierReleased, Deadline: handoff.BarrierDeadline,
		ReleasedAt: handoff.BarrierDeadline,
	}
	encoded, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	releasePath, err := identity.StateProjectionBarrierReleasePath(input.ModuleID, handoff.Projection.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[releasePath] = state.Record{Kind: "projection_barrier_release", SchemaVersion: 1, Data: encoded}
	if _, err = state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, input); err != nil {
		t.Fatalf("exact durable barrier release did not authorize selection: %v", err)
	}
}

func TestSelectAcceptedMatchesItsTrackPinWithoutCrossTrackLeaseOrPinFencing(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	seedTrackPinHistory(t, backend, request.ModuleID, map[string]string{
		request.Work.ContextID: request.SourceSHA,
		"azurerm/5":            strings.Repeat("c", 40),
	}, strings.Repeat("c", 40))
	if _, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID)); err != nil {
		t.Fatalf("v5 pin incorrectly fenced v4 selection: %v", err)
	}

	staleBackend, staleRequest, staleCompletionID := selectionFixture(t, false)
	seedTrackPinHistory(t, staleBackend, staleRequest.ModuleID, map[string]string{
		staleRequest.Work.ContextID: strings.Repeat("c", 40),
		"azurerm/5":                 staleRequest.SourceSHA,
	}, staleRequest.SourceSHA)
	if _, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: staleBackend}, selectionInput(staleRequest, staleCompletionID)); !errors.Is(err, state.ErrSelectionStale) {
		t.Fatalf("selection with changed current track pin = %v, want ErrSelectionStale", err)
	}
}

func TestDeselectRetiredWritesHigherSequenceNullReceiptAndRecoversLostAck(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	selected, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID))
	if err != nil {
		t.Fatal(err)
	}
	retireCatalogModule(t, backend, request.ModuleID)
	input := state.DeselectRetiredInput{
		ModuleID: request.ModuleID, AVMReleaseVersion: request.Work.AVMReleaseVersion, ContextID: request.Work.ContextID,
		DesiredGeneration: request.Work.DesiredGeneration + 1,
		InputFingerprint:  strings.Repeat("d", 64), EvidenceCommit: strings.Repeat("e", 40),
		SelectedAt: request.UpdatedAt.Add(3 * time.Minute),
	}
	backend.failAfterCommit = true
	if _, err = state.DeselectRetired(context.Background(), selectionTestCoordinator{backend: backend}, input); !errors.Is(err, errSelectionAcknowledgmentLost) {
		t.Fatalf("first deselection error = %v, want lost acknowledgment", err)
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	recovered, err := state.DeselectRetired(context.Background(), selectionTestCoordinator{backend: backend}, input)
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
	lateSelection := selectionInput(request, completionID)
	lateSelection.SelectedAt = input.SelectedAt.Add(time.Minute)
	if _, err = state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, lateSelection); !errors.Is(err, state.ErrSelectionIneligible) {
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
		ModuleID: request.ModuleID, SourceSHA: request.SourceSHA, AVMReleaseVersion: request.Work.AVMReleaseVersion, ContextID: request.Work.ContextID,
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

	if _, err = state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID)); err == nil {
		t.Fatal("selection of an older desired generation succeeded")
	}
	if backend.commitCount != 0 {
		t.Fatalf("stale selection committed %d state transactions, want none", backend.commitCount)
	}
}

func TestSelectionHistoryRetainsDistinctAVMReleases(t *testing.T) {
	backend, firstRequest, firstCompletionID := selectionFixture(t, false)
	first, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(firstRequest, firstCompletionID))
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := acceptedAssessmentRequestForRelease(t, firstRequest.ModuleID, firstRequest.Work.ContextID, firstRequest.Work.DesiredGeneration+1, "v2.0.0", firstRequest.UpdatedAt.Add(3*time.Minute))
	if err = addRequest(backend, secondRequest); err != nil {
		t.Fatal(err)
	}
	seedSelectionLifecycleSlot(t, &backend.snapshot, secondRequest)
	seedEvidenceAcceptanceReceipt(t, &backend.snapshot, secondRequest)
	second, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(secondRequest, secondRequest.Work.Completion.ID))
	if err != nil {
		t.Fatal(err)
	}
	if first.Receipt.AVMReleaseVersion != "v1.0.0" || second.Receipt.AVMReleaseVersion != "v2.0.0" || first.Receipt.ID == second.Receipt.ID {
		t.Fatalf("selection receipts do not retain distinct AVM releases: first=%+v second=%+v", first.Receipt, second.Receipt)
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	for _, receipt := range []state.SelectionReceipt{first.Receipt, second.Receipt} {
		path, pathErr := identity.StateSelectionPath(receipt.ModuleID, receipt.ContextID, receipt.ID)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		var persisted state.SelectionReceipt
		if record, exists := snapshot.Records[path]; !exists || json.Unmarshal(record.Data, &persisted) != nil || persisted.ID != receipt.ID || persisted.AVMReleaseVersion != receipt.AVMReleaseVersion {
			t.Fatalf("immutable versioned selection receipt %s was not retained", receipt.ID)
		}
		pointerPath := "state/selections/" + receipt.ModuleID + "/versions/" + receipt.AVMReleaseVersion + "/" + receipt.ContextID + "/current.json"
		var pointer state.SelectionPointerRecord
		if record, exists := snapshot.Records[pointerPath]; !exists || json.Unmarshal(record.Data, &pointer) != nil || pointer.SelectionID != receipt.ID || pointer.AVMReleaseVersion != receipt.AVMReleaseVersion || pointer.Sequence != 1 {
			t.Fatalf("release-scoped selection pointer %s was not retained independently: %+v", pointerPath, pointer)
		}
	}
}

func TestSelectAcceptedRecoversLostAcknowledgmentAndImportDoesNotReselect(t *testing.T) {
	backend, request, completionID := selectionFixture(t, true)
	input := selectionInput(request, completionID)

	if _, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, input); !errors.Is(err, errSelectionAcknowledgmentLost) {
		t.Fatalf("first selection error = %v, want lost acknowledgment", err)
	}
	retried, head := retryLostSelection(t, backend, input)
	mergedCommit := strings.Repeat("f", 40)
	importedAt := request.UpdatedAt.Add(2 * time.Minute)
	imported := importAndVerifySelectionReceipt(t, backend, head, retried.Receipt, mergedCommit, importedAt)
	assertSelectionImportReplaysExactIdentity(t, backend, imported, retried.Receipt, mergedCommit, importedAt)
	assertSelectionImportRejectsChangedIdentity(t, backend, imported, retried.Receipt, request)
	assertNoPendingSelectionExports(t, backend)
}

func retryLostSelection(t *testing.T, backend *selectionBackend, input state.SelectionInput) (state.SelectionResult, string) {
	t.Helper()
	head, _, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, input)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Changed || retried.Receipt.Sequence != 1 || backend.commitCount != 1 {
		t.Fatalf("selection retry = changed %t sequence %d commits %d, want unchanged sequence 1 and one commit", retried.Changed, retried.Receipt.Sequence, backend.commitCount)
	}
	return retried, head
}

func importAndVerifySelectionReceipt(t *testing.T, backend *selectionBackend, head string, receipt state.SelectionReceipt, mergedCommit string, importedAt time.Time) state.SelectionResult {
	t.Helper()
	imported, err := state.ImportSelectionReceipt(context.Background(), selectionTestCoordinator{backend: backend}, receipt, mergedCommit, importedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !imported.Changed || imported.Receipt.ID != receipt.ID || imported.Receipt.Sequence != receipt.Sequence || imported.Export.Status != state.ExportMerged {
		t.Fatalf("imported selection = %+v, want same selection marked merged", imported)
	}
	if backend.commitCount != 2 {
		t.Fatalf("receipt import committed %d times, want one additional state transaction", backend.commitCount)
	}
	return imported
}

func assertSelectionImportReplaysExactIdentity(t *testing.T, backend *selectionBackend, imported state.SelectionResult, receipt state.SelectionReceipt, mergedCommit string, importedAt time.Time) {
	t.Helper()
	replay, err := state.ImportSelectionReceipt(context.Background(), selectionTestCoordinator{backend: backend}, receipt, mergedCommit, importedAt)
	if err != nil || replay.Changed || replay.Head != imported.Head {
		t.Fatalf("same receipt import replay = %+v err=%v", replay, err)
	}
}

func assertSelectionImportRejectsChangedIdentity(t *testing.T, backend *selectionBackend, imported state.SelectionResult, receipt state.SelectionReceipt, request queue.Request) {
	t.Helper()
	if _, err := state.ImportSelectionReceipt(context.Background(), selectionTestCoordinator{backend: backend}, receipt, strings.Repeat("1", 40), request.UpdatedAt.Add(3*time.Minute)); !errors.Is(err, state.ErrTransactionIdentityConflict) {
		t.Fatalf("receipt replay with a different merged commit = %v, want ErrTransactionIdentityConflict", err)
	}
	if _, err := state.ImportSelectionReceipt(context.Background(), selectionTestCoordinator{backend: backend}, receipt, imported.Export.MergedCommit, imported.Export.MergedAt.Add(time.Second)); !errors.Is(err, state.ErrTransactionIdentityConflict) {
		t.Fatalf("receipt replay with a different import time = %v, want ErrTransactionIdentityConflict", err)
	}
	if backend.commitCount != 2 {
		t.Fatalf("duplicate receipt import changed state; commits=%d", backend.commitCount)
	}
}

func assertNoPendingSelectionExports(t *testing.T, backend *selectionBackend) {
	t.Helper()
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

func TestSelectAcceptedNewSiteCycleReusesCurrentSelection(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	input := selectionInput(request, completionID)
	first, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, input)
	if err != nil {
		t.Fatalf("select accepted assessment: %v", err)
	}
	input.SelectedAt = input.SelectedAt.Add(time.Hour)
	second, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, input)
	if err != nil {
		t.Fatalf("reuse current accepted selection in a later site cycle: %v", err)
	}
	if second.Changed || second.Receipt.ID != first.Receipt.ID || second.Head != backend.head || backend.commitCount != 1 {
		t.Fatalf("later site-cycle selection = %+v, first=%+v commits=%d; want unchanged current receipt", second, first, backend.commitCount)
	}
}

func TestAcceptedAssessmentSelectionCandidatesWaitForExactProjectionBarrier(t *testing.T) {
	backend, request, _ := selectionFixture(t, false)
	acceptedAt := request.UpdatedAt
	beforeBarrier := acceptedAt.Add(time.Minute)
	candidates, err := state.AcceptedAssessmentSelectionCandidates(backend.snapshot, request.ModuleID, beforeBarrier)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("selection candidates before predecessor acknowledgement = %+v, error=%v; want none", candidates, err)
	}
	afterBarrier := acceptedAt.Add(16 * time.Minute)
	candidates, err = state.AcceptedAssessmentSelectionCandidates(backend.snapshot, request.ModuleID, afterBarrier)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("selection candidates after exact predecessor acknowledgement = %+v, error=%v; want one", candidates, err)
	}
	candidate := candidates[0]
	if candidate.ModuleID != request.ModuleID || candidate.ContextID != request.Work.ContextID || candidate.RequestID != request.ID || candidate.AcceptanceHandoffID == "" {
		t.Fatalf("accepted assessment candidate = %+v, want exact current request and handoff", candidate)
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
		InputID: snapshotID, Targets: []state.AssessmentInputTarget{{ModuleID: request.ModuleID, AVMReleaseVersion: request.Work.AVMReleaseVersion, ContextID: request.Work.ContextID}},
		ImportedAt: request.UpdatedAt.Add(time.Minute),
	})
	if !imported.Changed || imported.Generations[request.ModuleID+"/"+request.Work.AVMReleaseVersion+"/"+request.Work.ContextID] != request.Work.DesiredGeneration+1 {
		t.Fatalf("provider input import = %+v, want one generation advance for its context", imported)
	}
	providerReplay := state.AssessmentInputImport{
		Kind: state.InputProviderSnapshot, InputKey: "registry.terraform.io/hashicorp/azurerm",
		InputID: snapshotID, Targets: []state.AssessmentInputTarget{{ModuleID: request.ModuleID, AVMReleaseVersion: request.Work.AVMReleaseVersion, ContextID: request.Work.ContextID}},
		ImportedAt: request.UpdatedAt.Add(2 * time.Minute),
	}
	replayed := importSelectionTestInput(t, backend, imported.Head, providerReplay)
	if replayed.Changed || replayed.Generations[request.ModuleID+"/"+request.Work.AVMReleaseVersion+"/"+request.Work.ContextID] != request.Work.DesiredGeneration+1 {
		t.Fatalf("duplicate provider import = %+v, want original generation without a commit", replayed)
	}
	sameSnapshotNewAcceptedCommit := state.AssessmentInputImport{
		Kind: state.InputProviderSnapshot, InputKey: "registry.terraform.io/hashicorp/azurerm",
		InputID: snapshotID, Targets: []state.AssessmentInputTarget{{ModuleID: request.ModuleID, AVMReleaseVersion: request.Work.AVMReleaseVersion, ContextID: request.Work.ContextID}},
		AcceptedCommit: strings.Repeat("8", 40),
		Producer:       projection.WorkflowProvenance{WorkflowID: "manual-import-assessment-input", Commit: strings.Repeat("8", 40), RunID: "input-refresh-same-id", RunAttempt: 1},
		ImportedAt:     request.UpdatedAt.Add(2 * time.Minute),
	}
	reaccepted := importSelectionTestInput(t, backend, replayed.Head, sameSnapshotNewAcceptedCommit)
	if !reaccepted.Changed || reaccepted.Generations[request.ModuleID+"/"+request.Work.AVMReleaseVersion+"/"+request.Work.ContextID] != request.Work.DesiredGeneration+1 {
		t.Fatalf("same provider snapshot from a later accepted commit = %+v, want import receipt without a generation advance", reaccepted)
	}
	policyID := selectionTestID(t, "provider-policy", struct {
		Fixture string `json:"fixture"`
	}{"selection-required-contexts"})
	policyImport := importSelectionTestInput(t, backend, reaccepted.Head, state.AssessmentInputImport{
		Kind: state.InputAssessmentPolicy, InputKey: state.AssessmentPolicyInputKey,
		InputID: policyID, Targets: []state.AssessmentInputTarget{{ModuleID: request.ModuleID, AVMReleaseVersion: request.Work.AVMReleaseVersion, ContextID: request.Work.ContextID}},
		ImportedAt: request.UpdatedAt.Add(3 * time.Minute),
	})
	if policyImport.Generations[request.ModuleID+"/"+request.Work.AVMReleaseVersion+"/"+request.Work.ContextID] != request.Work.DesiredGeneration+2 {
		t.Fatalf("policy import generation = %+v, want a second context generation advance", policyImport.Generations)
	}
	_, importedSnapshot := readSelectionTestSnapshot(t, backend)
	contextInputs, inputErr := state.AssessmentContextInputs(importedSnapshot, request.ModuleID, request.Work.AVMReleaseVersion, request.Work.ContextID)
	if inputErr != nil {
		t.Fatalf("read exact receipt-backed provider and policy inputs: %v", inputErr)
	}
	if contextInputs.DesiredGeneration != request.Work.DesiredGeneration+2 || len(contextInputs.Inputs) != 2 {
		t.Fatalf("accepted context inputs = %+v, want both exact provider snapshot and policy receipts at generation %d", contextInputs, request.Work.DesiredGeneration+2)
	}
	providerReplay.Producer = projection.WorkflowProvenance{WorkflowID: "manual-import-assessment-input", Commit: strings.Repeat("9", 40), RunID: "input-import-retry", RunAttempt: 2}
	lateReplay := importSelectionTestInput(t, backend, policyImport.Head, providerReplay)
	if lateReplay.Changed || lateReplay.Generations[request.ModuleID+"/"+request.Work.AVMReleaseVersion+"/"+request.Work.ContextID] != request.Work.DesiredGeneration+1 {
		t.Fatalf("provider replay after module state advanced = %+v, want the original generation without a commit", lateReplay)
	}

	if _, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID)); !errors.Is(err, state.ErrSelectionStale) {
		t.Fatalf("selection after provider invalidation = %v, want ErrSelectionStale", err)
	}
	if backend.commitCount != 3 {
		t.Fatalf("stale selection changed state; commits=%d", backend.commitCount)
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	moduleRecord := snapshot.Records["state/modules/"+request.ModuleID+".json"]
	var moduleState state.AssessmentModuleState
	if err := json.Unmarshal(moduleRecord.Data, &moduleState); err != nil {
		t.Fatal(err)
	}
	contextState := moduleState.Versions[request.Work.AVMReleaseVersion][request.Work.ContextID]
	if contextState.DesiredGeneration != request.Work.DesiredGeneration+2 || len(contextState.Inputs) != 2 {
		t.Fatalf("durable context state = %+v, want provider and policy inputs at generation %d", contextState, request.Work.DesiredGeneration+2)
	}
}

func TestProviderInputImportIsScopedToExactAVMRelease(t *testing.T) {
	backend, request, _ := selectionFixture(t, false)
	otherVersion, err := queue.NewAssessmentRequest(queue.AssessmentInput{
		ModuleID: request.ModuleID, SourceSHA: request.SourceSHA, AVMReleaseVersion: "v2.0.0",
		ContextID: request.Work.ContextID, Track: request.Work.Track,
		DesiredGeneration: 40, FactsID: request.Work.FactsID,
		DefinitionIDs: request.Work.DefinitionIDs, SnapshotIDs: request.Work.SnapshotIDs,
		TargetVersions: request.Work.TargetVersions,
	}, request.UpdatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = addRequest(backend, otherVersion); err != nil {
		t.Fatal(err)
	}
	snapshotID := selectionTestID(t, "snapshot", struct {
		Fixture string `json:"fixture"`
	}{"release-scoped-provider-refresh"})
	head, _ := readSelectionTestSnapshot(t, backend)
	result := importSelectionTestInput(t, backend, head, state.AssessmentInputImport{
		Kind: state.InputProviderSnapshot, InputKey: "registry.terraform.io/hashicorp/azurerm",
		InputID: snapshotID, Targets: []state.AssessmentInputTarget{{
			ModuleID: request.ModuleID, AVMReleaseVersion: request.Work.AVMReleaseVersion, ContextID: request.Work.ContextID,
		}}, ImportedAt: request.UpdatedAt.Add(2 * time.Minute),
	})
	if !result.Changed || result.Generations[request.ModuleID+"/"+request.Work.AVMReleaseVersion+"/"+request.Work.ContextID] != request.Work.DesiredGeneration+1 {
		t.Fatalf("release-scoped import = %+v, want only the targeted release generation advanced", result)
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	var moduleState state.AssessmentModuleState
	if err = json.Unmarshal(snapshot.Records["state/modules/"+request.ModuleID+".json"].Data, &moduleState); err != nil {
		t.Fatal(err)
	}
	if got := moduleState.Versions[request.Work.AVMReleaseVersion][request.Work.ContextID].DesiredGeneration; got != request.Work.DesiredGeneration+1 {
		t.Fatalf("target release generation = %d, want %d", got, request.Work.DesiredGeneration+1)
	}
	if _, exists := moduleState.Versions[otherVersion.Work.AVMReleaseVersion]; exists {
		t.Fatalf("provider import for %s mutated unrelated release %s", request.Work.AVMReleaseVersion, otherVersion.Work.AVMReleaseVersion)
	}
}

func TestProviderSnapshotAssessmentTargetsUseOnlyProvenFactsAcceptanceSets(t *testing.T) {
	backend, _, _ := selectionFixture(t, false)
	targets, err := state.ProviderSnapshotAssessmentTargets(backend.snapshot, "registry.terraform.io/hashicorp/azurerm", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("provider snapshot targets = %+v, want no target without an exact provider address in the accepted context set", targets)
	}
	otherMajor, err := state.ProviderSnapshotAssessmentTargets(backend.snapshot, "registry.terraform.io/hashicorp/azurerm", 5)
	if err != nil || len(otherMajor) != 0 {
		t.Fatalf("provider major 5 targets = %+v, error %v; want no matching required context", otherMajor, err)
	}

	_, snapshot := readSelectionTestSnapshot(t, backend)
	for _, record := range snapshot.Records {
		if record.Kind != "publication" {
			continue
		}
		var receipt state.EvidenceAcceptanceReceipt
		if json.Unmarshal(record.Data, &receipt) == nil && receipt.Phase == string(queue.PhaseAcquireSource) {
			record.Data = append(record.Data, ' ')
			snapshot.Records["state/publications/"+receipt.ID+".json"] = record
			break
		}
	}
	if _, err = state.ProviderSnapshotAssessmentTargets(snapshot, "registry.terraform.io/hashicorp/azurerm", 4); err == nil {
		t.Fatal("tampered facts acceptance proof was accepted")
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
	if input.AcceptedCommit == "" {
		input.AcceptedCommit = strings.Repeat("9", 40)
	}
	if input.Producer.WorkflowID == "" {
		input.Producer = projection.WorkflowProvenance{WorkflowID: "manual-import-assessment-input", Commit: input.AcceptedCommit, RunID: "input-import-test", RunAttempt: 1}
	}
	result, err := state.ImportAssessmentInputs(context.Background(), selectionTestCoordinator{backend: backend}, input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type selectionTestCoordinator struct {
	backend state.Backend
}

func (coordinator selectionTestCoordinator) Read(ctx context.Context) (string, state.Snapshot, error) {
	return coordinator.backend.Read(ctx)
}

func (coordinator selectionTestCoordinator) RunTransaction(ctx context.Context, operationID string, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	for attempt := 0; attempt < 3; attempt++ {
		head, snapshot, err := state.ReadSnapshot(ctx, coordinator.backend)
		if err != nil {
			return state.TransactionResult{}, err
		}
		if selectionOperationApplied(snapshot, operationID) {
			resolution, resolveErr := resolve(snapshot)
			if resolveErr != nil || !resolution.Found {
				if resolveErr != nil {
					return state.TransactionResult{}, resolveErr
				}
				return state.TransactionResult{}, state.ErrTransactionIdentityConflict
			}
			return state.TransactionResult{Head: head, Value: resolution.Value}, nil
		}
		decision, err := decide(snapshot)
		if err != nil {
			return state.TransactionResult{}, err
		}
		if decision.Noop {
			return state.TransactionResult{Head: head, Value: decision.Result}, nil
		}
		updated, _, err := state.ApplyEventsAtHead(ctx, coordinator.backend, head, decision.Events)
		if errors.Is(err, state.ErrHeadMoved) && attempt < 2 {
			continue
		}
		if err != nil {
			return state.TransactionResult{}, err
		}
		return state.TransactionResult{Head: updated, Value: decision.Result, Changed: true}, nil
	}
	return state.TransactionResult{}, state.ErrTransactionConflict
}

func (coordinator selectionTestCoordinator) RunOperationTransaction(ctx context.Context, moduleID, operationID string, expected []state.Event, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	return coordinator.RunTransaction(ctx, operationID, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
		decision, err := decide(snapshot)
		if err != nil {
			return decision, err
		}
		result, err := json.Marshal(decision.Result)
		if err != nil {
			return decision, err
		}
		receipt, err := state.NewOperationReceipt(moduleID, operationID, result, expected)
		if err != nil {
			return decision, err
		}
		decision.Events = append(decision.Events, receipt)
		return decision, nil
	}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
		path, err := identity.StateOperationReceiptPath(moduleID, operationID)
		if err != nil {
			return state.TransactionResolution{}, err
		}
		var receipt state.OperationReceipt
		if err := json.Unmarshal(snapshot.Records[path].Data, &receipt); err != nil {
			return state.TransactionResolution{}, err
		}
		return state.TransactionResolution{Found: true, Value: json.RawMessage(receipt.Result)}, nil
	})
}

func selectionOperationApplied(snapshot state.Snapshot, operationID string) bool {
	if _, exists := snapshot.AppliedEvents[operationID]; exists {
		return true
	}
	for _, aggregate := range snapshot.Aggregates {
		if _, exists := aggregate.AppliedEvents[operationID]; exists {
			return true
		}
	}
	return false
}

func selectionFixture(t *testing.T, loseAcknowledgment bool) (*selectionBackend, queue.Request, string) {
	return selectionFixtureWithRequiredContexts(t, loseAcknowledgment, nil)
}

func selectionFixtureWithRequiredContexts(t *testing.T, loseAcknowledgment bool, additionalContexts []contextset.Context) (*selectionBackend, queue.Request, string) {
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
		ModuleID: request.ModuleID, SourceSHA: request.SourceSHA, AVMReleaseVersion: request.Work.AVMReleaseVersion, ContextID: "azurerm/5", Track: "5.x",
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
	seedSelectionLifecycleSlot(t, &snapshot, request)
	addRequestToSnapshot(t, &snapshot, request)
	seedEvidenceAcceptanceReceiptWithRequiredContexts(t, &snapshot, request, additionalContexts)
	addRequestToSnapshot(t, &snapshot, claimed[0])
	return &selectionBackend{head: "head-0", snapshot: snapshot, failAfterCommit: loseAcknowledgment}, request, request.Work.Completion.ID
}

func acceptedAssessmentRequest(t *testing.T, moduleID, contextID string, generation uint64, at time.Time) queue.Request {
	return acceptedAssessmentRequestWithFacts(t, moduleID, contextID, generation, at, "selection")
}

func acceptedAssessmentRequestWithFacts(t *testing.T, moduleID, contextID string, generation uint64, at time.Time, fixture string) queue.Request {
	return acceptedAssessmentRequestForReleaseWithFacts(t, moduleID, contextID, generation, "v1.0.0", at, fixture)
}

func acceptedAssessmentRequestForRelease(t *testing.T, moduleID, contextID string, generation uint64, releaseVersion string, at time.Time) queue.Request {
	return acceptedAssessmentRequestForReleaseWithFacts(t, moduleID, contextID, generation, releaseVersion, at, "selection")
}

func acceptedAssessmentRequestForReleaseWithFacts(t *testing.T, moduleID, contextID string, generation uint64, releaseVersion string, at time.Time, fixture string) queue.Request {
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
		ModuleID: moduleID, SourceSHA: strings.Repeat("b", 40), AVMReleaseVersion: releaseVersion, ContextID: contextID,
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
		CompletionID: completionID, AcceptanceHandoffID: selectionAcceptanceHandoffID(request),
		AnalysisID:     "analysis-" + strings.Repeat("d", 64),
		EvidenceCommit: strings.Repeat("e", 40), SelectedAt: request.UpdatedAt.Add(16 * time.Minute),
		Producer: projection.WorkflowProvenance{WorkflowID: "manual-site-publish", Commit: strings.Repeat("a", 40), RunID: "selection-test", RunAttempt: 1},
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
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	var overall *pin
	if latest != "" {
		overall = &pin{Version: "v2.0.0", Commit: latest}
	}
	candidates := make(map[string]pin, len(tracks))
	for contextID, commit := range tracks {
		candidates[contextID] = pin{Version: contextID, Commit: commit}
	}
	versionPins := []pin{}
	for contextID, commit := range tracks {
		if strings.HasSuffix(contextID, "/4") || contextID == "azurerm-4" {
			versionPins = append(versionPins, pin{Version: "v1.0.0", Commit: commit})
		}
	}
	encoded, err := json.Marshal(struct {
		SchemaVersion   int            `json:"schema_version"`
		ModuleID        string         `json:"module_id"`
		OverallLatest   *pin           `json:"overall_latest"`
		ReleaseHistory  []pin          `json:"release_history"`
		TrackCandidates map[string]pin `json:"track_candidates"`
	}{SchemaVersion: 1, ModuleID: moduleID, OverallLatest: overall, ReleaseHistory: versionPins, TrackCandidates: candidates})
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
	seedEvidenceAcceptanceReceiptWithRequiredContexts(t, snapshot, request, nil)
}

func seedEvidenceAcceptanceReceiptWithRequiredContexts(t *testing.T, snapshot *state.Snapshot, request queue.Request, additionalContexts []contextset.Context) {
	t.Helper()
	seedRequiredContextSetAcceptanceWithAdditionalContexts(t, snapshot, request, additionalContexts)
	if path, err := identity.StateLifecycleSlotPath(request.ModuleID); err != nil {
		t.Fatal(err)
	} else if record, exists := snapshot.Records[path]; exists {
		var slot queue.LifecycleSlot
		if record.Kind != "lifecycle_slot" || json.Unmarshal(record.Data, &slot) != nil || slot.Validate() != nil {
			t.Fatal("invalid lifecycle slot fixture")
		}
		if request.Work.DesiredGeneration > slot.DesiredGeneration {
			slot.DesiredGeneration = request.Work.DesiredGeneration
			data, marshalErr := json.Marshal(slot)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			record.Data = data
			snapshot.Records[path] = record
		}
	}
	analysisID := "analysis-" + strings.Repeat("d", 64)
	proposalID := selectionTestID(t, "proposal", struct {
		RequestID string `json:"request_id"`
	}{request.ID})
	input := state.EvidenceAcceptanceInput{
		ProposalID: proposalID, ProposalKind: state.EvidenceProposalModule,
		ModuleID: request.ModuleID, ContextID: request.Work.ContextID, RequestID: request.ID,
		CompletionID: request.Work.Completion.ID, Phase: string(request.Work.Phase),
		AVMReleaseVersion: request.Work.AVMReleaseVersion,
		DesiredGeneration: request.Work.DesiredGeneration, SourceSHA: request.SourceSHA,
		FactsID: request.Work.FactsID, AnalysisID: analysisID, InputFingerprint: request.Fingerprint,
		ArtifactDigest: request.Work.Completion.ArtifactDigest, EvidenceCommit: strings.Repeat("e", 40),
		AcceptedAt: request.UpdatedAt,
		Producer:   projection.WorkflowProvenance{WorkflowID: "manual-accept-evidence", Commit: strings.Repeat("a", 40), RunID: "test-run", RunAttempt: 1},
	}
	receipt, err := state.NewEvidenceAcceptanceReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "evidence-acceptance:" + receipt.ID
	intentStatus := "accepted"
	fields := []projection.OwnedField{{Name: "phase", Value: "assess"}, {Name: "request_id", Value: request.ID}, {Name: "context_id", Value: request.Work.ContextID}}
	intent, err := projection.NewDesiredIntent(request.ModuleID, request.Work.DesiredGeneration, operationID, intentStatus, fields, nil)
	if err != nil {
		t.Fatal(err)
	}
	intentPath, err := identity.StateProjectionIntentPath(request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	intentIdentity, err := intent.Identity()
	if err != nil {
		t.Fatal(err)
	}
	inputFingerprint, err := identity.ID("input-fingerprint", 1, input.InputFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := projection.NewHandoff(projection.Handoff{
		ModuleID: request.ModuleID, Generation: request.Work.DesiredGeneration,
		TransitionID: operationID, InputFingerprint: inputFingerprint, OperationID: operationID,
		AcceptedFactsID: request.Work.FactsID, Producer: input.Producer, ConsumerStage: "selection", Projection: intentIdentity,
	}, input.AcceptedAt)
	if err != nil {
		t.Fatal(err)
	}
	handoffPath, err := identity.StateProjectionHandoffPath(request.ModuleID, handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	requestData, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	receiptData, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	intentData, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	handoffData, err := json.Marshal(handoff)
	if err != nil {
		t.Fatal(err)
	}
	events := []state.Event{
		{ID: "accepted-request:" + request.ID, RecordPath: "state/requests/" + request.ID + ".json", Record: state.Record{Kind: "request", SchemaVersion: 1, Data: requestData}},
		{ID: "accepted-evidence:" + receipt.ID, RecordPath: "state/publications/" + receipt.ID + ".json", Record: state.Record{Kind: "publication", SchemaVersion: 1, Data: receiptData}},
		{ID: intent.IntentID, RecordPath: intentPath, Record: state.Record{Kind: "projection_intent", SchemaVersion: 1, Data: intentData}},
		{ID: handoff.ID, RecordPath: handoffPath, Record: state.Record{Kind: "stage_handoff", SchemaVersion: 1, Data: handoffData}},
	}
	operationResult, err := json.Marshal(struct {
		Acceptance state.EvidenceAcceptanceResult `json:"acceptance"`
		Events     []state.Event                  `json:"events"`
	}{state.EvidenceAcceptanceResult{Receipt: receipt, Changed: true}, events})
	if err != nil {
		t.Fatal(err)
	}
	operationEvent, err := state.NewOperationReceipt(request.ModuleID, operationID, operationResult, events)
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, operationEvent)
	for _, event := range events {
		snapshot.Records[event.RecordPath] = event.Record
		digest, digestErr := identity.ID("state-event", 1, event)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		snapshot.AppliedEvents[event.ID] = strings.TrimPrefix(digest, "state-event-")
	}
	acknowledgement := projection.Acknowledgement{SchemaVersion: projection.SchemaVersion, Projection: intentIdentity, Status: projection.AcknowledgementDone, AcknowledgedAt: input.AcceptedAt.Add(projection.BarrierTimeout)}
	ackData, err := json.Marshal(acknowledgement)
	if err != nil {
		t.Fatal(err)
	}
	ackPath, err := identity.StateProjectionAcknowledgementPath(request.ModuleID, intentIdentity.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[ackPath] = state.Record{Kind: "projection_acknowledgement", SchemaVersion: 1, Data: ackData}
}

func seedRequiredContextSetAcceptance(t *testing.T, snapshot *state.Snapshot, request queue.Request) {
	seedRequiredContextSetAcceptanceWithAdditionalContexts(t, snapshot, request, nil)
}

func seedRequiredContextSetAcceptanceWithAdditionalContexts(t *testing.T, snapshot *state.Snapshot, request queue.Request, additionalContexts []contextset.Context) {
	t.Helper()
	for _, record := range snapshot.Records {
		if record.Kind != "publication" {
			continue
		}
		var receipt state.EvidenceAcceptanceReceipt
		if json.Unmarshal(record.Data, &receipt) == nil && receipt.RecordType == "evidence_acceptance" && receipt.Phase == "acquire_source" && receipt.ModuleID == request.ModuleID && receipt.SourceSHA == request.SourceSHA && receipt.AVMReleaseVersion == request.Work.AVMReleaseVersion && receipt.FactsID == request.Work.FactsID {
			return
		}
	}
	policyID, err := identity.ID("provider-policy", 1, struct {
		Fixture string `json:"fixture"`
	}{"selection-required-contexts"})
	if err != nil {
		t.Fatal(err)
	}
	contexts := []contextset.Context{{State: contextset.ContextState{
		ContextID: request.Work.ContextID, Track: request.Work.Track,
		Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible,
	}}}
	contexts = append(contexts, additionalContexts...)
	complete := true
	for _, context := range contexts {
		if context.State.Applicability == contextset.ApplicabilityUnresolved || context.State.Candidate == contextset.CandidateUnresolved {
			complete = false
			break
		}
	}
	required, err := contextset.New(request.ModuleID, request.SourceSHA, request.Work.AVMReleaseVersion, request.Work.FactsID, policyID, complete, contexts)
	if err != nil {
		t.Fatal(err)
	}
	completionID := selectionTestID(t, "completion", struct {
		RequestID string `json:"request_id"`
	}{request.ID + "-facts"})
	proposalID := selectionTestID(t, "proposal", struct {
		RequestID string `json:"request_id"`
	}{request.ID + "-facts"})
	acceptedAt := request.CreatedAt
	if acceptedAt.IsZero() {
		acceptedAt = request.UpdatedAt.Add(-time.Minute)
	}
	input := state.EvidenceAcceptanceInput{
		ProposalID: proposalID, ProposalKind: state.EvidenceProposalModule, ModuleID: request.ModuleID,
		RequestID: request.ID + "-facts", CompletionID: completionID, Phase: "acquire_source",
		AVMReleaseVersion: request.Work.AVMReleaseVersion, DesiredGeneration: request.Work.DesiredGeneration,
		SourceSHA: request.SourceSHA, FactsID: request.Work.FactsID, InputFingerprint: request.Fingerprint,
		ArtifactDigest: strings.Repeat("f", 64), EvidenceCommit: strings.Repeat("1", 40), AcceptedAt: acceptedAt,
		Producer:         projection.WorkflowProvenance{WorkflowID: "manual-accept-evidence", Commit: strings.Repeat("a", 40), RunID: "facts-fixture", RunAttempt: 1},
		RequiredContexts: &required,
	}
	receipt, err := state.NewEvidenceAcceptanceReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	operationID := "evidence-acceptance:" + receipt.ID
	intent, err := projection.NewDesiredIntent(request.ModuleID, request.Work.DesiredGeneration, operationID, "facts-accepted", []projection.OwnedField{{Name: "phase", Value: "acquire_source"}, {Name: "request_id", Value: input.RequestID}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := "state/requests/" + input.RequestID + ".json"
	receiptPath := "state/publications/" + receipt.ID + ".json"
	intentPath, err := identity.StateProjectionIntentPath(request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	values := []struct {
		id, path, kind string
		data           any
	}{
		{"facts-request:" + request.ID, requestPath, "request", map[string]string{"request_id": input.RequestID}},
		{"facts-acceptance:" + receipt.ID, receiptPath, "publication", receipt},
		{intent.IntentID, intentPath, "projection_intent", intent},
	}
	events := make([]state.Event, 0, len(values))
	for _, value := range values {
		data, marshalErr := json.Marshal(value.data)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		events = append(events, state.Event{ID: value.id, RecordPath: value.path, Record: state.Record{Kind: value.kind, SchemaVersion: 1, Data: data}})
	}
	result, err := json.Marshal(struct {
		Acceptance state.EvidenceAcceptanceResult `json:"acceptance"`
		Events     []state.Event                  `json:"events"`
	}{Acceptance: state.EvidenceAcceptanceResult{Receipt: receipt, Changed: true}, Events: events})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := state.NewOperationReceipt(request.ModuleID, operationID, result, events)
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, operation)
	for _, event := range events {
		snapshot.Records[event.RecordPath] = event.Record
		digest, digestErr := identity.ID("state-event", 1, event)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		snapshot.AppliedEvents[event.ID] = strings.TrimPrefix(digest, "state-event-")
	}
}

func acceptedFactsContextSet(t *testing.T, moduleID, sourceSHA, releaseVersion string) (terraform.Facts, contextset.Set) {
	t.Helper()
	root := t.TempDir()
	source := `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

resource "azurerm_resource_group" "main" {
  name     = "fixture"
  location = "eastus"
}
`
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: root, ModuleID: moduleID, SourceSHA: sourceSHA, ObservedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		Units: []terraform.UnitInput{{Path: ".", Role: terraform.RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := provider.LoadCollectionPolicy(filepath.Join("..", "..", "config", "providers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	contextSet, err := provider.DeriveRequiredAssessmentContexts(facts, releaseVersion, policy)
	if err != nil {
		t.Fatal(err)
	}
	return facts, contextSet
}

func selectionAcceptanceHandoffID(request queue.Request) string {
	analysisID := "analysis-" + strings.Repeat("d", 64)
	proposalID, _ := identity.ID("proposal", 1, struct {
		RequestID string `json:"request_id"`
	}{request.ID})
	input := state.EvidenceAcceptanceInput{
		ProposalID: proposalID, ProposalKind: state.EvidenceProposalModule,
		ModuleID: request.ModuleID, ContextID: request.Work.ContextID, RequestID: request.ID,
		CompletionID: request.Work.Completion.ID, Phase: string(request.Work.Phase), DesiredGeneration: request.Work.DesiredGeneration,
		AVMReleaseVersion: request.Work.AVMReleaseVersion,
		SourceSHA:         request.SourceSHA, FactsID: request.Work.FactsID, AnalysisID: analysisID,
		InputFingerprint: request.Fingerprint, ArtifactDigest: request.Work.Completion.ArtifactDigest,
		EvidenceCommit: strings.Repeat("e", 40), AcceptedAt: request.UpdatedAt,
		Producer: projection.WorkflowProvenance{WorkflowID: "manual-accept-evidence", Commit: strings.Repeat("a", 40), RunID: "test-run", RunAttempt: 1},
	}
	receipt, _ := state.NewEvidenceAcceptanceReceipt(input)
	operationID := "evidence-acceptance:" + receipt.ID
	intent, _ := projection.NewDesiredIntent(request.ModuleID, request.Work.DesiredGeneration, operationID, "accepted", []projection.OwnedField{
		{Name: "phase", Value: "assess"}, {Name: "request_id", Value: request.ID}, {Name: "context_id", Value: request.Work.ContextID},
	}, nil)
	identityTuple, _ := intent.Identity()
	fingerprint, _ := identity.ID("input-fingerprint", 1, input.InputFingerprint)
	handoff, _ := projection.NewHandoff(projection.Handoff{
		ModuleID: request.ModuleID, Generation: request.Work.DesiredGeneration, TransitionID: operationID,
		InputFingerprint: fingerprint, OperationID: operationID, AcceptedFactsID: request.Work.FactsID,
		Producer: input.Producer, ConsumerStage: "selection", Projection: identityTuple,
	}, input.AcceptedAt)
	return handoff.ID
}

func seedSelectionLifecycleSlot(t *testing.T, snapshot *state.Snapshot, request queue.Request) {
	t.Helper()
	path, err := identity.StateLifecycleSlotPath(request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct {
		SchemaVersion     int       `json:"schema_version"`
		ModuleID          string    `json:"module_id"`
		ReleaseVersion    string    `json:"release_version"`
		DesiredGeneration uint64    `json:"desired_generation"`
		RequestID         string    `json:"request_id"`
		AcquiredAt        time.Time `json:"acquired_at"`
		Status            string    `json:"status"`
	}{1, request.ModuleID, request.Work.AVMReleaseVersion, request.Work.DesiredGeneration, request.ID, request.UpdatedAt.Add(-time.Minute), "active"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[path] = state.Record{Kind: "lifecycle_slot", SchemaVersion: 1, Data: data}
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
