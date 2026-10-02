package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
)

const legacyMigrationPlanVersion = 1

var migrationRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type legacyMigrationPlan struct {
	SchemaVersion   int                     `json:"schema_version"`
	Mode            string                  `json:"mode"`
	WritesPerformed bool                    `json:"writes_performed"`
	Modules         []legacyMigrationModule `json:"modules"`
}

type legacyMigrationModule struct {
	ModuleID          string                  `json:"module_id"`
	SourceSHA         string                  `json:"source_sha"`
	LegacyFingerprint string                  `json:"legacy_fingerprint"`
	LegacyReport      legacyReportMigration   `json:"legacy_report"`
	SourceRevision    migrationSourceRevision `json:"source_revision"`
	ReleaseVersion    migrationAvailability   `json:"release_version"`
	Facts             migrationAvailability   `json:"facts"`
	Analysis          migrationAnalysisStatus `json:"analysis"`
}

type legacyReportMigration struct {
	SourcePath            string             `json:"source_path"`
	ArchivePath           string             `json:"archive_path"`
	ArchiveID             string             `json:"archive_id"`
	ArchiveRecordType     string             `json:"archive_record_type"`
	SHA256                string             `json:"sha256"`
	ByteLength            int                `json:"byte_length"`
	Provenance            iaclens.Provenance `json:"provenance"`
	UnavailableProvenance []string           `json:"unavailable_provenance_fields"`
	SourceDigest          string             `json:"source_digest"`
	RulesetName           string             `json:"ruleset_name"`
	RulesetDigest         string             `json:"ruleset_digest"`
}

type migrationAvailability struct {
	Status            string   `json:"status"`
	Reason            string   `json:"reason"`
	UnavailableFields []string `json:"unavailable_fields,omitempty"`
}

type migrationSourceRevision struct {
	Status              string   `json:"status"`
	Path                string   `json:"path"`
	Repository          string   `json:"repository,omitempty"`
	RepositorySource    string   `json:"repository_source,omitempty"`
	DiscoveryGeneration uint64   `json:"discovery_generation,omitempty"`
	DiscoveryAcceptedAt string   `json:"discovery_accepted_at,omitempty"`
	Reason              string   `json:"reason"`
	UnavailableFields   []string `json:"unavailable_fields,omitempty"`
}

type migrationAnalysisStatus struct {
	Status            string   `json:"status"`
	Reason            string   `json:"reason"`
	UnavailableInputs []string `json:"unavailable_inputs"`
}

func createLegacyMigrationPlan(dataRoot, outputPath string) error {
	if dataRoot == "" || outputPath == "" {
		return fmt.Errorf("legacy migration requires a data root and output path")
	}
	if err := ensureMigrationOutputOutsideData(dataRoot, outputPath); err != nil {
		return err
	}
	candidate, err := publish.LoadCandidate(dataRoot)
	if err != nil {
		return fmt.Errorf("load legacy candidate: %w", err)
	}
	discovery, err := catalog.LoadAccepted(filepath.Join(dataRoot, "discovery.json"))
	if err != nil {
		return fmt.Errorf("load accepted discovery identity: %w", err)
	}
	repositories := migrationRepositoriesFromAcceptedState(discovery, candidate.CatalogGeneration, candidate.BuiltAt)
	plan := legacyMigrationPlan{
		SchemaVersion:   legacyMigrationPlanVersion,
		Mode:            "dry_run",
		WritesPerformed: false,
		Modules:         make([]legacyMigrationModule, 0, len(candidate.Modules)),
	}
	for _, module := range candidate.Modules {
		migration, err := planLegacyModule(dataRoot, module, repositories[module.ModuleID])
		if err != nil {
			return fmt.Errorf("plan legacy migration for %s: %w", module.ModuleID, err)
		}
		plan.Modules = append(plan.Modules, migration)
	}
	if err := writeJSON(outputPath, plan); err != nil {
		return fmt.Errorf("write legacy migration plan: %w", err)
	}
	fmt.Printf("planned legacy migration: modules=%d writes=false output=%s\n", len(plan.Modules), outputPath)
	return nil
}

