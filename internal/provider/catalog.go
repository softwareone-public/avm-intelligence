package provider

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
	"golang.org/x/mod/semver"
)

const (
	defaultCatalogAPIBase     = "https://api.github.com"
	defaultCatalogRawBase     = "https://raw.githubusercontent.com"
	catalogPageSize           = 100
	catalogCollectorID        = "avm-intelligence-provider"
	catalogCollectorVersion   = "1.0.0"
	catalogContractVersion    = 1
	catalogETagHeader         = "ETag"
	catalogLastModifiedHeader = "Last-Modified"
	catalogLinkHeader         = "Link"
	catalogContentTypeHeader  = "Content-Type"
)

var catalogDirectories = map[string]string{
	"action": "actions", "datasource": "data-sources", "ephemeral": "ephemeral-resources",
	"function": "functions", "resource": "resources",
}

// CatalogCollector reads only documentation at an immutable provider commit.
// Its HTTP bases and clock are injectable so collection behavior is fixture-testable.
type CatalogCollector struct {
	APIBase string
	RawBase string
	Client  *http.Client
	Cache   CatalogCache
	Now     func() time.Time
	Commit  string
}

type catalogContentEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

type catalogSourceArtifact struct {
	URL        string `json:"url"`
	Digest     string `json:"digest"`
	HTTPStatus int    `json:"http_status"`
}

type catalogBudget struct {
	pages      int
	totalBytes int64
}

type catalogCollectionRun struct {
	client         *http.Client
	cache          CatalogCache
	apiBase        string
	rawBase        string
	providerPolicy ProviderPolicy
	revision       string
	budget         *catalogBudget
}

type catalogRequest struct {
	request    *http.Request
	cacheKey   string
	cached     CatalogCacheEntry
	cacheFound bool
}

// CollectTypeCatalog fetches documentation for one allowlisted provider and
// exact version/commit pair. Any truncated or failed listing returns no snapshot.
func (collector CatalogCollector) CollectTypeCatalog(ctx context.Context, policy CollectionPolicy, providerAddress, version, revision string) (assessment.ProviderSnapshot, error) {
	if err := policy.Validate(); err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	providerPolicy, found := providerPolicyForAddress(policy, providerAddress)
	if !found {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider address %q is not allowlisted", providerAddress)
	}
	providerAddress = providerPolicy.Address
	if err := validateCatalogVersion(version, providerPolicy.TrackMajors); err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	if !validCommitSHA1(revision) {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider revision must be an immutable 40-character commit SHA")
	}
	if !validCommitSHA1(collector.Commit) {
		return assessment.ProviderSnapshot{}, fmt.Errorf("collector commit is invalid")
	}
	apiBase, err := catalogBase(collector.APIBase, defaultCatalogAPIBase)
	if err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	rawBase, err := catalogBase(collector.RawBase, defaultCatalogRawBase)
	if err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	collectionContext, cancel := context.WithTimeout(ctx, time.Duration(providerPolicy.Limits.MaxWallTimeSeconds)*time.Second)
	defer cancel()
	budget := &catalogBudget{}
	run := catalogCollectionRun{
		client: catalogHTTPClient(collector.Client), cache: collector.Cache, apiBase: apiBase, rawBase: rawBase,
		providerPolicy: providerPolicy, revision: revision, budget: budget,
	}
	capabilities := make(map[string]string, len(providerPolicy.CollectionKinds))
	artifacts, err := resolveProviderVersion(collectionContext, run, version, revision)
	if err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	entries := make([]assessment.ProviderEntry, 0)
	for _, kind := range providerPolicy.CollectionKinds {
		kindEntries, kindArtifacts, complete, collectErr := collector.collectCatalogKind(collectionContext, run, providerPolicy, kind)
		if collectErr != nil {
			return assessment.ProviderSnapshot{}, fmt.Errorf("collect %s catalog: %w", kind, collectErr)
		}
		entries = append(entries, kindEntries...)
		artifacts = append(artifacts, kindArtifacts...)
		if complete {
			capabilities[kind] = "complete"
		} else {
			capabilities[kind] = "unavailable"
		}
	}
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].Kind != entries[right].Kind {
			return entries[left].Kind < entries[right].Kind
		}
		return entries[left].Name < entries[right].Name
	})
	observedAt := collector.observedAt()
	coverageComplete := len(entries) > 0 && allCatalogKindsComplete(capabilities)
	if len(entries) == 0 {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider catalog contains no documented types at the pinned revision")
	}
	reason := ""
	if !coverageComplete {
		reason = "one or more configured type catalogs are unavailable or empty"
	}
	sourceDigest, err := catalogSourceDigest(providerPolicy, version, revision, capabilities, artifacts)
	if err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	return assessment.ProviderSnapshot{
		SchemaVersion: 2, RecordType: "provider_snapshot", ProviderAddress: providerAddress,
		ProviderVersion: version,
		Coverage: assessment.SnapshotCoverage{
			Capabilities: capabilities, Complete: coverageComplete, ObservedAt: observedAt, Reason: reason,
		},
		Entries: entries,
		Producer: assessment.SnapshotProducer{
			ID: catalogCollectorID, Version: catalogCollectorVersion, Commit: collector.Commit,
			ContractVersion: catalogContractVersion,
		},
		SourceDigest: sourceDigest, ObservedAt: observedAt,
	}, nil
}

