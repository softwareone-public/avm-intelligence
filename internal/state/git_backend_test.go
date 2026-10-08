package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestVerifiedGitBackendReadsFreshHeadAndReusesBlobObjects(t *testing.T) {
	fixture := newGitBackendFixture(t)
	backend := fixture.backend()
	firstHead, first, err := backend.Read(context.Background())
	if err != nil || firstHead != fixture.head || !snapshotsEqual(first, fixture.snapshot) {
		t.Fatalf("cold read = (%s, %#v, %v)", firstHead, first, err)
	}
	blobReads := fixture.runner.count("cat-file")
	if blobReads != len(fixture.files) {
		t.Fatalf("cold blob reads = %d, want %d", blobReads, len(fixture.files))
	}
	first.Records["state/requests/request-1.json"] = Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"status":"mutated"}`)}
	secondHead, second, err := backend.Read(context.Background())
	if err != nil || secondHead != firstHead || !snapshotsEqual(second, fixture.snapshot) {
		t.Fatalf("warm read was stale or aliased: head=%s error=%v", secondHead, err)
	}
	if fixture.runner.count("fetch") != 2 || fixture.runner.count("cat-file") != blobReads {
		t.Fatalf("warm read calls fetch/blob=%d/%d, want 2/%d", fixture.runner.count("fetch"), fixture.runner.count("cat-file"), blobReads)
	}
}

func TestVerifiedGitBackendObservesChangedHeadAndLoadsOnlyMissingBlob(t *testing.T) {
	fixture := newGitBackendFixture(t)
	backend := fixture.backend()
	firstHead, first, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	initialBlobReads := fixture.runner.count("cat-file")
	previousFiles := fixture.files
	changed := cloneSnapshot(first)
	changed.Generation++
	changed.Records["state/requests/request-1.json"] = Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"status":"changed"}`)}
	fixture.commitSnapshot(changed)
	secondHead, second, err := backend.Read(context.Background())
	if err != nil || secondHead == firstHead || !snapshotsEqual(second, changed) {
		t.Fatalf("changed read = (%s, %#v, %v)", secondHead, second, err)
	}
	updatedFiles, err := encodeSnapshotFiles(changed)
	if err != nil {
		t.Fatal(err)
	}
	newObjectIDs := countNewBlobIDs(previousFiles, updatedFiles)
	if fixture.runner.count("fetch") != 2 || fixture.runner.count("cat-file") != initialBlobReads+newObjectIDs {
		t.Fatalf("changed read calls fetch/blob=%d/%d want 2/%d (new objects=%d)", fixture.runner.count("fetch"), fixture.runner.count("cat-file"), initialBlobReads+newObjectIDs, newObjectIDs)
	}
}

func countNewBlobIDs(previous, next map[string][]byte) int {
	previousIDs := map[string]bool{}
	for _, content := range previous {
		previousIDs[gitBlobID(content)] = true
	}
	newIDs := map[string]bool{}
	for _, content := range next {
		objectID := gitBlobID(content)
		if !previousIDs[objectID] {
			newIDs[objectID] = true
		}
	}
	return len(newIDs)
}

