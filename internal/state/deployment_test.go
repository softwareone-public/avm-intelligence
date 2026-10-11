package state_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/contextset"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
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
	if _, err = state.AcknowledgeModuleDeployment(context.Background(), selectionTestCoordinator{backend: backend}, request.ModuleID, request.Work.AVMReleaseVersion); !errors.Is(err, state.ErrDeploymentNotCurrent) {
		t.Fatalf("missing Pages callback was acknowledged as publication: %v", err)
	}
	assertDeploymentTestSlot(t, backend, request.ModuleID, request.Work.AVMReleaseVersion, "active", "")
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
	verifiedModule, err := state.VerifyDoneModule(snapshot, request.ModuleID, request.Work.AVMReleaseVersion, acknowledgement.AcceptedCommit)
	if err != nil || verifiedModule.ID != acknowledgement.ID {
		t.Fatalf("complete current module publication was not eligible for Done: ack=%+v err=%v", verifiedModule, err)
	}
	if _, err = state.VerifyDoneModule(snapshot, request.ModuleID, request.Work.AVMReleaseVersion, strings.Repeat("f", 40)); !errors.Is(err, state.ErrDeploymentNotCurrent) {
		t.Fatalf("module Done verification accepted another deployment commit: %v", err)
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
	additionalContext := contextset.Context{
		State: contextset.ContextState{
			ContextID: "azurerm/5", Track: "5.x",
			Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible,
		},
	}
	backend, firstRequest, firstCompletion := selectionFixtureWithRequiredContexts(t, false, []contextset.Context{additionalContext})
	coordinator := selectionTestCoordinator{backend: backend}
	selected := []state.SelectionResult{}
	first, err := state.SelectAccepted(context.Background(), coordinator, selectionInput(firstRequest, firstCompletion))
	if err != nil {
		t.Fatal(err)
	}
	selected = append(selected, first)

	at := firstRequest.UpdatedAt.Add(time.Second)
	secondContext := acceptedRequiredContextAssessment(t, firstRequest, "azurerm/5", "5.x", at)
	addRequestToSnapshot(t, &backend.snapshot, secondContext)
	seedEvidenceAcceptanceReceipt(t, &backend.snapshot, secondContext)
	secondSelection, err := state.SelectAccepted(context.Background(), coordinator, selectionInput(secondContext, secondContext.Work.Completion.ID))
	if err != nil {
		t.Fatalf("select second context for %s: %v", firstRequest.ModuleID, err)
	}
	selected = append(selected, secondSelection)
	at = at.Add(time.Second)
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

func TestPartialModuleDeploymentKeepsSlotUntilRequiredMembershipIsPublished(t *testing.T) {
	additionalContext := contextset.Context{
		State: contextset.ContextState{
			ContextID: "azurerm/5", Track: "5.x",
			Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible,
		},
	}
	ineligibleContext := contextset.Context{
		State: contextset.ContextState{
			ContextID: "azurerm/6", Track: "6.x",
			Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateIneligible,
		},
	}
	backend, firstRequest, firstCompletion := selectionFixtureWithRequiredContexts(t, false, []contextset.Context{additionalContext, ineligibleContext})
	coordinator := selectionTestCoordinator{backend: backend}
	first, err := state.SelectAccepted(context.Background(), coordinator, selectionInput(firstRequest, firstCompletion))
	if err != nil {
		t.Fatalf("select first required context: %v", err)
	}

	queuedNextVersion, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: firstRequest.ModuleID, SourceSHA: firstRequest.SourceSHA, AVMReleaseVersion: "v2.0.0", DesiredGeneration: 1,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{"hcl"},
	}, firstRequest.UpdatedAt.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	addRequestToSnapshot(t, &backend.snapshot, queuedNextVersion)

	firstMembership := deploymentMembershipForSelections(first)
	acknowledgement := publishDeploymentForSelections(t, backend, first, firstMembership, 1, firstRequest.UpdatedAt.Add(5*time.Minute))
	assertDeploymentTestSlot(t, backend, firstRequest.ModuleID, "v1.0.0", "active", "")
	assertDeploymentProjectionStatus(t, backend, firstRequest.ModuleID, "partial", "1", "1", "2", "0")
	assertRequestStatus(t, backend, queuedNextVersion.ID, queue.StatusQueued)

	secondRequest := acceptedRequiredContextAssessment(t, firstRequest, "azurerm/5", "5.x", firstRequest.UpdatedAt.Add(8*time.Minute))
	addRequestToSnapshot(t, &backend.snapshot, secondRequest)
	seedEvidenceAcceptanceReceipt(t, &backend.snapshot, secondRequest)
	second, err := state.SelectAccepted(context.Background(), coordinator, selectionInput(secondRequest, secondRequest.Work.Completion.ID))
	if err != nil {
		t.Fatalf("select second required context: %v", err)
	}

	// Even though both contexts now have accepted selections, a Pages receipt
	// containing only the first context remains partial and cannot release v1.
	partialMembershipAck := publishDeploymentForSelections(t, backend, first, firstMembership, 2, firstRequest.UpdatedAt.Add(12*time.Minute))
	if partialMembershipAck.ID == acknowledgement.ID {
		t.Fatal("different Pages generations produced the same module acknowledgement")
	}
	assertDeploymentTestSlot(t, backend, firstRequest.ModuleID, "v1.0.0", "active", "")
	assertDeploymentProjectionStatus(t, backend, firstRequest.ModuleID, "partial", "2", "1", "2", "0")
	assertRequestStatus(t, backend, queuedNextVersion.ID, queue.StatusQueued)

	completeMembership := deploymentMembershipForSelections(first, second)
	completeAck := publishDeploymentForSelections(t, backend, first, completeMembership, 3, firstRequest.UpdatedAt.Add(16*time.Minute))
	assertDeploymentTestSlot(t, backend, firstRequest.ModuleID, "v1.0.0", "released", completeAck.DeploymentReceiptID)
	assertDeploymentProjectionStatus(t, backend, firstRequest.ModuleID, "done", "2", "2", "2", "0")
	assertRequestStatus(t, backend, queuedNextVersion.ID, queue.StatusQueued)
}