func resolveProviderVersion(ctx context.Context, run catalogCollectionRun, version, revision string) ([]catalogSourceArtifact, error) {
	owner, repository, _ := strings.Cut(run.providerPolicy.SourceRepository, "/")
	tags := []string{"v" + version, version}
	artifacts := make([]catalogSourceArtifact, 0, len(tags))
	resolvedCommit := ""
	for _, tag := range tags {
		commitURL := catalogJoinURL(run.apiBase, "repos", owner, repository, "commits", tag)
		status, _, body, err := catalogGet(ctx, run.client, run.cache, commitURL, "application/vnd.github+json", run.providerPolicy.Limits, run.budget)
		if err != nil {
			return nil, fmt.Errorf("resolve provider version tag %q: %w", tag, err)
		}
		artifacts = append(artifacts, newCatalogArtifact(commitURL, body, status))
		if isMissingProviderTag(status, body, tag) {
			continue
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("provider version tag %q returned HTTP %d", tag, status)
		}
		var response struct {
			SHA string `json:"sha"`
		}
		if err = json.Unmarshal(body, &response); err != nil || !validCommitSHA1(response.SHA) {
			return nil, fmt.Errorf("provider version tag %q did not resolve to a valid commit", tag)
		}
		if resolvedCommit != "" && resolvedCommit != response.SHA {
			return nil, fmt.Errorf("provider version aliases resolve to different commits")
		}
		resolvedCommit = response.SHA
	}
	if resolvedCommit == "" {
		return nil, fmt.Errorf("provider version %s has no resolvable release tag", version)
	}
	if resolvedCommit != revision {
		return nil, fmt.Errorf("provider version %s resolves to %s, not requested revision %s", version, resolvedCommit, revision)
	}
	return artifacts, nil
}

func isMissingProviderTag(status int, body []byte, tag string) bool {
	if status == http.StatusNotFound {
		return true
	}
	if status != http.StatusUnprocessableEntity {
		return false
	}
	var response struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return false
	}
	return response.Message == "No commit found for SHA: "+tag
}

