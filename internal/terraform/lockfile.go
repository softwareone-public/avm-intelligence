package terraform

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/zclconf/go-cty/cty"
	"golang.org/x/mod/semver"
)

func extractProviderLockSelections(root string, lockFiles []LockFileInput, units []Unit, coverage map[string]string, reasons *[]string) error {
	if len(lockFiles) == 0 {
		*reasons = append(*reasons, "provider lock selections are unavailable because no lockfiles were supplied")
		return nil
	}
	unitByPath := make(map[string]*Unit, len(units))
	for position := range units {
		unitByPath[units[position].Path] = &units[position]
	}
	seenPaths := make(map[string]struct{}, len(lockFiles))
	complete := true
	for _, lockFile := range lockFiles {
		if err := validateLockFileInput(lockFile, unitByPath); err != nil {
			return err
		}
		if _, exists := seenPaths[lockFile.Path]; exists {
			return fmt.Errorf("duplicate provider lockfile input %q", lockFile.Path)
		}
		seenPaths[lockFile.Path] = struct{}{}
		absolute := filepath.Join(root, filepath.FromSlash(lockFile.Path))
		if err := rejectSymlinkComponents(root, absolute); err != nil {
			return fmt.Errorf("unsafe provider lockfile %q: %w", lockFile.Path, err)
		}
		data, err := os.ReadFile(absolute)
		if err != nil {
			return fmt.Errorf("read provider lockfile %q: %w", lockFile.Path, err)
		}
		selections, err := parseProviderLockFile(data, lockFile.Path)
		if err != nil {
			return err
		}
		unit := unitByPath[lockFile.UnitPath]
		if !unitProviderAddressesSelected(unit, selections) {
			complete = false
			*reasons = append(*reasons, "provider lockfile is missing a statically required provider selection")
		}
		for _, selection := range selections {
			unit.Symbols = append(unit.Symbols, FactSymbol{
				Kind: "provider_lock", Name: providerLockSymbolName(selection.address),
				ProviderAddress: selection.address, ProviderVersionSelection: selection.version,
				UnitPath: lockFile.UnitPath, Location: Location{File: lockFile.Path, Line: selection.line},
				Capability: CapabilityProviderLockSelections,
			})
		}
		sort.Slice(unit.Symbols, func(left, right int) bool { return symbolLess(unit.Symbols[left], unit.Symbols[right]) })
	}
	if complete {
		coverage[CapabilityProviderLockSelections] = CoverageComplete
	} else {
		coverage[CapabilityProviderLockSelections] = CoveragePartial
	}
	return nil
}

func unitProviderAddressesSelected(unit *Unit, selections []providerLockSelection) bool {
	selected := make(map[string]struct{}, len(selections))
	for _, selection := range selections {
		selected[selection.address] = struct{}{}
	}
	required := make(map[string]struct{})
	for _, symbol := range unit.Symbols {
		if symbol.ProviderAddress != "" {
			required[symbol.ProviderAddress] = struct{}{}
		}
		for _, mapping := range symbol.ProviderMappings {
			required[mapping.ProviderAddress] = struct{}{}
		}
	}
	for address := range required {
		if _, exists := selected[address]; !exists {
			return false
		}
	}
	return true
}

type providerLockSelection struct {
	address string
	version string
	line    int
}

func validateLockFileInput(lockFile LockFileInput, units map[string]*Unit) error {
	unit, exists := units[lockFile.UnitPath]
	if !exists {
		return fmt.Errorf("provider lockfile %q has no matching unit", lockFile.Path)
	}
	expectedPath := filepath.ToSlash(filepath.Join(filepath.FromSlash(unit.Path), ".terraform.lock.hcl"))
	if lockFile.Path != expectedPath {
		return fmt.Errorf("provider lockfile %q is not the lockfile for unit %q", lockFile.Path, lockFile.UnitPath)
	}
	if err := identity.ValidateRelativePath(lockFile.Path, false); err != nil {
		return fmt.Errorf("unsafe provider lockfile path %q: %w", lockFile.Path, err)
	}
	return nil
}

