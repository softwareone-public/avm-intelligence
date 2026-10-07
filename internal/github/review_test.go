package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEvidenceReviewCreationReplayAndConflictingHead(t *testing.T) {
	input := EvidenceReview{Owner: "owner", Repository: "repository", ProposalID: "proposal-" + strings.Repeat("a", 64), HeadSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("c", 40)}
	input.Branch = "factory/evidence-delivery-" + input.ProposalID
	var stored *reviewPullRequest
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "/git/ref/") {
			sha := input.HeadSHA
			if strings.HasSuffix(request.URL.Path, "/main") {
				sha = input.BaseSHA
			}
			json.NewEncoder(writer).Encode(map[string]any{"object": map[string]string{"sha": sha}})
			return
		}
		if request.Method == http.MethodGet {
			if stored == nil {
				json.NewEncoder(writer).Encode([]reviewPullRequest{})
			} else {
				json.NewEncoder(writer).Encode([]reviewPullRequest{*stored})
			}
			return
		}
		posts++
		var payload map[string]any
		json.NewDecoder(request.Body).Decode(&payload)
		created := reviewPullRequest{Number: 1, URL: "https://github.com/owner/repository/pull/1", Body: payload["body"].(string), State: "open"}
		created.Head.Ref, created.Head.SHA, created.Head.Repo.FullName = input.Branch, input.HeadSHA, "owner/repository"
		created.Base.Ref, created.Base.SHA, created.Base.Repo.FullName = "main", input.BaseSHA, "owner/repository"
		stored = &created
		json.NewEncoder(writer).Encode(created)
	}))
	defer server.Close()
	client := ReviewClient{Token: "fixture", Endpoint: server.URL, HTTPClient: server.Client(), ValidatePayload: func(ctx context.Context, actual EvidenceReview) error {
		if actual != input {
			return errors.New("mismatched payload identity")
		}
		return nil
	}}
	for iteration := 0; iteration < 2; iteration++ {
		number, _, err := client.EnsureEvidenceReview(t.Context(), input)
		if err != nil || number != 1 {
			t.Fatalf("review creation/replay = %d %v", number, err)
		}
	}
	if posts != 1 {
		t.Fatalf("replay created %d PRs", posts)
	}
	client.ValidatePayload = func(context.Context, EvidenceReview) error { return errors.New("extra code change") }
	if _, _, err := client.EnsureEvidenceReview(t.Context(), input); err == nil || posts != 1 {
		t.Fatal("invalid payload reached PR creation")
	}
	client.ValidatePayload = func(context.Context, EvidenceReview) error { return nil }
	stored.Head.SHA = strings.Repeat("d", 40)
	if _, _, err := client.EnsureEvidenceReview(t.Context(), input); err == nil {
		t.Fatal("conflicting PR head reused")
	}
}

func TestReviewResponseRejectsMalformedLookupWithoutLeakingValues(t *testing.T) {
	for _, response := range []string{"null", "[] trailing", "[] {}", "[", `[{"number":999999999999999999999999999999999999999999}]`} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Write([]byte(response))
		}))
		client := ReviewClient{Token: "fixture", Endpoint: server.URL, HTTPClient: server.Client()}
		var matches []reviewPullRequest
		err := client.reviewRequest(t.Context(), http.MethodGet, "/pulls", nil, &matches)
		server.Close()
		if response == "null" {
			if err != nil || matches != nil {
				t.Fatal("null response fixture did not preserve null for lookup rejection")
			}
			continue
		}
		if err == nil || strings.Contains(err.Error(), "999999") || strings.Contains(err.Error(), "trailing\"") {
			t.Fatalf("malformed response was accepted or leaked value: %v", err)
		}
	}
}
