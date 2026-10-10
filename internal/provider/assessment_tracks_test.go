package provider

import (
	"fmt"
	"testing"

	"github.com/softwareone-public/avm-intelligence/internal/contextset"
)

func TestDeriveRequiredAssessmentContextsUsesIndependentProviderMajorTracks(t *testing.T) {
	facts, _, policy := resourceIntelligenceFixture(t)
	policy.Providers[0].TrackMajors = []uint64{4, 5}
	set, err := DeriveRequiredAssessmentContexts(facts, "v1.2.3", policy)
	if err != nil {
		t.Fatal(err)
	}
	policyID, err := AssessmentTrackPolicyID(policy)
	if err != nil || set.PolicyID != policyID {
		t.Fatalf("assessment policy identity = %q, error = %v; set identity is %q", policyID, err, set.PolicyID)
	}
	if !set.Complete || len(set.Contexts) != 2 {
		t.Fatalf("required contexts = %+v, want complete independent major lanes", set)
	}
	for index, major := range []uint64{4, 5} {
		context := set.Contexts[index]
		if context.ProviderMajor != major || context.State.Applicability != contextset.ApplicabilityApplicable || context.State.Candidate != contextset.CandidateEligible {
			t.Fatalf("context %d = %+v, want provider major %d eligible", index, context, major)
		}
		if context.ProviderAddress != "registry.terraform.io/hashicorp/azurerm" || context.State.ContextID != fmt.Sprintf("%s/%d", context.ProviderAddress, major) {
			t.Fatalf("context ID = %q, want provider address and major %d", context.State.ContextID, major)
		}
	}
	replay, err := DeriveRequiredAssessmentContexts(facts, "v1.2.3", policy)
	if err != nil || replay.ID != set.ID {
		t.Fatalf("repeat derivation ID=%s, error=%v; want stable ID %s", replay.ID, err, set.ID)
	}
}

func TestDeriveRequiredAssessmentContextsRetainsUnconfiguredTrackAsUnresolved(t *testing.T) {
	facts, _, policy := resourceIntelligenceFixture(t)
	policy.Providers[0].TrackMajors = nil
	set, err := DeriveRequiredAssessmentContexts(facts, "v1.2.3", policy)
	if err != nil {
		t.Fatal(err)
	}
	if set.Complete || len(set.Contexts) != 1 || set.Contexts[0].State.Applicability != contextset.ApplicabilityUnresolved || set.Contexts[0].State.Candidate != contextset.CandidateUnresolved || set.Contexts[0].Reason == "" {
		t.Fatalf("unconfigured policy context = %+v, want retained unresolved entry", set)
	}
	if err = set.Validate(); err != nil {
		t.Fatalf("validate unresolved context set: %v", err)
	}
	set.Contexts[0].Reason = ""
	if err = set.Validate(); err == nil {
		t.Fatal("unresolved context without a reason was accepted")
	}
}
