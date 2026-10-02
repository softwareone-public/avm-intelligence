package github

import (
	"fmt"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/queue"
)

// ReducedProjectionInput contains quality and activity fields that are not
// calculated by the lifecycle reducer.
type ReducedProjectionInput struct {
	ModuleType   string
	Domain       string
	CLIVersion   string
	Outcome      string
	RunURL       string
	LastAnalyzed time.Time
}

// FromReducedStatus projects shared lifecycle truth while preserving separate
// quality, source, publication, and activity data.
func FromReducedStatus(status queue.ModuleStatus, input ReducedProjectionInput) (Projection, error) {
	if status.ModuleID == "" || status.Status == "" {
		return Projection{}, fmt.Errorf("reduced module lifecycle identity is required")
	}
	outcome := input.Outcome
	if outcome == "" {
		outcome = OutcomeUnassessed
	}
	workType := WorkTypeModuleAnalysis
	published := ""
	if status.Status == queue.ModuleStatusDone {
		published = status.PublishedCommit
	}
	projection := Projection{
		ModuleID: status.ModuleID, ModuleType: input.ModuleType, Domain: input.Domain,
		Version: status.SourceVersion, SourceSHA: status.SourceSHA, CLIVersion: input.CLIVersion,
		Status: status.Status, Outcome: outcome, WorkType: workType,
		RunURL: input.RunURL, Published: published, LastAnalyzed: input.LastAnalyzed.UTC(),
		IssueTitle: workType + ": " + status.ModuleID,
	}
	projection.IssueBody = projection.body()
	return projection, projection.Validate()
}
