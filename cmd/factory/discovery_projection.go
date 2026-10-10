package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/github"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

const (
	discoveryProjectionLeaseDuration      = 15 * time.Minute
	discoveryProjectionRetryDelay         = 2 * time.Minute
	discoveryProjectionRESTReserve        = 500
	discoveryProjectionGraphQLReserve     = 100
	discoveryProjectionSearchReserve      = 20
	discoveryProjectionSecondaryAllowance = 100
	discoveryProjectionSecondaryReserve   = 20
)

// requestModuleProjectionFlush leaves exact due intents for the scheduled
// projector without discovery, generation refresh, force replay or dispatch.
func requestModuleProjectionFlush(branch, checkoutRoot, moduleList, runURL, runID string) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	if err := validateDiscoveryProjectionIdentity(runURL, runID); err != nil {
		return err
	}
	moduleIDs, err := exactFlushModuleIDs(moduleList)
	if err != nil {
		return err
	}
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return err
	}
	backend := setup.backend
	head, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		return err
	}
	accepted, err := acceptedDiscoveryCatalog(snapshot)
	if err != nil {
		return err
	}
	if _, err := selectManualModules(accepted.Modules, manualModuleScope{names: strings.Join(moduleIDs, ",")}); err != nil {
		return err
	}
	now := time.Now().UTC()
	due, err := dueProjectionModules(snapshot, moduleIDs, now)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		slog.Info("projection flush request skipped", "reason", "no due existing delivery", "selected", len(moduleIDs), "state_head", head)
		return nil
	}
	slog.Info("projection flush target validated", "due", len(due), "selected", len(moduleIDs), "state_head", head, "run_id", runID, "run_url", runURL, "scheduled_consumer", "factory-projection")
	return nil
}

// projectDueDiscoveryIntents is the single scheduled owner for projection
// effects. It reads the fresh state snapshot and finds due work before minting
// any Issue/Project credential.
func projectDueDiscoveryIntents(branch, checkoutRoot, runURL, runID string) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	if err := validateDiscoveryProjectionIdentity(runURL, runID); err != nil {
		return err
	}
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return err
	}
	backend, coordinator := setup.backend, setup.coordinator
	head, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		return fmt.Errorf("read fresh projection state before credential mint: %w", err)
	}
	now := time.Now().UTC()
	moduleIDs, err := github.DueProjectionModuleIDs(snapshot, now)
	if err != nil {
		return fmt.Errorf("derive due projection modules from fresh state: %w", err)
	}
	if len(moduleIDs) == 0 {
		slog.Info("scheduled projection skipped", "reason", "no due intents", "state_head", head)
		return nil
	}
	slog.Info("scheduled projection targets selected", "modules", len(moduleIDs), "state_head", head)
	appID, privateKey := appCredentials()
	token, err := github.MintToken(context.Background(), appID, privateKey)
	if err != nil {
		return fmt.Errorf("mint reduced projection token after due-state check: %w", err)
	}
	return projectDiscoveryProjectionBatches(context.Background(), backend, coordinator,
		github.Client{Token: token, EvidenceAppID: appID}, head, moduleIDs,
		projectionEvidenceRun{ID: runID, URL: runURL, Phase: github.EvidencePhaseProjectionRecovery, Now: now})
}

func exactFlushModuleIDs(moduleList string) ([]string, error) {
	seen := make(map[string]bool)
	ids := make([]string, 0)
	for _, value := range strings.Split(moduleList, ",") {
		moduleID := strings.TrimSpace(value)
		if _, err := identity.OperationalModuleRoot(moduleID); err != nil || seen[moduleID] {
			return nil, fmt.Errorf("projection flush requires unique canonical module IDs")
		}
		seen[moduleID] = true
		ids = append(ids, moduleID)
	}
	sort.Strings(ids)
	return ids, nil
}

func dueProjectionModules(snapshot state.Snapshot, moduleIDs []string, now time.Time) ([]string, error) {
	due := make([]string, 0, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		intent, exists, err := readProjectDeliveryIntent(snapshot, moduleID)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		if err := github.ValidateProjectionAuthority(snapshot, intent); err != nil {
			return nil, err
		}
		if intent.DeliveryState == github.ProjectionPending || intent.DeliveryState == github.ProjectionRetryable && !now.Before(intent.RetryAt) || intent.DeliveryState == github.ProjectionProjecting && !now.Before(intent.LeaseExpiresAt) {
			due = append(due, moduleID)
		}
	}
	return due, nil
}

