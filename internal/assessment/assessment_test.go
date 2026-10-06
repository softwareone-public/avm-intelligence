package assessment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

func TestRunDimensionsPreservesSuccessfulResultsWhenInputsAreMissing(t *testing.T) {
	registry, _, results := runAssessmentFixture(t)
	if len(results) != 3 {
		t.Fatalf("dimension results = %d, want 3", len(results))
	}
	resultsByDimension := indexDimensionResults(t, registry, results)
	assertProviderRequirementPass(t, resultsByDimension[DimensionProviderRequirements])
	assertDimensionUnavailable(t, resultsByDimension[DimensionStaticFactsCoverage])
	assertDimensionUnavailable(t, resultsByDimension[DimensionProviderCatalog])
	assertDimensionIdentitiesIndependent(t, resultsByDimension)
}

func TestAssessProviderUsageMapsAllProviderSymbolKinds(t *testing.T) {
	const address = "registry.terraform.io/hashicorp/azurerm"
	tests := []struct {
		symbolKind string
		entryKind  string
		name       string
	}{
		{symbolKind: "resource", entryKind: "resource", name: "azurerm_resource_group.main"},
		{symbolKind: "data", entryKind: "datasource", name: "azurerm_client_config.current"},
		{symbolKind: "ephemeral", entryKind: "ephemeral", name: "azurerm_key_vault_secret.secret"},
		{symbolKind: "action", entryKind: "action", name: "azurerm_run_command.execute"},
		{symbolKind: "function", entryKind: "function", name: "command_id"},
	}
	for _, test := range tests {
		t.Run(test.symbolKind, func(t *testing.T) {
			snapshot := ProviderSnapshot{
				ProviderAddress: address,
				Coverage:        SnapshotCoverage{Capabilities: map[string]string{test.entryKind: terraform.CoverageComplete}},
				Entries:         []ProviderEntry{{Kind: test.entryKind, Name: symbolType(test.name)}},
			}
			symbol := terraform.FactSymbol{Kind: test.symbolKind, Name: test.name, ProviderAddress: address}
			assessment := assessProviderUsage(symbol, map[string]ProviderSnapshot{address: snapshot})
			if assessment.status != StatusPass {
				t.Fatalf("provider usage assessment = %#v, want pass", assessment)
			}
			snapshot.Coverage.Capabilities[test.entryKind] = terraform.CoveragePartial
			assessment = assessProviderUsage(symbol, map[string]ProviderSnapshot{address: snapshot})
			if assessment.status != StatusUnchecked {
				t.Fatalf("partial %s coverage assessment = %#v, want unchecked", test.entryKind, assessment)
			}
		})
	}
	coreFunction := assessProviderUsage(terraform.FactSymbol{Kind: "function", Name: "lower"}, nil)
	if coreFunction.status != StatusNotApplicable {
		t.Fatalf("core function assessment = %#v, want not_applicable", coreFunction)
	}
}

func TestProviderCurrencyUsesExactSnapshotTargetsAndConstraints(t *testing.T) {
	const address = "registry.terraform.io/hashicorp/azurerm"
	facts := providerFactsFixture(t)
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	checkCurrency := func(targetVersion, idCharacter string) DimensionResult {
		snapshot := providerSnapshotFixture(idCharacter, address, terraform.CoverageComplete, nil)
		snapshot.ProviderVersion = targetVersion
		results, runErr := RunDimensions(registry, RunInput{
			Facts: facts, Snapshots: []ProviderSnapshot{snapshot},
			TargetVersions: map[string]string{address: targetVersion},
			DimensionIDs:   []string{DimensionProviderCurrency},
		})
		if runErr != nil {
			t.Fatal(runErr)
		}
		return results[0]
	}
	compatible := checkCurrency("4.2.0", "1")
	if compatible.Checks[0].Status != StatusPass {
		t.Fatalf("compatible currency result = %#v, want pass", compatible.Checks[0])
	}
	incompatible := checkCurrency("5.0.0", "2")
	if incompatible.Checks[0].Status != StatusFail {
		t.Fatalf("incompatible currency result = %#v, want fail", incompatible.Checks[0])
	}
	if compatible.InputFingerprint == incompatible.InputFingerprint {
		t.Fatal("currency input fingerprint ignored a changed target version")
	}

	snapshot := providerSnapshotFixture("3", address, terraform.CoverageComplete, nil)
	unchecked, err := RunDimensions(registry, RunInput{
		Facts: facts, Snapshots: []ProviderSnapshot{snapshot}, DimensionIDs: []string{DimensionProviderCurrency},
	})
	if err != nil {
		t.Fatal(err)
	}
	if unchecked[0].Checks[0].Status != StatusUnchecked {
		t.Fatalf("missing target result = %#v, want unchecked", unchecked[0].Checks[0])
	}
	if _, err = RunDimensions(registry, RunInput{
		Facts: facts, Snapshots: []ProviderSnapshot{snapshot}, TargetVersions: map[string]string{address: "5.0.0"},
		DimensionIDs: []string{DimensionProviderCurrency},
	}); err == nil {
		t.Fatal("target vector mismatch with accepted snapshot was accepted")
	}
}

