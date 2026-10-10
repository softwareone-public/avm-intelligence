package provider

import (
	"context"
	"encoding/json"
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
		switch {
		case request.URL.Path == "/repos/hashicorp/terraform-provider-azurerm/releases/tags/v4.2.0":
			writeCollectorJSON(response, map[string]string{"tag_name": "v4.2.0", "name": "4.2.0 release", "body": "azurerm_resource_group behavior changed"})
		case request.URL.Path == "/repos/hashicorp/terraform-provider-azurerm/contents" && request.URL.Query().Get("ref") == "v4.2.0":
			writeCollectorJSON(response, []map[string]string{
				{"name": "CHANGELOG-v1.md", "type": "file"}, {"name": "CHANGELOG.md", "type": "file"}, {"name": "README.md", "type": "file"},
			})
		case request.URL.Path == "/hashicorp/terraform-provider-azurerm/v4.2.0/CHANGELOG.md":
			_, _ = response.Write([]byte("azurerm_resource_group changed in 4.2.0"))
		case request.URL.Path == "/hashicorp/terraform-provider-azurerm/v4.2.0/CHANGELOG-v1.md":
			_, _ = response.Write([]byte("azurerm_resource_group historical details"))
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
	if kinds["provider_changelog"] != 2 {
		t.Fatalf("collector did not preserve the split changelogs: %#v", kinds)
	}
}

func writeCollectorJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(response).Encode(value); err != nil {
		panic(err)
	}
}
