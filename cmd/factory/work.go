package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
	"github.com/softwareone-public/avm-intelligence/internal/worker"
)

const (
	workerProducerID       = "avm-intelligence-factory"
	workerProducerVersion  = "2.0.0"
	workerProducerContract = 1
)

type workerLaunchInput struct {
	WorkerInput      worker.Input `json:"worker_input"`
	SourceRepository string       `json:"source_repository,omitempty"`
}

type trustedWorkerOptions struct {
	prepare        bool
	seal           bool
	attemptID      string
	requestID      string
	dispatcherID   string
	runID          string
	runAttempt     uint64
	fence          uint64
	stateBranch    string
	dataRoot       string
	inputOutput    string
	artifactPath   string
	validationID   string
	workflowID     string
	producerCommit string
	checkoutRoot   string
}

func handleTrustedWorkerCommand(options trustedWorkerOptions) (bool, error) {
	if !options.prepare && !options.seal {
		if options.requestID != "" || options.validationID != "" || options.artifactPath != "" || options.inputOutput != "" {
			return true, fmt.Errorf("worker preparation/sealing options require their corresponding command")
		}
		return false, nil
	}
	if options.prepare && options.seal {
		return true, fmt.Errorf("--prepare-worker-input and --seal-worker are mutually exclusive")
	}
	backend, err := newFactoryStateBackend(options.stateBranch, options.checkoutRoot)
	if err != nil {
		return true, err
	}
	if options.checkoutRoot != "" {
		checkoutRoot, err := filepath.Abs(options.checkoutRoot)
		if err != nil {
			return true, fmt.Errorf("resolve worker state checkout root: %w", err)
		}
		if backend.Config.CheckoutRoot != checkoutRoot {
			return true, fmt.Errorf("worker checkout root differs from configured state root")
		}
	}
	if options.prepare {
		coordinator, err := coordinatorForFactoryBackend(backend)
		if err != nil {
			return true, err
		}
		launch, err := prepareWorkerInput(context.Background(), backend, coordinator, options)
		if err != nil {
			return true, err
		}
		if err = writeJSON(options.inputOutput, launch); err != nil {
			return true, err
		}
		slog.Info("worker input prepared", "phase", launch.WorkerInput.Request.Work.Phase, "module_id", launch.WorkerInput.Request.ModuleID, "request_id", launch.WorkerInput.Request.ID)
		return true, nil
	}
	coordinator, err := coordinatorForFactoryBackend(backend)
	if err != nil {
		return true, err
	}
	result, err := sealWorkerArtifact(context.Background(), backend, coordinator, options)
	if err != nil {
		return true, err
	}
	slog.Info("worker completion sealed", "phase", result.Request.Work.Phase, "module_id", result.Request.ModuleID, "request_id", result.Request.ID, "state_head", result.Head)
	return true, nil
}

func prepareWorkerInput(ctx context.Context, backend state.GitReader, coordinator state.TransactionCoordinator, options trustedWorkerOptions) (workerLaunchInput, error) {
	if options.attemptID == "" || options.requestID == "" || options.dispatcherID == "" || options.runID == "" || options.runAttempt == 0 || options.fence == 0 || options.dataRoot == "" || options.inputOutput == "" {
		return workerLaunchInput{}, fmt.Errorf("worker preparation requires attempt, request, dispatcher, run, fence, data root, and output")
	}
	claimed, err := queue.InspectAttempt(ctx, backend, options.attemptID)
	if err != nil {
		return workerLaunchInput{}, err
	}
	exactBoundRun := claimed.Attempt.Status == queue.AttemptDispatched && claimed.Attempt.RunID == options.runID && claimed.Attempt.RunAttempt == options.runAttempt && claimed.Request.Status == queue.StatusRunning && claimed.Request.OwnerRunID == options.runID
	if claimed.Request.ID != options.requestID || (claimed.Attempt.Status != queue.AttemptClaimed && !exactBoundRun) || claimed.Attempt.Dispatcher != options.dispatcherID || claimed.Attempt.Fence != options.fence || claimed.Request.Work.Fence != options.fence {
		return workerLaunchInput{}, fmt.Errorf("worker preparation inputs do not match the durable claim")
	}
	_, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return workerLaunchInput{}, err
	}
	input := worker.Input{
		Request: claimed.Request, Owner: options.runID, Fence: options.fence,
		SourceRoot: "source", ObservedAt: time.Now().UTC(),
		Producer: worker.Producer{ID: workerProducerID, Version: workerProducerVersion, ContractVersion: workerProducerContract},
	}
	launch := workerLaunchInput{WorkerInput: input}
	if claimed.Request.Work.Phase == queue.PhaseAcquireSource {
		repository, err := pinnedRepository(snapshot, claimed.Request.ModuleID, claimed.Request.SourceSHA)
		if err != nil {
			return workerLaunchInput{}, err
		}
		launch.SourceRepository = repository
	} else {
		repository, err := pinnedRepository(snapshot, claimed.Request.ModuleID, claimed.Request.SourceSHA)
		if err != nil {
			return workerLaunchInput{}, err
		}
		launch.SourceRepository = repository
		facts, err := readAcceptedFacts(options.dataRoot, claimed.Request)
		if err != nil {
			return workerLaunchInput{}, err
		}
		input.Facts = &facts
		input.SourceRoot = ""
		input.ProviderSnapshots, err = readAcceptedProviderSnapshots(options.dataRoot, claimed.Request)
		if err != nil {
			return workerLaunchInput{}, err
		}
		if err = validateTrustedAssessmentInputs(options.dataRoot, claimed.Request, nil); err != nil {
			return workerLaunchInput{}, err
		}
		launch.WorkerInput = input
	}
	bound, err := queue.BindActionsRun(ctx, coordinator, options.attemptID, options.runID, options.runAttempt, time.Now().UTC())
	if err != nil {
		return workerLaunchInput{}, fmt.Errorf("bind worker run after input validation: %w", err)
	}
	launch.WorkerInput.Request = bound.Request
	return launch, nil
}

