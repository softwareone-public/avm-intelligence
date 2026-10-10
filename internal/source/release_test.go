package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
)

const resolverTestRepository = "Azure/terraform-azurerm-avm-res-resources-resourcegroup"

func TestResolveStableReleasesAfterKeepsAllReleasesSinceBaseline(t *testing.T) {
	module := resourceModule()
	commitFor := func(version int) string { return fmt.Sprintf("%040x", version) }
	releases := make([]releaseRecord, 0, 102)
	for version := 0; version < 100; version++ {
		releases = append(releases, releaseRecord{TagName: fmt.Sprintf("v1.0.%d", version), PublishedAt: "2026-01-01T00:00:00Z"})
	}
	releases = append(releases,
		releaseRecord{TagName: "v1.0.100", PublishedAt: "2026-02-01T00:00:00Z"},
		releaseRecord{TagName: "v1.0.101", PublishedAt: "2026-03-01T00:00:00Z"},
	)
	encodedFirst, _ := json.Marshal(releases[:100])
	encodedSecond, _ := json.Marshal(releases[100:])
	var releasePages int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/releases") {
			releasePages++
			if request.URL.Query().Get("per_page") != "100" {
				t.Errorf("per_page = %q", request.URL.Query().Get("per_page"))
			}
			if request.URL.Query().Get("page") == "1" {
				_, _ = writer.Write(encodedFirst)
			} else if request.URL.Query().Get("page") == "2" {
				_, _ = writer.Write(encodedSecond)
			} else {
				t.Errorf("unexpected release page %q", request.URL.Query().Get("page"))
				http.NotFound(writer, request)
			}
			return
		}
		tag := strings.TrimPrefix(request.URL.Path, "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/git/ref/tags/")
		if tag == request.URL.Path {
			t.Errorf("unexpected URL %s", request.URL)
			http.NotFound(writer, request)
			return
		}
		version := 99
		for candidate := 100; candidate <= 101; candidate++ {
			if tag == fmt.Sprintf("v1.0.%d", candidate) {
				version = candidate
			}
		}
		if tag == "1.0.99" || tag == "1.0.100" || tag == "1.0.101" {
			http.NotFound(writer, request)
			return
		}
		if version == 99 && tag != "v1.0.99" {
			t.Errorf("unexpected tag %q", tag)
			http.NotFound(writer, request)
			return
		}
		_, _ = fmt.Fprintf(writer, `{"object":{"type":"commit","sha":%q}}`, commitFor(version))
	}))
	defer server.Close()
	baseline := ResolvedSource{ModuleID: module.ID, Repository: resolverTestRepository, Version: "v1.0.99", Tag: "v1.0.99", Commit: commitFor(99)}
	resolved, err := (Resolver{APIBase: server.URL}).ResolveStableReleasesAfter(context.Background(), module, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if releasePages != 2 {
		t.Fatalf("release pages = %d, want 2", releasePages)
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved count = %d, want 2: %#v", len(resolved), resolved)
	}
	for index, wantVersion := range []string{"v1.0.100", "v1.0.101"} {
		if resolved[index].Version != wantVersion || resolved[index].Commit != commitFor(100+index) {
			t.Fatalf("resolved[%d] = %#v", index, resolved[index])
		}
	}
}

func TestResolveLatestReusesTagsOnlyWithinOneResolution(t *testing.T) {
	commit := strings.Repeat("a", 40)
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		counts[request.URL.Path]++
		switch {
		case strings.HasSuffix(request.URL.Path, "/releases"):
			_, _ = writer.Write([]byte(`[{"tag_name":"v1.0.0","published_at":"2026-01-01T00:00:00Z"}]`))
		case strings.HasSuffix(request.URL.Path, "/v1.0.0"):
			_, _ = fmt.Fprintf(writer, `{"object":{"type":"commit","sha":%q}}`, commit)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	resolver := Resolver{APIBase: server.URL}
	pin, err := resolver.ResolveLatest(context.Background(), resourceModule(), nil)
	if err != nil {
		t.Fatal(err)
	}
	counts = map[string]int{}
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := resolver.ResolveLatest(context.Background(), resourceModule(), &pin); err != nil {
			t.Fatal(err)
		}
		for path, count := range counts {
			if count != attempt {
				t.Fatalf("tag or release read duplicated/cached across calls: %s %d want %d", path, count, attempt)
			}
		}
	}
	commit = strings.Repeat("b", 40)
	if _, err := resolver.ResolveLatest(context.Background(), resourceModule(), &pin); !errors.Is(err, ErrMovedTag) {
		t.Fatalf("cache hid moved tag: %v", err)
	}
}

