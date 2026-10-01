// Package toolchain loads the reviewed IaCLens release pin. It verifies archive
// identity before any worker is allowed to execute a downloaded binary.
package toolchain

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

const (
	// SupportedVersion is the only toolchain document version accepted by this factory.
	SupportedVersion = 1
	// RequiredSchemaVersion is the IaCLens report schema consumed by the normalizer.
	RequiredSchemaVersion = 2
	// RequiredRulesetFormatVersion is the ruleset document version, not a policy revision.
	RequiredRulesetFormatVersion = 1
)

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	assetPattern  = regexp.MustCompile(`^iaclens_[0-9]+\.[0-9]+\.[0-9]+_[a-z0-9]+_[a-z0-9]+\.(tar\.gz|zip)$`)
)

// Asset is one verified platform archive from the pinned IaCLens release.
type Asset struct {
	OS     string `yaml:"os"`
	Arch   string `yaml:"arch"`
	Name   string `yaml:"name"`
	SHA256 string `yaml:"sha256"`
}

// Toolchain is the complete reviewed consumer pin.
type Toolchain struct {
	Version int `yaml:"version"`
	CLI     struct {
		Repository           string `yaml:"repository"`
		Version              string `yaml:"version"`
		Commit               string `yaml:"commit"`
		SchemaVersion        int    `yaml:"schema_version"`
		RulesetFormatVersion int    `yaml:"ruleset_format_version"`
	} `yaml:"cli"`
	Adapter struct {
		Version int `yaml:"version"`
	} `yaml:"adapter"`
	Assets []Asset `yaml:"assets"`
}

// Load reads and validates a toolchain document. Unknown fields are rejected so
// a renamed pin cannot be silently ignored.
func Load(path string) (Toolchain, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Toolchain{}, fmt.Errorf("read toolchain: %w", err)
	}
	var doc Toolchain
	if err = yaml.Unmarshal(data, &doc); err != nil {
		return Toolchain{}, fmt.Errorf("parse toolchain: %w", err)
	}
	if err = doc.Validate(); err != nil {
		return Toolchain{}, err
	}
	return doc, nil
}

// Validate checks the consumer contract without trusting display strings alone.
func (t Toolchain) Validate() error {
	if t.Version != SupportedVersion {
		return fmt.Errorf("unsupported toolchain version %d", t.Version)
	}
	if t.CLI.Repository != "Ledermayer/iaclens" || t.CLI.Version == "" || !commitPattern.MatchString(t.CLI.Commit) {
		return fmt.Errorf("toolchain CLI identity is incomplete")
	}
	if t.CLI.SchemaVersion != RequiredSchemaVersion || t.CLI.RulesetFormatVersion != RequiredRulesetFormatVersion {
		return fmt.Errorf("toolchain schema or ruleset format is incompatible")
	}
	if t.Adapter.Version < 1 {
		return fmt.Errorf("toolchain adapter version is required")
	}
	if len(t.Assets) == 0 {
		return fmt.Errorf("toolchain requires at least one asset")
	}
	seen := map[string]struct{}{}
	for _, asset := range t.Assets {
		key := asset.OS + "/" + asset.Arch
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate toolchain asset %s", key)
		}
		seen[key] = struct{}{}
		if !assetPattern.MatchString(asset.Name) || !digestPattern.MatchString(asset.SHA256) {
			return fmt.Errorf("invalid toolchain asset %s/%s", asset.OS, asset.Arch)
		}
	}
	if _, err := t.Asset("linux", "amd64"); err != nil {
		return fmt.Errorf("toolchain requires the linux/amd64 worker asset: %w", err)
	}
	return nil
}

// Asset returns the archive selected for one worker platform.
func (t Toolchain) Asset(osName, arch string) (Asset, error) {
	for _, asset := range t.Assets {
		if asset.OS == osName && asset.Arch == arch {
			return asset, nil
		}
	}
	return Asset{}, fmt.Errorf("no toolchain asset for %s/%s", osName, arch)
}
