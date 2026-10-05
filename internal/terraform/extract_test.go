package terraform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExtractFactsFromNativeHCLAndTerraformJSON(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf", `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

provider "azurerm" {
  alias = "west"
}

resource "azurerm_resource_group" "main" {
  provider = azurerm.west
  name     = var.group_name
  secret   = "DO_NOT_LEAK"
}

data "azurerm_client_config" "current" {}

module "child" {
  source    = "Azure/avm-res-network-virtualnetwork/azurerm"
  version   = "1.2.3"
  providers = { azurerm = azurerm.west }
}

module "dynamic" {
  source = var.module_source
}
`)
	writeFixture(t, root, "extra.tf.json", `{
  "resource": {
    "azurerm_storage_account": {
      "store": {
        "provider": "${azurerm.west}",
        "name": "example"
      }
    }
  }
}`)

	input := Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("a", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []UnitInput{{Path: ".", Role: RoleComponent, Files: []string{"main.tf", "extra.tf.json"}}},
	}
	facts, err := ExtractFacts(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = facts.Validate(); err != nil {
		t.Fatal(err)
	}
	if facts.FactsID == "" || facts.ContentDigest == "" || len(facts.Units) != 1 || facts.Units[0].Path != "." {
		t.Fatalf("facts identity/units = %#v", facts)
	}
	if facts.Coverage.Complete {
		t.Fatal("dynamic module source was treated as complete static coverage")
	}
	if facts.Coverage.Capabilities[CapabilityChildModules] != CoveragePartial {
		t.Fatalf("module coverage = %#v", facts.Coverage)
	}
	assertFactSymbol(t, facts, FactSymbol{Kind: "resource", Name: "azurerm_resource_group.main", ProviderAddress: "registry.terraform.io/hashicorp/azurerm", LocalProviderName: "azurerm", Alias: "west", Location: Location{File: "main.tf", Line: 14}})
	assertFactSymbol(t, facts, FactSymbol{Kind: "data", Name: "azurerm_client_config.current", ProviderAddress: "registry.terraform.io/hashicorp/azurerm", LocalProviderName: "azurerm", Location: Location{File: "main.tf", Line: 20}})
	assertFactSymbol(t, facts, FactSymbol{Kind: "module_call", Name: "child", ProviderMappings: []ProviderMapping{{ChildLocalName: "azurerm", ParentLocalName: "azurerm", ParentAlias: "west", ProviderAddress: "registry.terraform.io/hashicorp/azurerm"}}, Location: Location{File: "main.tf", Line: 22}})
	assertFactSymbol(t, facts, FactSymbol{Kind: "resource", Name: "azurerm_storage_account.store", ProviderAddress: "registry.terraform.io/hashicorp/azurerm", LocalProviderName: "azurerm", Alias: "west", Location: Location{File: "extra.tf.json", Line: 4}})
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "DO_NOT_LEAK") || strings.Contains(string(encoded), "var.group_name") || strings.Contains(string(encoded), "var.module_source") {
		t.Fatal("facts record leaked source expressions or arbitrary source values")
	}
	second, err := ExtractFacts(input)
	if err != nil {
		t.Fatal(err)
	}
	if second.FactsID != facts.FactsID || second.ContentDigest != facts.ContentDigest {
		t.Fatalf("facts identity is not deterministic: %s/%s vs %s/%s", facts.FactsID, facts.ContentDigest, second.FactsID, second.ContentDigest)
	}
	corrupted := facts
	corrupted.Coverage.Capabilities = make(map[string]string, len(facts.Coverage.Capabilities))
	for capability, status := range facts.Coverage.Capabilities {
		corrupted.Coverage.Capabilities[capability] = status
	}
	corrupted.Coverage.Capabilities[CapabilityResourceUsage] = CoveragePartial
	if err := corrupted.Validate(); err == nil {
		t.Fatal("facts with modified coverage retained a valid content identity")
	}
}

func TestExtractFactsCollectsEphemeralActionsAndNestedFunctions(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf", `ephemeral "azurerm_key_vault_secret" "secret" {
  name = lower(upper("SAFE_VALUE"))
}

action "azurerm_run_command" "execute" {
  input = provider::hashicorp::azurerm::command_id("SAFE_VALUE")
}
`)
	facts, err := ExtractFacts(Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("9", 40),
		ObservedAt: time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC),
		Units:      []UnitInput{{Path: ".", Role: RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = facts.Validate(); err != nil {
		t.Fatal(err)
	}
	assertFactSymbol(t, facts, FactSymbol{
		Kind: "ephemeral", Name: "azurerm_key_vault_secret.secret",
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", LocalProviderName: "azurerm",
		UnitPath: ".", Location: Location{File: "main.tf", Line: 1}, Capability: CapabilityAdditionalKinds,
	})
	assertFactSymbol(t, facts, FactSymbol{
		Kind: "action", Name: "azurerm_run_command.execute",
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", LocalProviderName: "azurerm",
		UnitPath: ".", Location: Location{File: "main.tf", Line: 5}, Capability: CapabilityAdditionalKinds,
	})
	assertFactSymbol(t, facts, FactSymbol{
		Kind: "function", Name: "upper", UnitPath: ".", Location: Location{File: "main.tf", Line: 2}, Capability: CapabilityAdditionalKinds,
	})
	assertFactSymbol(t, facts, FactSymbol{
		Kind: "function", Name: "lower", UnitPath: ".", Location: Location{File: "main.tf", Line: 2}, Capability: CapabilityAdditionalKinds,
	})
	assertFactSymbol(t, facts, FactSymbol{
		Kind: "function", Name: "command_id", ProviderAddress: "registry.terraform.io/hashicorp/azurerm",
		UnitPath: ".", Location: Location{File: "main.tf", Line: 6}, Capability: CapabilityAdditionalKinds,
	})
	if facts.Coverage.Capabilities[CapabilityAdditionalKinds] != CoverageComplete {
		t.Fatalf("additional-kind coverage = %#v, want complete for native HCL", facts.Coverage)
	}
}

func TestExtractFactsCollectsAdditionalBlocksFromTerraformJSONAsPartial(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf.json", `{
  "ephemeral": {
    "azurerm_key_vault_secret": {
      "secret": {"name": "SAFE_VALUE"}
    }
  },
  "action": {
    "azurerm_run_command": {
      "execute": {"input": "${lower(\"SAFE_VALUE\")}"}
    }
  }
}`)
	facts, err := ExtractFacts(Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("8", 40),
		ObservedAt: time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC),
		Units:      []UnitInput{{Path: ".", Role: RoleComponent, Files: []string{"main.tf.json"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFactSymbol(t, facts, FactSymbol{
		Kind: "ephemeral", Name: "azurerm_key_vault_secret.secret",
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", LocalProviderName: "azurerm",
		UnitPath: ".", Location: Location{File: "main.tf.json", Line: 4}, Capability: CapabilityAdditionalKinds,
	})
	assertFactSymbol(t, facts, FactSymbol{
		Kind: "action", Name: "azurerm_run_command.execute",
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", LocalProviderName: "azurerm",
		UnitPath: ".", Location: Location{File: "main.tf.json", Line: 9}, Capability: CapabilityAdditionalKinds,
	})
	if facts.Coverage.Capabilities[CapabilityAdditionalKinds] != CoveragePartial {
		t.Fatalf("JSON additional-kind coverage = %#v, want partial function-call coverage", facts.Coverage)
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SAFE_VALUE") || strings.Contains(string(encoded), "lower") {
		t.Fatal("Terraform JSON facts leaked source values or an unparsed function call")
	}
}

func TestExtractFactsMarksUnknownNativeBlockCoveragePartial(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf", `custom_declaration "unknown" {
  setting = "SAFE_VALUE"
}`)
	facts, err := ExtractFacts(Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("7", 40),
		ObservedAt: time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC),
		Units:      []UnitInput{{Path: ".", Role: RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if facts.Coverage.Capabilities[CapabilityAdditionalKinds] != CoveragePartial {
		t.Fatalf("unknown-block coverage = %#v, want partial", facts.Coverage)
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SAFE_VALUE") {
		t.Fatal("unknown source block value was serialized")
	}
}

func TestExtractFactsRecordsExactProviderLockSelections(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf", `resource "azurerm_resource_group" "main" { name = "SAFE_VALUE" }`)
	writeFixture(t, root, ".terraform.lock.hcl", `provider "registry.terraform.io/hashicorp/azurerm" {
  version     = "4.2.0"
  constraints = "~> 4.0"
  hashes      = ["h1:DO_NOT_SERIALIZE"]
}`)
	units, err := DiscoverUnits(root)
	if err != nil {
		t.Fatal(err)
	}
	lockFiles, err := DiscoverLockFiles(root, units)
	if err != nil {
		t.Fatal(err)
	}
	if len(lockFiles) != 1 || lockFiles[0].Path != ".terraform.lock.hcl" || lockFiles[0].UnitPath != "." {
		t.Fatalf("discovered lockfiles = %#v", lockFiles)
	}
	facts, err := ExtractFacts(Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("6", 40),
		ObservedAt: time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC), Units: units, LockFiles: lockFiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFactSymbol(t, facts, FactSymbol{
		Kind: "provider_lock", Name: "registry.terraform.io_hashicorp_azurerm",
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", ProviderVersionSelection: "4.2.0",
		UnitPath: ".", Location: Location{File: ".terraform.lock.hcl", Line: 1}, Capability: CapabilityProviderLockSelections,
	})
	if facts.Coverage.Capabilities[CapabilityProviderLockSelections] != CoverageComplete {
		t.Fatalf("lock selection coverage = %#v, want complete", facts.Coverage)
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "DO_NOT_SERIALIZE") || strings.Contains(string(encoded), "~> 4.0") {
		t.Fatal("provider lock hashes or constraints were incorrectly serialized as selections")
	}
}

func TestExtractFactsRejectsDynamicOrNoncanonicalProviderLockVersions(t *testing.T) {
	tests := map[string]string{
		"dynamic":    `provider "registry.terraform.io/hashicorp/azurerm" { version = var.version }`,
		"v-prefixed": `provider "registry.terraform.io/hashicorp/azurerm" { version = "v4.2.0" }`,
	}
	for name, lockfile := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, "main.tf", `resource "azurerm_resource_group" "main" { name = "SAFE_VALUE" }`)
			writeFixture(t, root, ".terraform.lock.hcl", lockfile)
			units, err := DiscoverUnits(root)
			if err != nil {
				t.Fatal(err)
			}
			lockFiles, err := DiscoverLockFiles(root, units)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ExtractFacts(Input{
				RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("5", 40),
				ObservedAt: time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC), Units: units, LockFiles: lockFiles,
			}); err == nil {
				t.Fatal("dynamic or noncanonical lock version was accepted")
			}
		})
	}
}

func TestExtractFactsKeepsMissingProviderLockCoverageUnavailable(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf", `resource "azurerm_resource_group" "main" { name = "SAFE_VALUE" }`)
	units, err := DiscoverUnits(root)
	if err != nil {
		t.Fatal(err)
	}
	lockFiles, err := DiscoverLockFiles(root, units)
	if err != nil || len(lockFiles) != 0 {
		t.Fatalf("lockfiles = %#v, error = %v; want none", lockFiles, err)
	}
	facts, err := ExtractFacts(Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("4", 40),
		ObservedAt: time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC), Units: units,
	})
	if err != nil {
		t.Fatal(err)
	}
	if facts.Coverage.Capabilities[CapabilityProviderLockSelections] != CoverageUnavailable {
		t.Fatalf("missing lock coverage = %#v, want unavailable", facts.Coverage.Capabilities)
	}
}

func TestExtractFactsMarksProviderLockCoveragePartialWhenRequirementIsMissing(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf", `terraform {
  required_providers {
    azapi = { source = "Azure/azapi" }
  }
}`)
	writeFixture(t, root, ".terraform.lock.hcl", `provider "registry.terraform.io/hashicorp/azurerm" {
  version = "4.2.0"
}`)
	units, err := DiscoverUnits(root)
	if err != nil {
		t.Fatal(err)
	}
	lockFiles, err := DiscoverLockFiles(root, units)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := ExtractFacts(Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("3", 40),
		ObservedAt: time.Date(2026, 10, 4, 23, 0, 0, 0, time.UTC), Units: units, LockFiles: lockFiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	if facts.Coverage.Capabilities[CapabilityProviderLockSelections] != CoveragePartial {
		t.Fatalf("lock selection coverage = %#v, want partial for missing required provider", facts.Coverage.Capabilities)
	}
}

func TestExtractFactsResolvesProviderRequirementsAcrossFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf", `resource "custom_widget" "example" {
  provider = custom.region
}`)
	writeFixture(t, root, "versions.tf", `terraform {
  required_providers {
    custom = {
      source  = "example.com/acme/custom"
      version = "~> 2.0"
    }
  }
}

provider "custom" {
  alias = "region"
}`)
	facts, err := ExtractFacts(Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("b", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []UnitInput{{Path: ".", Role: RoleComponent, Files: []string{"main.tf", "versions.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFactSymbol(t, facts, FactSymbol{Kind: "resource", Name: "custom_widget.example", ProviderAddress: "example.com/acme/custom", LocalProviderName: "custom", Alias: "region", Location: Location{File: "main.tf", Line: 1}})
	assertFactSymbol(t, facts, FactSymbol{Kind: "provider", Name: "custom", ProviderAddress: "example.com/acme/custom", LocalProviderName: "custom", ProviderVersionConstraint: "~> 2.0", Capability: CapabilityProviderRequirements, Location: Location{File: "versions.tf", Line: 3}})
	if facts.Coverage.Capabilities[CapabilityProviderRequirements] != CoverageComplete {
		t.Fatal("static provider version constraint was not retained with complete coverage")
	}
}

func TestExtractFactsPreservesMultipleChildProviderMappings(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf", `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

provider "azurerm" {
  alias = "east"
}

provider "azurerm" {
  alias = "west"
}

module "child" {
  source = "Azure/avm-res-network-virtualnetwork/azurerm"
  providers = {
    azurerm         = azurerm.west
    azurerm_replica = azurerm.east
  }
}`)
	facts, err := ExtractFacts(Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("e", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []UnitInput{{Path: ".", Role: RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFactSymbol(t, facts, FactSymbol{
		Kind: "module_call", Name: "child",
		ProviderMappings: []ProviderMapping{
			{ChildLocalName: "azurerm", ParentLocalName: "azurerm", ParentAlias: "west", ProviderAddress: "registry.terraform.io/hashicorp/azurerm"},
			{ChildLocalName: "azurerm_replica", ParentLocalName: "azurerm", ParentAlias: "east", ProviderAddress: "registry.terraform.io/hashicorp/azurerm"},
		},
		Location: Location{File: "main.tf", Line: 18},
	})
	if facts.Coverage.Capabilities[CapabilityChildModules] != CoverageComplete || facts.Coverage.Capabilities[CapabilityProviderBindings] != CoverageComplete {
		t.Fatalf("static multi-provider mapping left partial coverage: %#v", facts.Coverage.Capabilities)
	}
}

func TestExtractFactsKeepsStaticProviderAddressWithDynamicVersion(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf", `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = var.provider_version
    }
  }
}

