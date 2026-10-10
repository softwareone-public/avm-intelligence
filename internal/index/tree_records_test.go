package index

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

func TestRepositoryDatasetIsNativeAndRebuildable(t *testing.T) {
	root := filepath.Join("..", "..", "data")
	for _, pattern := range []string{"modules/*.json", "requests/*.json", "candidate.json", "discovery.json"} {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil || len(matches) != 0 {
			t.Fatalf("retired control/report records remain: pattern=%s matches=%v err=%v", pattern, matches, err)
		}
	}
	records, err := ReadRecords(root, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	indexes, err := Build(records)
	if err != nil {
		t.Fatal(err)
	}
	files, err := indexes.Files()
	if err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]struct{}, len(files))
	for recordPath := range files {
		paths[recordPath] = struct{}{}
	}
	for _, recordPath := range sortedKeys(paths) {
		if recordPath == "data/dataset.json" {
			var rebuilt DatasetManifest
			if err = json.Unmarshal(files[recordPath], &rebuilt); err != nil || rebuilt.SchemaVersion != 3 || rebuilt.RecordType != "dataset_manifest" {
				t.Errorf("rebuilt dataset manifest is invalid: manifest=%+v err=%v", rebuilt, err)
			}
			continue
		}
		actual, readErr := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(recordPath)))
		if readErr != nil || !bytes.Equal(actual, files[recordPath]) {
			t.Errorf("accepted native index differs from complete rebuild: %s err=%v", recordPath, readErr)
		}
	}
}

func TestReadRecordsRebuildsIndexesFromAcceptedTree(t *testing.T) {
	root := t.TempDir()
	moduleID := "avm-res-tree-reader-demo"
	repository := "Azure/terraform-azurerm-avm-res-tree-reader-demo"
	sourceSHA := strings.Repeat("a", 40)
	createdAt := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	catalog := CatalogIndex{
		SchemaVersion: 1, RecordType: "catalog_index", CatalogGeneration: 7,
		Modules: []CatalogModuleEntry{{ModuleID: moduleID, Repository: repository, Lifecycle: "active"}},
	}
	writeTreeJSON(t, root, "catalog.json", catalog)
	metadataPath, err := identity.ModuleMetadataPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	writeTreeJSON(t, root, strings.TrimPrefix(metadataPath, "data/"), map[string]any{
		"record_type": "module_metadata", "module_id": moduleID, "repository": repository, "lifecycle": "active",
	})
	sourcePath, err := identity.ModuleSourcePath(moduleID, sourceSHA)
	if err != nil {
		t.Fatal(err)
	}
	writeTreeJSON(t, root, strings.TrimPrefix(sourcePath, "data/"), map[string]any{
		"schema_version": 2, "record_type": "source_revision", "module_id": moduleID,
		"repository": repository, "source_sha": sourceSHA, "immutable": true,
	})

	records, err := ReadRecords(root, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if records.CatalogGeneration != catalog.CatalogGeneration || records.CreatedAt != createdAt || len(records.Modules) != 1 || records.Modules[0].ModuleID != moduleID || len(records.SourceRevisions) != 1 {
		t.Fatalf("typed records were not reconstructed from accepted data: %#v", records)
	}
	if _, includesDerivedCatalog := records.RecordDigests["data/catalog.json"]; includesDerivedCatalog {
		t.Fatal("generated catalog index was treated as an input record digest")
	}
	indexes, err := Build(records)
	if err != nil {
		t.Fatal(err)
	}
	files, err := indexes.Files()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := files["data/modules/res/"+moduleID+"/index.json"]; !exists {
		t.Fatal("rebuilt module index is missing")
	}
	if _, exists := files["data/catalog.json"]; !exists {
		t.Fatal("rebuilt catalog index is missing")
	}
}

func TestReadRecordsRejectsLinkedDataEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecords(root, time.Now().UTC()); err == nil {
		t.Fatal("linked data tree entry was accepted")
	}
}

func writeTreeJSON(t *testing.T, root, relative string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
