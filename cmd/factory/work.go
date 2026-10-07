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
	prepare      bool
	seal         bool
	attemptID    string
	requestID    string
	dispatcherID string
	runID        string
	runAttempt   uint64
	fence        uint64
	stateBranch  string
	dataRoot     string
	inputOutput  string
	artifactPath string
	validationID string
	checkoutRoot string
	checkoutTree string
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
	if (options.checkoutRoot == "") != (options.checkoutTree == "") {
		return true, fmt.Errorf("worker checkout root and tree listing must be supplied together")
	}
	backend, err := newVerifiedFactoryStateBackend(options.stateBranch)
	if err != nil {
		return true, err
	}
	if options.checkoutRoot != "" {
		if err := seedDispatchCheckout(backend, options.checkoutRoot, options.checkoutTree); err != nil {
			return true, fmt.Errorf("seed worker state checkout: %w", err)
		}
	}
	if options.prepare {
		launch, err := prepareWorkerInput(context.Background(), backend, options)
		if err != nil {
			return true, err
		}
		if err = writeJSON(options.inputOutput, launch); err != nil {
			return true, err
		}
		slog.Info("worker input prepared", "phase", launch.WorkerInput.Request.Work.Phase, "module_id", launch.WorkerInput.Request.ModuleID, "request_id", launch.WorkerInput.Request.ID)
		return true, nil
	}
	result, err := sealWorkerArtifact(context.Background(), backend, options)
	if err != nil {
		return true, err
	}
	slog.Info("worker completion sealed", "phase", result.Request.Work.Phase, "module_id", result.Request.ModuleID, "request_id", result.Request.ID, "state_head", result.Head)
	return true, nil
}

func prepareWorkerInput(ctx context.Context, backend state.Backend, options trustedWorkerOptions) (workerLaunchInput, error) {
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
		launch.WorkerInput = input
	}
	bound, err := queue.BindActionsRun(ctx, backend, options.attemptID, options.runID, options.runAttempt, time.Now().UTC())
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
		if snapshot.SnapshotID != snapshotID || snapshot.ProviderAddress != address || snapshot.ProviderVersion != version {
			return assessment.ProviderSnapshot{}, fmt.Errorf("accepted provider snapshot does not match requested target vector")
		}
		return snapshot, nil
	}
	return assessment.ProviderSnapshot{}, fmt.Errorf("accepted provider snapshot %s is missing from requested target vector", snapshotID)
}

func sealWorkerArtifact(ctx context.Context, backend state.Backend, options trustedWorkerOptions) (queue.RecoveryResult, error) {
	if options.attemptID == "" || options.runID == "" || options.runAttempt == 0 || options.fence == 0 || options.artifactPath == "" || options.validationID == "" {
		return queue.RecoveryResult{}, fmt.Errorf("worker sealing requires attempt, run, attempt number, fence, artifact, and validation identity")
	}
	current, err := queue.InspectAttempt(ctx, backend, options.attemptID)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	if current.Attempt.Status != queue.AttemptDispatched || current.Attempt.RunID != options.runID || current.Attempt.RunAttempt != options.runAttempt || current.Attempt.Fence != options.fence {
		return queue.RecoveryResult{}, fmt.Errorf("trusted validator does not own the active worker attempt")
	}
	data, err := os.ReadFile(options.artifactPath)
	if err != nil {
		return queue.RecoveryResult{}, fmt.Errorf("read worker artifact: %w", err)
	}
	var artifact worker.Artifact
	if err = decodeStrictJSON(data, &artifact); err != nil {
		return queue.RecoveryResult{}, fmt.Errorf("decode worker artifact: %w", err)
	}
	sealed, err := worker.Seal(current.Request, options.runID, options.fence, artifact, options.validationID, time.Now().UTC())
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	requestPath := filepath.ToSlash(filepath.Join("state", "requests", sealed.ID+".json"))
	if err = identity.ValidateRelativePath(requestPath, false); err != nil {
		return queue.RecoveryResult{}, fmt.Errorf("invalid sealed request path: %w", err)
	}
	encoded, err := json.Marshal(sealed)
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	event := state.Event{
		ID:         "seal-" + sealed.Work.Completion.ID,
		RecordPath: requestPath,
		Record:     state.Record{Kind: "request", SchemaVersion: 1, Data: encoded},
	}
	newHead, _, err := state.ApplyEventsAtHead(ctx, backend, current.Head, []state.Event{event})
	if err != nil {
		return queue.RecoveryResult{}, err
	}
	current.Head = newHead
	current.Request = sealed
	current.Changed = true
	return current, nil
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
		if inputPath == "" || artifactPath == "" || trusted.prepare || trusted.seal || trusted.checkoutRoot != "" || trusted.checkoutTree != "" {
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
