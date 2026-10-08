package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestGitPublishAdvancesOnlyByOneNonForceCandidate(t *testing.T) {
	fixture := newGitBackendFixture(t)
	backend := fixture.publishableBackend(&BlobReadCache{})
	parent, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next := cloneSnapshot(snapshot)
	next.Generation++
	next.Records["state/requests/request-1.json"] = Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"status":"claimed"}`)}
	candidate, err := backend.BuildCandidate(context.Background(), CandidateOptions{ParentHead: parent, Snapshot: next, Subject: "state(dispatch): claim request"})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := backend.Publish(context.Background(), candidate)
	if err != nil || outcome.Kind != PublicationCommitted || outcome.Head != candidate.CandidateID {
		t.Fatalf("publication outcome = %+v, error = %v", outcome, err)
	}
	remoteHead := gitFixtureOutput(t, fixture.remote, "rev-parse", "refs/heads/factory-state")
	if string(remoteHead) == "" || string(remoteHead) != candidate.CandidateID+"\n" {
		t.Fatalf("remote ref = %s, want candidate %s", remoteHead, candidate.CandidateID)
	}
}

func TestGitPublishClassifiesRealSiblingPushAsRejected(t *testing.T) {
	fixture := newGitBackendFixture(t)
	backend := fixture.publishableBackend(&BlobReadCache{})
	parent, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	left := cloneSnapshot(snapshot)
	left.Generation++
	left.Records["state/requests/request-1.json"] = Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"writer":"left"}`)}
	leftCandidate, err := backend.BuildCandidate(context.Background(), CandidateOptions{ParentHead: parent, Snapshot: left, Subject: "state(test): left sibling"})
	if err != nil {
		t.Fatal(err)
	}
	right := cloneSnapshot(snapshot)
	right.Generation++
	right.Records["state/requests/request-1.json"] = Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"writer":"right"}`)}
	rightCandidate, err := backend.BuildCandidate(context.Background(), CandidateOptions{ParentHead: parent, Snapshot: right, Subject: "state(test): right sibling"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := backend.Publish(context.Background(), leftCandidate)
	if err != nil || first.Kind != PublicationCommitted {
		t.Fatalf("first sibling publish = %+v, %v", first, err)
	}
	second, err := backend.Publish(context.Background(), rightCandidate)
	var commandError *GitCommandError
	if !errors.As(err, &commandError) || commandError.Category != GitFailureContention || second.Kind != PublicationRejected {
		t.Fatalf("stale sibling publish = %+v, %v; want definite rejection", second, err)
	}
	remoteHead := gitFixtureOutput(t, fixture.remote, "rev-parse", "refs/heads/factory-state")
	if string(remoteHead) != leftCandidate.CandidateID+"\n" {
		t.Fatalf("rejected sibling changed remote head to %s", remoteHead)
	}
}

func TestGitPublishReconcilesAcknowledgementLossToDescendant(t *testing.T) {
	fixture := newGitBackendFixture(t)
	backend := fixture.publishableBackend(&BlobReadCache{})
	parent, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next := cloneSnapshot(snapshot)
	next.Generation++
	next.Records["state/requests/request-1.json"] = Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"status":"published"}`)}
	candidate, err := backend.BuildCandidate(context.Background(), CandidateOptions{ParentHead: parent, Snapshot: next, Subject: "state(test): lost acknowledgement"})
	if err != nil {
		t.Fatal(err)
	}
	backend.Runner = &ackLossGitRunner{countingGitRunner: fixture.runner, fixture: fixture, backend: backend, candidate: candidate}
	outcome, err := backend.Publish(context.Background(), candidate)
	if err != nil || outcome.Kind != PublicationCommitted || outcome.Head == candidate.CandidateID || outcome.CandidateID != candidate.CandidateID {
		t.Fatalf("lost acknowledgement reconciliation = %+v, %v", outcome, err)
	}
}

type ackLossGitRunner struct {
	*countingGitRunner
	fixture   *gitBackendFixture
	backend   *VerifiedGitBackend
	candidate GitCandidate
}

func (runner *ackLossGitRunner) Run(ctx context.Context, command GitCommand) (GitCommandResult, error) {
	if len(command.Arguments) == 0 || command.Arguments[0] != "push" {
		return runner.countingGitRunner.Run(ctx, command)
	}
	if _, err := runner.countingGitRunner.Run(ctx, command); err != nil {
		return GitCommandResult{}, err
	}
	if err := runner.fixture.advanceRemoteWithEmptyCommit(); err != nil {
		return GitCommandResult{}, err
	}
	return GitCommandResult{ExitCode: 1}, &GitCommandError{Operation: "push", Category: GitFailureCommand, ExitCode: 1}
}

