package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/softwareone-public/avm-intelligence/internal/provider"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
)

func TestProviderCollectionRejectsInvalidInputsBeforeNetwork(t *testing.T) {
	const commit = "323f987d7e7bcc31aa5a6436a7f89438c271f7ac"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	for _, name := range []string{"missing-mode", "missing-pin", "invalid-generation", "invalid-time", "cache-in-data", "proposal-in-data", "existing-output"} {
		t.Run(name, func(t *testing.T) {
			dataRoot := t.TempDir()
			options := providerCollectionOptions{
				collect: true, policyPath: "config/providers.yaml", address: "registry.terraform.io/hashicorp/azurerm",
				version: "4.0.0", revision: commit, collectorCommit: commit, expectedBase: commit,
				observedAt: "2026-10-05T12:00:00Z", generation: 1,
				cacheRoot: t.TempDir(), proposalRoot: filepath.Join(t.TempDir(), "proposal"),
			}
			switch name {
			case "missing-mode":
				options.collect = false
			case "missing-pin":
				options.revision = ""
			case "invalid-generation":
				options.generation = 0
			case "invalid-time":
				options.observedAt = "yesterday"
			case "cache-in-data":
				options.cacheRoot = dataRoot
			case "proposal-in-data":
				options.proposalRoot = filepath.Join(dataRoot, "proposal")
			case "existing-output":
				options.proposalRoot = t.TempDir()
			}
			handled, err := handleProviderCollection(context.Background(), options, dataRoot, provider.CatalogCollector{APIBase: server.URL, RawBase: server.URL})
			if !handled || err == nil {
				t.Fatalf("handled=%v error=%v, want rejection", handled, err)
			}
			if requests != 0 {
				t.Fatal("invalid collection made a network request")
			}
		})
	}
}

func TestProviderCollectionRejectsAliasedDataCache(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	err := validateProviderCollectionRoots(dataRoot, filepath.Join(alias, "data"), filepath.Join(t.TempDir(), "proposal"))
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("got %v, want aliased-directory overlap rejection", err)
	}
}

func TestProviderCollectionCommandRejectsMixedModes(t *testing.T) {
	for _, args := range [][]string{
		{"--provider-address", "registry.terraform.io/hashicorp/azurerm"},
		{"--collect-provider-catalog", "--build-site"},
		{"--collect-provider-catalog", "--import-evidence-receipt"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("run(%v) accepted incomplete or mixed provider collection options", args)
		}
	}
	if _, err := handleProviderCollectionCommand(flag.NewFlagSet("factory", flag.ContinueOnError), providerCollectionOptions{
		collect: true, collectResourceIntelligence: true,
	}, t.TempDir()); err == nil {
		t.Fatal("provider catalog and resource intelligence collection were combined")
	}
}

func TestLoadProviderFactsScopeRequiresOneStrictJSONReferenceList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "facts-scope.json")
	valid := `[{"module_id":"avm-res-example","source_sha":"` + strings.Repeat("a", 40) + `","facts_id":"facts-` + strings.Repeat("b", 64) + `"}]`
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	references, err := loadProviderFactsScope(path)
	if err != nil || len(references) != 1 || references[0].ModuleID != "avm-res-example" {
		t.Fatalf("loaded provider facts scope = %#v, error = %v", references, err)
	}
	for _, invalid := range []string{
		`[{"module_id":"avm-res-example","source_sha":"` + strings.Repeat("a", 40) + `","facts_id":"facts-` + strings.Repeat("b", 64) + `","extra":true}]`,
		valid + ` []`,
		`{}`,
	} {
		if err = os.WriteFile(path, []byte(invalid), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err = loadProviderFactsScope(path); err == nil {
			t.Fatalf("accepted invalid provider facts scope %q", invalid)
		}
	}
}

