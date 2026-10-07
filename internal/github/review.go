package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

// ReviewClient owns review-PR delivery separately from reduced board tokens.
// It never merges pull requests or updates repository refs.
type ReviewClient struct {
	Token           string
	Endpoint        string
	HTTPClient      *http.Client
	ValidatePayload func(context.Context, EvidenceReview) error
}

// EvidenceReview identifies the exact branch and base validated by a producer.
type EvidenceReview struct {
	Owner      string
	Repository string
	ProposalID string
	Branch     string
	HeadSHA    string
	BaseSHA    string
}

type reviewPullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Head   struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

// EnsureEvidenceReview creates or reuses only an exact proposal PR. A replay
// after lost creation acknowledgement finds the PR before issuing another POST.
func (client ReviewClient) EnsureEvidenceReview(ctx context.Context, input EvidenceReview) (int, string, error) {
	if err := identity.ValidateRecordID("proposal", input.ProposalID); err != nil {
		return 0, "", err
	}
	if !evidenceIdentityPattern.MatchString(input.Owner) || !evidenceIdentityPattern.MatchString(input.Repository) || !evidenceSourceSHAPattern.MatchString(input.HeadSHA) || !evidenceSourceSHAPattern.MatchString(input.BaseSHA) || input.Branch != "factory/evidence-delivery-"+input.ProposalID {
		return 0, "", fmt.Errorf("review delivery identities are invalid")
	}
	if client.ValidatePayload == nil {
		return 0, "", fmt.Errorf("review delivery requires trusted exact-head payload validation")
	}
	if err := client.ValidatePayload(ctx, input); err != nil {
		return 0, "", fmt.Errorf("review delivery payload validation failed")
	}
	root := "/repos/" + input.Owner + "/" + input.Repository
	if err := client.verifyReviewRefs(ctx, root, input); err != nil {
		return 0, "", err
	}
	query := url.Values{"state": {"all"}, "head": {input.Owner + ":" + input.Branch}, "base": {"main"}, "per_page": {"100"}}
	var matches []reviewPullRequest
	if err := client.reviewRequest(ctx, http.MethodGet, root+"/pulls?"+query.Encode(), nil, &matches); err != nil {
		return 0, "", err
	}
	if matches == nil {
		return 0, "", fmt.Errorf("review delivery lookup did not return an array")
	}
	if len(matches) == 100 || len(matches) > 1 {
		return 0, "", fmt.Errorf("review delivery lookup is ambiguous or incomplete")
	}
	marker := "<!-- factory-evidence-proposal:" + input.ProposalID + " -->"
	if len(matches) == 1 {
		if err := validateEvidenceReview(matches[0], input, marker); err != nil {
			return 0, "", err
		}
		return matches[0].Number, matches[0].URL, nil
	}
	if err := client.verifyReviewRefs(ctx, root, input); err != nil {
		return 0, "", err
	}
	if err := client.ValidatePayload(ctx, input); err != nil {
		return 0, "", fmt.Errorf("review delivery payload validation failed")
	}
	var created reviewPullRequest
	payload := map[string]any{"title": "data: review sealed module evidence", "head": input.Branch, "base": "main", "draft": false,
		"body": marker + "\n\nSealed acquisition evidence prepared against " + input.BaseSHA + ". Human review and verified merge-receipt import are required. No automatic merge or selection is requested."}
	if err := client.reviewRequest(ctx, http.MethodPost, root+"/pulls", payload, &created); err != nil {
		return 0, "", err
	}
	if err := validateEvidenceReview(created, input, marker); err != nil {
		return 0, "", err
	}
	return created.Number, created.URL, nil
}

func validateEvidenceReview(review reviewPullRequest, input EvidenceReview, marker string) error {
	repository := input.Owner + "/" + input.Repository
	expectedURL := fmt.Sprintf("https://github.com/%s/pull/%d", repository, review.Number)
	if review.Number < 1 || review.URL != expectedURL || review.State != "open" || review.Head.Ref != input.Branch || review.Head.SHA != input.HeadSHA || review.Head.Repo.FullName != repository || review.Base.Ref != "main" || review.Base.SHA != input.BaseSHA || review.Base.Repo.FullName != repository || !strings.Contains(review.Body, marker) {
		return fmt.Errorf("existing or created review does not match exact proposal delivery")
	}
	return nil
}

func (client ReviewClient) verifyReviewRefs(ctx context.Context, root string, input EvidenceReview) error {
	for _, target := range []struct{ ref, sha string }{{"main", input.BaseSHA}, {input.Branch, input.HeadSHA}} {
		var ref struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		if err := client.reviewRequest(ctx, http.MethodGet, root+"/git/ref/heads/"+target.ref, nil, &ref); err != nil {
			return err
		}
		if ref.Object.SHA != target.sha {
			return fmt.Errorf("review delivery branch or base moved")
		}
	}
	return nil
}

func (client ReviewClient) reviewRequest(ctx context.Context, method, path string, payload any, output any) error {
	if client.Token == "" {
		return fmt.Errorf("review delivery token is required")
	}
	endpoint := client.Endpoint
	if endpoint == "" {
		endpoint = "https://api.github.com"
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(endpoint, "/")+path, body)
	if err != nil {
		return fmt.Errorf("construct review request")
	}
	request.Header.Set("Authorization", "Bearer "+client.Token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Content-Type", "application/json")
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("review request transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("review request failed with HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1048577))
	if err != nil || len(data) > 1048576 {
		return fmt.Errorf("review response exceeds bounds or cannot be read")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("review response cannot be decoded")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("review response contains trailing data")
	}
	return nil
}
