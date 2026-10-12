package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/progress"
)

func TestProjectionProgressShowsOperationsAndPreservesRequests(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	var baseline string
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelError} {
		var output bytes.Buffer
		slog.SetDefault(slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: level})))
		client, transport, projection := newFleetRecoveryClient(t, "project")
		if _, err := client.UpsertFleet(context.Background(), "softwareone-public", "avm-intelligence", 1, projection); err == nil {
			t.Fatal("fixture failure did not propagate")
		}
		if level == slog.LevelInfo && strings.Contains(output.String(), "Issue and Project synchronized") {
			t.Fatal("failed module reported successful synchronization")
		}
		if _, err := client.UpsertFleet(context.Background(), "softwareone-public", "avm-intelligence", 1, projection); err != nil {
			t.Fatal(err)
		}
		fingerprint := fmt.Sprintf("%s/%d/%d/%d", transport.issueBody, transport.projectFieldWrites, transport.issueLabelWrites, transport.issueSearches)
		if baseline == "" {
			baseline = fingerprint
		} else if baseline != fingerprint {
			t.Fatal("logging changed Issue content or API request counts")
		}
		if level == slog.LevelInfo {
			assertProjectionProgress(t, output.String())
		}
	}
}

func assertProjectionProgress(t *testing.T, output string) {
	t.Helper()
	for _, expected := range []string{"module_id=avm-res-compute-disk", "phase=issue_lookup", "phase=issue_summary_update", "phase=project_field_update", "phase=issue_labels", "operation incomplete", "Issue and Project synchronized"} {
		if !strings.Contains(output, expected) {
			t.Errorf("missing projection progress %q", expected)
		}
	}
}

func TestGitHubStatusErrorAllowsOnlyValidationIdentifiers(t *testing.T) {
	body := []byte(`{"message":"SECRET_RESPONSE_SENTINEL","errors":[{"resource":"Installation","field":"permissions.pull_requests","code":"invalid"}]}`)
	err := githubStatusError(http.StatusUnprocessableEntity, body)
	status, ok := err.(*APIStatusError)
	if !ok {
		t.Fatalf("githubStatusError() type = %T, want *APIStatusError", err)
	}
	want := "GitHub API returned HTTP 422 (resource=Installation field=permissions.pull_requests code=invalid)"
	if got := status.SafeDiagnostic(); got != want {
		t.Fatalf("SafeDiagnostic() = %q, want %q", got, want)
	}
	if strings.Contains(err.Error(), "SECRET_RESPONSE_SENTINEL") || strings.Contains(status.SafeDiagnostic(), "SECRET_RESPONSE_SENTINEL") {
		t.Fatal("GitHub response message escaped the typed error")
	}

	unknown := githubStatusError(http.StatusUnprocessableEntity, []byte(`{"message":"SECRET_RESPONSE_SENTINEL","errors":[{"resource":"PrivateName","field":"secret.field","code":"SECRET_CODE"}]}`)).(*APIStatusError)
	if got := unknown.SafeDiagnostic(); got != "GitHub API returned HTTP 422" {
		t.Fatalf("unknown response fields were not discarded: %q", got)
	}
}

func TestProjectionProgressDoesNotLogAPIBodiesOrTokens(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	client := Client{Token: "private-token-sentinel", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private-body-sentinel"))}, nil
	})}}
	if _, err := client.UpsertFleet(context.Background(), "softwareone-public", "avm-intelligence", 1, fleetTestProjection()); err == nil {
		t.Fatal("API failure did not propagate")
	}
	if strings.Contains(output.String(), "private-token-sentinel") || strings.Contains(output.String(), "private-body-sentinel") {
		t.Fatal("private API material leaked to progress logs")
	}
}