func TestResolveLatestStableReleaseAndAnnotatedTag(t *testing.T) {
	t.Parallel()
	commit := strings.Repeat("a", 40)
	tagObject := strings.Repeat("b", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization header = %q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/releases":
			_, _ = writer.Write([]byte(`[{"tag_name":"v0.3.0","html_url":"https://github.com/Azure/repo/releases/tag/v0.3.0","published_at":"2025-01-01T00:00:00Z"},{"tag_name":"v0.5.0-rc.1","prerelease":true,"published_at":"2026-01-01T00:00:00Z"},{"tag_name":"v0.4.0","html_url":"https://github.com/Azure/repo/releases/tag/v0.4.0","published_at":"2025-06-01T00:00:00Z"},{"tag_name":"v9.0.0","draft":true,"published_at":"2026-01-01T00:00:00Z"}]`))
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/git/ref/tags/v0.4.0":
			_, _ = fmt.Fprintf(writer, `{"object":{"type":"tag","sha":%q}}`, tagObject)
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/git/tags/" + tagObject:
			_, _ = fmt.Fprintf(writer, `{"object":{"type":"commit","sha":%q}}`, commit)
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/git/ref/tags/0.4.0":
			http.NotFound(writer, request)
		default:
			t.Errorf("unexpected URL %s", request.URL.String())
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	resolved, err := (Resolver{APIBase: server.URL, Token: "test-token"}).ResolveLatest(context.Background(), resourceModule(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Version != "v0.4.0" || resolved.Tag != "v0.4.0" || resolved.Commit != commit {
		t.Fatalf("resolved = %#v", resolved)
	}
	if len(resolved.Aliases) != 1 || resolved.Aliases[0] != (TagObservation{Tag: "v0.4.0", Commit: commit}) {
		t.Fatalf("resolved aliases = %#v", resolved.Aliases)
	}
	if resolved.ArchiveURL != "https://github.com/Azure/terraform-azurerm-avm-res-resources-resourcegroup/archive/"+commit+".tar.gz" {
		t.Fatalf("archive URL = %s", resolved.ArchiveURL)
	}
}

func TestResolveLatestCanonicalizesBareSemVerRelease(t *testing.T) {
	t.Parallel()
	commit := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/releases":
			_, _ = writer.Write([]byte(`[{"tag_name":"0.5.0","html_url":"https://github.com/Azure/repo/releases/tag/0.5.0","published_at":"2026-01-01T00:00:00Z"}]`))
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/git/ref/tags/0.5.0",
			"/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/git/ref/tags/v0.5.0":
			_, _ = fmt.Fprintf(writer, `{"object":{"type":"commit","sha":%q}}`, commit)
		default:
			t.Errorf("unexpected URL %s", request.URL.String())
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	resolved, err := (Resolver{APIBase: server.URL}).ResolveLatest(context.Background(), resourceModule(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Version != "v0.5.0" || resolved.Tag != "0.5.0" || resolved.Commit != commit {
		t.Fatalf("resolved = %#v", resolved)
	}
	if len(resolved.Aliases) != 2 || resolved.Aliases[0] != (TagObservation{Tag: "0.5.0", Commit: commit}) || resolved.Aliases[1] != (TagObservation{Tag: "v0.5.0", Commit: commit}) {
		t.Fatalf("resolved aliases = %#v", resolved.Aliases)
	}
}

func TestResolveLatestRejectsConflictingTagAliases(t *testing.T) {
	t.Parallel()
	commitA := strings.Repeat("a", 40)
	commitB := strings.Repeat("b", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/releases":
			_, _ = writer.Write([]byte(`[{"tag_name":"v0.4.0","published_at":"2025-06-01T00:00:00Z"}]`))
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/git/ref/tags/v0.4.0":
			_, _ = fmt.Fprintf(writer, `{"object":{"type":"commit","sha":%q}}`, commitA)
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/git/ref/tags/0.4.0":
			_, _ = fmt.Fprintf(writer, `{"object":{"type":"commit","sha":%q}}`, commitB)
		default:
			t.Errorf("unexpected URL %s", request.URL.String())
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	if _, err := (Resolver{APIBase: server.URL}).ResolveLatest(context.Background(), resourceModule(), nil); err == nil || !strings.Contains(err.Error(), "different commits") {
		t.Fatalf("alias conflict error = %v", err)
	}
}

func TestResolveLatestRejectsMovedAcceptedTag(t *testing.T) {
	t.Parallel()
	commit := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/releases":
			_, _ = writer.Write([]byte(`[{"tag_name":"v0.4.0","published_at":"2025-06-01T00:00:00Z"}]`))
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/git/ref/tags/v0.4.0":
			_, _ = fmt.Fprintf(writer, `{"object":{"type":"commit","sha":%q}}`, commit)
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/git/ref/tags/0.4.0":
			http.NotFound(writer, request)
		default:
			t.Errorf("unexpected URL %s", request.URL.String())
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	previous := &ResolvedSource{ModuleID: resourceModule().ID, Repository: resolverTestRepository, Version: "v0.4.0", Tag: "v0.4.0", Commit: strings.Repeat("c", 40)}
	if _, err := (Resolver{APIBase: server.URL}).ResolveLatest(context.Background(), resourceModule(), previous); err == nil || !strings.Contains(err.Error(), "moved accepted release tag") {
		t.Fatalf("moved tag error = %v", err)
	}
}

func TestResolveLatestChecksPreviousTagEvenWhenNewerReleaseExists(t *testing.T) {
	t.Parallel()
	currentCommit := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/git/ref/tags/v0.4.0":
			_, _ = fmt.Fprintf(writer, `{"object":{"type":"commit","sha":%q}}`, currentCommit)
		case "/repos/Azure/terraform-azurerm-avm-res-resources-resourcegroup/git/ref/tags/0.4.0":
			http.NotFound(writer, request)
		default:
			t.Errorf("unexpected URL %s", request.URL.String())
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	previous := &ResolvedSource{ModuleID: resourceModule().ID, Repository: resolverTestRepository, Version: "v0.4.0", Tag: "v0.4.0", Commit: strings.Repeat("c", 40)}
	if _, err := (Resolver{APIBase: server.URL}).ResolveLatest(context.Background(), resourceModule(), previous); err == nil || !strings.Contains(err.Error(), "moved accepted release tag") {
		t.Fatalf("moved previous tag error = %v", err)
	}
}

func TestResolveLatestRequiresStableRelease(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`[{"tag_name":"v0.5.0-rc.1","prerelease":true,"published_at":"2026-01-01T00:00:00Z"}]`))
	}))
	defer server.Close()
	if _, err := (Resolver{APIBase: server.URL}).ResolveLatest(context.Background(), resourceModule(), nil); err == nil || !strings.Contains(err.Error(), "no published stable SemVer release") {
		t.Fatalf("missing stable release error = %v", err)
	}
}

func TestSelectStableReleaseSupportsBareSemVer(t *testing.T) {
	t.Parallel()
	release, err := selectStableRelease([]releaseRecord{{TagName: "0.9.0", PublishedAt: "2026-01-01T00:00:00Z"}, {TagName: "v0.8.0", PublishedAt: "2025-01-01T00:00:00Z"}})
	if err != nil {
		t.Fatal(err)
	}
	if release.TagName != "0.9.0" {
		t.Fatalf("selected tag = %q", release.TagName)
	}
}

func TestSelectStableReleaseRejectsVersionAliasCollision(t *testing.T) {
	t.Parallel()
	releases := []releaseRecord{
		{TagName: "v0.9.0", PublishedAt: "2026-01-01T00:00:00Z"},
		{TagName: "0.9.0", PublishedAt: "2026-01-01T00:00:00Z"},
	}
	if _, err := selectStableRelease(releases); err == nil || !strings.Contains(err.Error(), "ambiguous stable release tags") {
		t.Fatalf("version alias error = %v", err)
	}
}

func resourceModule() catalog.Module {
	return catalog.Module{
		ID: "avm-res-resources-resourcegroup", Type: catalog.Resource, Status: "Available",
		DisplayName: "Resource Group", Description: "AVM resource module",
		RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-resources-resourcegroup",
		RegistryURL:   "https://registry.terraform.io/modules/Azure/avm-res-resources-resourcegroup",
	}
}

func resourceModuleWithID(id string) catalog.Module {
	module := resourceModule()
	module.ID = id
	module.RepositoryURL = "https://github.com/Azure/terraform-" + strings.TrimPrefix(id, "avm-res-")
	module.RegistryURL = "https://registry.terraform.io/modules/Azure/" + id
	return module
}

func testAcceptedState(modules ...catalog.Module) catalog.AcceptedState {
	state := catalog.AcceptedState{
		SchemaVersion: catalog.AcceptedStateSchemaVersion,
		Generation:    1,
		AcceptedAt:    time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		LastFetchedAt: time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC),
		Feeds: []catalog.FeedSnapshot{
			{Kind: catalog.Resource, URL: catalog.OfficialSources[0].URL, SHA256: strings.Repeat("a", 64), ModuleCount: len(modules)},
			{Kind: catalog.Pattern, URL: catalog.OfficialSources[1].URL, SHA256: strings.Repeat("b", 64), ModuleCount: 1},
			{Kind: catalog.Utility, URL: catalog.OfficialSources[2].URL, SHA256: strings.Repeat("c", 64), ModuleCount: 1},
		},
	}
	for _, module := range modules {
		state.Modules = append(state.Modules, catalog.ModuleRecord{Module: module, Active: true, LastSeenGeneration: 1})
	}
	state.Modules = append(state.Modules,
		catalog.ModuleRecord{Module: catalog.Module{ID: "avm-ptn-test", Type: catalog.Pattern, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-avm-ptn-test", DisplayName: "Pattern", Description: "Pattern"}, Active: true, LastSeenGeneration: 1},
		catalog.ModuleRecord{Module: catalog.Module{ID: "avm-utl-test", Type: catalog.Utility, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-avm-utl-test", DisplayName: "Utility", Description: "Utility"}, Active: true, LastSeenGeneration: 1},
	)
	return state
}
