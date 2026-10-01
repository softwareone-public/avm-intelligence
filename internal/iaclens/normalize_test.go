package iaclens

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/softwareone-public/avm-intelligence/internal/toolchain"
	"gopkg.in/yaml.v3"
)

type assessmentFixtureProducer struct {
	Name                string `json:"name"`
	Version             string `json:"version"`
	Commit              string `json:"commit"`
	ReportSchemaVersion int    `json:"report_schema_version"`
	AdapterVersion      int    `json:"adapter_version"`
}

type assessmentFixtureCheck struct {
	ID     string `json:"id"`
	Engine string `json:"engine"`
	Status string `json:"status"`
}

type assessmentFixtureUnit struct {
	Path           string                   `json:"path"`
	Role           string                   `json:"role"`
	Owner          string                   `json:"owner,omitempty"`
	OwnerScope     string                   `json:"owner_scope,omitempty"`
	Classification Classification           `json:"classification"`
	SymbolCount    int                      `json:"symbol_count"`
	Checks         []assessmentFixtureCheck `json:"checks"`
}

type assessmentFixtureCase struct {
	Name          string                  `json:"name"`
	CLIKind       string                  `json:"cli_kind"`
	ModuleID      string                  `json:"module_id"`
	SourceSHA     string                  `json:"source_sha"`
	Raw           json.RawMessage         `json:"raw"`
	ExpectedUnits []assessmentFixtureUnit `json:"expected_units"`
}

type assessmentFixtureCorpus struct {
	PolicyPath            string                    `json:"policy_path"`
	PolicyName            string                    `json:"policy_name"`
	DeterministicCheckIDs []string                  `json:"deterministic_check_ids"`
	Producer              assessmentFixtureProducer `json:"producer"`
	Cases                 []assessmentFixtureCase   `json:"cases"`
}

type assessmentPolicyConfig struct {
	Name      string `yaml:"name"`
	Structure struct {
		ExampleDirs []string `yaml:"example_dirs"`
	} `yaml:"structure"`
	Classification struct {
		Overrides map[string]string `yaml:"overrides"`
		Signals   []struct {
			ID    string `yaml:"id"`
			Kind  string `yaml:"kind"`
			Block string `yaml:"block"`
		} `yaml:"signals"`
	} `yaml:"classification"`
	Checks []struct {
		ID      string `yaml:"id"`
		Engine  string `yaml:"engine"`
		Enabled *bool  `yaml:"enabled"`
	} `yaml:"checks"`
}

func TestNormalizeLegacyAssessmentCorpus(t *testing.T) {
	t.Parallel()
	corpus := readAssessmentFixtureCorpus(t)
	assertAssessmentPolicy(t, corpus)
	assertAssessmentProducer(t, corpus.Producer)
	for _, fixture := range corpus.Cases {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			t.Parallel()
			assertAssessmentFixture(t, corpus.Producer, fixture)
		})
	}
}

func readAssessmentFixtureCorpus(t *testing.T) assessmentFixtureCorpus {
	t.Helper()
	corpusPath := filepath.Join("..", "..", "testdata", "assessment", "legacy-v0.1.0.json")
	corpusData, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatal(err)
	}
	var corpus assessmentFixtureCorpus
	if err = json.Unmarshal(corpusData, &corpus); err != nil {
		t.Fatalf("decode assessment corpus: %v", err)
	}
	if corpus.PolicyPath != "config/iaclens.yaml" || corpus.PolicyName == "" || corpus.Producer.Name != "IaCLens" {
		t.Fatalf("assessment corpus identity is incomplete: %#v", corpus)
	}
	return corpus
}

