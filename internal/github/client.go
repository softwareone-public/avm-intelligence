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
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
)

const graphQLEndpoint = "https://api.github.com/graphql"
const fleetProjectionAckMarker = "<!-- factory-projection-ack -->"

// Client performs bounded GitHub writes for native module projections.
// It never receives repository contents permission.
type Client struct {
	Endpoint      string
	Token         string
	HTTPClient    *http.Client
	Budget        *ProjectionBudget
	MetadataCache *ProjectionMetadataCache
}

// Issue is the stable module work item.
type Issue struct {
	Number        int
	NodeID        string
	URL           string
	Body          string
	ProjectItemID string
	Labels        []string
}

type installationAccessToken struct {
	Token       string            `json:"token"`
	Permissions map[string]string `json:"permissions"`
}

// MintToken exchanges the App private key for a repository token limited to
// the Issue and Project permissions needed for projection.
func MintToken(ctx context.Context, appID, privateKey string) (string, error) {
	access, err := mintInstallationToken(ctx, appID, privateKey, map[string]string{"issues": "write", "organization_projects": "write", "metadata": "read"})
	if err != nil {
		return "", err
	}
	if access.Permissions["contents"] != "" || access.Permissions["issues"] != "write" || access.Permissions["organization_projects"] != "write" || access.Token == "" {
		return "", fmt.Errorf("reduced token permissions were not honored")
	}
	return access.Token, nil
}

// MintStateToken requests only repository Contents write and Metadata read for
// the dedicated operational-state branch. It does not grant Actions or bypass.
func MintStateToken(ctx context.Context, appID, privateKey string) (string, error) {
	access, err := mintInstallationToken(ctx, appID, privateKey, map[string]string{"contents": "write", "metadata": "read"})
	if err != nil {
		return "", err
	}
	if access.Permissions["contents"] != "write" || access.Permissions["issues"] != "" || access.Permissions["organization_projects"] != "" || access.Permissions["actions"] != "" || access.Token == "" {
		return "", fmt.Errorf("state token permissions were not honored")
	}
	return access.Token, nil
}

func mintInstallationToken(ctx context.Context, appID, privateKey string, permissions map[string]string) (installationAccessToken, error) {
	var access installationAccessToken
	if appID == "" || strings.TrimSpace(privateKey) == "" {
		return access, fmt.Errorf("GitHub App credentials are required")
	}
	key, err := parsePrivateKey(privateKey)
	if err != nil {
		return access, err
	}
	now := time.Now()
	claims := jwt.RegisteredClaims{Issuer: appID, IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute))}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		return access, fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	client := Client{Token: signed}
	var installation struct {
		ID int64 `json:"id"`
	}
	if err = client.rest(ctx, http.MethodGet, "/repos/softwareone-public/avm-intelligence/installation", nil, &installation); err != nil {
		return access, err
	}
	body := map[string]any{"repositories": []string{"avm-intelligence"}, "permissions": permissions}
	path := "/app/installations/" + strconv.FormatInt(installation.ID, 10) + "/access_tokens"
	if err = client.rest(ctx, http.MethodPost, path, body, &access); err != nil {
		return access, err
	}
	return access, nil
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

// Upsert creates or updates the recurring module Issue and its Project item. It refuses a
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
		if err == nil {
			issue.Body = projection.IssueBody
		}
	} else {
		issue.Body, err = c.updateIssue(ctx, issue.NodeID, issue.Body, projection)
	}
	if err != nil {
		return Issue{}, err
	}
	projectID, err := c.cachedProjectID(ctx, owner, projectNumber)
	if err != nil {
		return Issue{}, err
	}
	var itemID string
	if c.MetadataCache != nil {
		fields, fieldErr := c.cachedProjectFields(ctx, projectID)
		if fieldErr != nil {
			return Issue{}, fieldErr
		}
		if fieldErr = c.loadProjectItems(ctx, projectID); fieldErr != nil {
			return Issue{}, fieldErr
		}
		if item, found := c.MetadataCache.projectItem(projectID, issue.Number); found {
			itemID = item.ID
		} else {
			itemID, err = c.addItem(ctx, projectID, issue.NodeID)
			if err != nil {
				return Issue{}, err
			}
			c.MetadataCache.cacheProjectItem(projectID, ProjectItemSnapshot{ID: itemID, Number: issue.Number, Values: map[string]string{}})
		}
		if err = c.setFields(ctx, projectID, itemID, fields, projection); err != nil {
			return Issue{}, err
		}
	} else {
		itemID, err = c.addItem(ctx, projectID, issue.NodeID)
		if err != nil {
			return Issue{}, err
		}
		fields, fieldErr := c.projectFields(ctx, projectID)
		if fieldErr != nil {
			return Issue{}, fieldErr
		}
		if err = c.setFields(ctx, projectID, itemID, fields, projection); err != nil {
			return Issue{}, err
		}
	}
	issue.ProjectItemID = itemID
	return issue, nil
}