func pinnedRepository(snapshot state.Snapshot, moduleID, sourceSHA string) (string, error) {
	history, err := acceptedPinHistory(snapshot, moduleID)
	if err != nil {
		return "", err
	}
	return repositoryForSource(history, moduleID, sourceSHA)
}

func acceptedPinHistory(snapshot state.Snapshot, moduleID string) (discovery.PinHistory, error) {
	path, err := identity.StateSourcePinPath(moduleID)
	if err != nil {
		return discovery.PinHistory{}, err
	}
	record, exists := snapshot.Records[path]
	if !exists || record.Kind != "source_pins" || record.SchemaVersion != 1 {
		return discovery.PinHistory{}, fmt.Errorf("accepted source pin history is missing for %s", moduleID)
	}
	var history discovery.PinHistory
	if err = decodeStrictJSON(record.Data, &history); err != nil {
		return discovery.PinHistory{}, fmt.Errorf("decode accepted source pin history: %w", err)
	}
	if err = history.Validate(moduleID); err != nil {
		return discovery.PinHistory{}, err
	}
	return history, nil
}

func repositoryForSource(history discovery.PinHistory, moduleID, sourceSHA string) (string, error) {
	pins := append([]source.ResolvedSource(nil), history.ReleaseHistory...)
	if history.OverallLatest != nil {
		pins = append(pins, *history.OverallLatest)
	}
	trackNames := make([]string, 0, len(history.TrackCandidates))
	for track := range history.TrackCandidates {
		trackNames = append(trackNames, track)
	}
	sort.Strings(trackNames)
	for _, track := range trackNames {
		pins = append(pins, history.TrackCandidates[track])
	}
	repository := ""
	for _, pin := range pins {
		if pin.Commit != sourceSHA {
			continue
		}
		if !validAzureRepositoryIdentity(pin.Repository) {
			return "", fmt.Errorf("source pin repository identity is invalid for %s", moduleID)
		}
		if repository != "" && repository != pin.Repository {
			return "", fmt.Errorf("source SHA %s maps to multiple repositories for %s", sourceSHA, moduleID)
		}
		repository = pin.Repository
	}
	if repository == "" {
		return "", fmt.Errorf("no accepted source pin matches %s at %s", moduleID, sourceSHA)
	}
	return repository, nil
}

func validAzureRepositoryIdentity(repository string) bool {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Azure") || parts[1] == "" || parts[1] == "." || parts[1] == ".." {
		return false
	}
	parsed, err := url.Parse("https://github.com/" + repository)
	if err != nil || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return !strings.Contains(repository, "..") && !strings.ContainsAny(repository, "\\\x00")
}

