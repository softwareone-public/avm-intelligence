// Package worker executes one leased source-acquisition or assessment request.
// It has no state backend and cannot write canonical records.
package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

const (
	ArtifactSchemaVersion = 1
	AnalysisSchemaVersion = 2
)

type Producer struct {
	ID              string `json:"id"`
	Version         string `json:"version"`
	ContractVersion int    `json:"contract_version"`
}

type ProviderTarget struct {
	Address    string `json:"address"`
	Version    string `json:"version"`
	SnapshotID string `json:"snapshot_id"`
}

type Analysis struct {
	SchemaVersion      int              `json:"schema_version"`
	RecordType         string           `json:"record_type"`
	AnalysisID         string           `json:"analysis_id"`
	ModuleID           string           `json:"module_id"`
	SourceSHA          string           `json:"source_sha"`
	FactsID            string           `json:"facts_id"`
	ContextID          string           `json:"context_id"`
	DimensionResultIDs []string         `json:"dimension_result_ids"`
	ProviderTargets    []ProviderTarget `json:"provider_targets"`
	Producer           Producer         `json:"producer"`
	InputFingerprint   string           `json:"input_fingerprint"`
	CreatedAt          time.Time        `json:"created_at"`
}

type Artifact struct {
	SchemaVersion    int                          `json:"schema_version"`
	RecordType       string                       `json:"record_type"`
	Phase            queue.WorkPhase              `json:"phase"`
	RequestID        string                       `json:"request_id"`
	ModuleID         string                       `json:"module_id"`
	SourceSHA        string                       `json:"source_sha"`
	InputFingerprint string                       `json:"input_fingerprint"`
	FactsID          string                       `json:"facts_id,omitempty"`
	Facts            *terraform.Facts             `json:"facts,omitempty"`
	DimensionResults []assessment.DimensionResult `json:"dimension_results,omitempty"`
	Analysis         *Analysis                    `json:"analysis,omitempty"`
	ArtifactDigest   string                       `json:"artifact_digest"`
}

type Input struct {
	Request           queue.Request                 `json:"request"`
	Owner             string                        `json:"owner"`
	Fence             uint64                        `json:"fence"`
	SourceRoot        string                        `json:"source_root,omitempty"`
	Facts             *terraform.Facts              `json:"facts,omitempty"`
	ProviderSnapshots []assessment.ProviderSnapshot `json:"provider_snapshots,omitempty"`
	Classifications   map[string]string             `json:"classifications,omitempty"`
	ObservedAt        time.Time                     `json:"observed_at"`
	Producer          Producer                      `json:"producer"`
}

type analysisIdentity struct {
	SchemaVersion      int              `json:"schema_version"`
	RecordType         string           `json:"record_type"`
	ModuleID           string           `json:"module_id"`
	SourceSHA          string           `json:"source_sha"`
	FactsID            string           `json:"facts_id"`
	ContextID          string           `json:"context_id"`
	DimensionResultIDs []string         `json:"dimension_result_ids"`
	ProviderTargets    []ProviderTarget `json:"provider_targets"`
	Producer           Producer         `json:"producer"`
	InputFingerprint   string           `json:"input_fingerprint"`
}

// Process performs exactly one claimed work phase and returns a sanitized
// module_evidence artifact without persisting state or advancing acceptance.
func Process(input Input) (Artifact, error) {
	if err := validateExecution(input); err != nil {
		return Artifact{}, err
	}
	started := time.Now()
	logger := slog.With("module_id", input.Request.ModuleID, "request_id", input.Request.ID, "phase", input.Request.Work.Phase)
	logger.Info("worker phase started")
	var artifact Artifact
	var err error
	switch input.Request.Work.Phase {
	case queue.PhaseAcquireSource:
		artifact, err = processAcquisition(input)
	case queue.PhaseAssess:
		artifact, err = processAssessment(input)
	default:
		return Artifact{}, fmt.Errorf("unsupported worker phase %q", input.Request.Work.Phase)
	}
	if err != nil {
		return Artifact{}, err
	}
	artifact.ArtifactDigest, err = artifactDigest(artifact)
	if err != nil {
		return Artifact{}, err
	}
	logger.Info("worker phase completed", "duration", time.Since(started), "facts_id", artifact.FactsID, "dimension_results", len(artifact.DimensionResults))
	return artifact, nil
}

