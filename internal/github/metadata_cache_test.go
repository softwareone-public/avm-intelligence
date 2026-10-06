package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestIssueSnapshotPaginatesAndExactFiltersMarkers(t *testing.T) {
	pageCalls := 0
	client := Client{
		Token: "token", MetadataCache: &ProjectionMetadataCache{},
		HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
			pageCalls++
			pageNumber := request.URL.Query().Get("page")
			items := make([]map[string]any, 0, 100)
			if pageNumber == "1" {
				items = append(items, map[string]any{
					"number": 1, "node_id": "PREFIX", "html_url": "https://github.com/example/1",
					"body": "<!-- factory-module-id:avm-ptn-alz-management -->", "labels": []map[string]string{{"name": "human:keep"}},
				})
				for index := 2; index <= 100; index++ {
					items = append(items, map[string]any{"number": index, "node_id": fmt.Sprintf("ISSUE-%d", index), "html_url": "https://github.com/example/other", "body": "no module marker", "labels": []map[string]string{}})
				}
			} else {
				items = append(items, map[string]any{
					"number": 101, "node_id": "EXACT", "html_url": "https://github.com/example/101",
					"body": "<!-- factory-module-id:avm-ptn-alz -->", "labels": []map[string]string{{"name": "human:keep"}},
				})
			}
			body, err := json.Marshal(map[string]any{"total_count": 101, "incomplete_results": false, "items": items})
			if err != nil {
				return nil, err
			}
			return jsonResponse(string(body))
		})},
	}
	issue, err := client.findIssue(context.Background(), "softwareone-public", "avm-intelligence", "avm-ptn-alz")
	if err != nil {
		t.Fatal(err)
	}
	if issue.Number != 101 || issue.NodeID != "EXACT" || len(issue.Labels) != 1 || issue.Labels[0] != "human:keep" || pageCalls != 2 {
		t.Fatalf("exact paginated issue = %+v, page calls=%d", issue, pageCalls)
	}
	if strings.Contains(issue.Body, "alz-management") {
		t.Fatal("prefix marker was returned as the exact module issue")
	}
}

func TestIssueSnapshotRejectsIncompleteSearch(t *testing.T) {
	client := Client{Token: "token", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(`{"total_count":1500,"incomplete_results":false,"items":[]}`)
	})}}
	if _, err := client.ReadProjectionIssuePage(context.Background(), "softwareone-public", "avm-intelligence", 1); err == nil {
		t.Fatal("over-limit Issue snapshot was treated as complete")
	}
}

func TestReadProjectionBudgetSeparatesPrimaryResources(t *testing.T) {
	client := Client{Token: "token", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/rate_limit" {
			t.Fatalf("unexpected budget endpoint %s", request.URL.Path)
		}
		return jsonResponse(`{"resources":{"core":{"remaining":5000},"search":{"remaining":30},"graphql":{"remaining":4000}}}`)
	})}}
	budget, err := client.ReadProjectionBudget(context.Background(), ProjectionBudgetPolicy{
		RESTReserve: 50, GraphQLReserve: 100, SearchReserve: 5, SecondaryAllowance: 100, SecondaryReserve: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if budget.RESTRemaining != 5000 || budget.SearchRemaining != 30 || budget.GraphQLRemaining != 4000 || budget.SecondaryRemaining != 99 || budget.Calls[ProjectionREST] != 1 {
		t.Fatalf("projection rate limit budget = %+v", budget.Snapshot())
	}
}

func TestSetFieldsSkipsSnapshotMatchesAndWritesOnlyChangedFields(t *testing.T) {
	projection := fleetTestProjection()
	currentValues := map[string]string{
		"Status": projection.Status, "Work type": projection.WorkType, "Module ID": projection.ModuleID,
		"Module type": projection.ModuleType, "Domain": projection.Domain, "Source version": projection.Version,
		"Source SHA": projection.SourceSHA,
		"Outcome":    projection.Outcome, "Published commit": projection.Published, "Run URL": projection.RunURL,
		"Last analyzed": projection.LastAnalyzed.Format("2006-01-02"),
	}
	fields := make(map[string]fieldRef, len(currentValues))
	for name, value := range currentValues {
		options := map[string]string{}
		if name == "Status" || name == "Work type" || name == "Module type" || name == "Outcome" {
			options[value] = "option-" + name
		}
		fields[name] = fieldRef{ID: "field-" + name, Options: options}
	}
	cache := &ProjectionMetadataCache{}
	cache.setProjectItems("PROJECT", map[int]ProjectItemSnapshot{7: {ID: "ITEM", Number: 7, Values: currentValues}})
	calls := 0
	client := Client{Token: "token", MetadataCache: cache, HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(`{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"ITEM"}}}}`)
	})}}
	if err := client.setFields(context.Background(), "PROJECT", "ITEM", fields, projection); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("matching Project snapshot caused %d mutations, want zero", calls)
	}
	projection.Outcome = OutcomeFindings
	fields["Outcome"] = fieldRef{ID: "field-Outcome", Options: map[string]string{OutcomeFindings: "option-findings"}}
	if err := client.setFields(context.Background(), "PROJECT", "ITEM", fields, projection); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("one changed field caused %d mutations, want one", calls)
	}
}

