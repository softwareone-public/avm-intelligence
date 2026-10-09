package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVerifiedGitBackendReadsFreshHeadAndReusesBlobObjects(t *testing.T) {
	fixture := newGitBackendFixture(t)
	backend := fixture.backend()
	firstHead, first, err := backend.Read(context.Background())
	if err != nil || firstHead != fixture.head || !snapshotsEqual(first, fixture.snapshot) {
		t.Fatalf("cold read = (%s, %#v, %v)", firstHead, first, err)
	}
	blobReads := fixture.runner.count("cat-file")
	if blobReads != 2 {
		t.Fatalf("cold Git blob batch commands = %d, want 2", blobReads)
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
	if fixture.runner.count("fetch") != 2 || fixture.runner.count("cat-file") != initialBlobReads+2 {
		t.Fatalf("changed read calls fetch/blob=%d/%d want 2/%d (new objects=%d)", fixture.runner.count("fetch"), fixture.runner.count("cat-file"), initialBlobReads+2, newObjectIDs)
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

func TestGitBlobBatchParsersValidateIdentitySizeAndFraming(t *testing.T) {
	content := []byte(`{"valid":true}`)
	objectID := gitBlobID(content)
	sizeResponse := fmt.Appendf(nil, "%s blob %d\n", objectID, len(content))
	sizes, err := parseBatchBlobSizes(sizeResponse, []string{objectID})
	if err != nil || sizes[objectID] != len(content) {
		t.Fatalf("parse valid size response = (%v, %v)", sizes, err)
	}
	validBatch := fmt.Appendf(nil, "%s blob %d\n", objectID, len(content))
	validBatch = append(validBatch, content...)
	validBatch = append(validBatch, '\n')
	parsed, err := parseBatchBlobContents(validBatch, []string{objectID}, sizes)
	if err != nil || !bytes.Equal(parsed[objectID], content) {
		t.Fatalf("parse valid batch response = (%q, %v)", parsed[objectID], err)
	}

	testCases := []struct {
		name     string
		sizeLine []byte
		batch    []byte
	}{
		{name: "wrong size identity", sizeLine: fmt.Appendf(nil, "%s blob %d\n", strings.Repeat("0", gitObjectIDLength), len(content))},
		{name: "oversized object", sizeLine: fmt.Appendf(nil, "%s blob %d\n", objectID, maxStateBlobBytes+1)},
		{name: "truncated body", batch: validBatch[:len(validBatch)-2]},
		{name: "missing body delimiter", batch: append(append([]byte(nil), validBatch[:len(validBatch)-1]...), 'x')},
		{name: "trailing data", batch: append(append([]byte(nil), validBatch...), '\n')},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.sizeLine != nil {
				if _, parseErr := parseBatchBlobSizes(testCase.sizeLine, []string{objectID}); parseErr == nil {
					t.Fatal("invalid size response was accepted")
				}
				return
			}
			if _, parseErr := parseBatchBlobContents(testCase.batch, []string{objectID}, sizes); parseErr == nil {
				t.Fatal("invalid batch response was accepted")
			}
		})
	}
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

func TestVerifiedGitBackendAcceptsOnlyConfiguredBranchFetchMappings(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		mapping string
		wantErr bool
	}{
		{name: "actions single branch", mapping: "+refs/heads/factory-state:refs/remotes/origin/factory-state"},
		{name: "actions branch prefix wildcard", mapping: "+refs/heads/factory-state*:refs/remotes/origin/factory-state*"},
		{name: "standard wildcard", mapping: "+refs/heads/*:refs/remotes/origin/*"},
		{name: "different branch", mapping: "+refs/heads/main:refs/remotes/origin/main", wantErr: true},
		{name: "different branch wildcard", mapping: "+refs/heads/main*:refs/remotes/origin/main*", wantErr: true},
		{name: "remote destination", mapping: "+refs/heads/factory-state:refs/remotes/attacker/factory-state", wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newGitBackendFixture(t)
			if err := runGitFixture(fixture.checkout, "config", "--local", "--replace-all", "remote.origin.fetch", testCase.mapping); err != nil {
				t.Fatal(err)
			}
			_, _, err := fixture.backend().Read(context.Background())
			if (err != nil) != testCase.wantErr {
				t.Fatalf("read error = %v, want error %t", err, testCase.wantErr)
			}
		})
	}
}

