package identity

import (
	"strings"
	"testing"
)

func TestCanonicalModuleRecordPaths(t *testing.T) {
	t.Parallel()
	moduleID := "avm-res-compute-disk"
	sourceSHA := strings.Repeat("a", 40)
	factsID, err := ID("facts", 1, map[string]string{"source": sourceSHA})
	if err != nil {
		t.Fatal(err)
	}
	factsPath, err := ModuleRecordPath(moduleID, sourceSHA, "facts", factsID)
	if err != nil {
		t.Fatal(err)
	}
	wantFactsPath := "data/modules/res/avm-res-compute-disk/revisions/" + sourceSHA + "/facts/" + factsID + ".json"
	if factsPath != wantFactsPath {
		t.Fatalf("facts path = %q, want %q", factsPath, wantFactsPath)
	}
	sourcePath, err := ModuleSourcePath(moduleID, sourceSHA)
	if err != nil {
		t.Fatal(err)
	}
	wantSourcePath := "data/modules/res/avm-res-compute-disk/revisions/" + sourceSHA + "/source.json"
	if sourcePath != wantSourcePath {
		t.Fatalf("source path = %q, want %q", sourcePath, wantSourcePath)
	}
	if _, err = ModuleRecordPath(moduleID, sourceSHA, "legacy-analysis", "legacy-analysis-"+strings.Repeat("b", 64)); err == nil {
		t.Fatal("retired report archive kind was accepted")
	}
	if _, err = ModuleRecordPath(moduleID, sourceSHA, "unknown", factsID); err == nil {
		t.Fatal("unknown module record kind was accepted")
	}
}

