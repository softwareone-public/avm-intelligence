// Package github projects accepted factory state to Issues and Projects.
// This slice builds that projection and does not publish repository contents.
package github

import (
	"fmt"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
	"github.com/softwareone-public/avm-intelligence/internal/source"
)

const (
	// Marker identifies the stable module Issue without depending on its title.
	Marker = "factory-module-id:"
	// StatusPublishing is used until an exact Pages deployment is verified.
	StatusPublishing = "Publishing"
	// StatusDone is reserved for a deployed accepted result.
	StatusDone = "Done"
	// OutcomeUnassessed means at least one applicable check was not assessed.
	OutcomeUnassessed = "Unassessed"
	// OutcomeFindings means a check failed and no check remains unassessed.
	OutcomeFindings = "Findings"
	// OutcomeClean means every recorded check passed.
	OutcomeClean = "Clean"
)

// Projection is the board state for one accepted analysis. It is not deployment
// success: Done requires a separate publication record.
type Projection struct {
	ModuleID     string
	ModuleType   string
	Domain       string
	Version      string
	SourceSHA    string
	CLIVersion   string
	Status       string
	Outcome      string
	WorkType     string
	RunURL       string
	Published    string
	LastAnalyzed time.Time
	IssueTitle   string
	IssueBody    string
}

// FromAnalysis maps a normalized report to the approved Project fields.
func FromAnalysis(pilot source.Pilot, report iaclens.Report, runURL string, analyzedAt time.Time) (Projection, error) {
	if err := pilot.Validate(); err != nil {
		return Projection{}, err
	}
	if err := report.Validate(); err != nil {
		return Projection{}, err
	}
	if report.Provenance.ModuleID != pilot.Module.ID || report.Provenance.SourceSHA != pilot.Module.Commit {
		return Projection{}, fmt.Errorf("analysis provenance does not match the reviewed pilot")
	}
	if runURL == "" || !strings.HasPrefix(runURL, "https://github.com/") {
		return Projection{}, fmt.Errorf("analysis run URL is required")
	}
	outcome, err := outcome(report)
	if err != nil {
		return Projection{}, err
	}
	projection := Projection{
		ModuleID: pilot.Module.ID, ModuleType: pilot.Module.Type, Domain: pilot.Module.Domain,
		Version: pilot.Module.Version, SourceSHA: pilot.Module.Commit, CLIVersion: report.Provenance.CLIVersion,
		Status: StatusPublishing, Outcome: outcome, WorkType: "Module analysis", RunURL: runURL,
		LastAnalyzed: analyzedAt.UTC(), IssueTitle: "Module analysis: " + pilot.Module.ID,
	}
	projection.IssueBody = projection.body()
	return projection, projection.Validate()
}

// Validate rejects a projection that claims deployment or hides unassessed checks.
func (p Projection) Validate() error {
	if p.Status == StatusDone || p.Published != "" {
		return fmt.Errorf("analysis projection cannot claim publication")
	}
	if p.Status != StatusPublishing || p.WorkType != "Module analysis" || p.ModuleType != "res" {
		return fmt.Errorf("projection fields are outside the pilot contract")
	}
	switch p.Outcome {
	case OutcomeUnassessed, OutcomeFindings, OutcomeClean:
	default:
		return fmt.Errorf("invalid projection outcome %q", p.Outcome)
	}
	if !strings.Contains(p.IssueBody, Marker+p.ModuleID) {
		return fmt.Errorf("projection is missing its stable identity marker")
	}
	return nil
}

func (p Projection) body() string {
	return strings.Join([]string{
		"<!-- " + Marker + p.ModuleID + " -->",
		"## Factory summary",
		"",
		"- Module: `" + p.ModuleID + "`",
		"- Source: `" + p.Version + "` at `" + p.SourceSHA + "`",
		"- IaCLens: `" + p.CLIVersion + "`",
		"- Status: `" + p.Status + "`",
		"- Outcome: `" + p.Outcome + "`",
		"- Run: " + p.RunURL,
		"",
		"Publication is not claimed by this analysis projection.",
	}, "\n")
}

func outcome(report iaclens.Report) (string, error) {
	var pass, fail, unchecked int
	for _, unit := range report.Units {
		for _, check := range unit.Checks {
			switch check.Status {
			case "pass":
				pass++
			case "fail":
				fail++
			case "unchecked":
				unchecked++
			case "not_applicable":
			default:
				return "", fmt.Errorf("unknown check status %q", check.Status)
			}
		}
	}
	if unchecked > 0 {
		return OutcomeUnassessed, nil
	}
	if fail > 0 {
		return OutcomeFindings, nil
	}
	if pass == 0 {
		return "", fmt.Errorf("analysis has no assessed checks")
	}
	return OutcomeClean, nil
}
