// Package github projects accepted factory state to Issues and Projects.
// This slice builds that projection and does not publish repository contents.
package github

import (
	"fmt"
	"strings"
	"time"
)

const (
	// Marker identifies the stable module Issue without depending on its title.
	Marker = "factory-module-id:"
	// SummaryStart and SummaryEnd delimit only the bot-owned Issue text.
	SummaryStart = "<!-- BEGIN AVM FACTORY SUMMARY -->"
	SummaryEnd   = "<!-- END AVM FACTORY SUMMARY -->"
	// StatusPublishing is used until an exact Pages deployment is verified.
	StatusPublishing = "Publishing"
	// StatusDone is reserved for a deployed accepted result.
	StatusDone             = "Done"
	StatusBacklog          = "Backlog"
	StatusQueued           = "Queued"
	StatusAnalyzing        = "Analyzing"
	StatusValidating       = "Validating"
	StatusNeedsReview      = "Needs review"
	StatusFailed           = "Failed"
	StatusBlocked          = "Blocked"
	StatusRetired          = "Retired"
	WorkTypeModuleAnalysis = "Module analysis"
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
	ModuleID       string
	ModuleType     string
	Domain         string
	Version        string
	SourceSHA      string
	FactoryVersion string
	Status         string
	Outcome        string
	WorkType       string
	RunURL         string
	Published      string
	LastAnalyzed   time.Time
	IssueTitle     string
	IssueBody      string
}

// Labels returns stable type and lifecycle labels without removing human labels.
func Labels(projection Projection) []string {
	return []string{"factory:module-analysis", "factory:status-" + strings.ToLower(strings.ReplaceAll(projection.Status, " ", "-")), "avm:" + projection.ModuleType}
}

// Validate rejects a projection that claims deployment or hides unassessed checks.
func (p Projection) Validate() error {
	validStatuses := map[string]bool{StatusBacklog: true, StatusQueued: true, StatusAnalyzing: true, StatusValidating: true, StatusNeedsReview: true, StatusFailed: true, StatusBlocked: true, StatusPublishing: true, StatusDone: true, StatusRetired: true}
	if !validStatuses[p.Status] {
		return fmt.Errorf("invalid projection status %q", p.Status)
	}
	if p.Status == StatusDone && p.Published == "" {
		return fmt.Errorf("done projection requires a published commit")
	}
	if p.Status != StatusDone && p.Published != "" {
		return fmt.Errorf("unpublished projection cannot claim publication")
	}
	if p.WorkType != WorkTypeModuleAnalysis || (p.ModuleType != "res" && p.ModuleType != "ptn" && p.ModuleType != "utl") {
		return fmt.Errorf("projection fields are outside the fleet contract")
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
	publicationSummary := "Publication is not claimed by this analysis projection."
	if p.Published != "" {
		publicationSummary = "Published commit verified: `" + p.Published + "`."
	}
	return strings.Join([]string{
		"<!-- " + Marker + p.ModuleID + " -->",
		SummaryStart,
		"## Factory summary",
		"",
		"- Module: `" + p.ModuleID + "`",
		"- Source: `" + p.Version + "` at `" + p.SourceSHA + "`",
		"- Factory: `" + p.FactoryVersion + "`",
		"- Status: `" + p.Status + "`",
		"- Outcome: `" + p.Outcome + "`",
		"- Published commit: `" + p.Published + "`",
		"- Run: " + p.RunURL,
		"",
		publicationSummary,
		SummaryEnd,
	}, "\n")
}

// mergeIssueBody replaces only the bounded factory summary and preserves all
// text maintained by humans. For a pre-marker issue, it appends the summary
// once. Malformed or duplicate boundaries fail closed rather than deleting text.
func mergeIssueBody(existing, generated string) (string, error) {
	newStart, newEnd := strings.Index(generated, SummaryStart), strings.Index(generated, SummaryEnd)
	if newStart < 0 || newEnd <= newStart || strings.Count(generated, SummaryStart) != 1 || strings.Count(generated, SummaryEnd) != 1 {
		return "", fmt.Errorf("generated Issue body has invalid factory summary boundaries")
	}
	newBlock := generated[newStart : newEnd+len(SummaryEnd)]
	oldStart, oldEnd := strings.Index(existing, SummaryStart), strings.Index(existing, SummaryEnd)
	starts, ends := strings.Count(existing, SummaryStart), strings.Count(existing, SummaryEnd)
	if starts == 0 && ends == 0 {
		if strings.TrimSpace(existing) == "" {
			return generated, nil
		}
		return strings.TrimRight(existing, "\n") + "\n\n" + newBlock, nil
	}
	if starts != 1 || ends != 1 || oldStart < 0 || oldEnd <= oldStart {
		return "", fmt.Errorf("existing Issue body has invalid factory summary boundaries")
	}
	return existing[:oldStart] + newBlock + existing[oldEnd+len(SummaryEnd):], nil
}
