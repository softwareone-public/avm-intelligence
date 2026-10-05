package site

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
)

func TestRenderIndexedPortalIncludesBaselineViewsAndEmptyStates(t *testing.T) {
	const moduleID = "avm-res-portal-demo"
	const datasetID = "dataset-" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	outputRoot := t.TempDir()
	asOf := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	records := index.Records{
		CatalogGeneration: 3,
		Generator:         index.Producer{ID: "avm-intelligence-factory", Version: "2.0.0", ContractVersion: 1},
		Modules:           []index.ModuleRecord{{ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-portal-demo", Lifecycle: "active"}},
	}
	indexes := index.Indexes{
		CatalogGeneration: 3,
		Dataset:           index.DatasetManifest{DatasetID: datasetID},
		Modules:           []index.ModuleIndex{{ModuleID: moduleID, CurrentSelections: []index.SelectionPointer{}}},
	}
	presentation := publish.PresentationSnapshot{
		SchemaVersion: 2, RecordType: "presentation_snapshot", ID: "presentation-" + strings.Repeat("b", 64),
		Generation: 1, AsOf: asOf,
		Modules: []publish.PresentationModule{{
			ModuleID: moduleID, Lifecycle: "active", SelectedReceiptIDs: []string{},
			Coverage: []byte(`{"capabilities":{},"complete":false,"observed_at":"2026-10-04T12:00:00Z","reason":"facts unavailable"}`),
		}},
		CollectorHealth: []publish.PresentationCollectorHealth{},
	}
	if err := renderIndexedPortal(outputRoot, records, indexes, presentation, nil); err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"index.html", "quality.html", "provider.html", "currency.html", "issues.html", "activity.html", "operations.html", filepath.Join("modules", moduleID+".html")} {
		if _, err := os.Stat(filepath.Join(outputRoot, page)); err != nil {
			t.Errorf("portal page %s was not written: %v", page, err)
		}
		pageHTML, readErr := os.ReadFile(filepath.Join(outputRoot, page))
		if readErr != nil {
			t.Errorf("portal page %s could not be read: %v", page, readErr)
			continue
		}
		if !strings.Contains(string(pageHTML), `data-table-filter`) {
			t.Errorf("portal page %s omitted its table filter", page)
		}
	}
	indexHTML, err := os.ReadFile(filepath.Join(outputRoot, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"Catalog", "Quality", "Provider / Type", "Currency", "Issues", "Activity", "Operations", datasetID, moduleID} {
		if !strings.Contains(string(indexHTML), label) {
			t.Errorf("catalog page omitted %q", label)
		}
	}
	issuesHTML, err := os.ReadFile(filepath.Join(outputRoot, "issues.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(issuesHTML), "No accepted sanitized issue observations are available.") {
		t.Fatal("issues page omitted its explicit empty state")
	}
}

func TestAcceptedCatalogPresentationProposalBuildsIndexedPortal(t *testing.T) {
	dataRoot := filepath.Join("..", "..", "data")
	proposalRoot := t.TempDir()
	candidateRoot := filepath.Join(t.TempDir(), "accepted-candidate")
	siteRoot := t.TempDir()
	proposal, err := ExportPresentation(PresentationExportInput{
		DataRoot: dataRoot, ProposalRoot: proposalRoot,
		ExpectedBase: "e02c83c9bc9e251efdb8807f8dbeb152f6fded7a",
		AsOf:         time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = publish.ApplyProposal(proposalRoot, proposal.ExpectedBase, dataRoot, candidateRoot); err != nil {
		t.Fatal(err)
	}
	if err = BuildIndexed(candidateRoot, siteRoot); err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"index.html", "quality.html", "provider.html", "currency.html", "issues.html", "activity.html", "operations.html"} {
		if _, err = os.Stat(filepath.Join(siteRoot, page)); err != nil {
			t.Errorf("indexed portal page %s was not generated: %v", page, err)
		}
	}
}

func TestPortalNavigationMarksCurrentPage(t *testing.T) {
	for _, current := range []string{"catalog", "quality", "provider", "currency", "issues", "activity", "operations"} {
		t.Run(current, func(t *testing.T) {
			activeCount := 0
			for _, link := range portalNavigation("../", current) {
				if link.Current {
					activeCount++
				}
				if !strings.HasPrefix(link.Href, "../") {
					t.Errorf("navigation link lost its module-page prefix: %s", link.Href)
				}
			}
			if activeCount != 1 {
				t.Errorf("got %d current navigation links, want one", activeCount)
			}
		})
	}
}
