package publish

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

// SelectionExportProposal contains a proposal envelope and only the payloads
// it declares. The caller obtains Export from PendingSelectionExports after
// reading the durable state authority.
type SelectionExportProposal struct {
	Proposal Proposal
	Payloads map[string][]byte
}

// BuildSelectionExportProposal projects one pending durable state selection
// into the reviewed module-data tree and rebuilds the exact affected indexes.
func BuildSelectionExportProposal(dataRoot, expectedBase string, export state.SelectionExport) (SelectionExportProposal, error) {
	if !isLowerHex(expectedBase, 40) {
		return SelectionExportProposal{}, fmt.Errorf("selection export requires an exact accepted base commit")
	}
	if export.SchemaVersion != 1 || export.RecordType != "selection_export_intent" || export.Status != state.ExportPending || export.ID != export.SelectionID || export.ID != export.Receipt.ID || export.ModuleID != export.Receipt.ModuleID || export.ContextID != export.Receipt.ContextID || export.CreatedAt.IsZero() || !export.CreatedAt.Equal(export.Receipt.SelectedAt) {
		return SelectionExportProposal{}, fmt.Errorf("selection export outbox identity or status is invalid")
	}
	if err := export.Receipt.Validate(); err != nil {
		return SelectionExportProposal{}, fmt.Errorf("validate durable selection receipt: %w", err)
	}

	records, err := index.ReadRecords(dataRoot, time.Time{})
	if err != nil {
		return SelectionExportProposal{}, fmt.Errorf("read exact accepted data tree: %w", err)
	}
	if export.CreatedAt.After(records.CreatedAt) {
		records.CreatedAt = export.CreatedAt.UTC()
	}
	currentIndexes, err := index.Build(records)
	if err != nil {
		return SelectionExportProposal{}, fmt.Errorf("build current accepted indexes: %w", err)
	}
	if _, err = currentIndexes.Files(); err != nil {
		return SelectionExportProposal{}, fmt.Errorf("verify current accepted index digests: %w", err)
	}
	var expectedPointer *string
	for _, module := range currentIndexes.Modules {
		if module.ModuleID != export.ModuleID {
			continue
		}
		for _, selection := range module.CurrentSelections {
			if selection.AVMReleaseVersion == export.Receipt.AVMReleaseVersion && selection.ContextID == export.ContextID {
				if expectedPointer != nil {
					return SelectionExportProposal{}, fmt.Errorf("accepted indexes contain duplicate current selection pointers")
				}
				selectionID := selection.SelectionID
				expectedPointer = &selectionID
			}
		}
	}
	for _, selection := range records.Selections {
		if selection.ID == export.ID {
			return SelectionExportProposal{}, fmt.Errorf("selection receipt already exists in accepted data")
		}
	}

	receipt := selectionRecordForExport(export.Receipt)
	receiptPath, err := identity.SelectionPath(receipt.ModuleID, receipt.ContextID, receipt.ID)
	if err != nil {
		return SelectionExportProposal{}, err
	}
	receiptPayload, err := encodeProposalDocument(exportReceiptFromIndex(receipt))
	if err != nil {
		return SelectionExportProposal{}, err
	}
	digest := sha256.Sum256(receiptPayload)
	records.RecordDigests[receiptPath] = hex.EncodeToString(digest[:])
	records.Selections = append(records.Selections, receipt)
	nextIndexes, err := index.Build(records)
	if err != nil {
		return SelectionExportProposal{}, fmt.Errorf("build proposed selection indexes: %w", err)
	}
	files, err := nextIndexes.Files()
	if err != nil {
		return SelectionExportProposal{}, fmt.Errorf("serialize proposed selection indexes: %w", err)
	}
	payloads := map[string][]byte{receiptPath: receiptPayload}
	changes := []ProposalChange{{Path: receiptPath, Operation: "add_immutable", Digest: digestBytes(receiptPayload), RecordID: receipt.ID}}
	for canonicalPath, content := range files {
		baseContent, readErr := readCanonicalDataFile(dataRoot, canonicalPath)
		if readErr != nil {
			return SelectionExportProposal{}, readErr
		}
		if bytes.Equal(baseContent, content) {
			continue
		}
		var header struct {
			RecordType string `json:"record_type"`
			IndexID    string `json:"index_id"`
			DatasetID  string `json:"dataset_id"`
		}
		if err = json.Unmarshal(content, &header); err != nil {
			return SelectionExportProposal{}, fmt.Errorf("decode rebuilt index %s: %w", canonicalPath, err)
		}
		recordID := header.IndexID
		if header.RecordType == "dataset_manifest" {
			recordID = header.DatasetID
		}
		if recordID == "" {
			return SelectionExportProposal{}, fmt.Errorf("rebuilt index %s has no content identity", canonicalPath)
		}
		payloads[canonicalPath] = content
		changes = append(changes, ProposalChange{Path: canonicalPath, Operation: "set_pointer", Digest: digestBytes(content), RecordID: recordID})
	}
	if len(changes) < 4 { // Receipt, module index, track index, and dataset membership.
		return SelectionExportProposal{}, fmt.Errorf("selection export did not rebuild all required canonical indexes")
	}
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		paths = append(paths, change.Path)
	}
	proposal, err := IdentifyProposal(Proposal{
		Kind:             ProposalSelectionExport,
		Subject:          export.ModuleID + "/" + export.Receipt.AVMReleaseVersion + "/" + export.ContextID,
		SourceGeneration: export.Receipt.DesiredGeneration,
		ExpectedBase:     expectedBase, ExpectedPointer: expectedPointer,
		Producer: ProposalProducer{ID: expectedProposalProducer(ProposalSelectionExport), Version: "1.0.0", ContractVersion: 1},
		Paths:    paths, Changes: changes,
	})
	if err != nil {
		return SelectionExportProposal{}, err
	}
	return SelectionExportProposal{Proposal: proposal, Payloads: payloads}, nil
}

func selectionRecordForExport(receipt state.SelectionReceipt) index.SelectionReceipt {
	return index.SelectionReceipt{
		ID: receipt.ID, ModuleID: receipt.ModuleID, AVMReleaseVersion: receipt.AVMReleaseVersion,
		ContextID: receipt.ContextID, Sequence: receipt.Sequence, DesiredGeneration: receipt.DesiredGeneration,
		SourceSHA: receipt.SourceSHA, FactsID: receipt.FactsID, InputFingerprint: receipt.InputFingerprint,
		AnalysisID: receipt.AnalysisID, EvidenceCommit: receipt.EvidenceCommit, SelectedAt: receipt.SelectedAt,
	}
}

func encodeProposalDocument(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func readCanonicalDataFile(dataRoot, canonicalPath string) ([]byte, error) {
	if err := identity.ValidateRelativePath(canonicalPath, false); err != nil || !strings.HasPrefix(canonicalPath, "data/") {
		return nil, fmt.Errorf("generated data path is invalid")
	}
	path := filepath.Join(dataRoot, filepath.FromSlash(strings.TrimPrefix(canonicalPath, "data/")))
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read accepted data file %s: %w", canonicalPath, err)
	}
	return data, nil
}
