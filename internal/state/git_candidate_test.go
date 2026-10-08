package state

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBuildCandidateCreatesIsolatedOneParentCommitAndPreservesUnrelatedFiles(t *testing.T) {
	fixture := newGitBackendFixture(t)
	backend := fixture.backend()
	parentHead, current, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next := cloneSnapshot(current)
	next.Generation++
	next.Records["state/requests/request-1.json"] = Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"status":"updated"}`)}
	beforeHead := gitFixtureHead(t, fixture.root)
	candidate, err := backend.BuildCandidate(context.Background(), CandidateOptions{
		ParentHead: parentHead,
		Snapshot:   next,
		Subject:    "state(dispatch): claim one request",
		Now:        time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC),
		Provenance: GitProvenance{Operation: "dispatch.claim", Repository: "owner/repository", Workflow: "manual-dispatch.yml", Run: "123", RunAttempt: "1", Job: "claim", Event: "claim-1", Module: "avm-res-example", Request: "request-1", Fence: "4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ParentHead != parentHead || !gitObjectIDPattern.MatchString(candidate.CandidateID) || !gitObjectIDPattern.MatchString(candidate.TreeID) {
		t.Fatalf("candidate identity invalid: %+v", candidate)
	}
	if afterHead := gitFixtureHead(t, fixture.root); afterHead != beforeHead {
		t.Fatal("candidate construction changed a ref")
	}
	commit := gitFixtureOutput(t, fixture.checkout, "cat-file", "-p", candidate.CandidateID)
	if !validSingleParentCommit(commit, candidate.TreeID, parentHead) {
		t.Fatalf("candidate is not a one-parent commit: %s", commit)
	}
	if !strings.Contains(string(commit), "Factory-Operation: dispatch.claim") || !strings.Contains(string(commit), "Factory-Head: "+parentHead) {
		t.Fatalf("candidate provenance missing or incorrect: %s", commit)
	}
	candidateFiles := gitFixtureTreeFiles(t, fixture.checkout, candidate.TreeID)
	for name, content := range fixture.files {
		if name == "state/transaction.json" || name == "state/requests/request-1.json" {
			continue
		}
		if string(candidateFiles[name]) != string(content) {
			t.Fatalf("unrelated parent file %s changed", name)
		}
	}
	if string(candidateFiles["state/requests/request-1.json"]) != string(mustEncodeStateRecord(t, next.Records["state/requests/request-1.json"])) {
		t.Fatal("candidate did not contain updated request bytes")
	}
}

func TestBuildCandidateUsesExplicitDeletionAndNoopProducesNoCommit(t *testing.T) {
	fixture := newGitBackendFixture(t)
	backend := fixture.backend()
	parentHead, current, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := backend.BuildCandidate(context.Background(), CandidateOptions{ParentHead: parentHead, Snapshot: current, Subject: "state(noop): preserve request"})
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.CandidateID != "" || unchanged.TreeID == "" {
		t.Fatalf("no-op created a commit: %+v", unchanged)
	}
	deleted := cloneSnapshot(current)
	delete(deleted.Records, "state/requests/request-1.json")
	deleted.AppliedEvents["delete-request"] = strings.Repeat("a", 64)
	deleted.Generation++
	candidate, err := backend.BuildCandidate(context.Background(), CandidateOptions{ParentHead: parentHead, Snapshot: deleted, Subject: "state(cleanup): remove one authorized request"})
	if err != nil {
		t.Fatal(err)
	}
	files := gitFixtureTreeFiles(t, fixture.checkout, candidate.TreeID)
	if _, exists := files["state/requests/request-1.json"]; exists {
		t.Fatal("explicitly removed record survived candidate tree")
	}
	if _, exists := files["state/transaction.json"]; !exists {
		t.Fatal("candidate lost required replay metadata")
	}
}

func TestBuildCandidateChangesOnlyDeclaredStateDelta(t *testing.T) {
	fixture := newGitBackendFixture(t)
	backend := fixture.backend()
	parent, current, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	updated := cloneSnapshot(current)
	updated.Generation++
	updated.Records["state/requests/request-1.json"] = Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"status":"updated"}`)}
	candidate, err := backend.BuildCandidate(context.Background(), CandidateOptions{ParentHead: parent, Snapshot: updated, Subject: "state(dispatch): update one request"})
	if err != nil {
		t.Fatal(err)
	}
	parentFiles := gitFixtureTreeFiles(t, fixture.checkout, func() string {
		output := gitFixtureOutput(t, fixture.checkout, "rev-parse", parent+"^{tree}")
		return strings.TrimSpace(string(output))
	}())
	candidateFiles := gitFixtureTreeFiles(t, fixture.checkout, candidate.TreeID)
	changed := 0
	for path, content := range parentFiles {
		if string(candidateFiles[path]) != string(content) {
			changed++
		}
	}
	for path := range candidateFiles {
		if _, exists := parentFiles[path]; !exists {
			changed++
		}
	}
	if changed != 2 {
		t.Fatalf("candidate changed %d tree entries; request plus global metadata require exactly 2", changed)
	}
}

func TestBuildCandidateRejectsUnsafeDeltaAndProvenance(t *testing.T) {
	fixture := newGitBackendFixture(t)
	backend := fixture.backend()
	parent, current, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	invalidSnapshot := cloneSnapshot(current)
	invalidSnapshot.Records["state/../workflow.yml"] = Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"status":"bad"}`)}
	tests := []struct {
		name    string
		options CandidateOptions
	}{
		{name: "path escape", options: CandidateOptions{ParentHead: parent, Snapshot: invalidSnapshot, Subject: "state(update): invalid path"}},
		{name: "subject secret", options: CandidateOptions{ParentHead: parent, Snapshot: current, Subject: "state(update): token=secret"}},
		{name: "subject controls", options: CandidateOptions{ParentHead: parent, Snapshot: current, Subject: "bad\nFactory-Operation: evil"}},
		{name: "operation allowlist", options: CandidateOptions{ParentHead: parent, Snapshot: current, Subject: "state(update): valid subject", Provenance: GitProvenance{Operation: "arbitrary.command"}}},
		{name: "trailer secret", options: CandidateOptions{ParentHead: parent, Snapshot: current, Subject: "state(update): valid subject", Provenance: GitProvenance{Event: "token=secret"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := backend.BuildCandidate(context.Background(), test.options); err == nil {
				t.Fatal("unsafe candidate was accepted")
			}
		})
	}
}

func gitFixtureHead(t *testing.T, directory string) string {
	t.Helper()
	return strings.TrimSpace(string(gitFixtureOutput(t, directory, "rev-parse", "refs/heads/factory-state")))
}

func gitFixtureOutput(t *testing.T, directory string, arguments ...string) []byte {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Git fixture inspection failed: %s: %v", output, err)
	}
	return output
}

func gitFixtureTreeFiles(t *testing.T, directory, treeID string) map[string][]byte {
	t.Helper()
	listing := gitFixtureOutput(t, directory, "ls-tree", "-r", "-z", "--full-tree", treeID)
	files := map[string][]byte{}
	for _, entry := range bytes.Split(bytes.TrimSuffix(listing, []byte{0}), []byte{0}) {
		metadata, filename, _ := bytes.Cut(entry, []byte{'\t'})
		fields := strings.Fields(string(metadata))
		if len(fields) != 3 {
			t.Fatalf("invalid candidate tree entry: %q", entry)
		}
		content := gitFixtureOutput(t, directory, "cat-file", "blob", fields[2])
		files[string(filename)] = content
	}
	return files
}

func mustEncodeStateRecord(t *testing.T, record Record) []byte {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}
