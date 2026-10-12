package provider

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/terraform"
	"gopkg.in/yaml.v3"
)

const (
	collectionPolicySchemaVersion = 1
	maxPolicyReleases             = 50
	maxPolicyPages                = 100
	maxPolicyResponseBytes        = 5 << 20
	maxPolicyTotalBytes           = 50 << 20
	maxPolicyWallTimeSeconds      = 3600
)

var (
	providerAddressPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)
	providerRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	providerLicensePattern    = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	providerTypeNamePattern   = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

// CollectionPolicy is the reviewed allowlist and resource budget for public
// provider metadata collection. It does not assert that a provider supports
// every configured metadata kind; observed coverage is recorded per snapshot.
type CollectionPolicy struct {
	SchemaVersion int              `yaml:"schema_version"`
	Providers     []ProviderPolicy `yaml:"providers"`
}

// ProviderPolicy identifies one provider source, its assessment tracks, and
// the metadata kinds and budgets a future collector may attempt.
type ProviderPolicy struct {
	Address          string           `yaml:"address"`
	SourceRepository string           `yaml:"source_repository"`
	SourceLicense    string           `yaml:"source_license"`
	TrackMajors      []uint64         `yaml:"track_majors"`
	CollectionKinds  []string         `yaml:"collection_kinds"`
	Limits           CollectionLimits `yaml:"limits"`
}

// CollectionLimits bounds one provider collection claim.
type CollectionLimits struct {
	MaxReleases        int   `yaml:"max_releases"`
	MaxPages           int   `yaml:"max_pages"`
	MaxResponseBytes   int64 `yaml:"max_response_bytes"`
	MaxTotalBytes      int64 `yaml:"max_total_bytes"`
	MaxWallTimeSeconds int   `yaml:"max_wall_time_seconds"`
}

var allowedCollectionKinds = map[string]struct{}{
	"action": {}, "datasource": {}, "ephemeral": {}, "function": {}, "resource": {},
}

// LoadCollectionPolicy parses one reviewed YAML file, rejects unknown fields,
// and validates its provider identities and collection bounds.
func LoadCollectionPolicy(path string) (CollectionPolicy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CollectionPolicy{}, err
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	var policy CollectionPolicy
	if err = decoder.Decode(&policy); err != nil {
		return CollectionPolicy{}, fmt.Errorf("parse provider collection policy: %w", err)
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return CollectionPolicy{}, fmt.Errorf("provider collection policy must contain one YAML document")
		}
		return CollectionPolicy{}, fmt.Errorf("parse trailing provider policy content: %w", err)
	}
	if err = policy.Validate(); err != nil {
		return CollectionPolicy{}, err
	}
	return policy, nil
}

// Validate rejects ambiguous provider identity, track, kind, and resource
// budget entries before a collector can perform any network request.
func (policy CollectionPolicy) Validate() error {
	if policy.SchemaVersion != collectionPolicySchemaVersion || len(policy.Providers) == 0 {
		return fmt.Errorf("provider collection policy metadata or allowlist is invalid")
	}
	seenProviders := make(map[string]struct{}, len(policy.Providers))
	for index, provider := range policy.Providers {
		if err := provider.Validate(); err != nil {
			return fmt.Errorf("provider policy at index %d: %w", index, err)
		}
		canonicalAddress := terraform.CanonicalProviderAddress(provider.Address)
		if canonicalAddress != provider.Address {
			return fmt.Errorf("provider address %q must use canonical lowercase form", provider.Address)
		}
		if _, duplicate := seenProviders[canonicalAddress]; duplicate {
			return fmt.Errorf("duplicate provider address %q", provider.Address)
		}
		seenProviders[canonicalAddress] = struct{}{}
	}
	return nil
}

// Validate checks one provider entry without contacting its source.
func (provider ProviderPolicy) Validate() error {
	if !providerAddressPattern.MatchString(provider.Address) {
		return fmt.Errorf("provider address must include registry host, namespace, and name")
	}
	if !providerRepositoryPattern.MatchString(provider.SourceRepository) || !providerLicensePattern.MatchString(provider.SourceLicense) {
		return fmt.Errorf("provider source repository and SPDX license are required")
	}
	if err := validateTrackMajors(provider.TrackMajors); err != nil {
		return err
	}
	if err := validateCollectionKinds(provider.CollectionKinds); err != nil {
		return err
	}
	return provider.Limits.Validate()
}

func validateTrackMajors(majors []uint64) error {
	seen := make(map[uint64]struct{}, len(majors))
	for index, major := range majors {
		if index > 0 && major <= majors[index-1] {
			return fmt.Errorf("track majors must be unique and ascending")
		}
		if _, duplicate := seen[major]; duplicate {
			return fmt.Errorf("duplicate track major %d", major)
		}
		seen[major] = struct{}{}
	}
	return nil
}

func validateCollectionKinds(kinds []string) error {
	if len(kinds) == 0 {
		return fmt.Errorf("at least one collection kind is required")
	}
	seen := make(map[string]struct{}, len(kinds))
	previous := ""
	for _, kind := range kinds {
		if _, allowed := allowedCollectionKinds[kind]; !allowed || (previous != "" && kind <= previous) {
			return fmt.Errorf("collection kinds must be known, unique, and sorted")
		}
		if _, duplicate := seen[kind]; duplicate {
			return fmt.Errorf("duplicate collection kind %q", kind)
		}
		seen[kind] = struct{}{}
		previous = kind
	}
	return nil
}

func (limits CollectionLimits) Validate() error {
	if limits.MaxReleases < 1 || limits.MaxReleases > maxPolicyReleases || limits.MaxPages < 1 || limits.MaxPages > maxPolicyPages ||
		limits.MaxResponseBytes < 1 || limits.MaxResponseBytes > maxPolicyResponseBytes ||
		limits.MaxTotalBytes < limits.MaxResponseBytes || limits.MaxTotalBytes > maxPolicyTotalBytes ||
		limits.MaxWallTimeSeconds < 1 || limits.MaxWallTimeSeconds > maxPolicyWallTimeSeconds {
		return fmt.Errorf("provider collection limits are invalid or exceed reviewed hard bounds")
	}
	return nil
}
