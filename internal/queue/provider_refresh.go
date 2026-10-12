package queue

import (
	"fmt"
	"sort"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

// ProviderRefreshTarget identifies one facts/unit input affected by a newly
// accepted provider snapshot without implying that assessment work is persisted.
type ProviderRefreshTarget struct {
	ModuleID              string                    `json:"module_id"`
	FactsID               string                    `json:"facts_id"`
	UnitPath              string                    `json:"unit_path"`
	ProviderAddress       string                    `json:"provider_address"`
	TypeUsages            []index.ProviderTypeUsage `json:"type_usages"`
	RefreshTypeInventory  bool                      `json:"refresh_type_inventory"`
	RefreshCurrency       bool                      `json:"refresh_currency"`
	ProviderLevelFallback bool                      `json:"provider_level_fallback"`
}

// ProviderRefreshPlan is an in-memory invalidation result derived only from
// accepted dependency records. Callers still persist and fence work separately.
type ProviderRefreshPlan struct {
	ProviderAddress string                  `json:"provider_address"`
	Targets         []ProviderRefreshTarget `json:"targets"`
}

type providerRefreshKey struct {
	moduleID string
	factsID  string
	unitPath string
}

// PlanProviderRefresh selects affected units from a generated dependency index.
// Incomplete provider extraction conservatively falls back to provider-level refresh.
func PlanProviderRefresh(dependencies index.DimensionDependencyIndex, providerAddress string) (ProviderRefreshPlan, error) {
	if _, err := identity.ProviderIndexPath(providerAddress); err != nil {
		return ProviderRefreshPlan{}, fmt.Errorf("invalid provider refresh address: %w", err)
	}
	byKey := make(map[providerRefreshKey]*ProviderRefreshTarget)
	if err := addProviderCoverageTargets(byKey, dependencies.ProviderUsageCoverage, providerAddress); err != nil {
		return ProviderRefreshPlan{}, err
	}
	if err := addProviderUsageTargets(byKey, dependencies.ProviderTypeUsages, dependencies.ProviderUsageCoverage, providerAddress); err != nil {
		return ProviderRefreshPlan{}, err
	}
	targets := make([]ProviderRefreshTarget, 0, len(byKey))
	for _, target := range byKey {
		target.TypeUsages = uniqueProviderTypeUsages(target.TypeUsages)
		targets = append(targets, *target)
	}
	sort.Slice(targets, func(left, right int) bool {
		if targets[left].ModuleID != targets[right].ModuleID {
			return targets[left].ModuleID < targets[right].ModuleID
		}
		if targets[left].UnitPath != targets[right].UnitPath {
			return targets[left].UnitPath < targets[right].UnitPath
		}
		return targets[left].FactsID < targets[right].FactsID
	})
	return ProviderRefreshPlan{ProviderAddress: providerAddress, Targets: targets}, nil
}

func addProviderCoverageTargets(byKey map[providerRefreshKey]*ProviderRefreshTarget, coverageRecords []index.ProviderUsageCoverage, address string) error {
	for _, coverage := range coverageRecords {
		if err := validateProviderCoverageRecord(coverage); err != nil {
			return err
		}
		fallback := providerUsageNeedsFallback(coverage.Capabilities)
		if !fallback && !containsCanonicalProviderAddress(coverage.ProviderAddresses, address) {
			continue
		}
		key := providerRefreshKey{moduleID: coverage.ModuleID, factsID: coverage.FactsID, unitPath: coverage.UnitPath}
		target := providerRefreshTarget(byKey, key, address)
		target.RefreshCurrency = true
		target.ProviderLevelFallback = fallback
		target.RefreshTypeInventory = fallback
	}
	return nil
}

func addProviderUsageTargets(byKey map[providerRefreshKey]*ProviderRefreshTarget, usages []index.ProviderTypeUsage, coverageRecords []index.ProviderUsageCoverage, address string) error {
	for _, usage := range usages {
		if err := validateProviderUsageRecord(usage); err != nil {
			return err
		}
		coverage, hasCoverage := findProviderUsageCoverage(coverageRecords, usage)
		fallback := !hasCoverage || providerUsageNeedsFallback(coverage.Capabilities)
		if terraform.CanonicalProviderAddress(usage.ProviderAddress) != terraform.CanonicalProviderAddress(address) && !(usage.ProviderAddress == "" && fallback) {
			continue
		}
		key := providerRefreshKey{moduleID: usage.ModuleID, factsID: usage.FactsID, unitPath: usage.UnitPath}
		target := providerRefreshTarget(byKey, key, address)
		target.TypeUsages = append(target.TypeUsages, usage)
		target.RefreshTypeInventory = true
		target.RefreshCurrency = true
		target.ProviderLevelFallback = target.ProviderLevelFallback || fallback
	}
	return nil
}

func providerRefreshTarget(byKey map[providerRefreshKey]*ProviderRefreshTarget, key providerRefreshKey, address string) *ProviderRefreshTarget {
	if target, exists := byKey[key]; exists {
		return target
	}
	target := &ProviderRefreshTarget{
		ModuleID: key.moduleID, FactsID: key.factsID, UnitPath: key.unitPath,
		ProviderAddress: address, TypeUsages: []index.ProviderTypeUsage{},
	}
	byKey[key] = target
	return target
}

func validateProviderCoverageRecord(record index.ProviderUsageCoverage) error {
	if record.ModuleID == "" || identity.ValidateRecordID("facts", record.FactsID) != nil || identity.ValidateRelativePath(record.UnitPath, true) != nil {
		return fmt.Errorf("provider usage coverage record identity is invalid")
	}
	if _, err := identity.SortedSet(record.ProviderAddresses); err != nil {
		return fmt.Errorf("provider usage coverage addresses: %w", err)
	}
	for _, address := range record.ProviderAddresses {
		if _, err := identity.ProviderIndexPath(address); err != nil {
			return fmt.Errorf("provider usage coverage address is invalid: %w", err)
		}
	}
	for capability, status := range record.Capabilities {
		if capability == "" || status != terraform.CoverageComplete && status != terraform.CoveragePartial && status != terraform.CoverageUnavailable && status != terraform.CoverageUnknown {
			return fmt.Errorf("provider usage coverage state %q=%q is invalid", capability, status)
		}
	}
	return nil
}

func validateProviderUsageRecord(record index.ProviderTypeUsage) error {
	if record.ModuleID == "" || identity.ValidateRecordID("facts", record.FactsID) != nil || identity.ValidateRelativePath(record.UnitPath, true) != nil || record.Kind == "" || record.Name == "" {
		return fmt.Errorf("provider type usage record identity is invalid")
	}
	if record.ProviderAddress != "" {
		if _, err := identity.ProviderIndexPath(record.ProviderAddress); err != nil {
			return fmt.Errorf("provider type usage address is invalid: %w", err)
		}
	}
	return nil
}

func providerUsageNeedsFallback(capabilities map[string]string) bool {
	for _, capability := range []string{
		terraform.CapabilityProviderBindings, terraform.CapabilityResourceUsage,
		terraform.CapabilityDataUsage, terraform.CapabilityAdditionalKinds,
	} {
		if capabilities[capability] != terraform.CoverageComplete {
			return true
		}
	}
	return false
}

func findProviderUsageCoverage(records []index.ProviderUsageCoverage, usage index.ProviderTypeUsage) (index.ProviderUsageCoverage, bool) {
	for _, record := range records {
		if record.ModuleID == usage.ModuleID && record.FactsID == usage.FactsID && record.UnitPath == usage.UnitPath {
			return record, true
		}
	}
	return index.ProviderUsageCoverage{}, false
}

func uniqueProviderTypeUsages(usages []index.ProviderTypeUsage) []index.ProviderTypeUsage {
	seen := make(map[index.ProviderTypeUsage]struct{}, len(usages))
	unique := make([]index.ProviderTypeUsage, 0, len(usages))
	for _, usage := range usages {
		if _, exists := seen[usage]; exists {
			continue
		}
		seen[usage] = struct{}{}
		unique = append(unique, usage)
	}
	sort.Slice(unique, func(left, right int) bool {
		if unique[left].Kind != unique[right].Kind {
			return unique[left].Kind < unique[right].Kind
		}
		return unique[left].Name < unique[right].Name
	})
	return unique
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsCanonicalProviderAddress(values []string, target string) bool {
	target = terraform.CanonicalProviderAddress(target)
	for _, value := range values {
		if terraform.CanonicalProviderAddress(value) == target {
			return true
		}
	}
	return false
}
