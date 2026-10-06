package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/github"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"gopkg.in/yaml.v3"
)

func TestWorkflowFamiliesAndShellContracts(t *testing.T) {
	directory := filepath.Join("..", "..", ".github", "workflows")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			assertWorkflowFamily(t, filepath.Join(directory, entry.Name()))
		})
	}
}

func assertWorkflowFamily(t *testing.T, filename string) {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	name := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	if document["name"] != name {
		t.Fatal("workflow display name must match its filename")
	}
	triggers := document["on"].(map[string]any)
	_, dispatch := triggers["workflow_dispatch"]
	switch {
	case strings.HasPrefix(name, "factory-"):
		if dispatch {
			t.Fatal("factory workflows must not expose dispatch")
		}
	case strings.HasPrefix(name, "manual-"):
		if !dispatch || len(triggers) != 1 {
			t.Fatal("manual workflows must be dispatch-only")
		}
	default:
		t.Fatal("workflow must use a factory- or manual- prefix")
	}
	for _, value := range document["jobs"].(map[string]any) {
		assertWorkflowJobShellAndPins(t, value.(map[string]any))
	}
}

func assertWorkflowJobShellAndPins(t *testing.T, job map[string]any) {
	t.Helper()
	steps, _ := job["steps"].([]any)
	assertProviderWorkflowScriptsParse(t, steps)
	for _, value := range steps {
		uses, _ := value.(map[string]any)["uses"].(string)
		if uses == "" || strings.HasPrefix(uses, "./") {
			continue
		}
		_, commit, found := strings.Cut(uses, "@")
		decoded, err := hex.DecodeString(commit)
		if !found || err != nil || len(decoded) != 20 {
			t.Fatalf("action is not full-SHA pinned: %s", uses)
		}
	}
}

func TestDiscoveryWorkflowScopeBoundary(t *testing.T) {
	_, _, factory := providerWorkflowDocumentFor(t, "factory-discovery.yml", "persist")
	_, _, manual := providerWorkflowDocumentFor(t, "manual-discovery.yml", "persist")
	if !workflowRunsContain(factory, "--discovery-new-modules-only") || workflowRunsContain(manual, "--discovery-new-modules-only") {
		t.Fatal("factory new-module scope must be explicit and separate from manual selection")
	}
	if !workflowRunsContain(factory, ".projection_module_ids // [] | join(\",\")") || workflowRunsContain(factory, "--discovery-module-cycle") {
		t.Fatal("factory discovery must retain its new-ID-only transaction/result projection")
	}
	if !workflowRunsContain(manual, "--discovery-module-cycle") || workflowRunsContain(manual, "--project-discovery-modules") {
		t.Fatal("manual discovery must finish singleton cycles instead of a later fleet projection")
	}
}

func TestModuleCycleRejectsProtectedDelivery(t *testing.T) {
	now := time.Now().UTC()
	for _, deliveryState := range []string{github.ProjectionProjecting, github.ProjectionRetryable, github.ProjectionNeedsReview} {
		t.Run(deliveryState, func(t *testing.T) {
			snapshot := manualProjectionTestSnapshot(t, "avm-res-catalog-a", deliveryState, now)
			intent, _, err := readProjectDeliveryIntent(snapshot, "avm-res-catalog-a")
			if err != nil {
				t.Fatal(err)
			}
			if err := validateCycleDeliveryOwnership(intent, now); err == nil {
				t.Fatal("module cycle bypassed active lease, retry delay or review")
			}
		})
	}
}

func TestModuleCycleProjectorDoesNotBypassProtectedDeliveryOrReprocessAcknowledgedWork(t *testing.T) {
	for _, deliveryState := range []string{github.ProjectionProjecting, github.ProjectionRetryable, github.ProjectionNeedsReview, github.ProjectionProjected} {
		t.Run(deliveryState, func(t *testing.T) { assertCycleProjectorState(t, deliveryState) })
	}
}

