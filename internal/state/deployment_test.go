package state_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestDeploymentReceiptAndDoneRequireLatestGenerationAndExactSelection(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	selected, err := state.SelectAccepted(context.Background(), backend, backend.head, selectionInput(request, completionID))
	if err != nil {
		t.Fatal(err)
	}
	plan := deploymentTestPlan(t, selected, 0)
	prepared, err := state.PrepareDeployment(context.Background(), backend, backend.head, plan)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Deployment.Generation != 1 || prepared.State.HighWater != 1 || prepared.Deployment.Status != state.DeploymentAwaitingApproval {
		t.Fatalf("prepared deployment = %+v", prepared)
	}
	authorized, err := state.AuthorizeDeployment(context.Background(), backend, prepared.Head, state.DeploymentApproval{Generation: 1, CurrentTarget: plan.TargetCommit, CandidateIsAncestor: true, ApprovedBy: "reviewer", AuthorizedAt: request.UpdatedAt.Add(3 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	receipt := deploymentTestReceipt(authorized.Deployment, "run-deploy-1", true, request.UpdatedAt.Add(4*time.Minute))
	deployed, err := state.ImportDeploymentReceipt(context.Background(), backend, authorized.Head, receipt)
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	if _, err = state.VerifyDoneSelection(snapshot, request.ModuleID, request.Work.ContextID); err != nil {
		t.Fatalf("current deployment membership was not eligible for Done: %v", err)
	}

	newerPlan := deploymentTestPlan(t, selected, 0)
	newerPlan.AcceptedCommit = strings.Repeat("1", 40)
	newerPlan.TargetCommit = strings.Repeat("2", 40)
	newerPlan.PreparedAt = request.UpdatedAt.Add(5 * time.Minute)
	pending, err := state.PrepareDeployment(context.Background(), backend, deployed.Head, newerPlan)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Deployment.Generation != 2 {
		t.Fatalf("second deployment generation = %d, want 2", pending.Deployment.Generation)
	}
	_, snapshot = readSelectionTestSnapshot(t, backend)
	if _, err = state.VerifyDoneSelection(snapshot, request.ModuleID, request.Work.ContextID); !errors.Is(err, state.ErrDeploymentNotCurrent) {
		t.Fatalf("pending generation inherited prior Done receipt: %v", err)
	}
}

func TestDeploymentAuthorizationRechecksTargetAndRequiresRollbackApproval(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	selected, err := state.SelectAccepted(context.Background(), backend, backend.head, selectionInput(request, completionID))
	if err != nil {
		t.Fatal(err)
	}
	firstPlan := deploymentTestPlan(t, selected, 0)
	prepared, err := state.PrepareDeployment(context.Background(), backend, backend.head, firstPlan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.AuthorizeDeployment(context.Background(), backend, prepared.Head, state.DeploymentApproval{Generation: 1, CurrentTarget: strings.Repeat("3", 40), CandidateIsAncestor: true, ApprovedBy: "reviewer", AuthorizedAt: request.UpdatedAt.Add(3 * time.Minute)}); !errors.Is(err, state.ErrDeploymentBaseMoved) {
		t.Fatalf("moved target authorization error = %v, want ErrDeploymentBaseMoved", err)
	}
	if _, err = state.AuthorizeDeployment(context.Background(), backend, prepared.Head, state.DeploymentApproval{Generation: 1, CurrentTarget: firstPlan.TargetCommit, ApprovedBy: "reviewer", AuthorizedAt: request.UpdatedAt.Add(3 * time.Minute)}); !errors.Is(err, state.ErrDeploymentCandidateNotAncestor) {
		t.Fatalf("non-ancestor authorization error = %v, want ErrDeploymentCandidateNotAncestor", err)
	}
	authorized, err := state.AuthorizeDeployment(context.Background(), backend, prepared.Head, state.DeploymentApproval{Generation: 1, CurrentTarget: firstPlan.TargetCommit, CandidateIsAncestor: true, ApprovedBy: "reviewer", AuthorizedAt: request.UpdatedAt.Add(3 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	deployed, err := state.ImportDeploymentReceipt(context.Background(), backend, authorized.Head, deploymentTestReceipt(authorized.Deployment, "run-deploy-1", true, request.UpdatedAt.Add(4*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}

	rollbackPlan := deploymentTestPlan(t, selected, 1)
	rollbackPlan.AcceptedCommit = firstPlan.AcceptedCommit
	rollbackPlan.DatasetID = firstPlan.DatasetID
	rollbackPlan.TargetCommit = strings.Repeat("4", 40)
	rollbackPlan.PreparedAt = request.UpdatedAt.Add(5 * time.Minute)
	rollback, err := state.PrepareDeployment(context.Background(), backend, deployed.Head, rollbackPlan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.AuthorizeDeployment(context.Background(), backend, rollback.Head, state.DeploymentApproval{Generation: 2, CurrentTarget: rollbackPlan.TargetCommit, CandidateIsAncestor: true, ApprovedBy: "reviewer", AuthorizedAt: request.UpdatedAt.Add(6 * time.Minute)}); !errors.Is(err, state.ErrDeploymentRollbackApproval) {
		t.Fatalf("unapproved rollback error = %v, want ErrDeploymentRollbackApproval", err)
	}
	if _, err = state.AuthorizeDeployment(context.Background(), backend, rollback.Head, state.DeploymentApproval{Generation: 2, CurrentTarget: rollbackPlan.TargetCommit, CandidateIsAncestor: true, ApprovedBy: "reviewer", RollbackApproved: true, AuthorizedAt: request.UpdatedAt.Add(6 * time.Minute)}); err != nil {
		t.Fatalf("explicitly authorized rollback rejected: %v", err)
	}
}

func TestLateDeploymentCallbackIsHistorical(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	selected, err := state.SelectAccepted(context.Background(), backend, backend.head, selectionInput(request, completionID))
	if err != nil {
		t.Fatal(err)
	}
	firstPlan := deploymentTestPlan(t, selected, 0)
	first, err := state.PrepareDeployment(context.Background(), backend, backend.head, firstPlan)
	if err != nil {
		t.Fatal(err)
	}
	firstAuthorized, err := state.AuthorizeDeployment(context.Background(), backend, first.Head, state.DeploymentApproval{Generation: 1, CurrentTarget: firstPlan.TargetCommit, CandidateIsAncestor: true, ApprovedBy: "reviewer", AuthorizedAt: request.UpdatedAt.Add(3 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}

	secondPlan := deploymentTestPlan(t, selected, 0)
	secondPlan.AcceptedCommit = strings.Repeat("1", 40)
	secondPlan.TargetCommit = strings.Repeat("2", 40)
	secondPlan.PreparedAt = request.UpdatedAt.Add(4 * time.Minute)
	second, err := state.PrepareDeployment(context.Background(), backend, firstAuthorized.Head, secondPlan)
	if err != nil {
		t.Fatal(err)
	}
	secondAuthorized, err := state.AuthorizeDeployment(context.Background(), backend, second.Head, state.DeploymentApproval{Generation: 2, CurrentTarget: secondPlan.TargetCommit, CandidateIsAncestor: true, ApprovedBy: "reviewer", AuthorizedAt: request.UpdatedAt.Add(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	lateReceipt := deploymentTestReceipt(firstAuthorized.Deployment, "run-deploy-late", true, request.UpdatedAt.Add(6*time.Minute))
	late, err := state.ImportDeploymentReceipt(context.Background(), backend, secondAuthorized.Head, lateReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if late.Receipt.Status != state.DeploymentReceiptHistorical || late.State.ActiveGeneration != 2 {
		t.Fatalf("late receipt changed active deployment state: %+v", late)
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	if _, err = state.VerifyDoneSelection(snapshot, request.ModuleID, request.Work.ContextID); !errors.Is(err, state.ErrDeploymentNotCurrent) {
		t.Fatalf("late callback certified Done for the newer generation: %v", err)
	}
}

func TestDuplicateAndFailedDeploymentCallbacksNeverInheritDone(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	selected, err := state.SelectAccepted(context.Background(), backend, backend.head, selectionInput(request, completionID))
	if err != nil {
		t.Fatal(err)
	}
	firstPlan := deploymentTestPlan(t, selected, 0)
	prepared, err := state.PrepareDeployment(context.Background(), backend, backend.head, firstPlan)
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := state.AuthorizeDeployment(context.Background(), backend, prepared.Head, state.DeploymentApproval{Generation: 1, CurrentTarget: firstPlan.TargetCommit, CandidateIsAncestor: true, ApprovedBy: "reviewer", AuthorizedAt: request.UpdatedAt.Add(3 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	success := deploymentTestReceipt(authorized.Deployment, "run-deploy-1", true, request.UpdatedAt.Add(4*time.Minute))
	deployed, err := state.ImportDeploymentReceipt(context.Background(), backend, authorized.Head, success)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := state.ImportDeploymentReceipt(context.Background(), backend, deployed.Head, success)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Changed || backend.commitCount != 4 {
		t.Fatalf("duplicate successful callback changed state: result=%+v commits=%d", duplicate, backend.commitCount)
	}

	failedPlan := deploymentTestPlan(t, selected, 0)
	failedPlan.AcceptedCommit = strings.Repeat("1", 40)
	failedPlan.TargetCommit = strings.Repeat("2", 40)
	failedPlan.PreparedAt = request.UpdatedAt.Add(5 * time.Minute)
	failedPrepared, err := state.PrepareDeployment(context.Background(), backend, duplicate.Head, failedPlan)
	if err != nil {
		t.Fatal(err)
	}
	failedAuthorized, err := state.AuthorizeDeployment(context.Background(), backend, failedPrepared.Head, state.DeploymentApproval{Generation: 2, CurrentTarget: failedPlan.TargetCommit, CandidateIsAncestor: true, ApprovedBy: "reviewer", AuthorizedAt: request.UpdatedAt.Add(6 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	failedReceipt := deploymentTestReceipt(failedAuthorized.Deployment, "run-deploy-2", false, request.UpdatedAt.Add(7*time.Minute))
	failed, err := state.ImportDeploymentReceipt(context.Background(), backend, failedAuthorized.Head, failedReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Receipt.Status != state.DeploymentReceiptFailed {
		t.Fatalf("failed callback status = %q, want failed", failed.Receipt.Status)
	}
	_, snapshot := readSelectionTestSnapshot(t, backend)
	if _, err = state.VerifyDoneSelection(snapshot, request.ModuleID, request.Work.ContextID); !errors.Is(err, state.ErrDeploymentNotCurrent) {
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
		DataDigest: strings.Repeat("b", 64), SiteDigest: strings.Repeat("c", 64),
		Generator:    index.Producer{ID: "factory", Version: "2.0.0", ContractVersion: 1},
		ModuleIDs:    []string{"avm-ptn-selection-test", "avm-res-selection-test", "avm-utl-selection-test"},
		Membership:   []index.ModuleMembership{{ModuleID: selected.Receipt.ModuleID, ContextID: selected.Receipt.ContextID, AnalysisID: &analysisID, SelectionID: &selectionID}},
		TargetCommit: strings.Repeat("d", 40), RollbackOfGeneration: rollbackOf,
		PreparedAt: time.Date(2026, 10, 2, 12, 5, 0, 0, time.UTC),
	}
}

func deploymentTestReceipt(deployment state.DeploymentGeneration, runID string, succeeded bool, deployedAt time.Time) state.DeploymentReceipt {
	return state.DeploymentReceipt{
		RunID: runID, Generation: deployment.Generation, AcceptedCommit: deployment.AcceptedCommit,
		DatasetID: deployment.DatasetID, DataDigest: deployment.DataDigest, SiteDigest: deployment.SiteDigest,
		Generator: deployment.Generator, ModuleIDs: append([]string(nil), deployment.ModuleIDs...),
		Membership: deployment.Membership,
		Succeeded:  succeeded, DeployedAt: deployedAt,
	}
}
