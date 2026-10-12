package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/site"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

const indexedPublicationManifestName = "publication.json"

var publicationProducerRunPattern = regexp.MustCompile(`^[0-9]+-[0-9]+$`)

type moduleReleaseTarget struct{ moduleID, release string }

type assessmentWakeFailure struct {
	stage     string
	moduleID  string
	contextID string
	err       error
}

func (failure *assessmentWakeFailure) Error() string {
	return fmt.Sprintf("assessment wake %s failed: %v", failure.stage, failure.err)
}

func (failure *assessmentWakeFailure) Unwrap() error { return failure.err }

func assessmentWakeFailureAt(stage, moduleID string, err error, contextIDs ...string) error {
	var existing *assessmentWakeFailure
	if errors.As(err, &existing) {
		return err
	}
	failure := &assessmentWakeFailure{stage: stage, moduleID: moduleID, err: err}
	if len(contextIDs) != 0 {
		failure.contextID = contextIDs[0]
	}
	return failure
}

func validPublicationProducerRun(value string) bool {
	return publicationProducerRunPattern.MatchString(value)
}

func prepareIndexedSitePublication(branch, checkoutRoot, dataRoot, siteRoot, acceptedCommit, producerRun, moduleNames string, preparedAt time.Time) (bool, error) {
	if identity.ValidateSHA1(acceptedCommit) != nil || !validPublicationProducerRun(producerRun) || preparedAt.IsZero() {
		return false, fmt.Errorf("exact accepted commit, producer run, and preparation time are required")
	}
	moduleIDs, err := validateSiteAssessmentModuleScope(dataRoot, moduleNames)
	if err != nil {
		return false, fmt.Errorf("validate exact site assessment module scope: %w", err)
	}
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return false, err
	}
	producer, err := sitePublicationProducer(acceptedCommit, producerRun)
	if err != nil {
		return false, err
	}
	if err = importAcceptedSelectionExports(context.Background(), setup.coordinator, dataRoot, acceptedCommit, preparedAt, moduleIDs); err != nil {
		return false, fmt.Errorf("reconcile accepted selection exports before site-cycle assessment: %w", err)
	}
	if err = queueAcceptedSiteAssessments(context.Background(), setup.coordinator, dataRoot, acceptedCommit, producerRun, preparedAt, moduleIDs); err != nil {
		return false, fmt.Errorf("queue exact accepted assessment inputs for the site cycle: %w", err)
	}
	pendingSelectionBarriers, selectErr := selectAcceptedSiteAssessments(context.Background(), setup.coordinator, producer, preparedAt, moduleIDs)
	if selectErr != nil {
		return false, fmt.Errorf("select exact accepted assessment results for the site cycle: %w", selectErr)
	}
	if pendingSelectionBarriers != 0 {
		slog.Info("site publication deferred for acceptance projection barriers", "module_scope", moduleIDs,
			"pending_selection_barriers", pendingSelectionBarriers, "state_head_required", true)
		return false, nil
	}
	pending, err := pendingSelectionExportsInScope(context.Background(), setup.coordinator, moduleIDs)
	if err != nil {
		return false, fmt.Errorf("read selection exports prepared by the site cycle: %w", err)
	}
	if len(pending) != 0 {
		slog.Info("site publication deferred for reviewed selection exports", "module_scope", moduleIDs,
			"pending_exports", len(pending), "state_head_required", true)
		return false, nil
	}
	if err = site.BuildIndexedAt(dataRoot, siteRoot, preparedAt); err != nil {
		return false, fmt.Errorf("build indexed site from accepted data: %w", err)
	}
	manifest, err := publish.CreateIndexed(dataRoot, siteRoot, acceptedCommit, producerRun, 1, preparedAt)
	if err != nil {
		return false, fmt.Errorf("prepare combined indexed publication identity: %w", err)
	}
	plan := state.DeploymentPlan{
		AcceptedCommit: manifest.AcceptedCommit, DatasetID: manifest.DatasetID,
		DataDigest: manifest.DataDigest, ModuleDataDigest: manifest.ModuleDataDigest,
		ProviderDataDigest: manifest.ProviderDataDigest, SiteDigest: manifest.SiteDigest,
		Generator: manifest.Generator, ModuleIDs: append([]string(nil), manifest.ModuleIDs...),
		Membership:   append([]index.ModuleMembership(nil), manifest.ModuleMembership...),
		TargetCommit: acceptedCommit, PreparedAt: preparedAt.UTC(),
	}
	prepared, err := state.PrepareDeployment(context.Background(), setup.coordinator, plan)
	if err != nil {
		return false, fmt.Errorf("persist combined site publication generation: %w", err)
	}
	manifest, err = publish.CreateIndexed(dataRoot, siteRoot, acceptedCommit, producerRun, prepared.Deployment.Generation, preparedAt)
	if err != nil {
		return false, fmt.Errorf("bind combined site publication generation: %w", err)
	}
	if err = publish.Write(filepath.Join(siteRoot, indexedPublicationManifestName), manifest); err != nil {
		return false, fmt.Errorf("write combined site publication manifest: %w", err)
	}
	slog.Info("combined site publication prepared", "accepted_commit", acceptedCommit, "dataset_id", manifest.DatasetID,
		"deployment_generation", prepared.Deployment.Generation, "modules", len(manifest.ModuleIDs), "membership", len(manifest.ModuleMembership),
		"module_data_digest", manifest.ModuleDataDigest, "provider_data_digest", manifest.ProviderDataDigest, "assessment_scope", moduleIDs, "state_head", prepared.Head)
	return true, nil
}