func readAcceptedFacts(dataRoot string, request queue.Request) (terraform.Facts, error) {
	if request.Work == nil || request.Work.FactsID == "" {
		return terraform.Facts{}, fmt.Errorf("assessment request has no facts identity")
	}
	recordPath, err := identity.ModuleRecordPath(request.ModuleID, request.SourceSHA, "facts", request.Work.FactsID)
	if err != nil {
		return terraform.Facts{}, err
	}
	data, err := os.ReadFile(filepath.Join(dataRoot, filepath.FromSlash(strings.TrimPrefix(recordPath, "data/"))))
	if err != nil {
		return terraform.Facts{}, fmt.Errorf("read accepted facts %s: %w", request.Work.FactsID, err)
	}
	var facts terraform.Facts
	if err = decodeStrictJSON(data, &facts); err != nil {
		return terraform.Facts{}, fmt.Errorf("decode accepted facts: %w", err)
	}
	if err = facts.Validate(); err != nil {
		return terraform.Facts{}, err
	}
	if facts.FactsID != request.Work.FactsID || facts.ModuleID != request.ModuleID || facts.SourceSHA != request.SourceSHA {
		return terraform.Facts{}, fmt.Errorf("accepted facts identity does not match assessment request")
	}
	return facts, nil
}

func readAcceptedProviderSnapshots(dataRoot string, request queue.Request) ([]assessment.ProviderSnapshot, error) {
	if request.Work == nil || len(request.Work.SnapshotIDs) == 0 {
		return []assessment.ProviderSnapshot{}, nil
	}
	providers := make([]string, 0, len(request.Work.TargetVersions))
	for address := range request.Work.TargetVersions {
		providers = append(providers, address)
	}
	sort.Strings(providers)
	snapshots := make([]assessment.ProviderSnapshot, 0, len(request.Work.SnapshotIDs))
	for _, snapshotID := range request.Work.SnapshotIDs {
		snapshot, err := readAcceptedProviderSnapshot(dataRoot, snapshotID, providers, request.Work.TargetVersions)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func readAcceptedProviderSnapshot(dataRoot, snapshotID string, providers []string, targetVersions map[string]string) (assessment.ProviderSnapshot, error) {
	for _, address := range providers {
		version := targetVersions[address]
		recordPath, err := identity.ProviderSnapshotManifestPath(address, version, snapshotID)
		if err != nil {
			return assessment.ProviderSnapshot{}, err
		}
		data, err := os.ReadFile(filepath.Join(dataRoot, filepath.FromSlash(strings.TrimPrefix(recordPath, "data/"))))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return assessment.ProviderSnapshot{}, err
		}
		var snapshot assessment.ProviderSnapshot
		if err = decodeStrictJSON(data, &snapshot); err != nil {
			return assessment.ProviderSnapshot{}, fmt.Errorf("decode accepted provider snapshot %s: %w", snapshotID, err)
		}
		if err = assessment.ValidateProviderSnapshot(snapshot); err != nil {
			return assessment.ProviderSnapshot{}, fmt.Errorf("validate accepted provider snapshot %s: %w", snapshotID, err)
		}
		if snapshot.SnapshotID != snapshotID || snapshot.ProviderAddress != address || snapshot.ProviderVersion != version {
			return assessment.ProviderSnapshot{}, fmt.Errorf("accepted provider snapshot does not match requested target vector")
		}
		return snapshot, nil
	}
	return assessment.ProviderSnapshot{}, fmt.Errorf("accepted provider snapshot %s is missing from requested target vector", snapshotID)
}

func sealWorkerArtifact(ctx context.Context, backend state.GitReader, coordinator state.TransactionCoordinator, options trustedWorkerOptions) (queue.RecoveryResult, error) {
	if options.attemptID == "" || options.requestID == "" || options.dispatcherID == "" || options.runID == "" || options.runAttempt == 0 || options.fence == 0 || options.artifactPath == "" || options.validationID == "" || options.workflowID == "" || options.producerCommit == "" {
		return queue.RecoveryResult{}, fmt.Errorf("worker sealing requires exact attempt, request, dispatcher, run, fence, artifact, validation, and producer provenance")
	}
	current, err := queue.InspectAttempt(ctx, backend, options.attemptID)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	if current.Request.ID != options.requestID || current.Attempt.RequestID != options.requestID || current.Attempt.Dispatcher != options.dispatcherID || current.Attempt.Fence != options.fence || current.Attempt.RunID != options.runID || current.Attempt.RunAttempt != options.runAttempt || (current.Attempt.Status != queue.AttemptDispatched && current.Attempt.Status != queue.AttemptCompleted) {
		return queue.RecoveryResult{}, fmt.Errorf("trusted validator does not own the active worker attempt")
	}
	if current.Request.Work == nil || current.Attempt.Phase != current.Request.Work.Phase || (current.Attempt.Phase != queue.PhaseAcquireSource && current.Attempt.Phase != queue.PhaseAssess) {
		return queue.RecoveryResult{}, fmt.Errorf("trusted completion phase does not match the dispatched attempt")
	}
	data, err := os.ReadFile(options.artifactPath)
	if err != nil {
		return queue.RecoveryResult{}, fmt.Errorf("read worker artifact: %w", err)
	}
	var artifact worker.Artifact
	if err = decodeStrictJSON(data, &artifact); err != nil {
		return queue.RecoveryResult{}, fmt.Errorf("decode worker artifact: %w", err)
	}
	if current.Request.Work.Phase == queue.PhaseAssess {
		if err = validateTrustedAssessmentInputs(options.dataRoot, current.Request, &artifact); err != nil {
			return queue.RecoveryResult{}, err
		}
	}
	var sealed queue.Request
	sealedAt := time.Now().UTC()
	if current.Attempt.Status == queue.AttemptCompleted {
		if current.Request.Work == nil || current.Request.Work.Completion == nil || current.Request.Work.Completion.Fence != options.fence || current.Request.Work.Completion.ValidationID != options.validationID {
			return queue.RecoveryResult{}, fmt.Errorf("completed attempt has no matching sealed completion")
		}
		sealedAt = current.Request.Work.Completion.SealedAt
		activeRequest := current.Request
		activeRequest.Status = queue.StatusRunning
		activeRequest.OwnerRunID = options.runID
		activeRequest.Work.Completion = nil
		activeRequest.Work.AcceptedFactsID = ""
		activeRequest.Work.Lease = &queue.ComputeLease{Owner: options.runID, Fence: options.fence, ExpiresAt: current.Attempt.LeaseExpiresAt}
		activeRequest.Work.Fence = options.fence
		activeRequest.UpdatedAt = sealedAt
		activeRequest.UpdatedAt = sealedAt
		sealed, err = worker.Seal(activeRequest, options.runID, options.fence, artifact, options.validationID, sealedAt)
		if err != nil {
			return queue.RecoveryResult{}, err
		}
	} else {
		sealed, err = worker.Seal(current.Request, options.runID, options.fence, artifact, options.validationID, sealedAt)
		if err != nil {
			return queue.RecoveryResult{}, err
		}
	}
	activeRequest := current.Request
	if current.Attempt.Status == queue.AttemptCompleted {
		activeRequest = current.Request
		activeRequest.Status = queue.StatusRunning
		activeRequest.OwnerRunID = options.runID
		activeRequest.Work.Completion = nil
		activeRequest.Work.AcceptedFactsID = ""
		activeRequest.Work.Lease = &queue.ComputeLease{Owner: options.runID, Fence: options.fence, ExpiresAt: current.Attempt.LeaseExpiresAt}
		activeRequest.Work.Fence = options.fence
		activeRequest.UpdatedAt = sealedAt
	}
	operationID, err := identity.ID("worker-completion", 1, struct {
		AttemptID      string `json:"attempt_id"`
		RequestID      string `json:"request_id"`
		CompletionID   string `json:"completion_id"`
		ArtifactDigest string `json:"artifact_digest"`
		Fence          uint64 `json:"fence"`
		ValidationID   string `json:"validation_id"`
	}{current.Attempt.ID, sealed.ID, sealed.Work.Completion.ID, sealed.Work.Completion.ArtifactDigest, options.fence, options.validationID})
	if err != nil {
		return queue.RecoveryResult{}, fmt.Errorf("identify worker completion operation: %w", err)
	}
	_, operationSnapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	completedAttempt := current.Attempt
	if current.Attempt.Status == queue.AttemptDispatched {
		completedAttempt, err = queue.CompleteAttempt(current.Attempt, activeRequest, sealed, options.runID, options.runAttempt, options.fence, sealedAt)
		if err != nil {
			return queue.RecoveryResult{}, fmt.Errorf("complete trusted worker attempt: %w", err)
		}
	} else if !current.Attempt.CompletedAt.Equal(sealedAt) {
		return queue.RecoveryResult{}, state.ErrTransactionIdentityConflict
	}
	requestPath := filepath.ToSlash(filepath.Join("state", "requests", sealed.ID+".json"))
	requestEvent, err := workerStateEvent("worker-request:"+operationID, requestPath, "request", sealed)
	if err != nil {
		return queue.RecoveryResult{}, fmt.Errorf("build sealed request event: %w", err)
	}
	attemptPath := filepath.ToSlash(filepath.Join("state", "attempts", completedAttempt.RequestID, completedAttempt.ID+".json"))
	attemptEvent, err := workerStateEvent("worker-attempt:"+operationID, attemptPath, "attempt", completedAttempt)
	if err != nil {
		return queue.RecoveryResult{}, fmt.Errorf("build completed attempt event: %w", err)
	}
	transitionID := "worker-completion:" + operationID
	projectionFields := []projection.OwnedField{{Name: "phase", Value: "acceptance"}, {Name: "request_id", Value: sealed.ID}}
	if sealed.Work.AVMReleaseVersion != "" {
		projectionFields = append(projectionFields,
			projection.OwnedField{Name: "avm_release_version", Value: sealed.Work.AVMReleaseVersion},
			projection.OwnedField{Name: "source_version", Value: sealed.Work.AVMReleaseVersion},
		)
	}
	if sealed.SourceSHA != "" {
		projectionFields = append(projectionFields, projection.OwnedField{Name: "source_sha", Value: sealed.SourceSHA})
	}
	if sealed.Work.Phase == queue.PhaseAssess {
		projectionFields = append(projectionFields, projection.OwnedField{Name: "context_id", Value: sealed.Work.ContextID})
	}
	projectionGeneration, err := state.ModuleProjectionGeneration(operationSnapshot, sealed.ModuleID, sealed.Work.DesiredGeneration)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	intent, err := projection.NewDesiredIntent(sealed.ModuleID, projectionGeneration, transitionID, "needs-review", projectionFields, nil)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	intentIdentity, err := intent.Identity()
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	intentPath, err := identity.StateProjectionIntentPath(sealed.ModuleID)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	intentEvent, err := workerStateEvent("projection-intent:"+intent.IntentID, intentPath, "projection_intent", intent)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	inputFingerprint, err := identity.ID("input-fingerprint", 1, sealed.Fingerprint)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	handoff, err := projection.NewHandoff(projection.Handoff{
		ModuleID: sealed.ModuleID, Generation: sealed.Work.DesiredGeneration,
		TransitionID: transitionID, InputFingerprint: inputFingerprint, OperationID: operationID,
		Producer: projection.WorkflowProvenance{
			WorkflowID: options.workflowID, Commit: options.producerCommit,
			RunID: options.runID, RunAttempt: options.runAttempt,
		},
		ConsumerStage: "acceptance", Projection: intentIdentity,
	}, sealedAt)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	handoffPath, err := identity.StateProjectionHandoffPath(sealed.ModuleID, handoff.ID)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	handoffEvent, err := workerStateEvent(handoff.ID, handoffPath, "stage_handoff", handoff)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	expectedEvents := []state.Event{requestEvent, attemptEvent, intentEvent, handoffEvent}
	if prior, exists, loadErr := workerCompletionReceipt(operationSnapshot, sealed.ModuleID, operationID); loadErr != nil {
		return queue.RecoveryResult{}, loadErr
	} else if exists {
		if !workerCompletionReceiptMatches(prior, sealed, artifact) || prior.Recovery.Attempt.ID != completedAttempt.ID || prior.Recovery.Request.Work == nil || prior.Recovery.Request.Work.Completion == nil || prior.Recovery.Request.Work.Completion.ID != sealed.Work.Completion.ID || len(prior.Events) != len(expectedEvents) {
			return queue.RecoveryResult{}, state.ErrTransactionIdentityConflict
		}
		expectedEvents = prior.Events
	}
	operationCoordinator, ok := coordinator.(workerOperationCoordinator)
	if !ok {
		return queue.RecoveryResult{}, fmt.Errorf("trusted worker completion requires an operation-receipt coordinator")
	}
	result, err := operationCoordinator.RunOperationTransaction(ctx, sealed.ModuleID, operationID, expectedEvents, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
		attempt, found, loadErr := workerAttempt(snapshot, options.attemptID)
		if loadErr != nil {
			return state.TransactionDecision{}, loadErr
		}
		if !found || attempt.Status != queue.AttemptDispatched || attempt.RequestID != options.requestID || attempt.Dispatcher != options.dispatcherID || attempt.Fence != options.fence || attempt.RunID != options.runID || attempt.RunAttempt != options.runAttempt {
			return state.TransactionDecision{}, state.ErrStaleFence
		}
		requestRecord, exists := snapshot.Records[requestPath]
		if !exists || requestRecord.Kind != "request" {
			return state.TransactionDecision{}, state.ErrTransactionIdentityConflict
		}
		var durableRequest queue.Request
		if err := json.Unmarshal(requestRecord.Data, &durableRequest); err != nil {
			return state.TransactionDecision{}, err
		}
		if durableRequest.ID != options.requestID || durableRequest.Status != queue.StatusRunning || durableRequest.OwnerRunID != options.runID || durableRequest.Work == nil || durableRequest.Work.Fence != options.fence || durableRequest.Work.AttemptID != options.attemptID || durableRequest.Work.Lease == nil || durableRequest.Work.Lease.Owner != options.runID || durableRequest.Work.Lease.Fence != options.fence {
			return state.TransactionDecision{}, state.ErrStaleFence
		}
		if record, exists := snapshot.Records[intentPath]; exists {
			var currentIntent projection.DesiredIntent
			if record.Kind != "projection_intent" || json.Unmarshal(record.Data, &currentIntent) != nil || currentIntent.ValidatePath(intentPath) != nil || currentIntent.Generation > intent.Generation {
				return state.TransactionDecision{}, state.ErrTransactionConflict
			}
		}
		return state.TransactionDecision{
			ReadSet: state.TransactionReadSet{Owners: []string{sealed.ModuleID}}, Events: expectedEvents,
			Result: workerCompletionReceiptResult{
				Recovery: queue.RecoveryResult{Attempt: completedAttempt, Request: sealed},
				FactsID:  artifact.FactsID, ArtifactDigest: artifact.ArtifactDigest,
				Phase: sealed.Work.Phase, InputFingerprint: sealed.Fingerprint,
				ContextID: sealed.Work.ContextID, DefinitionIDs: append([]string(nil), sealed.Work.DefinitionIDs...),
				SnapshotIDs: append([]string(nil), sealed.Work.SnapshotIDs...), TargetVersions: cloneWorkerStringMap(sealed.Work.TargetVersions), Events: expectedEvents,
			},
			Candidate: state.CandidateOptions{Subject: "state(worker): seal trusted completion", Provenance: state.GitProvenance{Operation: "worker.seal", Event: operationID, Module: sealed.ModuleID, Request: sealed.ID, Attempt: options.attemptID, Fence: fmt.Sprint(options.fence)}},
		}, nil
	}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
		attempt, found, loadErr := workerAttempt(snapshot, options.attemptID)
		if loadErr != nil || !found {
			return state.TransactionResolution{}, loadErr
		}
		requestRecord, exists := snapshot.Records[requestPath]
		if !exists || requestRecord.Kind != "request" {
			return state.TransactionResolution{}, nil
		}
		var durableRequest queue.Request
		if err := json.Unmarshal(requestRecord.Data, &durableRequest); err != nil {
			return state.TransactionResolution{}, err
		}
		if attempt.Status != queue.AttemptCompleted || attempt.RunID != options.runID || attempt.RunAttempt != options.runAttempt || attempt.Fence != options.fence || durableRequest.Work == nil || durableRequest.Work.Completion == nil || durableRequest.Work.Completion.ID != sealed.Work.Completion.ID {
			return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
		}
		receiptPath, pathErr := identity.StateOperationReceiptPath(sealed.ModuleID, operationID)
		if pathErr != nil {
			return state.TransactionResolution{}, pathErr
		}
		receiptRecord, exists := snapshot.Records[receiptPath]
		if !exists || receiptRecord.Kind != "operation_receipt" {
			return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
		}
		var operationReceipt state.OperationReceipt
		if err := json.Unmarshal(receiptRecord.Data, &operationReceipt); err != nil || operationReceipt.Validate(receiptPath) != nil {
			return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
		}
		var immutableResult workerCompletionReceiptResult
		if err := json.Unmarshal(operationReceipt.Result, &immutableResult); err != nil || !workerCompletionReceiptMatches(immutableResult, sealed, artifact) || immutableResult.Recovery.Attempt.ID != attempt.ID || immutableResult.Recovery.Request.Work == nil || immutableResult.Recovery.Request.Work.Completion == nil || immutableResult.Recovery.Request.Work.Completion.ID != sealed.Work.Completion.ID || len(immutableResult.Events) != len(operationReceipt.WriteSet) {
			return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
		}
		return state.TransactionResolution{Found: true, Value: workerCompletionReceiptResult{
			Recovery: queue.RecoveryResult{Attempt: attempt, Request: durableRequest},
			FactsID:  artifact.FactsID, ArtifactDigest: artifact.ArtifactDigest,
			Phase: sealed.Work.Phase, InputFingerprint: sealed.Fingerprint,
			ContextID: sealed.Work.ContextID, DefinitionIDs: append([]string(nil), sealed.Work.DefinitionIDs...),
			SnapshotIDs: append([]string(nil), sealed.Work.SnapshotIDs...), TargetVersions: cloneWorkerStringMap(sealed.Work.TargetVersions), Events: immutableResult.Events,
		}}, nil
	})
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	resolved, err := workerCompletionResult(result.Value)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	resolved.Head = result.Head
	resolved.Changed = result.Changed
	return resolved, nil
}

