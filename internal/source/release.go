package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"golang.org/x/mod/semver"
)

const (
	defaultGitHubAPI = "https://api.github.com"
	releasePageSize  = 100
	maxReleasePages  = 20
	maxTagDepth      = 5
	maxAPIResponse   = 4 << 20
)

var commitSHA1 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ResolvedSource is an immutable source pin derived from an official catalog
// module and the repository's latest published stable release.
type ResolvedSource struct {
	ModuleID   string    `json:"module_id"`
	Repository string    `json:"repository"`
	Version    string    `json:"version"`
	Tag        string    `json:"tag"`
	Commit     string    `json:"commit"`
	ArchiveURL string    `json:"archive_url"`
	ReleaseURL string    `json:"release_url"`
	ResolvedAt time.Time `json:"resolved_at"`
}

// Resolver resolves public GitHub releases and tag refs. APIBase and Client
// are injectable for tests; production uses the public GitHub API.
type Resolver struct {
	APIBase string
	Client  *http.Client
}

type releaseRecord struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
}

type gitObject struct {
	SHA  string `json:"sha"`
	Type string `json:"type"`
}

type gitReference struct {
	Object gitObject `json:"object"`
}

// ResolveLatest selects the highest stable published release, verifies the
// release tag and its v-prefixed/unprefixed alias, and rejects moved tags.
func (r Resolver) ResolveLatest(ctx context.Context, module catalog.Module, previous *ResolvedSource) (ResolvedSource, error) {
	if err := module.Validate(); err != nil {
		return ResolvedSource{}, err
	}
	owner, repository, err := repositoryCoordinates(module.RepositoryURL)
	if err != nil {
		return ResolvedSource{}, err
	}
	if previous != nil {
		if err = r.verifyPrevious(ctx, module, owner, repository, *previous); err != nil {
			return ResolvedSource{}, err
		}
	}
	releases, err := r.listReleases(ctx, owner, repository)
	if err != nil {
		return ResolvedSource{}, err
	}
	selected, err := selectStableRelease(releases)
	if err != nil {
		return ResolvedSource{}, err
	}
	canonical := semver.Canonical(selected.TagName)
	tags := uniqueTags(selected.TagName, canonical, strings.TrimPrefix(canonical, "v"))
	resolvedTags := make(map[string]string, len(tags))
	for _, tag := range tags {
		commit, found, resolveErr := r.resolveTag(ctx, owner, repository, tag)
		if resolveErr != nil {
			return ResolvedSource{}, resolveErr
		}
		if found {
			resolvedTags[tag] = commit
		}
	}
	commit, found := resolvedTags[selected.TagName]
	if !found {
		for _, tag := range tags {
			if candidate, ok := resolvedTags[tag]; ok {
				commit, found = candidate, true
				break
			}
		}
	}
	if !found {
		return ResolvedSource{}, fmt.Errorf("stable release tag %q does not resolve in %s/%s", selected.TagName, owner, repository)
	}
	for tag, aliasCommit := range resolvedTags {
		if aliasCommit != commit {
			return ResolvedSource{}, fmt.Errorf("ambiguous release tags %q and %q resolve to different commits", selected.TagName, tag)
		}
	}
	return ResolvedSource{
		ModuleID: module.ID, Repository: owner + "/" + repository, Version: canonical, Tag: selected.TagName,
		Commit: commit, ArchiveURL: fmt.Sprintf("https://github.com/%s/%s/archive/%s.tar.gz", owner, repository, commit),
		ReleaseURL: selected.HTMLURL, ResolvedAt: time.Now().UTC(),
	}, nil
}

func (r Resolver) verifyPrevious(ctx context.Context, module catalog.Module, owner, repository string, previous ResolvedSource) error {
	canonical := semver.Canonical(previous.Version)
	if previous.ModuleID != module.ID || previous.Repository != owner+"/"+repository || canonical == "" || canonical != previous.Version || (previous.Tag != canonical && previous.Tag != strings.TrimPrefix(canonical, "v")) || !commitSHA1.MatchString(previous.Commit) {
		return fmt.Errorf("previous source pin is invalid for module %s", module.ID)
	}
	tags := uniqueTags(previous.Tag, canonical, strings.TrimPrefix(canonical, "v"))
	for _, tag := range tags {
		commit, found, err := r.resolveTag(ctx, owner, repository, tag)
		if err != nil {
			return err
		}
		if !found {
			if tag == previous.Tag {
				return fmt.Errorf("accepted release tag %q disappeared from %s/%s", previous.Tag, owner, repository)
			}
			continue
		}
		if commit != previous.Commit {
			return fmt.Errorf("moved accepted release tag %q for %s: accepted %s, now %s", tag, module.ID, previous.Commit, commit)
		}
	}
	return nil
}