func validateExecution(input Input) error {
	if err := input.Request.Validate(); err != nil {
		return fmt.Errorf("validate worker request: %w", err)
	}
	request := input.Request
	if request.Work == nil || request.Status != queue.StatusRunning || request.OwnerRunID != input.Owner || request.Work.Fence != input.Fence || request.Work.Lease == nil || request.Work.Lease.Owner != input.Owner || request.Work.Lease.Fence != input.Fence {
		return fmt.Errorf("worker request is not owned by the supplied run and fence")
	}
	if input.Owner == "" || input.Fence == 0 || input.ObservedAt.IsZero() || input.Producer.ID == "" || input.Producer.Version == "" || input.Producer.ContractVersion < 1 {
		return fmt.Errorf("worker owner, fence, observation time, and producer identity are required")
	}
	return nil
}

func processAcquisition(input Input) (Artifact, error) {
	if input.SourceRoot == "" || input.Facts != nil || len(input.ProviderSnapshots) > 0 {
		return Artifact{}, fmt.Errorf("source acquisition requires a source root and cannot receive prior facts or provider snapshots")
	}
	logger := slog.With("module_id", input.Request.ModuleID, "request_id", input.Request.ID)
	started := time.Now()
	logger.Info("source unit discovery started")
	units, err := terraform.DiscoverUnits(input.SourceRoot)
	if err != nil {
		return Artifact{}, err
	}
	lockFiles, err := terraform.DiscoverLockFiles(input.SourceRoot, units)
	if err != nil {
		return Artifact{}, err
	}
	logger.Info("source unit discovery completed", "units", len(units), "lock_files", len(lockFiles), "duration", time.Since(started))
	started = time.Now()
	logger.Info("source facts extraction started", "units", len(units))
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: input.SourceRoot, ModuleID: input.Request.ModuleID, SourceSHA: input.Request.SourceSHA,
		ObservedAt: input.ObservedAt, Units: units, LockFiles: lockFiles,
	})
	if err != nil {
		return Artifact{}, fmt.Errorf("extract source facts: %w", err)
	}
	logger.Info("source facts extraction completed", "facts_id", facts.FactsID, "units", len(facts.Units), "duration", time.Since(started))
	artifact := artifactFor(input, queue.PhaseAcquireSource)
	artifact.FactsID = facts.FactsID
	artifact.Facts = &facts
	return artifact, nil
}

func processAssessment(input Input) (Artifact, error) {
	request := input.Request
	if input.SourceRoot != "" || input.Facts == nil {
		return Artifact{}, fmt.Errorf("assessment requires accepted facts and must not check out source")
	}
	facts := *input.Facts
	if err := facts.Validate(); err != nil {
		return Artifact{}, fmt.Errorf("validate accepted facts: %w", err)
	}
	if facts.ModuleID != request.ModuleID || facts.SourceSHA != request.SourceSHA || facts.FactsID != request.Work.FactsID {
		return Artifact{}, fmt.Errorf("accepted facts do not match the assessment work contract")
	}
	if err := validateProviderTargetVector(request, input.ProviderSnapshots); err != nil {
		return Artifact{}, err
	}
	registry, err := assessment.DefaultRegistry()
	if err != nil {
		return Artifact{}, err
	}
	dimensionIDs, err := selectedDimensionIDs(registry, request.Work.DefinitionIDs)
	if err != nil {
		return Artifact{}, err
	}
	results, err := assessment.RunDimensions(registry, assessment.RunInput{
		Facts: facts, Snapshots: input.ProviderSnapshots, DimensionIDs: dimensionIDs,
		TargetVersions: request.Work.TargetVersions, Classifications: input.Classifications,
	})
	if err != nil {
		return Artifact{}, fmt.Errorf("run offline assessment: %w", err)
	}
	analysis, err := buildAnalysis(input, results)
	if err != nil {
		return Artifact{}, err
	}
	artifact := artifactFor(input, queue.PhaseAssess)
	artifact.FactsID = facts.FactsID
	artifact.DimensionResults = results
	artifact.Analysis = &analysis
	return artifact, nil
}

// ValidateProviderTargetVector checks exact snapshot membership and provider versions.
func ValidateProviderTargetVector(request queue.Request, snapshots []assessment.ProviderSnapshot) error {
	return validateProviderTargetVector(request, snapshots)
}