func TestAcknowledgedIssueSummaryDoesNotHideProjectFieldDrift(t *testing.T) {
	client, transport, projection := newProjectDriftFixture()
	if _, err := client.UpsertFleet(context.Background(), "softwareone-public", "avm-intelligence", 1, projection); err != nil {
		t.Fatalf("Project field drift repair failed: %v", err)
	}
	if transport.fieldWrites != 1 || transport.issueWrites < 2 || transport.labelWrites != 1 {
		t.Fatalf("drift repair writes field=%d issue=%d labels=%d, want one field plus Issue and label reconciliation", transport.fieldWrites, transport.issueWrites, transport.labelWrites)
	}
}

func newProjectDriftFixture() (Client, *projectDriftTransport, Projection) {
	projection := fleetTestProjection()
	projection.IssueBody, _ = fleetProjectionAcknowledgement(projection.IssueBody)
	labels := Labels(projection)
	values := desiredProjectFieldValues(projection)
	values["Status"] = StatusBacklog
	fields := make(map[string]fieldRef, len(values))
	for name, value := range desiredProjectFieldValues(projection) {
		options := map[string]string{}
		if name == "Status" || name == "Work type" || name == "Module type" || name == "Outcome" {
			options[value] = "option-" + name
		}
		fields[name] = fieldRef{ID: "field-" + name, Options: options}
	}
	cache := &ProjectionMetadataCache{}
	cache.setProject("softwareone-public:1", "PROJECT")
	cache.setProjectFields("PROJECT", fields)
	cache.setProjectItems("PROJECT", map[int]ProjectItemSnapshot{7: {ID: "ITEM", Number: 7, Values: values}})
	cache.setIssueIndex("softwareone-public/avm-intelligence", map[string][]Issue{
		projection.ModuleID: {{Number: 7, NodeID: "ISSUE", URL: "https://github.com/example/issues/7", Body: projection.IssueBody, Labels: labels}},
	})
	transport := &projectDriftTransport{}
	client := Client{Token: "token", MetadataCache: cache, HTTPClient: &http.Client{Transport: transport}}
	return client, transport, projection
}

type projectDriftTransport struct {
	fieldWrites int
	issueWrites int
	labelWrites int
}

func (transport *projectDriftTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Path == "/graphql" {
		return transport.graphResponse(request)
	}
	if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/issues/7/labels") {
		transport.labelWrites++
		return jsonResponse(`[]`)
	}
	return nil, fmt.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
}

func (transport *projectDriftTransport) graphResponse(request *http.Request) (*http.Response, error) {
	var payload struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if strings.Contains(payload.Query, "updateProjectV2ItemFieldValue") {
		transport.fieldWrites++
		return jsonResponse(`{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"ITEM"}}}}`)
	}
	if strings.Contains(payload.Query, "updateIssue(input") {
		transport.issueWrites++
		return jsonResponse(`{"data":{"updateIssue":{"issue":{"number":7}}}}`)
	}
	return nil, fmt.Errorf("unexpected GraphQL query %s", payload.Query)
}
