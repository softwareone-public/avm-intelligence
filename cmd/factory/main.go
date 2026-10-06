// Command factory orchestrates native discovery, fenced work, trusted evidence,
// typed proposals and offline assessment without executable analyzers.
package main

import (
	"context"

	"encoding/json"
	"flag"

	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/discovery"

	"github.com/softwareone-public/avm-intelligence/internal/queue"

	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

const (
	factoryOwner         = "softwareone-public"
	factoryRepository    = "avm-intelligence"
	factoryProjectNumber = 1
	factoryStateBranch   = "factory-state"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return
		}
		os.Exit(1)
	}
}

func decodeOne(data []byte, output any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("JSON input must contain one document")
	}
	return nil
}

func listAssessmentDefinitions(output io.Writer, dimensionID string) error {
	registry, err := assessment.DefaultRegistry()
	if err != nil {
		return err
	}
	if dimensionID == "" {
		return writeAssessmentJSON(output, registry.ListDimensions())
	}
	checks, err := registry.ListChecks(dimensionID)
	if err != nil {
		return err
	}
	return writeAssessmentJSON(output, checks)
}

func runOfflineAssessment(factsPath, snapshotsPath, classificationsPath, requestedDimensions, outputPath string) error {
	if factsPath == "" {
		return fmt.Errorf("--assess-facts requires a facts-v3 JSON path")
	}
	factsData, err := os.ReadFile(factsPath)
	if err != nil {
		return fmt.Errorf("read facts record: %w", err)
	}
	var facts terraform.Facts
	if err = decodeOne(factsData, &facts); err != nil {
		return fmt.Errorf("decode facts record: %w", err)
	}
	snapshots := []assessment.ProviderSnapshot{}
	if snapshotsPath != "" {
		snapshotData, readErr := os.ReadFile(snapshotsPath)
		if readErr != nil {
			return fmt.Errorf("read provider snapshots: %w", readErr)
		}
		if readErr = decodeOne(snapshotData, &snapshots); readErr != nil {
			return fmt.Errorf("decode provider snapshots: %w", readErr)
		}
	}
	classifications := map[string]string{}
	if classificationsPath != "" {
		classificationData, readErr := os.ReadFile(classificationsPath)
		if readErr != nil {
			return fmt.Errorf("read classification overrides: %w", readErr)
		}
		if readErr = decodeOne(classificationData, &classifications); readErr != nil {
			return fmt.Errorf("decode classification overrides: %w", readErr)
		}
	}
	dimensionIDs, err := parseDimensionSelection(requestedDimensions)
	if err != nil {
		return err
	}
	registry, err := assessment.DefaultRegistry()
	if err != nil {
		return err
	}
	results, err := assessment.RunDimensions(registry, assessment.RunInput{
		Facts: facts, Snapshots: snapshots, DimensionIDs: dimensionIDs, Classifications: classifications,
	})
	if err != nil {
		return err
	}
	if outputPath != "" {
		return writeJSON(outputPath, results)
	}
	return writeAssessmentJSON(os.Stdout, results)
}

func parseDimensionSelection(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	dimensions := make([]string, 0)
	for _, item := range strings.Split(strings.ReplaceAll(value, "\n", ","), ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			dimensions = append(dimensions, item)
		}
	}
	if len(dimensions) == 0 {
		return nil, fmt.Errorf("--dimension-ids must contain at least one dimension ID")
	}
	return dimensions, nil
}

func writeAssessmentJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
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
	slog.Info("catalog validated", "modules", len(snapshot.Modules), "resources", counts[catalog.Resource], "patterns", counts[catalog.Pattern], "utilities", counts[catalog.Utility])
	for _, feed := range snapshot.Feeds {
		slog.Info("catalog feed validated", "kind", feed.Kind, "modules", feed.ModuleCount, "sha256", feed.SHA256)
	}
	return nil
}

func persistAVMCatalog(candidatePath, branch, extractorContract, capabilityList, forceModuleList, resultPath string) error {
	return persistSelectedAVMCatalog(candidatePath, branch, extractorContract, capabilityList, forceModuleList, resultPath, nil)
}

// manualModuleScope separates operator selection from an explicit refresh.
type manualModuleScope struct {
	types string
	names string
	force bool
}