func TestProjectionCheckpointPreservesActiveModuleContext(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ctx, finish := progress.Start(context.Background(), "projection", "module_delivery", slog.String("module_id", "avm-res-compute-disk"))
	defer finish(true)
	cache := &ProjectionMetadataCache{}
	cache.setSnapshotCheckpoint("head", func(checkpointCtx context.Context, name, cursor string) error {
		progress.Logger(checkpointCtx).InfoContext(checkpointCtx, "checkpoint identity verified")
		return nil
	})
	output.Reset()
	if err := cache.checkpointCursor(ctx, "issues_page", "complete"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "module_id=avm-res-compute-disk") || !strings.Contains(output.String(), "checkpoint identity verified") {
		t.Fatal("checkpoint lost its active module context")
	}
}

func TestUpsertUsesReducedTokenAndDoesNotClaimPublication(t *testing.T) {
	t.Parallel()
	var calls []string
	client := Client{Token: "reduced-test-token", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.Method+" "+request.URL.Path)
		if request.Header.Get("Authorization") != "Bearer reduced-test-token" {
			t.Fatal("reduced token was not used")
		}
		var body []byte
		if request.Body != nil {
			body, _ = io.ReadAll(request.Body)
		}
		if strings.Contains(string(body), "contents") {
			t.Fatal("contents permission was requested")
		}
		switch request.URL.Path {
		case "/search/issues":
			return jsonResponse(`{"total_count":0,"incomplete_results":false,"items":[]}`)
		case "/repos/softwareone-public/avm-intelligence/issues":
			return jsonResponse(`{"number":7,"node_id":"ISSUE","html_url":"https://github.com/softwareone-public/avm-intelligence/issues/7"}`)
		case "/graphql":
			var payload struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(body, &payload)
			if strings.Contains(payload.Query, "projectV2(number") {
				return jsonResponse(`{"data":{"organization":{"projectV2":{"id":"PROJECT","title":"AVM Intelligence Factory"}}}}`)
			}
			if strings.Contains(payload.Query, "addProjectV2ItemById") {
				return jsonResponse(`{"data":{"addProjectV2ItemById":{"item":{"id":"ITEM"}}}}`)
			}
			if strings.Contains(payload.Query, "fields(first:50)") {
				return jsonResponse("{\"data\":{\"node\":{\"fields\":{\"nodes\":[{\"id\":\"status\",\"name\":\"Status\",\"options\":[{\"id\":\"publishing\",\"name\":\"Publishing\"}]},{\"id\":\"work\",\"name\":\"Work type\",\"options\":[{\"id\":\"analysis\",\"name\":\"Module analysis\"}]},{\"id\":\"module\",\"name\":\"Module ID\"},{\"id\":\"type\",\"name\":\"Module type\",\"options\":[{\"id\":\"res\",\"name\":\"res\"}]},{\"id\":\"domain\",\"name\":\"Domain\"},{\"id\":\"version\",\"name\":\"Source version\"},{\"id\":\"sha\",\"name\":\"Source SHA\"},{\"id\":\"date\",\"name\":\"Last analyzed\"},{\"id\":\"outcome\",\"name\":\"Outcome\",\"options\":[{\"id\":\"unassessed\",\"name\":\"Unassessed\"}]},{\"id\":\"run\",\"name\":\"Run URL\"},{\"id\":\"published\",\"name\":\"Published commit\"}]}}}}")
			}
			return jsonResponse(`{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"ITEM"}}}}`)
		default:
			t.Fatalf("unexpected call %s", request.URL.Path)
			return nil, nil
		}
	})}}
	projection := Projection{
		ModuleID: "avm-res-resources-resourcegroup", ModuleType: "res", Domain: "resources", Version: "v0.4.0",
		SourceSHA: strings.Repeat("a", 40), FactoryVersion: "2.0.0", Status: StatusPublishing, Outcome: OutcomeUnassessed,
		WorkType: "Module analysis", RunURL: "https://github.com/softwareone-public/avm-intelligence/actions/runs/1",
		LastAnalyzed: time.Now().UTC(), IssueTitle: "Module analysis: avm-res-resources-resourcegroup",
		IssueBody: "<!-- " + Marker + "avm-res-resources-resourcegroup -->",
	}
	issue, err := client.Upsert(context.Background(), "softwareone-public", "avm-intelligence", 1, projection)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Number != 7 || len(calls) < 5 {
		t.Fatalf("issue = %#v calls = %#v", issue, calls)
	}
}

