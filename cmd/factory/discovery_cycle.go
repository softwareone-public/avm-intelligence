package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/github"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
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
	paced                                                                bool
	continuationID                                                       string
	pacingState                                                          string
	candidatePath, branch, contract, capabilities, forcedIDs, resultPath string
	layoutConfig, checkoutRoot, checkoutTree                             string
	scope                                                                *manualModuleScope
	newOnly, cycle                                                       bool
	identity                                                             discoveryCycleIdentity
}

func runDiscoveryPersistence(command discoveryPersistenceCommand) error {
	if command.cycle {
		return persistManualDiscoveryCycleWithStorage(command)
	}
	return persistSelectedAVMCatalog(command.candidatePath, command.branch, command.contract, command.capabilities, command.forcedIDs, command.resultPath, command.scope, command.newOnly)
}

func persistManualDiscoveryCycle(candidatePath, branch, contract, capabilities, resultPath string, scope *manualModuleScope, identity discoveryCycleIdentity) error {
	return persistManualDiscoveryCycleWithStorage(discoveryPersistenceCommand{candidatePath: candidatePath, branch: branch, contract: contract, capabilities: capabilities, resultPath: resultPath, scope: scope, identity: identity})
}

func persistManualDiscoveryCycleWithStorage(command discoveryPersistenceCommand) (returnErr error) {
	candidatePath, branch, contract, capabilities, resultPath, scope := command.candidatePath, command.branch, command.contract, command.capabilities, command.resultPath, command.scope
	identity := command.identity
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
	var input discoveryPersistenceInput
	var err error
	if command.continuationID != "" {
		backend, backendErr := newFactoryStateBackend(branch)
		if backendErr != nil {
			return backendErr
		}
		input.backend = backend
		input.options = discovery.Options{ExtractorContract: contract, Capabilities: strings.Split(capabilities, ",")}
	} else {
		input, err = prepareDiscoveryPersistence(candidatePath, branch, contract, capabilities, "", scope)
	}
	if err != nil {
		return err
	}
	input.backend.ReadCache = &state.BlobReadCache{}
	var pacer *ratelimit.Pacer
	if command.paced {
		pacer = ratelimit.New()
		if command.pacingState != "" {
			if err := pacer.LoadSession(command.pacingState, identity.runID); err != nil {
				return err
			}
			defer func() { returnErr = errors.Join(returnErr, pacer.SaveSession(command.pacingState, identity.runID)) }()
		}
		input.backend.Pacer = pacer
		pacer.SetRefresh(input.backend.RefreshRateLimits)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 27*time.Minute)
	defer cancel()
	stopBefore := time.Now().Add(24 * time.Minute)
	projector := &moduleCycleProjector{backend: input.backend, client: github.Client{MetadataCache: &github.ProjectionMetadataCache{}, Pacer: pacer}, runURL: identity.runURL, runID: identity.runID}
	result, err := runDiscoverySegment(ctx, stopBefore, input, command, projector)
	if err != nil {
		if result.ContinuationID != "" {
			if resultPath != "" {
				if writeErr := writeJSON(resultPath, result); writeErr != nil {
					return errors.Join(err, writeErr)
				}
			}
			slog.Warn("manual discovery stopped", append(discoveryProgressAttributes(result), "continuation_durable", result.ContinuationDurable)...)
		}
		return err
	}
	if result.Deferred {
		slog.Warn("manual discovery continuation deferred", discoveryProgressAttributes(result)...)
	}
	return reportDiscoveryPersistence(result, resultPath)
}

// runDiscoverySegment executes one bounded paced (or legacy) discovery pass.
// A pacing deferral that escapes outside the module loop — storage
// preparation, the initial exact-cursor read, or claim cleanup — is only
// fatal when the durable record cannot prove a resumable wait; the deferred
// proof converts saved deferrals and completions into an ordinary wait.
func runDiscoverySegment(ctx context.Context, stopBefore time.Time, input discoveryPersistenceInput, command discoveryPersistenceCommand, projector *moduleCycleProjector) (result discovery.Result, returnErr error) {
	defer func() {
		if !command.paced || command.continuationID == "" || !errors.Is(returnErr, ratelimit.ErrDeferred) {
			return
		}
		proven, ok, proveErr := discovery.ProveDurableDeferral(ctx, input.backend, command.continuationID, deferredRetryAt(returnErr))
		if ok && proveErr == nil {
			result, returnErr = proven, nil
		}
	}()
	if err := prepareDiscoveryStorage(ctx, input.backend, command); err != nil {
		return result, err
	}
	if command.paced {
		result, returnErr = discovery.ContinueCycle(ctx, source.Resolver{Token: os.Getenv("GITHUB_TOKEN")}, input.backend, input.candidate, input.options, projector.deliver, command.identity.runID, command.continuationID, stopBefore, projector.skipAcknowledged)
		return result, returnErr
	}
	result, returnErr = discovery.Cycle(context.Background(), source.Resolver{Token: os.Getenv("GITHUB_TOKEN")}, input.backend, input.candidate, input.options, projector.deliver)
	return result, returnErr
}

