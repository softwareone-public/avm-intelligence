package site

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"

	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
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

func loadReport(path string) (iaclens.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return iaclens.Report{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var report iaclens.Report
	if err = decoder.Decode(&report); err != nil {
		return iaclens.Report{}, err
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return iaclens.Report{}, fmt.Errorf("report must contain one JSON document")
	}
	if err = report.Validate(); err != nil {
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
