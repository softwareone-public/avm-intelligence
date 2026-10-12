package site

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
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

func TestProviderPortalLinksVersionPinnedResourceIntelligence(t *testing.T) {
	snapshot := assessment.ProviderSnapshot{
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", ProviderVersion: "4.2.0",
		Entries: []assessment.ProviderEntry{{Kind: "resource", Name: "azurerm_resource_group", IntelligenceSources: []assessment.ProviderIntelligenceSource{{
			Kind: "provider_changelog", URL: "https://github.com/hashicorp/terraform-provider-azurerm/blob/v4.2.0/CHANGELOG.md",
			Version: "4.2.0", VersionBasis: "release_tag", ContentDigest: strings.Repeat("a", 64), Body: "resource group behavior change", ObservedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
		}}}},
	}
	rows := buildProviderPortalRows(index.Records{}, []assessment.ProviderSnapshot{snapshot})
	found := false
	for _, row := range rows {
		if row.Href == snapshot.Entries[0].IntelligenceSources[0].URL {
			found = true
			if row.Values[0] != snapshot.ProviderVersion || row.Values[1] != "provider_changelog" || !strings.Contains(row.Values[3], "4.2.0") {
				t.Fatalf("provider evidence row lost exact version context: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("provider portal omitted the version-pinned intelligence source link")
	}
}

func TestProviderPortalResolvesNormalizedResourceIntelligence(t *testing.T) {
	source := assessment.ProviderIntelligenceSource{
		Kind: "provider_changelog", URL: "https://github.com/hashicorp/terraform-provider-azurerm/blob/v4.2.0/CHANGELOG.md",
		Version: "4.2.0", VersionBasis: "release_tag", ContentDigest: strings.Repeat("a", 64), Body: "release notes",
		ObservedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
	}
	snapshot := assessment.ProviderSnapshot{
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", ProviderVersion: "4.2.0",
		IntelligenceSources: []assessment.ProviderIntelligenceDocument{{SourceID: "provider-source-" + strings.Repeat("b", 64), ProviderIntelligenceSource: source}},
		Entries:             []assessment.ProviderEntry{{Kind: "resource", Name: "azurerm_resource_group", IntelligenceSourceIDs: []string{"provider-source-" + strings.Repeat("b", 64)}}},
	}
	rows := buildProviderPortalRows(index.Records{}, []assessment.ProviderSnapshot{snapshot})
	for _, row := range rows {
		if row.Href == source.URL && row.Values[1] == "provider_changelog" && row.Values[2] == "azurerm_resource_group" {
			return
		}
	}
	t.Fatal("provider portal omitted the normalized version-pinned intelligence source link")
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
	if err = BuildIndexedAt(candidateRoot, siteRoot, time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"index.html", "quality.html", "provider.html", "currency.html", "issues.html", "activity.html", "operations.html"} {
		if _, err = os.Stat(filepath.Join(siteRoot, page)); err != nil {
			t.Errorf("indexed portal page %s was not generated: %v", page, err)
		}
	}
}

func TestBuildCatalogFirstPortalWithoutAcceptedPresentation(t *testing.T) {
	dataRoot := filepath.Join("..", "..", "data")
	firstSiteRoot := t.TempDir()
	secondSiteRoot := t.TempDir()
	asOf := time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC)

	manifestPath := filepath.Join(dataRoot, "dataset.json")
	presentationIndexPath := filepath.Join(dataRoot, "presentation", "index.json")
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	presentationIndexBefore, err := os.ReadFile(presentationIndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = BuildIndexedAt(dataRoot, firstSiteRoot, asOf); err != nil {
		t.Fatalf("build catalog-first portal without accepted presentation: %v", err)
	}
	if err = BuildIndexedAt(dataRoot, secondSiteRoot, asOf); err != nil {
		t.Fatalf("repeat catalog-first portal build: %v", err)
	}
	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	presentationIndexAfter, err := os.ReadFile(presentationIndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(manifestAfter) != string(manifestBefore) || string(presentationIndexAfter) != string(presentationIndexBefore) {
		t.Fatal("catalog-first build changed accepted dataset or presentation index")
	}

	records, err := index.ReadRecords(dataRoot, asOf)
	if err != nil {
		t.Fatal(err)
	}
	modulePages, err := filepath.Glob(filepath.Join(firstSiteRoot, "modules", "*.html"))
	if err != nil {
		t.Fatal(err)
	}
	if len(modulePages) != len(records.Modules) {
		t.Fatalf("rendered %d module pages for %d accepted catalog modules", len(modulePages), len(records.Modules))
	}
	for _, name := range []string{"index.html", "quality.html", "provider.html", "currency.html", "issues.html", "activity.html", "operations.html"} {
		first, readErr := os.ReadFile(filepath.Join(firstSiteRoot, name))
		if readErr != nil {
			t.Fatalf("read generated %s: %v", name, readErr)
		}
		second, readErr := os.ReadFile(filepath.Join(secondSiteRoot, name))
		if readErr != nil {
			t.Fatalf("read repeated %s: %v", name, readErr)
		}
		if string(first) != string(second) {
			t.Errorf("repeated build produced different %s", name)
		}
		if !strings.Contains(string(first), "Catalog-first view derived from accepted records; no accepted presentation snapshot exists.") {
			t.Errorf("%s omitted the derived-view disclosure", name)
		}
	}
	quality, err := os.ReadFile(filepath.Join(firstSiteRoot, "quality.html"))
	if err != nil {
		t.Fatal(err)
	}
	assessmentUnavailable := strings.Contains(string(quality), "No accepted native assessment results are available.")
	if assessmentUnavailable != (len(records.DimensionResults) == 0) {
		t.Fatalf("quality assessment availability disclosure does not match accepted results: unavailable=%t results=%d", assessmentUnavailable, len(records.DimensionResults))
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