// UpsertFleet projects a fleet module and synchronizes only factory-owned
// labels. Human labels are never removed.
func (c Client) UpsertFleet(ctx context.Context, owner, repo string, projectNumber int, projection Projection) (Issue, error) {
	ctx, finish := progress.Start(ctx, "projection", "issue_project_sync", slog.String("module_id", projection.ModuleID))
	defer finish(false)
	existing, err := c.findIssue(ctx, owner, repo, projection.ModuleID)
	if err != nil {
		return Issue{}, err
	}
	if existing.Number != 0 && fleetSummaryMatches(existing.Body, projection) {
		if c.MetadataCache == nil {
			progress.Logger(ctx).InfoContext(ctx, "projection skipped", "reason", "acknowledged summary matches", "issue_number", existing.Number)
			finish(true)
			return existing, nil
		}
		matches, verifyErr := c.cachedProjectionMatches(ctx, owner, projectNumber, existing, projection)
		if verifyErr != nil {
			return Issue{}, verifyErr
		}
		if matches {
			progress.Logger(ctx).InfoContext(ctx, "projection skipped", "reason", "summary and Project fields match", "issue_number", existing.Number)
			finish(true)
			return existing, nil
		}
	}
	issue, err := c.Upsert(ctx, owner, repo, projectNumber, projection)
	if err != nil {
		return Issue{}, err
	}
	if err = c.syncFactoryLabels(ctx, owner, repo, issue.Number, Labels(projection), issue.Labels); err != nil {
		return Issue{}, err
	}
	acknowledgedProjection := projection
	acknowledgedProjection.IssueBody, err = fleetProjectionAcknowledgement(projection.IssueBody)
	if err != nil {
		return Issue{}, err
	}
	issue.Body, err = c.updateIssue(ctx, issue.NodeID, issue.Body, acknowledgedProjection)
	if err != nil {
		return Issue{}, err
	}
	progress.Logger(ctx).InfoContext(ctx, "Issue and Project synchronized", "issue_number", issue.Number, "status", projection.Status)
	finish(true)
	return issue, nil
}

func fleetSummaryMatches(body string, projection Projection) bool {
	start := strings.Index(body, SummaryStart)
	end := strings.Index(body, SummaryEnd)
	if start < 0 || end <= start || strings.Count(body, SummaryStart) != 1 || strings.Count(body, SummaryEnd) != 1 {
		return false
	}
	summary := body[start : end+len(SummaryEnd)]
	return strings.Contains(summary, fleetProjectionAckMarker) && fleetSummaryContentMatches(summary, projection)
}

func fleetSummaryContentMatches(summary string, projection Projection) bool {
	matches := strings.Contains(summary, "- Source: `"+projection.Version+"` at `"+projection.SourceSHA+"`") &&
		strings.Contains(summary, "- Factory: `"+projection.FactoryVersion+"`") &&
		strings.Contains(summary, "- Status: `"+projection.Status+"`") &&
		strings.Contains(summary, "- Outcome: `"+projection.Outcome+"`") &&
		strings.Contains(summary, "- Published commit: `"+projection.Published+"`")
	if !matches {
		return false
	}
	if projection.Status == StatusPublishing || projection.Status == StatusDone {
		return true
	}
	return strings.Contains(summary, "- Run: "+projection.RunURL)
}

func fleetProjectionAcknowledgement(body string) (string, error) {
	if strings.Count(body, SummaryEnd) != 1 {
		return "", fmt.Errorf("generated Issue body has invalid factory summary boundary")
	}
	return strings.Replace(body, SummaryEnd, fleetProjectionAckMarker+"\n"+SummaryEnd, 1), nil
}

