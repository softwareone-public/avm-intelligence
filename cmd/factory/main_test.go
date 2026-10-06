package main

import (
	"bytes"
	"context"

	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
	"github.com/softwareone-public/avm-intelligence/internal/worker"
	"gopkg.in/yaml.v3"
)

func TestProjectionTokenDoesNotLoadProducerConfiguration(t *testing.T) {
	for _, name := range []string{"AVM_WRITER_APP_ID", "AVM_WRITER_PRIVATE_KEY", "APP_ID", "APP_PRIVATE_KEY"} {
		t.Setenv(name, "")
	}
	err := run([]string{"--mint-projection-token"})
	if err == nil || err.Error() != "GitHub App credentials are required" {
		t.Fatalf("token minting loaded unrelated producer configuration: %v", err)
	}
}

func TestNativeCommandsRejectRetiredFlags(t *testing.T) {
	for _, name := range []string{"show-toolchain", "show-pilot", "register-pilot", "analyze-pilot", "project-fleet", "project-accepted", "build-site", "plan-legacy-migration"} {
		t.Run(name, func(t *testing.T) {
			err := run([]string{"--" + name})
			if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Fatalf("retired command %s was accepted: %v", name, err)
			}
		})
	}
}

func TestNativeCommandsRejectMixedModesBeforeSideEffects(t *testing.T) {
	for _, arguments := range [][]string{
		{"--mint-projection-token", "--mint-state-token"},
		{"--list-dimensions", "--discover-catalog"},
		{"--build-indexes", "--prepare-worker-input"},
	} {
		if err := run(arguments); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("mixed native commands reached a side-effect path: args=%v err=%v", arguments, err)
		}
	}
}

