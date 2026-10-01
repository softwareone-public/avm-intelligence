package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

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
			return jsonResponse(`{"items":[]}`)
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
				return jsonResponse(`{"data":{"node":{"fields":{"nodes":[{"id":"status","name":"Status","options":[{"id":"publishing","name":"Publishing"}]},{"id":"work","name":"Work type","options":[{"id":"analysis","name":"Module analysis"}]},{"id":"module","name":"Module ID"},{"id":"type","name":"Module type","options":[{"id":"res","name":"res"}]},{"id":"domain","name":"Domain"},{"id":"version","name":"Source version"},{"id":"sha","name":"Source SHA"},{"id":"cli","name":"IaCLens version"},{"id":"date","name":"Last analyzed"},{"id":"outcome","name":"Outcome","options":[{"id":"unassessed","name":"Unassessed"}]},{"id":"run","name":"Run URL"}]}}}}`)
			}
			return jsonResponse(`{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"ITEM"}}}}`)
		default:
			t.Fatalf("unexpected call %s", request.URL.Path)
			return nil, nil
		}
	})}}
	projection := Projection{
		ModuleID: "avm-res-resources-resourcegroup", ModuleType: "res", Domain: "resources", Version: "v0.4.0",
		SourceSHA: strings.Repeat("a", 40), CLIVersion: "0.1.0", Status: StatusPublishing, Outcome: OutcomeUnassessed,
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

func TestRequestIncludesGitHubValidationMessage(t *testing.T) {
	t.Parallel()
	client := Client{Token: "token", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnprocessableEntity,
			Body:       io.NopCloser(strings.NewReader(`{"message":"Resource not accessible by integration"}`)),
			Header:     make(http.Header),
		}, nil
	})}}
	err := client.rest(context.Background(), http.MethodPost, "/app/installations/1/access_tokens", map[string]string{"fixture": "safe"}, nil)
	if err == nil || !strings.Contains(err.Error(), "Resource not accessible by integration") {
		t.Fatalf("rest() error = %v", err)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func jsonResponse(body string) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}