func TestUpsertRejectsDoneBeforeGitHubCall(t *testing.T) {
	t.Parallel()
	client := Client{Token: "token", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		t.Fatal("GitHub was called for an invalid projection")
		return nil, nil
	})}}
	_, err := client.Upsert(context.Background(), "softwareone-public", "avm-intelligence", 1, Projection{Status: StatusDone})
	if err == nil {
		t.Fatal("done projection was accepted")
	}
}

func TestFindIssueFiltersSubstringMarkers(t *testing.T) {
	t.Parallel()
	client := Client{Token: "token", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(`{"total_count":2,"incomplete_results":false,"items":[{"number":1,"node_id":"PREFIX","html_url":"https://github.com/example/1","body":"<!-- factory-module-id:avm-ptn-alz-management -->"},{"number":2,"node_id":"EXACT","html_url":"https://github.com/example/2","body":"<!-- factory-module-id:avm-ptn-alz -->"}]}`)
	})}}
	issue, err := client.findIssue(context.Background(), "softwareone-public", "avm-intelligence", "avm-ptn-alz")
	if err != nil {
		t.Fatal(err)
	}
	if issue.Number != 2 {
		t.Fatalf("issue = %#v", issue)
	}
}

func TestFleetSummaryMatchesSkipsUnchangedProjection(t *testing.T) {
	t.Parallel()
	projection := fleetTestProjection()
	projection.Status = StatusDone
	projection.Published = strings.Repeat("b", 40)
	projection.IssueBody = projection.body()
	if fleetSummaryMatches(projection.IssueBody, projection) {
		t.Fatal("unacknowledged issue body was treated as a complete projection")
	}
	const acknowledgementMarker = "<!-- factory-projection-ack -->"
	acknowledgedBody := strings.Replace(projection.IssueBody, SummaryEnd, acknowledgementMarker+"\n"+SummaryEnd, 1)
	if !fleetSummaryMatches(acknowledgedBody, projection) {
		t.Fatal("acknowledged projection summary was not recognized")
	}
	changedCommit := projection
	changedCommit.Published = strings.Repeat("c", 40)
	if fleetSummaryMatches(acknowledgedBody, changedCommit) {
		t.Fatal("changed published commit was skipped")
	}
	changedRun := strings.Replace(acknowledgedBody, "- Run: "+projection.RunURL, "- Run: https://example.invalid/other-run", 1)
	if fleetSummaryMatches(changedRun, projection) {
		t.Fatal("changed factory-owned run field was treated as exact")
	}
	withExtraFactoryContent := strings.Replace(acknowledgedBody, SummaryEnd, "- Unexpected: `value`\n"+SummaryEnd, 1)
	if fleetSummaryMatches(withExtraFactoryContent, projection) {
		t.Fatal("extra content inside the factory-owned summary was treated as exact")
	}
}

func TestUpsertFleetRepairsAfterIssueThenProjectOrLabelFailure(t *testing.T) {
	t.Parallel()
	for _, failurePoint := range []string{"project", "labels", "issue-response-lost", "label-response-lost"} {
		failurePoint := failurePoint
		t.Run(failurePoint, func(t *testing.T) {
			t.Parallel()
			assertFleetProjectionRecovery(t, failurePoint)
		})
	}
}