func TestIndexesCLIRebuildsNativeTreeDeterministically(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	moduleID := "avm-res-native-test"
	metadataPath, err := identity.ModuleMetadataPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	metadataFile := filepath.Join(root, filepath.FromSlash(metadataPath))
	if err = os.MkdirAll(filepath.Dir(metadataFile), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"schema_version":2,"record_type":"module_metadata","module_id":"avm-res-native-test"}`)
	if err = os.WriteFile(metadataFile, metadata, 0o644); err != nil {
		t.Fatal(err)
	}
	catalogBytes, err := json.Marshal(index.CatalogIndex{
		SchemaVersion: 1, RecordType: "catalog_index", CatalogGeneration: 1,
		Modules: []index.CatalogModuleEntry{{ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-native-test", Lifecycle: "active"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dataRoot, "catalog.json"), catalogBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	datasetBytes, err := json.Marshal(index.DatasetManifest{
		SchemaVersion: 2, RecordType: "dataset_manifest", CatalogGeneration: 1,
		Generator: index.Producer{ID: "factory", Version: "2.0.0", ContractVersion: 1},
		CreatedAt: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dataRoot, "dataset.json"), datasetBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	var outputs [][]byte
	for _, name := range []string{"first", "second"} {
		output := filepath.Join(root, name)
		if err = run([]string{"--build-indexes", "--data-root", dataRoot, "--indexes-out", output}); err != nil {
			t.Fatal(err)
		}
		generated, readErr := os.ReadFile(filepath.Join(output, "data", "dataset.json"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		outputs = append(outputs, generated)
	}
	if !bytes.Equal(outputs[0], outputs[1]) {
		t.Fatal("native tree rebuild changed deterministic dataset bytes")
	}
	unchanged, err := os.ReadFile(metadataFile)
	if err != nil || !bytes.Equal(unchanged, metadata) {
		t.Fatalf("native source record was modified: %v", err)
	}
}

func TestIndexesCLIWritesFreshCandidateOutsideAcceptedData(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "accepted-data")
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	moduleID := "avm-res-network-vnet"
	metadataPath, err := identity.ModuleMetadataPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	records := index.Records{
		CatalogGeneration: 1,
		CreatedAt:         time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		Generator:         index.Producer{ID: "factory", Version: "2.0.0", ContractVersion: 1},
		RecordDigests:     map[string]string{metadataPath: strings.Repeat("a", 64)},
		Modules: []index.ModuleRecord{{
			ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-network-vnet", Lifecycle: "active",
		}},
	}
	inputBytes, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(root, "accepted-records.json")
	if err = os.WriteFile(inputPath, inputBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(root, "work", "indexes")
	if err = run([]string{
		"--build-indexes", "--index-records", inputPath,
		"--data-root", dataRoot, "--indexes-out", outputRoot,
	}); err != nil {
		t.Fatal(err)
	}
	datasetPath, err := identity.CanonicalDataPath("dataset")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(outputRoot, filepath.FromSlash(datasetPath))); err != nil {
		t.Fatalf("generated candidate is missing dataset manifest: %v", err)
	}
	if entries, err := os.ReadDir(dataRoot); err != nil || len(entries) != 0 {
		t.Fatalf("index CLI changed accepted data root: entries=%v err=%v", entries, err)
	}
	if err = run([]string{
		"--build-indexes", "--index-records", inputPath,
		"--data-root", dataRoot, "--indexes-out", outputRoot,
	}); err == nil {
		t.Fatal("index CLI replaced an existing candidate output")
	}
}

func TestProcessWorkCLIProducesFactsArtifactWithoutStateCredentials(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := `terraform {
  required_providers {
    azurerm = { source = "hashicorp/azurerm" }
  }
}
resource "azurerm_resource_group" "main" {}`
	if err := os.WriteFile(filepath.Join(sourceRoot, "main.tf"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	request, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: "avm-res-worker-cli", SourceSHA: strings.Repeat("a", 40),
		DesiredGeneration: 1, ExtractorContract: "factory-hcl-v1",
		Capabilities: []string{terraform.CapabilityProviderRequirements},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	owner := "worker-cli-run"
	claimed, err := queue.ClaimModuleRequest([]queue.Request{request}, request.ID, owner, now, now.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	input := worker.Input{
		Request: claimed[0], Owner: owner, Fence: 1, SourceRoot: sourceRoot, ObservedAt: now,
		Producer: worker.Producer{ID: "avm-intelligence", Version: "2.0.0", ContractVersion: 1},
	}
	inputBytes, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(root, "work-input.json")
	if err = os.WriteFile(inputPath, inputBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(root, "module-evidence.json")
	if err = run([]string{"--process-work", inputPath, "--work-artifact-out", artifactPath}); err != nil {
		t.Fatal(err)
	}
	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var artifact worker.Artifact
	if err = decodeOne(artifactBytes, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.RecordType != "module_evidence" || artifact.Phase != queue.PhaseAcquireSource || artifact.Facts == nil || artifact.ArtifactDigest == "" {
		t.Fatalf("worker CLI did not emit facts-only module evidence: %#v", artifact)
	}
}

type workerWorkflowStep struct {
	Name string            `yaml:"name"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

type workerWorkflowJob struct {
	Environment string               `yaml:"environment"`
	Permissions map[string]string    `yaml:"permissions"`
	Env         map[string]string    `yaml:"env"`
	Steps       []workerWorkflowStep `yaml:"steps"`
}

func TestV2WorkerWorkflowKeepsComputeReadOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "v2_worker.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]workerWorkflowJob `yaml:"jobs"`
	}
	if err = yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse v2 worker workflow: %v", err)
	}
	compute, exists := workflow.Jobs["compute"]
	if !exists {
		t.Fatal("compute job is missing")
	}
	assertComputeWorkerReadOnly(t, compute)
	assertTrustedWorkerJobsUsePublisherEnvironment(t, workflow.Jobs)
}