func assertAssessmentPolicy(t *testing.T, corpus assessmentFixtureCorpus) {
	t.Helper()
	policyPath := filepath.Join("..", "..", filepath.FromSlash(corpus.PolicyPath))
	policyData, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	var policy assessmentPolicyConfig
	if err = yaml.Unmarshal(policyData, &policy); err != nil {
		t.Fatalf("decode factory assessment policy: %v", err)
	}
	if policy.Name != corpus.PolicyName {
		t.Fatalf("fixture policy = %q, configured policy = %q", corpus.PolicyName, policy.Name)
	}
	if len(policy.Structure.ExampleDirs) != 1 || policy.Structure.ExampleDirs[0] != "examples" {
		t.Fatalf("fixture example ownership policy = %#v", policy.Structure.ExampleDirs)
	}
	if len(policy.Classification.Overrides) != 0 {
		t.Fatalf("fixture assumes unreviewed path overrides: %#v", policy.Classification.Overrides)
	}
	configuredChecks := make(map[string]struct{}, len(policy.Checks))
	semanticCheckDisabled := false
	for _, check := range policy.Checks {
		configuredChecks[check.ID] = struct{}{}
		if check.ID == "module-purpose" && check.Engine == "jev" && check.Enabled != nil && !*check.Enabled {
			semanticCheckDisabled = true
		}
	}
	if !semanticCheckDisabled {
		t.Fatal("fixture expects the configured semantic check to remain disabled")
	}
	for _, checkID := range corpus.DeterministicCheckIDs {
		if _, exists := configuredChecks[checkID]; !exists {
			t.Errorf("fixture check %q is not in the configured policy", checkID)
		}
	}
	if !configuredSignalsMatchFixture(policy) {
		t.Fatalf("fixture classification signals drifted: %#v", policy.Classification.Signals)
	}
}

func configuredSignalsMatchFixture(policy assessmentPolicyConfig) bool {
	signals := make(map[string]string, len(policy.Classification.Signals))
	for _, signal := range policy.Classification.Signals {
		signals[signal.ID] = signal.Kind + ":" + signal.Block
	}
	return signals["declares-backend"] == "deployment:terraform.backend" &&
		signals["declares-cloud"] == "deployment:terraform.cloud"
}

