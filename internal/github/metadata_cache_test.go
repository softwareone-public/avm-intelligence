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

func TestProjectItemSnapshotStopsAtReviewedPageLimit(t *testing.T) {
	pageCalls := 0
	client := Client{
		Token: "token", MetadataCache: &ProjectionMetadataCache{},
		HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
			pageCalls++
			body, err := json.Marshal(map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{
				"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": true, "endCursor": fmt.Sprintf("cursor-%d", pageCalls)},
			}}}})
			if err != nil {
				return nil, err
			}
			return jsonResponse(string(body))
		})},
	}
	_, err := client.readAllProjectItems(context.Background(), "PROJECT")
	if err == nil || !strings.Contains(err.Error(), "reviewed 10-page limit") {
		t.Fatalf("unbounded Project item pagination error = %v", err)
	}
	if pageCalls != maxProjectionProjectItemPages {
		t.Fatalf("Project item pages requested = %d, want bounded %d", pageCalls, maxProjectionProjectItemPages)
	}
}

func TestProjectItemSnapshotRejectsDuplicateIssueMembership(t *testing.T) {
	client := Client{
		Token: "token", MetadataCache: &ProjectionMetadataCache{},
		HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
			return jsonResponse(`{"data":{"node":{"items":{"nodes":[{"id":"ITEM-1","content":{"number":7},"fieldValues":{"nodes":[]}},{"id":"ITEM-2","content":{"number":7},"fieldValues":{"nodes":[]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`)
		})},
	}
	if _, err := client.readAllProjectItems(context.Background(), "PROJECT"); err == nil || !strings.Contains(err.Error(), "duplicate items") {
		t.Fatalf("duplicate Project membership error = %v", err)
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

func TestExactModuleIssueSearchRejectsIncompleteResultSet(t *testing.T) {
	for name, body := range map[string]string{
		"truncated":  `{"total_count":2,"incomplete_results":false,"items":[{"number":1,"node_id":"ISSUE","html_url":"https://github.com/example/issues/1","body":"<!-- factory-module-id:avm-res-example -->"}]}`,
		"incomplete": `{"total_count":1,"incomplete_results":true,"items":[{"number":1,"node_id":"ISSUE","html_url":"https://github.com/example/issues/1","body":"<!-- factory-module-id:avm-res-example -->"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			client := Client{Token: "token", HTTPClient: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				return jsonResponse(body)
			})}}
			if _, err := client.findIssueRemote(context.Background(), "softwareone-public", "avm-intelligence", "avm-res-example"); err == nil || !strings.Contains(err.Error(), "incomplete") {
				t.Fatalf("incomplete exact Issue search error = %v", err)
			}
		})
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

func TestProjectFieldLostResponseIsReadBackBeforeRetry(t *testing.T) {
	projection := fleetTestProjection()
	values := desiredProjectFieldValues(projection)
	values["Status"] = StatusBacklog
	delete(values, "Published commit")
	fields := make(map[string]fieldRef, len(values))
	for name, value := range desiredProjectFieldValues(projection) {
		options := map[string]string{}
		if name == "Status" || name == "Work type" || name == "Module type" || name == "Outcome" {
			options[value] = value
		}
		if name == "Status" {
			options[StatusBacklog] = StatusBacklog
		}
		fields[name] = fieldRef{ID: name, Options: options}
	}
	cache := &ProjectionMetadataCache{}
	cache.setProjectItems("PROJECT", map[int]ProjectItemSnapshot{7: {ID: "ITEM", Number: 7, Values: values}})
	transport := &projectFieldLostResponseTransport{values: values}
	client := Client{Token: "token", MetadataCache: cache, HTTPClient: &http.Client{Transport: transport}}
	if err := client.setFields(context.Background(), "PROJECT", "ITEM", fields, projection); err == nil {
		t.Fatal("first Project field response was not lost")
	}
	page, err := client.readProjectItemsPage(context.Background(), "PROJECT", "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Values["Status"] != projection.Status {
		t.Fatalf("Project field readback = %+v, error=%v", page, err)
	}
	cache.setProjectItems("PROJECT", map[int]ProjectItemSnapshot{7: page.Items[0]})
	if err = client.setFields(context.Background(), "PROJECT", "ITEM", fields, projection); err != nil {
		t.Fatal(err)
	}
	if transport.fieldWrites != 1 {
		t.Fatalf("readback retry repeated %d field mutations, want only the lost-response attempt", transport.fieldWrites)
	}
}

func TestSetFieldsClearsStaleValueWhenDesiredValueIsEmpty(t *testing.T) {
	projection := fleetTestProjection()
	values := desiredProjectFieldValues(projection)
	values["Published commit"] = strings.Repeat("b", 40)
	fields := make(map[string]fieldRef, len(values))
	for name, value := range desiredProjectFieldValues(projection) {
		options := map[string]string{}
		if name == "Status" || name == "Work type" || name == "Module type" || name == "Outcome" {
			options[value] = value
		}
		fields[name] = fieldRef{ID: name, Options: options}
	}
	cache := &ProjectionMetadataCache{}
	cache.setProjectItems("PROJECT", map[int]ProjectItemSnapshot{7: {ID: "ITEM", Number: 7, Values: values}})
	writes := 0
	client := Client{Token: "token", MetadataCache: cache, HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		writes++
		return jsonResponse(`{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"ITEM"}}}}`)
	})}}
	if err := client.setFields(context.Background(), "PROJECT", "ITEM", fields, projection); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("stale value clear performed %d mutations, want one", writes)
	}
}

func TestProjectItemCreateLostResponseIsAdoptedOnNextRun(t *testing.T) {
	projection := fleetTestProjection()
	issue := Issue{Number: 7, NodeID: "ISSUE", URL: "https://github.com/example/issues/7", Body: projection.IssueBody}
	transport := &projectItemLostResponseTransport{}
	firstCache := projectItemRecoveryCache(projection, issue, true)
	first := Client{Token: "token", MetadataCache: firstCache, HTTPClient: &http.Client{Transport: transport}}
	if _, err := first.Upsert(context.Background(), "softwareone-public", "avm-intelligence", 1, projection); err == nil {
		t.Fatal("first Project item create response was not lost")
	}
	if transport.addCalls != 1 || !transport.itemExists {
		t.Fatalf("ambiguous Project item effect calls=%d exists=%t", transport.addCalls, transport.itemExists)
	}
	secondCache := projectItemRecoveryCache(projection, issue, false)
	second := Client{Token: "token", MetadataCache: secondCache, HTTPClient: &http.Client{Transport: transport}}
	if _, err := second.Upsert(context.Background(), "softwareone-public", "avm-intelligence", 1, projection); err != nil {
		t.Fatalf("next run did not reconcile the existing Project item: %v", err)
	}
	if transport.addCalls != 1 {
		t.Fatalf("retry created %d Project items, want to adopt the existing item", transport.addCalls)
	}
}

func projectItemRecoveryCache(projection Projection, issue Issue, includeEmptyItems bool) *ProjectionMetadataCache {
	cache := &ProjectionMetadataCache{}
	cache.setProject("softwareone-public:1", "PROJECT")
	fields := make(map[string]fieldRef)
	for name, value := range desiredProjectFieldValues(projection) {
		options := map[string]string{}
		if name == "Status" || name == "Work type" || name == "Module type" || name == "Outcome" {
			options[value] = value
		}
		fields[name] = fieldRef{ID: name, Options: options}
	}
	cache.setProjectFields("PROJECT", fields)
	if includeEmptyItems {
		cache.setProjectItems("PROJECT", map[int]ProjectItemSnapshot{})
	}
	cache.setIssueIndex("softwareone-public/avm-intelligence", map[string][]Issue{projection.ModuleID: {issue}})
	return cache
}

type projectItemLostResponseTransport struct {
	itemExists bool
	addCalls   int
	values     map[string]string
}

func (transport *projectItemLostResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Path != "/graphql" {
		return nil, fmt.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
	}
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
		return jsonResponse(`{"data":{"updateIssue":{"issue":{"number":7}}}}`)
	case strings.Contains(payload.Query, "addProjectV2ItemById"):
		transport.addCalls++
		transport.itemExists = true
		return nil, fmt.Errorf("fixture lost response after adding Project item")
	case strings.Contains(payload.Query, "items(first:100"):
		items := []any{}
		if transport.itemExists {
			fields := []any{}
			for name, value := range transport.values {
				fields = append(fields, map[string]any{"field": map[string]string{"name": name}, "text": value})
			}
			items = append(items, map[string]any{"id": "ITEM", "content": map[string]int{"number": 7}, "fieldValues": map[string]any{"nodes": fields}})
		}
		response := map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{"nodes": items, "pageInfo": map[string]any{"hasNextPage": false}}}}}
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		return jsonResponse(string(encoded))
	case strings.Contains(payload.Query, "updateProjectV2ItemFieldValue"):
		return jsonResponse(`{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"ITEM"}}}}`)
	default:
		return nil, fmt.Errorf("unexpected Project item recovery query")
	}
}

type projectFieldLostResponseTransport struct {
	values      map[string]string
	fieldWrites int
}

func (transport *projectFieldLostResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Path != "/graphql" {
		return nil, fmt.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
	}
	var payload struct {
		Query     string `json:"query"`
		Variables struct {
			Input struct {
				FieldID string `json:"fieldId"`
			} `json:"input"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if strings.Contains(payload.Query, "updateProjectV2ItemFieldValue") {
		transport.fieldWrites++
		transport.values[payload.Variables.Input.FieldID] = StatusPublishing
		return nil, fmt.Errorf("fixture lost response after applying Project field")
	}
	if strings.Contains(payload.Query, "items(first:100") {
		fieldValues := make([]map[string]any, 0, len(transport.values))
		for name, value := range transport.values {
			field := map[string]any{"field": map[string]string{"name": name}}
			if name == "Status" || name == "Work type" || name == "Module type" || name == "Outcome" {
				field["name"] = value
			} else if name == "Last analyzed" {
				field["date"] = value
			} else {
				field["text"] = value
			}
			fieldValues = append(fieldValues, field)
		}
		response := map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{
			"nodes":    []any{map[string]any{"id": "ITEM", "content": map[string]int{"number": 7}, "fieldValues": map[string]any{"nodes": fieldValues}}},
			"pageInfo": map[string]any{"hasNextPage": false},
		}}}}
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		return jsonResponse(string(encoded))
	}
	return nil, fmt.Errorf("unexpected Project GraphQL query")
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
