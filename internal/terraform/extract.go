// Package terraform extracts sanitized, source-backed Terraform facts without
// evaluating expressions or invoking Terraform or provider binaries.
package terraform

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	hcljson "github.com/hashicorp/hcl/v2/json"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/zclconf/go-cty/cty"
)

const (
	FactsSchemaVersion = 3

	RoleComponent      = "component"
	RoleChildComponent = "child_component"
	RoleExample        = "example"

	CapabilityProviderRequirements   = "provider_requirements"
	CapabilityProviderBindings       = "provider_bindings"
	CapabilityInterfaceMetadata      = "interface_metadata"
	CapabilityTerraformSettings      = "terraform_settings"
	CapabilityResourceUsage          = "resource_usage"
	CapabilityDataUsage              = "data_usage"
	CapabilityChildModules           = "child_modules"
	CapabilityUnitPaths              = "unit_paths"
	CapabilityLocations              = "locations"
	CapabilityAdditionalKinds        = "ephemeral_function_action"
	CapabilityProviderLockSelections = "provider_lock_selections"

	CoverageComplete    = "complete"
	CoveragePartial     = "partial"
	CoverageUnavailable = "unavailable"
	CoverageUnknown     = "unknown"
)

var (
	moduleIDPattern   = regexp.MustCompile(`^avm-(res|ptn|utl)-[a-z0-9]+(?:-[a-z0-9]+)*$`)
	sha1Pattern       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	addressPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*$`)
	versionPattern    = regexp.MustCompile(`^[~><=!0-9][~><=!0-9A-Za-z.+, *-]*$`)
	symbolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// Input identifies the immutable source revision and Terraform units to scan.
type Input struct {
	RootDir    string
	ModuleID   string
	SourceSHA  string
	ObservedAt time.Time
	Units      []UnitInput
	LockFiles  []LockFileInput
}

// UnitInput identifies one source unit and its explicitly allowed Terraform files.
type UnitInput struct {
	Path  string
	Role  string
	Owner string
	Files []string
}

// LockFileInput identifies one explicitly discovered provider lockfile and its owning unit.
type LockFileInput struct {
	Path     string
	UnitPath string
}

// Facts is the sanitized, content-addressed output of ExtractFacts.
type Facts struct {
	SchemaVersion int      `json:"schema_version"`
	RecordType    string   `json:"record_type"`
	FactsID       string   `json:"facts_id"`
	ModuleID      string   `json:"module_id"`
	SourceSHA     string   `json:"source_sha"`
	Units         []Unit   `json:"units"`
	Coverage      Coverage `json:"coverage"`
	ContentDigest string   `json:"content_digest"`
}

// Unit contains source-backed symbols for one component, child, or example.
type Unit struct {
	Path    string       `json:"path"`
	Role    string       `json:"role"`
	Owner   string       `json:"owner,omitempty"`
	Symbols []FactSymbol `json:"symbols"`
}

// FactSymbol contains a typed Terraform declaration without evaluated values.
type FactSymbol struct {
	Kind                      string            `json:"kind"`
	Name                      string            `json:"name"`
	ProviderAddress           string            `json:"provider_address,omitempty"`
	LocalProviderName         string            `json:"local_provider_name,omitempty"`
	Alias                     string            `json:"alias,omitempty"`
	ProviderVersionConstraint string            `json:"provider_version_constraint,omitempty"`
	ProviderVersionSelection  string            `json:"provider_version_selection,omitempty"`
	ProviderMappings          []ProviderMapping `json:"provider_mappings,omitempty"`
	DescriptionPresent        *bool             `json:"description_present,omitempty"`
	Setting                   string            `json:"setting,omitempty"`
	SettingType               string            `json:"setting_type,omitempty"`
	ModuleSource              string            `json:"module_source,omitempty"`
	ModuleVersion             string            `json:"module_version,omitempty"`
	UnitPath                  string            `json:"unit_path,omitempty"`
	Location                  Location          `json:"location"`
	Capability                string            `json:"capability"`
}

// ProviderMapping records a child's local provider name and its parent binding.
type ProviderMapping struct {
	ChildLocalName  string `json:"child_local_name"`
	ParentLocalName string `json:"parent_local_name"`
	ParentAlias     string `json:"parent_alias,omitempty"`
	ProviderAddress string `json:"provider_address"`
}

// Location identifies the safe relative source file and declaration line.
type Location struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Coverage reports completeness separately for each extraction capability.
type Coverage struct {
	Capabilities map[string]string `json:"capabilities"`
	Complete     bool              `json:"complete"`
	ObservedAt   string            `json:"observed_at"`
	Reason       string            `json:"reason,omitempty"`
}

type requirement struct {
	address       string
	version       string
	versionKnown  bool
	versionExists bool
}

type parsedSource struct {
	path string
	file *hcl.File
}

type extraction struct {
	unit      *Unit
	unitPath  string
	filePath  string
	providers map[string]requirement
	coverage  map[string]string
	reasons   []string
}

// ExtractFacts reads only the explicitly listed files and emits facts that
// can be derived from syntax alone. Dynamic values remain unknown in coverage.
func ExtractFacts(input Input) (Facts, error) {
	root, err := validateInput(input)
	if err != nil {
		return Facts{}, err
	}
	coverage := initialCoverage()
	units, reasons, err := extractUnits(root, input.Units, coverage)
	if err != nil {
		return Facts{}, err
	}
	if err = extractProviderLockSelections(root, input.LockFiles, units, coverage, &reasons); err != nil {
		return Facts{}, err
	}
	return buildFacts(input, units, coverage, reasons)
}

func validateInput(input Input) (string, error) {
	if !moduleIDPattern.MatchString(input.ModuleID) {
		return "", fmt.Errorf("invalid module ID %q", input.ModuleID)
	}
	if !sha1Pattern.MatchString(input.SourceSHA) {
		return "", fmt.Errorf("source SHA must be a lowercase 40-character SHA-1")
	}
	if input.ObservedAt.IsZero() || len(input.Units) == 0 || input.RootDir == "" {
		return "", fmt.Errorf("source root, observation time, and at least one unit are required")
	}
	root, err := filepath.Abs(input.RootDir)
	if err != nil {
		return "", fmt.Errorf("resolve source root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve source root symlinks: %w", err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return "", fmt.Errorf("source root must be a readable directory")
	}
	return root, nil
}

func initialCoverage() map[string]string {
	return map[string]string{
		CapabilityProviderRequirements:   CoverageComplete,
		CapabilityProviderBindings:       CoverageComplete,
		CapabilityInterfaceMetadata:      CoverageComplete,
		CapabilityTerraformSettings:      CoverageComplete,
		CapabilityResourceUsage:          CoverageComplete,
		CapabilityDataUsage:              CoverageComplete,
		CapabilityChildModules:           CoverageComplete,
		CapabilityUnitPaths:              CoverageComplete,
		CapabilityLocations:              CoverageComplete,
		CapabilityAdditionalKinds:        CoverageUnavailable,
		CapabilityProviderLockSelections: CoverageUnavailable,
	}
}

func extractUnits(root string, unitInputs []UnitInput, coverage map[string]string) ([]Unit, []string, error) {
	units := make([]Unit, 0, len(unitInputs))
	reasons := []string{}
	seenUnits := make(map[string]struct{}, len(unitInputs))
	seenFiles := make(map[string]struct{})
	for _, unitInput := range unitInputs {
		if _, exists := seenUnits[unitInput.Path]; exists {
			return nil, nil, fmt.Errorf("duplicate unit path %q", unitInput.Path)
		}
		seenUnits[unitInput.Path] = struct{}{}
		unit, unitReasons, err := extractUnit(root, unitInput, seenFiles, coverage)
		if err != nil {
			return nil, nil, err
		}
		units = append(units, unit)
		reasons = append(reasons, unitReasons...)
	}
	sort.Slice(units, func(i, j int) bool { return units[i].Path < units[j].Path })
	return units, reasons, nil
}

func extractUnit(root string, unitInput UnitInput, seenFiles map[string]struct{}, coverage map[string]string) (Unit, []string, error) {
	if err := validateUnitInput(unitInput); err != nil {
		return Unit{}, nil, err
	}
	unit := Unit{Path: unitInput.Path, Role: unitInput.Role, Owner: unitInput.Owner, Symbols: []FactSymbol{}}
	state := extraction{unit: &unit, unitPath: unitInput.Path, providers: make(map[string]requirement), coverage: coverage}
	parsedFiles, err := parseUnitFiles(root, unitInput, seenFiles)
	if err != nil {
		return Unit{}, nil, err
	}
	for _, source := range parsedFiles {
		state.filePath = source.path
		if err := state.collectRequirements(source.file); err != nil {
			return Unit{}, nil, fmt.Errorf("extract %q: %w", source.path, err)
		}
	}
	for _, source := range parsedFiles {
		state.filePath = source.path
		if err := state.extractFile(source.file); err != nil {
			return Unit{}, nil, fmt.Errorf("extract %q: %w", source.path, err)
		}
	}
	sort.Slice(unit.Symbols, func(i, j int) bool { return symbolLess(unit.Symbols[i], unit.Symbols[j]) })
	return unit, state.reasons, nil
}

func validateUnitInput(unitInput UnitInput) error {
	if err := identity.ValidateRelativePath(unitInput.Path, true); err != nil {
		return fmt.Errorf("unsafe unit path %q: %w", unitInput.Path, err)
	}
	if unitInput.Role != RoleComponent && unitInput.Role != RoleChildComponent && unitInput.Role != RoleExample {
		return fmt.Errorf("invalid unit role %q", unitInput.Role)
	}
	if unitInput.Owner != "" {
		if err := identity.ValidateRelativePath(unitInput.Owner, true); err != nil {
			return fmt.Errorf("unsafe owner path %q: %w", unitInput.Owner, err)
		}
	}
	if len(unitInput.Files) == 0 {
		return fmt.Errorf("unit %q has no source files", unitInput.Path)
	}
	return nil
}

func parseUnitFiles(root string, unitInput UnitInput, seenFiles map[string]struct{}) ([]parsedSource, error) {
	files := append([]string(nil), unitInput.Files...)
	sort.Strings(files)
	parsedFiles := make([]parsedSource, 0, len(files))
	for _, sourceFile := range files {
		parsed, err := readAndParseSource(root, unitInput.Path, sourceFile, seenFiles)
		if err != nil {
			return nil, err
		}
		parsedFiles = append(parsedFiles, parsed)
	}
	return parsedFiles, nil
}

func readAndParseSource(root, unitPath, sourceFile string, seenFiles map[string]struct{}) (parsedSource, error) {
	if err := identity.ValidateRelativePath(sourceFile, false); err != nil {
		return parsedSource{}, fmt.Errorf("unsafe source path %q: %w", sourceFile, err)
	}
	if !strings.HasSuffix(sourceFile, ".tf") && !strings.HasSuffix(sourceFile, ".tf.json") {
		return parsedSource{}, fmt.Errorf("unsupported Terraform source extension %q", sourceFile)
	}
	rootRelative := sourceFile
	if unitPath != "." {
		rootRelative = filepath.ToSlash(filepath.Join(filepath.FromSlash(unitPath), filepath.FromSlash(sourceFile)))
	}
	if err := identity.ValidateRelativePath(rootRelative, false); err != nil {
		return parsedSource{}, fmt.Errorf("unsafe root-relative source path %q: %w", rootRelative, err)
	}
	absolute := filepath.Join(root, filepath.FromSlash(rootRelative))
	if err := rejectSymlinkComponents(root, absolute); err != nil {
		return parsedSource{}, fmt.Errorf("unsafe source file %q: %w", rootRelative, err)
	}
	if _, exists := seenFiles[absolute]; exists {
		return parsedSource{}, fmt.Errorf("source file %q appears in multiple units", rootRelative)
	}
	seenFiles[absolute] = struct{}{}
	contents, err := os.ReadFile(absolute)
	if err != nil {
		return parsedSource{}, fmt.Errorf("read source file %q: %w", rootRelative, err)
	}
	parser := hclparse.NewParser()
	var file *hcl.File
	var diagnostics hcl.Diagnostics
	if strings.HasSuffix(sourceFile, ".tf.json") {
		file, diagnostics = parser.ParseJSON(contents, rootRelative)
	} else {
		file, diagnostics = parser.ParseHCL(contents, rootRelative)
	}
	if diagnostics.HasErrors() {
		return parsedSource{}, fmt.Errorf("parse %q: %s", rootRelative, diagnostics.Error())
	}
	return parsedSource{path: rootRelative, file: file}, nil
}

func buildFacts(input Input, units []Unit, coverage map[string]string, reasons []string) (Facts, error) {
	capabilities := make(map[string]string, len(coverage))
	complete := true
	for name, status := range coverage {
		capabilities[name] = status
		complete = complete && status == CoverageComplete
	}
	sort.Strings(reasons)
	sortedReasons := make([]string, 0, len(reasons))
	for _, item := range reasons {
		if len(sortedReasons) == 0 || sortedReasons[len(sortedReasons)-1] != item {
			sortedReasons = append(sortedReasons, item)
		}
	}
	reason := strings.Join(sortedReasons, "; ")
	if reason == "" {
		reason = "initial extraction supports resource and data usage; other symbol kinds are not collected"
	}
	coverageRecord := Coverage{Capabilities: capabilities, Complete: complete, ObservedAt: input.ObservedAt.UTC().Format(time.RFC3339Nano), Reason: reason}
	content := factsContent{SchemaVersion: FactsSchemaVersion, RecordType: "facts", ModuleID: input.ModuleID, SourceSHA: input.SourceSHA, Units: units, Coverage: coverageRecord}
	digest, id, err := factIdentity(content)
	if err != nil {
		return Facts{}, err
	}
	return Facts{
		SchemaVersion: FactsSchemaVersion, RecordType: "facts", FactsID: id, ModuleID: input.ModuleID,
		SourceSHA: input.SourceSHA, Units: units, Coverage: coverageRecord, ContentDigest: digest,
	}, nil
}

func (state *extraction) extractFile(file *hcl.File) error {
	rootSchema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{
		{Type: "terraform"}, {Type: "provider", LabelNames: []string{"name"}},
		{Type: "resource", LabelNames: []string{"type", "name"}}, {Type: "data", LabelNames: []string{"type", "name"}},
		{Type: "module", LabelNames: []string{"name"}}, {Type: "variable", LabelNames: []string{"name"}},
		{Type: "output", LabelNames: []string{"name"}}, {Type: "ephemeral", LabelNames: []string{"type", "name"}},
		{Type: "action", LabelNames: []string{"type", "name"}}, {Type: "function", LabelNames: []string{"name"}},
	}}
	content, _, diagnostics := file.Body.PartialContent(rootSchema)
	if diagnostics.HasErrors() {
		return fmt.Errorf("decode Terraform blocks: %s", diagnostics.Error())
	}
	if hasUnsupportedTopLevelBlocks(file.Body) {
		state.coverage[CapabilityAdditionalKinds] = CoveragePartial
		state.reasons = append(state.reasons, "unrecognized top-level Terraform blocks are not collected")
	}
	var providerBlocks, resourceBlocks, dataBlocks, ephemeralBlocks, actionBlocks, moduleBlocks, variableBlocks, outputBlocks, unsupported []*hcl.Block
	for _, block := range content.Blocks {
		switch block.Type {
		case "terraform":
			continue
		case "provider":
			providerBlocks = append(providerBlocks, block)
		case "resource":
			resourceBlocks = append(resourceBlocks, block)
		case "data":
			dataBlocks = append(dataBlocks, block)
		case "ephemeral":
			ephemeralBlocks = append(ephemeralBlocks, block)
		case "action":
			actionBlocks = append(actionBlocks, block)
		case "module":
			moduleBlocks = append(moduleBlocks, block)
		case "variable":
			variableBlocks = append(variableBlocks, block)
		case "output":
			outputBlocks = append(outputBlocks, block)
		default:
			unsupported = append(unsupported, block)
		}
	}
	if len(unsupported) > 0 {
		state.coverage[CapabilityAdditionalKinds] = CoveragePartial
		state.reasons = append(state.reasons, "unrecognized additional Terraform declarations are not collected")
	}
	for _, block := range providerBlocks {
		if err := state.extractProvider(block); err != nil {
			return err
		}
	}
	for _, block := range resourceBlocks {
		if err := state.extractResource(block, "resource", CapabilityResourceUsage); err != nil {
			return err
		}
	}
	for _, block := range dataBlocks {
		if err := state.extractResource(block, "data", CapabilityDataUsage); err != nil {
			return err
		}
	}
	for _, block := range ephemeralBlocks {
		if err := state.extractResource(block, "ephemeral", CapabilityAdditionalKinds); err != nil {
			return err
		}
	}
	for _, block := range actionBlocks {
		if err := state.extractResource(block, "action", CapabilityAdditionalKinds); err != nil {
			return err
		}
	}
	for _, block := range moduleBlocks {
		if err := state.extractModule(block); err != nil {
			return err
		}
	}
	for _, block := range variableBlocks {
		if err := state.extractInterfaceDeclaration(block, "variable"); err != nil {
			return err
		}
	}
	for _, block := range outputBlocks {
		if err := state.extractInterfaceDeclaration(block, "output"); err != nil {
			return err
		}
	}
	return state.extractFunctionCalls(file)
}

func hasUnsupportedTopLevelBlocks(body hcl.Body) bool {
	syntaxBody, ok := body.(*hclsyntax.Body)
	if !ok {
		return false
	}
	for _, block := range syntaxBody.Blocks {
		switch block.Type {
		case "terraform", "provider", "resource", "data", "ephemeral", "action", "module", "variable", "output":
			continue
		default:
			return true
		}
	}
	return false
}

func (state *extraction) extractFunctionCalls(file *hcl.File) error {
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		state.coverage[CapabilityAdditionalKinds] = CoveragePartial
		state.reasons = append(state.reasons, "function-call extraction from Terraform JSON is unavailable")
		return nil
	}
	if state.coverage[CapabilityAdditionalKinds] == CoverageUnavailable {
		state.coverage[CapabilityAdditionalKinds] = CoverageComplete
	}
	diagnostics := hclsyntax.VisitAll(body, func(node hclsyntax.Node) hcl.Diagnostics {
		call, isCall := node.(*hclsyntax.FunctionCallExpr)
		if !isCall {
			return nil
		}
		name, providerAddress, supported := staticFunctionIdentity(call.Name)
		if !supported {
			state.coverage[CapabilityAdditionalKinds] = CoveragePartial
			state.reasons = append(state.reasons, "a function call has an unsupported or unresolved provider identity")
			return nil
		}
		state.addSymbol(FactSymbol{
			Kind: "function", Name: name, ProviderAddress: providerAddress,
			UnitPath: state.unitPath, Location: state.location(call.NameRange), Capability: CapabilityAdditionalKinds,
		})
		return nil
	})
	if diagnostics.HasErrors() {
		state.coverage[CapabilityAdditionalKinds] = CoveragePartial
		state.reasons = append(state.reasons, "function-call syntax could not be fully traversed")
	}
	return nil
}

func staticFunctionIdentity(callName string) (string, string, bool) {
	if !strings.HasPrefix(callName, "provider::") {
		return callName, "", safeLabel(callName)
	}
	parts := strings.Split(callName, "::")
	var address, name string
	switch len(parts) {
	case 4:
		address = "registry.terraform.io/" + parts[1] + "/" + parts[2]
		name = parts[3]
	case 5:
		address = parts[1] + "/" + parts[2] + "/" + parts[3]
		name = parts[4]
	default:
		return "", "", false
	}
	return name, address, safeLabel(name) && addressPattern.MatchString(address)
}

func (state *extraction) collectRequirements(file *hcl.File) error {
	content, _, diagnostics := file.Body.PartialContent(&hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "terraform"}}})
	if diagnostics.HasErrors() {
		return fmt.Errorf("decode terraform block: %s", diagnostics.Error())
	}
	for _, block := range content.Blocks {
		if err := state.extractRequirements(block); err != nil {
			return err
		}
		if err := state.extractTerraformSettings(block); err != nil {
			return err
		}
	}
	return nil
}

func (state *extraction) extractTerraformSettings(terraformBlock *hcl.Block) error {
	content, _, diagnostics := terraformBlock.Body.PartialContent(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: "required_version"}},
		Blocks: []hcl.BlockHeaderSchema{
			{Type: "backend", LabelNames: []string{"type"}},
			{Type: "cloud"},
		},
	})
	if diagnostics.HasErrors() {
		return fmt.Errorf("decode Terraform settings: %s", diagnostics.Error())
	}
	if attribute := content.Attributes["required_version"]; attribute != nil {
		state.addSymbol(FactSymbol{
			Kind: "terraform_setting", Name: "terraform.required_version", Setting: "required_version",
			UnitPath: state.unitPath, Location: state.location(attribute.Range), Capability: CapabilityTerraformSettings,
		})
	}
	for _, block := range content.Blocks {
		symbol := FactSymbol{
			Kind: "terraform_setting", Name: "terraform." + block.Type, Setting: block.Type,
			UnitPath: state.unitPath, Location: state.location(block.DefRange), Capability: CapabilityTerraformSettings,
		}
		if block.Type == "backend" {
			if len(block.Labels) != 1 || !safeLabel(block.Labels[0]) {
				return fmt.Errorf("backend block at %s has invalid labels", block.DefRange.String())
			}
			symbol.SettingType = block.Labels[0]
		}
		state.addSymbol(symbol)
	}
	return nil
}

func (state *extraction) extractRequirements(terraformBlock *hcl.Block) error {
	content, _, diagnostics := terraformBlock.Body.PartialContent(&hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "required_providers"}}})
	if diagnostics.HasErrors() {
		return fmt.Errorf("decode required_providers: %s", diagnostics.Error())
	}
	for _, block := range content.Blocks {
		attributes, diagnostics := block.Body.JustAttributes()
		if diagnostics.HasErrors() {
			state.coverage[CapabilityProviderRequirements] = CoveragePartial
			state.reasons = append(state.reasons, "required provider entries include unsupported nested syntax")
			continue
		}
		for localName, attribute := range attributes {
			provider, ok := parseRequirement(attribute.Expr)
			if !ok {
				state.coverage[CapabilityProviderRequirements] = CoveragePartial
				state.reasons = append(state.reasons, "provider source or version requirement is dynamic or unsupported")
				continue
			}
			state.providers[localName] = provider
			state.addSymbol(FactSymbol{
				Kind: "provider", Name: localName, ProviderAddress: provider.address,
				LocalProviderName: localName, ProviderVersionConstraint: provider.version,
				UnitPath: state.unitPath,
				Location: state.location(attribute.Range), Capability: CapabilityProviderRequirements,
			})
			if provider.versionExists && !provider.versionKnown {
				state.coverage[CapabilityProviderRequirements] = CoveragePartial
				state.reasons = append(state.reasons, "provider version constraint is dynamic or unsupported")
			}
		}
	}
	return nil
}

func (state *extraction) extractProvider(block *hcl.Block) error {
	if len(block.Labels) != 1 {
		return fmt.Errorf("provider block at %s has invalid labels", block.DefRange.String())
	}
	attributes, _, diagnostics := block.Body.PartialContent(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "alias"}}})
	if diagnostics.HasErrors() {
		return fmt.Errorf("decode provider block: %s", diagnostics.Error())
	}
	localName := block.Labels[0]
	alias := ""
	if attribute := attributes.Attributes["alias"]; attribute != nil {
		value, ok := staticString(attribute.Expr)
		if !ok || !safeLabel(value) {
			state.coverage[CapabilityProviderBindings] = CoveragePartial
			state.reasons = append(state.reasons, "provider alias is dynamic or unsafe")
		} else {
			alias = value
		}
	}
	provider, exists := state.providers[localName]
	if !exists {
		provider = requirement{address: defaultProviderAddress(localName)}
	}
	state.addSymbol(FactSymbol{
		Kind: "provider", Name: localName, ProviderAddress: provider.address,
		LocalProviderName: localName, Alias: alias, UnitPath: state.unitPath,
		Location: state.location(block.DefRange), Capability: CapabilityProviderBindings,
	})
	return nil
}

func (state *extraction) extractInterfaceDeclaration(block *hcl.Block, kind string) error {
	if len(block.Labels) != 1 || !safeLabel(block.Labels[0]) {
		return fmt.Errorf("%s block at %s has invalid labels", kind, block.DefRange.String())
	}
	content, _, diagnostics := block.Body.PartialContent(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "description"}}})
	if diagnostics.HasErrors() {
		return fmt.Errorf("decode %s description: %s", kind, diagnostics.Error())
	}
	descriptionPresent := false
	if attribute := content.Attributes["description"]; attribute != nil {
		value, isStatic := staticString(attribute.Expr)
		if !isStatic {
			state.coverage[CapabilityInterfaceMetadata] = CoveragePartial
			state.reasons = append(state.reasons, "variable or output description is dynamic or unsupported")
			state.addSymbol(FactSymbol{
				Kind: kind, Name: block.Labels[0], UnitPath: state.unitPath,
				Location: state.location(block.DefRange), Capability: CapabilityInterfaceMetadata,
			})
			return nil
		}
		descriptionPresent = value != ""
	}
	state.addSymbol(FactSymbol{
		Kind: kind, Name: block.Labels[0], DescriptionPresent: &descriptionPresent,
		UnitPath: state.unitPath, Location: state.location(block.DefRange), Capability: CapabilityInterfaceMetadata,
	})
	return nil
}

func (state *extraction) moduleSource(attribute *hcl.Attribute) string {
	declared, static := staticStringAttribute(attribute)
	source, safe := normalizeModuleSource(declared, state.unitPath)
	if !static || !safe {
		state.coverage[CapabilityChildModules] = CoveragePartial
		state.reasons = append(state.reasons, "module source is dynamic, unsafe, or unsupported")
		return ""
	}
	return source
}

func (state *extraction) moduleVersion(attribute *hcl.Attribute) string {
	version, valid := staticStringAttribute(attribute)
	if valid {
		version, valid = normalizeVersionConstraint(version)
	}
	if !valid && attribute != nil {
		state.coverage[CapabilityChildModules] = CoveragePartial
		state.reasons = append(state.reasons, "module version is dynamic or unsupported")
		return ""
	}
	return version
}

func (state *extraction) applyModuleProviderMapping(symbol *FactSymbol, attribute *hcl.Attribute) {
	mappings, valid := providerMappings(attribute)
	if !valid {
		state.coverage[CapabilityProviderBindings] = CoveragePartial
		state.coverage[CapabilityChildModules] = CoveragePartial
		state.reasons = append(state.reasons, "module provider mappings are dynamic or unsupported")
		return
	}
	symbol.ProviderMappings = make([]ProviderMapping, 0, len(mappings))
	for _, mapping := range mappings {
		address := ""
		if provider, exists := state.providers[mapping.parent]; exists {
			address = provider.address
		} else if safeLabel(mapping.parent) {
			address = defaultProviderAddress(mapping.parent)
		}
		if address == "" {
			state.coverage[CapabilityProviderBindings] = CoveragePartial
			state.coverage[CapabilityChildModules] = CoveragePartial
			state.reasons = append(state.reasons, "module provider mapping has no safe provider address")
			continue
		}
		providerMapping := ProviderMapping{
			ChildLocalName: mapping.local, ParentLocalName: mapping.parent,
			ParentAlias: mapping.alias, ProviderAddress: address,
		}
		symbol.ProviderMappings = append(symbol.ProviderMappings, providerMapping)
	}
}

func (state *extraction) extractResource(block *hcl.Block, kind, capability string) error {
	if len(block.Labels) != 2 || !safeLabel(block.Labels[0]) || !safeLabel(block.Labels[1]) {
		return fmt.Errorf("%s block at %s has invalid labels", kind, block.DefRange.String())
	}
	attributes, _, diagnostics := block.Body.PartialContent(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "provider"}}})
	if diagnostics.HasErrors() {
		return fmt.Errorf("decode %s provider binding: %s", kind, diagnostics.Error())
	}
	localName, alias, ok := providerBinding(attributes.Attributes["provider"], block.Labels[0])
	if !ok {
		state.coverage[CapabilityProviderBindings] = CoveragePartial
		state.reasons = append(state.reasons, "resource or data provider binding is dynamic or cannot be mapped statically")
	}
	address := ""
	if requirement, exists := state.providers[localName]; exists {
		address = requirement.address
	} else if safeLabel(localName) {
		address = defaultProviderAddress(localName)
		if localName != strings.SplitN(block.Labels[0], "_", 2)[0] {
			state.coverage[CapabilityProviderBindings] = CoveragePartial
			state.reasons = append(state.reasons, "provider source is not declared for a non-default local name")
		}
	}
	if !ok {
		localName, alias, address = "", "", ""
	}
	state.addSymbol(FactSymbol{
		Kind: kind, Name: block.Labels[0] + "." + block.Labels[1],
		ProviderAddress: address, LocalProviderName: localName, Alias: alias,
		UnitPath: state.unitPath, Location: state.location(block.DefRange), Capability: capability,
	})
	return nil
}

func (state *extraction) extractModule(block *hcl.Block) error {
	if len(block.Labels) != 1 || !safeLabel(block.Labels[0]) {
		return fmt.Errorf("module block at %s has invalid labels", block.DefRange.String())
	}
	attributes, _, diagnostics := block.Body.PartialContent(&hcl.BodySchema{Attributes: []hcl.AttributeSchema{{Name: "source"}, {Name: "version"}, {Name: "providers"}}})
	if diagnostics.HasErrors() {
		return fmt.Errorf("decode module call: %s", diagnostics.Error())
	}
	symbol := FactSymbol{
		Kind: "module_call", Name: block.Labels[0],
		ModuleSource:  state.moduleSource(attributes.Attributes["source"]),
		ModuleVersion: state.moduleVersion(attributes.Attributes["version"]),
		UnitPath:      state.unitPath,
		Location:      state.location(block.DefRange), Capability: CapabilityChildModules,
	}
	if providerAttribute := attributes.Attributes["providers"]; providerAttribute != nil {
		state.applyModuleProviderMapping(&symbol, providerAttribute)
	}
	state.addSymbol(symbol)
	return nil
}

type providerMap struct {
	local  string
	parent string
	alias  string
}

func providerMappings(attribute *hcl.Attribute) ([]providerMap, bool) {
	if attribute == nil {
		return nil, true
	}
	pairs, diagnostics := hcl.ExprMap(attribute.Expr)
	if diagnostics.HasErrors() {
		return nil, false
	}
	result := make([]providerMap, 0, len(pairs))
	for _, pair := range pairs {
		local := staticObjectKey(pair.Key)
		parent, alias, ok := providerTraversal(pair.Value)
		if !safeLabel(local) || !ok {
			return nil, false
		}
		result = append(result, providerMap{local: local, parent: parent, alias: alias})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].local < result[j].local })
	return result, true
}

func providerBinding(attribute *hcl.Attribute, resourceType string) (string, string, bool) {
	if attribute != nil {
		return providerTraversal(attribute.Expr)
	}
	parts := strings.SplitN(resourceType, "_", 2)
	if len(parts) == 0 || !safeLabel(parts[0]) {
		return "", "", false
	}
	return parts[0], "", true
}

func providerTraversal(expression hcl.Expression) (string, string, bool) {
	traversal, diagnostics := hcl.AbsTraversalForExpr(expression)
	if hcljson.IsJSONExpression(expression) {
		value, valueDiagnostics := expression.Value(nil)
		if valueDiagnostics.HasErrors() || !value.IsKnown() || value.IsNull() || !value.Type().Equals(cty.String) {
			return "", "", false
		}
		literal := value.AsString()
		if !strings.HasPrefix(literal, "${") || !strings.HasSuffix(literal, "}") {
			return "", "", false
		}
		traversal, diagnostics = hclsyntax.ParseTraversalAbs([]byte(strings.TrimSpace(literal[2:len(literal)-1])), expression.Range().Filename, hcl.InitialPos)
	}
	if diagnostics.HasErrors() || len(traversal) < 1 {
		return "", "", false
	}
	root, ok := traversal[0].(hcl.TraverseRoot)
	if !ok || !safeLabel(root.Name) {
		return "", "", false
	}
	alias := ""
	if len(traversal) > 1 {
		attribute, ok := traversal[1].(hcl.TraverseAttr)
		if !ok || !safeLabel(attribute.Name) || len(traversal) != 2 {
			return "", "", false
		}
		alias = attribute.Name
	}
	return root.Name, alias, true
}

func parseRequirement(expression hcl.Expression) (requirement, bool) {
	pairs, diagnostics := hcl.ExprMap(expression)
	if diagnostics.HasErrors() {
		return requirement{}, false
	}
	parsed := requirement{versionKnown: true}
	foundSource := false
	for _, pair := range pairs {
		key := staticObjectKey(pair.Key)
		switch key {
		case "source":
			value, ok := staticString(pair.Value)
			if !ok {
				return requirement{}, false
			}
			parsed.address = normalizeProviderAddress(value)
			foundSource = parsed.address != ""
		case "version":
			parsed.versionExists = true
			parsed.version, parsed.versionKnown = staticString(pair.Value)
			if parsed.versionKnown {
				parsed.version, parsed.versionKnown = normalizeVersionConstraint(parsed.version)
			}
		}
	}
	if !foundSource {
		return requirement{}, false
	}
	return parsed, true
}

func staticObjectKey(expression hcl.Expression) string {
	if keyword := hcl.ExprAsKeyword(expression); keyword != "" {
		return keyword
	}
	value, ok := staticString(expression)
	if ok {
		return value
	}
	return ""
}

func staticStringAttribute(attribute *hcl.Attribute) (string, bool) {
	if attribute == nil {
		return "", false
	}
	return staticString(attribute.Expr)
}

func staticString(expression hcl.Expression) (string, bool) {
	if syntaxExpression, ok := expression.(hclsyntax.Expression); ok {
		template, ok := syntaxExpression.(*hclsyntax.TemplateExpr)
		if !ok || !template.IsStringLiteral() {
			return "", false
		}
		value, diagnostics := template.Value(nil)
		if diagnostics.HasErrors() || !value.IsKnown() || value.IsNull() || !value.Type().Equals(cty.String) {
			return "", false
		}
		return value.AsString(), true
	}
	if hcljson.IsJSONExpression(expression) {
		value, diagnostics := expression.Value(nil)
		if diagnostics.HasErrors() || !value.IsKnown() || value.IsNull() || !value.Type().Equals(cty.String) {
			return "", false
		}
		literal := value.AsString()
		if strings.Contains(literal, "${") || strings.Contains(literal, "%{") {
			return "", false
		}
		return literal, true
	}
	return "", false
}

func normalizeProviderAddress(source string) string {
	if strings.Count(source, "/") == 1 {
		source = "registry.terraform.io/" + source
	}
	if !addressPattern.MatchString(source) {
		return ""
	}
	return source
}

func normalizeModuleSource(source, unitPath string) (string, bool) {
	if !safeModuleSourceText(source) {
		return "", false
	}
	if isLocalModuleSource(source) {
		return normalizeLocalModuleSource(source, unitPath)
	}
	return normalizeRemoteModuleSource(source)
}

func safeModuleSourceText(source string) bool {
	return source != "" && !strings.ContainsAny(source, "\\\x00") && !strings.HasPrefix(source, "/") && !strings.Contains(source, ":")
}

func isLocalModuleSource(source string) bool {
	return source == "." || source == ".." || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../")
}

func normalizeLocalModuleSource(source, unitPath string) (string, bool) {
	if unitPath == "" {
		unitPath = "."
	}
	resolved := pathpkg.Clean(pathpkg.Join(unitPath, source))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return "", false
	}
	cleanSource := pathpkg.Clean(source)
	if strings.HasPrefix(source, "./") && cleanSource != "." {
		cleanSource = "./" + cleanSource
	} else if strings.HasPrefix(source, "../") && cleanSource == ".." {
		cleanSource = "../"
	}
	return cleanSource, true
}

func normalizeRemoteModuleSource(source string) (string, bool) {
	parts := strings.Split(source, "/")
	if len(parts) != 3 && len(parts) != 4 {
		return "", false
	}
	for _, part := range parts {
		if !safeLabel(part) {
			return "", false
		}
	}
	return source, true
}

func normalizeVersionConstraint(value string) (string, bool) {
	if strings.TrimSpace(value) != value || !versionPattern.MatchString(value) {
		return "", false
	}
	return value, true
}

func defaultProviderAddress(localName string) string {
	if !safeLabel(localName) {
		return ""
	}
	return "registry.terraform.io/hashicorp/" + localName
}

func safeLabel(value string) bool {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "/\\\x00") || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.') {
			return false
		}
	}
	return true
}

func (state *extraction) addSymbol(symbol FactSymbol) {
	state.unit.Symbols = append(state.unit.Symbols, symbol)
}

func (state *extraction) location(sourceRange hcl.Range) Location {
	return Location{File: state.filePath, Line: sourceRange.Start.Line}
}

func symbolLess(left, right FactSymbol) bool {
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.Name != right.Name {
		return left.Name < right.Name
	}
	if left.Capability != right.Capability {
		return left.Capability < right.Capability
	}
	if left.LocalProviderName != right.LocalProviderName {
		return left.LocalProviderName < right.LocalProviderName
	}
	return left.Alias < right.Alias
}

func rejectSymlinkComponents(root, absolute string) error {
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes the source root")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect source path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed")
		}
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return fmt.Errorf("inspect source file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("source path must be a regular file")
	}
	return nil
}

type factsContent struct {
	SchemaVersion int      `json:"schema_version"`
	RecordType    string   `json:"record_type"`
	ModuleID      string   `json:"module_id"`
	SourceSHA     string   `json:"source_sha"`
	Units         []Unit   `json:"units"`
	Coverage      Coverage `json:"coverage"`
}

func factIdentity(content factsContent) (string, string, error) {
	canonical, err := identity.CanonicalJSON("facts-content", FactsSchemaVersion, content)
	if err != nil {
		return "", "", fmt.Errorf("canonicalize facts content: %w", err)
	}
	digestBytes := sha256.Sum256(canonical)
	digest := hex.EncodeToString(digestBytes[:])
	id, err := identity.ID("facts", FactsSchemaVersion, content)
	if err != nil {
		return "", "", fmt.Errorf("identify facts: %w", err)
	}
	return digest, id, nil
}

// Validate checks the emitted facts against the strict v3 facts schema before
// the record can be handed to another factory component.
func (facts Facts) Validate() error {
	if err := validateFactsHeader(facts); err != nil {
		return err
	}
	for _, unit := range facts.Units {
		if err := validateFactUnit(unit); err != nil {
			return err
		}
		for _, symbol := range unit.Symbols {
			if err := validateFactSymbol(symbol); err != nil {
				return err
			}
		}
	}
	if err := validateCoverage(facts.Coverage); err != nil {
		return err
	}
	return validateFactsIdentity(facts)
}

func validateFactsHeader(facts Facts) error {
	if facts.SchemaVersion != FactsSchemaVersion || facts.RecordType != "facts" {
		return fmt.Errorf("facts schema identity is invalid")
	}
	if !moduleIDPattern.MatchString(facts.ModuleID) || !sha1Pattern.MatchString(facts.SourceSHA) {
		return fmt.Errorf("facts source identity is invalid")
	}
	if !sha256Pattern.MatchString(facts.ContentDigest) || !strings.HasPrefix(facts.FactsID, "facts-") || len(facts.FactsID) != len("facts-")+64 {
		return fmt.Errorf("facts content identity is invalid")
	}
	if len(facts.Units) == 0 {
		return fmt.Errorf("facts units are missing")
	}
	return nil
}

func validateFactUnit(unit Unit) error {
	if err := identity.ValidateRelativePath(unit.Path, true); err != nil {
		return fmt.Errorf("invalid unit path: %w", err)
	}
	if unit.Role != RoleComponent && unit.Role != RoleChildComponent && unit.Role != RoleExample {
		return fmt.Errorf("invalid unit role %q", unit.Role)
	}
	if unit.Owner != "" {
		if err := identity.ValidateRelativePath(unit.Owner, true); err != nil {
			return fmt.Errorf("invalid unit owner: %w", err)
		}
	}
	return nil
}

func validateFactSymbol(symbol FactSymbol) error {
	if !validSymbolKind(symbol.Kind) || !symbolNamePattern.MatchString(symbol.Name) || symbol.Capability == "" || symbol.Location.Line < 1 {
		return fmt.Errorf("fact symbol identity, capability, or location is invalid")
	}
	if err := validateSymbolProvider(symbol); err != nil {
		return err
	}
	if err := validateProviderLockSelection(symbol); err != nil {
		return err
	}
	if err := validateSymbolModule(symbol); err != nil {
		return err
	}
	return validateSymbolLocation(symbol)
}

func validateSymbolProvider(symbol FactSymbol) error {
	if err := validateProviderReference(symbol); err != nil {
		return err
	}
	if err := validateProviderVersion(symbol.ProviderVersionConstraint); err != nil {
		return err
	}
	return validateProviderMappings(symbol)
}

func validateProviderReference(symbol FactSymbol) error {
	if symbol.ProviderAddress != "" && !addressPattern.MatchString(symbol.ProviderAddress) {
		return fmt.Errorf("fact symbol provider address is invalid")
	}
	if (symbol.LocalProviderName != "" && !safeLabel(symbol.LocalProviderName)) || (symbol.Alias != "" && !safeLabel(symbol.Alias)) {
		return fmt.Errorf("fact symbol provider binding is invalid")
	}
	return nil
}

func validateProviderVersion(version string) error {
	if version == "" {
		return nil
	}
	constraint, valid := normalizeVersionConstraint(version)
	if !valid || constraint != version {
		return fmt.Errorf("fact symbol provider version constraint is invalid")
	}
	return nil
}

func validateProviderMappings(symbol FactSymbol) error {
	if symbol.ProviderMappings != nil && symbol.Kind != "module_call" {
		return fmt.Errorf("provider mappings are only valid on module-call symbols")
	}
	seenChildProviders := make(map[string]struct{}, len(symbol.ProviderMappings))
	for _, mapping := range symbol.ProviderMappings {
		if !safeLabel(mapping.ChildLocalName) || !safeLabel(mapping.ParentLocalName) || !addressPattern.MatchString(mapping.ProviderAddress) {
			return fmt.Errorf("fact symbol child-provider mapping is invalid")
		}
		if mapping.ParentAlias != "" && !safeLabel(mapping.ParentAlias) {
			return fmt.Errorf("fact symbol child-provider alias is invalid")
		}
		if _, exists := seenChildProviders[mapping.ChildLocalName]; exists {
			return fmt.Errorf("fact symbol has duplicate child-provider mappings")
		}
		seenChildProviders[mapping.ChildLocalName] = struct{}{}
	}
	return nil
}

func validateSymbolModule(symbol FactSymbol) error {
	if symbol.ModuleSource != "" {
		normalized, valid := normalizeModuleSource(symbol.ModuleSource, symbol.UnitPath)
		if !valid || normalized != symbol.ModuleSource {
			return fmt.Errorf("fact symbol module source is unsafe")
		}
	}
	if symbol.ModuleVersion != "" {
		normalized, valid := normalizeVersionConstraint(symbol.ModuleVersion)
		if !valid || normalized != symbol.ModuleVersion {
			return fmt.Errorf("fact symbol module version is unsafe")
		}
	}
	return nil
}

func validateSymbolLocation(symbol FactSymbol) error {
	if err := identity.ValidateRelativePath(symbol.Location.File, false); err != nil {
		return fmt.Errorf("invalid symbol location: %w", err)
	}
	if symbol.UnitPath != "" {
		if err := identity.ValidateRelativePath(symbol.UnitPath, true); err != nil {
			return fmt.Errorf("invalid symbol unit path: %w", err)
		}
	}
	return nil
}

func validSymbolKind(kind string) bool {
	switch kind {
	case "resource", "data", "ephemeral", "function", "action", "provider", "provider_lock", "module_call", "variable", "output", "terraform_setting":
		return true
	default:
		return false
	}
}

func validateInterfaceMetadata(symbol FactSymbol) error {
	if symbol.DescriptionPresent == nil {
		return nil
	}
	if symbol.Kind != "variable" && symbol.Kind != "output" {
		return fmt.Errorf("description presence is only valid on variable/output symbols")
	}
	return nil
}

func validateTerraformSetting(symbol FactSymbol) error {
	if symbol.Kind != "terraform_setting" {
		if symbol.Setting != "" || symbol.SettingType != "" {
			return fmt.Errorf("Terraform setting fields are only valid on terraform_setting symbols")
		}
		return nil
	}
	switch symbol.Setting {
	case "required_version", "cloud":
		if symbol.SettingType != "" {
			return fmt.Errorf("Terraform setting %s cannot include a setting type", symbol.Setting)
		}
	case "backend":
		if !safeLabel(symbol.SettingType) {
			return fmt.Errorf("Terraform backend type is invalid")
		}
	default:
		return fmt.Errorf("Terraform setting %q is unsupported", symbol.Setting)
	}
	return nil
}

func validateCoverage(coverage Coverage) error {
	if len(coverage.Capabilities) == 0 {
		return fmt.Errorf("facts coverage is missing")
	}
	if _, err := time.Parse(time.RFC3339Nano, coverage.ObservedAt); err != nil {
		return fmt.Errorf("facts coverage observation time is invalid")
	}
	complete := true
	for capability, status := range coverage.Capabilities {
		if capability == "" || status != CoverageComplete && status != CoveragePartial && status != CoverageUnavailable && status != CoverageUnknown {
			return fmt.Errorf("invalid coverage entry %q=%q", capability, status)
		}
		complete = complete && status == CoverageComplete
	}
	if complete != coverage.Complete {
		return fmt.Errorf("aggregate coverage completeness does not match capability statuses")
	}
	return nil
}

func validateFactsIdentity(facts Facts) error {
	content := factsContent{
		SchemaVersion: facts.SchemaVersion, RecordType: facts.RecordType,
		ModuleID: facts.ModuleID, SourceSHA: facts.SourceSHA,
		Units: facts.Units, Coverage: facts.Coverage,
	}
	digest, id, err := factIdentity(content)
	if err != nil {
		return err
	}
	if digest != facts.ContentDigest || id != facts.FactsID {
		return fmt.Errorf("facts content identity does not match the record")
	}
	return nil
}