// selectManualModules resolves canonical active IDs without broadening an explicit list.
func selectManualModules(records []catalog.ModuleRecord, scope manualModuleScope) ([]string, error) {
	types, err := manualModuleTypes(scope.types)
	if err != nil {
		return nil, err
	}
	active := make(map[string]catalog.Module, len(records))
	for _, record := range records {
		if record.Active {
			active[record.Module.ID] = record.Module
		}
	}
	if strings.TrimSpace(scope.names) != "" {
		return selectNamedManualModules(active, types, scope.names)
	}
	moduleIDs := make([]string, 0, len(active))
	for moduleID, module := range active {
		if types[module.Type] {
			moduleIDs = append(moduleIDs, moduleID)
		}
	}
	if len(moduleIDs) == 0 {
		return nil, fmt.Errorf("manual module selection must not be empty")
	}
	sort.Strings(moduleIDs)
	return moduleIDs, nil
}

// manualModuleTypes validates the explicit type selection independently of data access.
func manualModuleTypes(typeList string) (map[catalog.Kind]bool, error) {
	types := make(map[catalog.Kind]bool)
	for _, value := range strings.Split(typeList, ",") {
		kind := catalog.Kind(strings.TrimSpace(value))
		if kind != catalog.Resource && kind != catalog.Pattern && kind != catalog.Utility {
			return nil, fmt.Errorf("select at least one valid module type: res, ptn, utl")
		}
		types[kind] = true
	}
	return types, nil
}

// selectNamedManualModules rejects conflicting CSV targets instead of dropping them.
func selectNamedManualModules(active map[string]catalog.Module, types map[catalog.Kind]bool, names string) ([]string, error) {
	selected := make(map[string]bool)
	for _, value := range strings.Split(names, ",") {
		moduleID := strings.TrimSpace(value)
		module, exists := active[moduleID]
		if !exists || !types[module.Type] || !strings.HasPrefix(moduleID, "avm-"+string(module.Type)+"-") {
			return nil, fmt.Errorf("module %q is unknown, inactive, or outside the selected types", moduleID)
		}
		if selected[moduleID] {
			return nil, fmt.Errorf("module %q is duplicated", moduleID)
		}
		selected[moduleID] = true
	}
	moduleIDs := make([]string, 0, len(selected))
	for moduleID := range selected {
		moduleIDs = append(moduleIDs, moduleID)
	}
	sort.Strings(moduleIDs)
	return moduleIDs, nil
}

// persistSelectedAVMCatalog retains the complete observation while narrowing source work.
func persistSelectedAVMCatalog(candidatePath, branch, extractorContract, capabilityList, forceModuleList, resultPath string, scope *manualModuleScope) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	if strings.TrimSpace(extractorContract) == "" || strings.TrimSpace(capabilityList) == "" {
		return fmt.Errorf("--extractor-contract and --extractor-capabilities are required for --persist-discovery")
	}
	stateToken := strings.TrimSpace(os.Getenv("FACTORY_STATE_TOKEN"))
	if stateToken == "" {
		return fmt.Errorf("FACTORY_STATE_TOKEN is required for --persist-discovery")
	}
	data, err := os.ReadFile(candidatePath)
	if err != nil {
		return fmt.Errorf("read discovery candidate: %w", err)
	}
	var candidate catalog.Snapshot
	if err = decodeOne(data, &candidate); err != nil {
		return fmt.Errorf("parse discovery candidate: %w", err)
	}
	capabilities := make([]string, 0)
	for _, value := range strings.Split(capabilityList, ",") {
		if value = strings.TrimSpace(value); value != "" {
			capabilities = append(capabilities, value)
		}
	}
	if len(capabilities) == 0 {
		return fmt.Errorf("at least one extractor capability is required")
	}
	selectedModuleIDs, forceModuleIDs, err := discoveryWorkSelection(candidate, forceModuleList, scope)
	if err != nil {
		return err
	}
	backend := state.GitHubBackend{
		Owner: factoryOwner, Repository: factoryRepository, Branch: branch, Token: stateToken,
	}
	result, err := discovery.Persist(context.Background(), source.Resolver{Token: os.Getenv("GITHUB_TOKEN")}, backend, candidate, discovery.Options{
		AcceptedAt: time.Now().UTC(), ExtractorContract: extractorContract, Capabilities: capabilities,
		SelectedModuleIDs: selectedModuleIDs, ForceModuleIDs: forceModuleIDs,
	})
	if err != nil {
		return err
	}
	if resultPath != "" {
		if err = writeJSON(resultPath, result); err != nil {
			return fmt.Errorf("write discovery result: %w", err)
		}
	}
	slog.Info("discovery persisted", "generation", result.Accepted.Generation, "state_head", result.Head, "observation_id", result.ObservationID, "proposal_id", result.ProposalID, "projection_modules", len(result.ProjectionModuleIDs))
	slog.Debug("discovery projection targets", "module_ids", result.ProjectionModuleIDs)
	return nil
}