func TestUnresolvedRequiredContextKeepsModuleDeploymentSlotActive(t *testing.T) {
	unresolvedContext := contextset.Context{
		State: contextset.ContextState{
			ContextID: "azurerm/5", Track: "5.x",
			Applicability: contextset.ApplicabilityUnresolved, Candidate: contextset.CandidateUnresolved,
		},
		Reason: "provider constraint could not be resolved",
	}
	ineligibleContext := contextset.Context{
		State: contextset.ContextState{
			ContextID: "azurerm/6", Track: "6.x",
			Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateIneligible,
		},
	}
	backend, request, completionID := selectionFixtureWithRequiredContexts(t, false, []contextset.Context{unresolvedContext, ineligibleContext})
	selected, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID))
	if err != nil {
		t.Fatalf("select resolved required context: %v", err)
	}

	acknowledgement := publishDeploymentForSelections(t, backend, selected, deploymentMembershipForSelections(selected), 1, request.UpdatedAt.Add(5*time.Minute))
	if acknowledgement.ID == "" {
		t.Fatal("partial Pages deployment did not persist a module acknowledgement")
	}
	assertDeploymentTestSlot(t, backend, request.ModuleID, "v1.0.0", "active", "")
	assertDeploymentProjectionStatus(t, backend, request.ModuleID, "partial", "1", "1", "2", "1")
}

