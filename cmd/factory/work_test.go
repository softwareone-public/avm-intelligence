package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
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
		ModuleID: moduleID, SourceSHA: sourceSHA, DesiredGeneration: 1,
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
	backend := &workerTestBackend{
		head: "head-0",
		snapshot: state.Snapshot{
			SchemaVersion: state.SnapshotSchemaVersion, AppliedEvents: map[string]string{}, FencingTokens: map[string]uint64{},
			Records: map[string]state.Record{
				requestPath:   {Kind: "request", SchemaVersion: 1, Data: requestData},
				sourcePinPath: {Kind: "source_pins", SchemaVersion: 1, Data: pinData},
			},
		},
	}
	claim, err := queue.ClaimNext(ctx, backend, "dispatcher-run", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	configuration := `terraform {
  required_providers {
    azurerm = { source = "hashicorp/azurerm" }
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
	}
	launch, err := prepareWorkerInput(ctx, backend, options)
	if err != nil {
		t.Fatal(err)
	}
	if launch.SourceRepository != repository || launch.WorkerInput.Request.Status != queue.StatusRunning || launch.WorkerInput.Owner != runID {
		t.Fatalf("prepared worker input does not match bound source/run: %#v", launch)
	}
	launch.WorkerInput.SourceRoot = root
	artifact, err := worker.Process(launch.WorkerInput)
	if err != nil {
		t.Fatal(err)
	}
	assertInvalidWorkerArtifactsRejected(t, launch.WorkerInput.Request, runID, claim.Attempt.Fence, artifact, now)
	options.artifactPath = writeWorkerArtifact(t, artifact)
	options.validationID = "trusted-validator.v1"
	sealed, err := sealWorkerArtifact(ctx, backend, options)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkerCompletionReleased(t, sealed.Request)
	if _, err = os.Stat(filepath.Join(options.dataRoot, "modules")); !os.IsNotExist(err) {
		t.Fatalf("trusted worker path wrote accepted data: stat err = %v", err)
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