func validateProviderTargetVector(request queue.Request, snapshots []assessment.ProviderSnapshot) error {
	work := request.Work
	if len(snapshots) != len(work.SnapshotIDs) || len(snapshots) != len(work.TargetVersions) {
		return fmt.Errorf("assessment snapshots do not match the exact pinned provider target vector")
	}
	expectedIDs := make(map[string]struct{}, len(work.SnapshotIDs))
	for _, snapshotID := range work.SnapshotIDs {
		expectedIDs[snapshotID] = struct{}{}
	}
	seenAddresses := map[string]struct{}{}
	for _, snapshot := range snapshots {
		if _, exists := expectedIDs[snapshot.SnapshotID]; !exists || work.TargetVersions[snapshot.ProviderAddress] != snapshot.ProviderVersion {
			return fmt.Errorf("provider snapshot does not match the assessment target vector")
		}
		if _, duplicate := seenAddresses[snapshot.ProviderAddress]; duplicate {
			return fmt.Errorf("assessment target vector repeats provider %s", snapshot.ProviderAddress)
		}
		seenAddresses[snapshot.ProviderAddress] = struct{}{}
		delete(expectedIDs, snapshot.SnapshotID)
	}
	if len(expectedIDs) > 0 {
		return fmt.Errorf("assessment is missing one or more pinned provider snapshots")
	}
	return nil
}

func selectedDimensionIDs(registry assessment.Registry, definitionIDs []string) ([]string, error) {
	requested := make(map[string]struct{}, len(definitionIDs))
	for _, definitionID := range definitionIDs {
		requested[definitionID] = struct{}{}
	}
	dimensions := make([]string, 0, len(requested))
	for _, definition := range registry.ListDimensions() {
		if _, exists := requested[definition.DefinitionID]; exists {
			dimensions = append(dimensions, definition.DimensionID)
			delete(requested, definition.DefinitionID)
		}
	}
	if len(requested) > 0 || len(dimensions) == 0 {
		return nil, fmt.Errorf("work contract contains unknown or empty dimension definition IDs")
	}
	sort.Strings(dimensions)
	return dimensions, nil
}

func buildAnalysis(input Input, results []assessment.DimensionResult) (Analysis, error) {
	providerTargets := make([]ProviderTarget, 0, len(input.ProviderSnapshots))
	for _, snapshot := range input.ProviderSnapshots {
		providerTargets = append(providerTargets, ProviderTarget{Address: snapshot.ProviderAddress, Version: snapshot.ProviderVersion, SnapshotID: snapshot.SnapshotID})
	}
	sort.Slice(providerTargets, func(left, right int) bool { return providerTargets[left].Address < providerTargets[right].Address })
	resultIDs := make([]string, 0, len(results))
	for _, result := range results {
		resultIDs = append(resultIDs, result.DimensionResultID)
	}
	sort.Strings(resultIDs)
	value := analysisIdentity{
		SchemaVersion: AnalysisSchemaVersion, RecordType: "analysis", ModuleID: input.Request.ModuleID,
		SourceSHA: input.Request.SourceSHA, FactsID: input.Request.Work.FactsID,
		ContextID: input.Request.Work.ContextID, DimensionResultIDs: resultIDs,
		ProviderTargets: providerTargets, Producer: input.Producer,
		InputFingerprint: input.Request.Fingerprint,
	}
	analysisID, err := identity.ID("analysis", AnalysisSchemaVersion, value)
	if err != nil {
		return Analysis{}, fmt.Errorf("identify assessment analysis: %w", err)
	}
	return Analysis{
		SchemaVersion: value.SchemaVersion, RecordType: value.RecordType, AnalysisID: analysisID,
		ModuleID: value.ModuleID, SourceSHA: value.SourceSHA, FactsID: value.FactsID,
		ContextID: value.ContextID, DimensionResultIDs: resultIDs, ProviderTargets: providerTargets,
		Producer: value.Producer, InputFingerprint: value.InputFingerprint, CreatedAt: input.ObservedAt.UTC(),
	}, nil
}

func artifactFor(input Input, phase queue.WorkPhase) Artifact {
	return Artifact{
		SchemaVersion: ArtifactSchemaVersion, RecordType: "module_evidence", Phase: phase,
		RequestID: input.Request.ID, ModuleID: input.Request.ModuleID,
		SourceSHA: input.Request.SourceSHA, InputFingerprint: input.Request.Fingerprint,
		DimensionResults: []assessment.DimensionResult{},
	}
}

