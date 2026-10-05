package publish

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
)

// ProviderSnapshotProposalInput contains a collected snapshot and the accepted
// data tree against which its provider-index update will be prepared.
type ProviderSnapshotProposalInput struct {
	ProposalRoot     string
	BaseDataRoot     string
	ExpectedBase     string
	SourceGeneration uint64
	Snapshot         assessment.ProviderSnapshot
}

// BuildProviderSnapshotProposal stages one immutable provider snapshot and an
// append-only provider index update without accepting or publishing either.
func BuildProviderSnapshotProposal(input ProviderSnapshotProposalInput) (Proposal, error) {
	if err := ensureProposalRootsSeparated(input.BaseDataRoot, input.ProposalRoot); err != nil {
		return Proposal{}, err
	}
	snapshot, err := identifyProviderSnapshot(input.Snapshot)
	if err != nil {
		return Proposal{}, err
	}
	address, version, err := parseProviderSubject(snapshot.ProviderAddress + "@" + snapshot.ProviderVersion)
	if err != nil {
		return Proposal{}, err
	}
	indexPath, err := identity.ProviderIndexPath(address)
	if err != nil {
		return Proposal{}, err
	}
	nextIndex, expectedPointer, err := extendProviderSnapshotIndex(input.BaseDataRoot, address, version, snapshot.SnapshotID)
	if err != nil {
		return Proposal{}, err
	}
	return stageProviderSnapshotProposal(input, snapshot, address, version, indexPath, nextIndex, expectedPointer)
}

func extendProviderSnapshotIndex(baseDataRoot, address, version, snapshotID string) (index.ProviderIndex, *string, error) {
	currentIndex, expectedPointer, err := loadAcceptedProviderIndex(baseDataRoot, address)
	if err != nil {
		return index.ProviderIndex{}, nil, err
	}
	nextIndex := index.ProviderIndex{
		SchemaVersion: 1, RecordType: "provider_index", ProviderAddress: address,
		Snapshots: []index.ProviderSnapshotRecord{},
	}
	if currentIndex != nil {
		nextIndex.Snapshots = append(nextIndex.Snapshots, currentIndex.Snapshots...)
	}
	if err = appendProviderSnapshotRecord(&nextIndex, address, version, snapshotID); err != nil {
		return index.ProviderIndex{}, nil, err
	}
	if err = identifyProviderIndex(&nextIndex); err != nil {
		return index.ProviderIndex{}, nil, err
	}
	return nextIndex, expectedPointer, nil
}

func loadAcceptedProviderIndex(baseDataRoot, address string) (*index.ProviderIndex, *string, error) {
	indexPath, err := identity.ProviderIndexPath(address)
	if err != nil {
		return nil, nil, err
	}
	currentIndex, err := loadProviderIndex(baseDataRoot, indexPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load accepted provider index: %w", err)
	}
	if currentIndex == nil {
		return nil, nil, nil
	}
	if currentIndex.SchemaVersion != 1 || currentIndex.RecordType != "provider_index" || currentIndex.ProviderAddress != address {
		return nil, nil, fmt.Errorf("accepted provider index does not match provider address %q", address)
	}
	if err = validateProposalIndexID(providerIndexKind, currentIndex, currentIndex.IndexID); err != nil {
		return nil, nil, fmt.Errorf("accepted provider index identity is invalid: %w", err)
	}
	if err = validateProviderIndexRecords(currentIndex.Snapshots, address); err != nil {
		return nil, nil, err
	}
	pointer := currentIndex.IndexID
	return currentIndex, &pointer, nil
}

func validateProviderIndexRecords(records []index.ProviderSnapshotRecord, address string) error {
	seenSnapshots := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record.Address != address {
			return fmt.Errorf("accepted provider index contains a foreign provider address")
		}
		if _, err := identity.ProviderSnapshotManifestPath(record.Address, record.Version, record.ID); err != nil {
			return fmt.Errorf("accepted provider index contains an invalid snapshot record: %w", err)
		}
		if _, duplicate := seenSnapshots[record.ID]; duplicate {
			return fmt.Errorf("accepted provider index contains duplicate snapshot %s", record.ID)
		}
		seenSnapshots[record.ID] = struct{}{}
	}
	return nil
}

