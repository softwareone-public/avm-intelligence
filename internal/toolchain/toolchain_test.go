package toolchain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPinnedToolchain(t *testing.T) {
	t.Parallel()
	doc, err := Load(filepath.Join("..", "..", "config", "toolchain.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if doc.CLI.Version != "0.1.0" || doc.CLI.SchemaVersion != RequiredSchemaVersion {
		t.Fatalf("CLI pin = %#v", doc.CLI)
	}
	asset, err := doc.Asset("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	const want = "390ef9d188341c85d42e16adf61cbf6b91effeee9ec0af99c492ab50b08d6238"
	if asset.SHA256 != want {
		t.Fatalf("linux/amd64 digest = %s", asset.SHA256)
	}
	if _, err = doc.Asset("plan9", "amd64"); err == nil {
		t.Fatal("unknown platform was accepted")
	}
}

func TestRejectIncompleteToolchain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "toolchain.yaml")
	content := []byte("version: 1\ncli:\n  repository: example/iaclens\n  version: 0.1.0\n  commit: abc\n  schema_version: 2\n  ruleset_format_version: 1\nadapter:\n  version: 1\nassets: []\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("incomplete toolchain was accepted")
	}
}
