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

func TestProviderMajorContextPathsRemainDistinct(t *testing.T) {
	t.Parallel()
	moduleID := "avm-res-compute-disk"
	selectionID := "selection-" + strings.Repeat("a", 64)
	seen := map[string]bool{}
	for _, contextID := range []string{"azurerm/4", "azurerm/5", "azapi/2", "registry.terraform.io/hashicorp/azurerm/4", "registry.terraform.io/azure/azapi/2"} {
		accepted, err := SelectionPath(moduleID, contextID, selectionID)
		if err != nil || accepted != "data/selections/"+moduleID+"/"+contextID+"/"+selectionID+".json" {
			t.Fatalf("accepted context path was changed: %s, %v", accepted, err)
		}
		logical, err := StateSelectionPath(moduleID, contextID, selectionID)
		if err != nil {
			t.Fatal(err)
		}
		physical, err := OperationalRecordPath(logical, "selection", moduleID)
		if err != nil || physical != "state/modules/res/"+moduleID+"/selections/"+contextID+"/"+selectionID+".json" || seen[physical] {
			t.Fatalf("provider/major context collapsed: %s, %v", physical, err)
		}
		seen[physical] = true
		indexPath, err := TrackIndexPath(contextID)
		if err != nil || indexPath != "data/tracks/"+contextID+"/index.json" {
			t.Fatalf("context index path was changed: %s, %v", indexPath, err)
		}
	}
}

func TestCanonicalDataAndStatePathLayout(t *testing.T) {
	t.Parallel()
	assertCanonicalDataPaths(t)
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
	assertBasicOperationalPaths(t, moduleID)
}

func assertCanonicalDataPaths(t *testing.T) {
	t.Helper()
	for name, expected := range map[string]string{
		"schema": "data/schema.json", "catalog": "data/catalog.json", "dataset": "data/dataset.json",
	} {
		actual, err := CanonicalDataPath(name)
		if err != nil || actual != expected {
			t.Errorf("CanonicalDataPath(%q) = %q, %v; want %q", name, actual, err, expected)
		}
	}
}

func assertBasicOperationalPaths(t *testing.T, moduleID string) {
	t.Helper()
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

func TestStateOperationReceiptPathUsesModuleOwner(t *testing.T) {
	t.Parallel()
	moduleID := "avm-utl-sku-finder"
	operationID := "discovery:module:stable-operation-1"
	logical, err := StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		t.Fatal(err)
	}
	wantLogical := "state/operations/receipts/" + moduleID + "/" + operationID + ".json"
	if logical != wantLogical {
		t.Fatalf("receipt logical path = %q, want %q", logical, wantLogical)
	}
	physical, err := OperationalRecordPath(logical, "operation_receipt", moduleID)
	wantPhysical := "state/modules/utl/" + moduleID + "/operations/receipts/" + operationID + ".json"
	if err != nil || physical != wantPhysical {
		t.Fatalf("receipt physical path = %q, err = %v; want %q", physical, err, wantPhysical)
	}
	if _, err = StateOperationReceiptPath(moduleID, "../escape"); err == nil {
		t.Fatal("malformed operation ID was accepted")
	}
}

func TestStateDiscoveryWorkPathUsesModuleOwner(t *testing.T) {
	t.Parallel()
	moduleID := "avm-utl-sku-finder"
	logical, err := StateDiscoveryWorkPath(moduleID)
	if err != nil || logical != "state/discovery/work/"+moduleID+".json" {
		t.Fatalf("discovery-work logical path = %q, err = %v", logical, err)
	}
	physical, err := OperationalRecordPath(logical, "discovery_work", moduleID)
	wantPhysical := "state/modules/utl/" + moduleID + "/discovery/work.json"
	if err != nil || physical != wantPhysical {
		t.Fatalf("discovery-work physical path = %q, err = %v; want %q", physical, err, wantPhysical)
	}
	if _, err = StateDiscoveryWorkPath("../sku-finder"); err == nil {
		t.Fatal("invalid discovery-work module ID was accepted")
	}
}

func TestProjectionLifecyclePathsResolveToModuleOwner(t *testing.T) {
	t.Parallel()
	moduleID := "avm-ptn-test-landing-zone"
	handoffID, err := ID("stage-handoff", 1, map[string]string{"module": moduleID, "generation": "4"})
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := ID("projection-intent", 1, map[string]string{"module": moduleID, "generation": "4"})
	if err != nil {
		t.Fatal(err)
	}
	publicationAckID, err := ID("module-publication-ack", 1, map[string]string{"module": moduleID, "generation": "4"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		logical   string
		kind      string
		wantLeaf  string
		pathForID func() (string, error)
	}{
		{
			name: "immutable handoff keyed by handoff ID", kind: "stage_handoff",
			wantLeaf:  "projection/handoffs/" + handoffID + ".json",
			pathForID: func() (string, error) { return StateProjectionHandoffPath(moduleID, handoffID) },
		},
		{
			name: "exact projection acknowledgement", kind: "projection_acknowledgement",
			wantLeaf:  "projection/acknowledgements/" + intentID + ".json",
			pathForID: func() (string, error) { return StateProjectionAcknowledgementPath(moduleID, intentID) },
		},
		{
			name: "separate barrier release", kind: "projection_barrier_release",
			wantLeaf:  "projection/barrier-releases/" + intentID + ".json",
			pathForID: func() (string, error) { return StateProjectionBarrierReleasePath(moduleID, intentID) },
		},
		{
			name: "module pages publication acknowledgement", kind: "module_publication_ack",
			wantLeaf:  "publications/acknowledgements/" + publicationAckID + ".json",
			pathForID: func() (string, error) { return StateModulePublicationAcknowledgementPath(moduleID, publicationAckID) },
		},
	}
	owner, err := OperationalModuleRoot(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logical, pathErr := test.pathForID()
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			physical, pathErr := OperationalRecordPath(logical, test.kind, moduleID)
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			if physical != owner+"/"+test.wantLeaf {
				t.Fatalf("physical path = %q, want %q", physical, owner+"/"+test.wantLeaf)
			}
			if _, pathErr = OperationalRecordPath(logical, test.kind, "avm-res-other"); pathErr == nil {
				t.Fatal("logical path was accepted under another module owner")
			}
		})
	}
	if _, err = StateProjectionHandoffPath(moduleID, "stage-handoff-not-content-addressed"); err == nil {
		t.Fatal("non-content-addressed handoff ID was accepted")
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
