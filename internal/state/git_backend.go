package state

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

const (
	gitObjectIDLength   = 40
	gitFetchDepth       = 1
	maxTreeListingBytes = 16 << 20
	maxStateBlobBytes   = 16 << 20
)

const (
	remoteConfigPrefix   = "remote."
	remoteURLSuffix      = ".url"
	remoteFetchSuffix    = ".fetch"
	gitSubcommandCatFile = "cat-file"
	gitModeRegularBlob   = "100644"
	gitUnmergedStage     = "0"
	gitStateDirPrefix    = "state/"
)

var gitObjectIDPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var provenanceOperationAllowlist = map[string]bool{
	"discovery.persist": true, "dispatch.claim": true, "worker.seal": true,
	"projection.update": true, "evidence.accept": true, "selection.export": true,
	"deployment.prepare": true, "deployment.receipt": true,
}

// GitBackendConfig identifies the only production state repository and ref.
type GitBackendConfig struct {
	Owner          string
	Repository     string
	Branch         string
	CheckoutRoot   string
	Host           string
	CredentialHost string
}

// VerifiedGitBackend fetches and reads one configured branch through Git.
// The checkout supplies only a job-local object database; worktree files are
// never read as state data.
type VerifiedGitBackend struct {
	Config      GitBackendConfig
	Runner      GitCommandRunner
	Credentials StateCredentialProvider
	BlobCache   *BlobReadCache

	mu              sync.Mutex
	configured      bool
	pendingHead     string
	pendingSnapshot Snapshot
	testRemoteURL   string
}

// GitProvenance provides reviewed, bounded commit trailers. Fields omitted
// from the caller are left absent rather than guessed from ambient state.
type GitProvenance struct {
	Operation  string
	Repository string
	Workflow   string
	Run        string
	RunAttempt string
	Job        string
	Event      string
	Module     string
	Request    string
	Attempt    string
	Fence      string
}

// CandidateOptions contains the exact verified parent, final logical snapshot,
// commit subject and allowlisted provenance for one state mutation.
type CandidateOptions struct {
	ParentHead string
	Snapshot   Snapshot
	Subject    string
	Provenance GitProvenance
	Now        time.Time
}