func assertAssessmentProducer(t *testing.T, producer assessmentFixtureProducer) {
	t.Helper()
	toolchainDoc, err := toolchain.Load(filepath.Join("..", "..", "config", "toolchain.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if toolchainDoc.CLI.Version != producer.Version ||
		toolchainDoc.CLI.Commit != producer.Commit ||
		toolchainDoc.CLI.SchemaVersion != producer.ReportSchemaVersion ||
		toolchainDoc.Adapter.Version != producer.AdapterVersion {
		t.Fatalf("fixture producer does not match pinned toolchain: %#v", producer)
	}
	if producer.Name != "IaCLens" {
		t.Fatalf("unexpected legacy producer name %q", producer.Name)
	}
}

func assertAssessmentFixture(t *testing.T, producer assessmentFixtureProducer, fixture assessmentFixtureCase) {
	t.Helper()
	if fixture.CLIKind != "auto" && fixture.CLIKind != "module" {
		t.Fatalf("unsupported fixture CLI kind %q", fixture.CLIKind)
	}
	report, err := Normalize(fixture.Raw, Provenance{
		ModuleID: fixture.ModuleID, SourceSHA: fixture.SourceSHA,
		CLIVersion: producer.Version, CLICommit: producer.Commit,
		AdapterVersion: producer.AdapterVersion,
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(report.Units) != len(fixture.ExpectedUnits) {
		t.Fatalf("normalized units = %d, expected %d", len(report.Units), len(fixture.ExpectedUnits))
	}
	for index, expected := range fixture.ExpectedUnits {
		assertAssessmentUnit(t, index, report.Units[index], expected)
	}
}

func assertAssessmentUnit(t *testing.T, index int, actual Unit, expected assessmentFixtureUnit) {
	t.Helper()
	if actual.Path != expected.Path || actual.Role != expected.Role || actual.Owner != expected.Owner ||
		actual.OwnerScope != expected.OwnerScope || actual.Classification != expected.Classification ||
		actual.SymbolCount != expected.SymbolCount {
		t.Errorf("unit %d identity/ownership/classification = %#v", index, actual)
	}
	if len(actual.Checks) != len(expected.Checks) {
		t.Errorf("unit %s checks = %d, expected %d", actual.Path, len(actual.Checks), len(expected.Checks))
		return
	}
	for checkIndex, expectedCheck := range expected.Checks {
		actualCheck := actual.Checks[checkIndex]
		if actualCheck.ID != expectedCheck.ID || actualCheck.Engine != expectedCheck.Engine || actualCheck.Status != expectedCheck.Status {
			t.Errorf("unit %s check %d = %#v, expected %#v", actual.Path, checkIndex, actualCheck, expectedCheck)
		}
	}
}

func TestNormalizePreservesOwnershipAndStatuses(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
	  "iaclens": {
	    "schema_version": 2,
	    "source_digest": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	    "repository": {"root": "/private/worktree", "name": "source"},
	    "ruleset": {"name": "avm-intelligence-offline", "version": 1, "digest": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	    "units": [
	      {"path": ".", "symbols": [{"kind": "variable", "file": "main.tf", "line": 1, "attributes": {"description": "secret-looking expression"}}]},
	      {"path": "examples/default", "symbols": []}
	    ],
	    "analysis": {"units": [
	      {"path": ".", "role": "component", "classification": {"kind": "module", "method": "override", "reason": "factory root override"}, "checks": [
	        {"id": "input-descriptions", "engine": "go", "status": "fail", "severity": "warning", "message": "missing description", "evidence": ["main.tf:1"]},
	        {"id": "module-purpose", "engine": "jev", "status": "unchecked", "severity": "info", "message": "model disabled"}
	      ]},
	      {"path": "examples/default", "role": "example", "owner": ".", "owner_scope": "component", "classification": {"kind": "deployment", "method": "go", "reason": "declares-backend"}, "checks": [
	        {"id": "deployment-core-version", "engine": "go", "status": "not_applicable", "severity": "warning", "message": "no terraform block"}
	      ]}
	    ]}
	  }
	}`)
	report, err := Normalize(raw, Provenance{ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("c", 40), CLIVersion: "0.1.0", CLICommit: strings.Repeat("d", 40), AdapterVersion: AdapterVersion})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(report.Units) != 2 || report.Units[1].Owner != "." || report.Units[1].OwnerScope != "component" {
		t.Fatalf("ownership was flattened: %#v", report.Units)
	}
	if report.Units[0].Checks[0].Status != "fail" || report.Units[0].Checks[1].Status != "unchecked" || report.Units[1].Checks[0].Status != "not_applicable" {
		t.Fatalf("statuses changed: %#v", report.Units)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-looking") || strings.Contains(string(encoded), "/private/worktree") {
		t.Fatal("public projection retained source expression or local root")
	}
}

func TestNormalizeRejectsUnknownSchemaAndStatus(t *testing.T) {
	t.Parallel()
	provenance := Provenance{ModuleID: "demo", SourceSHA: strings.Repeat("a", 40), CLIVersion: "0.1.0", CLICommit: strings.Repeat("b", 40), AdapterVersion: AdapterVersion}
	if _, err := Normalize([]byte(`{"iaclens":{"schema_version":3}}`), provenance); err == nil {
		t.Fatal("future schema was accepted")
	}
	raw := []byte(`{"iaclens":{"schema_version":2,"source_digest":"aa","ruleset":{"name":"x","version":1,"digest":"bb"},"units":[{"path":".","symbols":[]}],"analysis":{"units":[{"path":".","role":"component","classification":{"kind":"module","method":"go","reason":"fixture"},"checks":[{"id":"x","engine":"go","status":"passed","severity":"warning","message":"bad"}]}]}}}`)
	if _, err := Normalize(raw, provenance); err == nil {
		t.Fatal("unknown status was accepted")
	}
}
