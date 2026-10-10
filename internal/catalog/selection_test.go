package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAcceptedSelectionIdentityIsDeterministicAndMembershipBound(t *testing.T) {
	t.Parallel()
	first, err := NewAcceptedSelection(7, []string{
		"avm-utl-example",
		"avm-res-resources-resourcegroup",
		"avm-ptn-example",
	}, FactoryNewOnly)
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := NewAcceptedSelection(7, []string{
		"avm-ptn-example",
		"avm-utl-example",
		"avm-res-resources-resourcegroup",
	}, FactoryNewOnly)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() != reordered.ID() {
		t.Fatalf("reordered membership changed identity: %q != %q", first.ID(), reordered.ID())
	}
	if first.SchemaVersion() != AcceptedSelectionSchemaVersion || first.CatalogGeneration() != 7 || first.Provenance() != FactoryNewOnly {
		t.Fatalf("selection metadata = %#v", first)
	}
	wantIDs := []string{"avm-ptn-example", "avm-res-resources-resourcegroup", "avm-utl-example"}
	if got := first.ModuleIDs(); len(got) != len(wantIDs) || strings.Join(got, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("module IDs = %v, want %v", got, wantIDs)
	}

	changedMembership, err := NewAcceptedSelection(7, []string{
		"avm-ptn-example",
		"avm-utl-example",
	}, FactoryNewOnly)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() == changedMembership.ID() {
		t.Fatal("changed membership at the same generation reused the identity")
	}
	changedGeneration, err := NewAcceptedSelection(8, first.ModuleIDs(), FactoryNewOnly)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() == changedGeneration.ID() {
		t.Fatal("changed catalog generation reused the identity")
	}
	changedProvenance, err := NewAcceptedSelection(7, first.ModuleIDs(), ManualExactScope)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() == changedProvenance.ID() {
		t.Fatal("changed selection provenance reused the identity")
	}

	ids := first.ModuleIDs()
	ids[0] = "avm-res-corrupted"
	if got := first.ModuleIDs()[0]; got != wantIDs[0] {
		t.Fatalf("mutating returned module IDs changed selection: %q", got)
	}
}

func TestAcceptedSelectionSupportsScheduledAllActiveProvenance(t *testing.T) {
	t.Parallel()
	want, err := NewAcceptedSelection(9, []string{"avm-res-example", "avm-ptn-example"}, FactoryAllActive)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var decoded AcceptedSelection
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ID() != want.ID() || decoded.Provenance() != FactoryAllActive || strings.Join(decoded.ModuleIDs(), ",") != strings.Join(want.ModuleIDs(), ",") {
		t.Fatalf("decoded scheduled membership = %#v, want %#v", decoded, want)
	}
}

func TestAcceptedSelectionRejectsDuplicateAndNoncanonicalIDs(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		ids  []string
	}{
		{name: "duplicate", ids: []string{"avm-res-example", "avm-res-example"}},
		{name: "mixed-case", ids: []string{"avm-res-Example"}},
		{name: "wrong-prefix", ids: []string{"res-example"}},
		{name: "empty", ids: []string{""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewAcceptedSelection(1, test.ids, ManualExactScope); err == nil {
				t.Fatalf("invalid IDs were accepted: %v", test.ids)
			}
		})
	}
}

func TestAcceptedSelectionJSONRejectsChangedOrCorruptMembership(t *testing.T) {
	t.Parallel()
	selection, err := NewAcceptedSelection(4, []string{"avm-res-example", "avm-ptn-example"}, ManualExactScope)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	var roundTripped AcceptedSelection
	if err = json.Unmarshal(encoded, &roundTripped); err != nil {
		t.Fatalf("valid selection did not round-trip: %v", err)
	}
	if roundTripped.ID() != selection.ID() {
		t.Fatalf("round-trip identity = %q, want %q", roundTripped.ID(), selection.ID())
	}
	var wire map[string]any
	if err = json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "changed membership same generation",
			mutate: func(value map[string]any) {
				value["module_ids"] = []any{"avm-res-example"}
			},
		},
		{
			name: "duplicate IDs",
			mutate: func(value map[string]any) {
				value["module_ids"] = []any{"avm-res-example", "avm-res-example"}
			},
		},
		{
			name: "corrupt ID",
			mutate: func(value map[string]any) {
				value["module_ids"] = []any{"avm-res-Example", "avm-ptn-example"}
			},
		},
		{
			name: "unsorted IDs",
			mutate: func(value map[string]any) {
				value["module_ids"] = []any{"avm-res-example", "avm-ptn-example"}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			modified := make(map[string]any, len(wire))
			for key, value := range wire {
				modified[key] = value
			}
			test.mutate(modified)
			data, marshalErr := json.Marshal(modified)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var decoded AcceptedSelection
			if decodeErr := json.Unmarshal(data, &decoded); decodeErr == nil {
				t.Fatal("corrupt membership was accepted")
			}
		})
	}
}

func TestAcceptedSelectionRejectsInvalidMetadata(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		generation uint64
		provenance SelectionProvenance
	}{
		{generation: 0, provenance: FactoryNewOnly},
		{generation: 1, provenance: "all-modules"},
	} {
		if _, err := NewAcceptedSelection(test.generation, []string{"avm-res-example"}, test.provenance); err == nil {
			t.Fatalf("invalid metadata was accepted: %#v", test)
		}
	}
}
