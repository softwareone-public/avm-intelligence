package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReviewedProviderCollectionPolicy(t *testing.T) {
	policy, err := LoadCollectionPolicy("../../config/providers.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Providers) != 2 {
		t.Fatalf("provider allowlist length = %d, want 2", len(policy.Providers))
	}
	azureRM, found := findProviderPolicy(policy, "registry.terraform.io/hashicorp/azurerm")
	if !found || len(azureRM.TrackMajors) != 2 || azureRM.TrackMajors[0] != 4 || azureRM.TrackMajors[1] != 5 {
		t.Fatalf("AzureRM policy = %#v, want approved 4.x/5.x tracks", azureRM)
	}
	if azureRM.SourceRepository != "hashicorp/terraform-provider-azurerm" || azureRM.SourceLicense != "MPL-2.0" {
		t.Fatalf("AzureRM source policy = %#v, want verified upstream source/license", azureRM)
	}
}

func TestCollectionPolicyRejectsDuplicateProvidersAndInvalidLimits(t *testing.T) {
	policy, err := LoadCollectionPolicy("../../config/providers.yaml")
	if err != nil {
		t.Fatal(err)
	}
	policy.Providers = append(policy.Providers, policy.Providers[0])
	if err = policy.Validate(); err == nil {
		t.Fatal("duplicate provider address was accepted")
	}
	policy.Providers = policy.Providers[:2]
	policy.Providers[0].Limits.MaxTotalBytes = policy.Providers[0].Limits.MaxResponseBytes - 1
	if err = policy.Validate(); err == nil {
		t.Fatal("total-byte limit smaller than response limit was accepted")
	}
}

func TestLoadCollectionPolicyRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.yaml")
	data := `schema_version: 1
providers: []
unexpected: true
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCollectionPolicy(path); err == nil {
		t.Fatal("unknown policy field was accepted")
	}
}

func findProviderPolicy(policy CollectionPolicy, address string) (ProviderPolicy, bool) {
	for _, provider := range policy.Providers {
		if provider.Address == address {
			return provider, true
		}
	}
	return ProviderPolicy{}, false
}