// BuildCandidate creates and verifies a one-parent commit object without
// publishing it. It reads only the verified parent tree and explicit snapshot.
func (backend *VerifiedGitBackend) BuildCandidate(ctx context.Context, options CandidateOptions) (GitCandidate, error) {
	if backend == nil {
		return GitCandidate{}, fmt.Errorf("verified Git backend is required")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if err := backend.validate(); err != nil {
		return GitCandidate{}, err
	}
	if !gitObjectIDPattern.MatchString(options.ParentHead) {
		return GitCandidate{}, fmt.Errorf("candidate parent must be a verified commit ID")
	}
	if err := options.Snapshot.Validate(); err != nil {
		return GitCandidate{}, fmt.Errorf("validate state candidate: %w", err)
	}
	if err := validateCandidateProvenance(options.Subject, options.Provenance); err != nil {
		return GitCandidate{}, err
	}
	files, err := encodeSnapshotFiles(options.Snapshot)
	if err != nil {
		return GitCandidate{}, fmt.Errorf("encode candidate snapshot: %w", err)
	}
	if len(files) == 0 {
		return GitCandidate{}, fmt.Errorf("candidate snapshot has no state files")
	}
	return backend.createCandidate(ctx, options, files)
}

func (backend *VerifiedGitBackend) createCandidate(ctx context.Context, options CandidateOptions, files map[string][]byte) (GitCandidate, error) {
	indexDirectory, err := os.MkdirTemp(backend.Config.CheckoutRoot, "avm-state-index-")
	if err != nil {
		return GitCandidate{}, fmt.Errorf("create private candidate index directory")
	}
	defer os.RemoveAll(indexDirectory)
	if err = os.Chmod(indexDirectory, 0700); err != nil {
		return GitCandidate{}, fmt.Errorf("protect private candidate index directory")
	}
	indexPath := filepath.Join(indexDirectory, "index")
	if _, err = backend.commandWithIndex(ctx, []string{"read-tree", options.ParentHead}, indexPath, nil); err != nil {
		return GitCandidate{}, fmt.Errorf("seed private candidate index: %w", err)
	}
	if err = backend.stageSnapshotDelta(ctx, files, indexPath); err != nil {
		return GitCandidate{}, err
	}
	tree, err := backend.commandWithIndex(ctx, []string{"write-tree"}, indexPath, nil)
	if err != nil {
		return GitCandidate{}, fmt.Errorf("write candidate tree: %w", err)
	}
	treeID := strings.TrimSpace(string(tree.Stdout))
	if !gitObjectIDPattern.MatchString(treeID) {
		return GitCandidate{}, fmt.Errorf("candidate tree object ID is invalid")
	}
	parentTree, err := backend.commitTreeID(ctx, options.ParentHead)
	if err != nil {
		return GitCandidate{}, err
	}
	if treeID == parentTree {
		return GitCandidate{ParentHead: options.ParentHead, TreeID: treeID, Snapshot: cloneSnapshot(options.Snapshot)}, nil
	}
	message := formatCandidateMessage(options.Subject, options.Provenance, options.ParentHead)
	commitEnvironment := candidateCommitEnvironment(options.Now)
	commit, err := backend.command(ctx, []string{"commit-tree", treeID, "-p", options.ParentHead, "-F", "-"}, commitEnvironment, []byte(message))
	if err != nil {
		return GitCandidate{}, fmt.Errorf("create candidate commit: %w", err)
	}
	commitID := strings.TrimSpace(string(commit.Stdout))
	if !gitObjectIDPattern.MatchString(commitID) {
		return GitCandidate{}, fmt.Errorf("candidate commit object ID is invalid")
	}
	verifiedTree, err := backend.commitTreeID(ctx, commitID)
	if err != nil || verifiedTree != treeID {
		return GitCandidate{}, fmt.Errorf("candidate commit tree verification failed")
	}
	verifiedCommit, err := backend.command(ctx, []string{gitSubcommandCatFile, "-p", commitID}, nil)
	if err != nil {
		return GitCandidate{}, fmt.Errorf("read candidate commit object: %w", err)
	}
	if !validSingleParentCommit(verifiedCommit.Stdout, treeID, options.ParentHead) {
		return GitCandidate{}, fmt.Errorf("candidate commit parent verification failed")
	}
	return GitCandidate{ParentHead: options.ParentHead, CandidateID: commitID, TreeID: treeID, Snapshot: cloneSnapshot(options.Snapshot)}, nil
}

func candidateCommitEnvironment(now time.Time) map[string]string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	date := now.UTC().Format("Mon Jan _2 15:04:05 2006 -0700")
	return map[string]string{"GIT_AUTHOR_DATE": date, "GIT_COMMITTER_DATE": date}
}

func (backend *VerifiedGitBackend) commandWithIndex(ctx context.Context, arguments []string, indexPath string, input []byte) (GitCommandResult, error) {
	if backend.Runner == nil {
		return GitCommandResult{}, fmt.Errorf("Git command runner is required")
	}
	arguments = append([]string{gitIndexFilePrefix + indexPath}, arguments...)
	command := GitCommand{Directory: backend.Config.CheckoutRoot, Arguments: arguments, Input: input, allowPrivateIndex: true}
	if runner, ok := backend.Runner.(interface{ trustedExecutable() string }); ok {
		command.Executable = runner.trustedExecutable()
	}
	return backend.Runner.Run(ctx, command)
}

