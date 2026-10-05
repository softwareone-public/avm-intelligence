package publish

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
	"github.com/softwareone-public/avm-intelligence/internal/worker"
)

type ModuleEvidenceProposalInput struct {
	ProposalRoot string
	BaseDataRoot string
	ExpectedBase string
	Repository   string
	WorkerInput  worker.Input
	Artifact     worker.Artifact
	ValidationID string
	SealedAt     time.Time
}

type sourceRevisionDocument struct {
	SchemaVersion int    `json:"schema_version"`
	RecordType    string `json:"record_type"`
	ModuleID      string `json:"module_id"`
	Repository    string `json:"repository"`
	SourceSHA     string `json:"source_sha"`
	Immutable     bool   `json:"immutable"`
}

type producerProvenanceDocument struct {
	SchemaVersion    int            `json:"schema_version"`
	RecordType       string         `json:"record_type"`
	ProvenanceID     string         `json:"provenance_id"`
	ModuleID         string         `json:"module_id"`
	SourceSHA        string         `json:"source_sha"`
	Producer         index.Producer `json:"producer"`
	FactsID          string         `json:"facts_id"`
	InputFingerprint string         `json:"input_fingerprint"`
	Capabilities     []string       `json:"capabilities"`
	RecordedAt       time.Time      `json:"recorded_at"`
}

type producerProvenanceIdentity struct {
	SchemaVersion    int            `json:"schema_version"`
	RecordType       string         `json:"record_type"`
	ModuleID         string         `json:"module_id"`
	SourceSHA        string         `json:"source_sha"`
	Producer         index.Producer `json:"producer"`
	FactsID          string         `json:"facts_id"`
	InputFingerprint string         `json:"input_fingerprint"`
	Capabilities     []string       `json:"capabilities"`
	RecordedAt       time.Time      `json:"recorded_at"`
}

// BuildModuleEvidenceProposal validates one sealed acquisition artifact and
// prepares immutable source, facts, provenance, and generated indexes. It does
// not write to accepted data or operational state.
func BuildModuleEvidenceProposal(input ModuleEvidenceProposalInput) (Proposal, error) {
	if err := validateModuleEvidenceProposalInput(input); err != nil {
		return Proposal{}, err
	}
	sealed, err := worker.Seal(input.WorkerInput.Request, input.WorkerInput.Owner, input.WorkerInput.Fence, input.Artifact, input.ValidationID, input.SealedAt)
	if err != nil {
		return Proposal{}, fmt.Errorf("validate sealed acquisition artifact: %w", err)
	}
	if sealed.Work.Phase != queue.PhaseAcquireSource || input.Artifact.Facts == nil {
		return Proposal{}, fmt.Errorf("initial evidence proposal supports source-acquisition facts only")
	}
	return buildAcquisitionEvidenceProposal(input)
}

func validateModuleEvidenceProposalInput(input ModuleEvidenceProposalInput) error {
	if strings.TrimSpace(input.ProposalRoot) == "" || strings.TrimSpace(input.BaseDataRoot) == "" || !isLowerHex(input.ExpectedBase, 40) || !validCatalogRepositoryIdentity(input.Repository) {
		return fmt.Errorf("module evidence proposal base, roots, and repository are required")
	}
	if err := ensureProposalRootsSeparated(input.BaseDataRoot, input.ProposalRoot); err != nil {
		return err
	}
	request := input.WorkerInput.Request
	if request.Work == nil || request.Work.Phase != queue.PhaseAcquireSource || input.WorkerInput.Owner == "" || input.WorkerInput.Fence == 0 || input.ValidationID == "" {
		return fmt.Errorf("module evidence proposal requires a trusted acquisition request and seal identity")
	}
	if input.SealedAt.IsZero() || input.WorkerInput.ObservedAt.IsZero() {
		return fmt.Errorf("module evidence proposal timestamps are required")
	}
	if _, err := os.Stat(filepath.Join(input.BaseDataRoot, "dataset.json")); err != nil {
		return fmt.Errorf("typed catalog dataset must be accepted before module evidence: %w", err)
	}
	return nil
}

