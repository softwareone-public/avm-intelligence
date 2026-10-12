package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
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
	case strings.HasPrefix(name, "admin-"):
		if len(triggers) == 0 {
			t.Fatal("admin workflows must declare administrative triggers")
		}
	default:
		t.Fatal("workflow must use a factory-, manual- or admin- prefix")
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
	if workflowRunsContain(factory, "--discovery-new-modules-only") || workflowRunsContain(manual, "--discovery-new-modules-only") {
		t.Fatal("nightly release discovery must not be restricted to newly catalogued module IDs")
	}
	if workflowRunsContain(factory, ".projection_module_ids") || workflowRunsContain(factory, "--project-discovery-modules") || workflowRunsContain(factory, "projection-recovery") || workflowRunsContain(factory, "project-discovery-run-") || workflowRunsContain(factory, "--discovery-module-cycle") {
		t.Fatal("factory discovery must persist release intents without projection follow-on work")
	}
	if !workflowRunsContain(manual, "--discovery-module-cycle") || workflowRunsContain(manual, "--project-discovery-modules") {
		t.Fatal("manual discovery must persist singleton cycles without projection follow-on work")
	}
}

func TestManualDiscoveryCycleUsesStateOnlyCompletionAdapter(t *testing.T) {
	module := discovery.ModuleCycle{ModuleID: "avm-res-test", Head: "committed-head"}
	head, err := completeStateOnlyCycleModule(context.Background(), module)
	if err != nil || head != module.Head {
		t.Fatalf("state-only completion = %q, %v", head, err)
	}
	if _, err := completeStateOnlyCycleModule(context.Background(), discovery.ModuleCycle{ModuleID: module.ModuleID}); err == nil {
		t.Fatal("state-only completion accepted a missing committed head")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := completeStateOnlyCycleModule(ctx, module); !errors.Is(err, context.Canceled) {
		t.Fatalf("state-only completion ignored cancellation: %v", err)
	}
}

func TestDiscoveryCycleRequiresPositiveActionsRunIdentity(t *testing.T) {
	for _, test := range []struct {
		name    string
		cycle   bool
		runID   string
		wantErr bool
	}{
		{name: "manual cycle requires run ID", cycle: true, wantErr: true},
		{name: "manual cycle rejects zero", cycle: true, runID: "0", wantErr: true},
		{name: "manual cycle rejects nonnumeric", cycle: true, runID: "run-123", wantErr: true},
		{name: "manual cycle accepts positive run ID", cycle: true, runID: "12345"},
		{name: "other discovery rejects cycle identity", runID: "12345", wantErr: true},
		{name: "other discovery has no identity", wantErr: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateDiscoveryCycleRunIdentity(test.cycle, test.runID)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateDiscoveryCycleRunIdentity(%t, %q) error = %v, wantErr %t", test.cycle, test.runID, err, test.wantErr)
			}
		})
	}
}

func TestValidateDiscoveryControlsAllowsStateOnlyManualSelection(t *testing.T) {
	scope := &manualModuleScope{names: "avm-res-test"}
	if err := validateDiscoveryControls(false, true, "candidate.json", "", scope); err != nil {
		t.Fatalf("manual state-only discovery required projection identity: %v", err)
	}
	if err := validateDiscoveryControls(false, true, "candidate.json", "", nil); err == nil {
		t.Fatal("manual discovery accepted an empty selection")
	}
}

func TestDiscoveryWorkSelectionPreservesExactTargetsAndForce(t *testing.T) {
	candidate := catalog.Snapshot{Modules: []catalog.Module{
		{ID: "avm-res-selected-b", Type: catalog.Resource},
		{ID: "avm-ptn-selected-a", Type: catalog.Pattern},
		{ID: "avm-res-unselected", Type: catalog.Resource},
	}}
	scope := &manualModuleScope{names: "avm-res-selected-b,avm-ptn-selected-a", force: true}
	selected, forced, err := discoveryWorkSelection(candidate, "", scope)
	if err != nil || strings.Join(selected, ",") != "avm-ptn-selected-a,avm-res-selected-b" || strings.Join(forced, ",") != strings.Join(selected, ",") {
		t.Fatalf("exact forced selection changed: selected=%v forced=%v error=%v", selected, forced, err)
	}
	if _, _, err := discoveryWorkSelection(candidate, "avm-res-unselected", scope); err == nil {
		t.Fatal("manual selection accepted an additional forced-ID selector")
	}
}

func TestRetiredStateTransportFlagsAreRejected(t *testing.T) {
	for _, option := range []string{"--state-layout-config", "--state-checkout-tree", "--dispatch-checkout-root", "--dispatch-checkout-tree"} {
		if err := run([]string{"--list-dimensions", option, "fixture"}); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("retired transport option %s was accepted: %v", option, err)
		}
	}
	if err := run([]string{"--list-dimensions", "--state-checkout-root", t.TempDir()}); err == nil || !strings.Contains(err.Error(), "require a state command") {
		t.Fatalf("state checkout option reached a non-state command: %v", err)
	}
}

func TestManualDiscoveryWorkflowUsesVerifiedGitStateCheckout(t *testing.T) {
	_, _, steps := providerWorkflowDocumentFor(t, "manual-discovery.yml", "persist")
	for _, required := range []string{"--state-checkout-root work/state", "--state-branch factory-state", "GITHUB_REPOSITORY"} {
		if !workflowRunsContain(steps, required) {
			t.Fatalf("missing verified state storage boundary: %s", required)
		}
	}
	stateCheckouts := 0
	for _, raw := range steps {
		step := raw.(map[string]any)
		with, ok := step["with"].(map[string]any)
		if !ok || with["path"] != "work/state" {
			continue
		}
		stateCheckouts++
		if with["ref"] != "factory-state" || with["persist-credentials"] != false || with["fetch-depth"] != 1 || with["token"] != "${{ steps.state-token.outputs.token }}" {
			t.Fatal("state checkout credentials, ref or depth are unsafe")
		}
	}
	if stateCheckouts != 1 {
		t.Fatal("manual discovery must have exactly one isolated state checkout")
	}
	if !workflowVerifiesStateOrigin(steps) {
		t.Fatal("manual discovery does not verify the state checkout repository identity")
	}
	_, _, factory := providerWorkflowDocumentFor(t, "factory-discovery.yml", "persist")
	if workflowRunsContain(factory, "--state-layout-config") || workflowRunsContain(factory, "--state-checkout-tree") {
		t.Fatal("autonomous discovery retains a retired state transport option")
	}
}

func TestEveryTrustedStateWorkflowUsesCommonVerifiedCheckout(t *testing.T) {
	workflowNames := []string{
		"factory-discovery.yml", "factory-dispatch.yml", "factory-worker.yml",
		"manual-accept-evidence.yml", "manual-catalog-export.yml", "manual-discovery.yml",
		"manual-dispatch-validation.yml", "manual-dispatch.yml", "manual-evidence-export.yml",
		"manual-operations.yml", "manual-projection-flush.yml", "manual-projection-recovery.yml",
		"manual-recovery.yml", "manual-worker.yml",
	}
	for _, workflowName := range workflowNames {
		t.Run(workflowName, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", workflowName))
			if err != nil {
				t.Fatal(err)
			}
			var workflow map[string]any
			if err := yaml.Unmarshal(data, &workflow); err != nil {
				t.Fatal(err)
			}
			jobs, ok := workflow["jobs"].(map[string]any)
			if !ok {
				t.Fatal("workflow has no jobs")
			}
			stateJobs := 0
			for jobName, rawJob := range jobs {
				job, ok := rawJob.(map[string]any)
				if !ok {
					continue
				}
				steps, _ := job["steps"].([]any)
				usesState := false
				for _, rawStep := range steps {
					step, ok := rawStep.(map[string]any)
					if !ok {
						continue
					}
					run, _ := step["run"].(string)
					if strings.Contains(run, "--state-branch") {
						usesState = true
						if !strings.Contains(run, "--state-checkout-root work/state") {
							t.Fatalf("state command in job %s omits common checkout root", jobName)
						}
					}
				}
				if !usesState {
					continue
				}
				stateJobs++
				if !workflowChecksOutState(steps) || !workflowVerifiesStateOrigin(steps) {
					t.Fatalf("state job %s lacks a pinned separate checkout or origin validation", jobName)
				}
			}
			if stateJobs == 0 {
				t.Fatal("expected at least one state-accessing job")
			}
		})
	}
	for _, retired := range []string{"FACTORY_STATE_TOKEN", "--dispatch-checkout-root", "--dispatch-checkout-tree", "--state-checkout-tree", "--state-layout-config"} {
		if workflowRunsContainAnyWorkflow(workflowNames, retired) {
			t.Fatalf("state workflows retain retired transport alias %q", retired)
		}
	}
}

