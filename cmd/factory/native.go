package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/github"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/site"
)

// run routes only compiled factory commands; no external analyzer or
// local-file queue configuration is loaded implicitly.
func run(args []string) (runErr error) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{ReplaceAttr: utcLogTime}))
	started := time.Now()
	command := "factory"
	defer func() {
		logCommandCompletion(logger, command, started, runErr)
	}()
	flags := flag.NewFlagSet("factory", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	logLevel := flags.String("log-level", defaultLogLevel, "Minimum severity: debug, info, warn or error (FACTORY_LOG_LEVEL)")
	logFormat := flags.String("log-format", defaultLogFormat, "Log format: text or json (FACTORY_LOG_FORMAT)")
	mintProjection := flags.Bool("mint-projection-token", false, "Mint a reduced Issue/Project installation token")
	mintState := flags.Bool("mint-state-token", false, "Mint a repository-scoped Contents token for factory-state")
	discover := flags.Bool("discover-catalog", false, "Fetch and validate the official AVM catalogs without dispatching")
	catalogOut := flags.String("catalog-out", "", "Optional output path for the validated discovery observation")
	persist := flags.String("persist-discovery", "", "Persist a discovery observation to the explicit factory-state branch")
	discoverModules := flags.String("discover-modules", "", "Exact active module IDs to refresh without dispatching")
	discoveryResult := flags.String("discovery-result-out", "", "Optional discovery transaction result output")
	newDiscoveryModules := flags.Bool("discovery-new-modules-only", false, "Limit source resolution and follow-on work to newly added catalog module IDs")
	moduleCycle := flags.Bool("discovery-module-cycle", false, "Complete manual source/state/Issue/Project delivery for one module before the next")
	pacedDiscovery := flags.Bool("discovery-paced", false, "Use shared weighted App pacing and durable manual continuation")
	continuationID := flags.String("discovery-continuation", "", "Exact durable discovery cycle to resume without resolving a new scope")
	pacingState := flags.String("discovery-pacing-state", "", "Run-local rolling cost checkpoint for automatic same-job continuation")
	stateLayoutConfig := flags.String("state-layout-config", "", "Reviewed module-v2 storage contract for manual discovery")
	stateCheckoutRoot := flags.String("state-checkout-root", "", "Separate trusted state checkout used only for immutable blob caching")
	stateCheckoutTree := flags.String("state-checkout-tree", "", "NUL-delimited git ls-tree listing for the trusted state checkout")
	moduleTypes := flags.String("module-types", "", "Selected manual module types: res,ptn,utl")
	moduleNames := flags.String("module-names", "", "Comma-separated canonical manual module IDs")
	forceModules := flags.Bool("force", false, "Repeat the selected supported manual operation without bypassing safety checks")
	projectSelection := flags.Bool("project-discovery-selection", false, "Project selected existing durable lifecycle intents")
	flushProjection := flags.String("flush-projection-modules", "", "Flush only due existing delivery for exact canonical module IDs")
	stateBranch := flags.String("state-branch", "", "Explicit factory-state branch for state operations")
	extractorContract := flags.String("extractor-contract", "", "Reviewed native extractor contract")
	extractorCapabilities := flags.String("extractor-capabilities", "", "Reviewed native extractor capabilities")
	dispatch := flags.String("dispatch-operation", "", "Native claim, claim-next, inspect, bind, heartbeat or recover operation")
	dispatchCheckoutRoot := flags.String("dispatch-checkout-root", "", "Trusted factory-state checkout used only to seed immutable blob reads")
	dispatchCheckoutTree := flags.String("dispatch-checkout-tree", "", "NUL-delimited git ls-tree listing for the trusted dispatch checkout")
	dispatcherID := flags.String("dispatcher-id", "", "Stable owner workflow/run identity")
	requestID := flags.String("request-id", "", "Exact queued request ID for a claim")
	dispatchModuleID := flags.String("dispatch-module-id", "", "Exact module whose oldest ready request should be claimed")
	attemptID := flags.String("attempt-id", "", "Exact durable attempt ID")
	actionsRunID := flags.String("actions-run-id", "", "Exact Actions run ID")
	actionsRunAttempt := flags.Uint64("actions-run-attempt", 0, "Exact Actions run attempt")
	leaseSeconds := flags.Int("lease-seconds", 0, "Positive lease duration in seconds")
	fence := flags.Uint64("fence", 0, "Expected ownership fence")
	actionsObservation := flags.String("actions-observation", "", "Trusted Actions observation for recovery")
	operator := flags.String("operator-command", "", "Authorized pause, resume, retry, refresh or global control")
	operationID := flags.String("operation-id", "", "Stable operation idempotency key")
	operationModule := flags.String("operation-module-id", "", "Exact module ID for a scoped operation")
	operationReason := flags.String("operation-reason", "", "Required operation reason")
	operationsPolicy := flags.String("operations-policy", "config/operations.yaml", "Reviewed operator policy")
	dataRoot := flags.String("data-root", "data", "Native accepted data root")
	siteRoot := flags.String("site-root", "site", "Native indexed portal output root")
	buildSite := flags.Bool("build-indexed-site", false, "Render an accepted native presentation snapshot")
	exportPresentation := flags.Bool("export-presentation", false, "Build a reviewable native presentation proposal")
	presentationBase := flags.String("presentation-base", "", "Exact accepted commit for presentation export")
	presentationAsOf := flags.String("presentation-as-of", "", "Explicit RFC3339 presentation time")
	presentationOut := flags.String("presentation-proposal-out", "work/presentation-proposal", "Fresh presentation proposal output")
	projectModules := flags.String("project-discovery-modules", "", "Exact module IDs with durable lifecycle intents")
	projectRunURL := flags.String("project-discovery-run-url", "", "Projection run URL")
	projectRunID := flags.String("project-discovery-run-id", "", "Stable projection run/attempt identity")
	listDimensions := flags.Bool("list-dimensions", false, "List compiled native dimensions")
	listChecks := flags.String("list-checks", "", "List compiled checks for one dimension")
	assessFacts := flags.String("assess-facts", "", "Assess a native facts record offline")
	dimensionIDs := flags.String("dimension-ids", "", "Explicit selected assessment dimensions")
	providerSnapshots := flags.String("provider-snapshots", "", "Exact provider snapshot inputs")
	classifications := flags.String("classifications", "", "Reviewed classification overrides")
	assessmentOut := flags.String("assessment-out", "", "Optional native dimension result output")
	buildIndexes := flags.Bool("build-indexes", false, "Build native indexes into a fresh separate output root")
	indexRecords := flags.String("index-records", "", "Optional typed records input; otherwise read --data-root")
	indexesOut := flags.String("indexes-out", "work/index-candidate", "Fresh native index output directory")
	processWork := flags.String("process-work", "", "Trusted input for one native acquisition or assessment")
	workArtifactOut := flags.String("work-artifact-out", "", "Sanitized native worker artifact output")
	prepareWorker := flags.Bool("prepare-worker-input", false, "Bind and prepare trusted native worker input")
	workerRequest := flags.String("worker-request-id", "", "Exact expected worker request ID")
	workerInputOut := flags.String("worker-input-out", "", "Trusted native worker input output")
	sealWorker := flags.Bool("seal-worker", false, "Validate and seal native worker evidence")
	workerArtifact := flags.String("worker-artifact", "", "Sanitized native worker evidence")
	validationID := flags.String("validation-id", "", "Trusted validator contract for sealing")
	applyProposal := flags.Bool("apply-proposal", false, "Validate one constrained native proposal locally")
	proposalRoot := flags.String("proposal-root", "", "Native proposal envelope/payload root")
	proposalBase := flags.String("proposal-base", "", "Exact current accepted commit")
	acceptedDataBase := flags.String("accepted-data-base", "data", "Native accepted proposal base tree")
	proposalDataOut := flags.String("proposal-data-out", "work/proposal-data", "Fresh proposal candidate output")
	proposalChangedPaths := flags.String("proposal-changed-paths", "", "Exact NUL-delimited candidate changed paths")
	buildCatalogProposal := flags.Bool("build-catalog-proposal", false, "Export the durable native catalog outbox")
	catalogProposalOut := flags.String("catalog-proposal-out", "work/v2/catalog-proposal", "Fresh catalog proposal output")
	catalogProposalBase := flags.String("catalog-proposal-base", "", "Exact catalog proposal base commit")
	buildEvidenceProposal := flags.Bool("build-module-evidence-proposal", false, "Build a reviewed native evidence proposal")
	workerLaunch := flags.String("worker-launch-input", "", "Trusted native launch artifact")
	evidenceArtifact := flags.String("module-evidence-artifact", "", "Sealed native evidence artifact")
	evidenceProposalOut := flags.String("module-evidence-proposal-out", "work/v2/module-evidence-proposal", "Fresh evidence proposal output")
	evidenceProposalBase := flags.String("module-evidence-proposal-base", "", "Exact evidence proposal base commit")
	evidenceValidationID := flags.String("module-evidence-validation-id", "", "Trusted evidence validator contract")
	evidenceCreatedAt := flags.String("module-evidence-proposal-created-at", "", "Deterministic native proposal timestamp")
	importEvidence := flags.Bool("import-evidence-receipt", false, "Import one verified native evidence merge receipt")
	evidenceAcceptance := flags.String("evidence-acceptance-input", "", "Exact native acceptance receipt input")
	providerCollection := registerProviderCollectionFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateNativeCommandSelection(flags); err != nil {
		return err
	}
	configured, err := factoryLogger(flags, *logLevel, *logFormat, os.Getenv, factoryLogOutput(flags, os.Stdout, os.Stderr))
	if err != nil {
		return err
	}
	logger = configured
	previousLogger := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(previousLogger)
	command = factoryCommandName(flags)
	logger.Info("factory command started", "command", command)
	manualScope, err := nativeManualScope(flags, *persist, *projectSelection || *moduleCycle, manualModuleScope{types: *moduleTypes, names: *moduleNames, force: *forceModules})
	if err != nil {
		return err
	}
	if err := validateDiscoveryControls(*newDiscoveryModules, *moduleCycle, *persist, *discoverModules, *projectRunURL, *projectRunID, manualScope); err != nil {
		return err
	}
	if err := validateStateStorageOptions(*moduleCycle, *stateLayoutConfig, *stateCheckoutRoot, *stateCheckoutTree); err != nil {
		return err
	}
	if (*pacedDiscovery || *continuationID != "") && !*moduleCycle {
		return fmt.Errorf("paced discovery requires --discovery-module-cycle")
	}
	if *continuationID != "" && !*pacedDiscovery {
		return fmt.Errorf("continuation requires --discovery-paced")
	}
	if *pacingState != "" && !*pacedDiscovery {
		return fmt.Errorf("pacing state requires --discovery-paced")
	}
	if *continuationID != "" {
		if err := identity.ValidateRecordID("discovery-cycle", *continuationID); err != nil {
			return err
		}
		if strings.TrimSpace(*moduleNames) != "" || *forceModules {
			return fmt.Errorf("continuation uses durable scope; do not supply module names or force")
		}
	}
	if handled, err := handleProviderCollectionCommand(flags, *providerCollection, *dataRoot); handled {
		return err
	}
	if (*requestID != "" || *dispatchModuleID != "") && *dispatch != "claim" {
		return fmt.Errorf("dispatch target options require --dispatch-operation claim")
	}
	if *requestID != "" && *dispatchModuleID != "" {
		return fmt.Errorf("claim accepts either --request-id or --dispatch-module-id, not both")
	}
	if *evidenceAcceptance != "" && !*importEvidence {
		return fmt.Errorf("--evidence-acceptance-input requires --import-evidence-receipt")
	}
	if handled, err := handleOfflineDataCommand(offlineDataCommandOptions{
		buildIndexes: *buildIndexes, indexRecords: *indexRecords, dataRoot: *dataRoot, indexesOut: *indexesOut,
		applyProposal: *applyProposal, proposalRoot: *proposalRoot, proposalBase: *proposalBase,
		proposalDataBase: *acceptedDataBase, proposalDataOut: *proposalDataOut, proposalChangedPaths: *proposalChangedPaths,
	}); handled {
		return err
	}
	if handled, err := handleWorkerCommand(*processWork, *workArtifactOut, trustedWorkerOptions{
		prepare: *prepareWorker, seal: *sealWorker, attemptID: *attemptID, requestID: *workerRequest,
		dispatcherID: *dispatcherID, runID: *actionsRunID, runAttempt: *actionsRunAttempt, fence: *fence,
		stateBranch: *stateBranch, dataRoot: *dataRoot, inputOutput: *workerInputOut,
		artifactPath: *workerArtifact, validationID: *validationID,
		checkoutRoot: *dispatchCheckoutRoot, checkoutTree: *dispatchCheckoutTree,
	}); handled {
		return err
	}
	switch {
	case *mintProjection || *mintState:
		return mintNativeToken(*mintState)
	case *importEvidence:
		return importEvidenceAcceptance(*stateBranch, *evidenceAcceptance)
	case *buildCatalogProposal:
		return buildDiscoveryCatalogProposal(*stateBranch, *catalogProposalBase, *dataRoot, *catalogProposalOut)
	case *buildEvidenceProposal:
		return buildModuleEvidenceProposal(*workerLaunch, *evidenceArtifact, *evidenceProposalBase, *dataRoot, *evidenceProposalOut, *evidenceValidationID, *evidenceCreatedAt)
	case *listDimensions:
		return listAssessmentDefinitions(os.Stdout, "")
	case *listChecks != "":
		return listAssessmentDefinitions(os.Stdout, *listChecks)
	case *assessFacts != "":
		return runOfflineAssessment(*assessFacts, *providerSnapshots, *classifications, *dimensionIDs, *assessmentOut)
	case *persist != "":
		return runDiscoveryPersistence(discoveryPersistenceCommand{
			paced: *pacedDiscovery, continuationID: *continuationID,
			pacingState:  *pacingState,
			layoutConfig: *stateLayoutConfig, checkoutRoot: *stateCheckoutRoot, checkoutTree: *stateCheckoutTree,
			candidatePath: *persist, branch: *stateBranch, contract: *extractorContract, capabilities: *extractorCapabilities,
			forcedIDs: *discoverModules, resultPath: *discoveryResult, scope: manualScope, newOnly: *newDiscoveryModules, cycle: *moduleCycle,
			identity: discoveryCycleIdentity{runURL: *projectRunURL, runID: *projectRunID},
		})
	case *projectSelection:
		return projectManualDiscoveryScope(*stateBranch, *projectRunURL, *projectRunID, *manualScope)
	case *flushProjection != "":
		return flushModuleProjection(*stateBranch, *flushProjection, *projectRunURL, *projectRunID)
	case *projectModules != "":
		return projectDiscoveredModules(*stateBranch, *projectModules, *projectRunURL, *projectRunID)
	case *exportPresentation:
		return exportPresentationProposal(*dataRoot, *presentationOut, *presentationBase, *presentationAsOf)
	case *buildSite:
		return site.BuildIndexed(*dataRoot, *siteRoot)
	case *operator != "":
		return runOperationalCommand(*operationsPolicy, *operationID, *operator, *operationModule, *operationReason)
	case *dispatch != "":
		return runDispatchOperation(*dispatch, *stateBranch, dispatchOperationOptions{
			dispatcherID: *dispatcherID, requestID: *requestID, moduleID: *dispatchModuleID,
			checkoutRoot: *dispatchCheckoutRoot, checkoutTree: *dispatchCheckoutTree, attemptID: *attemptID,
			actionsRunID: *actionsRunID, actionsRunAttempt: *actionsRunAttempt,
			leaseSeconds: *leaseSeconds, fence: *fence, actionsObservationPath: *actionsObservation,
		})
	case *discover:
		return discoverAVMCatalog(*catalogOut)
	case *dimensionIDs != "" || *providerSnapshots != "" || *classifications != "" || *assessmentOut != "":
		return fmt.Errorf("assessment options require --assess-facts")
	default:
		return fmt.Errorf("select an explicit native factory command; use --help to list commands")
	}
}

