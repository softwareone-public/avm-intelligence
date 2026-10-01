// Package catalog reads the official AVM Terraform catalog feeds. It is
// read-only: discovery never creates Issues, mutates Projects, or publishes data.
package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// Resource is the official resource-module feed category.
	Resource Kind = "res"
	// Pattern is the official pattern-module feed category.
	Pattern Kind = "ptn"
	// Utility is the official utility-module feed category.
	Utility      Kind = "utl"
	maxFeedBytes      = 16 << 20
	maxAttempts       = 3
)

// Kind is the stable AVM module category.
type Kind string

// Module is the normalized public catalog row. Source-specific columns that
// have no value for a category remain empty.
type Module struct {
	ID                   string `json:"id"`
	Type                 Kind   `json:"type"`
	Status               string `json:"status"`
	RepositoryURL        string `json:"repository_url"`
	RegistryURL          string `json:"registry_url,omitempty"`
	DisplayName          string `json:"display_name"`
	AlternativeNames     string `json:"alternative_names,omitempty"`
	Description          string `json:"description"`
	Comments             string `json:"comments,omitempty"`
	FirstPublished       string `json:"first_published,omitempty"`
	ProviderNamespace    string `json:"provider_namespace,omitempty"`
	ResourceType         string `json:"resource_type,omitempty"`
	ParentModule         string `json:"parent_module,omitempty"`
	PrimaryOwnerHandle   string `json:"primary_owner_handle,omitempty"`
	PrimaryOwnerName     string `json:"primary_owner_name,omitempty"`
	SecondaryOwnerHandle string `json:"secondary_owner_handle,omitempty"`
	SecondaryOwnerName   string `json:"secondary_owner_name,omitempty"`
}

// Source is one official upstream catalog feed.
type Source struct {
	Kind Kind
	URL  string
}

