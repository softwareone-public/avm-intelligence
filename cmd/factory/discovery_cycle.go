package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
	"github.com/softwareone-public/avm-intelligence/internal/source"
)

type discoveryCycleIdentity struct {
	runID string
}

type discoveryPersistenceCommand struct {
	paced                                                                bool
	continuationID                                                       string
	pacingState                                                          string
	candidatePath, branch, contract, capabilities, forcedIDs, resultPath string
	stateCheckoutRoot                                                    string
	scope                                                                *manualModuleScope
	newOnly, cycle                                                       bool
	identity                                                             discoveryCycleIdentity
}

func runDiscoveryPersistence(command discoveryPersistenceCommand) error {
	if command.cycle {
		return persistManualDiscoveryCycleWithStorage(command)
	}
	return persistSelectedAVMCatalog(command.candidatePath, command.branch, command.stateCheckoutRoot, command.contract, command.capabilities, command.forcedIDs, command.resultPath, command.scope, command.newOnly)
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
	if strings.TrimSpace(identity.runID) == "" {
		return fmt.Errorf("manual module cycle requires a run ID")
	}
	var input discoveryPersistenceInput
	var err error
	if command.continuationID != "" {
		setup, setupErr := newFactoryStateSetup(branch, command.stateCheckoutRoot)
		if setupErr != nil {
			return setupErr
		}
		input.backend = setup.backend
		input.coordinator = setup.coordinator
		input.options = discovery.Options{ExtractorContract: contract, Capabilities: strings.Split(capabilities, ",")}
	} else {
		input, err = prepareDiscoveryPersistence(candidatePath, branch, command.stateCheckoutRoot, contract, capabilities, "", scope)
	}
	if err != nil {
		return err
	}
	input.options.Producer = discoveryWorkflowProvenance(identity.runID)
	var pacer *ratelimit.Pacer
	if command.paced {
		pacer = ratelimit.New()
		if command.pacingState != "" {
			if err := pacer.LoadSession(command.pacingState, identity.runID); err != nil {
				return err
			}
			defer func() { returnErr = errors.Join(returnErr, pacer.SaveSession(command.pacingState, identity.runID)) }()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 27*time.Minute)
	defer cancel()
	stopBefore := time.Now().Add(24 * time.Minute)
	result, err := runDiscoverySegment(ctx, stopBefore, input, command)
	if err != nil {
		if result.ContinuationID != "" {
			result = stateOnlyDiscoveryResult(result)
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
	result = stateOnlyDiscoveryResult(result)
	return reportDiscoveryPersistence(result, resultPath)
}

func stateOnlyDiscoveryResult(result discovery.Result) discovery.Result {
	result.DeliveredModuleIDs = nil
	return result
}

func discoveryWorkflowProvenance(runID string) projection.WorkflowProvenance {
	workflowID := os.Getenv("GITHUB_WORKFLOW")
	if workflowID == "" {
		workflowID = "manual-discovery"
	}
	runAttempt, _ := strconv.ParseUint(os.Getenv("GITHUB_RUN_ATTEMPT"), 10, 64)
	if runAttempt == 0 {
		runAttempt = 1
	}
	return projection.WorkflowProvenance{
		WorkflowID: workflowID,
		Commit:     os.Getenv("GITHUB_SHA"),
		RunID:      runID,
		RunAttempt: runAttempt,
	}
}

// runDiscoverySegment executes one bounded paced (or legacy) discovery pass.
// A pacing deferral that escapes outside the module loop — storage
// preparation, the initial exact-cursor read, or claim cleanup — is only
// fatal when the durable record cannot prove a resumable wait; the deferred
// proof converts saved deferrals and completions into an ordinary wait.
func runDiscoverySegment(ctx context.Context, stopBefore time.Time, input discoveryPersistenceInput, command discoveryPersistenceCommand) (result discovery.Result, returnErr error) {
	defer func() {
		if !command.paced || command.continuationID == "" || !errors.Is(returnErr, ratelimit.ErrDeferred) {
			return
		}
		proven, ok, proveErr := discovery.ProveDurableDeferral(ctx, input.backend, command.continuationID, deferredRetryAt(returnErr))
		if ok && proveErr == nil {
			result, returnErr = proven, nil
		}
	}()
	if command.paced {
		result, returnErr = discovery.ContinueCycle(ctx, source.Resolver{Token: os.Getenv("GITHUB_TOKEN")}, input.backend, input.coordinator, input.candidate, input.options, completeStateOnlyCycleModule, command.identity.runID, command.continuationID, stopBefore)
		return result, returnErr
	}
	result, returnErr = discovery.Cycle(context.Background(), source.Resolver{Token: os.Getenv("GITHUB_TOKEN")}, input.coordinator, input.candidate, input.options, completeStateOnlyCycleModule)
	return result, returnErr
}

// completeStateOnlyCycleModule adapts the legacy cycle API to the state-only
// CLI boundary. WP-10/WP-12 will remove the callback and cursor contract.
func completeStateOnlyCycleModule(ctx context.Context, module discovery.ModuleCycle) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if module.Head == "" {
		return "", fmt.Errorf("state-only discovery completion requires a committed head")
	}
	return module.Head, nil
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