func buildAcquisitionEvidenceProposal(input ModuleEvidenceProposalInput) (Proposal, error) {
	request := input.WorkerInput.Request
	facts := *input.Artifact.Facts
	records, err := index.ReadRecords(input.BaseDataRoot, input.SealedAt)
	if err != nil {
		return Proposal{}, fmt.Errorf("read accepted typed catalog records: %w", err)
	}
	if err = requireEvidenceModule(records, request.ModuleID, input.Repository); err != nil {
		return Proposal{}, err
	}
	if err = os.MkdirAll(input.ProposalRoot, 0o755); err != nil {
		return Proposal{}, fmt.Errorf("create evidence proposal directory: %w", err)
	}
	changes := make([]ProposalChange, 0, 8)
	if err = addSourceRevisionEvidence(input, &records, &changes); err != nil {
		return Proposal{}, err
	}
	if err = addFactsAndProvenanceEvidence(input, facts, &records, &changes); err != nil {
		return Proposal{}, err
	}
	indexes, err := index.Build(records)
	if err != nil {
		return Proposal{}, fmt.Errorf("rebuild indexes for module evidence: %w", err)
	}
	indexChanges, err := addChangedEvidenceIndexes(input, indexes)
	if err != nil {
		return Proposal{}, err
	}
	changes = append(changes, indexChanges...)
	if len(changes) == 0 {
		return Proposal{}, fmt.Errorf("module evidence proposal contains no new records")
	}
	expectedPointer, err := currentModuleIndexPointer(input.BaseDataRoot, request.ModuleID)
	if err != nil {
		return Proposal{}, err
	}
	proposal, err := IdentifyProposal(Proposal{
		Kind: ProposalModuleEvidence, Subject: request.ModuleID + "@" + request.SourceSHA,
		SourceGeneration: request.Work.DesiredGeneration, ExpectedBase: input.ExpectedBase,
		ExpectedPointer: expectedPointer,
		Producer:        ProposalProducer{ID: expectedProposalProducer(ProposalModuleEvidence), Version: "2.0.0", ContractVersion: 1},
		Paths:           proposalChangePaths(changes), Changes: changes,
	})
	if err != nil {
		return Proposal{}, err
	}
	if err = validateProposal(proposal); err != nil {
		return Proposal{}, fmt.Errorf("validate module evidence proposal: %w", err)
	}
	return proposal, nil
}

func requireEvidenceModule(records index.Records, moduleID, repository string) error {
	for _, module := range records.Modules {
		if module.ModuleID == moduleID && module.Repository == repository && module.Lifecycle == "active" {
			return nil
		}
	}
	return fmt.Errorf("module evidence subject is not an active member of the accepted catalog")
}

func addSourceRevisionEvidence(input ModuleEvidenceProposalInput, records *index.Records, changes *[]ProposalChange) error {
	request := input.WorkerInput.Request
	path, err := identity.ModuleSourcePath(request.ModuleID, request.SourceSHA)
	if err != nil {
		return err
	}
	document := sourceRevisionDocument{
		SchemaVersion: 2, RecordType: "source_revision", ModuleID: request.ModuleID,
		Repository: input.Repository, SourceSHA: request.SourceSHA, Immutable: true,
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	change, added, err := stageImmutableEvidence(input, path, "", data)
	if err != nil || !added {
		return err
	}
	records.SourceRevisions = append(records.SourceRevisions, index.SourceRevisionRecord{
		ModuleID: request.ModuleID, Repository: input.Repository, SourceSHA: request.SourceSHA, Immutable: true,
	})
	records.RecordDigests[path] = digestBytes(data)
	*changes = append(*changes, change)
	return nil
}

func addFactsAndProvenanceEvidence(input ModuleEvidenceProposalInput, facts terraform.Facts, records *index.Records, changes *[]ProposalChange) error {
	request := input.WorkerInput.Request
	factsPath, err := identity.ModuleRecordPath(request.ModuleID, request.SourceSHA, "facts", facts.FactsID)
	if err != nil {
		return err
	}
	factsData, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		return err
	}
	factsChange, factsAdded, err := stageImmutableEvidence(input, factsPath, facts.FactsID, factsData)
	if err != nil {
		return err
	}
	if factsAdded {
		factRecord, recordErr := index.FactRecordFromTypedFacts(facts)
		if recordErr != nil {
			return fmt.Errorf("derive provider dependencies from facts: %w", recordErr)
		}
		records.Facts = append(records.Facts, factRecord)
		records.RecordDigests[factsPath] = digestBytes(factsData)
		*changes = append(*changes, factsChange)
	}
	provenance, provenanceData, err := acquisitionProvenance(input, facts)
	if err != nil {
		return err
	}
	provenancePath, err := identity.ModuleRecordPath(request.ModuleID, request.SourceSHA, "provenance", provenance.ID)
	if err != nil {
		return err
	}
	provenanceChange, provenanceAdded, err := stageImmutableEvidence(input, provenancePath, provenance.ID, provenanceData)
	if err != nil {
		return err
	}
	if provenanceAdded {
		records.Provenance = append(records.Provenance, provenance)
		records.RecordDigests[provenancePath] = digestBytes(provenanceData)
		*changes = append(*changes, provenanceChange)
	}
	return nil
}

