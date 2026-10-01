package iaclens

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRunRequiresAbsolutePaths(t *testing.T) {
	t.Parallel()
	_, err := Run(context.Background(), Request{Executable: "iaclens", SourceDir: "/source", ConfigPath: "/config", WorkDir: "/work", Kind: "module"})
	if err == nil {
		t.Fatal("relative executable was accepted")
	}
}

func TestRunKeepsProcessFailureSeparateFromFindings(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("fixture executable is a POSIX shell script")
	}
	root := t.TempDir()
	executable := filepath.Join(root, "iaclens")
	script := "#!/bin/sh\nprintf 'progress\\n'\nprintf 'iaclens: fixture failure\\n' >&2\nexit 1\n"
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), Request{
		Executable: executable,
		SourceDir:  root,
		ConfigPath: executable,
		WorkDir:    root,
		Kind:       "module",
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitCode != 1 || result.Report != nil || result.Stderr == "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunReadsFreshJSONOutput(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("fixture executable is a POSIX shell script")
	}
	root := t.TempDir()
	executable := filepath.Join(root, "iaclens")
	script := "#!/bin/sh\nout=\nwhile [ $# -gt 0 ]; do if [ \"$1\" = --out ]; then out=$2; fi; shift; done\nprintf 'Scanned fixture\\n'\nprintf '%s\\n' '{\"iaclens\":{\"schema_version\":2}}' > \"$out\"\n"
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), Request{
		Executable: executable,
		SourceDir:  root,
		ConfigPath: executable,
		WorkDir:    root,
		Kind:       "auto",
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitCode != 0 || string(result.Report) == "" {
		t.Fatalf("result = %+v", result)
	}
}
