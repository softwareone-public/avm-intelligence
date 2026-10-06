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

func projectDiscoveredModules(branch, moduleList, runURL, runID string) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	if err := validateDiscoveryProjectionIdentity(runURL, runID); err != nil {
		return err
	}
	moduleIDs, err := selectedDiscoveryModuleIDs(moduleList)
	if err != nil {
		return err
	}
	backend, err := newFactoryStateBackend(branch)
	if err != nil {
		return err
	}
	head, snapshot, accepted, err := readDiscoveryProjectionState(context.Background(), backend)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	intents, err := buildDiscoveryProjectionIntents(snapshot, accepted, moduleIDs, runURL, now)
	if err != nil {
		return err
	}
	saved, err := github.SaveProjectionIntents(context.Background(), backend, head, intents)
	if err != nil {
		return fmt.Errorf("save discovery projection intents: %w", err)
	}
	appID, privateKey := appCredentials()
	token, err := github.MintToken(context.Background(), appID, privateKey)
	if err != nil {
		return fmt.Errorf("mint bounded Issue/Project token: %w", err)
	}
	return projectDiscoveryProjectionBatches(context.Background(), backend, github.Client{Token: token}, saved.Head, moduleIDs, runID, now)
}

// projectManualDiscoveryScope reuses durable desired delivery rather than
// replacing it with a recovery run's lifecycle interpretation.
func projectManualDiscoveryScope(branch, runURL, runID string, scope manualModuleScope) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	if err := validateDiscoveryProjectionIdentity(runURL, runID); err != nil {
		return err
	}
	backend, err := newFactoryStateBackend(branch)
	if err != nil {
		return err
	}
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
	head, err = prepareManualDiscoveryDelivery(context.Background(), backend, head, intents, moduleIDs, scope.force, now)
	if err != nil {
		return err
	}
	due, err := manualProjectionDue(snapshot, moduleIDs, scope.force || len(intents) > 0, now)
	if err != nil {
		return err
	}
	if !due {
		slog.Info("manual projection skipped", "reason", "no due delivery", "modules", len(moduleIDs))
		return nil
	}
	appID, privateKey := appCredentials()
	token, err := github.MintToken(context.Background(), appID, privateKey)
	if err != nil {
		return fmt.Errorf("mint bounded Issue/Project token: %w", err)
	}
	return projectDiscoveryProjectionBatches(context.Background(), backend, github.Client{Token: token}, head, moduleIDs, runID, now)
}

// manualProjectionDue avoids token/API work for converged or protected delivery.
func manualProjectionDue(snapshot state.Snapshot, moduleIDs []string, newlyQueued bool, now time.Time) (bool, error) {
	if newlyQueued {
		return true, nil
	}
	for _, moduleID := range moduleIDs {
		intent, exists, readErr := readProjectDeliveryIntent(snapshot, moduleID)
		if readErr != nil {
			return false, readErr
		}
		if exists && (intent.DeliveryState == github.ProjectionPending || intent.DeliveryState == github.ProjectionRetryable && !now.Before(intent.RetryAt) || intent.DeliveryState == github.ProjectionProjecting && !now.Before(intent.LeaseExpiresAt)) {
			return true, nil
		}
	}
	return false, nil
}

// prepareManualDiscoveryDelivery retains the expected head when all delivery
// records already exist, then optionally reopens acknowledged delivery.
func prepareManualDiscoveryDelivery(ctx context.Context, backend state.Backend, head string, intents []github.ProjectionIntentRecord, moduleIDs []string, force bool, now time.Time) (string, error) {
	if len(intents) > 0 {
		saved, err := github.SaveProjectionIntents(ctx, backend, head, intents)
		if err != nil {
			return "", err
		}
		head = saved.Head
	}
	if force {
		return github.ReplayAcknowledgedProjections(ctx, backend, head, moduleIDs, now)
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

func selectedDiscoveryModuleIDs(moduleList string) ([]string, error) {
	selected, err := parseModuleIDs(moduleList)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("at least one discovery module ID is required")
	}
	moduleIDs := make([]string, 0, len(selected))
	for moduleID := range selected {
		moduleIDs = append(moduleIDs, moduleID)
	}
	sort.Strings(moduleIDs)
	return moduleIDs, nil
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

func projectDiscoveryProjectionBatches(ctx context.Context, backend state.Backend, client github.Client, head string, moduleIDs []string, runID string, now time.Time) error {
	policy := github.ProjectionBudgetPolicy{
		RESTReserve: discoveryProjectionRESTReserve, GraphQLReserve: discoveryProjectionGraphQLReserve,
		SearchReserve:      discoveryProjectionSearchReserve,
		SecondaryAllowance: discoveryProjectionSecondaryAllowance,
		SecondaryReserve:   discoveryProjectionSecondaryReserve,
	}
	for start := 0; start < len(moduleIDs); start += github.MaxProjectionBatchSize {
		end := min(start+github.MaxProjectionBatchSize, len(moduleIDs))
		batchModules := moduleIDs[start:end]
		claim, claimErr := github.ClaimProjectionBatchForModules(
			ctx, backend, github.ProjectionClaimOptions{
				ExpectedHead: head, RunID: runID, Now: now.Add(time.Duration(start) * time.Second),
				Lease: discoveryProjectionLeaseDuration, Limit: github.MaxProjectionBatchSize,
			}, batchModules,
		)
		if claimErr != nil {
			return fmt.Errorf("claim discovery projection batch: %w", claimErr)
		}
		head = claim.Head
		if len(claim.Intents) == 0 {
			continue
		}
		result, projectErr := client.ProjectClaimedBatch(ctx, backend, claim, github.ProjectionRunOptions{
			Owner: factoryOwner, Repository: factoryRepository, ProjectNumber: factoryProjectNumber,
			RetryDelay: discoveryProjectionRetryDelay, BudgetPolicy: policy,
		})
		if projectErr != nil {
			return fmt.Errorf("project discovery batch: %w", projectErr)
		}
		head = result.Head
		if len(result.Errors) > 0 {
			return fmt.Errorf("discovery projection partially completed (%d/%d): %s", result.Completed, len(claim.Intents), strings.Join(result.Errors, "; "))
		}
		slog.Info("discovery projection batch completed", "first", start+1, "last", end, "modules", len(moduleIDs))
	}
	slog.Info("discovery projection completed", "modules", len(moduleIDs), "state_head", head)
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
	case discovery.IntentBacklog:
		status = queue.ModuleStatusBacklog
	case discovery.IntentBlocked:
		status = queue.ModuleStatusBlocked
	case discovery.IntentNeedsReview:
		status = queue.ModuleStatusNeedsReview
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