func pendingSelectionExportsInScope(ctx context.Context, coordinator state.TransactionCoordinator, moduleIDs []string) ([]state.SelectionExport, error) {
	if coordinator == nil || len(moduleIDs) == 0 {
		return nil, fmt.Errorf("selection readiness requires state and a non-empty module scope")
	}
	_, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("read selection export state: %w", err)
	}
	pending, err := state.PendingSelectionExports(snapshot)
	if err != nil {
		return nil, fmt.Errorf("validate pending selection exports: %w", err)
	}
	return pendingSelectionExportsForModules(pending, moduleIDs), nil
}

func pendingSelectionExportsForModules(pending []state.SelectionExport, moduleIDs []string) []state.SelectionExport {
	allowed := moduleIDSet(moduleIDs)
	inScope := make([]state.SelectionExport, 0, len(pending))
	for _, export := range pending {
		if _, ok := allowed[export.ModuleID]; ok {
			inScope = append(inScope, export)
		}
	}
	return inScope
}

func validateSiteAssessmentModuleScope(dataRoot, moduleNames string) ([]string, error) {
	records, err := index.ReadRecords(dataRoot, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("read accepted catalog: %w", err)
	}
	active := make(map[string]catalog.Module, len(records.Modules))
	for _, record := range records.Modules {
		if record.Lifecycle != "active" {
			continue
		}
		parts := strings.SplitN(record.ModuleID, "-", 3)
		if len(parts) != 3 || parts[0] != "avm" {
			continue
		}
		active[record.ModuleID] = catalog.Module{ID: record.ModuleID, Type: catalog.Kind(parts[1])}
	}
	selected, err := selectNamedManualModules(active, moduleNames)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("site assessment module scope must not be empty")
	}
	return selected, nil
}

// validatePilotAssessmentModuleScope limits the one-time HUMAN-002 pilot wake
// to the exact ten modules approved for assessment and publication.
func validatePilotAssessmentModuleScope(dataRoot, moduleNames string) ([]string, error) {
	selected, err := validateSiteAssessmentModuleScope(dataRoot, moduleNames)
	if err != nil {
		return nil, err
	}
	if len(selected) != len(approvedProviderFactsScopeModules) {
		return nil, fmt.Errorf("assessment wake requires the exact approved ten-module pilot scope")
	}
	for _, moduleID := range selected {
		if _, approved := approvedProviderFactsScopeModules[moduleID]; !approved {
			return nil, fmt.Errorf("assessment wake includes a module outside the approved pilot scope")
		}
	}
	return selected, nil
}