// nativeManualScope validates manual-only options before credential or data access.
func validateStateStorageOptions(cycle bool, layoutConfig, checkoutRoot, checkoutTree string) error {
	if (layoutConfig != "" || checkoutRoot != "" || checkoutTree != "") && !cycle {
		return fmt.Errorf("state layout and checkout options require --discovery-module-cycle")
	}
	if (checkoutRoot == "") != (checkoutTree == "") {
		return fmt.Errorf("state checkout root and tree listing must be supplied together")
	}
	return nil
}

// validateDiscoveryControls rejects incompatible scope before operational work.
func validateDiscoveryControls(newOnly, cycle bool, persistPath, forcedIDs, runURL, runID string, scope *manualModuleScope) error {
	if err := validateNewDiscoveryScope(newOnly, persistPath, forcedIDs, scope); err != nil {
		return err
	}
	if !cycle {
		return nil
	}
	if persistPath == "" || forcedIDs != "" || scope == nil {
		return fmt.Errorf("--discovery-module-cycle requires --persist-discovery with manual selection and no --discover-modules")
	}
	return validateDiscoveryProjectionIdentity(runURL, runID)
}

// validateNewDiscoveryScope keeps autonomous new-ID scope separate from manual controls.
func validateNewDiscoveryScope(enabled bool, persistPath, forcedIDs string, scope *manualModuleScope) error {
	if enabled && (persistPath == "" || scope != nil || forcedIDs != "") {
		return fmt.Errorf("--discovery-new-modules-only requires --persist-discovery without manual or forced selection")
	}
	return nil
}