func TestCanonicalOtherRecordPaths(t *testing.T) {
	t.Parallel()
	selectionID, err := ID("selection", 1, map[string]string{"module": "avm-res-one"})
	if err != nil {
		t.Fatal(err)
	}
	selectionPath, err := SelectionPath("avm-res-one", "azurerm-4", selectionID)
	if err != nil {
		t.Fatal(err)
	}
	if selectionPath != "data/selections/avm-res-one/azurerm-4/"+selectionID+".json" {
		t.Fatalf("selection path = %q", selectionPath)
	}
	presentationID, err := ID("presentation", 1, map[string]int{"generation": 1})
	if err != nil {
		t.Fatal(err)
	}
	presentationPath, err := PresentationPath(presentationID)
	if err != nil {
		t.Fatal(err)
	}
	if presentationPath != "data/presentation/"+presentationID+".json" {
		t.Fatalf("presentation path = %q", presentationPath)
	}
	snapshotID, err := ID("snapshot", 1, map[string]string{"provider": "azurerm"})
	if err != nil {
		t.Fatal(err)
	}
	providerPath, err := ProviderSnapshotPath("registry.terraform.io/hashicorp/azurerm", "4.10.0", snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	wantProviderPath := "data/providers/registry.terraform.io/hashicorp/azurerm/versions/4.10.0/snapshots/" + snapshotID
	if providerPath != wantProviderPath {
		t.Fatalf("provider path = %q, want %q", providerPath, wantProviderPath)
	}
	corePath, err := CoreFunctionSnapshotPath("1.9.0", snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	if corePath != "data/terraform/core/1.9.0/snapshots/"+snapshotID {
		t.Fatalf("core path = %q", corePath)
	}
}

func TestCanonicalGeneratedIndexPaths(t *testing.T) {
	t.Parallel()
	modulePath, err := ModuleIndexPath("avm-res-compute-disk")
	if err != nil || modulePath != "data/modules/res/avm-res-compute-disk/index.json" {
		t.Fatalf("module index path = %q, err = %v", modulePath, err)
	}
	trackPath, err := TrackIndexPath("azurerm-4")
	if err != nil || trackPath != "data/tracks/azurerm-4/index.json" {
		t.Fatalf("track index path = %q, err = %v", trackPath, err)
	}
	providerPath, err := ProviderIndexPath("registry.terraform.io/hashicorp/azurerm")
	if err != nil || providerPath != "data/providers/registry.terraform.io/hashicorp/azurerm/index.json" {
		t.Fatalf("provider index path = %q, err = %v", providerPath, err)
	}
	if DimensionDependencyIndexPath() != "data/assessment/dependencies.json" || PresentationIndexPath() != "data/presentation/index.json" {
		t.Fatal("fixed assessment or presentation index path changed")
	}
	if _, err = TrackIndexPath("../escape"); err == nil {
		t.Fatal("parent-traversing track context was accepted")
	}
}

func TestCanonicalDataAndStatePathLayout(t *testing.T) {
	t.Parallel()
	for name, expected := range map[string]string{
		"schema": "data/schema.json", "catalog": "data/catalog.json", "dataset": "data/dataset.json",
	} {
		actual, err := CanonicalDataPath(name)
		if err != nil || actual != expected {
			t.Errorf("CanonicalDataPath(%q) = %q, %v; want %q", name, actual, err, expected)
		}
	}
	moduleID := "avm-ptn-test-landing-zone"
	metadataPath, err := ModuleMetadataPath(moduleID)
	if err != nil || metadataPath != "data/modules/ptn/"+moduleID+"/module.json" {
		t.Errorf("module metadata path = %q, %v", metadataPath, err)
	}
	versionsPath, err := ModuleVersionsPath(moduleID)
	if err != nil || versionsPath != "data/modules/ptn/"+moduleID+"/versions.json" {
		t.Errorf("module versions path = %q, %v", versionsPath, err)
	}
	versionObservationID, err := ID("version-observation", 1, map[string]string{"tag": "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	versionObservationPath, err := ModuleVersionObservationPath(moduleID, versionObservationID)
	if err != nil || versionObservationPath != "data/observations/modules/"+moduleID+"/"+versionObservationID+".json" {
		t.Errorf("version observation path = %q, %v", versionObservationPath, err)
	}
	observationID, err := ID("observation", 1, map[string]string{"kind": "issues"})
	if err != nil {
		t.Fatal(err)
	}
	observationPath, err := ModuleObservationPath(moduleID, observationID)
	if err != nil || observationPath != "data/observations/modules/"+moduleID+"/"+observationID+".json" {
		t.Errorf("module observation path = %q, %v", observationPath, err)
	}
	requestID, err := ID("request", 1, map[string]string{"module": moduleID})
	if err != nil {
		t.Fatal(err)
	}
	requestPath, err := StateRequestPath(requestID)
	if err != nil || requestPath != "state/requests/"+requestID+".json" {
		t.Errorf("state request path = %q, %v", requestPath, err)
	}
	if StateDiscoveryPath() != "state/discovery/catalog.json" {
		t.Fatalf("state discovery path = %q", StateDiscoveryPath())
	}
}

func TestValidateReferencesRejectsDanglingAndWrongKind(t *testing.T) {
	t.Parallel()
	factsID, err := ID("facts", 1, map[string]string{"source": "one"})
	if err != nil {
		t.Fatal(err)
	}
	analysisID, err := ID("analysis", 1, map[string]string{"facts": factsID})
	if err != nil {
		t.Fatal(err)
	}
	available := map[string]string{factsID: "facts", analysisID: "analysis"}
	if err = ValidateReferences(available, []Reference{{ID: factsID, Kind: "facts"}}); err != nil {
		t.Fatalf("valid reference rejected: %v", err)
	}
	if err = ValidateReferences(available, []Reference{{ID: factsID, Kind: "analysis"}}); err == nil {
		t.Fatal("wrong-kind reference was accepted")
	}
	missingID, err := ID("facts", 1, map[string]string{"source": "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateReferences(available, []Reference{{ID: missingID, Kind: "facts"}}); err == nil {
		t.Fatal("dangling reference was accepted")
	}
}
