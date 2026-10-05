package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/publish"
)

const catalogTestRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const catalogTestCollectorCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestCatalogCollectorBuildsPinnedCatalogAcrossPages(t *testing.T) {
	server := newPagedCatalogTestServer(t)
	defer server.Close()

	collector := CatalogCollector{
		APIBase: server.URL, RawBase: server.URL + "/raw",
		Commit: catalogTestCollectorCommit,
		Now:    func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) },
	}
	snapshot, err := collector.CollectTypeCatalog(context.Background(), catalogTestPolicy([]string{"resource"}, 10),
		"registry.terraform.io/hashicorp/azurerm", "4.0.0", catalogTestRevision)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Coverage.Complete || snapshot.Coverage.Capabilities["resource"] != "complete" {
		t.Fatalf("coverage = %#v, want complete resource catalog", snapshot.Coverage)
	}
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Name != "azurerm_resource_group" || snapshot.Entries[1].Name != "azurerm_storage_account" {
		t.Fatalf("entries = %#v, want two distinct sorted provider types", snapshot.Entries)
	}
	for _, entry := range snapshot.Entries {
		if !strings.Contains(entry.DocumentationRef, catalogTestRevision) || len(entry.ContentDigest) != 64 {
			t.Fatalf("entry provenance is not pinned: %#v", entry)
		}
	}
	if len(snapshot.SourceDigest) != 64 || snapshot.ObservedAt != "2026-10-04T12:00:00Z" {
		t.Fatalf("snapshot source/time identity = %q / %q", snapshot.SourceDigest, snapshot.ObservedAt)
	}
	if snapshot.Producer.Commit != catalogTestCollectorCommit {
		t.Fatalf("collector commit = %q, want pinned collector commit", snapshot.Producer.Commit)
	}
	proposalRoot, baseDataRoot := t.TempDir(), t.TempDir()
	outputDataRoot := t.TempDir() + "/accepted"
	proposal, err := publish.BuildProviderSnapshotProposal(publish.ProviderSnapshotProposalInput{
		ProposalRoot: proposalRoot, BaseDataRoot: baseDataRoot,
		ExpectedBase: strings.Repeat("d", 40), SourceGeneration: 1, Snapshot: snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = publish.ApplyProposal(proposalRoot, proposal.ExpectedBase, baseDataRoot, outputDataRoot); err != nil {
		t.Fatal(err)
	}
}

type pagedCatalogFixtureHandler struct {
	t      *testing.T
	server *httptest.Server
}

func (handler *pagedCatalogFixtureHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if serveCatalogVersionCommit(response, request) {
		return
	}
	if request.URL.Path == "/repos/hashicorp/terraform-provider-azurerm/contents/docs/resources" {
		if request.URL.Query().Get("ref") != catalogTestRevision {
			handler.t.Errorf("listing revision = %q, want exact commit", request.URL.Query().Get("ref"))
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Query().Get("page") {
		case "1":
			link := fmt.Sprintf("<%s%s?per_page=%d&page=2&ref=%s>; rel=\"next\"", handler.server.URL, request.URL.Path, catalogPageSize, catalogTestRevision)
			response.Header().Set("Link", link)
			_ = json.NewEncoder(response).Encode([]catalogContentEntry{{Path: "docs/resources/azurerm_resource_group.html.markdown", Type: "file"}})
		case "2":
			_ = json.NewEncoder(response).Encode([]catalogContentEntry{{Path: "docs/resources/azurerm_storage_account.md", Type: "file"}})
		default:
			http.Error(response, "unexpected page", http.StatusBadRequest)
		}
		return
	}
	if strings.HasPrefix(request.URL.Path, "/raw/hashicorp/terraform-provider-azurerm/") {
		_, _ = response.Write([]byte("# Provider type fixture\n"))
		return
	}
	http.NotFound(response, request)
}

func newPagedCatalogTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	handler := &pagedCatalogFixtureHandler{t: t}
	server := httptest.NewServer(handler)
	handler.server = server
	return server
}

func TestCatalogCollectorRejectsPaginationBeyondConfiguredLimit(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if serveCatalogVersionCommit(response, request) {
			return
		}
		if request.URL.Path == "/repos/hashicorp/terraform-provider-azurerm/contents/docs/resources" {
			response.Header().Set("Link", fmt.Sprintf("<%s%s?per_page=%d&page=2&ref=%s>; rel=\"next\"", server.URL, request.URL.Path, catalogPageSize, catalogTestRevision))
			_ = json.NewEncoder(response).Encode([]catalogContentEntry{{Path: "docs/resources/azurerm_resource_group.md", Type: "file"}})
			return
		}
		if strings.HasPrefix(request.URL.Path, "/raw/hashicorp/terraform-provider-azurerm/") {
			_, _ = response.Write([]byte("# Fixture\n"))
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()

	policy := catalogTestPolicy([]string{"resource"}, 1)
	snapshot, err := (CatalogCollector{APIBase: server.URL, RawBase: server.URL + "/raw", Commit: catalogTestCollectorCommit}).CollectTypeCatalog(
		context.Background(), policy, "registry.terraform.io/hashicorp/azurerm", "4.0.0", catalogTestRevision,
	)
	if err == nil {
		t.Fatalf("snapshot = %#v, want an error for truncated pagination", snapshot)
	}
	if snapshot.ProviderAddress != "" {
		t.Fatalf("truncated collection returned a proposal-ready snapshot: %#v", snapshot)
	}
}

func TestCatalogCollectorMarksMissingKindUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if serveCatalogVersionCommit(response, request) {
			return
		}
		if strings.HasSuffix(request.URL.Path, "/docs/actions") {
			http.NotFound(response, request)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/docs/resources") {
			_ = json.NewEncoder(response).Encode([]catalogContentEntry{{Path: "docs/resources/azurerm_resource_group.md", Type: "file"}})
			return
		}
		if strings.HasPrefix(request.URL.Path, "/raw/hashicorp/terraform-provider-azurerm/") {
			_, _ = response.Write([]byte("# Fixture\n"))
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()

	snapshot, err := (CatalogCollector{APIBase: server.URL, RawBase: server.URL + "/raw", Commit: catalogTestCollectorCommit}).CollectTypeCatalog(
		context.Background(), catalogTestPolicy([]string{"action", "resource"}, 10),
		"registry.terraform.io/hashicorp/azurerm", "4.0.0", catalogTestRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Coverage.Complete || snapshot.Coverage.Capabilities["action"] != "unavailable" || snapshot.Coverage.Capabilities["resource"] != "complete" {
		t.Fatalf("coverage = %#v, want unavailable action and complete resource", snapshot.Coverage)
	}
}

func TestCatalogCollectorKeepsSameNameAcrossKinds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if serveCatalogVersionCommit(response, request) {
			return
		}
		if strings.HasSuffix(request.URL.Path, "/docs/actions") {
			_ = json.NewEncoder(response).Encode([]catalogContentEntry{{Path: "docs/actions/azurerm_shared_type.md", Type: "file"}})
			return
		}
		if strings.HasSuffix(request.URL.Path, "/docs/resources") {
			_ = json.NewEncoder(response).Encode([]catalogContentEntry{{Path: "docs/resources/azurerm_shared_type.md", Type: "file"}})
			return
		}
		if strings.HasPrefix(request.URL.Path, "/raw/hashicorp/terraform-provider-azurerm/") {
			_, _ = response.Write([]byte("# Fixture\n"))
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	snapshot, err := (CatalogCollector{APIBase: server.URL, RawBase: server.URL + "/raw", Commit: catalogTestCollectorCommit}).CollectTypeCatalog(
		context.Background(), catalogTestPolicy([]string{"action", "resource"}, 10),
		"registry.terraform.io/hashicorp/azurerm", "4.0.0", catalogTestRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Coverage.Complete || len(snapshot.Entries) != 2 || snapshot.Entries[0].Name != snapshot.Entries[1].Name || snapshot.Entries[0].Kind == snapshot.Entries[1].Kind {
		t.Fatalf("cross-kind entries = %#v, want same name under distinct complete kinds", snapshot.Entries)
	}
}

func TestCatalogCollectorRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if serveCatalogVersionCommit(response, request) {
			return
		}
		_, _ = response.Write([]byte(strings.Repeat("x", 65)))
	}))
	defer server.Close()
	policy := catalogTestPolicy([]string{"resource"}, 10)
	policy.Providers[0].Limits.MaxResponseBytes = 64
	policy.Providers[0].Limits.MaxTotalBytes = 1024
	snapshot, err := (CatalogCollector{APIBase: server.URL, RawBase: server.URL + "/raw", Commit: catalogTestCollectorCommit}).CollectTypeCatalog(
		context.Background(), policy, "registry.terraform.io/hashicorp/azurerm", "4.0.0", catalogTestRevision,
	)
	if err == nil || snapshot.ProviderAddress != "" {
		t.Fatalf("snapshot/error = %#v / %v, want an error and no partial snapshot", snapshot, err)
	}
}