func nativeManualScope(flags *flag.FlagSet, persistPath string, required bool, scope manualModuleScope) (*manualModuleScope, error) {
	if (flags.Lookup("discover-modules").Value.String() != "" || flags.Lookup("discovery-result-out").Value.String() != "") && persistPath == "" {
		return nil, fmt.Errorf("--discover-modules and --discovery-result-out require --persist-discovery")
	}
	selected := required
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "module-types" || option.Name == "module-names" || option.Name == "force" {
			selected = true
		}
	})
	if !selected {
		return nil, nil
	}
	if persistPath == "" && !required {
		return nil, fmt.Errorf("manual module options require --persist-discovery or --project-discovery-selection")
	}
	if strings.TrimSpace(scope.names) == "" {
		if _, err := manualModuleTypes(scope.types); err != nil {
			return nil, err
		}
	}
	return &scope, nil
}

// mintNativeToken keeps job-specific App permission selection separate from
// command dispatch; missing credentials fail before any token request.
func mintNativeToken(stateWriter bool) error {
	appID, privateKey := appCredentials()
	mint := github.MintToken
	if stateWriter {
		mint = github.MintStateToken
	}
	token, err := mint(context.Background(), appID, privateKey)
	if err != nil {
		return err
	}
	fmt.Print(token)
	return nil
}

