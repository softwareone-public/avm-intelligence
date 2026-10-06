package state

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGitHubBackendReadsTypedSnapshotFiles(t *testing.T) {
	snapshot := snapshotWithRequest(t)
	files, err := encodeSnapshotFiles(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	blobs := make(map[string][]byte, len(files))
	tree := make([]map[string]string, 0, len(files))
	index := 0
	var blobCalls, headReads atomic.Int64
	for recordPath, content := range files {
		blobID := fmt.Sprintf("blob-%d", index)
		index++
		blobs[blobID] = content
		tree = append(tree, map[string]string{"path": recordPath, "mode": "100644", "type": "blob", "sha": blobID})
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/git/ref/heads/factory-state"):
			writeJSON(response, map[string]any{"object": map[string]string{"sha": fmt.Sprintf("head-%d", headReads.Add(1))}})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/git/commits/head-"):
			writeJSON(response, map[string]any{"tree": map[string]string{"sha": "tree-1"}})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/git/trees/tree-1"):
			writeJSON(response, map[string]any{"truncated": false, "tree": tree})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/git/blobs/"):
			blobCalls.Add(1)
			blobID := strings.TrimPrefix(request.URL.Path, "/repos/softwareone-public/avm-intelligence/git/blobs/")
			content, exists := blobs[blobID]
			if !exists {
				http.Error(response, "unknown blob", http.StatusNotFound)
				return
			}
			writeJSON(response, map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString(content)})
		default:
			http.Error(response, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	backend := GitHubBackend{Owner: "softwareone-public", Repository: "avm-intelligence", Branch: "factory-state", Token: "test-token", Endpoint: server.URL, ReadCache: &BlobReadCache{}}
	head, actual, err := backend.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if head != "head-1" || !reflect.DeepEqual(actual, snapshot) {
		t.Fatalf("read = (%q, %#v), want (head-1, %#v)", head, actual, snapshot)
	}
	assertCachedStateReadProtection(t, backend, snapshot, &blobCalls, len(files))
}

func assertCachedStateReadProtection(t *testing.T, backend GitHubBackend, snapshot Snapshot, blobCalls *atomic.Int64, fileCount int) {
	t.Helper()
	head, actual, err := backend.Read(context.Background())
	if err != nil || head != "head-2" || !reflect.DeepEqual(actual, snapshot) || blobCalls.Load() != int64(fileCount) {
		t.Fatalf("cache hid current head or redownloaded immutable blobs: head=%s calls=%d error=%v", head, blobCalls.Load(), err)
	}
	if _, err = backend.Commit(context.Background(), "head-2", snapshot); !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("blob caching weakened expected-head protection: %v", err)
	}
}

func TestGitHubBackendCommitUsesExpectedParentAndNeverForces(t *testing.T) {
	snapshot := snapshotWithRequest(t)
	var methods []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/softwareone-public/avm-intelligence/git/ref/heads/factory-state", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, map[string]any{"object": map[string]string{"sha": "head-0"}})
	})
	mux.HandleFunc("GET /repos/softwareone-public/avm-intelligence/git/commits/head-0", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, map[string]any{"tree": map[string]string{"sha": "tree-0"}})
	})
	mux.HandleFunc("POST /repos/softwareone-public/avm-intelligence/git/trees", func(response http.ResponseWriter, request *http.Request) {
		assertTreeRequest(t, request, snapshot)
		writeJSON(response, map[string]string{"sha": "tree-1"})
	})
	mux.HandleFunc("POST /repos/softwareone-public/avm-intelligence/git/commits", func(response http.ResponseWriter, request *http.Request) {
		assertCommitRequest(t, request)
		writeJSON(response, map[string]string{"sha": "head-1"})
	})
	mux.HandleFunc("PATCH /repos/softwareone-public/avm-intelligence/git/refs/heads/factory-state", func(response http.ResponseWriter, request *http.Request) {
		assertRefUpdateRequest(t, request)
		writeJSON(response, map[string]any{"object": map[string]string{"sha": "head-1"}})
	})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		methods = append(methods, request.Method+" "+request.URL.Path)
		mux.ServeHTTP(response, request)
	}))
	defer server.Close()

	backend := GitHubBackend{Owner: "softwareone-public", Repository: "avm-intelligence", Branch: "factory-state", Token: "test-token", Endpoint: server.URL}
	newHead, err := CommitExpectedHead(context.Background(), backend, "head-0", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if newHead != "head-1" {
		t.Fatalf("new head = %q, want head-1", newHead)
	}
	wantMethods := []string{
		"GET /repos/softwareone-public/avm-intelligence/git/ref/heads/factory-state",
		"GET /repos/softwareone-public/avm-intelligence/git/commits/head-0",
		"POST /repos/softwareone-public/avm-intelligence/git/trees",
		"POST /repos/softwareone-public/avm-intelligence/git/commits",
		"PATCH /repos/softwareone-public/avm-intelligence/git/refs/heads/factory-state",
	}
	if !reflect.DeepEqual(methods, wantMethods) {
		t.Fatalf("GitHub calls = %v, want %v", methods, wantMethods)
	}
}