func TestPagesAcknowledgesNonRequiredRetirementDeselectionWithoutCountingItAsPublished(t *testing.T) {
	ineligible := contextset.Context{State: contextset.ContextState{
		ContextID: "azurerm/6", Track: "6.x", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateIneligible,
	}}
	backend, request, completionID := selectionFixtureWithRequiredContexts(t, false, []contextset.Context{ineligible})
	coordinator := selectionTestCoordinator{backend: backend}
	selected, err := state.SelectAccepted(context.Background(), coordinator, selectionInput(request, completionID))
	if err != nil {
		t.Fatalf("select required context: %v", err)
	}

	_, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	markCatalogModuleRetiredForDeploymentTest(t, &snapshot, request.ModuleID)
	deselected := seedRetirementDeselectionForDeploymentTest(t, &snapshot, request.ModuleID, request.Work.AVMReleaseVersion, "azurerm/6", request.UpdatedAt.Add(4*time.Minute))
	backend.snapshot = snapshot

	plan := deploymentTestPlan(t, selected, 0)
	deselectionID := deselected.ID
	plan.Membership = append(plan.Membership, index.ModuleMembership{
		ModuleID: request.ModuleID, AVMReleaseVersion: request.Work.AVMReleaseVersion,
		ContextID: "azurerm/6", SelectionID: &deselectionID,
	})
	sort.Slice(plan.Membership, func(left, right int) bool {
		return plan.Membership[left].ContextID < plan.Membership[right].ContextID
	})
	plan.PreparedAt = request.UpdatedAt.Add(5 * time.Minute)
	prepared, err := state.PrepareDeployment(context.Background(), deploymentTestCoordinator{backend: backend}, plan)
	if err != nil {
		t.Fatalf("prepare Pages deployment with non-required deselection: %v", err)
	}
	approved, err := state.AuthorizeDeployment(context.Background(), deploymentTestCoordinator{backend: backend}, deploymentTestApproval(plan, prepared.Deployment.Generation, "reviewer", plan.PreparedAt.Add(time.Minute)))
	if err != nil {
		t.Fatalf("authorize Pages deployment: %v", err)
	}
	if _, err = state.ImportDeploymentReceipt(context.Background(), deploymentTestCoordinator{backend: backend}, deploymentTestReceipt(approved.Deployment, "run-retirement-deselection", true, plan.PreparedAt.Add(2*time.Minute))); err != nil {
		t.Fatalf("import Pages deployment: %v", err)
	}
	ack, err := state.AcknowledgeModuleDeployment(context.Background(), selectionTestCoordinator{backend: backend}, request.ModuleID, request.Work.AVMReleaseVersion)
	if err != nil {
		t.Fatalf("acknowledge Pages membership containing an exact non-required deselection: %v", err)
	}
	_, completedSnapshot := readSelectionTestSnapshot(t, backend)
	if _, err = state.VerifyDoneModule(completedSnapshot, request.ModuleID, request.Work.AVMReleaseVersion, ack.AcceptedCommit); err != nil {
		t.Fatalf("verify Done with a non-required deselection: %v", err)
	}
	assertDeploymentTestSlot(t, backend, request.ModuleID, request.Work.AVMReleaseVersion, "released", ack.DeploymentReceiptID)
	assertDeploymentProjectionStatus(t, backend, request.ModuleID, "done", "1", "1", "1", "0")
}

func TestPagesKeepsSlotWhenRetirementDeselectionLeavesRequiredContextUnselected(t *testing.T) {
	required := contextset.Context{State: contextset.ContextState{
		ContextID: "azurerm/5", Track: "5.x", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible,
	}}
	backend, request, completionID := selectionFixtureWithRequiredContexts(t, false, []contextset.Context{required})
	selected, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID))
	if err != nil {
		t.Fatalf("select one required context: %v", err)
	}
	_, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	markCatalogModuleRetiredForDeploymentTest(t, &snapshot, request.ModuleID)
	deselected := seedRetirementDeselectionForDeploymentTest(t, &snapshot, request.ModuleID, request.Work.AVMReleaseVersion, "azurerm/5", request.UpdatedAt.Add(4*time.Minute))
	backend.snapshot = snapshot
	plan := deploymentTestPlan(t, selected, 0)
	deselectionID := deselected.ID
	plan.Membership = append(plan.Membership, index.ModuleMembership{
		ModuleID: request.ModuleID, AVMReleaseVersion: request.Work.AVMReleaseVersion,
		ContextID: "azurerm/5", SelectionID: &deselectionID,
	})
	sort.Slice(plan.Membership, func(left, right int) bool { return plan.Membership[left].ContextID < plan.Membership[right].ContextID })
	plan.PreparedAt = request.UpdatedAt.Add(5 * time.Minute)
	prepared, err := state.PrepareDeployment(context.Background(), deploymentTestCoordinator{backend: backend}, plan)
	if err != nil {
		t.Fatalf("prepare Pages deployment with required deselection: %v", err)
	}
	approved, err := state.AuthorizeDeployment(context.Background(), deploymentTestCoordinator{backend: backend}, deploymentTestApproval(plan, prepared.Deployment.Generation, "reviewer", plan.PreparedAt.Add(time.Minute)))
	if err != nil {
		t.Fatalf("authorize Pages deployment: %v", err)
	}
	if _, err = state.ImportDeploymentReceipt(context.Background(), deploymentTestCoordinator{backend: backend}, deploymentTestReceipt(approved.Deployment, "run-required-retirement-deselection", true, plan.PreparedAt.Add(2*time.Minute))); err != nil {
		t.Fatalf("import Pages deployment: %v", err)
	}
	if _, err = state.AcknowledgeModuleDeployment(context.Background(), selectionTestCoordinator{backend: backend}, request.ModuleID, request.Work.AVMReleaseVersion); err != nil {
		t.Fatalf("acknowledge exact partial Pages membership: %v", err)
	}
	assertDeploymentTestSlot(t, backend, request.ModuleID, request.Work.AVMReleaseVersion, "active", "")
	assertDeploymentProjectionStatus(t, backend, request.ModuleID, "partial", "1", "1", "2", "0")
}

