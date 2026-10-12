package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/provider"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"golang.org/x/mod/semver"
)

type providerSnapshotInputImport struct {
	ProviderAddress string                        `json:"provider_address"`
	ProviderVersion string                        `json:"provider_version"`
	SnapshotID      string                        `json:"snapshot_id"`
	AcceptedCommit  string                        `json:"accepted_commit"`
	ImportedAt      time.Time                     `json:"imported_at"`
	Producer        projection.WorkflowProvenance `json:"producer"`
}

func importProviderSnapshotAssessmentInputs(branch, checkoutRoot, inputPath, acceptedDataRoot string) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	if strings.TrimSpace(inputPath) == "" || strings.TrimSpace(acceptedDataRoot) == "" {
		return fmt.Errorf("provider snapshot import requires an input file and exact accepted data tree")
	}
	encoded, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read provider snapshot input import: %w", err)
	}
	var input providerSnapshotInputImport
	if err = decodeStrictJSON(encoded, &input); err != nil {
		return fmt.Errorf("decode provider snapshot input import: %w", err)
	}
	if err = validateProviderSnapshotInputImport(input); err != nil {
		return err
	}
	snapshot, err := provider.LoadAcceptedProviderSnapshot(acceptedDataRoot, input.ProviderAddress, input.ProviderVersion, input.SnapshotID)
	if err != nil {
		return fmt.Errorf("verify exact accepted provider snapshot: %w", err)
	}
	if snapshot.ProviderAddress != input.ProviderAddress || snapshot.ProviderVersion != input.ProviderVersion || snapshot.SnapshotID != input.SnapshotID {
		return fmt.Errorf("accepted provider snapshot identity changed during verification")
	}
	major, err := providerVersionMajor(input.ProviderVersion)
	if err != nil {
		return fmt.Errorf("provider snapshot version major is invalid")
	}
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return err
	}
	_, snapshotState, err := setup.coordinator.Read(context.Background())
	if err != nil {
		return err
	}
	targets, err := state.ProviderSnapshotAssessmentTargets(snapshotState, input.ProviderAddress, major)
	if err != nil {
		return fmt.Errorf("derive targets from accepted facts receipts: %w", err)
	}
	result, err := state.ImportAssessmentInputs(context.Background(), setup.coordinator, state.AssessmentInputImport{
		Kind: state.InputProviderSnapshot, InputKey: input.ProviderAddress, InputID: input.SnapshotID,
		Targets: targets, AcceptedCommit: input.AcceptedCommit, Producer: input.Producer, ImportedAt: input.ImportedAt,
	})
	if err != nil {
		return fmt.Errorf("import accepted provider snapshot generations: %w", err)
	}
	if len(targets) > 0 {
		policy, policyErr := provider.LoadCollectionPolicy("config/providers.yaml")
		if policyErr != nil {
			return fmt.Errorf("load reviewed provider assessment policy: %w", policyErr)
		}
		policyID, policyErr := provider.AssessmentTrackPolicyID(policy)
		if policyErr != nil {
			return fmt.Errorf("identify reviewed provider assessment policy: %w", policyErr)
		}
		policyImport, importErr := state.ImportAssessmentInputs(context.Background(), setup.coordinator, state.AssessmentInputImport{
			Kind: state.InputAssessmentPolicy, InputKey: state.AssessmentPolicyInputKey, InputID: policyID,
			Targets: targets, AcceptedCommit: input.AcceptedCommit, Producer: input.Producer, ImportedAt: input.ImportedAt,
		})
		if importErr != nil {
			return fmt.Errorf("import exact provider assessment policy generations: %w", importErr)
		}
		slog.Info("provider assessment policy inputs imported", "policy_id", policyID, "targets", len(policyImport.Import.Targets), "state_head", policyImport.Head, "changed", policyImport.Changed)
	}
	slog.Info("provider snapshot inputs imported", "provider", input.ProviderAddress, "version", input.ProviderVersion,
		"snapshot_id", input.SnapshotID, "targets", len(result.Import.Targets), "state_head", result.Head, "changed", result.Changed)
	return nil
}

func validateProviderSnapshotInputImport(input providerSnapshotInputImport) error {
	if identity.ValidateRelativePath(input.ProviderAddress, false) != nil || len(strings.Split(input.ProviderAddress, "/")) != 3 ||
		identity.ValidateRecordID("snapshot", input.SnapshotID) != nil || input.ImportedAt.IsZero() || input.Producer.Validate() != nil {
		return fmt.Errorf("provider snapshot input import is invalid")
	}
	if len(input.AcceptedCommit) != 40 || strings.ToLower(input.AcceptedCommit) != input.AcceptedCommit || input.Producer.Commit != input.AcceptedCommit {
		return fmt.Errorf("provider snapshot import provenance must identify the exact accepted commit")
	}
	if _, err := providerVersionMajor(input.ProviderVersion); err != nil {
		return fmt.Errorf("provider snapshot version is invalid")
	}
	return nil
}

func providerVersionMajor(version string) (uint64, error) {
	semanticVersion := "v" + version
	if !semver.IsValid(semanticVersion) || semver.Canonical(semanticVersion) != semanticVersion || semver.Prerelease(semanticVersion) != "" {
		return 0, fmt.Errorf("provider version must be an exact stable SemVer")
	}
	return strconv.ParseUint(strings.TrimPrefix(semver.Major(semanticVersion), "v"), 10, 64)
}
