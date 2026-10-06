package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/github"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

// moduleCycleProjector retains reduced API budgets and metadata across singleton deliveries.
type moduleCycleProjector struct {
	backend state.Backend
	client  github.Client
	runURL  string
	runID   string
}

type discoveryCycleIdentity struct {
	runURL string
	runID  string
}

type discoveryPersistenceCommand struct {
	candidatePath, branch, contract, capabilities, forcedIDs, resultPath string
	scope                                                                *manualModuleScope
	newOnly, cycle                                                       bool
	identity                                                             discoveryCycleIdentity
}

func runDiscoveryPersistence(command discoveryPersistenceCommand) error {
	if command.cycle {
		return persistManualDiscoveryCycle(command.candidatePath, command.branch, command.contract, command.capabilities, command.resultPath, command.scope, command.identity)
	}
	return persistSelectedAVMCatalog(command.candidatePath, command.branch, command.contract, command.capabilities, command.forcedIDs, command.resultPath, command.scope, command.newOnly)
}

func persistManualDiscoveryCycle(candidatePath, branch, contract, capabilities, resultPath string, scope *manualModuleScope, identity discoveryCycleIdentity) error {
	if scope == nil {
		return fmt.Errorf("manual module cycle requires module selection")
	}
	if err := validateDiscoveryProjectionIdentity(identity.runURL, identity.runID); err != nil {
		return err
	}
	appID, privateKey := appCredentials()
	if appID == "" || privateKey == "" {
		return fmt.Errorf("GitHub App credentials are required")
	}
	input, err := prepareDiscoveryPersistence(candidatePath, branch, contract, capabilities, "", scope)
	if err != nil {
		return err
	}
	input.backend.ReadCache = &state.BlobReadCache{}
	projector := &moduleCycleProjector{backend: input.backend, client: github.Client{MetadataCache: &github.ProjectionMetadataCache{}}, runURL: identity.runURL, runID: identity.runID}
	result, err := discovery.Cycle(context.Background(), source.Resolver{Token: os.Getenv("GITHUB_TOKEN")}, input.backend, input.candidate, input.options, projector.deliver)
	if err != nil {
		return err
	}
	return reportDiscoveryPersistence(result, resultPath)
}

func (projector *moduleCycleProjector) deliver(ctx context.Context, module discovery.ModuleCycle) (newHead string, returnErr error) {
	ctx, finish := progress.Start(ctx, "projection", "module_cycle_delivery", slog.String("module_id", module.ModuleID), slog.String("run_id", projector.runID))
	defer func() { finish(returnErr == nil) }()
	head, snapshot, accepted, err := readDiscoveryProjectionState(ctx, projector.backend)
	if err != nil {
		return module.Head, err
	}
	if head != module.Head {
		return head, state.ErrHeadMoved
	}
	existing, exists, err := readProjectDeliveryIntent(snapshot, module.ModuleID)
	if err != nil {
		return head, err
	}
	if exists {
		if err := validateCycleDeliveryOwnership(existing, time.Now().UTC()); err != nil {
			return head, err
		}
	}
	bound, err := cycleDeliveryMatchesDiscovery(snapshot, module, existing, exists)
	if err != nil {
		return head, err
	}
	if module.ProjectionRequired || !bound {
		intents, buildErr := buildDiscoveryProjectionIntents(snapshot, accepted, []string{module.ModuleID}, projector.runURL, time.Now().UTC())
		if buildErr != nil {
			return head, buildErr
		}
		saved, saveErr := github.SaveProjectionIntents(ctx, projector.backend, head, intents)
		if saveErr != nil {
			return head, saveErr
		}
		head = saved.Head
	}
	head, err = queueDiscoveryRunEvidence(ctx, projector.backend, head, []string{module.ModuleID}, projectionEvidenceRun{ID: projector.runID, URL: projector.runURL, Phase: github.EvidencePhaseDiscovery, Now: time.Now().UTC()})
	if err != nil {
		return head, err
	}
	if head == module.Head && !module.ProjectionRequired && bound && existing.DeliveryState == github.ProjectionProjected {
		progress.Logger(ctx).InfoContext(ctx, "module delivery skipped", "reason", "this run evidence is already durably acknowledged", "state_head", head)
		return head, nil
	}
	return projector.deliverClaimedModule(ctx, module.ModuleID, head)
}

