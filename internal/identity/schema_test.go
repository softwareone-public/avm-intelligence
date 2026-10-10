package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFactoryDataSchemaLocalReferencesResolve(t *testing.T) {
	t.Parallel()
	schemaPath := filepath.Join("..", "..", "schemas", "factory-data-v2.schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode v2 schema: %v", err)
	}
	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("v2 schema has no $defs object")
	}
	if err = checkSchemaReferences(schema, definitions); err != nil {
		t.Fatal(err)
	}
	for _, proposalKind := range []string{
		"moduleEvidenceProposal", "catalogProposal", "providerSnapshotProposal",
		"moduleObservationProposal", "selectionExportProposal", "presentationProposal",
	} {
		if _, exists := definitions[proposalKind]; !exists {
			t.Errorf("v2 schema is missing proposal definition %q", proposalKind)
		}
	}
	if !hasConditionalRequired(definitions, "request", "acquire_source", "source_sha", "facts_id") {
		t.Error("acquire_source must require a source SHA and explicit facts_id")
	}
	if !hasConditionalRequired(definitions, "request", "assess", "facts_id") {
		t.Error("assess must require accepted facts identity")
	}
	for _, definition := range []string{"moduleVersionObservation", "moduleVersionsIndex"} {
		if _, exists := definitions[definition]; !exists {
			t.Errorf("v2 schema is missing append-only alias definition %q", definition)
		}
	}
	testFactsSchemaSeparatesProducerFromContent(t, definitions)
	testVersionHistorySchemaIsAppendOnly(t, definitions)
}

func TestFactoryDataV3VersionsSelectionReceiptsWithoutWideningV2(t *testing.T) {
	t.Parallel()
	readDefinitions := func(filename string) map[string]any {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", "schemas", filename))
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err = json.Unmarshal(data, &document); err != nil {
			t.Fatalf("decode %s: %v", filename, err)
		}
		definitions, ok := document["$defs"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema has no definitions", filename)
		}
		if err = checkSchemaReferences(document, definitions); err != nil {
			t.Fatalf("validate %s local references: %v", filename, err)
		}
		return definitions
	}
	v2 := propertiesForDefinition(readDefinitions("factory-data-v2.schema.json"), "selectionReceipt")
	v3 := propertiesForDefinition(readDefinitions("factory-data-v3.schema.json"), "selectionReceipt")
	if v2 == nil || v3 == nil {
		t.Fatal("selection receipt schema properties are missing")
	}
	if _, exists := v2["avm_release_version"]; exists {
		t.Fatal("frozen v2 selection receipt schema was widened")
	}
	if _, exists := v3["avm_release_version"]; !exists {
		t.Fatal("v3 selection receipt schema does not preserve its AVM release")
	}
}

func TestFactoryIndexSchemaLocalReferencesResolve(t *testing.T) {
	t.Parallel()
	schemaPath := filepath.Join("..", "..", "schemas", "factory-index-v1.schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode generated index schema: %v", err)
	}
	definitions, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("generated index schema has no $defs object")
	}
	if err = checkSchemaReferences(schema, definitions); err != nil {
		t.Fatal(err)
	}
	for _, definition := range []string{
		"catalogIndex", "moduleIndex", "trackIndex", "providerIndex",
		"dimensionDependencyIndex", "presentationIndex",
	} {
		if _, exists := definitions[definition]; !exists {
			t.Errorf("generated index schema is missing definition %q", definition)
		}
	}
}

func testFactsSchemaSeparatesProducerFromContent(t *testing.T, definitions map[string]any) {
	t.Helper()
	factsProperties := propertiesForDefinition(definitions, "facts")
	if factsProperties == nil {
		t.Fatal("facts schema properties are missing")
	}
	if _, exists := factsProperties["content_digest"]; !exists {
		t.Error("facts content digest is missing")
	}
	for _, producerField := range []string{"producer", "producer_id", "input_fingerprint"} {
		if _, exists := factsProperties[producerField]; exists {
			t.Errorf("producer-specific field %q is embedded in reusable facts", producerField)
		}
	}
	provenanceProperties := propertiesForDefinition(definitions, "provenance")
	if provenanceProperties == nil {
		t.Fatal("provenance schema properties are missing")
	}
	if _, exists := provenanceProperties["producer"]; !exists {
		t.Error("producer identity is missing from provenance")
	}
	if _, exists := provenanceProperties["facts_id"]; !exists {
		t.Error("provenance does not reference reusable facts")
	}
}

