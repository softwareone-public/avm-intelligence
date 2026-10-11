package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/github"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func listExpiredProjectionOwners(branch, checkoutRoot string) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return err
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), setup.backend)
	if err != nil {
		return fmt.Errorf("read projection state for owner recovery: %w", err)
	}
	owners, err := github.ExpiredProjectionOwners(snapshot, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("list exact expired projection owners: %w", err)
	}
	return json.NewEncoder(os.Stdout).Encode(owners)
}

func recoverExpiredProjectionOwner(branch, checkoutRoot, ownerJSON, observationJSON string) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	var owner github.ProjectionOwner
	if err := decodeOne([]byte(ownerJSON), &owner); err != nil {
		return fmt.Errorf("decode projection owner: %w", err)
	}
	var observation github.ProjectionOwnerObservation
	if err := decodeOne([]byte(observationJSON), &observation); err != nil {
		return fmt.Errorf("decode Actions attempt observation: %w", err)
	}
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return err
	}
	updated, err := github.RecoverProjectionOwner(context.Background(), setup.coordinator, owner, observation, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("recover exact projection owner: %w", err)
	}
	slog.Warn("expired projection owner parked because remote effect settlement is unknown", "module_id", updated.ModuleID, "fence", updated.Fence, "delivery_state", updated.DeliveryState, "reason", updated.RetryReason)
	return nil
}

func reportProjectionOwnerRecoverySkip(ownerJSON, reason string) error {
	var owner github.ProjectionOwner
	if err := decodeOne([]byte(ownerJSON), &owner); err != nil {
		return fmt.Errorf("decode projection owner for skip report: %w", err)
	}
	switch reason {
	case "attempt_lookup_failed", "attempt_active", "run_lookup_failed", "newer_run_active", "recovery_fenced":
	default:
		return fmt.Errorf("projection owner skip reason is not allowlisted")
	}
	slog.Warn("projection owner recovery skipped", "module_id", owner.ModuleID, "run_id", owner.RunID, "run_attempt", owner.RunAttempt, "reason", reason)
	return nil
}

const (
	discoveryProjectionLeaseDuration      = 55 * time.Minute
	discoveryProjectionCleanupReserve     = 5 * time.Minute
	discoveryProjectionRunTimeout         = 50 * time.Minute
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
func projectDueDiscoveryIntents(branch, checkoutRoot, runURL, runID string, ownerRunID, ownerRunAttempt uint64, maxModules int) error {
	if !validProjectionModuleCeiling(maxModules) {
		return fmt.Errorf("projection module ceiling must be 10, 50, or 100")
	}
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	if err := validateDiscoveryProjectionIdentity(runURL, runID); err != nil {
		return err
	}
	if ownerRunID == 0 || ownerRunAttempt == 0 {
		return fmt.Errorf("exact Actions run ID and attempt are required for scheduled projection")
	}
	if runID != fmt.Sprintf("%d-%d", ownerRunID, ownerRunAttempt) {
		return fmt.Errorf("projection run identity does not match its exact Actions attempt")
	}
	ctx, cancel := context.WithTimeout(context.Background(), discoveryProjectionRunTimeout)
	defer cancel()
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return err
	}
	backend, coordinator := setup.backend, setup.coordinator
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return fmt.Errorf("read fresh projection state before credential mint: %w", err)
	}
	now := time.Now().UTC()
	needsMaterialization, err := neutralProjectionMaterializationNeeded(snapshot)
	if err != nil {
		return fmt.Errorf("inspect neutral module projection intents: %w", err)
	}
	if needsMaterialization {
		slog.Info("neutral module projection materialization started", "state_head", head)
		accepted, catalogErr := acceptedDiscoveryCatalog(snapshot)
		if catalogErr != nil {
			return fmt.Errorf("read accepted catalog before neutral projection materialization: %w", catalogErr)
		}
		head, err = materializeNeutralProjectionDeliveries(ctx, coordinator, head, snapshot, accepted, runURL, now)
		if err != nil {
			return fmt.Errorf("materialize neutral module projection intents: %w", err)
		}
	}
	head, snapshot, err = state.ReadSnapshot(ctx, backend)
	if err != nil {
		return fmt.Errorf("re-read projection state before due-work selection: %w", err)
	}
	moduleIDs, err := github.DueProjectionModuleIDs(snapshot, now)
	if err != nil {
		return fmt.Errorf("derive due projection modules from fresh state: %w", err)
	}
	if len(moduleIDs) == 0 {
		slog.Info("scheduled projection skipped", "reason", "no due intents", "state_head", head)
		return nil
	}
	dueCount := len(moduleIDs)
	if len(moduleIDs) > maxModules {
		moduleIDs = moduleIDs[:maxModules]
	}
	slog.Info("scheduled projection targets selected", "due", dueCount, "modules", len(moduleIDs), "ceiling", maxModules, "state_head", head)
	appID, privateKey := appCredentials()
	token, err := github.MintToken(ctx, appID, privateKey)
	if err != nil {
		return fmt.Errorf("mint reduced projection token after due-state check: %w", err)
	}
	return projectDiscoveryProjectionBatches(ctx, backend, coordinator,
		scheduledProjectionClient(token, appID), head, moduleIDs,
		projectionEvidenceRun{ID: runID, URL: runURL, Phase: github.EvidencePhaseProjectionRecovery, RunID: ownerRunID, RunAttempt: ownerRunAttempt, Now: now})
}