func (backend *VerifiedGitBackend) stageSnapshotDelta(ctx context.Context, files map[string][]byte, indexPath string) error {
	parentFiles, err := backend.readParentFiles(ctx, indexPath)
	if err != nil {
		return err
	}
	for filename, content := range files {
		if err := validateSnapshotFile(filename, content); err != nil {
			return err
		}
		parent, exists := parentFiles[filename]
		if exists && bytes.Equal(parent, content) {
			continue
		}
		if _, err := backend.commandWithIndex(ctx, []string{"hash-object", "-w", "--stdin"}, indexPath, content); err != nil {
			return fmt.Errorf("write candidate state blob: %w", err)
		}
		objectID := gitBlobID(content)
		if !gitObjectIDPattern.MatchString(objectID) {
			return fmt.Errorf("candidate blob object ID is invalid")
		}
		if _, err := backend.commandWithIndex(ctx, []string{"update-index", "--add", "--cacheinfo", gitModeRegularBlob, objectID, filename}, indexPath, nil); err != nil {
			return fmt.Errorf("stage candidate state path: %w", err)
		}
	}
	for filename := range parentFiles {
		if _, exists := files[filename]; exists {
			continue
		}
		if _, err := backend.commandWithIndex(ctx, []string{"update-index", "--force-remove", "--", filename}, indexPath, nil); err != nil {
			return fmt.Errorf("stage authorized state deletion: %w", err)
		}
	}
	return nil
}

func (backend *VerifiedGitBackend) readParentFiles(ctx context.Context, indexPath string) (map[string][]byte, error) {
	listing, err := backend.commandWithIndex(ctx, []string{"ls-files", "--stage", "-z"}, indexPath, nil)
	if err != nil {
		return nil, fmt.Errorf("list candidate parent paths: %w", err)
	}
	if len(listing.Stdout) > maxTreeListingBytes || len(listing.Stdout) > 0 && listing.Stdout[len(listing.Stdout)-1] != 0 {
		return nil, fmt.Errorf("candidate parent index listing is invalid")
	}
	files := map[string][]byte{}
	for _, record := range bytes.Split(bytes.TrimSuffix(listing.Stdout, []byte{0}), []byte{0}) {
		if len(record) == 0 {
			continue
		}
		metadata, filenameBytes, found := bytes.Cut(record, []byte{'\t'})
		fields := strings.Fields(string(metadata))
		filename := string(filenameBytes)
		if !found || len(fields) != 3 || fields[0] != gitModeRegularBlob || fields[2] != gitUnmergedStage || !strings.HasPrefix(filename, gitStateDirPrefix) || filepath.Ext(filename) != ".json" {
			return nil, fmt.Errorf("candidate parent index contains an unsupported entry")
		}
		if err := identity.ValidateRelativePath(filename, false); err != nil {
			return nil, fmt.Errorf("candidate parent path is invalid")
		}
		result, readErr := backend.command(ctx, []string{gitSubcommandCatFile, "blob", fields[1]}, nil)
		if readErr != nil {
			return nil, fmt.Errorf("read candidate parent blob: %w", readErr)
		}
		if gitBlobID(result.Stdout) != fields[1] {
			return nil, fmt.Errorf("candidate parent blob object verification failed")
		}
		files[filename] = result.Stdout
	}
	return files, nil
}

func (backend *VerifiedGitBackend) commitTreeID(ctx context.Context, commitID string) (string, error) {
	commit, err := backend.command(ctx, []string{gitSubcommandCatFile, "-p", commitID}, nil)
	if err != nil {
		return "", fmt.Errorf("read candidate parent commit: %w", err)
	}
	return parseCommitTree(commit.Stdout)
}

func validateSnapshotFile(path string, content []byte) error {
	if err := identity.ValidateRelativePath(path, false); err != nil || !strings.HasPrefix(path, gitStateDirPrefix) || filepath.Ext(path) != ".json" {
		return fmt.Errorf("candidate contains an invalid state path")
	}
	if len(content) == 0 || len(content) > maxStateBlobBytes || !json.Valid(bytes.TrimSpace(content)) {
		return fmt.Errorf("candidate contains an invalid or oversized state blob")
	}
	return nil
}

func validateCandidateProvenance(subject string, provenance GitProvenance) error {
	if subject == "" || len(subject) > 100 || strings.ContainsAny(subject, "\r\n") || strings.Contains(strings.ToLower(subject), "token") || strings.Contains(strings.ToLower(subject), "secret") {
		return fmt.Errorf("candidate subject is empty, oversized, or unsafe")
	}
	for _, value := range provenanceValues(provenance) {
		if err := validateProvenanceValue(value); err != nil {
			return err
		}
	}
	if provenance.Operation != "" && !allowedProvenanceOperation(provenance.Operation) {
		return fmt.Errorf("candidate operation is not allowlisted")
	}
	return nil
}