func TestManagedLabelCleanupFailsClosedBeforeLabelWrites(t *testing.T) {
	requests := 0
	client := Client{
		Token: "fixture",
		HTTPClient: &http.Client{Transport: countingRoundTripper(func(*http.Request) (*http.Response, error) {
			requests++
			return nil, fmt.Errorf("unexpected request")
		})},
	}
	stale := make([]string, maxManagedLabelCleanupPerIssue+1)
	for index := range stale {
		stale[index] = fmt.Sprintf("factory:stale-%d", index)
	}
	err := client.syncFactoryLabels(context.Background(), "owner", "repo", 1, []string{"factory:desired"}, stale)
	if err == nil || !strings.Contains(err.Error(), "managed label cleanup exceeds") {
		t.Fatalf("cleanup overflow error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("label cleanup overflow performed %d requests before failing", requests)
	}
}

type countingRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip countingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func assertFleetProjectionRecovery(t *testing.T, failurePoint string) {
	t.Helper()
	client, transport, projection := newFleetRecoveryClient(t, failurePoint)
	if _, err := client.UpsertFleet(context.Background(), "softwareone-public", "avm-intelligence", 1, projection); err == nil {
		t.Fatal("first projection unexpectedly succeeded")
	}
	const acknowledgementMarker = "<!-- factory-projection-ack -->"
	if !fleetSummaryContentMatches(transport.issueBody, projection) || strings.Contains(transport.issueBody, acknowledgementMarker) {
		t.Fatalf("first Issue update did not leave an unacknowledged matching summary: %s", transport.issueBody)
	}
	if failurePoint == "label-response-lost" && (!factoryLabelsMatch(transport.issueLabels, Labels(projection)) || !containsLabel(transport.issueLabels, "human:keep")) {
		t.Fatalf("lost label response did not preserve the applied factory labels and human label: %#v", transport.issueLabels)
	}
	projectWritesBeforeRetry, labelWritesBeforeRetry := transport.projectFieldWrites, transport.issueLabelWrites
	if _, err := client.UpsertFleet(context.Background(), "softwareone-public", "avm-intelligence", 1, projection); err != nil {
		t.Fatalf("retry did not repair projection: %v", err)
	}
	if transport.projectFieldWrites <= projectWritesBeforeRetry || transport.issueLabelWrites <= labelWritesBeforeRetry {
		t.Fatal("retry acknowledged the Issue body without repairing Project fields and labels")
	}
	if !strings.Contains(transport.issueBody, acknowledgementMarker) {
		t.Fatal("successful full projection did not record its acknowledgement")
	}
	assertAcknowledgedProjectionIsNoOp(t, client, transport, projection)
}

func assertAcknowledgedProjectionIsNoOp(t *testing.T, client Client, transport *fleetRecoveryTransport, projection Projection) {
	t.Helper()
	projectWrites, labelWrites, searches := transport.projectFieldWrites, transport.issueLabelWrites, transport.issueSearches
	if _, err := client.UpsertFleet(context.Background(), "softwareone-public", "avm-intelligence", 1, projection); err != nil {
		t.Fatalf("verified no-op failed: %v", err)
	}
	if transport.issueSearches != searches+1 || transport.projectFieldWrites != projectWrites || transport.issueLabelWrites != labelWrites {
		t.Fatal("fully acknowledged projection repeated Project or label repair work")
	}
}

func containsLabel(labels []string, label string) bool {
	for _, candidate := range labels {
		if candidate == label {
			return true
		}
	}
	return false
}

func fleetTestProjection() Projection {
	projection := Projection{
		ModuleID: "avm-res-compute-disk", ModuleType: "res", Domain: "compute",
		Version: "v1.0.0", SourceSHA: strings.Repeat("a", 40), FactoryVersion: "2.0.0",
		Status: StatusPublishing, Outcome: OutcomeUnassessed, WorkType: WorkTypeModuleAnalysis,
		RunURL:       "https://github.com/softwareone-public/avm-intelligence/actions/runs/1",
		LastAnalyzed: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		IssueTitle:   "Module analysis: avm-res-compute-disk",
	}
	projection.IssueBody = projection.body()
	return projection
}

func newFleetRecoveryClient(t *testing.T, failurePoint string) (Client, *fleetRecoveryTransport, Projection) {
	t.Helper()
	projection := fleetTestProjection()
	previousProjection := projection
	previousProjection.SourceSHA = strings.Repeat("b", 40)
	previousProjection.IssueBody = previousProjection.body()
	transport := &fleetRecoveryTransport{issueBody: previousProjection.IssueBody, issueLabels: []string{"human:keep"}, failurePoint: failurePoint}
	return Client{Token: "reduced-test-token", HTTPClient: &http.Client{Transport: transport}}, transport, projection
}

type fleetRecoveryTransport struct {
	issueBody          string
	issueLabels        []string
	failurePoint       string
	failed             bool
	requestCount       int
	issueSearches      int
	projectFieldWrites int
	issueLabelWrites   int
}

func (transport *fleetRecoveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.requestCount++
	if request.URL.Path == "/search/issues" {
		transport.issueSearches++
		items, err := json.Marshal(map[string]any{"total_count": 1, "incomplete_results": false, "items": []map[string]any{{
			"number": 7, "node_id": "ISSUE", "html_url": "https://github.com/softwareone-public/avm-intelligence/issues/7", "body": transport.issueBody,
		}}})
		if err != nil {
			return nil, err
		}
		return jsonResponse(string(items))
	}
	if request.URL.Path == "/graphql" {
		return transport.graphResponse(request)
	}
	if request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/issues/7/labels") {
		labels := make([]map[string]string, 0, len(transport.issueLabels))
		for _, label := range transport.issueLabels {
			labels = append(labels, map[string]string{"name": label})
		}
		encoded, err := json.Marshal(labels)
		if err != nil {
			return nil, err
		}
		return jsonResponse(string(encoded))
	}
	if request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/labels/") {
		return jsonResponse(`{"name":"existing"}`)
	}
	if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/issues/7/labels") {
		if transport.failurePoint == "labels" && !transport.failed {
			transport.failed = true
			return statusResponse(http.StatusUnprocessableEntity, `{"message":"label write failed"}`), nil
		}
		var payload struct {
			Labels []string `json:"labels"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		for _, label := range payload.Labels {
			if !containsLabel(transport.issueLabels, label) {
				transport.issueLabels = append(transport.issueLabels, label)
			}
		}
		transport.issueLabelWrites++
		if transport.failurePoint == "label-response-lost" && !transport.failed {
			transport.failed = true
			return nil, fmt.Errorf("fixture lost response after applying Issue labels")
		}
		return jsonResponse(`[]`)
	}
	return nil, fmt.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
}

func (transport *fleetRecoveryTransport) graphResponse(request *http.Request) (*http.Response, error) {
	var payload struct {
		Query     string `json:"query"`
		Variables struct {
			Body string `json:"body"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	switch {
	case strings.Contains(payload.Query, "updateIssue(input"):
		transport.issueBody = payload.Variables.Body
		if transport.failurePoint == "issue-response-lost" && !transport.failed {
			transport.failed = true
			return nil, fmt.Errorf("fixture lost response after updating Issue body")
		}
		return jsonResponse(`{"data":{"updateIssue":{"issue":{"number":7}}}}`)
	case strings.Contains(payload.Query, "projectV2(number"):
		return jsonResponse(`{"data":{"organization":{"projectV2":{"id":"PROJECT","title":"AVM Intelligence Factory"}}}}`)
	case strings.Contains(payload.Query, "addProjectV2ItemById"):
		return jsonResponse(`{"data":{"addProjectV2ItemById":{"item":{"id":"ITEM"}}}}`)
	case strings.Contains(payload.Query, "fields(first:50)"):
		return jsonResponse(fleetProjectFieldsJSON)
	case strings.Contains(payload.Query, "updateProjectV2ItemFieldValue"):
		transport.projectFieldWrites++
		if transport.failurePoint == "project" && !transport.failed {
			transport.failed = true
			return jsonResponse(`{"errors":[{"message":"project field write failed"}]}`)
		}
		return jsonResponse(`{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"ITEM"}}}}`)
	default:
		return nil, fmt.Errorf("unexpected GraphQL query: %s", payload.Query)
	}
}

