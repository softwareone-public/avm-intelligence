package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
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
	ProviderAddress string                            `json:"provider_address"`
	ProviderVersion string                            `json:"provider_version"`
	SnapshotID      string                            `json:"snapshot_id"`
	AcceptedCommit  string                            `json:"accepted_commit"`
	FactsScope      []provider.AcceptedFactsReference `json:"facts_scope"`
	ImportedAt      time.Time                         `json:"imported_at"`
	Producer        projection.WorkflowProvenance     `json:"producer"`
}

var approvedProviderFactsScopeModules = map[string]struct{}{
	"avm-res-apimanagement-service":                {},
	"avm-res-authorization-roleassignment":         {},
	"avm-res-compute-capacityreservationgroup":     {},
	"avm-res-compute-disk":                         {},
	"avm-res-compute-virtualmachine":               {},
	"avm-res-compute-virtualmachinescaleset":       {},
	"avm-res-managedidentity-userassignedidentity": {},
	"avm-res-network-applicationgateway":           {},
	"avm-res-network-networkinterface":             {},
	"avm-res-network-virtualnetwork":               {},
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
	policy, err := provider.LoadCollectionPolicy("config/providers.yaml")
	if err != nil {
		return fmt.Errorf("load reviewed provider assessment policy: %w", err)
	}
	policyID, err := provider.AssessmentTrackPolicyID(policy)
	if err != nil {
		return fmt.Errorf("identify reviewed provider assessment policy: %w", err)
	}
	contextSets, err := deriveAssessmentContextSetBindings(acceptedDataRoot, snapshotState, input.FactsScope, policy, policyID)
	if err != nil {
		return fmt.Errorf("derive exact assessment context sets: %w", err)
	}
	policyTargets := assessmentContextSetTargets(contextSets)
	policyImport, err := state.ImportAssessmentInputs(context.Background(), setup.coordinator, state.AssessmentInputImport{
		Kind: state.InputAssessmentPolicy, InputKey: state.AssessmentPolicyInputKey, InputID: policyID,
		Targets: policyTargets, ContextSets: contextSets, AcceptedCommit: input.AcceptedCommit,
		Producer: input.Producer, ImportedAt: input.ImportedAt,
	})
	if err != nil {
		return fmt.Errorf("import exact provider assessment policy and context sets: %w", err)
	}
	slog.Info("provider assessment policy inputs imported", "policy_id", policyID,
		"targets", len(policyImport.Import.Targets), "context_sets", len(contextSets), "state_head", policyImport.Head, "changed", policyImport.Changed)
	targets := providerSnapshotTargetsForContextSets(contextSets, input.ProviderAddress, major)
	result, err := state.ImportAssessmentInputs(context.Background(), setup.coordinator, state.AssessmentInputImport{
		Kind: state.InputProviderSnapshot, InputKey: input.ProviderAddress, InputID: input.SnapshotID,
		Targets: targets, AcceptedCommit: input.AcceptedCommit, Producer: input.Producer, ImportedAt: input.ImportedAt,
	})
	if err != nil {
		return fmt.Errorf("import accepted provider snapshot generations: %w", err)
	}
	slog.Info("provider snapshot inputs imported", "provider", input.ProviderAddress, "version", input.ProviderVersion,
		"snapshot_id", input.SnapshotID, "targets", len(result.Import.Targets), "state_head", result.Head, "changed", result.Changed)
	return nil
}