// cycleDeliveryMatchesDiscovery compares durable lifecycle identity, not a transient change flag.
func cycleDeliveryMatchesDiscovery(snapshot state.Snapshot, module discovery.ModuleCycle, existing github.ProjectionIntentRecord, exists bool) (bool, error) {
	current, found, err := readDiscoveryProjectionIntent(snapshot, module.ModuleID)
	if err != nil || !found {
		return false, fmt.Errorf("current discovery lifecycle intent is missing or invalid")
	}
	fingerprint, err := identity.ID("discovery-intent", 1, snapshot.Records["state/projection/intents/"+module.ModuleID+".json"].Data)
	if err != nil {
		return false, err
	}
	if exists && !module.ProjectionRequired && legacyCycleDeliveryNeedsReconciliation(current, existing) {
		return false, fmt.Errorf("legacy advanced delivery lacks a current discovery binding; reconcile explicitly before continuing")
	}
	commit, version := "", ""
	if current.Source != nil {
		commit, version = current.Source.Commit, current.Source.Version
	}
	return exists && existing.DiscoveryFingerprint == fingerprint && existing.Projection.SourceSHA == commit && existing.Projection.Version == version, nil
}

func legacyCycleDeliveryNeedsReconciliation(current discovery.ProjectionIntent, existing github.ProjectionIntentRecord) bool {
	if existing.DiscoveryFingerprint != "" || current.Status != discovery.IntentBacklog || current.Source == nil || existing.Projection.SourceSHA != current.Source.Commit {
		return false
	}
	switch existing.Projection.Status {
	case queue.ModuleStatusBacklog, queue.ModuleStatusBlocked, queue.ModuleStatusNeedsReview:
		return false
	default:
		return true
	}
}

// validateCycleDeliveryOwnership refuses to use discovery as a lease, retry or review bypass.
func validateCycleDeliveryOwnership(intent github.ProjectionIntentRecord, now time.Time) error {
	protected := intent.DeliveryState == github.ProjectionNeedsReview ||
		intent.DeliveryState == github.ProjectionProjecting && now.Before(intent.LeaseExpiresAt) ||
		intent.DeliveryState == github.ProjectionRetryable && now.Before(intent.RetryAt)
	if protected {
		return fmt.Errorf("module delivery remains protected: %s", intent.DeliveryState)
	}
	return nil
}

func (projector *moduleCycleProjector) deliverClaimedModule(ctx context.Context, moduleID, head string) (string, error) {
	if err := projector.prepareClient(ctx); err != nil {
		return head, err
	}
	claim, err := github.ClaimProjectionBatchForModules(ctx, projector.backend, github.ProjectionClaimOptions{
		ExpectedHead: head, RunID: projector.runID, Now: time.Now().UTC(), Lease: discoveryProjectionLeaseDuration, Limit: 1,
	}, []string{moduleID})
	if err != nil {
		return head, err
	}
	if len(claim.Intents) != 1 {
		return claim.Head, fmt.Errorf("module delivery is not due or is protected by active ownership, retry or review")
	}
	result, err := projector.client.ProjectClaimedBatch(ctx, projector.backend, claim, github.ProjectionRunOptions{
		Owner: factoryOwner, Repository: factoryRepository, ProjectNumber: factoryProjectNumber,
		RetryDelay: discoveryProjectionRetryDelay, BudgetPolicy: discoveryCycleBudgetPolicy(),
		EvidenceRunID: projector.runID, EvidencePhase: github.EvidencePhaseDiscovery,
	})
	if err != nil {
		return result.Head, err
	}
	if result.Completed != 1 || len(result.Errors) != 0 || result.Partial {
		return result.Head, &github.ProjectionDeliveryError{Cause: result.Cause, RetryAt: result.RetryAt}
	}
	progress.Logger(ctx).InfoContext(ctx, "module board delivery acknowledged", "state_head", result.Head)
	return result.Head, nil
}

func (projector *moduleCycleProjector) prepareClient(ctx context.Context) (returnErr error) {
	ctx, finish := progress.Start(ctx, "projection", "module_cycle_client_prepare")
	defer func() { finish(returnErr == nil) }()
	if projector.client.Token == "" {
		appID, privateKey := appCredentials()
		token, err := github.MintToken(ctx, appID, privateKey)
		if err != nil {
			return fmt.Errorf("mint bounded Issue/Project token: %w", err)
		}
		projector.client.Token = token
		projector.client.EvidenceAppID = appID
	}
	if projector.client.Budget == nil {
		budget, err := projector.client.ReadProjectionBudget(ctx, discoveryCycleBudgetPolicy())
		if err != nil {
			return err
		}
		projector.client.Budget = budget
	}
	return nil
}

func discoveryCycleBudgetPolicy() github.ProjectionBudgetPolicy {
	return github.ProjectionBudgetPolicy{
		RESTReserve: discoveryProjectionRESTReserve, GraphQLReserve: discoveryProjectionGraphQLReserve,
		SearchReserve: discoveryProjectionSearchReserve, SecondaryAllowance: discoveryProjectionSecondaryAllowance,
		SecondaryReserve: discoveryProjectionSecondaryReserve,
	}
}
