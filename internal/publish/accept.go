package publish

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
)

const candidateIndexName = "candidate.json"

// AcceptCandidate remains as the compatibility entry point for older callers.
func AcceptCandidate(candidateRoot, discoveryPath, requestsPath, dataRoot string) error {
	return ApplyModuleDelta(candidateRoot, discoveryPath, requestsPath, dataRoot, dataRoot)
}

// ApplyModuleDelta validates and atomically merges candidate changes into the
// accepted data tree. The caller supplies the single-writer boundary; this
// function does not push Git or deploy Pages.
func ApplyModuleDelta(candidateRoot, discoveryPath, requestsPath, baseDataRoot, outputDataRoot string) error {
	candidate, err := readCandidate(filepath.Join(candidateRoot, candidateIndexName))
	if err != nil {
		return err
	}
	discovery, err := catalog.LoadAccepted(discoveryPath)
	if err != nil {
		return fmt.Errorf("load accepted discovery: %w", err)
	}
	if discovery == nil {
		return fmt.Errorf("accepted discovery state is missing")
	}
	requests, err := readRequests(requestsPath)
	if err != nil {
		return err
	}
	if err = validateCandidateReports(candidate, candidateRoot, requests); err != nil {
		return err
	}
	return applyModuleDelta(candidateRoot, candidate, discovery, requests, baseDataRoot, outputDataRoot)
}

