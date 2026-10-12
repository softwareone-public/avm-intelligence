package state

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	maxGitBatchBytes    = 8 << 20
	maxGitBatchObjects  = 256
)

const (
	gitSubcommandCatFile    = "cat-file"
	gitSubcommandRevParse   = "rev-parse"
	gitSubcommandFetch      = "fetch"
	remoteConfigPrefix      = "remote."
	remoteURLSuffix         = ".url"
	remoteFetchSuffix       = ".fetch"
	gitModeRegularBlob      = "100644"
	gitUnmergedStage        = "0"
	gitStateDirPrefix       = "state/"
	gitHeadsRefPrefix       = "refs/heads/"
	gitFetchDepthOption     = "--depth=1"
	gitNoTagsOption         = "--no-tags"
	gitVerifyOption         = "--verify"
	gitFetchHeadCommit      = "FETCH_HEAD^{commit}"
	gitParentHeader         = "parent "
	gitAbsoluteGitDirOption = "--absolute-git-dir"
)

var gitObjectIDPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var provenanceOperationAllowlist = map[string]bool{
	"discovery.persist": true, "dispatch.claim": true, "worker.seal": true,
	"projection.update": true, "projection.delivery": true, "evidence.accept": true, "selection.export": true,
	"selection.deselect": true, "inputs.import": true, "assessment.wake": true,
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

const (
	gitFactoryAuthorName  = "AVM Intelligence Factory"
	gitFactoryAuthorEmail = "factory@avm-intelligence.invalid"
)

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
	options.Now = truncateCommitTime(options.Now)
	files, err := encodeSnapshotFiles(options.Snapshot)
	if err != nil {
		return GitCandidate{}, fmt.Errorf("encode candidate snapshot: %w", err)
	}
	if len(files) == 0 {
		return GitCandidate{}, fmt.Errorf("candidate snapshot has no state files")
	}
	return backend.createCandidate(ctx, options, files)
}

func truncateCommitTime(value time.Time) time.Time {
	if value.IsZero() {
		value = time.Now().UTC()
	}
	return value.UTC().Truncate(time.Second)
}

