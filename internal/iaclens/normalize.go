package iaclens

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

const (
	// AdapterVersion identifies this normalized projection. It changes only when
	// the public field meaning changes.
	AdapterVersion = 1
	// SupportedSchemaVersion is the only IaCLens report schema accepted here.
	SupportedSchemaVersion = 2
)

// Provenance is factory-owned identity. IaCLens does not currently supply the
// source SHA, CLI version, or run timestamp in its report.
type Provenance struct {
	ModuleID       string `json:"module_id"`
	SourceSHA      string `json:"source_sha"`
	CLIVersion     string `json:"cli_version"`
	CLICommit      string `json:"cli_commit"`
	AdapterVersion int    `json:"adapter_version"`
}

// Check is one policy result. Status is preserved verbatim and never promoted
// from unchecked or not_applicable to pass.
type Check struct {
	ID       string   `json:"id"`
	Engine   string   `json:"engine"`
	Status   string   `json:"status"`
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	Evidence []string `json:"evidence,omitempty"`
}

// Classification preserves how a unit kind was decided without retaining model
// request state or probability payloads in the public projection.
type Classification struct {
	Kind   string `json:"kind"`
	Method string `json:"method"`
	Reason string `json:"reason"`
}

// Unit joins one inventory directory to its analysis record by path.
type Unit struct {
	Path           string         `json:"path"`
	Role           string         `json:"role"`
	Owner          string         `json:"owner,omitempty"`
	OwnerScope     string         `json:"owner_scope,omitempty"`
	Classification Classification `json:"classification"`
	Checks         []Check        `json:"checks"`
	SymbolCount    int            `json:"symbol_count"`
}

// Report is the minimized factory projection of one successful IaCLens report.
type Report struct {
	SchemaVersion  int        `json:"schema_version"`
	AdapterVersion int        `json:"adapter_version"`
	Provenance     Provenance `json:"provenance"`
	RulesetName    string     `json:"ruleset_name"`
	RulesetDigest  string     `json:"ruleset_digest"`
	SourceDigest   string     `json:"source_digest"`
	Units          []Unit     `json:"units"`
}

// Normalize validates schema v2 and returns the public projection. It rejects
// duplicate paths, missing analysis joins, unknown statuses, and unsupported
// schemas instead of flattening or repairing them.
func Normalize(raw []byte, provenance Provenance) (Report, error) {
	if provenance.ModuleID == "" || provenance.SourceSHA == "" || provenance.CLIVersion == "" || provenance.CLICommit == "" {
		return Report{}, fmt.Errorf("analysis provenance is incomplete")
	}
	var envelope struct {
		IaCLens struct {
			SchemaVersion int    `json:"schema_version"`
			SourceDigest  string `json:"source_digest"`
			Ruleset       struct {
				Name    string `json:"name"`
				Version int    `json:"version"`
				Digest  string `json:"digest"`
			} `json:"ruleset"`
			Units []struct {
				Path    string            `json:"path"`
				Symbols []json.RawMessage `json:"symbols"`
			} `json:"units"`
			Analysis struct {
				Units []struct {
					Path           string `json:"path"`
					Role           string `json:"role"`
					Owner          string `json:"owner"`
					OwnerScope     string `json:"owner_scope"`
					Classification struct {
						Kind   string `json:"kind"`
						Method string `json:"method"`
						Reason string `json:"reason"`
					} `json:"classification"`
					Checks []Check `json:"checks"`
				} `json:"units"`
			} `json:"analysis"`
		} `json:"iaclens"`
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return Report{}, fmt.Errorf("parse report: %w", err)
	}
	body, ok := document["iaclens"].(map[string]any)
	if !ok {
		return Report{}, fmt.Errorf("report envelope is missing iaclens")
	}
	if version, ok := body["schema_version"].(float64); !ok || int(version) != SupportedSchemaVersion {
		return Report{}, fmt.Errorf("unsupported report schema")
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Report{}, fmt.Errorf("decode report: %w", err)
	}
	if len(envelope.IaCLens.Units) == 0 || len(envelope.IaCLens.Analysis.Units) != len(envelope.IaCLens.Units) {
		return Report{}, fmt.Errorf("inventory and analysis units do not match")
	}
	symbols := map[string]int{}
	for _, unit := range envelope.IaCLens.Units {
		if _, exists := symbols[unit.Path]; exists {
			return Report{}, fmt.Errorf("duplicate inventory path %s", unit.Path)
		}
		symbols[unit.Path] = len(unit.Symbols)
	}
	report := Report{
		SchemaVersion:  SupportedSchemaVersion,
		AdapterVersion: AdapterVersion,
		Provenance:     provenance,
		RulesetName:    envelope.IaCLens.Ruleset.Name,
		RulesetDigest:  envelope.IaCLens.Ruleset.Digest,
		SourceDigest:   envelope.IaCLens.SourceDigest,
	}
	seen := map[string]struct{}{}
	for _, unit := range envelope.IaCLens.Analysis.Units {
		if _, exists := seen[unit.Path]; exists {
			return Report{}, fmt.Errorf("duplicate analysis path %s", unit.Path)
		}
		seen[unit.Path] = struct{}{}
		count, ok := symbols[unit.Path]
		if !ok {
			return Report{}, fmt.Errorf("analysis path %s has no inventory unit", unit.Path)
		}
		if unit.Role != "component" && unit.Role != "example" {
			return Report{}, fmt.Errorf("invalid role %q", unit.Role)
		}
		if unit.Role == "example" && (unit.Owner == "" || (unit.OwnerScope != "component" && unit.OwnerScope != "repository")) {
			return Report{}, fmt.Errorf("example %s is missing ownership", unit.Path)
		}
		for i := range unit.Checks {
			if !validStatus(unit.Checks[i].Status) {
				return Report{}, fmt.Errorf("unknown check status %q", unit.Checks[i].Status)
			}
		}
		report.Units = append(report.Units, Unit{
			Path: unit.Path, Role: unit.Role, Owner: unit.Owner, OwnerScope: unit.OwnerScope,
			Classification: Classification(unit.Classification), Checks: unit.Checks, SymbolCount: count,
		})
	}
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

// Digest is a stable identifier for the normalized projection.
func (r Report) Digest() (string, error) {
	encoded, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum), nil
}

// Validate checks the normalized projection independently of JSON decoding.
func (r Report) Validate() error {
	if r.SchemaVersion != SupportedSchemaVersion || r.AdapterVersion != AdapterVersion {
		return fmt.Errorf("normalized report version is unsupported")
	}
	if r.RulesetName == "" || r.RulesetDigest == "" || r.SourceDigest == "" {
		return fmt.Errorf("normalized report is missing ruleset or source identity")
	}
	if len(r.Units) == 0 {
		return fmt.Errorf("normalized report requires units")
	}
	return nil
}

func validStatus(status string) bool {
	switch status {
	case "pass", "fail", "unchecked", "not_applicable":
		return true
	default:
		return false
	}
}
