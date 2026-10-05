package publish

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
)

type PresentationModule struct {
	ModuleID           string          `json:"module_id"`
	Lifecycle          string          `json:"lifecycle"`
	DesiredSourceSHA   string          `json:"desired_source_sha,omitempty"`
	SelectedReceiptIDs []string        `json:"selected_receipt_ids,omitempty"`
	Coverage           json.RawMessage `json:"coverage"`
	ReasonCode         string          `json:"reason_code,omitempty"`
}

type PresentationCollectorHealth struct {
	CollectorID string     `json:"collector_id"`
	Status      string     `json:"status"`
	ObservedAt  time.Time  `json:"observed_at"`
	LastGoodAt  *time.Time `json:"last_good_at,omitempty"`
}

type PresentationSnapshot struct {
	SchemaVersion   int                           `json:"schema_version"`
	RecordType      string                        `json:"record_type"`
	ID              string                        `json:"presentation_id"`
	Generation      uint64                        `json:"generation"`
	AsOf            time.Time                     `json:"as_of"`
	Modules         []PresentationModule          `json:"modules"`
	CollectorHealth []PresentationCollectorHealth `json:"collector_health"`
}

type presentationSnapshotIdentity struct {
	SchemaVersion   int                           `json:"schema_version"`
	RecordType      string                        `json:"record_type"`
	Generation      uint64                        `json:"generation"`
	AsOf            time.Time                     `json:"as_of"`
	Modules         []PresentationModule          `json:"modules"`
	CollectorHealth []PresentationCollectorHealth `json:"collector_health"`
}

type PresentationProposalInput struct {
	ProposalRoot    string
	BaseDataRoot    string
	ExpectedBase    string
	AsOf            time.Time
	Modules         []PresentationModule
	CollectorHealth []PresentationCollectorHealth
}

// BuildPresentationProposal stages one safe as-of snapshot and an append-only
// presentation index update. It never accepts or publishes the proposal.
func BuildPresentationProposal(input PresentationProposalInput) (Proposal, error) {
	if err := ensureProposalRootsSeparated(input.BaseDataRoot, input.ProposalRoot); err != nil {
		return Proposal{}, err
	}
	if !isLowerHex(input.ExpectedBase, 40) || input.AsOf.IsZero() || len(input.Modules) == 0 {
		return Proposal{}, fmt.Errorf("presentation proposal requires an exact base, as-of time, and complete module set")
	}
	modules, err := normalizePresentationModules(input.Modules)
	if err != nil {
		return Proposal{}, err
	}
	health := append([]PresentationCollectorHealth(nil), input.CollectorHealth...)
	sort.Slice(health, func(left, right int) bool {
		if health[left].CollectorID != health[right].CollectorID {
			return health[left].CollectorID < health[right].CollectorID
		}
		return health[left].ObservedAt.Before(health[right].ObservedAt)
	})
	for _, record := range health {
		if err = validatePresentationHealth(record.CollectorID, record.Status, record.ObservedAt); err != nil {
			return Proposal{}, err
		}
	}
	currentIndex, err := loadPresentationIndex(input.BaseDataRoot)
	if err != nil {
		return Proposal{}, err
	}
	snapshot, nextIndex, expectedPointer, err := nextPresentationSnapshot(input.AsOf, modules, health, currentIndex)
	if err != nil {
		return Proposal{}, err
	}
	snapshotPath, err := identity.PresentationPath(snapshot.ID)
	if err != nil {
		return Proposal{}, err
	}
	snapshotData, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return Proposal{}, fmt.Errorf("encode presentation snapshot: %w", err)
	}
	indexData, err := json.MarshalIndent(nextIndex, "", "  ")
	if err != nil {
		return Proposal{}, fmt.Errorf("encode presentation index: %w", err)
	}
	if err = writeCatalogProposalPayload(input.ProposalRoot, snapshotPath, snapshotData); err != nil {
		return Proposal{}, err
	}
	indexPath := identity.PresentationIndexPath()
	changes := []ProposalChange{
		{Path: snapshotPath, Operation: "add_immutable", Digest: digestBytes(snapshotData), RecordID: snapshot.ID},
	}
	typed, err := typedDatasetPresent(input.BaseDataRoot)
	if err != nil {
		return Proposal{}, err
	}
	if typed {
		records, readErr := index.ReadRecords(input.BaseDataRoot, snapshot.AsOf)
		if readErr != nil {
			return Proposal{}, fmt.Errorf("read accepted records for presentation indexes: %w", readErr)
		}
		receiptIDs := make([]string, 0)
		for _, module := range snapshot.Modules {
			receiptIDs = append(receiptIDs, module.SelectedReceiptIDs...)
		}
		receiptIDs, err = identity.SortedSet(receiptIDs)
		if err != nil {
			return Proposal{}, err
		}
		records.Presentations = append(records.Presentations, index.PresentationSnapshotRecord{
			ID: snapshot.ID, Generation: snapshot.Generation, AsOf: snapshot.AsOf, SelectionReceiptIDs: receiptIDs,
		})
		records.RecordDigests[snapshotPath] = digestBytes(snapshotData)
		changes, err = stageChangedTypedIndexes(input.ProposalRoot, input.BaseDataRoot, records, changes)
		if err != nil {
			return Proposal{}, err
		}
	} else {
		if err = writeCatalogProposalPayload(input.ProposalRoot, indexPath, indexData); err != nil {
			return Proposal{}, err
		}
		changes = append(changes, ProposalChange{
			Path: indexPath, Operation: "set_pointer", Digest: digestBytes(indexData), RecordID: nextIndex.IndexID,
		})
	}
	proposal, err := IdentifyProposal(Proposal{
		Kind: ProposalPresentation, Subject: "presentation", SourceGeneration: snapshot.Generation,
		ExpectedBase: input.ExpectedBase, ExpectedPointer: expectedPointer,
		Producer: ProposalProducer{ID: expectedProposalProducer(ProposalPresentation), Version: "1.0.0", ContractVersion: 1},
		Paths:    proposalChangePaths(changes), Changes: changes,
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
		return Proposal{}, fmt.Errorf("encode presentation proposal: %w", err)
	}
	if err = os.WriteFile(filepath.Join(input.ProposalRoot, proposalFilename), proposalData, 0o600); err != nil {
		return Proposal{}, err
	}
	if err = validatePresentationProposal(proposal, input.ProposalRoot, input.BaseDataRoot); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

func normalizePresentationModules(modules []PresentationModule) ([]PresentationModule, error) {
	ordered := append([]PresentationModule(nil), modules...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].ModuleID < ordered[right].ModuleID })
	for position := range ordered {
		if position > 0 && ordered[position-1].ModuleID == ordered[position].ModuleID {
			return nil, fmt.Errorf("presentation snapshot repeats module %s", ordered[position].ModuleID)
		}
		if err := validatePresentationModule(ordered[position].ModuleID, ordered[position].Lifecycle, ordered[position].Coverage); err != nil {
			return nil, err
		}
		receiptIDs, err := identity.SortedSet(ordered[position].SelectedReceiptIDs)
		if err != nil {
			return nil, fmt.Errorf("presentation module %s receipts: %w", ordered[position].ModuleID, err)
		}
		ordered[position].SelectedReceiptIDs = receiptIDs
	}
	return ordered, nil
}