// importAcceptedSelectionExports acknowledges only pending durable selections
// whose exact public receipt is already present in the accepted module tree.
func importAcceptedSelectionExports(ctx context.Context, coordinator state.TransactionCoordinator, dataRoot, acceptedCommit string, importedAt time.Time, moduleIDs []string) error {
	if coordinator == nil || identity.ValidateSHA1(acceptedCommit) != nil || importedAt.IsZero() || len(moduleIDs) == 0 {
		return fmt.Errorf("accepted selection import requires state, exact commit, time, and module scope")
	}
	allowed := moduleIDSet(moduleIDs)
	records, err := index.ReadRecords(dataRoot, time.Time{})
	if err != nil {
		return fmt.Errorf("read canonical selection receipts: %w", err)
	}
	accepted := make(map[string]index.SelectionReceipt, len(records.Selections))
	for _, receipt := range records.Selections {
		if _, duplicate := accepted[receipt.ID]; duplicate {
			return fmt.Errorf("accepted data contains duplicate selection receipt IDs")
		}
		accepted[receipt.ID] = receipt
	}
	_, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return fmt.Errorf("read durable selection outboxes: %w", err)
	}
	pending, err := state.PendingSelectionExports(snapshot)
	if err != nil {
		return fmt.Errorf("validate durable selection outboxes: %w", err)
	}
	imported := 0
	for _, export := range pending {
		if _, inScope := allowed[export.ModuleID]; !inScope {
			continue
		}
		canonical, exists := accepted[export.ID]
		if !exists {
			continue
		}
		if !sameCanonicalSelection(canonical, export.Receipt) {
			return fmt.Errorf("accepted selection receipt %s does not match its durable state outbox", export.ID)
		}
		result, importErr := state.ImportSelectionReceipt(ctx, coordinator, export.Receipt, acceptedCommit, importedAt.UTC())
		if importErr != nil {
			return fmt.Errorf("import merged selection receipt %s: %w", export.ID, importErr)
		}
		imported++
		slog.Info("accepted selection receipt reconciled", "module_id", export.ModuleID,
			"selection_id", export.ID, "state_head", result.Head, "changed", result.Changed)
	}
	slog.Info("accepted selection receipt reconciliation completed", "accepted_receipts", imported, "pending_exports", len(pending))
	return nil
}

func sameCanonicalSelection(canonical index.SelectionReceipt, operational state.SelectionReceipt) bool {
	return canonical.ID == operational.ID && canonical.ModuleID == operational.ModuleID &&
		canonical.AVMReleaseVersion == operational.AVMReleaseVersion && canonical.ContextID == operational.ContextID &&
		canonical.Sequence == operational.Sequence && canonical.DesiredGeneration == operational.DesiredGeneration &&
		optionalSelectionString(canonical.SourceSHA) == optionalSelectionString(operational.SourceSHA) &&
		optionalSelectionString(canonical.FactsID) == optionalSelectionString(operational.FactsID) &&
		canonical.InputFingerprint == operational.InputFingerprint &&
		optionalSelectionString(canonical.AnalysisID) == optionalSelectionString(operational.AnalysisID) &&
		canonical.EvidenceCommit == operational.EvidenceCommit && canonical.SelectedAt.Equal(operational.SelectedAt)
}

func optionalSelectionString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func queueAcceptedSiteAssessments(ctx context.Context, coordinator state.TransactionCoordinator, dataRoot, acceptedCommit, producerRun string, queuedAt time.Time, moduleIDs []string) error {
	producer, err := workflowProducer(acceptedCommit, producerRun, "manual-site-publish")
	if err != nil {
		return err
	}
	return queueAcceptedAssessments(ctx, coordinator, dataRoot, acceptedCommit, producerRun, queuedAt, moduleIDs, producer)
}