const fleetProjectFieldsJSON = "{\"data\":{\"node\":{\"fields\":{\"nodes\":[{\"id\":\"status\",\"name\":\"Status\",\"options\":[{\"id\":\"publishing\",\"name\":\"Publishing\"}]},{\"id\":\"work\",\"name\":\"Work type\",\"options\":[{\"id\":\"analysis\",\"name\":\"Module analysis\"}]},{\"id\":\"module\",\"name\":\"Module ID\"},{\"id\":\"type\",\"name\":\"Module type\",\"options\":[{\"id\":\"res\",\"name\":\"res\"}]},{\"id\":\"domain\",\"name\":\"Domain\"},{\"id\":\"version\",\"name\":\"Source version\"},{\"id\":\"sha\",\"name\":\"Source SHA\"},{\"id\":\"date\",\"name\":\"Last analyzed\"},{\"id\":\"outcome\",\"name\":\"Outcome\",\"options\":[{\"id\":\"unassessed\",\"name\":\"Unassessed\"}]},{\"id\":\"run\",\"name\":\"Run URL\"},{\"id\":\"published\",\"name\":\"Published commit\"}]}}}}"

func TestRequestKeepsGitHubValidationMessageOutOfErrors(t *testing.T) {
	t.Parallel()
	client := Client{Token: "token", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnprocessableEntity,
			Body:       io.NopCloser(strings.NewReader(`{"message":"SECRET_RESPONSE_SENTINEL","errors":[{"resource":"Installation","field":"permissions.pull_requests","code":"invalid"}]}`)),
			Header:     make(http.Header),
		}, nil
	})}}
	err := client.rest(context.Background(), http.MethodPost, "/app/installations/1/access_tokens", map[string]string{"fixture": "safe"}, nil)
	status, ok := err.(*APIStatusError)
	if !ok || status.Status != http.StatusUnprocessableEntity {
		t.Fatalf("rest() error = %v", err)
	}
	if strings.Contains(err.Error(), "SECRET_RESPONSE_SENTINEL") {
		t.Fatalf("GitHub response message escaped the error: %v", err)
	}
	if got, want := status.SafeDiagnostic(), "GitHub API returned HTTP 422 (resource=Installation field=permissions.pull_requests code=invalid)"; got != want {
		t.Fatalf("SafeDiagnostic() = %q, want %q", got, want)
	}
}