func appendProviderSnapshotRecord(providerIndex *index.ProviderIndex, address, version, snapshotID string) error {
	for _, record := range providerIndex.Snapshots {
		if record.ID == snapshotID {
			return fmt.Errorf("provider snapshot %s is already indexed", snapshotID)
		}
	}
	providerIndex.Snapshots = append(providerIndex.Snapshots, index.ProviderSnapshotRecord{
		ID: snapshotID, Address: address, Version: version,
	})
	sort.Slice(providerIndex.Snapshots, func(left, right int) bool {
		if providerIndex.Snapshots[left].Version != providerIndex.Snapshots[right].Version {
			return providerIndex.Snapshots[left].Version < providerIndex.Snapshots[right].Version
		}
		return providerIndex.Snapshots[left].ID < providerIndex.Snapshots[right].ID
	})
	return nil
}

func identifyProviderIndex(providerIndex *index.ProviderIndex) error {
	var err error
	providerIndex.IndexID, err = proposalIndexID(providerIndexKind, providerIndex)
	if err != nil {
		return fmt.Errorf("identify updated provider index: %w", err)
	}
	return nil
}

func stageProviderSnapshotProposal(input ProviderSnapshotProposalInput, snapshot assessment.ProviderSnapshot, address, version, indexPath string, nextIndex index.ProviderIndex, expectedPointer *string) (Proposal, error) {
	_, changes, err := stageProviderSnapshotRecords(input, snapshot, address, version, indexPath, nextIndex)
	if err != nil {
		return Proposal{}, err
	}
	proposal, err := IdentifyProposal(Proposal{
		Kind: ProposalProviderSnapshot, Subject: address + "@" + version,
		SourceGeneration: input.SourceGeneration, ExpectedBase: input.ExpectedBase,
		ExpectedPointer: expectedPointer,
		Producer:        ProposalProducer{ID: expectedProposalProducer(ProposalProviderSnapshot), Version: "1.0.0", ContractVersion: 1},
		Paths:           proposalChangePaths(changes), Changes: changes,
	})
	if err != nil {
		return Proposal{}, err
	}
	if err = validateProposal(proposal); err != nil {
		return Proposal{}, err
	}
	if err = os.MkdirAll(input.ProposalRoot, 0o755); err != nil {
		return Proposal{}, err
	}
	proposalData, err := json.Marshal(proposal)
	if err != nil {
		return Proposal{}, fmt.Errorf("encode provider snapshot proposal: %w", err)
	}
	if err = os.WriteFile(filepath.Join(input.ProposalRoot, proposalFilename), proposalData, 0o600); err != nil {
		return Proposal{}, err
	}
	if err = validateProviderSnapshotProposal(proposal, input.ProposalRoot, input.BaseDataRoot); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

func stageProviderSnapshotRecords(input ProviderSnapshotProposalInput, snapshot assessment.ProviderSnapshot, address, version, indexPath string, nextIndex index.ProviderIndex) (string, []ProposalChange, error) {
	snapshotPath, err := identity.ProviderSnapshotManifestPath(address, version, snapshot.SnapshotID)
	if err != nil {
		return "", nil, err
	}
	snapshotData, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("encode provider snapshot: %w", err)
	}
	if err = writeCatalogProposalPayload(input.ProposalRoot, snapshotPath, snapshotData); err != nil {
		return "", nil, err
	}
	changes := []ProposalChange{
		{Path: snapshotPath, Operation: "add_immutable", Digest: digestBytes(snapshotData), RecordID: snapshot.SnapshotID},
	}
	typed, err := typedDatasetPresent(input.BaseDataRoot)
	if err != nil {
		return "", nil, err
	}
	if typed {
		observedAt, parseErr := time.Parse(time.RFC3339, snapshot.ObservedAt)
		if parseErr != nil {
			return "", nil, fmt.Errorf("parse provider snapshot observation time: %w", parseErr)
		}
		records, readErr := index.ReadRecords(input.BaseDataRoot, observedAt)
		if readErr != nil {
			return "", nil, fmt.Errorf("read accepted records for provider indexes: %w", readErr)
		}
		records.ProviderSnapshots = append(records.ProviderSnapshots, index.ProviderSnapshotRecord{
			ID: snapshot.SnapshotID, Address: address, Version: version,
		})
		records.RecordDigests[snapshotPath] = digestBytes(snapshotData)
		changes, err = stageChangedTypedIndexes(input.ProposalRoot, input.BaseDataRoot, records, changes)
		if err != nil {
			return "", nil, err
		}
		return snapshotPath, changes, nil
	}
	indexData, err := json.MarshalIndent(nextIndex, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("encode provider index: %w", err)
	}
	if err = writeCatalogProposalPayload(input.ProposalRoot, indexPath, indexData); err != nil {
		return "", nil, err
	}
	changes = append(changes, ProposalChange{Path: indexPath, Operation: "set_pointer", Digest: digestBytes(indexData), RecordID: nextIndex.IndexID})
	return snapshotPath, changes, nil
}

