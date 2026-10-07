// Package assessment runs compiled, offline checks against accepted facts and
// optional immutable snapshots.
package assessment

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	version "github.com/hashicorp/go-version"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

const (
	StatusPass          = "pass"
	StatusFail          = "fail"
	StatusUnchecked     = "unchecked"
	StatusNotApplicable = "not_applicable"

	ExecutionCompleted    = "completed"
	ExecutionDisabled     = "disabled"
	ExecutionUnselected   = "unselected"
	ExecutionMissingInput = "missing_input"
	ExecutionError        = "error"

	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"

	FreshnessUnknown = "unknown"

	applicabilityAlways              = "always"
	applicabilityProviderUsage       = "provider-usage"
	applicabilityProviderRequirement = "provider-requirement"

	DimensionProviderRequirements   = "provider-requirements"
	DimensionStaticFactsCoverage    = "static-facts-coverage"
	DimensionProviderCatalog        = "provider-type-catalog"
	DimensionProviderCurrency       = "provider-currency"
	DimensionInterfaceDocumentation = "interface-documentation"
	DimensionModuleOwnership        = "module-ownership"
	DimensionDeploymentCoreVersion  = "deployment-core-version"
	classificationModule            = "module"
	classificationDeployment        = "deployment"
	classificationUnknown           = "unknown"

	CheckDeclaredProviderSources   = "declared-provider-sources"
	CheckStaticFactsInventory      = "static-facts-inventory"
	CheckProviderTypeInventory     = "provider-type-inventory"
	CheckProviderVersionConstraint = "provider-version-constraint"
	CheckInputDescriptions         = "input-descriptions"
	CheckOutputDescriptions        = "output-descriptions"
	CheckModuleStateCallerOwned    = "module-state-is-caller-owned"
	CheckModuleCloudCallerOwned    = "module-cloud-is-caller-owned"
	CheckDeploymentCoreVersion     = "deployment-core-version"

	evidenceSchemaVersion = 1
	dimensionVersion      = 1
	checkVersion          = 1
)

var providerAddressPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)
var sha1Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type DimensionDefinition struct {
	SchemaVersion         int      `json:"schema_version"`
	RecordType            string   `json:"record_type"`
	DefinitionID          string   `json:"definition_id"`
	DimensionID           string   `json:"dimension_id"`
	Version               int      `json:"version"`
	UnitScope             string   `json:"unit_scope"`
	CheckIDs              []string `json:"check_ids"`
	RequiredCapabilities  []string `json:"required_capabilities"`
	PolicyID              string   `json:"policy_id"`
	EvidenceSchemaVersion int      `json:"evidence_schema_version"`
}

type CheckDefinition struct {
	SchemaVersion         int      `json:"schema_version"`
	RecordType            string   `json:"record_type"`
	CheckID               string   `json:"check_id"`
	Version               int      `json:"version"`
	DimensionID           string   `json:"dimension_id"`
	Applicability         string   `json:"applicability"`
	RequiredCapabilities  []string `json:"required_capabilities"`
	EvidenceSchemaVersion int      `json:"evidence_schema_version"`
	ImplementationID      string   `json:"implementation_id"`
	Severity              string   `json:"severity"`
}

type Registry struct {
	dimensions map[string]DimensionDefinition
	checks     map[string]CheckDefinition
	policyID   string
}

type RunInput struct {
	Facts           terraform.Facts
	Snapshots       []ProviderSnapshot
	TargetVersions  map[string]string
	DimensionIDs    []string
	Classifications map[string]string
}

type ProviderSnapshot struct {
	SchemaVersion   int              `json:"schema_version"`
	RecordType      string           `json:"record_type"`
	SnapshotID      string           `json:"snapshot_id"`
	ProviderAddress string           `json:"provider_address"`
	ProviderVersion string           `json:"provider_version"`
	Coverage        SnapshotCoverage `json:"coverage"`
	Entries         []ProviderEntry  `json:"entries"`
	Producer        SnapshotProducer `json:"producer"`
	SourceDigest    string           `json:"source_digest"`
	ObservedAt      string           `json:"observed_at"`
}

type SnapshotCoverage struct {
	Capabilities map[string]string `json:"capabilities"`
	Complete     bool              `json:"complete"`
	ObservedAt   string            `json:"observed_at"`
	Reason       string            `json:"reason,omitempty"`
}

type SnapshotProducer struct {
	ID              string `json:"id"`
	Version         string `json:"version"`
	Commit          string `json:"commit,omitempty"`
	BinaryDigest    string `json:"binary_digest,omitempty"`
	ContractVersion int    `json:"contract_version"`
}

type ProviderEntry struct {
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	DocumentationRef string `json:"documentation_ref,omitempty"`
	ContentDigest    string `json:"content_digest,omitempty"`
}

