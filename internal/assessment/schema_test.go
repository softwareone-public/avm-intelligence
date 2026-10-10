package assessment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

func TestCompiledAssessmentRecordsMatchFactoryV2Schemas(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "schemas", "factory-data-v2.schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode factory v2 schema: %v", err)
	}
	definitions, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("factory v2 schema has no definitions")
	}

	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, dimension := range registry.ListDimensions() {
		assertMatchesDefinition(t, definitions, "dimensionDefinition", dimension)
	}
	for _, dimension := range registry.ListDimensions() {
		checks, err := registry.ListChecks(dimension.DimensionID)
		if err != nil {
			t.Fatal(err)
		}
		for _, check := range checks {
			assertMatchesDefinition(t, definitions, "checkDefinition", check)
		}
	}

	results, err := RunDimensions(registry, RunInput{Facts: schemaFactsFixture(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		resultObject := assertMatchesDefinition(t, definitions, "dimensionResult", result)
		checkSchema := schemaDefinition(t, definitions, "checkResult")
		for _, rawCheck := range resultObject["checks"].([]any) {
			assertObjectShape(t, "checkResult", rawCheck.(map[string]any), checkSchema)
		}
		coverageSchema := resultObjectSchemaProperty(t, "dimensionResult", "coverage", definitions)
		assertObjectShape(t, "dimensionResult.coverage", resultObject["coverage"].(map[string]any), coverageSchema)
	}
}

func TestProviderIntelligenceStubMatchesFactoryDataV3Schema(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "schemas", "factory-data-v3.schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	definitions, ok := document["$defs"].(map[string]any)
	if !ok {
		t.Fatal("factory v3 schema has no definitions")
	}
	entrySchema := resultObjectSchemaProperty(t, "providerSnapshot", "entries", definitions)["items"].(map[string]any)
	entry := ProviderEntry{
		Kind: "resource", Name: "azurerm_resource_group",
		IntelligenceSources: []ProviderIntelligenceSource{{
			Kind: "github_release", URL: "https://github.com/hashicorp/terraform-provider-azurerm/releases/tag/v4.2.0",
			Version: "4.2.0", VersionBasis: "release_tag", ContentDigest: strings.Repeat("a", 64), Body: "release notes",
			ObservedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
		}},
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var entryObject map[string]any
	if err = json.Unmarshal(encoded, &entryObject); err != nil {
		t.Fatal(err)
	}
	assertObjectShape(t, "providerEntry", entryObject, entrySchema)
	intelligence := entryObject["intelligence_sources"].([]any)[0].(map[string]any)
	intelligenceSchema := entrySchema["properties"].(map[string]any)["intelligence_sources"].(map[string]any)["items"].(map[string]any)
	assertObjectShape(t, "providerIntelligenceSource", intelligence, intelligenceSchema)
}

func schemaFactsFixture(t *testing.T) terraform.Facts {
	t.Helper()
	root := t.TempDir()
	source := `terraform {
  required_providers {
    azurerm = {
      source = "hashicorp/azurerm"
    }
  }
}

resource "azurerm_resource_group" "main" {}`
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: root, ModuleID: "avm-res-assessment-schema", SourceSHA: "1234567890123456789012345678901234567890",
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []terraform.UnitInput{{Path: ".", Role: terraform.RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func assertMatchesDefinition(t *testing.T, definitions map[string]any, name string, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	assertObjectShape(t, name, object, schemaDefinition(t, definitions, name))
	return object
}

func schemaDefinition(t *testing.T, definitions map[string]any, name string) map[string]any {
	t.Helper()
	definition, ok := definitions[name].(map[string]any)
	if !ok {
		t.Fatalf("schema definition %q is missing", name)
	}
	allOf, ok := definition["allOf"].([]any)
	if !ok {
		return definition
	}
	for _, branch := range allOf {
		object, ok := branch.(map[string]any)
		if ok {
			if _, hasProperties := object["properties"]; hasProperties {
				return object
			}
		}
	}
	t.Fatalf("schema definition %q has no properties branch", name)
	return nil
}

func resultObjectSchemaProperty(t *testing.T, definitionName, propertyName string, definitions map[string]any) map[string]any {
	t.Helper()
	schema := schemaDefinition(t, definitions, definitionName)
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema definition %q has no properties", definitionName)
	}
	property, ok := properties[propertyName].(map[string]any)
	if !ok {
		t.Fatalf("schema property %s.%s is missing", definitionName, propertyName)
	}
	return property
}

func assertObjectShape(t *testing.T, name string, object, schema map[string]any) {
	t.Helper()
	if schema["additionalProperties"] != false {
		t.Fatalf("schema %q must reject additional properties", name)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema %q has no properties", name)
	}
	assertNoUndeclaredProperties(t, name, object, properties)
	assertRequiredProperties(t, name, object, properties, schema)
}

func assertNoUndeclaredProperties(t *testing.T, name string, object, properties map[string]any) {
	t.Helper()
	for property := range object {
		if _, exists := properties[property]; !exists {
			t.Errorf("%s includes undeclared property %q", name, property)
		}
	}
}

func assertRequiredProperties(t *testing.T, name string, object, properties, schema map[string]any) {
	t.Helper()
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("schema %q has no required list", name)
	}
	for _, rawProperty := range required {
		property := rawProperty.(string)
		value, exists := object[property]
		if !exists {
			t.Errorf("%s is missing required property %q", name, property)
			continue
		}
		assertPropertyValue(t, name, property, value, properties[property])
	}
}

func assertPropertyValue(t *testing.T, name, property string, value any, rawSchema any) {
	t.Helper()
	propertySchema, ok := rawSchema.(map[string]any)
	if !ok {
		return
	}
	if constant, exists := propertySchema["const"]; exists && !reflect.DeepEqual(value, constant) {
		t.Errorf("%s.%s = %#v, want constant %#v", name, property, value, constant)
	}
	if enum, exists := propertySchema["enum"].([]any); exists && !enumContains(enum, value) {
		t.Errorf("%s.%s = %#v, not in enum %#v", name, property, value, enum)
	}
}

func enumContains(values []any, target any) bool {
	for _, value := range values {
		if reflect.DeepEqual(value, target) {
			return true
		}
	}
	return false
}