func planLegacyModule(dataRoot string, module publish.CandidateModule, repository migrationRepositoryEvidence) (legacyMigrationModule, error) {
	sourceRevisionPath, err := identity.ModuleSourcePath(module.ModuleID, module.SourceSHA)
	if err != nil {
		return legacyMigrationModule{}, err
	}
	expectedReportPath := filepath.ToSlash(filepath.Join("modules", module.ModuleID+".json"))
	if module.ReportPath != expectedReportPath {
		return legacyMigrationModule{}, fmt.Errorf("candidate report path %q does not match expected path %q", module.ReportPath, expectedReportPath)
	}
	reportBytes, err := readLegacyReportBytes(dataRoot, expectedReportPath)
	if err != nil {
		return legacyMigrationModule{}, err
	}
	report, err := decodeLegacyReport(reportBytes)
	if err != nil {
		return legacyMigrationModule{}, err
	}
	if report.Provenance.ModuleID != module.ModuleID || report.Provenance.SourceSHA != module.SourceSHA {
		return legacyMigrationModule{}, fmt.Errorf("legacy report provenance does not match candidate module and source")
	}
	reportDigest := sha256.Sum256(reportBytes)
	reportDigestHex := hex.EncodeToString(reportDigest[:])
	unavailableProvenance := make([]string, 0, 2)
	if report.Provenance.CLIVersion == "" {
		unavailableProvenance = append(unavailableProvenance, "cli_version")
	}
	if report.Provenance.CLICommit == "" {
		unavailableProvenance = append(unavailableProvenance, "cli_commit")
	}
	archiveID := "legacy-analysis-" + reportDigestHex
	archivePath, err := identity.ModuleRecordPath(module.ModuleID, module.SourceSHA, "legacy-analysis", archiveID)
	if err != nil {
		return legacyMigrationModule{}, err
	}
	sourceRevision := migrationSourceRevision{
		Status:            "blocked",
		Path:              sourceRevisionPath,
		Reason:            "no matching accepted repository identity predating the candidate build is available",
		UnavailableFields: []string{"repository"},
	}
	if repository.Repository != "" {
		sourceRevision = migrationSourceRevision{
			Status:              "proposed",
			Path:                sourceRevisionPath,
			Repository:          repository.Repository,
			RepositorySource:    "data/discovery.json",
			DiscoveryGeneration: repository.Generation,
			DiscoveryAcceptedAt: repository.AcceptedAt,
			Reason:              "repository identity is retained from the accepted discovery state referenced by the candidate",
		}
	}
	return legacyMigrationModule{
		ModuleID:          module.ModuleID,
		SourceSHA:         module.SourceSHA,
		LegacyFingerprint: module.Fingerprint,
		LegacyReport: legacyReportMigration{
			SourcePath:            expectedReportPath,
			ArchivePath:           archivePath,
			ArchiveID:             archiveID,
			ArchiveRecordType:     "legacy_iaclens_report",
			SHA256:                reportDigestHex,
			ByteLength:            len(reportBytes),
			Provenance:            report.Provenance,
			UnavailableProvenance: unavailableProvenance,
			SourceDigest:          report.SourceDigest,
			RulesetName:           report.RulesetName,
			RulesetDigest:         report.RulesetDigest,
		},
		SourceRevision: sourceRevision,
		ReleaseVersion: migrationAvailability{
			Status: "unavailable",
			Reason: "the legacy candidate contains no reviewed release or version observation",
		},
		Facts: migrationAvailability{
			Status: "unavailable",
			Reason: "legacy normalized reports do not contain a native facts-v3 record",
		},
		Analysis: migrationAnalysisStatus{
			Status:            "not_created",
			Reason:            "native analysis is not created; original legacy evidence is separately mapped to a legacy-analysis archive entry",
			UnavailableInputs: []string{"facts_id", "input_fingerprint", "context_id", "dimension_result_ids"},
		},
	}, nil
}

type migrationRepositoryEvidence struct {
	Repository string
	Generation uint64
	AcceptedAt string
}

func migrationRepositoriesFromAcceptedState(discovery *catalog.AcceptedState, candidateGeneration uint64, candidateBuiltAt time.Time) map[string]migrationRepositoryEvidence {
	repositories := map[string]migrationRepositoryEvidence{}
	if discovery == nil || discovery.Generation != candidateGeneration || discovery.AcceptedAt.After(candidateBuiltAt) {
		return repositories
	}
	for _, record := range discovery.Modules {
		parsed, err := url.Parse(record.Module.RepositoryURL)
		if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			continue
		}
		repository := strings.Trim(parsed.Path, "/")
		if !migrationRepositoryPattern.MatchString(repository) {
			continue
		}
		repositories[record.Module.ID] = migrationRepositoryEvidence{
			Repository: repository,
			Generation: discovery.Generation,
			AcceptedAt: discovery.AcceptedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	return repositories
}

func readLegacyReportBytes(dataRoot, reportPath string) ([]byte, error) {
	rootInfo, err := os.Lstat(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("inspect data root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("data root must be a real directory")
	}
	modulesPath := filepath.Join(dataRoot, "modules")
	modulesInfo, err := os.Lstat(modulesPath)
	if err != nil {
		return nil, fmt.Errorf("inspect legacy modules directory: %w", err)
	}
	if !modulesInfo.IsDir() || modulesInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("legacy modules path must be a real directory")
	}
	fullPath := filepath.Join(dataRoot, filepath.FromSlash(reportPath))
	reportInfo, err := os.Lstat(fullPath)
	if err != nil {
		return nil, fmt.Errorf("inspect legacy report: %w", err)
	}
	if !reportInfo.Mode().IsRegular() || reportInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("legacy report must be a regular file")
	}
	return os.ReadFile(fullPath)
}

func decodeLegacyReport(data []byte) (iaclens.Report, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var report iaclens.Report
	if err := decoder.Decode(&report); err != nil {
		return iaclens.Report{}, fmt.Errorf("decode legacy normalized report: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return iaclens.Report{}, fmt.Errorf("legacy normalized report must contain one JSON document")
	}
	if err := report.Validate(); err != nil {
		return iaclens.Report{}, fmt.Errorf("validate legacy normalized report: %w", err)
	}
	return report, nil
}

func ensureMigrationOutputOutsideData(dataRoot, outputPath string) error {
	rootAbs, err := filepath.Abs(dataRoot)
	if err != nil {
		return fmt.Errorf("resolve data root: %w", err)
	}
	outputAbs, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("resolve migration output: %w", err)
	}
	relative, err := filepath.Rel(rootAbs, outputAbs)
	if err != nil {
		return fmt.Errorf("compare migration output and data root: %w", err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("migration output must be outside the accepted data root")
	}
	return nil
}