func wakeAcceptedAssessments(branch, checkoutRoot, dataRoot, acceptedCommit, producerRun, moduleNames string, queuedAt time.Time) error {
	if identity.ValidateSHA1(acceptedCommit) != nil || !validPublicationProducerRun(producerRun) || queuedAt.IsZero() {
		return fmt.Errorf("exact accepted commit, workflow run, and wake time are required")
	}
	moduleIDs, err := validatePilotAssessmentModuleScope(dataRoot, moduleNames)
	if err != nil {
		return fmt.Errorf("validate exact pilot assessment wake scope: %w", err)
	}
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return err
	}
	producer, err := workflowProducer(acceptedCommit, producerRun, "manual-assessment-wake")
	if err != nil {
		return err
	}
	slog.Info("pilot assessment wake started", "accepted_commit", acceptedCommit, "module_count", len(moduleIDs), "producer_run", producerRun)
	if err := queueAcceptedAssessments(context.Background(), setup.coordinator, dataRoot, acceptedCommit, producerRun, queuedAt, moduleIDs, producer); err != nil {
		return fmt.Errorf("queue exact accepted pilot assessment inputs: %w", err)
	}
	return nil
}

func queueAcceptedAssessments(ctx context.Context, coordinator state.TransactionCoordinator, dataRoot, acceptedCommit, producerRun string, queuedAt time.Time, moduleIDs []string, producer projection.WorkflowProvenance) error {
	if coordinator == nil || identity.ValidateSHA1(acceptedCommit) != nil || !validPublicationProducerRun(producerRun) || queuedAt.IsZero() || len(moduleIDs) == 0 {
		return fmt.Errorf("exact accepted commit, workflow run, coordinator, time, and module scope are required")
	}
	if producer.Validate() != nil || producer.Commit != acceptedCommit || fmt.Sprintf("%s-%d", producer.RunID, producer.RunAttempt) != producerRun {
		return fmt.Errorf("assessment wake producer must match the exact accepted commit and workflow run")
	}
	head, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return &assessmentWakeFailure{stage: "read_state", err: err}
	}
	if head == "" {
		return fmt.Errorf("state branch head is required for assessment planning")
	}
	cycle, err := planAcceptedSiteAssessments(dataRoot, snapshot, queuedAt, moduleIDs)
	if err != nil {
		return assessmentWakeFailureAt("plan_accepted_inputs", "", err)
	}
	byModule := make(map[string][]queue.Request)
	acceptedRequestsSkipped := 0
	for _, request := range cycle.Requests {
		alreadyAccepted, acceptedErr := acceptedAssessmentRequestAlreadyCurrent(snapshot, request)
		if acceptedErr != nil {
			return assessmentWakeFailureAt("inspect_current_assessment", request.ModuleID, acceptedErr)
		}
		if alreadyAccepted {
			acceptedRequestsSkipped++
			continue
		}
		byModule[request.ModuleID] = append(byModule[request.ModuleID], request)
	}
	deferredByModule := make(map[string][]queue.AssessmentWakeDeferred)
	for _, deferred := range cycle.Deferred {
		deferredByModule[deferred.ModuleID] = append(deferredByModule[deferred.ModuleID], deferred)
	}
	queuedModuleIDs := make([]string, 0, len(byModule))
	for moduleID := range byModule {
		queuedModuleIDs = append(queuedModuleIDs, moduleID)
	}
	sort.Strings(queuedModuleIDs)
	queued := 0
	for _, moduleID := range queuedModuleIDs {
		result, enqueueErr := queue.EnqueueAssessmentWake(ctx, coordinator, producer, acceptedCommit, byModule[moduleID], deferredByModule[moduleID], queuedAt)
		if enqueueErr != nil {
			return &assessmentWakeFailure{stage: "persist_module_queue", moduleID: moduleID, err: enqueueErr}
		}
		queued += len(result.Requests)
		slog.Info("assessment wake requests persisted", "module_id", moduleID, "requests", len(result.Requests), "deferred_contexts", len(result.Deferred), "state_head", result.Head, "changed", result.Changed)
	}
	slog.Info("assessment wake planning completed", "accepted_commit", acceptedCommit, "module_count", len(byModule), "eligible_requests", len(cycle.Requests), "already_accepted_requests_skipped", acceptedRequestsSkipped, "deferred_contexts", len(cycle.Deferred), "requests_persisted", queued)
	return nil
}