type UnitClassification struct {
	Role       string `json:"role,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Method     string `json:"method,omitempty"`
	Classified bool   `json:"classified"`
}

type CheckResult struct {
	ID         string   `json:"id"`
	Version    int      `json:"version"`
	Status     string   `json:"status"`
	Execution  string   `json:"execution"`
	Severity   string   `json:"severity,omitempty"`
	Evidence   []string `json:"evidence"`
	Message    string   `json:"message,omitempty"`
	ErrorClass string   `json:"error_class,omitempty"`
}

type ResultCoverage struct {
	Assessed    int `json:"assessed"`
	Applicable  int `json:"applicable"`
	Unavailable int `json:"unavailable"`
}

type DimensionResult struct {
	SchemaVersion     int            `json:"schema_version"`
	RecordType        string         `json:"record_type"`
	DimensionResultID string         `json:"dimension_result_id"`
	ModuleID          string         `json:"module_id"`
	SourceSHA         string         `json:"source_sha"`
	UnitPath          string         `json:"unit_path"`
	FactsID           string         `json:"facts_id"`
	DefinitionID      string         `json:"definition_id"`
	SnapshotIDs       []string       `json:"snapshot_ids,omitempty"`
	InputFingerprint  string         `json:"input_fingerprint"`
	Checks            []CheckResult  `json:"checks"`
	Coverage          ResultCoverage `json:"coverage"`
	ExecutionComplete bool           `json:"execution_complete"`
	Freshness         string         `json:"freshness"`
	ContentDigest     string         `json:"content_digest,omitempty"`
}

type evaluator func(CheckDefinition, evaluationInput) CheckResult

type evaluationInput struct {
	facts          terraform.Facts
	unit           terraform.Unit
	classification string
	snapshots      map[string]ProviderSnapshot
	targetVersions map[string]string
}

type dimensionRunContext struct {
	registry               Registry
	facts                  terraform.Facts
	classificationPolicyID string
	snapshots              map[string]ProviderSnapshot
	snapshotIDs            []string
	targetVersions         map[string]string
}

type dimensionDefinitionIdentity struct {
	SchemaVersion         int      `json:"schema_version"`
	RecordType            string   `json:"record_type"`
	DimensionID           string   `json:"dimension_id"`
	Version               int      `json:"version"`
	UnitScope             string   `json:"unit_scope"`
	CheckIDs              []string `json:"check_ids"`
	RequiredCapabilities  []string `json:"required_capabilities"`
	PolicyID              string   `json:"policy_id"`
	EvidenceSchemaVersion int      `json:"evidence_schema_version"`
}

type inputVector struct {
	FactsID                string            `json:"facts_id"`
	DefinitionID           string            `json:"definition_id"`
	UnitPath               string            `json:"unit_path"`
	Classification         string            `json:"classification"`
	ClassificationPolicyID string            `json:"classification_policy_id"`
	SnapshotIDs            []string          `json:"snapshot_ids"`
	TargetVersions         map[string]string `json:"target_versions,omitempty"`
}

type dimensionResultContent struct {
	SchemaVersion     int            `json:"schema_version"`
	RecordType        string         `json:"record_type"`
	ModuleID          string         `json:"module_id"`
	SourceSHA         string         `json:"source_sha"`
	UnitPath          string         `json:"unit_path"`
	FactsID           string         `json:"facts_id"`
	DefinitionID      string         `json:"definition_id"`
	SnapshotIDs       []string       `json:"snapshot_ids"`
	InputFingerprint  string         `json:"input_fingerprint"`
	Checks            []CheckResult  `json:"checks"`
	Coverage          ResultCoverage `json:"coverage"`
	ExecutionComplete bool           `json:"execution_complete"`
	Freshness         string         `json:"freshness"`
}

// DefaultRegistry returns the built-in offline assessments. Implementations
// are compiled into the binary; the registry never loads scripts or plugins.
func DefaultRegistry() (Registry, error) {
	policyID, err := identity.ID("definition", 2, struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
	}{Name: "avm-intelligence-offline-assessment", Version: 1})
	if err != nil {
		return Registry{}, fmt.Errorf("identify built-in assessment policy: %w", err)
	}
	registry := Registry{
		dimensions: make(map[string]DimensionDefinition),
		checks:     make(map[string]CheckDefinition),
		policyID:   policyID,
	}
	definitions := []DimensionDefinition{
		{DimensionID: DimensionProviderRequirements, UnitScope: "repository", CheckIDs: []string{CheckDeclaredProviderSources}, RequiredCapabilities: []string{terraform.CapabilityProviderRequirements}},
		{DimensionID: DimensionStaticFactsCoverage, UnitScope: "component", CheckIDs: []string{CheckStaticFactsInventory}, RequiredCapabilities: []string{terraform.CapabilityAdditionalKinds, terraform.CapabilityProviderLockSelections}},
		{DimensionID: DimensionProviderCatalog, UnitScope: "repository", CheckIDs: []string{CheckProviderTypeInventory}, RequiredCapabilities: []string{terraform.CapabilityAdditionalKinds, terraform.CapabilityDataUsage, terraform.CapabilityResourceUsage}},
		{DimensionID: DimensionProviderCurrency, UnitScope: "repository", CheckIDs: []string{CheckProviderVersionConstraint}, RequiredCapabilities: []string{terraform.CapabilityProviderRequirements}},
		{DimensionID: DimensionInterfaceDocumentation, UnitScope: "component", CheckIDs: []string{CheckInputDescriptions, CheckOutputDescriptions}, RequiredCapabilities: []string{terraform.CapabilityInterfaceMetadata}},
		{DimensionID: DimensionModuleOwnership, UnitScope: "component", CheckIDs: []string{CheckModuleStateCallerOwned, CheckModuleCloudCallerOwned}, RequiredCapabilities: []string{terraform.CapabilityTerraformSettings}},
		{DimensionID: DimensionDeploymentCoreVersion, UnitScope: "example", CheckIDs: []string{CheckDeploymentCoreVersion}, RequiredCapabilities: []string{terraform.CapabilityTerraformSettings}},
	}
	for _, definition := range definitions {
		definition.SchemaVersion = 2
		definition.RecordType = "dimension_definition"
		definition.Version = dimensionVersion
		definition.PolicyID = policyID
		definition.EvidenceSchemaVersion = evidenceSchemaVersion
		var definitionErr error
		definition.CheckIDs, definitionErr = identity.SortedSet(definition.CheckIDs)
		if definitionErr != nil {
			return Registry{}, definitionErr
		}
		definition.RequiredCapabilities, definitionErr = identity.SortedSet(definition.RequiredCapabilities)
		if definitionErr != nil {
			return Registry{}, definitionErr
		}
		payload := dimensionDefinitionIdentity{
			SchemaVersion: definition.SchemaVersion, RecordType: definition.RecordType,
			DimensionID: definition.DimensionID, Version: definition.Version,
			UnitScope: definition.UnitScope, CheckIDs: definition.CheckIDs,
			RequiredCapabilities: definition.RequiredCapabilities, PolicyID: definition.PolicyID,
			EvidenceSchemaVersion: definition.EvidenceSchemaVersion,
		}
		definition.DefinitionID, definitionErr = identity.ID("definition", 2, payload)
		if definitionErr != nil {
			return Registry{}, fmt.Errorf("identify dimension %s: %w", definition.DimensionID, definitionErr)
		}
		registry.dimensions[definition.DimensionID] = definition
	}
	checks := []CheckDefinition{
		{CheckID: CheckDeclaredProviderSources, DimensionID: DimensionProviderRequirements, Applicability: applicabilityAlways, RequiredCapabilities: []string{terraform.CapabilityProviderRequirements}, ImplementationID: "provider-requirements.v1", Severity: SeverityWarning},
		{CheckID: CheckStaticFactsInventory, DimensionID: DimensionStaticFactsCoverage, Applicability: applicabilityAlways, RequiredCapabilities: []string{terraform.CapabilityAdditionalKinds, terraform.CapabilityProviderLockSelections}, ImplementationID: "facts-capability.v1", Severity: SeverityWarning},
		{CheckID: CheckProviderTypeInventory, DimensionID: DimensionProviderCatalog, Applicability: applicabilityProviderUsage, RequiredCapabilities: []string{terraform.CapabilityAdditionalKinds, terraform.CapabilityDataUsage, terraform.CapabilityResourceUsage}, ImplementationID: "provider-inventory.v1", Severity: SeverityWarning},
		{CheckID: CheckProviderVersionConstraint, DimensionID: DimensionProviderCurrency, Applicability: applicabilityProviderRequirement, RequiredCapabilities: []string{terraform.CapabilityProviderRequirements}, ImplementationID: "provider-version-constraint.v1", Severity: SeverityWarning},
		{CheckID: CheckInputDescriptions, DimensionID: DimensionInterfaceDocumentation, Applicability: classificationModule, RequiredCapabilities: []string{terraform.CapabilityInterfaceMetadata}, ImplementationID: "input-descriptions.v1", Severity: SeverityWarning},
		{CheckID: CheckOutputDescriptions, DimensionID: DimensionInterfaceDocumentation, Applicability: classificationModule, RequiredCapabilities: []string{terraform.CapabilityInterfaceMetadata}, ImplementationID: "output-descriptions.v1", Severity: SeverityWarning},
		{CheckID: CheckModuleStateCallerOwned, DimensionID: DimensionModuleOwnership, Applicability: classificationModule, RequiredCapabilities: []string{terraform.CapabilityTerraformSettings}, ImplementationID: "module-backend.v1", Severity: SeverityError},
		{CheckID: CheckModuleCloudCallerOwned, DimensionID: DimensionModuleOwnership, Applicability: classificationModule, RequiredCapabilities: []string{terraform.CapabilityTerraformSettings}, ImplementationID: "module-cloud.v1", Severity: SeverityError},
		{CheckID: CheckDeploymentCoreVersion, DimensionID: DimensionDeploymentCoreVersion, Applicability: classificationDeployment, RequiredCapabilities: []string{terraform.CapabilityTerraformSettings}, ImplementationID: "deployment-core-version.v1", Severity: SeverityWarning},
	}
	for _, check := range checks {
		check.SchemaVersion = 2
		check.RecordType = "check_definition"
		check.Version = checkVersion
		check.EvidenceSchemaVersion = evidenceSchemaVersion
		check.RequiredCapabilities, err = identity.SortedSet(check.RequiredCapabilities)
		if err != nil {
			return Registry{}, err
		}
		registry.checks[check.CheckID] = check
	}
	if err := registry.Validate(); err != nil {
		return Registry{}, err
	}
	return registry, nil
}

// Validate rejects duplicate, dangling, or unimplemented definitions.
func (registry Registry) Validate() error {
	if len(registry.dimensions) == 0 || len(registry.checks) == 0 {
		return fmt.Errorf("assessment registry is empty")
	}
	if err := identity.ValidateRecordID("definition", registry.policyID); err != nil {
		return fmt.Errorf("invalid policy identity: %w", err)
	}
	for dimensionID, definition := range registry.dimensions {
		if err := registry.validateDimension(dimensionID, definition); err != nil {
			return err
		}
	}
	for checkID, check := range registry.checks {
		if err := registry.validateCheck(checkID, check); err != nil {
			return err
		}
	}
	return nil
}

func (registry Registry) validateDimension(dimensionID string, definition DimensionDefinition) error {
	if dimensionID == "" || definition.DimensionID != dimensionID || definition.Version < 1 {
		return fmt.Errorf("invalid dimension definition %q", dimensionID)
	}
	if err := identity.ValidateRecordID("definition", definition.DefinitionID); err != nil {
		return fmt.Errorf("invalid dimension definition identity: %w", err)
	}
	if definition.SchemaVersion != 2 || definition.RecordType != "dimension_definition" || definition.PolicyID != registry.policyID || definition.EvidenceSchemaVersion < 1 {
		return fmt.Errorf("dimension %s has invalid contract metadata", dimensionID)
	}
	if !validUnitScope(definition.UnitScope) || len(definition.CheckIDs) == 0 {
		return fmt.Errorf("dimension %s has invalid scope or no checks", dimensionID)
	}
	if _, err := identity.SortedSet(definition.CheckIDs); err != nil {
		return fmt.Errorf("dimension %s: %w", dimensionID, err)
	}
	if _, err := identity.SortedSet(definition.RequiredCapabilities); err != nil {
		return fmt.Errorf("dimension %s: %w", dimensionID, err)
	}
	return registry.validateDimensionChecks(dimensionID, definition.CheckIDs)
}

func (registry Registry) validateDimensionChecks(dimensionID string, checkIDs []string) error {
	for _, checkID := range checkIDs {
		check, exists := registry.checks[checkID]
		if !exists || check.DimensionID != dimensionID {
			return fmt.Errorf("dimension %s references invalid check %s", dimensionID, checkID)
		}
	}
	return nil
}

func (registry Registry) validateCheck(checkID string, check CheckDefinition) error {
	if checkID == "" || check.CheckID != checkID || check.Version < 1 || check.EvidenceSchemaVersion < 1 {
		return fmt.Errorf("invalid check definition %q", checkID)
	}
	if check.SchemaVersion != 2 || check.RecordType != "check_definition" || check.ImplementationID == "" || !validSeverity(check.Severity) || !validApplicability(check.Applicability) {
		return fmt.Errorf("check %s has invalid contract metadata", checkID)
	}
	if _, exists := registry.dimensions[check.DimensionID]; !exists {
		return fmt.Errorf("check %s references unknown dimension %s", checkID, check.DimensionID)
	}
	if _, exists := compiledEvaluators[check.ImplementationID]; !exists {
		return fmt.Errorf("check %s references uncompiled implementation %s", checkID, check.ImplementationID)
	}
	if _, err := identity.SortedSet(check.RequiredCapabilities); err != nil {
		return fmt.Errorf("check %s: %w", checkID, err)
	}
	return nil
}

// ListDimensions returns compiled dimension definitions in stable ID order.
func (registry Registry) ListDimensions() []DimensionDefinition {
	ids := make([]string, 0, len(registry.dimensions))
	for id := range registry.dimensions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	definitions := make([]DimensionDefinition, 0, len(ids))
	for _, id := range ids {
		definition := registry.dimensions[id]
		definition.CheckIDs = append([]string(nil), definition.CheckIDs...)
		definition.RequiredCapabilities = append([]string(nil), definition.RequiredCapabilities...)
		definitions = append(definitions, definition)
	}
	return definitions
}

// ListChecks returns compiled check definitions for one dimension.
func (registry Registry) ListChecks(dimensionID string) ([]CheckDefinition, error) {
	definition, exists := registry.dimensions[dimensionID]
	if !exists {
		return nil, fmt.Errorf("unknown dimension %q", dimensionID)
	}
	checks := make([]CheckDefinition, 0, len(definition.CheckIDs))
	for _, id := range definition.CheckIDs {
		check := registry.checks[id]
		check.RequiredCapabilities = append([]string(nil), check.RequiredCapabilities...)
		checks = append(checks, check)
	}
	return checks, nil
}

// ClassifyUnitPath applies only the reviewed root/modules/examples conventions.
// Paths outside those conventions remain unclassified for explicit review.
func ClassifyUnitPath(unitPath string) (UnitClassification, error) {
	if err := identity.ValidateRelativePath(unitPath, true); err != nil {
		return UnitClassification{}, fmt.Errorf("invalid unit path: %w", err)
	}
	if unitPath == "." {
		return UnitClassification{Role: terraform.RoleComponent, Owner: ".", Method: "root-convention", Classified: true}, nil
	}
	segments := strings.Split(unitPath, "/")
	for index, segment := range segments {
		if segment == "examples" {
			owner := strings.Join(segments[:index], "/")
			if owner == "" {
				owner = "."
			}
			return UnitClassification{Role: terraform.RoleExample, Owner: owner, Method: "examples-convention", Classified: true}, nil
		}
	}
	if len(segments) > 1 && segments[0] == "modules" {
		return UnitClassification{Role: terraform.RoleChildComponent, Owner: unitPath, Method: "modules-convention", Classified: true}, nil
	}
	return UnitClassification{Classified: false}, nil
}

// ClassifyUnit applies path conventions and then a narrow backend/cloud signal
// to examples. Unknown paths and unsignaled examples remain explicitly unknown.
func ClassifyUnit(unit terraform.Unit, override string) (UnitClassification, error) {
	classification, err := ClassifyUnitPath(unit.Path)
	if err != nil {
		return UnitClassification{}, err
	}
	if override != "" {
		if override != classificationModule && override != classificationDeployment && override != classificationUnknown {
			return UnitClassification{}, fmt.Errorf("invalid classification override %q", override)
		}
		classification.Kind = override
		classification.Method = "reviewed-override"
		classification.Classified = override != classificationUnknown
		return classification, nil
	}
	if classification.Role == terraform.RoleExample {
		if hasDeploymentSignal(unit) {
			classification.Kind = classificationDeployment
			classification.Method = "terraform-backend-cloud-signal"
			classification.Classified = true
		} else {
			classification.Kind = classificationUnknown
			classification.Method = "unavailable"
			classification.Classified = false
		}
		return classification, nil
	}
	classification.Kind = classificationUnknown
	classification.Method = "unavailable"
	classification.Classified = false
	return classification, nil
}

func hasDeploymentSignal(unit terraform.Unit) bool {
	for _, symbol := range unit.Symbols {
		if symbol.Kind == "terraform_setting" && (symbol.Setting == "backend" || symbol.Setting == "cloud") {
			return true
		}
	}
	return false
}

// RunDimensions evaluates selected compiled dimensions independently for every
// in-scope unit. Missing optional inputs never erase completed sibling results.
func RunDimensions(registry Registry, input RunInput) ([]DimensionResult, error) {
	if err := registry.Validate(); err != nil {
		return nil, err
	}
	if err := input.Facts.Validate(); err != nil {
		return nil, fmt.Errorf("validate assessment facts: %w", err)
	}
	dimensionIDs, err := selectedDimensions(registry, input.DimensionIDs)
	if err != nil {
		return nil, err
	}
	snapshots, snapshotIDs, err := validateSnapshots(input.Snapshots)
	if err != nil {
		return nil, err
	}
	targetVersions := cloneTargetVersions(input.TargetVersions)
	if err = validateTargetVersions(targetVersions, snapshots); err != nil {
		return nil, err
	}
	units := append([]terraform.Unit(nil), input.Facts.Units...)
	sort.Slice(units, func(i, j int) bool { return units[i].Path < units[j].Path })
	classifications, err := classifyUnits(units, input.Classifications)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	logger := slog.With("module_id", input.Facts.ModuleID, "facts_id", input.Facts.FactsID)
	logger.Info("assessment started", "units", len(units), "dimensions", len(dimensionIDs), "provider_snapshots", len(snapshotIDs))
	results := make([]DimensionResult, 0, len(units)*len(dimensionIDs))
	runContext := dimensionRunContext{
		registry: registry, facts: input.Facts, classificationPolicyID: registry.policyID,
		snapshots: snapshots, snapshotIDs: snapshotIDs, targetVersions: targetVersions,
	}
	for _, unit := range units {
		for _, dimensionID := range dimensionIDs {
			definition := registry.dimensions[dimensionID]
			if !unitInScope(unit, definition.UnitScope) {
				logger.Debug("dimension skipped", "dimension_id", dimensionID, "unit", unit.Path, "reason", "unit outside dimension scope")
				continue
			}
			dimensionStarted := time.Now()
			logger.Debug("dimension started", "dimension_id", dimensionID, "unit", unit.Path)
			classification := classifications[unit.Path]
			result, err := runDimension(definition, unit, classification.Kind, runContext)
			if err != nil {
				return nil, fmt.Errorf("run dimension %s for %s: %w", dimensionID, unit.Path, err)
			}
			results = append(results, result)
			logger.Info("dimension completed", "dimension_id", dimensionID, "unit", unit.Path, "checks", len(result.Checks), "execution_complete", result.ExecutionComplete, "duration", time.Since(dimensionStarted))
			for _, check := range result.Checks {
				logger.Debug("check evaluated", "dimension_id", dimensionID, "unit", unit.Path, "check_id", check.ID, "status", check.Status, "execution", check.Execution)
			}
			if !result.ExecutionComplete {
				logger.Warn("dimension execution incomplete", "dimension_id", dimensionID, "unit", unit.Path, "unavailable_checks", result.Coverage.Unavailable, "reason", "one or more checks did not complete; sibling results preserved")
			}
		}
	}
	logger.Info("assessment completed", "dimension_results", len(results), "duration", time.Since(started))
	return results, nil
}

func classifyUnits(units []terraform.Unit, overrides map[string]string) (map[string]UnitClassification, error) {
	knownPaths := make(map[string]struct{}, len(units))
	for _, unit := range units {
		knownPaths[unit.Path] = struct{}{}
	}
	for unitPath := range overrides {
		if _, exists := knownPaths[unitPath]; !exists {
			return nil, fmt.Errorf("classification override references unknown unit %q", unitPath)
		}
	}
	classifications := make(map[string]UnitClassification, len(units))
	for _, unit := range units {
		classification, err := ClassifyUnit(unit, overrides[unit.Path])
		if err != nil {
			return nil, fmt.Errorf("classify unit %s: %w", unit.Path, err)
		}
		classifications[unit.Path] = classification
	}
	return classifications, nil
}

func runDimension(definition DimensionDefinition, unit terraform.Unit, classification string, context dimensionRunContext) (DimensionResult, error) {
	targetVersions := dimensionTargetVersions(definition, unit, context.targetVersions)
	checks := make([]CheckResult, 0, len(definition.CheckIDs))
	for _, checkID := range definition.CheckIDs {
		check := context.registry.checks[checkID]
		result := runCheck(check, definition.RequiredCapabilities, evaluationInput{facts: context.facts, unit: unit, classification: classification, snapshots: context.snapshots, targetVersions: targetVersions})
		checks = append(checks, result)
	}
	coverage := summarizeChecks(checks)
	executionComplete := coverage.Unavailable == 0
	usedSnapshotIDs := dimensionSnapshotIDs(definition, unit, context.snapshots, context.snapshotIDs)
	vector := inputVector{
		FactsID: context.facts.FactsID, DefinitionID: definition.DefinitionID, UnitPath: unit.Path,
		Classification: classification, ClassificationPolicyID: context.classificationPolicyID,
		SnapshotIDs: usedSnapshotIDs, TargetVersions: targetVersions,
	}
	inputFingerprint, err := fingerprint(vector)
	if err != nil {
		return DimensionResult{}, err
	}
	content := dimensionResultContent{
		SchemaVersion: 2, RecordType: "dimension_result", ModuleID: context.facts.ModuleID,
		SourceSHA: context.facts.SourceSHA, UnitPath: unit.Path, FactsID: context.facts.FactsID,
		DefinitionID: definition.DefinitionID, SnapshotIDs: usedSnapshotIDs,
		InputFingerprint: inputFingerprint, Checks: checks, Coverage: coverage,
		ExecutionComplete: executionComplete, Freshness: FreshnessUnknown,
	}
	canonical, err := identity.CanonicalJSON("dimension-result-content", 2, content)
	if err != nil {
		return DimensionResult{}, fmt.Errorf("canonicalize dimension result: %w", err)
	}
	digestBytes := sha256.Sum256(canonical)
	digest := hex.EncodeToString(digestBytes[:])
	id, err := identity.ID("dimension-result", 2, content)
	if err != nil {
		return DimensionResult{}, fmt.Errorf("identify dimension result: %w", err)
	}
	return DimensionResult{
		SchemaVersion: 2, RecordType: "dimension_result", DimensionResultID: id,
		ModuleID: context.facts.ModuleID, SourceSHA: context.facts.SourceSHA, UnitPath: unit.Path,
		FactsID: context.facts.FactsID, DefinitionID: definition.DefinitionID,
		SnapshotIDs: append([]string(nil), usedSnapshotIDs...), InputFingerprint: inputFingerprint,
		Checks: checks, Coverage: coverage, ExecutionComplete: executionComplete,
		Freshness: FreshnessUnknown, ContentDigest: digest,
	}, nil
}

// Validate authenticates immutable dimension content independently of proposal hashes.
func (result DimensionResult) Validate() error {
	snapshotIDs := result.SnapshotIDs
	if len(snapshotIDs) == 0 {
		snapshotIDs = []string{}
	}
	content := dimensionResultContent{
		SchemaVersion: result.SchemaVersion, RecordType: result.RecordType, ModuleID: result.ModuleID,
		SourceSHA: result.SourceSHA, UnitPath: result.UnitPath, FactsID: result.FactsID,
		DefinitionID: result.DefinitionID, SnapshotIDs: snapshotIDs, InputFingerprint: result.InputFingerprint,
		Checks: result.Checks, Coverage: result.Coverage, ExecutionComplete: result.ExecutionComplete, Freshness: result.Freshness,
	}
	canonical, err := identity.CanonicalJSON("dimension-result-content", 2, content)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(canonical)
	expected, err := identity.ID("dimension-result", 2, content)
	if err != nil {
		return err
	}
	if result.SchemaVersion != 2 || result.RecordType != "dimension_result" || result.DimensionResultID != expected || result.ContentDigest != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("dimension result identity does not match content")
	}
	return nil
}

func dimensionSnapshotIDs(definition DimensionDefinition, unit terraform.Unit, snapshots map[string]ProviderSnapshot, allSnapshotIDs []string) []string {
	if !dimensionConsumesProviderSnapshots(definition) {
		return []string{}
	}
	usedAddresses := dimensionProviderAddresses(definition, unit)
	used := make(map[string]struct{})
	for address := range usedAddresses {
		if snapshot, exists := snapshots[address]; exists {
			used[snapshot.SnapshotID] = struct{}{}
		}
	}
	return selectedSnapshotIDs(allSnapshotIDs, used)
}

func dimensionConsumesProviderSnapshots(definition DimensionDefinition) bool {
	for _, checkID := range definition.CheckIDs {
		if checkID == CheckProviderTypeInventory || checkID == CheckProviderVersionConstraint {
			return true
		}
	}
	return false
}

func dimensionTargetVersions(definition DimensionDefinition, unit terraform.Unit, targetVersions map[string]string) map[string]string {
	consumesTargets := false
	for _, checkID := range definition.CheckIDs {
		if checkID == CheckProviderVersionConstraint {
			consumesTargets = true
			break
		}
	}
	if !consumesTargets || len(targetVersions) == 0 {
		return nil
	}
	selected := make(map[string]string)
	for address := range dimensionProviderAddresses(definition, unit) {
		if version, exists := targetVersions[address]; exists {
			selected[address] = version
		}
	}
	return selected
}

func dimensionProviderAddresses(definition DimensionDefinition, unit terraform.Unit) map[string]struct{} {
	addresses := make(map[string]struct{})
	needsTypeUsage := false
	needsProviderRequirements := false
	for _, checkID := range definition.CheckIDs {
		needsTypeUsage = needsTypeUsage || checkID == CheckProviderTypeInventory
		needsProviderRequirements = needsProviderRequirements || checkID == CheckProviderVersionConstraint
	}
	for _, symbol := range unit.Symbols {
		if needsTypeUsage && symbol.ProviderAddress != "" {
			if _, applicable := providerEntryKind(symbol.Kind, symbol.ProviderAddress); applicable {
				addresses[symbol.ProviderAddress] = struct{}{}
			}
		}
		if needsProviderRequirements && symbol.Kind == "provider" && symbol.Capability == terraform.CapabilityProviderRequirements && symbol.ProviderAddress != "" {
			addresses[symbol.ProviderAddress] = struct{}{}
		}
	}
	return addresses
}

func selectedSnapshotIDs(allSnapshotIDs []string, used map[string]struct{}) []string {
	result := make([]string, 0, len(used))
	for _, snapshotID := range allSnapshotIDs {
		if _, exists := used[snapshotID]; exists {
			result = append(result, snapshotID)
		}
	}
	return result
}

func runCheck(check CheckDefinition, dimensionCapabilities []string, input evaluationInput) CheckResult {
	if result, handled := checkApplicability(check, input); handled {
		return result
	}
	if check.Applicability == applicabilityProviderUsage && !hasProviderUsage(input.unit) {
		return resultFor(check, StatusNotApplicable, ExecutionCompleted, "No provider-backed type usage was observed.", nil)
	}
	if check.Applicability == applicabilityProviderRequirement && !hasProviderRequirement(input.unit) {
		return resultFor(check, StatusNotApplicable, ExecutionCompleted, "No provider requirement was declared.", nil)
	}
	missing := missingCapabilities(check.RequiredCapabilities, dimensionCapabilities, input.facts.Coverage.Capabilities)
	if len(missing) > 0 {
		return resultFor(check, StatusUnchecked, ExecutionMissingInput, "Required facts capability is incomplete: "+strings.Join(missing, ", "), nil)
	}
	evaluate := compiledEvaluators[check.ImplementationID]
	if evaluate == nil {
		return resultFor(check, StatusUnchecked, ExecutionError, "Compiled check implementation is unavailable.", nil)
	}
	return evaluate(check, input)
}

func missingCapabilities(checkCapabilities, dimensionCapabilities []string, coverage map[string]string) []string {
	all := append(append([]string(nil), dimensionCapabilities...), checkCapabilities...)
	sort.Strings(all)
	missing := make([]string, 0)
	for index, capability := range all {
		if index > 0 && capability == all[index-1] {
			continue
		}
		if coverage[capability] != terraform.CoverageComplete {
			missing = append(missing, capability)
		}
	}
	return missing
}

func checkApplicability(check CheckDefinition, input evaluationInput) (CheckResult, bool) {
	if check.Applicability == applicabilityProviderRequirement {
		return CheckResult{}, false
	}
	if check.Applicability != classificationModule && check.Applicability != classificationDeployment {
		return CheckResult{}, false
	}
	if input.classification == classificationUnknown || input.classification == "" {
		return resultFor(check, StatusUnchecked, ExecutionMissingInput, "Reviewed unit classification is unavailable.", nil), true
	}
	if input.classification != check.Applicability {
		return resultFor(check, StatusNotApplicable, ExecutionCompleted, "Check does not apply to the reviewed unit classification.", nil), true
	}
	return CheckResult{}, false
}

func evaluateProviderRequirements(check CheckDefinition, input evaluationInput) CheckResult {
	declarations := make(map[string]terraform.FactSymbol)
	for _, symbol := range input.unit.Symbols {
		if symbol.Kind == "provider" && symbol.Capability == terraform.CapabilityProviderRequirements {
			declarations[symbol.LocalProviderName] = symbol
		}
	}
	if len(declarations) > 0 {
		return resultFor(check, StatusPass, ExecutionCompleted, "Provider requirements are declared.", providerEvidence(declarations))
	}
	return resultFor(check, StatusFail, ExecutionCompleted, "Provider-backed resource or data usage has no explicit provider requirement declaration.", usageEvidence(input.unit))
}

func evaluateFactsCapability(check CheckDefinition, input evaluationInput) CheckResult {
	capabilities := []string{terraform.CapabilityAdditionalKinds, terraform.CapabilityProviderLockSelections}
	for _, capability := range capabilities {
		if input.facts.Coverage.Capabilities[capability] != terraform.CoverageComplete {
			return resultFor(check, StatusUnchecked, ExecutionMissingInput, "Additional symbol kinds or provider lock selections remain unavailable.", nil)
		}
	}
	return resultFor(check, StatusPass, ExecutionCompleted, "All registered static symbol kinds are covered.", nil)
}

func evaluateProviderInventory(check CheckDefinition, input evaluationInput) CheckResult {
	var missing, unavailable, present []string
	for _, symbol := range input.unit.Symbols {
		assessment := assessProviderUsage(symbol, input.snapshots)
		switch assessment.status {
		case StatusPass:
			present = append(present, assessment.evidence)
		case StatusFail:
			missing = append(missing, assessment.evidence)
		case StatusUnchecked:
			unavailable = append(unavailable, assessment.evidence)
		}
	}
	if len(unavailable) > 0 {
		return resultFor(check, StatusUnchecked, ExecutionMissingInput, "Provider inventory is missing or incomplete for one or more used types.", uniqueSorted(unavailable))
	}
	if len(missing) > 0 {
		return resultFor(check, StatusFail, ExecutionCompleted, "Provider inventory does not contain one or more used types.", uniqueSorted(missing))
	}
	return resultFor(check, StatusPass, ExecutionCompleted, "Provider inventory contains every used type.", uniqueSorted(present))
}

func evaluateProviderCurrency(check CheckDefinition, input evaluationInput) CheckResult {
	var unavailable, incompatible, evidence []string
	for _, symbol := range input.unit.Symbols {
		if symbol.Kind != "provider" || symbol.Capability != terraform.CapabilityProviderRequirements {
			continue
		}
		judgment := evaluateProviderConstraint(symbol, input)
		if judgment.err != nil {
			return resultFor(check, StatusUnchecked, ExecutionError, judgment.err.Error(), []string{factEvidence(symbol)})
		}
		if judgment.unavailable {
			unavailable = append(unavailable, factEvidence(symbol))
			continue
		}
		evidence = append(evidence, judgment.evidence)
		if judgment.incompatible {
			incompatible = append(incompatible, judgment.evidence)
		}
	}
	if len(unavailable) > 0 {
		return resultFor(check, StatusUnchecked, ExecutionMissingInput, "Exact accepted provider targets are missing for one or more declared providers.", uniqueSorted(unavailable))
	}
	if len(incompatible) > 0 {
		return resultFor(check, StatusFail, ExecutionCompleted, "Selected provider target violates a declared version constraint.", uniqueSorted(incompatible))
	}
	return resultFor(check, StatusPass, ExecutionCompleted, "Selected provider targets satisfy the declared version constraints.", uniqueSorted(evidence))
}

type providerConstraintJudgment struct {
	unavailable  bool
	incompatible bool
	evidence     string
	err          error
}

func evaluateProviderConstraint(symbol terraform.FactSymbol, input evaluationInput) providerConstraintJudgment {
	address := symbol.ProviderAddress
	targetVersion, hasTarget := input.targetVersions[address]
	snapshot, hasSnapshot := input.snapshots[address]
	if address == "" || !hasTarget || !hasSnapshot || snapshot.ProviderVersion != targetVersion {
		return providerConstraintJudgment{unavailable: true}
	}
	selectedVersion, err := version.NewVersion(targetVersion)
	if err != nil {
		return providerConstraintJudgment{err: fmt.Errorf("selected provider target is not a valid version")}
	}
	evidence := factEvidence(symbol) + "@" + targetVersion
	if symbol.ProviderVersionConstraint == "" {
		return providerConstraintJudgment{evidence: evidence}
	}
	constraints, err := version.NewConstraint(symbol.ProviderVersionConstraint)
	if err != nil {
		return providerConstraintJudgment{err: fmt.Errorf("provider version constraint is not supported by the compiled constraint parser")}
	}
	return providerConstraintJudgment{evidence: evidence, incompatible: !constraints.Check(selectedVersion)}
}

func evaluateInputDescriptions(check CheckDefinition, input evaluationInput) CheckResult {
	return evaluateDescriptionPresence(check, input, "variable")
}

func evaluateOutputDescriptions(check CheckDefinition, input evaluationInput) CheckResult {
	return evaluateDescriptionPresence(check, input, "output")
}

func evaluateDescriptionPresence(check CheckDefinition, input evaluationInput, kind string) CheckResult {
	var undocumented, evidence []string
	declarations := 0
	for _, symbol := range input.unit.Symbols {
		if symbol.Kind != kind {
			continue
		}
		declarations++
		if symbol.DescriptionPresent == nil {
			return resultFor(check, StatusUnchecked, ExecutionMissingInput, "Description-presence metadata is unavailable.", []string{factEvidence(symbol)})
		}
		evidence = append(evidence, factEvidence(symbol))
		if !*symbol.DescriptionPresent {
			undocumented = append(undocumented, factEvidence(symbol))
		}
	}
	if declarations == 0 {
		return resultFor(check, StatusNotApplicable, ExecutionCompleted, "No applicable declarations were observed.", nil)
	}
	if len(undocumented) > 0 {
		return resultFor(check, StatusFail, ExecutionCompleted, "One or more declarations lack a nonempty description.", uniqueSorted(undocumented))
	}
	return resultFor(check, StatusPass, ExecutionCompleted, "All declarations have nonempty descriptions.", uniqueSorted(evidence))
}

func evaluateModuleBackend(check CheckDefinition, input evaluationInput) CheckResult {
	return evaluateModuleSettingAbsent(check, input, "backend", "State backend belongs in the deployment entry point.")
}

func evaluateModuleCloud(check CheckDefinition, input evaluationInput) CheckResult {
	return evaluateModuleSettingAbsent(check, input, "cloud", "Cloud workspace configuration belongs in the deployment entry point.")
}

func evaluateModuleSettingAbsent(check CheckDefinition, input evaluationInput, setting, message string) CheckResult {
	for _, symbol := range input.unit.Symbols {
		if symbol.Kind == "terraform_setting" && symbol.Setting == setting {
			return resultFor(check, StatusFail, ExecutionCompleted, message, []string{factEvidence(symbol)})
		}
	}
	return resultFor(check, StatusPass, ExecutionCompleted, "Terraform setting is absent from the module.", nil)
}

func evaluateDeploymentCoreVersion(check CheckDefinition, input evaluationInput) CheckResult {
	for _, symbol := range input.unit.Symbols {
		if symbol.Kind == "terraform_setting" && symbol.Setting == "required_version" {
			return resultFor(check, StatusPass, ExecutionCompleted, "Terraform core version constraint is declared.", []string{factEvidence(symbol)})
		}
	}
	return resultFor(check, StatusFail, ExecutionCompleted, "Declare the Terraform core version supported by this deployment.", nil)
}

type providerUsageAssessment struct {
	status   string
	evidence string
}

func assessProviderUsage(symbol terraform.FactSymbol, snapshots map[string]ProviderSnapshot) providerUsageAssessment {
	evidence := factEvidence(symbol)
	kind, applicable := providerEntryKind(symbol.Kind, symbol.ProviderAddress)
	if !applicable {
		return providerUsageAssessment{status: StatusNotApplicable, evidence: evidence}
	}
	if symbol.ProviderAddress == "" {
		return providerUsageAssessment{status: StatusUnchecked, evidence: evidence}
	}
	snapshot, exists := snapshots[symbol.ProviderAddress]
	if !exists {
		return providerUsageAssessment{status: StatusUnchecked, evidence: evidence}
	}
	if snapshot.Coverage.Capabilities[kind] != terraform.CoverageComplete {
		return providerUsageAssessment{status: StatusUnchecked, evidence: evidence}
	}
	if containsProviderEntry(snapshot.Entries, kind, symbolType(symbol.Name)) {
		return providerUsageAssessment{status: StatusPass, evidence: evidence}
	}
	return providerUsageAssessment{status: StatusFail, evidence: evidence}
}

func providerEntryKind(symbolKind, providerAddress string) (string, bool) {
	switch symbolKind {
	case "resource":
		return "resource", true
	case "data":
		return "datasource", true
	case "ephemeral":
		return "ephemeral", true
	case "action":
		return "action", true
	case "function":
		return "function", providerAddress != ""
	default:
		return "", false
	}
}

func resultFor(check CheckDefinition, status, execution, message string, evidence []string) CheckResult {
	if evidence == nil {
		evidence = []string{}
	}
	return CheckResult{ID: check.CheckID, Version: check.Version, Status: status, Execution: execution, Severity: check.Severity, Evidence: evidence, Message: message}
}

func summarizeChecks(checks []CheckResult) ResultCoverage {
	coverage := ResultCoverage{}
	for _, check := range checks {
		if check.Status != StatusNotApplicable {
			coverage.Applicable++
		}
		if check.Execution == ExecutionCompleted && check.Status != StatusNotApplicable {
			coverage.Assessed++
		}
		if check.Execution == ExecutionMissingInput || check.Execution == ExecutionError {
			coverage.Unavailable++
		}
	}
	return coverage
}

func selectedDimensions(registry Registry, requested []string) ([]string, error) {
	if len(requested) == 0 {
		requested = make([]string, 0, len(registry.dimensions))
		for id := range registry.dimensions {
			requested = append(requested, id)
		}
	}
	selected, err := identity.SortedSet(requested)
	if err != nil {
		return nil, err
	}
	for _, id := range selected {
		if _, exists := registry.dimensions[id]; !exists {
			return nil, fmt.Errorf("unknown dimension %q", id)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("at least one dimension must be selected")
	}
	return selected, nil
}

func validateSnapshots(snapshots []ProviderSnapshot) (map[string]ProviderSnapshot, []string, error) {
	byAddress := make(map[string]ProviderSnapshot, len(snapshots))
	ids := make([]string, 0, len(snapshots))
	seenIDs := make(map[string]struct{}, len(snapshots))
	for _, snapshot := range snapshots {
		if err := validateProviderSnapshot(snapshot); err != nil {
			return nil, nil, err
		}
		if _, exists := byAddress[snapshot.ProviderAddress]; exists {
			return nil, nil, fmt.Errorf("multiple provider snapshots target %s; select a single context snapshot", snapshot.ProviderAddress)
		}
		if _, exists := seenIDs[snapshot.SnapshotID]; exists {
			return nil, nil, fmt.Errorf("duplicate provider snapshot ID %s", snapshot.SnapshotID)
		}
		seenIDs[snapshot.SnapshotID] = struct{}{}
		byAddress[snapshot.ProviderAddress] = snapshot
		ids = append(ids, snapshot.SnapshotID)
	}
	sort.Strings(ids)
	return byAddress, ids, nil
}

func cloneTargetVersions(targetVersions map[string]string) map[string]string {
	if len(targetVersions) == 0 {
		return nil
	}
	clone := make(map[string]string, len(targetVersions))
	for address, version := range targetVersions {
		clone[address] = version
	}
	return clone
}

func validateTargetVersions(targetVersions map[string]string, snapshots map[string]ProviderSnapshot) error {
	if len(targetVersions) == 0 {
		return nil
	}
	if len(targetVersions) != len(snapshots) {
		return fmt.Errorf("assessment target versions do not match the provider snapshot vector")
	}
	for address, version := range targetVersions {
		snapshot, exists := snapshots[address]
		if !exists || version == "" || snapshot.ProviderVersion != version {
			return fmt.Errorf("assessment target version for %s does not match its accepted provider snapshot", address)
		}
	}
	return nil
}

func validateProviderSnapshot(snapshot ProviderSnapshot) error {
	if snapshot.SchemaVersion != 2 || snapshot.RecordType != "provider_snapshot" {
		return fmt.Errorf("provider snapshot schema identity is invalid")
	}
	if err := identity.ValidateRecordID("snapshot", snapshot.SnapshotID); err != nil {
		return fmt.Errorf("invalid provider snapshot ID: %w", err)
	}
	if !providerAddressPattern.MatchString(snapshot.ProviderAddress) || snapshot.ProviderVersion == "" || !isSHA256(snapshot.SourceDigest) {
		return fmt.Errorf("provider snapshot identity is invalid")
	}
	if snapshot.Producer.ID == "" || snapshot.Producer.Version == "" || snapshot.Producer.ContractVersion < 1 {
		return fmt.Errorf("provider snapshot producer identity is invalid")
	}
	if snapshot.Producer.Commit != "" && !sha1Pattern.MatchString(snapshot.Producer.Commit) ||
		snapshot.Producer.BinaryDigest != "" && !sha256Pattern.MatchString(snapshot.Producer.BinaryDigest) {
		return fmt.Errorf("provider snapshot producer digest is invalid")
	}
	if !validTimestamp(snapshot.Coverage.ObservedAt) || !validTimestamp(snapshot.ObservedAt) {
		return fmt.Errorf("provider snapshot observation time is invalid")
	}
	if err := validateProviderEntries(snapshot.Entries); err != nil {
		return err
	}
	return validateSnapshotCoverage(snapshot.Coverage.Capabilities)
}

func validateProviderEntries(entries []ProviderEntry) error {
	for _, entry := range entries {
		if !validProviderEntryKind(entry.Kind) || entry.Name == "" {
			return fmt.Errorf("provider snapshot contains an invalid entry")
		}
		if entry.ContentDigest != "" && !sha256Pattern.MatchString(entry.ContentDigest) {
			return fmt.Errorf("provider snapshot entry digest is invalid")
		}
	}
	return nil
}

func validateSnapshotCoverage(coverage map[string]string) error {
	for capability, state := range coverage {
		if capability == "" || state != terraform.CoverageComplete && state != terraform.CoveragePartial && state != terraform.CoverageUnavailable && state != terraform.CoverageUnknown {
			return fmt.Errorf("provider snapshot coverage entry %q=%q is invalid", capability, state)
		}
	}
	return nil
}

func isSHA256(value string) bool {
	return sha256Pattern.MatchString(value)
}

func validTimestamp(value string) bool {
	if value == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func fingerprint(vector inputVector) (string, error) {
	canonical, err := identity.CanonicalJSON("assessment-input", 1, vector)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func unitInScope(unit terraform.Unit, scope string) bool {
	if scope == "repository" {
		return true
	}
	if scope == "component" {
		return unit.Role == terraform.RoleComponent || unit.Role == terraform.RoleChildComponent
	}
	return unit.Role == scope
}

func hasProviderUsage(unit terraform.Unit) bool {
	for _, symbol := range unit.Symbols {
		if symbol.ProviderAddress == "" {
			continue
		}
		if _, applicable := providerEntryKind(symbol.Kind, symbol.ProviderAddress); applicable {
			return true
		}
	}
	return false
}

func hasProviderRequirement(unit terraform.Unit) bool {
	for _, symbol := range unit.Symbols {
		if symbol.Kind == "provider" && symbol.Capability == terraform.CapabilityProviderRequirements {
			return true
		}
	}
	return false
}

func providerEvidence(declarations map[string]terraform.FactSymbol) []string {
	evidence := make([]string, 0, len(declarations))
	for _, symbol := range declarations {
		evidence = append(evidence, factEvidence(symbol))
	}
	return uniqueSorted(evidence)
}

func usageEvidence(unit terraform.Unit) []string {
	evidence := []string{}
	for _, symbol := range unit.Symbols {
		if _, applicable := providerEntryKind(symbol.Kind, symbol.ProviderAddress); applicable {
			evidence = append(evidence, factEvidence(symbol))
		}
	}
	return uniqueSorted(evidence)
}

func factEvidence(symbol terraform.FactSymbol) string {
	return fmt.Sprintf("%s:%d:%s", symbol.Location.File, symbol.Location.Line, symbol.Name)
}

func symbolType(name string) string {
	if separator := strings.IndexByte(name, '.'); separator >= 0 {
		return name[:separator]
	}
	return name
}

func containsProviderEntry(entries []ProviderEntry, kind, name string) bool {
	for _, entry := range entries {
		if entry.Kind == kind && entry.Name == name {
			return true
		}
	}
	return false
}

func validProviderEntryKind(kind string) bool {
	switch kind {
	case "resource", "datasource", "ephemeral", "function", "action":
		return true
	default:
		return false
	}
}

func validUnitScope(scope string) bool {
	switch scope {
	case "component", "child_component", "example", "repository":
		return true
	default:
		return false
	}
}

func validSeverity(severity string) bool {
	return severity == SeverityInfo || severity == SeverityWarning || severity == SeverityError
}

func validApplicability(applicability string) bool {
	switch applicability {
	case applicabilityAlways, applicabilityProviderUsage, applicabilityProviderRequirement, classificationModule, classificationDeployment:
		return true
	default:
		return false
	}
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

var compiledEvaluators = map[string]evaluator{
	"provider-requirements.v1":       evaluateProviderRequirements,
	"facts-capability.v1":            evaluateFactsCapability,
	"provider-inventory.v1":          evaluateProviderInventory,
	"provider-version-constraint.v1": evaluateProviderCurrency,
	"input-descriptions.v1":          evaluateInputDescriptions,
	"output-descriptions.v1":         evaluateOutputDescriptions,
	"module-backend.v1":              evaluateModuleBackend,
	"module-cloud.v1":                evaluateModuleCloud,
	"deployment-core-version.v1":     evaluateDeploymentCoreVersion,
}