// discoveryWorkSelection keeps the historical forced-ID flag distinct from manual selection.
func discoveryWorkSelection(candidate catalog.Snapshot, forceModuleList string, scope *manualModuleScope) ([]string, []string, error) {
	if scope == nil {
		selected, err := parseModuleIDs(forceModuleList)
		if err != nil {
			return nil, nil, fmt.Errorf("parse --discover-modules: %w", err)
		}
		forced := make([]string, 0, len(selected))
		for moduleID := range selected {
			forced = append(forced, moduleID)
		}
		sort.Strings(forced)
		return nil, forced, nil
	}
	if forceModuleList != "" {
		return nil, nil, fmt.Errorf("manual selection cannot be combined with --discover-modules")
	}
	records := make([]catalog.ModuleRecord, 0, len(candidate.Modules))
	for _, module := range candidate.Modules {
		records = append(records, catalog.ModuleRecord{Module: module, Active: true})
	}
	selected, err := selectManualModules(records, *scope)
	if err != nil {
		return nil, nil, err
	}
	if scope.force {
		return selected, selected, nil
	}
	return selected, nil, nil
}

func validateFactoryStateBranch(branch string) error {
	if branch == "" {
		return fmt.Errorf("--state-branch is required for state operations")
	}
	if branch != factoryStateBranch {
		return fmt.Errorf("--state-branch must be %q for state operations", factoryStateBranch)
	}
	return nil
}

func runDispatchOperation(operation, branch string, options dispatchOperationOptions) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	backend, err := newFactoryStateBackend(branch)
	if err != nil {
		return err
	}
	result, err := executeDispatchOperation(context.Background(), operation, backend, options, time.Now().UTC())
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode dispatcher result: %w", err)
	}
	fmt.Println(string(encoded))
	return nil
}

func runOperationalCommand(policyPath, commandID, kind, moduleID, reason string) error {
	policy, err := queue.LoadOperatorPolicy(policyPath)
	if err != nil {
		return err
	}
	command := queue.OperationalCommand{
		ID: commandID, Kind: kind, Actor: os.Getenv("GITHUB_ACTOR"),
		ModuleID: moduleID, Reason: reason, IssuedAt: time.Now().UTC(),
	}
	if err = command.Validate(); err != nil {
		return err
	}
	backend, err := newFactoryStateBackend(factoryStateBranch)
	if err != nil {
		return err
	}
	result, err := queue.ApplyOperationalCommand(context.Background(), backend, policy, command)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode operational command result: %w", err)
	}
	fmt.Println(string(encoded))
	return nil
}

func executeDispatchOperation(ctx context.Context, operation string, backend state.Backend, options dispatchOperationOptions, now time.Time) (any, error) {
	switch operation {
	case "inspect":
		return inspectDispatchAttempt(ctx, backend, options)
	case "claim":
		return claimDispatchRequest(ctx, backend, options, now)
	case "bind":
		return bindDispatchRun(ctx, backend, options, now)
	case "heartbeat":
		return heartbeatDispatchRun(ctx, backend, options, now)
	case "recover":
		return recoverDispatchRun(ctx, backend, options, now)
	default:
		return nil, fmt.Errorf("unknown --dispatch-operation %q", operation)
	}
}

func inspectDispatchAttempt(ctx context.Context, backend state.Backend, options dispatchOperationOptions) (any, error) {
	if options.attemptID == "" {
		return nil, fmt.Errorf("inspect requires --attempt-id")
	}
	return queue.InspectAttempt(ctx, backend, options.attemptID)
}

