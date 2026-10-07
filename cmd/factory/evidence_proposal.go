package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/publish"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/worker"
)

const moduleEvidenceProposalFilename = "proposal.json"

// validateSealedEvidenceDelivery permits proposal recovery only from the exact
// persisted completion; it does not grant a new compute owner or seal state.
func validateSealedEvidenceDelivery(current queue.RecoveryResult, launch workerLaunchInput, artifact worker.Artifact) error {
	request := current.Request
	if err := request.Validate(); err != nil {
		return err
	}
	if request.Work.Completion == nil || (request.Status != queue.StatusAwaitingAcceptance && request.Status != queue.StatusFactsAccepted && request.Status != queue.StatusAccepted) {
		return fmt.Errorf("evidence delivery requires a persisted sealed completion")
	}
	if launch.WorkerInput.Request.ID != request.ID || launch.WorkerInput.Owner != current.Attempt.RunID || launch.WorkerInput.Fence != current.Attempt.Fence || request.Work.AttemptID != current.Attempt.ID || request.Work.Fence != current.Attempt.Fence {
		return fmt.Errorf("evidence delivery launch does not match persisted ownership")
	}
	producer := launch.WorkerInput.Producer
	if producer.ID != workerProducerID || producer.Version != workerProducerVersion || producer.ContractVersion != workerProducerContract {
		return fmt.Errorf("evidence delivery producer is not the reviewed native producer")
	}
	observedAt := ""
	if artifact.Facts != nil {
		observedAt = artifact.Facts.Coverage.ObservedAt
	} else if artifact.Analysis != nil {
		observedAt = artifact.Analysis.CreatedAt.UTC().Format(time.RFC3339Nano)
		if artifact.Analysis.Producer != producer {
			return fmt.Errorf("evidence delivery analysis producer differs from launch")
		}
	}
	observed, err := time.Parse(time.RFC3339Nano, observedAt)
	if err != nil || !observed.Equal(launch.WorkerInput.ObservedAt) {
		return fmt.Errorf("evidence delivery observation differs from sealed artifact")
	}
	completion := request.Work.Completion
	sealed, err := worker.Seal(launch.WorkerInput.Request, launch.WorkerInput.Owner, launch.WorkerInput.Fence, artifact, completion.ValidationID, completion.SealedAt)
	if err != nil {
		return fmt.Errorf("validate delivery artifact against persisted completion: %w", err)
	}
	actual := sealed.Work.Completion
	if actual.ID != completion.ID || actual.ArtifactDigest != completion.ArtifactDigest || actual.InputFingerprint != completion.InputFingerprint || actual.DesiredGeneration != completion.DesiredGeneration || actual.Fence != completion.Fence {
		return fmt.Errorf("evidence delivery completion identity differs from durable seal")
	}
	return nil
}

func prepareEvidenceDelivery(branch, attemptID, launchPath, artifactPath, expectedBase, dataRoot, outputRoot string) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	if attemptID == "" || launchPath == "" || artifactPath == "" || outputRoot == "" || !lowerHexSHA(expectedBase, 40) {
		return fmt.Errorf("evidence delivery requires exact attempt, launch, artifact, base and output")
	}
	if err := validateEvidenceDeliveryRoots(dataRoot, outputRoot); err != nil {
		return err
	}
	launchData, err := os.ReadFile(launchPath)
	if err != nil {
		return fmt.Errorf("read delivery launch: %w", err)
	}
	var launch workerLaunchInput
	if err := decodeStrictJSON(launchData, &launch); err != nil {
		return fmt.Errorf("decode delivery launch: %w", err)
	}
	artifactData, err := os.ReadFile(artifactPath)
	if err != nil {
		return fmt.Errorf("read delivery artifact: %w", err)
	}
	var artifact worker.Artifact
	if err := decodeStrictJSON(artifactData, &artifact); err != nil {
		return fmt.Errorf("decode delivery artifact: %w", err)
	}
	backend, err := newVerifiedFactoryStateBackend(branch)
	if err != nil {
		return err
	}
	current, err := queue.InspectAttempt(context.Background(), backend, attemptID)
	if err != nil {
		return fmt.Errorf("inspect sealed delivery attempt: %w", err)
	}
	if err := validateSealedEvidenceDelivery(current, launch, artifact); err != nil {
		return err
	}
	proposalTime, err := evidenceDeliveryTime(dataRoot, current.Request.Work.Completion.SealedAt)
	if err != nil {
		return err
	}
	proposal, err := publish.BuildModuleEvidenceProposal(publish.ModuleEvidenceProposalInput{
		ProposalRoot: outputRoot, BaseDataRoot: dataRoot, ExpectedBase: expectedBase,
		Repository: launch.SourceRepository, WorkerInput: launch.WorkerInput, Artifact: artifact,
		ValidationID: current.Request.Work.Completion.ValidationID, SealedAt: proposalTime,
	})
	if err != nil {
		return fmt.Errorf("build verified delivery proposal: %w", err)
	}
	return writeJSON(filepath.Join(outputRoot, moduleEvidenceProposalFilename), proposal)
}

