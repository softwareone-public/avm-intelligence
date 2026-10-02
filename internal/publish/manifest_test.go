package publish

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/index"
)

func TestCreateAndReadManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	siteRoot := filepath.Join(root, "site")
	if err := os.MkdirAll(filepath.Join(dataRoot, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siteRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "modules", "avm-res-one.json"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(dataRoot, candidateIndexName), Candidate{
		SchemaVersion:     CandidateSchemaVersion,
		CatalogGeneration: 1,
		BuiltAt:           time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC),
		Modules: []CandidateModule{{
			ModuleID: "avm-res-one", SourceSHA: strings.Repeat("a", 40),
			Fingerprint: strings.Repeat("b", 64), ReportPath: "modules/avm-res-one.json",
		}},
	})
	if err := os.WriteFile(filepath.Join(siteRoot, "index.html"), []byte("<main>accepted</main>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := Create(dataRoot, siteRoot, strings.Repeat("a", 40), "run-18", time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "publication.json")
	if err = Write(path, manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, manifest) {
		t.Fatalf("loaded manifest differs: %#v != %#v", loaded, manifest)
	}
	if len(loaded.ModuleIDs) != 1 || loaded.ModuleIDs[0] != "avm-res-one" {
		t.Fatalf("manifest module membership = %#v", loaded.ModuleIDs)
	}
}

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
		SchemaVersion: 2, RecordType: "dataset_manifest",
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
	receiptPath := filepath.Join(root, "publication.json")
	if err = Write(receiptPath, manifest); err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyIndexedDeploymentReceipt(receiptPath, dataRoot, "run-25", 7)
	if err != nil {
		t.Fatal(err)
	}
	if verified.DatasetID != manifest.DatasetID || !reflect.DeepEqual(verified.ModuleMembership, manifest.ModuleMembership) {
		t.Fatalf("verified indexed receipt differs from manifest: %+v", verified)
	}
	if _, err = VerifyIndexedDeploymentReceipt(receiptPath, dataRoot, "run-25", 6); err == nil {
		t.Fatal("receipt for another deployment generation was accepted")
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
		SchemaVersion: ManifestSchemaVersion, AcceptedCommit: strings.Repeat("A", 40),
		DataDigest: strings.Repeat("a", 64), SiteDigest: strings.Repeat("b", 64),
		ProducerRun: "run-18", GeneratedAt: time.Now().UTC(),
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("uppercase accepted commit was accepted")
	}
}

func TestCreateManifestRequiresCandidateMembership(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	siteRoot := filepath.Join(root, "site")
	if err := os.MkdirAll(filepath.Join(dataRoot, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siteRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "modules", "one.json"), []byte("report\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "index.html"), []byte("<main>site</main>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(dataRoot, siteRoot, strings.Repeat("a", 40), "run-19", time.Now().UTC()); err == nil {
		t.Fatal("publication manifest was created without candidate membership")
	}
}

func TestVerifyDeploymentReceiptRejectsMismatchedIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	siteRoot := filepath.Join(root, "site")
	if err := os.MkdirAll(filepath.Join(dataRoot, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(siteRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(dataRoot, "modules", "avm-res-one.json")
	if err := os.WriteFile(reportPath, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(dataRoot, candidateIndexName), Candidate{
		SchemaVersion:     CandidateSchemaVersion,
		CatalogGeneration: 1,
		BuiltAt:           time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC),
		Modules: []CandidateModule{{
			ModuleID: "avm-res-one", SourceSHA: strings.Repeat("a", 40),
			Fingerprint: strings.Repeat("b", 64), ReportPath: "modules/avm-res-one.json",
		}},
	})
	if err := os.WriteFile(filepath.Join(siteRoot, "index.html"), []byte("<main>site</main>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := Create(dataRoot, siteRoot, strings.Repeat("a", 40), "run-20", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(root, "publication.json")
	if err = Write(receiptPath, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyDeploymentReceipt(receiptPath, dataRoot, "run-20"); err != nil {
		t.Fatalf("valid deployment receipt rejected: %v", err)
	}
	if _, err = VerifyDeploymentReceipt(receiptPath, dataRoot, "another-run"); err == nil {
		t.Fatal("receipt from a different producer run was accepted")
	}

	wrongMembership := manifest
	wrongMembership.ModuleIDs = []string{"avm-res-other"}
	wrongMembershipPath := filepath.Join(root, "wrong-membership.json")
	if err = Write(wrongMembershipPath, wrongMembership); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyDeploymentReceipt(wrongMembershipPath, dataRoot, "run-20"); err == nil {
		t.Fatal("receipt with incorrect module membership was accepted")
	}

	if err = os.WriteFile(reportPath, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = VerifyDeploymentReceipt(receiptPath, dataRoot, "run-20"); err == nil {
		t.Fatal("receipt with an outdated dataset digest was accepted")
	}
}
