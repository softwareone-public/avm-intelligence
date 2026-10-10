package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultRegistryBase = "https://registry.terraform.io"

// ResourceIntelligenceCollector reads public provider sources for exact
// accepted provider snapshots. Endpoints, clock, HTTP client and cache are
// injectable for fixture tests.
type ResourceIntelligenceCollector struct {
	APIBase      string
	RawBase      string
	RegistryBase string
	Client       *http.Client
	Cache        CatalogCache
	Now          func() time.Time
}

type providerReleaseResponse struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Body    string `json:"body"`
}

type providerRootEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type providerIssueSearchResponse struct {
	Items []providerIssue `json:"items"`
}

type providerIssue struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	HTMLURL   string `json:"html_url"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Collect returns immutable source results for all provider snapshots sealed
// into accepted. A missing release, issue, changelog, or guide is represented
// by no source record; transport, integrity, and bounds failures abort the
// collection so they cannot be mistaken for an empty result.
func (collector ResourceIntelligenceCollector) Collect(ctx context.Context, policy CollectionPolicy, accepted AcceptedResourceIntelligenceInputs, collectorCommit string) ([]ResourceIntelligenceSnapshot, error) {
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("validate resource intelligence policy: %w", err)
	}
	if err := accepted.validateSeal(); err != nil {
		return nil, err
	}
	if !validCommitSHA1(collectorCommit) {
		return nil, fmt.Errorf("resource intelligence collector commit is invalid")
	}
	apiBase, err := catalogBase(collector.APIBase, defaultCatalogAPIBase)
	if err != nil {
		return nil, fmt.Errorf("validate GitHub API endpoint: %w", err)
	}
	rawBase, err := catalogBase(collector.RawBase, defaultCatalogRawBase)
	if err != nil {
		return nil, fmt.Errorf("validate raw GitHub endpoint: %w", err)
	}
	registryBase, err := catalogBase(collector.RegistryBase, defaultRegistryBase)
	if err != nil {
		return nil, fmt.Errorf("validate Terraform Registry endpoint: %w", err)
	}
	now := time.Now
	if collector.Now != nil {
		now = collector.Now
	}
	observedAt := now().UTC()
	if observedAt.IsZero() {
		return nil, fmt.Errorf("resource intelligence collection time is invalid")
	}
	client := catalogHTTPClient(collector.Client)
	results := make([]ResourceIntelligenceSnapshot, 0, len(accepted.Snapshots))
	for _, acceptedSnapshot := range accepted.Snapshots {
		providerPolicy, found := providerPolicyForAddress(policy, acceptedSnapshot.ProviderAddress)
		if !found {
			return nil, fmt.Errorf("accepted provider snapshot is outside reviewed policy")
		}
		collectionContext, cancel := context.WithTimeout(ctx, time.Duration(providerPolicy.Limits.MaxWallTimeSeconds)*time.Second)
		budget := &catalogBudget{}
		sources, collectErr := collector.collectProviderSnapshot(collectionContext, client, apiBase, rawBase, registryBase, providerPolicy, acceptedSnapshot, observedAt, budget)
		cancel()
		if collectErr != nil {
			return nil, fmt.Errorf("collect provider resource intelligence: %w", collectErr)
		}
		results = append(results, sources)
	}
	return results, nil
}

func (collector ResourceIntelligenceCollector) collectProviderSnapshot(ctx context.Context, client *http.Client, apiBase, rawBase, registryBase string, policy ProviderPolicy, accepted ResourceIntelligenceSnapshot, observedAt time.Time, budget *catalogBudget) (ResourceIntelligenceSnapshot, error) {
	owner, repository, _ := strings.Cut(policy.SourceRepository, "/")
	output := cloneResourceSnapshots([]ResourceIntelligenceSnapshot{accepted})[0]
	for index := range output.Resources {
		resource := &output.Resources[index]
		resource.Sources = append([]ResourceIntelligenceSource(nil), resource.Sources...)
	}
	tag, releaseBody, err := collector.collectRelease(ctx, client, apiBase, policy, accepted.ProviderVersion, budget)
	if err != nil {
		return ResourceIntelligenceSnapshot{}, err
	}
	changelogs, err := collector.collectChangelogs(ctx, client, apiBase, rawBase, policy, owner, repository, tag, budget)
	if err != nil {
		return ResourceIntelligenceSnapshot{}, err
	}
	for index := range output.Resources {
		resource := &output.Resources[index]
		if releaseBody != "" && containsTerraformType(releaseBody, resource.Name) {
			url := "https://github.com/" + owner + "/" + repository + "/releases/tag/" + url.PathEscape(tag)
			resource.Sources = append(resource.Sources, ResourceIntelligenceSource{
				Kind: "github_release", URL: url, Version: accepted.ProviderVersion, VersionBasis: "release_tag",
				ContentDigest: digestText(releaseBody), Body: releaseBody, ObservedAt: observedAt,
			})
		}
		for _, changelog := range changelogs {
			if containsTerraformType(changelog.Body, resource.Name) {
				resource.Sources = append(resource.Sources, ResourceIntelligenceSource{
					Kind: "provider_changelog", URL: changelog.URL, Version: accepted.ProviderVersion, VersionBasis: "release_tag",
					ContentDigest: digestText(changelog.Body), Body: changelog.Body, ObservedAt: observedAt,
				})
			}
		}
		issues, issueErr := collector.collectIssues(ctx, client, apiBase, policy, owner, repository, resource.Name, budget)
		if issueErr != nil {
			return ResourceIntelligenceSnapshot{}, issueErr
		}
		for _, issue := range issues {
			resource.Sources = append(resource.Sources, ResourceIntelligenceSource{
				Kind: "resource_issue", URL: issue.HTMLURL, Version: accepted.ProviderVersion, VersionBasis: "provider_snapshot",
				ContentDigest: digestText(issue.Body), Body: issue.Body, ObservedAt: observedAt,
			})
		}
		guides, guideErr := collector.collectUpgradeGuide(ctx, client, registryBase, policy, accepted.ProviderVersion, resource.Name, budget)
		if guideErr != nil {
			return ResourceIntelligenceSnapshot{}, guideErr
		}
		for _, guide := range guides {
			resource.Sources = append(resource.Sources, ResourceIntelligenceSource{
				Kind: "registry_upgrade", URL: guide.URL, Version: accepted.ProviderVersion, VersionBasis: "registry_path",
				ContentDigest: digestText(guide.Body), Body: guide.Body, ObservedAt: observedAt,
			})
		}
	}
	return output, nil
}

func (collector ResourceIntelligenceCollector) collectRelease(ctx context.Context, client *http.Client, apiBase string, policy ProviderPolicy, version string, budget *catalogBudget) (string, string, error) {
	owner, repository, _ := strings.Cut(policy.SourceRepository, "/")
	for _, tag := range []string{"v" + version, version} {
		endpoint := catalogJoinURL(apiBase, "repos", owner, repository, "releases", "tags", tag)
		status, _, data, err := collector.get(ctx, client, endpoint, "application/vnd.github+json", policy.Limits, budget)
		if err != nil {
			return "", "", err
		}
		if status == http.StatusNotFound {
			continue
		}
		if status != http.StatusOK {
			return "", "", fmt.Errorf("GitHub release source returned HTTP %d", status)
		}
		var release providerReleaseResponse
		if json.Unmarshal(data, &release) != nil || !versionMatchesTag(release.TagName, version) {
			return "", "", fmt.Errorf("GitHub release does not match the exact provider version")
		}
		body := strings.TrimSpace(strings.Join([]string{release.Name, release.Body}, "\n\n"))
		if body == "" {
			return tag, "", nil
		}
		return tag, body, nil
	}
	return "v" + version, "", nil
}

func (collector ResourceIntelligenceCollector) collectChangelogs(ctx context.Context, client *http.Client, apiBase, rawBase string, policy ProviderPolicy, owner, repository, tag string, budget *catalogBudget) ([]resourceArtifact, error) {
	listingURL, err := url.Parse(catalogJoinURL(apiBase, "repos", owner, repository, "contents"))
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	query := listingURL.Query()
	query.Set("ref", tag)
	query.Set("per_page", "100")
	query.Set("page", "1")
	listingURL.RawQuery = query.Encode()
	for page := 1; ; page++ {
		status, headers, data, getErr := collector.get(ctx, client, listingURL.String(), "application/vnd.github+json", policy.Limits, budget)
		if getErr != nil {
			return nil, getErr
		}
		if status == http.StatusNotFound {
			return nil, nil
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("GitHub changelog listing returned HTTP %d", status)
		}
		var entries []providerRootEntry
		if json.Unmarshal(data, &entries) != nil {
			return nil, fmt.Errorf("GitHub changelog listing is invalid")
		}
		for _, entry := range entries {
			name := strings.ToLower(entry.Name)
			if entry.Type == "file" && strings.HasPrefix(name, "changelog") && (strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".markdown") || strings.HasSuffix(name, ".txt")) && path.Base(entry.Name) == entry.Name {
				paths = append(paths, entry.Name)
			}
		}
		nextURL, hasNext, linkErr := catalogNextURL(headers.Get(catalogLinkHeader))
		if linkErr != nil {
			return nil, linkErr
		}
		if !hasNext {
			break
		}
		listingURLString, linkErr := validateResourceContentsNextURL(nextURL, listingURL.String(), tag, page+1)
		if linkErr != nil {
			return nil, linkErr
		}
		listingURL, _ = url.Parse(listingURLString)
	}
	sort.Strings(paths)
	artifacts := make([]resourceArtifact, 0, len(paths))
	for _, changelogPath := range paths {
		rawURL := catalogJoinURL(rawBase, owner, repository, tag, changelogPath)
		status, _, body, getErr := collector.get(ctx, client, rawURL, "text/plain", policy.Limits, budget)
		if getErr != nil {
			return nil, getErr
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("pinned provider changelog returned HTTP %d", status)
		}
		githubURL := "https://github.com/" + owner + "/" + repository + "/blob/" + tag + "/" + changelogPath
		artifacts = append(artifacts, resourceArtifact{URL: githubURL, Body: strings.TrimSpace(string(body))})
	}
	return artifacts, nil
}

func validateResourceContentsNextURL(nextURL, currentURL, tag string, expectedPage int) (string, error) {
	next, err := url.Parse(nextURL)
	if err != nil {
		return "", fmt.Errorf("provider changelog pagination URL is invalid")
	}
	current, err := url.Parse(currentURL)
	if err != nil {
		return "", err
	}
	query := next.Query()
	page, pageErr := strconv.Atoi(query.Get("page"))
	if !next.IsAbs() || next.User != nil || next.Fragment != "" || !sameURLOrigin(next, current) || next.Path != current.Path ||
		len(query) != 3 || len(query["ref"]) != 1 || len(query["per_page"]) != 1 || len(query["page"]) != 1 ||
		query.Get("ref") != tag || query.Get("per_page") != "100" || pageErr != nil || page != expectedPage {
		return "", fmt.Errorf("provider changelog pagination escaped its pinned listing")
	}
	return next.String(), nil
}

func (collector ResourceIntelligenceCollector) collectIssues(ctx context.Context, client *http.Client, apiBase string, policy ProviderPolicy, owner, repository, resourceName string, budget *catalogBudget) ([]providerIssue, error) {
	endpoint := catalogJoinURL(apiBase, "search", "issues")
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	query := parsed.Query()
	query.Set("q", "repo:"+owner+"/"+repository+" is:issue \""+resourceName+"\"")
	query.Set("per_page", "100")
	query.Set("page", "1")
	parsed.RawQuery = query.Encode()
	issues := make([]providerIssue, 0)
	for page := 1; ; page++ {
		if page > policy.Limits.MaxPages {
			return nil, fmt.Errorf("GitHub issue search exceeds the reviewed page limit")
		}
		if page > 1 {
			query.Set("page", fmt.Sprint(page))
			parsed.RawQuery = query.Encode()
		}
		status, headers, data, getErr := collector.get(ctx, client, parsed.String(), "application/vnd.github+json", policy.Limits, budget)
		if getErr != nil {
			return nil, getErr
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("GitHub resource issue search returned HTTP %d", status)
		}
		var result providerIssueSearchResponse
		if json.Unmarshal(data, &result) != nil {
			return nil, fmt.Errorf("GitHub resource issue search response is invalid")
		}
		for _, issue := range result.Items {
			if issue.Number <= 0 || !containsTerraformType(issue.Title+"\n"+issue.Body, resourceName) || !validResourceIntelligenceSourceURL("resource_issue", issue.HTMLURL, "", policy) {
				continue
			}
			issue.Body = strings.TrimSpace(issue.Title + "\n\n" + issue.Body)
			if issue.Body != "" {
				issues = append(issues, issue)
			}
		}
		if _, hasNext, linkErr := catalogNextURL(headers.Get(catalogLinkHeader)); linkErr != nil {
			return nil, linkErr
		} else if !hasNext {
			break
		}
	}
	sort.Slice(issues, func(left, right int) bool { return issues[left].Number < issues[right].Number })
	return issues, nil
}

func (collector ResourceIntelligenceCollector) collectUpgradeGuide(ctx context.Context, client *http.Client, registryBase string, policy ProviderPolicy, version, resourceName string, budget *catalogBudget) ([]resourceArtifact, error) {
	parts := strings.Split(policy.Address, "/")
	major, _, _ := strings.Cut(version, ".")
	guidePath := catalogJoinURL(registryBase, "providers", parts[1], parts[2], version, "docs", "guides", major+".0-upgrade-guide")
	status, _, body, err := collector.get(ctx, client, guidePath, "text/html", policy.Limits, budget)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("Terraform Registry upgrade guide returned HTTP %d", status)
	}
	text := normalizeRegistryHTML(string(body))
	if !containsTerraformType(text, resourceName) {
		return nil, nil
	}
	publicURL := "https://registry.terraform.io/providers/" + parts[1] + "/" + parts[2] + "/" + version + "/docs/guides/" + major + ".0-upgrade-guide"
	return []resourceArtifact{{URL: publicURL, Body: text}}, nil
}

type resourceArtifact struct {
	URL  string
	Body string
}

func (collector ResourceIntelligenceCollector) get(ctx context.Context, client *http.Client, endpoint, accept string, limits CollectionLimits, budget *catalogBudget) (int, http.Header, []byte, error) {
	if budget.pages >= limits.MaxPages {
		return 0, nil, nil, fmt.Errorf("resource intelligence collection exceeds the reviewed page limit")
	}
	budget.pages++
	return catalogGet(ctx, client, collector.Cache, endpoint, accept, limits, budget)
}

var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)
var whitespacePattern = regexp.MustCompile(`\s+`)

func normalizeRegistryHTML(input string) string {
	text := htmlTagPattern.ReplaceAllString(input, " ")
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", `"`)
	return strings.TrimSpace(whitespacePattern.ReplaceAllString(text, " "))
}

func containsTerraformType(text, resourceName string) bool {
	lowerText, lowerName := strings.ToLower(text), strings.ToLower(resourceName)
	for offset := 0; ; {
		position := strings.Index(lowerText[offset:], lowerName)
		if position < 0 {
			return false
		}
		start, end := offset+position, offset+position+len(lowerName)
		isNameChar := func(char byte) bool { return char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' }
		if (start == 0 || !isNameChar(lowerText[start-1])) && (end == len(lowerText) || !isNameChar(lowerText[end])) {
			return true
		}
		offset = end
		if offset >= len(lowerText) {
			return false
		}
	}
}