func parseProviderLockFile(data []byte, filePath string) ([]providerLockSelection, error) {
	parser := hclparse.NewParser()
	file, diagnostics := parser.ParseHCL(data, filePath)
	if diagnostics.HasErrors() {
		return nil, fmt.Errorf("parse provider lockfile %q: %s", filePath, diagnostics.Error())
	}
	content, diagnostics := file.Body.Content(&hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "provider", LabelNames: []string{"address"}}}})
	if diagnostics.HasErrors() {
		return nil, fmt.Errorf("decode provider lockfile %q: %s", filePath, diagnostics.Error())
	}
	if body, ok := file.Body.(*hclsyntax.Body); ok && len(body.Blocks) != len(content.Blocks) {
		return nil, fmt.Errorf("provider lockfile %q contains unsupported top-level blocks", filePath)
	}
	selections := make([]providerLockSelection, 0, len(content.Blocks))
	seenAddresses := make(map[string]struct{}, len(content.Blocks))
	for _, block := range content.Blocks {
		if len(block.Labels) != 1 || !addressPattern.MatchString(block.Labels[0]) {
			return nil, fmt.Errorf("provider lockfile %q contains an invalid provider address", filePath)
		}
		address := block.Labels[0]
		if _, duplicate := seenAddresses[address]; duplicate {
			return nil, fmt.Errorf("provider lockfile %q repeats provider %q", filePath, address)
		}
		seenAddresses[address] = struct{}{}
		attributes, diagnostics := block.Body.Content(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{
			{Name: "version", Required: true}, {Name: "constraints"}, {Name: "hashes"},
		}})
		if diagnostics.HasErrors() {
			return nil, fmt.Errorf("decode provider lock for %q: %s", address, diagnostics.Error())
		}
		version, ok := literalLockString(attributes.Attributes["version"])
		if !ok || !validExactProviderVersion(version) {
			return nil, fmt.Errorf("provider lock for %q has no exact static version", address)
		}
		selections = append(selections, providerLockSelection{address: address, version: version, line: block.DefRange.Start.Line})
	}
	sort.Slice(selections, func(left, right int) bool { return selections[left].address < selections[right].address })
	return selections, nil
}

func literalLockString(attribute *hcl.Attribute) (string, bool) {
	if attribute == nil {
		return "", false
	}
	template, ok := attribute.Expr.(*hclsyntax.TemplateExpr)
	if !ok || !template.IsStringLiteral() {
		return "", false
	}
	value, diagnostics := template.Value(nil)
	if diagnostics.HasErrors() || !value.IsKnown() || value.IsNull() || !value.Type().Equals(cty.String) {
		return "", false
	}
	return value.AsString(), true
}

func validExactProviderVersion(version string) bool {
	if strings.HasPrefix(version, "v") || !semver.IsValid("v"+version) {
		return false
	}
	return semver.Canonical("v"+version) == "v"+version
}

func providerLockSymbolName(address string) string {
	return strings.ReplaceAll(address, "/", "_")
}

func validateProviderLockSelection(symbol FactSymbol) error {
	if symbol.Kind != "provider_lock" {
		if symbol.ProviderVersionSelection != "" {
			return fmt.Errorf("provider version selections are only valid on provider_lock symbols")
		}
		return nil
	}
	if symbol.Capability != CapabilityProviderLockSelections || !addressPattern.MatchString(symbol.ProviderAddress) ||
		!validExactProviderVersion(symbol.ProviderVersionSelection) || symbol.ProviderVersionConstraint != "" ||
		symbol.Name != providerLockSymbolName(symbol.ProviderAddress) || symbol.LocalProviderName != "" || symbol.Alias != "" {
		return fmt.Errorf("provider lock selection identity is invalid")
	}
	return nil
}