func readCandidate(path string) (Candidate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Candidate{}, fmt.Errorf("read candidate index: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var candidate Candidate
	if err = decoder.Decode(&candidate); err != nil {
		return Candidate{}, fmt.Errorf("parse candidate index: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return Candidate{}, fmt.Errorf("candidate index must contain one JSON document")
	}
	if err = validateCandidateIndex(candidate); err != nil {
		return Candidate{}, err
	}
	return candidate, nil
}

func validateCandidateIndex(candidate Candidate) error {
	if candidate.SchemaVersion != CandidateSchemaVersion || candidate.CatalogGeneration == 0 || candidate.BuiltAt.IsZero() || len(candidate.Modules) == 0 {
		return fmt.Errorf("candidate index metadata is invalid")
	}
	previous := ""
	for _, module := range candidate.Modules {
		if module.ModuleID == "" || module.SourceSHA == "" || module.Fingerprint == "" || module.ReportPath == "" || (previous != "" && module.ModuleID <= previous) {
			return fmt.Errorf("candidate module index is invalid for %s", module.ModuleID)
		}
		if filepath.IsAbs(module.ReportPath) || strings.Contains(filepath.ToSlash(module.ReportPath), "..") || filepath.ToSlash(module.ReportPath) != filepath.ToSlash(filepath.Clean(module.ReportPath)) {
			return fmt.Errorf("candidate report path is unsafe for %s", module.ModuleID)
		}
		previous = module.ModuleID
	}
	return nil
}

func readRequests(root string) (map[string]queue.Request, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read accepted requests: %w", err)
	}
	requests := make(map[string]queue.Request, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		moduleID := strings.TrimSuffix(entry.Name(), ".json")
		request, err := queue.Load(root, moduleID)
		if err != nil {
			return nil, fmt.Errorf("load accepted request %s: %w", moduleID, err)
		}
		requests[moduleID] = request
	}
	return requests, nil
}

func validateCandidateReports(candidate Candidate, root string, requests map[string]queue.Request) error {
	for _, module := range candidate.Modules {
		request, exists := requests[module.ModuleID]
		if !exists || request.Status != queue.StatusAccepted || request.Fingerprint != module.Fingerprint || request.SourceSHA != module.SourceSHA {
			return fmt.Errorf("accepted request does not match candidate module %s", module.ModuleID)
		}
		path := filepath.Join(root, filepath.FromSlash(module.ReportPath))
		report, err := readReport(path)
		if err != nil {
			return err
		}
		if report.Provenance.ModuleID != module.ModuleID || report.Provenance.SourceSHA != module.SourceSHA {
			return fmt.Errorf("candidate report does not match module %s", module.ModuleID)
		}
	}
	return nil
}

func applyModuleDelta(candidateRoot string, delta Candidate, discovery *catalog.AcceptedState, requests map[string]queue.Request, baseDataRoot, outputDataRoot string) error {
	merged, err := mergeCandidateIndex(baseDataRoot, delta)
	if err != nil {
		return err
	}
	parent := filepath.Dir(outputDataRoot)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".accepted-data-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err = copyAcceptedTree(baseDataRoot, staging); err != nil {
		return err
	}
	if err = copyCandidateReports(candidateRoot, delta, staging); err != nil {
		return err
	}
	if err = catalog.SaveAccepted(filepath.Join(staging, "discovery.json"), *discovery); err != nil {
		return err
	}
	requestRoot := filepath.Join(staging, "requests")
	for _, moduleID := range sortedRequestIDs(requests) {
		requestPath := filepath.Join(requestRoot, moduleID+".json")
		if err = os.Remove(requestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = queue.Save(requestRoot, requests[moduleID]); err != nil {
			return err
		}
	}
	if err = writeCandidateIndex(staging, merged); err != nil {
		return err
	}
	return swapDirectory(staging, outputDataRoot)
}

func mergeCandidateIndex(dataRoot string, delta Candidate) (Candidate, error) {
	base, err := readCandidate(filepath.Join(dataRoot, candidateIndexName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Candidate{}, fmt.Errorf("read accepted candidate index: %w", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(filepath.Join(dataRoot, "modules"))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return Candidate{}, fmt.Errorf("read accepted module tree: %w", readErr)
		}
		if len(entries) != 0 {
			return Candidate{}, fmt.Errorf("accepted candidate index is missing from a non-empty module tree")
		}
		base = Candidate{SchemaVersion: CandidateSchemaVersion}
	}
	modules := make(map[string]CandidateModule, len(base.Modules)+len(delta.Modules))
	for _, module := range base.Modules {
		modules[module.ModuleID] = module
	}
	for _, module := range delta.Modules {
		modules[module.ModuleID] = module
	}
	merged := delta
	merged.Modules = make([]CandidateModule, 0, len(modules))
	for _, module := range modules {
		merged.Modules = append(merged.Modules, module)
	}
	sort.Slice(merged.Modules, func(left, right int) bool {
		return merged.Modules[left].ModuleID < merged.Modules[right].ModuleID
	})
	return merged, nil
}

func copyAcceptedTree(sourceRoot, destinationRoot string) error {
	rootInfo, err := os.Stat(sourceRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("accepted data root is not a directory")
	}
	return filepath.WalkDir(sourceRoot, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if sourcePath == sourceRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("accepted data symlink is not allowed: %s", sourcePath)
		}
		relative, err := filepath.Rel(sourceRoot, sourcePath)
		if err != nil {
			return err
		}
		destinationPath := filepath.Join(destinationRoot, relative)
		if entry.IsDir() {
			return os.MkdirAll(destinationPath, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("accepted data entry is not a regular file: %s", sourcePath)
		}
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(destinationPath, data, info.Mode().Perm())
	})
}

func copyCandidateReports(sourceRoot string, candidate Candidate, destinationRoot string) error {
	for _, module := range candidate.Modules {
		relative := filepath.FromSlash(module.ReportPath)
		sourcePath := filepath.Join(sourceRoot, relative)
		info, err := os.Lstat(sourcePath)
		if err != nil {
			return fmt.Errorf("stat delta report for %s: %w", module.ModuleID, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("delta report for %s is not a regular file", module.ModuleID)
		}
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return fmt.Errorf("read delta report for %s: %w", module.ModuleID, err)
		}
		destinationPath := filepath.Join(destinationRoot, relative)
		if err = os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			return err
		}
		if err = os.WriteFile(destinationPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeCandidateIndex(root string, candidate Candidate) error {
	index, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, candidateIndexName), append(index, '\n'), 0o644)
}

func sortedRequestIDs(requests map[string]queue.Request) []string {
	ids := make([]string, 0, len(requests))
	for id := range requests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func swapDirectory(staging, destination string) error {
	backup := destination + ".previous"
	if err := os.RemoveAll(backup); err != nil {
		return err
	}
	if _, err := os.Stat(destination); err == nil {
		if err = os.Rename(destination, backup); err != nil {
			return err
		}
		defer os.RemoveAll(backup)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staging, destination); err != nil {
		if _, statErr := os.Stat(backup); statErr == nil {
			_ = os.Rename(backup, destination)
		}
		return err
	}
	return nil
}
