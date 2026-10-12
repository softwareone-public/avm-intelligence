package provider

import (
	"fmt"
	"sort"

	"github.com/softwareone-public/avm-intelligence/internal/contextset"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

// DeriveRequiredAssessmentContexts derives provider-major lanes from exact
// accepted facts and the reviewed provider policy. The policy's track_majors
// list defines independent provider contexts; it does not form a Cartesian
// product. Missing policy or incomplete provider-requirement extraction is
// retained as an unresolved context instead of being treated as inapplicable.
func DeriveRequiredAssessmentContexts(facts terraform.Facts, avmReleaseVersion string, policy CollectionPolicy) (contextset.Set, error) {
	if err := facts.Validate(); err != nil {
		return contextset.Set{}, fmt.Errorf("validate accepted assessment facts: %w", err)
	}
	if err := policy.Validate(); err != nil {
		return contextset.Set{}, fmt.Errorf("validate reviewed provider track policy: %w", err)
	}
	policyID, err := AssessmentTrackPolicyID(policy)
	if err != nil {
		return contextset.Set{}, fmt.Errorf("identify reviewed provider track policy: %w", err)
	}
	addresses := make(map[string]struct{})
	for _, unit := range facts.Units {
		for _, symbol := range unit.Symbols {
			if symbol.ProviderAddress != "" && !terraform.IsBuiltinTerraformSymbol(symbol) {
				if address := terraform.CanonicalProviderAddress(symbol.ProviderAddress); address != "" {
					addresses[address] = struct{}{}
				}
			}
		}
	}
	orderedAddresses := make([]string, 0, len(addresses))
	for address := range addresses {
		orderedAddresses = append(orderedAddresses, address)
	}
	sort.Strings(orderedAddresses)

	contexts := make([]contextset.Context, 0)
	complete := true
	providerFactsComplete := facts.Coverage.Capabilities[terraform.CapabilityProviderRequirements] == terraform.CoverageComplete
	for _, address := range orderedAddresses {
		providerPolicy, found := providerPolicyForAddress(policy, address)
		if !found || len(providerPolicy.TrackMajors) == 0 {
			complete = false
			reason := "provider assessment tracks are not configured"
			if !found {
				reason = "provider is absent from reviewed assessment track policy"
			}
			contextID, idErr := unresolvedAssessmentContextID(address)
			if idErr != nil {
				return contextset.Set{}, idErr
			}
			contexts = append(contexts, contextset.Context{
				State:           contextset.ContextState{ContextID: contextID, Track: contextID, Applicability: contextset.ApplicabilityUnresolved, Candidate: contextset.CandidateUnresolved},
				ProviderAddress: address, Reason: reason,
			})
			continue
		}
		for _, major := range providerPolicy.TrackMajors {
			contextID := fmt.Sprintf("%s/%d", address, major)
			contexts = append(contexts, contextset.Context{
				State:           contextset.ContextState{ContextID: contextID, Track: contextID, Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible},
				ProviderAddress: address, ProviderMajor: major,
			})
		}
	}
	if !providerFactsComplete {
		complete = false
		contexts = append(contexts, contextset.Context{
			State:  contextset.ContextState{ContextID: "unresolved/provider-requirements", Track: "unresolved/provider-requirements", Applicability: contextset.ApplicabilityUnresolved, Candidate: contextset.CandidateUnresolved},
			Reason: "accepted provider requirement facts are incomplete",
		})
	}
	if len(contexts) == 0 && providerFactsComplete {
		contexts = append(contexts, contextset.Context{
			State: contextset.ContextState{ContextID: contextset.LatestSourceContext, Track: contextset.LatestSourceContext, Applicability: contextset.ApplicabilityApplicable, Candidate: contextset.CandidateEligible},
		})
	}
	sort.Slice(contexts, func(left, right int) bool {
		return contexts[left].State.ContextID < contexts[right].State.ContextID
	})
	set, err := contextset.New(facts.ModuleID, facts.SourceSHA, avmReleaseVersion, facts.FactsID, policyID, complete, contexts)
	if err != nil {
		return contextset.Set{}, fmt.Errorf("build required assessment contexts: %w", err)
	}
	return set, nil
}

// AssessmentTrackPolicyID identifies the exact reviewed provider-major track
// policy shared by accepted facts receipts and assessment input imports.
func AssessmentTrackPolicyID(policy CollectionPolicy) (string, error) {
	if err := policy.Validate(); err != nil {
		return "", fmt.Errorf("validate assessment track policy: %w", err)
	}
	return identity.ID("provider-policy", 1, policy)
}

func unresolvedAssessmentContextID(providerAddress string) (string, error) {
	id, err := identity.ID("assessment-provider", 1, providerAddress)
	if err != nil {
		return "", err
	}
	return "unresolved/provider-" + id, nil
}