// FeedSnapshot records the exact public input used to build a candidate catalog.
type FeedSnapshot struct {
	Kind        Kind   `json:"kind"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	ModuleCount int    `json:"module_count"`
}

// Snapshot is a validated candidate catalog. It is not yet accepted state and
// callers must not publish it without comparing against the previous snapshot.
type Snapshot struct {
	SchemaVersion int            `json:"schema_version"`
	FetchedAt     time.Time      `json:"fetched_at"`
	Feeds         []FeedSnapshot `json:"feeds"`
	Modules       []Module       `json:"modules"`
}

const SnapshotSchemaVersion = 1

// Validate checks candidate schema, feed provenance, row counts, and identities.
func (s Snapshot) Validate() error {
	if s.SchemaVersion != SnapshotSchemaVersion || s.FetchedAt.IsZero() {
		return fmt.Errorf("catalog snapshot metadata is invalid")
	}
	if len(s.Feeds) != len(OfficialSources) || len(s.Modules) == 0 {
		return fmt.Errorf("catalog snapshot must contain all feeds and modules")
	}
	feedByKind := make(map[Kind]FeedSnapshot, len(s.Feeds))
	for _, feed := range s.Feeds {
		if _, exists := feedByKind[feed.Kind]; exists {
			return fmt.Errorf("catalog snapshot repeats feed %q", feed.Kind)
		}
		feedByKind[feed.Kind] = feed
	}
	counts := make(map[Kind]int, len(OfficialSources))
	seen := make(map[string]struct{}, len(s.Modules))
	for _, module := range s.Modules {
		if err := module.Validate(); err != nil {
			return err
		}
		key := strings.ToLower(module.ID)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("catalog snapshot repeats module %q", module.ID)
		}
		seen[key] = struct{}{}
		counts[module.Type]++
	}
	for _, source := range OfficialSources {
		feed, exists := feedByKind[source.Kind]
		if !exists || feed.URL != source.URL || len(feed.SHA256) != sha256.Size*2 || feed.ModuleCount != counts[source.Kind] || feed.ModuleCount == 0 {
			return fmt.Errorf("catalog snapshot provenance is invalid for %s", source.Kind)
		}
		if _, err := hex.DecodeString(feed.SHA256); err != nil {
			return fmt.Errorf("catalog snapshot has invalid %s feed digest", source.Kind)
		}
	}
	return nil
}

// OfficialSources is the reviewed list of official Terraform AVM feeds.
var OfficialSources = []Source{
	{Kind: Resource, URL: "https://raw.githubusercontent.com/Azure/Azure-Verified-Modules/main/docs/static/module-indexes/TerraformResourceModules.csv"},
	{Kind: Pattern, URL: "https://raw.githubusercontent.com/Azure/Azure-Verified-Modules/main/docs/static/module-indexes/TerraformPatternModules.csv"},
	{Kind: Utility, URL: "https://raw.githubusercontent.com/Azure/Azure-Verified-Modules/main/docs/static/module-indexes/TerraformUtilityModules.csv"},
}

var moduleIDPattern = regexp.MustCompile(`^avm-(res|ptn|utl)-[a-z0-9]+(?:-[a-z0-9]+)*$`)

var commonHeaders = []string{
	"ModuleDisplayName", "AlternativeNames", "ModuleName", "ModuleStatus", "RepoURL",
	"PublicRegistryReference", "PrimaryModuleOwnerGHHandle", "PrimaryModuleOwnerDisplayName",
	"SecondaryModuleOwnerGHHandle", "SecondaryModuleOwnerDisplayName", "Description", "Comments", "FirstPublishedIn",
}

var resourceHeaders = []string{"ProviderNamespace", "ResourceType", "ParentModule"}

var knownStatuses = map[string]struct{}{
	"Available":  {},
	"Proposed":   {},
	"Deprecated": {},
	"Orphaned":   {},
}

// ParseFeed parses one feed and validates its category-specific contract.
func ParseFeed(kind Kind, data []byte) ([]Module, error) {
	reader, err := newFeedReader(kind, data)
	if err != nil {
		return nil, err
	}
	columns, err := readHeaders(kind, reader)
	if err != nil {
		return nil, err
	}
	modules := make([]Module, 0)
	seen := make(map[string]struct{})
	for rowNumber := 2; ; rowNumber++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("%s catalog row %d: %w", kind, rowNumber, readErr)
		}
		module, rowErr := parseRow(kind, record, len(columns), columns)
		if rowErr != nil {
			return nil, fmt.Errorf("%s catalog row %d: %w", kind, rowNumber, rowErr)
		}
		key := strings.ToLower(module.ID)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%s catalog has duplicate module ID %q", kind, module.ID)
		}
		seen[key] = struct{}{}
		modules = append(modules, module)
	}
	if len(modules) == 0 {
		return nil, fmt.Errorf("%s catalog contains no modules", kind)
	}
	return modules, nil
}

func newFeedReader(kind Kind, data []byte) (*csv.Reader, error) {
	if !validKind(kind) {
		return nil, fmt.Errorf("unknown catalog kind %q", kind)
	}
	if len(data) == 0 || len(data) > maxFeedBytes {
		return nil, fmt.Errorf("%s catalog size is invalid", kind)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%s catalog is not valid UTF-8", kind)
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\uFEFF")))
	reader.FieldsPerRecord = -1
	return reader, nil
}

func readHeaders(kind Kind, reader *csv.Reader) (map[string]int, error) {
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("%s catalog header: %w", kind, err)
	}
	columns := make(map[string]int, len(header))
	for index, raw := range header {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, fmt.Errorf("%s catalog has an empty header", kind)
		}
		if _, exists := columns[name]; exists {
			return nil, fmt.Errorf("%s catalog has duplicate header %q", kind, name)
		}
		columns[name] = index
	}
	if err = validateHeaders(kind, columns); err != nil {
		return nil, err
	}
	return columns, nil
}

func validateHeaders(kind Kind, columns map[string]int) error {
	required := commonHeaders
	if kind == Resource {
		required = append(append([]string(nil), commonHeaders...), resourceHeaders...)
	}
	for _, name := range required {
		if _, exists := columns[name]; !exists {
			return fmt.Errorf("%s catalog is missing required header %q", kind, name)
		}
	}
	return nil
}

func parseRow(kind Kind, record []string, expectedColumns int, columns map[string]int) (Module, error) {
	if len(record) != expectedColumns {
		return Module{}, fmt.Errorf("has %d columns; expected %d", len(record), expectedColumns)
	}
	value := func(name string) string {
		index, exists := columns[name]
		if !exists {
			return ""
		}
		text := strings.TrimSpace(record[index])
		if strings.EqualFold(text, "n/a") {
			return ""
		}
		return text
	}
	module := Module{
		ID: value("ModuleName"), Type: kind, Status: value("ModuleStatus"), RepositoryURL: value("RepoURL"),
		RegistryURL: value("PublicRegistryReference"), DisplayName: value("ModuleDisplayName"),
		AlternativeNames: value("AlternativeNames"), Description: value("Description"), Comments: value("Comments"),
		FirstPublished: value("FirstPublishedIn"), ProviderNamespace: value("ProviderNamespace"),
		ResourceType: value("ResourceType"), ParentModule: value("ParentModule"),
		PrimaryOwnerHandle: value("PrimaryModuleOwnerGHHandle"), PrimaryOwnerName: value("PrimaryModuleOwnerDisplayName"),
		SecondaryOwnerHandle: value("SecondaryModuleOwnerGHHandle"), SecondaryOwnerName: value("SecondaryModuleOwnerDisplayName"),
	}
	return module, module.Validate()
}

// ParseFeeds parses all three official feeds and rejects duplicate identities
// across categories. It returns no partial catalog on any feed error.
func ParseFeeds(feeds map[Kind][]byte) ([]Module, error) {
	if len(feeds) != len(OfficialSources) {
		return nil, fmt.Errorf("expected all %d official feeds; received %d", len(OfficialSources), len(feeds))
	}
	modules := make([]Module, 0)
	seen := make(map[string]struct{})
	for _, source := range OfficialSources {
		data, ok := feeds[source.Kind]
		if !ok {
			return nil, fmt.Errorf("missing %s catalog feed", source.Kind)
		}
		parsed, err := ParseFeed(source.Kind, data)
		if err != nil {
			return nil, err
		}
		for _, module := range parsed {
			key := strings.ToLower(module.ID)
			if _, exists := seen[key]; exists {
				return nil, fmt.Errorf("catalog feeds contain duplicate module ID %q", module.ID)
			}
			seen[key] = struct{}{}
			modules = append(modules, module)
		}
	}
	return modules, nil
}

// Discover fetches and validates every official feed. Any systemic failure
// rejects the entire candidate catalog instead of returning a partial result.
func Discover(ctx context.Context, client *http.Client) (Snapshot, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	feeds := make(map[Kind][]byte, len(OfficialSources))
	for _, source := range OfficialSources {
		data, err := fetch(ctx, client, source.URL)
		if err != nil {
			return Snapshot{}, fmt.Errorf("fetch %s catalog: %w", source.Kind, err)
		}
		feeds[source.Kind] = data
	}
	modules, err := ParseFeeds(feeds)
	if err != nil {
		return Snapshot{}, err
	}
	return buildSnapshot(feeds, modules, time.Now().UTC()), nil
}

func buildSnapshot(feeds map[Kind][]byte, modules []Module, fetchedAt time.Time) Snapshot {
	counts := make(map[Kind]int, len(OfficialSources))
	for _, module := range modules {
		counts[module.Type]++
	}
	snapshot := Snapshot{SchemaVersion: SnapshotSchemaVersion, FetchedAt: fetchedAt.UTC(), Modules: modules}
	for _, source := range OfficialSources {
		digest := sha256.Sum256(feeds[source.Kind])
		snapshot.Feeds = append(snapshot.Feeds, FeedSnapshot{
			Kind: source.Kind, URL: source.URL, SHA256: hex.EncodeToString(digest[:]), ModuleCount: counts[source.Kind],
		})
	}
	return snapshot
}

func fetch(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		body, retryAfter, retry, err := fetchAttempt(ctx, client, endpoint)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry || attempt == maxAttempts {
			break
		}
		if err := waitRetry(ctx, retryDelay(attempt, retryAfter, time.Now())); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("after %d attempts: %w", maxAttempts, lastErr)
}

func fetchAttempt(ctx context.Context, client *http.Client, endpoint string) ([]byte, string, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", false, err
	}
	request.Header.Set("Accept", "text/csv, text/plain;q=0.9, */*;q=0.1")
	request.Header.Set("User-Agent", "avm-intelligence-factory")
	response, err := client.Do(request)
	if err != nil {
		return nil, "", true, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxFeedBytes+1))
	if err != nil {
		return nil, response.Header.Get("Retry-After"), true, err
	}
	if len(body) > maxFeedBytes {
		return nil, "", false, fmt.Errorf("feed exceeds %d bytes", maxFeedBytes)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return body, "", false, nil
	}
	err = fmt.Errorf("HTTP %d", response.StatusCode)
	return nil, response.Header.Get("Retry-After"), retryable(response.StatusCode), err
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryable(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status == http.StatusInternalServerError || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func retryDelay(attempt int, retryAfter string, now time.Time) time.Duration {
	base := time.Duration(1<<uint(attempt-1)) * 250 * time.Millisecond
	delay := base + time.Duration(rand.Float64()*float64(base/2))
	if retryAfter != "" {
		if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
			delay = max(delay, time.Duration(seconds)*time.Second)
		} else if retryTime, err := http.ParseTime(retryAfter); err == nil {
			delay = max(delay, retryTime.Sub(now))
		}
	}
	return min(delay, 10*time.Second)
}

// Validate checks stable identity, lifecycle state and URL allowlists.
func (m Module) Validate() error {
	if !moduleIDPattern.MatchString(m.ID) || !strings.HasPrefix(m.ID, "avm-"+string(m.Type)+"-") {
		return fmt.Errorf("invalid stable module ID %q for type %q", m.ID, m.Type)
	}
	if _, ok := knownStatuses[m.Status]; !ok {
		return fmt.Errorf("unknown lifecycle status %q", m.Status)
	}
	if m.DisplayName == "" || m.Description == "" {
		return fmt.Errorf("module %s has an empty display name or description", m.ID)
	}
	if err := validateRepositoryURL(m.RepositoryURL); err != nil {
		return fmt.Errorf("module %s repository URL: %w", m.ID, err)
	}
	if m.RegistryURL != "" {
		if err := validateRegistryURL(m.RegistryURL, m.ID); err != nil {
			return fmt.Errorf("module %s registry URL: %w", m.ID, err)
		}
	}
	return nil
}

func validKind(kind Kind) bool {
	return kind == Resource || kind == Pattern || kind == Utility
}

func validateRepositoryURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must be an HTTPS GitHub URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Azure") || parts[1] == "" || parts[1] == "." || parts[1] == ".." {
		return fmt.Errorf("must point to a repository owned by Azure")
	}
	return nil
}

func validateRegistryURL(raw, moduleID string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "registry.terraform.io") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must be an HTTPS Terraform Registry URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "modules" || !strings.EqualFold(parts[1], "Azure") || parts[2] != moduleID {
		return fmt.Errorf("must reference Azure module %s", moduleID)
	}
	return nil
}