func workflowRunsContainAnyWorkflow(workflowNames []string, expected string) bool {
	for _, workflowName := range workflowNames {
		data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", workflowName))
		if err != nil || strings.Contains(string(data), expected) {
			return true
		}
	}
	return false
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
			if filename == "manual-discovery.yml" {
				continuation, ok := inputs["continuation_id"].(map[string]any)
				if !ok || continuation["type"] != "string" || continuation["default"] != "" || continuation["required"] != false {
					t.Fatal("manual discovery continuation input is invalid")
				}
				delete(inputs, "continuation_id")
			}
			if filename == "manual-projection-recovery.yml" {
				resolve, ok := inputs["resolve_unknown_effects"].(map[string]any)
				if !ok || resolve["type"] != "boolean" || resolve["default"] != false {
					t.Fatal("projection recovery unknown-effect input is invalid")
				}
				delete(inputs, "resolve_unknown_effects")
			}
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

func TestManualContinuationScopeScriptRejectsConflictingInputs(t *testing.T) {
	_, _, steps := providerWorkflowDocumentFor(t, "manual-discovery.yml", "persist")
	script := manualScopeWorkflowScript(t, steps)
	for _, fixture := range []struct {
		id, names, force string
		valid            bool
	}{
		{"discovery-cycle-" + strings.Repeat("a", 64), "", "false", true},
		{"invalid", "", "false", false},
		{"discovery-cycle-" + strings.Repeat("a", 64), "avm-res-test", "false", false},
		{"discovery-cycle-" + strings.Repeat("a", 64), "", "true", false},
	} {
		output := filepath.Join(t.TempDir(), "output")
		command := exec.Command("bash", "-c", script)
		command.Env = append(os.Environ(), "CONTINUATION_ID="+fixture.id, "MODULE_NAMES="+fixture.names, "FORCE="+fixture.force, "RES=false", "PTN=false", "UTL=false", "GITHUB_OUTPUT="+output)
		_, err := command.CombinedOutput()
		if (err == nil) != fixture.valid {
			t.Fatalf("continuation scope validation mismatch: %+v %v", fixture, err)
		}
	}
	if !workflowRunsContain(steps, "--discovery-paced") || !workflowRunsContain(steps, "--discovery-continuation \"$CONTINUATION_ID\"") {
		t.Fatal("manual continuation inputs are inert")
	}
	_, _, factory := providerWorkflowDocumentFor(t, "factory-discovery.yml", "persist")
	if workflowRunsContain(factory, "--discovery-paced") || workflowRunsContain(factory, "--discovery-continuation") {
		t.Fatal("manual pacing was promoted to autonomous discovery")
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
	command.Env = append(os.Environ(), "GITHUB_OUTPUT="+outputPath, "MODULE_NAMES="+names, "CONTINUATION_ID=", "FORCE=false")
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
	for _, path := range []string{".github/workflows/manual-worker.yml", ".github/workflows/v2_worker.yml", ".github/workflows/manual-worker-validation.yml", ".github/workflows/manual-dispatch-validation.yml", ".github/workflows/untrusted-manual-worker.yml", "manual-worker.yml"} {
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
			valid := !foreign && (path == ".github/workflows/manual-worker.yml" || path == ".github/workflows/v2_worker.yml" || path == ".github/workflows/manual-worker-validation.yml" || path == ".github/workflows/manual-dispatch-validation.yml")
			if (err == nil) != valid {
				t.Fatalf("worker path=%q foreign=%t error=%v output=%s", path, foreign, err, output)
			}
		}
	}
}

func TestRecoveryWorkflowRequiresExactDispatcherRunAttempt(t *testing.T) {
	_, _, steps := providerWorkflowDocumentFor(t, "manual-recovery.yml", "inspect-and-recover")
	var filter string
	for _, value := range steps {
		script, _ := value.(map[string]any)["run"].(string)
		_, remaining, found := strings.Cut(script, `--arg run_id "$dispatcher_run_id" --argjson attempt "$dispatcher_run_attempt" '`)
		if found {
			filter, _, _ = strings.Cut(remaining, `' <<< "$dispatcher_run"`)
		}
	}
	if filter == "" {
		t.Fatal("recovery dispatcher identity filter is missing")
	}
	for _, attempt := range []int{1, 2} {
		for _, status := range []string{"in_progress", "completed"} {
			command := exec.Command("jq", "-e", "--arg", "run_id", "123", "--argjson", "attempt", "1", filter)
			data, marshalErr := json.Marshal(map[string]any{"id": 123, "run_attempt": attempt, "status": status})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			command.Stdin = strings.NewReader(string(data))
			output, err := command.CombinedOutput()
			if (err == nil) != (attempt == 1) {
				t.Fatalf("dispatcher attempt=%d status=%s: %s %v", attempt, status, output, err)
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
	testCoordinator := workerTestCoordinator{backend}
	head, err := prepareManualDiscoveryDelivery(context.Background(), testCoordinator, backend.head, intents, []string{moduleID}, false, now)
	if err != nil || head != "head-0" {
		t.Fatalf("ordinary existing-delivery recovery failed or wrote state: head=%s error=%v", head, err)
	}
	_, err = prepareManualDiscoveryDelivery(context.Background(), testCoordinator, head, intents, []string{moduleID}, true, now)
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
		intent.LeaseOwner, intent.ClaimID, intent.Fence = "active-owner", "active-batch", 1
		intent.LeaseExpiresAt, intent.LeaseDuration = now.Add(time.Hour), time.Hour
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
	if err == nil || !strings.Contains(err.Error(), "state-checkout-root") {
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
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "factory-worker.yml"))
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

func TestEvidenceExportWorkflowRequiresExactProducerAndSeparateDelivery(t *testing.T) {
	document, job, steps := providerWorkflowDocumentFor(t, "manual-evidence-export.yml", "export")
	triggers := document["on"].(map[string]any)
	permissions := job["permissions"].(map[string]any)
	if len(triggers) != 1 || triggers["workflow_dispatch"] == nil || permissions["contents"] != "write" || permissions["actions"] != "read" || len(permissions) != 2 {
		t.Fatal("evidence export must be manual with scoped review-branch/artifact permissions")
	}
	if !workflowRunsContain(steps, "--prepare-evidence-delivery") || workflowRunsContain(steps, "--process-work") || workflowRunsContain(steps, "--seal-worker") || workflowRunsContain(steps, "--import-evidence-receipt") || workflowRunsContain(steps, "--force") {
		t.Fatal("export must deliver persisted evidence without compute, sealing or acceptance")
	}
	if !workflowRunsContain(steps, "--open-evidence-review") || !workflowRunsContain(steps, "--mint-review-token") || workflowRunsContain(steps, "gh pr merge") {
		t.Fatal("export must open a validated review without merging")
	}
	for _, name := range []string{"Inspect exact sealed attempt", "Build only persisted sealed evidence"} {
		found := false
		for _, value := range steps {
			step := value.(map[string]any)
			if step["name"] != name {
				continue
			}
			found = true
			env, ok := step["env"].(map[string]any)
			if !ok || env["AVM_WRITER_APP_ID"] != "${{ secrets.AVM_WRITER_APP_ID }}" || env["AVM_WRITER_PRIVATE_KEY"] != "${{ secrets.AVM_WRITER_PRIVATE_KEY }}" {
				t.Fatalf("%s must receive App credentials for its trusted factory-state operation", name)
			}
		}
		if !found {
			t.Fatalf("evidence export is missing trusted state step %q", name)
		}
	}
	if jobEnv, ok := job["env"].(map[string]any); ok {
		if jobEnv["AVM_WRITER_APP_ID"] != nil || jobEnv["AVM_WRITER_PRIVATE_KEY"] != nil {
			t.Fatal("evidence export must keep App credentials scoped to trusted mint/state/review steps")
		}
	}
	artifactDownloads := 0
	for _, value := range steps {
		step := value.(map[string]any)
		uses, _ := step["uses"].(string)
		if strings.HasPrefix(uses, "actions/download-artifact@") {
			with := step["with"].(map[string]any)
			if with["artifact-ids"] == nil || with["name"] != nil || with["merge-multiple"] != true {
				t.Fatal("export must consume verified immutable artifact IDs")
			}
			artifactDownloads++
		}
	}
	if artifactDownloads != 2 {
		t.Fatal("export must download exactly two verified artifact objects")
	}
	var filter string
	for _, value := range steps {
		script, _ := value.(map[string]any)["run"].(string)
		_, remaining, found := strings.Cut(script, `--arg repository "$GITHUB_REPOSITORY" --arg run "$PRODUCER_RUN" --argjson attempt "$PRODUCER_ATTEMPT" '`)
		if found {
			filter, _, _ = strings.Cut(remaining, "' work/v2/attempt/run.json")
		}
	}
	if filter == "" {
		t.Fatal("exact producer filter missing")
	}
	for _, test := range []struct {
		path    string
		attempt int
		valid   bool
	}{{".github/workflows/manual-worker.yml", 1, true}, {".github/workflows/manual-dispatch-validation.yml", 1, true}, {".github/workflows/manual-worker.yml", 2, false}, {".github/workflows/untrusted.yml", 1, false}} {
		data, err := json.Marshal(map[string]any{"id": 123, "repository": map[string]string{"full_name": "owner/repository"}, "status": "completed", "event": "workflow_dispatch", "head_branch": "main", "head_sha": strings.Repeat("a", 40), "run_attempt": test.attempt, "path": test.path, "conclusion": "failure"})
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command("jq", "-e", "--arg", "repository", "owner/repository", "--arg", "run", "123", "--argjson", "attempt", "1", filter)
		command.Stdin = strings.NewReader(string(data))
		output, err := command.CombinedOutput()
		if (err == nil) != test.valid {
			t.Fatalf("producer=%s attempt=%d: %s %v", test.path, test.attempt, output, err)
		}
	}
}

func TestProjectionFlushSelectsOnlyDueExistingDelivery(t *testing.T) {
	for _, ids := range []string{"", "avm-invalid", "avm-res-valid,avm-res-valid", "avm-res-valid,invalid", "avm-res-valid,"} {
		if _, err := exactFlushModuleIDs(ids); err == nil {
			t.Fatalf("invalid flush IDs passed: %q", ids)
		}
	}
	now := time.Now().UTC()
	moduleID := "avm-res-catalog-a"
	for _, stage := range []string{github.ProjectionPending, github.ProjectionProjected, github.ProjectionNeedsReview, github.ProjectionRetryable} {
		snapshot := manualProjectionTestSnapshot(t, moduleID, stage, now)
		due, err := dueProjectionModules(snapshot, []string{moduleID, "avm-res-missing"}, now)
		if err != nil {
			t.Fatal(err)
		}
		if (len(due) == 1) != (stage == github.ProjectionPending) {
			t.Fatalf("stage=%s due=%v", stage, due)
		}
	}
	retrySnapshot := manualProjectionTestSnapshot(t, moduleID, github.ProjectionRetryable, now)
	deadline := now.Add(time.Hour)
	dueAtDeadline, err := dueProjectionModules(retrySnapshot, []string{moduleID}, deadline)
	if err != nil || len(dueAtDeadline) != 1 || dueAtDeadline[0] != moduleID {
		t.Fatalf("manual flush did not preserve the exact retry deadline for the next scheduled tick: due=%v error=%v", dueAtDeadline, err)
	}
	if err := run([]string{"--request-projection-flush", moduleID, "--discover-catalog"}); err == nil {
		t.Fatal("projection flush request accepted discovery mode")
	}
}

func TestProjectionReadbackRequiresExactBoundedModuleSet(t *testing.T) {
	if _, err := exactReadbackModuleIDs("avm-res-one,avm-res-two"); err != nil {
		t.Fatalf("bounded exact selection rejected: %v", err)
	}
	ids := make([]string, 11)
	for i := range ids {
		ids[i] = fmt.Sprintf("avm-res-module-%02d", i)
	}
	if _, err := exactReadbackModuleIDs(strings.Join(ids, ",")); err == nil {
		t.Fatal("readback accepted more than ten exact modules")
	}
}

func TestProjectionFlushWorkflowNeverDiscoversOrDispatches(t *testing.T) {
	document, job, steps := providerWorkflowDocumentFor(t, "manual-projection-flush.yml", "project")
	assertProviderWorkflowScriptsParse(t, steps)
	triggers := document["on"].(map[string]any)
	if len(triggers) != 1 || triggers["workflow_dispatch"] == nil {
		t.Fatal("projection flush must be manual only")
	}
	permissions := job["permissions"].(map[string]any)
	if len(permissions) != 1 || permissions["contents"] != "read" || job["environment"] != "data-publisher" {
		t.Fatal("projection flush permissions/environment changed")
	}
	if !workflowRunsContain(steps, `--request-projection-flush "$MODULE_NAMES"`) || workflowRunsContain(steps, "--persist-discovery") || workflowRunsContain(steps, "--dispatch-operation") || workflowRunsContain(steps, "--force") || workflowRunsContain(steps, "--mint-projection-token") {
		t.Fatal("projection flush must only request scheduled delivery of existing due work")
	}
	for _, raw := range steps {
		step := raw.(map[string]any)
		if step["name"] == "Flush only due existing delivery" {
			env, _ := step["env"].(map[string]any)
			if len(env) != 1 || env["MODULE_NAMES"] != "${{ inputs.module_names }}" {
				t.Fatalf("projection flush step must receive only its selected module names: %#v", env)
			}
		}
	}
}

func TestLifecycleProjectionBuilderRetainsAuthorityBinding(t *testing.T) {
	now := time.Now().UTC()
	module := catalog.Module{ID: "avm-res-lifecycle-builder", Type: catalog.Resource}
	request, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{ModuleID: module.ID, SourceSHA: strings.Repeat("a", 40), DesiredGeneration: 1, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.NewSnapshot()
	put := func(path, kind string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Records[path] = state.Record{Kind: kind, SchemaVersion: 1, Data: data}
	}
	put("state/requests/"+request.ID+".json", "request", request)
	put("state/projection/intents/"+module.ID+".json", "projection_intent", discovery.ProjectionIntent{SchemaVersion: 1, ModuleID: module.ID, DesiredGeneration: 1, Status: discovery.IntentBacklog, Source: &source.ResolvedSource{ModuleID: module.ID, Commit: request.SourceSHA, Version: "v1.0.0"}, UpdatedAt: now})
	events, err := queue.AcquisitionLifecycleEvents(snapshot, request, nil, now)
	if err != nil || len(events) != 1 {
		t.Fatalf("prepare lifecycle fixture: %v %v", events, err)
	}
	snapshot.Records[events[0].RecordPath] = events[0].Record
	intent, err := buildDiscoveryProjectionIntent(snapshot, map[string]catalog.ModuleRecord{module.ID: {Module: module, Active: true}}, module.ID, "https://github.com/example/run", now)
	if err != nil || intent.Projection.Status != queue.ModuleStatusBacklog || intent.LifecycleFingerprint == "" || intent.DiscoveryFingerprint == "" {
		t.Fatalf("lifecycle projection lost current authority: %+v %v", intent, err)
	}
}

func TestNeutralProjectionBuilderBindsExactModuleIntent(t *testing.T) {
	module := catalog.Module{ID: "avm-res-neutral-builder", Type: catalog.Resource}
	now := time.Now().UTC()
	neutral, err := projection.NewDesiredIntent(module.ID, 4, "selection:current-release", "publishing", []projection.OwnedField{
		{Name: "avm_release_version", Value: "v2.0.0"},
		{Name: "source_sha", Value: strings.Repeat("b", 40)},
		{Name: "source_version", Value: "v2.0.0"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(neutral)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.NewSnapshot()
	path, err := identity.StateProjectionIntentPath(module.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[path] = state.Record{Kind: "projection_intent", SchemaVersion: projection.SchemaVersion, Data: data}
	delivery, err := buildDiscoveryProjectionIntent(snapshot, map[string]catalog.ModuleRecord{module.ID: {Module: module, Active: true}}, module.ID, "https://github.com/example/run", now)
	if err != nil {
		t.Fatal(err)
	}
	identityTuple, err := neutral.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Projection.Status != github.StatusPublishing || delivery.Projection.Version != "v2.0.0" || delivery.Projection.SourceSHA != strings.Repeat("b", 40) || delivery.NeutralProjection == nil || *delivery.NeutralProjection != identityTuple {
		t.Fatalf("delivery did not preserve the current neutral module projection: %+v", delivery)
	}
	effectID, err := identity.ID("projection-effect", projection.SchemaVersion, map[string]string{"effect": "comment"})
	if err != nil {
		t.Fatal(err)
	}
	effectFingerprint, err := identity.ID("effect-fingerprint", projection.SchemaVersion, map[string]string{"content": "required"})
	if err != nil {
		t.Fatal(err)
	}
	withEffect, err := projection.NewDesiredIntent(module.ID, 5, "selection:effectful-release", "publishing", []projection.OwnedField{
		{Name: "avm_release_version", Value: "v2.0.0"},
		{Name: "source_sha", Value: strings.Repeat("b", 40)},
		{Name: "source_version", Value: "v2.0.0"},
	}, []projection.EvidenceEffect{{ID: effectID, Kind: "comment", Fingerprint: effectFingerprint}})
	if err != nil {
		t.Fatal(err)
	}
	withEffectData, err := json.Marshal(withEffect)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[path] = state.Record{Kind: "projection_intent", SchemaVersion: projection.SchemaVersion, Data: withEffectData}
	if _, err = buildDiscoveryProjectionIntent(snapshot, map[string]catalog.ModuleRecord{module.ID: {Module: module, Active: true}}, module.ID, "https://github.com/example/run", now); err == nil {
		t.Fatal("delivery silently ignored a required neutral evidence effect")
	}
}

func TestScheduledProjectorMaterializesNeutralIntentsOnce(t *testing.T) {
	module := catalog.Module{ID: "avm-res-neutral-scheduled", Type: catalog.Resource}
	now := time.Now().UTC()
	desired, err := projection.NewDesiredIntent(module.ID, 1, "dispatch:request-1", "queued", []projection.OwnedField{
		{Name: "avm_release_version", Value: "v1.0.0"},
		{Name: "source_sha", Value: strings.Repeat("c", 40)},
		{Name: "source_version", Value: "v1.0.0"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.NewSnapshot()
	path, err := identity.StateProjectionIntentPath(module.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[path] = state.Record{Kind: "projection_intent", SchemaVersion: projection.SchemaVersion, Data: data}
	backend := &workerTestBackend{head: "head-0", snapshot: snapshot}
	coordinator := workerTestCoordinator{backend}
	accepted := catalog.AcceptedState{Modules: []catalog.ModuleRecord{{Module: module, Active: true}}}
	if _, err = materializeNeutralProjectionDeliveries(context.Background(), coordinator, backend.head, snapshot, accepted, "https://github.com/example/run/1", now); err != nil {
		t.Fatal(err)
	}
	_, current, err := coordinator.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	due, err := github.DueProjectionModuleIDs(current, now)
	if err != nil || len(due) != 1 || due[0] != module.ID {
		t.Fatalf("neutral producer intent did not become scheduled due work: due=%v err=%v", due, err)
	}
	firstHead := backend.head
	if _, err = materializeNeutralProjectionDeliveries(context.Background(), coordinator, backend.head, current, accepted, "https://github.com/example/run/2", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if backend.head != firstHead {
		t.Fatalf("unchanged neutral intent was rewritten on the next schedule: head=%s want=%s", backend.head, firstHead)
	}
}

func TestScheduledProjectorMaterializesNeighborsWhenOneNeutralIntentHasUnsupportedEffects(t *testing.T) {
	blocked := catalog.Module{ID: "avm-res-neutral-effect-blocked", Type: catalog.Resource}
	ready := catalog.Module{ID: "avm-res-neutral-effect-ready", Type: catalog.Resource}
	now := time.Now().UTC()
	snapshot := state.NewSnapshot()
	for index, module := range []catalog.Module{blocked, ready} {
		fields := []projection.OwnedField{{Name: "avm_release_version", Value: "v1.0.0"}, {Name: "source_sha", Value: strings.Repeat("d", 40)}, {Name: "source_version", Value: "v1.0.0"}}
		effects := []projection.EvidenceEffect(nil)
		if index == 0 {
			effectID, err := identity.ID("projection-effect", projection.SchemaVersion, map[string]string{"effect": "comment"})
			if err != nil {
				t.Fatal(err)
			}
			effectFingerprint, err := identity.ID("effect-fingerprint", projection.SchemaVersion, map[string]string{"content": "required"})
			if err != nil {
				t.Fatal(err)
			}
			effects = []projection.EvidenceEffect{{ID: effectID, Kind: "comment", Fingerprint: effectFingerprint}}
		}
		desired, err := projection.NewDesiredIntent(module.ID, 1, "dispatch:"+module.ID, "queued", fields, effects)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(desired)
		if err != nil {
			t.Fatal(err)
		}
		path, err := identity.StateProjectionIntentPath(module.ID)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Records[path] = state.Record{Kind: "projection_intent", SchemaVersion: projection.SchemaVersion, Data: data}
	}
	backend := &workerTestBackend{head: "head-0", snapshot: snapshot}
	coordinator := workerTestCoordinator{backend}
	accepted := catalog.AcceptedState{Modules: []catalog.ModuleRecord{{Module: blocked, Active: true}, {Module: ready, Active: true}}}
	if _, err := materializeNeutralProjectionDeliveries(context.Background(), coordinator, backend.head, snapshot, accepted, "https://github.com/example/run/1", now); err != nil {
		t.Fatal(err)
	}
	_, current, err := coordinator.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := readProjectDeliveryIntent(current, blocked.ID); err != nil || exists {
		t.Fatalf("unsupported effect unexpectedly created a delivery: exists=%t err=%v", exists, err)
	}
	readyIntent, exists, err := readProjectDeliveryIntent(current, ready.ID)
	if err != nil || !exists || readyIntent.DeliveryState != github.ProjectionPending {
		t.Fatalf("valid neighbor was not materialized: intent=%+v exists=%t err=%v", readyIntent, exists, err)
	}
	due, err := github.DueProjectionModuleIDs(current, now)
	if err != nil || len(due) != 1 || due[0] != ready.ID {
		t.Fatalf("due neighbors=%v err=%v, want only %s", due, err, ready.ID)
	}
}

func TestScheduledProjectorCatchesUpToLatestNeutralStatusAfterOutage(t *testing.T) {
	module := catalog.Module{ID: "avm-res-neutral-latest-status", Type: catalog.Resource}
	now := time.Now().UTC()
	latest, err := projection.NewDesiredIntent(module.ID, 3, "worker:latest", "needs-review", []projection.OwnedField{{Name: "avm_release_version", Value: "v1.0.0"}, {Name: "phase", Value: "assessment"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.NewSnapshot()
	path, err := identity.StateProjectionIntentPath(module.ID)
	if err != nil {
		t.Fatal(err)
	}
	latestData, err := json.Marshal(latest)
	if err != nil {
		t.Fatal(err)
	}
	// Model a projector outage: the canonical module intent already contains
	// the newest status and no delivery record was created for an older status.
	snapshot.Records[path] = state.Record{Kind: "projection_intent", SchemaVersion: projection.SchemaVersion, Data: latestData}
	backend := &workerTestBackend{head: "head-0", snapshot: snapshot}
	coordinator := workerTestCoordinator{backend}
	accepted := catalog.AcceptedState{Modules: []catalog.ModuleRecord{{Module: module, Active: true}}}
	if _, err = materializeNeutralProjectionDeliveries(context.Background(), coordinator, backend.head, snapshot, accepted, "https://github.com/example/run/1", now); err != nil {
		t.Fatal(err)
	}
	_, current, err := coordinator.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	delivery, exists, err := readProjectDeliveryIntent(current, module.ID)
	if err != nil || !exists || delivery.Projection.Status != queue.ModuleStatusNeedsReview || delivery.NeutralProjection == nil {
		t.Fatalf("projector did not catch up to the latest status: delivery=%+v exists=%t err=%v", delivery, exists, err)
	}
	latestIdentity, err := latest.Identity()
	if err != nil || *delivery.NeutralProjection != latestIdentity {
		t.Fatalf("delivery neutral identity=%+v, want latest=%+v err=%v", delivery.NeutralProjection, latestIdentity, err)
	}
}

func TestNeutralProjectionUsesHeldReleaseOverNewerQueuedNeutralStatus(t *testing.T) {
	module := catalog.Module{ID: "avm-res-neutral-held-release", Type: catalog.Resource}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	active, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: module.ID, SourceSHA: strings.Repeat("a", 40), AVMReleaseVersion: "1.0.0",
		DesiredGeneration: 1, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	active.Status, active.OwnerRunID = queue.StatusRunning, "worker-active"
	active.Work.AttemptID, active.Work.Fence = "attempt-active", 1
	active.Work.Lease = &queue.ComputeLease{Owner: active.OwnerRunID, Fence: active.Work.Fence, ExpiresAt: now.Add(time.Hour)}
	if err = active.Validate(); err != nil {
		t.Fatalf("validate active source request fixture: %v", err)
	}
	queued, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: module.ID, SourceSHA: strings.Repeat("b", 40), AVMReleaseVersion: "2.0.0",
		DesiredGeneration: 2, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	snapshot := state.NewSnapshot()
	put := func(path, kind string, value any) {
		t.Helper()
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		snapshot.Records[path] = state.Record{Kind: kind, SchemaVersion: 1, Data: data}
	}
	put("state/requests/"+active.ID+".json", "request", active)
	put("state/requests/"+queued.ID+".json", "request", queued)
	put("state/lifecycle/slots/"+module.ID+".json", "lifecycle_slot", queue.LifecycleSlot{
		SchemaVersion: 1, ModuleID: module.ID, ReleaseVersion: "1.0.0", DesiredGeneration: 1,
		RequestID: active.ID, AcquiredAt: now, Status: "active",
	})
	neutral, err := projection.NewDesiredIntent(module.ID, 3, "discovery:newer-release", "backlog", []projection.OwnedField{
		{Name: "avm_release_version", Value: "v2.0.0"},
		{Name: "source_version", Value: "v2.0.0"},
		{Name: "source_sha", Value: strings.Repeat("b", 40)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	delivery, err := buildNeutralProjectionDelivery(snapshot, module, neutral, "https://github.com/example/run/3", now)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Projection.Status != queue.ModuleStatusAnalyzing || delivery.Projection.Version != "1.0.0" || delivery.Projection.SourceSHA != strings.Repeat("a", 40) {
		t.Fatalf("newer neutral status displaced held release: projection=%+v", delivery.Projection)
	}
	if delivery.Priority != github.ProjectionPriorityActive {
		t.Fatalf("held release projection priority=%q, want active", delivery.Priority)
	}
	identityTuple, err := neutral.Identity()
	if err != nil || delivery.NeutralProjection == nil || *delivery.NeutralProjection != identityTuple {
		t.Fatalf("delivery lost neutral producer identity: got=%+v want=%+v err=%v", delivery.NeutralProjection, identityTuple, err)
	}
}

func TestNeutralProjectionUsesOldestQueuedReleaseWhenSlotIsFree(t *testing.T) {
	module := catalog.Module{ID: "avm-res-neutral-queued-order", Type: catalog.Resource}
	now := time.Date(2026, 10, 11, 13, 0, 0, 0, time.UTC)
	older, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: module.ID, SourceSHA: strings.Repeat("a", 40), AVMReleaseVersion: "1.2.0",
		DesiredGeneration: 1, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	newer, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: module.ID, SourceSHA: strings.Repeat("b", 40), AVMReleaseVersion: "1.10.0",
		DesiredGeneration: 2, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	neutral, err := projection.NewDesiredIntent(module.ID, 3, "discovery:newer-release", "backlog", []projection.OwnedField{
		{Name: "avm_release_version", Value: "1.10.0"},
		{Name: "source_version", Value: "1.10.0"},
		{Name: "source_sha", Value: newer.SourceSHA},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.NewSnapshot()
	put := func(path, kind string, value any) {
		t.Helper()
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		snapshot.Records[path] = state.Record{Kind: kind, SchemaVersion: 1, Data: data}
	}
	put("state/requests/"+older.ID+".json", "request", older)
	put("state/requests/"+newer.ID+".json", "request", newer)
	delivery, err := buildNeutralProjectionDelivery(snapshot, module, neutral, "https://github.com/example/run/1", now)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Projection.Status != queue.ModuleStatusBacklog || delivery.Projection.Version != "1.2.0" || delivery.Projection.SourceSHA != older.SourceSHA {
		t.Fatalf("newer neutral intent displaced the oldest queued release: %+v", delivery.Projection)
	}
	identityTuple, err := neutral.Identity()
	if err != nil || delivery.NeutralProjection == nil || *delivery.NeutralProjection != identityTuple {
		t.Fatalf("delivery lost newest neutral producer identity: got=%+v want=%+v err=%v", delivery.NeutralProjection, identityTuple, err)
	}
}

func TestNeutralMaterializationReopensWhenActiveSlotStateAdvances(t *testing.T) {
	module := catalog.Module{ID: "avm-res-neutral-active-advance", Type: catalog.Resource}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	request, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: module.ID, SourceSHA: strings.Repeat("a", 40), AVMReleaseVersion: "1.0.0",
		DesiredGeneration: 1, ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	neutral, err := projection.NewDesiredIntent(module.ID, 1, "dispatch:held-release", "backlog", []projection.OwnedField{
		{Name: "avm_release_version", Value: "1.0.0"},
		{Name: "source_version", Value: "1.0.0"},
		{Name: "source_sha", Value: strings.Repeat("a", 40)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.NewSnapshot()
	put := func(path, kind string, value any) {
		t.Helper()
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		snapshot.Records[path] = state.Record{Kind: kind, SchemaVersion: 1, Data: data}
	}
	put("state/requests/"+request.ID+".json", "request", request)
	slotPath, err := queue.LifecycleSlotPath(module.ID)
	if err != nil {
		t.Fatal(err)
	}
	put(slotPath, "lifecycle_slot", queue.LifecycleSlot{
		SchemaVersion: 1, ModuleID: module.ID, ReleaseVersion: "1.0.0", DesiredGeneration: 1,
		RequestID: request.ID, AcquiredAt: now, Status: "active",
	})
	neutralPath, err := identity.StateProjectionIntentPath(module.ID)
	if err != nil {
		t.Fatal(err)
	}
	put(neutralPath, "projection_intent", neutral)
	staleDelivery, err := buildNeutralProjectionDelivery(snapshot, module, neutral, "https://github.com/example/run/1", now)
	if err != nil || staleDelivery.Projection.Status != queue.ModuleStatusBacklog {
		t.Fatalf("initial Backlog delivery = %+v err=%v", staleDelivery, err)
	}
	// A rematerialization must update desired projection data without releasing
	// a delivery parked because its prior remote effect is unknown.
	staleDelivery.DeliveryState = github.ProjectionNeedsReview
	staleDelivery.LastErrorClass = github.ProjectionErrorNeedsReview
	staleDelivery.RetryReason = "owner_terminated_effect_unknown"
	staleDelivery.UnknownEffect = &github.UnknownProjectionEffect{
		Owner: github.ProjectionOwner{ModuleID: module.ID, ClaimID: "claim", Fence: 1, RunID: 1, RunAttempt: 1, LeaseExpiresAt: now.Add(-time.Minute)},
		Observation: github.ProjectionOwnerObservation{Repository: "softwareone-public/avm-intelligence", WorkflowPath: ".github/workflows/factory-projection.yml", HeadBranch: "main",
			RunID: 1, RunAttempt: 1, Status: "completed", Conclusion: "failure", CurrentRunAttempt: 1, CurrentRunStatus: "completed"},
		Generation: staleDelivery.Generation, Fingerprint: staleDelivery.Fingerprint, Projection: staleDelivery.Projection, NeutralProjection: staleDelivery.NeutralProjection,
	}
	put("state/projection/delivery/"+module.ID+".json", "projection_delivery", staleDelivery)

	activeRequest := request
	activeRequest.Status, activeRequest.OwnerRunID = queue.StatusRunning, "worker-active"
	activeWork := *request.Work
	activeWork.AttemptID, activeWork.Fence = "attempt-active", 1
	activeWork.Lease = &queue.ComputeLease{Owner: activeRequest.OwnerRunID, Fence: 1, ExpiresAt: now.Add(time.Hour)}
	activeRequest.Work = &activeWork
	if err = activeRequest.Validate(); err != nil {
		t.Fatalf("validate advanced running request fixture: %v", err)
	}
	put("state/requests/"+request.ID+".json", "request", activeRequest)
	needed, err := neutralProjectionMaterializationNeeded(snapshot)
	if err != nil || !needed {
		t.Fatalf("advanced active slot did not reopen materialization: needed=%t err=%v", needed, err)
	}
	backend := &workerTestBackend{head: "head-0", snapshot: snapshot}
	coordinator := workerTestCoordinator{backend}
	accepted := catalog.AcceptedState{Modules: []catalog.ModuleRecord{{Module: module, Active: true}}}
	if _, err = materializeNeutralProjectionDeliveries(context.Background(), coordinator, backend.head, snapshot, accepted, "https://github.com/example/run/2", now.Add(time.Minute)); err != nil {
		t.Fatalf("materialize advanced active slot: %v", err)
	}
	_, current, err := coordinator.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	freshDelivery, exists, err := readProjectDeliveryIntent(current, module.ID)
	if err != nil || !exists {
		t.Fatalf("read rematerialized active delivery: delivery=%+v exists=%t err=%v", freshDelivery, exists, err)
	}
	if freshDelivery.Projection.Status != queue.ModuleStatusAnalyzing || freshDelivery.Projection.Version != "1.0.0" || freshDelivery.Projection.SourceSHA != strings.Repeat("a", 40) {
		t.Fatalf("persisted active-slot reduction = %+v, want held release Analyzing", freshDelivery.Projection)
	}
	identityTuple, err := neutral.Identity()
	if err != nil || freshDelivery.NeutralProjection == nil || *freshDelivery.NeutralProjection != identityTuple {
		t.Fatalf("rematerialization lost neutral identity: got=%+v want=%+v err=%v", freshDelivery.NeutralProjection, identityTuple, err)
	}
	if freshDelivery.DeliveryState != github.ProjectionNeedsReview || freshDelivery.RetryReason != "owner_terminated_effect_unknown" || freshDelivery.LastErrorClass != github.ProjectionErrorNeedsReview {
		t.Fatalf("rematerialization released unknown-owner hold: %+v", freshDelivery)
	}
}

func TestNeutralProjectionBuilderDefersRetirementWithoutReceiptProof(t *testing.T) {
	module := catalog.Module{ID: "avm-res-neutral-retired", Type: catalog.Resource}
	now := time.Now().UTC()
	desired, err := projection.NewDesiredIntent(module.ID, 2, "catalog:retired", "retired", []projection.OwnedField{
		{Name: "source_version", Value: "v1.0.0"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := state.NewSnapshot()
	path, err := identity.StateProjectionIntentPath(module.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[path] = state.Record{Kind: "projection_intent", SchemaVersion: projection.SchemaVersion, Data: data}
	delivery, err := buildDiscoveryProjectionIntent(snapshot, map[string]catalog.ModuleRecord{
		module.ID: {Module: module, Active: false},
	}, module.ID, "https://github.com/example/run", now)
	if err == nil || delivery.Projection.Status == github.StatusRetired {
		t.Fatalf("catalog retirement without exact Pages and accepted-facts proof was projected: delivery=%+v", delivery)
	}
}

func TestLifecycleProjectionMapsActiveWorkAndValidation(t *testing.T) {
	module := catalog.Module{ID: "avm-res-status-project", Type: catalog.Resource}
	for _, stage := range []string{queue.ModuleStatusQueued, queue.ModuleStatusAnalyzing, queue.ModuleStatusValidating} {
		projection, priority, err := projectionForDiscoveryIntent(module, discovery.ProjectionIntent{Status: stage}, "https://github.com/example/run")
		expected := stage
		if stage == queue.ModuleStatusValidating {
			expected = queue.ModuleStatusAnalyzing
		}
		if err != nil || projection.Status != expected || priority != github.ProjectionPriorityActive {
			t.Fatalf("stage %s projection = %+v priority=%d error=%v", stage, projection, priority, err)
		}
	}
	for _, stage := range []string{queue.ModuleStatusBacklog, queue.ModuleStatusBlocked, queue.ModuleStatusNeedsReview, queue.ModuleStatusPublishing, queue.ModuleStatusFailed} {
		projection, _, err := projectionForDiscoveryIntent(module, discovery.ProjectionIntent{Status: stage}, "https://github.com/example/run")
		if err != nil || projection.Status != stage {
			t.Fatalf("stage %s projection = %+v, %v", stage, projection, err)
		}
	}
	if _, _, err := projectionForDiscoveryIntent(module, discovery.ProjectionIntent{Status: queue.ModuleStatusDone}, "https://github.com/example/run"); err == nil {
		t.Fatal("unverified lifecycle intent projected Done")
	}
}

func TestValidationDispatcherWorkflowCallsExactClaim(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "manual-dispatch-validation.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	triggers := document["on"].(map[string]any)
	if len(triggers) != 1 || triggers["workflow_dispatch"] == nil {
		t.Fatal("validation dispatcher must be manual only")
	}
	jobs := document["jobs"].(map[string]any)
	claim := jobs["claim-and-dispatch"].(map[string]any)
	permissions := claim["permissions"].(map[string]any)
	if len(permissions) != 1 || permissions["contents"] != "read" {
		t.Fatal("validation claim must not dispatch Actions runs")
	}
	worker := jobs["worker"].(map[string]any)
	if worker["uses"] != "./.github/workflows/factory-worker.yml" || worker["needs"] != "claim-and-dispatch" || worker["if"] != "${{ needs.claim-and-dispatch.outputs.attempt_id != '' }}" {
		t.Fatal("validation worker must require an actual claim")
	}
	outputs := claim["outputs"].(map[string]any)
	inputs := worker["with"].(map[string]any)
	for _, name := range []string{"attempt_id", "request_id", "fence", "dispatcher_id"} {
		if outputs[name] != "${{ steps.claim.outputs."+name+" }}" || inputs[name] != "${{ needs.claim-and-dispatch.outputs."+name+" }}" {
			t.Fatalf("validation handoff changed %s", name)
		}
	}
	steps := claim["steps"].([]any)
	if !workflowRunsContain(steps, "--dispatch-module-id") || workflowRunsContain(steps, "claim-next") || workflowRunsContain(steps, "gh workflow run") {
		t.Fatal("validation must claim only an exact target")
	}
}

func TestWorkerWorkflowSharesManualImplementation(t *testing.T) {
	for _, filename := range []string{"manual-worker-validation.yml", "factory-worker.yml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", filename))
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := yaml.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		triggers := document["on"].(map[string]any)
		trigger := "workflow_call"
		if filename == "factory-worker.yml" {
			call := triggers[trigger].(map[string]any)
			secrets, ok := call["secrets"].(map[string]any)
			if !ok || len(secrets) != 2 {
				t.Fatal("reusable worker must declare exactly its environment credential names")
			}
			for _, name := range []string{"AVM_WRITER_APP_ID", "AVM_WRITER_PRIVATE_KEY"} {
				secret, ok := secrets[name].(map[string]any)
				if !ok || secret["required"] != false {
					t.Fatal("environment secrets must not require caller forwarding")
				}
			}
		}
		if filename == "manual-worker-validation.yml" {
			trigger = "workflow_dispatch"
			jobs := document["jobs"].(map[string]any)
			worker := jobs["worker"].(map[string]any)
			if len(jobs) != 1 || worker["uses"] != "./.github/workflows/factory-worker.yml" {
				t.Fatal("manual worker must call the shared implementation only")
			}
			if document["concurrency"] != nil {
				t.Fatal("manual wrapper must not hold the callee's concurrency group")
			}
			permissions, ok := worker["permissions"].(map[string]any)
			if !ok || len(permissions) != 1 || permissions["contents"] != "write" || worker["secrets"] != nil {
				t.Fatal("manual wrapper must use only the contents write ceiling without inherited secrets")
			}
			inputs := worker["with"].(map[string]any)
			if len(inputs) != 4 {
				t.Fatal("manual wrapper must pass exactly the four ownership inputs")
			}
			for _, input := range []string{"attempt_id", "request_id", "fence", "dispatcher_id"} {
				if inputs[input] != "${{ inputs."+input+" }}" {
					t.Fatalf("manual worker does not preserve exact %s", input)
				}
			}
		}
		if len(triggers) != 1 || triggers[trigger] == nil {
			t.Fatalf("%s must have only %s", filename, trigger)
		}
		permissions, ok := document["permissions"].(map[string]any)
		if !ok || len(permissions) != 0 {
			t.Fatal("worker workflow must default to no permissions")
		}
		inputs := triggers[trigger].(map[string]any)["inputs"].(map[string]any)
		if len(inputs) != 4 {
			t.Fatal("worker workflow must expose exactly four ownership inputs")
		}
		for _, name := range []string{"attempt_id", "request_id", "fence", "dispatcher_id"} {
			input, ok := inputs[name].(map[string]any)
			if !ok || input["type"] != "string" || input["required"] != true {
				t.Fatalf("invalid ownership input %s", name)
			}
		}
	}
}

func TestWorkerWorkflowSeedsOnlyTrustedStateJobs(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "factory-worker.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	jobs := document["jobs"].(map[string]any)
	for _, name := range []string{"prepare", "validate-and-seal", "compute"} {
		job := jobs[name].(map[string]any)
		steps := job["steps"].([]any)
		seeds := workflowRunsContain(steps, "--state-checkout-root work/state")
		if seeds != (name != "compute") {
			t.Fatalf("state seeding for %s = %t", name, seeds)
		}
		if name != "compute" && !workflowVerifiesStateOrigin(steps) {
			t.Fatalf("state checkout identity is not verified for %s", name)
		}
	}
}

func TestWorkerStateCheckoutOptionRequiresTrustedStateCommand(t *testing.T) {
	if err := run([]string{"--process-work", "missing", "--work-artifact-out", "missing-output", "--state-checkout-root", "missing"}); err == nil || !strings.Contains(err.Error(), "require a state command") {
		t.Fatalf("compute accepted state checkout option: %v", err)
	}
}

func TestWorkerCommandsUseCommonStateCheckoutContract(t *testing.T) {
	for _, mode := range []string{"--prepare-worker-input", "--seal-worker"} {
		args := []string{mode, "--state-branch", "factory-state", "--state-checkout-root", t.TempDir()}
		if mode == "--seal-worker" {
			args = append(args, "--worker-workflow-id", "factory-worker", "--worker-producer-commit", strings.Repeat("a", 40))
		}
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "GitHub App credentials are required") {
			t.Fatalf("%s did not construct the verified Git backend: %v", mode, err)
		}
	}
}

func TestWorkerReceiptWorkflowRequiresExactSuccessfulReusableJobs(t *testing.T) {
	_, _, steps := providerWorkflowDocumentFor(t, "manual-accept-evidence.yml", "import")
	var filter string
	for _, value := range steps {
		script, _ := value.(map[string]any)["run"].(string)
		_, remaining, found := strings.Cut(script, "jq -e '\n")
		if found && strings.Contains(remaining, "work/v2/attempt/jobs.json") {
			filter, _, _ = strings.Cut(remaining, "' work/v2/attempt/jobs.json")
		}
	}
	if filter == "" {
		t.Fatal("worker job receipt filter is missing")
	}
	for _, test := range []struct {
		prefix     string
		conclusion string
		valid      bool
	}{{"", "success", true}, {"worker / ", "success", true}, {"untrusted / ", "success", false}, {"worker / ", "failure", false}} {
		data, err := json.Marshal(map[string]any{"jobs": []map[string]string{
			{"name": test.prefix + "Compute without state credentials", "conclusion": test.conclusion},
			{"name": test.prefix + "Trusted artifact validation and completion seal", "conclusion": "success"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command("jq", "-e", filter)
		command.Stdin = strings.NewReader(string(data))
		output, err := command.CombinedOutput()
		if (err == nil) != test.valid {
			t.Fatalf("receipt jobs prefix=%q conclusion=%q: %s %v", test.prefix, test.conclusion, output, err)
		}
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
		checkoutHasReadCredential = ok && with["persist-credentials"] == false
	}
	checksMergedAncestry := workflowRunsContain(steps, "git merge-base --is-ancestor")
	checksLiveMainThroughAPI := workflowRunsContain(steps, "git/ref/heads/main") && workflowRunsContain(steps, "remote_main_sha") && !workflowRunsContain(steps, "git fetch --no-tags origin main")
	checksSuccessfulWorkerJobs := workflowRunsContain(steps, "/actions/runs/$run_id/jobs") && workflowRunsContain(steps, "Compute without state credentials") && workflowRunsContain(steps, "Trusted artifact validation and completion seal")
	requiresWholeRunSuccess := workflowRunsContain(steps, ".conclusion == \"success\" and")
	rebuildsArtifactProposal := workflowRunsContain(steps, "--build-module-evidence-proposal")
	usesDedicatedValidationFlag := workflowRunsContain(steps, "--module-evidence-validation-id")
	usesMergedDatasetTimestamp := workflowRunsContain(steps, ".created_at") && workflowRunsContain(steps, "--module-evidence-proposal-created-at")
	comparesRebuiltProposalSemantically := workflowRunsContain(steps, "jq -S . work/v2/proposal/proposal.json") && workflowRunsContain(steps, "jq -S . work/v2/rebuilt-proposal/proposal.json") && workflowRunsContain(steps, "cmp work/v2/proposal-normalized.json work/v2/rebuilt-proposal-normalized.json")
	validatesMergedCandidate := workflowRunsContain(steps, "--apply-proposal") && workflowRunsContain(steps, "diff -qr work/v2/rebuilt-data work/v2/merged/data")
	importsReceipt := workflowRunsContain(steps, "--import-evidence-receipt")
	derivesRequiredContexts := workflowRunsContain(steps, "avm_release_version: $attempt[0].request.work.avm_release_version") && workflowRunsContain(steps, "--evidence-accepted-data-root work/v2/merged/data") && workflowRunsContain(steps, "--assessment-track-policy config/providers.yaml")
	writesRepository := workflowRunsContain(steps, "git push") || workflowRunsContain(steps, "gh pr create")
	if !checkoutHasReadCredential || !checksMergedAncestry || !checksLiveMainThroughAPI || !checksSuccessfulWorkerJobs || requiresWholeRunSuccess || !rebuildsArtifactProposal || !usesDedicatedValidationFlag || !usesMergedDatasetTimestamp || !comparesRebuiltProposalSemantically || !validatesMergedCandidate || !importsReceipt || !derivesRequiredContexts || writesRepository {
		t.Fatalf("evidence acceptance must verify merged history and live main ref, compare rebuilt proposal content, then derive the exact facts context set before state import: readCredential=%t ancestry=%t liveMainAPI=%t workerJobs=%t wholeRunSuccess=%t rebuild=%t validatorFlag=%t replayTimestamp=%t semanticProposal=%t candidate=%t import=%t requiredContexts=%t repositoryWrite=%t", checkoutHasReadCredential, checksMergedAncestry, checksLiveMainThroughAPI, checksSuccessfulWorkerJobs, requiresWholeRunSuccess, rebuildsArtifactProposal, usesDedicatedValidationFlag, usesMergedDatasetTimestamp, comparesRebuiltProposalSemantically, validatesMergedCandidate, importsReceipt, derivesRequiredContexts, writesRepository)
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

func TestV2DiscoveryWorkflowSchedulesReleaseChecksForActiveModules(t *testing.T) {
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
	restrictsToNewCatalogIDs := workflowRunsContain(steps, "--discovery-new-modules-only")
	projectsTargets := workflowRunsContain(steps, "--project-discovery-modules")
	dispatchesWorker := workflowRunsContain(steps, "manual-worker.yml") || workflowRunsContain(steps, "--dispatch-operation claim")
	if passesTargets || !writesResult || restrictsToNewCatalogIDs || projectsTargets || dispatchesWorker {
		t.Fatalf("factory discovery must persist active-module release checks without projection or dispatch: restricted=%t writesResult=%t newCatalogOnly=%t projects=%t dispatchesWorker=%t", passesTargets, writesResult, restrictsToNewCatalogIDs, projectsTargets, dispatchesWorker)
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
	resolveInput, resolveOK := inputs["resolve_unknown_effects"].(map[string]any)
	if !ok || !dispatchOK || !inputsOK || !moduleOK || moduleInput["type"] != "string" || moduleInput["required"] != false || !resolveOK || resolveInput["type"] != "boolean" || resolveInput["default"] != false || on["schedule"] != nil || on["push"] != nil {
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
	assertProviderWorkflowScriptsParse(t, steps)
	projectsSelectedModules := workflowRunsContain(steps, "--request-projection-recovery") && workflowRunsContain(steps, "--module-names \"$MODULE_NAMES\"")
	readsBackExactEffects := workflowRunsContain(steps, "--resolve-projection-owner-effects \"$MODULE_NAMES\"")
	usesStateToken := workflowRunsContain(steps, "--mint-state-token")
	performsDiscovery := workflowRunsContain(steps, "--discover-catalog") || workflowRunsContain(steps, "--persist-discovery") || workflowRunsContain(steps, "--discover-modules")
	dispatchesWorkers := workflowRunsContain(steps, "manual-worker.yml") || workflowRunsContain(steps, "--dispatch-operation claim")
	if !projectsSelectedModules || !readsBackExactEffects || !usesStateToken || performsDiscovery || dispatchesWorkers || workflowRunsContain(steps, "--mint-projection-token") {
		t.Fatalf("recovery must queue selected modules or read back exact parked effects: queued=%t exactReadback=%t stateToken=%t discovery=%t workerDispatch=%t", projectsSelectedModules, readsBackExactEffects, usesStateToken, performsDiscovery, dispatchesWorkers)
	}
	for _, raw := range steps {
		step := raw.(map[string]any)
		if step["name"] == "Project selected lifecycle intents" && workflowRunsContain([]any{step}, "--project-discovery-modules") {
			t.Fatal("recovery workflow must not call a direct projection command")
		}
		if step["name"] == "Read back selected uncertain projection effects" {
			if step["if"] != "${{ inputs.resolve_unknown_effects }}" || !strings.Contains(step["run"].(string), "Exact module_names are required") {
				t.Fatal("unknown-effect readback must require explicit exact module names")
			}
		}
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
	moduleInput, moduleOK := inputs["module_id"].(map[string]any)
	if !ok || !dispatchOK || !inputsOK || !requestOK || !moduleOK || requestInput["required"] != false || moduleInput["required"] != false || requestInput["type"] != "string" || moduleInput["type"] != "string" || on["schedule"] != nil || on["push"] != nil {
		t.Fatalf("dispatcher must accept one manual request or module target and have no automatic trigger: %#v", on)
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
	seedsCheckout := workflowRunsContain(steps, "--state-checkout-root work/state") && workflowChecksOutState(steps)
	rejectsBothTargets := workflowRunsContain(steps, "either request_id or module_id, not both")
	requestScopedClaim := workflowRunsContain(steps, "--request-id \"$REQUEST_ID\"")
	moduleScopedClaim := workflowRunsContain(steps, "--dispatch-module-id \"$MODULE_ID\"")
	usesClaimNext := workflowRunsContain(steps, "--dispatch-operation claim-next")
	if !seedsCheckout || !workflowVerifiesStateOrigin(steps) || !rejectsBothTargets || !requestScopedClaim || !moduleScopedClaim || usesClaimNext {
		t.Fatalf("manual dispatcher target contract is invalid: checkout=%t rejectsBoth=%t request=%t module=%t claimNext=%t", seedsCheckout, rejectsBothTargets, requestScopedClaim, moduleScopedClaim, usesClaimNext)
	}
}

func TestFactoryDispatchClaimsOnlyTheNextReadyRequest(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "factory-dispatch.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse factory dispatch workflow: %v", err)
	}
	on, ok := document["on"].(map[string]any)
	if !ok || on["schedule"] == nil || on["workflow_dispatch"] != nil || on["push"] != nil {
		t.Fatalf("factory dispatch must be scheduled without manual dispatch: %#v", on)
	}
	jobs, ok := document["jobs"].(map[string]any)
	job, jobOK := jobs["claim-and-dispatch"].(map[string]any)
	steps, stepsOK := job["steps"].([]any)
	permissions, permissionsOK := job["permissions"].(map[string]any)
	if !ok || !jobOK || !stepsOK || !permissionsOK || job["environment"] != "data-publisher" || permissions["contents"] != "read" || permissions["actions"] != "write" {
		t.Fatalf("factory dispatch job contract is invalid: %#v", job)
	}
	seedsCheckout := workflowRunsContain(steps, "--state-checkout-root work/state") && workflowChecksOutState(steps)
	claimsNext := workflowRunsContain(steps, "--dispatch-operation claim-next")
	targetsModule := workflowRunsContain(steps, "--dispatch-module-id") || workflowRunsContain(steps, "--request-id")
	startsWorker := workflowRunsContain(steps, "gh workflow run manual-worker.yml")
	if !seedsCheckout || !workflowVerifiesStateOrigin(steps) || !claimsNext || targetsModule || !startsWorker {
		t.Fatalf("factory dispatch scope is invalid: checkout=%t claimsNext=%t targetsModule=%t startsWorker=%t", seedsCheckout, claimsNext, targetsModule, startsWorker)
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

func workflowChecksOutState(steps []any) bool {
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok || !strings.HasPrefix(fmt.Sprint(step["uses"]), "actions/checkout@") {
			continue
		}
		with, ok := step["with"].(map[string]any)
		if ok && with["path"] == "work/state" && with["ref"] == "factory-state" && with["persist-credentials"] == false && with["fetch-depth"] == 1 {
			return true
		}
	}
	return false
}

func workflowVerifiesStateOrigin(steps []any) bool {
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			continue
		}
		run, _ := step["run"].(string)
		env, _ := step["env"].(map[string]any)
		if strings.Contains(run, "git -C work/state remote get-url origin") && env["GIT_NO_REPLACE_OBJECTS"] == "1" && strings.Contains(run, "repository identity mismatch") {
			return true
		}
	}
	return false
}
