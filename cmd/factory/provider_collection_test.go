package main

import (
	"context"
	"encoding/json"
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
		case strings.HasSuffix(request.URL.Path, "/contents/docs/resources"):
			if request.URL.Query().Get("ref") != revision {
				t.Error("provider catalog listing is not commit-pinned")
			}
			_ = json.NewEncoder(writer).Encode([]map[string]string{{"path": "docs/resources/azurerm_resource_group.md", "type": "file"}})
		case strings.Contains(request.URL.Path, "/raw/"):
			_, _ = writer.Write([]byte("# Resource group fixture\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	return server, &conditionalRequests
}
