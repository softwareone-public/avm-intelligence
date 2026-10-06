package state

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStateTreeDeltaSendsOnlyChangedPaths(t *testing.T) {
	files := map[string][]byte{}
	parent := gitStateTree{}
	const fileCount = 889
	for index := range fileCount {
		filename := fmt.Sprintf("state/requests/request-%d.json", index)
		content := fmt.Appendf(nil, "{\"position\":%d}\n", index)
		files[filename] = content
		parent.Tree = append(parent.Tree, gitStateTreeEntry{Path: filename, Mode: "100644", Type: "blob", SHA: gitBlobID(content)})
	}
	files["state/requests/request-0.json"] = []byte("{\"changed\":true}\n")
	entries, deleted, err := stateTreeDelta(files, parent, &BlobReadCache{})
	if err != nil || len(entries) != 1 || deleted != 0 || entries[0]["path"] != "state/requests/request-0.json" {
		t.Fatalf("one-file transition expanded to a full-tree payload: entries=%d deleted=%d err=%v", len(entries), deleted, err)
	}
}

func TestStateTreeDeltaDeletesRemovedPathsAndReusesExistingObjects(t *testing.T) {
	content := []byte("{}\n")
	objectID := gitBlobID(content)
	parent := gitStateTree{Tree: []gitStateTreeEntry{
		{Path: "state/requests/old.json", Mode: "100644", Type: "blob", SHA: objectID},
		{Path: "state/requests/unchanged.json", Mode: "100644", Type: "blob", SHA: objectID},
	}}
	files := map[string][]byte{"state/requests/new.json": content, "state/requests/unchanged.json": content}
	entries, deleted, err := stateTreeDelta(files, parent, nil)
	if err != nil || deleted != 1 || len(entries) != 2 {
		t.Fatalf("invalid relocation delta: %v %d %v", entries, deleted, err)
	}
	want := []map[string]any{
		{"path": "state/requests/new.json", "mode": "100644", "type": "blob", "sha": objectID},
		{"path": "state/requests/old.json", "mode": "100644", "type": "blob", "sha": nil},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("deletions/immutable object reuse changed: %v", entries)
	}
}

func TestStateTreeDeltaRejectsUnsafeParentBeforeBuildingChanges(t *testing.T) {
	for _, parent := range []gitStateTree{
		{},
		{Truncated: true},
		{Tree: []gitStateTreeEntry{{Path: "state/../workflow.yml", Mode: "100644", Type: "blob", SHA: "blob"}}},
		{Tree: []gitStateTreeEntry{{Path: "state/requests/request.json", Mode: "120000", Type: "blob", SHA: "blob"}}},
		{Tree: []gitStateTreeEntry{{Path: "code.json", Mode: "100644", Type: "blob", SHA: "blob"}}},
		{Tree: []gitStateTreeEntry{{Path: "state/requests/a.json", Mode: "100644", Type: "blob", SHA: "blob"}, {Path: "state/requests/a.json", Mode: "100644", Type: "blob", SHA: "blob"}}},
	} {
		if _, _, err := stateTreeDelta(map[string][]byte{}, parent, nil); err == nil {
			t.Fatal("unsafe parent was accepted for delta/deletion construction")
		}
	}
}

func TestGitHubBackendCommitsBoundedDeltaWithExplicitDeletion(t *testing.T) {
	base := NewSnapshot()
	const recordCount = 1000
	for position := range recordCount {
		filename := fmt.Sprintf("state/requests/request-%d.json", position)
		base.Records[filename] = Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(fmt.Sprintf(`{"position":%d}`, position))}
	}
	files, err := encodeSnapshotFiles(base)
	if err != nil {
		t.Fatal(err)
	}
	cache := &BlobReadCache{}
	parent := gitStateTree{}
	for filename, content := range files {
		objectID := gitBlobID(content)
		parent.Tree = append(parent.Tree, gitStateTreeEntry{Path: filename, Mode: "100644", Type: "blob", SHA: objectID})
		cache.store(objectID, content)
	}
	next := cloneSnapshot(base)
	next.Generation++
	delete(next.Records, "state/requests/request-1.json")
	next.Records["state/requests/request-0.json"] = Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"position":0,"status":"changed"}`)}
	next.Records["state/requests/request-new.json"] = base.Records["state/requests/request-99.json"]
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/git/ref/"):
			writeJSON(response, map[string]any{"object": map[string]string{"sha": "head-0"}})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/git/commits/"):
			writeJSON(response, map[string]any{"tree": map[string]string{"sha": "tree-0"}})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/git/trees/"):
			writeJSON(response, parent)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/git/trees"):
			assertBoundedTreeRequest(t, request, files)
			writeJSON(response, map[string]string{"sha": "tree-1"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/git/commits"):
			assertCommitRequest(t, request)
			writeJSON(response, map[string]string{"sha": "head-1"})
		case request.Method == http.MethodPatch:
			assertRefUpdateRequest(t, request)
			writeJSON(response, map[string]any{"object": map[string]string{"sha": "head-1"}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	backend := GitHubBackend{Owner: "owner", Repository: "repo", Branch: "factory-state", Token: "fixture", Endpoint: server.URL, ReadCache: cache}
	if _, err := CommitExpectedHead(context.Background(), backend, "head-0", next); err != nil {
		t.Fatal(err)
	}
}

func assertBoundedTreeRequest(t *testing.T, request *http.Request, previous map[string][]byte) {
	t.Helper()
	var body struct {
		BaseTree string           `json:"base_tree"`
		Tree     []map[string]any `json:"tree"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.BaseTree != "tree-0" || len(body.Tree) != 4 {
		t.Fatalf("unbounded or unbound tree payload: %s/%d", body.BaseTree, len(body.Tree))
	}
	result := map[string]string{}
	for filename, content := range previous {
		result[filename] = gitBlobID(content)
	}
	for _, entry := range body.Tree {
		applyTreeTestEntry(t, result, entry)
	}
	if _, retained := result["state/requests/request-1.json"]; retained {
		t.Fatal("removed record survived base-tree overlay")
	}
	if result["state/requests/request-2.json"] != gitBlobID(previous["state/requests/request-2.json"]) {
		t.Fatal("unrelated record changed")
	}
	if result["state/requests/request-new.json"] != gitBlobID(previous["state/requests/request-99.json"]) {
		t.Fatal("existing object was not reused for added path")
	}
}