func loadPresentationIndex(baseDataRoot string) (*index.PresentationIndex, error) {
	data, err := readOptionalDataFile(baseDataRoot, identity.PresentationIndexPath())
	if err != nil || len(data) == 0 {
		return nil, err
	}
	var current index.PresentationIndex
	if err = decodeOne(data, &current); err != nil {
		return nil, fmt.Errorf("decode accepted presentation index: %w", err)
	}
	if current.SchemaVersion != 1 || current.RecordType != "presentation_index" {
		return nil, fmt.Errorf("accepted presentation index metadata is invalid")
	}
	if err = validateProposalIndexID(presentationIndexKind, &current, current.IndexID); err != nil {
		return nil, fmt.Errorf("accepted presentation index identity is invalid: %w", err)
	}
	if len(current.Snapshots) == 0 {
		if current.CurrentID != nil {
			return nil, fmt.Errorf("accepted presentation index has a pointer without history")
		}
		return &current, nil
	}
	if current.CurrentID == nil || *current.CurrentID != current.Snapshots[len(current.Snapshots)-1].ID {
		return nil, fmt.Errorf("accepted presentation index pointer does not match its history")
	}
	return &current, nil
}

func nextPresentationSnapshot(asOf time.Time, modules []PresentationModule, health []PresentationCollectorHealth, current *index.PresentationIndex) (PresentationSnapshot, index.PresentationIndex, *string, error) {
	snapshot := PresentationSnapshot{
		SchemaVersion: 2, RecordType: "presentation_snapshot", AsOf: asOf.UTC(),
		Modules: modules, CollectorHealth: health,
	}
	nextIndex := index.PresentationIndex{
		SchemaVersion: 1, RecordType: "presentation_index", Snapshots: []index.PresentationSnapshotRecord{},
	}
	var expectedPointer *string
	if current != nil {
		nextIndex.Snapshots = append(nextIndex.Snapshots, current.Snapshots...)
		if len(current.Snapshots) > 0 {
			previous := current.Snapshots[len(current.Snapshots)-1]
			if !snapshot.AsOf.After(previous.AsOf) {
				return PresentationSnapshot{}, index.PresentationIndex{}, nil, fmt.Errorf("presentation as-of time must advance")
			}
			snapshot.Generation = previous.Generation + 1
			pointer := previous.ID
			expectedPointer = &pointer
		} else {
			snapshot.Generation = 1
		}
	} else {
		snapshot.Generation = 1
	}
	snapshotIdentity := presentationSnapshotIdentity{
		SchemaVersion: snapshot.SchemaVersion, RecordType: snapshot.RecordType,
		Generation: snapshot.Generation, AsOf: snapshot.AsOf,
		Modules: snapshot.Modules, CollectorHealth: snapshot.CollectorHealth,
	}
	id, err := identity.ID("presentation", snapshot.SchemaVersion, snapshotIdentity)
	if err != nil {
		return PresentationSnapshot{}, index.PresentationIndex{}, nil, err
	}
	snapshot.ID = id
	receiptIDs := make([]string, 0)
	for _, module := range snapshot.Modules {
		receiptIDs = append(receiptIDs, module.SelectedReceiptIDs...)
	}
	receiptIDs, err = identity.SortedSet(receiptIDs)
	if err != nil {
		return PresentationSnapshot{}, index.PresentationIndex{}, nil, err
	}
	nextIndex.Snapshots = append(nextIndex.Snapshots, index.PresentationSnapshotRecord{
		ID: snapshot.ID, Generation: snapshot.Generation, AsOf: snapshot.AsOf, SelectionReceiptIDs: receiptIDs,
	})
	currentID := snapshot.ID
	nextIndex.CurrentID = &currentID
	nextIndex.IndexID, err = proposalIndexID(presentationIndexKind, nextIndex)
	if err != nil {
		return PresentationSnapshot{}, index.PresentationIndex{}, nil, err
	}
	return snapshot, nextIndex, expectedPointer, nil
}
