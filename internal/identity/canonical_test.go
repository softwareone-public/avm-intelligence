package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalJSONSortsObjectKeysAndSeparatesIdentity(t *testing.T) {
	t.Parallel()
	first, err := CanonicalJSON("facts", 1, map[string]any{"z": 3, "a": map[string]any{"y": true, "b": 2}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalJSON("facts", 1, map[string]any{"a": map[string]any{"b": 2, "y": true}, "z": 3})
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical encodings differ:\n%s\n%s", first, second)
	}
	otherKind, err := ID("analysis", 1, map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	factsID, err := ID("facts", 1, map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	otherSchemaID, err := ID("facts", 2, map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	if factsID == otherKind || factsID == otherSchemaID {
		t.Fatal("record kind or schema version did not domain-separate the identity")
	}
	canonical, err := CanonicalJSON("facts", 1, map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	if factsID != "facts-"+hex.EncodeToString(digest[:]) {
		t.Fatalf("facts ID = %s, canonical digest = %x", factsID, digest)
	}
}

func TestCanonicalJSONDistinguishesExplicitNullFromEmpty(t *testing.T) {
	t.Parallel()
	type value struct {
		Items []string `json:"items"`
	}
	nullID, err := ID("facts", 1, value{Items: nil})
	if err != nil {
		t.Fatal(err)
	}
	emptyID, err := ID("facts", 1, value{Items: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if nullID == emptyID {
		t.Fatal("explicit null and empty array shared an identity")
	}
	encoded, err := CanonicalJSON("facts", 1, value{Items: nil})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(document["encoding_version"], float64(CanonicalEncodingVersion)) {
		t.Fatalf("canonical envelope = %#v", document)
	}
}

func TestSortedSetCanonicalizesAndRejectsDuplicates(t *testing.T) {
	t.Parallel()
	sorted, err := SortedSet([]string{"z", "a", "m"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sorted, []string{"a", "m", "z"}) {
		t.Fatalf("sorted set = %#v", sorted)
	}
	if _, err = SortedSet([]string{"a", "a"}); err == nil {
		t.Fatal("duplicate set value was accepted")
	}
}

func TestValidateAppendOnlyIDs(t *testing.T) {
	t.Parallel()
	firstID, err := ID("version-observation", 1, map[string]string{"generation": "1"})
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := ID("version-observation", 1, map[string]string{"generation": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateAppendOnlyIDs([]string{firstID}, []string{firstID, secondID}); err != nil {
		t.Fatalf("append-only extension rejected: %v", err)
	}
	for _, invalid := range [][]string{{}, {secondID, firstID}, {firstID, firstID}} {
		if err = ValidateAppendOnlyIDs([]string{firstID}, invalid); err == nil {
			t.Errorf("rewritten or duplicate history accepted: %#v", invalid)
		}
	}
}

func TestValidateRelativePath(t *testing.T) {
	t.Parallel()
	for _, path := range []string{".", "examples/default", "modules/child_1"} {
		if err := ValidateRelativePath(path, path == "."); err != nil {
			t.Errorf("valid path %q rejected: %v", path, err)
		}
	}
	for _, path := range []string{"", "../secret", "a/../secret", "/absolute", `C:\secret`, "a\\b", "a//b", "a/./b"} {
		if err := ValidateRelativePath(path, false); err == nil {
			t.Errorf("unsafe or non-canonical path %q was accepted", path)
		}
	}
	if err := ValidateRelativePath(".", false); err == nil || !strings.Contains(err.Error(), "root path") {
		t.Fatalf("root-path error = %v", err)
	}
}
