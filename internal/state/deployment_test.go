package state_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestDeploymentReceiptAndDoneRequireLatestGenerationAndExactSelection(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	coordinator := deploymentTestCoordinator{backend: backend}
	selected, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID))
	if err != nil {
		t.Fatal(err)
	}
	assertDeploymentTestSlot(t, backend, request.ModuleID, request.Work.AVMReleaseVersion, "active", "")
	plan := deploymentTestPlan(t, selected, 0)
	prepared, err := state.PrepareDeployment(context.Background(), coordinator, plan)
	if err != nil {
		t.Fatal(err)
	}
	wrongRootApproval := deploymentTestApproval(plan, prepared.Deployment.Generation, "reviewer", request.UpdatedAt.Add(3*time.Minute))
	wrongRootApproval.ExpectedProviderDataDigest = strings.Repeat("9", 64)
	if _, err = state.AuthorizeDeployment(context.Background(), coordinator, wrongRootApproval); !errors.Is(err, state.ErrDeploymentNotCurrent) {
		t.Fatalf("authorization accepted a provider root digest different from the prepared generation: %v", err)
	}
	if prepared.Deployment.Generation != 1 || prepared.State.HighWater != 1 || prepared.Deployment.Status != state.DeploymentAwaitingApproval {
		t.Fatalf("prepared deployment = %+v", prepared)
	}
	authorized, err := state.AuthorizeDeployment(context.Background(), coordinator, deploymentTestApproval(plan, 1, "reviewer", request.UpdatedAt.Add(3*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	receipt := deploymentTestReceipt(authorized.Deployment, "run-deploy-1", true, request.UpdatedAt.Add(4*time.Minute))
	_, err = state.ImportDeploymentReceipt(context.Background(), coordinator, receipt)
	if err != nil {
		t.Fatal(err)
	}
	assertDeploymentTestSlot(t, backend, request.ModuleID, request.Work.AVMReleaseVersion, "active", "")
	acknowledgement, err := state.AcknowledgeModuleDeployment(context.Background(), selectionTestCoordinator{backend: backend}, request.ModuleID, request.Work.AVMReleaseVersion)
	if err != nil {
		t.Fatalf("module deployment acknowledgement failed: %v", err)
	}
	replayedAcknowledgement, err := state.AcknowledgeModuleDeployment(context.Background(), selectionTestCoordinator{backend: backend}, request.ModuleID, request.Work.AVMReleaseVersion)
	if err != nil || replayedAcknowledgement.ID != acknowledgement.ID {
		t.Fatalf("module deployment acknowledgement replay = %+v, err=%v", replayedAcknowledgement, err)
	}
	assertDeploymentTestSlot(t, backend, request.ModuleID, request.Work.AVMReleaseVersion, "released", acknowledgement.DeploymentReceiptID)
	if _, err = state.AcknowledgeModuleDeployment(context.Background(), selectionTestCoordinator{backend: backend}, request.ModuleID, "v2.0.0"); !errors.Is(err, state.ErrDeploymentNotCurrent) {
		t.Fatalf("module slot released for a different AVM version: %v", err)
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	if _, err = state.VerifyDoneSelection(snapshot, request.ModuleID, request.Work.AVMReleaseVersion, request.Work.ContextID); err != nil {
		t.Fatalf("current deployment membership was not eligible for Done: %v", err)
	}
	if _, err = state.VerifyDoneSelection(snapshot, request.ModuleID, "v2.0.0", request.Work.ContextID); !errors.Is(err, state.ErrDeploymentNotCurrent) {
		t.Fatalf("deployment membership was reused for another AVM release: %v", err)
	}

	newerPlan := deploymentTestPlan(t, selected, 0)
	newerPlan.AcceptedCommit = strings.Repeat("1", 40)
	newerPlan.TargetCommit = strings.Repeat("2", 40)
	newerPlan.PreparedAt = request.UpdatedAt.Add(5 * time.Minute)
	pending, err := state.PrepareDeployment(context.Background(), coordinator, newerPlan)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Deployment.Generation != 2 {
		t.Fatalf("second deployment generation = %d, want 2", pending.Deployment.Generation)
	}
	_, snapshot = readSelectionTestSnapshot(t, backend)
	if _, err = state.VerifyDoneSelection(snapshot, request.ModuleID, request.Work.AVMReleaseVersion, request.Work.ContextID); !errors.Is(err, state.ErrDeploymentNotCurrent) {
		t.Fatalf("pending generation inherited prior Done receipt: %v", err)
	}
}

func TestGlobalDeploymentReceiptFanoutReplaysAfterPartialModuleAcknowledgements(t *testing.T) {
	backend, firstRequest, firstCompletion := selectionFixture(t, false)
	coordinator := selectionTestCoordinator{backend: backend}
	selected := []state.SelectionResult{}
	first, err := state.SelectAccepted(context.Background(), coordinator, selectionInput(firstRequest, firstCompletion))
	if err != nil {
		t.Fatal(err)
	}
	selected = append(selected, first)

	at := firstRequest.UpdatedAt.Add(time.Second)
	for _, moduleID := range []string{"avm-ptn-selection-test", "avm-utl-selection-test"} {
		request := acceptedAssessmentRequestForRelease(t, moduleID, "azurerm/4", 1, "v1.0.0", at)
		seedSelectionLifecycleSlot(t, &backend.snapshot, request)
		addRequestToSnapshot(t, &backend.snapshot, request)
		seedEvidenceAcceptanceReceipt(t, &backend.snapshot, request)
		result, selectErr := state.SelectAccepted(context.Background(), coordinator, selectionInput(request, request.Work.Completion.ID))
		if selectErr != nil {
			t.Fatalf("select exact accepted context for %s: %v", moduleID, selectErr)
		}
		selected = append(selected, result)
		at = at.Add(time.Second)
	}

	plan := deploymentTestPlan(t, selected[0], 0)
	plan.ModuleIDs = []string{"avm-ptn-selection-test", "avm-res-selection-test", "avm-utl-selection-test"}
	plan.Membership = make([]index.ModuleMembership, 0, len(selected))
	for _, result := range selected {
		selectionID, analysisID := result.Receipt.ID, *result.Receipt.AnalysisID
		plan.Membership = append(plan.Membership, index.ModuleMembership{
			ModuleID: result.Receipt.ModuleID, AVMReleaseVersion: result.Receipt.AVMReleaseVersion,
			ContextID: result.Receipt.ContextID, SelectionID: &selectionID, AnalysisID: &analysisID,
		})
	}
	sort.Slice(plan.Membership, func(left, right int) bool {
		return plan.Membership[left].ModuleID < plan.Membership[right].ModuleID
	})
	plan.PreparedAt = at.Add(time.Minute)
	prepared, err := state.PrepareDeployment(context.Background(), deploymentTestCoordinator{backend: backend}, plan)
	if err != nil {
		t.Fatalf("prepare global deployment: %v", err)
	}
	authorized, err := state.AuthorizeDeployment(context.Background(), deploymentTestCoordinator{backend: backend}, deploymentTestApproval(plan, prepared.Deployment.Generation, "reviewer", plan.PreparedAt.Add(time.Minute)))
	if err != nil {
		t.Fatalf("authorize global deployment: %v", err)
	}
	receipt := deploymentTestReceipt(authorized.Deployment, "run-three-module-deploy", true, plan.PreparedAt.Add(2*time.Minute))
	if _, err = state.ImportDeploymentReceipt(context.Background(), deploymentTestCoordinator{backend: backend}, receipt); err != nil {
		t.Fatalf("import global deployment receipt: %v", err)
	}

	firstModule := plan.Membership[0].ModuleID
	if _, err = state.AcknowledgeModuleDeployment(context.Background(), coordinator, firstModule, "v1.0.0"); err != nil {
		t.Fatalf("first module fan-out: %v", err)
	}
	if got := modulePublicationAcknowledgementCount(t, backend); got != 1 {
		t.Fatalf("partial fan-out acknowledgements=%d, want one", got)
	}

	// Reimporting the same global receipt replays the durable fan-out from its
	// original membership; a lost continuation cannot strand other modules.
	if _, err = state.ImportDeploymentReceipt(context.Background(), deploymentTestCoordinator{backend: backend}, receipt); err != nil {
		t.Fatalf("replay global deployment receipt: %v", err)
	}
	for _, moduleID := range plan.ModuleIDs {
		if _, err = state.AcknowledgeModuleDeployment(context.Background(), coordinator, moduleID, "v1.0.0"); err != nil {
			t.Fatalf("replay module fan-out for %s: %v", moduleID, err)
		}
	}
	if got := modulePublicationAcknowledgementCount(t, backend); got != 3 {
		t.Fatalf("replayed fan-out acknowledgements=%d, want all three modules", got)
	}
	if _, err = state.ImportDeploymentReceipt(context.Background(), deploymentTestCoordinator{backend: backend}, receipt); err != nil {
		t.Fatalf("second global receipt replay: %v", err)
	}
	for _, moduleID := range plan.ModuleIDs {
		if _, err = state.AcknowledgeModuleDeployment(context.Background(), coordinator, moduleID, "v1.0.0"); err != nil {
			t.Fatalf("idempotent module acknowledgement replay for %s: %v", moduleID, err)
		}
	}
	if got := modulePublicationAcknowledgementCount(t, backend); got != 3 {
		t.Fatalf("idempotent fan-out created duplicate acknowledgements: %d", got)
	}
}

func modulePublicationAcknowledgementCount(t *testing.T, backend *selectionBackend) int {
	t.Helper()
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, record := range snapshot.Records {
		if record.Kind == "module_publication_ack" {
			count++
		}
	}
	return count
}

func assertDeploymentTestSlot(t *testing.T, backend interface {
	Read(context.Context) (string, state.Snapshot, error)
}, moduleID, release, wantStatus, wantReceipt string) {
	t.Helper()
	path, err := identity.StateLifecycleSlotPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	record, exists := snapshot.Records[path]
	if !exists || record.Kind != "lifecycle_slot" {
		t.Fatalf("lifecycle slot %s is missing", path)
	}
	var slot struct {
		ModuleID       string `json:"module_id"`
		ReleaseVersion string `json:"release_version"`
		Status         string `json:"status"`
		ReleaseProof   *struct {
			DeploymentReceiptID string `json:"deployment_receipt_id"`
		} `json:"release_proof,omitempty"`
	}
	if err = json.Unmarshal(record.Data, &slot); err != nil || slot.ModuleID != moduleID || slot.ReleaseVersion != release || slot.Status != wantStatus {
		t.Fatalf("lifecycle slot = %+v, error %v; want module=%s release=%s status=%s", slot, err, moduleID, release, wantStatus)
	}
	gotReceipt := ""
	if slot.ReleaseProof != nil {
		gotReceipt = slot.ReleaseProof.DeploymentReceiptID
	}
	if gotReceipt != wantReceipt {
		t.Fatalf("lifecycle slot release proof = %q, want %q", gotReceipt, wantReceipt)
	}
}

type deploymentTestCoordinator struct {
	backend state.LegacyCommitBackend
}

func (coordinator deploymentTestCoordinator) Read(ctx context.Context) (string, state.Snapshot, error) {
	return coordinator.backend.Read(ctx)
}

func (coordinator deploymentTestCoordinator) RunTransaction(ctx context.Context, operationID string, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	for attempt := 0; attempt < 3; attempt++ {
		head, snapshot, err := coordinator.backend.Read(ctx)
		if err != nil {
			return state.TransactionResult{}, err
		}
		if _, applied := snapshot.AppliedEvents[operationID]; applied {
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

func TestDeploymentAuthorizationRechecksTargetAndRequiresRollbackApproval(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	coordinator := deploymentTestCoordinator{backend: backend}
	selected, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID))
	if err != nil {
		t.Fatal(err)
	}
	firstPlan := deploymentTestPlan(t, selected, 0)
	_, err = state.PrepareDeployment(context.Background(), coordinator, firstPlan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.AuthorizeDeployment(context.Background(), coordinator, deploymentTestApproval(firstPlan, 1, "reviewer", request.UpdatedAt.Add(3*time.Minute), strings.Repeat("3", 40))); !errors.Is(err, state.ErrDeploymentBaseMoved) {
		t.Fatalf("moved target authorization error = %v, want ErrDeploymentBaseMoved", err)
	}
	approval := deploymentTestApproval(firstPlan, 1, "reviewer", request.UpdatedAt.Add(3*time.Minute))
	approval.CandidateIsAncestor = false
	if _, err = state.AuthorizeDeployment(context.Background(), coordinator, approval); !errors.Is(err, state.ErrDeploymentCandidateNotAncestor) {
		t.Fatalf("non-ancestor authorization error = %v, want ErrDeploymentCandidateNotAncestor", err)
	}
	authorized, err := state.AuthorizeDeployment(context.Background(), coordinator, deploymentTestApproval(firstPlan, 1, "reviewer", request.UpdatedAt.Add(3*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.ImportDeploymentReceipt(context.Background(), coordinator, deploymentTestReceipt(authorized.Deployment, "run-deploy-1", true, request.UpdatedAt.Add(4*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	rollbackPlan := deploymentTestPlan(t, selected, 1)
	rollbackPlan.AcceptedCommit = firstPlan.AcceptedCommit
	rollbackPlan.DatasetID = firstPlan.DatasetID
	rollbackPlan.TargetCommit = strings.Repeat("4", 40)
	rollbackPlan.PreparedAt = request.UpdatedAt.Add(5 * time.Minute)
	_, err = state.PrepareDeployment(context.Background(), coordinator, rollbackPlan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.AuthorizeDeployment(context.Background(), coordinator, deploymentTestApproval(rollbackPlan, 2, "reviewer", request.UpdatedAt.Add(6*time.Minute))); !errors.Is(err, state.ErrDeploymentRollbackApproval) {
		t.Fatalf("unapproved rollback error = %v, want ErrDeploymentRollbackApproval", err)
	}
	rollbackApproval := deploymentTestApproval(rollbackPlan, 2, "reviewer", request.UpdatedAt.Add(6*time.Minute))
	rollbackApproval.RollbackApproved = true
	if _, err = state.AuthorizeDeployment(context.Background(), coordinator, rollbackApproval); err != nil {
		t.Fatalf("explicitly authorized rollback rejected: %v", err)
	}
}

func TestLateDeploymentCallbackIsHistorical(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	coordinator := deploymentTestCoordinator{backend: backend}
	selected, selectErr := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID))
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	firstPlan := deploymentTestPlan(t, selected, 0)
	_, err := state.PrepareDeployment(context.Background(), coordinator, firstPlan)
	if err != nil {
		t.Fatal(err)
	}
	firstAuthorized, err := state.AuthorizeDeployment(context.Background(), coordinator, deploymentTestApproval(firstPlan, 1, "reviewer", request.UpdatedAt.Add(3*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	secondPlan := deploymentTestPlan(t, selected, 0)
	secondPlan.AcceptedCommit = strings.Repeat("1", 40)
	secondPlan.TargetCommit = strings.Repeat("2", 40)
	secondPlan.PreparedAt = request.UpdatedAt.Add(4 * time.Minute)
	_, err = state.PrepareDeployment(context.Background(), coordinator, secondPlan)
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.AuthorizeDeployment(context.Background(), coordinator, deploymentTestApproval(secondPlan, 2, "reviewer", request.UpdatedAt.Add(5*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	lateReceipt := deploymentTestReceipt(firstAuthorized.Deployment, "run-deploy-late", true, request.UpdatedAt.Add(6*time.Minute))
	late, err := state.ImportDeploymentReceipt(context.Background(), coordinator, lateReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if late.Receipt.Status != state.DeploymentReceiptHistorical || late.State.ActiveGeneration != 2 {
		t.Fatalf("late receipt changed active deployment state: %+v", late)
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	if _, err = state.VerifyDoneSelection(snapshot, request.ModuleID, request.Work.AVMReleaseVersion, request.Work.ContextID); !errors.Is(err, state.ErrDeploymentNotCurrent) {
		t.Fatalf("late callback certified Done for the newer generation: %v", err)
	}
}

func TestDuplicateAndFailedDeploymentCallbacksNeverInheritDone(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	coordinator := deploymentTestCoordinator{backend: backend}
	selected, selectErr := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID))
	if selectErr != nil {
		t.Fatal(selectErr)
	}
	firstPlan := deploymentTestPlan(t, selected, 0)
	_, err := state.PrepareDeployment(context.Background(), coordinator, firstPlan)
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := state.AuthorizeDeployment(context.Background(), coordinator, deploymentTestApproval(firstPlan, 1, "reviewer", request.UpdatedAt.Add(3*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	success := deploymentTestReceipt(authorized.Deployment, "run-deploy-1", true, request.UpdatedAt.Add(4*time.Minute))
	_, err = state.ImportDeploymentReceipt(context.Background(), coordinator, success)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := state.ImportDeploymentReceipt(context.Background(), coordinator, success)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Changed || backend.commitCount != 4 {
		t.Fatalf("duplicate successful callback changed state: result=%+v commits=%d", duplicate, backend.commitCount)
	}
	wrongProviderRoot := success
	wrongProviderRoot.ProviderDataDigest = strings.Repeat("9", 64)
	if _, err = state.ImportDeploymentReceipt(context.Background(), coordinator, wrongProviderRoot); !errors.Is(err, state.ErrDeploymentReceiptConflict) {
		t.Fatalf("same run replay with a different provider root digest was not rejected: %v", err)
	}
	wrongPagesKey := success
	wrongPagesKey.PagesDeploymentID = strings.Repeat("9", 40)
	if _, err = state.ImportDeploymentReceipt(context.Background(), coordinator, wrongPagesKey); err == nil {
		t.Fatal("same run replay with a Pages key for another accepted commit was accepted")
	}

	failedPlan := deploymentTestPlan(t, selected, 0)
	failedPlan.AcceptedCommit = strings.Repeat("1", 40)
	failedPlan.TargetCommit = strings.Repeat("2", 40)
	failedPlan.PreparedAt = request.UpdatedAt.Add(5 * time.Minute)
	_, err = state.PrepareDeployment(context.Background(), coordinator, failedPlan)
	if err != nil {
		t.Fatal(err)
	}
	failedAuthorized, err := state.AuthorizeDeployment(context.Background(), coordinator, deploymentTestApproval(failedPlan, 2, "reviewer", request.UpdatedAt.Add(6*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	failedReceipt := deploymentTestReceipt(failedAuthorized.Deployment, "run-deploy-2", false, request.UpdatedAt.Add(7*time.Minute))
	failed, err := state.ImportDeploymentReceipt(context.Background(), coordinator, failedReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Receipt.Status != state.DeploymentReceiptFailed {
		t.Fatalf("failed callback status = %q, want failed", failed.Receipt.Status)
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	if _, err = state.VerifyDoneSelection(snapshot, request.ModuleID, request.Work.AVMReleaseVersion, request.Work.ContextID); !errors.Is(err, state.ErrDeploymentNotCurrent) {
		t.Fatalf("failed current generation inherited Done: %v", err)
	}
}

func deploymentTestPlan(t *testing.T, selected state.SelectionResult, rollbackOf uint64) state.DeploymentPlan {
	t.Helper()
	selectionID := selected.Receipt.ID
	analysisID := *selected.Receipt.AnalysisID
	datasetID := selectionTestID(t, "dataset", struct {
		Fixture string `json:"fixture"`
	}{"deployment"})
	return state.DeploymentPlan{
		AcceptedCommit: strings.Repeat("a", 40), DatasetID: datasetID,
		DataDigest: strings.Repeat("b", 64), ModuleDataDigest: strings.Repeat("e", 64),
		ProviderDataDigest: strings.Repeat("f", 64), SiteDigest: strings.Repeat("c", 64),
		Generator:    index.Producer{ID: "factory", Version: "2.0.0", ContractVersion: 1},
		ModuleIDs:    []string{"avm-ptn-selection-test", "avm-res-selection-test", "avm-utl-selection-test"},
		Membership:   []index.ModuleMembership{{ModuleID: selected.Receipt.ModuleID, AVMReleaseVersion: selected.Receipt.AVMReleaseVersion, ContextID: selected.Receipt.ContextID, AnalysisID: &analysisID, SelectionID: &selectionID}},
		TargetCommit: strings.Repeat("d", 40), RollbackOfGeneration: rollbackOf,
		PreparedAt: time.Date(2026, 10, 2, 12, 5, 0, 0, time.UTC),
	}
}

func deploymentTestApproval(plan state.DeploymentPlan, generation uint64, approvedBy string, authorizedAt time.Time, targets ...string) state.DeploymentApproval {
	currentTarget := plan.TargetCommit
	if len(targets) > 0 {
		currentTarget = targets[0]
	}
	return state.DeploymentApproval{
		Generation: generation, CurrentTarget: currentTarget, CandidateIsAncestor: true,
		ExpectedAcceptedCommit: plan.AcceptedCommit, ExpectedDatasetID: plan.DatasetID,
		ExpectedDataDigest: plan.DataDigest, ExpectedModuleDataDigest: plan.ModuleDataDigest,
		ExpectedProviderDataDigest: plan.ProviderDataDigest, ExpectedSiteDigest: plan.SiteDigest,
		ApprovedBy: approvedBy, AuthorizedAt: authorizedAt,
	}
}

func deploymentTestReceipt(deployment state.DeploymentGeneration, runID string, succeeded bool, deployedAt time.Time) state.DeploymentReceipt {
	return state.DeploymentReceipt{
		RunID: runID, PagesDeploymentID: deployment.AcceptedCommit, Generation: deployment.Generation, AcceptedCommit: deployment.AcceptedCommit,
		DatasetID: deployment.DatasetID, DataDigest: deployment.DataDigest,
		ModuleDataDigest: deployment.ModuleDataDigest, ProviderDataDigest: deployment.ProviderDataDigest,
		SiteDigest: deployment.SiteDigest,
		Generator:  deployment.Generator, ModuleIDs: append([]string(nil), deployment.ModuleIDs...),
		Membership: deployment.Membership,
		Succeeded:  succeeded, DeployedAt: deployedAt,
	}
}
