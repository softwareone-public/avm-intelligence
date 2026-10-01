package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const graphQLEndpoint = "https://api.github.com/graphql"

// Client performs the minimum GitHub writes needed for one pilot projection.
// It never receives repository contents permission.
type Client struct {
	Endpoint   string
	Token      string
	HTTPClient *http.Client
}

// Issue is the stable module work item.
type Issue struct {
	Number int
	NodeID string
	URL    string
	Body   string
}

// MintToken exchanges the App private key for a one-hour token limited to this
// repository and the Issue and Project permissions needed for projection.
func MintToken(ctx context.Context, appID, privateKey string) (string, error) {
	if appID == "" || strings.TrimSpace(privateKey) == "" {
		return "", fmt.Errorf("GitHub App credentials are required")
	}
	key, err := parsePrivateKey(privateKey)
	if err != nil {
		return "", err
	}
	now := time.Now()
	claims := jwt.RegisteredClaims{Issuer: appID, IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute))}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	client := Client{Token: signed}
	var installation struct {
		ID int64 `json:"id"`
	}
	if err = client.rest(ctx, http.MethodGet, "/repos/softwareone-public/avm-intelligence/installation", nil, &installation); err != nil {
		return "", err
	}
	body := map[string]any{"repositories": []string{"avm-intelligence"}, "permissions": map[string]string{"issues": "write", "organization_projects": "write", "metadata": "read"}}
	var access struct {
		Token       string            `json:"token"`
		Permissions map[string]string `json:"permissions"`
	}
	path := "/app/installations/" + strconv.FormatInt(installation.ID, 10) + "/access_tokens"
	if err = client.rest(ctx, http.MethodPost, path, body, &access); err != nil {
		return "", err
	}
	if access.Permissions["contents"] != "" || access.Permissions["issues"] != "write" || access.Permissions["organization_projects"] != "write" {
		return "", fmt.Errorf("reduced token permissions were not honored")
	}
	return access.Token, nil
}

func parsePrivateKey(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, fmt.Errorf("GitHub App private key is not PEM")
	}
	parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err == nil {
		return parsed, nil
	}
	key, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if pkcs8Err != nil {
		return nil, fmt.Errorf("parse GitHub App private key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("GitHub App private key is not RSA")
	}
	return rsaKey, nil
}

// Upsert creates or updates the pilot Issue and its Project item. It refuses a
// projection that claims publication.
func (c Client) Upsert(ctx context.Context, owner, repo string, projectNumber int, projection Projection) (Issue, error) {
	if err := projection.Validate(); err != nil {
		return Issue{}, err
	}
	issue, err := c.findIssue(ctx, owner, repo, projection.ModuleID)
	if err != nil {
		return Issue{}, err
	}
	if issue.Number == 0 {
		issue, err = c.createIssue(ctx, owner, repo, projection)
	} else {
		err = c.updateIssue(ctx, issue.NodeID, issue.Body, projection)
	}
	if err != nil {
		return Issue{}, err
	}
	projectID, err := c.projectID(ctx, owner, projectNumber)
	if err != nil {
		return Issue{}, err
	}
	itemID, err := c.addItem(ctx, projectID, issue.NodeID)
	if err != nil {
		return Issue{}, err
	}
	fields, err := c.projectFields(ctx, projectID)
	if err != nil {
		return Issue{}, err
	}
	if err = c.setFields(ctx, projectID, itemID, fields, projection); err != nil {
		return Issue{}, err
	}
	return issue, nil
}

// UpsertFleet projects a fleet module and synchronizes only factory-owned
// labels. Human labels are never removed.
func (c Client) UpsertFleet(ctx context.Context, owner, repo string, projectNumber int, projection Projection) (Issue, error) {
	issue, err := c.Upsert(ctx, owner, repo, projectNumber, projection)
	if err != nil {
		return Issue{}, err
	}
	if err = c.syncFactoryLabels(ctx, owner, repo, issue.Number, Labels(projection)); err != nil {
		return Issue{}, err
	}
	return issue, nil
}