func TestPagesRetiresModuleWhenExactRequiredMembershipContainsOnlyDeselections(t *testing.T) {
	backend, request, completionID := selectionFixture(t, false)
	selected, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID))
	if err != nil {
		t.Fatalf("select current assessment: %v", err)
	}
	_, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	markCatalogModuleRetiredForDeploymentTest(t, &snapshot, request.ModuleID)
	deselected := seedRetirementDeselectionForDeploymentTest(t, &snapshot, request.ModuleID, request.Work.AVMReleaseVersion, request.Work.ContextID, request.UpdatedAt.Add(4*time.Minute))
	backend.snapshot = snapshot

	plan := deploymentTestPlan(t, selected, 0)
	deselectionID := deselected.ID
	plan.Membership = []index.ModuleMembership{{
		ModuleID: request.ModuleID, AVMReleaseVersion: request.Work.AVMReleaseVersion,
		ContextID: request.Work.ContextID, SelectionID: &deselectionID,
	}}
	plan.PreparedAt = request.UpdatedAt.Add(5 * time.Minute)
	prepared, err := state.PrepareDeployment(context.Background(), deploymentTestCoordinator{backend: backend}, plan)
	if err != nil {
		t.Fatalf("prepare Pages deployment with only retirement deselections: %v", err)
	}
	approved, err := state.AuthorizeDeployment(context.Background(), deploymentTestCoordinator{backend: backend}, deploymentTestApproval(plan, prepared.Deployment.Generation, "reviewer", plan.PreparedAt.Add(time.Minute)))
	if err != nil {
		t.Fatalf("authorize Pages deployment: %v", err)
	}
	if _, err = state.ImportDeploymentReceipt(context.Background(), deploymentTestCoordinator{backend: backend}, deploymentTestReceipt(approved.Deployment, "run-all-retirement-deselections", true, plan.PreparedAt.Add(2*time.Minute))); err != nil {
		t.Fatalf("import Pages deployment: %v", err)
	}
	ack, err := state.AcknowledgeModuleDeployment(context.Background(), selectionTestCoordinator{backend: backend}, request.ModuleID, request.Work.AVMReleaseVersion)
	if err != nil {
		t.Fatalf("acknowledge exact all-deselected retired membership: %v", err)
	}
	_, completedSnapshot := readSelectionTestSnapshot(t, backend)
	if _, err = state.VerifyDoneModule(completedSnapshot, request.ModuleID, request.Work.AVMReleaseVersion, ack.AcceptedCommit); !errors.Is(err, state.ErrDeploymentNotCurrent) {
		t.Fatalf("retired module was incorrectly certified as assessment Done: %v", err)
	}
	if _, err = state.VerifyRetiredModule(completedSnapshot, request.ModuleID, request.Work.AVMReleaseVersion, request.SourceSHA, ack.DeploymentReceiptID, ack.MembershipDigest); err != nil {
		t.Fatalf("verify exact retired module deployment acknowledgement: %v", err)
	}
	assertDeploymentTestSlot(t, backend, request.ModuleID, request.Work.AVMReleaseVersion, "released", ack.DeploymentReceiptID)
	assertDeploymentProjectionStatus(t, backend, request.ModuleID, "retired", "0", "0", "1", "0")
	intentPath, err := identity.StateProjectionIntentPath(request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	_, completedSnapshot = readSelectionTestSnapshot(t, backend)
	var intent projection.DesiredIntent
	if record := completedSnapshot.Records[intentPath]; record.Kind != "projection_intent" || json.Unmarshal(record.Data, &intent) != nil || intent.Status != "retired" {
		t.Fatalf("retired module projection intent = %+v; want neutral retired status", intent)
	}
	for _, field := range intent.OwnedFields {
		if field.Name == "published_commit" {
			t.Fatal("retired module projection claims an assessment publication commit")
		}
	}
}