func acceptedAssessmentRequestAlreadyCurrent(snapshot state.Snapshot, planned queue.Request) (bool, error) {
	if planned.Validate() != nil || planned.Work == nil || planned.Work.Phase != queue.PhaseAssess {
		return false, fmt.Errorf("planned assessment request is invalid")
	}
	path := "state/requests/" + planned.ID + ".json"
	record, exists := snapshot.Records[path]
	if !exists {
		return false, nil
	}
	if record.Kind != "request" || record.SchemaVersion != 1 {
		return false, fmt.Errorf("current assessment request record has an invalid kind or schema")
	}
	var current queue.Request
	if err := json.Unmarshal(record.Data, &current); err != nil {
		return false, fmt.Errorf("decode current assessment request: %w", err)
	}
	if err := current.Validate(); err != nil {
		return false, fmt.Errorf("validate current assessment request: %w", err)
	}
	return current.ID == planned.ID && current.ModuleID == planned.ModuleID && current.Fingerprint == planned.Fingerprint &&
		current.SourceSHA == planned.SourceSHA && current.Status == queue.StatusAccepted && current.Work != nil &&
		current.Work.Phase == planned.Work.Phase && current.Work.AVMReleaseVersion == planned.Work.AVMReleaseVersion &&
		current.Work.ContextID == planned.Work.ContextID && current.Work.DesiredGeneration == planned.Work.DesiredGeneration &&
		current.Work.FactsID == planned.Work.FactsID && current.LastAcceptedFingerprint == planned.Fingerprint &&
		current.LastAcceptedSourceSHA == planned.SourceSHA && current.Work.Completion != nil, nil
}

func selectAcceptedSiteAssessments(ctx context.Context, coordinator state.TransactionCoordinator, producer projection.WorkflowProvenance, selectedAt time.Time, moduleIDs []string) (int, error) {
	if coordinator == nil || producer.Validate() != nil || producer.WorkflowID != "manual-site-publish" || selectedAt.IsZero() || len(moduleIDs) == 0 {
		return 0, fmt.Errorf("selection requires the exact site workflow producer, timestamp, and module scope")
	}
	allowed := moduleIDSet(moduleIDs)
	_, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return 0, fmt.Errorf("read accepted assessment state for site selection: %w", err)
	}
	candidates, err := state.AcceptedAssessmentSelectionCandidates(snapshot, "", selectedAt)
	if err != nil {
		return 0, fmt.Errorf("plan receipt-backed accepted assessment selections: %w", err)
	}
	selected, skipped, pendingBarriers := 0, 0, 0
	for _, candidate := range candidates {
		if _, inScope := allowed[candidate.ModuleID]; !inScope {
			continue
		}
		if candidate.ProjectionBarrierPending {
			pendingBarriers++
			slog.Warn("site-cycle selection deferred", "module_id", candidate.ModuleID, "context_id", candidate.ContextID, "reason", "exact acceptance projection barrier is still pending")
			continue
		}
		result, selectErr := state.SelectAccepted(ctx, coordinator, state.SelectionInput{
			ModuleID: candidate.ModuleID, ContextID: candidate.ContextID, RequestID: candidate.RequestID,
			CompletionID: candidate.CompletionID, AcceptanceHandoffID: candidate.AcceptanceHandoffID,
			AnalysisID: candidate.AnalysisID, EvidenceCommit: candidate.EvidenceCommit,
			SelectedAt: selectedAt.UTC(), Producer: producer,
		})
		if errors.Is(selectErr, state.ErrSelectionBarrierPending) || errors.Is(selectErr, state.ErrSelectionStale) || errors.Is(selectErr, state.ErrSelectionIneligible) {
			skipped++
			slog.Warn("site-cycle selection deferred", "module_id", candidate.ModuleID, "context_id", candidate.ContextID, "reason", "accepted result lost readiness before its fenced selection transaction")
			continue
		}
		if selectErr != nil {
			return pendingBarriers, fmt.Errorf("select accepted result for %s %s: %w", candidate.ModuleID, candidate.ContextID, selectErr)
		}
		selected++
		slog.Info("site-cycle assessment result selected", "module_id", candidate.ModuleID, "context_id", candidate.ContextID, "selection_id", result.Receipt.ID, "changed", result.Changed)
	}
	slog.Info("site-cycle selection completed", "candidates", len(candidates), "selected", selected, "deferred", skipped, "pending_projection_barriers", pendingBarriers)
	return pendingBarriers, nil
}