resource "azurerm_resource_group" "main" {}`)
	facts, err := ExtractFacts(Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("d", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []UnitInput{{Path: ".", Role: RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFactSymbol(t, facts, FactSymbol{Kind: "resource", Name: "azurerm_resource_group.main", ProviderAddress: "registry.terraform.io/hashicorp/azurerm", LocalProviderName: "azurerm", Location: Location{File: "main.tf", Line: 10}})
	if facts.Coverage.Capabilities[CapabilityProviderRequirements] != CoveragePartial {
		t.Fatal("dynamic provider version constraint was not reported as partial")
	}
}

func TestExtractFactsCapturesLegacyCheckMetadataWithoutValues(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf", `terraform {
  required_version = ">= 1.9.0"
  backend "azurerm" {}
  cloud {}
}

variable "documented" {
  description = "DO_NOT_PUBLISH_DESCRIPTION"
  default     = "DO_NOT_PUBLISH_DEFAULT"
}

variable "undocumented" {}

output "documented" {
  description = "DO_NOT_PUBLISH_OUTPUT_DESCRIPTION"
  value       = var.secret
}`)
	facts, err := ExtractFacts(Input{
		RootDir: root, ModuleID: "avm-res-metadata-test", SourceSHA: strings.Repeat("9", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []UnitInput{{Path: ".", Role: RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if facts.Coverage.Capabilities[CapabilityInterfaceMetadata] != CoverageComplete || facts.Coverage.Capabilities[CapabilityTerraformSettings] != CoverageComplete {
		t.Fatalf("legacy metadata capability coverage = %#v", facts.Coverage.Capabilities)
	}
	assertFactSymbol(t, facts, FactSymbol{Kind: "variable", Name: "documented", DescriptionPresent: boolPointer(true), Location: Location{File: "main.tf", Line: 7}})
	assertFactSymbol(t, facts, FactSymbol{Kind: "variable", Name: "undocumented", DescriptionPresent: boolPointer(false), Location: Location{File: "main.tf", Line: 12}})
	assertFactSymbol(t, facts, FactSymbol{Kind: "output", Name: "documented", DescriptionPresent: boolPointer(true), Location: Location{File: "main.tf", Line: 14}})
	assertFactSymbol(t, facts, FactSymbol{Kind: "terraform_setting", Name: "terraform.required_version", Setting: "required_version", Location: Location{File: "main.tf", Line: 2}})
	assertFactSymbol(t, facts, FactSymbol{Kind: "terraform_setting", Name: "terraform.backend", Setting: "backend", SettingType: "azurerm", Location: Location{File: "main.tf", Line: 3}})
	assertFactSymbol(t, facts, FactSymbol{Kind: "terraform_setting", Name: "terraform.cloud", Setting: "cloud", Location: Location{File: "main.tf", Line: 4}})
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	for _, sourceValue := range []string{"DO_NOT_PUBLISH_DESCRIPTION", "DO_NOT_PUBLISH_DEFAULT", "DO_NOT_PUBLISH_OUTPUT_DESCRIPTION", "var.secret", ">= 1.9.0"} {
		if strings.Contains(string(encoded), sourceValue) {
			t.Fatalf("facts record leaked source value %q", sourceValue)
		}
	}
}

func TestExtractFactsDoesNotPublishUnsafeModuleSources(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf", `module "local" {
	  source  = "/Users/build/private-module"
	  version = "/Users/build/private-version"
}`)
	facts, err := ExtractFacts(Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("c", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []UnitInput{{Path: ".", Role: RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "/Users/build/private-module") || strings.Contains(string(encoded), "/Users/build/private-version") {
		t.Fatal("facts record leaked an absolute module source or version path")
	}
	if facts.Coverage.Capabilities[CapabilityChildModules] != CoveragePartial {
		t.Fatal("unsafe module source was not reflected in coverage")
	}
}

func TestExtractFactsAcceptsContainedParentRelativeModuleSource(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "examples/default/main.tf", `module "root" {
	source = "../../"
}