func claimDispatchRequest(ctx context.Context, backend state.Backend, options dispatchOperationOptions, now time.Time) (any, error) {
	if options.requestID == "" || options.dispatcherID == "" || options.leaseSeconds <= 0 {
		return nil, fmt.Errorf("claim requires --request-id, --dispatcher-id, and positive --lease-seconds")
	}
	claim, err := queue.ClaimRequest(ctx, backend, options.requestID, options.dispatcherID, now, time.Duration(options.leaseSeconds)*time.Second)
	if err == queue.ErrNoDispatchableWork {
		return map[string]string{"status": "no_work"}, nil
	}
	return claim, err
}

func bindDispatchRun(ctx context.Context, backend state.Backend, options dispatchOperationOptions, now time.Time) (any, error) {
	if options.attemptID == "" || options.actionsRunID == "" || options.actionsRunAttempt == 0 {
		return nil, fmt.Errorf("bind requires --attempt-id, --actions-run-id, and --actions-run-attempt")
	}
	return queue.BindActionsRun(ctx, backend, options.attemptID, options.actionsRunID, options.actionsRunAttempt, now)
}

func heartbeatDispatchRun(ctx context.Context, backend state.Backend, options dispatchOperationOptions, now time.Time) (any, error) {
	if options.attemptID == "" || options.actionsRunID == "" || options.fence == 0 || options.leaseSeconds <= 0 {
		return nil, fmt.Errorf("heartbeat requires --attempt-id, --actions-run-id, --fence, and positive --lease-seconds")
	}
	return queue.Heartbeat(ctx, backend, options.attemptID, options.actionsRunID, options.fence, now, now.Add(time.Duration(options.leaseSeconds)*time.Second))
}

func recoverDispatchRun(ctx context.Context, backend state.Backend, options dispatchOperationOptions, now time.Time) (any, error) {
	if options.attemptID == "" || options.actionsObservationPath == "" {
		return nil, fmt.Errorf("recover requires --attempt-id and --actions-observation")
	}
	data, err := os.ReadFile(options.actionsObservationPath)
	if err != nil {
		return nil, fmt.Errorf("read Actions observation: %w", err)
	}
	var observation queue.ActionsObservation
	if err = decodeOne(data, &observation); err != nil {
		return nil, fmt.Errorf("parse Actions observation: %w", err)
	}
	return queue.RecoverAttempt(ctx, backend, options.attemptID, now, observation)
}

func appCredentials() (string, string) {
	appID := os.Getenv("AVM_WRITER_APP_ID")
	if appID == "" {
		appID = os.Getenv("APP_ID")
	}
	privateKey := os.Getenv("AVM_WRITER_PRIVATE_KEY")
	if privateKey == "" {
		privateKey = os.Getenv("APP_PRIVATE_KEY")
	}
	return appID, privateKey
}

func newFactoryStateBackend(branch string) (state.GitHubBackend, error) {
	if err := validateFactoryStateBranch(branch); err != nil {
		return state.GitHubBackend{}, err
	}
	token := strings.TrimSpace(os.Getenv("FACTORY_STATE_TOKEN"))
	if token == "" {
		return state.GitHubBackend{}, fmt.Errorf("FACTORY_STATE_TOKEN is required for state operations")
	}
	return state.GitHubBackend{Owner: factoryOwner, Repository: factoryRepository, Branch: branch, Token: token}, nil
}

type dispatchOperationOptions struct {
	dispatcherID           string
	requestID              string
	attemptID              string
	actionsRunID           string
	actionsRunAttempt      uint64
	leaseSeconds           int
	fence                  uint64
	actionsObservationPath string
}

func parseModuleIDs(value string) (map[string]bool, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	allowed := map[string]bool{}
	for _, raw := range strings.Split(strings.ReplaceAll(value, "\n", ","), ",") {
		moduleID := strings.TrimSpace(raw)
		if moduleID == "" {
			continue
		}
		if !strings.HasPrefix(moduleID, "avm-") {
			return nil, fmt.Errorf("invalid module ID %q", moduleID)
		}
		allowed[moduleID] = true
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("at least one module ID is required")
	}
	return allowed, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
