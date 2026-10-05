package publish

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/softwareone-public/avm-intelligence/internal/index"
)

func typedDatasetPresent(dataRoot string) (bool, error) {
	info, err := os.Lstat(filepath.Join(dataRoot, "dataset.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("typed dataset manifest must be a regular non-symlink file")
	}
	return true, nil
}

func stageChangedTypedIndexes(proposalRoot, baseDataRoot string, records index.Records, initialChanges []ProposalChange) ([]ProposalChange, error) {
	indexes, err := index.Build(records)
	if err != nil {
		return nil, fmt.Errorf("build generated indexes for proposal: %w", err)
	}
	files, err := indexes.Files()
	if err != nil {
		return nil, fmt.Errorf("serialize generated indexes for proposal: %w", err)
	}
	changesByPath := make(map[string]ProposalChange, len(initialChanges))
	for _, change := range initialChanges {
		changesByPath[change.Path] = change
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	changes := append([]ProposalChange(nil), initialChanges...)
	for _, path := range paths {
		change, changed, stageErr := stageGeneratedIndexPath(proposalRoot, baseDataRoot, path, files[path], changesByPath)
		if stageErr != nil {
			return nil, stageErr
		}
		if changed {
			changes = append(changes, change)
		}
	}
	return changes, nil
}

func stageGeneratedIndexPath(proposalRoot, baseDataRoot, path string, content []byte, declared map[string]ProposalChange) (ProposalChange, bool, error) {
	baseContent, err := readOptionalDataFile(baseDataRoot, path)
	if err != nil {
		return ProposalChange{}, false, err
	}
	if change, exists := declared[path]; exists {
		return change, false, verifyStagedGeneratedIndex(proposalRoot, path, content, change)
	}
	if bytes.Equal(baseContent, content) {
		return ProposalChange{}, false, nil
	}
	recordID, err := generatedCatalogRecordID(content)
	if err != nil {
		return ProposalChange{}, false, err
	}
	if err = writeCatalogProposalPayload(proposalRoot, path, content); err != nil {
		return ProposalChange{}, false, err
	}
	return ProposalChange{Path: path, Operation: "set_pointer", Digest: digestBytes(content), RecordID: recordID}, true, nil
}

func verifyStagedGeneratedIndex(proposalRoot, path string, content []byte, change ProposalChange) error {
	proposalContent, err := readProposalPayload(proposalRoot, path)
	if err != nil {
		return err
	}
	if change.Digest != digestBytes(content) || !bytes.Equal(proposalContent, content) {
		return fmt.Errorf("staged generated index %s differs from its rebuilt contents", path)
	}
	return nil
}