func TestVerifiedGitBackendFailsClosedOnFetchFailureAndUnsafeConfig(t *testing.T) {
	t.Run("fetch failure", func(t *testing.T) {
		fixture := newGitBackendFixture(t)
		backend := fixture.backend()
		if _, _, err := backend.Read(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := runGitFixture(fixture.remote, "update-ref", "-d", "refs/heads/factory-state"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := backend.Read(context.Background()); err == nil {
			t.Fatal("reader used old checkout objects after remote fetch failed")
		}
	})
	for _, setting := range []string{
		"url.insteadOf",
		"include.path",
		"remote.origin.url",
	} {
		t.Run(setting, func(t *testing.T) {
			fixture := newGitBackendFixture(t)
			if err := runGitFixture(fixture.checkout, "config", "--local", setting, "https://attacker.invalid/owner/repository.git"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := fixture.backend().Read(context.Background()); err == nil {
				t.Fatalf("unsafe checkout config was accepted: %s", setting)
			}
		})
	}
}

func TestVerifiedGitBackendRejectsUnsafeStateTreeEntries(t *testing.T) {
	for _, test := range []struct {
		name   string
		mode   string
		typeID string
		path   string
	}{
		{name: "symlink", mode: "120000", typeID: "blob", path: "state/requests/request-1.json"},
		{name: "executable", mode: "100755", typeID: "blob", path: "state/requests/request-1.json"},
		{name: "tree escape", mode: "100644", typeID: "blob", path: "../workflow.yml"},
		{name: "non-state path", mode: "100644", typeID: "blob", path: "workflow.yml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitBackendFixture(t)
			backend := fixture.backend()
			originalRunner := backend.Runner
			backend.Runner = &listingOverrideRunner{
				GitCommandRunner: originalRunner,
				path:             test.path,
				mode:             test.mode,
				typeID:           test.typeID,
			}
			if _, _, err := backend.Read(context.Background()); err == nil {
				t.Fatal("unsafe state tree entry was accepted")
			}
		})
	}
}

type listingOverrideRunner struct {
	GitCommandRunner
	path   string
	mode   string
	typeID string
}

func (runner *listingOverrideRunner) Run(ctx context.Context, command GitCommand) (GitCommandResult, error) {
	result, err := runner.GitCommandRunner.Run(ctx, command)
	if err == nil && len(command.Arguments) > 0 && command.Arguments[0] == "ls-tree" {
		entry := fmt.Sprintf("%s %s %s\t%s\x00", runner.mode, runner.typeID, strings.Repeat("0", gitObjectIDLength), runner.path)
		result.Stdout = []byte(entry)
	}
	return result, err
}

type countingGitRunner struct {
	SafeGitCommandRunner
	counts map[string]int
	mu     sync.Mutex
}

func (runner *countingGitRunner) Run(ctx context.Context, command GitCommand) (GitCommandResult, error) {
	runner.mu.Lock()
	if len(command.Arguments) != 0 && (command.Arguments[0] != "cat-file" || len(command.Arguments) > 1 && command.Arguments[1] == "blob") {
		runner.counts[command.Arguments[0]]++
	}
	runner.mu.Unlock()
	return runner.SafeGitCommandRunner.Run(ctx, command)
}

func (runner *countingGitRunner) count(subcommand string) int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.counts[subcommand]
}

func (runner *countingGitRunner) allowLocalRemoteForTest() bool {
	return runner.SafeGitCommandRunner.allowLocalRemoteForTest()
}

func (runner *countingGitRunner) trustedExecutable() string {
	return runner.SafeGitCommandRunner.trustedExecutable()
}

func (runner *countingGitRunner) RunWithConfig(ctx context.Context, command GitCommand) (GitCommandResult, error) {
	return runner.Run(ctx, command)
}

type gitBackendFixture struct {
	t        *testing.T
	base     string
	root     string
	checkout string
	remote   string
	head     string
	snapshot Snapshot
	files    map[string][]byte
	runner   *countingGitRunner
}

func newGitBackendFixture(t *testing.T) *gitBackendFixture {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	runGitFixtureMust(t, base, "init", "--bare", "--initial-branch=factory-state", remote)
	root := filepath.Join(base, "working")
	runGitFixtureMust(t, base, "clone", remote, root)
	runGitFixtureMust(t, root, "checkout", "-b", "factory-state")
	snapshot := snapshotWithRequest(t)
	files, err := encodeSnapshotFiles(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	writeStateFiles(t, root, files)
	runGitFixtureMust(t, root, "add", "state")
	runGitFixtureMust(t, root, "-c", "user.name=Factory Fixture", "-c", "user.email=factory@example.invalid", "commit", "-m", "seed state")
	runGitFixtureMust(t, root, "push", "origin", "HEAD:refs/heads/factory-state")
	checkout := filepath.Join(base, "checkout")
	runGitFixtureMust(t, base, "clone", "--no-checkout", remote, checkout)
	runGitFixtureMust(t, checkout, "remote", "set-url", "origin", "https://github.com/owner/repository.git")
	headBytes, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git executable is not installed")
	}
	git, err = filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}
	runner := &countingGitRunner{SafeGitCommandRunner: SafeGitCommandRunner{Executable: git, testLocalRemote: true}, counts: map[string]int{}}
	return &gitBackendFixture{t: t, base: base, root: root, checkout: checkout, remote: remote, head: strings.TrimSpace(string(headBytes)), snapshot: snapshot, files: files, runner: runner}
}

func (fixture *gitBackendFixture) backend() *VerifiedGitBackend {
	return &VerifiedGitBackend{
		Config: GitBackendConfig{Owner: "owner", Repository: "repository", Branch: "factory-state", CheckoutRoot: fixture.checkout, Host: "github.com", CredentialHost: "github.com"},
		Runner: fixture.runner,
		Credentials: FileStateCredentialProvider{Token: func(context.Context, string, string) (string, error) {
			return "fixture-token", nil
		}},
		BlobCache:     &BlobReadCache{},
		testRemoteURL: "file://" + fixture.remote,
	}
}

func (fixture *gitBackendFixture) commitSnapshot(snapshot Snapshot) {
	fixture.t.Helper()
	files, err := encodeSnapshotFiles(snapshot)
	if err != nil {
		fixture.t.Fatal(err)
	}
	writeStateFiles(fixture.t, fixture.root, files)
	runGitFixtureMust(fixture.t, fixture.root, "add", "-A", "state")
	runGitFixtureMust(fixture.t, fixture.root, "-c", "user.name=Factory Fixture", "-c", "user.email=factory@example.invalid", "commit", "--allow-empty", "-m", "update state")
	runGitFixtureMust(fixture.t, fixture.root, "push", "origin", "HEAD:refs/heads/factory-state")
	fixture.snapshot, fixture.files = snapshot, files
	result, err := exec.Command("git", "-C", fixture.root, "rev-parse", "HEAD").Output()
	if err != nil {
		fixture.t.Fatal(err)
	}
	fixture.head = strings.TrimSpace(string(result))
}

func writeStateFiles(t *testing.T, root string, files map[string][]byte) {
	t.Helper()
	for name, content := range files {
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func runGitFixtureMust(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	if err := runGitFixture(directory, arguments...); err != nil {
		t.Fatal(err)
	}
}

func runGitFixture(directory string, arguments ...string) error {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fixture command failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}