func assertCycleProjectorState(t *testing.T, deliveryState string) {
	t.Helper()
	now := time.Now().UTC()
	moduleID := "avm-res-catalog-a"
	snapshot := manualProjectionTestSnapshot(t, moduleID, deliveryState, now)
	accepted := cycleProjectorTestCatalog(t, now)
	data, err := json.Marshal(discovery.DurableCatalogState{SchemaVersion: 1, Accepted: accepted})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[discoveryCatalogStatePath] = state.Record{Kind: "discovery_catalog", SchemaVersion: 1, Data: data}
	lifecycle := discovery.ProjectionIntent{SchemaVersion: 1, ModuleID: moduleID, DesiredGeneration: 1, Status: discovery.IntentBacklog, UpdatedAt: now}
	data, err = json.Marshal(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records["state/projection/intents/"+moduleID+".json"] = state.Record{Kind: "projection_intent", SchemaVersion: 1, Data: data}
	if deliveryState == github.ProjectionProjected {
		intent, _, readErr := readProjectDeliveryIntent(snapshot, moduleID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		intent.LastAcknowledgedFingerprint = intent.Fingerprint
		intent.DiscoveryFingerprint, err = identity.ID("discovery-intent", 1, json.RawMessage(data))
		if err != nil {
			t.Fatal(err)
		}
		data, err = json.Marshal(intent)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Records["state/projection/delivery/"+moduleID+".json"] = state.Record{Kind: "projection_delivery", SchemaVersion: 1, Data: data}
	}
	backend := &workerTestBackend{head: "head-0", snapshot: snapshot}
	projector := &moduleCycleProjector{backend: backend, runURL: "https://github.com/example/run", runID: "cycle-test"}
	for _, changed := range []bool{false, true} {
		if changed && deliveryState == github.ProjectionProjected {
			continue
		}
		head, deliveryErr := projector.deliver(context.Background(), discovery.ModuleCycle{ModuleID: moduleID, Head: backend.head, ProjectionRequired: changed})
		if (deliveryErr == nil) != (deliveryState == github.ProjectionProjected) || head != "head-0" || backend.head != "head-0" || projector.client.Token != "" {
			t.Fatalf("protected or acknowledged delivery was rewritten: state=%s changed=%t head=%s error=%v", deliveryState, changed, head, deliveryErr)
		}
	}
	if _, err := projector.deliver(context.Background(), discovery.ModuleCycle{ModuleID: moduleID, Head: "stale-head"}); err != state.ErrHeadMoved {
		t.Fatalf("cycle ignored an intervening state writer: %v", err)
	}
}

func cycleProjectorTestCatalog(t *testing.T, now time.Time) catalog.AcceptedState {
	t.Helper()
	candidate := catalog.Snapshot{SchemaVersion: 1, FetchedAt: now.Add(-time.Minute)}
	for _, source := range catalog.OfficialSources {
		moduleID := "avm-" + string(source.Kind) + "-catalog-a"
		candidate.Modules = append(candidate.Modules, catalog.Module{ID: moduleID, Type: source.Kind, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-" + moduleID, DisplayName: moduleID, Description: "fixture"})
		candidate.Feeds = append(candidate.Feeds, catalog.FeedSnapshot{Kind: source.Kind, URL: source.URL, SHA256: strings.Repeat("a", 64), ModuleCount: 1})
	}
	accepted, _, err := catalog.AcceptSnapshot(nil, candidate, now)
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func TestModuleCycleRetryReconcilesNewerDurableDiscovery(t *testing.T) {
	for _, previousState := range []string{github.ProjectionProjected, github.ProjectionRetryable} {
		t.Run(previousState, func(t *testing.T) { assertCycleReconcilesNewerDiscovery(t, previousState) })
	}
}

func assertCycleReconcilesNewerDiscovery(t *testing.T, previousState string) {
	t.Helper()
	now := time.Now().UTC()
	moduleID := "avm-res-catalog-a"
	snapshot := manualProjectionTestSnapshot(t, moduleID, previousState, now.Add(-2*time.Hour))
	accepted := cycleProjectorTestCatalog(t, now)
	data, err := json.Marshal(discovery.DurableCatalogState{SchemaVersion: 1, Accepted: accepted})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[discoveryCatalogStatePath] = state.Record{Kind: "discovery_catalog", SchemaVersion: 1, Data: data}
	previous := discovery.ProjectionIntent{SchemaVersion: 1, ModuleID: moduleID, DesiredGeneration: 1, Status: discovery.IntentBacklog, UpdatedAt: now.Add(-time.Hour), Source: &source.ResolvedSource{ModuleID: moduleID, Version: "v1.0.0", Commit: strings.Repeat("a", 40)}}
	data, err = json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	existing, _, err := readProjectDeliveryIntent(snapshot, moduleID)
	if err != nil {
		t.Fatal(err)
	}
	existing.Projection, _, err = projectionForDiscoveryIntent(catalog.Module{ID: moduleID, Type: catalog.Resource}, previous, "https://github.com/example/old-run")
	if err != nil {
		t.Fatal(err)
	}
	existing.Fingerprint, err = github.ProjectionFingerprint(existing.Projection)
	if err != nil {
		t.Fatal(err)
	}
	existing.DiscoveryFingerprint, err = identity.ID("discovery-intent", 1, json.RawMessage(data))
	if err != nil {
		t.Fatal(err)
	}
	if previousState == github.ProjectionProjected {
		existing.LastAcknowledgedFingerprint = existing.Fingerprint
	}
	data, err = json.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records["state/projection/delivery/"+moduleID+".json"] = state.Record{Kind: "projection_delivery", SchemaVersion: 1, Data: data}
	current := previous
	current.DesiredGeneration = 2
	current.Source = &source.ResolvedSource{ModuleID: moduleID, Version: "v2.0.0", Commit: strings.Repeat("b", 40)}
	data, err = json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records["state/projection/intents/"+moduleID+".json"] = state.Record{Kind: "projection_intent", SchemaVersion: 1, Data: data}
	wanted, err := identity.ID("discovery-intent", 1, json.RawMessage(data))
	if err != nil {
		t.Fatal(err)
	}
	backend := &workerTestBackend{head: "head-0", snapshot: snapshot}
	projector := &moduleCycleProjector{backend: backend, runURL: "https://github.com/example/retry", runID: "retry", client: github.Client{Token: "fixture", Budget: &github.ProjectionBudget{}}}
	if _, err := projector.deliver(context.Background(), discovery.ModuleCycle{ModuleID: moduleID, Head: backend.head}); err == nil {
		t.Fatal("exhausted fixture API budget falsely acknowledged old delivery")
	}
	updated, _, err := readProjectDeliveryIntent(backend.snapshot, moduleID)
	if err != nil || updated.Projection.SourceSHA != current.Source.Commit || updated.DiscoveryFingerprint != wanted || updated.DeliveryState == github.ProjectionProjected {
		t.Fatalf("retry retained obsolete delivery instead of newer discovery: %+v %v", updated, err)
	}
}

func TestModuleCycleRequiresRunIdentityBeforeOperations(t *testing.T) {
	for _, args := range [][]string{
		{"--discovery-module-cycle", "--module-types", "res"},
		{"--persist-discovery", "candidate", "--discovery-module-cycle", "--module-types", "res"},
		{"--persist-discovery", "candidate", "--discovery-module-cycle", "--discovery-new-modules-only", "--module-types", "res"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("unsafe manual cycle inputs reached operations: %v", args)
		}
	}
}

func TestNewDiscoveryScopeRejectsConflictingControls(t *testing.T) {
	for _, extra := range [][]string{nil, {"--persist-discovery", "candidate", "--module-names", "avm-res-compute-disk"}, {"--persist-discovery", "candidate", "--discover-modules", "avm-res-compute-disk"}} {
		args := append([]string{"--discovery-new-modules-only"}, extra...)
		if err := run(args); err == nil || !strings.Contains(err.Error(), "--discovery-new-modules-only") {
			t.Fatalf("conflicting scope reached operations: %v", err)
		}
	}
}

func TestManualWorkflowInputsAndExecutableTypes(t *testing.T) {
	for _, filename := range []string{"manual-discovery.yml", "manual-projection-recovery.yml"} {
		t.Run(filename, func(t *testing.T) {
			jobName := "persist"
			if filename == "manual-projection-recovery.yml" {
				jobName = "project"
			}
			document, _, steps := providerWorkflowDocumentFor(t, filename, jobName)
			inputs := document["on"].(map[string]any)["workflow_dispatch"].(map[string]any)["inputs"].(map[string]any)
			assertManualStandardInputs(t, inputs)
			assertManualWorkflowTargets(t, filename, steps)
			script := manualScopeWorkflowScript(t, steps)
			for bits := 0; bits < 8; bits++ {
				for _, names := range []string{"", " \t\n ", "avm-res-catalog-a", "avm-res-catalog-a,avm-ptn-catalog-b"} {
					assertManualTypesCase(t, script, bits, names)
				}
			}
		})
	}
}

func assertManualWorkflowTargets(t *testing.T, filename string, steps []any) {
	t.Helper()
	if !workflowRunsContain(steps, "--module-types \"$MODULE_TYPES\"") || !workflowRunsContain(steps, "--module-names \"$MODULE_NAMES\"") || !workflowRunsContain(steps, "--force=\"$FORCE\"") {
		t.Fatal("standard inputs must control the operation, not be inert UI fields")
	}
	if filename == "manual-discovery.yml" && (!workflowRunsContain(steps, "--discovery-module-cycle") || workflowRunsContain(steps, "--dispatch-operation") || workflowRunsContain(steps, "gh workflow run")) {
		t.Fatal("manual discovery must project transaction results and never dispatch workers")
	}
}

func assertManualStandardInputs(t *testing.T, inputs map[string]any) {
	t.Helper()
	if len(inputs) != 5 {
		t.Fatal("module-targeted manual workflows must expose exactly the five standard inputs")
	}
	for _, name := range []string{"res", "ptn", "utl", "force", "module_names"} {
		input, ok := inputs[name].(map[string]any)
		if !ok || input["required"] != false {
			t.Fatalf("missing optional standard input %s", name)
		}
		if name == "module_names" {
			if input["type"] != "string" || input["default"] != "" {
				t.Fatal("module_names must be an empty-default CSV string")
			}
		} else if input["type"] != "boolean" || input["default"] != (name != "force") {
			t.Fatalf("incorrect boolean/default for %s", name)
		}
	}
}

func manualScopeWorkflowScript(t *testing.T, steps []any) string {
	t.Helper()
	for _, value := range steps {
		step := value.(map[string]any)
		if step["id"] == "scope" {
			return step["run"].(string)
		}
	}
	t.Fatal("executable module scope validation is missing")
	return ""
}

func assertManualTypesCase(t *testing.T, script string, bits int, names string) {
	t.Helper()
	outputPath := filepath.Join(t.TempDir(), "output")
	command := exec.Command("bash", "-c", script)
	command.Env = append(os.Environ(), "GITHUB_OUTPUT="+outputPath, "MODULE_NAMES="+names)
	var expected []string
	for position, name := range []string{"res", "ptn", "utl"} {
		enabled := bits&(1<<position) != 0
		if enabled {
			expected = append(expected, name)
		}
		command.Env = append(command.Env, strings.ToUpper(name)+"="+strconv.FormatBool(enabled))
	}
	if strings.TrimSpace(names) != "" {
		expected = nil
	}
	output, err := command.CombinedOutput()
	if bits == 0 && strings.TrimSpace(names) == "" {
		if err == nil {
			t.Fatal("all-false checkboxes broadened the target instead of failing")
		}
		return
	}
	if err != nil {
		t.Fatalf("checkbox shell failed: %v %s", err, output)
	}
	selected, err := os.ReadFile(outputPath)
	if err != nil || string(selected) != "module_types="+strings.Join(expected, ",")+"\n" {
		t.Fatalf("checkbox shell output=%q error=%v, want %v", selected, err, expected)
	}
}

func TestGitHubOperationsInstructionFrontmatter(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "instructions", "github-operations.instructions.md"))
	if err != nil {
		t.Fatal(err)
	}
	sections := strings.SplitN(string(data), "---", 3)
	if len(sections) != 3 || sections[0] != "" {
		t.Fatal("GitHub operations instruction lacks YAML frontmatter")
	}
	var frontmatter map[string]any
	if err = yaml.Unmarshal([]byte(sections[1]), &frontmatter); err != nil {
		t.Fatal(err)
	}
	if frontmatter["description"] == "" || frontmatter["applyTo"] != ".github/workflows/**,cmd/factory/*workflow*test.go" {
		t.Fatal("GitHub operations instruction must have a meaningful description and scoped applyTo")
	}
}

func TestWorkerReceiptWorkflowRecognizesExactHistoricalProducers(t *testing.T) {
	_, _, steps := providerWorkflowDocumentFor(t, "manual-accept-evidence.yml", "import")
	var filter string
	for _, value := range steps {
		script, _ := value.(map[string]any)["run"].(string)
		_, remaining, found := strings.Cut(script, `--argjson attempt "$run_attempt" --arg repository "$GITHUB_REPOSITORY" '`)
		if found {
			filter, _, _ = strings.Cut(remaining, "' work/v2/attempt/run.json")
		}
	}
	if filter == "" {
		t.Fatal("worker receipt provenance filter is missing")
	}
	for _, path := range []string{".github/workflows/manual-worker.yml", ".github/workflows/v2_worker.yml", ".github/workflows/untrusted-manual-worker.yml", "manual-worker.yml"} {
		for _, foreign := range []bool{false, true} {
			repository := "softwareone-public/avm-intelligence"
			if foreign {
				repository = "foreign/repository"
			}
			data, err := json.Marshal(map[string]any{
				"status": "completed", "head_branch": "main", "event": "workflow_dispatch", "path": path,
				"run_attempt": 2, "repository": map[string]any{"full_name": repository},
			})
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command("jq", "-e", "--argjson", "attempt", "2", "--arg", "repository", "softwareone-public/avm-intelligence", filter)
			command.Stdin = strings.NewReader(string(data))
			output, err := command.CombinedOutput()
			valid := !foreign && (path == ".github/workflows/manual-worker.yml" || path == ".github/workflows/v2_worker.yml")
			if (err == nil) != valid {
				t.Fatalf("worker path=%q foreign=%t error=%v output=%s", path, foreign, err, output)
			}
		}
	}
}

func TestRecoveryWorkflowHistoricalLookupAndFailedSearch(t *testing.T) {
	filter, assignment := recoveryWorkflowLookup(t)
	assertRecoveryWorkflowLookup(t, filter, assignment)
}

func recoveryWorkflowLookup(t *testing.T) (string, string) {
	t.Helper()
	_, _, steps := providerWorkflowDocumentFor(t, "manual-recovery.yml", "inspect-and-recover")
	var filter, assignment string
	for _, value := range steps {
		script, _ := value.(map[string]any)["run"].(string)
		for _, line := range strings.Split(script, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), `runs="$(gh api --paginate`) {
				assignment = strings.TrimSpace(line)
				_, remaining, found := strings.Cut(line, `jq -s --arg title "$title" '`)
				if found {
					filter, _, _ = strings.Cut(remaining, "')")
				}
			}
		}
	}
	if filter == "" || assignment == "" || !strings.Contains(assignment, "/actions/runs?") {
		t.Fatal("recovery must search repository runs across both worker filenames")
	}
	return filter, assignment
}

func assertRecoveryWorkflowLookup(t *testing.T, filter, assignment string) {
	t.Helper()
	pages := `{"workflow_runs":[{"id":1,"display_title":"AVM v2 attempt exact","path":".github/workflows/v2_worker.yml"},{"id":2,"display_title":"AVM v2 attempt exact","path":".github/workflows/manual-worker.yml"},{"id":3,"display_title":"AVM v2 attempt exact","path":".github/workflows/untrusted-manual-worker.yml"},{"id":4,"display_title":"other attempt","path":".github/workflows/manual-worker.yml"}]}
{"workflow_runs":[{"id":1,"display_title":"AVM v2 attempt exact","path":".github/workflows/v2_worker.yml"}]}`
	command := exec.Command("jq", "-s", "--arg", "title", "AVM v2 attempt exact", filter)
	command.Stdin = strings.NewReader(pages)
	output, err := command.CombinedOutput()
	var runs []map[string]any
	if err != nil || json.Unmarshal(output, &runs) != nil || len(runs) != 2 || runs[0]["id"] != float64(1) || runs[1]["id"] != float64(2) {
		t.Fatalf("historical recovery lookup failed exact-path filtering/deduplication: %s %v", output, err)
	}
	command = exec.Command("bash", "-c", "set -euo pipefail\ngh() { return 1; }\nGITHUB_REPOSITORY=softwareone-public/avm-intelligence\ntitle='AVM v2 attempt exact'\n"+assignment+"\nprintf 'unsafe-absence'\n")
	output, err = command.CombinedOutput()
	if err == nil || strings.Contains(string(output), "unsafe-absence") {
		t.Fatalf("failed lookup continued toward an absence observation: %s %v", output, err)
	}
}

func TestCatalogIndexEntriesPreserveAcceptedLifecycle(t *testing.T) {
	accepted := catalog.AcceptedState{
		Generation: 1,
		Modules: []catalog.ModuleRecord{
			{Module: catalog.Module{ID: "avm-res-catalog-a", Type: catalog.Resource, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-catalog-a", DisplayName: "Catalog A", Description: "A"}, Active: true, LastSeenGeneration: 1},
			{Module: catalog.Module{ID: "avm-ptn-catalog-b", Type: catalog.Pattern, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-ptn-catalog-b.git", DisplayName: "Catalog B", Description: "B"}, Active: false, MissingObservations: 1, LastSeenGeneration: 1},
		},
	}

	entries, err := catalogIndexEntries(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ModuleID != "avm-ptn-catalog-b" || entries[0].Lifecycle != "retired" || entries[1].ModuleID != "avm-res-catalog-a" || entries[1].Lifecycle != "active" {
		t.Fatalf("catalog entries = %+v", entries)
	}
	if entries[0].Repository != "Azure/terraform-azurerm-avm-ptn-catalog-b" {
		t.Fatalf("normalized repository = %q", entries[0].Repository)
	}
	metadata, err := catalogModuleMetadata(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 2 || metadata[0].ModuleID != "avm-ptn-catalog-b" || metadata[0].ModuleType != "ptn" || metadata[0].Lifecycle != "retired" || metadata[0].MissingObservations != 1 || metadata[1].ModuleID != "avm-res-catalog-a" || metadata[1].ModuleType != "res" || metadata[1].Lifecycle != "active" || metadata[1].DisplayName != "Catalog A" {
		t.Fatalf("catalog metadata = %+v", metadata)
	}
}

func TestManualModuleSelection(t *testing.T) {
	records := []catalog.ModuleRecord{
		{Module: catalog.Module{ID: "avm-res-catalog-a", Type: catalog.Resource}, Active: true},
		{Module: catalog.Module{ID: "avm-ptn-catalog-b", Type: catalog.Pattern}, Active: true},
		{Module: catalog.Module{ID: "avm-utl-catalog-c", Type: catalog.Utility}, Active: false},
	}
	for _, test := range []struct {
		name, types, names, want string
		invalid                  bool
	}{
		{name: "types", types: "res", want: "avm-res-catalog-a"},
		{name: "all active", types: "res,ptn,utl", want: "avm-ptn-catalog-b,avm-res-catalog-a"},
		{name: "CSV overrides", types: "res,ptn,utl", names: " avm-res-catalog-a ", want: "avm-res-catalog-a"},
		{name: "conflicting types ignored", types: "ptn", names: "avm-res-catalog-a", want: "avm-res-catalog-a"},
		{name: "names only", names: "avm-res-catalog-a", want: "avm-res-catalog-a"},
		{name: "bad types ignored", types: "ras", names: "avm-res-catalog-a", want: "avm-res-catalog-a"},
		{name: "mixed types", types: "res", names: "avm-res-catalog-a,avm-ptn-catalog-b", want: "avm-ptn-catalog-b,avm-res-catalog-a"},
		{name: "mixed invalid targets", names: "avm-res-catalog-a,avm-res-missing", invalid: true},
		{name: "whitespace without types", names: " \t\n ", invalid: true},
		{name: "whitespace with types", types: "res", names: " \t\n ", want: "avm-res-catalog-a"},
		{name: "inactive", types: "utl", names: "avm-utl-catalog-c", invalid: true},
		{name: "unknown", types: "res", names: "avm-res-missing", invalid: true},
		{name: "malformed", types: "res", names: "catalog-a", invalid: true},
		{name: "duplicate", types: "res", names: "avm-res-catalog-a,avm-res-catalog-a", invalid: true},
		{name: "empty CSV item", types: "res", names: "avm-res-catalog-a,", invalid: true},
		{name: "no types", invalid: true},
		{name: "bad type", types: "ras", invalid: true},
		{name: "no active matches", types: "utl", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, err := selectManualModules(records, manualModuleScope{types: test.types, names: test.names})
			if (err != nil) != test.invalid || (!test.invalid && strings.Join(selected, ",") != test.want) {
				t.Fatalf("selection=%v error=%v, want %q invalid=%t", selected, err, test.want, test.invalid)
			}
		})
	}
}

// TestNativeManualScopeNamePrecedence exercises early CLI validation and exact selection together.
func TestNativeManualScopeNamePrecedence(t *testing.T) {
	records := []catalog.ModuleRecord{
		{Module: catalog.Module{ID: "avm-res-catalog-a", Type: catalog.Resource}, Active: true},
		{Module: catalog.Module{ID: "avm-ptn-catalog-b", Type: catalog.Pattern}, Active: true},
		{Module: catalog.Module{ID: "avm-res-catalog-unselected", Type: catalog.Resource}, Active: true},
	}
	for _, operation := range []string{"discovery", "projection"} {
		for bits := 0; bits < 8; bits++ {
			t.Run(operation+"/"+strconv.Itoa(bits), func(t *testing.T) {
				assertNativeManualNamesCase(t, records, operation, bits)
			})
		}
	}
}

// assertNativeManualNamesCase checks one operation/category combination without side effects.
func assertNativeManualNamesCase(t *testing.T, records []catalog.ModuleRecord, operation string, bits int) {
	t.Helper()
	flags := flag.NewFlagSet("factory", flag.ContinueOnError)
	flags.String("discover-modules", "", "")
	flags.String("discovery-result-out", "", "")
	flags.String("module-names", "", "")
	if err := flags.Set("module-names", "avm-res-catalog-a,avm-ptn-catalog-b"); err != nil {
		t.Fatal(err)
	}
	var types []string
	for position, kind := range []string{"res", "ptn", "utl"} {
		if bits&(1<<position) != 0 {
			types = append(types, kind)
		}
	}
	persistPath := "candidate.json"
	if operation == "projection" {
		persistPath = ""
	}
	scope, err := nativeManualScope(flags, persistPath, operation == "projection", manualModuleScope{
		types: strings.Join(types, ","), names: flags.Lookup("module-names").Value.String(),
	})
	if err != nil || scope == nil {
		t.Fatalf("explicit list rejected before selection: %v", err)
	}
	selected, err := selectManualModules(records, *scope)
	if err != nil || strings.Join(selected, ",") != "avm-ptn-catalog-b,avm-res-catalog-a" {
		t.Fatalf("checkboxes changed explicit selection: %v, error=%v", selected, err)
	}
}

func TestManualProjectionPreservesExistingDelivery(t *testing.T) {
	for _, deliveryState := range []string{github.ProjectionProjected, github.ProjectionProjecting, github.ProjectionRetryable, github.ProjectionNeedsReview} {
		t.Run(deliveryState, func(t *testing.T) {
			assertManualExistingDelivery(t, deliveryState)
		})
	}
}

func assertManualExistingDelivery(t *testing.T, deliveryState string) {
	t.Helper()
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	moduleID := "avm-res-catalog-a"
	snapshot := manualProjectionTestSnapshot(t, moduleID, deliveryState, now)
	intents, err := manualDiscoveryProjectionIntents(snapshot, catalog.AcceptedState{}, []string{moduleID}, "https://github.com/example/run", now)
	if err != nil || len(intents) != 0 {
		t.Fatalf("existing delivery was replaced: %+v %v", intents, err)
	}
	backend := &workerTestBackend{head: "head-0", snapshot: snapshot}
	head, err := prepareManualDiscoveryDelivery(context.Background(), backend, backend.head, intents, []string{moduleID}, false, now)
	if err != nil || head != "head-0" {
		t.Fatalf("ordinary existing-delivery recovery failed or wrote state: head=%s error=%v", head, err)
	}
	_, err = prepareManualDiscoveryDelivery(context.Background(), backend, head, intents, []string{moduleID}, true, now)
	if err != nil {
		t.Fatalf("forced existing-delivery recovery failed: %v", err)
	}
	updated, _, err := readProjectDeliveryIntent(backend.snapshot, moduleID)
	if err != nil {
		t.Fatal(err)
	}
	wantState := deliveryState
	if deliveryState == github.ProjectionProjected {
		wantState = github.ProjectionPending
	}
	if updated.DeliveryState != wantState {
		t.Fatalf("forced recovery changed protected delivery state: got %s want %s", updated.DeliveryState, wantState)
	}
}

func manualProjectionTestSnapshot(t *testing.T, moduleID, deliveryState string, now time.Time) state.Snapshot {
	t.Helper()
	projection, priority, err := projectionForDiscoveryIntent(catalog.Module{ID: moduleID, Type: catalog.Resource}, discovery.ProjectionIntent{Status: discovery.IntentBacklog}, "https://github.com/example/run")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := github.NewProjectionIntent(projection, 2, priority, now)
	if err != nil {
		t.Fatal(err)
	}
	intent.DeliveryState = deliveryState
	if deliveryState == github.ProjectionProjecting {
		intent.LeaseOwner, intent.BatchID, intent.Fence = "active-owner", "active-batch", 1
		intent.LeaseExpiresAt = now.Add(time.Hour)
	}
	if deliveryState == github.ProjectionRetryable {
		intent.RetryAt = now.Add(time.Hour)
	}
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.NewSnapshot()
	snapshot.Records["state/projection/delivery/"+moduleID+".json"] = state.Record{Kind: "projection_delivery", SchemaVersion: 1, Data: data}
	return snapshot
}

func TestRepositoryIdentityRejectsNonGitHubURLs(t *testing.T) {
	for _, value := range []string{
		"http://github.com/Azure/repo",
		"https://user@github.com/Azure/repo",
		"https://github.com/Azure/repo?fork=true",
		"https://example.com/Azure/repo",
		"https://github.com/Azure/repo/tree/main",
	} {
		if _, err := repositoryIdentity(value); err == nil {
			t.Errorf("repositoryIdentity(%q) accepted an unsafe or noncanonical URL", value)
		}
	}
}

func TestCurrentCatalogOutboxUsesExactCycleReferenceAndRetainsEarlierOutboxes(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	snapshot := state.NewSnapshot()
	var latestID string
	for _, suffix := range []string{"a", "b"} {
		outbox := discovery.CatalogProposal{SchemaVersion: 1, ProposalKind: "catalog", Generation: 1, ObservationID: "discovery-observation-" + strings.Repeat(suffix, 64), ModuleIDs: []string{"avm-res-catalog-a"}, CreatedAt: now}
		proposalID, err := identity.ID("catalog-proposal", 1, outbox)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(outbox)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Records[discoveryExportsPrefix+proposalID+".json"] = state.Record{Kind: "export", SchemaVersion: 1, Data: data}
		latestID = proposalID
	}
	if _, err := currentCatalogOutbox(snapshot, 1); err == nil {
		t.Fatal("legacy ambiguous outboxes stopped failing closed")
	}
	reference := discovery.CatalogProposalReference{SchemaVersion: 1, ProposalKind: discovery.CatalogProposalReferenceKind, Generation: 1, ProposalID: latestID}
	data, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[discovery.CatalogProposalReferencePath] = state.Record{Kind: "export", SchemaVersion: 1, Data: data}
	outbox, err := currentCatalogOutbox(snapshot, 1)
	if err != nil || outbox.ObservationID != "discovery-observation-"+strings.Repeat("b", 64) || len(snapshot.Records) != 3 {
		t.Fatalf("reference did not select exact retained outbox: %+v %v", outbox, err)
	}
	delete(snapshot.Records, discoveryExportsPrefix+latestID+".json")
	if _, err := currentCatalogOutbox(snapshot, 1); err == nil {
		t.Fatal("dangling current-outbox reference was accepted")
	}
}

func TestCurrentCatalogOutboxRequiresAcceptedGeneration(t *testing.T) {
	outbox := discovery.CatalogProposal{
		SchemaVersion: 1, ProposalKind: "catalog", Generation: 1,
		ObservationID: "discovery-observation-" + strings.Repeat("a", 64),
		ModuleIDs:     []string{"avm-res-catalog-a"}, CreatedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
	proposalID, err := identity.ID("catalog-proposal", 1, outbox)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(outbox)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.Snapshot{Records: map[string]state.Record{
		"state/exports/" + proposalID + ".json": {Kind: "export", SchemaVersion: 1, Data: data},
	}}
	if _, err = currentCatalogOutbox(snapshot, 1); err != nil {
		t.Fatalf("current outbox was rejected: %v", err)
	}
	if _, err = currentCatalogOutbox(snapshot, 2); err == nil {
		t.Fatal("outbox for a different accepted generation was selected")
	}
}

func TestBuildCatalogProposalRequiresFactoryStateBranch(t *testing.T) {
	err := run([]string{
		"--build-catalog-proposal",
		"--state-branch", "main",
		"--catalog-proposal-base", strings.Repeat("a", 40),
		"--catalog-proposal-out", filepath.Join(t.TempDir(), "proposal"),
	})
	if err == nil || !strings.Contains(err.Error(), "factory-state") {
		t.Fatalf("wrong state branch error = %v", err)
	}
}

func TestBuildModuleEvidenceProposalRequiresTrustedInputs(t *testing.T) {
	err := run([]string{"--build-module-evidence-proposal"})
	if err == nil || !strings.Contains(err.Error(), "worker launch") {
		t.Fatalf("wrong missing-input error = %v", err)
	}
}

func TestBuildModuleEvidenceProposalUsesDedicatedValidationFlag(t *testing.T) {
	err := run([]string{
		"--build-module-evidence-proposal",
		"--worker-launch-input", "missing-launch.json",
		"--module-evidence-artifact", "missing-evidence.json",
		"--module-evidence-proposal-base", strings.Repeat("a", 40),
		"--module-evidence-proposal-out", filepath.Join(t.TempDir(), "proposal"),
		"--module-evidence-validation-id", "module-evidence-validator.v1",
	})
	if err == nil || !strings.Contains(err.Error(), "read trusted worker launch artifact") {
		t.Fatalf("proposal validation flag was intercepted by worker sealing: %v", err)
	}
}

func TestBuildModuleEvidenceProposalValidatesReplayTimestamp(t *testing.T) {
	err := run([]string{
		"--build-module-evidence-proposal",
		"--worker-launch-input", "missing-launch.json",
		"--module-evidence-artifact", "missing-evidence.json",
		"--module-evidence-proposal-base", strings.Repeat("a", 40),
		"--module-evidence-proposal-out", filepath.Join(t.TempDir(), "proposal"),
		"--module-evidence-validation-id", "module-evidence-validator.v1",
		"--module-evidence-proposal-created-at", "not-a-timestamp",
	})
	if err == nil || !strings.Contains(err.Error(), "parse module evidence proposal timestamp") {
		t.Fatalf("invalid replay timestamp error = %v", err)
	}
}

func TestImportEvidenceReceiptRequiresFactoryStateAndInput(t *testing.T) {
	err := run([]string{"--import-evidence-receipt", "--state-branch", "main", "--evidence-acceptance-input", "receipt.json"})
	if err == nil || !strings.Contains(err.Error(), "factory-state") {
		t.Fatalf("wrong state branch error = %v", err)
	}
	err = run([]string{"--import-evidence-receipt", "--state-branch", "factory-state"})
	if err == nil || !strings.Contains(err.Error(), "evidence-acceptance-input") {
		t.Fatalf("wrong missing input error = %v", err)
	}
}

func TestV2CatalogExportWorkflowIsManualAndReviewOnly(t *testing.T) {
	steps := catalogExportWorkflowSteps(t)
	buildsProposal := workflowRunsContain(steps, "--build-catalog-proposal")
	copiesCompleteData := workflowRunsContain(steps, "cp -R work/v2/catalog-proposal/data/. data/")
	pushesReviewBranch := workflowRunsContain(steps, "git push --no-verify") && workflowRunsContain(steps, "factory/v2-catalog-")
	createsPullRequest := workflowRunsContain(steps, "gh pr create")
	launchesDispatcher := workflowRunsContain(steps, "manual-dispatch.yml")
	if !buildsProposal || !copiesCompleteData || !pushesReviewBranch || createsPullRequest || launchesDispatcher {
		t.Fatalf("catalog workflow must copy the complete proposal and prepare only a review branch: builds=%t copiesData=%t pushes=%t createsPR=%t dispatches=%t", buildsProposal, copiesCompleteData, pushesReviewBranch, createsPullRequest, launchesDispatcher)
	}
}

func TestV2WorkerPreparesOnlySealedEvidenceReviewBranch(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "manual-worker.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse worker workflow: %v", err)
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("worker workflow has no jobs")
	}
	job, ok := jobs["prepare-evidence-proposal"].(map[string]any)
	if !ok || job["environment"] != "data-publisher" {
		t.Fatal("evidence proposal job must use the data-publisher environment")
	}
	needs, ok := job["needs"].([]any)
	if !ok || len(needs) != 2 || needs[0] != "prepare" || needs[1] != "validate-and-seal" {
		t.Fatalf("evidence proposal job must wait for the trusted seal: %#v", job["needs"])
	}
	permissions, ok := job["permissions"].(map[string]any)
	if !ok || len(permissions) != 1 || permissions["contents"] != "write" {
		t.Fatalf("evidence proposal job permissions are unexpected: %#v", job["permissions"])
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("evidence proposal job has no steps")
	}
	buildsProposal := workflowRunsContain(steps, "--build-module-evidence-proposal")
	usesDedicatedValidationFlag := workflowRunsContain(steps, "--module-evidence-validation-id")
	checksDeclaredPaths := workflowRunsContain(steps, "test \"$actual_paths\" = \"$expected_paths\"")
	pushesReviewBranch := workflowRunsContain(steps, "factory/v2-evidence-") && workflowRunsContain(steps, "git push --no-verify")
	createsPullRequest := workflowRunsContain(steps, "gh pr create")
	if !buildsProposal || !usesDedicatedValidationFlag || !checksDeclaredPaths || !pushesReviewBranch || createsPullRequest {
		t.Fatalf("worker workflow must prepare only a proposal-declared review branch: builds=%t validatorFlag=%t checksPaths=%t pushes=%t createsPR=%t", buildsProposal, usesDedicatedValidationFlag, checksDeclaredPaths, pushesReviewBranch, createsPullRequest)
	}
}

func TestV2EvidenceAcceptanceWorkflowVerifiesBeforeStateImport(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "manual-accept-evidence.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse evidence acceptance workflow: %v", err)
	}
	on, ok := document["on"].(map[string]any)
	if !ok || on["workflow_dispatch"] == nil || on["pull_request"] != nil || on["push"] != nil {
		t.Fatal("evidence acceptance must be manually dispatched after merge")
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("evidence acceptance workflow has no jobs")
	}
	job, ok := jobs["import"].(map[string]any)
	if !ok || job["environment"] != "data-publisher" {
		t.Fatal("evidence acceptance must use the data-publisher environment")
	}
	permissions, ok := job["permissions"].(map[string]any)
	if !ok || len(permissions) != 2 || permissions["contents"] != "read" || permissions["actions"] != "read" {
		t.Fatalf("evidence acceptance permissions are unexpected: %#v", job["permissions"])
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("evidence acceptance job has no steps")
	}
	checkoutHasReadCredential := false
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			continue
		}
		uses, ok := step["uses"].(string)
		if !ok || !strings.HasPrefix(uses, "actions/checkout@") {
			continue
		}
		with, ok := step["with"].(map[string]any)
		checkoutHasReadCredential = ok && with["persist-credentials"] == true
	}
	checksMergedAncestry := workflowRunsContain(steps, "git merge-base --is-ancestor")
	checksSuccessfulWorkerJobs := workflowRunsContain(steps, "/actions/runs/$run_id/jobs") && workflowRunsContain(steps, "Compute without state credentials") && workflowRunsContain(steps, "Trusted artifact validation and completion seal")
	requiresWholeRunSuccess := workflowRunsContain(steps, ".conclusion == \"success\" and")
	rebuildsArtifactProposal := workflowRunsContain(steps, "--build-module-evidence-proposal")
	usesDedicatedValidationFlag := workflowRunsContain(steps, "--module-evidence-validation-id")
	usesMergedDatasetTimestamp := workflowRunsContain(steps, ".created_at") && workflowRunsContain(steps, "--module-evidence-proposal-created-at")
	validatesMergedCandidate := workflowRunsContain(steps, "--apply-proposal") && workflowRunsContain(steps, "diff -qr work/v2/rebuilt-data work/v2/merged/data")
	importsReceipt := workflowRunsContain(steps, "--import-evidence-receipt")
	writesRepository := workflowRunsContain(steps, "git push") || workflowRunsContain(steps, "gh pr create")
	if !checkoutHasReadCredential || !checksMergedAncestry || !checksSuccessfulWorkerJobs || requiresWholeRunSuccess || !rebuildsArtifactProposal || !usesDedicatedValidationFlag || !usesMergedDatasetTimestamp || !validatesMergedCandidate || !importsReceipt || writesRepository {
		t.Fatalf("evidence acceptance must fetch merged history with read-only credentials and verify before state import: readCredential=%t ancestry=%t workerJobs=%t wholeRunSuccess=%t rebuild=%t validatorFlag=%t replayTimestamp=%t candidate=%t import=%t repositoryWrite=%t", checkoutHasReadCredential, checksMergedAncestry, checksSuccessfulWorkerJobs, requiresWholeRunSuccess, rebuildsArtifactProposal, usesDedicatedValidationFlag, usesMergedDatasetTimestamp, validatesMergedCandidate, importsReceipt, writesRepository)
	}
}

func TestRetiredWorkflowsAreAbsent(t *testing.T) {
	for _, name := range []string{"module.yml", "project.yml", "fleet-plan.yml", "project-accepted.yml", "project-pages-complete.yml", "publish-pages.yml"} {
		if _, err := os.Stat(filepath.Join("..", "..", ".github", "workflows", name)); !os.IsNotExist(err) {
			t.Fatalf("retired workflow %s remains or cannot be verified absent: %v", name, err)
		}
	}
}

func TestDiscoveryProjectionMapsDurableBacklogIntent(t *testing.T) {
	module := catalog.Module{ID: "avm-res-discovery-test", Type: catalog.Resource, DisplayName: "Discovery test"}
	projection, _, err := projectionForDiscoveryIntent(module, discovery.ProjectionIntent{
		SchemaVersion: 1, ModuleID: module.ID, DesiredGeneration: 2, Status: discovery.IntentBacklog,
	}, "https://github.com/softwareone-public/avm-intelligence/actions/runs/123")
	if err != nil {
		t.Fatal(err)
	}
	if projection.Status != "Backlog" || projection.Outcome != "Unassessed" || projection.ModuleID != module.ID {
		t.Fatalf("discovery projection = %+v, want Backlog/Unassessed for %s", projection, module.ID)
	}
}

func TestV2DiscoveryWorkflowSchedulesAndProjectsOnlyDiscoveryTargets(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "factory-discovery.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse v2 discovery workflow: %v", err)
	}
	on, ok := document["on"].(map[string]any)
	if !ok || on["schedule"] == nil || on["workflow_dispatch"] != nil {
		t.Fatal("factory discovery must remain nightly without manual dispatch")
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("v2 discovery workflow has no jobs")
	}
	job, ok := jobs["persist"].(map[string]any)
	if !ok {
		t.Fatal("v2 discovery workflow has no persist job")
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("v2 discovery job has no steps")
	}
	passesTargets := workflowRunsContain(steps, "--module-types") || workflowRunsContain(steps, "--discover-modules")
	writesResult := workflowRunsContain(steps, "--discovery-result-out work/v2/discovery-result.json")
	projectsTargets := workflowRunsContain(steps, "--project-discovery-modules \"$module_ids\"")
	usesResultTargets := workflowRunsContain(steps, ".projection_module_ids // [] | join(\",\")")
	dispatchesWorker := workflowRunsContain(steps, "manual-worker.yml") || workflowRunsContain(steps, "--dispatch-operation claim")
	if passesTargets || !writesResult || !projectsTargets || !usesResultTargets || dispatchesWorker {
		t.Fatalf("factory discovery must retain full scope and project only transaction results: restricted=%t writesResult=%t projects=%t resultTargets=%t dispatchesWorker=%t", passesTargets, writesResult, projectsTargets, usesResultTargets, dispatchesWorker)
	}
}

