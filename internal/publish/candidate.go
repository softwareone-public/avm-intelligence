package publish

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
)

const CandidateSchemaVersion = 1

// Candidate is the validated data index assembled from a complete worker plan.
type Candidate struct {
	SchemaVersion     int               `json:"schema_version"`
	CatalogGeneration uint64            `json:"catalog_generation"`
	BuiltAt           time.Time         `json:"built_at"`
	Modules           []CandidateModule `json:"modules"`
}

// CandidateModule identifies one normalized report written to the candidate tree.
type CandidateModule struct {
	ModuleID    string `json:"module_id"`
	SourceSHA   string `json:"source_sha"`
	Fingerprint string `json:"fingerprint"`
	ReportPath  string `json:"report_path"`
}

type artifactRecord struct {
	request *queue.Request
	report  *iaclens.Report
}

// Assemble validates every planned module artifact and writes a complete data
// candidate. Missing, duplicate, failed, stale, or mismatched artifacts abort
// assembly before any candidate output is accepted.
func Assemble(plan queue.BatchPlan, artifactsRoot, outputRoot string, builtAt time.Time) (Candidate, error) {
	if builtAt.IsZero() {
		return Candidate{}, fmt.Errorf("candidate build time is required")
	}
	expected := expectedModules(plan)
	if len(expected) == 0 {
		return Candidate{}, fmt.Errorf("plan contains no dispatchable modules")
	}
	artifacts, err := collectArtifacts(artifactsRoot)
	if err != nil {
		return Candidate{}, err
	}
	candidate := Candidate{SchemaVersion: CandidateSchemaVersion, CatalogGeneration: plan.CatalogGeneration, BuiltAt: builtAt.UTC()}
	for _, moduleID := range expected {
		record, exists := artifacts[moduleID]
		if !exists || record.request == nil || record.report == nil {
			return Candidate{}, fmt.Errorf("complete accepted artifact is missing for %s", moduleID)
		}
		item, err := planItem(plan, moduleID)
		if err != nil {
			return Candidate{}, err
		}
		if err := validateArtifact(moduleID, item, *record.request, *record.report); err != nil {
			return Candidate{}, err
		}
		reportPath := filepath.ToSlash(filepath.Join("modules", moduleID+".json"))
		candidate.Modules = append(candidate.Modules, CandidateModule{ModuleID: moduleID, SourceSHA: record.request.SourceSHA, Fingerprint: record.request.Fingerprint, ReportPath: reportPath})
	}
	sort.Slice(candidate.Modules, func(i, j int) bool { return candidate.Modules[i].ModuleID < candidate.Modules[j].ModuleID })
	if err := writeCandidate(outputRoot, candidate, artifacts, expected); err != nil {
		return Candidate{}, err
	}
	return candidate, nil
}

func expectedModules(plan queue.BatchPlan) []string {
	set := make(map[string]struct{})
	for _, batch := range plan.Batches {
		for _, moduleID := range batch.ModuleIDs {
			set[moduleID] = struct{}{}
		}
	}
	modules := make([]string, 0, len(set))
	for moduleID := range set {
		modules = append(modules, moduleID)
	}
	sort.Strings(modules)
	return modules
}

func planItem(plan queue.BatchPlan, moduleID string) (queue.PlanItem, error) {
	for _, item := range plan.Items {
		if item.ModuleID == moduleID {
			return item, nil
		}
	}
	return queue.PlanItem{}, fmt.Errorf("plan item is missing for %s", moduleID)
}

func collectArtifacts(root string) (map[string]artifactRecord, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("worker artifacts root is missing: %w", err)
	}
	artifacts := map[string]artifactRecord{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return collectArtifactEntry(root, path, entry, artifacts)
	})
	if err != nil {
		return nil, fmt.Errorf("collect worker artifacts: %w", err)
	}
	byModule := map[string]artifactRecord{}
	for _, record := range artifacts {
		if record.request == nil {
			continue
		}
		if _, exists := byModule[record.request.ModuleID]; exists {
			return nil, fmt.Errorf("duplicate worker artifact for %s", record.request.ModuleID)
		}
		byModule[record.request.ModuleID] = record
	}
	return byModule, nil
}