func TestVerifiedGitBackendAllowsOnlyConfiguredBranchCheckoutConfig(t *testing.T) {
	for _, testCase := range []struct {
		key     string
		value   string
		wantErr bool
	}{
		{key: "branch.factory-state.remote", value: "."},
		{key: "branch.factory-state.merge", value: "refs/heads/factory-state"},
		{key: "branch.main.remote", value: ".", wantErr: true},
		{key: "branch.factory-state.pushremote", value: "attacker", wantErr: true},
	} {
		t.Run(testCase.key, func(t *testing.T) {
			fixture := newGitBackendFixture(t)
			if err := runGitFixture(fixture.checkout, "config", "--local", testCase.key, testCase.value); err != nil {
				t.Fatal(err)
			}
			_, _, err := fixture.backend().Read(context.Background())
			if (err != nil) != testCase.wantErr {
				t.Fatalf("read error = %v, want error %t", err, testCase.wantErr)
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
	if len(command.Arguments) != 0 && (command.Arguments[0] != "cat-file" || len(command.Arguments) > 1 && (command.Arguments[1] == "--batch" || command.Arguments[1] == "--batch-check")) {
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

func (fixture *gitBackendFixture) refreshHead() {
	fixture.t.Helper()
	result, err := exec.Command("git", "-C", fixture.root, "rev-parse", "HEAD").Output()
	if err != nil {
		fixture.t.Fatal(err)
	}
	fixture.head = strings.TrimSpace(string(result))
}

func newGitBackendFixture(t *testing.T) *gitBackendFixture {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	runGitFixtureMust(t, base, "init", "--bare", "--initial-branch=factory-state", remote)
	root := filepath.Join(base, "working")
	runGitFixtureMust(t, base, "clone", remote, root)
	runGitFixtureMust(t, root, "checkout", "-b", "factory-state")
	snapshot := NewSnapshot()
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

func (fixture *gitBackendFixture) publishableBackend(cache *BlobReadCache) *VerifiedGitBackend {
	backend := fixture.backend()
	backend.BlobCache = cache
	return backend
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

func BenchmarkVerifiedGitStateSnapshot(b *testing.B) {
	for _, fileCount := range []int{1336, 10000} {
		b.Run(fmt.Sprintf("files-%d", fileCount), func(b *testing.B) {
			snapshot := benchmarkStateSnapshot(b, fileCount)
			files, err := encodeSnapshotFiles(snapshot)
			if err != nil {
				b.Fatal(err)
			}
			paths := make([]string, 0, len(files))
			for path := range files {
				paths = append(paths, path)
			}
			sort.Strings(paths)
			fixtureBytes := 0
			for _, path := range paths {
				fixtureBytes += len(path) + len(files[path])
			}
			b.ReportMetric(float64(fixtureBytes), "fixture-B")
			b.ReportMetric(float64(len(snapshot.Records)), "records/op")
			b.ReportMetric(float64(len(files)), "files/op")
			fixture := newGitBenchmarkFixture(b, files, snapshot)

			b.Run("decode-validate", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(fixtureBytes))
				for iteration := 0; iteration < b.N; iteration++ {
					decoded, err := decodeSnapshotFiles(files)
					if err != nil || decoded.Generation != snapshot.Generation || len(decoded.Records) != len(snapshot.Records) {
						b.Fatalf("decode snapshot: generation=%d records=%d error=%v", decoded.Generation, len(decoded.Records), err)
					}
				}
			})

			b.Run("cold-read", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(fixture.fixtureBytes))
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					b.StopTimer()
					checkout := filepath.Join(b.TempDir(), "checkout")
					if err := os.MkdirAll(checkout, 0700); err != nil {
						b.Fatal(err)
					}
					benchmarkGit(b, checkout, "init", "--quiet", "--initial-branch=factory-state")
					benchmarkGit(b, checkout, "remote", "add", "origin", "https://github.com/owner/repository.git")
					backend := fixture.backendAt(checkout)
					b.StartTimer()
					if _, decoded, err := backend.Read(context.Background()); err != nil || len(decoded.Records) != len(snapshot.Records) {
						b.Fatalf("cold verified read: records=%d error=%v", len(decoded.Records), err)
					}
					b.StopTimer()
				}
			})

			b.Run("warm-read", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(fixture.fixtureBytes))
				backend := fixture.backend()
				if _, _, err := backend.Read(context.Background()); err != nil {
					b.Fatal(err)
				}
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					if _, decoded, err := backend.Read(context.Background()); err != nil || len(decoded.Records) != len(snapshot.Records) {
						b.Fatalf("warm verified read: records=%d error=%v", len(decoded.Records), err)
					}
				}
			})

			b.Run("single-record-update-candidate", func(b *testing.B) {
				backend := fixture.backend()
				_, base, err := backend.Read(context.Background())
				if err != nil {
					b.Fatal(err)
				}
				updateIndex := 0
				b.ReportAllocs()
				b.ResetTimer()
				for iteration := 0; iteration < b.N; iteration++ {
					updated := cloneSnapshot(base)
					moduleID := fmt.Sprintf("avm-res-benchmark-module-%05d", updateIndex%len(updated.Records))
					path := fmt.Sprintf("state/modules/%s.json", moduleID)
					updated.Records[path] = Record{Kind: "module", SchemaVersion: 1, Data: json.RawMessage(fmt.Sprintf(`{"module_id":%q,"active":true,"revision":%d}`, moduleID, iteration+1))}
					parent, _, err := backend.Read(context.Background())
					if err != nil {
						b.Fatal(err)
					}
					candidate, err := backend.BuildCandidate(context.Background(), CandidateOptions{ParentHead: parent, Snapshot: updated, Subject: "state(test): benchmark module update"})
					if err != nil {
						b.Fatal(err)
					}
					if candidate.CandidateID == "" {
						b.Fatal("candidate was not constructed")
					}
					updateIndex++
				}
			})

			b.Run("publish", func(b *testing.B) {
				b.ReportAllocs()
				for iteration := 0; iteration < b.N; iteration++ {
					b.StopTimer()
					fixture := newGitBenchmarkFixture(b, files, snapshot)
					backend := fixture.backend()
					parent, base, err := backend.Read(context.Background())
					if err != nil {
						b.Fatal(err)
					}
					updated := cloneSnapshot(base)
					updated.Generation++
					moduleID := "avm-res-benchmark-module-00000"
					path := fmt.Sprintf("state/modules/%s.json", moduleID)
					updated.Records[path] = Record{Kind: "module", SchemaVersion: 1, Data: json.RawMessage(fmt.Sprintf(`{"module_id":%q,"active":true,"revision":%d}`, moduleID, iteration+1))}
					candidate, err := backend.BuildCandidate(context.Background(), CandidateOptions{ParentHead: parent, Snapshot: updated, Subject: "state(test): benchmark publication"})
					if err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
					outcome, err := backend.Publish(context.Background(), candidate)
					b.StopTimer()
					if err != nil || outcome.Kind != PublicationCommitted {
						b.Fatalf("publish outcome=%+v error=%v", outcome, err)
					}
					b.StartTimer()
				}
				b.StopTimer()
			})

			b.Run("one-conflict-recompute", func(b *testing.B) {
				b.ReportAllocs()
				for iteration := 0; iteration < b.N; iteration++ {
					fixture := newGitBenchmarkFixture(b, files, snapshot)
					backend := &benchmarkContentionBackend{backend: fixture.backend(), fixture: fixture, forceConflict: true}
					coordinator := DecisionCoordinator{Backend: backend, Timing: TransactionTiming{Sleep: func(context.Context, time.Duration) error { return nil }, Jitter: func(time.Duration) time.Duration { return 0 }}}
					decisionCount := 0
					_, err := coordinator.RunTransaction(context.Background(), "benchmark-conflict", func(current Snapshot) (TransactionDecision, error) {
						decisionCount++
						b.ReportMetric(float64(decisionCount), "decisions/op")
						decision, err := benchmarkModuleDecision(current, "benchmark-conflict", 1)
						if err == nil {
							decision.Events[0].ID = "benchmark-conflict"
						}
						return decision, err
					}, func(current Snapshot) (TransactionResolution, error) {
						if _, found := current.AppliedEvents["benchmark-conflict"]; found {
							return TransactionResolution{Found: true, Value: true}, nil
						}
						return TransactionResolution{}, nil
					})
					if err != nil || decisionCount != 2 || backend.rejections != 1 || backend.lastCandidate.CandidateID == "" {
						b.Fatalf("conflict recompute decisions=%d rejected=%d candidate=%s error=%v", decisionCount, backend.rejections, backend.lastCandidate.CandidateID, err)
					}
				}
			})

			b.Run("three-attempt-exhaustion", func(b *testing.B) {
				b.ReportAllocs()
				for iteration := 0; iteration < b.N; iteration++ {
					fixture := newGitBenchmarkFixture(b, files, snapshot)
					backend := &benchmarkContentionBackend{backend: fixture.backend(), alwaysReject: true}
					coordinator := DecisionCoordinator{Backend: backend, Timing: TransactionTiming{Sleep: func(context.Context, time.Duration) error { return nil }, Jitter: func(time.Duration) time.Duration { return 0 }}}
					decisions := 0
					_, err := coordinator.RunTransaction(context.Background(), "benchmark-exhaustion", func(current Snapshot) (TransactionDecision, error) {
						decisions++
						return benchmarkModuleDecision(current, "benchmark-exhaustion", decisions)
					}, func(Snapshot) (TransactionResolution, error) { return TransactionResolution{}, nil })
					if !errors.Is(err, ErrTransactionConflict) || decisions != transactionAttempts {
						b.Fatalf("exhaustion decisions=%d error=%v", decisions, err)
					}
				}
			})
		})
	}
}