func acquisitionProvenance(input ModuleEvidenceProposalInput, facts terraform.Facts) (index.ProvenanceRecord, []byte, error) {
	request := input.WorkerInput.Request
	capabilities, err := identity.SortedSet(request.Work.Capabilities)
	if err != nil {
		return index.ProvenanceRecord{}, nil, err
	}
	producer := index.Producer{ID: input.WorkerInput.Producer.ID, Version: input.WorkerInput.Producer.Version, ContractVersion: input.WorkerInput.Producer.ContractVersion}
	recordedAt := input.WorkerInput.ObservedAt.UTC()
	identityValue := producerProvenanceIdentity{
		SchemaVersion: 2, RecordType: "provenance", ModuleID: request.ModuleID,
		SourceSHA: request.SourceSHA, Producer: producer, FactsID: facts.FactsID,
		InputFingerprint: request.Fingerprint, Capabilities: capabilities, RecordedAt: recordedAt,
	}
	provenanceID, err := identity.ID("provenance", 2, identityValue)
	if err != nil {
		return index.ProvenanceRecord{}, nil, err
	}
	document := producerProvenanceDocument{
		SchemaVersion: identityValue.SchemaVersion, RecordType: identityValue.RecordType,
		ProvenanceID: provenanceID, ModuleID: identityValue.ModuleID, SourceSHA: identityValue.SourceSHA,
		Producer: identityValue.Producer, FactsID: identityValue.FactsID,
		InputFingerprint: identityValue.InputFingerprint, Capabilities: identityValue.Capabilities,
		RecordedAt: identityValue.RecordedAt,
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return index.ProvenanceRecord{}, nil, err
	}
	return index.ProvenanceRecord{
		ID: provenanceID, ModuleID: request.ModuleID, SourceSHA: request.SourceSHA,
		FactsID: facts.FactsID, InputFingerprint: request.Fingerprint,
		Producer: producer, Capabilities: capabilities,
	}, data, nil
}

func stageImmutableEvidence(input ModuleEvidenceProposalInput, path, recordID string, data []byte) (ProposalChange, bool, error) {
	current, err := readOptionalDataFile(input.BaseDataRoot, path)
	if err != nil {
		return ProposalChange{}, false, err
	}
	if len(current) > 0 {
		if !bytes.Equal(current, data) {
			return ProposalChange{}, false, fmt.Errorf("immutable module evidence already exists with different content at %s", path)
		}
		return ProposalChange{}, false, nil
	}
	if err = writeEvidenceProposalPayload(input.ProposalRoot, path, data); err != nil {
		return ProposalChange{}, false, err
	}
	return ProposalChange{Path: path, Operation: "add_immutable", Digest: digestBytes(data), RecordID: recordID}, true, nil
}

func addChangedEvidenceIndexes(input ModuleEvidenceProposalInput, indexes index.Indexes) ([]ProposalChange, error) {
	files, err := indexes.Files()
	if err != nil {
		return nil, fmt.Errorf("encode module evidence indexes: %w", err)
	}
	changes := make([]ProposalChange, 0, len(files))
	for path, data := range files {
		current, readErr := readOptionalDataFile(input.BaseDataRoot, path)
		if readErr != nil {
			return nil, readErr
		}
		if bytes.Equal(current, data) {
			continue
		}
		recordID, idErr := generatedCatalogRecordID(data)
		if idErr != nil {
			return nil, idErr
		}
		if err = writeEvidenceProposalPayload(input.ProposalRoot, path, data); err != nil {
			return nil, err
		}
		changes = append(changes, ProposalChange{Path: path, Operation: "set_pointer", Digest: digestBytes(data), RecordID: recordID})
	}
	return changes, nil
}

func currentModuleIndexPointer(dataRoot, moduleID string) (*string, error) {
	path, err := identity.ModuleIndexPath(moduleID)
	if err != nil {
		return nil, err
	}
	data, err := readOptionalDataFile(dataRoot, path)
	if err != nil || len(data) == 0 {
		return nil, err
	}
	var moduleIndex index.ModuleIndex
	if err = json.Unmarshal(data, &moduleIndex); err != nil {
		return nil, err
	}
	if moduleIndex.IndexID == "" || moduleIndex.ModuleID != moduleID {
		return nil, fmt.Errorf("accepted module index identity is invalid")
	}
	return optionalString(moduleIndex.IndexID), nil
}

func writeEvidenceProposalPayload(root, canonicalPath string, data []byte) error {
	if !strings.HasPrefix(canonicalPath, "data/") {
		return fmt.Errorf("module evidence payload is not under data/: %s", canonicalPath)
	}
	if err := identity.ValidateRelativePath(canonicalPath, false); err != nil {
		return err
	}
	destination := filepath.Join(root, filepath.FromSlash(canonicalPath))
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o644)
}