func assertTreeRequest(t *testing.T, request *http.Request, snapshot Snapshot) {
	t.Helper()
	var body struct {
		BaseTree string `json:"base_tree"`
		Tree     []struct {
			Mode string `json:"mode"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatalf("decode tree request: %v", err)
	}
	if body.BaseTree != "tree-0" || len(body.Tree) != len(snapshot.Records)+1 {
		t.Fatalf("tree request base/entries = %q/%d", body.BaseTree, len(body.Tree))
	}
	for _, entry := range body.Tree {
		if entry.Mode != "100644" || entry.Type != "blob" {
			t.Errorf("unsupported tree entry: %#v", entry)
		}
	}
}

func assertCommitRequest(t *testing.T, request *http.Request) {
	t.Helper()
	var body struct {
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatalf("decode commit request: %v", err)
	}
	if body.Tree != "tree-1" || !reflect.DeepEqual(body.Parents, []string{"head-0"}) {
		t.Fatalf("commit tree/parents = %q/%v", body.Tree, body.Parents)
	}
}

func assertRefUpdateRequest(t *testing.T, request *http.Request) {
	t.Helper()
	var body struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatalf("decode ref request: %v", err)
	}
	if body.SHA != "head-1" || body.Force {
		t.Fatalf("ref update = %#v, want new SHA and force=false", body)
	}
}

func TestGitHubBackendRejectsMovedExpectedHeadBeforeWriting(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestCount++
		writeJSON(response, map[string]any{"object": map[string]string{"sha": "head-newer"}})
	}))
	defer server.Close()
	backend := GitHubBackend{Owner: "owner", Repository: "repo", Branch: "factory-state", Token: "test-token", Endpoint: server.URL}

	if _, err := CommitExpectedHead(context.Background(), backend, "head-stale", NewSnapshot()); !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("stale commit error = %v, want ErrHeadMoved", err)
	}
	if requestCount != 1 {
		t.Fatalf("stale expected head made %d requests, want one read and no writes", requestCount)
	}
}

func TestGitHubBackendDetectsRaceAtNonForceRefUpdate(t *testing.T) {
	refReads := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/owner/repo/git/ref/heads/factory-state", func(response http.ResponseWriter, _ *http.Request) {
		refReads++
		head := "head-0"
		if refReads > 1 {
			head = "head-raced"
		}
		writeJSON(response, map[string]any{"object": map[string]string{"sha": head}})
	})
	mux.HandleFunc("GET /repos/owner/repo/git/commits/head-0", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, map[string]any{"tree": map[string]string{"sha": "tree-0"}})
	})
	mux.HandleFunc("POST /repos/owner/repo/git/trees", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, map[string]string{"sha": "tree-1"})
	})
	mux.HandleFunc("POST /repos/owner/repo/git/commits", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, map[string]string{"sha": "candidate-commit"})
	})
	mux.HandleFunc("PATCH /repos/owner/repo/git/refs/heads/factory-state", func(response http.ResponseWriter, request *http.Request) {
		var body struct {
			Force bool `json:"force"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode ref request: %v", err)
		}
		if body.Force {
			t.Error("raced ref update requested force")
		}
		http.Error(response, `{"message":"Update is not a fast forward"}`, http.StatusUnprocessableEntity)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	backend := GitHubBackend{Owner: "owner", Repository: "repo", Branch: "factory-state", Token: "test-token", Endpoint: server.URL}

	if _, err := CommitExpectedHead(context.Background(), backend, "head-0", NewSnapshot()); !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("concurrent ref advance error = %v, want ErrHeadMoved", err)
	}
	if refReads != 2 {
		t.Fatalf("state head was read %d times, want initial and post-conflict reads", refReads)
	}
}

func snapshotWithRequest(t *testing.T) Snapshot {
	t.Helper()
	snapshot, changed, err := applyEvent(NewSnapshot(), requestEvent("event-1", `{"status":"queued"}`, "module-a", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("initial request event did not change state")
	}
	return snapshot
}

func writeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		http.Error(response, err.Error(), http.StatusInternalServerError)
	}
}