module "child" {
	source = "./child"
}`)
	facts, err := ExtractFacts(Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("f", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []UnitInput{{Path: "examples/default", Role: RoleExample, Owner: ".", Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFactSymbol(t, facts, FactSymbol{
		Kind: "module_call", Name: "root", ModuleSource: "../..",
		Location: Location{File: "examples/default/main.tf", Line: 1},
	})
	assertFactSymbol(t, facts, FactSymbol{
		Kind: "module_call", Name: "child", ModuleSource: "./child",
		Location: Location{File: "examples/default/main.tf", Line: 5},
	})
	if facts.Coverage.Capabilities[CapabilityChildModules] != CoverageComplete {
		t.Fatalf("contained relative module source left partial coverage: %#v", facts.Coverage.Capabilities)
	}
}

func TestExtractFactsRejectsMalformedAndUnsafeInput(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "main.tf", `resource "azurerm_resource_group" {`)
	input := Input{
		RootDir: root, ModuleID: "avm-res-demo", SourceSHA: strings.Repeat("a", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []UnitInput{{Path: ".", Role: RoleComponent, Files: []string{"main.tf"}}},
	}
	if _, err := ExtractFacts(input); err == nil {
		t.Fatal("malformed HCL was accepted")
	}
	writeFixture(t, root, "bad.tf.json", `{"resource":`)
	input.Units[0].Files = []string{"bad.tf.json"}
	if _, err := ExtractFacts(input); err == nil {
		t.Fatal("malformed Terraform JSON was accepted")
	}

	input.Units[0].Files = []string{"../outside.tf"}
	if _, err := ExtractFacts(input); err == nil {
		t.Fatal("parent-traversing source path was accepted")
	}

	input.Units[0].Files = []string{"link.tf"}
	if err := os.Symlink(filepath.Join(root, "main.tf"), filepath.Join(root, "link.tf")); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractFacts(input); err == nil {
		t.Fatal("symlink source file was accepted")
	}
}

func writeFixture(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFactSymbol(t *testing.T, facts Facts, expected FactSymbol) {
	t.Helper()
	symbol, found := findFactSymbol(facts, expected.Kind, expected.Name, expected.Capability)
	if !found {
		t.Fatalf("fact symbol %s %s not found", expected.Kind, expected.Name)
	}
	if symbol.ProviderAddress != expected.ProviderAddress || symbol.LocalProviderName != expected.LocalProviderName || symbol.Alias != expected.Alias {
		t.Fatalf("symbol %s provider mapping = %#v", expected.Name, symbol)
	}
	if symbol.ProviderVersionConstraint != expected.ProviderVersionConstraint || !slices.Equal(symbol.ProviderMappings, expected.ProviderMappings) {
		t.Fatalf("symbol %s typed provider data = %#v", expected.Name, symbol)
	}
	if !sameBoolPointer(symbol.DescriptionPresent, expected.DescriptionPresent) || symbol.Setting != expected.Setting || symbol.SettingType != expected.SettingType {
		t.Fatalf("symbol %s metadata = %#v", expected.Name, symbol)
	}
	if expected.Capability != "" && symbol.Capability != expected.Capability {
		t.Fatalf("symbol %s capability = %q, want %q", expected.Name, symbol.Capability, expected.Capability)
	}
	if symbol.Location != expected.Location {
		t.Fatalf("symbol %s location = %#v, want %#v", expected.Name, symbol.Location, expected.Location)
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func sameBoolPointer(left, right *bool) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func findFactSymbol(facts Facts, kind, name, capability string) (FactSymbol, bool) {
	for _, unit := range facts.Units {
		for _, symbol := range unit.Symbols {
			if symbol.Kind == kind && symbol.Name == name && (capability == "" || symbol.Capability == capability) {
				return symbol, true
			}
		}
	}
	return FactSymbol{}, false
}