// Seal uses the trusted validator's copy of the claimed request and artifact.
// The compute worker can create artifacts but cannot persist or seal state.
func Seal(request queue.Request, owner string, fence uint64, artifact Artifact, validationID string, sealedAt time.Time) (queue.Request, error) {
	if err := validateArtifact(request, artifact); err != nil {
		return queue.Request{}, err
	}
	if validationID == "" || sealedAt.IsZero() {
		return queue.Request{}, fmt.Errorf("trusted validation identity and seal time are required")
	}
	digest, err := artifactDigest(artifact)
	if err != nil {
		return queue.Request{}, err
	}
	if artifact.ArtifactDigest != digest {
		return queue.Request{}, fmt.Errorf("module evidence artifact digest does not match its content")
	}
	completionID, err := identity.ID("completion", 1, struct {
		RequestID      string `json:"request_id"`
		ArtifactDigest string `json:"artifact_digest"`
		Fence          uint64 `json:"fence"`
	}{request.ID, digest, fence})
	if err != nil {
		return queue.Request{}, err
	}
	return queue.SealCompletion(request, owner, fence, queue.SealedCompletion{
		ID: completionID, RequestID: request.ID, InputFingerprint: request.Fingerprint,
		DesiredGeneration: request.Work.DesiredGeneration, Fence: fence,
		ArtifactDigest: digest, ValidationID: validationID, SealedAt: sealedAt,
	})
}

func validateArtifact(request queue.Request, artifact Artifact) error {
	if request.Work == nil || artifact.SchemaVersion != ArtifactSchemaVersion || artifact.RecordType != "module_evidence" || artifact.Phase != request.Work.Phase || artifact.RequestID != request.ID || artifact.ModuleID != request.ModuleID || artifact.SourceSHA != request.SourceSHA || artifact.InputFingerprint != request.Fingerprint {
		return fmt.Errorf("module evidence artifact does not match its claimed request")
	}
	switch request.Work.Phase {
	case queue.PhaseAcquireSource:
		if artifact.Facts == nil || artifact.Analysis != nil || len(artifact.DimensionResults) != 0 || artifact.Facts.FactsID != artifact.FactsID || artifact.Facts.ModuleID != request.ModuleID || artifact.Facts.SourceSHA != request.SourceSHA {
			return fmt.Errorf("acquisition artifact must contain matching facts only")
		}
		return artifact.Facts.Validate()
	case queue.PhaseAssess:
		if artifact.Facts != nil || artifact.Analysis == nil || artifact.FactsID != request.Work.FactsID || artifact.Analysis.FactsID != request.Work.FactsID || artifact.Analysis.ModuleID != request.ModuleID || artifact.Analysis.SourceSHA != request.SourceSHA || artifact.Analysis.ContextID != request.Work.ContextID || len(artifact.DimensionResults) == 0 {
			return fmt.Errorf("assessment artifact must reference accepted facts and contain results")
		}
		return validateAnalysisArtifact(request, artifact)
	default:
		return fmt.Errorf("unsupported artifact phase %q", request.Work.Phase)
	}
}

func validateAnalysisArtifact(request queue.Request, artifact Artifact) error {
	analysis := artifact.Analysis
	if err := validateAnalysisMetadata(request, analysis); err != nil {
		return err
	}
	if err := validateAnalysisTargets(request, analysis.ProviderTargets); err != nil {
		return err
	}
	if err := validateAnalysisResults(request, artifact.DimensionResults, analysis); err != nil {
		return err
	}
	return validateAnalysisIdentity(analysis)
}

func validateAnalysisMetadata(request queue.Request, analysis *Analysis) error {
	if analysis == nil || analysis.SchemaVersion != AnalysisSchemaVersion || analysis.RecordType != "analysis" || analysis.InputFingerprint != request.Fingerprint || analysis.CreatedAt.IsZero() || analysis.Producer.ID == "" || analysis.Producer.Version == "" || analysis.Producer.ContractVersion < 1 {
		return fmt.Errorf("assessment artifact analysis metadata is invalid")
	}
	return nil
}

