package site

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

func TestBuildPresentationModulesKeepsCatalogAndSelectedCoverageDistinct(t *testing.T) {
	const selectedModule = "avm-res-selected"
	const catalogOnlyModule = "avm-res-catalog-only"
	const sourceSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	factsID := "facts-" + strings.Repeat("b", 64)
	analysisID := "analysis-" + strings.Repeat("c", 64)
	selectionID := "selection-" + strings.Repeat("d", 64)
	asOf := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	facts := index.FactRecord{
		ID: factsID, ModuleID: selectedModule, SourceSHA: sourceSHA,
		FactsCoverage:         map[string]string{terraform.CapabilityResourceUsage: terraform.CoverageComplete},
		FactsCoverageComplete: true, FactsCoverageObservedAt: asOf.Format(time.RFC3339Nano),
	}
	selectedSource := sourceSHA
	selectedFacts := factsID
	selectedAnalysis := analysisID
	records := index.Records{
		Modules: []index.ModuleRecord{
			{ModuleID: selectedModule, Repository: "Azure/terraform-azurerm-avm-res-selected", Lifecycle: "active"},
			{ModuleID: catalogOnlyModule, Repository: "Azure/terraform-azurerm-avm-res-catalog-only", Lifecycle: "active"},
		},
		Facts: []index.FactRecord{facts},
		VersionIndexes: []index.ModuleVersionsIndexRecord{{
			ModuleID:      catalogOnlyModule,
			OverallLatest: &index.VersionPointer{SourceSHA: strings.Repeat("e", 40)},
		}},
	}
	indexes := index.Indexes{Modules: []index.ModuleIndex{
		{ModuleID: selectedModule, CurrentSelections: []index.SelectionPointer{{
			ModuleID: selectedModule, ContextID: "azurerm-4", Sequence: 1, DesiredGeneration: 1,
			SelectionID: selectionID, AnalysisID: &selectedAnalysis, SourceSHA: &selectedSource,
			FactsID: &selectedFacts, SelectedAt: asOf,
		}}},
		{ModuleID: catalogOnlyModule},
	}}
	modules, err := buildPresentationModules(records, indexes, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 2 || modules[0].ModuleID != catalogOnlyModule || modules[1].ModuleID != selectedModule {
		t.Fatalf("presentation module order = %#v", modules)
	}
	if modules[0].ReasonCode != "analysis-not-selected" || len(modules[0].SelectedReceiptIDs) != 0 {
		t.Fatalf("catalog-only module presentation = %#v", modules[0])
	}
	var selectedCoverage presentationCoverage
	if err = json.Unmarshal(modules[1].Coverage, &selectedCoverage); err != nil {
		t.Fatal(err)
	}
	if !selectedCoverage.Complete || modules[1].SelectedReceiptIDs[0] != selectionID {
		t.Fatalf("selected module presentation = %#v / %#v", modules[1], selectedCoverage)
	}
}
