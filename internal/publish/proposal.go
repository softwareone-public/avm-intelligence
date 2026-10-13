package publish

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
	"github.com/softwareone-public/avm-intelligence/internal/worker"
)

func validCatalogRepositoryIdentity(repository string) bool {
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, part := range parts {
		for _, character := range part {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '.' || character == '-') {
				return false
			}
		}
	}
	return true
}

const (
	ProposalSchemaVersion        = 2
	catalogPointerPath           = "data/catalog.json"
	datasetManifestPath          = "data/dataset.json"
	coreFunctionDataPrefix       = "data/terraform/core/"
	moduleObservationDataPrefix  = "data/observations/modules/"
	generatedIndexName           = "index.json"
	proposalFilename             = "proposal.json"
	catalogIndexKind             = "catalog-index"
	moduleIndexKind              = "module-index"
	trackIndexKind               = "track-index"
	providerIndexKind            = "provider-index"
	dimensionDependencyIndexKind = "dimension-dependency-index"
	presentationIndexKind        = "presentation-index"
)

var (
	repositoryIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	releaseVersionPattern     = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
)

type ProposalKind string

const (
	ProposalModuleEvidence    ProposalKind = "module_evidence"
	ProposalCatalog           ProposalKind = "catalog"
	ProposalProviderSnapshot  ProposalKind = "provider_snapshot"
	ProposalModuleObservation ProposalKind = "module_observation"
	ProposalSelectionExport   ProposalKind = "selection_export"
	ProposalPresentation      ProposalKind = "presentation"
)

type ProposalProducer struct {
	ID              string `json:"id"`
	Version         string `json:"version"`
	ContractVersion int    `json:"contract_version"`
}

type ProposalChange struct {
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Digest    string `json:"digest"`
	RecordID  string `json:"record_id,omitempty"`
}

// CatalogModuleMetadata is the accepted public catalog row persisted beside
// the generated catalog pointer.
type CatalogModuleMetadata struct {
	SchemaVersion        int    `json:"schema_version"`
	RecordType           string `json:"record_type"`
	ModuleID             string `json:"module_id"`
	ModuleType           string `json:"module_type"`
	Status               string `json:"status"`
	Repository           string `json:"repository"`
	Lifecycle            string `json:"lifecycle"`
	DisplayName          string `json:"display_name"`
	RegistryURL          string `json:"registry_url,omitempty"`
	AlternativeNames     string `json:"alternative_names,omitempty"`
	Description          string `json:"description"`
	Comments             string `json:"comments,omitempty"`
	FirstPublished       string `json:"first_published,omitempty"`
	ProviderNamespace    string `json:"provider_namespace,omitempty"`
	ResourceType         string `json:"resource_type,omitempty"`
	ParentModule         string `json:"parent_module,omitempty"`
	PrimaryOwnerHandle   string `json:"primary_owner_handle,omitempty"`
	PrimaryOwnerName     string `json:"primary_owner_name,omitempty"`
	SecondaryOwnerHandle string `json:"secondary_owner_handle,omitempty"`
	SecondaryOwnerName   string `json:"secondary_owner_name,omitempty"`
	MissingObservations  int    `json:"missing_observations"`
	LastSeenGeneration   uint64 `json:"last_seen_generation"`
}

type CatalogIndexProposalInput struct {
	ProposalRoot    string
	BaseDataRoot    string
	Outbox          discovery.CatalogProposal
	Generation      uint64
	Modules         []index.CatalogModuleEntry
	Metadata        []CatalogModuleMetadata
	ExpectedBase    string
	ExpectedPointer *string
}

// Validate checks the typed catalog metadata needed for a deterministic index rebuild.
func (record CatalogModuleMetadata) Validate() error {
	if record.SchemaVersion != 2 || record.RecordType != "module_metadata" || record.ModuleType != "res" && record.ModuleType != "ptn" && record.ModuleType != "utl" || record.Status == "" || record.Repository == "" || record.DisplayName == "" || record.MissingObservations < 0 || record.LastSeenGeneration == 0 {
		return fmt.Errorf("module metadata record is incomplete")
	}
	if _, err := identity.ModuleMetadataPath(record.ModuleID); err != nil {
		return err
	}
	if !validCatalogRepositoryIdentity(record.Repository) || record.Lifecycle != "active" && record.Lifecycle != "retired" {
		return fmt.Errorf("module metadata repository or lifecycle is invalid")
	}
	return nil
}

// Proposal is the schema-v2, single-subject change envelope consumed by the
// constrained local acceptance writer.
type Proposal struct {
	SchemaVersion    int               `json:"schema_version"`
	RecordType       string            `json:"record_type"`
	ProposalID       string            `json:"proposal_id"`
	Kind             ProposalKind      `json:"kind"`
	Subject          string            `json:"subject"`
	SourceGeneration uint64            `json:"source_generation"`
	ExpectedBase     string            `json:"expected_base"`
	ExpectedPointer  *string           `json:"expected_pointer"`
	Producer         ProposalProducer  `json:"producer"`
	ContentDigest    string            `json:"content_digest"`
	Paths            []string          `json:"paths"`
	Preconditions    map[string]string `json:"preconditions,omitempty"`
	Changes          []ProposalChange  `json:"changes,omitempty"`
}

type proposalIdentity struct {
	SchemaVersion    int               `json:"schema_version"`
	RecordType       string            `json:"record_type"`
	Kind             ProposalKind      `json:"kind"`
	Subject          string            `json:"subject"`
	SourceGeneration uint64            `json:"source_generation"`
	ExpectedBase     string            `json:"expected_base"`
	ExpectedPointer  *string           `json:"expected_pointer"`
	Producer         ProposalProducer  `json:"producer"`
	ContentDigest    string            `json:"content_digest"`
	Paths            []string          `json:"paths"`
	Preconditions    map[string]string `json:"preconditions,omitempty"`
	Changes          []ProposalChange  `json:"changes,omitempty"`
}

type selectionExportReceipt struct {
	SchemaVersion     int       `json:"schema_version"`
	RecordType        string    `json:"record_type"`
	ID                string    `json:"selection_id"`
	ModuleID          string    `json:"module_id"`
	AVMReleaseVersion string    `json:"avm_release_version"`
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

type moduleObservationRecord struct {
	SchemaVersion int              `json:"schema_version"`
	RecordType    string           `json:"record_type"`
	ObservationID string           `json:"observation_id"`
	ModuleID      string           `json:"module_id"`
	Kind          string           `json:"kind"`
	Generation    uint64           `json:"generation"`
	SourceSHA     string           `json:"source_sha"`
	SourceDigest  string           `json:"source_digest"`
	Producer      ProposalProducer `json:"producer"`
	Coverage      string           `json:"coverage"`
	ObservedAt    time.Time        `json:"observed_at"`
}

type moduleObservationIndex struct {
	IndexID        string   `json:"index_id"`
	SchemaVersion  int      `json:"schema_version"`
	RecordType     string   `json:"record_type"`
	ModuleID       string   `json:"module_id"`
	Kind           string   `json:"kind"`
	Generation     uint64   `json:"generation"`
	ObservationIDs []string `json:"observation_ids"`
	CurrentID      *string  `json:"current_id"`
}

type coreFunctionIndex struct {
	IndexID          string   `json:"index_id"`
	SchemaVersion    int      `json:"schema_version"`
	RecordType       string   `json:"record_type"`
	TerraformVersion string   `json:"terraform_version"`
	Generation       uint64   `json:"generation"`
	SnapshotIDs      []string `json:"snapshot_ids"`
	CurrentID        *string  `json:"current_id"`
}

type presentationSnapshotPayload struct {
	SchemaVersion int       `json:"schema_version"`
	RecordType    string    `json:"record_type"`
	ID            string    `json:"presentation_id"`
	Generation    uint64    `json:"generation"`
	AsOf          time.Time `json:"as_of"`
	Modules       []struct {
		ModuleID           string          `json:"module_id"`
		Lifecycle          string          `json:"lifecycle"`
		DesiredSourceSHA   string          `json:"desired_source_sha,omitempty"`
		SelectedReceiptIDs []string        `json:"selected_receipt_ids,omitempty"`
		Coverage           json.RawMessage `json:"coverage"`
		ReasonCode         string          `json:"reason_code,omitempty"`
	} `json:"modules"`
	CollectorHealth []struct {
		CollectorID string     `json:"collector_id"`
		Status      string     `json:"status"`
		ObservedAt  time.Time  `json:"observed_at"`
		LastGoodAt  *time.Time `json:"last_good_at,omitempty"`
	} `json:"collector_health"`
}

// IdentifyProposal fills the content digest and stable proposal ID after the
// caller has populated the typed subject, paths, preconditions, and changes.
func IdentifyProposal(proposal Proposal) (Proposal, error) {
	proposal.SchemaVersion = ProposalSchemaVersion
	proposal.RecordType = "proposal"
	paths, err := identity.SortedSet(proposal.Paths)
	if err != nil {
		return Proposal{}, fmt.Errorf("proposal paths are invalid: %w", err)
	}
	proposal.Paths = paths
	proposal.Changes = append([]ProposalChange(nil), proposal.Changes...)
	sort.Slice(proposal.Changes, func(left, right int) bool { return proposal.Changes[left].Path < proposal.Changes[right].Path })
	content, err := proposalChangesDigest(proposal.Changes)
	if err != nil {
		return Proposal{}, err
	}
	proposal.ContentDigest = content
	proposal.ProposalID = ""
	id, err := identity.ID("proposal", ProposalSchemaVersion, proposalIdentityFor(proposal))
	if err != nil {
		return Proposal{}, err
	}
	proposal.ProposalID = id
	return proposal, nil
}

// ApplyProposal validates one proposal against the exact current base and
// applies its data/ paths to a fresh accepted-data tree. It never pushes or commits.
func ApplyProposal(proposalRoot, currentBase, baseDataRoot, outputDataRoot string) error {
	return applyProposalKind(proposalRoot, currentBase, baseDataRoot, outputDataRoot, "")
}

// ApplyProposalPatch additionally proves that every changed path in the
// candidate commit is represented by the proposal envelope.
func ApplyProposalPatch(proposalRoot, currentBase, baseDataRoot, outputDataRoot string, changedPaths []string) error {
	if err := ValidateProposalPatch(proposalRoot, changedPaths); err != nil {
		return err
	}
	return ApplyProposal(proposalRoot, currentBase, baseDataRoot, outputDataRoot)
}

// ValidateProposalPatch rejects mixed code/data changes and omitted payloads.
func ValidateProposalPatch(proposalRoot string, changedPaths []string) error {
	proposal, err := readProposal(filepath.Join(proposalRoot, proposalFilename))
	if err != nil {
		return err
	}
	if err = validateProposal(proposal); err != nil {
		return err
	}
	actual, err := identity.SortedSet(changedPaths)
	if err != nil {
		return fmt.Errorf("candidate diff has duplicate paths: %w", err)
	}
	expectedPaths := append(append([]string(nil), proposal.Paths...), proposalFilename)
	expected, err := identity.SortedSet(expectedPaths)
	if err != nil || !equalProposalStrings(actual, expected) {
		return fmt.Errorf("candidate diff paths do not exactly match the proposal envelope")
	}
	return nil
}

// ApplyModuleEvidenceProposal validates one typed module-evidence proposal using
// the native constrained applier and complete-dataset preservation rules.
func ApplyModuleEvidenceProposal(proposalRoot, currentBase, baseDataRoot, outputDataRoot string) error {
	return applyProposalKind(proposalRoot, currentBase, baseDataRoot, outputDataRoot, ProposalModuleEvidence)
}

func applyProposalKind(proposalRoot, currentBase, baseDataRoot, outputDataRoot string, requiredKind ProposalKind) error {
	if err := ensureProposalRootsSeparated(baseDataRoot, outputDataRoot); err != nil {
		return err
	}
	proposal, err := readProposal(filepath.Join(proposalRoot, proposalFilename))
	if err != nil {
		return err
	}
	if err = validateProposal(proposal); err != nil {
		return err
	}
	if requiredKind != "" && proposal.Kind != requiredKind {
		return fmt.Errorf("proposal kind %q cannot be applied through the module evidence adapter", proposal.Kind)
	}
	if !isLowerHex(currentBase, 40) {
		return fmt.Errorf("current merge base must be a lowercase Git SHA")
	}
	rebased := proposal.ExpectedBase != currentBase
	if rebased {
		manifest, readErr := readOptionalDataFile(baseDataRoot, "data/dataset.json")
		if readErr != nil {
			return fmt.Errorf("read typed dataset manifest for base refresh: %w", readErr)
		}
		if len(manifest) == 0 {
			return fmt.Errorf("proposal base changed and accepted data has no typed dataset manifest")
		}
	}
	if err = validateProposalPreconditions(proposal, baseDataRoot); err != nil {
		return err
	}
	if err = validateProposalPointer(proposal, baseDataRoot); err != nil {
		return err
	}
	if proposal.Kind == ProposalSelectionExport {
		if err = validateSelectionExportProposal(proposal, proposalRoot, baseDataRoot); err != nil {
			return err
		}
	}
	if proposal.Kind == ProposalModuleEvidence {
		if err = validateModuleEvidenceProposal(proposal, proposalRoot, baseDataRoot); err != nil {
			return err
		}
	}
	if proposal.Kind == ProposalCatalog {
		if err = validateCatalogProposal(proposal, proposalRoot, baseDataRoot); err != nil {
			return err
		}
	}
	if proposal.Kind == ProposalProviderSnapshot {
		if err = validateProviderSnapshotProposal(proposal, proposalRoot, baseDataRoot); err != nil {
			return err
		}
	}
	if proposal.Kind == ProposalModuleObservation {
		if err = validateModuleObservationProposal(proposal, proposalRoot, baseDataRoot); err != nil {
			return err
		}
	}
	if proposal.Kind == ProposalPresentation {
		if err = validatePresentationProposal(proposal, proposalRoot, baseDataRoot); err != nil {
			return err
		}
	}
	if err = validateProposalChanges(proposal, proposalRoot, baseDataRoot); err != nil {
		return err
	}
	parent := filepath.Dir(outputDataRoot)
	if err = os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".proposal-data-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err = copyAcceptedTree(baseDataRoot, staging); err != nil {
		return err
	}
	if err = applyProposalChanges(proposal, proposalRoot, baseDataRoot, staging); err != nil {
		return err
	}
	if err = rebuildTypedIndexes(staging, baseDataRoot, proposalRoot, proposal); err != nil {
		return err
	}
	return swapDirectory(staging, outputDataRoot)
}