func (c Client) syncFactoryLabels(ctx context.Context, owner, repo string, issueNumber int, desired, snapshotLabels []string) (returnErr error) {
	ctx, finish := progress.Start(ctx, "projection", "issue_labels", slog.Int("issue_number", issueNumber))
	defer func() { finish(returnErr == nil) }()
	labels := snapshotLabels
	if labels == nil {
		var response []struct {
			Name string `json:"name"`
		}
		if err := c.rest(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues/%d/labels?per_page=100", owner, repo, issueNumber), nil, &response); err != nil {
			return err
		}
		labels = make([]string, 0, len(response))
		for _, label := range response {
			labels = append(labels, label.Name)
		}
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
	for _, label := range labels {
		if (strings.HasPrefix(label, "factory:") || strings.HasPrefix(label, "avm:")) && !desiredSet[label] {
			path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels/%s", owner, repo, issueNumber, url.PathEscape(label))
			if err := c.rest(ctx, http.MethodDelete, path, nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c Client) ensureFactoryLabel(ctx context.Context, owner, repo, label string) error {
	repositoryKey := owner + "/" + repo
	if c.MetadataCache != nil {
		if exists, known := c.MetadataCache.repoLabel(repositoryKey, label); known {
			if exists {
				return nil
			}
			return c.createFactoryLabel(ctx, owner, repo, label)
		}
	}
	path := fmt.Sprintf("/repos/%s/%s/labels/%s", owner, repo, url.PathEscape(label))
	var existing map[string]any
	if err := c.rest(ctx, http.MethodGet, path, nil, &existing); err == nil {
		if c.MetadataCache != nil {
			c.MetadataCache.setRepoLabel(repositoryKey, label, true)
		}
		return nil
	} else if !strings.Contains(err.Error(), "github status 404") {
		return err
	}
	return c.createFactoryLabel(ctx, owner, repo, label)
}

func (c Client) createFactoryLabel(ctx context.Context, owner, repo, label string) error {
	if err := c.rest(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/labels", owner, repo), map[string]string{"name": label, "color": labelColor(label)}, nil); err != nil {
		return err
	}
	if c.MetadataCache != nil {
		c.MetadataCache.setRepoLabel(owner+"/"+repo, label, true)
	}
	return nil
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
	ctx, finish := progress.Start(ctx, "projection", "issue_lookup", slog.String("module_id", moduleID))
	defer finish(false)
	var issue Issue
	var err error
	if c.MetadataCache != nil {
		issue, err = c.findIssueFromSnapshot(ctx, owner, repo, moduleID)
	} else {
		issue, err = c.findIssueRemote(ctx, owner, repo, moduleID)
	}
	if err == nil {
		progress.Logger(ctx).InfoContext(ctx, "Issue lookup completed", "issue_number", issue.Number, "found", issue.Number != 0)
		finish(true)
	}
	return issue, err
}

func (c Client) findIssueRemote(ctx context.Context, owner, repo, moduleID string) (Issue, error) {
	var response struct {
		Items []struct {
			Number int    `json:"number"`
			NodeID string `json:"node_id"`
			URL    string `json:"html_url"`
			Body   string `json:"body"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"items"`
	}
	query := "repo:" + owner + "/" + repo + " is:issue in:body " + Marker + moduleID
	if err := c.rest(ctx, http.MethodGet, "/search/issues?q="+urlQuery(query), nil, &response); err != nil {
		return Issue{}, err
	}
	matching := make([]Issue, 0, len(response.Items))
	for _, item := range response.Items {
		if !strings.Contains(item.Body, "<!-- "+Marker+moduleID+" -->") {
			continue
		}
		labels := make([]string, 0, len(item.Labels))
		for _, label := range item.Labels {
			labels = append(labels, label.Name)
		}
		matching = append(matching, Issue{Number: item.Number, NodeID: item.NodeID, URL: item.URL, Body: item.Body, Labels: labels})
	}
	if len(matching) > 1 {
		return Issue{}, fmt.Errorf("multiple Issues matched exact module marker %s", moduleID)
	}
	if len(matching) == 0 {
		return Issue{}, nil
	}
	return matching[0], nil
}

func (c Client) createIssue(ctx context.Context, owner, repo string, projection Projection) (Issue, error) {
	ctx, finish := progress.Start(ctx, "projection", "issue_create", slog.String("module_id", projection.ModuleID))
	defer finish(false)
	body := map[string]string{"title": projection.IssueTitle, "body": projection.IssueBody}
	var response struct {
		Number int    `json:"number"`
		NodeID string `json:"node_id"`
		URL    string `json:"html_url"`
	}
	if err := c.rest(ctx, http.MethodPost, "/repos/"+owner+"/"+repo+"/issues", body, &response); err != nil {
		return Issue{}, err
	}
	progress.Logger(ctx).InfoContext(ctx, "Issue created", "issue_number", response.Number)
	finish(true)
	return Issue{Number: response.Number, NodeID: response.NodeID, URL: response.URL, Body: projection.IssueBody, Labels: []string{}}, nil
}

func (c Client) updateIssue(ctx context.Context, nodeID, existingBody string, projection Projection) (string, error) {
	phase := "issue_summary_update"
	if strings.Contains(projection.IssueBody, fleetProjectionAckMarker) {
		phase = "issue_acknowledgement"
	}
	ctx, finish := progress.Start(ctx, "projection", phase, slog.String("module_id", projection.ModuleID))
	defer finish(false)
	body, err := mergeIssueBody(existingBody, projection.IssueBody)
	if err != nil {
		return "", err
	}
	payload := map[string]any{"query": `mutation($id:ID!,$title:String!,$body:String!){updateIssue(input:{id:$id,title:$title,body:$body}){issue{number}}}`, "variables": map[string]string{"id": nodeID, "title": projection.IssueTitle, "body": body}}
	if err = c.graph(ctx, payload, nil); err != nil {
		return "", err
	}
	finish(true)
	return body, nil
}

func (c Client) projectID(ctx context.Context, owner string, number int) (string, error) {
	ctx, finish := progress.Start(ctx, "projection", "project_identity_read")
	defer finish(false)
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
	finish(true)
	return response.Organization.Project.ID, nil
}

func (c Client) addItem(ctx context.Context, projectID, issueNodeID string) (string, error) {
	ctx, finish := progress.Start(ctx, "projection", "project_item_add")
	defer finish(false)
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
	finish(true)
	return response.Add.Item.ID, nil
}

type fieldRef struct {
	ID      string
	Options map[string]string
}

func (c Client) projectFields(ctx context.Context, projectID string) (map[string]fieldRef, error) {
	ctx, finish := progress.Start(ctx, "projection", "project_fields_read")
	defer finish(false)
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
	progress.Logger(ctx).InfoContext(ctx, "Project field definitions loaded", "fields", len(fields))
	finish(true)
	return fields, nil
}

func (c Client) setFields(ctx context.Context, projectID, itemID string, fields map[string]fieldRef, projection Projection) error {
	values := desiredProjectFieldValues(projection)
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var currentValues map[string]string
	if c.MetadataCache != nil {
		if item, found := c.MetadataCache.projectItemByID(projectID, itemID); found {
			currentValues = item.Values
		}
	}
	for _, name := range names {
		value := values[name]
		if current, exists := currentValues[name]; exists && current == value {
			progress.Logger(ctx).InfoContext(ctx, "Project field skipped", "field", name, "reason", "value already matches")
			continue
		}
		if err := c.setProjectField(ctx, projectID, itemID, fields, name, value); err != nil {
			return err
		}
	}
	return nil
}

func (c Client) setProjectField(ctx context.Context, projectID, itemID string, fields map[string]fieldRef, name, value string) error {
	ctx, finish := progress.Start(ctx, "projection", "project_field_update", slog.String("field", name))
	defer finish(false)
	field, ok := fields[name]
	if !ok {
		return fmt.Errorf("project field %q was not found", name)
	}
	input := map[string]any{"projectId": projectID, "itemId": itemID, "fieldId": field.ID}
	if len(field.Options) > 0 {
		optionID, optionExists := field.Options[value]
		if !optionExists {
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
	if c.MetadataCache != nil {
		c.MetadataCache.setProjectItemValue(projectID, itemID, name, value)
	}
	finish(true)
	return nil
}

func desiredProjectFieldValues(projection Projection) map[string]string {
	values := map[string]string{
		"Status": projection.Status, "Work type": projection.WorkType, "Module ID": projection.ModuleID,
		"Module type": projection.ModuleType, "Domain": projection.Domain, "Source version": projection.Version,
		"Source SHA": projection.SourceSHA,
		"Outcome":    projection.Outcome, "Published commit": projection.Published,
		"Run URL": projection.RunURL,
	}
	if !projection.LastAnalyzed.IsZero() {
		values["Last analyzed"] = projection.LastAnalyzed.Format("2006-01-02")
	}
	return values
}

func (c Client) rest(ctx context.Context, method, path string, payload any, out any) error {
	return c.request(ctx, method, "https://api.github.com"+path, payload, out)
}

func (c Client) graph(ctx context.Context, payload any, data any) error {
	for attempt := 0; attempt < 3; attempt++ {
		var response struct {
			Data   json.RawMessage `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		if err := c.request(ctx, http.MethodPost, graphQLEndpoint, payload, &response); err != nil {
			return err
		}
		if len(response.Errors) == 0 {
			if data == nil || len(response.Data) == 0 {
				return nil
			}
			return json.Unmarshal(response.Data, data)
		}
		err := fmt.Errorf("github graphql: %s", response.Errors[0].Message)
		if !strings.Contains(response.Errors[0].Message, "Something went wrong") || attempt == 2 {
			return err
		}
		progress.Logger(ctx).WarnContext(ctx, "GitHub GraphQL request retry scheduled", "attempt", attempt+1, "delay", time.Duration(attempt+1)*time.Second, "reason", "transient API failure")
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	return fmt.Errorf("github graphql request exhausted retries")
}

func (c Client) request(ctx context.Context, method, endpoint string, payload any, out any) error {
	if strings.TrimSpace(c.Token) == "" {
		return fmt.Errorf("reduced GitHub token is required")
	}
	var encoded []byte
	if payload != nil {
		var err error
		encoded, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return c.performRequest(ctx, client, method, endpoint, encoded, out, classifyProjectionRequest(endpoint))
}

type clientResponse struct {
	status int
	header http.Header
	body   []byte
}

func (c Client) performRequest(ctx context.Context, client *http.Client, method, endpoint string, encoded []byte, out any, requestClass ProjectionRequestClass) error {
	for attempt := 0; attempt < 3; attempt++ {
		if err := c.Budget.Take(requestClass, time.Now()); err != nil {
			return err
		}
		response, err := sendGitHubRequest(ctx, client, method, endpoint, encoded, c.Token)
		if err != nil {
			return err
		}
		c.Budget.Observe(requestClass, response.header, response.status, time.Now())
		if response.status >= 200 && response.status < 300 {
			return decodeGitHubResponse(response.body, out)
		}
		if c.Budget != nil && (response.status == http.StatusTooManyRequests || (response.status == http.StatusForbidden && isRateLimited(response.header))) {
			progress.Logger(ctx).WarnContext(ctx, "GitHub projection budget exhausted", "status", response.status, "reason", "rate limit; bounded projection must resume later")
			return ErrProjectionBudgetExhausted
		}
		if shouldRetryGitHubStatus(response.status, attempt) {
			progress.Logger(ctx).WarnContext(ctx, "GitHub request retry scheduled", "method", method, "status", response.status, "attempt", attempt+1, "delay", time.Duration(attempt+1)*time.Second)
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}
		return githubStatusError(response.status, response.body)
	}
	return fmt.Errorf("github request exhausted retries")
}

func sendGitHubRequest(ctx context.Context, client *http.Client, method, endpoint string, encoded []byte, token string) (clientResponse, error) {
	var body io.Reader
	if encoded != nil {
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return clientResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return clientResponse{}, err
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	closeErr := response.Body.Close()
	if readErr != nil {
		return clientResponse{}, readErr
	}
	if closeErr != nil {
		return clientResponse{}, closeErr
	}
	return clientResponse{status: response.StatusCode, header: response.Header.Clone(), body: responseBody}, nil
}

func decodeGitHubResponse(body []byte, out any) error {
	if out == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}

func shouldRetryGitHubStatus(statusCode, attempt int) bool {
	return attempt < 2 && (statusCode == http.StatusTooManyRequests || statusCode >= 500)
}

func githubStatusError(statusCode int, body []byte) error {
	var apiError struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &apiError); err == nil && apiError.Message != "" {
		return fmt.Errorf("github status %d: %s", statusCode, apiError.Message)
	}
	return fmt.Errorf("github status %d", statusCode)
}

func urlQuery(value string) string {
	replacer := strings.NewReplacer(" ", "%20", ":", "%3A", "/", "%2F")
	return replacer.Replace(value)
}
