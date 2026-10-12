package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResourceIntelligenceCollectorPinsAllSourceFamiliesToAcceptedVersion(t *testing.T) {
	root, facts, snapshot, policy := acceptedResourceIntelligenceTree(t)
	accepted, err := LoadAcceptedResourceIntelligenceInputs(root, facts.ModuleID, facts.SourceSHA, facts.FactsID, policy, []AcceptedResourceSnapshotReference{{
		ProviderAddress: snapshot.ProviderAddress, ProviderVersion: snapshot.ProviderVersion, SnapshotID: snapshot.SnapshotID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/search/issues" && request.URL.Query().Get("per_page") != "10" {
			http.Error(response, "issue search page size exceeds fixture bound", http.StatusBadRequest)
			return
		}
		if strings.Contains(request.URL.Path, "/git/trees/") && request.URL.Query().Get("recursive") != "" {
			http.Error(response, "recursive tree reads are not allowed", http.StatusBadRequest)
			return
		}
		switch {
		case request.URL.Path == "/repos/hashicorp/terraform-provider-azurerm/releases/tags/v4.2.0":
			writeCollectorJSON(response, map[string]string{"tag_name": "v4.2.0", "name": "4.2.0 release", "body": "azurerm_resource_group behavior changed"})
		case request.URL.Path == "/repos/hashicorp/terraform-provider-azurerm/git/trees/v4.2.0":
			writeCollectorJSON(response, map[string]any{"tree": []providerGitTreeEntry{
				{Path: "CHANGELOG-v1.md", Type: "blob"}, {Path: "CHANGELOG.md", Type: "blob"},
				{Path: "website", Type: "tree", SHA: strings.Repeat("1", 40)}, {Path: "vendor", Type: "tree", SHA: strings.Repeat("2", 40)},
				{Path: "README.md", Type: "blob"},
			}, "truncated": false})
		case request.URL.Path == "/repos/hashicorp/terraform-provider-azurerm/git/trees/"+strings.Repeat("1", 40):
			writeCollectorJSON(response, map[string]any{"tree": []providerGitTreeEntry{{Path: "changelogs", Type: "tree", SHA: strings.Repeat("3", 40)}}, "truncated": false})
		case request.URL.Path == "/repos/hashicorp/terraform-provider-azurerm/git/trees/"+strings.Repeat("3", 40):
			writeCollectorJSON(response, map[string]any{"tree": []providerGitTreeEntry{{Path: "4.2", Type: "tree", SHA: strings.Repeat("4", 40)}}, "truncated": false})
		case request.URL.Path == "/repos/hashicorp/terraform-provider-azurerm/git/trees/"+strings.Repeat("4", 40):
			writeCollectorJSON(response, map[string]any{"tree": []providerGitTreeEntry{{Path: "CHANGELOG-4.2.md", Type: "blob"}}, "truncated": false})
		case request.URL.Path == "/hashicorp/terraform-provider-azurerm/v4.2.0/CHANGELOG.md":
			_, _ = response.Write([]byte("azurerm_resource_group changed in 4.2.0"))
		case request.URL.Path == "/hashicorp/terraform-provider-azurerm/v4.2.0/CHANGELOG-v1.md":
			_, _ = response.Write([]byte("azurerm_resource_group historical details"))
		case request.URL.Path == "/hashicorp/terraform-provider-azurerm/v4.2.0/website/changelogs/4.2/CHANGELOG-4.2.md":
			_, _ = response.Write([]byte("azurerm_resource_group nested release notes"))
		case request.URL.Path == "/search/issues" && strings.Contains(request.URL.Query().Get("q"), "azurerm_resource_group"):
			writeCollectorJSON(response, map[string]any{"items": []map[string]any{{
				"number": 42, "title": "azurerm_resource_group import issue", "body": "Observed while reviewing 4.2.0; affects 4.1.0 only.",
				"html_url": "https://github.com/hashicorp/terraform-provider-azurerm/issues/42",
			}}})
		case request.URL.Path == "/providers/hashicorp/azurerm/4.2.0/docs/guides/4.0-upgrade-guide":
			response.Header().Set("Content-Type", "text/html")
			_, _ = response.Write([]byte("<h1>4.0 upgrade</h1><p>azurerm_resource_group migration notes</p>"))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	collector := ResourceIntelligenceCollector{
		APIBase: server.URL, RawBase: server.URL, RegistryBase: server.URL,
		Now: func() time.Time { return observedAt },
	}
	results, err := collector.Collect(context.Background(), policy, accepted, strings.Repeat("f", 40))
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := NewResourceIntelligenceProposal(policy, accepted, results, observedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err = proposal.Validate(); err != nil {
		t.Fatal(err)
	}
	sources := proposal.Snapshots[0].Resources[0].Sources
	kinds := map[string]int{}
	for _, source := range sources {
		kinds[source.Kind]++
		if source.Version != "4.2.0" || !source.ObservedAt.Equal(observedAt) {
			t.Fatalf("source lost exact accepted-version context: %+v", source)
		}
		if source.Kind == "resource_issue" && source.VersionBasis != "provider_snapshot" {
			t.Fatalf("issue was assigned an affected-version basis: %+v", source)
		}
	}
	for _, kind := range []string{"provider_changelog", "github_release", "resource_issue", "registry_upgrade"} {
		if kinds[kind] == 0 {
			t.Errorf("collector omitted source family %q: %#v", kind, kinds)
		}
	}
	if kinds["provider_changelog"] != 3 {
		t.Fatalf("collector did not preserve root and nested split changelogs: %#v", kinds)
	}
}

func TestResourceIntelligenceCollectorRejectsTruncatedChangelogTree(t *testing.T) {
	_, _, snapshot, collectionPolicy := acceptedResourceIntelligenceTree(t)
	policy, found := providerPolicyForAddress(collectionPolicy, snapshot.ProviderAddress)
	if !found {
		t.Fatal("fixture provider policy is missing")
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/hashicorp/terraform-provider-azurerm/git/trees/v4.2.0" || request.URL.Query().Get("recursive") != "" {
			http.NotFound(response, request)
			return
		}
		writeCollectorJSON(response, map[string]any{
			"tree": []providerGitTreeEntry{{Path: "CHANGELOG.md", Type: "blob"}}, "truncated": true,
		})
	}))
	defer server.Close()
	collector := ResourceIntelligenceCollector{APIBase: server.URL, RawBase: server.URL}

	artifacts, err := collector.collectChangelogs(context.Background(), server.Client(), server.URL, server.URL, policy, "hashicorp", "terraform-provider-azurerm", "v4.2.0", &catalogBudget{})
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated provider changelog tree was not rejected: artifacts=%#v err=%v", artifacts, err)
	}
}

