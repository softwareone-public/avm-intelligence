package publish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/index"
)

func TestCreateAndVerifyIndexedManifestBindsDatasetAndGeneration(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	siteRoot := filepath.Join(root, "site")
	moduleID := "avm-res-one"
	if err := os.MkdirAll(filepath.Join(dataRoot, "modules", "res", moduleID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siteRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(dataRoot, "catalog.json"), index.CatalogIndex{
		SchemaVersion: 1, RecordType: "catalog_index", CatalogGeneration: 1,
		Modules: []index.CatalogModuleEntry{{ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-one", Lifecycle: "active"}},
	})
	writeJSONFile(t, filepath.Join(dataRoot, "dataset.json"), index.DatasetManifest{
		SchemaVersion: 3, RecordType: "dataset_manifest",
		Generator:         index.Producer{ID: "factory", Version: "2.0.0", ContractVersion: 1},
		CatalogGeneration: 1, RecordDigests: map[string]string{}, ModuleMembership: []index.ModuleMembership{},
		CreatedAt: time.Date(2026, 10, 2, 11, 0, 0, 0, time.UTC),
	})
	writeJSONFile(t, filepath.Join(dataRoot, "modules", "res", moduleID, "module.json"), map[string]string{
		"record_type": "module_metadata", "module_id": moduleID,
	})
	if err := os.WriteFile(filepath.Join(siteRoot, "index.html"), []byte("<main>indexed</main>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	generatedAt := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	manifest, err := CreateIndexed(dataRoot, siteRoot, strings.Repeat("a", 40), "run-25", 7, generatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != IndexedManifestSchemaVersion || manifest.DatasetID == "" || manifest.Generator.ID != "factory" || manifest.DeploymentGeneration != 7 || !reflect.DeepEqual(manifest.ModuleIDs, []string{moduleID}) {
		t.Fatalf("indexed manifest lost dataset or deployment identity: %+v", manifest)
	}
	wantModuleDigest, err := DigestSubtree(dataRoot, "modules")
	if err != nil {
		t.Fatal(err)
	}
	emptyTree := sha256.Sum256(nil)
	if manifest.ModuleDataDigest != wantModuleDigest || manifest.ProviderDataDigest != hex.EncodeToString(emptyTree[:]) {
		t.Fatalf("manifest did not preserve separate accepted roots: modules=%s providers=%s", manifest.ModuleDataDigest, manifest.ProviderDataDigest)
	}
	unindexed := manifest
	unindexed.SchemaVersion = 2
	if err := unindexed.Validate(); err == nil {
		t.Fatal("retired unindexed publication format was accepted")
	}
	receiptPath := filepath.Join(root, "publication.json")
	if err = Write(receiptPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err = Write(filepath.Join(siteRoot, "publication.json"), manifest); err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyIndexedDeploymentReceipt(receiptPath, dataRoot, "run-25", 7)
	if err != nil {
		t.Fatal(err)
	}
	if verified.DatasetID != manifest.DatasetID || !reflect.DeepEqual(verified.ModuleMembership, manifest.ModuleMembership) {
		t.Fatalf("verified indexed receipt differs from manifest: %+v", verified)
	}
	if _, err = VerifyIndexedPublication(filepath.Join(siteRoot, "publication.json"), dataRoot, siteRoot, "run-25", 7); err != nil {
		t.Fatalf("exact combined data and site artifact did not verify: %v", err)
	}
	wrongRoot := manifest
	wrongRoot.ModuleDataDigest = strings.Repeat("f", 64)
	wrongRootPath := filepath.Join(root, "wrong-root-publication.json")
	if err = Write(wrongRootPath, wrongRoot); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyIndexedDeploymentReceipt(wrongRootPath, dataRoot, "run-25", 7); err == nil || !strings.Contains(err.Error(), "root digest") {
		t.Fatalf("receipt with a mismatched module root digest was accepted: %v", err)
	}
	if err = os.WriteFile(filepath.Join(siteRoot, "index.html"), []byte("<main>tampered</main>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyIndexedPublication(filepath.Join(siteRoot, "publication.json"), dataRoot, siteRoot, "run-25", 7); err == nil || !strings.Contains(err.Error(), "site digest") {
		t.Fatalf("publication receipt accepted a changed rendered site: %v", err)
	}
	if _, err = VerifyIndexedDeploymentReceipt(receiptPath, dataRoot, "run-25", 6); err == nil {
		t.Fatal("receipt for another deployment generation was accepted")
	}
	if _, err = VerifyIndexedDeploymentReceipt(receiptPath, dataRoot, "another-run", 7); err == nil {
		t.Fatal("receipt for another producer was accepted")
	}
	if err = os.WriteFile(filepath.Join(dataRoot, "modules", "res", moduleID, "module.json"), []byte(`{"record_type":"module_metadata","module_id":"avm-res-one","description":"changed"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyIndexedDeploymentReceipt(receiptPath, dataRoot, "run-25", 7); err == nil {
		t.Fatal("receipt for a changed dataset was accepted")
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDigestTreeRejectsSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := DigestTree(root); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestManifestRejectsMovedAcceptedCommit(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: IndexedManifestSchemaVersion, AcceptedCommit: strings.Repeat("A", 40),
		DataDigest: strings.Repeat("a", 64), ModuleDataDigest: strings.Repeat("c", 64),
		ProviderDataDigest: strings.Repeat("d", 64), SiteDigest: strings.Repeat("b", 64),
		ProducerRun: "run-18", GeneratedAt: time.Now().UTC(),
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("uppercase accepted commit was accepted")
	}
}

func TestManifestMembershipDistinguishesAVMReleases(t *testing.T) {
	analysisV1 := "analysis-" + strings.Repeat("a", 64)
	analysisV2 := "analysis-" + strings.Repeat("b", 64)
	selectionV1 := "selection-" + strings.Repeat("c", 64)
	selectionV2 := "selection-" + strings.Repeat("d", 64)
	manifest := Manifest{
		SchemaVersion: IndexedManifestSchemaVersion, AcceptedCommit: strings.Repeat("a", 40),
		DataDigest: strings.Repeat("a", 64), ModuleDataDigest: strings.Repeat("c", 64),
		ProviderDataDigest: strings.Repeat("d", 64), SiteDigest: strings.Repeat("b", 64),
		ProducerRun: "run-27", GeneratedAt: time.Now().UTC(),
		DatasetID: "dataset-" + strings.Repeat("e", 64), Generator: index.Producer{ID: "factory", Version: "2.0.0", ContractVersion: 1},
		DeploymentGeneration: 1, ModuleIDs: []string{"avm-res-one"},
		ModuleMembership: []index.ModuleMembership{
			{ModuleID: "avm-res-one", AVMReleaseVersion: "v1.0.0", ContextID: "registry.terraform.io/hashicorp/azurerm/4", AnalysisID: &analysisV1, SelectionID: &selectionV1},
			{ModuleID: "avm-res-one", AVMReleaseVersion: "v2.0.0", ContextID: "registry.terraform.io/hashicorp/azurerm/4", AnalysisID: &analysisV2, SelectionID: &selectionV2},
		},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("manifest rejected distinct AVM release membership: %v", err)
	}
	manifest.ModuleMembership[1].AVMReleaseVersion = "v1.0.0"
	if err := manifest.Validate(); err == nil {
		t.Fatal("manifest accepted duplicate module/release/provider-context membership")
	}
}