func (collector CatalogCollector) collectCatalogKind(ctx context.Context, run catalogCollectionRun, policy ProviderPolicy, kind string) ([]assessment.ProviderEntry, []catalogSourceArtifact, bool, error) {
	directory, supported := catalogDocumentationDirectory(policy, kind)
	if !supported {
		return []assessment.ProviderEntry{}, []catalogSourceArtifact{}, false, nil
	}
	owner, repository, _ := strings.Cut(run.providerPolicy.SourceRepository, "/")
	listURL, err := catalogListURL(run.apiBase, owner, repository, directory, run.revision, 1)
	if err != nil {
		return nil, nil, false, err
	}
	entries := make([]assessment.ProviderEntry, 0)
	artifacts := make([]catalogSourceArtifact, 0)
	seen := make(map[string]struct{})
	for page := 1; ; page++ {
		run.budget.pages++
		if run.budget.pages > run.providerPolicy.Limits.MaxPages {
			return nil, nil, false, fmt.Errorf("provider catalog exceeds %d pages", run.providerPolicy.Limits.MaxPages)
		}
		status, headers, body, fetchErr := catalogGet(ctx, run.client, run.cache, listURL, "application/vnd.github+json", run.providerPolicy.Limits, run.budget)
		if fetchErr != nil {
			return nil, nil, false, fetchErr
		}
		if status == http.StatusNotFound && page == 1 {
			artifacts = append(artifacts, newCatalogArtifact(listURL, body, status))
			return entries, artifacts, false, nil
		}
		if status != http.StatusOK {
			return nil, nil, false, fmt.Errorf("provider catalog listing returned HTTP %d", status)
		}
		artifacts = append(artifacts, newCatalogArtifact(listURL, body, status))
		pageEntries, pageArtifacts, decodeErr := collector.collectCatalogDocuments(ctx, run, kind, directory, run.providerPolicy.Address, body, seen)
		if decodeErr != nil {
			return nil, nil, false, decodeErr
		}
		entries = append(entries, pageEntries...)
		artifacts = append(artifacts, pageArtifacts...)
		nextURL, hasNext, nextErr := catalogNextURL(headers.Get("Link"))
		if nextErr != nil {
			return nil, nil, false, nextErr
		}
		if !hasNext {
			return entries, artifacts, true, nil
		}
		if run.budget.pages >= run.providerPolicy.Limits.MaxPages {
			return nil, nil, false, fmt.Errorf("provider catalog pagination is incomplete at page limit %d", run.providerPolicy.Limits.MaxPages)
		}
		listURL, err = validateCatalogNextURL(nextURL, listURL, run.revision, page+1)
		if err != nil {
			return nil, nil, false, err
		}
	}
}

func (collector CatalogCollector) collectCatalogDocuments(ctx context.Context, run catalogCollectionRun, kind, directory string, providerAddress string, body []byte, seen map[string]struct{}) ([]assessment.ProviderEntry, []catalogSourceArtifact, error) {
	var contents []catalogContentEntry
	if err := json.Unmarshal(body, &contents); err != nil {
		return nil, nil, fmt.Errorf("decode provider catalog listing: %w", err)
	}
	owner, repository, _ := strings.Cut(run.providerPolicy.SourceRepository, "/")
	entries := make([]assessment.ProviderEntry, 0)
	artifacts := make([]catalogSourceArtifact, 0)
	for _, content := range contents {
		if content.Type == "dir" && strings.HasPrefix(content.Path, directory+"/") {
			return nil, nil, fmt.Errorf("nested provider documentation directories are unsupported")
		}
		if content.Type != "file" || !isCatalogMarkdownPath(content.Path, directory) {
			continue
		}
		name, isTypeDocument := providerTypeName(content.Path, directory, kind, providerAddress)
		if !isTypeDocument {
			return nil, nil, fmt.Errorf("provider Markdown document has an unsupported type identity: %s", content.Path)
		}
		entryKey := kind + "\x00" + name
		if _, duplicate := seen[entryKey]; duplicate {
			return nil, nil, fmt.Errorf("provider catalog duplicates %s %q", kind, name)
		}
		seen[entryKey] = struct{}{}
		documentURL, err := catalogDocumentURL(run.rawBase, owner, repository, run.revision, content.Path)
		if err != nil {
			return nil, nil, err
		}
		status, _, documentBody, err := catalogGet(ctx, run.client, run.cache, documentURL, "text/plain", run.providerPolicy.Limits, run.budget)
		if err != nil {
			return nil, nil, err
		}
		if status != http.StatusOK {
			return nil, nil, fmt.Errorf("provider documentation returned HTTP %d", status)
		}
		if len(documentBody) == 0 {
			return nil, nil, fmt.Errorf("provider documentation is empty: %s", content.Path)
		}
		artifacts = append(artifacts, newCatalogArtifact(documentURL, documentBody, status))
		entries = append(entries, assessment.ProviderEntry{
			Kind: kind, Name: name, DocumentationRef: providerDocumentationRef(owner, repository, run.revision, content.Path),
			ContentDigest: catalogDigest(documentBody),
		})
	}
	return entries, artifacts, nil
}

