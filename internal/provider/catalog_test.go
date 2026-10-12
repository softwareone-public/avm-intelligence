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

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
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

func TestCatalogCollectorReadsAzureRMWebsiteDocumentationLayout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if serveCatalogVersionCommit(response, request) {
			return
		}
		switch request.URL.Path {
		case "/repos/hashicorp/terraform-provider-azurerm/contents/website/docs/r":
			_ = json.NewEncoder(response).Encode([]catalogContentEntry{{Path: "website/docs/r/resource_group.html.markdown", Type: "file"}})
		case "/repos/hashicorp/terraform-provider-azurerm/contents/website/docs/d":
			_ = json.NewEncoder(response).Encode([]catalogContentEntry{{Path: "website/docs/d/client_config.html.markdown", Type: "file"}})
		default:
			if strings.HasPrefix(request.URL.Path, "/raw/hashicorp/terraform-provider-azurerm/"+catalogTestRevision+"/website/docs/") {
				_, _ = response.Write([]byte("# AzureRM provider type fixture\n"))
				return
			}
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	policy := catalogTestPolicy([]string{"datasource", "resource"}, 10)
	policy.Providers[0].SourceRepository = "hashicorp/terraform-provider-azurerm"
	snapshot, err := (CatalogCollector{APIBase: server.URL, RawBase: server.URL + "/raw", Commit: catalogTestCollectorCommit}).CollectTypeCatalog(
		context.Background(), policy, "registry.terraform.io/hashicorp/azurerm", "4.0.0", catalogTestRevision,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Coverage.Complete || len(snapshot.Entries) != 2 {
		t.Fatalf("coverage=%#v entries=%#v, want complete resource and datasource entries", snapshot.Coverage, snapshot.Entries)
	}
	want := map[string]string{"datasource": "azurerm_client_config", "resource": "azurerm_resource_group"}
	for _, entry := range snapshot.Entries {
		if want[entry.Kind] != entry.Name {
			t.Errorf("entry %#v does not match AzureRM Terraform type identity", entry)
		}
		delete(want, entry.Kind)
	}
	if len(want) != 0 {
		t.Fatalf("missing AzureRM entries: %#v", want)
	}
}

func TestCatalogCollectorReadsOnlyExactUsedTypesAndPreservesUnknownCoverage(t *testing.T) {
	requests := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.Path)
		if strings.HasSuffix(request.URL.Path, "/commits/v4.0.0") || strings.HasSuffix(request.URL.Path, "/commits/4.0.0") {
			_ = json.NewEncoder(response).Encode(struct {
				SHA string `json:"sha"`
			}{catalogTestRevision})
			return
		}
		switch request.URL.Path {
		case "/raw/hashicorp/terraform-provider-azurerm/" + catalogTestRevision + "/website/docs/r/resource_group.html.markdown":
			_, _ = response.Write([]byte("# Resource group documentation\n"))
		case "/raw/hashicorp/terraform-provider-azurerm/" + catalogTestRevision + "/website/docs/r/storage_account.html.markdown",
			"/raw/hashicorp/terraform-provider-azurerm/" + catalogTestRevision + "/website/docs/r/storage_account.markdown",
			"/raw/hashicorp/terraform-provider-azurerm/" + catalogTestRevision + "/website/docs/r/storage_account.md",
			"/raw/hashicorp/terraform-provider-azurerm/" + catalogTestRevision + "/website/docs/r/run_command.html.markdown",
			"/raw/hashicorp/terraform-provider-azurerm/" + catalogTestRevision + "/website/docs/r/run_command.markdown",
			"/raw/hashicorp/terraform-provider-azurerm/" + catalogTestRevision + "/website/docs/r/run_command.md":
			http.NotFound(response, request)
		default:
			if strings.Contains(request.URL.Path, "/contents/") {
				response.WriteHeader(http.StatusInternalServerError)
				return
			}
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	policy := catalogTestPolicy([]string{"action", "resource"}, 10)
	policy.Providers[0].SourceRepository = "hashicorp/terraform-provider-azurerm"
	collector := CatalogCollector{
		APIBase: server.URL, RawBase: server.URL + "/raw", Commit: catalogTestCollectorCommit,
		Now: func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) },
	}
	scope := []ProviderTypeReference{
		{Kind: "resource", Name: "azurerm_storage_account"},
		{Kind: "resource", Name: "azurerm_resource_group"},
		{Kind: "action", Name: "azurerm_run_command"},
	}
	snapshot, err := collector.CollectScopedTypeCatalog(context.Background(), policy,
		"registry.terraform.io/hashicorp/azurerm", "4.0.0", catalogTestRevision, scope)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Coverage.Complete || snapshot.Coverage.Capabilities["resource"] != "partial" || snapshot.Coverage.Capabilities["action"] != "unavailable" {
		t.Fatalf("scoped coverage = %#v, want partial resource and unavailable action coverage", snapshot.Coverage)
	}
	if len(snapshot.Coverage.ScopedTypes) != 3 || snapshot.Coverage.ScopedTypes[0].Status != assessment.ProviderTypeUnavailable ||
		snapshot.Coverage.ScopedTypes[1].Status != assessment.ProviderTypePresent || snapshot.Coverage.ScopedTypes[2].Status != assessment.ProviderTypeAbsent {
		t.Fatalf("scoped type statuses = %#v", snapshot.Coverage.ScopedTypes)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Name != "azurerm_resource_group" {
		t.Fatalf("scoped entries = %#v, want only the documented requested type", snapshot.Entries)
	}
	for _, requestPath := range requests {
		if strings.Contains(requestPath, "/contents/") {
			t.Fatalf("scoped collection requested a whole-catalog listing: %s", requestPath)
		}
	}
	if err = assessment.ValidateProviderSnapshotCoverage(snapshot.Coverage, snapshot.Entries); err != nil {
		t.Fatalf("validate scoped snapshot coverage: %v", err)
	}
}