type workerOperationCoordinator interface {
	RunOperationTransaction(context.Context, string, string, []state.Event, state.TransactionDecisionFunc, state.TransactionResultResolver) (state.TransactionResult, error)
}

type workerCompletionReceiptResult struct {
	Recovery         queue.RecoveryResult `json:"recovery"`
	FactsID          string               `json:"facts_id"`
	ArtifactDigest   string               `json:"artifact_digest"`
	Phase            queue.WorkPhase      `json:"phase"`
	InputFingerprint string               `json:"input_fingerprint"`
	ContextID        string               `json:"context_id,omitempty"`
	DefinitionIDs    []string             `json:"definition_ids,omitempty"`
	SnapshotIDs      []string             `json:"snapshot_ids,omitempty"`
	TargetVersions   map[string]string    `json:"target_versions,omitempty"`
	Events           []state.Event        `json:"events"`
}

func workerCompletionReceipt(snapshot state.Snapshot, moduleID, operationID string) (workerCompletionReceiptResult, bool, error) {
	path, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		return workerCompletionReceiptResult{}, false, err
	}
	record, exists := snapshot.Records[path]
	if !exists {
		return workerCompletionReceiptResult{}, false, nil
	}
	if record.Kind != "operation_receipt" {
		return workerCompletionReceiptResult{}, false, state.ErrTransactionIdentityConflict
	}
	var receipt state.OperationReceipt
	if json.Unmarshal(record.Data, &receipt) != nil || receipt.Validate(path) != nil || receipt.ModuleID != moduleID || receipt.OperationID != operationID {
		return workerCompletionReceiptResult{}, false, state.ErrTransactionIdentityConflict
	}
	var result workerCompletionReceiptResult
	if json.Unmarshal(receipt.Result, &result) != nil || len(result.Events) != len(receipt.WriteSet) {
		return workerCompletionReceiptResult{}, false, state.ErrTransactionIdentityConflict
	}
	return result, true, nil
}