func moduleIDSet(moduleIDs []string) map[string]struct{} {
	allowed := make(map[string]struct{}, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		allowed[moduleID] = struct{}{}
	}
	return allowed
}

func sitePublicationProducer(acceptedCommit, producerRun string) (projection.WorkflowProvenance, error) {
	return workflowProducer(acceptedCommit, producerRun, "manual-site-publish")
}

func workflowProducer(acceptedCommit, producerRun, workflowID string) (projection.WorkflowProvenance, error) {
	if identity.ValidateSHA1(acceptedCommit) != nil || !validPublicationProducerRun(producerRun) {
		return projection.WorkflowProvenance{}, fmt.Errorf("exact accepted commit and workflow run are required")
	}
	runIDText, attemptText, _ := strings.Cut(producerRun, "-")
	runID, runErr := strconv.ParseUint(runIDText, 10, 64)
	attempt, attemptErr := strconv.ParseUint(attemptText, 10, 64)
	if runErr != nil || attemptErr != nil || runID == 0 || attempt == 0 {
		return projection.WorkflowProvenance{}, fmt.Errorf("workflow run provenance is invalid")
	}
	producer := projection.WorkflowProvenance{WorkflowID: workflowID, Commit: acceptedCommit, RunID: runIDText, RunAttempt: attempt}
	if err := producer.Validate(); err != nil {
		return projection.WorkflowProvenance{}, err
	}
	return producer, nil
}

func authorizeIndexedSitePublication(branch, checkoutRoot, manifestPath, dataRoot, siteRoot, approvedBy string, authorizedAt time.Time) error {
	manifest, err := readIndexedPublicationManifest(manifestPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(approvedBy) == "" || authorizedAt.IsZero() {
		return fmt.Errorf("publication authorizer and authorization time are required")
	}
	if _, err = publish.VerifyIndexedPublication(manifestPath, dataRoot, siteRoot, manifest.ProducerRun, manifest.DeploymentGeneration); err != nil {
		return fmt.Errorf("verify prepared publication against accepted data and rendered site: %w", err)
	}
	if manifest.AcceptedCommit == "" {
		return fmt.Errorf("publication manifest has no accepted commit")
	}
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return err
	}
	result, err := state.AuthorizeDeployment(context.Background(), setup.coordinator, state.DeploymentApproval{
		Generation: manifest.DeploymentGeneration, CurrentTarget: manifest.AcceptedCommit,
		CandidateIsAncestor: true, ExpectedAcceptedCommit: manifest.AcceptedCommit,
		ExpectedDatasetID: manifest.DatasetID, ExpectedDataDigest: manifest.DataDigest,
		ExpectedModuleDataDigest: manifest.ModuleDataDigest, ExpectedProviderDataDigest: manifest.ProviderDataDigest,
		ExpectedSiteDigest: manifest.SiteDigest, ApprovedBy: approvedBy, AuthorizedAt: authorizedAt.UTC(),
	})
	if err != nil {
		return fmt.Errorf("authorize combined site publication generation: %w", err)
	}
	slog.Info("combined site publication authorized", "accepted_commit", manifest.AcceptedCommit,
		"deployment_generation", result.Deployment.Generation, "state_head", result.Head)
	return nil
}