func validateAnalysisResults(request queue.Request, results []assessment.DimensionResult, analysis *Analysis) error {
	resultIDs := make([]string, 0, len(results))
	for _, result := range results {
		if err := result.Validate(); err != nil {
			return err
		}
		if err := identity.ValidateRecordID("dimension-result", result.DimensionResultID); err != nil {
			return err
		}
		if result.SchemaVersion != 2 || result.RecordType != "dimension_result" || result.ModuleID != request.ModuleID || result.SourceSHA != request.SourceSHA || result.FactsID != request.Work.FactsID || !validWorkDefinition(request.Work.DefinitionIDs, result.DefinitionID) || !isLowerSHA256(result.InputFingerprint) {
			return fmt.Errorf("assessment artifact contains a result outside its work contract")
		}
		if err := identity.ValidateRelativePath(result.UnitPath, true); err != nil {
			return fmt.Errorf("assessment artifact contains an invalid unit path: %w", err)
		}
		if !containsAllWorkSnapshots(request.Work.SnapshotIDs, result.SnapshotIDs) {
			return fmt.Errorf("assessment result uses a provider snapshot outside its work contract")
		}
		resultIDs = append(resultIDs, result.DimensionResultID)
	}
	resultIDs, err := identity.SortedSet(resultIDs)
	if err != nil {
		return fmt.Errorf("assessment artifact repeats a dimension result: %w", err)
	}
	analysisIDs, err := identity.SortedSet(analysis.DimensionResultIDs)
	if err != nil || !equalStrings(resultIDs, analysisIDs) {
		return fmt.Errorf("assessment analysis references do not match the emitted dimension results")
	}
	return nil
}

// Validate verifies an immutable analysis content identity without queue ownership.
func (analysis Analysis) Validate() error {
	if analysis.SchemaVersion != AnalysisSchemaVersion || analysis.RecordType != "analysis" || analysis.CreatedAt.IsZero() || analysis.Producer.ID == "" || analysis.Producer.Version == "" || analysis.Producer.ContractVersion < 1 {
		return fmt.Errorf("analysis metadata is invalid")
	}
	return validateAnalysisIdentity(&analysis)
}

func validateAnalysisIdentity(analysis *Analysis) error {
	resultIDs, err := identity.SortedSet(analysis.DimensionResultIDs)
	if err != nil {
		return fmt.Errorf("assessment analysis contains duplicate dimension results: %w", err)
	}
	content := analysisIdentity{
		SchemaVersion: analysis.SchemaVersion, RecordType: analysis.RecordType, ModuleID: analysis.ModuleID,
		SourceSHA: analysis.SourceSHA, FactsID: analysis.FactsID, ContextID: analysis.ContextID,
		DimensionResultIDs: resultIDs, ProviderTargets: analysis.ProviderTargets,
		Producer: analysis.Producer, InputFingerprint: analysis.InputFingerprint,
	}
	expectedID, err := identity.ID("analysis", AnalysisSchemaVersion, content)
	if err != nil {
		return err
	}
	if analysis.AnalysisID != expectedID {
		return fmt.Errorf("assessment analysis ID does not match its content")
	}
	return nil
}

func validateAnalysisTargets(request queue.Request, targets []ProviderTarget) error {
	work := request.Work
	if len(targets) != len(work.SnapshotIDs) || len(targets) != len(work.TargetVersions) {
		return fmt.Errorf("analysis provider targets do not match the exact work vector")
	}
	seenSnapshots := map[string]struct{}{}
	seenAddresses := map[string]struct{}{}
	for _, target := range targets {
		if work.TargetVersions[target.Address] != target.Version || !containsAllWorkSnapshots(work.SnapshotIDs, []string{target.SnapshotID}) {
			return fmt.Errorf("analysis provider target differs from the work contract")
		}
		if _, duplicate := seenSnapshots[target.SnapshotID]; duplicate {
			return fmt.Errorf("analysis repeats provider snapshot %s", target.SnapshotID)
		}
		if _, duplicate := seenAddresses[target.Address]; duplicate {
			return fmt.Errorf("analysis repeats provider address %s", target.Address)
		}
		seenSnapshots[target.SnapshotID] = struct{}{}
		seenAddresses[target.Address] = struct{}{}
	}
	return nil
}

func validWorkDefinition(definitionIDs []string, definitionID string) bool {
	for _, id := range definitionIDs {
		if id == definitionID {
			return true
		}
	}
	return false
}

func containsAllWorkSnapshots(available, requested []string) bool {
	availableSet := make(map[string]struct{}, len(available))
	for _, id := range available {
		availableSet[id] = struct{}{}
	}
	for _, id := range requested {
		if _, exists := availableSet[id]; !exists {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
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

func isLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func artifactDigest(artifact Artifact) (string, error) {
	artifact.ArtifactDigest = ""
	encoded, err := json.Marshal(artifact)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