func validateTrustedAssessmentInputs(dataRoot string, request queue.Request, artifact *worker.Artifact) error {
	if request.Work == nil || request.Work.Phase != queue.PhaseAssess {
		return fmt.Errorf("trusted assessment input validation requires an assessment request")
	}
	facts, err := readAcceptedFacts(dataRoot, request)
	if err != nil {
		return err
	}
	snapshots, err := readAcceptedProviderSnapshots(dataRoot, request)
	if err != nil {
		return err
	}
	if err = worker.ValidateProviderTargetVector(request, snapshots); err != nil {
		return err
	}
	registry, err := assessment.DefaultRegistry()
	if err != nil {
		return err
	}
	compiledDefinitions := make(map[string]struct{})
	for _, definition := range registry.ListDimensions() {
		compiledDefinitions[definition.DefinitionID] = struct{}{}
	}
	for _, definitionID := range request.Work.DefinitionIDs {
		if _, exists := compiledDefinitions[definitionID]; !exists {
			return fmt.Errorf("assessment definition vector is not in the compiled registry")
		}
	}
	if artifact == nil {
		return nil
	}
	if artifact.FactsID != facts.FactsID || artifact.Analysis == nil || artifact.Analysis.ContextID != request.Work.ContextID {
		return fmt.Errorf("assessment artifact does not match its accepted context inputs")
	}
	requestedDefinitions := make(map[string]struct{}, len(request.Work.DefinitionIDs))
	for _, definitionID := range request.Work.DefinitionIDs {
		requestedDefinitions[definitionID] = struct{}{}
	}
	for _, result := range artifact.DimensionResults {
		if _, exists := requestedDefinitions[result.DefinitionID]; !exists {
			return fmt.Errorf("assessment artifact contains a definition outside the exact request vector")
		}
	}
	return nil
}