func TestV2ProjectionRecoveryWorkflowIsManualAndProjectionOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "manual-projection-recovery.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse v2 projection recovery workflow: %v", err)
	}
	on, ok := document["on"].(map[string]any)
	dispatch, dispatchOK := on["workflow_dispatch"].(map[string]any)
	inputs, inputsOK := dispatch["inputs"].(map[string]any)
	moduleInput, moduleOK := inputs["module_names"].(map[string]any)
	if !ok || !dispatchOK || !inputsOK || !moduleOK || moduleInput["type"] != "string" || moduleInput["required"] != false || on["schedule"] != nil || on["push"] != nil {
		t.Fatalf("projection recovery must support manual module_names and have no automatic trigger: %#v", on)
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("projection recovery workflow has no jobs")
	}
	job, ok := jobs["project"].(map[string]any)
	if !ok || job["environment"] != "data-publisher" {
		t.Fatal("projection recovery must use the data-publisher environment")
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("projection recovery job has no steps")
	}
	projectsSelectedModules := workflowRunsContain(steps, "--project-discovery-selection") && workflowRunsContain(steps, "--module-names \"$MODULE_NAMES\"")
	usesStateToken := workflowRunsContain(steps, "--mint-state-token")
	performsDiscovery := workflowRunsContain(steps, "--discover-catalog") || workflowRunsContain(steps, "--persist-discovery") || workflowRunsContain(steps, "--discover-modules")
	dispatchesWorkers := workflowRunsContain(steps, "manual-worker.yml") || workflowRunsContain(steps, "--dispatch-operation claim")
	if !projectsSelectedModules || !usesStateToken || performsDiscovery || dispatchesWorkers {
		t.Fatalf("recovery must only project selected modules: projects=%t stateToken=%t discovery=%t workerDispatch=%t", projectsSelectedModules, usesStateToken, performsDiscovery, dispatchesWorkers)
	}
}

