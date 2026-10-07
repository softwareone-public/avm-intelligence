package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
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

type localQuotaTransport struct{ target *url.URL }

func (transport localQuotaTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	copyURL := *request.URL
	copyURL.Scheme, copyURL.Host = transport.target.Scheme, transport.target.Host
	copyRequest.URL = &copyURL
	return http.DefaultTransport.RoundTrip(copyRequest)
}

func TestModuleCycleLocalRetryRechecksBeforeClaim(t *testing.T) {
	probes := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/rate_limit" {
			t.Fatalf("recheck attempted projection mutation: %s", request.URL.Path)
		}
		probes++
		_, _ = writer.Write([]byte(`{"resources":{"core":{"remaining":500,"limit":5000,"reset":1999999999},"graphql":{"remaining":5000,"limit":5000,"reset":1999999999},"search":{"remaining":30,"limit":30,"reset":1999999999}}}`))
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	projector := &moduleCycleProjector{client: github.Client{Token: "fixture", HTTPClient: &http.Client{Transport: localQuotaTransport{target: target}}, Pacer: ratelimit.New()}}
	request := ratelimit.Request{Resource: "rest", Points: 5, Mutation: true}
	intent := github.ProjectionIntentRecord{DeliveryState: github.ProjectionRetryable, RetryAt: time.Now().Add(-time.Minute), RetryReason: "primary_reserve", RetryRequest: &request}
	err := projector.validateDeliveryOwnership(context.Background(), intent)
	var deferred *ratelimit.DeferredError
	if !errors.As(err, &deferred) || deferred.Reason != "primary_reserve" || time.Until(deferred.RetryAt) > 5*time.Minute || probes != 1 {
		t.Fatalf("local recheck did not stop before claim: %v probes=%d", err, probes)
	}
	intent.RetryAt = time.Now().Add(time.Hour)
	intent.RetryReason = "server_retry_after"
	err = projector.validateDeliveryOwnership(context.Background(), intent)
	if !errors.As(err, &deferred) || !deferred.RetryAt.Equal(intent.RetryAt) || probes != 1 {
		t.Fatal("server retry probed or shortened")
	}
}

func TestModuleCycleRetryWaitDefersAtStoredDeadline(t *testing.T) {
	now := time.Now().UTC()
	deadline := now.Add(30 * time.Minute)
	intent := github.ProjectionIntentRecord{DeliveryState: github.ProjectionRetryable, RetryAt: deadline}
	err := validateCycleDeliveryOwnership(intent, now)
	var deferred *ratelimit.DeferredError
	if !errors.As(err, &deferred) || !deferred.RetryAt.Equal(deadline) || deferred.Resource != "projection_retry" {
		t.Fatalf("stored backoff became generic failure: %v", err)
	}
	if err := validateCycleDeliveryOwnership(intent, deadline); err != nil {
		t.Fatalf("due retry stayed blocked: %v", err)
	}
}

func TestModuleCycleProjectorDoesNotBypassProtectedDeliveryOrReprocessAcknowledgedWork(t *testing.T) {
	for _, deliveryState := range []string{github.ProjectionProjecting, github.ProjectionRetryable, github.ProjectionNeedsReview, github.ProjectionProjected} {
		t.Run(deliveryState, func(t *testing.T) { assertCycleProjectorState(t, deliveryState) })
	}
}

func TestCycleNoopRequiresCurrentAcknowledgementAndNoPendingWork(t *testing.T) {
	base := github.ProjectionIntentRecord{Fingerprint: "current", LastAcknowledgedFingerprint: "current", DeliveryState: github.ProjectionProjected}
	module := discovery.ModuleCycle{ModuleID: "avm-res-test"}
	if !canSkipAcknowledgedCycle(module, base, true) {
		t.Fatal("unchanged acknowledged cycle was not skipped")
	}
	changed := module
	changed.ProjectionRequired = true
	if canSkipAcknowledgedCycle(changed, base, true) {
		t.Fatal("new source or forced lifecycle was skipped")
	}
	if canSkipAcknowledgedCycle(module, base, false) {
		t.Fatal("obsolete discovery binding was skipped")
	}
	staleAck := base
	staleAck.LastAcknowledgedFingerprint = "older"
	if canSkipAcknowledgedCycle(module, staleAck, true) {
		t.Fatal("mismatched acknowledgement was skipped")
	}
	pending := base
	pending.PendingEvidence = map[string]github.RunEvidence{"pending": {}}
	if canSkipAcknowledgedCycle(module, pending, true) {
		t.Fatal("pending evidence was skipped")
	}
	for _, status := range []string{github.ProjectionRetryable, github.ProjectionProjecting, github.ProjectionNeedsReview} {
		protected := base
		protected.DeliveryState = status
		if canSkipAcknowledgedCycle(module, protected, true) {
			t.Fatalf("unfinished/protected delivery was skipped: %s", status)
		}
	}
}