// deferredRetryAt returns the farthest typed pacing deadline anywhere inside
// an error tree, so a proven durable wait never resumes before the pacer's
// next feasible time.
func deferredRetryAt(err error) time.Time {
	var walk func(error) time.Time
	walk = func(candidate error) time.Time {
		retryAt := time.Time{}
		var deferred *ratelimit.DeferredError
		if errors.As(candidate, &deferred) {
			retryAt = deferred.RetryAt
		}
		switch joined := candidate.(type) {
		case interface{ Unwrap() []error }:
			for _, part := range joined.Unwrap() {
				if deeper := walk(part); deeper.After(retryAt) {
					retryAt = deeper
				}
			}
		case interface{ Unwrap() error }:
			if deeper := walk(joined.Unwrap()); deeper.After(retryAt) {
				retryAt = deeper
			}
		}
		return retryAt
	}
	return walk(err).UTC()
}

// prepareDiscoveryStorage validates local inputs before the exact-head App-only layout transition.
func prepareDiscoveryStorage(ctx context.Context, backend state.GitHubBackend, command discoveryPersistenceCommand) error {
	if command.layoutConfig != "" {
		if _, err := state.LoadLayoutContract(command.layoutConfig); err != nil {
			return err
		}
	}
	if (command.checkoutRoot == "") != (command.checkoutTree == "") {
		return fmt.Errorf("state checkout root and tree listing must be supplied together")
	}
	if command.checkoutRoot != "" {
		listing, err := os.ReadFile(command.checkoutTree)
		if err != nil {
			return fmt.Errorf("read state checkout tree listing: %w", err)
		}
		if err := backend.ReadCache.SeedCheckout(command.checkoutRoot, listing); err != nil {
			return err
		}
	}
	if command.layoutConfig != "" {
		_, err := state.MigrateModuleLayout(ctx, backend)
		return err
	}
	return nil
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
		if err := projector.validateDeliveryOwnership(ctx, existing); err != nil {
			return head, err
		}
	}
	bound, err := cycleDeliveryMatchesDiscovery(snapshot, module, existing, exists)
	if err != nil {
		return head, err
	}
	if canSkipAcknowledgedCycle(module, existing, bound) {
		progress.Logger(ctx).InfoContext(ctx, "module delivery skipped", "reason", "unchanged discovery already acknowledged", "state_head", head)
		return head, nil
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
	evidenceHead, err := queueDiscoveryRunEvidence(ctx, projector.backend, head, []string{module.ModuleID}, projectionEvidenceRun{ID: projector.runID, URL: projector.runURL, Phase: github.EvidencePhaseDiscovery, Now: time.Now().UTC()})
	if err != nil {
		return head, err
	}
	head = evidenceHead
	if head == module.Head && !module.ProjectionRequired && bound && existing.DeliveryState == github.ProjectionProjected {
		progress.Logger(ctx).InfoContext(ctx, "module delivery skipped", "reason", "this run evidence is already durably acknowledged", "state_head", head)
		return head, nil
	}
	return projector.deliverClaimedModule(ctx, module.ModuleID, head)
}

// skipAcknowledged reuses the release-check snapshot without remote reads or mutations.
func (projector *moduleCycleProjector) skipAcknowledged(ctx context.Context, module discovery.ModuleCycle) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	existing, exists, err := readProjectDeliveryIntent(module.Snapshot, module.ModuleID)
	if err != nil || !exists {
		return false, err
	}
	if err := projector.validateDeliveryOwnership(ctx, existing); err != nil {
		return false, err
	}
	bound, err := cycleDeliveryMatchesDiscovery(module.Snapshot, module, existing, exists)
	if err != nil {
		return false, err
	}
	return canSkipAcknowledgedCycle(module, existing, bound), nil
}

func canSkipAcknowledgedCycle(module discovery.ModuleCycle, existing github.ProjectionIntentRecord, bound bool) bool {
	return !module.ProjectionRequired && bound && existing.DeliveryState == github.ProjectionProjected && existing.LastAcknowledgedFingerprint == existing.Fingerprint && len(existing.PendingEvidence) == 0
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
func (projector *moduleCycleProjector) validateDeliveryOwnership(ctx context.Context, intent github.ProjectionIntentRecord) error {
	now := time.Now().UTC()
	if intent.DeliveryState == github.ProjectionRetryable && intent.RetryRequest != nil && projector.client.Pacer != nil && isLocalRetryReason(intent.RetryReason) && !now.Before(intent.RetryAt) {
		if err := projector.prepareClient(ctx); err != nil {
			return err
		}
		if err := projector.client.Pacer.Recheck(ctx, *intent.RetryRequest); err != nil {
			return err
		}
	}
	return validateCycleDeliveryOwnership(intent, now)
}

func isLocalRetryReason(reason string) bool {
	switch reason {
	case "primary_reserve", "local_hourly_mutations", "local_minute_mutations", "local_minute_points", "mutation_spacing":
		return true
	default:
		return false
	}
}

func validateCycleDeliveryOwnership(intent github.ProjectionIntentRecord, now time.Time) error {
	if intent.DeliveryState == github.ProjectionRetryable && now.Before(intent.RetryAt) {
		return &ratelimit.DeferredError{Resource: "projection_retry", RetryAt: intent.RetryAt, Reason: intent.RetryReason}
	}
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
	if projector.client.Budget == nil || projector.client.Pacer != nil {
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