func TestApplyProposalCLIRequiresPinnedBaseAndMatchingMode(t *testing.T) {
	for _, args := range [][]string{
		{"--apply-proposal"},
		{"--proposal-root", "work/proposal"},
	} {
		if err := run(args); err == nil {
			t.Errorf("run(%q) accepted incomplete proposal options", args)
		}
	}
}

func TestV2ProposalValidationWorkflowUsesReadOnlyBaseCode(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "v2_proposal_validation.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse proposal validation workflow: %v", err)
	}
	steps := proposalValidationWorkflowSteps(t, document)
	assertProposalValidationWorkflowSteps(t, steps)
}

func proposalValidationWorkflowSteps(t *testing.T, document map[string]any) []any {
	t.Helper()
	on, ok := document["on"].(map[string]any)
	if !ok || on["pull_request_target"] == nil || on["merge_group"] == nil {
		t.Fatal("proposal validation workflow must cover pull_request_target and merge_group")
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("proposal validation workflow has no jobs")
	}
	job, ok := jobs["validate"].(map[string]any)
	if !ok || job["environment"] != nil {
		t.Fatal("proposal validation job is missing or has a privileged environment")
	}
	permissions, ok := job["permissions"].(map[string]any)
	if !ok || len(permissions) != 1 || permissions["contents"] != "read" {
		t.Fatalf("proposal validation permissions are not read-only: %#v", job["permissions"])
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("proposal validation job has no steps")
	}
	return steps
}

func assertProposalValidationWorkflowSteps(t *testing.T, steps []any) {
	t.Helper()
	evidence := collectProposalWorkflowEvidence(steps)
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if ok {
			run, _ := step["run"].(string)
			assertProposalRunDoesNotCheckoutCandidate(t, run)
			assertProposalStepHasNoCredentials(t, step)
		}
	}
	if !evidence.complete() {
		t.Fatalf("proposal workflow trust boundary is incomplete: %#v", evidence)
	}
}

type proposalWorkflowEvidence struct {
	checkedOutBase, pinsEventBase, validatesProposalBase, usesMergeCandidate                   bool
	persistsCandidateSHA, candidateSHAFromFetch, validatesCandidateTree, validatedChangedPaths bool
	detectsRenames, rejectsNonRegularBlobs                                                     bool
	persistsFetchCredentials, skipsCodeOnlyChanges, exportsProposalApplicability               bool
	gatesProposalApplier                                                                       bool
}

func collectProposalWorkflowEvidence(steps []any) proposalWorkflowEvidence {
	var evidence proposalWorkflowEvidence
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			continue
		}
		stepEvidence := inspectProposalWorkflowStep(step)
		evidence.checkedOutBase = evidence.checkedOutBase || stepEvidence.checkedOutBase
		evidence.pinsEventBase = evidence.pinsEventBase || stepEvidence.pinsEventBase
		evidence.validatesProposalBase = evidence.validatesProposalBase || stepEvidence.validatesProposalBase
		evidence.usesMergeCandidate = evidence.usesMergeCandidate || stepEvidence.usesMergeCandidate
		evidence.persistsCandidateSHA = evidence.persistsCandidateSHA || stepEvidence.persistsCandidateSHA
		evidence.candidateSHAFromFetch = evidence.candidateSHAFromFetch || stepEvidence.candidateSHAFromFetch
		evidence.validatesCandidateTree = evidence.validatesCandidateTree || stepEvidence.validatesCandidateTree
		evidence.validatedChangedPaths = evidence.validatedChangedPaths || stepEvidence.validatedChangedPaths
		evidence.detectsRenames = evidence.detectsRenames || stepEvidence.detectsRenames
		evidence.rejectsNonRegularBlobs = evidence.rejectsNonRegularBlobs || stepEvidence.rejectsNonRegularBlobs
		evidence.persistsFetchCredentials = evidence.persistsFetchCredentials || stepEvidence.persistsFetchCredentials
		evidence.skipsCodeOnlyChanges = evidence.skipsCodeOnlyChanges || stepEvidence.skipsCodeOnlyChanges
		evidence.exportsProposalApplicability = evidence.exportsProposalApplicability || stepEvidence.exportsProposalApplicability
		evidence.gatesProposalApplier = evidence.gatesProposalApplier || stepEvidence.gatesProposalApplier
	}
	return evidence
}