func deriveAssessmentContextSetBindings(dataRoot string, snapshot state.Snapshot, scope []provider.AcceptedFactsReference, policy provider.CollectionPolicy, policyID string) ([]state.AssessmentContextSetBinding, error) {
	if len(scope) == 0 {
		return nil, fmt.Errorf("exact accepted facts scope is required")
	}
	ordered := append([]provider.AcceptedFactsReference(nil), scope...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].ModuleID < ordered[right].ModuleID })
	for index, reference := range ordered {
		if identity.ValidateRelativePath(reference.ModuleID, false) != nil || strings.Contains(reference.ModuleID, "/") || !lowerHexSHA(reference.SourceSHA, 40) || identity.ValidateRecordID("facts", reference.FactsID) != nil ||
			(index > 0 && ordered[index-1].ModuleID == reference.ModuleID) {
			return nil, fmt.Errorf("accepted facts scope contains an invalid or duplicate reference")
		}
	}
	receipts, err := state.AcceptedFactsContextSets(snapshot, "")
	if err != nil {
		return nil, fmt.Errorf("read operation-proven accepted facts receipts: %w", err)
	}
	bindings := make([]state.AssessmentContextSetBinding, 0, len(ordered))
	for _, reference := range ordered {
		var matched *state.EvidenceAcceptanceReceipt
		for index := range receipts {
			receipt := receipts[index]
			if receipt.ModuleID != reference.ModuleID || receipt.SourceSHA != reference.SourceSHA || receipt.FactsID != reference.FactsID {
				continue
			}
			if matched != nil {
				return nil, fmt.Errorf("accepted facts scope matches multiple receipts for %s", reference.ModuleID)
			}
			matched = &receipt
		}
		if matched == nil {
			return nil, fmt.Errorf("accepted facts scope has no exact facts acceptance receipt for %s", reference.ModuleID)
		}
		pinned, pinErr := state.CurrentSourcePinMatches(snapshot, matched.ModuleID, matched.AVMReleaseVersion, matched.SourceSHA)
		if pinErr != nil || !pinned {
			return nil, fmt.Errorf("accepted facts scope is not the current pinned release for %s", reference.ModuleID)
		}
		facts, factsErr := provider.LoadAcceptedModuleFacts(dataRoot, matched.ModuleID, matched.SourceSHA, matched.FactsID)
		if factsErr != nil {
			return nil, fmt.Errorf("load exact accepted facts for %s: %w", reference.ModuleID, factsErr)
		}
		set, deriveErr := provider.DeriveRequiredAssessmentContexts(facts, matched.AVMReleaseVersion, policy)
		if deriveErr != nil {
			return nil, fmt.Errorf("derive reviewed provider contexts for %s: %w", reference.ModuleID, deriveErr)
		}
		if set.PolicyID != policyID {
			return nil, fmt.Errorf("derived assessment context set does not match the reviewed policy")
		}
		bindings = append(bindings, state.AssessmentContextSetBinding{FactsAcceptanceID: matched.ID, Set: set})
	}
	return bindings, nil
}

func assessmentContextSetTargets(bindings []state.AssessmentContextSetBinding) []state.AssessmentInputTarget {
	targets := make([]state.AssessmentInputTarget, 0)
	for _, binding := range bindings {
		for _, required := range binding.Set.Contexts {
			if required.State.Applicability == "applicable" && required.State.Candidate == "eligible" {
				targets = append(targets, state.AssessmentInputTarget{ModuleID: binding.Set.ModuleID, AVMReleaseVersion: binding.Set.AVMReleaseVersion, ContextID: required.State.ContextID})
			}
		}
	}
	return sortAssessmentInputTargets(targets)
}

func providerSnapshotTargetsForContextSets(bindings []state.AssessmentContextSetBinding, providerAddress string, providerMajor uint64) []state.AssessmentInputTarget {
	targets := make([]state.AssessmentInputTarget, 0)
	for _, binding := range bindings {
		for _, required := range binding.Set.Contexts {
			if required.ProviderAddress == providerAddress && required.ProviderMajor == providerMajor &&
				required.State.Applicability == "applicable" && required.State.Candidate == "eligible" {
				targets = append(targets, state.AssessmentInputTarget{ModuleID: binding.Set.ModuleID, AVMReleaseVersion: binding.Set.AVMReleaseVersion, ContextID: required.State.ContextID})
			}
		}
	}
	return sortAssessmentInputTargets(targets)
}

func sortAssessmentInputTargets(targets []state.AssessmentInputTarget) []state.AssessmentInputTarget {
	sort.Slice(targets, func(left, right int) bool {
		leftKey := targets[left].ModuleID + "\x00" + targets[left].AVMReleaseVersion + "\x00" + targets[left].ContextID
		rightKey := targets[right].ModuleID + "\x00" + targets[right].AVMReleaseVersion + "\x00" + targets[right].ContextID
		return leftKey < rightKey
	})
	return targets
}

func validateProviderSnapshotInputImport(input providerSnapshotInputImport) error {
	if identity.ValidateRelativePath(input.ProviderAddress, false) != nil || len(strings.Split(input.ProviderAddress, "/")) != 3 || len(input.FactsScope) != 10 ||
		identity.ValidateRecordID("snapshot", input.SnapshotID) != nil || input.ImportedAt.IsZero() || input.Producer.Validate() != nil {
		return fmt.Errorf("provider snapshot input import is invalid")
	}
	seenModules := make(map[string]struct{}, len(input.FactsScope))
	for _, reference := range input.FactsScope {
		if _, approved := approvedProviderFactsScopeModules[reference.ModuleID]; !approved || !lowerHexSHA(reference.SourceSHA, 40) || identity.ValidateRecordID("facts", reference.FactsID) != nil {
			return fmt.Errorf("provider snapshot import facts scope is outside the approved ten-module pilot")
		}
		if _, duplicate := seenModules[reference.ModuleID]; duplicate {
			return fmt.Errorf("provider snapshot import facts scope repeats a module")
		}
		seenModules[reference.ModuleID] = struct{}{}
	}
	if len(seenModules) != len(approvedProviderFactsScopeModules) {
		return fmt.Errorf("provider snapshot import facts scope must contain the exact approved ten modules")
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