func (r Resolver) listReleases(ctx context.Context, owner, repository string) ([]releaseRecord, error) {
	all := make([]releaseRecord, 0)
	for page := 1; page <= maxReleasePages; page++ {
		endpoint := fmt.Sprintf("/repos/%s/%s/releases?per_page=%d&page=%d", url.PathEscape(owner), url.PathEscape(repository), releasePageSize, page)
		var releases []releaseRecord
		if err := r.getJSON(ctx, endpoint, &releases); err != nil {
			return nil, fmt.Errorf("list releases for %s/%s: %w", owner, repository, err)
		}
		all = append(all, releases...)
		if len(releases) < releasePageSize {
			return all, nil
		}
	}
	return nil, fmt.Errorf("release list for %s/%s exceeds %d pages", owner, repository, maxReleasePages)
}

func selectStableRelease(releases []releaseRecord) (releaseRecord, error) {
	stable := make([]releaseRecord, 0, len(releases))
	for _, release := range releases {
		if release.Draft || release.Prerelease || release.PublishedAt == "" {
			continue
		}
		if _, ok := stableVersion(release.TagName); !ok {
			continue
		}
		stable = append(stable, release)
	}
	if len(stable) == 0 {
		return releaseRecord{}, fmt.Errorf("repository has no published stable SemVer release")
	}
	sort.Slice(stable, func(i, j int) bool {
		left, _ := stableVersion(stable[i].TagName)
		right, _ := stableVersion(stable[j].TagName)
		return semver.Compare(left, right) > 0
	})
	best := stable[0]
	for _, candidate := range stable[1:] {
		candidateVersion, _ := stableVersion(candidate.TagName)
		bestVersion, _ := stableVersion(best.TagName)
		if semver.Compare(candidateVersion, bestVersion) == 0 && candidate.TagName != best.TagName {
			return releaseRecord{}, fmt.Errorf("ambiguous stable release tags %q and %q", best.TagName, candidate.TagName)
		}
	}
	return best, nil
}

func stableVersion(tag string) (string, bool) {
	version := tag
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	if !semver.IsValid(version) || semver.Prerelease(version) != "" {
		return "", false
	}
	return semver.Canonical(version), true
}

func (r Resolver) resolveTag(ctx context.Context, owner, repository, tag string) (string, bool, error) {
	endpoint := fmt.Sprintf("/repos/%s/%s/git/ref/tags/%s", url.PathEscape(owner), url.PathEscape(repository), url.PathEscape(tag))
	var reference gitReference
	if err := r.getJSON(ctx, endpoint, &reference); err != nil {
		if isNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}
	object := reference.Object
	for depth := 0; depth < maxTagDepth; depth++ {
		switch object.Type {
		case "commit":
			if !commitSHA1.MatchString(object.SHA) {
				return "", false, fmt.Errorf("tag %q resolved to invalid commit SHA", tag)
			}
			return object.SHA, true, nil
		case "tag":
			if !commitSHA1.MatchString(object.SHA) {
				return "", false, fmt.Errorf("tag %q resolved to invalid annotated tag SHA", tag)
			}
			var annotated struct {
				Object gitObject `json:"object"`
			}
			path := fmt.Sprintf("/repos/%s/%s/git/tags/%s", url.PathEscape(owner), url.PathEscape(repository), object.SHA)
			if err := r.getJSON(ctx, path, &annotated); err != nil {
				return "", false, err
			}
			object = annotated.Object
		default:
			return "", false, fmt.Errorf("tag %q resolved to unsupported Git object type %q", tag, object.Type)
		}
	}
	return "", false, fmt.Errorf("tag %q exceeds annotated tag depth limit", tag)
}

func (r Resolver) getJSON(ctx context.Context, path string, output any) error {
	base := strings.TrimRight(r.APIBase, "/")
	if base == "" {
		base = defaultGitHubAPI
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "avm-intelligence-factory")
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponse+1))
	if err != nil {
		return err
	}
	if len(body) > maxAPIResponse {
		return fmt.Errorf("GitHub API response exceeds %d bytes", maxAPIResponse)
	}
	if response.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &apiError)
		if apiError.Message != "" {
			return fmt.Errorf("GitHub API status %d: %s", response.StatusCode, apiError.Message)
		}
		return fmt.Errorf("GitHub API status %d", response.StatusCode)
	}
	if err = json.Unmarshal(body, output); err != nil {
		return fmt.Errorf("decode GitHub API response: %w", err)
	}
	return nil
}

var errNotFound = fmt.Errorf("not found")

func isNotFound(err error) bool {
	return err == errNotFound
}

func repositoryCoordinates(raw string) (string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("invalid public GitHub repository URL %q", raw)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Azure") || parts[1] == "" {
		return "", "", fmt.Errorf("repository URL is not an Azure GitHub repository")
	}
	return parts[0], parts[1], nil
}

func uniqueTags(tags ...string) []string {
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(tags))
	for _, tag := range tags {
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		unique = append(unique, tag)
	}
	return unique
}