func (c Client) syncFactoryLabels(ctx context.Context, owner, repo string, issueNumber int, desired []string) error {
	var response []struct {
		Name string `json:"name"`
	}
	if err := c.rest(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues/%d/labels?per_page=100", owner, repo, issueNumber), nil, &response); err != nil {
		return err
	}
	desiredSet := map[string]bool{}
	for _, label := range desired {
		desiredSet[label] = true
		if err := c.ensureFactoryLabel(ctx, owner, repo, label); err != nil {
			return err
		}
	}
	if err := c.rest(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, repo, issueNumber), map[string]any{"labels": desired}, nil); err != nil {
		return err
	}
	for _, label := range response {
		if (strings.HasPrefix(label.Name, "factory:") || strings.HasPrefix(label.Name, "avm:")) && !desiredSet[label.Name] {
			path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels/%s", owner, repo, issueNumber, url.PathEscape(label.Name))
			if err := c.rest(ctx, http.MethodDelete, path, nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c Client) ensureFactoryLabel(ctx context.Context, owner, repo, label string) error {
	path := fmt.Sprintf("/repos/%s/%s/labels/%s", owner, repo, url.PathEscape(label))
	var existing map[string]any
	if err := c.rest(ctx, http.MethodGet, path, nil, &existing); err == nil {
		return nil
	} else if !strings.Contains(err.Error(), "github status 404") {
		return err
	}
	return c.rest(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/labels", owner, repo), map[string]string{"name": label, "color": labelColor(label)}, nil)
}

func labelColor(label string) string {
	if strings.HasPrefix(label, "avm:") {
		return "0e7490"
	}
	switch {
	case strings.Contains(label, "failed"), strings.Contains(label, "blocked"):
		return "b91c1c"
	case strings.Contains(label, "done"):
		return "15803d"
	default:
		return "2563eb"
	}
}

func (c Client) findIssue(ctx context.Context, owner, repo, moduleID string) (Issue, error) {
	var response struct {
		Items []struct {
			Number int    `json:"number"`
			NodeID string `json:"node_id"`
			URL    string `json:"html_url"`
			Body   string `json:"body"`
		} `json:"items"`
	}
	query := "repo:" + owner + "/" + repo + " is:issue in:body " + Marker + moduleID
	if err := c.rest(ctx, http.MethodGet, "/search/issues?q="+urlQuery(query), nil, &response); err != nil {
		return Issue{}, err
	}
	if len(response.Items) > 1 {
		return Issue{}, fmt.Errorf("multiple Issues matched %s", moduleID)
	}
	if len(response.Items) == 0 {
		return Issue{}, nil
	}
	item := response.Items[0]
	return Issue{Number: item.Number, NodeID: item.NodeID, URL: item.URL, Body: item.Body}, nil
}

func (c Client) createIssue(ctx context.Context, owner, repo string, projection Projection) (Issue, error) {
	body := map[string]string{"title": projection.IssueTitle, "body": projection.IssueBody}
	var response struct {
		Number int    `json:"number"`
		NodeID string `json:"node_id"`
		URL    string `json:"html_url"`
	}
	if err := c.rest(ctx, http.MethodPost, "/repos/"+owner+"/"+repo+"/issues", body, &response); err != nil {
		return Issue{}, err
	}
	return Issue{Number: response.Number, NodeID: response.NodeID, URL: response.URL}, nil
}

func (c Client) updateIssue(ctx context.Context, nodeID, existingBody string, projection Projection) error {
	body, err := mergeIssueBody(existingBody, projection.IssueBody)
	if err != nil {
		return err
	}
	payload := map[string]any{"query": `mutation($id:ID!,$title:String!,$body:String!){updateIssue(input:{id:$id,title:$title,body:$body}){issue{number}}}`, "variables": map[string]string{"id": nodeID, "title": projection.IssueTitle, "body": body}}
	return c.graph(ctx, payload, nil)
}

func (c Client) projectID(ctx context.Context, owner string, number int) (string, error) {
	payload := map[string]any{"query": `query($owner:String!,$number:Int!){organization(login:$owner){projectV2(number:$number){id title}}}`, "variables": map[string]any{"owner": owner, "number": number}}
	var response struct {
		Organization struct {
			Project struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"projectV2"`
		} `json:"organization"`
	}
	if err := c.graph(ctx, payload, &response); err != nil {
		return "", err
	}
	if response.Organization.Project.Title != "AVM Intelligence Factory" || response.Organization.Project.ID == "" {
		return "", fmt.Errorf("project %d is not the approved AVM Intelligence Factory", number)
	}
	return response.Organization.Project.ID, nil
}

func (c Client) addItem(ctx context.Context, projectID, issueNodeID string) (string, error) {
	payload := map[string]any{"query": `mutation($project:ID!,$content:ID!){addProjectV2ItemById(input:{projectId:$project,contentId:$content}){item{id}}}`, "variables": map[string]string{"project": projectID, "content": issueNodeID}}
	var response struct {
		Add struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"addProjectV2ItemById"`
	}
	if err := c.graph(ctx, payload, &response); err != nil {
		return "", err
	}
	if response.Add.Item.ID == "" {
		return "", fmt.Errorf("project item was not created")
	}
	return response.Add.Item.ID, nil
}

type fieldRef struct {
	ID      string
	Options map[string]string
}

func (c Client) projectFields(ctx context.Context, projectID string) (map[string]fieldRef, error) {
	payload := map[string]any{"query": `query($id:ID!){node(id:$id){... on ProjectV2{fields(first:50){nodes{... on ProjectV2FieldCommon{id name}... on ProjectV2SingleSelectField{options{id name}}}}}}}`, "variables": map[string]string{"id": projectID}}
	var response struct {
		Node struct {
			Fields struct {
				Nodes []struct {
					ID      string `json:"id"`
					Name    string `json:"name"`
					Options []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"options"`
				} `json:"nodes"`
			} `json:"fields"`
		} `json:"node"`
	}
	if err := c.graph(ctx, payload, &response); err != nil {
		return nil, err
	}
	fields := map[string]fieldRef{}
	for _, node := range response.Node.Fields.Nodes {
		ref := fieldRef{ID: node.ID, Options: map[string]string{}}
		for _, option := range node.Options {
			ref.Options[option.Name] = option.ID
		}
		fields[node.Name] = ref
	}
	return fields, nil
}

func (c Client) setFields(ctx context.Context, projectID, itemID string, fields map[string]fieldRef, projection Projection) error {
	values := map[string]string{
		"Status": projection.Status, "Work type": projection.WorkType, "Module ID": projection.ModuleID,
		"Module type": projection.ModuleType, "Domain": projection.Domain, "Source version": projection.Version,
		"Source SHA": projection.SourceSHA, "IaCLens version": projection.CLIVersion,
		"Outcome": projection.Outcome,
		"Run URL": projection.RunURL,
	}
	if !projection.LastAnalyzed.IsZero() {
		values["Last analyzed"] = projection.LastAnalyzed.Format("2006-01-02")
	}
	for name, value := range values {
		field, ok := fields[name]
		if !ok {
			return fmt.Errorf("project field %q was not found", name)
		}
		input := map[string]any{"projectId": projectID, "itemId": itemID, "fieldId": field.ID}
		if len(field.Options) > 0 {
			optionID, ok := field.Options[value]
			if !ok {
				return fmt.Errorf("project option %q was not found in %s", value, name)
			}
			input["value"] = map[string]string{"singleSelectOptionId": optionID}
		} else if name == "Last analyzed" {
			input["value"] = map[string]string{"date": value}
		} else {
			input["value"] = map[string]string{"text": value}
		}
		payload := map[string]any{"query": `mutation($input:UpdateProjectV2ItemFieldValueInput!){updateProjectV2ItemFieldValue(input:$input){projectV2Item{id}}}`, "variables": map[string]any{"input": input}}
		if err := c.graph(ctx, payload, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c Client) rest(ctx context.Context, method, path string, payload any, out any) error {
	return c.request(ctx, method, "https://api.github.com"+path, payload, out)
}

func (c Client) graph(ctx context.Context, payload any, data any) error {
	var response struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.request(ctx, http.MethodPost, graphQLEndpoint, payload, &response); err != nil {
		return err
	}
	if len(response.Errors) > 0 {
		return fmt.Errorf("github graphql: %s", response.Errors[0].Message)
	}
	if data == nil || len(response.Data) == 0 {
		return nil
	}
	return json.Unmarshal(response.Data, data)
}

func (c Client) request(ctx context.Context, method, endpoint string, payload any, out any) error {
	if strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("reduced GitHub token is required")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("Content-Type", "application/json")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(responseBody, &apiError); err == nil && apiError.Message != "" {
			return fmt.Errorf("github status %d: %s", response.StatusCode, apiError.Message)
		}
		return fmt.Errorf("github status %d", response.StatusCode)
	}
	if out == nil || len(responseBody) == 0 {
		return nil
	}
	return json.Unmarshal(responseBody, out)
}

func urlQuery(value string) string {
	replacer := strings.NewReplacer(" ", "%20", ":", "%3A", "/", "%2F")
	return replacer.Replace(value)
}
