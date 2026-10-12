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
	mintState := flags.Bool("mint-state-token", false, "Mint a repository-scoped Contents token for factory-state")
	mintReview := flags.Bool("mint-review-token", false, "Mint a reduced review PR token without Contents write")
	discover := flags.Bool("discover-catalog", false, "Fetch and validate the official AVM catalogs without dispatching")
	catalogOut := flags.String("catalog-out", "", "Optional output path for the validated discovery observation")
	persist := flags.String("persist-discovery", "", "Persist a discovery observation to the explicit factory-state branch")
	discoverModules := flags.String("discover-modules", "", "Exact active module IDs to refresh without dispatching")
	discoveryResult := flags.String("discovery-result-out", "", "Optional discovery transaction result output")
	newDiscoveryModules := flags.Bool("discovery-new-modules-only", false, "Limit source resolution and follow-on work to newly added catalog module IDs")
	moduleCycle := flags.Bool("discovery-module-cycle", false, "Complete manual source and durable state intents for one module before the next")
	pacedDiscovery := flags.Bool("discovery-paced", false, "Use shared weighted App pacing and durable manual continuation")
	continuationID := flags.String("discovery-continuation", "", "Exact durable discovery cycle to resume without resolving a new scope")
	pacingState := flags.String("discovery-pacing-state", "", "Run-local rolling cost checkpoint for automatic same-job continuation")
	stateCheckoutRoot := flags.String("state-checkout-root", "", "Separate trusted factory-state checkout used for verified Git object transport")
	moduleTypes := flags.String("module-types", "", "Selected manual module types: res,ptn,utl")
	moduleNames := flags.String("module-names", "", "Comma-separated canonical manual module IDs")
	forceModules := flags.Bool("force", false, "Repeat the selected supported manual operation without bypassing safety checks")
	requestProjectionRecovery := flags.Bool("request-projection-recovery", false, "Queue selected durable projection intents for the scheduled projector")
	projectDue := flags.Bool("project-due-intents", false, "Poll due durable projection intents")
	listProjectionRecoveryCandidates := flags.Bool("list-projection-recovery-candidates", false, "List expired projection owners with exact Actions attempts")
	recoverProjectionOwner := flags.String("recover-projection-owner", "", "Release one expired projection owner after its exact Actions attempt is verified terminal")
	resolveProjectionOwnerEffects := flags.String("resolve-projection-owner-effects", "", "Read back exact Issue/Project effects for selected parked module IDs and adopt only complete exact matches")
	projectionOwnerSkip := flags.String("projection-owner-skip", "", "Report a structured recovery skip for one exact projection owner")
	projectionOwnerSkipReason := flags.String("projection-owner-skip-reason", "", "Allowlisted reason for a projection owner recovery skip")
	projectOwnerRunID := flags.Uint64("project-owner-run-id", 0, "Exact current GitHub Actions run ID for a scheduled projection claim")
	projectOwnerRunAttempt := flags.Uint64("project-owner-run-attempt", 0, "Exact current GitHub Actions run attempt for a scheduled projection claim")
	projectMaxModules := flags.Int("project-max-modules", 50, "Projection run ceiling: 10, 50 (default), or 100 modules")
	requestProjectionFlush := flags.String("request-projection-flush", "", "Request scheduled delivery for exact canonical modules with due existing intents")
	stateBranch := flags.String("state-branch", "", "Explicit factory-state branch for state operations")
	extractorContract := flags.String("extractor-contract", "", "Reviewed native extractor contract")
	extractorCapabilities := flags.String("extractor-capabilities", "", "Reviewed native extractor capabilities")
	dispatch := flags.String("dispatch-operation", "", "Native claim, claim-next, inspect, bind, heartbeat or recover operation")
	dispatcherID := flags.String("dispatcher-id", "", "Stable owner workflow/run identity")
	producerWorkflowID := flags.String("producer-workflow-id", "", "Trusted producer workflow ID for durable stage handoffs")
	producerCommit := flags.String("producer-commit", "", "Reviewed producer code commit SHA for durable stage handoffs")
	producerRunID := flags.String("producer-run-id", "", "Trusted Actions run ID for durable stage handoffs")
	producerRunAttempt := flags.Uint64("producer-run-attempt", 0, "Trusted Actions run attempt for durable stage handoffs")
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
	prepareSitePublication := flags.Bool("prepare-indexed-site-publication", false, "Build one exact combined site and persist its publication generation")
	wakePilotAssessments := flags.Bool("wake-pilot-assessments", false, "Queue exact accepted assessment inputs for the approved ten-module pilot without preparing or deploying a site")
	authorizeSitePublication := flags.Bool("authorize-indexed-site-publication", false, "Authorize the exact prepared site generation after its environment gate")
	importSiteDeployment := flags.Bool("import-indexed-site-deployment", false, "Import one verified Pages deployment receipt and replay module acknowledgements")
	publicationManifest := flags.String("publication-manifest", "", "Exact combined site publication manifest")
	publicationAcceptedCommit := flags.String("publication-accepted-commit", "", "Exact accepted main commit used to prepare the combined site")
	publicationProducerRun := flags.String("publication-producer-run", "", "Exact workflow run and attempt identity for the site artifact")
	publicationPagesDeploymentID := flags.String("publication-pages-deployment-id", "", "Pages deployment key (the exact accepted commit SHA)")
	publicationActor := flags.String("publication-actor", "", "Workflow dispatch actor authorizing the prepared site")
	publicationTime := flags.String("publication-time", "", "RFC3339 time for preparation, authorization, or deployment import")
	publicationModuleNames := flags.String("publication-module-names", "", "Exact active module IDs eligible for assessment planning during this full-catalog site publication")
	assessmentWakeAcceptedCommit := flags.String("assessment-wake-accepted-commit", "", "Exact accepted main commit for the bounded pilot assessment wake")
	assessmentWakeProducerRun := flags.String("assessment-wake-producer-run", "", "Exact manual assessment wake workflow run and attempt")
	assessmentWakeModuleNames := flags.String("assessment-wake-module-names", "", "Exact approved ten-module pilot scope")
	exportPresentation := flags.Bool("export-presentation", false, "Build a reviewable native presentation proposal")
	presentationBase := flags.String("presentation-base", "", "Exact accepted commit for presentation export")
	presentationAsOf := flags.String("presentation-as-of", "", "Explicit RFC3339 presentation time")
	presentationOut := flags.String("presentation-proposal-out", "work/presentation-proposal", "Fresh presentation proposal output")
	projectRunURL := flags.String("project-discovery-run-url", "", "Projection run URL for explicit projection commands")
	projectRunID := flags.String("project-discovery-run-id", "", "Stable projection run/attempt identity for explicit projection commands")
	discoveryRunID := flags.String("discovery-run-id", "", "Exact Actions run ID for a manual discovery cycle")
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
	workerWorkflowID := flags.String("worker-workflow-id", "", "Reviewed workflow identity producing trusted worker completion")
	workerProducerCommit := flags.String("worker-producer-commit", "", "Reviewed source commit producing trusted worker completion")
	applyProposal := flags.Bool("apply-proposal", false, "Validate one constrained native proposal locally")
	proposalRoot := flags.String("proposal-root", "", "Native proposal envelope/payload root")
	proposalBase := flags.String("proposal-base", "", "Exact current accepted commit")
	acceptedDataBase := flags.String("accepted-data-base", "data", "Native accepted proposal base tree")
	proposalDataOut := flags.String("proposal-data-out", "work/proposal-data", "Fresh proposal candidate output")
	proposalChangedPaths := flags.String("proposal-changed-paths", "", "Exact NUL-delimited candidate changed paths")
	buildCatalogProposal := flags.Bool("build-catalog-proposal", false, "Export the durable native catalog outbox")
	catalogProposalOut := flags.String("catalog-proposal-out", "work/v2/catalog-proposal", "Fresh catalog proposal output")
	catalogProposalBase := flags.String("catalog-proposal-base", "", "Exact catalog proposal base commit")
	buildSelectionExport := flags.Bool("build-selection-export-proposal", false, "Build a reviewed proposal from one pending durable selection export")
	selectionExportID := flags.String("selection-export-id", "", "Exact pending selection receipt ID")
	selectionExportModuleID := flags.String("selection-export-module-id", "", "Exact module owning the pending selection")
	selectionExportBase := flags.String("selection-export-base", "", "Exact accepted commit for selection export")
	selectionExportOut := flags.String("selection-export-out", "work/v2/selection-proposal", "Fresh selection proposal output")
	buildEvidenceProposal := flags.Bool("build-module-evidence-proposal", false, "Build a reviewed native evidence proposal")
	prepareDelivery := flags.Bool("prepare-evidence-delivery", false, "Prepare a proposal only after validating its persisted sealed completion")
	openReview := flags.Bool("open-evidence-review", false, "Open or reuse an exact-head typed evidence review PR")
	reviewInput := flags.String("evidence-review-input", "", "Exact proposal/base/head review identity JSON")
	reviewRoot := flags.String("evidence-review-repository-root", ".", "Trusted local Git object transport for review validation")
	workerLaunch := flags.String("worker-launch-input", "", "Trusted native launch artifact")
	evidenceArtifact := flags.String("module-evidence-artifact", "", "Sealed native evidence artifact")
	evidenceProposalOut := flags.String("module-evidence-proposal-out", "work/v2/module-evidence-proposal", "Fresh evidence proposal output")
	evidenceProposalBase := flags.String("module-evidence-proposal-base", "", "Exact evidence proposal base commit")
	evidenceValidationID := flags.String("module-evidence-validation-id", "", "Trusted evidence validator contract")
	evidenceCreatedAt := flags.String("module-evidence-proposal-created-at", "", "Deterministic native proposal timestamp")
	importEvidence := flags.Bool("import-evidence-receipt", false, "Import one verified native evidence merge receipt")
	evidenceAcceptance := flags.String("evidence-acceptance-input", "", "Exact native acceptance receipt input")
	evidenceAcceptedDataRoot := flags.String("evidence-accepted-data-root", "data", "Exact accepted data tree used to derive facts acceptance contexts")
	assessmentTrackPolicy := flags.String("assessment-track-policy", "config/providers.yaml", "Reviewed provider track policy used for facts acceptance")
	importProviderInputs := flags.Bool("import-provider-snapshot-inputs", false, "Import assessment generations for an exact accepted provider snapshot")
	providerInputsImport := flags.String("provider-snapshot-input-import", "", "Exact accepted provider snapshot import input")
	providerInputsDataRoot := flags.String("provider-snapshot-input-data-root", "data", "Exact accepted data tree containing the provider snapshot")
	providerCollection := registerProviderCollectionFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := validateNativeCommandSelection(flags); err != nil {
		return err
	}
	if *openReview {
		allowed := map[string]bool{"open-evidence-review": true, "evidence-review-input": true, "evidence-review-repository-root": true, "log-level": true, "log-format": true}
		var unsupported string
		flags.Visit(func(option *flag.Flag) {
			if !allowed[option.Name] {
				unsupported = option.Name
			}
		})
		if unsupported != "" {
			return fmt.Errorf("--%s is unsupported for evidence review", unsupported)
		}
		if *reviewInput == "" {
			return fmt.Errorf("evidence review input is required")
		}
	} else if *reviewInput != "" || *reviewRoot != "." {
		return fmt.Errorf("evidence review options require --open-evidence-review")
	}
	if *prepareDelivery {
		allowed := map[string]bool{"prepare-evidence-delivery": true, "state-branch": true, "state-checkout-root": true, "attempt-id": true, "worker-launch-input": true, "module-evidence-artifact": true, "module-evidence-proposal-base": true, "module-evidence-proposal-out": true, "data-root": true, "log-level": true, "log-format": true}
		var unsupported string
		flags.Visit(func(option *flag.Flag) {
			if !allowed[option.Name] {
				unsupported = option.Name
			}
		})
		if unsupported != "" {
			return fmt.Errorf("--%s is unsupported for evidence delivery", unsupported)
		}
	}
	publicationCommandCount := 0
	for _, selected := range []bool{*prepareSitePublication, *authorizeSitePublication, *importSiteDeployment} {
		if selected {
			publicationCommandCount++
		}
	}
	if publicationCommandCount > 1 {
		return fmt.Errorf("indexed site publication commands are mutually exclusive")
	}
	if publicationCommandCount != 0 {
		allowed := map[string]bool{
			"prepare-indexed-site-publication": true, "authorize-indexed-site-publication": true,
			"import-indexed-site-deployment": true, "publication-manifest": true,
			"publication-accepted-commit": true, "publication-producer-run": true,
			"publication-pages-deployment-id": true, "publication-actor": true,
			"publication-time": true, "publication-module-names": true, "data-root": true, "site-root": true,
			"state-branch": true, "state-checkout-root": true,
			"log-level": true, "log-format": true,
		}
		var unsupported string
		flags.Visit(func(selected *flag.Flag) {
			if !allowed[selected.Name] {
				unsupported = selected.Name
			}
		})
		if unsupported != "" {
			return fmt.Errorf("--%s is unsupported for indexed site publication", unsupported)
		}
		if *publicationTime == "" {
			return fmt.Errorf("--publication-time is required for indexed site publication")
		}
		switch {
		case *prepareSitePublication:
			if identity.ValidateSHA1(*publicationAcceptedCommit) != nil || !validPublicationProducerRun(*publicationProducerRun) || strings.TrimSpace(*publicationModuleNames) == "" || *publicationManifest != "" || *publicationPagesDeploymentID != "" || *publicationActor != "" {
				return fmt.Errorf("site preparation requires an exact commit, producer run, and module scope only")
			}
		case *authorizeSitePublication:
			if strings.TrimSpace(*publicationManifest) == "" || strings.TrimSpace(*publicationActor) == "" || *publicationAcceptedCommit != "" || *publicationProducerRun != "" || *publicationPagesDeploymentID != "" || *publicationModuleNames != "" {
				return fmt.Errorf("site authorization requires the prepared manifest and workflow actor only")
			}
		case *importSiteDeployment:
			if strings.TrimSpace(*publicationManifest) == "" || strings.TrimSpace(*publicationPagesDeploymentID) == "" || *publicationAcceptedCommit != "" || *publicationProducerRun != "" || *publicationActor != "" || *publicationModuleNames != "" {
				return fmt.Errorf("Pages receipt import requires the prepared manifest and exact deployment ID only")
			}
		}
	}
	if *wakePilotAssessments {
		allowed := map[string]bool{
			"wake-pilot-assessments": true, "assessment-wake-accepted-commit": true,
			"assessment-wake-producer-run": true, "assessment-wake-module-names": true,
			"publication-time": true, "data-root": true, "state-branch": true,
			"state-checkout-root": true, "log-level": true, "log-format": true,
		}
		var unsupported string
		flags.Visit(func(selected *flag.Flag) {
			if !allowed[selected.Name] {
				unsupported = selected.Name
			}
		})
		if unsupported != "" {
			return fmt.Errorf("--%s is unsupported for pilot assessment wake", unsupported)
		}
		if publicationCommandCount != 0 || identity.ValidateSHA1(*assessmentWakeAcceptedCommit) != nil ||
			!validPublicationProducerRun(*assessmentWakeProducerRun) || strings.TrimSpace(*assessmentWakeModuleNames) == "" || strings.TrimSpace(*publicationTime) == "" {
			return fmt.Errorf("pilot assessment wake requires its exact accepted commit, workflow run, ten-module scope, and time only")
		}
	} else if flagWasProvided(flags, "assessment-wake-accepted-commit") || flagWasProvided(flags, "assessment-wake-producer-run") || flagWasProvided(flags, "assessment-wake-module-names") {
		return fmt.Errorf("assessment wake inputs require --wake-pilot-assessments")
	}
	if *buildSelectionExport {
		allowed := map[string]bool{
			"build-selection-export-proposal": true, "selection-export-id": true,
			"selection-export-module-id": true, "selection-export-base": true,
			"selection-export-out": true, "data-root": true,
			"state-branch": true, "state-checkout-root": true,
			"log-level": true, "log-format": true,
		}
		var unsupported string
		flags.Visit(func(option *flag.Flag) {
			if !allowed[option.Name] {
				unsupported = option.Name
			}
		})
		if unsupported != "" {
			return fmt.Errorf("--%s is unsupported for selection export proposal", unsupported)
		}
		if *selectionExportID == "" || *selectionExportModuleID == "" || *selectionExportBase == "" {
			return fmt.Errorf("selection export requires its receipt ID, module ID, and exact base commit")
		}
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
	manualScope, err := nativeManualScope(flags, *persist, *requestProjectionRecovery || *moduleCycle, manualModuleScope{types: *moduleTypes, names: *moduleNames, force: *forceModules})
	if err != nil {
		return err
	}
	if err := validateDiscoveryControls(*newDiscoveryModules, *moduleCycle, *persist, *discoverModules, manualScope); err != nil {
		return err
	}
	if err := validateDiscoveryCycleRunIdentity(*moduleCycle, *discoveryRunID); err != nil {
		return err
	}
	if *requestProjectionRecovery && manualScope == nil {
		return fmt.Errorf("--request-projection-recovery requires exact module selection")
	}
	if *projectDue {
		allowed := map[string]bool{
			"project-due-intents": true, "state-branch": true, "state-checkout-root": true,
			"project-discovery-run-url": true, "project-discovery-run-id": true, "project-max-modules": true,
			"project-owner-run-id": true, "project-owner-run-attempt": true,
			"log-level": true, "log-format": true,
		}
		var unsupported string
		flags.Visit(func(option *flag.Flag) {
			if !allowed[option.Name] {
				unsupported = option.Name
			}
		})
		if unsupported != "" {
			return fmt.Errorf("--%s is unsupported for scheduled projection polling", unsupported)
		}
		if !validProjectionModuleCeiling(*projectMaxModules) {
			return fmt.Errorf("--project-max-modules must be 10, 50, or 100")
		}
		if err := validateDiscoveryProjectionIdentity(*projectRunURL, *projectRunID); err != nil {
			return err
		}
		if *projectOwnerRunID == 0 || *projectOwnerRunAttempt == 0 {
			return fmt.Errorf("scheduled projection requires exact Actions run ID and attempt")
		}
	}
	if *listProjectionRecoveryCandidates || *recoverProjectionOwner != "" {
		allowed := map[string]bool{"list-projection-recovery-candidates": true, "recover-projection-owner": true, "actions-observation": true,
			"state-branch": true, "state-checkout-root": true, "log-level": true, "log-format": true}
		var unsupported string
		flags.Visit(func(option *flag.Flag) {
			if !allowed[option.Name] {
				unsupported = option.Name
			}
		})
		if unsupported != "" {
			return fmt.Errorf("--%s is unsupported for projection owner recovery", unsupported)
		}
		if *listProjectionRecoveryCandidates && (*recoverProjectionOwner != "" || *actionsObservation != "") {
			return fmt.Errorf("candidate listing cannot include a recovery observation")
		}
		if !*listProjectionRecoveryCandidates && (*recoverProjectionOwner == "" || *actionsObservation == "") {
			return fmt.Errorf("owner recovery requires the exact owner and Actions observation")
		}
	}
	if *resolveProjectionOwnerEffects != "" {
		allowed := map[string]bool{"resolve-projection-owner-effects": true, "state-branch": true, "state-checkout-root": true, "log-level": true, "log-format": true}
		var unsupported string
		flags.Visit(func(option *flag.Flag) {
			if !allowed[option.Name] {
				unsupported = option.Name
			}
		})
		if unsupported != "" {
			return fmt.Errorf("--%s is unsupported for exact projection-effect readback", unsupported)
		}
		if _, err := exactFlushModuleIDs(*resolveProjectionOwnerEffects); err != nil {
			return err
		}
	}
	if *projectionOwnerSkip != "" {
		allowed := map[string]bool{"projection-owner-skip": true, "projection-owner-skip-reason": true, "log-level": true, "log-format": true}
		var unsupported string
		flags.Visit(func(option *flag.Flag) {
			if !allowed[option.Name] {
				unsupported = option.Name
			}
		})
		if unsupported != "" {
			return fmt.Errorf("--%s is unsupported for projection owner skip reporting", unsupported)
		}
		if *projectionOwnerSkipReason == "" {
			return fmt.Errorf("projection owner skip reason is required")
		}
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
	if (*requestID != "" || *dispatchModuleID != "") && *dispatch != "claim" {
		return fmt.Errorf("dispatch target options require --dispatch-operation claim")
	}
	if (*producerWorkflowID != "" || *producerCommit != "" || *producerRunID != "" || *producerRunAttempt != 0) && *dispatch != "claim" && *dispatch != "claim-next" {
		return fmt.Errorf("producer provenance options require a claim dispatch operation")
	}
	if (*workerWorkflowID != "" || *workerProducerCommit != "") && !*sealWorker {
		return fmt.Errorf("worker producer provenance options require --seal-worker")
	}
	if *sealWorker && (*workerWorkflowID == "" || *workerProducerCommit == "") {
		return fmt.Errorf("--seal-worker requires trusted workflow ID and producer commit provenance")
	}
	if *requestID != "" && *dispatchModuleID != "" {
		return fmt.Errorf("claim accepts either --request-id or --dispatch-module-id, not both")
	}
	if *evidenceAcceptance != "" && !*importEvidence {
		return fmt.Errorf("--evidence-acceptance-input requires --import-evidence-receipt")
	}
	if *providerInputsImport != "" && !*importProviderInputs {
		return fmt.Errorf("--provider-snapshot-input-import requires --import-provider-snapshot-inputs")
	}
	if *importProviderInputs {
		allowed := map[string]bool{
			"import-provider-snapshot-inputs": true, "provider-snapshot-input-import": true,
			"provider-snapshot-input-data-root": true, "state-branch": true, "state-checkout-root": true,
			"log-level": true, "log-format": true,
		}
		var unsupported string
		flags.Visit(func(option *flag.Flag) {
			if !allowed[option.Name] {
				unsupported = option.Name
			}
		})
		if unsupported != "" {
			return fmt.Errorf("--%s is unsupported for provider snapshot input import", unsupported)
		}
		if *providerInputsImport == "" {
			return fmt.Errorf("--provider-snapshot-input-import is required")
		}
	}
	if handled, err := handleProviderCollectionCommand(flags, *providerCollection, *dataRoot); handled {
		return err
	}
	if err := validateStateCommandFlags(flags, *stateBranch, *stateCheckoutRoot,
		flagWasProvided(flags, "persist-discovery") || flagWasProvided(flags, "dispatch-operation") || flagWasProvided(flags, "operator-command") ||
			flagWasProvided(flags, "request-projection-recovery") || flagWasProvided(flags, "project-due-intents") || flagWasProvided(flags, "request-projection-flush") || flagWasProvided(flags, "resolve-projection-owner-effects") ||
			flagWasProvided(flags, "prepare-worker-input") || flagWasProvided(flags, "seal-worker") || flagWasProvided(flags, "build-catalog-proposal") || flagWasProvided(flags, "build-selection-export-proposal") ||
			flagWasProvided(flags, "prepare-evidence-delivery") || flagWasProvided(flags, "import-evidence-receipt") ||
			flagWasProvided(flags, "import-provider-snapshot-inputs") || publicationCommandCount != 0 || *wakePilotAssessments,
	); err != nil {
		return err
	}
	if *openReview {
		return openEvidenceReview(*reviewRoot, *reviewInput)
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
		stateBranch: *stateBranch, checkoutRoot: *stateCheckoutRoot, dataRoot: *dataRoot, inputOutput: *workerInputOut,
		artifactPath: *workerArtifact, validationID: *validationID,
		workflowID: *workerWorkflowID, producerCommit: *workerProducerCommit,
	}); handled {
		return err
	}
	switch {
	case *mintState:
		return mintStateNativeToken()
	case *mintReview:
		appID, key := appCredentials()
		token, err := github.MintReviewToken(context.Background(), appID, key)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, token)
		return nil
	case *importEvidence:
		return importEvidenceAcceptance(*stateBranch, *stateCheckoutRoot, *evidenceAcceptance, *evidenceAcceptedDataRoot, *assessmentTrackPolicy)
	case *importProviderInputs:
		return importProviderSnapshotAssessmentInputs(*stateBranch, *stateCheckoutRoot, *providerInputsImport, *providerInputsDataRoot)
	case *prepareSitePublication:
		publicationAt, timeErr := time.Parse(time.RFC3339, *publicationTime)
		if timeErr != nil {
			return fmt.Errorf("publication time must be RFC3339: %w", timeErr)
		}
		_, err = prepareIndexedSitePublication(*stateBranch, *stateCheckoutRoot, *dataRoot, *siteRoot, *publicationAcceptedCommit, *publicationProducerRun, *publicationModuleNames, publicationAt)
		return err
	case *wakePilotAssessments:
		publicationAt, timeErr := time.Parse(time.RFC3339, *publicationTime)
		if timeErr != nil {
			return fmt.Errorf("assessment wake time must be RFC3339: %w", timeErr)
		}
		return wakeAcceptedAssessments(*stateBranch, *stateCheckoutRoot, *dataRoot, *assessmentWakeAcceptedCommit, *assessmentWakeProducerRun, *assessmentWakeModuleNames, publicationAt)
	case *authorizeSitePublication:
		publicationAt, timeErr := time.Parse(time.RFC3339, *publicationTime)
		if timeErr != nil {
			return fmt.Errorf("publication time must be RFC3339: %w", timeErr)
		}
		return authorizeIndexedSitePublication(*stateBranch, *stateCheckoutRoot, *publicationManifest, *dataRoot, *siteRoot, *publicationActor, publicationAt)
	case *importSiteDeployment:
		publicationAt, timeErr := time.Parse(time.RFC3339, *publicationTime)
		if timeErr != nil {
			return fmt.Errorf("publication time must be RFC3339: %w", timeErr)
		}
		return importIndexedSiteDeployment(*stateBranch, *stateCheckoutRoot, *publicationManifest, *dataRoot, *siteRoot, *publicationPagesDeploymentID, publicationAt)
	case *buildCatalogProposal:
		return buildDiscoveryCatalogProposal(*stateBranch, *stateCheckoutRoot, *catalogProposalBase, *dataRoot, *catalogProposalOut)
	case *buildSelectionExport:
		return buildPendingSelectionProposal(*stateBranch, *stateCheckoutRoot, *selectionExportID, *selectionExportModuleID, *selectionExportBase, *dataRoot, *selectionExportOut)
	case *buildEvidenceProposal:
		return buildModuleEvidenceProposal(*workerLaunch, *evidenceArtifact, *evidenceProposalBase, *dataRoot, *evidenceProposalOut, *evidenceValidationID, *evidenceCreatedAt)
	case *prepareDelivery:
		return prepareEvidenceDelivery(*stateBranch, *stateCheckoutRoot, *attemptID, *workerLaunch, *evidenceArtifact, *evidenceProposalBase, *dataRoot, *evidenceProposalOut)
	case *openReview:
		return openEvidenceReview(*reviewRoot, *reviewInput)
	case *listDimensions:
		return listAssessmentDefinitions(os.Stdout, "")
	case *listChecks != "":
		return listAssessmentDefinitions(os.Stdout, *listChecks)
	case *assessFacts != "":
		return runOfflineAssessment(*assessFacts, *providerSnapshots, *classifications, *dimensionIDs, *assessmentOut)
	case *persist != "":
		return runDiscoveryPersistence(discoveryPersistenceCommand{
			paced: *pacedDiscovery, continuationID: *continuationID,
			pacingState:       *pacingState,
			stateCheckoutRoot: *stateCheckoutRoot,
			candidatePath:     *persist, branch: *stateBranch, contract: *extractorContract, capabilities: *extractorCapabilities,
			forcedIDs: *discoverModules, resultPath: *discoveryResult, scope: manualScope, newOnly: *newDiscoveryModules, cycle: *moduleCycle,
			identity: discoveryCycleIdentity{runID: *discoveryRunID},
		})
	case *requestProjectionRecovery:
		return requestManualProjectionRecovery(*stateBranch, *stateCheckoutRoot, *projectRunURL, *projectRunID, *manualScope)
	case *projectDue:
		return projectDueDiscoveryIntents(*stateBranch, *stateCheckoutRoot, *projectRunURL, *projectRunID, *projectOwnerRunID, *projectOwnerRunAttempt, *projectMaxModules)
	case *listProjectionRecoveryCandidates:
		return listExpiredProjectionOwners(*stateBranch, *stateCheckoutRoot)
	case *recoverProjectionOwner != "":
		return recoverExpiredProjectionOwner(*stateBranch, *stateCheckoutRoot, *recoverProjectionOwner, *actionsObservation)
	case *resolveProjectionOwnerEffects != "":
		return resolveProjectionOwnerEffectsForModules(*stateBranch, *stateCheckoutRoot, *resolveProjectionOwnerEffects)
	case *projectionOwnerSkip != "":
		return reportProjectionOwnerRecoverySkip(*projectionOwnerSkip, *projectionOwnerSkipReason)
	case *requestProjectionFlush != "":
		return requestModuleProjectionFlush(*stateBranch, *stateCheckoutRoot, *requestProjectionFlush, *projectRunURL, *projectRunID)
	case *exportPresentation:
		return exportPresentationProposal(*dataRoot, *presentationOut, *presentationBase, *presentationAsOf)
	case *buildSite:
		return site.BuildIndexed(*dataRoot, *siteRoot)
	case *operator != "":
		return runOperationalCommand(*operationsPolicy, *stateCheckoutRoot, *operationID, *operator, *operationModule, *operationReason)
	case *dispatch != "":
		return runDispatchOperation(*dispatch, *stateBranch, dispatchOperationOptions{
			dispatcherID: *dispatcherID, requestID: *requestID, moduleID: *dispatchModuleID,
			producerWorkflowID: *producerWorkflowID, producerCommit: *producerCommit,
			producerRunID: *producerRunID, producerRunAttempt: *producerRunAttempt,
			checkoutRoot: *stateCheckoutRoot, attemptID: *attemptID,
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
func validateStateCommandFlags(flags *flag.FlagSet, branch, checkoutRoot string, selectedStateCommand bool) error {
	stateOptions := map[string]bool{"state-branch": true, "state-checkout-root": true}
	stateOptionProvided := false
	flags.Visit(func(option *flag.Flag) {
		if stateOptions[option.Name] {
			stateOptionProvided = true
		}
	})
	if selectedStateCommand {
		if err := validateFactoryStateBranch(branch); err != nil {
			return err
		}
		if strings.TrimSpace(checkoutRoot) == "" {
			return fmt.Errorf("--state-checkout-root is required for state operations")
		}
		return nil
	}
	if stateOptionProvided || branch != "" || checkoutRoot != "" {
		return fmt.Errorf("--state-branch and --state-checkout-root require a state command")
	}
	return nil
}

func flagWasProvided(flags *flag.FlagSet, name string) bool {
	provided := false
	flags.Visit(func(option *flag.Flag) {
		provided = provided || option.Name == name
	})
	return provided
}

// validateDiscoveryControls rejects incompatible scope before operational work.
func validateDiscoveryControls(newOnly, cycle bool, persistPath, forcedIDs string, scope *manualModuleScope) error {
	if err := validateNewDiscoveryScope(newOnly, persistPath, forcedIDs, scope); err != nil {
		return err
	}
	if !cycle {
		return nil
	}
	if persistPath == "" || forcedIDs != "" || scope == nil {
		return fmt.Errorf("--discovery-module-cycle requires --persist-discovery with manual selection and no --discover-modules")
	}
	return nil
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
		return nil, fmt.Errorf("manual module options require --persist-discovery or --request-projection-recovery")
	}
	if strings.TrimSpace(scope.names) == "" {
		if _, err := manualModuleTypes(scope.types); err != nil {
			return nil, err
		}
	}
	return &scope, nil
}

// mintStateNativeToken mints only the state-writer installation token.
func mintStateNativeToken() error {
	appID, privateKey := appCredentials()
	token, err := github.MintStateToken(context.Background(), appID, privateKey)
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
		"mint-state-token": true, "discover-catalog": true,
		"persist-discovery": true, "dispatch-operation": true, "operator-command": true,
		"build-indexed-site": true, "export-presentation": true,
		"request-projection-recovery": true, "project-due-intents": true,
		"list-projection-recovery-candidates": true, "recover-projection-owner": true, "resolve-projection-owner-effects": true,
		"prepare-evidence-delivery": true,
		"open-evidence-review":      true,
		"mint-review-token":         true,
		"request-projection-flush":  true,
		"list-dimensions":           true, "list-checks": true, "assess-facts": true, "build-indexes": true,
		"process-work": true, "prepare-worker-input": true, "seal-worker": true, "apply-proposal": true,
		"build-catalog-proposal": true, "build-selection-export-proposal": true, "build-module-evidence-proposal": true,
		"import-evidence-receipt": true, "import-provider-snapshot-inputs": true, "collect-provider-catalog": true,
		"collect-provider-resource-intelligence": true,
		"wake-pilot-assessments":                 true,
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