func provenanceValues(provenance GitProvenance) []string {
	return []string{provenance.Operation, provenance.Repository, provenance.Workflow, provenance.Run, provenance.RunAttempt, provenance.Job, provenance.Event, provenance.Module, provenance.Request, provenance.Attempt, provenance.Fence}
}

func validateProvenanceValue(value string) error {
	if len(value) > 256 || strings.ContainsAny(value, controlCharacters) || strings.Contains(strings.ToLower(value), "token=") || strings.Contains(strings.ToLower(value), "secret=") {
		return fmt.Errorf("candidate provenance contains an unsafe value")
	}
	return nil
}

func allowedProvenanceOperation(operation string) bool {
	return provenanceOperationAllowlist[operation]
}

func formatCandidateMessage(subject string, provenance GitProvenance, parent string) string {
	trailers := []struct{ key, value string }{
		{"Factory-Operation", provenance.Operation}, {"Factory-Repository", provenance.Repository},
		{"Factory-Workflow", provenance.Workflow}, {"Factory-Run", provenance.Run},
		{"Factory-Run-Attempt", provenance.RunAttempt}, {"Factory-Job", provenance.Job},
		{"Factory-Head", parent}, {"Factory-Event", provenance.Event},
		{"Factory-Module", provenance.Module}, {"Factory-Request", provenance.Request},
		{"Factory-Attempt", provenance.Attempt}, {"Factory-Fence", provenance.Fence},
	}
	message := subject
	for _, trailer := range trailers {
		if trailer.value != "" {
			message += "\n" + trailer.key + ": " + trailer.value
		}
	}
	return message
}

func validSingleParentCommit(commit []byte, treeID, parentID string) bool {
	lines := strings.SplitN(string(commit), "\n\n", 2)
	if len(lines) != 2 {
		return false
	}
	parents := 0
	for _, line := range strings.Split(lines[0], "\n") {
		if strings.HasPrefix(line, "parent ") {
			parents++
			if strings.TrimPrefix(line, "parent ") != parentID {
				return false
			}
		}
	}
	return parents == 1 && strings.HasPrefix(lines[0], "tree "+treeID+"\n")
}