func (fixture *gitBackendFixture) advanceRemoteWithEmptyCommit() error {
	remoteHead := strings.TrimSpace(string(gitFixtureOutput(fixture.t, fixture.remote, "rev-parse", "refs/heads/factory-state")))
	treeID := strings.TrimSpace(string(gitFixtureOutput(fixture.t, fixture.remote, "rev-parse", remoteHead+"^{tree}")))
	command := exec.Command("git", "--git-dir", fixture.remote, "commit-tree", treeID, "-p", remoteHead, "-m", "descendant after lost acknowledgement")
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_AUTHOR_NAME=Factory Fixture", "GIT_AUTHOR_EMAIL=factory@example.invalid", "GIT_COMMITTER_NAME=Factory Fixture", "GIT_COMMITTER_EMAIL=factory@example.invalid", "GIT_AUTHOR_DATE=@1791455696 +0000", "GIT_COMMITTER_DATE=@1791455696 +0000")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("create acknowledgement-loss descendant: %s: %w", output, err)
	}
	commitID := strings.TrimSpace(string(output))
	command = exec.Command("git", "--git-dir", fixture.remote, "update-ref", "refs/heads/factory-state", commitID, remoteHead)
	if output, err = command.CombinedOutput(); err != nil {
		return fmt.Errorf("advance test bare ref: %s: %w", output, err)
	}
	return nil
}

func TestGitPublishDoesNotPublishUnverifiedCandidate(t *testing.T) {
	fixture := newGitBackendFixture(t)
	backend := fixture.publishableBackend(&BlobReadCache{})
	parent, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	forged := GitCandidate{ParentHead: parent, CandidateID: parent, TreeID: parent, Snapshot: snapshot}
	if outcome, err := backend.Publish(context.Background(), forged); err == nil || outcome.Kind != "" {
		t.Fatalf("forged candidate was not rejected: %+v, %v", outcome, err)
	}
}

func TestGitPublishUnknownWhenBranchMovesOutsideCandidateParentChain(t *testing.T) {
	fixture := newGitBackendFixture(t)
	backend := fixture.publishableBackend(&BlobReadCache{})
	parent, snapshot, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next := cloneSnapshot(snapshot)
	next.Generation++
	next.Records["state/requests/request-1.json"] = Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"status":"candidate"}`)}
	candidate, err := backend.BuildCandidate(context.Background(), CandidateOptions{ParentHead: parent, Snapshot: next, Subject: "state(test): unknown ref movement"})
	if err != nil {
		t.Fatal(err)
	}
	orphan := createUnrelatedRootCommit(t, fixture.remote)
	moveBareRef(t, fixture.remote, "refs/heads/factory-state", orphan, fixture.head)
	backend.Runner = &failedPushGitRunner{countingGitRunner: fixture.runner}
	outcome, err := backend.Publish(context.Background(), candidate)
	if err == nil || outcome.Kind != PublicationUnknown {
		t.Fatalf("non-descendant ref movement = %+v, %v; want unknown", outcome, err)
	}
}

type failedPushGitRunner struct{ *countingGitRunner }

func (runner *failedPushGitRunner) Run(ctx context.Context, command GitCommand) (GitCommandResult, error) {
	if len(command.Arguments) > 0 && command.Arguments[0] == "push" {
		return GitCommandResult{}, &GitCommandError{Operation: "push", Category: GitFailureCommand, ExitCode: 1}
	}
	return runner.countingGitRunner.Run(ctx, command)
}

func createUnrelatedRootCommit(t *testing.T, bare string) string {
	t.Helper()
	object := exec.Command("git", "--git-dir", bare, "mktree")
	object.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
	tree, err := object.Output()
	if err != nil {
		t.Fatal(err)
	}
	commit := exec.Command("git", "--git-dir", bare, "commit-tree", strings.TrimSpace(string(tree)), "-m", "unrelated root")
	commit.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_AUTHOR_NAME=Factory Fixture", "GIT_AUTHOR_EMAIL=factory@example.invalid", "GIT_COMMITTER_NAME=Factory Fixture", "GIT_COMMITTER_EMAIL=factory@example.invalid", "GIT_AUTHOR_DATE=@1791455696 +0000", "GIT_COMMITTER_DATE=@1791455696 +0000")
	output, err := commit.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}

func moveBareRef(t *testing.T, bare, ref, next, expected string) {
	t.Helper()
	command := exec.Command("git", "--git-dir", bare, "update-ref", ref, next, expected)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("move bare ref: %s: %v", output, err)
	}
}