func propertiesForDefinition(definitions map[string]any, name string) map[string]any {
	definition, ok := definitions[name].(map[string]any)
	if !ok {
		return nil
	}
	branches, ok := definition["allOf"].([]any)
	if !ok {
		return nil
	}
	for _, branch := range branches {
		branchObject, ok := branch.(map[string]any)
		if !ok {
			continue
		}
		properties, ok := branchObject["properties"].(map[string]any)
		if ok {
			return properties
		}
	}
	return nil
}

func testVersionHistorySchemaIsAppendOnly(t *testing.T, definitions map[string]any) {
	t.Helper()
	observationProperties := propertiesForDefinition(definitions, "moduleVersionObservation")
	if observationProperties == nil {
		t.Fatal("module version observation properties are missing")
	}
	if _, exists := observationProperties["source_sha"]; !exists {
		t.Error("version observation does not retain the resolved source SHA")
	}
	if _, exists := observationProperties["tags"]; !exists {
		t.Error("version observation does not retain original tag spellings")
	}
	immutable, ok := observationProperties["immutable"].(map[string]any)
	if !ok || immutable["const"] != true {
		t.Error("version observations are not declared immutable")
	}
	indexProperties := propertiesForDefinition(definitions, "moduleVersionsIndex")
	if indexProperties == nil {
		t.Fatal("module versions index properties are missing")
	}
	observationIDs, ok := indexProperties["observation_ids"].(map[string]any)
	if !ok || observationIDs["uniqueItems"] != true {
		t.Error("versions index observations are not a unique append-only reference set")
	}
}

func hasConditionalRequired(definitions map[string]any, definitionName, kind string, fields ...string) bool {
	definition, ok := definitions[definitionName].(map[string]any)
	if !ok {
		return false
	}
	allOf, ok := definition["allOf"].([]any)
	if !ok {
		return false
	}
	for _, branch := range allOf {
		branchObject, ok := branch.(map[string]any)
		if !ok {
			continue
		}
		conditions, ok := branchObject["allOf"].([]any)
		if !ok {
			continue
		}
		for _, condition := range conditions {
			conditionObject, ok := condition.(map[string]any)
			if !ok || !conditionMatchesKind(conditionObject, kind) || !requiredFieldsPresent(conditionObject, fields) {
				continue
			}
			return true
		}
	}
	return false
}

func conditionMatchesKind(condition map[string]any, kind string) bool {
	ifObject, ok := condition["if"].(map[string]any)
	if !ok {
		return false
	}
	properties, ok := ifObject["properties"].(map[string]any)
	if !ok {
		return false
	}
	kindObject, ok := properties["kind"].(map[string]any)
	return ok && kindObject["const"] == kind
}

func requiredFieldsPresent(condition map[string]any, expected []string) bool {
	thenObject, ok := condition["then"].(map[string]any)
	if !ok {
		return false
	}
	requiredValues, ok := thenObject["required"].([]any)
	if !ok {
		return false
	}
	required := make(map[string]bool, len(requiredValues))
	for _, value := range requiredValues {
		field, ok := value.(string)
		if ok {
			required[field] = true
		}
	}
	for _, field := range expected {
		if !required[field] {
			return false
		}
	}
	return true
}

func checkSchemaReferences(value any, definitions map[string]any) error {
	switch node := value.(type) {
	case map[string]any:
		if reference, exists := node["$ref"].(string); exists && strings.HasPrefix(reference, "#/$defs/") {
			definitionName := strings.TrimPrefix(reference, "#/$defs/")
			if _, ok := definitions[definitionName]; !ok {
				return fmt.Errorf("schema reference %q does not resolve", reference)
			}
		}
		for _, child := range node {
			if err := checkSchemaReferences(child, definitions); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range node {
			if err := checkSchemaReferences(child, definitions); err != nil {
				return err
			}
		}
	}
	return nil
}