func catalogGet(ctx context.Context, client *http.Client, cache CatalogCache, rawURL, accept string, limits CollectionLimits, budget *catalogBudget) (int, http.Header, []byte, error) {
	request, err := newCatalogRequest(ctx, cache, rawURL, accept)
	if err != nil {
		return 0, nil, nil, err
	}
	return sendCatalogRequest(ctx, client, cache, request, limits, budget)
}

func newCatalogRequest(ctx context.Context, cache CatalogCache, rawURL, accept string) (catalogRequest, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return catalogRequest{}, err
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "avm-intelligence-provider")
	cacheKey := catalogRequestCacheKey(rawURL, accept)
	cached, cacheFound, err := attachCatalogCacheValidator(ctx, cache, cacheKey, request)
	if err != nil {
		return catalogRequest{}, err
	}
	return catalogRequest{request: request, cacheKey: cacheKey, cached: cached, cacheFound: cacheFound}, nil
}

func sendCatalogRequest(ctx context.Context, client *http.Client, cache CatalogCache, request catalogRequest, limits CollectionLimits, budget *catalogBudget) (int, http.Header, []byte, error) {
	response, err := client.Do(request.request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer response.Body.Close()
	body, err := readCatalogResponseBody(response.Body, limits, budget)
	if err != nil {
		return 0, nil, nil, err
	}
	return completeCatalogResponse(ctx, cache, request, response, body)
}

func completeCatalogResponse(ctx context.Context, cache CatalogCache, request catalogRequest, response *http.Response, body []byte) (int, http.Header, []byte, error) {
	if response.StatusCode == http.StatusNotModified {
		return useNotModifiedCatalogResponse(ctx, cache, request.cacheKey, request.cached, request.cacheFound, response.Header)
	}
	if err := storeCatalogResponse(ctx, cache, request.cacheKey, response.StatusCode, response.Header, body); err != nil {
		return 0, nil, nil, err
	}
	return response.StatusCode, response.Header.Clone(), body, nil
}

func attachCatalogCacheValidator(ctx context.Context, cache CatalogCache, cacheKey string, request *http.Request) (CatalogCacheEntry, bool, error) {
	if cache == nil {
		return CatalogCacheEntry{}, false, nil
	}
	entry, found, err := cache.Load(ctx, cacheKey)
	if err != nil || !found {
		return entry, found, err
	}
	if entry.ETag != "" {
		request.Header.Set("If-None-Match", entry.ETag)
	}
	if entry.LastModified != "" {
		request.Header.Set("If-Modified-Since", entry.LastModified)
	}
	return entry, true, nil
}

func readCatalogResponseBody(body io.Reader, limits CollectionLimits, budget *catalogBudget) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limits.MaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limits.MaxResponseBytes {
		return nil, fmt.Errorf("provider response exceeds %d bytes", limits.MaxResponseBytes)
	}
	budget.totalBytes += int64(len(data))
	if budget.totalBytes > limits.MaxTotalBytes {
		return nil, fmt.Errorf("provider collection exceeds %d total bytes", limits.MaxTotalBytes)
	}
	return data, nil
}