// evidenceFailureBackend permits the intent save but rejects the subsequent evidence transaction.
type evidenceFailureBackend struct {
	*workerTestBackend
	commits int
	failure error
}

func (backend *evidenceFailureBackend) Commit(ctx context.Context, head string, snapshot state.Snapshot) (string, error) {
	backend.commits++
	if backend.commits == 2 {
		return "", backend.failure
	}
	return backend.workerTestBackend.Commit(ctx, head, snapshot)
}

func TestCycleDeliveryRetainsCommittedHeadWhenEvidenceWriteDefers(t *testing.T) {
	now := time.Now().UTC()
	moduleID := "avm-res-catalog-a"
	snapshot := manualProjectionTestSnapshot(t, moduleID, github.ProjectionPending, now)
	data, err := json.Marshal(discovery.DurableCatalogState{SchemaVersion: 1, Accepted: cycleProjectorTestCatalog(t, now)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records[discoveryCatalogStatePath] = state.Record{Kind: "discovery_catalog", SchemaVersion: 1, Data: data}
	data, err = json.Marshal(discovery.ProjectionIntent{SchemaVersion: 1, ModuleID: moduleID, DesiredGeneration: 2, Status: discovery.IntentBacklog, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records["state/projection/intents/"+moduleID+".json"] = state.Record{Kind: "projection_intent", SchemaVersion: 1, Data: data}
	origin := &ratelimit.DeferredError{Resource: "rest", RetryAt: now.Add(5 * time.Minute), Reason: "local_hourly_mutations"}
	backend := &evidenceFailureBackend{workerTestBackend: &workerTestBackend{head: "head-0", snapshot: snapshot}, failure: origin}
	projector := &moduleCycleProjector{backend: backend, runURL: "https://github.com/softwareone-public/avm-intelligence/actions/runs/2", runID: "2-1"}
	head, err := projector.deliver(context.Background(), discovery.ModuleCycle{ModuleID: moduleID, Head: "head-0"})
	if !errors.Is(err, origin) || backend.commits != 2 || head == "" || head == "head-0" || head != backend.head {
		t.Fatalf("evidence failure lost committed head: returned=%s actual=%s commits=%d error=%v", head, backend.head, backend.commits, err)
	}
	if projector.client.Token != "" {
		t.Fatal("evidence failure reached remote projection")
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
		addCycleEvidenceReceiptFixture(t, &snapshot, intent, now)
	}
	backend := &workerTestBackend{head: "head-0", snapshot: snapshot}
	projector := &moduleCycleProjector{backend: backend, runURL: "https://github.com/softwareone-public/avm-intelligence/actions/runs/2", runID: "2-1"}
	skipped, skipErr := projector.skipAcknowledged(context.Background(), discovery.ModuleCycle{ModuleID: moduleID, Head: backend.head, Snapshot: snapshot})
	if skipped != (deliveryState == github.ProjectionProjected) || skipped && skipErr != nil || backend.head != "head-0" || projector.client.Token != "" {
		t.Fatalf("snapshot skip ignored acknowledgement or ownership: state=%s skipped=%t error=%v", deliveryState, skipped, skipErr)
	}
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

func addCycleEvidenceReceiptFixture(t *testing.T, snapshot *state.Snapshot, intent github.ProjectionIntentRecord, now time.Time) {
	t.Helper()
	evidence := github.RunEvidence{ModuleID: intent.ModuleID, Phase: github.EvidencePhaseDiscovery, RunID: "1-1", RunURL: "https://github.com/softwareone-public/avm-intelligence/actions/runs/1", StateHead: "head-0", Status: intent.Projection.Status, Outcome: github.EvidenceOutcomeCompleted}
	evidenceID, err := evidence.ID()
	if err != nil {
		t.Fatal(err)
	}
	contentID, err := identity.ID("run-evidence-content", 1, evidence)
	if err != nil {
		t.Fatal(err)
	}
	binding := github.ProjectionBinding{SchemaVersion: 1, ModuleID: intent.ModuleID, IssueNumber: 1, IssueNodeID: "fixture-issue", IssueURL: "https://github.com/softwareone-public/avm-intelligence/issues/1", ProjectItemID: "fixture-item", Generation: intent.Generation, UpdatedAt: now,
		EvidenceComments: map[string]github.RunEvidenceReceipt{evidenceID: {EvidenceID: evidenceID, CommentID: 1, RunID: "1-1", Phase: github.EvidencePhaseDiscovery, Outcome: github.EvidenceOutcomeCompleted, ContentID: contentID}}}
	data, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records["state/projection/bindings/"+intent.ModuleID+".json"] = state.Record{Kind: "projection_binding", SchemaVersion: 1, Data: data}
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
	projector := &moduleCycleProjector{backend: backend, runURL: "https://github.com/softwareone-public/avm-intelligence/actions/runs/2", runID: "2-1", client: github.Client{Token: "fixture", Budget: &github.ProjectionBudget{}}}
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

func TestStateLayoutOptionsRequireManualCycleBeforeOperations(t *testing.T) {
	for _, option := range []string{"--state-layout-config", "--state-checkout-root", "--state-checkout-tree"} {
		if err := run([]string{"--list-dimensions", option, "fixture"}); err == nil || !strings.Contains(err.Error(), "require --discovery-module-cycle") {
			t.Fatalf("unscoped storage option reached operations: %v", err)
		}
	}
}

func TestManualDiscoveryWorkflowUsesVerifiedModuleLayoutStorage(t *testing.T) {
	_, _, steps := providerWorkflowDocumentFor(t, "manual-discovery.yml", "persist")
	for _, required := range []string{"--state-layout-config config/layout.yaml", "--state-checkout-root work/state", "--state-checkout-tree work/state-tree", "git -C work/state ls-tree -r -z HEAD", "GITHUB_REPOSITORY"} {
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
	_, _, factory := providerWorkflowDocumentFor(t, "factory-discovery.yml", "persist")
	if workflowRunsContain(factory, "--state-layout-config") || workflowRunsContain(factory, "--state-checkout-root") {
		t.Fatal("manual state migration transport was promoted to autonomous discovery")
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
			if filename == "manual-discovery.yml" {
				continuation, ok := inputs["continuation_id"].(map[string]any)
				if !ok || continuation["type"] != "string" || continuation["default"] != "" || continuation["required"] != false {
					t.Fatal("manual discovery continuation input is invalid")
				}
				delete(inputs, "continuation_id")
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
	if err := run([]string{"--flush-projection-modules", moduleID, "--discover-catalog"}); err == nil {
		t.Fatal("projection flush accepted discovery mode")
	}
}

func TestProjectionFlushWorkflowNeverDiscoversOrDispatches(t *testing.T) {
	document, job, steps := providerWorkflowDocumentFor(t, "manual-projection-flush.yml", "project")
	triggers := document["on"].(map[string]any)
	if len(triggers) != 1 || triggers["workflow_dispatch"] == nil {
		t.Fatal("projection flush must be manual only")
	}
	permissions := job["permissions"].(map[string]any)
	if len(permissions) != 1 || permissions["contents"] != "read" || job["environment"] != "data-publisher" {
		t.Fatal("projection flush permissions/environment changed")
	}
	if !workflowRunsContain(steps, `--flush-projection-modules "$MODULE_NAMES"`) || workflowRunsContain(steps, "--persist-discovery") || workflowRunsContain(steps, "--dispatch-operation") || workflowRunsContain(steps, "--force") {
		t.Fatal("projection flush must not discover, dispatch or replay acknowledged work")
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
		seeds := workflowRunsContain(steps, "--dispatch-checkout-root work/state") && workflowRunsContain(steps, "--dispatch-checkout-tree work/state-tree")
		if seeds != (name != "compute") {
			t.Fatalf("state seeding for %s = %t", name, seeds)
		}
	}
}

func TestWorkerCheckoutOptionsRequireBothPaths(t *testing.T) {
	if err := run([]string{"--process-work", "missing", "--work-artifact-out", "missing-output", "--dispatch-checkout-root", "missing", "--dispatch-checkout-tree", "missing"}); err == nil || !strings.Contains(err.Error(), "cannot be combined with trusted worker commands") {
		t.Fatalf("compute accepted state checkout options: %v", err)
	}
	for _, mode := range []string{"--prepare-worker-input", "--seal-worker"} {
		for _, option := range []string{"--dispatch-checkout-root", "--dispatch-checkout-tree"} {
			err := run([]string{mode, option, "missing"})
			if err == nil || !strings.Contains(err.Error(), "worker checkout root and tree listing must be supplied together") {
				t.Fatalf("unpaired %s %s: %v", mode, option, err)
			}
		}
	}
}

func TestWorkerCheckoutUsesVerifiedBackendBeforeReadingListing(t *testing.T) {
	t.Setenv("FACTORY_STATE_TOKEN", "fixture-token")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "state"), 0700); err != nil {
		t.Fatal(err)
	}
	content := "{}\n"
	if err := os.WriteFile(filepath.Join(root, "state", "transaction.json"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	hash := exec.Command("git", "hash-object", "--stdin")
	hash.Stdin = strings.NewReader(content)
	objectID, err := hash.Output()
	if err != nil {
		t.Fatal(err)
	}
	listing := filepath.Join(t.TempDir(), "tree")
	if err := os.WriteFile(listing, []byte("100644 blob "+strings.TrimSpace(string(objectID))+"\tstate/transaction.json\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"--prepare-worker-input", "--seal-worker"} {
		err := run([]string{mode, "--state-branch", "factory-state", "--dispatch-checkout-root", t.TempDir(), "--dispatch-checkout-tree", filepath.Join(t.TempDir(), "missing-tree")})
		if err == nil || !strings.Contains(err.Error(), "read dispatch checkout tree listing") {
			t.Fatalf("%s did not reach verified checkout listing: %v", mode, err)
		}
		err = run([]string{mode, "--state-branch", "factory-state", "--dispatch-checkout-root", root, "--dispatch-checkout-tree", listing})
		if err == nil || !strings.Contains(err.Error(), "requires attempt") {
			t.Fatalf("%s did not seed a valid checkout before identity validation: %v", mode, err)
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
	seedsCheckout := workflowRunsContain(steps, "--dispatch-checkout-root work/state") && workflowRunsContain(steps, "git -C work/state ls-tree -r -z HEAD")
	rejectsBothTargets := workflowRunsContain(steps, "either request_id or module_id, not both")
	requestScopedClaim := workflowRunsContain(steps, "--request-id \"$REQUEST_ID\"")
	moduleScopedClaim := workflowRunsContain(steps, "--dispatch-module-id \"$MODULE_ID\"")
	usesClaimNext := workflowRunsContain(steps, "--dispatch-operation claim-next")
	if !seedsCheckout || !rejectsBothTargets || !requestScopedClaim || !moduleScopedClaim || usesClaimNext {
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
	seedsCheckout := workflowRunsContain(steps, "--dispatch-checkout-root work/state") && workflowRunsContain(steps, "git -C work/state ls-tree -r -z HEAD")
	claimsNext := workflowRunsContain(steps, "--dispatch-operation claim-next")
	targetsModule := workflowRunsContain(steps, "--dispatch-module-id") || workflowRunsContain(steps, "--request-id")
	startsWorker := workflowRunsContain(steps, "gh workflow run manual-worker.yml")
	if !seedsCheckout || !claimsNext || targetsModule || !startsWorker {
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