func TestV2DispatcherRequiresAndPassesExactRequestID(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "manual-dispatch.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse v2 dispatcher workflow: %v", err)
	}
	on, ok := document["on"].(map[string]any)
	dispatch, dispatchOK := on["workflow_dispatch"].(map[string]any)
	inputs, inputsOK := dispatch["inputs"].(map[string]any)
	requestInput, requestOK := inputs["request_id"].(map[string]any)
	if !ok || !dispatchOK || !inputsOK || !requestOK || requestInput["required"] != true || requestInput["type"] != "string" || on["schedule"] != nil || on["push"] != nil {
		t.Fatalf("dispatcher must require a manual exact request_id and have no automatic trigger: %#v", on)
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("v2 dispatcher workflow has no jobs")
	}
	job, ok := jobs["claim-and-dispatch"].(map[string]any)
	if !ok {
		t.Fatal("v2 dispatcher has no claim-and-dispatch job")
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("v2 dispatcher job has no steps")
	}
	requestScopedClaim := false
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			continue
		}
		run, _ := step["run"].(string)
		if strings.Contains(run, "--dispatch-operation claim") && strings.Contains(run, "--request-id \"$REQUEST_ID\"") {
			requestScopedClaim = true
		}
	}
	if !requestScopedClaim {
		t.Fatal("dispatcher must pass the required request ID to the targeted claim operation")
	}
}