func benchmarkStateSnapshot(b *testing.B, recordCount int) Snapshot {
	b.Helper()
	snapshot := NewSnapshot()
	for recordIndex := 0; recordIndex < recordCount; recordIndex++ {
		moduleID := fmt.Sprintf("avm-res-benchmark-module-%05d", recordIndex)
		data, err := json.Marshal(map[string]any{
			"module_id": moduleID,
			"active":    true,
			"name":      strings.Repeat("benchmark-state-record-", 4),
			"metadata": map[string]string{
				"purpose": "TEST-015 synthetic state decode and validation",
				"owner":   "Ledermayer",
			},
		})
		if err != nil {
			b.Fatal(err)
		}
		path := fmt.Sprintf("state/modules/%s.json", moduleID)
		snapshot.Records[path] = Record{Kind: "module", SchemaVersion: 1, Data: data}
	}
	snapshot.Generation = uint64(recordCount)
	return snapshot
}

type gitBenchmarkFixture struct {
	checkout, working, remote, benchmarkRemote, gitPath string
	files                                               map[string][]byte
	snapshot                                            Snapshot
	fixtureBytes                                        int
}

func newGitBenchmarkFixture(b *testing.B, files map[string][]byte, snapshot Snapshot) *gitBenchmarkFixture {
	b.Helper()
	root := b.TempDir()
	remote := filepath.Join(root, "remote.git")
	working := filepath.Join(root, "working")
	checkout := filepath.Join(root, "checkout")
	if err := os.MkdirAll(remote, 0700); err != nil {
		b.Fatal(err)
	}
	if err := exec.Command("git", "init", "--bare", "--initial-branch=factory-state", remote).Run(); err != nil {
		b.Fatalf("initialize benchmark remote: %v", err)
	}
	if err := exec.Command("git", "init", "--quiet", "--initial-branch=factory-state", working).Run(); err != nil {
		b.Fatalf("initialize benchmark checkout: %v", err)
	}
	writeBenchmarkStateFiles(b, working, files)
	benchmarkGit(b, working, "add", "state")
	benchmarkGit(b, working, "-c", "user.name=Benchmark", "-c", "user.email=benchmark@example.invalid", "commit", "--quiet", "-m", "seed state")
	benchmarkGit(b, working, "remote", "add", "origin", remote)
	benchmarkGit(b, working, "push", "--quiet", "origin", "HEAD:refs/heads/factory-state")
	if err := exec.Command("git", "init", "--quiet", "--initial-branch=factory-state", checkout).Run(); err != nil {
		b.Fatalf("initialize benchmark checkout: %v", err)
	}
	benchmarkGit(b, checkout, "remote", "add", "origin", "https://github.com/owner/repository.git")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		b.Skip("Git executable is not installed")
	}
	gitPath, err = filepath.Abs(gitPath)
	if err != nil {
		b.Fatal(err)
	}
	return &gitBenchmarkFixture{checkout: checkout, working: working, remote: remote, benchmarkRemote: "file://" + remote, gitPath: gitPath, files: files, snapshot: snapshot, fixtureBytes: benchmarkFilesSize(files)}
}