// Publish makes one ordinary fast-forward push attempt for an already-built
// candidate. It never builds, merges, or retries a candidate.
func (backend *VerifiedGitBackend) Publish(ctx context.Context, candidate GitCandidate) (PublicationOutcome, error) {
	if backend == nil {
		return PublicationOutcome{}, fmt.Errorf("verified Git backend is required")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if err := backend.validate(); err != nil {
		return PublicationOutcome{}, err
	}
	if !gitObjectIDPattern.MatchString(candidate.ParentHead) || !gitObjectIDPattern.MatchString(candidate.CandidateID) || !gitObjectIDPattern.MatchString(candidate.TreeID) {
		return PublicationOutcome{}, fmt.Errorf("publication candidate identity is invalid")
	}
	if err := validateCandidateCommit(ctx, backend, candidate); err != nil {
		return PublicationOutcome{}, err
	}
	pushErr := backend.publishWithCredential(ctx, candidate)
	if pushErr == nil {
		return backend.verifyPublishedCandidate(ctx, candidate)
	}
	if category, classified := gitFailureCategory(pushErr); classified {
		if category == GitFailureAuthentication || category == GitFailurePolicy || category == GitFailureIntegrity {
			return PublicationOutcome{Kind: PublicationRejected, CandidateID: candidate.CandidateID}, pushErr
		}
		if category == GitFailureContention {
			return backend.reconcilePublication(ctx, candidate, pushErr)
		}
	}
	return backend.reconcilePublication(ctx, candidate, pushErr)
}

func (backend *VerifiedGitBackend) publishWithCredential(ctx context.Context, candidate GitCandidate) error {
	return backend.Credentials.WithCredential(ctx, backend.Config.CredentialHost, backend.Config.Owner+"/"+backend.Config.Repository, func(askpassPath string) error {
		environment, err := GitCredentialEnvironment(askpassPath)
		if err != nil {
			return err
		}
		return backend.pushWithEnvironment(ctx, candidate, environment)
	})
}

func (backend *VerifiedGitBackend) pushWithEnvironment(ctx context.Context, candidate GitCandidate, environment map[string]string) error {
	branchRef := gitHeadsRefPrefix + backend.Config.Branch
	_, err := backend.command(ctx, []string{"push", "--porcelain", backend.remoteURL(), candidate.CandidateID + ":" + branchRef}, environment)
	return err
}

func (backend *VerifiedGitBackend) verifyPublishedCandidate(ctx context.Context, candidate GitCandidate) (PublicationOutcome, error) {
	var currentHead string
	err := backend.Credentials.WithCredential(ctx, backend.Config.CredentialHost, backend.Config.Owner+"/"+backend.Config.Repository, func(askpassPath string) error {
		environment, err := GitCredentialEnvironment(askpassPath)
		if err != nil {
			return err
		}
		currentHead, err = backend.fetchTip(ctx, environment)
		return err
	})
	if err != nil {
		return PublicationOutcome{Kind: PublicationUnknown, CandidateID: candidate.CandidateID}, fmt.Errorf("verify pushed candidate: %w", err)
	}
	if currentHead != candidate.CandidateID {
		isAncestor, err := backend.proveAncestorWithBoundedFetch(ctx, candidate.CandidateID, currentHead)
		if err != nil {
			return PublicationOutcome{Kind: PublicationUnknown, Head: currentHead, CandidateID: candidate.CandidateID}, err
		}
		if !isAncestor {
			return PublicationOutcome{Kind: PublicationUnknown, Head: currentHead, CandidateID: candidate.CandidateID}, fmt.Errorf("pushed candidate is not present at observed branch tip")
		}
	}
	return PublicationOutcome{Kind: PublicationCommitted, Head: currentHead, CandidateID: candidate.CandidateID}, nil
}

func validateCandidateCommit(ctx context.Context, backend *VerifiedGitBackend, candidate GitCandidate) error {
	commit, err := backend.command(ctx, []string{gitSubcommandCatFile, "-p", candidate.CandidateID}, nil)
	if err != nil {
		if category, ok := gitFailureCategory(err); ok && category == GitFailureIntegrity {
			return fmt.Errorf("%w: candidate commit", ErrGitObjectProofUnavailable)
		}
		return fmt.Errorf("read candidate commit before publication: %w", err)
	}
	if !candidateCommitMatchesMetadata(candidate.CandidateID, commit.Stdout, candidate) {
		return fmt.Errorf("candidate commit object does not match its declared identity, message, and time")
	}
	if !bytes.Contains(commit.Stdout, []byte("author "+gitFactoryAuthorName+" <"+gitFactoryAuthorEmail+"> ")) || !bytes.Contains(commit.Stdout, []byte("committer "+gitFactoryAuthorName+" <"+gitFactoryAuthorEmail+"> ")) {
		return fmt.Errorf("candidate commit identity does not match the factory transport")
	}
	parentTree, err := backend.command(ctx, []string{gitSubcommandCatFile, "-p", candidate.ParentHead}, nil)
	if err != nil {
		if category, ok := gitFailureCategory(err); ok && category == GitFailureIntegrity {
			return fmt.Errorf("%w: candidate parent", ErrGitObjectProofUnavailable)
		}
		return fmt.Errorf("read candidate parent before publication: %w", err)
	}
	if _, err = parseCommitTree(parentTree.Stdout); err != nil {
		return err
	}
	if !validSingleParentCommit(commit.Stdout, candidate.TreeID, candidate.ParentHead) {
		return fmt.Errorf("candidate commit does not match its declared parent and tree")
	}
	committedSnapshot, err := mustSnapshotFromCommit(ctx, backend, candidate.CandidateID)
	if err != nil {
		return err
	}
	if !snapshotsEqual(candidate.Snapshot, committedSnapshot) {
		return fmt.Errorf("candidate commit snapshot does not match publication payload")
	}
	return nil
}

func candidateCommitMatchesMetadata(candidateID string, commit []byte, candidate GitCandidate) bool {
	object := fmt.Appendf(nil, "commit %d\x00", len(commit))
	object = append(object, commit...)
	if !candidateCommitIDMatches(object, candidateID) {
		return false
	}
	if candidate.CommitTime.IsZero() || candidate.CommitTime.Nanosecond() != 0 {
		return false
	}
	if !candidateCommitHeadersMatch(commit, candidate) {
		return false
	}
	return true
}

func candidateCommitIDMatches(object []byte, candidateID string) bool {
	return fmt.Sprintf("%x", sha1.Sum(object)) == candidateID
}

func candidateCommitHeadersMatch(commit []byte, candidate GitCandidate) bool {
	headers, message, found := bytes.Cut(commit, []byte("\n\n"))
	if !found || !candidateMessageMatches(message, candidate.Message) {
		return false
	}
	expectedDate := fmt.Sprintf(" %d +0000", candidate.CommitTime.Unix())
	authorFound, committerFound := false, false
	for _, line := range bytes.Split(headers, []byte("\n")) {
		authorFound, committerFound = matchCandidateIdentityHeaders(line, expectedDate, authorFound, committerFound)
	}
	return authorFound && committerFound
}

func matchCandidateIdentityHeaders(line []byte, expectedDate string, authorFound, committerFound bool) (bool, bool) {
	if bytes.HasPrefix(line, []byte("author ")) {
		return !authorFound && bytes.HasSuffix(line, []byte(expectedDate)), committerFound
	}
	if bytes.HasPrefix(line, []byte("committer ")) {
		return authorFound, !committerFound && bytes.HasSuffix(line, []byte(expectedDate))
	}
	return authorFound, committerFound
}

func candidateMessageMatches(message []byte, expected string) bool {
	return bytes.Equal(bytes.TrimSuffix(message, []byte("\n")), []byte(expected))
}

func mustSnapshotFromCommit(ctx context.Context, backend *VerifiedGitBackend, commitID string) (Snapshot, error) {
	commit, err := backend.command(ctx, []string{gitSubcommandCatFile, "-p", commitID}, nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read candidate before publication: %w", err)
	}
	treeID, err := parseCommitTree(commit.Stdout)
	if err != nil {
		return Snapshot{}, err
	}
	listing, err := backend.command(ctx, []string{"ls-tree", "-r", "-z", "--full-tree", treeID}, nil)
	if err != nil {
		return Snapshot{}, err
	}
	files, err := backend.readStateTree(ctx, listing.Stdout, nil)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, err := decodeSnapshotFiles(files)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func gitFailureCategory(err error) (GitFailureCategory, bool) {
	var commandError *GitCommandError
	if !errors.As(err, &commandError) {
		return "", false
	}
	return commandError.Category, true
}

func isIncompleteAncestryProofFailure(err error) bool {
	category, classified := gitFailureCategory(err)
	if !classified {
		return false
	}
	if category == GitFailureCommand {
		return true
	}
	return category == GitFailureIntegrity && strings.Contains(strings.ToLower(err.Error()), "missing object")
}

func (backend *VerifiedGitBackend) reconcilePublication(ctx context.Context, candidate GitCandidate, pushErr error) (PublicationOutcome, error) {
	if ctx.Err() != nil {
		return PublicationOutcome{Kind: PublicationUnknown, CandidateID: candidate.CandidateID}, fmt.Errorf("publication outcome unknown after cancellation: %w", ctx.Err())
	}
	var latestHead string
	err := backend.Credentials.WithCredential(ctx, backend.Config.CredentialHost, backend.Config.Owner+"/"+backend.Config.Repository, func(askpassPath string) error {
		environment, err := GitCredentialEnvironment(askpassPath)
		if err != nil {
			return err
		}
		head, fetchErr := backend.fetchTip(ctx, environment)
		if fetchErr != nil {
			return fetchErr
		}
		latestHead = head
		return nil
	})
	if err != nil {
		return PublicationOutcome{Kind: PublicationUnknown, CandidateID: candidate.CandidateID}, fmt.Errorf("publication outcome cannot be reconciled: %w", err)
	}
	if latestHead == candidate.CandidateID {
		return PublicationOutcome{Kind: PublicationCommitted, Head: latestHead, CandidateID: candidate.CandidateID}, nil
	}
	if latestHead == candidate.ParentHead {
		return PublicationOutcome{Kind: PublicationRejected, Head: latestHead, CandidateID: candidate.CandidateID}, pushErr
	}
	candidateIsAncestor, candidateErr := backend.proveAncestorWithBoundedFetch(ctx, candidate.CandidateID, latestHead)
	if candidateErr == nil && candidateIsAncestor {
		return PublicationOutcome{Kind: PublicationCommitted, Head: latestHead, CandidateID: candidate.CandidateID}, nil
	}
	if candidateErr != nil && !errors.Is(candidateErr, ErrGitObjectProofUnavailable) {
		return PublicationOutcome{Kind: PublicationUnknown, Head: latestHead, CandidateID: candidate.CandidateID}, candidateErr
	}
	parentIsAncestor, parentErr := backend.proveAncestorWithBoundedFetch(ctx, candidate.ParentHead, latestHead)
	if parentErr == nil && parentIsAncestor {
		return PublicationOutcome{Kind: PublicationRejected, Head: latestHead, CandidateID: candidate.CandidateID}, &GitCommandError{Operation: "push", Category: GitFailureContention, ExitCode: 1}
	}
	if parentErr != nil {
		return PublicationOutcome{Kind: PublicationUnknown, Head: latestHead, CandidateID: candidate.CandidateID}, candidateErr
	}
	return PublicationOutcome{Kind: PublicationUnknown, Head: latestHead, CandidateID: candidate.CandidateID}, fmt.Errorf("publication outcome conflicts with observed branch ancestry")
}

func (backend *VerifiedGitBackend) fetchTip(ctx context.Context, environment map[string]string) (string, error) {
	remote := backend.remoteURL()
	ref := gitHeadsRefPrefix + backend.Config.Branch
	if _, err := backend.command(ctx, []string{gitSubcommandFetch, gitFetchDepthOption, gitNoTagsOption, remote, ref}, environment); err != nil {
		return "", err
	}
	result, err := backend.command(ctx, []string{gitSubcommandRevParse, gitVerifyOption, gitFetchHeadCommit}, environment)
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(result.Stdout))
	if !gitObjectIDPattern.MatchString(head) {
		return "", fmt.Errorf("fetched state tip ID is invalid")
	}
	return head, nil
}

func (backend *VerifiedGitBackend) isAncestorWithBoundedFetch(ctx context.Context, ancestor, descendant string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	isAncestor, err := backend.checkAncestryAtCurrentDepth(ctx, ancestor, descendant)
	if err != nil || isAncestor {
		return isAncestor, err
	}
	if known, proofErr := backend.knownNonAncestorAtShallowBoundary(ctx, ancestor, descendant); proofErr != nil || known {
		return false, proofErr
	}
	return backend.deepenAndCheckAncestry(ctx, ancestor, descendant)
}

func (backend *VerifiedGitBackend) proveAncestorWithBoundedFetch(ctx context.Context, ancestor, descendant string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return backend.walkCommitAncestry(ctx, ancestor, descendant)
}

// ProveAncestor exposes the backend's bounded commit ancestry proof to the
// transaction coordinator for reconciling ambiguous publication outcomes.
func (backend *VerifiedGitBackend) ProveAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.proveAncestorWithBoundedFetch(ctx, ancestor, descendant)
}