func TestRunDimensionsIdentityStableForEqualInputs(t *testing.T) {
	registry, input, firstRun := runAssessmentFixture(t)
	secondRun, err := RunDimensions(registry, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstRun) != len(secondRun) {
		t.Fatalf("repeat run results = %d, want %d", len(secondRun), len(firstRun))
	}
	for index := range firstRun {
		if firstRun[index].DimensionResultID != secondRun[index].DimensionResultID {
			t.Fatalf("dimension result identity changed between equal inputs: %#v vs %#v", firstRun[index], secondRun[index])
		}
	}
}

func TestRunDimensionsIgnoreSnapshotInputsUnusedByDimension(t *testing.T) {
	facts := providerFactsFixture(t)
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	input := RunInput{Facts: facts, DimensionIDs: []string{DimensionProviderRequirements}}
	withoutSnapshot, err := RunDimensions(registry, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Snapshots = []ProviderSnapshot{providerSnapshotFixture("1", "registry.terraform.io/hashicorp/aws", "complete", nil)}
	withUnrelatedSnapshot, err := RunDimensions(registry, input)
	if err != nil {
		t.Fatal(err)
	}
	if withoutSnapshot[0].DimensionResultID != withUnrelatedSnapshot[0].DimensionResultID {
		t.Fatal("unrelated snapshot changed a fact-only dimension result identity")
	}
}

func TestDimensionRequiredCapabilitiesGateCheckExecution(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definition := registry.dimensions[DimensionProviderRequirements]
	definition.RequiredCapabilities = append(definition.RequiredCapabilities, "upstream_snapshot")
	registry.dimensions[DimensionProviderRequirements] = definition
	results, err := RunDimensions(registry, RunInput{
		Facts: providerFactsFixture(t), DimensionIDs: []string{DimensionProviderRequirements},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || len(results[0].Checks) != 1 {
		t.Fatalf("dimension gate results = %#v", results)
	}
	assertCheckOutcome(t, results[0].Checks[0], StatusUnchecked, ExecutionMissingInput)
	if results[0].ExecutionComplete || results[0].Coverage.Unavailable == 0 {
		t.Fatalf("dimension capability gap was hidden: %#v", results[0])
	}
}

func TestProviderTypeInventoryHonorsSnapshotCoverage(t *testing.T) {
	tests := []struct {
		name        string
		coverage    string
		entries     []ProviderEntry
		wantStatus  string
		wantExecute string
	}{
		{name: "listed in complete snapshot", coverage: "complete", entries: []ProviderEntry{{Kind: "resource", Name: "azurerm_resource_group"}}, wantStatus: StatusPass, wantExecute: ExecutionCompleted},
		{name: "absent from complete snapshot", coverage: "complete", entries: []ProviderEntry{{Kind: "resource", Name: "azurerm_virtual_network"}}, wantStatus: StatusFail, wantExecute: ExecutionCompleted},
		{name: "absent from partial snapshot", coverage: "partial", entries: []ProviderEntry{}, wantStatus: StatusUnchecked, wantExecute: ExecutionMissingInput},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := DefaultRegistry()
			if err != nil {
				t.Fatal(err)
			}
			facts := providerFactsFixture(t)
			results, err := RunDimensions(registry, RunInput{
				Facts: facts, DimensionIDs: []string{DimensionProviderCatalog},
				Snapshots: []ProviderSnapshot{providerSnapshotFixture(string(rune('1'+index)), "registry.terraform.io/hashicorp/azurerm", test.coverage, test.entries)},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || len(results[0].Checks) != 1 {
				t.Fatalf("provider type results = %#v", results)
			}
			check := results[0].Checks[0]
			if check.Status != test.wantStatus || check.Execution != test.wantExecute {
				t.Fatalf("provider type check = %#v, want %s/%s", check, test.wantStatus, test.wantExecute)
			}
		})
	}
}

func providerSnapshotFixture(idCharacter, address, coverage string, entries []ProviderEntry) ProviderSnapshot {
	observedAt := "2026-10-02T23:00:00Z"
	return ProviderSnapshot{
		SchemaVersion: 2, RecordType: "provider_snapshot",
		SnapshotID:      "snapshot-" + strings.Repeat(idCharacter, 64),
		ProviderAddress: address, ProviderVersion: "4.0.0",
		Coverage:     SnapshotCoverage{Capabilities: map[string]string{"resource": coverage}, Complete: coverage == terraform.CoverageComplete, ObservedAt: observedAt},
		Entries:      entries,
		Producer:     SnapshotProducer{ID: "assessment-test", Version: "1.0.0", ContractVersion: 1},
		SourceDigest: strings.Repeat("b", 64), ObservedAt: observedAt,
	}
}

func TestClassifyUnitPathUsesReviewedConventions(t *testing.T) {
	tests := []struct {
		path       string
		role       string
		owner      string
		classified bool
	}{
		{path: ".", role: terraform.RoleComponent, owner: ".", classified: true},
		{path: "modules/peering", role: terraform.RoleChildComponent, owner: "modules/peering", classified: true},
		{path: "examples/default", role: terraform.RoleExample, owner: ".", classified: true},
		{path: "modules/peering/examples/default", role: terraform.RoleExample, owner: "modules/peering", classified: true},
		{path: "tools/fixtures", classified: false},
	}
	for _, test := range tests {
		classification, err := ClassifyUnitPath(test.path)
		if err != nil {
			t.Fatalf("ClassifyUnitPath(%q): %v", test.path, err)
		}
		if classification.Classified != test.classified || classification.Role != test.role || classification.Owner != test.owner {
			t.Errorf("ClassifyUnitPath(%q) = %#v", test.path, classification)
		}
	}
	if _, err := ClassifyUnitPath("../outside"); err == nil {
		t.Fatal("parent-traversing unit path was accepted")
	}
}

func TestRunDimensionsMapsNativeMetadataChecksWithReviewedApplicability(t *testing.T) {
	facts := nativeMetadataFactsFixture(t)
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	results, err := RunDimensions(registry, RunInput{
		Facts:           facts,
		Classifications: map[string]string{".": classificationModule},
		DimensionIDs: []string{
			DimensionInterfaceDocumentation,
			DimensionModuleOwnership,
			DimensionDeploymentCoreVersion,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("native metadata dimension results = %d, want 3", len(results))
	}
	byDimension := indexDimensionResults(t, registry, results)
	interfaceChecks := checksByID(byDimension[DimensionInterfaceDocumentation].Checks)
	assertCheckOutcome(t, interfaceChecks[CheckInputDescriptions], StatusFail, ExecutionCompleted)
	assertCheckOutcome(t, interfaceChecks[CheckOutputDescriptions], StatusPass, ExecutionCompleted)
	moduleChecks := checksByID(byDimension[DimensionModuleOwnership].Checks)
	assertCheckOutcome(t, moduleChecks[CheckModuleStateCallerOwned], StatusFail, ExecutionCompleted)
	assertCheckOutcome(t, moduleChecks[CheckModuleCloudCallerOwned], StatusFail, ExecutionCompleted)
	deploymentChecks := checksByID(byDimension[DimensionDeploymentCoreVersion].Checks)
	assertCheckOutcome(t, deploymentChecks[CheckDeploymentCoreVersion], StatusPass, ExecutionCompleted)
	if byDimension[DimensionDeploymentCoreVersion].UnitPath != "examples/backend" {
		t.Fatalf("deployment dimension applied to %q", byDimension[DimensionDeploymentCoreVersion].UnitPath)
	}
}

func TestDeterministicChecksMatchReviewedNativeCorpus(t *testing.T) {
	facts := reviewedNativeFactsFixture(t)
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	results, err := RunDimensions(registry, RunInput{
		Facts: facts, Classifications: map[string]string{".": classificationModule},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := readNativeExpectedCase(t, "component-root-and-configured-example")
	assertNativeCheckOutcomes(t, results, fixture.ExpectedUnits)
}

type nativeExpectedCheck struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	Severity string   `json:"severity"`
	Evidence []string `json:"evidence"`
}

type nativeExpectedUnit struct {
	Path   string                `json:"path"`
	Checks []nativeExpectedCheck `json:"checks"`
}

type nativeExpectedCase struct {
	Name          string               `json:"name"`
	ModuleID      string               `json:"module_id"`
	SourceSHA     string               `json:"source_sha"`
	ExpectedUnits []nativeExpectedUnit `json:"expected_units"`
}

type nativeAssessmentCorpus struct {
	Cases []nativeExpectedCase `json:"cases"`
}

func readNativeExpectedCase(t *testing.T, caseName string) nativeExpectedCase {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "assessment", "native-checks.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var corpus nativeAssessmentCorpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("decode reviewed native corpus: %v", err)
	}
	for _, fixture := range corpus.Cases {
		if fixture.Name == caseName {
			return fixture
		}
	}
	t.Fatalf("reviewed native corpus is missing %s", caseName)
	return nativeExpectedCase{}
}

func nativeEvidenceLocationsMatch(unitPath, checkID string, expected, actual []string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for position, expectedLocation := range expected {
		nativeLocation := actual[position]
		if unitPath != "." {
			nativeLocation = strings.TrimPrefix(nativeLocation, unitPath+"/")
		}
		if checkID == CheckDeclaredProviderSources {
			if strings.SplitN(nativeLocation, ":", 2)[0] == strings.SplitN(expectedLocation, ":", 2)[0] {
				continue
			}
			return false
		}
		if nativeLocation != expectedLocation && !strings.HasPrefix(nativeLocation, expectedLocation+":") {
			return false
		}
	}
	return true
}

func assertNativeCheckOutcomes(t *testing.T, results []DimensionResult, expected []nativeExpectedUnit) {
	t.Helper()
	for _, unit := range expected {
		assertNativeUnitCheckOutcomes(t, results, unit)
	}
}

func assertNativeUnitCheckOutcomes(t *testing.T, results []DimensionResult, expected nativeExpectedUnit) {
	t.Helper()
	for _, expectedCheck := range expected.Checks {
		actual, found := findCheckResult(results, expected.Path, expectedCheck.ID)
		if !found {
			t.Errorf("native check %s for %s was not emitted", expectedCheck.ID, expected.Path)
			continue
		}
		if actual.Status != expectedCheck.Status || actual.Severity != expectedCheck.Severity {
			t.Errorf("native check %s for %s = %s/%s, want %s/%s", expectedCheck.ID, expected.Path, actual.Status, actual.Severity, expectedCheck.Status, expectedCheck.Severity)
		}
		if !nativeEvidenceLocationsMatch(expected.Path, expectedCheck.ID, expectedCheck.Evidence, actual.Evidence) {
			t.Errorf("native check %s for %s evidence = %v, want locations %v", expectedCheck.ID, expected.Path, actual.Evidence, expectedCheck.Evidence)
		}
	}
}

func TestRunDimensionsRequiresReviewedDeploymentClassification(t *testing.T) {
	facts := standaloneExampleFactsFixture(t)
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	input := RunInput{Facts: facts, DimensionIDs: []string{DimensionDeploymentCoreVersion}}
	unknownResults, err := RunDimensions(registry, input)
	if err != nil {
		t.Fatal(err)
	}
	assertCheckOutcome(t, unknownResults[0].Checks[0], StatusUnchecked, ExecutionMissingInput)

	input.Classifications = map[string]string{"examples/standalone": classificationDeployment}
	overriddenResults, err := RunDimensions(registry, input)
	if err != nil {
		t.Fatal(err)
	}
	assertCheckOutcome(t, overriddenResults[0].Checks[0], StatusPass, ExecutionCompleted)
	if unknownResults[0].DimensionResultID == overriddenResults[0].DimensionResultID {
		t.Fatal("classification override did not change the dimension input identity")
	}
}

func TestRegistryDimensionListReturnsDefensiveCopies(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	dimensions := registry.ListDimensions()
	definition, found := findDimensionDefinition(dimensions, DimensionProviderRequirements)
	if !found || len(definition.CheckIDs) == 0 || len(definition.RequiredCapabilities) == 0 {
		t.Fatal("provider-requirements definition is missing compiled inputs")
	}
	definition.CheckIDs[0] = "mutated-check"
	definition.RequiredCapabilities[0] = "mutated-capability"
	updated, _ := findDimensionDefinition(registry.ListDimensions(), DimensionProviderRequirements)
	if updated.CheckIDs[0] != CheckDeclaredProviderSources || updated.RequiredCapabilities[0] == "mutated-capability" {
		t.Fatalf("dimension list mutation affected registry: %#v", updated)
	}
}

func TestRegistryCheckListReturnsDefensiveCopies(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	checks, err := registry.ListChecks(DimensionProviderRequirements)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) == 0 || len(checks[0].RequiredCapabilities) == 0 {
		t.Fatalf("provider check definition is incomplete: %#v", checks)
	}
	checks[0].RequiredCapabilities[0] = "mutated-check-capability"
	checks, err = registry.ListChecks(DimensionProviderRequirements)
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].RequiredCapabilities[0] == "mutated-check-capability" {
		t.Fatalf("check list mutation affected registry: %#v", checks[0])
	}
}

func findDimensionDefinition(definitions []DimensionDefinition, id string) (DimensionDefinition, bool) {
	for _, definition := range definitions {
		if definition.DimensionID == id {
			return definition, true
		}
	}
	return DimensionDefinition{}, false
}

func runAssessmentFixture(t *testing.T) (Registry, RunInput, []DimensionResult) {
	t.Helper()
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	input := RunInput{
		Facts: providerFactsFixture(t),
		DimensionIDs: []string{
			DimensionProviderRequirements,
			DimensionStaticFactsCoverage,
			DimensionProviderCatalog,
		},
	}
	results, err := RunDimensions(registry, input)
	if err != nil {
		t.Fatal(err)
	}
	return registry, input, results
}

func indexDimensionResults(t *testing.T, registry Registry, results []DimensionResult) map[string]DimensionResult {
	t.Helper()
	dimensionByDefinition := make(map[string]string)
	for _, definition := range registry.ListDimensions() {
		dimensionByDefinition[definition.DefinitionID] = definition.DimensionID
	}
	indexed := make(map[string]DimensionResult, len(results))
	for _, result := range results {
		dimensionID, exists := dimensionByDefinition[result.DefinitionID]
		if !exists {
			t.Fatalf("result references unknown definition %s", result.DefinitionID)
		}
		indexed[dimensionID] = result
	}
	return indexed
}

func assertProviderRequirementPass(t *testing.T, result DimensionResult) {
	t.Helper()
	if len(result.Checks) != 1 || result.Checks[0].Status != StatusPass || result.Checks[0].Execution != ExecutionCompleted {
		t.Fatalf("provider-requirements result = %#v", result)
	}
}

func assertDimensionUnavailable(t *testing.T, result DimensionResult) {
	t.Helper()
	if result.ExecutionComplete || result.Coverage.Unavailable == 0 || !hasMissingInput(result.Checks) {
		t.Fatalf("dimension should remain explicitly unavailable: %#v", result)
	}
}

func assertDimensionIdentitiesIndependent(t *testing.T, results map[string]DimensionResult) {
	t.Helper()
	provider := results[DimensionProviderRequirements]
	coverage := results[DimensionStaticFactsCoverage]
	catalog := results[DimensionProviderCatalog]
	if provider.DimensionResultID == coverage.DimensionResultID || provider.DimensionResultID == catalog.DimensionResultID {
		t.Fatal("independent dimensions shared a result identity")
	}
}

func nativeMetadataFactsFixture(t *testing.T) terraform.Facts {
	t.Helper()
	root := t.TempDir()
	writeFixture := func(relativePath, source string) {
		t.Helper()
		path := filepath.Join(root, relativePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture("main.tf", `terraform {
  required_version = ">= 1.9.0"
  backend "azurerm" {}
  cloud {}
}

variable "documented" { description = "name" }
variable "undocumented" {}
output "documented" { description = "id" }
`)
	writeFixture("examples/backend/main.tf", `terraform {
  required_version = ">= 1.9.0"
  backend "azurerm" {}
}
`)
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: root, ModuleID: "avm-res-assessment-metadata", SourceSHA: strings.Repeat("9", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units: []terraform.UnitInput{
			{Path: ".", Role: terraform.RoleComponent, Files: []string{"main.tf"}},
			{Path: "examples/backend", Role: terraform.RoleExample, Owner: ".", Files: []string{"main.tf"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func standaloneExampleFactsFixture(t *testing.T) terraform.Facts {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "examples", "standalone", "main.tf")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	source := `terraform {
  required_version = ">= 1.9.0"
}`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: root, ModuleID: "avm-res-assessment-unknown", SourceSHA: strings.Repeat("8", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []terraform.UnitInput{{Path: "examples/standalone", Role: terraform.RoleExample, Owner: ".", Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func providerFactsFixture(t *testing.T) terraform.Facts {
	t.Helper()
	root := t.TempDir()
	configuration := `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}
resource "azurerm_resource_group" "main" {}`
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: root, ModuleID: "avm-res-assessment-fixture", SourceSHA: strings.Repeat("a", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []terraform.UnitInput{{Path: ".", Role: terraform.RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func checksByID(checks []CheckResult) map[string]CheckResult {
	byID := make(map[string]CheckResult, len(checks))
	for _, check := range checks {
		byID[check.ID] = check
	}
	return byID
}

func assertCheckOutcome(t *testing.T, check CheckResult, status, execution string) {
	t.Helper()
	if check.ID == "" || check.Status != status || check.Execution != execution {
		t.Fatalf("check outcome = %#v, want status/execution %s/%s", check, status, execution)
	}
}

func reviewedNativeFactsFixture(t *testing.T) terraform.Facts {
	t.Helper()
	root := t.TempDir()
	writeSource := func(relativePath, source string) {
		t.Helper()
		path := filepath.Join(root, relativePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeSource("variables.tf", `variable "undocumented" {}`)
	writeSource("outputs.tf", `output "documented" { description = "id" }`)
	writeSource("versions.tf", `terraform {
  required_providers { azurerm = { source = "hashicorp/azurerm" } }
}`)
	writeSource("examples/backend/main.tf", `terraform {
	required_version = ">= 1.9.0"
	required_providers { azurerm = { source = "hashicorp/azurerm" } }
	backend "azurerm" {}
}
`)
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: root, ModuleID: "avm-res-assessment-root", SourceSHA: strings.Repeat("1", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units: []terraform.UnitInput{
			{Path: ".", Role: terraform.RoleComponent, Files: []string{"variables.tf", "outputs.tf", "versions.tf"}},
			{Path: "examples/backend", Role: terraform.RoleExample, Owner: ".", Files: []string{"main.tf"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func findCheckResult(results []DimensionResult, unitPath, checkID string) (CheckResult, bool) {
	for _, dimension := range results {
		if dimension.UnitPath != unitPath {
			continue
		}
		for _, check := range dimension.Checks {
			if check.ID == checkID {
				return check, true
			}
		}
	}
	return CheckResult{}, false
}

func hasMissingInput(results []CheckResult) bool {
	for _, result := range results {
		if result.Execution == ExecutionMissingInput && result.Status == StatusUnchecked {
			return true
		}
	}
	return false
}
