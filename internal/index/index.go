// Package index builds complete deterministic projections from accepted records.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

var repositoryIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type ModuleRecord struct {
	ModuleID   string `json:"module_id"`
	Repository string `json:"repository"`
	Lifecycle  string `json:"lifecycle"`
}

type SourceRevisionRecord struct {
	ModuleID   string `json:"module_id"`
	Repository string `json:"repository"`
	SourceSHA  string `json:"source_sha"`
	Immutable  bool   `json:"immutable"`
}

type FactRecord struct {
	ID                      string                  `json:"facts_id"`
	ModuleID                string                  `json:"module_id"`
	SourceSHA               string                  `json:"source_sha"`
	ContentDigest           string                  `json:"content_digest"`
	ProviderTypeUsages      []ProviderTypeUsage     `json:"-"`
	ProviderUsageCoverage   []ProviderUsageCoverage `json:"-"`
	ProviderRequirements    []ProviderRequirement   `json:"-"`
	FactsCoverage           map[string]string       `json:"-"`
	FactsCoverageComplete   bool                    `json:"-"`
	FactsCoverageObservedAt string                  `json:"-"`
}

type ProviderRequirement struct {
	ModuleID        string `json:"module_id"`
	FactsID         string `json:"facts_id"`
	UnitPath        string `json:"unit_path"`
	ProviderAddress string `json:"provider_address,omitempty"`
	LocalName       string `json:"local_name"`
	Constraint      string `json:"constraint,omitempty"`
}

type ProvenanceRecord struct {
	ID               string   `json:"provenance_id"`
	ModuleID         string   `json:"module_id"`
	SourceSHA        string   `json:"source_sha"`
	FactsID          string   `json:"facts_id"`
	InputFingerprint string   `json:"input_fingerprint"`
	Producer         Producer `json:"producer"`
	Capabilities     []string `json:"capabilities"`
}

type Producer struct {
	ID              string `json:"id"`
	Version         string `json:"version"`
	ContractVersion int    `json:"contract_version"`
}

type VersionObservationRecord struct {
	ID         string    `json:"observation_id"`
	ModuleID   string    `json:"module_id"`
	Generation uint64    `json:"generation"`
	Repository string    `json:"repository"`
	Version    string    `json:"version"`
	Tags       []string  `json:"tags"`
	SourceSHA  string    `json:"source_sha"`
	ObservedAt time.Time `json:"observed_at"`
}

type VersionPointer struct {
	Version       string `json:"version"`
	Tag           string `json:"tag"`
	SourceSHA     string `json:"source_sha"`
	ObservationID string `json:"observation_id"`
}

type ModuleVersionsIndexRecord struct {
	ModuleID       string          `json:"module_id"`
	Generation     uint64          `json:"generation"`
	ObservationIDs []string        `json:"observation_ids"`
	OverallLatest  *VersionPointer `json:"overall_latest"`
}

type ProviderSnapshotRecord struct {
	ID      string `json:"snapshot_id"`
	Address string `json:"provider_address"`
	Version string `json:"provider_version"`
}

type PresentationSnapshotRecord struct {
	ID                  string    `json:"presentation_id"`
	Generation          uint64    `json:"generation"`
	AsOf                time.Time `json:"as_of"`
	SelectionReceiptIDs []string  `json:"selection_receipt_ids"`
}

type ProviderTarget struct {
	Address    string `json:"address"`
	Version    string `json:"version"`
	SnapshotID string `json:"snapshot_id"`
}

type AnalysisRecord struct {
	ID                 string           `json:"analysis_id"`
	ModuleID           string           `json:"module_id"`
	SourceSHA          string           `json:"source_sha"`
	FactsID            string           `json:"facts_id"`
	ContextID          string           `json:"context_id"`
	DimensionResultIDs []string         `json:"dimension_result_ids"`
	ProviderTargets    []ProviderTarget `json:"provider_targets,omitempty"`
	InputFingerprint   string           `json:"input_fingerprint"`
}

type SelectionReceipt struct {
	ID                string    `json:"selection_id"`
	ModuleID          string    `json:"module_id"`
	ContextID         string    `json:"context_id"`
	Sequence          uint64    `json:"sequence"`
	DesiredGeneration uint64    `json:"desired_generation"`
	SourceSHA         *string   `json:"source_sha"`
	FactsID           *string   `json:"facts_id"`
	InputFingerprint  string    `json:"input_fingerprint"`
	AnalysisID        *string   `json:"analysis_id"`
	EvidenceCommit    string    `json:"evidence_commit"`
	SelectedAt        time.Time `json:"selected_at"`
}

type Records struct {
	CatalogGeneration   uint64                           `json:"catalog_generation"`
	CreatedAt           time.Time                        `json:"created_at"`
	Generator           Producer                         `json:"generator"`
	RecordDigests       map[string]string                `json:"record_digests"`
	Modules             []ModuleRecord                   `json:"modules"`
	SourceRevisions     []SourceRevisionRecord           `json:"source_revisions"`
	VersionObservations []VersionObservationRecord       `json:"version_observations"`
	VersionIndexes      []ModuleVersionsIndexRecord      `json:"version_indexes"`
	Facts               []FactRecord                     `json:"facts"`
	Provenance          []ProvenanceRecord               `json:"provenance"`
	Definitions         []assessment.DimensionDefinition `json:"definitions"`
	DimensionResults    []assessment.DimensionResult     `json:"dimension_results"`
	Analyses            []AnalysisRecord                 `json:"analyses"`
	ProviderSnapshots   []ProviderSnapshotRecord         `json:"provider_snapshots"`
	Selections          []SelectionReceipt               `json:"selections"`
	Presentations       []PresentationSnapshotRecord     `json:"presentations"`
}

type CatalogIndex struct {
	IndexID           string               `json:"index_id"`
	SchemaVersion     int                  `json:"schema_version"`
	RecordType        string               `json:"record_type"`
	CatalogGeneration uint64               `json:"catalog_generation"`
	Modules           []CatalogModuleEntry `json:"modules"`
}

type CatalogModuleEntry struct {
	ModuleID   string `json:"module_id"`
	Repository string `json:"repository"`
	Lifecycle  string `json:"lifecycle"`
}

type ModuleIndex struct {
	IndexID               string                     `json:"index_id"`
	SchemaVersion         int                        `json:"schema_version"`
	RecordType            string                     `json:"record_type"`
	ModuleID              string                     `json:"module_id"`
	Repository            string                     `json:"repository"`
	Lifecycle             string                     `json:"lifecycle"`
	SourceSHAs            []string                   `json:"source_shas"`
	VersionObservationIDs []string                   `json:"version_observation_ids"`
	VersionIndex          *ModuleVersionsIndexRecord `json:"version_index"`
	FactsIDs              []string                   `json:"facts_ids"`
	ProvenanceIDs         []string                   `json:"provenance_ids"`
	DefinitionIDs         []string                   `json:"definition_ids"`
	DimensionResultIDs    []string                   `json:"dimension_result_ids"`
	AnalysisIDs           []string                   `json:"analysis_ids"`
	SelectionIDs          []string                   `json:"selection_ids"`
	SelectionHistory      []SelectionPointer         `json:"selection_history"`
	CurrentSelections     []SelectionPointer         `json:"current_selections"`
}

type SelectionPointer struct {
	ModuleID          string           `json:"module_id"`
	ContextID         string           `json:"context_id"`
	Sequence          uint64           `json:"sequence"`
	DesiredGeneration uint64           `json:"desired_generation"`
	SelectionID       string           `json:"selection_id"`
	AnalysisID        *string          `json:"analysis_id"`
	SourceSHA         *string          `json:"source_sha"`
	FactsID           *string          `json:"facts_id"`
	InputFingerprint  string           `json:"input_fingerprint"`
	SelectedAt        time.Time        `json:"selected_at"`
	ProviderTargets   []ProviderTarget `json:"provider_targets"`
}

type TrackIndex struct {
	IndexID       string              `json:"index_id"`
	SchemaVersion int                 `json:"schema_version"`
	RecordType    string              `json:"record_type"`
	ContextID     string              `json:"context_id"`
	ModuleIDs     []string            `json:"module_ids"`
	Selections    []SelectionPointer  `json:"selections"`
	TargetVectors []TrackTargetVector `json:"target_vectors"`
}

type TrackTargetVector struct {
	ModuleID        string           `json:"module_id"`
	SelectionID     string           `json:"selection_id"`
	Sequence        uint64           `json:"sequence"`
	AnalysisID      *string          `json:"analysis_id"`
	ProviderTargets []ProviderTarget `json:"provider_targets"`
}

