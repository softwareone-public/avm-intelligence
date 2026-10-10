package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestFactoryProjectionWorkflowPollsDueStateWithoutProducerFanIn(t *testing.T) {
	document, job, steps := providerWorkflowDocumentFor(t, "factory-projection.yml", "project")
	triggers, ok := document["on"].(map[string]any)
	if !ok || len(triggers) != 1 {
		t.Fatalf("factory projection triggers = %#v, want only schedule", document["on"])
	}
	schedule, ok := triggers["schedule"].([]any)
	if !ok || len(schedule) != 1 || schedule[0].(map[string]any)["cron"] != "*/5 * * * *" {
		t.Fatalf("factory projection schedule = %#v, want */5 * * * *", triggers["schedule"])
	}
	if !reflect.DeepEqual(document["permissions"], map[string]any{}) {
		t.Fatalf("workflow permissions = %#v, want empty top-level permissions", document["permissions"])
	}
	concurrency, ok := document["concurrency"].(map[string]any)
	if !ok || concurrency["group"] != "avm-intelligence-factory-projection" || concurrency["cancel-in-progress"] != false {
		t.Fatalf("projection concurrency = %#v", document["concurrency"])
	}
	if job["environment"] != "data-publisher" || !reflect.DeepEqual(job["permissions"], map[string]any{"contents": "read"}) {
		t.Fatalf("projection job environment/permissions = %q / %#v", job["environment"], job["permissions"])
	}
	assertProviderWorkflowScriptsParse(t, steps)

	var mainCheckout, stateCheckout, mintState, dueCommand int
	for _, value := range steps {
		step := value.(map[string]any)
		uses, _ := step["uses"].(string)
		if uses != "" && !strings.Contains(uses, "@") {
			t.Errorf("action is not pinned: %s", uses)
		}
		if strings.HasPrefix(uses, "actions/checkout@") {
			options, _ := step["with"].(map[string]any)
			switch options["ref"] {
			case "main":
				mainCheckout++
				if options["persist-credentials"] != false {
					t.Error("main checkout must not persist credentials")
				}
			case "factory-state":
				stateCheckout++
				if options["path"] != "work/state" || options["fetch-depth"] != 1 || options["persist-credentials"] != false || options["token"] != "${{ steps.state-token.outputs.token }}" || options["repository"] != "${{ github.repository }}" {
					t.Errorf("state checkout is not isolated and explicit: %#v", options)
				}
			}
		}
		name, _ := step["name"].(string)
		script, _ := step["run"].(string)
		if name == "Mint state-only installation token" {
			mintState++
			if !strings.Contains(script, "--mint-state-token") {
				t.Error("state checkout token step does not use the state-only token command")
			}
		}
		if name == "Project fresh due state intents" {
			dueCommand++
			if !strings.Contains(script, "--project-due-intents") || !strings.Contains(script, "--state-branch factory-state") || !strings.Contains(script, "--state-checkout-root work/state") {
				t.Errorf("scheduled command does not poll durable state: %s", script)
			}
		}
		if strings.Contains(script, "workflow_run") || strings.Contains(script, "workflow_dispatch") || strings.Contains(script, "gh workflow run") {
			t.Errorf("projector contains producer fan-in or dispatch: %s", name)
		}
	}
	if mainCheckout != 1 || stateCheckout != 1 || mintState != 1 || dueCommand != 1 {
		t.Fatalf("workflow step counts main=%d state=%d state-token=%d due-command=%d", mainCheckout, stateCheckout, mintState, dueCommand)
	}
}

func TestProjectDueIntentsRequiresTrustedRunIdentityAndRejectsExtraScope(t *testing.T) {
	if err := run([]string{"--project-due-intents"}); err == nil || !strings.Contains(err.Error(), "run URL and run ID are required") {
		t.Fatalf("missing projector run identity error = %v", err)
	}
	if err := run([]string{"--project-due-intents", "--project-discovery-run-url", "https://github.com/softwareone-public/avm-intelligence/actions/runs/1", "--project-discovery-run-id", "1-1", "--data-root", "data"}); err == nil || !strings.Contains(err.Error(), "unsupported for scheduled projection polling") {
		t.Fatalf("extra scheduled projection scope error = %v", err)
	}
}

func TestOnlyScheduledProjectorCanMintProjectionToken(t *testing.T) {
	commandFiles, err := filepath.Glob(filepath.Join(".", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var mintCallers, batchCallers []string
	for _, filename := range commandFiles {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		data, readErr := os.ReadFile(filename)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), "github.MintToken(") {
			mintCallers = append(mintCallers, filepath.Base(filename))
		}
		if strings.Contains(string(data), ".ProjectClaimedBatch(") {
			batchCallers = append(batchCallers, filepath.Base(filename))
		}
	}
	sort.Strings(mintCallers)
	if !reflect.DeepEqual(mintCallers, []string{"discovery_projection.go"}) {
		t.Fatalf("projection token mint callers = %v, want only the scheduled projector", mintCallers)
	}
	sort.Strings(batchCallers)
	if !reflect.DeepEqual(batchCallers, []string{"discovery_projection.go"}) {
		t.Fatalf("projection batch callers = %v, want only the scheduled projector", batchCallers)
	}
	native, err := os.ReadFile("native.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"mint-projection-token", "project-discovery-modules"} {
		if strings.Contains(string(native), retired) {
			t.Errorf("retired non-projector command %q remains registered", retired)
		}
	}
	workflows, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, workflow := range workflows {
		data, readErr := os.ReadFile(workflow)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, retired := range []string{"--mint-projection-token", "--project-discovery-modules"} {
			if strings.Contains(string(data), retired) {
				t.Errorf("workflow %s contains retired projection command %q", filepath.Base(workflow), retired)
			}
		}
	}
}