func (backend *VerifiedGitBackend) walkCommitAncestry(ctx context.Context, target, tip string) (bool, error) {
	current := tip
	for position := 0; position <= maxPublishAncestryDepth; position++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if current == target {
			return true, nil
		}
		commit, err := backend.command(ctx, []string{gitSubcommandCatFile, "-p", current}, nil)
		if err != nil {
			category, _ := gitFailureCategory(err)
			if category == GitFailureIntegrity {
				if err = backend.deepenForAncestryProof(ctx); err != nil {
					return false, err
				}
				commit, err = backend.command(ctx, []string{gitSubcommandCatFile, "-p", current}, nil)
			}
			if err != nil {
				return false, fmt.Errorf("%w: read commit ancestry", ErrGitObjectProofUnavailable)
			}
		}
		parents := commitParents(commit.Stdout)
		if len(parents) == 0 {
			return false, nil
		}
		if len(parents) != 1 {
			return false, fmt.Errorf("%w: merge commit encountered in state ancestry", ErrGitObjectProofUnavailable)
		}
		current = parents[0]
	}
	return false, fmt.Errorf("%w: ancestry exceeded %d commits", ErrGitObjectProofUnavailable, maxPublishAncestryDepth)
}

func commitParents(commit []byte) []string {
	var parents []string
	for _, line := range bytes.Split(commit, []byte("\n")) {
		if len(line) == 0 {
			break
		}
		if bytes.HasPrefix(line, []byte(gitParentHeader)) {
			parents = append(parents, strings.TrimPrefix(string(line), gitParentHeader))
		}
	}
	return parents
}