func TestCatalogCollectorRejectsVersionRevisionMismatch(t *testing.T) {
	listingRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if serveCatalogVersionCommit(response, request) {
			return
		}
		if strings.Contains(request.URL.Path, "/contents/") {
			listingRequests++
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	snapshot, err := (CatalogCollector{APIBase: server.URL, RawBase: server.URL + "/raw", Commit: catalogTestCollectorCommit}).CollectTypeCatalog(
		context.Background(), catalogTestPolicy([]string{"resource"}, 10),
		"registry.terraform.io/hashicorp/azurerm", "4.0.0", strings.Repeat("c", 40),
	)
	if err == nil || snapshot.ProviderAddress != "" {
		t.Fatalf("snapshot/error = %#v / %v, want version-pin rejection", snapshot, err)
	}
	if listingRequests != 0 {
		t.Fatalf("catalog requests = %d, want zero after version mismatch", listingRequests)
	}
}

func TestValidateCatalogNextURLRejectsUnpinnedLinks(t *testing.T) {
	current := "https://api.github.com/repos/hashicorp/terraform-provider-azurerm/contents/docs/resources?ref=" + catalogTestRevision + "&per_page=100&page=1"
	tests := map[string]string{
		"foreign host":     "https://example.com/repos/hashicorp/terraform-provider-azurerm/contents/docs/resources?ref=" + catalogTestRevision + "&per_page=100&page=2",
		"changed revision": "https://api.github.com/repos/hashicorp/terraform-provider-azurerm/contents/docs/resources?ref=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb&per_page=100&page=2",
		"skipped page":     "https://api.github.com/repos/hashicorp/terraform-provider-azurerm/contents/docs/resources?ref=" + catalogTestRevision + "&per_page=100&page=3",
		"extra query":      "https://api.github.com/repos/hashicorp/terraform-provider-azurerm/contents/docs/resources?ref=" + catalogTestRevision + "&per_page=100&page=2&redirect=1",
	}
	for name, nextURL := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := validateCatalogNextURL(nextURL, current, catalogTestRevision, 2); err == nil {
				t.Fatal("unpinned provider pagination link was accepted")
			}
		})
	}
}

func catalogTestPolicy(kinds []string, maxPages int) CollectionPolicy {
	return CollectionPolicy{
		SchemaVersion: 1,
		Providers: []ProviderPolicy{{
			Address: "registry.terraform.io/hashicorp/azurerm", SourceRepository: "hashicorp/terraform-provider-azurerm",
			SourceLicense: "MPL-2.0", TrackMajors: []uint64{4, 5}, CollectionKinds: kinds,
			Limits: CollectionLimits{
				MaxReleases: 10, MaxPages: maxPages, MaxResponseBytes: 4096,
				MaxTotalBytes: 1 << 20, MaxWallTimeSeconds: 10,
			},
		}},
	}
}

func serveCatalogVersionCommit(response http.ResponseWriter, request *http.Request) bool {
	if !strings.HasSuffix(request.URL.Path, "/commits/v4.0.0") && !strings.HasSuffix(request.URL.Path, "/commits/4.0.0") {
		return false
	}
	_ = json.NewEncoder(response).Encode(struct {
		SHA string `json:"sha"`
	}{SHA: catalogTestRevision})
	return true
}
