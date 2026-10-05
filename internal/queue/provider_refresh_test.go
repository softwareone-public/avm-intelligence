package queue

import (
	"strings"
	"testing"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

func TestPlanProviderRefreshUsesExactTypesAndCoverageFallback(t *testing.T) {
	const targetAddress = "registry.terraform.io/hashicorp/azurerm"
	const otherAddress = "registry.terraform.io/hashicorp/azapi"
	exactFacts := "facts-" + strings.Repeat("a", 64)
	partialFacts := "facts-" + strings.Repeat("b", 64)
	unknownFacts := "facts-" + strings.Repeat("c", 64)
	otherFacts := "facts-" + strings.Repeat("d", 64)
	completeCoverage := map[string]string{
		terraform.CapabilityProviderBindings: terraform.CoverageComplete,
		terraform.CapabilityResourceUsage:    terraform.CoverageComplete,
		terraform.CapabilityDataUsage:        terraform.CoverageComplete,
		terraform.CapabilityAdditionalKinds:  terraform.CoverageComplete,
	}
	partialCoverage := cloneCoverage(completeCoverage)
	partialCoverage[terraform.CapabilityProviderBindings] = terraform.CoveragePartial
	dependencies := index.DimensionDependencyIndex{
		ProviderTypeUsages: []index.ProviderTypeUsage{
			{ModuleID: "avm-res-exact", FactsID: exactFacts, UnitPath: ".", ProviderAddress: targetAddress, Kind: "ephemeral", Name: "azurerm_secret"},
			{ModuleID: "avm-res-partial", FactsID: partialFacts, UnitPath: ".", ProviderAddress: targetAddress, Kind: "resource", Name: "azurerm_group"},
			{ModuleID: "avm-res-other", FactsID: otherFacts, UnitPath: ".", ProviderAddress: otherAddress, Kind: "action", Name: "azapi_run"},
		},
		ProviderUsageCoverage: []index.ProviderUsageCoverage{
			{ModuleID: "avm-res-exact", FactsID: exactFacts, UnitPath: ".", ProviderAddresses: []string{targetAddress}, Capabilities: completeCoverage},
			{ModuleID: "avm-res-partial", FactsID: partialFacts, UnitPath: ".", ProviderAddresses: []string{targetAddress}, Capabilities: partialCoverage},
			{ModuleID: "avm-res-unknown", FactsID: unknownFacts, UnitPath: ".", ProviderAddresses: []string{}, Capabilities: partialCoverage},
			{ModuleID: "avm-res-other", FactsID: otherFacts, UnitPath: ".", ProviderAddresses: []string{otherAddress}, Capabilities: completeCoverage},
		},
	}
	plan, err := PlanProviderRefresh(dependencies, targetAddress)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Targets) != 3 {
		t.Fatalf("refresh targets = %#v, want exact, partial, and provider-level fallback units", plan.Targets)
	}
	if plan.Targets[0].ModuleID != "avm-res-exact" || plan.Targets[0].ProviderLevelFallback || !plan.Targets[0].RefreshTypeInventory || !plan.Targets[0].RefreshCurrency || len(plan.Targets[0].TypeUsages) != 1 {
		t.Fatalf("exact dependency target = %#v", plan.Targets[0])
	}
	if plan.Targets[1].ModuleID != "avm-res-partial" || !plan.Targets[1].ProviderLevelFallback {
		t.Fatalf("partial-coverage target = %#v, want provider fallback", plan.Targets[1])
	}
	if plan.Targets[2].ModuleID != "avm-res-unknown" || !plan.Targets[2].ProviderLevelFallback || len(plan.Targets[2].TypeUsages) != 0 {
		t.Fatalf("unknown-coverage target = %#v, want conservative provider fallback", plan.Targets[2])
	}
	if _, err = PlanProviderRefresh(dependencies, "invalid-address"); err == nil {
		t.Fatal("invalid provider address was accepted")
	}
}

func cloneCoverage(coverage map[string]string) map[string]string {
	clone := make(map[string]string, len(coverage))
	for capability, status := range coverage {
		clone[capability] = status
	}
	return clone
}

func TestPlanProviderRefreshRejectsMalformedCoverageRecords(t *testing.T) {
	_, err := PlanProviderRefresh(index.DimensionDependencyIndex{
		ProviderUsageCoverage: []index.ProviderUsageCoverage{{
			ModuleID: "avm-res-demo", FactsID: "facts-invalid", UnitPath: ".",
		}},
	}, "registry.terraform.io/hashicorp/azurerm")
	if err == nil {
		t.Fatal("malformed provider usage coverage record was accepted")
	}
	if err = identity.ValidateRecordID("facts", "facts-invalid"); err == nil {
		t.Fatal("test facts ID unexpectedly passed validation")
	}
}