func inspectProposalWorkflowStep(step map[string]any) proposalWorkflowEvidence {
	run, _ := step["run"].(string)
	stepID, _ := step["id"].(string)
	condition, _ := step["if"].(string)
	return proposalWorkflowEvidence{
		checkedOutBase:               checksOutTrustedMain(step),
		pinsEventBase:                strings.Contains(run, "jq -er '.merge_group.base_sha'") && strings.Contains(run, `git reset --hard "$BASE_SHA"`),
		validatesProposalBase:        strings.Contains(run, `git merge-base --is-ancestor "$proposal_base" "$BASE_SHA"`),
		usesMergeCandidate:           usesPullRequestMergeCandidate(step) || strings.Contains(run, `refs/pull/${PULL_REQUEST_NUMBER}/merge`),
		persistsCandidateSHA:         strings.Contains(run, `echo "CANDIDATE_SHA=$CANDIDATE_SHA" >> "$GITHUB_ENV"`),
		candidateSHAFromFetch:        strings.Contains(run, `CANDIDATE_SHA="$(git rev-parse 'FETCH_HEAD^{commit}')"`),
		validatesCandidateTree:       strings.Contains(run, "find \"$accepted_root\" -type f -print0") && strings.Contains(run, "cmp -s \"$accepted_file\" \"$comparison_file\""),
		validatedChangedPaths:        strings.Contains(run, "--proposal-changed-paths"),
		detectsRenames:               strings.Contains(run, "git diff --no-renames --name-only -z"),
		rejectsNonRegularBlobs:       strings.Contains(run, "git ls-tree -z") && strings.Contains(run, `"100644"`),
		persistsFetchCredentials:     trustedBaseCheckoutPersistsCredentials(step),
		skipsCodeOnlyChanges:         strings.Contains(run, "No typed proposal paths changed; validation is not applicable."),
		exportsProposalApplicability: stepID == "fetch-candidate" && strings.Contains(run, `echo "has_proposal_change=$has_proposal_change" >> "$GITHUB_OUTPUT"`),
		gatesProposalApplier:         strings.Contains(run, "--apply-proposal") && strings.Contains(condition, "steps.fetch-candidate.outputs.has_proposal_change == 'true'"),
	}
}

func (evidence proposalWorkflowEvidence) complete() bool {
	return evidence.checkedOutBase && evidence.pinsEventBase && evidence.validatesProposalBase && evidence.usesMergeCandidate && evidence.persistsCandidateSHA && evidence.candidateSHAFromFetch && evidence.validatesCandidateTree && evidence.validatedChangedPaths && evidence.detectsRenames && evidence.rejectsNonRegularBlobs && evidence.persistsFetchCredentials && evidence.skipsCodeOnlyChanges && evidence.exportsProposalApplicability && evidence.gatesProposalApplier
}

func trustedBaseCheckoutPersistsCredentials(step map[string]any) bool {
	uses, _ := step["uses"].(string)
	if !strings.HasPrefix(uses, "actions/checkout@") {
		return false
	}
	with, _ := step["with"].(map[string]any)
	return with["ref"] == "main" && with["persist-credentials"] == true
}

func usesPullRequestMergeCandidate(step map[string]any) bool {
	env, _ := step["env"].(map[string]any)
	candidateSHA, _ := env["CANDIDATE_SHA"].(string)
	return strings.Contains(candidateSHA, "pull_request.merge_commit_sha")
}

func checksOutTrustedMain(step map[string]any) bool {
	uses, _ := step["uses"].(string)
	if !strings.HasPrefix(uses, "actions/checkout@") {
		return false
	}
	with, _ := step["with"].(map[string]any)
	ref, _ := with["ref"].(string)
	return ref == "main"
}