func TestScopedCatalogDigestBindsExactRequestedTypes(t *testing.T) {
	policy := catalogTestPolicy([]string{"action"}, 10).Providers[0]
	coverage := map[string]string{"action": "unavailable"}
	first, err := scopedCatalogSourceDigest(policy, "4.0.0", catalogTestRevision, coverage,
		[]assessment.ProviderTypeCoverage{{Kind: "action", Name: "azurerm_first", Status: assessment.ProviderTypeUnavailable}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := scopedCatalogSourceDigest(policy, "4.0.0", catalogTestRevision, coverage,
		[]assessment.ProviderTypeCoverage{{Kind: "action", Name: "azurerm_second", Status: assessment.ProviderTypeUnavailable}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("scoped provider source digest did not bind the exact requested type list")
	}
}

func TestScopedCatalogStripsProviderPrefixFromHashicorpDocumentationPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/commits/v2.53.1") || strings.HasSuffix(request.URL.Path, "/commits/2.53.1") {
			_ = json.NewEncoder(response).Encode(struct {
				SHA string `json:"sha"`
			}{SHA: catalogTestRevision})
			return
		}
		switch request.URL.Path {
		case "/raw/hashicorp/terraform-provider-azuread/" + catalogTestRevision + "/docs/resources/application.md",
			"/raw/hashicorp/terraform-provider-azuread/" + catalogTestRevision + "/docs/data-sources/group.md":
			_, _ = response.Write([]byte("# AzureAD documentation\n"))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	policy := catalogTestPolicy([]string{"datasource", "resource"}, 10)
	policy.Providers[0].Address = "registry.terraform.io/hashicorp/azuread"
	policy.Providers[0].SourceRepository = "hashicorp/terraform-provider-azuread"
	policy.Providers[0].TrackMajors = []uint64{2}
	collector := CatalogCollector{APIBase: server.URL, RawBase: server.URL + "/raw", Commit: catalogTestCollectorCommit}
	snapshot, err := collector.CollectScopedTypeCatalog(context.Background(), policy,
		"registry.terraform.io/hashicorp/azuread", "2.53.1", catalogTestRevision, []ProviderTypeReference{
			{Kind: "datasource", Name: "azuread_group"},
			{Kind: "resource", Name: "azuread_application"},
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Name != "azuread_group" || snapshot.Entries[1].Name != "azuread_application" {
		t.Fatalf("scoped AzureAD entries = %#v, want both exact module-used types", snapshot.Entries)
	}
	for _, coverage := range snapshot.Coverage.ScopedTypes {
		if coverage.Status != assessment.ProviderTypePresent {
			t.Fatalf("scoped AzureAD coverage = %#v, want every exact type present", snapshot.Coverage.ScopedTypes)
		}
	}
}

func TestCatalogCollectorRejectsPinnedRevisionWithNoDocumentedTypes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if serveCatalogVersionCommit(response, request) {
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	_, err := (CatalogCollector{APIBase: server.URL, RawBase: server.URL + "/raw", Commit: catalogTestCollectorCommit}).CollectTypeCatalog(
		context.Background(), catalogTestPolicy([]string{"resource"}, 10),
		"registry.terraform.io/hashicorp/azurerm", "4.0.0", catalogTestRevision,
	)
	if err == nil || !strings.Contains(err.Error(), "contains no documented types") {
		t.Fatalf("empty catalog error = %v, want fail-closed no-types error", err)
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
	if request.URL.Path == "/repos/example/terraform-provider-azurerm/contents/docs/resources" {
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
	if strings.HasPrefix(request.URL.Path, "/raw/example/terraform-provider-azurerm/") {
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
		if request.URL.Path == "/repos/example/terraform-provider-azurerm/contents/docs/resources" {
			response.Header().Set("Link", fmt.Sprintf("<%s%s?per_page=%d&page=2&ref=%s>; rel=\"next\"", server.URL, request.URL.Path, catalogPageSize, catalogTestRevision))
			_ = json.NewEncoder(response).Encode([]catalogContentEntry{{Path: "docs/resources/azurerm_resource_group.md", Type: "file"}})
			return
		}
		if strings.HasPrefix(request.URL.Path, "/raw/example/terraform-provider-azurerm/") {
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
		if strings.HasPrefix(request.URL.Path, "/raw/example/terraform-provider-azurerm/") {
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
		if strings.HasPrefix(request.URL.Path, "/raw/example/terraform-provider-azurerm/") {
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

func TestCatalogCollectorAcceptsMissingUnprefixedReleaseAlias(t *testing.T) {
	listingRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/commits/v4.0.0"):
			_ = json.NewEncoder(response).Encode(struct {
				SHA string `json:"sha"`
			}{catalogTestRevision})
		case strings.HasSuffix(request.URL.Path, "/commits/4.0.0"):
			response.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = response.Write([]byte(`{"message":"No commit found for SHA: 4.0.0"}`))
		case request.URL.Path == "/repos/example/terraform-provider-azurerm/contents/docs/resources":
			listingRequests++
			_ = json.NewEncoder(response).Encode([]catalogContentEntry{{Path: "docs/resources/azurerm_resource_group.md", Type: "file"}})
		case strings.HasPrefix(request.URL.Path, "/raw/example/terraform-provider-azurerm/"):
			_, _ = response.Write([]byte("# Resource fixture\n"))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	snapshot, err := (CatalogCollector{APIBase: server.URL, RawBase: server.URL + "/raw", Commit: catalogTestCollectorCommit}).CollectTypeCatalog(
		context.Background(), catalogTestPolicy([]string{"resource"}, 10),
		"registry.terraform.io/hashicorp/azurerm", "4.0.0", catalogTestRevision,
	)
	if err != nil {
		t.Fatalf("collect with absent optional bare tag alias: %v", err)
	}
	if listingRequests != 1 || snapshot.ProviderVersion != "4.0.0" {
		t.Fatalf("listing requests=%d snapshot version=%q, want one listing for the pinned release", listingRequests, snapshot.ProviderVersion)
	}
}

func TestCatalogCollectorRejectsUnexpectedUnprocessableTagResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/commits/v4.0.0") {
			_ = json.NewEncoder(response).Encode(struct {
				SHA string `json:"sha"`
			}{catalogTestRevision})
			return
		}
		if strings.HasSuffix(request.URL.Path, "/commits/4.0.0") {
			response.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = response.Write([]byte(`{"message":"Validation Failed"}`))
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()

	if _, err := (CatalogCollector{APIBase: server.URL, RawBase: server.URL + "/raw", Commit: catalogTestCollectorCommit}).CollectTypeCatalog(
		context.Background(), catalogTestPolicy([]string{"resource"}, 10),
		"registry.terraform.io/hashicorp/azurerm", "4.0.0", catalogTestRevision,
	); err == nil || !strings.Contains(err.Error(), "returned HTTP 422") {
		t.Fatalf("unexpected 422 response error = %v, want fail-closed API error", err)
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
			Address: "registry.terraform.io/hashicorp/azurerm", SourceRepository: "example/terraform-provider-azurerm",
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