// requestManualProjectionRecovery reuses durable desired delivery rather than
// replacing it with a recovery run's lifecycle interpretation.
func requestManualProjectionRecovery(branch, checkoutRoot, runURL, runID string, scope manualModuleScope) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	if err := validateDiscoveryProjectionIdentity(runURL, runID); err != nil {
		return err
	}
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return err
	}
	backend, coordinator := setup.backend, setup.coordinator
	head, snapshot, accepted, err := readDiscoveryProjectionState(context.Background(), backend)
	if err != nil {
		return err
	}
	moduleIDs, err := selectManualModules(accepted.Modules, scope)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	intents, err := manualDiscoveryProjectionIntents(snapshot, accepted, moduleIDs, runURL, now)
	if err != nil {
		return err
	}
	head, err = prepareManualDiscoveryDelivery(context.Background(), coordinator, head, intents, moduleIDs, scope.force, now)
	if err != nil {
		return err
	}
	slog.Info("projection recovery request queued", "modules", len(moduleIDs), "state_head", head, "scheduled_consumer", "factory-projection")
	return nil
}

// prepareManualDiscoveryDelivery retains the expected head when all delivery
// records already exist, then optionally reopens acknowledged delivery.
func prepareManualDiscoveryDelivery(ctx context.Context, coordinator state.TransactionCoordinator, head string, intents []github.ProjectionIntentRecord, moduleIDs []string, force bool, now time.Time) (string, error) {
	if len(intents) > 0 {
		saved, err := github.SaveProjectionIntents(ctx, coordinator, intents)
		if err != nil {
			return "", err
		}
		head = saved.Head
	}
	if force {
		return github.ReplayAcknowledgedProjections(ctx, coordinator, moduleIDs, now)
	}
	return head, nil
}

// manualDiscoveryProjectionIntents initializes missing delivery only; existing
// lifecycle fingerprints, lease ownership, retries and acknowledgements remain authoritative.
func manualDiscoveryProjectionIntents(snapshot state.Snapshot, accepted catalog.AcceptedState, moduleIDs []string, runURL string, now time.Time) ([]github.ProjectionIntentRecord, error) {
	missing := make([]string, 0)
	for _, moduleID := range moduleIDs {
		_, exists, err := readProjectDeliveryIntent(snapshot, moduleID)
		if err != nil {
			return nil, err
		}
		if !exists {
			missing = append(missing, moduleID)
		}
	}
	return buildDiscoveryProjectionIntents(snapshot, accepted, missing, runURL, now)
}

func validateDiscoveryProjectionIdentity(runURL, runID string) error {
	if strings.TrimSpace(runURL) == "" || strings.TrimSpace(runID) == "" {
		return fmt.Errorf("discovery run URL and run ID are required for Issue projection")
	}
	return nil
}

func readDiscoveryProjectionState(ctx context.Context, backend state.Backend) (string, state.Snapshot, catalog.AcceptedState, error) {
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return "", state.Snapshot{}, catalog.AcceptedState{}, fmt.Errorf("read accepted discovery state for projection: %w", err)
	}
	accepted, err := acceptedDiscoveryCatalog(snapshot)
	if err != nil {
		return "", state.Snapshot{}, catalog.AcceptedState{}, err
	}
	return head, snapshot, accepted, nil
}