func identifyProviderSnapshot(snapshot assessment.ProviderSnapshot) (assessment.ProviderSnapshot, error) {
	if err := validateProviderSnapshotMetadata(snapshot); err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	if err := validateProviderSnapshotCoverage(snapshot.Coverage, len(snapshot.Entries)); err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	entries, err := normalizeProviderEntries(snapshot.Entries)
	if err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	snapshot.Entries = entries
	snapshotID, err := identity.ID("snapshot", snapshot.SchemaVersion, snapshot)
	if err != nil {
		return assessment.ProviderSnapshot{}, fmt.Errorf("identify provider snapshot: %w", err)
	}
	snapshot.SnapshotID = snapshotID
	return snapshot, nil
}

func validateProviderSnapshotMetadata(snapshot assessment.ProviderSnapshot) error {
	if snapshot.SchemaVersion != 2 || snapshot.RecordType != "provider_snapshot" || snapshot.SnapshotID != "" ||
		!isLowerHex(snapshot.SourceDigest, 64) || snapshot.Producer.ID == "" || snapshot.Producer.Version == "" || snapshot.Producer.ContractVersion < 1 {
		return fmt.Errorf("provider snapshot metadata or source identity is invalid")
	}
	if _, err := time.Parse(time.RFC3339, snapshot.ObservedAt); err != nil {
		return fmt.Errorf("provider snapshot observation time is invalid")
	}
	if _, err := time.Parse(time.RFC3339, snapshot.Coverage.ObservedAt); err != nil {
		return fmt.Errorf("provider snapshot coverage time is invalid")
	}
	return nil
}

func validateProviderSnapshotCoverage(coverage assessment.SnapshotCoverage, entryCount int) error {
	if !coverage.Complete {
		return nil
	}
	if len(coverage.Capabilities) == 0 || entryCount == 0 {
		return fmt.Errorf("complete provider snapshot requires covered capabilities and entries")
	}
	for _, capabilityCoverage := range coverage.Capabilities {
		if capabilityCoverage != "complete" {
			return fmt.Errorf("complete provider snapshot contains incomplete capability coverage")
		}
	}
	return nil
}

func normalizeProviderEntries(entries []assessment.ProviderEntry) ([]assessment.ProviderEntry, error) {
	normalized := append([]assessment.ProviderEntry(nil), entries...)
	sort.Slice(normalized, func(left, right int) bool {
		if normalized[left].Kind != normalized[right].Kind {
			return normalized[left].Kind < normalized[right].Kind
		}
		return normalized[left].Name < normalized[right].Name
	})
	for index, entry := range normalized {
		if entry.Kind == "" || entry.Name == "" || (index > 0 && normalized[index-1].Kind == entry.Kind && normalized[index-1].Name == entry.Name) {
			return nil, fmt.Errorf("provider snapshot entries contain an empty or duplicate identity")
		}
	}
	return normalized, nil
}