// Read fetches the configured ref on every call, verifies the exact fetched
// commit and all state blobs, then returns a validated immutable snapshot.
func (backend *VerifiedGitBackend) Read(ctx context.Context) (string, Snapshot, error) {
	if backend == nil {
		return "", Snapshot{}, fmt.Errorf("verified Git backend is required")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if err := backend.prepareRead(ctx); err != nil {
		return "", Snapshot{}, err
	}
	if err := backend.Credentials.WithCredential(ctx, backend.Config.CredentialHost, backend.Config.Owner+"/"+backend.Config.Repository, func(askpassPath string) error {
		return backend.fetchAndRead(ctx, askpassPath)
	}); err != nil {
		return "", Snapshot{}, fmt.Errorf("read verified Git state: %w", err)
	}
	head := backend.pendingHead
	snapshot := cloneSnapshot(backend.pendingSnapshot)
	backend.pendingHead = ""
	backend.pendingSnapshot = Snapshot{}
	return head, snapshot, nil
}

func (backend *VerifiedGitBackend) prepareRead(ctx context.Context) error {
	backend.configured = false
	if err := backend.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (backend *VerifiedGitBackend) fetchAndRead(ctx context.Context, askpassPath string) error {
	environment, err := GitCredentialEnvironment(askpassPath)
	if err != nil {
		return err
	}
	remote := backend.remoteURL()
	ref := "refs/heads/" + backend.Config.Branch
	if _, err = backend.command(ctx, []string{"fetch", "--depth=1", "--no-tags", remote, ref}, environment); err != nil {
		return err
	}
	commit, err := backend.command(ctx, []string{"rev-parse", "--verify", "FETCH_HEAD^{commit}"}, environment)
	if err != nil {
		return err
	}
	head := strings.TrimSpace(string(commit.Stdout))
	if !gitObjectIDPattern.MatchString(head) {
		return fmt.Errorf("fetched state ref did not resolve to a valid commit ID")
	}
	parent, err := backend.command(ctx, []string{gitSubcommandCatFile, "-p", head}, environment)
	if err != nil {
		return err
	}
	treeID, err := parseCommitTree(parent.Stdout)
	if err != nil {
		return err
	}
	listing, err := backend.command(ctx, []string{"ls-tree", "-r", "-z", "--full-tree", treeID}, environment)
	if err != nil {
		return err
	}
	files, err := backend.readStateTree(ctx, listing.Stdout, environment)
	if err != nil {
		return err
	}
	snapshot, err := decodeSnapshotFiles(files)
	if err != nil {
		return fmt.Errorf("decode verified Git state snapshot: %w", err)
	}
	if err = snapshot.Validate(); err != nil {
		return fmt.Errorf("validate verified Git state snapshot: %w", err)
	}
	backend.pendingSnapshot = cloneSnapshot(snapshot)
	backend.pendingHead = head
	return nil
}

func snapshotsEqual(first, second Snapshot) bool {
	firstJSON, firstErr := json.Marshal(first)
	secondJSON, secondErr := json.Marshal(second)
	return firstErr == nil && secondErr == nil && bytes.Equal(firstJSON, secondJSON)
}

// Head fetches the branch and resolves its exact commit without decoding state.
func (backend *VerifiedGitBackend) Head(ctx context.Context) (string, error) {
	if backend == nil {
		return "", fmt.Errorf("verified Git backend is required")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if err := backend.validate(); err != nil {
		return "", err
	}
	remote := backend.remoteURL()
	ref := "refs/heads/" + backend.Config.Branch
	var head string
	err := backend.Credentials.WithCredential(ctx, backend.Config.CredentialHost, backend.Config.Owner+"/"+backend.Config.Repository, func(askpassPath string) error {
		environment, err := GitCredentialEnvironment(askpassPath)
		if err != nil {
			return err
		}
		if _, err = backend.command(ctx, []string{"fetch", "--depth=1", "--no-tags", remote, ref}, environment); err != nil {
			return err
		}
		result, err := backend.command(ctx, []string{"rev-parse", "--verify", "FETCH_HEAD^{commit}"}, environment)
		if err != nil {
			return err
		}
		head = strings.TrimSpace(string(result.Stdout))
		if !gitObjectIDPattern.MatchString(head) {
			return fmt.Errorf("fetched state ref did not resolve to a valid commit ID")
		}
		return nil
	})
	return head, err
}

func (backend *VerifiedGitBackend) command(ctx context.Context, arguments []string, environment map[string]string, inputs ...[]byte) (GitCommandResult, error) {
	if backend.Runner == nil {
		return GitCommandResult{}, fmt.Errorf("Git command runner is required")
	}
	if err := ctx.Err(); err != nil {
		return GitCommandResult{}, err
	}
	var input []byte
	if len(inputs) > 0 {
		input = inputs[0]
	}
	command := GitCommand{
		Directory:   backend.Config.CheckoutRoot,
		Arguments:   arguments,
		Environment: environment,
		Input:       input,
	}
	if runner, ok := backend.Runner.(interface{ allowLocalRemoteForTest() bool }); ok && runner.allowLocalRemoteForTest() {
		command.allowLocalRemote = true
	}
	return backend.Runner.Run(ctx, command)
}

func (backend *VerifiedGitBackend) remoteURL() string {
	if backend.testRemoteURL != "" {
		return backend.testRemoteURL
	}
	return fmt.Sprintf("https://%s/%s/%s.git", backend.Config.Host, backend.Config.Owner, backend.Config.Repository)
}

func (backend *VerifiedGitBackend) validate() error {
	config := backend.Config
	if config.Owner == "" || !validRepositorySegment(config.Owner) || !validRepositorySegment(config.Repository) {
		return fmt.Errorf("Git state repository identity is invalid")
	}
	if config.Host == "" || !validCredentialHost(config.Host) || config.CredentialHost != config.Host {
		return fmt.Errorf("Git state host and credential host must match a valid DNS hostname")
	}
	if config.Branch == "" || strings.HasPrefix(config.Branch, "-") || strings.ContainsAny(config.Branch, controlCharacters+" \\~^:?*[[]") || strings.HasSuffix(config.Branch, ".") || strings.HasSuffix(config.Branch, ".lock") {
		return fmt.Errorf("Git state branch name is invalid")
	}
	if config.CheckoutRoot == "" || !filepath.IsAbs(config.CheckoutRoot) {
		return fmt.Errorf("Git state checkout root must be absolute")
	}
	info, err := os.Lstat(config.CheckoutRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Git state checkout root must be a real directory")
	}
	if backend.Runner == nil || backend.Credentials == nil {
		return fmt.Errorf("Git runner and state credential provider are required")
	}
	if backend.BlobCache == nil {
		backend.BlobCache = &BlobReadCache{}
	}
	if backend.configured {
		return nil
	}
	if _, err = url.Parse("https://" + config.Host + "/" + config.Owner + "/" + config.Repository + ".git"); err != nil {
		return fmt.Errorf("Git state remote URL is invalid")
	}
	if err = backend.validateRepositoryIdentity(); err != nil {
		return err
	}
	backend.configured = true
	return nil
}

func (backend *VerifiedGitBackend) validateRepositoryIdentity() error {
	command := GitCommand{
		Directory: backend.Config.CheckoutRoot,
		Arguments: []string{"config", "--local", "--no-includes", "--null", "--list"},
	}
	if runner, ok := backend.Runner.(interface{ trustedExecutable() string }); ok {
		command.Executable = runner.trustedExecutable()
	}
	result, err := backend.Runner.Run(context.Background(), command)
	if err != nil {
		return fmt.Errorf("inspect Git checkout configuration: %w", err)
	}
	if err = validateCheckoutConfiguration(result.Stdout, backend.Config.Host, backend.Config.Owner, backend.Config.Repository); err != nil {
		return err
	}
	return nil
}

func validateCheckoutConfiguration(data []byte, host, owner, repository string) error {
	entries, err := parseCheckoutConfigEntries(data)
	if err != nil {
		return err
	}
	remoteURLs := 0
	for _, entry := range entries {
		if err := validateCheckoutConfigEntry(entry, host, owner, repository, &remoteURLs); err != nil {
			return err
		}
	}
	if remoteURLs != 1 {
		return fmt.Errorf("Git checkout must contain exactly one origin URL")
	}
	return nil
}

type checkoutConfigEntry struct{ key, value string }

func parseCheckoutConfigEntries(data []byte) ([]checkoutConfigEntry, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("Git checkout has no repository identity configuration")
	}
	entries := bytes.Split(data, []byte{0})
	if len(entries[len(entries)-1]) != 0 {
		return nil, fmt.Errorf("Git checkout configuration is incomplete")
	}
	parsed := make([]checkoutConfigEntry, 0, len(entries)-1)
	for _, entry := range entries[:len(entries)-1] {
		if len(entry) == 0 {
			return nil, fmt.Errorf("Git checkout configuration has an invalid entry")
		}
		key, value, found := strings.Cut(string(entry), "\n")
		if !found {
			key, value, found = strings.Cut(string(entry), " ")
		}
		if !found {
			return nil, fmt.Errorf("Git checkout configuration entry is malformed")
		}
		parsed = append(parsed, checkoutConfigEntry{key: strings.ToLower(strings.TrimSpace(key)), value: strings.TrimSpace(value)})
	}
	return parsed, nil
}

func validateCheckoutConfigEntry(entry checkoutConfigEntry, host, owner, repository string, remoteURLs *int) error {
	if strings.HasPrefix(entry.key, remoteConfigPrefix) && strings.HasSuffix(entry.key, remoteURLSuffix) {
		*remoteURLs++
		if entry.key != "remote.origin.url" || !repositoryURLMatches(entry.value, host, owner, repository) {
			return fmt.Errorf("Git checkout origin does not match configured repository")
		}
		return nil
	}
	if strings.HasPrefix(entry.key, remoteConfigPrefix) && strings.HasSuffix(entry.key, remoteFetchSuffix) {
		if entry.key != "remote.origin.fetch" || entry.value != "+refs/heads/*:refs/remotes/origin/*" {
			return fmt.Errorf("Git checkout has an unexpected ref mapping")
		}
		return nil
	}
	if isApprovedCheckoutConfigKey(entry.key) {
		return nil
	}
	if isUntrustedCheckoutConfigKey(entry.key) {
		return fmt.Errorf("Git checkout contains an unreviewed local configuration setting")
	}
	return nil
}

func isApprovedCheckoutConfigKey(key string) bool {
	switch key {
	case "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode":
		return true
	default:
		return false
	}
}

func isUntrustedCheckoutConfigKey(key string) bool {
	return strings.HasPrefix(key, "url.") || strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "credential.") || strings.HasPrefix(key, "core.sshcommand") || strings.HasPrefix(key, remoteConfigPrefix)
}

func repositoryURLMatches(value, host, owner, repository string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, host) || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	path := strings.TrimSuffix(parsed.EscapedPath(), ".git")
	return path == "/"+owner+"/"+repository
}