func collectArtifactEntry(root, path string, entry fs.DirEntry, artifacts map[string]artifactRecord) error {
	if path == root || entry.IsDir() {
		return nil
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("worker artifact symlink is not allowed: %s", path)
	}
	if !entry.Type().IsRegular() {
		return nil
	}
	base := filepath.Base(path)
	if base == "report.raw.json" {
		return nil
	}
	if base != "analysis.json" && !strings.HasSuffix(entry.Name(), ".json") {
		return nil
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	artifactKey := strings.Split(filepath.ToSlash(relative), "/")[0]
	record := artifacts[artifactKey]
	if base == "analysis.json" {
		value, err := readReport(path)
		if err != nil {
			return err
		}
		if record.report != nil {
			return fmt.Errorf("duplicate analysis artifact in %s", artifactKey)
		}
		record.report = &value
	} else {
		value, err := readRequest(path)
		if err != nil {
			return err
		}
		if record.request != nil {
			return fmt.Errorf("duplicate request artifact in %s", artifactKey)
		}
		record.request = &value
	}
	artifacts[artifactKey] = record
	return nil
}

func readRequest(path string) (queue.Request, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return queue.Request{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request queue.Request
	if err = decoder.Decode(&request); err != nil {
		return queue.Request{}, fmt.Errorf("parse worker request %s: %w", path, err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return queue.Request{}, fmt.Errorf("worker request %s must contain one JSON document", path)
	}
	if err = request.Validate(); err != nil {
		return queue.Request{}, fmt.Errorf("validate worker request %s: %w", path, err)
	}
	return request, nil
}

func readReport(path string) (iaclens.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return iaclens.Report{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var report iaclens.Report
	if err = decoder.Decode(&report); err != nil {
		return iaclens.Report{}, fmt.Errorf("parse worker report %s: %w", path, err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return iaclens.Report{}, fmt.Errorf("worker report %s must contain one JSON document", path)
	}
	if err = report.Validate(); err != nil {
		return iaclens.Report{}, fmt.Errorf("validate worker report %s: %w", path, err)
	}
	return report, nil
}

func validateArtifact(moduleID string, item queue.PlanItem, request queue.Request, report iaclens.Report) error {
	if request.ModuleID != moduleID || request.Status != queue.StatusAccepted || request.OwnerRunID != "" || request.Pending != nil || request.LastAcceptedFingerprint != request.Fingerprint {
		return fmt.Errorf("worker request for %s is not an accepted terminal request", moduleID)
	}
	if item.Fingerprint != request.Fingerprint || item.SourceSHA != request.SourceSHA {
		return fmt.Errorf("worker request for %s does not match the plan", moduleID)
	}
	if report.Provenance.ModuleID != moduleID || report.Provenance.SourceSHA != request.SourceSHA {
		return fmt.Errorf("worker report for %s does not match request provenance", moduleID)
	}
	return nil
}

func writeCandidate(root string, candidate Candidate, artifacts map[string]artifactRecord, expected []string) error {
	if err := os.MkdirAll(filepath.Join(root, "modules"), 0o755); err != nil {
		return err
	}
	for _, moduleID := range expected {
		report := artifacts[findArtifactPath(artifacts, moduleID)].report
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(root, "modules", moduleID+".json"), append(data, '\n'), 0o644); err != nil {
			return err
		}
	}
	index, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "candidate.json"), append(index, '\n'), 0o644)
}

func findArtifactPath(artifacts map[string]artifactRecord, moduleID string) string {
	for path, record := range artifacts {
		if record.request != nil && record.request.ModuleID == moduleID {
			return path
		}
	}
	return ""
}