func TestPagesKeepsSlotWhenRetirementMembershipOmitsRequiredContext(t *testing.T) {
	additional := contextset.Context{State: contextset.ContextState{
		ContextID: "azurerm/5", Track: "azurerm/5", Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible,
	}}
	backend, request, completionID := selectionFixtureWithRequiredContexts(t, false, []contextset.Context{additional})
	selected, err := state.SelectAccepted(context.Background(), selectionTestCoordinator{backend: backend}, selectionInput(request, completionID))
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	markCatalogModuleRetiredForDeploymentTest(t, &snapshot, request.ModuleID)
	deselected := seedRetirementDeselectionForDeploymentTest(t, &snapshot, request.ModuleID, request.Work.AVMReleaseVersion, request.Work.ContextID, request.UpdatedAt.Add(4*time.Minute))
	backend.snapshot = snapshot
	plan := deploymentTestPlan(t, selected, 0)
	deselectionID := deselected.ID
	plan.Membership = []index.ModuleMembership{{
		ModuleID: request.ModuleID, AVMReleaseVersion: request.Work.AVMReleaseVersion,
		ContextID: request.Work.ContextID, SelectionID: &deselectionID,
	}}
	plan.PreparedAt = request.UpdatedAt.Add(5 * time.Minute)
	prepared, err := state.PrepareDeployment(context.Background(), deploymentTestCoordinator{backend: backend}, plan)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := state.AuthorizeDeployment(context.Background(), deploymentTestCoordinator{backend: backend}, deploymentTestApproval(plan, prepared.Deployment.Generation, "reviewer", plan.PreparedAt.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = state.ImportDeploymentReceipt(context.Background(), deploymentTestCoordinator{backend: backend}, deploymentTestReceipt(approved.Deployment, "run-incomplete-retirement-membership", true, plan.PreparedAt.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err = state.AcknowledgeModuleDeployment(context.Background(), selectionTestCoordinator{backend: backend}, request.ModuleID, request.Work.AVMReleaseVersion); !errors.Is(err, state.ErrDeploymentNotCurrent) {
		t.Fatalf("incomplete retired membership error = %v, want ErrDeploymentNotCurrent", err)
	}
	assertDeploymentTestSlot(t, backend, request.ModuleID, request.Work.AVMReleaseVersion, "active", "")
}

func markCatalogModuleRetiredForDeploymentTest(t *testing.T, snapshot *state.Snapshot, moduleID string) {
	t.Helper()
	const path = "state/discovery/catalog.json"
	var durable struct {
		SchemaVersion int                   `json:"schema_version"`
		Accepted      catalog.AcceptedState `json:"accepted"`
	}
	if err := json.Unmarshal(snapshot.Records[path].Data, &durable); err != nil {
		t.Fatal(err)
	}
	for i := range durable.Accepted.Modules {
		if durable.Accepted.Modules[i].Module.ID == moduleID {
			durable.Accepted.Modules[i].Active = false
			durable.Accepted.Modules[i].MissingObservations = catalog.RequiredMissingObservations
		}
	}
	encoded, err := json.Marshal(durable)
	if err != nil {
		t.Fatal(err)
	}
	record := snapshot.Records[path]
	record.Data = encoded
	snapshot.Records[path] = record
}

func seedRetirementDeselectionForDeploymentTest(t *testing.T, snapshot *state.Snapshot, moduleID, release, contextID string, selectedAt time.Time) state.SelectionReceipt {
	t.Helper()
	receipt := state.SelectionReceipt{
		SchemaVersion: 3, RecordType: "selection_receipt", ModuleID: moduleID, AVMReleaseVersion: release,
		ContextID: contextID, Sequence: 2, DesiredGeneration: 2,
		InputFingerprint: strings.Repeat("e", 64), EvidenceCommit: strings.Repeat("f", 40), SelectedAt: selectedAt.UTC(),
	}
	var err error
	receipt.ID, err = identity.ID("selection", receipt.SchemaVersion, struct {
		SchemaVersion     int       `json:"schema_version"`
		RecordType        string    `json:"record_type"`
		ModuleID          string    `json:"module_id"`
		AVMReleaseVersion string    `json:"avm_release_version"`
		ContextID         string    `json:"context_id"`
		Sequence          uint64    `json:"sequence"`
		DesiredGeneration uint64    `json:"desired_generation"`
		SourceSHA         *string   `json:"source_sha"`
		FactsID           *string   `json:"facts_id"`
		InputFingerprint  string    `json:"input_fingerprint"`
		AnalysisID        *string   `json:"analysis_id"`
		EvidenceCommit    string    `json:"evidence_commit"`
		SelectedAt        time.Time `json:"selected_at"`
	}{receipt.SchemaVersion, receipt.RecordType, receipt.ModuleID, receipt.AVMReleaseVersion, receipt.ContextID, receipt.Sequence,
		receipt.DesiredGeneration, receipt.SourceSHA, receipt.FactsID, receipt.InputFingerprint, receipt.AnalysisID, receipt.EvidenceCommit, receipt.SelectedAt})
	if err != nil {
		t.Fatal(err)
	}
	selectionPath, err := identity.StateSelectionPath(receipt.ModuleID, contextID, receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	receiptData, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[selectionPath] = state.Record{Kind: "selection", SchemaVersion: 1, Data: receiptData}
	pointer := state.SelectionPointerRecord{
		SchemaVersion: 1, ModuleID: receipt.ModuleID, AVMReleaseVersion: receipt.AVMReleaseVersion,
		ContextID: contextID, Sequence: receipt.Sequence, SelectionID: receipt.ID,
		DesiredGeneration: receipt.DesiredGeneration, UpdatedAt: receipt.SelectedAt,
	}
	currentPath := "state/selections/" + receipt.ModuleID + "/versions/" + receipt.AVMReleaseVersion + "/" + contextID + "/current.json"
	pointerData, err := json.Marshal(pointer)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[currentPath] = state.Record{Kind: "selection", SchemaVersion: 1, Data: pointerData}
	export := state.SelectionExport{
		SchemaVersion: 1, RecordType: "selection_export_intent", ID: receipt.ID, SelectionID: receipt.ID,
		ModuleID: receipt.ModuleID, ContextID: contextID, Status: state.ExportPending,
		Receipt: receipt, CreatedAt: receipt.SelectedAt,
	}
	exportData, err := json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records["state/exports/"+receipt.ID+".json"] = state.Record{Kind: "export", SchemaVersion: 1, Data: exportData}
	return receipt
}

func deploymentMembershipForSelections(selections ...state.SelectionResult) []index.ModuleMembership {
	membership := make([]index.ModuleMembership, 0, len(selections))
	for _, selected := range selections {
		selectionID, analysisID := selected.Receipt.ID, *selected.Receipt.AnalysisID
		membership = append(membership, index.ModuleMembership{
			ModuleID: selected.Receipt.ModuleID, AVMReleaseVersion: selected.Receipt.AVMReleaseVersion,
			ContextID: selected.Receipt.ContextID, SelectionID: &selectionID, AnalysisID: &analysisID,
		})
	}
	sort.Slice(membership, func(left, right int) bool {
		if membership[left].ModuleID != membership[right].ModuleID {
			return membership[left].ModuleID < membership[right].ModuleID
		}
		if membership[left].AVMReleaseVersion != membership[right].AVMReleaseVersion {
			return membership[left].AVMReleaseVersion < membership[right].AVMReleaseVersion
		}
		return membership[left].ContextID < membership[right].ContextID
	})
	return membership
}

func publishDeploymentForSelections(t *testing.T, backend *selectionBackend, selected state.SelectionResult, membership []index.ModuleMembership, generation uint64, at time.Time) state.ModuleDeploymentAcknowledgement {
	t.Helper()
	coordinator := deploymentTestCoordinator{backend: backend}
	plan := deploymentTestPlan(t, selected, 0)
	plan.Membership = membership
	plan.PreparedAt = at
	plan.DatasetID = selectionTestID(t, "dataset", struct {
		Generation uint64 `json:"generation"`
	}{generation})
	if generation > 1 {
		plan.AcceptedCommit = strings.Repeat("1", 40)
		plan.TargetCommit = strings.Repeat("2", 40)
	}
	prepared, err := state.PrepareDeployment(context.Background(), coordinator, plan)
	if err != nil {
		t.Fatalf("prepare deployment generation %d: %v", generation, err)
	}
	authorized, err := state.AuthorizeDeployment(context.Background(), coordinator, deploymentTestApproval(plan, prepared.Deployment.Generation, "reviewer", at.Add(time.Minute)))
	if err != nil {
		t.Fatalf("authorize deployment generation %d: %v", generation, err)
	}
	receipt := deploymentTestReceipt(authorized.Deployment, fmt.Sprintf("run-partial-membership-%d", generation), true, at.Add(2*time.Minute))
	if _, err = state.ImportDeploymentReceipt(context.Background(), coordinator, receipt); err != nil {
		t.Fatalf("import deployment generation %d: %v", generation, err)
	}
	acknowledgement, err := state.AcknowledgeModuleDeployment(context.Background(), selectionTestCoordinator{backend: backend}, selected.Receipt.ModuleID, selected.Receipt.AVMReleaseVersion)
	if err != nil {
		t.Fatalf("acknowledge deployment generation %d: %v", generation, err)
	}
	return acknowledgement
}

func assertDeploymentProjectionStatus(t *testing.T, backend *selectionBackend, moduleID, wantStatus, wantSelected, wantPublished, wantRequired, wantUnresolved string) {
	t.Helper()
	_, snapshot := readSelectionTestSnapshot(t, backend)
	path, err := identity.StateProjectionIntentPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	var intent projection.DesiredIntent
	record, exists := snapshot.Records[path]
	if !exists || json.Unmarshal(record.Data, &intent) != nil || intent.Status != wantStatus || intent.ValidatePath(path) != nil {
		t.Fatalf("deployment projection intent = %+v; want status %s", intent, wantStatus)
	}
	if !containsOwnedField(intent.OwnedFields, "selected_contexts", wantSelected) || !containsOwnedField(intent.OwnedFields, "published_contexts", wantPublished) || !containsOwnedField(intent.OwnedFields, "required_contexts", wantRequired) || !containsOwnedField(intent.OwnedFields, "unresolved_contexts", wantUnresolved) {
		t.Fatalf("deployment projection coverage = %+v; want selected=%s published=%s required=%s unresolved=%s", intent.OwnedFields, wantSelected, wantPublished, wantRequired, wantUnresolved)
	}
}

func assertRequestStatus(t *testing.T, backend *selectionBackend, requestID, want string) {
	t.Helper()
	_, snapshot := readSelectionTestSnapshot(t, backend)
	var request queue.Request
	record, exists := snapshot.Records["state/requests/"+requestID+".json"]
	if !exists || json.Unmarshal(record.Data, &request) != nil || request.Status != want {
		t.Fatalf("request %s = %+v, want status %s", requestID, request, want)
	}
}

func acceptedRequiredContextAssessment(t *testing.T, base queue.Request, contextID, track string, at time.Time) queue.Request {
	t.Helper()
	request, err := queue.NewAssessmentRequest(queue.AssessmentInput{
		ModuleID: base.ModuleID, SourceSHA: base.SourceSHA, AVMReleaseVersion: base.Work.AVMReleaseVersion,
		ContextID: contextID, Track: track, DesiredGeneration: base.Work.DesiredGeneration,
		FactsID: base.Work.FactsID, DefinitionIDs: base.Work.DefinitionIDs,
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := queue.ClaimModuleRequest([]queue.Request{request}, request.ID, "required-context-worker", at, at.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	completionID := selectionTestID(t, "completion", struct {
		RequestID string `json:"request_id"`
	}{request.ID})
	sealed, err := queue.SealCompletion(claimed[0], "required-context-worker", 1, queue.SealedCompletion{
		ID: completionID, RequestID: request.ID, InputFingerprint: request.Fingerprint,
		DesiredGeneration: request.Work.DesiredGeneration, Fence: 1, ArtifactDigest: strings.Repeat("c", 64),
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
