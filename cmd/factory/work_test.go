package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/github"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
	"github.com/softwareone-public/avm-intelligence/internal/worker"
)

func TestTrustedPreparationAndSealBindPinnedSourceAndReleaseOnlyAfterValidation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	moduleID := "avm-res-worker-trusted"
	sourceSHA := strings.Repeat("a", 40)
	repository := "Azure/terraform-azurerm-avm-res-worker-trusted"
	request, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: moduleID, SourceSHA: sourceSHA, AVMReleaseVersion: "v1.0.0", DesiredGeneration: 1,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{terraform.CapabilityProviderRequirements},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.ToSlash(filepath.Join("state", "requests", request.ID+".json"))
	pin := source.ResolvedSource{
		ModuleID: moduleID, Repository: repository, Version: "v1.0.0", Tag: "v1.0.0",
		Commit: sourceSHA, ArchiveURL: "https://github.com/" + repository + "/archive/" + sourceSHA + ".tar.gz",
		ReleaseURL: "https://github.com/" + repository + "/releases/tag/v1.0.0", ResolvedAt: now,
	}
	pinHistory := discovery.PinHistory{
		SchemaVersion: 1, ModuleID: moduleID, ReleaseHistory: []source.ResolvedSource{pin},
		TrackCandidates: map[string]source.ResolvedSource{},
	}
	requestData, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	pinData, err := json.Marshal(pinHistory)
	if err != nil {
		t.Fatal(err)
	}
	sourcePinPath, err := identity.StateSourcePinPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	backend := &workerTestBackend{head: "head-0", snapshot: state.NewSnapshot()}
	intent, err := projection.NewDesiredIntent(moduleID, request.Work.DesiredGeneration, "trusted-worker-"+request.ID, "queued", []projection.OwnedField{{Name: "status", Value: "Queued"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	requiredProjection, err := intent.Identity()
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := identity.ID("operation", 1, struct {
		RequestID string `json:"request_id"`
	}{request.ID})
	if err != nil {
		t.Fatal(err)
	}
	inputFingerprint, err := identity.ID("input-fingerprint", 1, request.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := projection.NewHandoff(projection.Handoff{
		ModuleID: moduleID, Generation: request.Work.DesiredGeneration,
		TransitionID: requiredProjection.TransitionID, InputFingerprint: inputFingerprint,
		OperationID:   operationID,
		Producer:      projection.WorkflowProvenance{WorkflowID: "factory-worker-test", Commit: sourceSHA, RunID: "worker-test-run", RunAttempt: 1},
		ConsumerStage: "acquire_source", Projection: requiredProjection,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	intentPath, err := identity.StateProjectionIntentPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	handoffPath, err := identity.StateProjectionHandoffPath(moduleID, handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	acknowledgementPath, err := identity.StateProjectionAcknowledgementPath(moduleID, intent.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	acknowledgement := projection.Acknowledgement{
		SchemaVersion: projection.SchemaVersion, Projection: requiredProjection,
		Status: projection.AcknowledgementDone, AcknowledgedAt: now,
	}
	encode := func(value any) json.RawMessage {
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return data
	}
	seedEvents := []state.Event{
		{ID: request.ID, RecordPath: requestPath, Record: state.Record{Kind: "request", SchemaVersion: 1, Data: requestData}},
		{ID: "source-pin-" + moduleID, RecordPath: sourcePinPath, Record: state.Record{Kind: "source_pins", SchemaVersion: 1, Data: pinData}},
		{ID: "projection-intent-" + request.ID, RecordPath: intentPath, Record: state.Record{Kind: "projection_intent", SchemaVersion: 1, Data: encode(intent)}},
		{ID: "stage-handoff-" + request.ID, RecordPath: handoffPath, Record: state.Record{Kind: "stage_handoff", SchemaVersion: 1, Data: encode(handoff)}},
		{ID: "projection-ack-" + request.ID, RecordPath: acknowledgementPath, Record: state.Record{Kind: "projection_acknowledgement", SchemaVersion: 1, Data: encode(acknowledgement)}},
	}
	receiptEvent, err := state.NewOperationReceipt(moduleID, operationID, json.RawMessage(`{"queued":true}`), seedEvents)
	if err != nil {
		t.Fatal(err)
	}
	seedEvents = append(seedEvents, receiptEvent)
	if _, _, err = state.ApplyEventsAtHead(ctx, backend, backend.head, seedEvents); err != nil {
		t.Fatalf("seed durable worker handoff: %v", err)
	}
	backend.head = "head-seeded"
	if _, err = state.MigrateModuleLayout(ctx, backend); err != nil {
		t.Fatalf("migrate worker snapshot to module-v2: %v", err)
	}
	claim, err := queue.ClaimNext(ctx, workerTestCoordinator{backend}, "dispatcher-run", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	configuration := `terraform {
  required_providers {
		azurerm = {
			source  = "hashicorp/azurerm"
			version = "~> 4.0"
		}
  }
}
resource "azurerm_resource_group" "main" {}`
	if err = os.WriteFile(filepath.Join(root, "main.tf"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	runID := "worker-run-1"
	options := trustedWorkerOptions{
		attemptID: claim.Attempt.ID, requestID: request.ID, dispatcherID: "dispatcher-run",
		runID: runID, runAttempt: 1, fence: claim.Attempt.Fence,
		dataRoot: t.TempDir(), inputOutput: filepath.Join(t.TempDir(), "worker-input.json"),
		workflowID: "factory-worker", producerCommit: strings.Repeat("b", 40),
	}
	coordinator := workerTestCoordinator{backend}
	launch, err := prepareWorkerInput(ctx, backend, coordinator, options)
	if err != nil {
		t.Fatal(err)
	}
	if launch.SourceRepository != repository || launch.WorkerInput.Request.Status != queue.StatusRunning || launch.WorkerInput.Owner != runID {
		t.Fatalf("prepared worker input does not match bound source/run: %#v", launch)
	}
	replayed, err := prepareWorkerInput(ctx, backend, coordinator, options)
	if err != nil || replayed.WorkerInput.Request.ID != launch.WorkerInput.Request.ID {
		t.Fatalf("exact preparation replay failed: %#v, %v", replayed, err)
	}
	foreign := options
	foreign.runID = "different-worker"
	if _, err := prepareWorkerInput(ctx, backend, coordinator, foreign); err == nil {
		t.Fatal("preparation replay transferred ownership to another worker")
	}
	foreign = options
	foreign.runAttempt++
	if _, err := prepareWorkerInput(ctx, backend, coordinator, foreign); err == nil {
		t.Fatal("preparation replay transferred ownership to another run attempt")
	}
	launch.WorkerInput.SourceRoot = root
	artifact, err := worker.Process(launch.WorkerInput)
	if err != nil {
		t.Fatal(err)
	}
	acceptedRoot := t.TempDir()
	factsPath, err := identity.ModuleRecordPath(moduleID, sourceSHA, "facts", artifact.FactsID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(acceptedRoot, strings.TrimPrefix(factsPath, "data/")), artifact.Facts); err != nil {
		t.Fatal(err)
	}
	assessmentRequest, err := queue.NewAssessmentRequest(queue.AssessmentInput{
		ModuleID: moduleID, SourceSHA: sourceSHA, ContextID: "azurerm/4", Track: "azurerm/4", DesiredGeneration: 1,
		FactsID: artifact.FactsID, DefinitionIDs: []string{"definition-" + strings.Repeat("d", 64)},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if facts, err := readAcceptedFacts(acceptedRoot, assessmentRequest); err != nil || facts.FactsID != artifact.FactsID {
		t.Fatalf("normal accepted facts root failed: %v", err)
	}
	snapshotID := "snapshot-" + strings.Repeat("b", 64)
	address := "registry.terraform.io/hashicorp/azurerm"
	manifest, err := identity.ProviderSnapshotManifestPath(address, "4.0.0", snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(acceptedRoot, strings.TrimPrefix(manifest, "data/")), assessment.ProviderSnapshot{
		SchemaVersion: 2, RecordType: "provider_snapshot", SnapshotID: snapshotID,
		ProviderAddress: address, ProviderVersion: "4.0.0",
		Coverage: assessment.SnapshotCoverage{Capabilities: map[string]string{}, ObservedAt: "2026-10-02T12:00:00Z"},
		Entries:  []assessment.ProviderEntry{}, Producer: assessment.SnapshotProducer{ID: "worker-test", Version: "1.0.0", ContractVersion: 1},
		SourceDigest: strings.Repeat("c", 64), ObservedAt: "2026-10-02T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := readAcceptedProviderSnapshot(acceptedRoot, snapshotID, []string{address}, map[string]string{address: "4.0.0"}); err != nil {
		t.Fatalf("normal accepted provider root failed: %v", err)
	}
	assertInvalidWorkerArtifactsRejected(t, launch.WorkerInput.Request, runID, claim.Attempt.Fence, artifact, now)
	options.artifactPath = writeWorkerArtifact(t, artifact)
	options.validationID = "trusted-validator.v1"
	sealed, err := sealWorkerArtifact(ctx, backend, coordinator, options)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkerCompletionReleased(t, sealed.Request)
	assertAcquireCompletionHandoff(t, backend.snapshot, options, artifact, sealed)
	replayedSeal, err := sealWorkerArtifact(ctx, backend, coordinator, options)
	if err != nil || replayedSeal.Attempt.ID != sealed.Attempt.ID || replayedSeal.Request.Work.Completion.ID != sealed.Request.Work.Completion.ID || replayedSeal.Changed {
		t.Fatalf("immutable completion receipt replay mismatch: result=%+v err=%v", replayedSeal, err)
	}
	for _, mutate := range []func(*trustedWorkerOptions){
		func(value *trustedWorkerOptions) { value.runID = "foreign-run" },
		func(value *trustedWorkerOptions) { value.runAttempt++ },
		func(value *trustedWorkerOptions) { value.fence++ },
		func(value *trustedWorkerOptions) { value.requestID = "foreign-request" },
		func(value *trustedWorkerOptions) { value.dispatcherID = "foreign-dispatcher" },
	} {
		foreign := options
		mutate(&foreign)
		if _, err = sealWorkerArtifact(ctx, backend, coordinator, foreign); err == nil {
			t.Fatal("trusted seal accepted a foreign owner, attempt, request, dispatcher, or fence")
		}
	}
	malformed := artifact
	malformed.FactsID = "facts-malformed"
	options.artifactPath = writeWorkerArtifact(t, malformed)
	if _, err = sealWorkerArtifact(ctx, backend, coordinator, options); err == nil {
		t.Fatal("trusted seal accepted an artifact with a malformed facts identity")
	}
	if err := validateSealedEvidenceDelivery(sealed, launch, artifact); err != nil {
		t.Fatalf("persisted sealed evidence could not be delivered: %v", err)
	}
	tampered := artifact
	tampered.ArtifactDigest = strings.Repeat("f", 64)
	if err := validateSealedEvidenceDelivery(sealed, launch, tampered); err == nil {
		t.Fatal("delivery accepted artifact outside persisted completion")
	}
	foreignLaunch := launch
	foreignLaunch.WorkerInput.Owner = "foreign-worker"
	if err := validateSealedEvidenceDelivery(sealed, foreignLaunch, artifact); err == nil {
		t.Fatal("delivery accepted foreign launch ownership")
	}
	changedProducer := launch
	changedProducer.WorkerInput.Producer.Version = "unreviewed"
	if err := validateSealedEvidenceDelivery(sealed, changedProducer, artifact); err == nil {
		t.Fatal("delivery accepted altered launch producer")
	}
	changedObservation := launch
	changedObservation.WorkerInput.ObservedAt = launch.WorkerInput.ObservedAt.Add(time.Second)
	if err := validateSealedEvidenceDelivery(sealed, changedObservation, artifact); err == nil {
		t.Fatal("delivery accepted altered launch observation")
	}
	if _, err = os.Stat(filepath.Join(options.dataRoot, "modules")); !os.IsNotExist(err) {
		t.Fatalf("trusted worker path wrote accepted data: stat err = %v", err)
	}
}

func TestTrustedAssessmentCompletionCommitsExactContextHandoff(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	moduleID, sourceSHA, owner := "avm-res-assessment-trusted", strings.Repeat("c", 40), "assessment-run-7"
	const fence = 7

	sourceRoot := t.TempDir()
	configuration := `terraform {
  required_providers {
    azurerm = {
      source = "hashicorp/azurerm"
    }
  }
}`
	if err := os.WriteFile(filepath.Join(sourceRoot, "versions.tf"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "main.tf"), []byte(`resource "azurerm_resource_group" "main" {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	acquisition, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: moduleID, SourceSHA: sourceSHA, DesiredGeneration: 3,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{terraform.CapabilityProviderRequirements},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	factsArtifact, err := worker.Process(worker.Input{
		Request: runningWorkerRequest(acquisition, owner, fence, now), Owner: owner, Fence: fence,
		SourceRoot: sourceRoot, ObservedAt: now,
		Producer: worker.Producer{ID: workerProducerID, Version: workerProducerVersion, ContractVersion: workerProducerContract},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := assessment.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.ListDimensions()
	if len(definitions) == 0 {
		t.Fatal("compiled assessment registry has no definitions")
	}
	var providerDefinitionID string
	for _, definition := range definitions {
		if definition.DimensionID == assessment.DimensionProviderRequirements {
			providerDefinitionID = definition.DefinitionID
		}
	}
	if providerDefinitionID == "" {
		t.Fatal("provider-requirements definition is missing")
	}
	address, version := "registry.terraform.io/hashicorp/azurerm", "4.0.0"
	snapshotID := "snapshot-" + strings.Repeat("b", 64)
	observedAt := now.Add(-time.Hour).Format(time.RFC3339)
	snapshot := assessment.ProviderSnapshot{
		SchemaVersion: 2, RecordType: "provider_snapshot", SnapshotID: snapshotID,
		ProviderAddress: address, ProviderVersion: version,
		Coverage: assessment.SnapshotCoverage{Capabilities: map[string]string{"resource": "complete"}, Complete: true, ObservedAt: observedAt},
		Entries:  []assessment.ProviderEntry{}, Producer: assessment.SnapshotProducer{ID: "worker-test", Version: "1.0.0", ContractVersion: 1},
		SourceDigest: strings.Repeat("e", 64), ObservedAt: observedAt,
	}
	request, err := queue.NewAssessmentRequest(queue.AssessmentInput{
		ModuleID: moduleID, SourceSHA: sourceSHA, ContextID: "azurerm/4", Track: "azurerm/4",
		DesiredGeneration: 3, FactsID: factsArtifact.FactsID, DefinitionIDs: []string{providerDefinitionID},
		SnapshotIDs: []string{snapshotID}, TargetVersions: map[string]string{address: version},
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	running := runningWorkerRequest(request, owner, fence, now.Add(2*time.Minute))
	artifact, err := worker.Process(worker.Input{
		Request: running, Owner: owner, Fence: fence, Facts: factsArtifact.Facts,
		ProviderSnapshots: []assessment.ProviderSnapshot{snapshot}, ObservedAt: now.Add(3 * time.Minute),
		Producer: worker.Producer{ID: workerProducerID, Version: workerProducerVersion, ContractVersion: workerProducerContract},
	})
	if err != nil {
		t.Fatal(err)
	}
	dataRoot := t.TempDir()
	factsPath, err := identity.ModuleRecordPath(moduleID, sourceSHA, "facts", factsArtifact.FactsID)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeJSON(filepath.Join(dataRoot, strings.TrimPrefix(factsPath, "data/")), factsArtifact.Facts); err != nil {
		t.Fatal(err)
	}
	snapshotPath, err := identity.ProviderSnapshotManifestPath(address, version, snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeJSON(filepath.Join(dataRoot, strings.TrimPrefix(snapshotPath, "data/")), snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = readAcceptedProviderSnapshot(dataRoot, snapshotID, []string{address}, map[string]string{address: version}); err != nil {
		t.Fatalf("trusted loader rejected the exact accepted provider snapshot: %v", err)
	}
	options := trustedWorkerOptions{
		attemptID: running.Work.AttemptID, requestID: running.ID, dispatcherID: "assessment-dispatcher",
		runID: owner, runAttempt: 2, fence: fence, dataRoot: dataRoot,
		artifactPath: writeWorkerArtifact(t, artifact), validationID: "trusted-assessment-validator.v1",
		workflowID: "factory-worker", producerCommit: strings.Repeat("d", 40),
	}
	otherContext, err := queue.NewAssessmentRequest(queue.AssessmentInput{
		ModuleID: moduleID, SourceSHA: sourceSHA, ContextID: "azurerm/5", Track: "azurerm/5",
		DesiredGeneration: 3, FactsID: factsArtifact.FactsID, DefinitionIDs: []string{providerDefinitionID},
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	backend := seedTrustedAssessmentState(t, running, otherContext, options, now)
	coordinator := workerTestCoordinator{backend}
	sealed, err := sealWorkerArtifact(ctx, backend, coordinator, options)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Request.Status != queue.StatusAwaitingAcceptance || sealed.Request.Work.Completion == nil || sealed.Request.Work.Lease != nil || sealed.Request.LastAcceptedFingerprint != "" {
		t.Fatalf("assessment seal crossed acceptance or retained compute ownership: %+v", sealed.Request)
	}
	assertAssessmentCompletionHandoff(t, backend.snapshot, options, artifact, sealed)
	replayed, err := sealWorkerArtifact(ctx, backend, coordinator, options)
	if err != nil || replayed.Changed || replayed.Request.Work.Completion.ID != sealed.Request.Work.Completion.ID {
		t.Fatalf("assessment receipt replay changed its completion: result=%+v err=%v", replayed, err)
	}
	for _, mutate := range []func(*trustedWorkerOptions){
		func(value *trustedWorkerOptions) { value.runID = "foreign-assessment-run" },
		func(value *trustedWorkerOptions) { value.runAttempt++ },
		func(value *trustedWorkerOptions) { value.fence++ },
		func(value *trustedWorkerOptions) { value.attemptID = "attempt-" + strings.Repeat("f", 64) },
		func(value *trustedWorkerOptions) { value.dispatcherID = "foreign-assessment-dispatcher" },
	} {
		foreign := options
		mutate(&foreign)
		if _, err = sealWorkerArtifact(ctx, backend, coordinator, foreign); err == nil {
			t.Fatal("assessment seal accepted foreign attempt/run/fence ownership")
		}
	}
	if err = validateTrustedAssessmentInputs(dataRoot, request, &artifact); err != nil {
		t.Fatal(err)
	}
	malformedSnapshot := snapshot
	malformedSnapshot.SourceDigest = "invalid"
	if err = writeJSON(filepath.Join(dataRoot, strings.TrimPrefix(snapshotPath, "data/")), malformedSnapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = readAcceptedProviderSnapshot(dataRoot, snapshotID, []string{address}, map[string]string{address: version}); err == nil {
		t.Fatal("trusted loader accepted an invalid accepted provider snapshot record")
	}
	if err = writeJSON(filepath.Join(dataRoot, strings.TrimPrefix(snapshotPath, "data/")), snapshot); err != nil {
		t.Fatal(err)
	}
	wrongFacts := request
	wrongFacts.Work = cloneTestWork(request.Work)
	wrongFacts.Work.FactsID = "facts-" + strings.Repeat("f", 64)
	if err = validateTrustedAssessmentInputs(dataRoot, wrongFacts, nil); err == nil {
		t.Fatal("assessment accepted facts outside its exact request vector")
	}
	wrongDefinitions := request
	wrongDefinitions.Work = cloneTestWork(request.Work)
	wrongDefinitions.Work.DefinitionIDs = []string{"definition-" + strings.Repeat("f", 64)}
	if err = validateTrustedAssessmentInputs(dataRoot, wrongDefinitions, nil); err == nil {
		t.Fatal("assessment accepted a definition outside the compiled request vector")
	}
	wrongSnapshots := request
	wrongSnapshots.Work = cloneTestWork(request.Work)
	wrongSnapshots.Work.SnapshotIDs = []string{"snapshot-" + strings.Repeat("f", 64)}
	if err = validateTrustedAssessmentInputs(dataRoot, wrongSnapshots, nil); err == nil {
		t.Fatal("assessment accepted a snapshot outside its exact context vector")
	}
	wrongTargets := request
	wrongTargets.Work = cloneTestWork(request.Work)
	wrongTargets.Work.TargetVersions = map[string]string{address: "5.0.0"}
	if err = validateTrustedAssessmentInputs(dataRoot, wrongTargets, nil); err == nil {
		t.Fatal("assessment accepted a target version outside its exact context vector")
	}
	wrongContext := artifact
	wrongContext.Analysis = cloneTestAnalysis(artifact.Analysis)
	wrongContext.Analysis.ContextID = "azurerm/5"
	if err = validateTrustedAssessmentInputs(dataRoot, request, &wrongContext); err == nil {
		t.Fatal("assessment artifact crossed its context boundary")
	}
}

func runningWorkerRequest(request queue.Request, owner string, fence uint64, now time.Time) queue.Request {
	request.Status, request.OwnerRunID, request.UpdatedAt = queue.StatusRunning, owner, now.UTC()
	request.Work.Fence = fence
	request.Work.AttemptID = "attempt-" + strings.Repeat("a", 64)
	request.Work.Lease = &queue.ComputeLease{Owner: owner, Fence: fence, ExpiresAt: now.Add(time.Hour).UTC()}
	return request
}

func seedTrustedAssessmentState(t *testing.T, running, sibling queue.Request, options trustedWorkerOptions, now time.Time) *workerTestBackend {
	t.Helper()
	attempt := queue.Attempt{
		SchemaVersion: 1, ID: options.attemptID, RequestID: running.ID, ModuleID: running.ModuleID,
		InputFingerprint: running.Fingerprint, SourceSHA: running.SourceSHA, Phase: queue.PhaseAssess,
		DesiredGeneration: running.Work.DesiredGeneration, Number: 1, Fence: options.fence,
		Dispatcher: options.dispatcherID, Status: queue.AttemptDispatched, RunID: options.runID,
		RunAttempt: options.runAttempt, ClaimedAt: now, BoundAt: now.Add(time.Minute), LeaseExpiresAt: now.Add(time.Hour),
	}
	if err := attempt.Validate(); err != nil {
		t.Fatalf("validate trusted assessment fixture attempt: %v", err)
	}
	requestPath, attemptPath := "state/requests/"+running.ID+".json", "state/attempts/"+running.ID+"/"+attempt.ID+".json"
	encode := func(value any) json.RawMessage {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	backend := &workerTestBackend{head: "assessment-head-0", snapshot: state.NewSnapshot()}
	events := []state.Event{
		{ID: "assessment-running-request", RecordPath: requestPath, Record: state.Record{Kind: "request", SchemaVersion: 1, Data: encode(running)}},
		{ID: "assessment-attempt", RecordPath: attemptPath, Record: state.Record{Kind: "attempt", SchemaVersion: 1, Data: encode(attempt)}},
		{ID: "assessment-sibling-request", RecordPath: "state/requests/" + sibling.ID + ".json", Record: state.Record{Kind: "request", SchemaVersion: 1, Data: encode(sibling)}},
	}
	if _, _, err := state.ApplyEventsAtHead(context.Background(), backend, backend.head, events); err != nil {
		t.Fatalf("seed trusted assessment state: %v", err)
	}
	backend.head = "assessment-head-seeded"
	if _, err := state.MigrateModuleLayout(context.Background(), backend); err != nil {
		t.Fatalf("migrate trusted assessment state to module-v2: %v", err)
	}
	return backend
}

func cloneTestWork(work *queue.WorkContract) *queue.WorkContract {
	data, _ := json.Marshal(work)
	var cloned queue.WorkContract
	if err := json.Unmarshal(data, &cloned); err != nil {
		panic(err)
	}
	return &cloned
}

func cloneTestAnalysis(analysis *worker.Analysis) *worker.Analysis {
	data, _ := json.Marshal(analysis)
	var cloned worker.Analysis
	if err := json.Unmarshal(data, &cloned); err != nil {
		panic(err)
	}
	return &cloned
}

func assertAssessmentCompletionHandoff(t *testing.T, snapshot state.Snapshot, options trustedWorkerOptions, artifact worker.Artifact, completion queue.RecoveryResult) {
	t.Helper()
	var handoff projection.Handoff
	var receipt state.OperationReceipt
	var intent projection.DesiredIntent
	for path, record := range snapshot.Records {
		switch record.Kind {
		case "stage_handoff":
			var candidate projection.Handoff
			if json.Unmarshal(record.Data, &candidate) == nil && candidate.Producer.RunID == options.runID {
				handoff = candidate
				wantPath, err := identity.StateProjectionHandoffPath(candidate.ModuleID, candidate.ID)
				if err != nil || path != wantPath {
					t.Fatal("assessment handoff is not stored at its canonical path")
				}
			}
		case "operation_receipt":
			var candidate state.OperationReceipt
			if json.Unmarshal(record.Data, &candidate) == nil && candidate.ModuleID == completion.Request.ModuleID {
				var result workerCompletionReceiptResult
				if json.Unmarshal(candidate.Result, &result) == nil && result.Phase == queue.PhaseAssess {
					receipt = candidate
					if candidate.Validate(path) != nil || result.ContextID != completion.Request.Work.ContextID || !equalWorkerStrings(result.DefinitionIDs, completion.Request.Work.DefinitionIDs) || !equalWorkerStrings(result.SnapshotIDs, completion.Request.Work.SnapshotIDs) || !equalWorkerStringMaps(result.TargetVersions, completion.Request.Work.TargetVersions) || result.FactsID != artifact.FactsID || result.ArtifactDigest != artifact.ArtifactDigest {
						t.Fatal("assessment receipt does not preserve its exact context input vector")
					}
				}
			}
		case "projection_intent":
			if json.Unmarshal(record.Data, &intent) == nil && intent.ModuleID == completion.Request.ModuleID {
				if intent.Validate() != nil || intent.Status != "needs-review" || intent.Generation != completion.Request.Work.DesiredGeneration {
					t.Fatal("assessment projection intent is invalid or implies acceptance")
				}
			}
		}
	}
	if handoff.ID == "" || handoff.ConsumerStage != "acceptance" || handoff.Producer.WorkflowID != options.workflowID || handoff.Producer.Commit != options.producerCommit || handoff.Producer.RunAttempt != options.runAttempt || receipt.OperationID == "" || intent.IntentID == "" {
		t.Fatalf("assessment completion handoff, receipt or intent is missing: handoff=%+v", handoff)
	}
	sibling := readSiblingAssessmentRequest(t, snapshot, completion.Request.ModuleID)
	contexts, err := queue.ContextsFromRequests(completion.Request.ModuleID, []queue.TrackState{
		{ContextID: "azurerm/4", Track: "azurerm/4", Applicability: queue.ApplicabilityApplicable, Candidate: queue.CandidateEligible},
		{ContextID: "azurerm/5", Track: "azurerm/5", Applicability: queue.ApplicabilityApplicable, Candidate: queue.CandidateEligible},
	}, []queue.Request{completion.Request, sibling})
	if err != nil || len(contexts) != 2 {
		t.Fatalf("assessment seal changed independent context reduction: contexts=%+v err=%v", contexts, err)
	}
	if contexts[0].State.ContextID != "azurerm/4" || !contexts[0].NeedsReview || contexts[1].State.ContextID != "azurerm/5" || contexts[1].DesiredPhase != queue.ModuleStatusBacklog {
		t.Fatalf("assessment completion did not preserve per-context reducer outcomes: %+v", contexts)
	}
}

func readSiblingAssessmentRequest(t *testing.T, snapshot state.Snapshot, moduleID string) queue.Request {
	t.Helper()
	for _, record := range snapshot.Records {
		if record.Kind != "request" {
			continue
		}
		var request queue.Request
		if json.Unmarshal(record.Data, &request) == nil && request.ModuleID == moduleID && request.Work != nil && request.Work.ContextID == "azurerm/5" {
			if request.Status != queue.StatusQueued {
				t.Fatal("assessment completion mutated the sibling context request")
			}
			return request
		}
	}
	t.Fatal("sibling assessment context was not preserved")
	return queue.Request{}
}

func assertAcquireCompletionHandoff(t *testing.T, snapshot state.Snapshot, options trustedWorkerOptions, artifact worker.Artifact, completion queue.RecoveryResult) {
	t.Helper()
	if completion.Attempt.Status != queue.AttemptCompleted || completion.Request.Status != queue.StatusAwaitingAcceptance || completion.Request.OwnerRunID != "" || completion.Request.Work.Lease != nil {
		t.Fatalf("acquisition completion did not release exactly its active attempt: %+v", completion)
	}
	if completion.Request.Work.Completion == nil || completion.Request.Work.AcceptedFactsID != "" {
		t.Fatal("trusted sealing crossed the evidence acceptance boundary")
	}
	receiptPath, err := identity.StateOperationReceiptPath(completion.Request.ModuleID, completion.Attempt.OperationID)
	if err == nil {
		if _, exists := snapshot.Records[receiptPath]; exists {
			t.Fatal("worker completion reused the dispatcher claim operation receipt")
		}
	}
	var foundReceipt state.OperationReceipt
	for recordPath, record := range snapshot.Records {
		if record.Kind != "operation_receipt" {
			continue
		}
		var receipt state.OperationReceipt
		if json.Unmarshal(record.Data, &receipt) == nil && receipt.ModuleID == completion.Request.ModuleID {
			var result workerCompletionReceiptResult
			if json.Unmarshal(receipt.Result, &result) != nil || result.FactsID != artifact.FactsID || result.ArtifactDigest != artifact.ArtifactDigest {
				continue
			}
			for _, entry := range receipt.WriteSet {
				if entry.RecordPath == "state/requests/"+completion.Request.ID+".json" && entry.RecordKind == "request" {
					foundReceipt = receipt
					if receipt.Validate(recordPath) != nil {
						t.Fatal("completion receipt is invalid")
					}
				}
			}
		}
	}
	if foundReceipt.OperationID == "" {
		t.Fatal("acquisition completion has no immutable owner-scoped receipt")
	}
	var receiptResult workerCompletionReceiptResult
	if len(foundReceipt.Result) == 0 || json.Unmarshal(foundReceipt.Result, &receiptResult) != nil || receiptResult.FactsID != artifact.FactsID || receiptResult.ArtifactDigest != artifact.ArtifactDigest || receiptResult.Recovery.Request.Work == nil || receiptResult.Recovery.Request.Work.Completion == nil || receiptResult.Recovery.Request.Work.Completion.ID != completion.Request.Work.Completion.ID {
		t.Fatal("immutable completion receipt does not reference the acquired facts artifact")
	}
	var handoff projection.Handoff
	var handoffPath string
	for recordPath, record := range snapshot.Records {
		if record.Kind != "stage_handoff" {
			continue
		}
		if err := json.Unmarshal(record.Data, &handoff); err == nil && handoff.OperationID == foundReceipt.OperationID {
			var pathErr error
			handoffPath, pathErr = identity.StateProjectionHandoffPath(handoff.ModuleID, handoff.ID)
			if pathErr != nil || recordPath != handoffPath {
				t.Fatal("completion handoff is not stored at its canonical path")
			}
			break
		}
	}
	if err := handoff.Validate(); err != nil || handoff.ConsumerStage != "acceptance" || handoff.Producer.WorkflowID != options.workflowID || handoff.Producer.Commit != options.producerCommit || handoff.Producer.RunID != options.runID || handoff.Producer.RunAttempt != options.runAttempt {
		t.Fatalf("trusted acquisition handoff provenance or acceptance boundary is invalid: %+v, %v", handoff, err)
	}
	intentPath, err := identity.StateProjectionIntentPath(completion.Request.ModuleID)
	if err != nil {
		t.Fatal(err)
	}
	var receiptSet map[string]struct{}
	for _, entry := range foundReceipt.WriteSet {
		if receiptSet == nil {
			receiptSet = make(map[string]struct{}, len(foundReceipt.WriteSet))
		}
		receiptSet[entry.RecordPath] = struct{}{}
	}
	for _, requiredPath := range []string{"state/requests/" + completion.Request.ID + ".json", "state/attempts/" + completion.Attempt.RequestID + "/" + completion.Attempt.ID + ".json", intentPath, handoffPath} {
		if _, exists := receiptSet[requiredPath]; !exists {
			t.Fatalf("completion receipt omits atomically committed handoff record %s", requiredPath)
		}
	}
	var intent projection.DesiredIntent
	if record := snapshot.Records[intentPath]; record.Kind != "projection_intent" || json.Unmarshal(record.Data, &intent) != nil || intent.Validate() != nil || intent.Status != "needs-review" || intent.Generation != completion.Request.Work.DesiredGeneration {
		t.Fatal("acquisition completion projection intent is missing or invalid")
	}
}

func TestReviewTreeReadsRawBlobsAndCommittedModes(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture Git failed: %v %s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--quiet")
	if err := os.MkdirAll(filepath.Join(root, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "record.json"), []byte(`{"raw":"$Format:%H$"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("data/** export-ignore export-subst\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "fixture")
	commit := git("rev-parse", "HEAD")
	output := t.TempDir()
	if err := extractReviewTree(t.Context(), root, commit, output, "data"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "data", "record.json"))
	if err != nil || string(data) != `{"raw":"$Format:%H$"}` {
		t.Fatalf("attributes changed raw evidence: %s %v", data, err)
	}
	remaining := len(data)
	if err := extractReviewTreeWithBudget(t.Context(), root, commit, t.TempDir(), &remaining, "data"); err != nil {
		t.Fatal(err)
	}
	if err := extractReviewTreeWithBudget(t.Context(), root, commit, t.TempDir(), &remaining, "data"); err == nil {
		t.Fatal("base and head extraction reset shared byte allowance")
	}
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "new-base")
	newBase := git("rev-parse", "HEAD")
	if err := validateEvidenceReviewTree(t.Context(), root, github.EvidenceReview{BaseSHA: newBase, HeadSHA: commit}); err == nil || !strings.Contains(err.Error(), "does not descend") {
		t.Fatalf("equal-tree older head passed ancestry check: %v", err)
	}
	git("update-index", "--chmod=+x", "data/record.json")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "executable")
	git("config", "tar.umask", "0777")
	if err := extractReviewTree(t.Context(), root, git("rev-parse", "HEAD"), t.TempDir(), "data"); err == nil {
		t.Fatal("committed executable passed raw mode validation")
	}
	t.Setenv("GIT_DIR", filepath.Join(root, ".git"))
	if _, err := reviewGit(t.Context(), root, "rev-parse", "HEAD"); err == nil {
		t.Fatal("redirected Git environment passed")
	}
}

func TestEvidenceReviewDataComparisonRejectsExtraOrAlteredRecords(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(left, "record.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(right, "record.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := compareReviewData(left, right); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(right, "extra.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := compareReviewData(left, right); err == nil {
		t.Fatal("review accepted undeclared data")
	}
	if err := run([]string{"--open-evidence-review", "--prepare-evidence-delivery"}); err == nil {
		t.Fatal("review accepted mixed commands")
	}
}

func TestEvidenceDeliveryTimeCoversAcceptedBaseDeterministically(t *testing.T) {
	root := t.TempDir()
	baseTime := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	if err := writeJSON(filepath.Join(root, "dataset.json"), map[string]any{"created_at": baseTime}); err != nil {
		t.Fatal(err)
	}
	for _, seal := range []time.Time{baseTime.Add(-time.Hour), baseTime.Add(time.Hour)} {
		expected := baseTime
		if seal.After(baseTime) {
			expected = seal
		}
		first, err := evidenceDeliveryTime(root, seal)
		if err != nil || !first.Equal(expected) {
			t.Fatalf("delivery timestamp = %s, %v", first, err)
		}
		second, err := evidenceDeliveryTime(root, seal)
		if err != nil || !second.Equal(first) {
			t.Fatal("delivery timestamp changed across retry")
		}
	}
}

func TestPrepareEvidenceDeliveryRejectsIncompleteAndMixedModes(t *testing.T) {
	for _, args := range [][]string{
		{"--prepare-evidence-delivery", "--state-branch", "factory-state"},
		{"--prepare-evidence-delivery", "--build-module-evidence-proposal"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("incomplete or mixed delivery command passed: %v", args)
		}
	}
	for _, flag := range []string{"--fence", "--actions-run-id", "--module-evidence-validation-id", "--module-evidence-proposal-created-at"} {
		if err := run([]string{"--prepare-evidence-delivery", flag, "1"}); err == nil || !strings.Contains(err.Error(), "unsupported for evidence delivery") {
			t.Fatalf("delivery ignored auxiliary flag %s: %v", flag, err)
		}
	}
	dataRoot := t.TempDir()
	parent := t.TempDir()
	link := filepath.Join(parent, "accepted-link")
	if err := os.Symlink(dataRoot, link); err != nil {
		t.Fatal(err)
	}
	if err := validateEvidenceDeliveryRoots(dataRoot, filepath.Join(link, "proposal")); err == nil {
		t.Fatal("delivery accepted symlinked output into accepted data")
	}
	if err := validateEvidenceDeliveryRoots(dataRoot, filepath.Join(parent, "proposal")); err != nil {
		t.Fatal(err)
	}
}

func assertInvalidWorkerArtifactsRejected(t *testing.T, request queue.Request, owner string, fence uint64, artifact worker.Artifact, now time.Time) {
	t.Helper()
	if _, err := worker.Seal(request, owner, fence+1, artifact, "trusted-validator.v1", now.Add(time.Minute)); err == nil {
		t.Fatal("worker artifact sealed under a stale fencing token")
	}
	tamperedArtifact := artifact
	tamperedArtifact.FactsID = "facts-" + strings.Repeat("f", 64)
	if _, err := worker.Seal(request, owner, fence, tamperedArtifact, "trusted-validator.v1", now.Add(time.Minute)); err == nil {
		t.Fatal("worker artifact with altered facts identity was sealed")
	}
	tamperedArtifact = artifact
	tamperedArtifact.ArtifactDigest = strings.Repeat("0", 64)
	if _, err := worker.Seal(request, owner, fence, tamperedArtifact, "trusted-validator.v1", now.Add(time.Minute)); err == nil {
		t.Fatal("worker artifact with altered digest was sealed")
	}
}

func writeWorkerArtifact(t *testing.T, artifact worker.Artifact) string {
	t.Helper()
	artifactData, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(t.TempDir(), "module-evidence.json")
	if err = os.WriteFile(artifactPath, artifactData, 0o600); err != nil {
		t.Fatal(err)
	}
	return artifactPath
}

func assertWorkerCompletionReleased(t *testing.T, request queue.Request) {
	t.Helper()
	if request.Status != queue.StatusAwaitingAcceptance || request.Work.Lease != nil || request.Work.Completion == nil {
		t.Fatalf("trusted validation did not seal/release the request: %#v", request)
	}
}

func TestPinnedWorkerRepositoryMustBeAzureOwned(t *testing.T) {
	for _, test := range []struct {
		repository string
		valid      bool
	}{
		{repository: "Azure/terraform-azurerm-avm-res-demo", valid: true},
		{repository: "Other/terraform-azurerm-avm-res-demo"},
		{repository: "Azure/../private"},
		{repository: "Azure/private?archive=true"},
	} {
		if got := validAzureRepositoryIdentity(test.repository); got != test.valid {
			t.Errorf("validAzureRepositoryIdentity(%q) = %t, want %t", test.repository, got, test.valid)
		}
	}
}

type workerTestBackend struct {
	head     string
	snapshot state.Snapshot
}

type workerTestCoordinator struct{ backend state.LegacyCommitBackend }

func (coordinator workerTestCoordinator) Read(ctx context.Context) (string, state.Snapshot, error) {
	return coordinator.backend.Read(ctx)
}

func (coordinator workerTestCoordinator) RunTransaction(ctx context.Context, operationID string, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	for attempt := 0; attempt < 3; attempt++ {
		head, snapshot, err := coordinator.backend.Read(ctx)
		if err != nil {
			return state.TransactionResult{}, err
		}
		if workerEventApplied(snapshot, operationID) {
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
		var commitErr error
		updated, _, commitErr := state.ApplyEventsAtHead(ctx, coordinator.backend, head, decision.Events)
		if errors.Is(commitErr, state.ErrHeadMoved) && attempt < 2 {
			continue
		}
		if commitErr != nil {
			return state.TransactionResult{}, commitErr
		}
		return state.TransactionResult{Head: updated, Value: decision.Result, Changed: true}, nil
	}
	return state.TransactionResult{}, state.ErrTransactionConflict
}

func (coordinator workerTestCoordinator) RunOperationTransaction(ctx context.Context, moduleID, operationID string, expectedEvents []state.Event, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	for attempt := 0; attempt < 3; attempt++ {
		head, snapshot, err := coordinator.backend.Read(ctx)
		if err != nil {
			return state.TransactionResult{}, err
		}
		receiptPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
		if err != nil {
			return state.TransactionResult{}, err
		}
		if workerEventApplied(snapshot, operationID) {
			record, exists := snapshot.Records[receiptPath]
			if !exists || record.Kind != "operation_receipt" {
				return state.TransactionResult{}, state.ErrTransactionIdentityConflict
			}
			var receipt state.OperationReceipt
			if json.Unmarshal(record.Data, &receipt) != nil || receipt.Validate(receiptPath) != nil || len(receipt.WriteSet) != len(expectedEvents) {
				return state.TransactionResult{}, state.ErrTransactionIdentityConflict
			}
			return state.TransactionResult{Head: head, Value: append(json.RawMessage(nil), receipt.Result...)}, nil
		}
		decision, err := decide(snapshot)
		if err != nil {
			return state.TransactionResult{}, err
		}
		if len(decision.Events) != len(expectedEvents) {
			return state.TransactionResult{}, state.ErrTransactionIdentityConflict
		}
		for _, event := range expectedEvents {
			var matched *state.Event
			for index := range decision.Events {
				if decision.Events[index].ID == event.ID {
					matched = &decision.Events[index]
					break
				}
			}
			left, leftErr := json.Marshal(matched)
			right, rightErr := json.Marshal(event)
			if matched == nil || leftErr != nil || rightErr != nil || !bytes.Equal(left, right) {
				return state.TransactionResult{}, state.ErrTransactionIdentityConflict
			}
		}
		resultData, err := json.Marshal(decision.Result)
		if err != nil {
			return state.TransactionResult{}, err
		}
		receipt, err := state.NewOperationReceipt(moduleID, operationID, resultData, expectedEvents)
		if err != nil {
			return state.TransactionResult{}, err
		}
		events := append(append([]state.Event(nil), expectedEvents...), receipt)
		updated, _, err := state.ApplyEventsAtHead(ctx, coordinator.backend, head, events)
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

func workerEventApplied(snapshot state.Snapshot, operationID string) bool {
	if _, exists := snapshot.AppliedEvents[operationID]; exists {
		return true
	}
	for _, aggregate := range snapshot.Aggregates {
		if _, exists := aggregate.AppliedEvents[operationID]; exists {
			return true
		}
	}
	return false
}

func (backend *workerTestBackend) Read(_ context.Context) (string, state.Snapshot, error) {
	return backend.head, backend.snapshot, nil
}

func (backend *workerTestBackend) Commit(_ context.Context, expectedHead string, snapshot state.Snapshot) (string, error) {
	if expectedHead != backend.head {
		return "", state.ErrHeadMoved
	}
	backend.head = fmt.Sprintf("head-%d", snapshot.Generation)
	backend.snapshot = snapshot
	return backend.head, nil
}