// validateNativeCommandSelection rejects conflicting commands before any
// credential lookup, source fetch, or state mutation can occur.
func validateNativeCommandSelection(flags *flag.FlagSet) error {
	if len(flags.Args()) != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	modes := map[string]bool{
		"mint-projection-token": true, "mint-state-token": true, "discover-catalog": true,
		"persist-discovery": true, "dispatch-operation": true, "operator-command": true,
		"build-indexed-site": true, "export-presentation": true, "project-discovery-modules": true,
		"project-discovery-selection": true,
		"flush-projection-modules":    true,
		"list-dimensions":             true, "list-checks": true, "assess-facts": true, "build-indexes": true,
		"process-work": true, "prepare-worker-input": true, "seal-worker": true, "apply-proposal": true,
		"build-catalog-proposal": true, "build-module-evidence-proposal": true,
		"import-evidence-receipt": true, "collect-provider-catalog": true,
	}
	selected := 0
	flags.Visit(func(option *flag.Flag) {
		if !modes[option.Name] {
			return
		}
		value := option.Value.(flag.Getter).Get()
		switch value := value.(type) {
		case bool:
			if value {
				selected++
			}
		case string:
			if value != "" {
				selected++
			}
		}
	})
	if selected > 1 {
		return fmt.Errorf("native command modes cannot be combined")
	}
	return nil
}
