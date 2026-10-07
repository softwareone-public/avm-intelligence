package main

import (
	"context"
	"encoding/json"
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
	replayed, err := prepareWorkerInput(ctx, backend, options)
	if err != nil || replayed.WorkerInput.Request.ID != launch.WorkerInput.Request.ID {
		t.Fatalf("exact preparation replay failed: %#v, %v", replayed, err)
	}
	foreign := options
	foreign.runID = "different-worker"
	if _, err := prepareWorkerInput(ctx, backend, foreign); err == nil {
		t.Fatal("preparation replay transferred ownership to another worker")
	}
	foreign = options
	foreign.runAttempt++
	if _, err := prepareWorkerInput(ctx, backend, foreign); err == nil {
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
	if err := writeJSON(filepath.Join(acceptedRoot, strings.TrimPrefix(manifest, "data/")), assessment.ProviderSnapshot{SnapshotID: snapshotID, ProviderAddress: address, ProviderVersion: "4.0.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := readAcceptedProviderSnapshot(acceptedRoot, snapshotID, []string{address}, map[string]string{address: "4.0.0"}); err != nil {
		t.Fatalf("normal accepted provider root failed: %v", err)
	}
	assertInvalidWorkerArtifactsRejected(t, launch.WorkerInput.Request, runID, claim.Attempt.Fence, artifact, now)
	options.artifactPath = writeWorkerArtifact(t, artifact)
	options.validationID = "trusted-validator.v1"
	sealed, err := sealWorkerArtifact(ctx, backend, options)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkerCompletionReleased(t, sealed.Request)
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