func (fixture *gitBenchmarkFixture) backend() *VerifiedGitBackend {
	runner := SafeGitCommandRunner{Executable: fixture.gitPath, testLocalRemote: true}
	return &VerifiedGitBackend{
		Config:      GitBackendConfig{Owner: "owner", Repository: "repository", Branch: "factory-state", CheckoutRoot: fixture.checkout, Host: "github.com", CredentialHost: "github.com"},
		Runner:      benchmarkGitRunner{SafeGitCommandRunner: runner, remote: fixture.benchmarkRemote},
		Credentials: FileStateCredentialProvider{Token: func(context.Context, string, string) (string, error) { return "fixture-token", nil }},
		BlobCache:   &BlobReadCache{}, testRemoteURL: fixture.benchmarkRemote,
	}
}

func (fixture *gitBenchmarkFixture) backendAt(checkout string) *VerifiedGitBackend {
	backend := fixture.backend()
	backend.Config.CheckoutRoot = checkout
	return backend
}

type benchmarkGitRunner struct {
	SafeGitCommandRunner
	remote string
}

func (runner benchmarkGitRunner) Run(ctx context.Context, command GitCommand) (GitCommandResult, error) {
	for index, argument := range command.Arguments {
		if argument == "https://github.com/owner/repository.git" {
			command.Arguments[index] = runner.remote
		}
	}
	return runner.SafeGitCommandRunner.Run(ctx, command)
}