func catalogExportWorkflowSteps(t *testing.T) []any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "manual-catalog-export.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse catalog export workflow: %v", err)
	}
	on, ok := document["on"].(map[string]any)
	if !ok {
		t.Fatal("catalog export workflow has no trigger map")
	}
	_, dispatchesManually := on["workflow_dispatch"]
	_, triggersOnPullRequest := on["pull_request"]
	_, triggersOnPush := on["push"]
	if !ok || !dispatchesManually || triggersOnPullRequest || triggersOnPush {
		t.Fatal("catalog export must be manually dispatched and must not run on untrusted PR or push events")
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("catalog export workflow has no jobs")
	}
	job, ok := jobs["export"].(map[string]any)
	if !ok || job["environment"] != "data-publisher" {
		t.Fatal("catalog export must use the data-publisher environment")
	}
	permissions, ok := job["permissions"].(map[string]any)
	if !ok || len(permissions) != 1 || permissions["contents"] != "write" {
		t.Fatalf("catalog export permissions are insufficient or unexpected: %#v", job["permissions"])
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("catalog export workflow has no steps")
	}
	return steps
}

func workflowRunsContain(steps []any, expected string) bool {
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			continue
		}
		run, _ := step["run"].(string)
		if strings.Contains(run, expected) {
			return true
		}
	}
	return false
}