type ProviderIndex struct {
	IndexID         string                   `json:"index_id"`
	SchemaVersion   int                      `json:"schema_version"`
	RecordType      string                   `json:"record_type"`
	ProviderAddress string                   `json:"provider_address"`
	Snapshots       []ProviderSnapshotRecord `json:"snapshots"`
}

type DimensionDependencyIndex struct {
	IndexID               string                  `json:"index_id"`
	SchemaVersion         int                     `json:"schema_version"`
	RecordType            string                  `json:"record_type"`
	Definitions           []DimensionDependency   `json:"definitions"`
	ProviderTypeUsages    []ProviderTypeUsage     `json:"provider_type_usages"`
	ProviderUsageCoverage []ProviderUsageCoverage `json:"provider_usage_coverage"`
}

type ProviderTypeUsage struct {
	ModuleID        string `json:"module_id"`
	FactsID         string `json:"facts_id"`
	UnitPath        string `json:"unit_path"`
	ProviderAddress string `json:"provider_address,omitempty"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
}

type ProviderUsageCoverage struct {
	ModuleID          string            `json:"module_id"`
	FactsID           string            `json:"facts_id"`
	UnitPath          string            `json:"unit_path"`
	ProviderAddresses []string          `json:"provider_addresses"`
	Capabilities      map[string]string `json:"capabilities"`
}

type DimensionDependency struct {
	DefinitionID         string   `json:"definition_id"`
	DimensionID          string   `json:"dimension_id"`
	Version              int      `json:"version"`
	CheckIDs             []string `json:"check_ids"`
	RequiredCapabilities []string `json:"required_capabilities"`
	FactsIDs             []string `json:"facts_ids"`
	ProviderSnapshotIDs  []string `json:"provider_snapshot_ids"`
	DimensionResultIDs   []string `json:"dimension_result_ids"`
}

type PresentationIndex struct {
	IndexID       string                       `json:"index_id"`
	SchemaVersion int                          `json:"schema_version"`
	RecordType    string                       `json:"record_type"`
	Snapshots     []PresentationSnapshotRecord `json:"snapshots"`
	CurrentID     *string                      `json:"current_id"`
}

type ModuleMembership struct {
	ModuleID    string  `json:"module_id"`
	ContextID   string  `json:"context_id"`
	AnalysisID  *string `json:"analysis_id"`
	SelectionID *string `json:"selection_id"`
}

type DatasetManifest struct {
	SchemaVersion     int                `json:"schema_version"`
	RecordType        string             `json:"record_type"`
	DatasetID         string             `json:"dataset_id"`
	Generator         Producer           `json:"generator"`
	CatalogGeneration uint64             `json:"catalog_generation"`
	RecordDigests     map[string]string  `json:"record_digests"`
	ModuleMembership  []ModuleMembership `json:"module_membership"`
	CreatedAt         time.Time          `json:"created_at"`
}

type Indexes struct {
	SchemaVersion         int                      `json:"schema_version"`
	CatalogGeneration     uint64                   `json:"catalog_generation"`
	Catalog               CatalogIndex             `json:"catalog"`
	Dataset               DatasetManifest          `json:"dataset"`
	Modules               []ModuleIndex            `json:"modules"`
	Tracks                []TrackIndex             `json:"tracks"`
	Providers             []ProviderIndex          `json:"providers"`
	DimensionDependencies DimensionDependencyIndex `json:"dimension_dependencies"`
	Presentation          PresentationIndex        `json:"presentation"`
}

type moduleIdentity struct {
	module              ModuleRecord
	shas                map[string]struct{}
	versionObservations map[string]struct{}
	facts               map[string]struct{}
	provenance          map[string]struct{}
	definitions         map[string]struct{}
	dimensions          map[string]struct{}
	analyses            map[string]struct{}
	selections          []SelectionPointer
	selectionByContext  map[string]SelectionPointer
}

type validatedIndexRecords struct {
	modules             map[string]*moduleIdentity
	revisions           map[string]SourceRevisionRecord
	versionObservations map[string]VersionObservationRecord
	versionIndexes      map[string]ModuleVersionsIndexRecord
	facts               map[string]FactRecord
	provenance          map[string]ProvenanceRecord
	definitions         map[string]assessment.DimensionDefinition
	dimensions          map[string]assessment.DimensionResult
	analyses            map[string]AnalysisRecord
	providerSnapshots   map[string]ProviderSnapshotRecord
}

// Build validates all cross-record references and creates stable index views.
// Only explicit selection receipts determine the current analysis per context.
func Build(records Records) (Indexes, error) {
	if records.CatalogGeneration == 0 || len(records.Modules) == 0 || records.CreatedAt.IsZero() || records.Generator.ID == "" || records.Generator.Version == "" || records.Generator.ContractVersion < 1 {
		return Indexes{}, fmt.Errorf("catalog generation, module records, creation time, and generator are required")
	}
	if err := validateRecordDigestCoverage(records); err != nil {
		return Indexes{}, err
	}
	modules, err := validateModules(records.Modules)
	if err != nil {
		return Indexes{}, err
	}
	revisions, err := validateSourceRevisions(records.SourceRevisions, modules)
	if err != nil {
		return Indexes{}, err
	}
	versionObservations, versionIndexes, err := validateVersionRecords(records.VersionObservations, records.VersionIndexes, modules, revisions)
	if err != nil {
		return Indexes{}, err
	}
	facts, err := validateFacts(records.Facts, revisions)
	if err != nil {
		return Indexes{}, err
	}
	provenance, err := validateProvenance(records.Provenance, facts)
	if err != nil {
		return Indexes{}, err
	}
	providerSnapshots, err := validateProviderSnapshots(records.ProviderSnapshots)
	if err != nil {
		return Indexes{}, err
	}
	definitions, err := validateDefinitions(records.Definitions)
	if err != nil {
		return Indexes{}, err
	}
	dimensions, err := validateDimensions(records.DimensionResults, facts, definitions, providerSnapshots)
	if err != nil {
		return Indexes{}, err
	}
	analyses, err := validateAnalyses(records.Analyses, facts, dimensions, providerSnapshots)
	if err != nil {
		return Indexes{}, err
	}
	selections, err := validateSelections(records.Selections, modules, facts, analyses)
	if err != nil {
		return Indexes{}, err
	}
	if err = validateDatasetAsOf(records.CreatedAt, selections); err != nil {
		return Indexes{}, err
	}
	presentations, err := validatePresentations(records.Presentations, selections)
	if err != nil {
		return Indexes{}, err
	}
	for _, presentation := range presentations {
		if presentation.AsOf.After(records.CreatedAt) {
			return Indexes{}, fmt.Errorf("presentation %s is newer than dataset creation time", presentation.ID)
		}
	}
	validated := validatedIndexRecords{
		modules: modules, revisions: revisions, versionObservations: versionObservations, versionIndexes: versionIndexes,
		facts: facts, provenance: provenance, definitions: definitions, dimensions: dimensions,
		analyses: analyses, providerSnapshots: providerSnapshots,
	}
	indexes := buildBaseIndexes(records.CatalogGeneration, validated, selections, presentations)
	if err := buildDatasetManifest(records, &indexes); err != nil {
		return Indexes{}, err
	}
	return indexes, nil
}

func validateRecordDigestCoverage(records Records) error {
	paths, err := requiredRecordPaths(records)
	if err != nil {
		return err
	}
	for _, recordPath := range paths {
		digest, exists := records.RecordDigests[recordPath]
		if !exists || !sha256Pattern.MatchString(digest) {
			return fmt.Errorf("accepted record digest is missing or invalid for %s", recordPath)
		}
	}
	return nil
}

func requiredRecordPaths(records Records) ([]string, error) {
	builders := []func() ([]string, error){
		func() ([]string, error) {
			return appendRecordPaths(nil, records.Modules, func(record ModuleRecord) (string, error) { return identity.ModuleMetadataPath(record.ModuleID) })
		},
		func() ([]string, error) {
			return appendRecordPaths(nil, records.SourceRevisions, func(record SourceRevisionRecord) (string, error) {
				return identity.ModuleSourcePath(record.ModuleID, record.SourceSHA)
			})
		},
		func() ([]string, error) {
			return appendRecordPaths(nil, records.VersionObservations, func(record VersionObservationRecord) (string, error) {
				return identity.ModuleVersionObservationPath(record.ModuleID, record.ID)
			})
		},
		func() ([]string, error) {
			return appendRecordPaths(nil, records.VersionIndexes, func(record ModuleVersionsIndexRecord) (string, error) {
				return identity.ModuleVersionsPath(record.ModuleID)
			})
		},
		func() ([]string, error) {
			return appendRecordPaths(nil, records.Facts, func(record FactRecord) (string, error) {
				return identity.ModuleRecordPath(record.ModuleID, record.SourceSHA, "facts", record.ID)
			})
		},
		func() ([]string, error) {
			return appendRecordPaths(nil, records.Provenance, func(record ProvenanceRecord) (string, error) {
				return identity.ModuleRecordPath(record.ModuleID, record.SourceSHA, "provenance", record.ID)
			})
		},
		func() ([]string, error) {
			return appendRecordPaths(nil, records.Definitions, func(record assessment.DimensionDefinition) (string, error) {
				return identity.DefinitionPath(record.DefinitionID)
			})
		},
		func() ([]string, error) {
			return appendRecordPaths(nil, records.DimensionResults, func(record assessment.DimensionResult) (string, error) {
				return identity.ModuleRecordPath(record.ModuleID, record.SourceSHA, "dimension-result", record.DimensionResultID)
			})
		},
		func() ([]string, error) {
			return appendRecordPaths(nil, records.Analyses, func(record AnalysisRecord) (string, error) {
				return identity.ModuleRecordPath(record.ModuleID, record.SourceSHA, "analysis", record.ID)
			})
		},
		func() ([]string, error) {
			return appendRecordPaths(nil, records.ProviderSnapshots, func(record ProviderSnapshotRecord) (string, error) {
				return identity.ProviderSnapshotManifestPath(record.Address, record.Version, record.ID)
			})
		},
		func() ([]string, error) {
			return appendRecordPaths(nil, records.Selections, func(record SelectionReceipt) (string, error) {
				return identity.SelectionPath(record.ModuleID, record.ContextID, record.ID)
			})
		},
		func() ([]string, error) {
			return appendRecordPaths(nil, records.Presentations, func(record PresentationSnapshotRecord) (string, error) { return identity.PresentationPath(record.ID) })
		},
	}
	paths := []string{}
	for _, build := range builders {
		group, err := build()
		if err != nil {
			return nil, err
		}
		paths = append(paths, group...)
	}
	return paths, nil
}

func appendRecordPaths[T any](paths []string, records []T, pathFor func(T) (string, error)) ([]string, error) {
	for _, record := range records {
		recordPath, err := pathFor(record)
		if err != nil {
			return nil, err
		}
		paths = append(paths, recordPath)
	}
	return paths, nil
}

func validateModules(records []ModuleRecord) (map[string]*moduleIdentity, error) {
	modules := make(map[string]*moduleIdentity, len(records))
	for _, module := range records {
		if _, err := identity.ModuleMetadataPath(module.ModuleID); err != nil {
			return nil, err
		}
		if !repositoryIdentityPattern.MatchString(module.Repository) {
			return nil, fmt.Errorf("module %s has invalid repository identity", module.ModuleID)
		}
		if module.Lifecycle != "active" && module.Lifecycle != "blocked" && module.Lifecycle != "retired" {
			return nil, fmt.Errorf("module %s has invalid lifecycle %q", module.ModuleID, module.Lifecycle)
		}
		if _, exists := modules[module.ModuleID]; exists {
			return nil, fmt.Errorf("duplicate module %s", module.ModuleID)
		}
		modules[module.ModuleID] = &moduleIdentity{
			module: module, shas: map[string]struct{}{}, versionObservations: map[string]struct{}{}, facts: map[string]struct{}{}, provenance: map[string]struct{}{},
			definitions: map[string]struct{}{}, dimensions: map[string]struct{}{}, analyses: map[string]struct{}{},
			selectionByContext: map[string]SelectionPointer{},
		}
	}
	return modules, nil
}

func validateSourceRevisions(records []SourceRevisionRecord, modules map[string]*moduleIdentity) (map[string]SourceRevisionRecord, error) {
	revisions := make(map[string]SourceRevisionRecord, len(records))
	for _, revision := range records {
		module, exists := modules[revision.ModuleID]
		if !exists || !revision.Immutable || !sha1Pattern.MatchString(revision.SourceSHA) || !repositoryIdentityPattern.MatchString(revision.Repository) {
			return nil, fmt.Errorf("source revision identity is invalid for %s", revision.ModuleID)
		}
		if revision.Repository != module.module.Repository {
			return nil, fmt.Errorf("source revision repository does not match module %s", revision.ModuleID)
		}
		key := revisionKey(revision.ModuleID, revision.SourceSHA)
		if _, exists = revisions[key]; exists {
			return nil, fmt.Errorf("duplicate source revision %s at %s", revision.ModuleID, revision.SourceSHA)
		}
		revisions[key] = revision
		module.shas[revision.SourceSHA] = struct{}{}
	}
	return revisions, nil
}

func validateVersionRecords(observations []VersionObservationRecord, indexes []ModuleVersionsIndexRecord, modules map[string]*moduleIdentity, revisions map[string]SourceRevisionRecord) (map[string]VersionObservationRecord, map[string]ModuleVersionsIndexRecord, error) {
	byID, err := validateVersionObservations(observations, modules, revisions)
	if err != nil {
		return nil, nil, err
	}
	byModule := make(map[string]ModuleVersionsIndexRecord, len(indexes))
	for _, versionIndex := range indexes {
		if _, exists := modules[versionIndex.ModuleID]; !exists || versionIndex.Generation == 0 {
			return nil, nil, fmt.Errorf("module version index is invalid for %s", versionIndex.ModuleID)
		}
		if _, exists := byModule[versionIndex.ModuleID]; exists {
			return nil, nil, fmt.Errorf("duplicate module version index for %s", versionIndex.ModuleID)
		}
		if err := validateModuleVersionIndex(versionIndex, byID); err != nil {
			return nil, nil, err
		}
		byModule[versionIndex.ModuleID] = versionIndex
	}
	if err := validateVersionIndexCoverage(byID, byModule); err != nil {
		return nil, nil, err
	}
	return byID, byModule, nil
}

func validateVersionObservations(observations []VersionObservationRecord, modules map[string]*moduleIdentity, revisions map[string]SourceRevisionRecord) (map[string]VersionObservationRecord, error) {
	byID := make(map[string]VersionObservationRecord, len(observations))
	for _, observation := range observations {
		if err := validateVersionObservation(observation, modules, revisions); err != nil {
			return nil, err
		}
		if _, exists := byID[observation.ID]; exists {
			return nil, fmt.Errorf("duplicate version observation %s", observation.ID)
		}
		byID[observation.ID] = observation
		modules[observation.ModuleID].versionObservations[observation.ID] = struct{}{}
	}
	return byID, nil
}

func validateVersionObservation(observation VersionObservationRecord, modules map[string]*moduleIdentity, revisions map[string]SourceRevisionRecord) error {
	if err := identity.ValidateRecordID("version-observation", observation.ID); err != nil {
		return err
	}
	module, exists := modules[observation.ModuleID]
	if !exists || observation.Generation == 0 || observation.Version == "" || observation.ObservedAt.IsZero() || !repositoryIdentityPattern.MatchString(observation.Repository) {
		return fmt.Errorf("module version observation %s is invalid", observation.ID)
	}
	if observation.Repository != module.module.Repository {
		return fmt.Errorf("version observation %s repository does not match module", observation.ID)
	}
	if _, exists = revisions[revisionKey(observation.ModuleID, observation.SourceSHA)]; !exists {
		return fmt.Errorf("version observation %s has no source revision", observation.ID)
	}
	if len(observation.Tags) == 0 {
		return fmt.Errorf("version observation %s has no tags", observation.ID)
	}
	if _, err := identity.SortedSet(observation.Tags); err != nil {
		return fmt.Errorf("version observation %s tags: %w", observation.ID, err)
	}
	return nil
}

func validateModuleVersionIndex(versionIndex ModuleVersionsIndexRecord, observations map[string]VersionObservationRecord) error {
	seen := map[string]struct{}{}
	for _, id := range versionIndex.ObservationIDs {
		observation, exists := observations[id]
		if !exists || observation.ModuleID != versionIndex.ModuleID || observation.Generation > versionIndex.Generation {
			return fmt.Errorf("module version index %s has a dangling observation %s", versionIndex.ModuleID, id)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("module version index %s repeats observation %s", versionIndex.ModuleID, id)
		}
		seen[id] = struct{}{}
	}
	if versionIndex.OverallLatest == nil {
		return nil
	}
	latest, exists := observations[versionIndex.OverallLatest.ObservationID]
	if !exists || latest.ModuleID != versionIndex.ModuleID || latest.Version != versionIndex.OverallLatest.Version || latest.SourceSHA != versionIndex.OverallLatest.SourceSHA || !containsString(latest.Tags, versionIndex.OverallLatest.Tag) {
		return fmt.Errorf("module version index %s has an invalid explicit overall-latest pointer", versionIndex.ModuleID)
	}
	if _, exists = seen[versionIndex.OverallLatest.ObservationID]; !exists {
		return fmt.Errorf("module version index %s latest pointer is absent from observation history", versionIndex.ModuleID)
	}
	return nil
}

func validateVersionIndexCoverage(observations map[string]VersionObservationRecord, indexes map[string]ModuleVersionsIndexRecord) error {
	for id, observation := range observations {
		versionIndex, exists := indexes[observation.ModuleID]
		if !exists || !containsString(versionIndex.ObservationIDs, id) {
			return fmt.Errorf("version observation %s is missing from its module index", id)
		}
	}
	return nil
}

func validateFacts(records []FactRecord, revisions map[string]SourceRevisionRecord) (map[string]FactRecord, error) {
	facts := make(map[string]FactRecord, len(records))
	for _, fact := range records {
		if err := identity.ValidateRecordID("facts", fact.ID); err != nil {
			return nil, err
		}
		if _, exists := revisions[revisionKey(fact.ModuleID, fact.SourceSHA)]; !exists {
			return nil, fmt.Errorf("facts %s has no immutable source revision", fact.ID)
		}
		if !sha256Pattern.MatchString(fact.ContentDigest) {
			return nil, fmt.Errorf("facts %s has invalid content digest", fact.ID)
		}
		if _, exists := facts[fact.ID]; exists {
			return nil, fmt.Errorf("duplicate facts %s", fact.ID)
		}
		facts[fact.ID] = fact
	}
	return facts, nil
}

func validateProvenance(records []ProvenanceRecord, facts map[string]FactRecord) (map[string]ProvenanceRecord, error) {
	provenance := make(map[string]ProvenanceRecord, len(records))
	for _, record := range records {
		if err := identity.ValidateRecordID("provenance", record.ID); err != nil {
			return nil, err
		}
		fact, exists := facts[record.FactsID]
		if !exists || fact.ModuleID != record.ModuleID || fact.SourceSHA != record.SourceSHA {
			return nil, fmt.Errorf("provenance %s has a dangling or mismatched facts reference", record.ID)
		}
		if !sha256Pattern.MatchString(record.InputFingerprint) || record.Producer.ID == "" || record.Producer.Version == "" || record.Producer.ContractVersion < 1 {
			return nil, fmt.Errorf("provenance %s has invalid producer or input identity", record.ID)
		}
		if _, err := identity.SortedSet(record.Capabilities); err != nil {
			return nil, fmt.Errorf("provenance %s capabilities: %w", record.ID, err)
		}
		if _, exists := provenance[record.ID]; exists {
			return nil, fmt.Errorf("duplicate provenance %s", record.ID)
		}
		provenance[record.ID] = record
	}
	return provenance, nil
}

func validateProviderSnapshots(records []ProviderSnapshotRecord) (map[string]ProviderSnapshotRecord, error) {
	snapshots := make(map[string]ProviderSnapshotRecord, len(records))
	for _, snapshot := range records {
		if err := identity.ValidateRecordID("snapshot", snapshot.ID); err != nil {
			return nil, err
		}
		if _, err := identity.ProviderSnapshotManifestPath(snapshot.Address, snapshot.Version, snapshot.ID); err != nil {
			return nil, fmt.Errorf("provider snapshot %s: %w", snapshot.ID, err)
		}
		if _, exists := snapshots[snapshot.ID]; exists {
			return nil, fmt.Errorf("duplicate provider snapshot %s", snapshot.ID)
		}
		snapshots[snapshot.ID] = snapshot
	}
	return snapshots, nil
}

func validateDefinitions(records []assessment.DimensionDefinition) (map[string]assessment.DimensionDefinition, error) {
	definitions := make(map[string]assessment.DimensionDefinition, len(records))
	for _, definition := range records {
		if err := validateDefinition(definition); err != nil {
			return nil, err
		}
		if _, exists := definitions[definition.DefinitionID]; exists {
			return nil, fmt.Errorf("duplicate dimension definition %s", definition.DefinitionID)
		}
		definitions[definition.DefinitionID] = definition
	}
	return definitions, nil
}

func validateDefinition(definition assessment.DimensionDefinition) error {
	if err := identity.ValidateRecordID("definition", definition.DefinitionID); err != nil {
		return err
	}
	if definition.SchemaVersion != 2 || definition.RecordType != "dimension_definition" || definition.DimensionID == "" || definition.Version < 1 || definition.UnitScope == "" || definition.EvidenceSchemaVersion < 1 {
		return fmt.Errorf("dimension definition %s is invalid", definition.DefinitionID)
	}
	if err := identity.ValidateRecordID("definition", definition.PolicyID); err != nil {
		return fmt.Errorf("dimension definition %s has invalid policy identity", definition.DefinitionID)
	}
	if err := validateUniqueNonemptyStrings(definition.DimensionID, "check IDs", definition.CheckIDs); err != nil {
		return err
	}
	return validateUniqueNonemptyStrings(definition.DimensionID, "required capabilities", definition.RequiredCapabilities)
}

func validateUniqueNonemptyStrings(owner, field string, values []string) error {
	if _, err := identity.SortedSet(values); err != nil {
		return fmt.Errorf("dimension %s %s: %w", owner, field, err)
	}
	for _, value := range values {
		if value == "" {
			return fmt.Errorf("dimension %s has an empty %s value", owner, field)
		}
	}
	return nil
}

func validateDimensions(records []assessment.DimensionResult, facts map[string]FactRecord, definitions map[string]assessment.DimensionDefinition, providerSnapshots map[string]ProviderSnapshotRecord) (map[string]assessment.DimensionResult, error) {
	dimensions := make(map[string]assessment.DimensionResult, len(records))
	for _, result := range records {
		if err := validateDimensionResult(result, facts, definitions, providerSnapshots); err != nil {
			return nil, err
		}
		if _, exists := dimensions[result.DimensionResultID]; exists {
			return nil, fmt.Errorf("duplicate dimension result %s", result.DimensionResultID)
		}
		dimensions[result.DimensionResultID] = result
	}
	return dimensions, nil
}

func validateDimensionResult(result assessment.DimensionResult, facts map[string]FactRecord, definitions map[string]assessment.DimensionDefinition, providerSnapshots map[string]ProviderSnapshotRecord) error {
	if err := identity.ValidateRecordID("dimension-result", result.DimensionResultID); err != nil {
		return err
	}
	fact, exists := facts[result.FactsID]
	if !exists || fact.ModuleID != result.ModuleID || fact.SourceSHA != result.SourceSHA {
		return fmt.Errorf("dimension result %s has a dangling or mismatched facts reference", result.DimensionResultID)
	}
	if _, exists = definitions[result.DefinitionID]; !exists {
		return fmt.Errorf("dimension result %s has a dangling definition reference", result.DimensionResultID)
	}
	if result.SchemaVersion != 2 || result.RecordType != "dimension_result" || !sha256Pattern.MatchString(result.InputFingerprint) {
		return fmt.Errorf("dimension result %s has invalid record or input identity", result.DimensionResultID)
	}
	if err := identity.ValidateRelativePath(result.UnitPath, true); err != nil {
		return fmt.Errorf("dimension result %s has invalid unit path: %w", result.DimensionResultID, err)
	}
	return validateSnapshotReferences(result.DimensionResultID, result.SnapshotIDs, providerSnapshots)
}

func validateSnapshotReferences(resultID string, snapshotIDs []string, providerSnapshots map[string]ProviderSnapshotRecord) error {
	seen := map[string]struct{}{}
	for _, snapshotID := range snapshotIDs {
		if _, exists := providerSnapshots[snapshotID]; !exists {
			return fmt.Errorf("dimension result %s has a dangling provider snapshot %s", resultID, snapshotID)
		}
		if _, duplicate := seen[snapshotID]; duplicate {
			return fmt.Errorf("dimension result %s repeats provider snapshot %s", resultID, snapshotID)
		}
		seen[snapshotID] = struct{}{}
	}
	return nil
}

func validateAnalyses(records []AnalysisRecord, facts map[string]FactRecord, dimensions map[string]assessment.DimensionResult, providerSnapshots map[string]ProviderSnapshotRecord) (map[string]AnalysisRecord, error) {
	analyses := make(map[string]AnalysisRecord, len(records))
	for _, analysis := range records {
		if err := validateAnalysis(analysis, facts, dimensions, providerSnapshots); err != nil {
			return nil, err
		}
		if _, exists := analyses[analysis.ID]; exists {
			return nil, fmt.Errorf("duplicate analysis %s", analysis.ID)
		}
		analyses[analysis.ID] = analysis
	}
	return analyses, nil
}

func validateAnalysis(analysis AnalysisRecord, facts map[string]FactRecord, dimensions map[string]assessment.DimensionResult, providerSnapshots map[string]ProviderSnapshotRecord) error {
	if err := identity.ValidateRecordID("analysis", analysis.ID); err != nil {
		return err
	}
	fact, exists := facts[analysis.FactsID]
	if !exists || fact.ModuleID != analysis.ModuleID || fact.SourceSHA != analysis.SourceSHA {
		return fmt.Errorf("analysis %s has a dangling or mismatched facts reference", analysis.ID)
	}
	if analysis.ContextID == "" || !sha256Pattern.MatchString(analysis.InputFingerprint) {
		return fmt.Errorf("analysis %s has an invalid context or input fingerprint", analysis.ID)
	}
	targetSnapshotIDs, err := validateAnalysisTargets(analysis, providerSnapshots)
	if err != nil {
		return err
	}
	return validateAnalysisDimensions(analysis, dimensions, targetSnapshotIDs)
}

func validateAnalysisTargets(analysis AnalysisRecord, providerSnapshots map[string]ProviderSnapshotRecord) (map[string]struct{}, error) {
	seenTargets := map[string]struct{}{}
	targetSnapshotIDs := map[string]struct{}{}
	for _, target := range analysis.ProviderTargets {
		snapshot, exists := providerSnapshots[target.SnapshotID]
		if !exists || snapshot.Address != target.Address || snapshot.Version != target.Version {
			return nil, fmt.Errorf("analysis %s has a dangling or mismatched provider target", analysis.ID)
		}
		if _, err := identity.ProviderSnapshotPath(target.Address, target.Version, target.SnapshotID); err != nil {
			return nil, fmt.Errorf("analysis %s provider target: %w", analysis.ID, err)
		}
		key := target.Address + "\x00" + target.Version
		if _, duplicate := seenTargets[key]; duplicate {
			return nil, fmt.Errorf("analysis %s repeats provider target %s %s", analysis.ID, target.Address, target.Version)
		}
		seenTargets[key] = struct{}{}
		targetSnapshotIDs[target.SnapshotID] = struct{}{}
	}
	return targetSnapshotIDs, nil
}

func validateAnalysisDimensions(analysis AnalysisRecord, dimensions map[string]assessment.DimensionResult, targetSnapshotIDs map[string]struct{}) error {
	seenDimensions := map[string]struct{}{}
	for _, dimensionID := range analysis.DimensionResultIDs {
		if _, duplicate := seenDimensions[dimensionID]; duplicate {
			return fmt.Errorf("analysis %s repeats dimension result %s", analysis.ID, dimensionID)
		}
		seenDimensions[dimensionID] = struct{}{}
		result, exists := dimensions[dimensionID]
		if !exists || result.ModuleID != analysis.ModuleID || result.SourceSHA != analysis.SourceSHA || result.FactsID != analysis.FactsID {
			return fmt.Errorf("analysis %s has a dangling or mismatched dimension result %s", analysis.ID, dimensionID)
		}
		for _, snapshotID := range result.SnapshotIDs {
			if _, targeted := targetSnapshotIDs[snapshotID]; !targeted {
				return fmt.Errorf("analysis %s dimension result %s uses a provider snapshot outside its exact target vector", analysis.ID, dimensionID)
			}
		}
	}
	return nil
}

func validateSelections(records []SelectionReceipt, modules map[string]*moduleIdentity, facts map[string]FactRecord, analyses map[string]AnalysisRecord) (map[string][]SelectionPointer, error) {
	grouped := map[string][]SelectionReceipt{}
	seen := map[string]struct{}{}
	for _, selection := range records {
		if err := validateSelectionReceipt(selection, modules, facts, analyses); err != nil {
			return nil, err
		}
		if _, exists := seen[selection.ID]; exists {
			return nil, fmt.Errorf("duplicate selection receipt %s", selection.ID)
		}
		seen[selection.ID] = struct{}{}
		key := selectionKey(selection.ModuleID, selection.ContextID)
		grouped[key] = append(grouped[key], selection)
	}
	selectedByContext := make(map[string][]SelectionPointer, len(grouped))
	for key, history := range grouped {
		pointers, err := selectionPointersForHistory(key, history, analyses)
		if err != nil {
			return nil, err
		}
		selectedByContext[key] = pointers
	}
	return selectedByContext, nil
}

func validateSelectionReceipt(selection SelectionReceipt, modules map[string]*moduleIdentity, facts map[string]FactRecord, analyses map[string]AnalysisRecord) error {
	if err := identity.ValidateRecordID("selection", selection.ID); err != nil {
		return err
	}
	if _, exists := modules[selection.ModuleID]; !exists || selection.Sequence == 0 || selection.DesiredGeneration == 0 || selection.SelectedAt.IsZero() || !sha256Pattern.MatchString(selection.InputFingerprint) {
		return fmt.Errorf("selection receipt %s is invalid", selection.ID)
	}
	if err := identity.ValidateRelativePath(selection.ContextID, false); err != nil {
		return fmt.Errorf("selection receipt %s has invalid context: %w", selection.ID, err)
	}
	return validateSelectionReferences(selection, modules, facts, analyses)
}

func validateSelectionReferences(selection SelectionReceipt, modules map[string]*moduleIdentity, facts map[string]FactRecord, analyses map[string]AnalysisRecord) error {
	if selection.SourceSHA != nil {
		if !sha1Pattern.MatchString(*selection.SourceSHA) {
			return fmt.Errorf("selection receipt %s has an invalid source SHA", selection.ID)
		}
		if _, exists := modules[selection.ModuleID].shas[*selection.SourceSHA]; !exists {
			return fmt.Errorf("selection receipt %s has no immutable source revision", selection.ID)
		}
	}
	if selection.FactsID != nil {
		fact, exists := facts[*selection.FactsID]
		if selection.SourceSHA == nil || !exists || fact.ModuleID != selection.ModuleID || fact.SourceSHA != *selection.SourceSHA {
			return fmt.Errorf("selection receipt %s has a dangling or mismatched facts reference", selection.ID)
		}
	}
	if selection.AnalysisID == nil {
		return nil
	}
	analysis, exists := analyses[*selection.AnalysisID]
	if selection.SourceSHA == nil || selection.FactsID == nil || !exists || analysis.ModuleID != selection.ModuleID || analysis.ContextID != selection.ContextID || analysis.SourceSHA != *selection.SourceSHA || analysis.FactsID != *selection.FactsID || analysis.InputFingerprint != selection.InputFingerprint {
		return fmt.Errorf("selection receipt %s has a dangling or mismatched analysis reference", selection.ID)
	}
	return nil
}

func selectionPointersForHistory(key string, history []SelectionReceipt, analyses map[string]AnalysisRecord) ([]SelectionPointer, error) {
	sort.Slice(history, func(left, right int) bool { return history[left].Sequence < history[right].Sequence })
	pointers := make([]SelectionPointer, len(history))
	var previousAt time.Time
	var previousGeneration uint64
	for position, selection := range history {
		if selection.Sequence != uint64(position+1) {
			return nil, fmt.Errorf("selection history %s is not contiguous from sequence 1", key)
		}
		if position > 0 && (selection.SelectedAt.Before(previousAt) || selection.DesiredGeneration < previousGeneration) {
			return nil, fmt.Errorf("selection history %s regresses as-of order", key)
		}
		pointers[position] = selectionPointer(selection, analyses)
		previousAt = selection.SelectedAt
		previousGeneration = selection.DesiredGeneration
	}
	return pointers, nil
}

func selectionPointer(selection SelectionReceipt, analyses map[string]AnalysisRecord) SelectionPointer {
	pointer := SelectionPointer{
		ModuleID: selection.ModuleID, ContextID: selection.ContextID, Sequence: selection.Sequence,
		DesiredGeneration: selection.DesiredGeneration, SelectionID: selection.ID, SelectedAt: selection.SelectedAt,
		AnalysisID: copyStringPointer(selection.AnalysisID), SourceSHA: copyStringPointer(selection.SourceSHA), FactsID: copyStringPointer(selection.FactsID),
		InputFingerprint: selection.InputFingerprint, ProviderTargets: []ProviderTarget{},
	}
	if selection.AnalysisID != nil {
		pointer.ProviderTargets = append(pointer.ProviderTargets, analyses[*selection.AnalysisID].ProviderTargets...)
	}
	return pointer
}

func validatePresentations(records []PresentationSnapshotRecord, selections map[string][]SelectionPointer) ([]PresentationSnapshotRecord, error) {
	selectionByID := map[string]SelectionPointer{}
	for _, history := range selections {
		for _, selection := range history {
			selectionByID[selection.SelectionID] = selection
		}
	}
	presentations := append([]PresentationSnapshotRecord(nil), records...)
	seenIDs := map[string]struct{}{}
	seenGenerations := map[uint64]struct{}{}
	for _, presentation := range presentations {
		if err := validatePresentation(presentation, selectionByID); err != nil {
			return nil, err
		}
		if _, exists := seenIDs[presentation.ID]; exists {
			return nil, fmt.Errorf("duplicate presentation snapshot %s", presentation.ID)
		}
		seenIDs[presentation.ID] = struct{}{}
		if _, exists := seenGenerations[presentation.Generation]; exists {
			return nil, fmt.Errorf("duplicate presentation generation %d", presentation.Generation)
		}
		seenGenerations[presentation.Generation] = struct{}{}
	}
	sort.Slice(presentations, func(left, right int) bool { return presentations[left].Generation < presentations[right].Generation })
	for index := 1; index < len(presentations); index++ {
		if presentations[index].AsOf.Before(presentations[index-1].AsOf) {
			return nil, fmt.Errorf("presentation history regresses as-of order")
		}
	}
	return presentations, nil
}

func validatePresentation(presentation PresentationSnapshotRecord, selections map[string]SelectionPointer) error {
	if err := identity.ValidateRecordID("presentation", presentation.ID); err != nil {
		return err
	}
	if _, err := identity.PresentationPath(presentation.ID); err != nil {
		return err
	}
	if presentation.Generation == 0 || presentation.AsOf.IsZero() {
		return fmt.Errorf("presentation snapshot %s is invalid", presentation.ID)
	}
	seenSelections := map[string]struct{}{}
	for _, selectionID := range presentation.SelectionReceiptIDs {
		selection, exists := selections[selectionID]
		if !exists {
			return fmt.Errorf("presentation %s has a dangling selection receipt %s", presentation.ID, selectionID)
		}
		if selection.SelectedAt.After(presentation.AsOf) {
			return fmt.Errorf("presentation %s references a selection receipt newer than its as-of time", presentation.ID)
		}
		if _, duplicate := seenSelections[selectionID]; duplicate {
			return fmt.Errorf("presentation %s repeats selection receipt %s", presentation.ID, selectionID)
		}
		seenSelections[selectionID] = struct{}{}
	}
	return nil
}

func validateDatasetAsOf(createdAt time.Time, selections map[string][]SelectionPointer) error {
	for _, history := range selections {
		for _, selection := range history {
			if selection.SelectedAt.After(createdAt) {
				return fmt.Errorf("selection receipt %s is newer than dataset creation time", selection.SelectionID)
			}
		}
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func buildBaseIndexes(generation uint64, records validatedIndexRecords, selections map[string][]SelectionPointer, presentations []PresentationSnapshotRecord) Indexes {
	attachModuleHistory(records)
	indexes := Indexes{
		SchemaVersion: 1, CatalogGeneration: generation,
		Catalog: CatalogIndex{SchemaVersion: 1, RecordType: "catalog_index", CatalogGeneration: generation, Modules: []CatalogModuleEntry{}},
		Modules: []ModuleIndex{}, Tracks: []TrackIndex{}, Providers: buildProviderIndexes(records.providerSnapshots),
		DimensionDependencies: buildDimensionDependencies(records.definitions, records.dimensions, records.facts),
		Presentation:          buildPresentationIndex(presentations),
	}
	indexes.Modules, indexes.Catalog.Modules, indexes.Tracks = buildModuleTrackIndexes(records.modules, records.versionIndexes, selections)
	return indexes
}

func attachModuleHistory(records validatedIndexRecords) {
	for _, revision := range records.revisions {
		records.modules[revision.ModuleID].shas[revision.SourceSHA] = struct{}{}
	}
	for _, observation := range records.versionObservations {
		records.modules[observation.ModuleID].versionObservations[observation.ID] = struct{}{}
	}
	for _, fact := range records.facts {
		records.modules[fact.ModuleID].facts[fact.ID] = struct{}{}
	}
	for _, record := range records.provenance {
		records.modules[record.ModuleID].provenance[record.ID] = struct{}{}
	}
	for id, dimension := range records.dimensions {
		module := records.modules[dimension.ModuleID]
		module.dimensions[id] = struct{}{}
		module.definitions[dimension.DefinitionID] = struct{}{}
	}
	for id, analysis := range records.analyses {
		records.modules[analysis.ModuleID].analyses[id] = struct{}{}
	}
}

func buildPresentationIndex(presentations []PresentationSnapshotRecord) PresentationIndex {
	ordered := append([]PresentationSnapshotRecord(nil), presentations...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].Generation != ordered[right].Generation {
			return ordered[left].Generation < ordered[right].Generation
		}
		if !ordered[left].AsOf.Equal(ordered[right].AsOf) {
			return ordered[left].AsOf.Before(ordered[right].AsOf)
		}
		return ordered[left].ID < ordered[right].ID
	})
	index := PresentationIndex{SchemaVersion: 1, RecordType: "presentation_index", Snapshots: ordered}
	if len(ordered) > 0 {
		currentID := ordered[len(ordered)-1].ID
		index.CurrentID = &currentID
	}
	return index
}

func buildDimensionDependencies(definitions map[string]assessment.DimensionDefinition, dimensions map[string]assessment.DimensionResult, facts map[string]FactRecord) DimensionDependencyIndex {
	dependencies := map[string]*DimensionDependency{}
	factsByDefinition := map[string]map[string]struct{}{}
	snapshotsByDefinition := map[string]map[string]struct{}{}
	definitionIDs := make([]string, 0, len(definitions))
	for id, definition := range definitions {
		definitionIDs = append(definitionIDs, id)
		dependencies[id] = &DimensionDependency{
			DefinitionID: id, DimensionID: definition.DimensionID, Version: definition.Version,
			CheckIDs:             append([]string(nil), definition.CheckIDs...),
			RequiredCapabilities: append([]string(nil), definition.RequiredCapabilities...),
			FactsIDs:             []string{}, ProviderSnapshotIDs: []string{}, DimensionResultIDs: []string{},
		}
		factsByDefinition[id] = map[string]struct{}{}
		snapshotsByDefinition[id] = map[string]struct{}{}
	}
	for id, dimension := range dimensions {
		dependency := dependencies[dimension.DefinitionID]
		dependency.DimensionResultIDs = append(dependency.DimensionResultIDs, id)
		factsByDefinition[dimension.DefinitionID][dimension.FactsID] = struct{}{}
		for _, snapshotID := range dimension.SnapshotIDs {
			snapshotsByDefinition[dimension.DefinitionID][snapshotID] = struct{}{}
		}
	}
	sort.Strings(definitionIDs)
	result := DimensionDependencyIndex{
		SchemaVersion: 1, RecordType: "dimension_dependency_index",
		Definitions:        make([]DimensionDependency, 0, len(definitionIDs)),
		ProviderTypeUsages: []ProviderTypeUsage{}, ProviderUsageCoverage: []ProviderUsageCoverage{},
	}
	for _, id := range definitionIDs {
		dependency := dependencies[id]
		sort.Strings(dependency.CheckIDs)
		sort.Strings(dependency.RequiredCapabilities)
		dependency.FactsIDs = sortedKeys(factsByDefinition[id])
		dependency.ProviderSnapshotIDs = sortedKeys(snapshotsByDefinition[id])
		sort.Strings(dependency.DimensionResultIDs)
		result.Definitions = append(result.Definitions, *dependency)
	}
	factIDs := make([]string, 0, len(facts))
	for factID := range facts {
		factIDs = append(factIDs, factID)
	}
	sort.Strings(factIDs)
	for _, factID := range factIDs {
		fact := facts[factID]
		result.ProviderTypeUsages = append(result.ProviderTypeUsages, fact.ProviderTypeUsages...)
		result.ProviderUsageCoverage = append(result.ProviderUsageCoverage, fact.ProviderUsageCoverage...)
	}
	sort.Slice(result.ProviderTypeUsages, func(left, right int) bool {
		return providerTypeUsageLess(result.ProviderTypeUsages[left], result.ProviderTypeUsages[right])
	})
	sort.Slice(result.ProviderUsageCoverage, func(left, right int) bool {
		return providerUsageCoverageLess(result.ProviderUsageCoverage[left], result.ProviderUsageCoverage[right])
	})
	return result
}

func providerTypeUsageLess(left, right ProviderTypeUsage) bool {
	if left.ModuleID != right.ModuleID {
		return left.ModuleID < right.ModuleID
	}
	if left.UnitPath != right.UnitPath {
		return left.UnitPath < right.UnitPath
	}
	if left.ProviderAddress != right.ProviderAddress {
		return left.ProviderAddress < right.ProviderAddress
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	return left.FactsID < right.FactsID
}

func providerUsageCoverageLess(left, right ProviderUsageCoverage) bool {
	if left.ModuleID != right.ModuleID {
		return left.ModuleID < right.ModuleID
	}
	if left.UnitPath != right.UnitPath {
		return left.UnitPath < right.UnitPath
	}
	return left.FactsID < right.FactsID
}

func buildProviderIndexes(records map[string]ProviderSnapshotRecord) []ProviderIndex {
	byAddress := map[string][]ProviderSnapshotRecord{}
	for _, snapshot := range records {
		byAddress[snapshot.Address] = append(byAddress[snapshot.Address], snapshot)
	}
	addresses := make([]string, 0, len(byAddress))
	for address := range byAddress {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	indexes := make([]ProviderIndex, 0, len(addresses))
	for _, address := range addresses {
		snapshots := byAddress[address]
		sort.Slice(snapshots, func(left, right int) bool {
			if snapshots[left].Version == snapshots[right].Version {
				return snapshots[left].ID < snapshots[right].ID
			}
			return snapshots[left].Version < snapshots[right].Version
		})
		indexes = append(indexes, ProviderIndex{SchemaVersion: 1, RecordType: "provider_index", ProviderAddress: address, Snapshots: snapshots})
	}
	return indexes
}

func buildModuleTrackIndexes(modules map[string]*moduleIdentity, versionIndexes map[string]ModuleVersionsIndexRecord, selections map[string][]SelectionPointer) ([]ModuleIndex, []CatalogModuleEntry, []TrackIndex) {
	moduleIDs := make([]string, 0, len(modules))
	for moduleID := range modules {
		moduleIDs = append(moduleIDs, moduleID)
	}
	sort.Strings(moduleIDs)
	moduleIndexes := make([]ModuleIndex, 0, len(moduleIDs))
	catalogModules := make([]CatalogModuleEntry, 0, len(moduleIDs))
	tracks := map[string]*TrackIndex{}
	for _, moduleID := range moduleIDs {
		module := modules[moduleID]
		moduleIndex := buildModuleIndex(moduleID, module, versionIndexes, selections)
		moduleIndexes = append(moduleIndexes, moduleIndex)
		catalogModules = append(catalogModules, CatalogModuleEntry{ModuleID: moduleID, Repository: module.module.Repository, Lifecycle: module.module.Lifecycle})
		appendModuleTracks(moduleID, moduleIndex.SelectionHistory, tracks)
	}
	return moduleIndexes, catalogModules, finalizeTrackIndexes(tracks)
}

func buildModuleIndex(moduleID string, module *moduleIdentity, versionIndexes map[string]ModuleVersionsIndexRecord, selections map[string][]SelectionPointer) ModuleIndex {
	index := ModuleIndex{
		SchemaVersion: 1, RecordType: "module_index", ModuleID: moduleID, Repository: module.module.Repository, Lifecycle: module.module.Lifecycle,
		SourceSHAs: sortedKeys(module.shas), VersionObservationIDs: sortedKeys(module.versionObservations),
		FactsIDs: sortedKeys(module.facts), ProvenanceIDs: sortedKeys(module.provenance),
		DefinitionIDs: sortedKeys(module.definitions), DimensionResultIDs: sortedKeys(module.dimensions),
		AnalysisIDs: sortedKeys(module.analyses), SelectionHistory: []SelectionPointer{}, CurrentSelections: []SelectionPointer{},
	}
	if versionIndex, exists := versionIndexes[moduleID]; exists {
		versionCopy := versionIndex
		index.VersionIndex = &versionCopy
		index.VersionObservationIDs = append([]string(nil), versionIndex.ObservationIDs...)
	}
	for key, history := range selections {
		if !strings.HasPrefix(key, moduleID+"\x00") || len(history) == 0 {
			continue
		}
		index.SelectionHistory = append(index.SelectionHistory, history...)
		index.CurrentSelections = append(index.CurrentSelections, history[len(history)-1])
	}
	sort.Slice(index.SelectionHistory, func(left, right int) bool {
		if index.SelectionHistory[left].ContextID == index.SelectionHistory[right].ContextID {
			return index.SelectionHistory[left].Sequence < index.SelectionHistory[right].Sequence
		}
		return index.SelectionHistory[left].ContextID < index.SelectionHistory[right].ContextID
	})
	index.SelectionIDs = selectionIDs(index.SelectionHistory)
	sort.Slice(index.CurrentSelections, func(left, right int) bool {
		return index.CurrentSelections[left].ContextID < index.CurrentSelections[right].ContextID
	})
	return index
}

func appendModuleTracks(moduleID string, histories []SelectionPointer, tracks map[string]*TrackIndex) {
	byContext := map[string][]SelectionPointer{}
	for _, selection := range histories {
		byContext[selection.ContextID] = append(byContext[selection.ContextID], selection)
	}
	for contextID, history := range byContext {
		track := tracks[contextID]
		if track == nil {
			track = &TrackIndex{SchemaVersion: 1, RecordType: "track_index", ContextID: contextID, ModuleIDs: []string{}, Selections: []SelectionPointer{}, TargetVectors: []TrackTargetVector{}}
			tracks[contextID] = track
		}
		track.ModuleIDs = append(track.ModuleIDs, moduleID)
		track.Selections = append(track.Selections, history...)
		for _, selection := range history {
			track.TargetVectors = append(track.TargetVectors, TrackTargetVector{
				ModuleID: moduleID, SelectionID: selection.SelectionID, Sequence: selection.Sequence,
				AnalysisID: copyStringPointer(selection.AnalysisID), ProviderTargets: append([]ProviderTarget(nil), selection.ProviderTargets...),
			})
		}
	}
}

func finalizeTrackIndexes(tracks map[string]*TrackIndex) []TrackIndex {
	contexts := make([]string, 0, len(tracks))
	for contextID := range tracks {
		contexts = append(contexts, contextID)
	}
	sort.Strings(contexts)
	indexes := make([]TrackIndex, 0, len(contexts))
	for _, contextID := range contexts {
		track := tracks[contextID]
		sort.Strings(track.ModuleIDs)
		sort.Slice(track.Selections, func(left, right int) bool {
			if track.Selections[left].ModuleID == track.Selections[right].ModuleID {
				return track.Selections[left].Sequence < track.Selections[right].Sequence
			}
			return track.Selections[left].ModuleID < track.Selections[right].ModuleID
		})
		sort.Slice(track.TargetVectors, func(left, right int) bool {
			if track.TargetVectors[left].ModuleID == track.TargetVectors[right].ModuleID {
				return track.TargetVectors[left].Sequence < track.TargetVectors[right].Sequence
			}
			return track.TargetVectors[left].ModuleID < track.TargetVectors[right].ModuleID
		})
		indexes = append(indexes, *track)
	}
	return indexes
}

func buildDatasetManifest(records Records, indexes *Indexes) error {
	if len(records.RecordDigests) == 0 {
		return fmt.Errorf("accepted record digests are required to build the dataset manifest")
	}
	recordDigests, err := acceptedRecordDigests(records.RecordDigests)
	if err != nil {
		return err
	}
	if err = assignIndexIDs(indexes); err != nil {
		return err
	}
	if err = addGeneratedIndexDigests(recordDigests, indexes); err != nil {
		return err
	}
	membership := datasetMembership(indexes.Modules)
	datasetID, err := datasetIdentity(records.CatalogGeneration, recordDigests, membership)
	if err != nil {
		return err
	}
	indexes.Dataset = DatasetManifest{
		SchemaVersion: 2, RecordType: "dataset_manifest", DatasetID: datasetID,
		Generator: records.Generator, CatalogGeneration: records.CatalogGeneration,
		RecordDigests: recordDigests, ModuleMembership: membership, CreatedAt: records.CreatedAt.UTC(),
	}
	return nil
}

func acceptedRecordDigests(accepted map[string]string) (map[string]string, error) {
	datasetPath, err := identity.CanonicalDataPath("dataset")
	if err != nil {
		return nil, err
	}
	digests := make(map[string]string, len(accepted))
	for recordPath, digest := range accepted {
		if err := identity.ValidateRelativePath(recordPath, false); err != nil {
			return nil, fmt.Errorf("accepted record digest path %q: %w", recordPath, err)
		}
		if !sha256Pattern.MatchString(digest) {
			return nil, fmt.Errorf("accepted record %s has invalid digest", recordPath)
		}
		if recordPath != datasetPath {
			digests[recordPath] = digest
		}
	}
	return digests, nil
}

func addGeneratedIndexDigests(digests map[string]string, indexes *Indexes) error {
	catalogPath, err := identity.CanonicalDataPath("catalog")
	if err != nil {
		return err
	}
	if err = addIndexDigest(digests, catalogPath, indexes.Catalog); err != nil {
		return err
	}
	if err = addModuleIndexDigests(digests, indexes.Modules); err != nil {
		return err
	}
	if err = addTrackIndexDigests(digests, indexes.Tracks); err != nil {
		return err
	}
	if err = addProviderIndexDigests(digests, indexes.Providers); err != nil {
		return err
	}
	if err = addIndexDigest(digests, identity.DimensionDependencyIndexPath(), indexes.DimensionDependencies); err != nil {
		return err
	}
	return addIndexDigest(digests, identity.PresentationIndexPath(), indexes.Presentation)
}

func addModuleIndexDigests(digests map[string]string, modules []ModuleIndex) error {
	for _, module := range modules {
		recordPath, err := identity.ModuleIndexPath(module.ModuleID)
		if err != nil {
			return err
		}
		if err = addIndexDigest(digests, recordPath, module); err != nil {
			return err
		}
	}
	return nil
}

func addTrackIndexDigests(digests map[string]string, tracks []TrackIndex) error {
	for _, track := range tracks {
		recordPath, err := identity.TrackIndexPath(track.ContextID)
		if err != nil {
			return err
		}
		if err = addIndexDigest(digests, recordPath, track); err != nil {
			return err
		}
	}
	return nil
}

func addProviderIndexDigests(digests map[string]string, providers []ProviderIndex) error {
	for _, provider := range providers {
		recordPath, err := identity.ProviderIndexPath(provider.ProviderAddress)
		if err != nil {
			return err
		}
		if err = addIndexDigest(digests, recordPath, provider); err != nil {
			return err
		}
	}
	return nil
}

func datasetMembership(modules []ModuleIndex) []ModuleMembership {
	membership := make([]ModuleMembership, 0)
	for _, module := range modules {
		for _, selection := range module.CurrentSelections {
			selectionID := selection.SelectionID
			membership = append(membership, ModuleMembership{
				ModuleID: module.ModuleID, ContextID: selection.ContextID,
				AnalysisID: copyStringPointer(selection.AnalysisID), SelectionID: &selectionID,
			})
		}
	}
	sort.Slice(membership, func(left, right int) bool {
		if membership[left].ModuleID == membership[right].ModuleID {
			return membership[left].ContextID < membership[right].ContextID
		}
		return membership[left].ModuleID < membership[right].ModuleID
	})
	return membership
}

func datasetIdentity(generation uint64, digests map[string]string, membership []ModuleMembership) (string, error) {
	payload := struct {
		CatalogGeneration uint64             `json:"catalog_generation"`
		RecordDigests     map[string]string  `json:"record_digests"`
		ModuleMembership  []ModuleMembership `json:"module_membership"`
	}{CatalogGeneration: generation, RecordDigests: digests, ModuleMembership: membership}
	datasetID, err := identity.ID("dataset", 2, payload)
	if err != nil {
		return "", fmt.Errorf("identify dataset manifest: %w", err)
	}
	return datasetID, nil
}

func assignIndexIDs(indexes *Indexes) error {
	indexID, err := indexContentID("catalog-index", indexes.Catalog)
	if err != nil {
		return err
	}
	indexes.Catalog.IndexID = indexID
	for position := range indexes.Modules {
		indexID, err = indexContentID("module-index", indexes.Modules[position])
		if err != nil {
			return err
		}
		indexes.Modules[position].IndexID = indexID
	}
	for position := range indexes.Tracks {
		indexID, err = indexContentID("track-index", indexes.Tracks[position])
		if err != nil {
			return err
		}
		indexes.Tracks[position].IndexID = indexID
	}
	for position := range indexes.Providers {
		indexID, err = indexContentID("provider-index", indexes.Providers[position])
		if err != nil {
			return err
		}
		indexes.Providers[position].IndexID = indexID
	}
	indexID, err = indexContentID("dimension-dependency-index", indexes.DimensionDependencies)
	if err != nil {
		return err
	}
	indexes.DimensionDependencies.IndexID = indexID
	indexID, err = indexContentID("presentation-index", indexes.Presentation)
	if err != nil {
		return err
	}
	indexes.Presentation.IndexID = indexID
	return nil
}

func indexContentID(recordKind string, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var payload map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &payload); err != nil {
		return "", err
	}
	delete(payload, "index_id")
	return identity.ID(recordKind, 1, payload)
}

func addIndexDigest(digests map[string]string, recordPath string, value any) error {
	if err := identity.ValidateRelativePath(recordPath, false); err != nil {
		return fmt.Errorf("generated index path %q: %w", recordPath, err)
	}
	encoded, err := encodeDocument(value)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	digests[recordPath] = hex.EncodeToString(digest[:])
	return nil
}

// Files returns the complete generated index set at its canonical data paths.
// It verifies each emitted index's exact bytes against the dataset manifest.
func (indexes Indexes) Files() (map[string][]byte, error) {
	if indexes.Dataset.DatasetID == "" {
		return nil, fmt.Errorf("dataset manifest is missing")
	}
	files := map[string][]byte{}
	catalogPath, err := identity.CanonicalDataPath("catalog")
	if err != nil {
		return nil, err
	}
	if err = addVerifiedIndexFile(files, indexes.Dataset.RecordDigests, catalogPath, indexes.Catalog); err != nil {
		return nil, err
	}
	if err = addIndexFileCollection(files, indexes.Dataset.RecordDigests, indexes.Modules, func(module ModuleIndex) (string, error) {
		return identity.ModuleIndexPath(module.ModuleID)
	}); err != nil {
		return nil, err
	}
	if err = addIndexFileCollection(files, indexes.Dataset.RecordDigests, indexes.Tracks, func(track TrackIndex) (string, error) {
		return identity.TrackIndexPath(track.ContextID)
	}); err != nil {
		return nil, err
	}
	if err = addIndexFileCollection(files, indexes.Dataset.RecordDigests, indexes.Providers, func(provider ProviderIndex) (string, error) {
		return identity.ProviderIndexPath(provider.ProviderAddress)
	}); err != nil {
		return nil, err
	}
	if err = addVerifiedIndexFile(files, indexes.Dataset.RecordDigests, identity.DimensionDependencyIndexPath(), indexes.DimensionDependencies); err != nil {
		return nil, err
	}
	if err = addVerifiedIndexFile(files, indexes.Dataset.RecordDigests, identity.PresentationIndexPath(), indexes.Presentation); err != nil {
		return nil, err
	}
	datasetPath, err := identity.CanonicalDataPath("dataset")
	if err != nil {
		return nil, err
	}
	if _, exists := indexes.Dataset.RecordDigests[datasetPath]; exists {
		return nil, fmt.Errorf("dataset manifest contains its own digest")
	}
	if err = addGeneratedFile(files, datasetPath, indexes.Dataset); err != nil {
		return nil, err
	}
	return files, nil
}

func addIndexFileCollection[T any](files map[string][]byte, digests map[string]string, records []T, pathFor func(T) (string, error)) error {
	for _, record := range records {
		recordPath, err := pathFor(record)
		if err != nil {
			return err
		}
		if err = addVerifiedIndexFile(files, digests, recordPath, record); err != nil {
			return err
		}
	}
	return nil
}

func addVerifiedIndexFile(files map[string][]byte, digests map[string]string, recordPath string, value any) error {
	encoded, err := encodeDocument(value)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	if digests[recordPath] != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("dataset digest does not match generated index %s", recordPath)
	}
	return addGeneratedFile(files, recordPath, encoded)
}

func addGeneratedFile(files map[string][]byte, recordPath string, value any) error {
	if _, exists := files[recordPath]; exists {
		return fmt.Errorf("generated index path is duplicated: %s", recordPath)
	}
	encoded, ok := value.([]byte)
	if !ok {
		var err error
		encoded, err = encodeDocument(value)
		if err != nil {
			return err
		}
	}
	files[recordPath] = encoded
	return nil
}

func encodeDocument(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func copyStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copiedValue := *value
	return &copiedValue
}

func revisionKey(moduleID, sourceSHA string) string  { return moduleID + "\x00" + sourceSHA }
func selectionKey(moduleID, contextID string) string { return moduleID + "\x00" + contextID }

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}

func selectionIDs(selections []SelectionPointer) []string {
	ids := make([]string, 0, len(selections))
	for _, selection := range selections {
		ids = append(ids, selection.SelectionID)
	}
	return ids
}

var sha1Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