func importIndexedSiteDeployment(branch, checkoutRoot, manifestPath, dataRoot, siteRoot, pagesDeploymentID string, deployedAt time.Time) error {
	manifest, err := readIndexedPublicationManifest(manifestPath)
	if err != nil {
		return err
	}
	if identity.ValidateSHA1(pagesDeploymentID) != nil || pagesDeploymentID != manifest.AcceptedCommit || deployedAt.IsZero() {
		return fmt.Errorf("Pages deployment key must be the exact accepted commit and deployment time is required")
	}
	if _, err = publish.VerifyIndexedPublication(manifestPath, dataRoot, siteRoot, manifest.ProducerRun, manifest.DeploymentGeneration); err != nil {
		return fmt.Errorf("verify deployed publication against accepted data and rendered site: %w", err)
	}
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return err
	}
	receipt := state.DeploymentReceipt{
		RunID: manifest.ProducerRun, PagesDeploymentID: pagesDeploymentID,
		Generation: manifest.DeploymentGeneration, AcceptedCommit: manifest.AcceptedCommit,
		DatasetID: manifest.DatasetID, DataDigest: manifest.DataDigest,
		ModuleDataDigest: manifest.ModuleDataDigest, ProviderDataDigest: manifest.ProviderDataDigest,
		SiteDigest: manifest.SiteDigest, Generator: manifest.Generator,
		ModuleIDs:  append([]string(nil), manifest.ModuleIDs...),
		Membership: append([]index.ModuleMembership(nil), manifest.ModuleMembership...),
		Succeeded:  true, DeployedAt: deployedAt.UTC(),
	}
	imported, err := state.ImportDeploymentReceipt(context.Background(), setup.coordinator, receipt)
	if err != nil {
		return fmt.Errorf("import exact Pages deployment receipt: %w", err)
	}
	ordered := deploymentFanoutTargets(imported.Receipt.Membership)
	acknowledged := 0
	deferred := 0
	for _, target := range ordered {
		if _, err = state.AcknowledgeModuleDeployment(context.Background(), setup.coordinator, target.moduleID, target.release); err != nil {
			if errors.Is(err, state.ErrDeploymentNotCurrent) {
				deferred++
				slog.Warn("module deployment acknowledgement deferred", "module_id", target.moduleID, "avm_release_version", target.release, "reason", "membership is not current for every accepted context")
				continue
			}
			return fmt.Errorf("fan out deployment acknowledgement for %s %s: %w", target.moduleID, target.release, err)
		}
		acknowledged++
	}
	slog.Info("combined Pages deployment imported", "accepted_commit", manifest.AcceptedCommit,
		"pages_deployment_id", pagesDeploymentID, "deployment_generation", manifest.DeploymentGeneration,
		"module_versions_acknowledged", acknowledged, "module_versions_deferred", deferred, "state_head", imported.Head)
	return nil
}

func deploymentFanoutTargets(membership []index.ModuleMembership) []moduleReleaseTarget {
	unique := make(map[moduleReleaseTarget]struct{}, len(membership))
	for _, member := range membership {
		unique[moduleReleaseTarget{moduleID: member.ModuleID, release: member.AVMReleaseVersion}] = struct{}{}
	}
	ordered := make([]moduleReleaseTarget, 0, len(unique))
	for target := range unique {
		ordered = append(ordered, target)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].moduleID != ordered[right].moduleID {
			return ordered[left].moduleID < ordered[right].moduleID
		}
		return ordered[left].release < ordered[right].release
	})
	return ordered
}

func readIndexedPublicationManifest(filename string) (publish.Manifest, error) {
	encoded, err := os.ReadFile(filename)
	if err != nil {
		return publish.Manifest{}, fmt.Errorf("read combined site publication manifest: %w", err)
	}
	var manifest publish.Manifest
	if err = decodeOne(encoded, &manifest); err != nil {
		return publish.Manifest{}, fmt.Errorf("decode combined site publication manifest: %w", err)
	}
	if err = manifest.Validate(); err != nil {
		return publish.Manifest{}, fmt.Errorf("validate combined site publication manifest: %w", err)
	}
	return manifest, nil
}