func scheduledProjectionClient(token, appID string) github.Client {
	return github.Client{Token: token, EvidenceAppID: appID, Pacer: ratelimit.New()}
}

func validProjectionModuleCeiling(value int) bool {
	return value == 10 || value == 50 || value == 100
}

func neutralProjectionMaterializationNeeded(snapshot state.Snapshot) (bool, error) {
	for logicalPath, record := range snapshot.Records {
		if record.Kind != "projection_intent" || record.SchemaVersion != projection.SchemaVersion {
			continue
		}
		var header map[string]json.RawMessage
		if err := json.Unmarshal(record.Data, &header); err != nil {
			return false, fmt.Errorf("decode projection intent header: %w", err)
		}
		if _, neutral := header["intent_id"]; !neutral {
			continue
		}
		var desired projection.DesiredIntent
		if err := json.Unmarshal(record.Data, &desired); err != nil || desired.ValidatePath(logicalPath) != nil {
			return false, fmt.Errorf("neutral projection intent is invalid")
		}
		identityTuple, err := desired.Identity()
		if err != nil {
			return false, err
		}
		current, exists, err := readProjectDeliveryIntent(snapshot, desired.ModuleID)
		if err != nil {
			return false, err
		}
		if !exists || current.NeutralProjection == nil || *current.NeutralProjection != identityTuple {
			return true, nil
		}
		if err = github.ValidateProjectionAuthority(snapshot, current); err != nil {
			// The neutral producer identity can remain unchanged while an active
			// request or accepted-context reduction advances. Re-materialize from
			// this snapshot; the save transaction fences it again if state moves.
			return true, nil
		}
	}
	return false, nil
}

