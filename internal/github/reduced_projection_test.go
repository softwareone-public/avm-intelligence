package github

import (
	"strings"
	"testing"

	"github.com/softwareone-public/avm-intelligence/internal/queue"
)

func TestNativeSummaryPreservesHumanNotesAndRejectsAmbiguousBoundaries(t *testing.T) {
	projection, err := FromReducedStatus(queue.ModuleStatus{
		ModuleID: "avm-res-status-test", Status: queue.ModuleStatusBacklog,
	}, ReducedProjectionInput{ModuleType: "res"})
	if err != nil {
		t.Fatal(err)
	}
	existing := "Human introduction\n\n" + projection.IssueBody + "\n\nHuman follow-up"
	projection.Status = StatusQueued
	projection.IssueBody = projection.body()
	merged, err := mergeIssueBody(existing, projection.IssueBody)
	if err != nil || !strings.HasPrefix(merged, "Human introduction\n\n") || !strings.HasSuffix(merged, "\n\nHuman follow-up") || !strings.Contains(merged, "- Status: `Queued`") {
		t.Fatalf("native summary changed human notes: body=%q err=%v", merged, err)
	}
	for _, malformed := range []string{SummaryStart, SummaryEnd, SummaryStart + SummaryStart + SummaryEnd, SummaryEnd + SummaryStart} {
		if _, err := mergeIssueBody(malformed, projection.IssueBody); err == nil {
			t.Fatalf("ambiguous summary boundaries were accepted: %q", malformed)
		}
	}
}

func TestFromReducedStatusKeepsLifecycleAndVerifiedPublicationSeparate(t *testing.T) {
	base := queue.ModuleStatus{
		ModuleID: "avm-res-status-test", Status: queue.ModuleStatusBacklog,
		SourceVersion: "v1.2.3", SourceSHA: strings.Repeat("a", 40),
	}
	projection, err := FromReducedStatus(base, ReducedProjectionInput{ModuleType: "res", Domain: "compute"})
	if err != nil {
		t.Fatal(err)
	}
	if projection.Status != StatusBacklog || projection.Published != "" || projection.Outcome != OutcomeUnassessed {
		t.Fatalf("backlog projection = %+v", projection)
	}

	base.Status = queue.ModuleStatusDone
	if _, err = FromReducedStatus(base, ReducedProjectionInput{ModuleType: "res"}); err == nil {
		t.Fatal("Done without an exact published commit was accepted")
	}
	base.PublishedCommit = strings.Repeat("b", 40)
	projection, err = FromReducedStatus(base, ReducedProjectionInput{ModuleType: "res"})
	if err != nil {
		t.Fatal(err)
	}
	if projection.Status != StatusDone || projection.Published != base.PublishedCommit {
		t.Fatalf("Done projection = %+v", projection)
	}

	base.Status = queue.ModuleStatusRetired
	base.PublishedCommit = ""
	projection, err = FromReducedStatus(base, ReducedProjectionInput{ModuleType: "res"})
	if err != nil {
		t.Fatal(err)
	}
	if projection.Status != StatusRetired {
		t.Fatalf("retired lifecycle projection = %+v", projection)
	}
}
