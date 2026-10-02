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
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/github"
	"github.com/softwareone-public/avm-intelligence/internal/iaclens"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/site"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
	"github.com/softwareone-public/avm-intelligence/internal/toolchain"
)

const (
	factoryOwner         = "softwareone-public"
	factoryRepository    = "avm-intelligence"
	factoryProjectNumber = 1
	factoryStateBranch   = "factory-state"
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
	mintStateToken := flags.Bool("mint-state-token", false, "Mint a repository-scoped Contents token for factory-state operations")
	analysisPath := flags.String("analysis", "", "Normalized analysis JSON to project")
	runURL := flags.String("run-url", "", "GitHub Actions run URL for the accepted analysis")
	executable := flags.String("iaclens", "", "Absolute path to the verified IaCLens executable")
	sourceDir := flags.String("source", "", "Absolute path to the prepared pilot Git worktree")
	workDir := flags.String("work", "work", "Attempt directory for the request and raw report")
	pagePath := flags.String("page", "", "Optional path for the evidence HTML page")
	discoverCatalog := flags.Bool("discover-catalog", false, "Fetch and validate the three official AVM Terraform catalogs")
	catalogOut := flags.String("catalog-out", "", "Optional path for the validated candidate catalog JSON")
	acceptCatalog := flags.String("accept-catalog", "", "Accept a reviewed candidate catalog JSON into durable discovery state")
	persistDiscovery := flags.String("persist-discovery", "", "Persist a candidate catalog and source outcomes atomically to an explicit state branch")
	stateBranch := flags.String("state-branch", "", "Must be factory-state for --persist-discovery; no default branch is assumed")
	extractorContract := flags.String("extractor-contract", "", "Reviewed extractor contract ID required by --persist-discovery")
	extractorCapabilities := flags.String("extractor-capabilities", "", "Comma-separated extractor capability IDs required by --persist-discovery")
	dispatchOperation := flags.String("dispatch-operation", "", "Durable dispatcher operation: claim, bind, heartbeat, or recover")
	dispatcherID := flags.String("dispatcher-id", "", "Stable workflow run identity for a claim")
	attemptID := flags.String("attempt-id", "", "Persisted factory attempt ID for bind, heartbeat, or recovery")
	actionsRunID := flags.String("actions-run-id", "", "Actual Actions run ID to bind or heartbeat")
	actionsRunAttempt := flags.Uint64("actions-run-attempt", 0, "Actual Actions run attempt number")
	leaseSeconds := flags.Int("lease-seconds", 0, "Positive lease duration for claim or heartbeat")
	fence := flags.Uint64("fence", 0, "Expected fencing token for heartbeat")
	actionsObservationPath := flags.String("actions-observation", "", "JSON file containing a trusted Actions lookup result for recovery")
	operatorCommand := flags.String("operator-command", "", "Authorized operational command: pause, resume, retry, refresh, global_disable, or global_enable")
	operationID := flags.String("operation-id", "", "Stable idempotency key for an operational command")
	operationModuleID := flags.String("operation-module-id", "", "Active accepted module ID for a module-scoped command")
	operationReason := flags.String("operation-reason", "", "Required reason for an operational command")
	operationsPolicy := flags.String("operations-policy", "config/operations.yaml", "Reviewed operator authorization policy")
	catalogState := flags.String("catalog-state", "data/discovery.json", "Path to accepted discovery control state")
	resolveSource := flags.String("resolve-source", "", "Resolve latest stable source for an accepted module ID")
	previousSource := flags.String("previous-source", "", "Previous source pin used to detect moved tags")
	sourceOut := flags.String("source-out", "", "Optional path for resolved source JSON")
	resolveFleet := flags.Bool("resolve-fleet", false, "Resolve latest stable sources for every active accepted module")
	previousSources := flags.String("previous-sources", "", "Directory containing prior source pins keyed by module ID")
	fleetSourcesOut := flags.String("fleet-sources-out", "work/resolved-sources.json", "Path for the complete fleet source-pin JSON array")
	moduleTypes := flags.String("module-types", "all", "Fleet module types to resolve: all or comma-separated res,ptn,utl")
	moduleIDs := flags.String("module-ids", "", "Optional comma-separated active module IDs to resolve")
	planFleet := flags.Bool("plan-fleet", false, "Plan durable work for active accepted modules without starting jobs")
	resolvedSources := flags.String("resolved-sources", "", "JSON array of immutable source pins for accepted modules")
	requestsDir := flags.String("requests-dir", "data/requests", "Directory for durable per-module requests")
	fleetPlanOut := flags.String("fleet-plan-out", "work/fleet-plan.json", "Path for the bounded fleet dispatch plan")
	reconcileFleet := flags.Bool("reconcile-fleet", false, "Reconcile durable fleet requests from confirmed run states")
	runStates := flags.String("run-states", "", "JSON array of confirmed run states for running requests")
	reconciliationOut := flags.String("reconciliation-out", "work/reconciliation-plan.json", "Path for the fleet reconciliation plan")
	publicationManifest := flags.Bool("publication-manifest", false, "Create an exact-commit publication manifest")
	acceptedCommit := flags.String("accepted-commit", "", "Exact accepted Git commit for publication")
	dataRoot := flags.String("data-root", "data", "Accepted data tree to bind to the publication")
	siteRoot := flags.String("site-root", "site", "Generated site tree to bind to the publication")
	producerRun := flags.String("producer-run", "", "Run identity that produced the publication")
	publicationManifestOut := flags.String("publication-manifest-out", "site/publication.json", "Publication manifest output path")
	buildSite := flags.Bool("build-site", false, "Build the accepted catalog site")
	assembleCandidate := flags.Bool("assemble-candidate", false, "Assemble accepted worker artifacts into a candidate data tree")
	fleetPlanInput := flags.String("fleet-plan-input", "", "Fleet plan JSON used to validate worker artifacts")
	workerArtifacts := flags.String("worker-artifacts", "", "Directory containing downloaded worker artifacts")
	candidateDataOut := flags.String("candidate-data-out", "data", "Candidate data tree output path")
	acceptCandidate := flags.Bool("accept-candidate", false, "Atomically accept candidate data and control state into a bundle")
	acceptedDiscovery := flags.String("accepted-discovery", "", "Accepted discovery state to include in the bundle")
	acceptedRequests := flags.String("accepted-requests", "", "Durable request directory to include in the bundle")
	acceptedDataBase := flags.String("accepted-data-base", "data", "Current accepted data tree to merge")
	acceptedDataOut := flags.String("accepted-data-out", "data", "Accepted bundle output path")
	projectFleet := flags.Bool("project-fleet", false, "Project planned fleet module work to Issues and Projects")
	projectFleetPlan := flags.String("project-fleet-plan", "", "Fleet plan JSON for fleet projection")
	projectFleetState := flags.String("project-fleet-state", "", "Accepted catalog state for fleet projection")
	projectFleetSources := flags.String("project-fleet-sources", "", "Resolved source pins for fleet projection")
	projectFleetRequests := flags.String("project-fleet-requests", "", "Durable request directory for fleet projection")
	projectFleetRunURL := flags.String("project-fleet-run-url", "", "Actions run URL for fleet projection")
	projectAccepted := flags.Bool("project-accepted", false, "Project accepted reports to Issues and Projects")
	projectAcceptedRunURL := flags.String("project-accepted-run-url", "", "Actions run URL for accepted projection")
	projectAcceptedStatus := flags.String("project-accepted-status", github.StatusPublishing, "Accepted projection status")
	projectAcceptedPublished := flags.String("project-accepted-published", "", "Published commit for a Done projection")
	publicationReceipt := flags.String("publication-receipt", "", "Verified deployment receipt required for a Done projection")
	deploymentRunID := flags.String("deployment-run-id", "", "Upstream Pages deployment run ID bound to the receipt")
	analyzeRequest := flags.Bool("analyze-request", false, "Claim and analyze one planned fleet request")
	moduleID := flags.String("module-id", "", "Stable module ID for a fleet request")
	fingerprint := flags.String("fingerprint", "", "Expected durable request fingerprint")
	runID := flags.String("run-id", "", "Owning Actions run ID")
	analysisOut := flags.String("analysis-out", "", "Optional normalized analysis output path")
	listDimensions := flags.Bool("list-dimensions", false, "List compiled offline assessment dimensions")
	listChecks := flags.String("list-checks", "", "List compiled checks for a dimension ID")
	assessFacts := flags.String("assess-facts", "", "Run selected offline dimensions for a validated facts-v3 record")
	dimensionIDs := flags.String("dimension-ids", "", "Comma-separated dimension IDs for --assess-facts; defaults to all")
	providerSnapshots := flags.String("provider-snapshots", "", "Optional strict JSON array of accepted provider snapshots")
	classifications := flags.String("classifications", "", "Optional strict JSON map of reviewed unit-path to module/deployment/unknown classification")
	assessmentOut := flags.String("assessment-out", "", "Optional path for offline dimension results; defaults to stdout")
	planLegacyMigration := flags.Bool("plan-legacy-migration", false, "Create a read-only migration plan for accepted legacy reports")
	migrationOut := flags.String("migration-out", "work/legacy-migration-plan.json", "Path for the dry-run legacy migration plan; must be outside --data-root")
	buildIndexes := flags.Bool("build-indexes", false, "Build a complete index candidate from accepted typed records")
	indexRecords := flags.String("index-records", "", "Strict JSON index.Records input for --build-indexes")
	indexesOut := flags.String("indexes-out", "work/index-candidate", "Fresh output directory for --build-indexes; must be outside --data-root")
	processWork := flags.String("process-work", "", "Strict JSON worker input for one claimed v2 acquisition or assessment request")
	workArtifactOut := flags.String("work-artifact-out", "", "Output path for the worker's module_evidence artifact")
	prepareWorkerInput := flags.Bool("prepare-worker-input", false, "Bind a v2 worker run and emit its pinned, read-only compute input")
	workerRequestID := flags.String("worker-request-id", "", "Expected durable request ID for --prepare-worker-input")
	workerInputOut := flags.String("worker-input-out", "", "Path for the trusted worker input artifact")
	sealWorker := flags.Bool("seal-worker", false, "Validate a worker artifact and seal completion in factory state")
	workerArtifact := flags.String("worker-artifact", "", "Worker module_evidence artifact for --seal-worker")
	validationID := flags.String("validation-id", "", "Trusted validator contract ID for --seal-worker")
	applyProposal := flags.Bool("apply-proposal", false, "Validate and apply one typed proposal to a fresh candidate data tree")
	proposalRoot := flags.String("proposal-root", "", "Directory containing proposal.json and canonical data/ payloads")
	proposalBase := flags.String("proposal-base", "", "Expected current accepted Git SHA for --apply-proposal")
	proposalDataOut := flags.String("proposal-data-out", "work/proposal-data", "Fresh candidate data output for --apply-proposal")
	proposalChangedPaths := flags.String("proposal-changed-paths", "", "NUL-delimited changed paths from the candidate Git diff")
	buildCatalogProposal := flags.Bool("build-catalog-proposal", false, "Build a reviewed catalog proposal from the accepted factory-state discovery outbox")
	catalogProposalOut := flags.String("catalog-proposal-out", "work/v2/catalog-proposal", "Fresh output directory for --build-catalog-proposal")
	catalogProposalBase := flags.String("catalog-proposal-base", "", "Exact current Git SHA for --build-catalog-proposal")
	buildEvidenceProposal := flags.Bool("build-module-evidence-proposal", false, "Build a reviewed module evidence proposal from a sealed worker artifact")
	workerLaunchInput := flags.String("worker-launch-input", "", "Trusted worker launch artifact for --build-module-evidence-proposal")
	moduleEvidenceArtifact := flags.String("module-evidence-artifact", "", "Worker evidence artifact for --build-module-evidence-proposal")
	moduleEvidenceProposalOut := flags.String("module-evidence-proposal-out", "work/v2/module-evidence-proposal", "Fresh output directory for --build-module-evidence-proposal")
	moduleEvidenceProposalBase := flags.String("module-evidence-proposal-base", "", "Exact current Git SHA for --build-module-evidence-proposal")
	moduleEvidenceValidationID := flags.String("module-evidence-validation-id", "", "Trusted validator contract ID for --build-module-evidence-proposal")
	moduleEvidenceProposalCreatedAt := flags.String("module-evidence-proposal-created-at", "", "Deterministic proposal dataset timestamp for --build-module-evidence-proposal replay")
	importEvidenceReceipt := flags.Bool("import-evidence-receipt", false, "Import one verified merged module_evidence receipt into factory-state")
	evidenceAcceptanceInput := flags.String("evidence-acceptance-input", "", "Strict EvidenceAcceptanceInput JSON for --import-evidence-receipt")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *importEvidenceReceipt {
		return importEvidenceAcceptance(*stateBranch, *evidenceAcceptanceInput)
	}
	if *evidenceAcceptanceInput != "" {
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
		prepare: *prepareWorkerInput, seal: *sealWorker, attemptID: *attemptID, requestID: *workerRequestID,
		dispatcherID: *dispatcherID, runID: *actionsRunID, runAttempt: *actionsRunAttempt, fence: *fence,
		stateBranch: *stateBranch, dataRoot: *dataRoot, inputOutput: *workerInputOut,
		artifactPath: *workerArtifact, validationID: *validationID,
	}); handled {
		return err
	}
	if *buildCatalogProposal {
		return buildDiscoveryCatalogProposal(*stateBranch, *catalogProposalBase, *dataRoot, *catalogProposalOut)
	}
	if *buildEvidenceProposal {
		return buildModuleEvidenceProposal(*workerLaunchInput, *moduleEvidenceArtifact, *moduleEvidenceProposalBase, *dataRoot, *moduleEvidenceProposalOut, *moduleEvidenceValidationID, *moduleEvidenceProposalCreatedAt)
	}
	if *planLegacyMigration {
		return createLegacyMigrationPlan(*dataRoot, *migrationOut)
	}
	if *listDimensions {
		return listAssessmentDefinitions(os.Stdout, "")
	}
	if *listChecks != "" {
		return listAssessmentDefinitions(os.Stdout, *listChecks)
	}
	if *assessFacts != "" {
		return runOfflineAssessment(*assessFacts, *providerSnapshots, *classifications, *dimensionIDs, *assessmentOut)
	}
	if *dimensionIDs != "" || *providerSnapshots != "" || *classifications != "" || *assessmentOut != "" {
		return fmt.Errorf("--dimension-ids, --provider-snapshots, --classifications, and --assessment-out require --assess-facts")
	}
	if *persistDiscovery != "" {
		return persistAVMCatalog(*persistDiscovery, *stateBranch, *extractorContract, *extractorCapabilities)
	}
	if *mintStateToken {
		appID, privateKey := appCredentials()
		token, err := github.MintStateToken(context.Background(), appID, privateKey)
		if err != nil {
			return err
		}
		fmt.Print(token)
		return nil
	}
	if *planFleet {
		return planFleetRequests(fleetPlanOptions{
			statePath: *catalogState, sourcesPath: *resolvedSources, toolchainPath: *toolchainPath,
			policyPath: *configPath, requestsDir: *requestsDir, outputPath: *fleetPlanOut,
			moduleTypes: *moduleTypes, moduleIDs: *moduleIDs,
		})
	}
	if *reconcileFleet {
		return reconcileFleetRequests(*runStates, *requestsDir, *reconciliationOut)
	}
	if *analyzeRequest {
		return analyzeFleetRequest(fleetWorkerOptions{
			moduleID: *moduleID, fingerprint: *fingerprint, runID: *runID, requestsDir: *requestsDir,
			executable: *executable, sourceDir: *sourceDir, configPath: *configPath, workDir: *workDir,
			outputPath: *analysisOut, toolchainPath: *toolchainPath,
		})
	}
	if *publicationManifest {
		return createPublicationManifest(*dataRoot, *siteRoot, *acceptedCommit, *producerRun, *publicationManifestOut)
	}
	if *buildSite {
		return buildAcceptedSite(*dataRoot, *siteRoot)
	}
	if *assembleCandidate {
		return assembleCandidateData(*fleetPlanInput, *workerArtifacts, *candidateDataOut)
	}
	if *acceptCandidate {
		return acceptCandidateData(*candidateDataOut, *acceptedDiscovery, *acceptedRequests, *acceptedDataBase, *acceptedDataOut)
	}
	if *projectFleet {
		return projectFleetRequestsToGitHub(*projectFleetPlan, *projectFleetState, *projectFleetSources, *projectFleetRequests, *projectFleetRunURL)
	}
	if *projectAccepted {
		return projectAcceptedDataToGitHub(*dataRoot, *projectAcceptedRunURL, *projectAcceptedStatus, *projectAcceptedPublished, *publicationReceipt, *deploymentRunID)
	}
	if handled, err := runCatalogCommands(catalogCommandOptions{
		discover: discoverCatalog, candidateOut: catalogOut, candidatePath: acceptCatalog, statePath: catalogState,
		moduleID: resolveSource, previousPath: previousSource, sourceOut: sourceOut,
		resolveFleet: resolveFleet, previousSources: previousSources, fleetSourcesOut: fleetSourcesOut, moduleTypes: moduleTypes, moduleIDs: moduleIDs,
		dispatchOperation: dispatchOperation, stateBranch: stateBranch, dispatcherID: dispatcherID,
		attemptID: attemptID, actionsRunID: actionsRunID, actionsRunAttempt: actionsRunAttempt,
		leaseSeconds: leaseSeconds, fence: fence, actionsObservationPath: actionsObservationPath,
		operatorCommand: operatorCommand, operationID: operationID, operationModuleID: operationModuleID,
		operationReason: operationReason, operationsPolicy: operationsPolicy,
	}); handled {
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
		appID, privateKey := appCredentials()
		mintToken := github.MintToken
		if *mintStateToken {
			mintToken = github.MintStateToken
		}
		token, err := mintToken(context.Background(), appID, privateKey)
		if err != nil {
			return err
		}
		fmt.Print(token)
		return nil
	}
	if *mintStateToken {
		appID, privateKey := appCredentials()
		token, err := github.MintStateToken(context.Background(), appID, privateKey)
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

func assembleCandidateData(planPath, artifactsPath, outputPath string) error {
	if planPath == "" || artifactsPath == "" {
		return fmt.Errorf("--fleet-plan-input and --worker-artifacts are required for --assemble-candidate")
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("read fleet plan: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var plan queue.BatchPlan
	if err = decoder.Decode(&plan); err != nil {
		return fmt.Errorf("parse fleet plan: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("fleet plan must contain one JSON document")
	}
	candidate, err := publish.Assemble(plan, artifactsPath, outputPath, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Printf("assembled candidate generation %d with %d modules\n", candidate.CatalogGeneration, len(candidate.Modules))
	return nil
}

func acceptCandidateData(candidatePath, discoveryPath, requestsPath, basePath, outputPath string) error {
	if candidatePath == "" || discoveryPath == "" || requestsPath == "" || basePath == "" {
		return fmt.Errorf("--candidate-data-out, --accepted-discovery, --accepted-requests, and --accepted-data-base are required for --accept-candidate")
	}
	if err := publish.ApplyModuleDelta(candidatePath, discoveryPath, requestsPath, basePath, outputPath); err != nil {
		return err
	}
	fmt.Printf("accepted candidate bundle at %s\n", outputPath)
	return nil
}

func projectFleetRequestsToGitHub(planPath, statePath, sourcesPath, requestsDir, runURL string) error {
	if planPath == "" || statePath == "" || sourcesPath == "" || requestsDir == "" || runURL == "" {
		return fmt.Errorf("fleet projection inputs are required")
	}
	planData, err := os.ReadFile(planPath)
	if err != nil {
		return err
	}
	var plan queue.BatchPlan
	if err = decodeOne(planData, &plan); err != nil {
		return fmt.Errorf("parse fleet plan: %w", err)
	}
	state, err := catalog.LoadAccepted(statePath)
	if err != nil || state == nil {
		if err == nil {
			err = fmt.Errorf("accepted catalog state is missing")
		}
		return err
	}
	sourceData, err := os.ReadFile(sourcesPath)
	if err != nil {
		return err
	}
	var pins []source.ResolvedSource
	if err = decodeOne(sourceData, &pins); err != nil {
		return fmt.Errorf("parse resolved sources: %w", err)
	}
	pinByID := make(map[string]source.ResolvedSource, len(pins))
	for _, pin := range pins {
		pinByID[pin.ModuleID] = pin
	}
	moduleByID := make(map[string]catalog.Module, len(state.Modules))
	for _, record := range state.Modules {
		moduleByID[record.Module.ID] = record.Module
	}
	token, err := github.MintToken(context.Background(), os.Getenv("APP_ID"), os.Getenv("APP_PRIVATE_KEY"))
	if err != nil {
		return err
	}
	client := github.Client{Token: token}
	projectionContext := fleetProjectionContext{modules: moduleByID, pins: pinByID, requestsDir: requestsDir, runURL: runURL, client: client}
	for _, item := range plan.Items {
		if err = projectFleetItem(projectionContext, item); err != nil {
			return err
		}
	}
	return nil
}

type fleetProjectionContext struct {
	modules     map[string]catalog.Module
	pins        map[string]source.ResolvedSource
	requestsDir string
	runURL      string
	client      github.Client
}

func projectFleetItem(projectionContext fleetProjectionContext, item queue.PlanItem) error {
	if item.Action == queue.PlanBlocked || item.Action == queue.PlanNotSelected || item.Action == queue.PlanRetired {
		return nil
	}
	module, exists := projectionContext.modules[item.ModuleID]
	if !exists {
		return nil
	}
	request, err := queue.Load(projectionContext.requestsDir, module.ID)
	if err != nil {
		return fmt.Errorf("load request for %s: %w", module.ID, err)
	}
	var pin *source.ResolvedSource
	if resolved, ok := projectionContext.pins[module.ID]; ok {
		pin = &resolved
	}
	projection, err := github.FromFleet(github.FleetProjectionInput{
		Module: module, Domain: module.ParentModule, Source: pin, Request: request,
		Status: projectionStatus(item.Action, request.Status), RunURL: projectionContext.runURL,
	})
	if err != nil {
		return err
	}
	if _, err = projectionContext.client.UpsertFleet(context.Background(), factoryOwner, factoryRepository, factoryProjectNumber, projection); err != nil {
		return fmt.Errorf("project %s: %w", module.ID, err)
	}
	return nil
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

func projectionStatus(action, requestStatus string) string {
	switch {
	case action == queue.PlanBlocked:
		return github.StatusBlocked
	case requestStatus == queue.StatusRunning:
		return github.StatusAnalyzing
	case requestStatus == queue.StatusFailed:
		return github.StatusFailed
	case requestStatus == queue.StatusAccepted:
		return github.StatusPublishing
	default:
		return github.StatusQueued
	}
}

func projectAcceptedDataToGitHub(dataRoot, runURL, status, published, receiptPath, deploymentRunID string) error {
	if dataRoot == "" || runURL == "" {
		return fmt.Errorf("accepted projection inputs are required")
	}
	if status == github.StatusDone {
		verifiedCommit, err := verifiedPublishedCommit(dataRoot, receiptPath, deploymentRunID, published)
		if err != nil {
			return err
		}
		published = verifiedCommit
	}
	candidate, err := publish.LoadCandidate(dataRoot)
	if err != nil {
		return err
	}
	state, err := catalog.LoadAccepted(filepath.Join(dataRoot, "discovery.json"))
	if err != nil || state == nil {
		if err == nil {
			err = fmt.Errorf("accepted discovery state is missing")
		}
		return err
	}
	moduleByID := make(map[string]catalog.Module, len(state.Modules))
	for _, record := range state.Modules {
		moduleByID[record.Module.ID] = record.Module
	}
	token, err := github.MintToken(context.Background(), os.Getenv("APP_ID"), os.Getenv("APP_PRIVATE_KEY"))
	if err != nil {
		return err
	}
	client := github.Client{Token: token}
	for index, module := range candidate.Modules {
		fmt.Printf("projecting accepted module %d/%d: %s\n", index+1, len(candidate.Modules), module.ModuleID)
		if err = projectAcceptedModule(acceptedProjectionContext{
			dataRoot: dataRoot, runURL: runURL, status: status, published: published,
			builtAt: candidate.BuiltAt, modules: moduleByID, client: client,
		}, module); err != nil {
			return err
		}
		fmt.Printf("projected accepted module: %s\n", module.ModuleID)
	}
	return nil
}

func verifiedPublishedCommit(dataRoot, receiptPath, deploymentRunID, requestedCommit string) (string, error) {
	receipt, err := publish.VerifyDeploymentReceipt(receiptPath, dataRoot, deploymentRunID)
	if err != nil {
		return "", fmt.Errorf("verify deployment receipt before Done projection: %w", err)
	}
	if requestedCommit != "" && requestedCommit != receipt.AcceptedCommit {
		return "", fmt.Errorf("requested published commit does not match verified deployment receipt")
	}
	return receipt.AcceptedCommit, nil
}

type acceptedProjectionContext struct {
	dataRoot, runURL, status, published string
	builtAt                             time.Time
	modules                             map[string]catalog.Module
	client                              github.Client
}

func projectAcceptedModule(projectionContext acceptedProjectionContext, candidate publish.CandidateModule) error {
	catalogModule, exists := projectionContext.modules[candidate.ModuleID]
	if !exists {
		return fmt.Errorf("accepted module %s is missing from discovery state", candidate.ModuleID)
	}
	request, err := queue.Load(filepath.Join(projectionContext.dataRoot, "requests"), candidate.ModuleID)
	if err != nil {
		return err
	}
	report, err := readAnalysis(filepath.Join(projectionContext.dataRoot, filepath.FromSlash(candidate.ReportPath)))
	if err != nil {
		return err
	}
	projection, err := github.FromFleet(github.FleetProjectionInput{
		Module: catalogModule, Domain: catalogModule.ParentModule, Request: request,
		Report: &report, Status: projectionContext.status, Published: projectionContext.published,
		RunURL: projectionContext.runURL, LastAnalyzed: projectionContext.builtAt,
	})
	if err != nil {
		return err
	}
	if _, err = projectionContext.client.UpsertFleet(context.Background(), factoryOwner, factoryRepository, factoryProjectNumber, projection); err != nil {
		return fmt.Errorf("project accepted %s: %w", candidate.ModuleID, err)
	}
	return nil
}

func createPublicationManifest(dataRoot, siteRoot, acceptedCommit, producerRun, outputPath string) error {
	manifest, err := publish.Create(dataRoot, siteRoot, acceptedCommit, producerRun, time.Now().UTC())
	if err != nil {
		return err
	}
	if err = publish.Write(outputPath, manifest); err != nil {
		return err
	}
	fmt.Printf("publication manifest %s data=%s site=%s\n", manifest.AcceptedCommit, manifest.DataDigest, manifest.SiteDigest)
	return nil
}

func buildAcceptedSite(dataRoot, siteRoot string) error {
	if err := site.Build(dataRoot, siteRoot); err != nil {
		return err
	}
	fmt.Printf("built accepted site at %s\n", siteRoot)
	return nil
}

type fleetWorkerOptions struct {
	moduleID, fingerprint, runID, requestsDir  string
	executable, sourceDir, configPath, workDir string
	outputPath, toolchainPath                  string
}

func analyzeFleetRequest(options fleetWorkerOptions) (err error) {
	if options.moduleID == "" || options.fingerprint == "" || options.runID == "" {
		return fmt.Errorf("--module-id, --fingerprint, and --run-id are required for --analyze-request")
	}
	if !filepath.IsAbs(options.executable) || !filepath.IsAbs(options.sourceDir) {
		return fmt.Errorf("--iaclens and --source must be absolute")
	}
	doc, err := toolchain.Load(options.toolchainPath)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	request, err := queue.Start(options.requestsDir, options.moduleID, options.fingerprint, options.runID, now)
	if err != nil {
		return err
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		_, finishErr := queue.Finish(options.requestsDir, options.moduleID, options.fingerprint, options.runID, false, time.Now().UTC())
		if err == nil {
			err = finishErr
		}
	}()
	absConfig, err := filepath.Abs(options.configPath)
	if err != nil {
		return err
	}
	absWork, err := filepath.Abs(options.workDir)
	if err != nil {
		return err
	}
	result, err := iaclens.Run(context.Background(), iaclens.Request{
		Executable: options.executable, SourceDir: options.sourceDir, ConfigPath: absConfig, WorkDir: absWork, Kind: "auto",
	})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("iaclens exited %d; request remains unaccepted", result.ExitCode)
	}
	report, err := iaclens.Normalize(result.Report, iaclens.Provenance{
		ModuleID: request.ModuleID, SourceSHA: request.SourceSHA, CLIVersion: doc.CLI.Version,
		CLICommit: doc.CLI.Commit, AdapterVersion: iaclens.AdapterVersion,
	})
	if err != nil {
		return err
	}
	if options.outputPath == "" {
		options.outputPath = filepath.Join(absWork, "analysis.json")
	}
	if err = writeJSON(options.outputPath, report); err != nil {
		return err
	}
	if _, err = queue.Finish(options.requestsDir, options.moduleID, options.fingerprint, options.runID, true, time.Now().UTC()); err != nil {
		return err
	}
	finished = true
	fmt.Printf("accepted %s with %d units\n", request.ID, len(report.Units))
	return nil
}

func reconcileFleetRequests(statesPath, requestsDir, outputPath string) error {
	if statesPath == "" {
		return fmt.Errorf("--run-states is required for --reconcile-fleet")
	}
	data, err := os.ReadFile(statesPath)
	if err != nil {
		return fmt.Errorf("read run states: %w", err)
	}
	observations, err := queue.DecodeRunObservations(data)
	if err != nil {
		return err
	}
	plan, err := queue.ReconcileFleet(requestsDir, observations, time.Now().UTC())
	if err != nil {
		return err
	}
	if err = writeJSON(outputPath, plan); err != nil {
		return err
	}
	fmt.Printf("reconciled %d requests: batches=%d\n", len(plan.Items), len(plan.Batches))
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
	issue, err := (github.Client{Token: os.Getenv("FACTORY_GITHUB_TOKEN")}).Upsert(context.Background(), factoryOwner, factoryRepository, factoryProjectNumber, projection)
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

func persistAVMCatalog(candidatePath, branch, extractorContract, capabilityList string) error {
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
	backend := state.GitHubBackend{
		Owner: factoryOwner, Repository: factoryRepository, Branch: branch, Token: stateToken,
	}
	result, err := discovery.Persist(context.Background(), source.Resolver{Token: os.Getenv("GITHUB_TOKEN")}, backend, candidate, discovery.Options{
		AcceptedAt: time.Now().UTC(), ExtractorContract: extractorContract, Capabilities: capabilities,
	})
	if err != nil {
		return err
	}
	fmt.Printf("persisted discovery generation %d at %s observation=%s proposal=%s\n",
		result.Accepted.Generation, result.Head, result.ObservationID, result.ProposalID)
	return nil
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
	if options.dispatcherID == "" || options.leaseSeconds <= 0 {
		return nil, fmt.Errorf("claim requires --dispatcher-id and positive --lease-seconds")
	}
	claim, err := queue.ClaimNext(ctx, backend, options.dispatcherID, now, time.Duration(options.leaseSeconds)*time.Second)
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

type catalogCommandOptions struct {
	discover               *bool
	candidateOut           *string
	candidatePath          *string
	statePath              *string
	moduleID               *string
	previousPath           *string
	sourceOut              *string
	resolveFleet           *bool
	previousSources        *string
	fleetSourcesOut        *string
	moduleTypes            *string
	moduleIDs              *string
	dispatchOperation      *string
	stateBranch            *string
	dispatcherID           *string
	attemptID              *string
	actionsRunID           *string
	actionsRunAttempt      *uint64
	leaseSeconds           *int
	fence                  *uint64
	actionsObservationPath *string
	operatorCommand        *string
	operationID            *string
	operationModuleID      *string
	operationReason        *string
	operationsPolicy       *string
}

type dispatchOperationOptions struct {
	dispatcherID           string
	attemptID              string
	actionsRunID           string
	actionsRunAttempt      uint64
	leaseSeconds           int
	fence                  uint64
	actionsObservationPath string
}

func runCatalogCommands(options catalogCommandOptions) (bool, error) {
	switch {
	case *options.operatorCommand != "":
		return true, runOperationalCommand(*options.operationsPolicy, *options.operationID, *options.operatorCommand, *options.operationModuleID, *options.operationReason)
	case *options.dispatchOperation != "":
		operation := dispatchOperationOptions{
			dispatcherID: *options.dispatcherID, attemptID: *options.attemptID,
			actionsRunID: *options.actionsRunID, actionsRunAttempt: *options.actionsRunAttempt,
			leaseSeconds: *options.leaseSeconds, fence: *options.fence,
			actionsObservationPath: *options.actionsObservationPath,
		}
		return true, runDispatchOperation(*options.dispatchOperation, *options.stateBranch, operation)
	case *options.discover:
		return true, discoverAVMCatalog(*options.candidateOut)
	case *options.candidatePath != "":
		return true, acceptAVMCatalog(*options.candidatePath, *options.statePath)
	case *options.moduleID != "":
		return true, resolveAVMSource(*options.moduleID, *options.statePath, *options.previousPath, *options.sourceOut)
	case *options.resolveFleet:
		return true, resolveAVMFleet(*options.statePath, *options.previousSources, *options.fleetSourcesOut, *options.moduleTypes, *options.moduleIDs)
	default:
		return false, nil
	}
}

func resolveAVMFleet(statePath, previousDir, outputPath, moduleTypes, moduleIDs string) error {
	state, err := catalog.LoadAccepted(statePath)
	if err != nil {
		return err
	}
	if state == nil {
		return fmt.Errorf("accepted catalog state does not exist at %s", statePath)
	}
	kinds, err := parseModuleTypes(moduleTypes)
	if err != nil {
		return err
	}
	ids, err := parseModuleIDs(moduleIDs)
	if err != nil {
		return err
	}
	resolved, err := (source.Resolver{Token: os.Getenv("GITHUB_TOKEN")}).ResolveAcceptedSelection(context.Background(), *state, previousDir, kinds, ids)
	if err != nil {
		return err
	}
	if err = writeJSON(outputPath, resolved); err != nil {
		return err
	}
	fmt.Printf("resolved %d active catalog modules at generation %d\n", len(resolved), state.Generation)
	return nil
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

func parseModuleTypes(value string) (map[catalog.Kind]bool, error) {
	if strings.TrimSpace(value) == "" || strings.EqualFold(strings.TrimSpace(value), "all") {
		return nil, nil
	}
	allowed := map[catalog.Kind]bool{}
	for _, raw := range strings.Split(value, ",") {
		kind := catalog.Kind(strings.TrimSpace(strings.ToLower(raw)))
		if kind != catalog.Resource && kind != catalog.Pattern && kind != catalog.Utility {
			return nil, fmt.Errorf("invalid module type %q; use all, res, ptn, or utl", raw)
		}
		allowed[kind] = true
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("at least one module type is required")
	}
	return allowed, nil
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

type fleetPlanOptions struct {
	statePath     string
	sourcesPath   string
	toolchainPath string
	policyPath    string
	requestsDir   string
	outputPath    string
	moduleTypes   string
	moduleIDs     string
}

func planFleetRequests(options fleetPlanOptions) error {
	if options.sourcesPath == "" {
		return fmt.Errorf("--resolved-sources is required for --plan-fleet")
	}
	state, err := catalog.LoadAccepted(options.statePath)
	if err != nil {
		return err
	}
	if state == nil {
		return fmt.Errorf("accepted catalog state does not exist at %s", options.statePath)
	}
	kinds, err := parseModuleTypes(options.moduleTypes)
	if err != nil {
		return err
	}
	selectedIDs, err := parseModuleIDs(options.moduleIDs)
	if err != nil {
		return err
	}
	pinsData, err := os.ReadFile(options.sourcesPath)
	if err != nil {
		return fmt.Errorf("read resolved sources: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(pinsData)))
	decoder.DisallowUnknownFields()
	var pins []source.ResolvedSource
	if err = decoder.Decode(&pins); err != nil {
		return fmt.Errorf("parse resolved sources: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("resolved sources must contain one JSON document")
	}
	doc, err := toolchain.Load(options.toolchainPath)
	if err != nil {
		return err
	}
	asset, err := doc.Asset("linux", "amd64")
	if err != nil {
		return err
	}
	policyDigest, err := fileDigest(options.policyPath)
	if err != nil {
		return err
	}
	plan, err := queue.PlanAcceptedCatalog(*state, pins, queue.ModuleSelection{ModuleTypes: kinds, ModuleIDs: selectedIDs}, queue.AnalysisContract{
		CLIVersion: doc.CLI.Version, CLICommit: doc.CLI.Commit, BinaryDigest: asset.SHA256,
		PolicyDigest: policyDigest, ReportSchemaVersion: doc.CLI.SchemaVersion, AdapterVersion: doc.Adapter.Version,
	}, options.requestsDir, time.Now().UTC())
	if err != nil {
		return err
	}
	if err = writeJSON(options.outputPath, plan); err != nil {
		return err
	}
	fmt.Printf("planned catalog generation %d: items=%d batches=%d\n", plan.CatalogGeneration, len(plan.Items), len(plan.Batches))
	return nil
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