func buildDiscoveryProjectionIntents(snapshot state.Snapshot, accepted catalog.AcceptedState, moduleIDs []string, runURL string, now time.Time) ([]github.ProjectionIntentRecord, error) {
	modules := make(map[string]catalog.ModuleRecord, len(accepted.Modules))
	for _, record := range accepted.Modules {
		modules[record.Module.ID] = record
	}
	intents := make([]github.ProjectionIntentRecord, 0, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		intent, err := buildDiscoveryProjectionIntent(snapshot, modules, moduleID, runURL, now)
		if err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	return intents, nil
}

func buildDiscoveryProjectionIntent(snapshot state.Snapshot, modules map[string]catalog.ModuleRecord, moduleID, runURL string, now time.Time) (github.ProjectionIntentRecord, error) {
	moduleRecord, exists := modules[moduleID]
	if !exists {
		return github.ProjectionIntentRecord{}, fmt.Errorf("discovered module %s is missing from the accepted catalog", moduleID)
	}
	if !moduleRecord.Active {
		return github.ProjectionIntentRecord{}, fmt.Errorf("discovered module %s is inactive in the accepted catalog", moduleID)
	}
	discoveryIntent, intentExists, err := readDiscoveryProjectionIntent(snapshot, moduleID)
	if err != nil {
		return github.ProjectionIntentRecord{}, err
	}
	if !intentExists {
		return github.ProjectionIntentRecord{}, fmt.Errorf("discovery projection intent is missing for %s", moduleID)
	}
	lifecycle, current, err := queue.ReadCurrentLifecycleIntent(snapshot, moduleID)
	if err != nil {
		return github.ProjectionIntentRecord{}, err
	}
	if current && discoveryIntent.Status == discovery.IntentBacklog && discoveryIntent.Source != nil && discoveryIntent.Source.Commit == lifecycle.SourceSHA {
		discoveryIntent.Status = lifecycle.Status
		discoveryIntent.Reason = lifecycle.Reason
	}
	projection, priority, err := projectionForDiscoveryIntent(moduleRecord.Module, discoveryIntent, runURL)
	if err != nil {
		return github.ProjectionIntentRecord{}, err
	}
	generation, err := nextProjectionGeneration(snapshot, moduleID, discoveryIntent.DesiredGeneration)
	if err != nil {
		return github.ProjectionIntentRecord{}, err
	}
	intent, err := github.NewProjectionIntent(projection, generation, priority, now)
	if err != nil {
		return github.ProjectionIntentRecord{}, fmt.Errorf("build Backlog projection for %s: %w", moduleID, err)
	}
	intent.DiscoveryFingerprint, err = identity.ID("discovery-intent", 1, snapshot.Records["state/projection/intents/"+moduleID+".json"].Data)
	if err != nil {
		return github.ProjectionIntentRecord{}, err
	}
	if current && discoveryIntent.Status == lifecycle.Status {
		intent.LifecycleFingerprint, err = github.LifecycleProjectionFingerprint(snapshot, moduleID)
		if err != nil {
			return github.ProjectionIntentRecord{}, err
		}
	}
	return intent, nil
}

func nextProjectionGeneration(snapshot state.Snapshot, moduleID string, desiredGeneration uint64) (uint64, error) {
	generation := desiredGeneration
	existing, exists, err := readProjectDeliveryIntent(snapshot, moduleID)
	if err != nil || !exists || generation > existing.Generation {
		return generation, err
	}
	if existing.Generation == ^uint64(0) {
		return 0, fmt.Errorf("projection generation exhausted for %s", moduleID)
	}
	return existing.Generation + 1, nil
}

type projectionEvidenceRun struct {
	ID, URL, Phase string
	Now            time.Time
}

func queueDiscoveryRunEvidence(ctx context.Context, backend state.Backend, coordinator state.TransactionCoordinator, expectedHead string, moduleIDs []string, run projectionEvidenceRun) (string, error) {
	reader, ok := backend.(state.GitReader)
	if !ok {
		return "", fmt.Errorf("run evidence queue requires a verified state reader")
	}
	head, snapshot, err := reader.Read(ctx)
	if err != nil {
		return head, err
	}
	if head != expectedHead {
		return head, state.ErrHeadMoved
	}
	evidences := make([]github.RunEvidence, 0, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		intent, exists, readErr := readProjectDeliveryIntent(snapshot, moduleID)
		if readErr != nil || !exists {
			return head, fmt.Errorf("run evidence target has no valid durable delivery")
		}
		evidences = append(evidences, github.RunEvidence{ModuleID: moduleID, Phase: run.Phase, RunID: run.ID, RunURL: run.URL,
			SourceVersion: intent.Projection.Version, SourceSHA: intent.Projection.SourceSHA, StateHead: head,
			Status: intent.Projection.Status, Outcome: github.EvidenceOutcomePending})
	}
	return github.QueueRunEvidenceBatch(ctx, coordinator, factoryOwner, factoryRepository, evidences, run.Now)
}

func projectDiscoveryProjectionBatches(ctx context.Context, backend state.Backend, coordinator state.TransactionCoordinator, client github.Client, head string, moduleIDs []string, run projectionEvidenceRun) (returnErr error) {
	ctx, finish := progress.Start(ctx, "projection", "run_delivery", slog.String("run_id", run.ID), slog.Int("total", len(moduleIDs)))
	defer func() { finish(returnErr == nil) }()
	var err error
	head, err = queueDiscoveryRunEvidence(ctx, backend, coordinator, head, moduleIDs, run)
	if err != nil {
		return err
	}
	for position, moduleID := range moduleIDs {
		progress.Logger(ctx).InfoContext(ctx, "projection target selected", "module_id", moduleID, "position", position+1)
	}
	completed, unclaimed := 0, 0
	policy := github.ProjectionBudgetPolicy{
		RESTReserve: discoveryProjectionRESTReserve, GraphQLReserve: discoveryProjectionGraphQLReserve,
		SearchReserve:      discoveryProjectionSearchReserve,
		SecondaryAllowance: discoveryProjectionSecondaryAllowance,
		SecondaryReserve:   discoveryProjectionSecondaryReserve,
	}
	for start := 0; start < len(moduleIDs); start += github.MaxProjectionBatchSize {
		end := min(start+github.MaxProjectionBatchSize, len(moduleIDs))
		batchModules := moduleIDs[start:end]
		batchAt := run.Now.Add(time.Duration(start) * time.Second)
		due, dueErr := github.DueProjectionIntents(ctx, coordinator, batchAt, batchModules)
		if dueErr != nil {
			return fmt.Errorf("read due projection intents before API admission: %w", dueErr)
		}
		if due == 0 {
			progress.Logger(ctx).InfoContext(ctx, "projection batch skipped", "reason", "no due delivery", "targets", len(batchModules))
			continue
		}
		if client.Budget == nil {
			budget, budgetErr := client.ReadProjectionBudget(ctx, policy)
			if budgetErr != nil {
				return fmt.Errorf("admit projection batch from GitHub API budget: %w", budgetErr)
			}
			client.Budget = budget
		}
		admitted := client.Budget.AdmitModules(min(due, github.MaxProjectionBatchSize))
		if admitted == 0 {
			progress.Logger(ctx).WarnContext(ctx, "projection batch deferred before claim", "reason", "API budget has no per-resource headroom above reserves", "due", due)
			unclaimed += due
			continue
		}
		claim, claimErr := github.ClaimProjectionBatchForModules(
			ctx, coordinator, github.ProjectionClaimOptions{
				RunID: run.ID, Now: batchAt,
				Lease: discoveryProjectionLeaseDuration, Limit: admitted, Budget: client.Budget.Snapshot(),
			}, batchModules,
		)
		if claimErr != nil {
			return fmt.Errorf("claim discovery projection batch: %w", claimErr)
		}
		head = claim.Head
		unclaimed += len(batchModules) - len(claim.Intents)
		if len(claim.Intents) == 0 {
			progress.Logger(ctx).InfoContext(ctx, "projection batch skipped", "reason", "no due delivery", "targets", len(batchModules))
			continue
		}
		result, projectErr := client.ProjectClaimedBatch(ctx, backend, coordinator, claim, github.ProjectionRunOptions{
			Owner: factoryOwner, Repository: factoryRepository, ProjectNumber: factoryProjectNumber,
			RetryDelay: discoveryProjectionRetryDelay, BudgetPolicy: policy,
			InitialBudget: client.Budget,
			EvidenceRunID: run.ID, EvidencePhase: run.Phase,
		})
		if projectErr != nil {
			return fmt.Errorf("project discovery batch: %w", projectErr)
		}
		head = result.Head
		completed += result.Completed
		if len(result.Errors) > 0 {
			progress.Logger(ctx).WarnContext(ctx, "discovery projection incomplete", "completed", completed, "failed", len(result.Errors), "unclaimed", unclaimed, "total", len(moduleIDs))
			return fmt.Errorf("discovery projection partially completed (%d/%d): %w", result.Completed, len(claim.Intents), &github.ProjectionDeliveryError{Cause: result.Cause, RetryAt: result.RetryAt})
		}
		slog.Info("discovery projection batch completed", "first", start+1, "last", end, "modules", len(moduleIDs))
	}
	progress.Logger(ctx).InfoContext(ctx, "discovery projection completed", "modules", len(moduleIDs), "completed", completed, "unclaimed", unclaimed, "state_head", head)
	return nil
}

func readDiscoveryProjectionIntent(snapshot state.Snapshot, moduleID string) (discovery.ProjectionIntent, bool, error) {
	path := "state/projection/intents/" + moduleID + ".json"
	record, exists := snapshot.Records[path]
	if !exists {
		return discovery.ProjectionIntent{}, false, nil
	}
	if record.Kind != "projection_intent" || record.SchemaVersion != 1 {
		return discovery.ProjectionIntent{}, false, fmt.Errorf("discovery projection intent has invalid record type for %s", moduleID)
	}
	var intent discovery.ProjectionIntent
	if err := json.Unmarshal(record.Data, &intent); err != nil {
		return discovery.ProjectionIntent{}, false, fmt.Errorf("decode discovery projection intent for %s: %w", moduleID, err)
	}
	if intent.ModuleID != moduleID || intent.DesiredGeneration == 0 {
		return discovery.ProjectionIntent{}, false, fmt.Errorf("discovery projection intent identity is invalid for %s", moduleID)
	}
	return intent, true, nil
}

func readProjectDeliveryIntent(snapshot state.Snapshot, moduleID string) (github.ProjectionIntentRecord, bool, error) {
	path := "state/projection/delivery/" + moduleID + ".json"
	record, exists := snapshot.Records[path]
	if !exists {
		return github.ProjectionIntentRecord{}, false, nil
	}
	if record.Kind != "projection_delivery" || record.SchemaVersion != 1 {
		return github.ProjectionIntentRecord{}, false, fmt.Errorf("project delivery intent has invalid record type for %s", moduleID)
	}
	var intent github.ProjectionIntentRecord
	if err := json.Unmarshal(record.Data, &intent); err != nil {
		return github.ProjectionIntentRecord{}, false, fmt.Errorf("decode project delivery intent for %s: %w", moduleID, err)
	}
	if err := intent.Validate(); err != nil {
		return github.ProjectionIntentRecord{}, false, err
	}
	return intent, true, nil
}

func projectionForDiscoveryIntent(module catalog.Module, intent discovery.ProjectionIntent, runURL string) (github.Projection, github.ProjectionPriority, error) {
	status := ""
	priority := github.ProjectionPriorityChanged
	switch intent.Status {
	case discovery.IntentBacklog, queue.ModuleStatusBacklog:
		status = queue.ModuleStatusBacklog
	case discovery.IntentBlocked, queue.ModuleStatusBlocked:
		status = queue.ModuleStatusBlocked
	case discovery.IntentNeedsReview, queue.ModuleStatusNeedsReview:
		status = queue.ModuleStatusNeedsReview
	case queue.ModuleStatusQueued, queue.ModuleStatusAnalyzing:
		status = intent.Status
		priority = github.ProjectionPriorityActive
	case queue.ModuleStatusValidating:
		status = queue.ModuleStatusAnalyzing
		priority = github.ProjectionPriorityActive
	case queue.ModuleStatusPublishing, queue.ModuleStatusFailed:
		status = intent.Status
	case discovery.IntentRetired:
		status = queue.ModuleStatusRetired
		priority = github.ProjectionPriorityCleanup
	default:
		return github.Projection{}, 0, fmt.Errorf("unsupported discovery projection status %q for %s", intent.Status, module.ID)
	}
	moduleStatus := queue.ModuleStatus{ModuleID: module.ID, Status: status}
	if intent.Source != nil {
		moduleStatus.SourceVersion = intent.Source.Version
		moduleStatus.SourceSHA = intent.Source.Commit
	}
	projection, err := github.FromReducedStatus(moduleStatus, github.ReducedProjectionInput{
		ModuleType: string(module.Type), Domain: module.ParentModule, Outcome: github.OutcomeUnassessed, RunURL: runURL,
	})
	if err != nil {
		return github.Projection{}, 0, fmt.Errorf("reduce discovery projection for %s: %w", module.ID, err)
	}
	return projection, priority, nil
}
