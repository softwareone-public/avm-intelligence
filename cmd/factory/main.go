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

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/github"
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
	project := flags.Bool("project-pilot", false, "Print the Issue and Project projection without writing to GitHub")
	upsert := flags.Bool("upsert-pilot", false, "Upsert the pilot Issue and Project item with a reduced token")
	mint := flags.Bool("mint-projection-token", false, "Mint a reduced Issues and Projects installation token")
	analysisPath := flags.String("analysis", "", "Normalized analysis JSON to project")
	runURL := flags.String("run-url", "", "GitHub Actions run URL for the accepted analysis")
	executable := flags.String("iaclens", "", "Absolute path to the verified IaCLens executable")
	sourceDir := flags.String("source", "", "Absolute path to the prepared pilot Git worktree")
	workDir := flags.String("work", "work", "Attempt directory for the request and raw report")
	pagePath := flags.String("page", "", "Optional path for the evidence HTML page")
	discoverCatalog := flags.Bool("discover-catalog", false, "Fetch and validate the three official AVM Terraform catalogs")
	catalogOut := flags.String("catalog-out", "", "Optional path for the validated candidate catalog JSON")
	acceptCatalog := flags.String("accept-catalog", "", "Accept a reviewed candidate catalog JSON into durable discovery state")
	catalogState := flags.String("catalog-state", "data/discovery.json", "Path to accepted discovery control state")
	resolveSource := flags.String("resolve-source", "", "Resolve latest stable source for an accepted module ID")
	previousSource := flags.String("previous-source", "", "Previous source pin used to detect moved tags")
	sourceOut := flags.String("source-out", "", "Optional path for resolved source JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if handled, err := runCatalogCommands(*discoverCatalog, *catalogOut, *acceptCatalog, *catalogState, *resolveSource, *previousSource, *sourceOut); handled {
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
	if *project {
		return projectPilot(pilot, *analysisPath, *runURL)
	}
	if *mint {
		token, err := github.MintToken(context.Background(), os.Getenv("APP_ID"), os.Getenv("APP_PRIVATE_KEY"))
		if err != nil {
			return err
		}
		fmt.Print(token)
		return nil
	}
	if *upsert {
		return upsertPilot(pilot, *analysisPath, *runURL)
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
	requestDir := filepath.Join(*workDir, "requests")
	request, err := preparePilotRequest(doc, pilot, rulesetDigest, requestDir, *register)
	if err != nil {
		return err
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
	request.LastAcceptedFingerprint = request.Fingerprint
	request.LastAcceptedSourceSHA = request.SourceSHA
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

func preparePilotRequest(doc toolchain.Toolchain, pilot source.Pilot, policyDigest, requestDir string, register bool) (queue.Request, error) {
	asset, err := doc.Asset("linux", "amd64")
	if err != nil {
		return queue.Request{}, err
	}
	input := queue.AnalysisInput{
		ModuleID: pilot.Module.ID, SourceSHA: pilot.Module.Commit, CLIVersion: doc.CLI.Version,
		CLICommit: doc.CLI.Commit, BinaryDigest: asset.SHA256, PolicyDigest: policyDigest,
		ReportSchemaVersion: doc.CLI.SchemaVersion, AdapterVersion: doc.Adapter.Version,
	}
	now := time.Now()
	request, err := queue.New(input, now)
	if err != nil || !register {
		return request, err
	}
	var changed bool
	request, changed, err = queue.Register(requestDir, input, now)
	if err != nil {
		return queue.Request{}, err
	}
	if changed {
		fmt.Printf("registered %s attempt %d\n", request.ID, request.Attempt)
	} else {
		fmt.Printf("coalesced %s status %s\n", request.ID, request.Status)
	}
	return request, nil
}

func upsertPilot(pilot source.Pilot, analysisPath, runURL string) error {
	report, err := readAnalysis(analysisPath)
	if err != nil {
		return err
	}
	projection, err := github.FromAnalysis(pilot, report, runURL, time.Now())
	if err != nil {
		return err
	}
	issue, err := (github.Client{Token: os.Getenv("FACTORY_GITHUB_TOKEN")}).Upsert(context.Background(), "softwareone-public", "avm-intelligence", 1, projection)
	if err != nil {
		return err
	}
	fmt.Printf("upserted issue %d status %s outcome %s\n", issue.Number, projection.Status, projection.Outcome)
	return nil
}

func readAnalysis(path string) (iaclens.Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return iaclens.Report{}, fmt.Errorf("read analysis: %w", err)
	}
	var report iaclens.Report
	if err = json.Unmarshal(data, &report); err != nil {
		return iaclens.Report{}, fmt.Errorf("parse analysis: %w", err)
	}
	return report, nil
}

func discoverAVMCatalog(outputPath string) error {
	snapshot, err := catalog.Discover(context.Background(), nil)
	if err != nil {
		return err
	}
	counts := map[catalog.Kind]int{}
	for _, module := range snapshot.Modules {
		counts[module.Type]++
	}
	if outputPath != "" {
		if err := writeJSON(outputPath, snapshot); err != nil {
			return err
		}
	}
	fmt.Printf("validated %d AVM Terraform modules (res=%d ptn=%d utl=%d)\n", len(snapshot.Modules), counts[catalog.Resource], counts[catalog.Pattern], counts[catalog.Utility])
	for _, feed := range snapshot.Feeds {
		fmt.Printf("%s: %d modules sha256=%s\n", feed.Kind, feed.ModuleCount, feed.SHA256)
	}
	return nil
}

func acceptAVMCatalog(candidatePath, statePath string) error {
	state, delta, err := catalog.ApplyCandidate(candidatePath, statePath, time.Now().UTC())
	if err != nil {
		return err
	}
	if delta.NoChange {
		fmt.Printf("catalog unchanged at generation %d\n", state.Generation)
		return nil
	}
	fmt.Printf("accepted catalog generation %d: added=%d changed=%d pending-removal=%d retired=%d changed-feeds=%d\n",
		state.Generation, len(delta.Added), len(delta.Changed), len(delta.PendingRemoval), len(delta.Retired), len(delta.ChangedFeeds))
	return nil
}

func runCatalogCommands(discover bool, candidateOut, candidatePath, statePath, moduleID, previousPath, sourceOut string) (bool, error) {
	switch {
	case discover:
		return true, discoverAVMCatalog(candidateOut)
	case candidatePath != "":
		return true, acceptAVMCatalog(candidatePath, statePath)
	case moduleID != "":
		return true, resolveAVMSource(moduleID, statePath, previousPath, sourceOut)
	default:
		return false, nil
	}
}

func resolveAVMSource(moduleID, statePath, previousPath, outputPath string) error {
	state, err := catalog.LoadAccepted(statePath)
	if err != nil {
		return err
	}
	if state == nil {
		return fmt.Errorf("accepted catalog state does not exist at %s", statePath)
	}
	var module *catalog.Module
	for _, record := range state.Modules {
		if record.Module.ID == moduleID {
			if !record.Active {
				return fmt.Errorf("module %s is retired", moduleID)
			}
			selected := record.Module
			module = &selected
			break
		}
	}
	if module == nil {
		return fmt.Errorf("module %s is not in accepted discovery state", moduleID)
	}
	var previous *source.ResolvedSource
	if previousPath != "" {
		data, readErr := os.ReadFile(previousPath)
		if readErr != nil {
			return fmt.Errorf("read previous source pin: %w", readErr)
		}
		var pin source.ResolvedSource
		if readErr = json.Unmarshal(data, &pin); readErr != nil {
			return fmt.Errorf("parse previous source pin: %w", readErr)
		}
		previous = &pin
	}
	resolved, err := (source.Resolver{}).ResolveLatest(context.Background(), *module, previous)
	if err != nil {
		return err
	}
	if outputPath != "" {
		return writeJSON(outputPath, resolved)
	}
	return json.NewEncoder(os.Stdout).Encode(resolved)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func projectPilot(pilot source.Pilot, analysisPath, runURL string) error {
	report, err := readAnalysis(analysisPath)
	if err != nil {
		return err
	}
	projection, err := github.FromAnalysis(pilot, report, runURL, time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("status %s\noutcome %s\nmodule %s\nsource %s\npublication not claimed\n", projection.Status, projection.Outcome, projection.ModuleID, projection.SourceSHA)
	return nil
}