func (backend *VerifiedGitBackend) deepenForAncestryProof(ctx context.Context) error {
	return backend.Credentials.WithCredential(ctx, backend.Config.CredentialHost, backend.Config.Owner+"/"+backend.Config.Repository, func(askpassPath string) error {
		environment, err := GitCredentialEnvironment(askpassPath)
		if err != nil {
			return err
		}
		_, err = backend.command(ctx, []string{gitSubcommandFetch, gitAncestryDeepenOption, gitNoTagsOption, backend.remoteURL(), gitHeadsRefPrefix + backend.Config.Branch}, environment)
		return err
	})
}

func (backend *VerifiedGitBackend) knownNonAncestorAtShallowBoundary(ctx context.Context, ancestor, descendant string) (bool, error) {
	shallow, err := backend.shallowCommits(ctx)
	if err != nil {
		return false, err
	}
	if !shallow[descendant] {
		return false, nil
	}
	ancestorCommit, err := backend.command(ctx, []string{gitSubcommandCatFile, "-p", ancestor}, nil)
	if err != nil {
		return false, err
	}
	parent := firstCommitParent(ancestorCommit.Stdout)
	return parent != "" && shallow[parent], nil
}

func firstCommitParent(commit []byte) string {
	for _, line := range bytes.Split(commit, []byte("\n")) {
		if bytes.HasPrefix(line, []byte(gitParentHeader)) {
			return strings.TrimPrefix(string(line), gitParentHeader)
		}
		if len(line) == 0 {
			break
		}
	}
	return ""
}

