package state

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/softwareone-public/avm-intelligence/internal/progress"
)

const snapshotPath = "state/transaction.json"

type snapshotMetadata struct {
	SchemaVersion int               `json:"schema_version"`
	Generation    uint64            `json:"generation"`
	AppliedEvents map[string]string `json:"applied_events"`
	FencingTokens map[string]uint64 `json:"fencing_tokens"`
}

// GitHubBackend stores one validated state snapshot as JSON records on a
// dedicated branch. It only commits a child of the caller's exact expected
// head and updates the ref without force.
type GitHubBackend struct {
	Owner      string
	Repository string
	Branch     string
	Token      string
	Endpoint   string
	HTTPClient *http.Client
	ReadCache  *BlobReadCache
}

// BlobReadCache reuses immutable blob contents within one run, never refs,
// trees or decoded snapshots. Every read still observes the current branch head.
type BlobReadCache struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func (cache *BlobReadCache) load(sha string) ([]byte, bool) {
	if cache == nil {
		return nil, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	content, exists := cache.blobs[sha]
	return append([]byte(nil), content...), exists
}

func (cache *BlobReadCache) store(sha string, content []byte) {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.blobs == nil {
		cache.blobs = make(map[string][]byte)
	}
	cache.blobs[sha] = append([]byte(nil), content...)
}

// Read fetches and validates every file on the configured state branch.
func (backend GitHubBackend) Read(ctx context.Context) (string, Snapshot, error) {
	if err := backend.validate(); err != nil {
		return "", Snapshot{}, err
	}
	refPath := backend.repositoryPath("/git/ref/heads/" + url.PathEscape(backend.Branch))
	var reference struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := backend.request(ctx, http.MethodGet, refPath, nil, &reference); err != nil {
		return "", Snapshot{}, fmt.Errorf("read state branch ref: %w", err)
	}
	if reference.Object.SHA == "" {
		return "", Snapshot{}, fmt.Errorf("state branch ref has no commit SHA")
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := backend.request(ctx, http.MethodGet, backend.repositoryPath("/git/commits/"+url.PathEscape(reference.Object.SHA)), nil, &commit); err != nil {
		return "", Snapshot{}, fmt.Errorf("read state branch commit: %w", err)
	}
	if commit.Tree.SHA == "" {
		return "", Snapshot{}, fmt.Errorf("state branch commit has no tree SHA")
	}
	var tree struct {
		Truncated bool `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}
	treePath := backend.repositoryPath("/git/trees/" + url.PathEscape(commit.Tree.SHA) + "?recursive=1")
	if err := backend.request(ctx, http.MethodGet, treePath, nil, &tree); err != nil {
		return "", Snapshot{}, fmt.Errorf("read state branch tree: %w", err)
	}
	if tree.Truncated {
		return "", Snapshot{}, fmt.Errorf("state branch tree response was truncated")
	}
	files := make(map[string][]byte)
	reused := 0
	for _, entry := range tree.Tree {
		if entry.Type == "tree" {
			continue
		}
		if entry.Type != "blob" || entry.Mode != "100644" || entry.SHA == "" {
			return "", Snapshot{}, fmt.Errorf("state branch contains unsupported tree entry %q", entry.Path)
		}
		if !strings.HasPrefix(entry.Path, "state/") {
			return "", Snapshot{}, fmt.Errorf("state branch contains non-state path %q", entry.Path)
		}
		if content, found := backend.ReadCache.load(entry.SHA); found {
			files[entry.Path] = content
			reused++
			continue
		}
		var blob struct {
			Encoding string `json:"encoding"`
			Content  string `json:"content"`
		}
		if err := backend.request(ctx, http.MethodGet, backend.repositoryPath("/git/blobs/"+url.PathEscape(entry.SHA)), nil, &blob); err != nil {
			return "", Snapshot{}, fmt.Errorf("read state blob %q: %w", entry.Path, err)
		}
		if blob.Encoding != "base64" {
			return "", Snapshot{}, fmt.Errorf("state blob %q has unsupported encoding %q", entry.Path, blob.Encoding)
		}
		content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
		if err != nil {
			return "", Snapshot{}, fmt.Errorf("decode state blob %q: %w", entry.Path, err)
		}
		if gitBlobID(content) != entry.SHA {
			return "", Snapshot{}, fmt.Errorf("state blob does not match immutable object identity")
		}
		files[entry.Path] = content
		backend.ReadCache.store(entry.SHA, content)
	}
	progress.Logger(ctx).InfoContext(ctx, "state blobs loaded", "files", len(files), "reused", reused, "downloaded", len(files)-reused)
	snapshot, err := decodeSnapshotFiles(files)
	if err != nil {
		return "", Snapshot{}, fmt.Errorf("decode state branch snapshot: %w", err)
	}
	return reference.Object.SHA, snapshot, nil
}

// Commit creates a tree and commit parented by expectedHead, then performs a
// non-force ref update. Concurrent writers therefore cannot overwrite state.
func (backend GitHubBackend) Commit(ctx context.Context, expectedHead string, snapshot Snapshot) (string, error) {
	if err := backend.validate(); err != nil {
		return "", err
	}
	if expectedHead == "" {
		return "", fmt.Errorf("expected state head is required")
	}
	if err := snapshot.Validate(); err != nil {
		return "", fmt.Errorf("validate state snapshot: %w", err)
	}
	currentHead, err := backend.readHead(ctx)
	if err != nil {
		return "", err
	}
	if currentHead != expectedHead {
		return "", ErrHeadMoved
	}
	var parent struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err = backend.request(ctx, http.MethodGet, backend.repositoryPath("/git/commits/"+url.PathEscape(expectedHead)), nil, &parent); err != nil {
		return "", fmt.Errorf("read expected state commit: %w", err)
	}
	files, err := encodeSnapshotFiles(snapshot)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(files))
	for recordPath := range files {
		paths = append(paths, recordPath)
	}
	sort.Strings(paths)
	treeEntries := make([]map[string]string, 0, len(paths))
	for _, recordPath := range paths {
		entry := map[string]string{
			"path": recordPath,
			"mode": "100644",
			"type": "blob",
		}
		objectID := gitBlobID(files[recordPath])
		if _, cached := backend.ReadCache.load(objectID); cached {
			entry["sha"] = objectID
		} else {
			entry["content"] = string(files[recordPath])
		}
		treeEntries = append(treeEntries, entry)
	}
	var createdTree struct {
		SHA string `json:"sha"`
	}
	treeBody := map[string]any{"tree": treeEntries}
	if err = backend.request(ctx, http.MethodPost, backend.repositoryPath("/git/trees"), treeBody, &createdTree); err != nil {
		return "", fmt.Errorf("create state tree: %w", err)
	}
	if createdTree.SHA == "" {
		return "", fmt.Errorf("GitHub returned an empty state tree SHA")
	}
	var createdCommit struct {
		SHA string `json:"sha"`
	}
	commitBody := map[string]any{
		"message": "factory: advance operational state",
		"tree":    createdTree.SHA,
		"parents": []string{expectedHead},
	}
	if err = backend.request(ctx, http.MethodPost, backend.repositoryPath("/git/commits"), commitBody, &createdCommit); err != nil {
		return "", fmt.Errorf("create state commit: %w", err)
	}
	if createdCommit.SHA == "" {
		return "", fmt.Errorf("GitHub returned an empty state commit SHA")
	}
	var updatedRef struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	refPath := backend.repositoryPath("/git/refs/heads/" + url.PathEscape(backend.Branch))
	updateBody := map[string]any{"sha": createdCommit.SHA, "force": false}
	if err = backend.request(ctx, http.MethodPatch, refPath, updateBody, &updatedRef); err != nil {
		latestHead, headErr := backend.readHead(ctx)
		if headErr == nil && latestHead != expectedHead {
			return "", ErrHeadMoved
		}
		return "", fmt.Errorf("advance state branch without force: %w", err)
	}
	if updatedRef.Object.SHA != createdCommit.SHA {
		return "", fmt.Errorf("state ref moved to unexpected commit %q", updatedRef.Object.SHA)
	}
	for _, content := range files {
		backend.ReadCache.store(gitBlobID(content), content)
	}
	return createdCommit.SHA, nil
}

func (backend GitHubBackend) readHead(ctx context.Context) (string, error) {
	var reference struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	refPath := backend.repositoryPath("/git/ref/heads/" + url.PathEscape(backend.Branch))
	if err := backend.request(ctx, http.MethodGet, refPath, nil, &reference); err != nil {
		return "", fmt.Errorf("read current state branch head: %w", err)
	}
	if reference.Object.SHA == "" {
		return "", fmt.Errorf("state branch ref has no commit SHA")
	}
	return reference.Object.SHA, nil
}

func (backend GitHubBackend) validate() error {
	if backend.Owner == "" || backend.Repository == "" || backend.Branch == "" || strings.TrimSpace(backend.Token) == "" {
		return fmt.Errorf("GitHub state owner, repository, branch, and token are required")
	}
	if strings.ContainsAny(backend.Owner+backend.Repository, "/?#") || strings.ContainsAny(backend.Branch, "?#") {
		return fmt.Errorf("invalid GitHub state repository or branch")
	}
	return nil
}

func (backend GitHubBackend) repositoryPath(suffix string) string {
	return fmt.Sprintf("/repos/%s/%s%s", url.PathEscape(backend.Owner), url.PathEscape(backend.Repository), suffix)
}

func (backend GitHubBackend) request(ctx context.Context, method, apiPath string, body any, result any) error {
	endpoint := strings.TrimRight(backend.Endpoint, "/")
	if endpoint == "" {
		endpoint = "https://api.github.com"
	}
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode GitHub request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint+apiPath, requestBody)
	if err != nil {
		return fmt.Errorf("create GitHub request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+backend.Token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := backend.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("call GitHub API: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read GitHub response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("GitHub status %d: %s", response.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	if result != nil && len(responseBody) > 0 {
		if err = json.Unmarshal(responseBody, result); err != nil {
			return fmt.Errorf("decode GitHub response: %w", err)
		}
	}
	return nil
}

func encodeSnapshotFiles(snapshot Snapshot) (map[string][]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	if snapshot.LayoutVersion == "module-v2" {
		return encodeModuleSnapshotFiles(snapshot)
	}
	files := make(map[string][]byte, len(snapshot.Records)+1)
	metadata, err := json.Marshal(snapshotMetadata{
		SchemaVersion: snapshot.SchemaVersion,
		Generation:    snapshot.Generation,
		AppliedEvents: snapshot.AppliedEvents,
		FencingTokens: snapshot.FencingTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("encode state transaction metadata: %w", err)
	}
	files[snapshotPath] = append(metadata, '\n')
	for recordPath, record := range snapshot.Records {
		encoded, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode state record %q: %w", recordPath, marshalErr)
		}
		files[recordPath] = append(encoded, '\n')
	}
	return files, nil
}

func decodeSnapshotFiles(files map[string][]byte) (Snapshot, error) {
	if _, exists := files[layoutMarkerPath]; exists {
		return decodeModuleSnapshotFiles(files)
	}
	return decodeLegacySnapshotFiles(files)
}

func decodeLegacySnapshotFiles(files map[string][]byte) (Snapshot, error) {
	metadata, exists := files[snapshotPath]
	if !exists {
		if len(files) == 0 {
			return NewSnapshot(), nil
		}
		return Snapshot{}, fmt.Errorf("state transaction metadata is missing")
	}
	var metadataRecord snapshotMetadata
	if err := decodeOneJSON(metadata, &metadataRecord); err != nil {
		return Snapshot{}, fmt.Errorf("decode transaction metadata: %w", err)
	}
	snapshot := Snapshot{
		SchemaVersion: metadataRecord.SchemaVersion,
		Generation:    metadataRecord.Generation,
		AppliedEvents: metadataRecord.AppliedEvents,
		FencingTokens: metadataRecord.FencingTokens,
		Records:       map[string]Record{},
	}
	seen := make(map[string]struct{}, len(files))
	for recordPath, content := range files {
		if recordPath == snapshotPath {
			continue
		}
		if _, duplicate := seen[recordPath]; duplicate {
			return Snapshot{}, fmt.Errorf("duplicate state path %q", recordPath)
		}
		seen[recordPath] = struct{}{}
		var record Record
		if err := decodeOneJSON(content, &record); err != nil {
			return Snapshot{}, fmt.Errorf("decode record %q: %w", recordPath, err)
		}
		if _, exists := snapshot.Records[recordPath]; exists {
			return Snapshot{}, fmt.Errorf("record %q is duplicated in transaction metadata", recordPath)
		}
		snapshot.Records[recordPath] = record
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func decodeOneJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("JSON must contain one document")
	}
	return nil
}