func useNotModifiedCatalogResponse(ctx context.Context, cache CatalogCache, cacheKey string, cached CatalogCacheEntry, cacheFound bool, responseHeaders http.Header) (int, http.Header, []byte, error) {
	if !cacheFound || cache == nil {
		return 0, nil, nil, fmt.Errorf("provider source returned HTTP 304 without a verified cached body")
	}
	if err := validateCatalogCacheEntry(cached); err != nil {
		return 0, nil, nil, fmt.Errorf("provider source returned HTTP 304 with an invalid cached body: %w", err)
	}
	headers := cached.Headers.Clone()
	mergeCatalogCacheHeaders(headers, responseHeaders)
	cached.ETag = firstNonEmpty(responseHeaders.Get(catalogETagHeader), cached.ETag)
	cached.LastModified = firstNonEmpty(responseHeaders.Get(catalogLastModifiedHeader), cached.LastModified)
	cached.Headers = headers
	if err := cache.Store(ctx, cacheKey, cached); err != nil {
		return 0, nil, nil, fmt.Errorf("refresh provider cache validators: %w", err)
	}
	return http.StatusOK, headers, cached.Body, nil
}

func storeCatalogResponse(ctx context.Context, cache CatalogCache, cacheKey string, status int, headers http.Header, body []byte) error {
	if status != http.StatusOK || cache == nil || (headers.Get(catalogETagHeader) == "" && headers.Get(catalogLastModifiedHeader) == "") {
		return nil
	}
	entry := CatalogCacheEntry{
		ETag: headers.Get(catalogETagHeader), LastModified: headers.Get(catalogLastModifiedHeader),
		Digest: catalogDigest(body), Body: body, Headers: catalogCacheHeaders(headers),
	}
	if err := cache.Store(ctx, cacheKey, entry); err != nil {
		return fmt.Errorf("cache provider response: %w", err)
	}
	return nil
}

func catalogRequestCacheKey(rawURL, accept string) string {
	return catalogDigest([]byte(rawURL + "\n" + accept))
}

func catalogCacheHeaders(headers http.Header) http.Header {
	filtered := make(http.Header)
	for _, key := range []string{catalogLinkHeader, catalogContentTypeHeader} {
		if values := headers.Values(key); len(values) > 0 {
			filtered[key] = append([]string(nil), values...)
		}
	}
	return filtered
}

func mergeCatalogCacheHeaders(cached, refreshed http.Header) {
	for _, key := range []string{catalogLinkHeader, catalogContentTypeHeader} {
		if values := refreshed.Values(key); len(values) > 0 {
			cached[key] = append([]string(nil), values...)
		}
	}
}

func firstNonEmpty(preferred, fallback string) string {
	if preferred != "" {
		return preferred
	}
	return fallback
}

func catalogHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	boundedClient := *client
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &boundedClient
}