func applyTreeTestEntry(t *testing.T, result map[string]string, entry map[string]any) {
	t.Helper()
	filename := entry["path"].(string)
	objectID, hasSHA := entry["sha"]
	if !hasSHA {
		result[filename] = gitBlobID([]byte(entry["content"].(string)))
		return
	}
	if entry["content"] != nil {
		t.Fatal("tree entry specified both sha and content")
	}
	if objectID == nil {
		delete(result, filename)
	} else {
		result[filename] = objectID.(string)
	}
}

func TestGitHubBackendRejectsIncompleteTreeResponsesBeforeCommit(t *testing.T) {
	for _, fixture := range []struct{ name, parentBody, createdBody string }{
		{"missing-parent", `{}`, `{"sha":"tree-1"}`},
		{"null-parent", `{"tree":null}`, `{"sha":"tree-1"}`},
		{"missing-created-sha", `{"tree":[]}`, `{}`},
		{"null-created-sha", `{"tree":[]}`, `{"sha":null}`},
		{"empty-created-body", `{"tree":[]}`, ``},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			commitWrites := 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch {
				case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/git/ref/"):
					writeJSON(response, map[string]any{"object": map[string]string{"sha": "head-0"}})
				case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/git/commits/"):
					writeJSON(response, map[string]any{"tree": map[string]string{"sha": "tree-0"}})
				case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/git/trees/"):
					_, _ = response.Write([]byte(fixture.parentBody))
				case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/git/trees"):
					response.WriteHeader(http.StatusCreated)
					_, _ = response.Write([]byte(fixture.createdBody))
				default:
					commitWrites++
					http.NotFound(response, request)
				}
			}))
			defer server.Close()
			backend := GitHubBackend{Owner: "owner", Repository: "repo", Branch: "factory-state", Token: "fixture", Endpoint: server.URL}
			if _, err := backend.Commit(context.Background(), "head-0", NewSnapshot()); err == nil || commitWrites != 0 {
				t.Fatalf("incomplete response reached commit/ref writes: %v writes=%d", err, commitWrites)
			}
		})
	}
}

func TestStateTreeDeltaExactHeadFixture(t *testing.T) {
	root := os.Getenv("FACTORY_TREE_DELTA_AUDIT_ROOT")
	if root == "" {
		t.Skip("optional exact-head state fixture not supplied")
	}
	files := map[string][]byte{}
	parent := gitStateTree{Tree: []gitStateTreeEntry{}}
	err := filepath.WalkDir(filepath.Join(root, "state"), func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("fixture contains unsupported file mode")
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		files[relative] = content
		parent.Tree = append(parent.Tree, gitStateTreeEntry{Path: relative, Mode: "100644", Type: "blob", SHA: gitBlobID(content)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := decodeSnapshotFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := encodeSnapshotFiles(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, deleted, err := stateTreeDelta(rebuilt, parent, nil)
	if err != nil || len(unchanged) != 0 || deleted != 0 {
		t.Fatalf("exact-head rebuild changed current state: changes=%d deletions=%d error=%v", len(unchanged), deleted, err)
	}
	event := Event{ID: "fixture-observation-delta", RecordPath: "state/discovery/observations/fixture-observation.json", Record: Record{Kind: "discovery_observation", SchemaVersion: 1, Data: json.RawMessage(`{"schema_version":1,"fixture":true}`)}}
	next, _, err := applyEvent(snapshot, event)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeSnapshotFiles(next)
	if err != nil {
		t.Fatal(err)
	}
	changes, deleted, err := stateTreeDelta(encoded, parent, nil)
	if err != nil || len(changes) != 3 || deleted != 0 {
		t.Fatalf("one observation expanded beyond its owned metadata: changes=%d deletions=%d error=%v", len(changes), deleted, err)
	}
	t.Logf("verified %d actual files: zero-change rebuild and three-entry observation delta", len(files))
}
