package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
)

type offlineDataCommandOptions struct {
	buildIndexes         bool
	indexRecords         string
	dataRoot             string
	indexesOut           string
	applyProposal        bool
	proposalRoot         string
	proposalBase         string
	proposalDataBase     string
	proposalDataOut      string
	proposalChangedPaths string
}

func handleOfflineDataCommand(options offlineDataCommandOptions) (bool, error) {
	if handled, err := handleProposalCommand(options.applyProposal, options.proposalRoot, options.proposalBase, options.proposalDataBase, options.proposalDataOut, options.proposalChangedPaths); handled {
		return handled, err
	}
	if options.buildIndexes {
		return true, createIndexCandidate(options.indexRecords, options.dataRoot, options.indexesOut)
	}
	if options.indexRecords != "" {
		return true, fmt.Errorf("--index-records requires --build-indexes")
	}
	return false, nil
}

func createIndexCandidate(recordsPath, dataRoot, outputRoot string) error {
	if dataRoot == "" || outputRoot == "" {
		return fmt.Errorf("--build-indexes requires --data-root and --indexes-out")
	}
	if err := ensureOutputOutsideData(dataRoot, outputRoot); err != nil {
		return fmt.Errorf("unsafe index output path: %w", err)
	}
	outputAbs, err := filepath.Abs(outputRoot)
	if err != nil {
		return fmt.Errorf("resolve index output: %w", err)
	}
	if _, err = os.Lstat(outputAbs); err == nil {
		return fmt.Errorf("index output already exists: %s", outputAbs)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect index output: %w", err)
	}
	var records index.Records
	if recordsPath == "" {
		records, err = index.ReadRecords(dataRoot, time.Time{})
	} else {
		records, err = readIndexRecords(recordsPath)
	}
	if err != nil {
		return err
	}
	indexes, err := index.Build(records)
	if err != nil {
		return fmt.Errorf("build indexes: %w", err)
	}
	files, err := indexes.Files()
	if err != nil {
		return fmt.Errorf("serialize indexes: %w", err)
	}
	parent := filepath.Dir(outputAbs)
	if err = os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create index output parent: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".index-candidate-")
	if err != nil {
		return fmt.Errorf("create index staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	paths := make([]string, 0, len(files))
	for recordPath := range files {
		paths = append(paths, recordPath)
	}
	sort.Strings(paths)
	for _, recordPath := range paths {
		if err = identity.ValidateRelativePath(recordPath, false); err != nil {
			return fmt.Errorf("invalid generated index path: %w", err)
		}
		if !strings.HasPrefix(recordPath, "data/") {
			return fmt.Errorf("generated index path is outside data/: %s", recordPath)
		}
		destination := filepath.Join(staging, filepath.FromSlash(recordPath))
		if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return fmt.Errorf("create index directory for %s: %w", recordPath, err)
		}
		if err = os.WriteFile(destination, files[recordPath], 0o644); err != nil {
			return fmt.Errorf("write generated index %s: %w", recordPath, err)
		}
	}
	if err = os.Rename(staging, outputAbs); err != nil {
		return fmt.Errorf("publish index candidate directory: %w", err)
	}
	slog.Info("index candidate built", "modules", len(indexes.Modules), "tracks", len(indexes.Tracks), "providers", len(indexes.Providers), "files", len(files), "output", outputAbs)
	return nil
}

func readIndexRecords(recordsPath string) (index.Records, error) {
	data, err := os.ReadFile(recordsPath)
	if err != nil {
		return index.Records{}, fmt.Errorf("read index records: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var records index.Records
	if err = decoder.Decode(&records); err != nil {
		return index.Records{}, fmt.Errorf("decode index records: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return index.Records{}, fmt.Errorf("index records must contain one JSON document")
	}
	return records, nil
}