func catalogBase(configured, fallback string) (string, error) {
	if configured == "" {
		configured = fallback
	}
	parsed, err := url.Parse(configured)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !catalogTransportAllowed(parsed) {
		return "", fmt.Errorf("provider catalog base URL is invalid")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func catalogTransportAllowed(parsed *url.URL) bool {
	if parsed.Scheme == "https" {
		return true
	}
	host := strings.Trim(parsed.Hostname(), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func catalogListURL(base, owner, repository, directory, revision string, page int) (string, error) {
	segments := []string{"repos", owner, repository, "contents"}
	segments = append(segments, strings.Split(strings.Trim(directory, "/"), "/")...)
	rawURL := catalogJoinURL(base, segments...)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("ref", revision)
	query.Set("per_page", strconv.Itoa(catalogPageSize))
	query.Set("page", strconv.Itoa(page))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func catalogDocumentURL(base, owner, repository, revision, documentPath string) (string, error) {
	cleanPath := path.Clean(documentPath)
	if cleanPath != documentPath || strings.HasPrefix(cleanPath, "../") || path.IsAbs(cleanPath) {
		return "", fmt.Errorf("provider documentation path is invalid")
	}
	segments := []string{base, owner, repository, revision}
	segments = append(segments, strings.Split(cleanPath, "/")...)
	return catalogJoinURL(segments[0], segments[1:]...), nil
}

func catalogJoinURL(base string, segments ...string) string {
	escaped := make([]string, len(segments))
	for index, segment := range segments {
		escaped[index] = url.PathEscape(segment)
	}
	return strings.TrimRight(base, "/") + "/" + strings.Join(escaped, "/")
}

func catalogNextURL(header string) (string, bool, error) {
	for _, link := range strings.Split(header, ",") {
		parts := strings.Split(link, ";")
		if len(parts) < 2 {
			continue
		}
		isNext := false
		for _, parameter := range parts[1:] {
			if strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(parameter), "rel=")), `"`) == "next" {
				isNext = true
			}
		}
		if !isNext {
			continue
		}
		linkTarget := strings.TrimSpace(parts[0])
		if len(linkTarget) < 3 || linkTarget[0] != '<' || linkTarget[len(linkTarget)-1] != '>' {
			return "", false, fmt.Errorf("provider pagination link is malformed")
		}
		return linkTarget[1 : len(linkTarget)-1], true, nil
	}
	return "", false, nil
}

func validateCatalogNextURL(nextURL, currentURL, revision string, expectedPage int) (string, error) {
	next, err := url.Parse(nextURL)
	if err != nil {
		return "", fmt.Errorf("provider pagination URL is invalid: %w", err)
	}
	current, err := url.Parse(currentURL)
	if err != nil {
		return "", err
	}
	query := next.Query()
	page, pageErr := strconv.Atoi(query.Get("page"))
	if len(query) != 3 || len(query["ref"]) != 1 || len(query["page"]) != 1 || len(query["per_page"]) != 1 ||
		!next.IsAbs() || next.User != nil || next.Fragment != "" || !sameURLOrigin(next, current) || next.Path != current.Path || query.Get("ref") != revision ||
		query.Get("per_page") != strconv.Itoa(catalogPageSize) || pageErr != nil || page != expectedPage {
		return "", fmt.Errorf("provider pagination URL escaped its pinned listing")
	}
	return next.String(), nil
}

func sameURLOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func providerTypeName(documentPath, directory, kind, providerAddress string) (string, bool) {
	cleanPath := path.Clean(documentPath)
	prefix := directory + "/"
	if cleanPath != documentPath || !strings.HasPrefix(cleanPath, prefix) || path.Dir(cleanPath) != strings.TrimSuffix(prefix, "/") || path.Base(cleanPath) == "." {
		return "", false
	}
	name, supported := trimCatalogDocumentationExtension(path.Base(cleanPath))
	if !supported {
		return "", false
	}
	providerName := path.Base(providerAddress)
	if !strings.HasPrefix(name, providerName+"_") || strings.TrimPrefix(name, providerName+"_") == "" {
		if providerAddress != "registry.terraform.io/hashicorp/azurerm" ||
			(directory != "website/docs/r" && directory != "website/docs/d") ||
			(kind != "resource" && kind != "datasource") || name == "" {
			return "", false
		}
		name = providerName + "_" + name
	}
	return name, true
}

func isCatalogMarkdownPath(documentPath, directory string) bool {
	cleanPath := path.Clean(documentPath)
	prefix := directory + "/"
	if cleanPath != documentPath || !strings.HasPrefix(cleanPath, prefix) || path.Dir(cleanPath) != strings.TrimSuffix(prefix, "/") {
		return false
	}
	_, supported := trimCatalogDocumentationExtension(path.Base(cleanPath))
	return supported
}

func catalogDocumentationDirectory(policy ProviderPolicy, kind string) (string, bool) {
	directory, supported := catalogDirectories[kind]
	if !supported {
		return "", false
	}
	if policy.SourceRepository == "hashicorp/terraform-provider-azurerm" {
		switch kind {
		case "resource":
			return "website/docs/r", true
		case "datasource":
			return "website/docs/d", true
		default:
			return "", false
		}
	}
	return "docs/" + directory, true
}

func trimCatalogDocumentationExtension(filename string) (string, bool) {
	for _, extension := range []string{".html.markdown", ".markdown", ".md"} {
		if strings.HasSuffix(filename, extension) {
			return strings.TrimSuffix(filename, extension), true
		}
	}
	return "", false
}

func providerDocumentationRef(owner, repository, revision, documentPath string) string {
	return "https://github.com/" + owner + "/" + repository + "/blob/" + revision + "/" + documentPath
}

func newCatalogArtifact(rawURL string, body []byte, status int) catalogSourceArtifact {
	return catalogSourceArtifact{URL: rawURL, Digest: catalogDigest(body), HTTPStatus: status}
}

func catalogSourceDigest(policy ProviderPolicy, version, revision string, coverage map[string]string, artifacts []catalogSourceArtifact) (string, error) {
	orderedArtifacts := append([]catalogSourceArtifact(nil), artifacts...)
	sort.Slice(orderedArtifacts, func(left, right int) bool { return orderedArtifacts[left].URL < orderedArtifacts[right].URL })
	manifest := struct {
		ProviderAddress string                  `json:"provider_address"`
		Repository      string                  `json:"repository"`
		License         string                  `json:"license"`
		Version         string                  `json:"version"`
		Revision        string                  `json:"revision"`
		Coverage        map[string]string       `json:"coverage"`
		Artifacts       []catalogSourceArtifact `json:"artifacts"`
	}{policy.Address, policy.SourceRepository, policy.SourceLicense, version, revision, coverage, orderedArtifacts}
	canonical, err := identity.CanonicalJSON("provider-source-manifest", 1, manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func catalogDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validateCatalogVersion(version string, trackMajors []uint64) error {
	if strings.HasPrefix(version, "v") || !semver.IsValid("v"+version) || semver.Prerelease("v"+version) != "" || semver.Canonical("v"+version) != "v"+version {
		return fmt.Errorf("provider version must be an exact canonical stable SemVer without a v prefix")
	}
	major, err := strconv.ParseUint(strings.TrimPrefix(semver.Major("v"+version), "v"), 10, 64)
	if err != nil {
		return fmt.Errorf("provider version major is invalid: %w", err)
	}
	if len(trackMajors) == 0 {
		return nil
	}
	for _, allowedMajor := range trackMajors {
		if major == allowedMajor {
			return nil
		}
	}
	return fmt.Errorf("provider version %s is outside configured major tracks", version)
}

func validCommitSHA1(value string) bool {
	if len(value) != sha1.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func allCatalogKindsComplete(capabilities map[string]string) bool {
	if len(capabilities) == 0 {
		return false
	}
	for _, coverage := range capabilities {
		if coverage != "complete" {
			return false
		}
	}
	return true
}

func providerPolicyForAddress(policy CollectionPolicy, address string) (ProviderPolicy, bool) {
	canonicalAddress := terraform.CanonicalProviderAddress(address)
	if canonicalAddress == "" {
		return ProviderPolicy{}, false
	}
	for _, providerPolicy := range policy.Providers {
		if providerPolicy.Address == canonicalAddress {
			return providerPolicy, true
		}
	}
	return ProviderPolicy{}, false
}

func (collector CatalogCollector) observedAt() string {
	now := time.Now
	if collector.Now != nil {
		now = collector.Now
	}
	return now().UTC().Format(time.RFC3339)
}
