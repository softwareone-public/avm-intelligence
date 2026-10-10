// Package contextset defines the neutral durable contract for required
// assessment contexts derived from accepted module facts and reviewed policy.
package contextset

import (
	"fmt"
	"regexp"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

const SchemaVersion = 1

const (
	ApplicabilityApplicable    = "applicable"
	ApplicabilityNotApplicable = "not_applicable"
	ApplicabilityUnresolved    = "unresolved"
	CandidateEligible          = "eligible"
	CandidateIneligible        = "ineligible"
	CandidateUnresolved        = "unresolved"
	LatestSourceContext        = "latest-source"
)

var (
	modulePattern  = regexp.MustCompile(`^avm-(res|ptn|utl)-[a-z0-9]+(?:-[a-z0-9]+)*$`)
	sha1Pattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	releasePattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	addressPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)
)

type ContextState struct {
	ContextID     string `json:"context_id"`
	Track         string `json:"track"`
	Applicability string `json:"applicability"`
	Candidate     string `json:"candidate"`
}

type Context struct {
	State           ContextState `json:"state"`
	ProviderAddress string       `json:"provider_address,omitempty"`
	ProviderMajor   uint64       `json:"provider_major,omitempty"`
	Reason          string       `json:"reason,omitempty"`
}

type Set struct {
	SchemaVersion     int       `json:"schema_version"`
	RecordType        string    `json:"record_type"`
	ID                string    `json:"required_context_set_id"`
	ModuleID          string    `json:"module_id"`
	SourceSHA         string    `json:"source_sha"`
	AVMReleaseVersion string    `json:"avm_release_version"`
	FactsID           string    `json:"facts_id"`
	PolicyID          string    `json:"policy_id"`
	Complete          bool      `json:"complete"`
	Contexts          []Context `json:"contexts"`
}

type setIdentity struct {
	ModuleID          string    `json:"module_id"`
	SourceSHA         string    `json:"source_sha"`
	AVMReleaseVersion string    `json:"avm_release_version"`
	FactsID           string    `json:"facts_id"`
	PolicyID          string    `json:"policy_id"`
	Complete          bool      `json:"complete"`
	Contexts          []Context `json:"contexts"`
}

func New(moduleID, sourceSHA, avmReleaseVersion, factsID, policyID string, complete bool, contexts []Context) (Set, error) {
	set := Set{
		SchemaVersion: SchemaVersion, RecordType: "required_assessment_context_set",
		ModuleID: moduleID, SourceSHA: sourceSHA, AVMReleaseVersion: avmReleaseVersion,
		FactsID: factsID, PolicyID: policyID, Complete: complete, Contexts: append([]Context(nil), contexts...),
	}
	id, err := set.identity()
	if err != nil {
		return Set{}, err
	}
	set.ID = id
	if err = set.Validate(); err != nil {
		return Set{}, err
	}
	return set, nil
}

func (set Set) Validate() error {
	if set.SchemaVersion != SchemaVersion || set.RecordType != "required_assessment_context_set" ||
		!modulePattern.MatchString(set.ModuleID) || !sha1Pattern.MatchString(set.SourceSHA) || !releasePattern.MatchString(set.AVMReleaseVersion) ||
		identity.ValidateRecordID("facts", set.FactsID) != nil || identity.ValidateRecordID("provider-policy", set.PolicyID) != nil || identity.ValidateRecordID("required-context-set", set.ID) != nil || len(set.Contexts) == 0 {
		return fmt.Errorf("required assessment context set identity is invalid")
	}
	unresolved := false
	previous := ""
	for _, context := range set.Contexts {
		if err := validateContextState(context.State); err != nil {
			return err
		}
		if previous != "" && context.State.ContextID <= previous {
			return fmt.Errorf("required assessment contexts must be unique and sorted")
		}
		previous = context.State.ContextID
		if context.State.Applicability == ApplicabilityUnresolved || context.State.Candidate == CandidateUnresolved {
			unresolved = true
			if context.Reason == "" {
				return fmt.Errorf("unresolved required context must explain its state")
			}
		} else if context.State.Applicability == ApplicabilityApplicable {
			if context.ProviderAddress == "" && context.ProviderMajor != 0 {
				return fmt.Errorf("assessment context major requires a provider address")
			}
			if context.ProviderAddress != "" && (context.ProviderMajor == 0 || !addressPattern.MatchString(context.ProviderAddress) || context.State.ContextID != fmt.Sprintf("%s/%d", context.ProviderAddress, context.ProviderMajor)) {
				return fmt.Errorf("assessment context does not match its provider address and major")
			}
		}
	}
	if set.Complete == unresolved {
		return fmt.Errorf("required context completeness conflicts with unresolved contexts")
	}
	id, err := set.identity()
	if err != nil || set.ID != id {
		return fmt.Errorf("required assessment context set identity does not match its contents")
	}
	return nil
}

func (set Set) identity() (string, error) {
	return identity.ID("required-context-set", SchemaVersion, setIdentity{
		ModuleID: set.ModuleID, SourceSHA: set.SourceSHA, AVMReleaseVersion: set.AVMReleaseVersion,
		FactsID: set.FactsID, PolicyID: set.PolicyID, Complete: set.Complete, Contexts: set.Contexts,
	})
}

func validateContextState(state ContextState) error {
	if state.ContextID == "" || state.Track == "" ||
		state.Applicability != ApplicabilityApplicable && state.Applicability != ApplicabilityNotApplicable && state.Applicability != ApplicabilityUnresolved ||
		state.Candidate != CandidateEligible && state.Candidate != CandidateIneligible && state.Candidate != CandidateUnresolved {
		return fmt.Errorf("required assessment context state is invalid")
	}
	if err := identity.ValidateRelativePath(state.ContextID, false); err != nil {
		return err
	}
	return identity.ValidateRelativePath(state.Track, false)
}