// materializeNeutralProjectionDeliveries transfers only changed neutral
// desired intents into projector-owned delivery records. The projector remains
// the sole owner of Issue/Project effects and acknowledgements.
func materializeNeutralProjectionDeliveries(ctx context.Context, coordinator state.TransactionCoordinator, expectedHead string, snapshot state.Snapshot, accepted catalog.AcceptedState, runURL string, now time.Time) (string, error) {
	reader, ok := coordinator.(state.GitReader)
	if !ok {
		return "", fmt.Errorf("neutral projection materialization requires a verified state reader")
	}
	head, _, err := reader.Read(ctx)
	if err != nil {
		return "", err
	}
	if head != expectedHead {
		return "", state.ErrHeadMoved
	}
	modules := make(map[string]catalog.ModuleRecord, len(accepted.Modules))
	for _, module := range accepted.Modules {
		modules[module.Module.ID] = module
	}
	moduleIDs := make([]string, 0)
	for logicalPath, record := range snapshot.Records {
		if record.Kind != "projection_intent" || record.SchemaVersion != projection.SchemaVersion {
			continue
		}
		var header map[string]json.RawMessage
		if err := json.Unmarshal(record.Data, &header); err != nil {
			return "", fmt.Errorf("decode neutral projection intent: %w", err)
		}
		if _, neutral := header["intent_id"]; !neutral {
			continue
		}
		var desired projection.DesiredIntent
		if err := json.Unmarshal(record.Data, &desired); err != nil || desired.ValidatePath(logicalPath) != nil {
			return "", fmt.Errorf("neutral projection intent is invalid")
		}
		identityTuple, err := desired.Identity()
		if err != nil {
			return "", err
		}
		current, exists, err := readProjectDeliveryIntent(snapshot, desired.ModuleID)
		if err != nil {
			return "", err
		}
		if exists && current.NeutralProjection != nil && *current.NeutralProjection == identityTuple {
			// Neutral identity can stay stable while an active release advances.
			// Skip only when the saved delivery still matches lifecycle authority.
			if github.ValidateProjectionAuthority(snapshot, current) == nil {
				continue
			}
		}
		module, exists := modules[desired.ModuleID]
		if !exists || (!module.Active && desired.Status != "retired") {
			return "", fmt.Errorf("neutral projection module is not active in the accepted catalog")
		}
		moduleIDs = append(moduleIDs, desired.ModuleID)
	}
	sort.Strings(moduleIDs)
	if len(moduleIDs) == 0 {
		slog.Info("neutral module projection materialization completed", "modules", 0, "state_head", head)
		return head, nil
	}
	materialized := 0
	failed := 0
	currentHead := head
	for _, moduleID := range moduleIDs {
		intent, err := buildDiscoveryProjectionIntent(snapshot, modules, moduleID, runURL, now)
		if err != nil {
			failed++
			slog.Warn("neutral module projection remains pending", "module_id", moduleID, "reason", "handoff cannot be represented by the scheduled delivery contract")
			continue
		}
		saved, saveErr := github.SaveProjectionIntent(ctx, coordinator, intent)
		if saveErr != nil {
			failed++
			slog.Warn("neutral module projection remains pending", "module_id", moduleID, "reason", "delivery intent could not be committed")
			continue
		}
		currentHead = saved.Head
		if saved.Changed {
			materialized++
		}
	}
	slog.Info("neutral module projection materialization completed", "modules", materialized, "failed", failed, "state_head", currentHead)
	return currentHead, nil
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
	neutralPath, err := identity.StateProjectionIntentPath(moduleID)
	if err != nil {
		return github.ProjectionIntentRecord{}, err
	}
	if record, exists := snapshot.Records[neutralPath]; exists && record.Kind == "projection_intent" {
		var header map[string]json.RawMessage
		if err := json.Unmarshal(record.Data, &header); err != nil {
			return github.ProjectionIntentRecord{}, fmt.Errorf("decode module projection intent: %w", err)
		}
		if _, neutral := header["intent_id"]; neutral {
			var desired projection.DesiredIntent
			if json.Unmarshal(record.Data, &desired) != nil || record.SchemaVersion != projection.SchemaVersion || desired.ValidatePath(neutralPath) != nil {
				return github.ProjectionIntentRecord{}, fmt.Errorf("module neutral projection intent is invalid")
			}
			if !moduleRecord.Active && desired.Status != "retired" {
				return github.ProjectionIntentRecord{}, fmt.Errorf("inactive module %s has a non-retired neutral projection", moduleID)
			}
			return buildNeutralProjectionDelivery(snapshot, moduleRecord.Module, desired, runURL, now)
		}
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

func buildNeutralProjectionDelivery(snapshot state.Snapshot, module catalog.Module, desired projection.DesiredIntent, runURL string, now time.Time) (github.ProjectionIntentRecord, error) {
	if len(desired.EvidenceEffects) != 0 {
		return github.ProjectionIntentRecord{}, fmt.Errorf("neutral projection evidence effects are not supported by the scheduled delivery contract")
	}
	fields := make(map[string]string, len(desired.OwnedFields))
	for _, field := range desired.OwnedFields {
		fields[field.Name] = field.Value
	}
	status := ""
	priority := github.ProjectionPriorityChanged
	switch desired.Status {
	case "backlog":
		status = queue.ModuleStatusBacklog
	case "facts-accepted":
		status = queue.ModuleStatusBacklog
	case "queued":
		status, priority = queue.ModuleStatusQueued, github.ProjectionPriorityActive
	case "analyzing":
		status, priority = queue.ModuleStatusAnalyzing, github.ProjectionPriorityActive
	case "validating":
		status, priority = queue.ModuleStatusValidating, github.ProjectionPriorityActive
	case "needs-review":
		status = queue.ModuleStatusNeedsReview
	case "accepted", "partial":
		status = queue.ModuleStatusPublishing
	case "blocked":
		status = queue.ModuleStatusBlocked
	case "failed":
		status = queue.ModuleStatusFailed
	case "publishing":
		status = queue.ModuleStatusPublishing
	case "done":
		status, priority = queue.ModuleStatusDone, github.ProjectionPriorityCleanup
	case "retired":
		status, priority = queue.ModuleStatusRetired, github.ProjectionPriorityCleanup
	default:
		return github.ProjectionIntentRecord{}, fmt.Errorf("unsupported neutral module projection status %q", desired.Status)
	}
	sourceVersion := fields["source_version"]
	if sourceVersion == "" {
		sourceVersion = fields["avm_release_version"]
	}
	lifecycle := queue.ModuleStatus{
		ModuleID: module.ID, Status: status, SourceVersion: sourceVersion, SourceSHA: fields["source_sha"],
		PublishedCommit: fields["published_commit"],
	}
	activeLifecycle, active, err := queue.ReduceModuleProjectionStatus(snapshot, module.ID, nil)
	if err != nil {
		return github.ProjectionIntentRecord{}, fmt.Errorf("reduce controlling module release for %s: %w", module.ID, err)
	}
	if active {
		// Neutral intents are producer-owned handoffs. The held release, or the
		// oldest queued release when the slot is free, controls module status;
		// a newer release discovered in the same poll cannot jump the backlog.
		lifecycle = activeLifecycle
		status = activeLifecycle.Status
		priority = projectionPriorityForLifecycleStatus(status)
	}
	if desired.Status == "done" && !active {
		acknowledgement, verifyErr := state.VerifyDoneModule(snapshot, module.ID, fields["avm_release_version"], fields["published_commit"])
		if verifyErr != nil || fields["published_commit"] == "" || acknowledgement.AcceptedCommit != fields["published_commit"] {
			return github.ProjectionIntentRecord{}, fmt.Errorf("neutral Done status lacks exact current module deployment proof")
		}
	}
	reduced, err := github.FromReducedStatus(lifecycle, github.ReducedProjectionInput{
		ModuleType: string(module.Type), Domain: module.ParentModule,
		Outcome: github.OutcomeUnassessed, RunURL: runURL,
	})
	if err != nil {
		return github.ProjectionIntentRecord{}, fmt.Errorf("reduce neutral projection for %s: %w", module.ID, err)
	}
	generation, err := nextProjectionGeneration(snapshot, module.ID, desired.Generation)
	if err != nil {
		return github.ProjectionIntentRecord{}, err
	}
	delivery, err := github.NewProjectionIntent(reduced, generation, priority, now)
	if err != nil {
		return github.ProjectionIntentRecord{}, err
	}
	identityTuple, err := desired.Identity()
	if err != nil {
		return github.ProjectionIntentRecord{}, err
	}
	delivery.NeutralProjection = &identityTuple
	return delivery, nil
}

func projectionPriorityForLifecycleStatus(status string) github.ProjectionPriority {
	switch status {
	case queue.ModuleStatusQueued, queue.ModuleStatusAnalyzing, queue.ModuleStatusValidating, queue.ModuleStatusPublishing:
		return github.ProjectionPriorityActive
	case queue.ModuleStatusDone, queue.ModuleStatusRetired:
		return github.ProjectionPriorityCleanup
	default:
		return github.ProjectionPriorityChanged
	}
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
	RunID          uint64
	RunAttempt     uint64
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
	completed, failed, unclaimed := 0, 0, 0
	policy := github.ProjectionBudgetPolicy{
		RESTReserve: discoveryProjectionRESTReserve, GraphQLReserve: discoveryProjectionGraphQLReserve,
		SearchReserve:      discoveryProjectionSearchReserve,
		SecondaryAllowance: discoveryProjectionSecondaryAllowance,
		SecondaryReserve:   discoveryProjectionSecondaryReserve,
	}
	remaining := append([]string(nil), moduleIDs...)
	for len(remaining) > 0 {
		if err := ctx.Err(); err != nil {
			progress.Logger(ctx).WarnContext(ctx, "projection run stopped before next claim", "reason", "bounded run deadline reached", "unclaimed", len(remaining))
			unclaimed += len(remaining)
			break
		}
		batchAt := time.Now().UTC()
		_, snapshot, readErr := state.ReadSnapshot(ctx, backend)
		if readErr != nil {
			return fmt.Errorf("read due projection state before API admission: %w", readErr)
		}
		dueIntents, dueErr := github.DueProjectionIntentRecords(snapshot, batchAt, remaining)
		if dueErr != nil {
			return fmt.Errorf("select due projection intents before API admission: %w", dueErr)
		}
		if len(dueIntents) == 0 {
			progress.Logger(ctx).InfoContext(ctx, "projection run skipped remaining targets", "reason", "no due delivery", "targets", len(remaining))
			break
		}
		if client.Budget == nil {
			budget, budgetErr := client.ReadProjectionBudget(ctx, policy)
			if budgetErr != nil {
				return fmt.Errorf("admit projection module from GitHub API budget: %w", budgetErr)
			}
			client.Budget = budget
		}
		var runDeadline time.Time
		if deadline, bounded := ctx.Deadline(); bounded {
			runDeadline = deadline
		}
		available := projectionAdmissionWindow(batchAt, time.Now(), runDeadline)
		candidate, effectCost, admitted, admissionErr := github.FirstAdmissibleProjectionIntent(dueIntents, client.Budget, available)
		if admissionErr != nil {
			return fmt.Errorf("estimate projection effect cost before claim: %w", admissionErr)
		}
		if !admitted {
			progress.Logger(ctx).WarnContext(ctx, "projection run deferred before claim", "reason", "no due module fits per-effect API reserves and remaining run time", "due", len(dueIntents))
			unclaimed += len(dueIntents)
			break
		}
		progress.Logger(ctx).InfoContext(ctx, "projection module admitted", "module_id", candidate.ModuleID, "rest_requests", effectCost.RESTRequests, "graphql_requests", effectCost.GraphQLRequests, "search_requests", effectCost.SearchRequests, "secondary_requests", effectCost.SecondaryRequests, "estimated_duration", effectCost.EstimatedDuration)
		claim, claimErr := github.ClaimProjectionBatchForModules(
			ctx, coordinator, github.ProjectionClaimOptions{
				RunID: run.ID, OwnerRunID: run.RunID, OwnerRunAttempt: run.RunAttempt, Now: batchAt,
				Lease: discoveryProjectionLeaseDuration, Limit: 1, Budget: client.Budget.Snapshot(),
			}, []string{candidate.ModuleID},
		)
		if claimErr != nil {
			return fmt.Errorf("claim discovery projection module: %w", claimErr)
		}
		head = claim.Head
		if len(claim.Intents) == 0 {
			progress.Logger(ctx).InfoContext(ctx, "projection candidate skipped", "module_id", candidate.ModuleID, "reason", "delivery changed before claim")
			continue
		}
		claimedModule := claim.Intents[0].ModuleID
		updatedRemaining := removeProjectionModule(remaining, claimedModule)
		if len(updatedRemaining) == len(remaining) {
			return fmt.Errorf("projection claim returned an untargeted module %s", claimedModule)
		}
		remaining = updatedRemaining
		moduleCtx, moduleCancel := context.WithDeadline(ctx, claim.Intents[0].LeaseExpiresAt.Add(-discoveryProjectionCleanupReserve))
		moduleCtx = ratelimit.WithCleanup(moduleCtx, context.Background())
		result, projectErr := client.ProjectClaimedBatch(moduleCtx, backend, coordinator, claim, github.ProjectionRunOptions{
			Owner: factoryOwner, Repository: factoryRepository, ProjectNumber: factoryProjectNumber,
			RetryDelay: discoveryProjectionRetryDelay, BudgetPolicy: policy,
			InitialBudget: client.Budget,
			EvidenceRunID: run.ID, EvidencePhase: run.Phase,
		})
		moduleCancel()
		if projectErr != nil {
			return fmt.Errorf("project discovery module %s: %w", claimedModule, projectErr)
		}
		head = result.Head
		completed += result.Completed
		if len(result.Errors) > 0 {
			failed += len(result.Errors)
			progress.Logger(ctx).WarnContext(ctx, "projection module deferred", "module_id", claimedModule, "reason", "remote effects were parked for retry or review", "completed", completed, "failed", failed, "unclaimed", unclaimed+len(remaining), "total", len(moduleIDs))
		}
		if result.Partial {
			unclaimed += len(remaining)
			break
		}
		slog.Info("discovery projection module completed", "module_id", claimedModule, "completed", result.Completed, "failed", len(result.Errors))
	}
	progress.Logger(ctx).InfoContext(ctx, "discovery projection completed", "modules", len(moduleIDs), "completed", completed, "failed", failed, "unclaimed", unclaimed, "state_head", head)
	return nil
}

// projectionAdmissionWindow uses the claim's fixed lease expiry, so time spent
// reading state or budget before claiming is not accidentally granted to the
// remote effect. A run deadline may shorten that window further.
func projectionAdmissionWindow(claimStartedAt, now, runDeadline time.Time) time.Duration {
	available := claimStartedAt.Add(discoveryProjectionLeaseDuration - discoveryProjectionCleanupReserve).Sub(now)
	if !runDeadline.IsZero() {
		if runRemaining := runDeadline.Sub(now); runRemaining < available {
			available = runRemaining
		}
	}
	return available
}

func removeProjectionModule(moduleIDs []string, claimed string) []string {
	remaining := make([]string, 0, len(moduleIDs)-1)
	for _, moduleID := range moduleIDs {
		if moduleID != claimed {
			remaining = append(remaining, moduleID)
		}
	}
	return remaining
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