func TestRequestStopsBeforeGitHubWhenProjectionBudgetIsExhausted(t *testing.T) {
	t.Parallel()
	calls := 0
	client := Client{
		Token:  "token",
		Budget: &ProjectionBudget{RESTRemaining: 0, RESTReserve: 0, SecondaryRemaining: 10},
		HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(`{}`)
		})},
	}
	err := client.rest(context.Background(), http.MethodGet, "/repos/softwareone-public/avm-intelligence/issues", nil, nil)
	if !errors.Is(err, ErrProjectionBudgetExhausted) || calls != 0 {
		t.Fatalf("request error=%v calls=%d, want budget pause before any network call", err, calls)
	}
}

func TestProjectionBudgetAccountsForResourcesAndReserve(t *testing.T) {
	t.Parallel()
	budget := &ProjectionBudget{
		RESTRemaining: 4, RESTReserve: 1, GraphQLRemaining: 3, GraphQLReserve: 1,
		SearchRemaining: 2, SearchReserve: 1, SecondaryRemaining: 10, SecondaryReserve: 1,
	}
	for range 3 {
		if err := budget.Take(ProjectionREST, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := budget.Take(ProjectionREST, time.Now()); !errors.Is(err, ErrProjectionBudgetExhausted) {
		t.Fatalf("REST budget error = %v, want reserve exhaustion", err)
	}
	if err := budget.Take(ProjectionGraphQL, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := budget.Take(ProjectionSearch, time.Now()); err != nil {
		t.Fatal(err)
	}
	if budget.Calls[ProjectionREST] != 3 || budget.Calls[ProjectionGraphQL] != 1 || budget.Calls[ProjectionSearch] != 1 {
		t.Fatalf("budget call accounting = %+v", budget.Calls)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func jsonResponse(body string) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

func statusResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
