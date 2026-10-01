package github

import (
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
	"github.com/softwareone-public/avm-intelligence/internal/source"
)

func TestPilotProjectionStaysUnassessedUntilPublication(t *testing.T) {
	t.Parallel()
	pilot := reviewedPilot()
	report := iaclens.Report{
		SchemaVersion: iaclens.SupportedSchemaVersion, AdapterVersion: iaclens.AdapterVersion,
		Provenance:  iaclens.Provenance{ModuleID: pilot.Module.ID, SourceSHA: pilot.Module.Commit, CLIVersion: "0.1.0", CLICommit: strings.Repeat("b", 40), AdapterVersion: iaclens.AdapterVersion},
		RulesetName: "factory", RulesetDigest: strings.Repeat("c", 64), SourceDigest: strings.Repeat("d", 64),
		Units: []iaclens.Unit{
			{Path: ".", Role: "component", Classification: iaclens.Classification{Kind: "module", Method: "override", Reason: "fixture"}, Checks: []iaclens.Check{{ID: "input-descriptions", Engine: "go", Status: "pass", Severity: "warning"}}},
			{Path: "examples/default", Role: "example", Owner: ".", OwnerScope: "component", Classification: iaclens.Classification{Kind: "unknown", Method: "unavailable", Reason: "fixture"}, Checks: []iaclens.Check{{ID: "deployment-core-version", Engine: "go", Status: "unchecked", Severity: "warning"}}},
		},
	}
	projection, err := FromAnalysis(pilot, report, "https://github.com/softwareone-public/avm-intelligence/actions/runs/36847787791", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if projection.Status != StatusPublishing || projection.Outcome != OutcomeUnassessed || projection.Published != "" {
		t.Fatalf("projection overstated completion: %#v", projection)
	}
	if !strings.Contains(projection.IssueBody, Marker+pilot.Module.ID) {
		t.Fatal("stable marker missing")
	}
}

func TestProjectionRejectsDone(t *testing.T) {
	t.Parallel()
	projection := Projection{Status: StatusDone, Outcome: OutcomeClean, WorkType: "Module analysis", ModuleType: "res", IssueBody: "<!-- " + Marker + "x -->"}
	if err := projection.Validate(); err == nil {
		t.Fatal("unpublished projection was marked done")
	}
}

func reviewedPilot() source.Pilot {
	pilot := source.Pilot{Version: 1}
	pilot.Module.ID = "avm-res-resources-resourcegroup"
	pilot.Module.Type = "res"
	pilot.Module.Domain = "resources"
	pilot.Module.Repository = source.PilotRepository
	pilot.Module.Version = source.PilotVersion
	pilot.Module.Commit = source.PilotCommit
	return pilot
}
