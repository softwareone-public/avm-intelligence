package publish

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
)

const candidateIndexName = "candidate.json"

// AcceptCandidate validates and atomically replaces one local accepted data
// bundle. The caller supplies the single-writer boundary; this function does
// not push Git or deploy Pages.
func AcceptCandidate(candidateRoot, discoveryPath, requestsPath, dataRoot string) error {
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
	return replaceAcceptedBundle(candidateRoot, discovery, requests, dataRoot)
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

func replaceAcceptedBundle(candidateRoot string, discovery *catalog.AcceptedState, requests map[string]queue.Request, dataRoot string) error {
	parent := filepath.Dir(dataRoot)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".accepted-data-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err = copyCandidateFiles(candidateRoot, staging); err != nil {
		return err
	}
	if err = catalog.SaveAccepted(filepath.Join(staging, "discovery.json"), *discovery); err != nil {
		return err
	}
	requestRoot := filepath.Join(staging, "requests")
	for _, moduleID := range sortedRequestIDs(requests) {
		if err = queue.Save(requestRoot, requests[moduleID]); err != nil {
			return err
		}
	}
	return swapDirectory(staging, dataRoot)
}

func copyCandidateFiles(sourceRoot, destinationRoot string) error {
	if err := os.MkdirAll(filepath.Join(destinationRoot, "modules"), 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(sourceRoot, "modules"))
	if err != nil {
		return fmt.Errorf("read candidate reports: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			return fmt.Errorf("unsafe candidate report entry %s", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(sourceRoot, "modules", entry.Name()))
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(destinationRoot, "modules", entry.Name()), data, 0o644); err != nil {
			return err
		}
	}
	index, err := os.ReadFile(filepath.Join(sourceRoot, candidateIndexName))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destinationRoot, candidateIndexName), index, 0o644)
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
