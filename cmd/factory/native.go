package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/github"
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
	stateBranch := flags.String("state-branch", "", "Explicit factory-state branch for state operations")
	extractorContract := flags.String("extractor-contract", "", "Reviewed native extractor contract")
	extractorCapabilities := flags.String("extractor-capabilities", "", "Reviewed native extractor capabilities")
	dispatch := flags.String("dispatch-operation", "", "Native claim, inspect, bind, heartbeat or recover operation")
	dispatcherID := flags.String("dispatcher-id", "", "Stable owner workflow/run identity")
	requestID := flags.String("request-id", "", "Exact queued request ID for a claim")
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
	if handled, err := handleProviderCollectionCommand(flags, *providerCollection, *dataRoot); handled {
		return err
	}
	if (*discoverModules != "" || *discoveryResult != "") && *persist == "" {
		return fmt.Errorf("--discover-modules and --discovery-result-out require --persist-discovery")
	}
	if *requestID != "" && *dispatch != "claim" {
		return fmt.Errorf("--request-id requires --dispatch-operation claim")
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
		return persistAVMCatalog(*persist, *stateBranch, *extractorContract, *extractorCapabilities, *discoverModules, *discoveryResult)
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
			dispatcherID: *dispatcherID, requestID: *requestID, attemptID: *attemptID,
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
		"list-dimensions": true, "list-checks": true, "assess-facts": true, "build-indexes": true,
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