func (fixture *gitBenchmarkFixture) commitSnapshot(snapshot Snapshot) {
	files, err := encodeSnapshotFiles(snapshot)
	if err != nil {
		panic(err)
	}
	writeBenchmarkStateFilesNoFatal(fixture.working, files)
	benchmarkGitNoTesting(fixture.working, "add", "-A", "state")
	benchmarkGitNoTesting(fixture.working, "-c", "user.name=Benchmark", "-c", "user.email=benchmark@example.invalid", "commit", "--allow-empty", "--quiet", "-m", "advance benchmark state")
	benchmarkGitNoTesting(fixture.working, "-c", "url."+fixture.remote+".insteadOf=https://github.com/owner/repository.git", "push", "--quiet", "origin", "HEAD:refs/heads/factory-state")
}

func writeBenchmarkStateFilesNoFatal(root string, files map[string][]byte) {
	for name, content := range files {
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			panic(err)
		}
		if err := os.WriteFile(filename, content, 0600); err != nil {
			panic(err)
		}
	}
}

func benchmarkGitNoTesting(directory string, arguments ...string) {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
	if output, err := command.CombinedOutput(); err != nil {
		panic(fmt.Errorf("benchmark fixture Git failed: %s: %w", strings.TrimSpace(string(output)), err))
	}
}

