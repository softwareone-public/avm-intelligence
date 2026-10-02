package site

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"

	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
)

const catalogIndexTemplate = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>AVM Intelligence</title>
<style>body{font:16px system-ui,sans-serif;line-height:1.5;max-width:1100px;margin:2rem auto;padding:0 1rem;color:#17212b}table{border-collapse:collapse;width:100%}th,td{text-align:left;border-bottom:1px solid #ccd3d8;padding:.6rem}a{color:#075985}code{font-size:.9em}</style>
<main>
<h1>AVM Intelligence</h1>
<p>Accepted catalog generation {{.Generation}}, built {{.BuiltAt}}.</p>
<p>{{.ModuleCount}} analyzed modules. Results preserve pass, fail, unchecked, and not-applicable statuses; no aggregate score is claimed.</p>
<table><caption>Accepted module analyses</caption><thead><tr><th>Module</th><th>Source commit</th><th>Checks</th><th>Findings</th><th>Unchecked</th></tr></thead><tbody>
{{range .Modules}}<tr><td><a href="{{.Link}}">{{.ModuleID}}</a></td><td><code>{{.SourceSHA}}</code></td><td>{{.Checks}}</td><td>{{.Findings}}</td><td>{{.Unchecked}}</td></tr>{{end}}
</tbody></table>
</main>
</html>
`

const catalogModuleTemplate = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.ModuleID}} | AVM Intelligence</title>
<style>body{font:16px system-ui,sans-serif;line-height:1.5;max-width:1100px;margin:2rem auto;padding:0 1rem;color:#17212b}table{border-collapse:collapse;width:100%}th,td{text-align:left;border-bottom:1px solid #ccd3d8;padding:.6rem}a{color:#075985}code{font-size:.9em}</style>
<main>
<p><a href="../index.html">Back to catalog</a></p>
<h1>{{.ModuleID}}</h1>
<p>Source commit <code>{{.SourceSHA}}</code>. IaCLens {{.CLIVersion}} ({{.CLICommit}}).</p>
<p>Ruleset {{.RulesetName}}. Status values are evidence, not certification.</p>
<table><caption>Checks</caption><thead><tr><th>Path</th><th>Role</th><th>Check</th><th>Status</th><th>Message</th></tr></thead><tbody>
{{range .Rows}}<tr><td>{{.Path}}</td><td>{{.Role}}</td><td>{{.Check}}</td><td>{{.Status}}</td><td>{{.Message}}</td></tr>{{end}}
</tbody></table>
</main>
</html>
`

type catalogRow struct {
	ModuleID, SourceSHA, Link   string
	Checks, Findings, Unchecked int
}

type moduleRow struct {
	Path, Role, Check, Status, Message string
}

type modulePage struct {
	ModuleID, SourceSHA, CLIVersion, CLICommit, RulesetName string
	Rows                                                    []moduleRow
}

type CatalogReadiness struct {
	CatalogGeneration uint64                    `json:"catalog_generation"`
	LegacyModuleCount int                       `json:"legacy_module_count"`
	IndexModuleCount  int                       `json:"index_module_count"`
	Modules           []ModuleReadinessEvidence `json:"modules"`
}

type ModuleReadinessEvidence struct {
	ModuleID            string `json:"module_id"`
	SourceSHA           string `json:"source_sha"`
	LegacyReportSHA256  string `json:"legacy_report_sha256"`
	CheckEvidenceSHA256 string `json:"check_evidence_sha256"`
	CheckCount          int    `json:"check_count"`
}

type legacyCheckEvidence struct {
	UnitPath string        `json:"unit_path"`
	Check    iaclens.Check `json:"check"`
}

// Build renders the accepted candidate into a static catalog without scoring
// or exposing source expressions. Every report is validated before rendering.
func Build(dataRoot, outputRoot string) error {
	candidate, err := publish.LoadCandidate(dataRoot)
	if err != nil {
		return err
	}
	rows := make([]catalogRow, 0, len(candidate.Modules))
	for _, module := range candidate.Modules {
		report, err := loadReport(filepath.Join(dataRoot, filepath.FromSlash(module.ReportPath)))
		if err != nil {
			return fmt.Errorf("load report for %s: %w", module.ModuleID, err)
		}
		if report.Provenance.ModuleID != module.ModuleID || report.Provenance.SourceSHA != module.SourceSHA {
			return fmt.Errorf("report provenance does not match %s", module.ModuleID)
		}
		page := modulePage{ModuleID: module.ModuleID, SourceSHA: module.SourceSHA, CLIVersion: report.Provenance.CLIVersion, CLICommit: report.Provenance.CLICommit, RulesetName: report.RulesetName}
		findings, unchecked := 0, 0
		for _, unit := range report.Units {
			for _, check := range unit.Checks {
				page.Rows = append(page.Rows, moduleRow{Path: unit.Path, Role: unit.Role, Check: check.ID, Status: check.Status, Message: check.Message})
				switch check.Status {
				case "fail":
					findings++
				case "unchecked":
					unchecked++
				}
			}
		}
		link := filepath.ToSlash(filepath.Join("modules", module.ModuleID+".html"))
		rows = append(rows, catalogRow{ModuleID: module.ModuleID, SourceSHA: module.SourceSHA, Link: link, Checks: len(page.Rows), Findings: findings, Unchecked: unchecked})
		if err = render(filepath.Join(outputRoot, filepath.FromSlash(link)), catalogModuleTemplate, page); err != nil {
			return err
		}
	}
	return render(filepath.Join(outputRoot, "index.html"), catalogIndexTemplate, struct {
		Generation  uint64
		BuiltAt     string
		ModuleCount int
		Modules     []catalogRow
	}{candidate.CatalogGeneration, candidate.BuiltAt.UTC().Format("2006-01-02 15:04 UTC"), len(rows), rows})
}

// CompareLegacyAndIndex checks reader readiness without changing the active
// legacy site reader or selecting an index as a new source of truth.
func CompareLegacyAndIndex(legacyDataRoot, indexedCandidateRoot string) (CatalogReadiness, error) {
	candidate, err := publish.LoadCandidate(legacyDataRoot)
	if err != nil {
		return CatalogReadiness{}, fmt.Errorf("load legacy catalog candidate: %w", err)
	}
	newCatalog, err := readIndexDocument[index.CatalogIndex](indexedCandidateRoot, "data/catalog.json")
	if err != nil {
		return CatalogReadiness{}, err
	}
	if newCatalog.SchemaVersion != 1 || newCatalog.RecordType != "catalog_index" || identity.ValidateRecordID("catalog-index", newCatalog.IndexID) != nil {
		return CatalogReadiness{}, fmt.Errorf("generated catalog index identity is invalid")
	}
	datasetPath, err := identity.CanonicalDataPath("dataset")
	if err != nil {
		return CatalogReadiness{}, err
	}
	dataset, err := readIndexDocument[index.DatasetManifest](indexedCandidateRoot, datasetPath)
	if err != nil {
		return CatalogReadiness{}, err
	}
	if dataset.SchemaVersion != 2 || dataset.RecordType != "dataset_manifest" || identity.ValidateRecordID("dataset", dataset.DatasetID) != nil {
		return CatalogReadiness{}, fmt.Errorf("generated dataset manifest identity is invalid")
	}
	if newCatalog.CatalogGeneration != candidate.CatalogGeneration || dataset.CatalogGeneration != candidate.CatalogGeneration {
		return CatalogReadiness{}, fmt.Errorf("legacy candidate and generated indexes have different catalog generations")
	}
	if err := compareCatalogModuleIDs(candidate, newCatalog); err != nil {
		return CatalogReadiness{}, err
	}
	readiness := CatalogReadiness{
		CatalogGeneration: candidate.CatalogGeneration,
		LegacyModuleCount: len(candidate.Modules),
		IndexModuleCount:  len(newCatalog.Modules),
		Modules:           make([]ModuleReadinessEvidence, 0, len(candidate.Modules)),
	}
	for _, module := range candidate.Modules {
		evidence, err := compareLegacyModuleEvidence(legacyDataRoot, indexedCandidateRoot, module, dataset.RecordDigests)
		if err != nil {
			return CatalogReadiness{}, err
		}
		readiness.Modules = append(readiness.Modules, evidence)
	}
	return readiness, nil
}

func compareCatalogModuleIDs(candidate publish.Candidate, catalogIndex index.CatalogIndex) error {
	if catalogIndex.RecordType != "catalog_index" || catalogIndex.SchemaVersion != 1 || len(candidate.Modules) != len(catalogIndex.Modules) {
		return fmt.Errorf("legacy candidate and catalog index module counts or contracts differ")
	}
	for position, module := range candidate.Modules {
		if catalogIndex.Modules[position].ModuleID != module.ModuleID {
			return fmt.Errorf("legacy/index module identity differs at position %d", position)
		}
	}
	return nil
}

func compareLegacyModuleEvidence(legacyDataRoot, indexedCandidateRoot string, module publish.CandidateModule, recordDigests map[string]string) (ModuleReadinessEvidence, error) {
	moduleIndexPath, err := identity.ModuleIndexPath(module.ModuleID)
	if err != nil {
		return ModuleReadinessEvidence{}, err
	}
	moduleIndex, err := readIndexDocument[index.ModuleIndex](indexedCandidateRoot, moduleIndexPath)
	if err != nil {
		return ModuleReadinessEvidence{}, err
	}
	if moduleIndex.SchemaVersion != 1 || moduleIndex.RecordType != "module_index" || identity.ValidateRecordID("module-index", moduleIndex.IndexID) != nil || moduleIndex.ModuleID != module.ModuleID || !containsSHA(moduleIndex.SourceSHAs, module.SourceSHA) {
		return ModuleReadinessEvidence{}, fmt.Errorf("legacy/index source identity differs for %s", module.ModuleID)
	}
	reportPath := filepath.Join(legacyDataRoot, filepath.FromSlash(module.ReportPath))
	rawReport, err := os.ReadFile(reportPath)
	if err != nil {
		return ModuleReadinessEvidence{}, fmt.Errorf("read legacy report bytes for %s: %w", module.ModuleID, err)
	}
	report, err := decodeReport(rawReport)
	if err != nil {
		return ModuleReadinessEvidence{}, fmt.Errorf("decode legacy report for %s: %w", module.ModuleID, err)
	}
	if report.Provenance.ModuleID != module.ModuleID || report.Provenance.SourceSHA != module.SourceSHA {
		return ModuleReadinessEvidence{}, fmt.Errorf("legacy report provenance differs for %s", module.ModuleID)
	}
	reportDigest := sha256.Sum256(rawReport)
	reportDigestHex := hex.EncodeToString(reportDigest[:])
	canonicalReportPath := filepath.ToSlash(filepath.Join("data", filepath.FromSlash(module.ReportPath)))
	if recordDigests[canonicalReportPath] != reportDigestHex {
		return ModuleReadinessEvidence{}, fmt.Errorf("dataset does not preserve exact legacy report bytes for %s", module.ModuleID)
	}
	checkEvidence := make([]legacyCheckEvidence, 0)
	for _, unit := range report.Units {
		for _, check := range unit.Checks {
			checkEvidence = append(checkEvidence, legacyCheckEvidence{UnitPath: unit.Path, Check: check})
		}
	}
	encodedEvidence, err := json.Marshal(checkEvidence)
	if err != nil {
		return ModuleReadinessEvidence{}, err
	}
	checkDigest := sha256.Sum256(encodedEvidence)
	return ModuleReadinessEvidence{
		ModuleID: module.ModuleID, SourceSHA: module.SourceSHA,
		LegacyReportSHA256: reportDigestHex, CheckEvidenceSHA256: hex.EncodeToString(checkDigest[:]),
		CheckCount: len(checkEvidence),
	}, nil
}

func readIndexDocument[T any](root, relativePath string) (T, error) {
	var value T
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relativePath)))
	if err != nil {
		return value, fmt.Errorf("read generated index %s: %w", relativePath, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode generated index %s: %w", relativePath, err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return value, fmt.Errorf("generated index %s must contain one JSON document", relativePath)
	}
	return value, nil
}

func containsSHA(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func loadReport(path string) (iaclens.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return iaclens.Report{}, err
	}
	return decodeReport(data)
}

func decodeReport(data []byte) (iaclens.Report, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var report iaclens.Report
	if err := decoder.Decode(&report); err != nil {
		return iaclens.Report{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return iaclens.Report{}, fmt.Errorf("report must contain one JSON document")
	}
	if err := report.Validate(); err != nil {
		return iaclens.Report{}, err
	}
	return report, nil
}

func render(path, source string, value any) error {
	parsed, err := template.New(filepath.Base(path)).Parse(source)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return parsed.Execute(file, value)
}
