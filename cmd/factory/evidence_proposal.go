package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/publish"
	"github.com/softwareone-public/avm-intelligence/internal/worker"
)

const moduleEvidenceProposalFilename = "proposal.json"

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