func workerCompletionReceiptMatches(result workerCompletionReceiptResult, request queue.Request, artifact worker.Artifact) bool {
	if request.Work == nil || result.Recovery.Request.ID != request.ID || result.Recovery.Request.ModuleID != request.ModuleID || result.Recovery.Request.Fingerprint != request.Fingerprint || result.Recovery.Request.Work == nil || result.Recovery.Request.Work.Completion == nil || request.Work.Completion == nil || result.Recovery.Request.Work.Completion.ID != request.Work.Completion.ID || result.Recovery.Attempt.ID == "" || result.Recovery.Attempt.RequestID != request.ID || result.FactsID != artifact.FactsID || result.ArtifactDigest != artifact.ArtifactDigest || result.Phase != request.Work.Phase || result.InputFingerprint != request.Fingerprint || result.ContextID != request.Work.ContextID {
		return false
	}
	definitions, definitionErr := identity.SortedSet(result.DefinitionIDs)
	wantedDefinitions, wantedDefinitionErr := identity.SortedSet(request.Work.DefinitionIDs)
	snapshots, snapshotErr := identity.SortedSet(result.SnapshotIDs)
	wantedSnapshots, wantedSnapshotErr := identity.SortedSet(request.Work.SnapshotIDs)
	return definitionErr == nil && wantedDefinitionErr == nil && equalWorkerStrings(definitions, wantedDefinitions) && snapshotErr == nil && wantedSnapshotErr == nil && equalWorkerStrings(snapshots, wantedSnapshots) && equalWorkerStringMaps(result.TargetVersions, request.Work.TargetVersions)
}

func equalWorkerStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneWorkerStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func equalWorkerStringMaps(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func workerStateEvent(eventID, recordPath, kind string, value any) (state.Event, error) {
	if err := identity.ValidateRelativePath(recordPath, false); err != nil {
		return state.Event{}, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return state.Event{}, err
	}
	return state.Event{ID: eventID, RecordPath: recordPath, Record: state.Record{Kind: kind, SchemaVersion: 1, Data: data}}, nil
}

func workerCompletionResult(value any) (queue.RecoveryResult, error) {
	switch result := value.(type) {
	case queue.RecoveryResult:
		return result, nil
	case workerCompletionReceiptResult:
		if result.FactsID == "" || result.ArtifactDigest == "" || result.Recovery.Attempt.ID == "" || result.Recovery.Request.ID == "" {
			return queue.RecoveryResult{}, state.ErrTransactionIdentityConflict
		}
		return result.Recovery, nil
	case json.RawMessage:
		var decoded workerCompletionReceiptResult
		if err := json.Unmarshal(result, &decoded); err != nil {
			return queue.RecoveryResult{}, fmt.Errorf("decode immutable worker completion result: %w", err)
		}
		if decoded.FactsID == "" || decoded.ArtifactDigest == "" || decoded.Recovery.Attempt.ID == "" || decoded.Recovery.Request.ID == "" {
			return queue.RecoveryResult{}, state.ErrTransactionIdentityConflict
		}
		return decoded.Recovery, nil
	default:
		return queue.RecoveryResult{}, fmt.Errorf("committed worker completion result has an invalid type")
	}
}

func workerAttempt(snapshot state.Snapshot, attemptID string) (queue.Attempt, bool, error) {
	for recordPath, record := range snapshot.Records {
		if record.Kind != "attempt" {
			continue
		}
		var attempt queue.Attempt
		if err := json.Unmarshal(record.Data, &attempt); err != nil {
			return queue.Attempt{}, false, err
		}
		if err := attempt.Validate(); err != nil || recordPath != "state/attempts/"+attempt.RequestID+"/"+attempt.ID+".json" {
			return queue.Attempt{}, false, fmt.Errorf("durable attempt identity is invalid")
		}
		if attempt.ID == attemptID {
			return attempt, true, nil
		}
	}
	return queue.Attempt{}, false, nil
}

func decodeStrictJSON(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("JSON input must contain one document")
	}
	return nil
}

func handleWorkerCommand(inputPath, artifactPath string, trusted trustedWorkerOptions) (bool, error) {
	if inputPath != "" || artifactPath != "" {
		if inputPath == "" || artifactPath == "" || trusted.prepare || trusted.seal || trusted.checkoutRoot != "" {
			return true, fmt.Errorf("--process-work and --work-artifact-out must be provided together and cannot be combined with trusted worker commands")
		}
		return true, processWorkerInput(inputPath, artifactPath)
	}
	return handleTrustedWorkerCommand(trusted)
}

func processWorkerInput(inputPath, artifactPath string) error {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read worker input: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var input worker.Input
	if err = decoder.Decode(&input); err != nil {
		return fmt.Errorf("decode worker input: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("worker input must contain one JSON document")
	}
	artifact, err := worker.Process(input)
	if err != nil {
		return fmt.Errorf("process v2 work request: %w", err)
	}
	if err = writeJSON(artifactPath, artifact); err != nil {
		return fmt.Errorf("write module evidence artifact: %w", err)
	}
	slog.Info("worker artifact written", "phase", artifact.Phase, "module_id", artifact.ModuleID, "request_id", artifact.RequestID, "artifact", artifactPath)
	return nil
}