func assertProposalRunDoesNotCheckoutCandidate(t *testing.T, run string) {
	t.Helper()
	if strings.Contains(run, "git checkout") {
		t.Fatal("proposal validation must not check out candidate code")
	}
}

func assertProposalStepHasNoCredentials(t *testing.T, step map[string]any) {
	t.Helper()
	env, _ := step["env"].(map[string]any)
	for name, value := range env {
		if strings.Contains(strings.ToLower(name), "token") || strings.Contains(strings.ToLower(name), "private_key") || strings.Contains(fmt.Sprint(value), "secrets.") {
			t.Fatalf("proposal validation step receives credential-like environment %s", name)
		}
	}
}

func assertComputeWorkerReadOnly(t *testing.T, compute workerWorkflowJob) {
	t.Helper()
	if compute.Environment != "" || compute.Permissions["contents"] != "read" {
		t.Fatalf("compute job is not isolated with read-only contents: %#v", compute)
	}
	if _, hasWriterID := compute.Env["AVM_WRITER_APP_ID"]; hasWriterID {
		t.Fatal("compute job received the state writer App ID")
	}
	if _, hasWriterKey := compute.Env["AVM_WRITER_PRIVATE_KEY"]; hasWriterKey {
		t.Fatal("compute job received the state writer private key")
	}
	for permission, access := range compute.Permissions {
		if access != "read" {
			t.Fatalf("compute job has non-read permission %s=%s", permission, access)
		}
	}
	assertComputeStepsHaveNoCredentials(t, compute.Steps)
}

func assertComputeStepsHaveNoCredentials(t *testing.T, steps []workerWorkflowStep) {
	t.Helper()
	for _, step := range steps {
		for name, value := range step.Env {
			if strings.Contains(strings.ToLower(name), "token") || strings.Contains(strings.ToLower(name), "private_key") || strings.Contains(value, "secrets.") {
				t.Fatalf("compute step %q receives credential-like environment %s", step.Name, name)
			}
		}
		if strings.Contains(step.Run, "--mint-state-token") || strings.Contains(step.Run, "AVM_WRITER_PRIVATE_KEY") {
			t.Fatalf("compute step %q can mint a state credential", step.Name)
		}
	}
}

func assertTrustedWorkerJobsUsePublisherEnvironment(t *testing.T, jobs map[string]workerWorkflowJob) {
	t.Helper()
	if jobs["prepare"].Environment != "data-publisher" || jobs["validate-and-seal"].Environment != "data-publisher" {
		t.Fatal("trusted preparation and sealing jobs must use data-publisher")
	}
}

func TestAssessmentCLIListsCompiledDimensionsAndChecks(t *testing.T) {
	var dimensionsJSON bytes.Buffer
	if err := listAssessmentDefinitions(&dimensionsJSON, ""); err != nil {
		t.Fatal(err)
	}
	var dimensions []assessment.DimensionDefinition
	if err := json.Unmarshal(dimensionsJSON.Bytes(), &dimensions); err != nil {
		t.Fatal(err)
	}
	if len(dimensions) != 7 {
		t.Fatalf("listed dimensions = %d, want 7", len(dimensions))
	}
	var checksJSON bytes.Buffer
	if err := listAssessmentDefinitions(&checksJSON, assessment.DimensionProviderRequirements); err != nil {
		t.Fatal(err)
	}
	var checks []assessment.CheckDefinition
	if err := json.Unmarshal(checksJSON.Bytes(), &checks); err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].CheckID != assessment.CheckDeclaredProviderSources {
		t.Fatalf("listed provider-requirements checks = %#v", checks)
	}
}

