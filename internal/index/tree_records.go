package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

const (
	defaultIndexProducerID      = "avm-intelligence-factory"
	defaultIndexProducerVersion = "2.0.0"
)

type dataPresentationSnapshot struct {
	ID         string    `json:"presentation_id"`
	Generation uint64    `json:"generation"`
	AsOf       time.Time `json:"as_of"`
	Modules    []struct {
		SelectedReceiptIDs []string `json:"selected_receipt_ids"`
	} `json:"modules"`
}

// ReadRecords reconstructs typed index inputs and exact source digests from an
// accepted data-tree root. Generated indexes and the dataset manifest are
// omitted so Build recalculates them from source records.
func ReadRecords(dataRoot string, createdAt time.Time) (Records, error) {
	if err := validateDataTreeRoot(dataRoot); err != nil {
		return Records{}, err
	}
	records := Records{
		Generator:     Producer{ID: defaultIndexProducerID, Version: defaultIndexProducerVersion, ContractVersion: 1},
		RecordDigests: map[string]string{},
	}
	if err := loadCatalogIndex(dataRoot, &records); err != nil {
		return Records{}, err
	}
	if err := loadDatasetMetadata(dataRoot, &records); err != nil {
		return Records{}, err
	}
	if !createdAt.IsZero() {
		records.CreatedAt = createdAt.UTC()
	}
	if records.CreatedAt.IsZero() {
		records.CreatedAt = time.Now().UTC()
	}
	if err := readTypedDataTree(dataRoot, &records); err != nil {
		return Records{}, err
	}
	return records, nil
}

func validateDataTreeRoot(dataRoot string) error {
	rootInfo, err := os.Lstat(dataRoot)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("index source must be a real data directory")
	}
	return nil
}

func readTypedDataTree(dataRoot string, records *Records) error {
	err := filepath.WalkDir(dataRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == dataRoot || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("index source contains a non-regular or linked record: %s", path)
		}
		relative, relErr := filepath.Rel(dataRoot, path)
		if relErr != nil {
			return relErr
		}
		recordPath := filepath.ToSlash(filepath.Join("data", relative))
		if err := identity.ValidateRelativePath(recordPath, false); err != nil {
			return err
		}
		return readTypedDataFile(records, path, relative, recordPath)
	})
	if err != nil {
		return fmt.Errorf("read accepted typed records: %w", err)
	}
	return nil
}

func readTypedDataFile(records *Records, path, relative, recordPath string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !isGeneratedIndexPath(relative) {
		digest := sha256.Sum256(data)
		records.RecordDigests[recordPath] = hex.EncodeToString(digest[:])
	}
	if filepath.Ext(path) != ".json" || isGeneratedIndexPath(relative) {
		return nil
	}
	return addTypedRecord(records, data)
}

func loadCatalogIndex(dataRoot string, records *Records) error {
	data, err := os.ReadFile(filepath.Join(dataRoot, "catalog.json"))
	if err != nil {
		return fmt.Errorf("read accepted catalog index: %w", err)
	}
	var catalog CatalogIndex
	if err = json.Unmarshal(data, &catalog); err != nil {
		return fmt.Errorf("decode accepted catalog index: %w", err)
	}
	if catalog.SchemaVersion != 1 || catalog.RecordType != "catalog_index" || catalog.CatalogGeneration == 0 {
		return fmt.Errorf("accepted catalog index metadata is invalid")
	}
	records.CatalogGeneration = catalog.CatalogGeneration
	records.Modules = make([]ModuleRecord, 0, len(catalog.Modules))
	for _, module := range catalog.Modules {
		records.Modules = append(records.Modules, ModuleRecord{
			ModuleID: module.ModuleID, Repository: module.Repository, Lifecycle: module.Lifecycle,
		})
	}
	return nil
}

func loadDatasetMetadata(dataRoot string, records *Records) error {
	data, err := os.ReadFile(filepath.Join(dataRoot, "dataset.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var manifest DatasetManifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("decode accepted dataset manifest: %w", err)
	}
	if manifest.SchemaVersion != 2 || manifest.RecordType != "dataset_manifest" || manifest.Generator.ID == "" || manifest.Generator.Version == "" || manifest.Generator.ContractVersion < 1 || manifest.CreatedAt.IsZero() {
		return fmt.Errorf("accepted dataset metadata is invalid")
	}
	records.Generator = manifest.Generator
	records.CreatedAt = manifest.CreatedAt.UTC()
	return nil
}

func addTypedRecord(records *Records, data []byte) error {
	var header struct {
		RecordType string `json:"record_type"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return fmt.Errorf("decode typed record header: %w", err)
	}
	switch header.RecordType {
	case "source_revision":
		var record SourceRevisionRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		records.SourceRevisions = append(records.SourceRevisions, record)
	case "module_version_observation":
		var record VersionObservationRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		records.VersionObservations = append(records.VersionObservations, record)
	case "module_versions_index":
		var record ModuleVersionsIndexRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		records.VersionIndexes = append(records.VersionIndexes, record)
	case "facts":
		var record struct {
			ID            string `json:"facts_id"`
			ModuleID      string `json:"module_id"`
			SourceSHA     string `json:"source_sha"`
			ContentDigest string `json:"content_digest"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		records.Facts = append(records.Facts, FactRecord{ID: record.ID, ModuleID: record.ModuleID, SourceSHA: record.SourceSHA, ContentDigest: record.ContentDigest})
	case "provenance":
		var record ProvenanceRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		records.Provenance = append(records.Provenance, record)
	case "dimension_definition":
		var record assessment.DimensionDefinition
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		records.Definitions = append(records.Definitions, record)
	case "dimension_result":
		var record assessment.DimensionResult
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		records.DimensionResults = append(records.DimensionResults, record)
	case "analysis":
		var record AnalysisRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		records.Analyses = append(records.Analyses, record)
	case "provider_snapshot":
		var record struct {
			ID      string `json:"snapshot_id"`
			Address string `json:"provider_address"`
			Version string `json:"provider_version"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		records.ProviderSnapshots = append(records.ProviderSnapshots, ProviderSnapshotRecord{ID: record.ID, Address: record.Address, Version: record.Version})
	case "selection_receipt":
		var record SelectionReceipt
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		records.Selections = append(records.Selections, record)
	case "presentation_snapshot":
		var record dataPresentationSnapshot
		if err := json.Unmarshal(data, &record); err != nil {
			return err
		}
		receiptIDs := make([]string, 0)
		for _, module := range record.Modules {
			receiptIDs = append(receiptIDs, module.SelectedReceiptIDs...)
		}
		receiptIDs, err := identity.SortedSet(receiptIDs)
		if err != nil {
			return fmt.Errorf("presentation selection receipts: %w", err)
		}
		records.Presentations = append(records.Presentations, PresentationSnapshotRecord{
			ID: record.ID, Generation: record.Generation, AsOf: record.AsOf, SelectionReceiptIDs: receiptIDs,
		})
	}
	return nil
}

func isGeneratedIndexPath(relative string) bool {
	if relative == "catalog.json" || relative == "dataset.json" || relative == "assessment/dependencies.json" || relative == "presentation/index.json" {
		return true
	}
	if filepath.Base(relative) != "index.json" {
		return false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) == 1 {
		return false
	}
	switch parts[0] {
	case "tracks":
		return true
	case "providers":
		return len(parts) == 5 && parts[len(parts)-1] == "index.json"
	case "modules":
		return len(parts) == 3 || len(parts) == 4
	default:
		return false
	}
}
