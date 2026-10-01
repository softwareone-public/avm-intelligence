// Package site renders one evidence page for the pilot module. It does not
// calculate a score or expose source expressions.
package site

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"

	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
)

const pageTemplate = `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>{{.ModuleID}}</title>
<h1>{{.ModuleID}}</h1>
<p>Pilot module {{.Version}} at commit {{.Commit}}. This is not full-catalog coverage.</p>
<p>IaCLens {{.CLIVersion}} ({{.CLICommit}}), ruleset {{.RulesetName}}.</p>
<table>
<thead><tr><th>Path</th><th>Role</th><th>Owner</th><th>Check</th><th>Status</th></tr></thead>
<tbody>
{{range .Rows}}<tr><td>{{.Path}}</td><td>{{.Role}}</td><td>{{.Owner}}</td><td>{{.Check}}</td><td>{{.Status}}</td></tr>{{end}}
</tbody>
</table>
</html>
`

// Row is one visible check result. Status text is escaped with the surrounding HTML.
type Row struct {
	Path, Role, Owner, Check, Status string
}

// Write renders the normalized report to a standalone HTML file.
func Write(path string, report iaclens.Report, version string) error {
	if err := report.Validate(); err != nil {
		return err
	}
	rows := make([]Row, 0)
	for _, unit := range report.Units {
		for _, check := range unit.Checks {
			rows = append(rows, Row{Path: unit.Path, Role: unit.Role, Owner: unit.Owner, Check: check.ID, Status: check.Status})
		}
	}
	data := struct {
		ModuleID, Version, Commit, CLIVersion, CLICommit, RulesetName string
		Rows                                                          []Row
	}{
		ModuleID: report.Provenance.ModuleID, Version: version, Commit: report.Provenance.SourceSHA,
		CLIVersion: report.Provenance.CLIVersion, CLICommit: report.Provenance.CLICommit, RulesetName: report.RulesetName, Rows: rows,
	}
	parsed, err := template.New("module").Parse(pageTemplate)
	if err != nil {
		return fmt.Errorf("parse module page: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return parsed.Execute(file, data)
}