func (backend *VerifiedGitBackend) checkAncestryAtCurrentDepth(ctx context.Context, ancestor, descendant string) (bool, error) {
	_, err := backend.command(ctx, []string{"merge-base", "--is-ancestor", ancestor, descendant}, nil)
	if err == nil {
		return true, nil
	}
	if category, classified := gitFailureCategory(err); classified && category == GitFailureIntegrity {
		return false, fmt.Errorf("%w: ancestry endpoint is unavailable locally", ErrGitObjectProofUnavailable)
	}
	if category, classified := gitFailureCategory(err); !classified || category != GitFailureCommand {
		return false, fmt.Errorf("check ancestry proof: %w", err)
	}
	var commandError *GitCommandError
	if !errors.As(err, &commandError) || commandError.ExitCode != 1 {
		return false, fmt.Errorf("check ancestry proof: %w", err)
	}
	return false, nil
}

func (backend *VerifiedGitBackend) deepenAndCheckAncestry(ctx context.Context, ancestor, descendant string) (bool, error) {
	shallow, err := backend.shallowCommits(ctx)
	if err != nil {
		return false, fmt.Errorf("inspect shallow ancestry boundary: %w", err)
	}
	if !hasShallowBoundary(shallow) {
		return false, nil
	}
	for depth := publishAncestryStep; depth <= maxPublishAncestryDepth; depth += publishAncestryStep {
		if err = ctx.Err(); err != nil {
			return false, err
		}
		if err = backend.deepenForAncestryProof(ctx); err != nil {
			return false, fmt.Errorf("deepen ancestry proof: %w", err)
		}
		isAncestor, proofErr := backend.checkAncestryAtCurrentDepth(ctx, ancestor, descendant)
		if proofErr != nil || isAncestor {
			return isAncestor, proofErr
		}
		shallow, err = backend.shallowCommits(ctx)
		if err != nil {
			return false, fmt.Errorf("inspect shallow ancestry boundary: %w", err)
		}
		if !hasShallowBoundary(shallow) {
			return false, nil
		}
	}
	return false, fmt.Errorf("%w: ancestry exceeded %d commits", ErrGitObjectProofUnavailable, maxPublishAncestryDepth)
}