func TestAssessmentCLISelectsDimensionsFromFactsFile(t *testing.T) {
	root := t.TempDir()
	factsPath := writeAssessmentFactsFile(t, root)
	resultsPath := filepath.Join(root, "results.json")
	selection := assessment.DimensionProviderRequirements + "," + assessment.DimensionProviderCatalog
	if err := run([]string{"--assess-facts", factsPath, "--dimension-ids", selection, "--assessment-out", resultsPath}); err != nil {
		t.Fatal(err)
	}
	results := readAssessmentResults(t, resultsPath)
	if len(results) != 2 {
		t.Fatalf("selected dimensions produced %d results, want 2", len(results))
	}
	assertAssessmentResultStatuses(t, results, assessment.StatusUnchecked)

	snapshotPath := filepath.Join(root, "provider-snapshots.json")
	observedAt := "2026-10-02T23:00:00Z"
	snapshots := []assessment.ProviderSnapshot{{
		SchemaVersion: 2, RecordType: "provider_snapshot",
		SnapshotID:      "snapshot-" + strings.Repeat("b", 64),
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", ProviderVersion: "4.0.0",
		Coverage:     assessment.SnapshotCoverage{Capabilities: map[string]string{"resource": "complete"}, Complete: true, ObservedAt: observedAt},
		Entries:      []assessment.ProviderEntry{{Kind: "resource", Name: "azurerm_resource_group"}},
		Producer:     assessment.SnapshotProducer{ID: "fixture", Version: "1.0.0", ContractVersion: 1},
		SourceDigest: strings.Repeat("c", 64), ObservedAt: observedAt,
	}}
	snapshotData, err := json.Marshal(snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(snapshotPath, snapshotData, 0o600); err != nil {
		t.Fatal(err)
	}
	withSnapshotPath := filepath.Join(root, "results-with-snapshot.json")
	if err = run([]string{
		"--assess-facts", factsPath, "--dimension-ids", selection,
		"--provider-snapshots", snapshotPath, "--assessment-out", withSnapshotPath,
	}); err != nil {
		t.Fatal(err)
	}
	assertAssessmentResultStatuses(t, readAssessmentResults(t, withSnapshotPath), assessment.StatusPass)
}

func TestAssessmentCLIRequiresReviewedClassificationForModuleChecks(t *testing.T) {
	root := t.TempDir()
	factsPath := writeAssessmentFactsFile(t, root)
	dimension := assessment.DimensionInterfaceDocumentation
	unreviewedPath := filepath.Join(root, "unreviewed.json")
	if err := run([]string{"--assess-facts", factsPath, "--dimension-ids", dimension, "--assessment-out", unreviewedPath}); err != nil {
		t.Fatal(err)
	}
	unreviewed := readAssessmentResults(t, unreviewedPath)
	if len(unreviewed) != 1 {
		t.Fatalf("unreviewed dimension results = %#v", unreviewed)
	}
	for _, check := range unreviewed[0].Checks {
		if check.Status != assessment.StatusUnchecked || check.Execution != assessment.ExecutionMissingInput {
			t.Fatalf("unreviewed module check = %#v", check)
		}
	}

	classificationPath := filepath.Join(root, "classifications.json")
	if err := os.WriteFile(classificationPath, []byte(`{".":"module"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reviewedPath := filepath.Join(root, "reviewed.json")
	if err := run([]string{
		"--assess-facts", factsPath, "--dimension-ids", dimension,
		"--classifications", classificationPath, "--assessment-out", reviewedPath,
	}); err != nil {
		t.Fatal(err)
	}
	reviewed := readAssessmentResults(t, reviewedPath)
	if len(reviewed) != 1 || len(reviewed[0].Checks) != 2 {
		t.Fatalf("reviewed dimension results = %#v", reviewed)
	}
	for _, check := range reviewed[0].Checks {
		if check.Status != assessment.StatusNotApplicable || check.Execution != assessment.ExecutionCompleted {
			t.Fatalf("reviewed module check = %#v", check)
		}
	}
}

func writeAssessmentFactsFile(t *testing.T, root string) string {
	t.Helper()
	configuration := `terraform {
  required_providers {
    azurerm = {
      source = "hashicorp/azurerm"
    }
  }
}

resource "azurerm_resource_group" "main" {}`
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: root, ModuleID: "avm-res-cli-assessment", SourceSHA: strings.Repeat("a", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []terraform.UnitInput{{Path: ".", Role: terraform.RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	factsPath := filepath.Join(root, "facts.json")
	factsData, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(factsPath, factsData, 0o600); err != nil {
		t.Fatal(err)
	}
	return factsPath
}

func readAssessmentResults(t *testing.T, path string) []assessment.DimensionResult {
	t.Helper()
	resultsData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var results []assessment.DimensionResult
	if err = json.Unmarshal(resultsData, &results); err != nil {
		t.Fatal(err)
	}
	return results
}

func assertAssessmentResultStatuses(t *testing.T, results []assessment.DimensionResult, providerCatalogStatus string) {
	t.Helper()
	statuses := map[string]string{}
	registry, err := assessment.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		for _, definition := range registry.ListDimensions() {
			if definition.DefinitionID == result.DefinitionID {
				statuses[definition.DimensionID] = result.Checks[0].Status
			}
		}
	}
	if statuses[assessment.DimensionProviderRequirements] != assessment.StatusPass || statuses[assessment.DimensionProviderCatalog] != providerCatalogStatus {
		t.Fatalf("selected dimension statuses = %#v", statuses)
	}
}

func TestPersistDiscoveryRequiresExplicitStateBranch(t *testing.T) {
	err := run([]string{
		"--persist-discovery", "missing-candidate.json",
		"--extractor-contract", "factory-hcl-v1",
		"--extractor-capabilities", "native-hcl-v1",
	})
	if err == nil || !strings.Contains(err.Error(), "--state-branch is required") {
		t.Fatalf("missing state branch error = %v", err)
	}
}

func TestPersistDiscoveryRejectsMainBeforeReadingCredentialsOrCandidate(t *testing.T) {
	err := persistAVMCatalog("missing-candidate.json", "main", "factory-hcl-v1", "native-hcl-v1", "", "")
	if err == nil || !strings.Contains(err.Error(), `must be "factory-state"`) {
		t.Fatalf("main branch error = %v", err)
	}
}

func TestDispatchOperationRequiresFactoryStateBranch(t *testing.T) {
	err := run([]string{"--dispatch-operation", "claim", "--state-branch", "main"})
	if err == nil || !strings.Contains(err.Error(), `must be "factory-state"`) {
		t.Fatalf("unsafe dispatcher branch error = %v", err)
	}
}

func TestDispatchClaimRequiresRequestID(t *testing.T) {
	_, err := claimDispatchRequest(context.Background(), nil, dispatchOperationOptions{
		dispatcherID: "dispatcher-test", leaseSeconds: 1800,
	}, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "--request-id") {
		t.Fatalf("missing targeted request error = %v", err)
	}
}

func TestDispatchOperationRequiresDedicatedStateToken(t *testing.T) {
	t.Setenv("FACTORY_STATE_TOKEN", "")
	err := run([]string{"--dispatch-operation", "claim", "--state-branch", "factory-state"})
	if err == nil || !strings.Contains(err.Error(), "FACTORY_STATE_TOKEN is required") {
		t.Fatalf("missing dispatcher state token error = %v", err)
	}
}

func TestMintStateTokenRequiresAppCredentials(t *testing.T) {
	t.Setenv("AVM_WRITER_APP_ID", "")
	t.Setenv("AVM_WRITER_PRIVATE_KEY", "")
	t.Setenv("APP_ID", "")
	t.Setenv("APP_PRIVATE_KEY", "")
	err := run([]string{"--mint-state-token"})
	if err == nil || !strings.Contains(err.Error(), "GitHub App credentials are required") {
		t.Fatalf("missing App credentials error = %v", err)
	}
}
