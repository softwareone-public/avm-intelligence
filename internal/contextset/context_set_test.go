package contextset

import (
	"strings"
	"testing"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

func TestProviderMajorZeroIsAValidExactTrack(t *testing.T) {
	policyID, err := identity.ID("provider-policy", 1, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	set, err := New("avm-res-example", strings.Repeat("a", 40), "v1.0.0",
		"facts-"+strings.Repeat("b", 64), policyID, true, []Context{{
			State: ContextState{
				ContextID: "registry.terraform.io/hashicorp/time/0", Track: "registry.terraform.io/hashicorp/time/0",
				Applicability: ApplicabilityApplicable, Candidate: CandidateEligible,
			},
			ProviderAddress: "registry.terraform.io/hashicorp/time",
		}})
	if err != nil {
		t.Fatalf("major-zero provider context rejected: %v", err)
	}
	if err = set.Validate(); err != nil {
		t.Fatalf("validate major-zero provider context: %v", err)
	}
}