func TestResourceIntelligenceRequestBudgetCountsIssuePagesNotAllRequests(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		_, _ = response.Write([]byte("ok"))
	}))
	defer server.Close()
	collector := ResourceIntelligenceCollector{APIBase: server.URL}
	limits := CollectionLimits{MaxPages: 1, MaxResponseBytes: 16, MaxTotalBytes: 64}
	budget := &catalogBudget{}
	for range 12 {
		if _, _, _, err := collector.get(context.Background(), server.Client(), server.URL+"/source", "text/plain", limits, budget); err != nil {
			t.Fatalf("request %d exceeded a page limit without pagination: %v", requests+1, err)
		}
	}
	if requests != 12 || budget.pages != 0 {
		t.Fatalf("resource requests counted as pagination: requests=%d pages=%d", requests, budget.pages)
	}
}

func TestResourceIntelligenceIssueSearchRetriesUntilGitHubPrimaryRateLimitReset(t *testing.T) {
	_, _, snapshot, collectionPolicy := acceptedResourceIntelligenceTree(t)
	policy, found := providerPolicyForAddress(collectionPolicy, snapshot.ProviderAddress)
	if !found {
		t.Fatal("fixture provider policy is missing")
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/search/issues" {
			http.NotFound(response, request)
			return
		}
		if requests == 1 {
			response.Header().Set("X-RateLimit-Remaining", "0")
			response.Header().Set("X-RateLimit-Reset", "1")
			response.WriteHeader(http.StatusForbidden)
			writeCollectorJSON(response, map[string]string{"message": "API rate limit exceeded"})
			return
		}
		writeCollectorJSON(response, map[string]any{"items": []providerIssue{}})
	}))
	defer server.Close()
	collector := ResourceIntelligenceCollector{APIBase: server.URL}
	issues, err := collector.collectIssues(context.Background(), server.Client(), server.URL, policy, "hashicorp", "terraform-provider-azurerm", "azurerm_resource_group", &catalogBudget{})
	if err != nil {
		t.Fatalf("rate-limited issue search did not recover: %v", err)
	}
	if requests != 2 || len(issues) != 0 {
		t.Fatalf("issue search retry result: requests=%d issues=%d", requests, len(issues))
	}
}

