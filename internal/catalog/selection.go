package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

// AcceptedSelectionSchemaVersion identifies the immutable membership wire contract.
const AcceptedSelectionSchemaVersion = 1

// SelectionProvenance identifies why the exact module membership was selected.
type SelectionProvenance string

const (
	// FactoryNewOnly selects IDs newly added in the accepted catalog generation.
	FactoryNewOnly SelectionProvenance = "factory-new-only"
	// FactoryAllActive selects every active module for scheduled release discovery.
	FactoryAllActive SelectionProvenance = "factory-all-active"
	// ManualExactScope selects only the IDs named by an explicit manual request.
	ManualExactScope SelectionProvenance = "manual-exact-scope"
)

// AcceptedSelection is immutable membership evidence for one accepted catalog
// generation. Its accessors return values or defensive copies.
type AcceptedSelection struct {
	schemaVersion     int
	id                string
	catalogGeneration uint64
	moduleIDs         []string
	provenance        SelectionProvenance
}

type acceptedSelectionIdentity struct {
	CatalogGeneration uint64              `json:"catalog_generation"`
	ModuleIDs         []string            `json:"module_ids"`
	Provenance        SelectionProvenance `json:"provenance"`
}

type acceptedSelectionJSON struct {
	SchemaVersion     int                 `json:"schema_version"`
	ID                string              `json:"id"`
	CatalogGeneration uint64              `json:"catalog_generation"`
	ModuleIDs         []string            `json:"module_ids"`
	Provenance        SelectionProvenance `json:"provenance"`
}

// NewAcceptedSelection creates deterministic immutable membership evidence.
// Module IDs are sorted, while duplicates and non-canonical IDs are rejected.
func NewAcceptedSelection(generation uint64, moduleIDs []string, provenance SelectionProvenance) (AcceptedSelection, error) {
	if generation == 0 {
		return AcceptedSelection{}, fmt.Errorf("catalog generation must be positive")
	}
	if !validSelectionProvenance(provenance) {
		return AcceptedSelection{}, fmt.Errorf("invalid selection provenance %q", provenance)
	}
	ids := append([]string{}, moduleIDs...)
	for _, id := range ids {
		if !moduleIDPattern.MatchString(id) {
			return AcceptedSelection{}, fmt.Errorf("invalid canonical module ID %q", id)
		}
	}
	sort.Strings(ids)
	for index := 1; index < len(ids); index++ {
		if ids[index-1] == ids[index] {
			return AcceptedSelection{}, fmt.Errorf("selection repeats module ID %q", ids[index])
		}
	}
	selection := AcceptedSelection{
		schemaVersion:     AcceptedSelectionSchemaVersion,
		catalogGeneration: generation,
		moduleIDs:         ids,
		provenance:        provenance,
	}
	id, err := selection.stableID()
	if err != nil {
		return AcceptedSelection{}, err
	}
	selection.id = id
	return selection, nil
}

// SchemaVersion returns the membership contract version.
func (s AcceptedSelection) SchemaVersion() int { return s.schemaVersion }

// ID returns the stable content-addressed selection identity.
func (s AcceptedSelection) ID() string { return s.id }

// CatalogGeneration returns the accepted catalog generation this selection uses.
func (s AcceptedSelection) CatalogGeneration() uint64 { return s.catalogGeneration }

// ModuleIDs returns a defensive copy of the canonical sorted membership.
func (s AcceptedSelection) ModuleIDs() []string { return append([]string{}, s.moduleIDs...) }

// Provenance returns the explicit selection source.
func (s AcceptedSelection) Provenance() SelectionProvenance { return s.provenance }

// Validate checks the complete immutable membership contract and its identity.
func (s AcceptedSelection) Validate() error {
	if s.schemaVersion != AcceptedSelectionSchemaVersion || s.catalogGeneration == 0 {
		return fmt.Errorf("accepted selection metadata is invalid")
	}
	if !validSelectionProvenance(s.provenance) {
		return fmt.Errorf("accepted selection provenance is invalid")
	}
	for index, id := range s.moduleIDs {
		if !moduleIDPattern.MatchString(id) {
			return fmt.Errorf("accepted selection contains invalid canonical module ID %q", id)
		}
		if index > 0 && s.moduleIDs[index-1] >= id {
			return fmt.Errorf("accepted selection module IDs are not sorted and unique")
		}
	}
	id, err := s.stableID()
	if err != nil {
		return err
	}
	if s.id != id {
		return fmt.Errorf("accepted selection identity conflicts with its immutable membership")
	}
	return nil
}

func (s AcceptedSelection) stableID() (string, error) {
	return identity.ID("accepted-selection", s.schemaVersion, acceptedSelectionIdentity{
		CatalogGeneration: s.catalogGeneration,
		ModuleIDs:         s.moduleIDs,
		Provenance:        s.provenance,
	})
}

func validSelectionProvenance(provenance SelectionProvenance) bool {
	return provenance == FactoryNewOnly || provenance == FactoryAllActive || provenance == ManualExactScope
}

// MarshalJSON validates membership before encoding its stable wire contract.
func (s AcceptedSelection) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(acceptedSelectionJSON{
		SchemaVersion:     s.schemaVersion,
		ID:                s.id,
		CatalogGeneration: s.catalogGeneration,
		ModuleIDs:         s.moduleIDs,
		Provenance:        s.provenance,
	})
}

// UnmarshalJSON accepts only valid, canonical immutable membership records.
func (s *AcceptedSelection) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("accepted selection destination is nil")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var encoded acceptedSelectionJSON
	if err := decoder.Decode(&encoded); err != nil {
		return fmt.Errorf("decode accepted selection: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("accepted selection must contain one JSON value")
	}
	selection := AcceptedSelection{
		schemaVersion:     encoded.SchemaVersion,
		id:                encoded.ID,
		catalogGeneration: encoded.CatalogGeneration,
		moduleIDs:         append([]string{}, encoded.ModuleIDs...),
		provenance:        encoded.Provenance,
	}
	if err := selection.Validate(); err != nil {
		return err
	}
	*s = selection
	return nil
}
