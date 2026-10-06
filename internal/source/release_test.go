package source

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
)

const resolverTestRepository = "Azure/terraform-azurerm-avm-res-resources-resourcegroup"

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