func evidenceDeliveryTime(dataRoot string, sealedAt time.Time) (time.Time, error) {
	data, err := os.ReadFile(filepath.Join(dataRoot, "dataset.json"))
	if err != nil {
		return time.Time{}, err
	}
	var dataset struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(data, &dataset); err != nil || dataset.CreatedAt.IsZero() || sealedAt.IsZero() {
		return time.Time{}, fmt.Errorf("delivery dataset and seal timestamps are required")
	}
	if dataset.CreatedAt.After(sealedAt) {
		return dataset.CreatedAt.UTC(), nil
	}
	return sealedAt.UTC(), nil
}

func validateEvidenceDeliveryRoots(dataRoot, outputRoot string) error {
	dataPath, err := providerCollectionDirectory(dataRoot)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(outputRoot); !os.IsNotExist(err) {
		return fmt.Errorf("evidence delivery output must not already exist")
	}
	parent, err := providerCollectionDirectory(filepath.Dir(outputRoot))
	if err != nil {
		return fmt.Errorf("evidence delivery requires an existing real output parent: %w", err)
	}
	if providerCollectionPathsOverlap(dataPath, filepath.Join(parent, filepath.Base(outputRoot))) {
		return fmt.Errorf("evidence delivery output overlaps accepted data")
	}
	return nil
}

func buildModuleEvidenceProposal(launchPath, artifactPath, expectedBase, dataRoot, outputRoot, validationID, proposalCreatedAt string) error {
	if strings.TrimSpace(launchPath) == "" || strings.TrimSpace(artifactPath) == "" || strings.TrimSpace(outputRoot) == "" || strings.TrimSpace(validationID) == "" || !lowerHexSHA(expectedBase, 40) {
		return fmt.Errorf("worker launch, evidence artifact, validation ID, output directory, and lowercase proposal base are required")
	}
	sealedAt := time.Now().UTC()
	if strings.TrimSpace(proposalCreatedAt) != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, proposalCreatedAt)
		if parseErr != nil {
			return fmt.Errorf("parse module evidence proposal timestamp: %w", parseErr)
		}
		sealedAt = parsed.UTC()
	}
	if err := ensureOutputOutsideData(dataRoot, filepath.Join(outputRoot, moduleEvidenceProposalFilename)); err != nil {
		return err
	}
	if _, err := os.Lstat(outputRoot); err == nil {
		return fmt.Errorf("module evidence proposal output directory already exists")
	} else if !os.IsNotExist(err) {
		return err
	}

	launchData, err := os.ReadFile(launchPath)
	if err != nil {
		return fmt.Errorf("read trusted worker launch artifact: %w", err)
	}
	var launch workerLaunchInput
	if err = decodeStrictJSON(launchData, &launch); err != nil {
		return fmt.Errorf("decode trusted worker launch artifact: %w", err)
	}
	if strings.TrimSpace(launch.SourceRepository) == "" {
		return fmt.Errorf("worker launch artifact does not identify the source repository")
	}

	artifactData, err := os.ReadFile(artifactPath)
	if err != nil {
		return fmt.Errorf("read worker evidence artifact: %w", err)
	}
	var artifact worker.Artifact
	if err = decodeStrictJSON(artifactData, &artifact); err != nil {
		return fmt.Errorf("decode worker evidence artifact: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(outputRoot), 0o755); err != nil {
		return fmt.Errorf("create module evidence proposal parent: %w", err)
	}

	proposal, err := publish.BuildModuleEvidenceProposal(publish.ModuleEvidenceProposalInput{
		ProposalRoot: outputRoot, BaseDataRoot: dataRoot, ExpectedBase: expectedBase,
		Repository: launch.SourceRepository, WorkerInput: launch.WorkerInput,
		Artifact: artifact, ValidationID: validationID, SealedAt: sealedAt,
	})
	if err != nil {
		return fmt.Errorf("build module evidence proposal: %w", err)
	}
	proposalData, err := json.MarshalIndent(proposal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode module evidence proposal: %w", err)
	}
	if err = os.WriteFile(filepath.Join(outputRoot, moduleEvidenceProposalFilename), proposalData, 0o644); err != nil {
		return fmt.Errorf("write module evidence proposal: %w", err)
	}
	slog.Info("module evidence proposal built", "module_id", proposal.Subject, "proposal_id", proposal.ProposalID, "output", outputRoot)
	return nil
}