func hasShallowBoundary(shallow map[string]bool) bool {
	return len(shallow) > 0
}

func (backend *VerifiedGitBackend) shallowCommits(ctx context.Context) (map[string]bool, error) {
	gitDirectory, err := backend.gitDirectory(ctx)
	if err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(filepath.Join(gitDirectory, "shallow"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	shallow := map[string]bool{}
	for _, line := range strings.Fields(string(contents)) {
		if !gitObjectIDPattern.MatchString(line) {
			return nil, fmt.Errorf("shallow boundary contains an invalid object ID")
		}
		shallow[line] = true
	}
	return shallow, nil
}

func (backend *VerifiedGitBackend) gitDirectory(ctx context.Context) (string, error) {
	result, err := backend.command(ctx, []string{gitSubcommandRevParse, gitAbsoluteGitDirOption}, nil)
	if err != nil {
		return "", fmt.Errorf("resolve Git object database path: %w", err)
	}
	path := strings.TrimSpace(string(result.Stdout))
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("Git object database path is not absolute")
	}
	return path, nil
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
	return GitCandidate{ParentHead: options.ParentHead, CandidateID: commitID, TreeID: treeID, Snapshot: cloneSnapshot(options.Snapshot), Message: message, CommitTime: options.Now}, nil
}

func candidateCommitEnvironment(now time.Time) map[string]string {
	now = truncateCommitTime(now)
	date := fmt.Sprintf("@%d +0000", now.Unix())
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
	type parentBlob struct {
		path string
		id   string
	}
	entries := make([]parentBlob, 0)
	objectIDs := make([]string, 0)
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
		entries = append(entries, parentBlob{path: filename, id: fields[1]})
		objectIDs = append(objectIDs, fields[1])
	}
	blobs, err := backend.readMissingBlobs(ctx, objectIDs, nil)
	if err != nil {
		return nil, fmt.Errorf("read candidate parent blobs: %w", err)
	}
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		content, exists := blobs[entry.id]
		if !exists || gitBlobID(content) != entry.id {
			return nil, fmt.Errorf("candidate parent blob object verification failed")
		}
		files[entry.path] = content
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
		if strings.HasPrefix(line, gitParentHeader) {
			parents++
			if strings.TrimPrefix(line, gitParentHeader) != parentID {
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
	ref := gitHeadsRefPrefix + backend.Config.Branch
	if _, err = backend.command(ctx, []string{gitSubcommandFetch, gitFetchDepthOption, gitNoTagsOption, remote, ref}, environment); err != nil {
		return err
	}
	commit, err := backend.command(ctx, []string{gitSubcommandRevParse, gitVerifyOption, gitFetchHeadCommit}, environment)
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
	ref := gitHeadsRefPrefix + backend.Config.Branch
	var head string
	err := backend.Credentials.WithCredential(ctx, backend.Config.CredentialHost, backend.Config.Owner+"/"+backend.Config.Repository, func(askpassPath string) error {
		environment, err := GitCredentialEnvironment(askpassPath)
		if err != nil {
			return err
		}
		if _, err = backend.command(ctx, []string{gitSubcommandFetch, gitFetchDepthOption, gitNoTagsOption, remote, ref}, environment); err != nil {
			return err
		}
		result, err := backend.command(ctx, []string{gitSubcommandRevParse, gitVerifyOption, gitFetchHeadCommit}, environment)
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
	if err = validateCheckoutConfiguration(result.Stdout, backend.Config.Host, backend.Config.Owner, backend.Config.Repository, backend.Config.Branch); err != nil {
		return err
	}
	return nil
}

func validateCheckoutConfiguration(data []byte, host, owner, repository, branch string) error {
	entries, err := parseCheckoutConfigEntries(data)
	if err != nil {
		return err
	}
	remoteURLs := 0
	for _, entry := range entries {
		if err := validateCheckoutConfigEntry(entry, host, owner, repository, branch, &remoteURLs); err != nil {
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

func validateCheckoutConfigEntry(entry checkoutConfigEntry, host, owner, repository, branch string, remoteURLs *int) error {
	if strings.HasPrefix(entry.key, remoteConfigPrefix) && strings.HasSuffix(entry.key, remoteURLSuffix) {
		*remoteURLs++
		if entry.key != "remote.origin.url" || !repositoryURLMatches(entry.value, host, owner, repository) {
			return fmt.Errorf("Git checkout origin does not match configured repository")
		}
		return nil
	}
	if strings.HasPrefix(entry.key, remoteConfigPrefix) && strings.HasSuffix(entry.key, remoteFetchSuffix) {
		wildcardMapping := "+refs/heads/*:refs/remotes/origin/*"
		branchMapping := "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
		checkoutBranchMapping := "+refs/heads/" + branch + "*:refs/remotes/origin/" + branch + "*"
		if entry.key != "remote.origin.fetch" || (entry.value != wildcardMapping && entry.value != branchMapping && entry.value != checkoutBranchMapping) {
			return fmt.Errorf("Git checkout has an unexpected ref mapping")
		}
		return nil
	}
	if isApprovedCheckoutConfigKey(entry.key) {
		return nil
	}
	if strings.HasPrefix(entry.key, "branch.") {
		prefix := "branch." + branch + "."
		if strings.HasPrefix(entry.key, prefix) {
			switch strings.TrimPrefix(entry.key, prefix) {
			case "remote":
				if entry.value == "." || entry.value == "origin" {
					return nil
				}
			case "merge":
				if entry.value == "refs/heads/"+branch {
					return nil
				}
			}
		}
		return fmt.Errorf("Git checkout has unapproved branch configuration")
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
	return strings.HasPrefix(key, "url.") || strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "credential.") || strings.HasPrefix(key, "core.sshcommand") || strings.HasPrefix(key, remoteConfigPrefix) || strings.HasPrefix(key, "branch.")
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
	objectIDs := make([]string, 0)
	missing := map[string]bool{}
	if err := backend.indexCachedStateEntries(bytes.Split(listing[:len(listing)-1], []byte{0}), files, &objectIDs, missing); err != nil {
		return nil, err
	}
	if err := backend.loadMissingStateEntries(ctx, listing, environment, files, objectIDs); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("Git state tree contains no state files")
	}
	return files, nil
}

func (backend *VerifiedGitBackend) indexCachedStateEntries(entries [][]byte, files map[string][]byte, objectIDs *[]string, missing map[string]bool) error {
	for _, entry := range entries {
		objectID, err := backend.addCachedStateBlob(entry, files)
		if err != nil {
			return err
		}
		if objectID != "" && !missing[objectID] {
			missing[objectID] = true
			*objectIDs = append(*objectIDs, objectID)
		}
	}
	return nil
}

func (backend *VerifiedGitBackend) loadMissingStateEntries(ctx context.Context, listing []byte, environment map[string]string, files map[string][]byte, objectIDs []string) error {
	if len(objectIDs) == 0 {
		return nil
	}
	blobs, err := backend.readMissingBlobs(ctx, objectIDs, environment)
	if err != nil {
		return err
	}
	for _, entry := range bytes.Split(listing[:len(listing)-1], []byte{0}) {
		if err = backend.attachBatchBlob(entry, blobs, files); err != nil {
			return err
		}
	}
	return nil
}

func (backend *VerifiedGitBackend) attachBatchBlob(entry []byte, blobs map[string][]byte, files map[string][]byte) error {
	path, objectID, err := parseStateTreeEntry(entry)
	if err != nil {
		return err
	}
	if _, exists := files[path]; exists {
		return nil
	}
	content, exists := blobs[objectID]
	if !exists {
		return fmt.Errorf("Git state tree blob batch omitted an object")
	}
	files[path] = content
	return nil
}

func (backend *VerifiedGitBackend) addCachedStateBlob(entry []byte, files map[string][]byte) (string, error) {
	path, objectID, err := parseStateTreeEntry(entry)
	if err != nil {
		return "", err
	}
	if _, exists := files[path]; exists {
		return "", fmt.Errorf("Git state tree contains a duplicate path")
	}
	if content, found := backend.BlobCache.load(objectID); found {
		files[path] = content
		return "", nil
	}
	return objectID, nil
}

func (backend *VerifiedGitBackend) readMissingBlobs(ctx context.Context, objectIDs []string, environment map[string]string) (map[string][]byte, error) {
	input := make([]byte, 0, len(objectIDs)*(gitObjectIDLength+1))
	for _, objectID := range objectIDs {
		input = append(input, objectID...)
		input = append(input, '\n')
	}
	sizeResult, err := backend.command(ctx, []string{gitSubcommandCatFile, "--batch-check"}, environment, input)
	if err != nil {
		return nil, fmt.Errorf("inspect immutable Git state blobs: %w", err)
	}
	sizes, err := parseBatchBlobSizes(sizeResult.Stdout, objectIDs)
	if err != nil {
		return nil, err
	}
	blobs := make(map[string][]byte, len(objectIDs))
	for start := 0; start < len(objectIDs); {
		end := gitBatchEnd(objectIDs, sizes, start)
		batchInput := make([]byte, 0, (end-start)*(gitObjectIDLength+1))
		for _, objectID := range objectIDs[start:end] {
			batchInput = append(batchInput, objectID...)
			batchInput = append(batchInput, '\n')
		}
		batchResult, batchErr := backend.command(ctx, []string{gitSubcommandCatFile, "--batch"}, environment, batchInput)
		if batchErr != nil {
			return nil, fmt.Errorf("read immutable Git state blobs: %w", batchErr)
		}
		parsed, parseErr := parseBatchBlobContents(batchResult.Stdout, objectIDs[start:end], sizes)
		if parseErr != nil {
			return nil, parseErr
		}
		for objectID, content := range parsed {
			if gitBlobID(content) != objectID {
				return nil, fmt.Errorf("Git state blob content does not match its object identity")
			}
			backend.BlobCache.store(objectID, content)
			blobs[objectID] = content
		}
		start = end
	}
	return blobs, nil
}

func gitBatchEnd(objectIDs []string, sizes map[string]int, start int) int {
	end, total := start, 0
	for end < len(objectIDs) && end-start < maxGitBatchObjects {
		size := sizes[objectIDs[end]]
		if end > start && total+size > maxGitBatchBytes {
			break
		}
		total += size
		end++
	}
	return end
}

func parseBatchBlobSizes(output []byte, expectedIDs []string) (map[string]int, error) {
	lines := bytes.Split(bytes.TrimSuffix(output, []byte("\n")), []byte("\n"))
	if len(lines) != len(expectedIDs) {
		return nil, fmt.Errorf("Git blob size response count does not match requested objects")
	}
	sizes := make(map[string]int, len(expectedIDs))
	for index, line := range lines {
		fields := strings.Fields(string(line))
		if len(fields) != 3 || fields[0] != expectedIDs[index] || fields[1] != "blob" {
			return nil, fmt.Errorf("Git blob size response has an invalid object identity")
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 || size > maxStateBlobBytes {
			return nil, fmt.Errorf("Git state blob exceeds the size limit")
		}
		sizes[expectedIDs[index]] = size
	}
	return sizes, nil
}

func parseBatchBlobContents(output []byte, expectedIDs []string, sizes map[string]int) (map[string][]byte, error) {
	blobs := make(map[string][]byte, len(expectedIDs))
	position := 0
	for _, objectID := range expectedIDs {
		lineEnd := bytes.IndexByte(output[position:], '\n')
		if lineEnd < 0 {
			return nil, fmt.Errorf("Git blob batch response is incomplete")
		}
		lineEnd += position
		fields := strings.Fields(string(output[position:lineEnd]))
		if len(fields) != 3 || fields[0] != objectID || fields[1] != "blob" {
			return nil, fmt.Errorf("Git blob batch response has an invalid object identity")
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size != sizes[objectID] {
			return nil, fmt.Errorf("Git blob batch response size does not match its verified size")
		}
		position = lineEnd + 1
		if size > len(output)-position || len(output)-position-size < 1 || output[position+size] != '\n' {
			return nil, fmt.Errorf("Git blob batch response content is incomplete")
		}
		blobs[objectID] = append([]byte(nil), output[position:position+size]...)
		position += size + 1
	}
	if position != len(output) {
		return nil, fmt.Errorf("Git blob batch response contains trailing data")
	}
	return blobs, nil
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
