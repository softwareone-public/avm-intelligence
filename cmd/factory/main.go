// Command factory orchestrates the reviewed AVM Intelligence pilot. This slice
// can register and analyze one pinned module. It has no publication credentials.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/site"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/toolchain"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "factory:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("factory", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	toolchainPath := flags.String("toolchain", "config/toolchain.yaml", "Pinned IaCLens toolchain document")
	pilotPath := flags.String("pilot", "config/pilot.yaml", "Reviewed single-module pilot document")
	configPath := flags.String("config", "config/iaclens.yaml", "Factory-owned IaCLens ruleset")
	show := flags.Bool("show-toolchain", false, "Validate and print the pinned IaCLens consumer contract")
	showPilot := flags.Bool("show-pilot", false, "Print the reviewed pilot source archive URL")
	register := flags.Bool("register-pilot", false, "Write the durable request for the reviewed pilot")
	analyze := flags.Bool("analyze-pilot", false, "Analyze the prepared pilot source with a verified IaCLens executable")
	executable := flags.String("iaclens", "", "Absolute path to the verified IaCLens executable")
	sourceDir := flags.String("source", "", "Absolute path to the prepared pilot Git worktree")
	workDir := flags.String("work", "work", "Attempt directory for the request and raw report")
	pagePath := flags.String("page", "", "Optional path for the evidence HTML page")
	if err := flags.Parse(args); err != nil {
		return err
	}
	doc, err := toolchain.Load(*toolchainPath)
	if err != nil {
		return err
	}
	if *show {
		asset, err := doc.Asset("linux", "amd64")
		if err != nil {
			return err
		}
		fmt.Printf("iaclens %s (%s), schema %d, adapter %d, linux/amd64 %s\n", doc.CLI.Version, doc.CLI.Commit, doc.CLI.SchemaVersion, doc.Adapter.Version, asset.SHA256)
	}
	pilot, err := source.LoadPilot(*pilotPath)
	if err != nil {
		return err
	}
	if *showPilot {
		fmt.Println(pilot.ArchiveURL())
	}
	if !*register && !*analyze {
		if *show || *showPilot {
			return nil
		}
		return fmt.Errorf("no factory operation requested")
	}
	rulesetDigest, err := fileDigest(*configPath)
	if err != nil {
		return err
	}
	request, err := queue.New(pilot.Module.ID, pilot.Module.Commit, doc.CLI.Commit, rulesetDigest, time.Now())
	if err != nil {
		return err
	}
	requestDir := filepath.Join(*workDir, "requests")
	if *register {
		if err = queue.Save(requestDir, request); err != nil {
			return err
		}
		fmt.Printf("registered %s attempt %d\n", request.ID, request.Attempt)
	}
	if !*analyze {
		return nil
	}
	if !filepath.IsAbs(*executable) || !filepath.IsAbs(*sourceDir) {
		return fmt.Errorf("--iaclens and --source must be absolute")
	}
	absConfig, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	absWork, err := filepath.Abs(*workDir)
	if err != nil {
		return err
	}
	result, err := iaclens.Run(context.Background(), iaclens.Request{
		Executable: *executable, SourceDir: *sourceDir, ConfigPath: absConfig, WorkDir: absWork, Kind: "module",
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		request.Status = queue.StatusFailed
		if saveErr := queue.Save(requestDir, request); saveErr != nil {
			return saveErr
		}
		return fmt.Errorf("iaclens exited %d; fingerprint remains unaccepted", result.ExitCode)
	}
	report, err := iaclens.Normalize(result.Report, iaclens.Provenance{
		ModuleID: pilot.Module.ID, SourceSHA: pilot.Module.Commit, CLIVersion: doc.CLI.Version,
		CLICommit: doc.CLI.Commit, AdapterVersion: iaclens.AdapterVersion,
	})
	if err != nil {
		return err
	}
	if err = writeJSON(filepath.Join(absWork, "analysis.json"), report); err != nil {
		return err
	}
	if *pagePath != "" {
		if err = site.Write(*pagePath, report, pilot.Module.Version); err != nil {
			return err
		}
	}
	request.Status = queue.StatusAccepted
	if err = queue.Save(requestDir, request); err != nil {
		return err
	}
	fmt.Printf("accepted %s with %d units\n", request.ID, len(report.Units))
	return nil
}

func fileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read ruleset: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