func rebuildTypedIndexes(dataRoot, baseDataRoot, proposalRoot string, proposal Proposal) error {
	if err := requireTypedDatasetManifest(dataRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	files, err := buildTypedIndexes(dataRoot)
	if err != nil {
		return err
	}
	changes := make(map[string]ProposalChange, len(proposal.Changes))
	for _, change := range proposal.Changes {
		changes[change.Path] = change
	}
	for canonicalPath, content := range files {
		if err = validateAndWriteRebuiltIndex(dataRoot, baseDataRoot, proposalRoot, canonicalPath, content, changes); err != nil {
			return err
		}
	}
	return nil
}

func requireTypedDatasetManifest(dataRoot string) error {
	_, err := os.Lstat(filepath.Join(dataRoot, "dataset.json"))
	if errors.Is(err, os.ErrNotExist) {
		return os.ErrNotExist
	}
	return err
}

func buildTypedIndexes(dataRoot string) (map[string][]byte, error) {
	records, err := index.ReadRecords(dataRoot, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("rebuild proposal indexes from merged data: %w", err)
	}
	indexes, err := index.Build(records)
	if err != nil {
		return nil, fmt.Errorf("validate rebuilt proposal indexes: %w", err)
	}
	files, err := indexes.Files()
	if err != nil {
		return nil, fmt.Errorf("serialize rebuilt proposal indexes: %w", err)
	}
	return files, nil
}

func validateAndWriteRebuiltIndex(dataRoot, baseDataRoot, proposalRoot, canonicalPath string, content []byte, changes map[string]ProposalChange) error {
	relativePath, err := dataRelativePath(canonicalPath)
	if err != nil {
		return err
	}
	baseContent, err := readOptionalDataFile(baseDataRoot, canonicalPath)
	if err != nil {
		return err
	}
	change, declared := changes[canonicalPath]
	if declared {
		proposalContent, readErr := readProposalPayload(proposalRoot, canonicalPath)
		if readErr != nil {
			return readErr
		}
		if change.Digest != digestBytes(content) || !bytes.Equal(proposalContent, content) {
			return fmt.Errorf("proposal index %s is not rebuilt from the merged data", canonicalPath)
		}
	} else if !bytes.Equal(baseContent, content) {
		return fmt.Errorf("proposal omits rebuilt index path %s", canonicalPath)
	}
	path := filepath.Join(dataRoot, filepath.FromSlash(relativePath))
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

func readProposal(path string) (Proposal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Proposal{}, fmt.Errorf("read proposal envelope: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var proposal Proposal
	if err = decoder.Decode(&proposal); err != nil {
		return Proposal{}, fmt.Errorf("decode proposal envelope: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return Proposal{}, fmt.Errorf("proposal envelope must contain one JSON document")
	}
	return proposal, nil
}

func validateProposal(proposal Proposal) error {
	if err := validateProposalMetadata(proposal); err != nil {
		return err
	}
	if err := validateProposalIdentity(proposal); err != nil {
		return err
	}
	if err := validateProposalChangeSet(proposal); err != nil {
		return err
	}
	return validateProposalOperations(proposal)
}

func validateProposalMetadata(proposal Proposal) error {
	if proposal.SchemaVersion != ProposalSchemaVersion || proposal.RecordType != "proposal" || proposal.Subject == "" || proposal.SourceGeneration == 0 || !isLowerHex(proposal.ExpectedBase, 40) || !isLowerHex(proposal.ContentDigest, 64) || len(proposal.Paths) == 0 || len(proposal.Changes) == 0 {
		return fmt.Errorf("proposal envelope metadata is invalid")
	}
	if err := validateProposalSubject(proposal.Kind, proposal.Subject); err != nil {
		return err
	}
	if proposal.Producer.ID != expectedProposalProducer(proposal.Kind) || proposal.Producer.Version == "" || proposal.Producer.ContractVersion < 1 {
		return fmt.Errorf("proposal producer is not authorized for kind %q", proposal.Kind)
	}
	return nil
}

func validateProposalSubject(kind ProposalKind, subject string) error {
	switch kind {
	case ProposalCatalog:
		return validateLiteralProposalSubject(subject, "catalog")
	case ProposalPresentation:
		return validateLiteralProposalSubject(subject, "presentation")
	case ProposalModuleEvidence:
		return validateModuleEvidenceSubject(subject)
	case ProposalProviderSnapshot:
		return validateProviderProposalSubject(subject)
	case ProposalModuleObservation:
		return validateModuleObservationSubject(subject)
	case ProposalSelectionExport:
		return validateSelectionSubject(subject)
	default:
		return fmt.Errorf("unsupported proposal kind %q", kind)
	}
}

func validateLiteralProposalSubject(subject, expected string) error {
	if subject != expected {
		return fmt.Errorf("proposal subject must be %s", expected)
	}
	return nil
}

func validateModuleEvidenceSubject(subject string) error {
	_, _, err := moduleRevisionSubject(subject)
	return err
}

func validateProviderProposalSubject(subject string) error {
	if !strings.HasPrefix(subject, "core:") {
		_, _, err := parseProviderSubject(subject)
		return err
	}
	version := strings.TrimPrefix(subject, "core:")
	if err := identity.ValidateRelativePath(version, false); err != nil {
		return fmt.Errorf("invalid core-function proposal subject: %w", err)
	}
	return nil
}

func validateModuleObservationSubject(subject string) error {
	moduleID, kind, found := strings.Cut(subject, "@")
	if !found || kind == "" {
		return fmt.Errorf("module observation subject must contain module ID and kind")
	}
	if _, err := identity.ModuleMetadataPath(moduleID); err != nil {
		return err
	}
	if err := identity.ValidateRelativePath(kind, false); err != nil {
		return fmt.Errorf("invalid module observation kind: %w", err)
	}
	return nil
}

func validateSelectionSubject(subject string) error {
	_, _, _, err := selectionSubject(subject)
	return err
}

func validateProposalIdentity(proposal Proposal) error {
	if err := identity.ValidateRecordID("proposal", proposal.ProposalID); err != nil {
		return err
	}
	computedID, err := identity.ID("proposal", ProposalSchemaVersion, proposalIdentityFor(proposal))
	if err != nil || computedID != proposal.ProposalID {
		return fmt.Errorf("proposal ID does not match its content")
	}
	contentDigest, err := proposalChangesDigest(proposal.Changes)
	if err != nil || contentDigest != proposal.ContentDigest {
		return fmt.Errorf("proposal content digest does not match its change set")
	}
	return nil
}

func validateProposalChangeSet(proposal Proposal) error {
	paths, err := identity.SortedSet(proposal.Paths)
	if err != nil {
		return fmt.Errorf("proposal paths are invalid: %w", err)
	}
	changePaths := make([]string, 0, len(proposal.Changes))
	for _, change := range proposal.Changes {
		if err = validateProposalChange(proposal, change); err != nil {
			return err
		}
		changePaths = append(changePaths, change.Path)
	}
	changePaths, err = identity.SortedSet(changePaths)
	if err != nil || !equalProposalStrings(paths, changePaths) {
		return fmt.Errorf("proposal paths do not exactly match its unique changes")
	}
	return validateProposalPreconditionShapes(proposal.Preconditions)
}

func validateProposalChange(proposal Proposal, change ProposalChange) error {
	if err := identity.ValidateRelativePath(change.Path, false); err != nil || !strings.HasPrefix(change.Path, "data/") {
		return fmt.Errorf("proposal contains an invalid canonical data path %q", change.Path)
	}
	if !isLowerHex(change.Digest, 64) {
		return fmt.Errorf("proposal change digest is invalid for %s", change.Path)
	}
	if change.RecordID != "" {
		if err := validateAnyProposalRecordID(change.RecordID); err != nil {
			return fmt.Errorf("proposal change record ID is invalid for %s: %w", change.Path, err)
		}
	}
	if change.Operation != "add_immutable" && change.Operation != "set_pointer" && change.Operation != "remove_pointer" {
		return fmt.Errorf("proposal change operation %q is unsupported", change.Operation)
	}
	if !proposalPathAllowed(proposal.Kind, proposal.Subject, change.Path) {
		return fmt.Errorf("proposal kind %q cannot change %s", proposal.Kind, change.Path)
	}
	return nil
}

func validateAnyProposalRecordID(recordID string) error {
	kindEnd := strings.LastIndex(recordID, "-")
	if kindEnd < 1 {
		return fmt.Errorf("record ID lacks a kind prefix")
	}
	return identity.ValidateRecordID(recordID[:kindEnd], recordID)
}

func validateProposalPreconditionShapes(preconditions map[string]string) error {
	for recordPath, digest := range preconditions {
		if err := identity.ValidateRelativePath(recordPath, false); err != nil || !strings.HasPrefix(recordPath, "data/") || (digest != "absent" && !isLowerHex(digest, 64)) {
			return fmt.Errorf("proposal precondition is invalid for %s", recordPath)
		}
	}
	return nil
}

func validateProposalOperations(proposal Proposal) error {
	pointerPath := proposalPointerPath(proposal)
	pointerChanges := 0
	for _, change := range proposal.Changes {
		if err := validateProposalOperation(proposal, change, pointerPath); err != nil {
			return err
		}
		if change.Operation == "set_pointer" || change.Operation == "remove_pointer" {
			pointerChanges++
		}
	}
	maximumPointerChanges := len(proposal.Changes)
	if pointerChanges > maximumPointerChanges {
		return fmt.Errorf("proposal changes too many subject pointers")
	}
	return nil
}

func validateProposalOperation(proposal Proposal, change ProposalChange, pointerPath string) error {
	if isGeneratedProposalIndexPath(change.Path) && change.Operation != "set_pointer" {
		return fmt.Errorf("generated index changes must replace the complete index pointer")
	}
	if (change.Operation == "set_pointer" || change.Operation == "remove_pointer") && !proposalPointerChangeAllowed(proposal, pointerPath, change.Path) {
		return fmt.Errorf("proposal kind %q cannot change pointer %s", proposal.Kind, change.Path)
	}
	if proposal.Kind == ProposalModuleEvidence && change.Operation != "add_immutable" && !proposalPointerChangeAllowed(proposal, pointerPath, change.Path) {
		return fmt.Errorf("module evidence proposals may add history and update history indexes only")
	}
	if change.Operation == "remove_pointer" && (proposal.Kind == ProposalSelectionExport || proposal.Kind == ProposalCatalog) {
		return fmt.Errorf("proposal kind %q preserves history rather than removing pointers", proposal.Kind)
	}
	if proposal.Kind == ProposalCatalog && change.Operation == "set_pointer" && change.Path != pointerPath && !isGeneratedProposalIndexPath(change.Path) {
		if _, hasPrecondition := proposal.Preconditions[change.Path]; !hasPrecondition {
			return fmt.Errorf("catalog module pointer %s requires an explicit precondition", change.Path)
		}
	}
	return nil
}

func proposalPointerChangeAllowed(proposal Proposal, expectedPath, changePath string) bool {
	if changePath == expectedPath && expectedPath != "" {
		return true
	}
	if isGeneratedProposalIndexPath(changePath) {
		return true
	}
	if proposal.Kind == ProposalCatalog {
		return catalogModulePointerPath(changePath)
	}
	if proposal.Kind != ProposalSelectionExport {
		if proposal.Kind != ProposalModuleEvidence {
			return false
		}
		moduleID, _, err := moduleRevisionSubject(proposal.Subject)
		if err != nil {
			return false
		}
		moduleIndexPath, err := identity.ModuleIndexPath(moduleID)
		return err == nil && changePath == moduleIndexPath
	}
	_, _, contextID, err := selectionSubject(proposal.Subject)
	if err != nil {
		return false
	}
	trackPath, err := identity.TrackIndexPath(contextID)
	return err == nil && changePath == trackPath
}

func catalogModulePointerPath(recordPath string) bool {
	parts := strings.Split(recordPath, "/")
	if len(parts) != 5 || parts[0] != "data" || parts[1] != "modules" {
		return false
	}
	moduleID := parts[3]
	metadataPath, err := identity.ModuleMetadataPath(moduleID)
	if err == nil && recordPath == metadataPath {
		return true
	}
	versionsPath, err := identity.ModuleVersionsPath(moduleID)
	return err == nil && recordPath == versionsPath
}

func validateProposalPreconditions(proposal Proposal, baseDataRoot string) error {
	for relativePath, expected := range proposal.Preconditions {
		if expected == "absent" {
			exists, existsErr := regularDataFileExists(baseDataRoot, relativePath)
			if existsErr != nil {
				return fmt.Errorf("read proposal precondition %s: %w", relativePath, existsErr)
			}
			if exists {
				return fmt.Errorf("proposal precondition changed for %s", relativePath)
			}
			continue
		}
		data, err := readOptionalDataFile(baseDataRoot, relativePath)
		if err != nil {
			return fmt.Errorf("read proposal precondition %s: %w", relativePath, err)
		}
		if digestBytes(data) != expected {
			return fmt.Errorf("proposal precondition changed for %s", relativePath)
		}
	}
	return nil
}

func regularFileExists(root, relativePath string) (bool, error) {
	_, err := readRegularFile(root, relativePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func regularDataFileExists(root, canonicalPath string) (bool, error) {
	dataPath, err := dataRelativePath(canonicalPath)
	if err != nil {
		return false, err
	}
	_, err = readRegularFile(root, dataPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func readOptionalDataFile(root, canonicalPath string) ([]byte, error) {
	dataPath, err := dataRelativePath(canonicalPath)
	if err != nil {
		return nil, err
	}
	return readOptionalRegularFile(root, dataPath)
}

func dataRelativePath(canonicalPath string) (string, error) {
	if !strings.HasPrefix(canonicalPath, "data/") {
		return "", fmt.Errorf("canonical proposal path must be beneath data/")
	}
	relativePath := strings.TrimPrefix(canonicalPath, "data/")
	if err := identity.ValidateRelativePath(relativePath, false); err != nil {
		return "", err
	}
	return relativePath, nil
}

func ensureProposalRootsSeparated(baseDataRoot, outputDataRoot string) error {
	if baseDataRoot == "" || outputDataRoot == "" {
		return fmt.Errorf("base and output data roots are required")
	}
	base, err := filepath.Abs(baseDataRoot)
	if err != nil {
		return err
	}
	output, err := filepath.Abs(outputDataRoot)
	if err != nil {
		return err
	}
	if pathContains(base, output) || pathContains(output, base) {
		return fmt.Errorf("proposal output root must be separate from accepted data")
	}
	return nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func validateProposalPointer(proposal Proposal, baseDataRoot string) error {
	current, err := currentProposalPointer(proposal, baseDataRoot)
	if err != nil {
		return err
	}
	if !equalOptionalStrings(current, proposal.ExpectedPointer) {
		return fmt.Errorf("proposal expected subject pointer does not match current data")
	}
	return nil
}

func validateProposalChanges(proposal Proposal, proposalRoot, baseDataRoot string) error {
	for _, change := range proposal.Changes {
		if change.Operation == "remove_pointer" {
			current, err := readOptionalDataFile(baseDataRoot, change.Path)
			if err != nil || len(current) == 0 || digestBytes(current) != change.Digest {
				return fmt.Errorf("proposal removal does not match current pointer %s", change.Path)
			}
			continue
		}
		data, err := readProposalPayload(proposalRoot, change.Path)
		if err != nil {
			return err
		}
		if digestBytes(data) != change.Digest {
			return fmt.Errorf("proposal payload digest does not match %s", change.Path)
		}
		if err = validateProposalRecordType(proposal.Kind, data); err != nil {
			return fmt.Errorf("validate proposal payload %s: %w", change.Path, err)
		}
		if err = validateProposalPayloadPath(proposal, change, data); err != nil {
			return fmt.Errorf("validate proposal record path %s: %w", change.Path, err)
		}
		if change.Operation == "add_immutable" {
			current, readErr := readOptionalDataFile(baseDataRoot, change.Path)
			if readErr != nil {
				return readErr
			}
			if len(current) > 0 && !bytes.Equal(current, data) {
				return fmt.Errorf("immutable proposal path already contains different data: %s", change.Path)
			}
		}
	}
	return nil
}

func validateProposalPayloadPath(proposal Proposal, change ProposalChange, data []byte) error {
	var header struct {
		RecordType string `json:"record_type"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return err
	}
	if isGeneratedIndexRecordType(header.RecordType) {
		return validateGeneratedIndexPayloadPath(change, data, header.RecordType)
	}
	switch proposal.Kind {
	case ProposalModuleEvidence:
		return validateModuleEvidencePayloadPath(proposal, change, data, header.RecordType)
	case ProposalCatalog:
		return validateCatalogPayloadPath(change, data, header.RecordType)
	case ProposalProviderSnapshot:
		return validateProviderPayloadPath(proposal, change, data, header.RecordType)
	case ProposalModuleObservation:
		return validateModuleObservationPayloadPath(proposal, change, data, header.RecordType)
	case ProposalSelectionExport:
		return validateSelectionPayloadPath(proposal, change, data, header.RecordType)
	case ProposalPresentation:
		return validatePresentationPayloadPath(change, data, header.RecordType)
	}
	return fmt.Errorf("unsupported proposal kind %q", proposal.Kind)
}

func validateModuleEvidencePayloadPath(proposal Proposal, change ProposalChange, data []byte, recordType string) error {
	if recordType == "dimension_definition" {
		var definition assessment.DimensionDefinition
		if err := decodeOne(data, &definition); err != nil {
			return err
		}
		path, err := identity.DefinitionPath(definition.DefinitionID)
		if err != nil || change.Path != path || change.RecordID != definition.DefinitionID || change.Operation != "add_immutable" {
			return fmt.Errorf("dimension definition path or identity is invalid")
		}
		return validateModuleEvidenceRecordContent(recordType, definition.DefinitionID, data, "", "")
	}
	moduleID, sourceSHA, err := moduleRevisionSubject(proposal.Subject)
	if err != nil {
		return err
	}
	if recordType == "module_index" {
		path, _ := identity.ModuleIndexPath(moduleID)
		var value index.ModuleIndex
		if err = json.Unmarshal(data, &value); err != nil {
			return err
		}
		if err = validateProposalIndexID(moduleIndexKind, value, value.IndexID); err != nil {
			return err
		}
		return requireProposalPointer(path, change, value.IndexID, "module history index")
	}
	if recordType == "source_revision" {
		path, pathErr := identity.ModuleSourcePath(moduleID, sourceSHA)
		if pathErr != nil || change.Path != path || change.RecordID != "" {
			return fmt.Errorf("source revision path or identity is invalid")
		}
		return nil
	}
	kind := map[string]string{"facts": "facts", "provenance": "provenance", "dimension_result": "dimension-result", "analysis": recordType}[recordType]
	path, err := identity.ModuleRecordPath(moduleID, sourceSHA, kind, change.RecordID)
	if err != nil || change.Operation != "add_immutable" || change.Path != path {
		return fmt.Errorf("module evidence record is not at its canonical path")
	}
	return nil
}

func validateCatalogPayloadPath(change ProposalChange, data []byte, recordType string) error {
	switch recordType {
	case "catalog_index":
		var value index.CatalogIndex
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if err := validateProposalIndexID(catalogIndexKind, value, value.IndexID); err != nil {
			return err
		}
		return requireProposalPointer(catalogPointerPath, change, value.IndexID, "catalog index")
	case "module_metadata":
		var value CatalogModuleMetadata
		if err := decodeOne(data, &value); err != nil {
			return err
		}
		if err := value.Validate(); err != nil {
			return err
		}
		path, err := identity.ModuleMetadataPath(value.ModuleID)
		if err != nil || change.Path != path || change.Operation != "set_pointer" {
			return fmt.Errorf("module metadata path is invalid")
		}
	case "module_versions_index":
		var value index.ModuleVersionsIndexRecord
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		path, err := identity.ModuleVersionsPath(value.ModuleID)
		if err != nil || change.Path != path || change.Operation != "set_pointer" {
			return fmt.Errorf("module versions index path is invalid")
		}
	case "module_version_observation":
		var value index.VersionObservationRecord
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		path, err := identity.ModuleVersionObservationPath(value.ModuleID, value.ID)
		if err != nil || change.Path != path || change.RecordID != value.ID || change.Operation != "add_immutable" {
			return fmt.Errorf("module version observation path is invalid")
		}
	}
	return nil
}

func validateProviderPayloadPath(proposal Proposal, change ProposalChange, data []byte, recordType string) error {
	if strings.HasPrefix(proposal.Subject, "core:") {
		return validateCorePayloadPath(proposal.Subject, change, data, recordType)
	}
	address, version, err := parseProviderSubject(proposal.Subject)
	if err != nil {
		return err
	}
	if recordType == "provider_snapshot" {
		var value assessment.ProviderSnapshot
		if err = json.Unmarshal(data, &value); err != nil {
			return err
		}
		path, pathErr := identity.ProviderSnapshotManifestPath(address, version, value.SnapshotID)
		if pathErr != nil || change.Path != path || value.ProviderAddress != address || value.ProviderVersion != version || change.RecordID != value.SnapshotID {
			return fmt.Errorf("provider snapshot path or subject is invalid")
		}
		return nil
	}
	if recordType == "provider_index" {
		path, _ := identity.ProviderIndexPath(address)
		var value index.ProviderIndex
		if err = json.Unmarshal(data, &value); err != nil {
			return err
		}
		if err = validateProposalIndexID(providerIndexKind, value, value.IndexID); err != nil {
			return err
		}
		return requireProposalPointer(path, change, value.IndexID, "provider index")
	}
	return fmt.Errorf("provider payload record type %q is unsupported", recordType)
}

func validateCorePayloadPath(subject string, change ProposalChange, data []byte, recordType string) error {
	version := strings.TrimPrefix(subject, "core:")
	if recordType == "core_function_index" {
		var value coreFunctionIndex
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		return requireProposalPointer(coreFunctionDataPrefix+version+"/"+generatedIndexName, change, value.IndexID, "core function index")
	}
	if recordType != "core_function_snapshot" {
		return fmt.Errorf("core snapshot proposal contains a non-core record")
	}
	var value struct {
		SnapshotID string `json:"snapshot_id"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	path, err := identity.CoreFunctionSnapshotFilePath(version, value.SnapshotID)
	if err != nil || change.Path != path || change.RecordID != value.SnapshotID {
		return fmt.Errorf("core function snapshot path is invalid")
	}
	return nil
}

func validateModuleObservationPayloadPath(proposal Proposal, change ProposalChange, data []byte, recordType string) error {
	moduleID, observationKind, found := strings.Cut(proposal.Subject, "@")
	if !found || observationKind == "" {
		return fmt.Errorf("module observation subject must include its observation kind")
	}
	if recordType == "module_observation_index" {
		path := moduleObservationDataPrefix + moduleID + "/" + generatedIndexName
		var value moduleObservationIndex
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		return requireProposalPointer(path, change, value.IndexID, "module observation index")
	}
	var value struct {
		ObservationID string `json:"observation_id"`
		ModuleID      string `json:"module_id"`
		Kind          string `json:"kind"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	path, err := identity.ModuleObservationPath(moduleID, value.ObservationID)
	if err != nil || change.Path != path || change.RecordID != value.ObservationID || value.ModuleID != moduleID || value.Kind != observationKind {
		return fmt.Errorf("module observation path or subject is invalid")
	}
	return nil
}

func validateSelectionPayloadPath(proposal Proposal, change ProposalChange, data []byte, recordType string) error {
	moduleID, release, contextID, err := selectionSubject(proposal.Subject)
	if err != nil {
		return err
	}
	switch recordType {
	case "selection_receipt":
		var value selectionExportReceipt
		if err = json.Unmarshal(data, &value); err != nil {
			return err
		}
		if value.AVMReleaseVersion != release {
			return fmt.Errorf("selection receipt release does not match proposal subject")
		}
		path, pathErr := identity.SelectionPath(moduleID, contextID, value.ID)
		if pathErr != nil || change.Path != path || change.RecordID != value.ID || change.Operation != "add_immutable" {
			return fmt.Errorf("selection receipt path or identity is invalid")
		}
	case "module_index":
		path, _ := identity.ModuleIndexPath(moduleID)
		var value index.ModuleIndex
		if err = json.Unmarshal(data, &value); err != nil {
			return err
		}
		if err = validateProposalIndexID(moduleIndexKind, value, value.IndexID); err != nil {
			return err
		}
		return requireProposalPointer(path, change, value.IndexID, "module selection index")
	case "track_index":
		path, _ := identity.TrackIndexPath(contextID)
		var value index.TrackIndex
		if err = json.Unmarshal(data, &value); err != nil {
			return err
		}
		if err = validateProposalIndexID(trackIndexKind, value, value.IndexID); err != nil {
			return err
		}
		return requireProposalPointer(path, change, value.IndexID, "track selection index")
	}
	return nil
}

func validatePresentationPayloadPath(change ProposalChange, data []byte, recordType string) error {
	if recordType == "presentation_index" {
		var value index.PresentationIndex
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if err := validateProposalIndexID(presentationIndexKind, value, value.IndexID); err != nil {
			return err
		}
		return requireProposalPointer(identity.PresentationIndexPath(), change, value.IndexID, "presentation index")
	}
	var value struct {
		ID string `json:"presentation_id"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	path, err := identity.PresentationPath(value.ID)
	if err != nil || change.Path != path || change.RecordID != value.ID || change.Operation != "add_immutable" {
		return fmt.Errorf("presentation snapshot path or identity is invalid")
	}
	return nil
}

func requireProposalPointer(path string, change ProposalChange, recordID, description string) error {
	if change.Path != path || change.Operation != "set_pointer" || change.RecordID != recordID {
		return fmt.Errorf("%s path or identity is invalid", description)
	}
	return nil
}

func applyProposalChanges(proposal Proposal, proposalRoot, baseDataRoot, staging string) error {
	for _, change := range proposal.Changes {
		relativePath, err := dataRelativePath(change.Path)
		if err != nil {
			return err
		}
		destination := filepath.Join(staging, filepath.FromSlash(relativePath))
		if change.Operation == "remove_pointer" {
			if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		current, err := readOptionalDataFile(baseDataRoot, change.Path)
		if err != nil {
			return err
		}
		data, err := readProposalPayload(proposalRoot, change.Path)
		if err != nil {
			return err
		}
		if change.Operation == "add_immutable" && bytes.Equal(current, data) {
			continue
		}
		if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err = os.WriteFile(destination, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func validateProposalRecordType(kind ProposalKind, data []byte) error {
	var record struct {
		RecordType string `json:"record_type"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	allowed := map[ProposalKind]map[string]struct{}{
		ProposalModuleEvidence:    setOf("source_revision", "facts", "provenance", "dimension_definition", "dimension_result", "analysis", "module_index", "dataset_manifest", "dimension_dependency_index"),
		ProposalCatalog:           setOf("catalog_index", "module_metadata", "module_versions_index", "module_version_observation", "module_index", "track_index", "provider_index", "dimension_dependency_index", "presentation_index", "dataset_manifest"),
		ProposalProviderSnapshot:  setOf("provider_snapshot", "core_function_snapshot", "core_function_index", "provider_index", "module_index", "track_index", "catalog_index", "dimension_dependency_index", "presentation_index", "dataset_manifest"),
		ProposalModuleObservation: setOf("module_observation", "module_observation_index", "module_index", "track_index", "provider_index", "catalog_index", "dimension_dependency_index", "presentation_index", "dataset_manifest"),
		ProposalSelectionExport:   setOf("selection_receipt", "module_index", "track_index", "provider_index", "catalog_index", "dimension_dependency_index", "presentation_index", "dataset_manifest"),
		ProposalPresentation:      setOf("presentation_snapshot", "presentation_index", "module_index", "track_index", "provider_index", "catalog_index", "dimension_dependency_index", "dataset_manifest"),
	}
	if _, exists := allowed[kind][record.RecordType]; !exists {
		return fmt.Errorf("record type %q is not allowed for %s", record.RecordType, kind)
	}
	return nil
}

func proposalPathAllowed(kind ProposalKind, subject, recordPath string) bool {
	if isGeneratedProposalIndexPath(recordPath) {
		return true
	}
	switch kind {
	case ProposalModuleEvidence:
		return moduleEvidencePathAllowed(subject, recordPath)
	case ProposalCatalog:
		return catalogPathAllowed(recordPath)
	case ProposalProviderSnapshot:
		prefix, err := providerSubjectPrefix(subject)
		return err == nil && strings.HasPrefix(recordPath, prefix)
	case ProposalModuleObservation:
		return moduleObservationPathAllowed(subject, recordPath)
	case ProposalSelectionExport:
		return selectionExportPathAllowed(subject, recordPath)
	case ProposalPresentation:
		return strings.HasPrefix(recordPath, "data/presentation/")
	default:
		return false
	}
}

func moduleEvidencePathAllowed(subject, recordPath string) bool {
	if strings.HasPrefix(recordPath, "data/assessment/definitions/") {
		return true
	}
	moduleID, sourceSHA, err := moduleRevisionSubject(subject)
	if err != nil {
		return false
	}
	moduleIndexPath, err := identity.ModuleIndexPath(moduleID)
	if err == nil && recordPath == moduleIndexPath {
		return true
	}
	moduleRoot, err := identity.ModuleMetadataPath(moduleID)
	return err == nil && strings.HasPrefix(recordPath, strings.TrimSuffix(filepath.ToSlash(filepath.Dir(moduleRoot)), "/")+"/revisions/"+sourceSHA+"/")
}

func catalogPathAllowed(recordPath string) bool {
	if recordPath == catalogPointerPath {
		return true
	}
	parts := strings.Split(recordPath, "/")
	if len(parts) == 5 && parts[0] == "data" && parts[1] == "modules" {
		moduleID := parts[3]
		metadataPath, err := identity.ModuleMetadataPath(moduleID)
		versionsPath, versionsErr := identity.ModuleVersionsPath(moduleID)
		return err == nil && versionsErr == nil && (recordPath == metadataPath || recordPath == versionsPath)
	}
	return strings.HasPrefix(recordPath, moduleObservationDataPrefix) && !strings.Contains(recordPath, "/../")
}

func generatedIndexRecordType(recordPath string) string {
	switch recordPath {
	case catalogPointerPath:
		return "catalog_index"
	case datasetManifestPath:
		return "dataset_manifest"
	case identity.DimensionDependencyIndexPath():
		return "dimension_dependency_index"
	case identity.PresentationIndexPath():
		return "presentation_index"
	}
	parts := strings.Split(recordPath, "/")
	return nestedGeneratedIndexRecordType(parts, recordPath)
}

func nestedGeneratedIndexRecordType(parts []string, recordPath string) string {
	if len(parts) < 2 || parts[0] != "data" || parts[len(parts)-1] != generatedIndexName {
		return ""
	}
	switch parts[1] {
	case "modules":
		return generatedModuleIndexType(parts, recordPath)
	case "tracks":
		return generatedTrackIndexType(parts, recordPath)
	case "providers":
		return generatedProviderIndexType(parts, recordPath)
	}
	return ""
}

func generatedModuleIndexType(parts []string, recordPath string) string {
	if len(parts) != 5 {
		return ""
	}
	path, err := identity.ModuleIndexPath(parts[3])
	if err == nil && path == recordPath {
		return "module_index"
	}
	return ""
}

func generatedTrackIndexType(parts []string, recordPath string) string {
	if len(parts) < 4 {
		return ""
	}
	contextID := strings.Join(parts[2:len(parts)-1], "/")
	path, err := identity.TrackIndexPath(contextID)
	if err == nil && path == recordPath {
		return "track_index"
	}
	return ""
}

func generatedProviderIndexType(parts []string, recordPath string) string {
	if len(parts) != 6 {
		return ""
	}
	address := strings.Join(parts[2:5], "/")
	path, err := identity.ProviderIndexPath(address)
	if err == nil && path == recordPath {
		return "provider_index"
	}
	return ""
}

func isGeneratedProposalIndexPath(recordPath string) bool {
	return generatedIndexRecordType(recordPath) != ""
}

func isGeneratedIndexRecordType(recordType string) bool {
	switch recordType {
	case "catalog_index", "dataset_manifest", "dimension_dependency_index", "presentation_index", "module_index", "track_index", "provider_index":
		return true
	default:
		return false
	}
}

func validateGeneratedIndexPayloadPath(change ProposalChange, data []byte, recordType string) error {
	if generatedIndexRecordType(change.Path) != recordType || change.Operation != "set_pointer" {
		return fmt.Errorf("generated index path or operation does not match record type %q", recordType)
	}
	switch recordType {
	case "catalog_index":
		var record index.CatalogIndex
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		return requireContentAddressedIndex(catalogIndexKind, change, record.IndexID, record)
	case "module_index":
		var record index.ModuleIndex
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		return requireContentAddressedIndex(moduleIndexKind, change, record.IndexID, record)
	case "track_index":
		var record index.TrackIndex
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		return requireContentAddressedIndex(trackIndexKind, change, record.IndexID, record)
	case "provider_index":
		var record index.ProviderIndex
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		return requireContentAddressedIndex(providerIndexKind, change, record.IndexID, record)
	case "dimension_dependency_index":
		var record index.DimensionDependencyIndex
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		return requireContentAddressedIndex(dimensionDependencyIndexKind, change, record.IndexID, record)
	case "presentation_index":
		var record index.PresentationIndex
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		return requireContentAddressedIndex(presentationIndexKind, change, record.IndexID, record)
	case "dataset_manifest":
		var record index.DatasetManifest
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		if change.RecordID != record.DatasetID {
			return fmt.Errorf("dataset manifest path identity does not match its dataset ID")
		}
		return identity.ValidateRecordID("dataset", record.DatasetID)
	default:
		return fmt.Errorf("unsupported generated index record type %q", recordType)
	}
}

func requireContentAddressedIndex(recordKind string, change ProposalChange, indexID string, value any) error {
	if change.RecordID != indexID {
		return fmt.Errorf("generated index record ID does not match index_id")
	}
	return validateProposalIndexID(recordKind, value, indexID)
}

func moduleObservationPathAllowed(subject, recordPath string) bool {
	moduleID := subjectModuleID(subject)
	path, err := identity.ModuleObservationPath(moduleID, "observation-"+strings.Repeat("0", 64))
	indexPath := moduleObservationDataPrefix + moduleID + "/" + generatedIndexName
	return err == nil && (strings.HasPrefix(recordPath, filepath.ToSlash(filepath.Dir(path))+
		"/") || recordPath == indexPath)
}

func selectionExportPathAllowed(subject, recordPath string) bool {
	moduleID, _, contextID, err := selectionSubject(subject)
	if err != nil {
		return false
	}
	selectionRoot := "data/selections/" + moduleID + "/" + contextID + "/"
	moduleIndexPath, _ := identity.ModuleIndexPath(moduleID)
	trackIndexPath, _ := identity.TrackIndexPath(contextID)
	return strings.HasPrefix(recordPath, selectionRoot) || recordPath == moduleIndexPath || recordPath == trackIndexPath
}

func moduleRevisionSubject(subject string) (string, string, error) {
	moduleID, sourceSHA, found := strings.Cut(subject, "@")
	if !found || !isLowerHex(sourceSHA, 40) {
		return "", "", fmt.Errorf("module evidence subject must be module@source-sha")
	}
	if _, err := identity.ModuleMetadataPath(moduleID); err != nil {
		return "", "", err
	}
	return moduleID, sourceSHA, nil
}

func proposalPointerPath(proposal Proposal) string {
	switch proposal.Kind {
	case ProposalModuleEvidence:
		path, _ := identity.ModuleIndexPath(subjectModuleID(proposal.Subject))
		return path
	case ProposalModuleObservation:
		return moduleObservationDataPrefix + subjectModuleID(proposal.Subject) + "/" + generatedIndexName
	case ProposalCatalog:
		return catalogPointerPath
	case ProposalProviderSnapshot:
		if strings.HasPrefix(proposal.Subject, "core:") {
			return coreFunctionDataPrefix + strings.TrimPrefix(proposal.Subject, "core:") + "/" + generatedIndexName
		}
		address, _, err := parseProviderSubject(proposal.Subject)
		if err != nil {
			return ""
		}
		path, _ := identity.ProviderIndexPath(address)
		return path
	case ProposalSelectionExport:
		moduleID, _, _, err := selectionSubject(proposal.Subject)
		if err != nil {
			return ""
		}
		path, _ := identity.ModuleIndexPath(moduleID)
		return path
	case ProposalPresentation:
		return identity.PresentationIndexPath()
	default:
		return ""
	}
}

func currentProposalPointer(proposal Proposal, dataRoot string) (*string, error) {
	path := proposalPointerPath(proposal)
	if path == "" {
		return nil, fmt.Errorf("proposal subject is invalid for kind %q", proposal.Kind)
	}
	data, err := readOptionalDataFile(dataRoot, path)
	if err != nil || len(data) == 0 {
		return nil, err
	}
	switch proposal.Kind {
	case ProposalCatalog:
		return readIndexPointer(data)
	case ProposalProviderSnapshot:
		if strings.HasPrefix(proposal.Subject, "core:") {
			var record coreFunctionIndex
			if err = json.Unmarshal(data, &record); err != nil {
				return nil, err
			}
			return optionalString(record.IndexID), nil
		}
		return readIndexPointer(data)
	case ProposalPresentation:
		var record struct {
			CurrentID *string `json:"current_id"`
		}
		if err = json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		return record.CurrentID, nil
	case ProposalModuleObservation:
		var record moduleObservationIndex
		if err = json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		return record.CurrentID, nil
	case ProposalSelectionExport:
		_, release, contextID, _ := selectionSubject(proposal.Subject)
		var record index.ModuleIndex
		if err = json.Unmarshal(data, &record); err != nil {
			return nil, err
		}
		for _, selection := range record.CurrentSelections {
			if selection.AVMReleaseVersion == release && selection.ContextID == contextID {
				return optionalString(selection.SelectionID), nil
			}
		}
		return nil, nil
	default:
		return readIndexPointer(data)
	}
}

func readIndexPointer(data []byte) (*string, error) {
	var record struct {
		IndexID string `json:"index_id"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	return optionalString(record.IndexID), nil
}

func validateProposalIndexID(recordKind string, record any, actualID string) error {
	expectedID, err := proposalIndexID(recordKind, record)
	if err != nil {
		return err
	}
	if actualID != expectedID {
		return fmt.Errorf("%s content identity does not match its index_id", recordKind)
	}
	return nil
}

func proposalIndexID(recordKind string, record any) (string, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &fields); err != nil {
		return "", err
	}
	delete(fields, "index_id")
	return identity.ID(recordKind, 1, fields)
}

func validateSelectionExportProposal(proposal Proposal, proposalRoot, baseDataRoot string) error {
	moduleID, release, contextID, err := selectionSubject(proposal.Subject)
	if err != nil {
		return err
	}
	moduleIndexPath, err := identity.ModuleIndexPath(moduleID)
	if err != nil {
		return err
	}
	trackIndexPath, err := identity.TrackIndexPath(contextID)
	if err != nil {
		return err
	}
	parts, err := readSelectionProposalParts(proposal, proposalRoot, moduleID, contextID, moduleIndexPath, trackIndexPath)
	if err != nil {
		return err
	}
	if err = validateSelectionReceipt(*parts.receipt, proposal, moduleID, release, contextID); err != nil {
		return err
	}
	currentModuleIndex, err := loadModuleIndex(baseDataRoot, moduleIndexPath)
	if err != nil {
		return err
	}
	indexedReceipt := indexSelectionReceipt(*parts.receipt)
	if err = validateSelectionModuleIndex(currentModuleIndex, *parts.moduleIndex, release, contextID, indexedReceipt); err != nil {
		return err
	}
	if err = validateTrackIndexExtension(baseDataRoot, trackIndexPath, *parts.trackIndex, moduleID, release, contextID, indexedReceipt); err != nil {
		return err
	}
	return validateSelectionEvidence(baseDataRoot, *parts.receipt)
}

type selectionProposalParts struct {
	receipt     *selectionExportReceipt
	moduleIndex *index.ModuleIndex
	trackIndex  *index.TrackIndex
}

func readSelectionProposalParts(proposal Proposal, proposalRoot, moduleID, contextID, moduleIndexPath, trackIndexPath string) (selectionProposalParts, error) {
	var parts selectionProposalParts
	for _, change := range proposal.Changes {
		if err := readSelectionProposalChange(&parts, proposalRoot, change, moduleID, contextID, moduleIndexPath, trackIndexPath); err != nil {
			return parts, err
		}
	}
	if parts.receipt == nil || parts.moduleIndex == nil || parts.trackIndex == nil {
		return parts, fmt.Errorf("selection export requires one receipt and both module and track index updates")
	}
	return parts, nil
}

func readSelectionProposalChange(parts *selectionProposalParts, proposalRoot string, change ProposalChange, moduleID, contextID, moduleIndexPath, trackIndexPath string) error {
	if change.Path == moduleIndexPath {
		value, err := readSelectionModuleIndex(proposalRoot, change)
		if err != nil {
			return err
		}
		parts.moduleIndex = value
		return nil
	}
	if change.Path == trackIndexPath {
		value, err := readSelectionTrackIndex(proposalRoot, change)
		if err != nil {
			return err
		}
		parts.trackIndex = value
		return nil
	}
	if !isSelectionReceiptChange(change, moduleID, contextID) {
		return nil
	}
	if parts.receipt != nil {
		return fmt.Errorf("selection export contains multiple receipts for one subject")
	}
	value, err := readSelectionReceipt(proposalRoot, change, moduleID, contextID)
	if err != nil {
		return err
	}
	parts.receipt = value
	return nil
}

func readSelectionModuleIndex(root string, change ProposalChange) (*index.ModuleIndex, error) {
	if change.Operation != "set_pointer" {
		return nil, fmt.Errorf("selection export must update its module index pointer")
	}
	data, err := readProposalPayload(root, change.Path)
	if err != nil {
		return nil, err
	}
	var value index.ModuleIndex
	if err = decodeOne(data, &value); err != nil {
		return nil, fmt.Errorf("decode proposed module index: %w", err)
	}
	return &value, nil
}

func readSelectionTrackIndex(root string, change ProposalChange) (*index.TrackIndex, error) {
	if change.Operation != "set_pointer" {
		return nil, fmt.Errorf("selection export must update its track index pointer")
	}
	data, err := readProposalPayload(root, change.Path)
	if err != nil {
		return nil, err
	}
	var value index.TrackIndex
	if err = decodeOne(data, &value); err != nil {
		return nil, fmt.Errorf("decode proposed track index: %w", err)
	}
	return &value, nil
}

func isSelectionReceiptChange(change ProposalChange, moduleID, contextID string) bool {
	return strings.HasPrefix(change.Path, "data/selections/"+moduleID+"/"+contextID+"/") && change.Operation == "add_immutable"
}

func readSelectionReceipt(root string, change ProposalChange, moduleID, contextID string) (*selectionExportReceipt, error) {
	data, err := readProposalPayload(root, change.Path)
	if err != nil {
		return nil, err
	}
	var value selectionExportReceipt
	if err = decodeOne(data, &value); err != nil {
		return nil, fmt.Errorf("decode proposed selection receipt: %w", err)
	}
	wantPath, pathErr := identity.SelectionPath(moduleID, contextID, value.ID)
	if pathErr != nil || wantPath != change.Path || change.RecordID != value.ID {
		return nil, fmt.Errorf("selection receipt path identity is invalid")
	}
	return &value, nil
}

func validateSelectionReceipt(receipt selectionExportReceipt, proposal Proposal, moduleID, release, contextID string) error {
	if receipt.SchemaVersion != 3 || receipt.RecordType != "selection_receipt" || receipt.ModuleID != moduleID || receipt.AVMReleaseVersion != release || receipt.ContextID != contextID || receipt.Sequence == 0 || receipt.DesiredGeneration == 0 || receipt.DesiredGeneration != proposal.SourceGeneration || receipt.SelectedAt.IsZero() || !isLowerHex(receipt.InputFingerprint, 64) || !isLowerHex(receipt.EvidenceCommit, 40) {
		return fmt.Errorf("selection receipt does not match its proposal subject or generation")
	}
	if err := identity.ValidateRecordID("selection", receipt.ID); err != nil {
		return err
	}
	expectedID, err := selectionReceiptID(receipt)
	if err != nil || expectedID != receipt.ID {
		return fmt.Errorf("selection receipt ID does not match its content")
	}
	return nil
}

func validateSelectionModuleIndex(current *index.ModuleIndex, next index.ModuleIndex, release, contextID string, receipt index.SelectionReceipt) error {
	if current == nil {
		if next.SchemaVersion != 1 || next.RecordType != "module_index" || next.ModuleID != receipt.ModuleID {
			return fmt.Errorf("proposed module index does not match selection subject")
		}
		return validateModuleIndexExtension(index.ModuleIndex{SchemaVersion: 1, RecordType: "module_index", ModuleID: receipt.ModuleID}, next, release, contextID, receipt)
	}
	lastSequence := currentSelectionSequence(*current, release, contextID)
	if err := validateModuleIndexExtension(*current, next, release, contextID, receipt); err != nil {
		return err
	}
	if receipt.Sequence <= lastSequence {
		return fmt.Errorf("selection sequence %d does not advance current sequence %d", receipt.Sequence, lastSequence)
	}
	return nil
}

func currentSelectionSequence(moduleIndex index.ModuleIndex, release, contextID string) uint64 {
	var sequence uint64
	for _, selection := range moduleIndex.SelectionHistory {
		if selection.AVMReleaseVersion == release && selection.ContextID == contextID && selection.Sequence > sequence {
			sequence = selection.Sequence
		}
	}
	return sequence
}

// BuildCatalogProposal binds a durable discovery outbox entry to a catalog
// proposal envelope without requiring any module analysis results.
func BuildCatalogProposal(proposalRoot string, outbox discovery.CatalogProposal, expectedBase string, expectedPointer *string, changes []ProposalChange, preconditions ...map[string]string) (Proposal, error) {
	moduleIDs, err := validateCatalogOutbox(outbox)
	if err != nil {
		return Proposal{}, err
	}
	if err = validateCatalogOutboxPayload(proposalRoot, outbox, moduleIDs, changes); err != nil {
		return Proposal{}, err
	}
	conditions, err := catalogProposalPreconditions(preconditions)
	if err != nil {
		return Proposal{}, err
	}
	proposal, err := IdentifyProposal(Proposal{
		Kind: ProposalCatalog, Subject: "catalog", SourceGeneration: outbox.Generation,
		ExpectedBase: expectedBase, ExpectedPointer: expectedPointer,
		Producer: ProposalProducer{ID: expectedProposalProducer(ProposalCatalog), Version: "2.0.0", ContractVersion: 1},
		Paths:    proposalChangePaths(changes), Preconditions: conditions, Changes: changes,
	})
	if err != nil {
		return Proposal{}, err
	}
	if err = validateProposal(proposal); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

// BuildCatalogIndexProposal persists every accepted module row and the complete
// generated index set for the exact discovery outbox generation.
func BuildCatalogIndexProposal(input CatalogIndexProposalInput) (Proposal, error) {
	catalogIndex, metadata, err := validatedCatalogIndex(input)
	if err != nil {
		return Proposal{}, err
	}
	staging, cleanup, err := createCatalogStaging(input)
	if err != nil {
		return Proposal{}, err
	}
	defer cleanup()
	catalogData, err := json.MarshalIndent(catalogIndex, "", "  ")
	if err != nil {
		return Proposal{}, fmt.Errorf("encode staged catalog index: %w", err)
	}
	if err = writeCatalogDataFile(staging, catalogPointerPath, catalogData); err != nil {
		return Proposal{}, err
	}
	metadataChanges, preconditions, err := stageCatalogMetadata(input, staging, metadata)
	if err != nil {
		return Proposal{}, err
	}
	indexChanges, err := stageCatalogIndexes(input, staging)
	if err != nil {
		return Proposal{}, err
	}
	changes := append(metadataChanges, indexChanges...)
	if err = validateUnchangedCatalogPointer(input, catalogIndex, changes); err != nil {
		return Proposal{}, err
	}
	return BuildCatalogProposal(input.ProposalRoot, input.Outbox, input.ExpectedBase, input.ExpectedPointer, changes, preconditions)
}

func validatedCatalogIndex(input CatalogIndexProposalInput) (index.CatalogIndex, []CatalogModuleMetadata, error) {
	moduleIDs, err := validateCatalogOutbox(input.Outbox)
	if err != nil {
		return index.CatalogIndex{}, nil, err
	}
	if input.Generation != input.Outbox.Generation || len(input.Modules) != len(moduleIDs) || len(input.Metadata) != len(moduleIDs) {
		return index.CatalogIndex{}, nil, fmt.Errorf("catalog generation or module records do not match discovery outbox")
	}
	modules := append([]index.CatalogModuleEntry(nil), input.Modules...)
	sort.Slice(modules, func(left, right int) bool { return modules[left].ModuleID < modules[right].ModuleID })
	metadata := append([]CatalogModuleMetadata(nil), input.Metadata...)
	sort.Slice(metadata, func(left, right int) bool { return metadata[left].ModuleID < metadata[right].ModuleID })
	for position, module := range modules {
		if module.ModuleID != moduleIDs[position] || module.Repository == "" || (module.Lifecycle != "active" && module.Lifecycle != "retired") {
			return index.CatalogIndex{}, nil, fmt.Errorf("catalog module entry does not match discovery outbox at position %d", position)
		}
		if err = metadata[position].Validate(); err != nil || metadata[position].ModuleID != module.ModuleID || metadata[position].Repository != module.Repository || metadata[position].Lifecycle != module.Lifecycle {
			return index.CatalogIndex{}, nil, fmt.Errorf("catalog metadata does not match discovery membership at position %d", position)
		}
	}
	catalogIndex := index.CatalogIndex{SchemaVersion: 1, RecordType: "catalog_index", CatalogGeneration: input.Generation, Modules: modules}
	catalogIndex.IndexID, err = proposalIndexID(catalogIndexKind, catalogIndex)
	if err != nil {
		return index.CatalogIndex{}, nil, fmt.Errorf("identify discovery catalog index: %w", err)
	}
	return catalogIndex, metadata, nil
}

func createCatalogStaging(input CatalogIndexProposalInput) (string, func(), error) {
	if err := os.MkdirAll(input.ProposalRoot, 0o755); err != nil {
		return "", nil, fmt.Errorf("create fresh catalog proposal directory: %w", err)
	}
	staging, err := os.MkdirTemp(filepath.Dir(input.ProposalRoot), ".catalog-bootstrap-")
	if err != nil {
		return "", nil, fmt.Errorf("create catalog bootstrap staging tree: %w", err)
	}
	if err = copyAcceptedTree(input.BaseDataRoot, staging); err != nil {
		_ = os.RemoveAll(staging)
		return "", nil, fmt.Errorf("copy current accepted data for catalog indexes: %w", err)
	}
	return staging, func() { _ = os.RemoveAll(staging) }, nil
}

func stageCatalogMetadata(input CatalogIndexProposalInput, staging string, metadata []CatalogModuleMetadata) ([]ProposalChange, map[string]string, error) {
	changes := make([]ProposalChange, 0, len(metadata))
	preconditions := make(map[string]string, len(metadata))
	for _, record := range metadata {
		path, err := identity.ModuleMetadataPath(record.ModuleID)
		if err != nil {
			return nil, nil, err
		}
		data, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			return nil, nil, fmt.Errorf("encode module metadata %s: %w", record.ModuleID, err)
		}
		if err = writeCatalogDataFile(staging, path, data); err != nil {
			return nil, nil, err
		}
		current, err := readOptionalDataFile(input.BaseDataRoot, path)
		if err != nil {
			return nil, nil, err
		}
		if bytes.Equal(current, data) {
			continue
		}
		precondition := "absent"
		if len(current) > 0 {
			precondition = digestBytes(current)
		}
		preconditions[path] = precondition
		if err = writeCatalogProposalPayload(input.ProposalRoot, path, data); err != nil {
			return nil, nil, err
		}
		changes = append(changes, ProposalChange{Path: path, Operation: "set_pointer", Digest: digestBytes(data)})
	}
	return changes, preconditions, nil
}

func stageCatalogIndexes(input CatalogIndexProposalInput, staging string) ([]ProposalChange, error) {
	records, err := index.ReadRecords(staging, input.Outbox.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("read staged catalog records: %w", err)
	}
	indexes, err := index.Build(records)
	if err != nil {
		return nil, fmt.Errorf("build complete catalog indexes: %w", err)
	}
	generatedFiles, err := indexes.Files()
	if err != nil {
		return nil, fmt.Errorf("encode complete catalog indexes: %w", err)
	}
	changes := make([]ProposalChange, 0, len(generatedFiles))
	for path, data := range generatedFiles {
		current, readErr := readOptionalDataFile(input.BaseDataRoot, path)
		if readErr != nil {
			return nil, readErr
		}
		if bytes.Equal(current, data) {
			continue
		}
		if err = writeCatalogDataFile(staging, path, data); err != nil {
			return nil, err
		}
		if err = writeCatalogProposalPayload(input.ProposalRoot, path, data); err != nil {
			return nil, err
		}
		recordID, idErr := generatedCatalogRecordID(data)
		if idErr != nil {
			return nil, fmt.Errorf("identify generated catalog record %s: %w", path, idErr)
		}
		changes = append(changes, ProposalChange{Path: path, Operation: "set_pointer", Digest: digestBytes(data), RecordID: recordID})
	}
	return changes, nil
}

func validateUnchangedCatalogPointer(input CatalogIndexProposalInput, catalogIndex index.CatalogIndex, changes []ProposalChange) error {
	if _, changed := proposalChangeAtPath(changes, catalogPointerPath); changed {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(input.BaseDataRoot, "catalog.json"))
	if err != nil {
		return fmt.Errorf("read existing catalog pointer: %w", err)
	}
	var current index.CatalogIndex
	if err = json.Unmarshal(data, &current); err != nil {
		return fmt.Errorf("decode existing catalog pointer: %w", err)
	}
	if current.CatalogGeneration != input.Generation || current.IndexID != catalogIndex.IndexID {
		return fmt.Errorf("same-generation metadata hydration requires an identical current catalog pointer")
	}
	return nil
}

func writeCatalogProposalPayload(root, relativePath string, data []byte) error {
	if !strings.HasPrefix(relativePath, "data/") {
		return fmt.Errorf("catalog proposal payload is not under data/: %s", relativePath)
	}
	if err := identity.ValidateRelativePath(relativePath, false); err != nil {
		return err
	}
	destination := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o644)
}

func writeCatalogDataFile(root, canonicalPath string, data []byte) error {
	relativePath, err := dataRelativePath(canonicalPath)
	if err != nil {
		return err
	}
	destination := filepath.Join(root, filepath.FromSlash(relativePath))
	if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o644)
}

func generatedCatalogRecordID(data []byte) (string, error) {
	var record struct {
		IndexID   string `json:"index_id"`
		DatasetID string `json:"dataset_id"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return "", err
	}
	if record.IndexID != "" {
		return record.IndexID, nil
	}
	if record.DatasetID != "" {
		return record.DatasetID, nil
	}
	return "", fmt.Errorf("generated record has neither index_id nor dataset_id")
}

func proposalChangeAtPath(changes []ProposalChange, recordPath string) (ProposalChange, bool) {
	for _, change := range changes {
		if change.Path == recordPath {
			return change, true
		}
	}
	return ProposalChange{}, false
}

func validateCatalogOutbox(outbox discovery.CatalogProposal) ([]string, error) {
	if outbox.SchemaVersion != 1 || outbox.ProposalKind != string(ProposalCatalog) || outbox.Generation == 0 || outbox.CreatedAt.IsZero() {
		return nil, fmt.Errorf("catalog outbox entry is invalid")
	}
	if err := identity.ValidateRecordID("discovery-observation", outbox.ObservationID); err != nil {
		return nil, fmt.Errorf("catalog outbox observation is invalid: %w", err)
	}
	moduleIDs, err := identity.SortedSet(outbox.ModuleIDs)
	if err != nil || !equalProposalStrings(moduleIDs, outbox.ModuleIDs) {
		return nil, fmt.Errorf("catalog outbox module membership is not unique and ordered")
	}
	for _, moduleID := range moduleIDs {
		if _, err := identity.ModuleMetadataPath(moduleID); err != nil {
			return nil, err
		}
	}
	for _, pin := range outbox.SourcePins {
		if !containsProposalString(moduleIDs, pin.ModuleID) || !isLowerHex(pin.Commit, 40) || pin.Version == "" || pin.Tag == "" {
			return nil, fmt.Errorf("catalog outbox contains an invalid source pin")
		}
	}
	return moduleIDs, nil
}

func catalogProposalPreconditions(preconditions []map[string]string) (map[string]string, error) {
	if len(preconditions) > 1 {
		return nil, fmt.Errorf("catalog proposal accepts at most one precondition map")
	}
	if len(preconditions) == 0 {
		return nil, nil
	}
	return preconditions[0], nil
}

func validateCatalogOutboxPayload(proposalRoot string, outbox discovery.CatalogProposal, moduleIDs []string, changes []ProposalChange) error {
	for _, change := range changes {
		if change.Path != catalogPointerPath {
			continue
		}
		if change.Operation != "set_pointer" {
			return fmt.Errorf("catalog outbox must update the catalog pointer")
		}
		data, err := readProposalPayload(proposalRoot, change.Path)
		if err != nil {
			return err
		}
		if digestBytes(data) != change.Digest {
			return fmt.Errorf("catalog outbox payload digest does not match its change")
		}
		var catalogIndex index.CatalogIndex
		if err = decodeOne(data, &catalogIndex); err != nil {
			return fmt.Errorf("decode catalog outbox index: %w", err)
		}
		indexModuleIDs := make([]string, 0, len(catalogIndex.Modules))
		for _, module := range catalogIndex.Modules {
			indexModuleIDs = append(indexModuleIDs, module.ModuleID)
		}
		if catalogIndex.CatalogGeneration != outbox.Generation || !equalProposalStrings(indexModuleIDs, moduleIDs) || catalogIndex.IndexID != change.RecordID {
			return fmt.Errorf("catalog index does not match discovery outbox generation and membership")
		}
		return validateCatalogMetadataOutboxPayload(proposalRoot, moduleIDs, changes, false)
	}
	return validateCatalogMetadataOutboxPayload(proposalRoot, moduleIDs, changes, true)
}

func validateCatalogMetadataOutboxPayload(proposalRoot string, moduleIDs []string, changes []ProposalChange, requireAll bool) error {
	metadataIDs := make([]string, 0, len(moduleIDs))
	for _, change := range changes {
		moduleID, found, err := catalogMetadataOutboxModuleID(proposalRoot, moduleIDs, change)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if containsProposalString(metadataIDs, moduleID) {
			return fmt.Errorf("catalog proposal repeats module metadata for %s", moduleID)
		}
		metadataIDs = append(metadataIDs, moduleID)
	}
	if !requireAll {
		return nil
	}
	sortedMetadataIDs, err := identity.SortedSet(metadataIDs)
	if err != nil || !equalProposalStrings(sortedMetadataIDs, moduleIDs) {
		return fmt.Errorf("same-generation catalog bootstrap must include metadata for every discovered module")
	}
	return nil
}

func catalogMetadataOutboxModuleID(proposalRoot string, moduleIDs []string, change ProposalChange) (string, bool, error) {
	if !catalogModulePointerPath(change.Path) || !strings.HasSuffix(change.Path, "/module.json") {
		return "", false, nil
	}
	if change.Operation != "set_pointer" || change.RecordID != "" {
		return "", false, fmt.Errorf("catalog module metadata must update its pointer")
	}
	data, err := readProposalPayload(proposalRoot, change.Path)
	if err != nil {
		return "", false, err
	}
	var metadata CatalogModuleMetadata
	if err = decodeOne(data, &metadata); err != nil {
		return "", false, err
	}
	if err = metadata.Validate(); err != nil {
		return "", false, err
	}
	if !containsProposalString(moduleIDs, metadata.ModuleID) {
		return "", false, fmt.Errorf("catalog metadata module %s is outside discovery membership", metadata.ModuleID)
	}
	wantPath, pathErr := identity.ModuleMetadataPath(metadata.ModuleID)
	if pathErr != nil || wantPath != change.Path {
		return "", false, fmt.Errorf("catalog metadata module identity does not match its path")
	}
	return metadata.ModuleID, true, nil
}

func validateCatalogProposal(proposal Proposal, proposalRoot, baseDataRoot string) error {
	next, err := readProposedCatalogIndex(proposal, proposalRoot)
	if err != nil {
		return err
	}
	current, err := readCurrentCatalogIndex(baseDataRoot)
	if err != nil {
		return err
	}
	if next == nil {
		if current == nil {
			return fmt.Errorf("catalog proposal lacks its initial catalog pointer")
		}
		return validateCatalogMetadataBootstrap(proposal, proposalRoot, baseDataRoot, *current, *current)
	}
	if err = validateProposedCatalogIndex(*next, proposal); err != nil {
		return err
	}
	return validateCatalogGeneration(proposal, proposalRoot, baseDataRoot, current, *next)
}

func readProposedCatalogIndex(proposal Proposal, proposalRoot string) (*index.CatalogIndex, error) {
	for _, change := range proposal.Changes {
		if change.Path != catalogPointerPath {
			continue
		}
		if change.Operation != "set_pointer" {
			return nil, fmt.Errorf("catalog proposal must update the catalog index pointer")
		}
		data, err := readProposalPayload(proposalRoot, change.Path)
		if err != nil {
			return nil, err
		}
		var value index.CatalogIndex
		if err = json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		return &value, nil
	}
	return nil, nil
}

func readCurrentCatalogIndex(baseDataRoot string) (*index.CatalogIndex, error) {
	data, err := readOptionalDataFile(baseDataRoot, catalogPointerPath)
	if err != nil || len(data) == 0 {
		return nil, err
	}
	var current index.CatalogIndex
	if err = json.Unmarshal(data, &current); err != nil {
		return nil, fmt.Errorf("decode current catalog index: %w", err)
	}
	return &current, nil
}

func validateProposedCatalogIndex(next index.CatalogIndex, proposal Proposal) error {
	if next.SchemaVersion != 1 || next.RecordType != "catalog_index" || next.CatalogGeneration != proposal.SourceGeneration {
		return fmt.Errorf("catalog index does not match proposal generation")
	}
	return validateCatalogMembership(next.Modules)
}

func validateCatalogGeneration(proposal Proposal, proposalRoot, baseDataRoot string, current *index.CatalogIndex, next index.CatalogIndex) error {
	if current == nil {
		return nil
	}
	if next.CatalogGeneration < current.CatalogGeneration {
		return fmt.Errorf("catalog generation does not advance the accepted generation")
	}
	if next.CatalogGeneration == current.CatalogGeneration {
		return validateCatalogMetadataBootstrap(proposal, proposalRoot, baseDataRoot, *current, next)
	}
	for _, module := range current.Modules {
		if !catalogHasModule(next.Modules, module.ModuleID) {
			return fmt.Errorf("catalog proposal removed historical module membership %s", module.ModuleID)
		}
	}
	return nil
}

func validateCatalogMetadataBootstrap(proposal Proposal, proposalRoot, baseDataRoot string, current, next index.CatalogIndex) error {
	if current.IndexID != next.IndexID || !reflect.DeepEqual(current.Modules, next.Modules) {
		return fmt.Errorf("same-generation catalog bootstrap changed the accepted catalog pointer")
	}
	if err := requireDatasetMissing(baseDataRoot); err != nil {
		return err
	}
	if err := validateCatalogBootstrapMetadata(proposal, proposalRoot, baseDataRoot, current.Modules); err != nil {
		return err
	}
	if err := validateCatalogBootstrapManifest(proposal, proposalRoot, current.CatalogGeneration, len(current.Modules)); err != nil {
		return err
	}
	return validateCatalogBootstrapModuleIndexes(proposal, current.Modules)
}

func requireDatasetMissing(baseDataRoot string) error {
	datasetData, err := readOptionalDataFile(baseDataRoot, datasetManifestPath)
	if err != nil {
		return err
	}
	if len(datasetData) != 0 {
		return fmt.Errorf("same-generation catalog bootstrap is only allowed before an indexed dataset exists")
	}
	return nil
}

func validateCatalogBootstrapMetadata(proposal Proposal, proposalRoot, baseDataRoot string, modules []index.CatalogModuleEntry) error {
	for _, module := range modules {
		if err := validateCatalogBootstrapMetadataRecord(proposal, proposalRoot, baseDataRoot, module); err != nil {
			return err
		}
	}
	return nil
}

func validateCatalogBootstrapMetadataRecord(proposal Proposal, proposalRoot, baseDataRoot string, module index.CatalogModuleEntry) error {
	metadataPath, err := identity.ModuleMetadataPath(module.ModuleID)
	if err != nil {
		return err
	}
	currentMetadata, err := readOptionalDataFile(baseDataRoot, metadataPath)
	if err != nil {
		return err
	}
	if len(currentMetadata) != 0 || proposal.Preconditions[metadataPath] != "absent" {
		return fmt.Errorf("same-generation bootstrap requires absent module metadata for %s", module.ModuleID)
	}
	change, exists := proposalChangeAtPath(proposal.Changes, metadataPath)
	if !exists || change.Operation != "set_pointer" {
		return fmt.Errorf("same-generation bootstrap omitted module metadata for %s", module.ModuleID)
	}
	data, err := readProposalPayload(proposalRoot, metadataPath)
	if err != nil {
		return err
	}
	var metadata CatalogModuleMetadata
	if err = decodeOne(data, &metadata); err != nil {
		return err
	}
	if err = metadata.Validate(); err != nil || metadata.ModuleID != module.ModuleID || metadata.Repository != module.Repository || metadata.Lifecycle != module.Lifecycle {
		return fmt.Errorf("module metadata does not match accepted catalog entry %s", module.ModuleID)
	}
	return nil
}

func validateCatalogBootstrapManifest(proposal Proposal, proposalRoot string, generation uint64, moduleCount int) error {
	manifestChange, exists := proposalChangeAtPath(proposal.Changes, datasetManifestPath)
	if !exists || manifestChange.Operation != "set_pointer" {
		return fmt.Errorf("same-generation catalog bootstrap must create the initial dataset manifest")
	}
	manifestData, err := readProposalPayload(proposalRoot, datasetManifestPath)
	if err != nil {
		return err
	}
	var manifest index.DatasetManifest
	if err = decodeOne(manifestData, &manifest); err != nil {
		return err
	}
	if manifest.RecordType != "dataset_manifest" || manifest.CatalogGeneration != generation || len(manifest.RecordDigests) < moduleCount {
		return fmt.Errorf("initial dataset manifest does not cover catalog metadata")
	}
	return nil
}

func validateCatalogBootstrapModuleIndexes(proposal Proposal, modules []index.CatalogModuleEntry) error {
	for _, module := range modules {
		moduleIndexPath, pathErr := identity.ModuleIndexPath(module.ModuleID)
		if pathErr != nil {
			return pathErr
		}
		if change, found := proposalChangeAtPath(proposal.Changes, moduleIndexPath); !found || change.Operation != "set_pointer" {
			return fmt.Errorf("same-generation bootstrap omitted generated module index for %s", module.ModuleID)
		}
	}
	return nil
}

func validateCatalogMembership(modules []index.CatalogModuleEntry) error {
	previous := ""
	for _, module := range modules {
		if _, err := identity.ModuleMetadataPath(module.ModuleID); err != nil || module.Repository == "" || (previous != "" && module.ModuleID <= previous) {
			return fmt.Errorf("catalog proposal module membership is invalid")
		}
		previous = module.ModuleID
	}
	return nil
}

func catalogHasModule(modules []index.CatalogModuleEntry, moduleID string) bool {
	for _, module := range modules {
		if module.ModuleID == moduleID {
			return true
		}
	}
	return false
}

func validateProviderSnapshotProposal(proposal Proposal, proposalRoot, baseDataRoot string) error {
	if strings.HasPrefix(proposal.Subject, "core:") {
		version := strings.TrimPrefix(proposal.Subject, "core:")
		return validateCoreFunctionProposal(proposal, proposalRoot, baseDataRoot, version)
	}
	address, version, err := parseProviderSubject(proposal.Subject)
	if err != nil {
		return err
	}
	indexPath, err := identity.ProviderIndexPath(address)
	if err != nil {
		return err
	}
	changes, err := readProviderProposalChanges(proposal, proposalRoot, address, version, indexPath)
	if err != nil {
		return err
	}
	currentIndex, err := loadProviderIndex(baseDataRoot, indexPath)
	if err != nil {
		return err
	}
	return validateProviderIndexExtension(baseDataRoot, changes, currentIndex, address, version)
}

type providerProposalChanges struct {
	index     *index.ProviderIndex
	snapshots []assessment.ProviderSnapshot
}

func readProviderProposalChanges(proposal Proposal, proposalRoot, address, version, indexPath string) (providerProposalChanges, error) {
	var result providerProposalChanges
	for _, change := range proposal.Changes {
		data, readErr := readProposalPayload(proposalRoot, change.Path)
		if readErr != nil && change.Path != indexPath {
			return result, readErr
		}
		if change.Path == indexPath {
			if change.Operation != "set_pointer" {
				return result, fmt.Errorf("provider proposal must update its provider index")
			}
			var value index.ProviderIndex
			if err := decodeOne(data, &value); err != nil {
				return result, err
			}
			result.index = &value
			continue
		}
		if isGeneratedProposalIndexPath(change.Path) {
			continue
		}
		snapshot, err := readProviderSnapshotChange(change, data, address, version)
		if err != nil {
			return result, err
		}
		result.snapshots = append(result.snapshots, snapshot)
	}
	if result.index == nil || len(result.snapshots) == 0 || result.index.SchemaVersion != 1 || result.index.RecordType != "provider_index" || result.index.ProviderAddress != address {
		return result, fmt.Errorf("provider proposal requires a matching provider index and snapshot")
	}
	return result, nil
}

func readProviderSnapshotChange(change ProposalChange, data []byte, address, version string) (assessment.ProviderSnapshot, error) {
	var snapshot assessment.ProviderSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	if change.Operation != "add_immutable" || change.RecordID != snapshot.SnapshotID || identity.ValidateRecordID("snapshot", snapshot.SnapshotID) != nil || (snapshot.SchemaVersion != 2 && snapshot.SchemaVersion != 3) || snapshot.RecordType != "provider_snapshot" || snapshot.ProviderAddress != address || snapshot.ProviderVersion != version || !isLowerHex(snapshot.SourceDigest, 64) || snapshot.Producer.ID == "" || snapshot.Producer.Version == "" || snapshot.Producer.ContractVersion < 1 {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider snapshot record does not match its proposal subject")
	}
	if err := assessment.ValidateProviderSnapshot(snapshot); err != nil {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider snapshot content is invalid")
	}
	storedID := snapshot.SnapshotID
	snapshot.SnapshotID = ""
	expectedID, err := identity.ID("snapshot", snapshot.SchemaVersion, snapshot)
	if err != nil || storedID != expectedID {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider snapshot identity does not match its content")
	}
	snapshot.SnapshotID = storedID
	if _, err := time.Parse(time.RFC3339, snapshot.ObservedAt); err != nil {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider snapshot observation time is invalid")
	}
	return snapshot, nil
}

func loadProviderIndex(dataRoot, relativePath string) (*index.ProviderIndex, error) {
	currentBytes, err := readOptionalDataFile(dataRoot, relativePath)
	if err != nil {
		return nil, err
	}
	if len(currentBytes) > 0 {
		var value index.ProviderIndex
		if err = decodeOne(currentBytes, &value); err != nil {
			return nil, fmt.Errorf("decode current provider index: %w", err)
		}
		return &value, nil
	}
	return nil, nil
}

func validateProviderIndexExtension(baseDataRoot string, changes providerProposalChanges, currentIndex *index.ProviderIndex, address, version string) error {
	wantSnapshots := []index.ProviderSnapshotRecord{}
	if currentIndex != nil {
		wantSnapshots = append(wantSnapshots, currentIndex.Snapshots...)
	}
	for _, snapshot := range changes.snapshots {
		record := index.ProviderSnapshotRecord{ID: snapshot.SnapshotID, Address: address, Version: version}
		wantSnapshots = append(wantSnapshots, record)
		if err := validateProviderObservationTime(baseDataRoot, currentIndex, version, snapshot.ObservedAt); err != nil {
			return err
		}
	}
	if !sameProviderSnapshotSet(changes.index.Snapshots, wantSnapshots) {
		return fmt.Errorf("provider index must contain exactly the current history plus proposed snapshots")
	}
	return nil
}

func sameProviderSnapshotSet(actual, expected []index.ProviderSnapshotRecord) bool {
	if len(actual) != len(expected) {
		return false
	}
	counts := make(map[index.ProviderSnapshotRecord]int, len(expected))
	for _, record := range expected {
		counts[record]++
	}
	for _, record := range actual {
		counts[record]--
		if counts[record] < 0 {
			return false
		}
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func validateProviderObservationTime(dataRoot string, current *index.ProviderIndex, version, observedAt string) error {
	if current == nil {
		return nil
	}
	nextTime, err := time.Parse(time.RFC3339, observedAt)
	if err != nil {
		return err
	}
	for _, snapshot := range current.Snapshots {
		if snapshot.Version != version {
			continue
		}
		path, pathErr := identity.ProviderSnapshotManifestPath(snapshot.Address, snapshot.Version, snapshot.ID)
		if pathErr != nil {
			return pathErr
		}
		data, readErr := readOptionalDataFile(dataRoot, path)
		if readErr != nil {
			return readErr
		}
		if len(data) == 0 {
			continue
		}
		var previous assessment.ProviderSnapshot
		if err = json.Unmarshal(data, &previous); err != nil {
			return err
		}
		previousTime, parseErr := time.Parse(time.RFC3339, previous.ObservedAt)
		if parseErr != nil {
			return parseErr
		}
		if !nextTime.After(previousTime) {
			return fmt.Errorf("provider snapshot observation time does not advance its version history")
		}
	}
	return nil
}

func containsProviderSnapshot(values []index.ProviderSnapshotRecord, target index.ProviderSnapshotRecord) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateCoreFunctionProposal(proposal Proposal, proposalRoot, baseDataRoot, version string) error {
	indexPath := coreFunctionDataPrefix + version + "/" + generatedIndexName
	changes, err := readCoreFunctionChanges(proposalRoot, proposal.Changes, indexPath, version)
	if err != nil {
		return err
	}
	if err = validateCoreFunctionIndex(*changes.index, changes.snapshotID, version, proposal.SourceGeneration); err != nil {
		return err
	}
	return validateCoreFunctionHistory(baseDataRoot, indexPath, *changes.index)
}

type coreFunctionProposalChanges struct {
	index      *coreFunctionIndex
	snapshotID string
}

func readCoreFunctionChanges(proposalRoot string, changes []ProposalChange, indexPath, version string) (coreFunctionProposalChanges, error) {
	var result coreFunctionProposalChanges
	for _, change := range changes {
		data, err := readProposalPayload(proposalRoot, change.Path)
		if err != nil {
			return result, err
		}
		if change.Path == indexPath {
			var value coreFunctionIndex
			if err = decodeOne(data, &value); err != nil {
				return result, err
			}
			result.index = &value
			continue
		}
		if isGeneratedProposalIndexPath(change.Path) {
			continue
		}
		var record struct {
			SchemaVersion    int    `json:"schema_version"`
			RecordType       string `json:"record_type"`
			SnapshotID       string `json:"snapshot_id"`
			TerraformVersion string `json:"terraform_version"`
		}
		if err = json.Unmarshal(data, &record); err != nil {
			return result, err
		}
		if record.RecordType != "core_function_snapshot" || record.SchemaVersion != 2 || record.TerraformVersion != version || record.SnapshotID != change.RecordID || change.Operation != "add_immutable" {
			return result, fmt.Errorf("core function snapshot does not match its proposal subject")
		}
		if result.snapshotID != "" {
			return result, fmt.Errorf("core proposal contains multiple snapshots for one subject")
		}
		result.snapshotID = record.SnapshotID
	}
	if result.index == nil || result.snapshotID == "" {
		return result, fmt.Errorf("core proposal requires one snapshot and its index")
	}
	return result, nil
}

func validateCoreFunctionIndex(next coreFunctionIndex, snapshotID, version string, generation uint64) error {
	if next.SchemaVersion != 1 || next.RecordType != "core_function_index" || next.TerraformVersion != version || next.Generation != generation || next.Generation == 0 || next.CurrentID == nil || *next.CurrentID != snapshotID || !containsProposalString(next.SnapshotIDs, snapshotID) {
		return fmt.Errorf("core function index does not match its proposed snapshot generation")
	}
	return nil
}

func validateCoreFunctionHistory(baseDataRoot, indexPath string, next coreFunctionIndex) error {
	currentBytes, err := readOptionalDataFile(baseDataRoot, indexPath)
	if err != nil {
		return err
	}
	if len(currentBytes) > 0 {
		var current coreFunctionIndex
		if err = decodeOne(currentBytes, &current); err != nil {
			return err
		}
		if next.Generation <= current.Generation {
			return fmt.Errorf("core function snapshot generation does not advance")
		}
		for _, id := range current.SnapshotIDs {
			if !containsProposalString(next.SnapshotIDs, id) {
				return fmt.Errorf("core function index removed snapshot history")
			}
		}
	}
	return nil
}

func validateModuleObservationProposal(proposal Proposal, proposalRoot, baseDataRoot string) error {
	moduleID, kind, found := strings.Cut(proposal.Subject, "@")
	if !found || kind == "" {
		return fmt.Errorf("module observation subject must include a kind")
	}
	indexPath := moduleObservationDataPrefix + moduleID + "/" + generatedIndexName
	indexValue, observation, err := readModuleObservationChanges(proposal, proposalRoot, indexPath, moduleID, kind)
	if err != nil {
		return err
	}
	if err = validateModuleObservationIndex(*indexValue, *observation, proposal.SourceGeneration, moduleID, kind); err != nil {
		return err
	}
	return validateModuleObservationHistory(baseDataRoot, indexPath, *indexValue)
}

func readModuleObservationChanges(proposal Proposal, proposalRoot, indexPath, moduleID, kind string) (*moduleObservationIndex, *moduleObservationRecord, error) {
	var nextIndex *moduleObservationIndex
	var observation *moduleObservationRecord
	for _, change := range proposal.Changes {
		data, err := readProposalPayload(proposalRoot, change.Path)
		if err != nil {
			return nil, nil, err
		}
		if change.Path == indexPath {
			var value moduleObservationIndex
			if err = decodeOne(data, &value); err != nil {
				return nil, nil, err
			}
			nextIndex = &value
			continue
		}
		if isGeneratedProposalIndexPath(change.Path) {
			continue
		}
		var value moduleObservationRecord
		if err = decodeOne(data, &value); err != nil {
			return nil, nil, fmt.Errorf("decode module observation: %w", err)
		}
		if err = validateModuleObservationRecord(change, value, moduleID, kind, proposal.SourceGeneration); err != nil {
			return nil, nil, err
		}
		if observation != nil {
			return nil, nil, fmt.Errorf("module observation proposal contains multiple records for one subject")
		}
		observation = &value
	}
	if nextIndex == nil || observation == nil {
		return nil, nil, fmt.Errorf("module observation proposal requires one record and its pointer index")
	}
	return nextIndex, observation, nil
}

func validateModuleObservationRecord(change ProposalChange, value moduleObservationRecord, moduleID, kind string, generation uint64) error {
	if change.Operation != "add_immutable" || change.RecordID != value.ObservationID || identity.ValidateRecordID("observation", value.ObservationID) != nil || value.SchemaVersion != 2 || value.RecordType != "module_observation" || value.ModuleID != moduleID || value.Kind != kind || value.Generation != generation || value.Generation == 0 || !isLowerHex(value.SourceDigest, 64) || value.Producer.ID != expectedProposalProducer(ProposalModuleObservation) || value.Producer.ContractVersion < 1 || value.ObservedAt.IsZero() {
		return fmt.Errorf("module observation does not match its proposal subject or generation")
	}
	return nil
}

func validateModuleObservationIndex(next moduleObservationIndex, observation moduleObservationRecord, generation uint64, moduleID, kind string) error {
	if next.SchemaVersion != 1 || next.RecordType != "module_observation_index" || next.ModuleID != moduleID || next.Kind != kind || next.Generation != generation || next.CurrentID == nil || *next.CurrentID != observation.ObservationID || !containsProposalString(next.ObservationIDs, observation.ObservationID) {
		return fmt.Errorf("module observation index does not select the proposed observation")
	}
	return nil
}

func validateModuleObservationHistory(dataRoot, indexPath string, next moduleObservationIndex) error {
	currentBytes, err := readOptionalDataFile(dataRoot, indexPath)
	if err != nil {
		return err
	}
	if len(currentBytes) == 0 {
		if len(next.ObservationIDs) != 1 || next.ObservationIDs[0] != *next.CurrentID {
			return fmt.Errorf("module observation index contains unrelated initial history")
		}
		return nil
	}
	var current moduleObservationIndex
	if err = decodeOne(currentBytes, &current); err != nil {
		return err
	}
	if next.Generation <= current.Generation {
		return fmt.Errorf("module observation generation does not advance")
	}
	if !appendOnlyRecordIDs(current.ObservationIDs, next.ObservationIDs, *next.CurrentID) {
		return fmt.Errorf("module observation index is not an exact append to accepted history")
	}
	return nil
}

func appendOnlyRecordIDs(previous, next []string, newID string) bool {
	if len(next) != len(previous)+1 || (len(previous) > 0 && !equalProposalStrings(next[:len(previous)], previous)) {
		return false
	}
	return next[len(next)-1] == newID && !containsProposalString(previous, newID)
}

func validatePresentationProposal(proposal Proposal, proposalRoot, baseDataRoot string) error {
	snapshot, nextIndex, err := readPresentationProposal(proposal, proposalRoot)
	if err != nil {
		return err
	}
	if err = validatePresentationSnapshot(*snapshot, proposal.SourceGeneration); err != nil {
		return err
	}
	if err = validatePresentationIndex(*nextIndex, *snapshot); err != nil {
		return err
	}
	return validatePresentationHistory(baseDataRoot, *nextIndex, *snapshot)
}

func readPresentationProposal(proposal Proposal, proposalRoot string) (*presentationSnapshotPayload, *index.PresentationIndex, error) {
	var snapshot *presentationSnapshotPayload
	var nextIndex *index.PresentationIndex
	for _, change := range proposal.Changes {
		data, err := readProposalPayload(proposalRoot, change.Path)
		if err != nil {
			return nil, nil, err
		}
		if change.Path == identity.PresentationIndexPath() {
			var value index.PresentationIndex
			if err = decodeOne(data, &value); err != nil {
				return nil, nil, err
			}
			nextIndex = &value
			continue
		}
		if isGeneratedProposalIndexPath(change.Path) {
			continue
		}
		var value presentationSnapshotPayload
		if err = decodeOne(data, &value); err != nil {
			return nil, nil, fmt.Errorf("decode presentation snapshot: %w", err)
		}
		if snapshot != nil {
			return nil, nil, fmt.Errorf("presentation proposal contains multiple snapshots")
		}
		snapshot = &value
	}
	if snapshot == nil || nextIndex == nil {
		return nil, nil, fmt.Errorf("presentation proposal requires a snapshot and its index")
	}
	return snapshot, nextIndex, nil
}

func validatePresentationSnapshot(value presentationSnapshotPayload, generation uint64) error {
	if value.ID == "" || identity.ValidateRecordID("presentation", value.ID) != nil || value.SchemaVersion != 2 || value.RecordType != "presentation_snapshot" || value.Generation != generation || value.Generation == 0 || value.AsOf.IsZero() {
		return fmt.Errorf("presentation snapshot does not match its proposal generation")
	}
	for _, module := range value.Modules {
		if err := validatePresentationModule(module.ModuleID, module.Lifecycle, module.Coverage); err != nil {
			return err
		}
	}
	for _, health := range value.CollectorHealth {
		if err := validatePresentationHealth(health.CollectorID, health.Status, health.ObservedAt); err != nil {
			return err
		}
	}
	return nil
}

func validatePresentationModule(moduleID, lifecycle string, coverage json.RawMessage) error {
	if _, err := identity.ModuleMetadataPath(moduleID); err != nil || (lifecycle != "active" && lifecycle != "blocked" && lifecycle != "retired") || len(coverage) == 0 || !json.Valid(coverage) {
		return fmt.Errorf("presentation snapshot contains an invalid module summary")
	}
	return nil
}

func validatePresentationHealth(collectorID, status string, observedAt time.Time) error {
	if collectorID == "" || (status != "healthy" && status != "stale" && status != "failed" && status != "unknown") || observedAt.IsZero() {
		return fmt.Errorf("presentation snapshot contains invalid collector health")
	}
	return nil
}

func validatePresentationIndex(next index.PresentationIndex, snapshot presentationSnapshotPayload) error {
	if next.SchemaVersion != 1 || next.RecordType != "presentation_index" || next.CurrentID == nil || *next.CurrentID != snapshot.ID || len(next.Snapshots) == 0 {
		return fmt.Errorf("presentation index does not point to the proposed snapshot")
	}
	latest := next.Snapshots[len(next.Snapshots)-1]
	if latest.ID != snapshot.ID || latest.Generation != snapshot.Generation || !latest.AsOf.Equal(snapshot.AsOf) {
		return fmt.Errorf("presentation index does not end at the proposed snapshot")
	}
	selectedIDs := make([]string, 0)
	for _, module := range snapshot.Modules {
		selectedIDs = append(selectedIDs, module.SelectedReceiptIDs...)
	}
	selectedIDs, err := identity.SortedSet(selectedIDs)
	if err != nil || !sameCanonicalSet(latest.SelectionReceiptIDs, selectedIDs) {
		return fmt.Errorf("presentation index selection membership does not match the snapshot")
	}
	return nil
}

func validatePresentationHistory(baseDataRoot string, next index.PresentationIndex, snapshot presentationSnapshotPayload) error {
	currentBytes, err := readOptionalDataFile(baseDataRoot, identity.PresentationIndexPath())
	if err != nil {
		return err
	}
	if len(currentBytes) > 0 {
		var current index.PresentationIndex
		if err = decodeOne(currentBytes, &current); err != nil {
			return err
		}
		if len(current.Snapshots) > 0 && snapshot.Generation <= current.Snapshots[len(current.Snapshots)-1].Generation {
			return fmt.Errorf("presentation generation does not advance")
		}
		if len(next.Snapshots) != len(current.Snapshots)+1 {
			return fmt.Errorf("presentation index added unrelated snapshot history")
		}
		for position, previous := range current.Snapshots {
			if !reflect.DeepEqual(next.Snapshots[position], previous) {
				return fmt.Errorf("presentation index rewrote existing snapshot history")
			}
		}
	} else if len(next.Snapshots) != 1 {
		return fmt.Errorf("initial presentation index contains unrelated history")
	}
	return nil
}

func containsPresentationSnapshot(values []index.PresentationSnapshotRecord, target index.PresentationSnapshotRecord) bool {
	for _, value := range values {
		if value.ID == target.ID && value.Generation == target.Generation && value.AsOf.Equal(target.AsOf) {
			return true
		}
	}
	return false
}

func proposalChangePaths(changes []ProposalChange) []string {
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		paths = append(paths, change.Path)
	}
	return paths
}

func validateModuleEvidenceProposal(proposal Proposal, proposalRoot, baseDataRoot string) error {
	moduleID, sourceSHA, err := moduleRevisionSubject(proposal.Subject)
	if err != nil {
		return err
	}
	moduleIndexPath, err := identity.ModuleIndexPath(moduleID)
	if err != nil {
		return err
	}
	nextModuleIndex, newIDs, err := readModuleEvidenceChanges(proposal, proposalRoot, moduleID, sourceSHA, moduleIndexPath)
	if err != nil {
		return err
	}
	currentModuleIndex, err := loadModuleIndex(baseDataRoot, moduleIndexPath)
	if err != nil {
		return err
	}
	return validateModuleEvidenceIndex(currentModuleIndex, *nextModuleIndex, moduleID, sourceSHA, newIDs)
}

func readModuleEvidenceChanges(proposal Proposal, proposalRoot, moduleID, sourceSHA, moduleIndexPath string) (*index.ModuleIndex, map[string]map[string]struct{}, error) {
	newIDs := map[string]map[string]struct{}{
		"source_revision": {}, "facts": {}, "provenance": {}, "dimension_definition": {}, "dimension_result": {}, "analysis": {},
	}
	var nextModuleIndex *index.ModuleIndex
	for _, change := range proposal.Changes {
		if change.Path == moduleIndexPath {
			if change.Operation != "set_pointer" {
				return nil, nil, fmt.Errorf("module evidence must update its history index")
			}
			data, readErr := readProposalPayload(proposalRoot, change.Path)
			if readErr != nil {
				return nil, nil, readErr
			}
			var value index.ModuleIndex
			if err := decodeOne(data, &value); err != nil {
				return nil, nil, fmt.Errorf("decode proposed module evidence index: %w", err)
			}
			nextModuleIndex = &value
			continue
		}
		if isGeneratedProposalIndexPath(change.Path) {
			continue
		}
		data, readErr := readProposalPayload(proposalRoot, change.Path)
		if readErr != nil {
			return nil, nil, readErr
		}
		recordType, recordID, err := validateModuleEvidenceRecord(change, data, moduleID, sourceSHA)
		if err != nil {
			return nil, nil, err
		}
		newIDs[recordType][recordID] = struct{}{}
		if recordType == "dimension_result" {
			var result assessment.DimensionResult
			if err = decodeOne(data, &result); err != nil {
				return nil, nil, fmt.Errorf("decode proposed dimension result: %w", err)
			}
			newIDs["dimension_definition"][result.DefinitionID] = struct{}{}
		}
	}
	if nextModuleIndex == nil {
		return nil, nil, fmt.Errorf("module evidence proposal lacks its history index")
	}
	return nextModuleIndex, newIDs, nil
}

func validateModuleEvidenceRecord(change ProposalChange, data []byte, moduleID, sourceSHA string) (string, string, error) {
	if change.Operation != "add_immutable" {
		return "", "", fmt.Errorf("module evidence records must be immutable")
	}
	var header struct {
		RecordType string `json:"record_type"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return "", "", err
	}
	if header.RecordType == "source_revision" {
		var revision index.SourceRevisionRecord
		if err := json.Unmarshal(data, &revision); err != nil {
			return "", "", err
		}
		if change.RecordID != "" || revision.ModuleID != moduleID || revision.SourceSHA != sourceSHA || !revision.Immutable {
			return "", "", fmt.Errorf("source revision does not match module evidence subject")
		}
		return header.RecordType, sourceSHA, nil
	}
	if change.RecordID == "" {
		return "", "", fmt.Errorf("module evidence records must have a content-addressed record ID")
	}
	if !strings.HasSuffix(change.Path, "/"+change.RecordID+".json") {
		return "", "", fmt.Errorf("module evidence record ID does not match canonical path %s", change.Path)
	}
	recordKinds := map[string]string{"facts": "facts", "provenance": "provenance", "dimension_definition": "definition", "dimension_result": "dimension-result", "analysis": "analysis"}
	recordKind := recordKinds[header.RecordType]
	if recordKind == "" || identity.ValidateRecordID(recordKind, change.RecordID) != nil {
		return "", "", fmt.Errorf("module evidence record identity is invalid for %s", change.Path)
	}
	if err := validateModuleEvidenceRecordContent(header.RecordType, change.RecordID, data, moduleID, sourceSHA); err != nil {
		return "", "", err
	}
	return header.RecordType, change.RecordID, nil
}

func validateModuleEvidenceRecordContent(recordType, recordID string, data []byte, moduleID, sourceSHA string) error {
	switch recordType {
	case "dimension_definition":
		var definition assessment.DimensionDefinition
		if err := decodeOne(data, &definition); err != nil {
			return err
		}
		if definition.DefinitionID != recordID {
			return fmt.Errorf("dimension definition identity does not match its record ID")
		}
		registry, err := assessment.DefaultRegistry()
		if err != nil {
			return err
		}
		for _, compiled := range registry.ListDimensions() {
			if compiled.DefinitionID == recordID && reflect.DeepEqual(compiled, definition) {
				return nil
			}
		}
		return fmt.Errorf("dimension definition is not an exact compiled assessment definition")
	case "facts":
		var facts terraform.Facts
		if err := decodeOne(data, &facts); err != nil {
			return err
		}
		if err := facts.Validate(); err != nil || facts.FactsID != recordID || facts.ModuleID != moduleID || facts.SourceSHA != sourceSHA {
			return fmt.Errorf("facts record does not match module evidence subject")
		}
	case "dimension_result":
		var result assessment.DimensionResult
		if err := decodeOne(data, &result); err != nil {
			return err
		}
		if result.DimensionResultID != recordID || result.ModuleID != moduleID || result.SourceSHA != sourceSHA {
			return fmt.Errorf("dimension result does not match module evidence subject")
		}
		if err := result.Validate(); err != nil {
			return err
		}
	case "analysis":
		var analysis worker.Analysis
		if err := decodeOne(data, &analysis); err != nil {
			return err
		}
		if analysis.AnalysisID != recordID || analysis.ModuleID != moduleID || analysis.SourceSHA != sourceSHA {
			return fmt.Errorf("analysis does not match module evidence subject")
		}
		if err := analysis.Validate(); err != nil {
			return err
		}
	case "provenance":
		var provenance struct {
			ProvenanceID string `json:"provenance_id"`
			ModuleID     string `json:"module_id"`
			SourceSHA    string `json:"source_sha"`
		}
		if err := json.Unmarshal(data, &provenance); err != nil {
			return err
		}
		if provenance.ProvenanceID != recordID || provenance.ModuleID != moduleID || provenance.SourceSHA != sourceSHA {
			return fmt.Errorf("provenance does not match module evidence subject")
		}
	}
	return nil
}

func validateModuleEvidenceIndex(current *index.ModuleIndex, next index.ModuleIndex, moduleID, sourceSHA string, newIDs map[string]map[string]struct{}) error {
	if next.SchemaVersion != 1 || next.RecordType != "module_index" || next.ModuleID != moduleID {
		return fmt.Errorf("module evidence proposal lacks its matching history index")
	}
	if err := validateEvidenceIndexPreservation(current, next); err != nil {
		return err
	}
	if !containsProposalString(next.SourceSHAs, sourceSHA) {
		return fmt.Errorf("module history index omits the proposed source revision")
	}
	for recordType, IDs := range newIDs {
		for id := range IDs {
			if !moduleIndexContainsRecord(next, recordType, id) {
				return fmt.Errorf("module history index omits %s record %s", recordType, id)
			}
		}
	}
	return validateEvidenceIndexHistory(current, next, newIDs, sourceSHA)
}

func validateEvidenceIndexPreservation(current *index.ModuleIndex, next index.ModuleIndex) error {
	if current == nil {
		if len(next.SelectionIDs) > 0 || len(next.SelectionHistory) > 0 || len(next.CurrentSelections) > 0 {
			return fmt.Errorf("module evidence may not create selected pointers")
		}
		return nil
	}
	if !reflect.DeepEqual(current.SelectionIDs, next.SelectionIDs) || !reflect.DeepEqual(current.SelectionHistory, next.SelectionHistory) || !reflect.DeepEqual(current.CurrentSelections, next.CurrentSelections) {
		return fmt.Errorf("module evidence may not change selected pointers or selection history")
	}
	for _, definitionID := range current.DefinitionIDs {
		if !containsProposalString(next.DefinitionIDs, definitionID) {
			return fmt.Errorf("module evidence removed an accepted definition reference")
		}
	}
	currentValue, nextValue := *current, next
	currentValue.IndexID, nextValue.IndexID = "", ""
	currentValue.SourceSHAs, nextValue.SourceSHAs = nil, nil
	currentValue.FactsIDs, nextValue.FactsIDs = nil, nil
	currentValue.ProvenanceIDs, nextValue.ProvenanceIDs = nil, nil
	currentValue.DefinitionIDs, nextValue.DefinitionIDs = nil, nil
	currentValue.DimensionResultIDs, nextValue.DimensionResultIDs = nil, nil
	currentValue.AnalysisIDs, nextValue.AnalysisIDs = nil, nil
	if !reflect.DeepEqual(currentValue, nextValue) {
		return fmt.Errorf("module evidence changed unrelated index content")
	}
	return nil
}

func validateEvidenceIndexHistory(current *index.ModuleIndex, next index.ModuleIndex, additions map[string]map[string]struct{}, sourceSHA string) error {
	existing := map[string][]string{}
	if current != nil {
		existing = map[string][]string{
			"source_revision": current.SourceSHAs, "facts": current.FactsIDs,
			"provenance": current.ProvenanceIDs, "dimension_definition": current.DefinitionIDs,
			"dimension_result": current.DimensionResultIDs,
			"analysis":         current.AnalysisIDs,
		}
	}
	if current == nil || !containsProposalString(current.SourceSHAs, sourceSHA) {
		if _, sourceAdded := additions["source_revision"][sourceSHA]; !sourceAdded {
			return fmt.Errorf("module evidence must add a missing source revision to history")
		}
	}
	expected := map[string][]string{}
	for recordType, values := range existing {
		expected[recordType] = append([]string(nil), values...)
		present := make(map[string]struct{}, len(values)+len(additions[recordType]))
		for _, id := range values {
			present[id] = struct{}{}
		}
		for id := range additions[recordType] {
			if _, alreadyAccepted := present[id]; alreadyAccepted {
				continue
			}
			expected[recordType] = append(expected[recordType], id)
			present[id] = struct{}{}
		}
	}
	for recordType, values := range additions {
		if _, exists := expected[recordType]; exists {
			continue
		}
		for id := range values {
			expected[recordType] = append(expected[recordType], id)
		}
	}
	for recordType, ids := range expected {
		if !sameCanonicalSet(moduleIndexRecordIDs(next, recordType), ids) {
			return fmt.Errorf("module history index does not equal accepted %s records plus this proposal", recordType)
		}
	}
	return nil
}

func moduleIndexRecordIDs(moduleIndex index.ModuleIndex, recordType string) []string {
	switch recordType {
	case "source_revision":
		return moduleIndex.SourceSHAs
	case "facts":
		return moduleIndex.FactsIDs
	case "provenance":
		return moduleIndex.ProvenanceIDs
	case "dimension_definition":
		return moduleIndex.DefinitionIDs
	case "dimension_result":
		return moduleIndex.DimensionResultIDs
	case "analysis":
		return moduleIndex.AnalysisIDs
	default:
		return nil
	}
}

func sameCanonicalSet(actual, expected []string) bool {
	sortedExpected, err := identity.SortedSet(expected)
	return err == nil && equalProposalStrings(actual, sortedExpected)
}

func moduleIndexContainsRecord(moduleIndex index.ModuleIndex, recordType, id string) bool {
	var values []string
	switch recordType {
	case "source_revision":
		values = moduleIndex.SourceSHAs
	case "facts":
		values = moduleIndex.FactsIDs
	case "provenance":
		values = moduleIndex.ProvenanceIDs
	case "dimension_definition":
		values = moduleIndex.DefinitionIDs
	case "dimension_result":
		values = moduleIndex.DimensionResultIDs
	case "analysis":
		values = moduleIndex.AnalysisIDs
	default:
		return false
	}
	return containsProposalString(values, id)
}

func indexSelectionReceipt(receipt selectionExportReceipt) index.SelectionReceipt {
	return index.SelectionReceipt{
		ID: receipt.ID, ModuleID: receipt.ModuleID, AVMReleaseVersion: receipt.AVMReleaseVersion, ContextID: receipt.ContextID,
		Sequence: receipt.Sequence, DesiredGeneration: receipt.DesiredGeneration,
		SourceSHA: receipt.SourceSHA, FactsID: receipt.FactsID,
		InputFingerprint: receipt.InputFingerprint, AnalysisID: receipt.AnalysisID,
		EvidenceCommit: receipt.EvidenceCommit, SelectedAt: receipt.SelectedAt,
	}
}

func selectionReceiptID(receipt selectionExportReceipt) (string, error) {
	return identity.ID("selection", 3, struct {
		SchemaVersion     int       `json:"schema_version"`
		RecordType        string    `json:"record_type"`
		ModuleID          string    `json:"module_id"`
		AVMReleaseVersion string    `json:"avm_release_version"`
		ContextID         string    `json:"context_id"`
		Sequence          uint64    `json:"sequence"`
		DesiredGeneration uint64    `json:"desired_generation"`
		SourceSHA         *string   `json:"source_sha"`
		FactsID           *string   `json:"facts_id"`
		InputFingerprint  string    `json:"input_fingerprint"`
		AnalysisID        *string   `json:"analysis_id"`
		EvidenceCommit    string    `json:"evidence_commit"`
		SelectedAt        time.Time `json:"selected_at"`
	}{
		receipt.SchemaVersion, receipt.RecordType, receipt.ModuleID, receipt.AVMReleaseVersion, receipt.ContextID,
		receipt.Sequence, receipt.DesiredGeneration, receipt.SourceSHA, receipt.FactsID,
		receipt.InputFingerprint, receipt.AnalysisID, receipt.EvidenceCommit, receipt.SelectedAt,
	})
}

func loadModuleIndex(dataRoot, relativePath string) (*index.ModuleIndex, error) {
	data, err := readOptionalDataFile(dataRoot, relativePath)
	if err != nil || len(data) == 0 {
		return nil, err
	}
	var value index.ModuleIndex
	if err = decodeOne(data, &value); err != nil {
		return nil, fmt.Errorf("decode current module index: %w", err)
	}
	return &value, nil
}

func validateModuleIndexExtension(current, next index.ModuleIndex, release, contextID string, receipt index.SelectionReceipt) error {
	if current.ModuleID != next.ModuleID || next.RecordType != "module_index" || next.SchemaVersion != 1 || next.ModuleID != receipt.ModuleID {
		return fmt.Errorf("selection export module index identity is invalid")
	}
	if err := validateUnchangedModuleIndexFields(current, next); err != nil {
		return err
	}
	newPointer, err := exportedSelectionPointer(next.SelectionHistory, release, contextID, receipt)
	if err != nil {
		return err
	}
	if err = validateModuleSelectionHistory(current, next, *newPointer, receipt); err != nil {
		return err
	}
	return validateModuleCurrentSelections(current, next, contextID, *newPointer)
}

func validateUnchangedModuleIndexFields(current, next index.ModuleIndex) error {
	currentComparable, nextComparable := current, next
	currentComparable.IndexID, nextComparable.IndexID = "", ""
	currentComparable.SelectionIDs, nextComparable.SelectionIDs = nil, nil
	currentComparable.SelectionHistory, nextComparable.SelectionHistory = nil, nil
	currentComparable.CurrentSelections, nextComparable.CurrentSelections = nil, nil
	if !reflect.DeepEqual(currentComparable, nextComparable) {
		return fmt.Errorf("selection export changed unrelated module index content")
	}
	return nil
}

func exportedSelectionPointer(history []index.SelectionPointer, release, contextID string, receipt index.SelectionReceipt) (*index.SelectionPointer, error) {
	exported := exportReceiptFromIndex(receipt)
	var newPointer *index.SelectionPointer
	for position := range history {
		pointer := history[position]
		if pointer.AVMReleaseVersion == release && pointer.ContextID == contextID && pointer.SelectionID == receipt.ID {
			if newPointer != nil {
				return nil, fmt.Errorf("module index duplicates the exported selection")
			}
			pointerCopy := pointer
			newPointer = &pointerCopy
		}
	}
	if newPointer == nil {
		return nil, fmt.Errorf("module index history omits the exported selection")
	}
	if !containsReceiptPointer(history, exported) {
		return nil, fmt.Errorf("module index history omits the exported selection receipt")
	}
	return newPointer, nil
}

func validateModuleSelectionHistory(current, next index.ModuleIndex, newPointer index.SelectionPointer, receipt index.SelectionReceipt) error {
	wantHistory := append(append([]index.SelectionPointer(nil), current.SelectionHistory...), newPointer)
	sort.Slice(wantHistory, func(left, right int) bool {
		if wantHistory[left].AVMReleaseVersion == wantHistory[right].AVMReleaseVersion {
			if wantHistory[left].ContextID == wantHistory[right].ContextID {
				return wantHistory[left].Sequence < wantHistory[right].Sequence
			}
			return wantHistory[left].ContextID < wantHistory[right].ContextID
		}
		return wantHistory[left].AVMReleaseVersion < wantHistory[right].AVMReleaseVersion
	})
	wantIDs := make([]string, 0, len(wantHistory))
	for _, selection := range wantHistory {
		wantIDs = append(wantIDs, selection.SelectionID)
	}
	if !reflect.DeepEqual(next.SelectionIDs, wantIDs) {
		return fmt.Errorf("selection IDs do not match exported history")
	}
	if !reflect.DeepEqual(next.SelectionHistory, wantHistory) {
		return fmt.Errorf("selection export changed existing selection history")
	}
	return nil
}

func validateModuleCurrentSelections(current, next index.ModuleIndex, contextID string, newPointer index.SelectionPointer) error {
	exported := exportReceiptFromIndex(index.SelectionReceipt{
		ID: newPointer.SelectionID, ModuleID: newPointer.ModuleID, AVMReleaseVersion: newPointer.AVMReleaseVersion, ContextID: newPointer.ContextID,
		Sequence: newPointer.Sequence, DesiredGeneration: newPointer.DesiredGeneration,
		AnalysisID: newPointer.AnalysisID, SourceSHA: newPointer.SourceSHA, FactsID: newPointer.FactsID,
		InputFingerprint: newPointer.InputFingerprint,
	})
	if !containsReceiptPointer(next.CurrentSelections, exported) {
		return fmt.Errorf("module index does not include the exported current selection")
	}
	wantCurrent := make([]index.SelectionPointer, 0, len(current.CurrentSelections)+1)
	for _, pointer := range current.CurrentSelections {
		if pointer.ContextID != contextID || pointer.AVMReleaseVersion != newPointer.AVMReleaseVersion {
			wantCurrent = append(wantCurrent, pointer)
		}
	}
	wantCurrent = append(wantCurrent, newPointer)
	sort.Slice(wantCurrent, func(left, right int) bool {
		if wantCurrent[left].AVMReleaseVersion == wantCurrent[right].AVMReleaseVersion {
			return wantCurrent[left].ContextID < wantCurrent[right].ContextID
		}
		return wantCurrent[left].AVMReleaseVersion < wantCurrent[right].AVMReleaseVersion
	})
	if !reflect.DeepEqual(next.CurrentSelections, wantCurrent) {
		return fmt.Errorf("selection export changed another context pointer")
	}
	return nil
}

func validateTrackIndexExtension(dataRoot, relativePath string, next index.TrackIndex, moduleID, release, contextID string, receipt index.SelectionReceipt) error {
	if next.SchemaVersion != 1 || next.RecordType != "track_index" || next.ContextID != contextID {
		return fmt.Errorf("selection export track index identity is invalid")
	}
	var current index.TrackIndex
	data, err := readOptionalDataFile(dataRoot, relativePath)
	if err != nil {
		return err
	}
	if len(data) > 0 {
		if err = decodeOne(data, &current); err != nil {
			return fmt.Errorf("decode current track index: %w", err)
		}
		if err = validateOtherTrackMembersPreserved(current, next); err != nil {
			return err
		}
	}
	exported := exportReceiptFromIndex(receipt)
	if !containsProposalString(next.ModuleIDs, moduleID) || !containsReceiptPointer(next.Selections, exported) || !containsReceiptTargetVector(next.TargetVectors, exported) {
		return fmt.Errorf("track index does not include the exported selection")
	}
	wantModuleIDs := append(append([]string(nil), current.ModuleIDs...), moduleID)
	if containsProposalString(current.ModuleIDs, moduleID) {
		wantModuleIDs = append([]string(nil), current.ModuleIDs...)
	}
	if !sameCanonicalSet(next.ModuleIDs, wantModuleIDs) || len(next.Selections) != len(current.Selections)+1 || len(next.TargetVectors) != len(current.TargetVectors)+1 {
		return fmt.Errorf("selection export added unrelated track membership")
	}
	return nil
}

func validateOtherTrackMembersPreserved(current, next index.TrackIndex) error {
	for _, pointer := range current.Selections {
		if !containsSelectionPointer(next.Selections, pointer) {
			return fmt.Errorf("selection export changed existing track history")
		}
	}
	for _, vector := range current.TargetVectors {
		if !containsTargetVector(next.TargetVectors, vector) {
			return fmt.Errorf("selection export changed an existing track target vector")
		}
	}
	return nil
}

func exportReceiptFromIndex(receipt index.SelectionReceipt) selectionExportReceipt {
	return selectionExportReceipt{
		SchemaVersion: 3, RecordType: "selection_receipt", ID: receipt.ID,
		ModuleID: receipt.ModuleID, AVMReleaseVersion: receipt.AVMReleaseVersion, ContextID: receipt.ContextID, Sequence: receipt.Sequence,
		DesiredGeneration: receipt.DesiredGeneration, SourceSHA: receipt.SourceSHA,
		FactsID: receipt.FactsID, InputFingerprint: receipt.InputFingerprint,
		AnalysisID: receipt.AnalysisID, EvidenceCommit: receipt.EvidenceCommit, SelectedAt: receipt.SelectedAt,
	}
}

func validateSelectionEvidence(dataRoot string, receipt selectionExportReceipt) error {
	if receipt.SourceSHA == nil || receipt.FactsID == nil || receipt.AnalysisID == nil {
		if receipt.SourceSHA != nil || receipt.FactsID != nil || receipt.AnalysisID != nil {
			return fmt.Errorf("null selection receipt has partial evidence identity")
		}
		return nil
	}
	for _, record := range []struct{ kind, id string }{{"facts", *receipt.FactsID}, {"analysis", *receipt.AnalysisID}} {
		path, err := identity.ModuleRecordPath(receipt.ModuleID, *receipt.SourceSHA, record.kind, record.id)
		if err != nil {
			return err
		}
		data, err := readOptionalDataFile(dataRoot, path)
		if err != nil || len(data) == 0 {
			return fmt.Errorf("selection receipt references unaccepted %s evidence", record.kind)
		}
	}
	sourcePath, err := identity.ModuleSourcePath(receipt.ModuleID, *receipt.SourceSHA)
	if err != nil {
		return err
	}
	data, err := readOptionalDataFile(dataRoot, sourcePath)
	if err != nil || len(data) == 0 {
		return fmt.Errorf("selection receipt references an unaccepted source revision")
	}
	return nil
}

func containsSelectionPointer(values []index.SelectionPointer, target index.SelectionPointer) bool {
	for _, value := range values {
		if reflect.DeepEqual(value, target) {
			return true
		}
	}
	return false
}

func containsReceiptPointer(values []index.SelectionPointer, receipt selectionExportReceipt) bool {
	for _, value := range values {
		if value.ModuleID == receipt.ModuleID && value.AVMReleaseVersion == receipt.AVMReleaseVersion && value.ContextID == receipt.ContextID && value.Sequence == receipt.Sequence && value.SelectionID == receipt.ID && sameOptionalProposalString(value.AnalysisID, receipt.AnalysisID) && sameOptionalProposalString(value.SourceSHA, receipt.SourceSHA) && sameOptionalProposalString(value.FactsID, receipt.FactsID) && value.InputFingerprint == receipt.InputFingerprint {
			return true
		}
	}
	return false
}

func containsTargetVector(values []index.TrackTargetVector, target index.TrackTargetVector) bool {
	for _, value := range values {
		if reflect.DeepEqual(value, target) {
			return true
		}
	}
	return false
}

func containsReceiptTargetVector(values []index.TrackTargetVector, receipt selectionExportReceipt) bool {
	for _, value := range values {
		if value.ModuleID == receipt.ModuleID && value.AVMReleaseVersion == receipt.AVMReleaseVersion && value.SelectionID == receipt.ID && value.Sequence == receipt.Sequence && sameOptionalProposalString(value.AnalysisID, receipt.AnalysisID) {
			return true
		}
	}
	return false
}

func sameOptionalProposalString(left, right *string) bool {
	return equalOptionalStrings(left, right)
}

func containsProposalString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func expectedProposalProducer(kind ProposalKind) string {
	return map[ProposalKind]string{
		ProposalModuleEvidence: "avm-intelligence-factory", ProposalCatalog: "avm-intelligence-catalog",
		ProposalProviderSnapshot: "avm-intelligence-provider", ProposalModuleObservation: "avm-intelligence-observations",
		ProposalSelectionExport: "avm-intelligence-selection", ProposalPresentation: "avm-intelligence-presentation",
	}[kind]
}

func proposalChangesDigest(changes []ProposalChange) (string, error) {
	ordered := append([]ProposalChange(nil), changes...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Path < ordered[right].Path })
	canonical, err := identity.CanonicalJSON("proposal-changes", ProposalSchemaVersion, ordered)
	if err != nil {
		return "", err
	}
	return digestBytes(canonical), nil
}

func proposalIdentityFor(proposal Proposal) proposalIdentity {
	return proposalIdentity{
		SchemaVersion: proposal.SchemaVersion, RecordType: proposal.RecordType, Kind: proposal.Kind,
		Subject: proposal.Subject, SourceGeneration: proposal.SourceGeneration, ExpectedBase: proposal.ExpectedBase,
		ExpectedPointer: proposal.ExpectedPointer, Producer: proposal.Producer, ContentDigest: proposal.ContentDigest,
		Paths: proposal.Paths, Preconditions: proposal.Preconditions, Changes: proposal.Changes,
	}
}

func providerSubjectPrefix(subject string) (string, error) {
	if strings.HasPrefix(subject, "core:") {
		version := strings.TrimPrefix(subject, "core:")
		if version == "" {
			return "", fmt.Errorf("core provider subject lacks a Terraform version")
		}
		return coreFunctionDataPrefix + version + "/", nil
	}
	address, _, err := parseProviderSubject(subject)
	if err != nil {
		return "", err
	}
	return "data/providers/" + address + "/", nil
}

func parseProviderSubject(subject string) (string, string, error) {
	address, version, found := strings.Cut(subject, "@")
	if !found || version == "" {
		return "", "", fmt.Errorf("provider subject must contain address@version")
	}
	if _, err := identity.ProviderSnapshotPath(address, version, "snapshot-"+strings.Repeat("0", 64)); err != nil {
		return "", "", err
	}
	return address, version, nil
}

func selectionSubject(subject string) (string, string, string, error) {
	moduleID, remainder, found := strings.Cut(subject, "/")
	if !found {
		return "", "", "", fmt.Errorf("selection subject must be module/release/context")
	}
	release, contextID, found := strings.Cut(remainder, "/")
	if !found || contextID == "" || !releaseVersionPattern.MatchString(release) {
		return "", "", "", fmt.Errorf("selection subject must contain a valid AVM release and context")
	}
	if _, err := identity.ModuleMetadataPath(moduleID); err != nil {
		return "", "", "", err
	}
	if err := identity.ValidateRelativePath(contextID, false); err != nil {
		return "", "", "", err
	}
	return moduleID, release, contextID, nil
}

func subjectModuleID(subject string) string {
	moduleID, _, _ := strings.Cut(subject, "@")
	moduleID, _, _ = strings.Cut(moduleID, "/")
	return moduleID
}

func readProposalPayload(root, relativePath string) ([]byte, error) {
	return readRegularFile(root, relativePath)
}

func readRegularFile(root, relativePath string) ([]byte, error) {
	if err := identity.ValidateRelativePath(relativePath, false); err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, fmt.Errorf("proposal data root must be a real directory")
	}
	current := root
	components := strings.Split(filepath.FromSlash(relativePath), string(filepath.Separator))
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("proposal path contains a symlink: %s", relativePath)
		}
		if index < len(components)-1 && !info.IsDir() {
			return nil, fmt.Errorf("proposal path parent is not a directory: %s", relativePath)
		}
		if index == len(components)-1 && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("proposal payload is not a regular file: %s", relativePath)
		}
	}
	return os.ReadFile(filepath.Join(root, filepath.FromSlash(relativePath)))
}

func readOptionalRegularFile(root, relativePath string) ([]byte, error) {
	if err := identity.ValidateRelativePath(relativePath, false); err != nil {
		return nil, err
	}
	data, err := readRegularFile(root, relativePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

func decodeOne(data []byte, output any) error {
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

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func isLowerHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func equalOptionalStrings(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalProposalStrings(left, right []string) bool {
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

func setOf(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}
