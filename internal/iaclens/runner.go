// Package iaclens invokes a verified IaCLens executable. It never shells out
// through a user-controlled command string and never reads upstream policy.
package iaclens

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	// DefaultTimeout bounds one offline module analysis.
	DefaultTimeout = 10 * time.Minute
	// OutputName is the only report filename created inside an attempt directory.
	OutputName = "report.raw.json"
)

// Request is one isolated analysis attempt. All paths must be absolute.
type Request struct {
	Executable string
	SourceDir  string
	ConfigPath string
	WorkDir    string
	Kind       string
	Timeout    time.Duration
}

// Result distinguishes process success from check findings. A zero exit can
// still contain failing checks; a nonzero exit never yields a usable report.
type Result struct {
	ExitCode int
	Report   []byte
	Stdout   string
	Stderr   string
}

// Run executes IaCLens with the factory-owned config and a fresh output file.
func Run(ctx context.Context, req Request) (Result, error) {
	if err := req.validate(); err != nil {
		return Result{}, err
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := filepath.Join(req.WorkDir, OutputName)
	if _, err := os.Stat(out); err == nil {
		return Result{}, fmt.Errorf("refusing to reuse existing report %s", out)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, req.Executable,
		"--source", req.SourceDir,
		"--mode", "offline",
		"--kind", req.Kind,
		"--config", req.ConfigPath,
		"--format", "json",
		"--out", out,
	)
	cmd.Dir = req.WorkDir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			result.ExitCode = exit.ExitCode()
			return result, nil
		}
		if ctx.Err() != nil {
			return result, fmt.Errorf("iaclens timed out: %w", ctx.Err())
		}
		return result, fmt.Errorf("start iaclens: %w", err)
	}
	report, err := os.ReadFile(out)
	if err != nil {
		return result, fmt.Errorf("read iaclens report: %w", err)
	}
	result.Report = report
	return result, nil
}

func (r Request) validate() error {
	for _, path := range []string{r.Executable, r.SourceDir, r.ConfigPath, r.WorkDir} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("analysis path must be absolute: %q", path)
		}
	}
	if r.Kind != "module" && r.Kind != "auto" {
		return fmt.Errorf("unsupported analysis kind %q", r.Kind)
	}
	info, err := os.Stat(r.Executable)
	if err != nil {
		return fmt.Errorf("iaclens executable: %w", err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("iaclens path is not executable")
	}
	return nil
}
