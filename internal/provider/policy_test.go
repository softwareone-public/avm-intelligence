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
	if len(policy.Providers) != 10 {
		t.Fatalf("provider allowlist length = %d, want 10 cohort providers", len(policy.Providers))
	}
	azureRM, found := findProviderPolicy(policy, "registry.terraform.io/hashicorp/azurerm")
	if !found || len(azureRM.TrackMajors) != 3 || azureRM.TrackMajors[0] != 3 || azureRM.TrackMajors[1] != 4 || azureRM.TrackMajors[2] != 5 {
		t.Fatalf("AzureRM policy = %#v, want used 3.x/4.x plus latest 5.x tracks", azureRM)
	}
	if azureRM.SourceRepository != "hashicorp/terraform-provider-azurerm" || azureRM.SourceLicense != "MPL-2.0" {
		t.Fatalf("AzureRM source policy = %#v, want verified upstream source/license", azureRM)
	}
	if azureRM.Limits.MaxPages != 100 || azureRM.Limits.MaxResponseBytes != 1<<20 || azureRM.Limits.MaxTotalBytes != 50<<20 || azureRM.Limits.MaxWallTimeSeconds != 1800 {
		t.Fatalf("AzureRM collection bounds = %#v, want 100 issue pages within the existing 1 MiB response, 50 MiB total, and 30 minute hard caps", azureRM.Limits)
	}
	azureAD, found := findProviderPolicy(policy, "registry.terraform.io/hashicorp/azuread")
	if !found || len(azureAD.TrackMajors) != 2 || azureAD.TrackMajors[0] != 2 || azureAD.TrackMajors[1] != 3 {
		t.Fatalf("AzureAD policy = %#v, want the used 2.x and 3.x tracks", azureAD)
	}
	if azureAD.Limits.MaxPages != 100 || azureAD.Limits.MaxResponseBytes != 1<<20 || azureAD.Limits.MaxTotalBytes != 10<<20 || azureAD.Limits.MaxWallTimeSeconds != 900 {
		t.Fatalf("AzureAD collection bounds = %#v, want 100 issue pages within the existing 1 MiB response, 10 MiB total, and 15 minute per-provider bounds", azureAD.Limits)
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
	policy.Providers = policy.Providers[:len(policy.Providers)-1]
	policy.Providers[0].Limits.MaxTotalBytes = policy.Providers[0].Limits.MaxResponseBytes - 1
	if err = policy.Validate(); err == nil {
		t.Fatal("total-byte limit smaller than response limit was accepted")
	}
}

func TestCollectionPolicySupportsMajorZeroAndCanonicalProviderLookup(t *testing.T) {
	policy, err := LoadCollectionPolicy("../../config/providers.yaml")
	if err != nil {
		t.Fatal(err)
	}
	modtm, found := providerPolicyForAddress(policy, "registry.terraform.io/Azure/modtm")
	if !found || len(modtm.TrackMajors) != 1 || modtm.TrackMajors[0] != 0 {
		t.Fatalf("modtm policy = %#v, found %v; want canonical lookup and major zero", modtm, found)
	}
	if err = validateTrackMajors([]uint64{0, 2}); err != nil {
		t.Fatalf("ascending zero-based tracks rejected: %v", err)
	}
	if err = validateTrackMajors([]uint64{2, 0}); err == nil {
		t.Fatal("descending tracks were accepted")
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