func TestProviderResourceIntelligenceFailureClassDoesNotExposeErrorText(t *testing.T) {
	for _, test := range []struct {
		message string
		want    string
	}{
		{message: "provider response exceeds 1048576 bytes", want: "response_byte_limit"},
		{message: "GitHub issue search exceeds the reviewed page limit", want: "page_limit"},
		{message: "GitHub resource issue search returned HTTP 403 with secret body", want: "source_http_status"},
		{message: "private diagnostic text", want: "source_request_failed"},
	} {
		if got := providerResourceIntelligenceFailureClass(errors.New(test.message)); got != test.want {
			t.Errorf("failure class for %q = %q, want %q", test.message, got, test.want)
		}
	}
}

func TestResourceIntelligenceGitHubTokenIsSentOnlyToConfiguredAPIHost(t *testing.T) {
	var apiAuthorization, rawAuthorization string
	apiServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		apiAuthorization = request.Header.Get("Authorization")
		_, _ = response.Write([]byte("api"))
	}))
	defer apiServer.Close()
	rawServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		rawAuthorization = request.Header.Get("Authorization")
		_, _ = response.Write([]byte("raw"))
	}))
	defer rawServer.Close()
	collector := ResourceIntelligenceCollector{APIBase: apiServer.URL, GitHubToken: "fixture-token"}
	limits := CollectionLimits{MaxPages: 1, MaxResponseBytes: 16, MaxTotalBytes: 64}
	for _, endpoint := range []string{apiServer.URL + "/api", rawServer.URL + "/raw"} {
		if _, _, _, err := collector.get(context.Background(), apiServer.Client(), endpoint, "text/plain", limits, &catalogBudget{}); err != nil {
			t.Fatal(err)
		}
	}
	if apiAuthorization != "Bearer fixture-token" {
		t.Fatalf("GitHub API authorization = %q", apiAuthorization)
	}
	if rawAuthorization != "" {
		t.Fatalf("workflow token was sent to non-API host: %q", rawAuthorization)
	}
}

func TestResourceIntelligenceCollectorRejectsMalformedChangelogTree(t *testing.T) {
	_, _, snapshot, collectionPolicy := acceptedResourceIntelligenceTree(t)
	policy, found := providerPolicyForAddress(collectionPolicy, snapshot.ProviderAddress)
	if !found {
		t.Fatal("fixture provider policy is missing")
	}
	for _, test := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "missing tree", body: `{}`, wantErr: true},
		{name: "null tree", body: `{"tree":null}`, wantErr: true},
		{name: "null envelope", body: `null`, wantErr: true},
		{name: "non-array tree", body: `{"tree":{}}`, wantErr: true},
		{name: "missing truncation status", body: `{"tree":[]}`, wantErr: true},
		{name: "nested path escapes directory", body: `{"tree":[{"path":"../CHANGELOG.md","type":"blob"}],"truncated":false}`, wantErr: true},
		{name: "empty complete tree", body: `{"tree":[],"truncated":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/repos/hashicorp/terraform-provider-azurerm/git/trees/v4.2.0" || request.URL.Query().Get("recursive") != "" {
					http.NotFound(response, request)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			collector := ResourceIntelligenceCollector{APIBase: server.URL, RawBase: server.URL}

			artifacts, err := collector.collectChangelogs(context.Background(), server.Client(), server.URL, server.URL, policy, "hashicorp", "terraform-provider-azurerm", "v4.2.0", &catalogBudget{})
			if test.wantErr && err == nil {
				t.Fatalf("malformed changelog tree was accepted: %#v", artifacts)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("valid empty changelog tree failed: %v", err)
			}
			if !test.wantErr && len(artifacts) != 0 {
				t.Fatalf("valid empty changelog tree returned artifacts: %#v", artifacts)
			}
		})
	}
}

func TestIsProviderChangelogPath(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
	}{
		{path: "CHANGELOG.md", want: true},
		{path: "website/docs/Changelog-4.2.MARKDOWN", want: true},
		{path: "docs/changelog-release.txt", want: true},
		{path: "vendor/sdk/CHANGELOG.md", want: false},
		{path: "README.md", want: false},
		{path: "../CHANGELOG.md", want: false},
		{path: "docs/../CHANGELOG.md", want: false},
		{path: "/CHANGELOG.md", want: false},
		{path: "docs/CHANGELOG.yaml", want: false},
	} {
		t.Run(test.path, func(t *testing.T) {
			if got := isProviderChangelogPath(test.path); got != test.want {
				t.Fatalf("isProviderChangelogPath(%q) = %t, want %t", test.path, got, test.want)
			}
		})
	}
}

func writeCollectorJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		panic(err)
	}
}
