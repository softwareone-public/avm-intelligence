package publish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if err := os.WriteFile(filepath.Join(dataRoot, "modules", "one.json"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	if loaded != manifest {
		t.Fatalf("loaded manifest differs: %#v != %#v", loaded, manifest)
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
