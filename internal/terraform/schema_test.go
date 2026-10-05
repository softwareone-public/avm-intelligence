package terraform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFactsV3SchemaIncludesProviderConstraintsAndMappings(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "schemas", "factory-facts-v3.schema.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode facts v3 schema: %v", err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("facts v3 schema properties are missing")
	}
	version, ok := properties["schema_version"].(map[string]any)
	if !ok || version["const"] != float64(3) {
		t.Fatal("facts schema version is not 3")
	}
	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("facts v3 schema definitions are missing")
	}
	symbol, ok := definitions["factSymbol"].(map[string]any)
	if !ok {
		t.Fatal("fact symbol definition is missing")
	}
	symbolProperties, ok := symbol["properties"].(map[string]any)
	if !ok {
		t.Fatal("fact symbol properties are missing")
	}
	for _, name := range []string{"provider_version_constraint", "provider_version_selection", "provider_mappings"} {
		if _, exists := symbolProperties[name]; !exists {
			t.Errorf("fact symbol property %q is missing", name)
		}
	}
	if symbol["additionalProperties"] != false {
		t.Error("fact symbol permits undeclared properties")
	}
	if schema["additionalProperties"] != false {
		t.Error("facts record permits undeclared properties")
	}
}