func TestProviderResourceIntelligenceCommandRequiresExactAcceptedInputs(t *testing.T) {
	const commit = "323f987d7e7bcc31aa5a6436a7f89438c271f7ac"
	root := t.TempDir()
	cacheRoot := filepath.Join(root, "cache")
	if err := os.Mkdir(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	options := providerCollectionOptions{
		collectResourceIntelligence: true, policyPath: "config/providers.yaml",
		address: "registry.terraform.io/hashicorp/azurerm", version: "4.0.0",
		moduleID: "res.test", sourceSHA: strings.Repeat("a", 40), factsID: "facts-" + strings.Repeat("b", 64),
		snapshotID: "snapshot-" + strings.Repeat("c", 64), collectorCommit: commit, expectedBase: commit,
		observedAt: "2026-10-10T12:00:00Z", generation: 1, cacheRoot: cacheRoot,
		proposalRoot: filepath.Join(root, "proposal"),
	}
	if _, err := validateProviderResourceIntelligenceOptions(options, filepath.Join(root, "data")); err == nil {
		t.Fatal("resource intelligence collection accepted a missing accepted data root")
	}
	options.factsID = ""
	if _, err := validateProviderResourceIntelligenceOptions(options, filepath.Join(root, "data")); err == nil {
		t.Fatal("resource intelligence collection accepted missing facts identity")
	}
}

func TestProviderResourceIntelligenceCollectionStatsContainOnlyCountsAndBodyBytes(t *testing.T) {
	snapshots := []provider.ResourceIntelligenceSnapshot{{
		Resources: []provider.ResourceIntelligenceResource{
			{Kind: "resource", Name: "azurerm_example", Sources: []provider.ResourceIntelligenceSource{{Body: "first"}, {Body: "second"}}},
			{Kind: "datasource", Name: "data_azurerm_example"},
		},
	}}
	resources, sources, sourceBytes := providerResourceIntelligenceCollectionStats(snapshots)
	if resources != 2 || sources != 2 || sourceBytes != len("first")+len("second") {
		t.Fatalf("collection stats = (%d, %d, %d), want (2, 2, %d)", resources, sources, sourceBytes, len("first")+len("second"))
	}
}

func TestProviderResourceIntelligenceCommandRejectsMixedModes(t *testing.T) {
	err := run([]string{
		"--collect-provider-resource-intelligence", "--build-indexed-site",
		"--provider-policy", "config/providers.yaml", "--provider-address", "registry.terraform.io/hashicorp/azurerm",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("mixed resource intelligence command error = %v", err)
	}
}

func TestProviderCollectionBuildsReviewableProposal(t *testing.T) {
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const commit = "323f987d7e7bcc31aa5a6436a7f89438c271f7ac"
	server, conditionalRequests := newProviderCollectionFixture(t, revision)
	defer server.Close()
	dataRoot := filepath.Join("..", "..", "data")
	options := providerCollectionOptions{
		collect: true, policyPath: filepath.Join("..", "..", "config", "providers.yaml"),
		address: "registry.terraform.io/hashicorp/azurerm", version: "4.0.0", revision: revision,
		collectorCommit: commit, expectedBase: commit, observedAt: "2026-10-05T12:00:00Z", generation: 1,
		cacheRoot: t.TempDir(), proposalRoot: filepath.Join(t.TempDir(), "proposal"),
	}
	handled, err := handleProviderCollection(context.Background(), options, dataRoot, provider.CatalogCollector{
		APIBase: server.URL, RawBase: server.URL + "/raw",
	})
	if !handled || err != nil {
		t.Fatalf("handled=%v error=%v", handled, err)
	}
	proposalData, err := os.ReadFile(filepath.Join(options.proposalRoot, "proposal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var proposal publish.Proposal
	if err = json.Unmarshal(proposalData, &proposal); err != nil {
		t.Fatal(err)
	}
	if proposal.Kind != publish.ProposalProviderSnapshot || proposal.ExpectedBase != commit || proposal.SourceGeneration != 1 {
		t.Fatalf("unexpected proposal identity: %#v", proposal)
	}
	if err = publish.ApplyProposal(options.proposalRoot, commit, dataRoot, filepath.Join(t.TempDir(), "candidate")); err != nil {
		t.Fatal(err)
	}
	cacheEntries, err := os.ReadDir(options.cacheRoot)
	if err != nil || len(cacheEntries) == 0 {
		t.Fatalf("persistent cache entries=%d error=%v", len(cacheEntries), err)
	}
	collector := provider.CatalogCollector{APIBase: server.URL, RawBase: server.URL + "/raw"}
	options.proposalRoot = filepath.Join(t.TempDir(), "revalidated-proposal")
	options.observedAt = "2026-10-05T13:00:00Z"
	options.generation++
	if _, err = handleProviderCollection(context.Background(), options, dataRoot, collector); err != nil {
		t.Fatal(err)
	}
	if *conditionalRequests == 0 {
		t.Fatal("second collection did not conditionally revalidate persistent cache entries")
	}
	options.proposalRoot = filepath.Join(t.TempDir(), "rejected-proposal")
	options.revision = strings.Repeat("b", 40)
	if _, err = handleProviderCollection(context.Background(), options, dataRoot, collector); err == nil {
		t.Fatal("collection accepted a revision that does not match the release tag")
	}
	if _, err = os.Lstat(options.proposalRoot); !os.IsNotExist(err) {
		t.Fatalf("failed collection created proposal output: %v", err)
	}
}

func newProviderCollectionFixture(t *testing.T, revision string) (*httptest.Server, *int) {
	t.Helper()
	conditionalRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("ETag", `"provider-collection-fixture"`)
		if request.Header.Get("If-None-Match") == `"provider-collection-fixture"` {
			conditionalRequests++
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		switch {
		case strings.Contains(request.URL.Path, "/commits/"):
			_ = json.NewEncoder(writer).Encode(map[string]string{"sha": revision})
		case strings.HasSuffix(request.URL.Path, "/contents/website/docs/r"):
			if request.URL.Query().Get("ref") != revision {
				t.Error("provider catalog listing is not commit-pinned")
			}
			_ = json.NewEncoder(writer).Encode([]map[string]string{{"path": "website/docs/r/resource_group.html.markdown", "type": "file"}})
		case strings.Contains(request.URL.Path, "/raw/"):
			_, _ = writer.Write([]byte("# Resource group fixture\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	return server, &conditionalRequests
}