func benchmarkFilesSize(files map[string][]byte) int {
	size := 0
	for name, content := range files {
		size += len(name) + len(content)
	}
	return size
}

func writeBenchmarkStateFiles(b *testing.B, root string, files map[string][]byte) {
	b.Helper()
	for name, content := range files {
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filename, content, 0600); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkGit(b *testing.B, directory string, arguments ...string) {
	b.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1")
	if output, err := command.CombinedOutput(); err != nil {
		b.Fatalf("Git benchmark fixture setup failed: %s: %v", strings.TrimSpace(string(output)), err)
	}
}

type benchmarkContentionBackend struct {
	backend       *VerifiedGitBackend
	fixture       *gitBenchmarkFixture
	conflicted    bool
	alwaysReject  bool
	forceConflict bool
	rejections    int
	lastCandidate GitCandidate
}

func (backend *benchmarkContentionBackend) Read(ctx context.Context) (string, Snapshot, error) {
	return backend.backend.Read(ctx)
}

func (backend *benchmarkContentionBackend) BuildCandidate(ctx context.Context, options CandidateOptions) (GitCandidate, error) {
	return backend.backend.BuildCandidate(ctx, options)
}

func (backend *benchmarkContentionBackend) Publish(ctx context.Context, candidate GitCandidate) (PublicationOutcome, error) {
	backend.lastCandidate = candidate
	if backend.alwaysReject {
		backend.rejections++
		return PublicationOutcome{Kind: PublicationRejected, CandidateID: candidate.CandidateID}, &GitCommandError{Operation: "push", Category: GitFailureContention}
	}
	if !backend.conflicted && backend.forceConflict {
		backend.conflicted = true
		backend.rejections++
		_, parent, err := backend.backend.Read(ctx)
		if err != nil {
			return PublicationOutcome{}, err
		}
		parent = cloneSnapshot(parent)
		moduleID := "avm-res-benchmark-module-00001"
		path := fmt.Sprintf("state/modules/%s.json", moduleID)
		parent.Records[path] = Record{Kind: "module", SchemaVersion: 1, Data: json.RawMessage(fmt.Sprintf(`{"module_id":%q,"active":true,"revision":-1}`, moduleID))}
		backend.fixture.commitSnapshot(parent)
		return PublicationOutcome{Kind: PublicationRejected, CandidateID: candidate.CandidateID}, &GitCommandError{Operation: "push", Category: GitFailureContention}
	}
	return backend.backend.Publish(ctx, candidate)
}

func benchmarkModuleDecision(snapshot Snapshot, operationID string, revision int) (TransactionDecision, error) {
	moduleID := "avm-res-benchmark-module-00000"
	path := fmt.Sprintf("state/modules/%s.json", moduleID)
	data, err := json.Marshal(map[string]any{"module_id": moduleID, "active": true, "revision": revision})
	if err != nil {
		return TransactionDecision{}, err
	}
	event := Event{ID: operationID, RecordPath: path, Record: Record{Kind: "module", SchemaVersion: 1, Data: data}}
	return TransactionDecision{Events: []Event{event}, Result: revision, Candidate: CandidateOptions{Subject: "state(test): benchmark module decision", Provenance: GitProvenance{Operation: "discovery.persist", Event: operationID, Module: moduleID}}}, nil
}