func parseCommitTree(commit []byte) (string, error) {
	line, _, _ := bytes.Cut(commit, []byte("\n"))
	if !bytes.HasPrefix(line, []byte("tree ")) {
		return "", fmt.Errorf("Git commit has no leading tree header")
	}
	treeID := strings.TrimPrefix(string(line), "tree ")
	if !gitObjectIDPattern.MatchString(treeID) {
		return "", fmt.Errorf("Git commit tree ID is invalid")
	}
	return treeID, nil
}

func (backend *VerifiedGitBackend) readStateTree(ctx context.Context, listing []byte, environment map[string]string) (map[string][]byte, error) {
	if len(listing) == 0 || len(listing) > maxTreeListingBytes || listing[len(listing)-1] != 0 {
		return nil, fmt.Errorf("Git state tree listing is empty, too large, or incomplete")
	}
	files := map[string][]byte{}
	for _, rawEntry := range bytes.Split(listing[:len(listing)-1], []byte{0}) {
		if err := backend.addListedStateBlob(ctx, rawEntry, environment, files); err != nil {
			return nil, err
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("Git state tree contains no state files")
	}
	return files, nil
}

func (backend *VerifiedGitBackend) addListedStateBlob(ctx context.Context, entry []byte, environment map[string]string, files map[string][]byte) error {
	path, objectID, err := parseStateTreeEntry(entry)
	if err != nil {
		return err
	}
	if _, exists := files[path]; exists {
		return fmt.Errorf("Git state tree contains a duplicate path")
	}
	content, found := backend.BlobCache.load(objectID)
	if !found {
		result, readErr := backend.command(ctx, []string{gitSubcommandCatFile, "blob", objectID}, environment)
		if readErr != nil {
			return fmt.Errorf("read immutable Git state blob: %w", readErr)
		}
		content = result.Stdout
		if len(content) > maxStateBlobBytes || gitBlobID(content) != objectID {
			return fmt.Errorf("Git state blob content does not match its object identity")
		}
		backend.BlobCache.store(objectID, content)
	}
	files[path] = content
	return nil
}

func parseStateTreeEntry(entry []byte) (string, string, error) {
	metadata, filename, found := bytes.Cut(entry, []byte{'\t'})
	if !found {
		return "", "", fmt.Errorf("Git state tree entry is malformed")
	}
	fields := strings.Fields(string(metadata))
	path := string(filename)
	if len(fields) != 3 || fields[0] != gitModeRegularBlob || fields[1] != "blob" || !gitObjectIDPattern.MatchString(fields[2]) {
		return "", "", fmt.Errorf("Git state tree contains unsupported object mode or type")
	}
	if err := identity.ValidateRelativePath(path, false); err != nil || !strings.HasPrefix(path, gitStateDirPrefix) || filepath.Ext(path) != ".json" {
		return "", "", fmt.Errorf("Git state tree contains an invalid state path")
	}
	return path, fields[2], nil
}

var _ HeadReader = (*VerifiedGitBackend)(nil)
var _ GitReader = (*VerifiedGitBackend)(nil)
