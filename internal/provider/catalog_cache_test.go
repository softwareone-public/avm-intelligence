package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryCatalogCacheVerifiesAndCopiesEntries(t *testing.T) {
	cache := &MemoryCatalogCache{}
	entry := CatalogCacheEntry{
		ETag: `"fixture"`, Digest: catalogDigest([]byte("body")), Body: []byte("body"),
		Headers: http.Header{"Link": []string{"<next>; rel=\"next\""}},
	}
	if err := cache.Store(context.Background(), "exact-request", entry); err != nil {
		t.Fatal(err)
	}
	entry.Body[0] = 'X'
	entry.Headers.Set("Link", "changed")

	cached, found, err := cache.Load(context.Background(), "exact-request")
	if err != nil || !found {
		t.Fatalf("Load() = %v, %v; want a cached entry", found, err)
	}
	if string(cached.Body) != "body" || cached.Headers.Get("Link") != `<next>; rel="next"` {
		t.Fatalf("cached entry was not isolated from caller mutation: %#v", cached)
	}
	cached.Body[0] = 'Y'
	reloaded, _, err := cache.Load(context.Background(), "exact-request")
	if err != nil || string(reloaded.Body) != "body" {
		t.Fatalf("Load() returned mutable cache storage: %q, %v", reloaded.Body, err)
	}
}

func TestMemoryCatalogCacheRejectsUnvalidatedEntry(t *testing.T) {
	cache := &MemoryCatalogCache{}
	if err := cache.Store(context.Background(), "exact-request", CatalogCacheEntry{ETag: "fixture", Body: []byte("body"), Digest: "wrong"}); err == nil {
		t.Fatal("cache accepted a body with an incorrect digest")
	}
}

func TestFileCatalogCachePersistsVerifiedEntries(t *testing.T) {
	root := t.TempDir()
	key := catalogRequestCacheKey("https://api.github.com/repos/hashicorp/terraform-provider-azurerm/contents/docs", "application/vnd.github+json")
	entry := CatalogCacheEntry{
		ETag: `"v1"`, Digest: catalogDigest([]byte("body")), Body: []byte("body"),
		Headers: http.Header{"Link": []string{"<next>; rel=\"next\""}},
	}
	if err := (&FileCatalogCache{Root: root}).Store(context.Background(), key, entry); err != nil {
		t.Fatal(err)
	}
	cached, found, err := (&FileCatalogCache{Root: root}).Load(context.Background(), key)
	if err != nil || !found || string(cached.Body) != "body" || cached.Headers.Get("Link") == "" {
		t.Fatalf("persisted cache entry = %#v, found=%v, err=%v", cached, found, err)
	}
	info, err := os.Stat(filepath.Join(root, key+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestCatalogGetReusesVerifiedBodyAndHeadersOnNotModified(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			response.Header().Set("ETag", `"fixture-v1"`)
			response.Header().Set("Link", "<https://api.example.test/page/2>; rel=\"next\"")
			_, _ = response.Write([]byte(`[{"path":"docs/resources/azurerm_example.md","type":"file"}]`))
			return
		}
		if request.Header.Get("If-None-Match") != `"fixture-v1"` {
			t.Errorf("If-None-Match = %q, want cached ETag", request.Header.Get("If-None-Match"))
		}
		response.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	cache := &MemoryCatalogCache{}
	limits := catalogTestPolicy([]string{"resource"}, 10).Providers[0].Limits
	requestURL := server.URL + "/catalog?page=1"
	status, _, firstBody, err := catalogGet(context.Background(), server.Client(), cache, requestURL, "application/vnd.github+json", limits, &catalogBudget{})
	if err != nil || status != http.StatusOK {
		t.Fatalf("first catalogGet() = %d, %v", status, err)
	}
	status, headers, secondBody, err := catalogGet(context.Background(), server.Client(), cache, requestURL, "application/vnd.github+json", limits, &catalogBudget{})
	if err != nil || status != http.StatusOK {
		t.Fatalf("conditional catalogGet() = %d, %v", status, err)
	}
	if string(secondBody) != string(firstBody) || headers.Get("Link") != "<https://api.example.test/page/2>; rel=\"next\"" {
		t.Fatalf("revalidated response = %q with Link %q", secondBody, headers.Get("Link"))
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want 2", requests)
	}
}

func TestCatalogGetRejectsNotModifiedWithoutVerifiedCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()
	limits := catalogTestPolicy([]string{"resource"}, 10).Providers[0].Limits
	_, _, _, err := catalogGet(context.Background(), server.Client(), &MemoryCatalogCache{}, server.URL, "application/vnd.github+json", limits, &catalogBudget{})
	if err == nil || !strings.Contains(err.Error(), "without a verified cached body") {
		t.Fatalf("catalogGet() error = %v, want orphan-304 rejection", err)
	}
}

func TestCatalogRequestCacheKeyIncludesRepresentation(t *testing.T) {
	url := "https://api.github.com/repos/example/provider/contents/docs"
	jsonKey := catalogRequestCacheKey(url, "application/vnd.github+json")
	textKey := catalogRequestCacheKey(url, "text/plain")
	if jsonKey == textKey {
		t.Fatalf("cache key did not vary by representation: %q", jsonKey)
	}
}
