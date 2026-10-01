// Package source resolves the approved pilot module to an immutable commit.
// It does not discover the AVM catalog or execute repository-controlled code.
package source

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

const (
	// PilotRepository is the only upstream repository accepted by this slice.
	PilotRepository = "Azure/terraform-azurerm-avm-res-resources-resourcegroup"
	// PilotVersion is the reviewed stable release, including its v prefix.
	PilotVersion = "v0.4.0"
	// PilotCommit is the commit currently pointed to by PilotVersion.
	PilotCommit = "2c605230f1bcb5dc29a667f2a43258bfa9140c32"
)

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	repoPattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

// Pilot is the reviewed single-module cohort.
type Pilot struct {
	Version int `yaml:"version"`
	Module  struct {
		ID         string `yaml:"id"`
		Type       string `yaml:"type"`
		Domain     string `yaml:"domain"`
		Repository string `yaml:"repository"`
		Version    string `yaml:"version"`
		Commit     string `yaml:"commit"`
	} `yaml:"module"`
}

// LoadPilot reads and validates the approved pilot document.
func LoadPilot(path string) (Pilot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Pilot{}, fmt.Errorf("read pilot: %w", err)
	}
	var pilot Pilot
	if err = yaml.Unmarshal(data, &pilot); err != nil {
		return Pilot{}, fmt.Errorf("parse pilot: %w", err)
	}
	if err = pilot.Validate(); err != nil {
		return Pilot{}, err
	}
	return pilot, nil
}

// Validate rejects any repository, release, or commit other than the reviewed pin.
func (p Pilot) Validate() error {
	if p.Version != 1 || p.Module.ID == "" || p.Module.Type != "res" || p.Module.Domain == "" {
		return fmt.Errorf("pilot identity is incomplete")
	}
	if !repoPattern.MatchString(p.Module.Repository) || p.Module.Repository != PilotRepository {
		return fmt.Errorf("pilot repository is not approved")
	}
	if p.Module.Version != PilotVersion || p.Module.Commit != PilotCommit || !commitPattern.MatchString(p.Module.Commit) {
		return fmt.Errorf("pilot release pin does not match the reviewed commit")
	}
	return nil
}

// ArchiveURL is the public source archive for the immutable commit. The tag is
// not used here because a moved tag must not change the downloaded source.
func (p Pilot) ArchiveURL() string {
	return fmt.Sprintf("https://github.com/%s/archive/%s.tar.gz", p.Module.Repository, p.Module.Commit)
}
