package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

// AcceptedResourceSnapshotReference names one provider snapshot already
// accepted into the canonical data tree.
type AcceptedResourceSnapshotReference struct {
	ProviderAddress string
	ProviderVersion string
	SnapshotID      string
}

// LoadAcceptedProviderSnapshot proves that an exact immutable provider
// snapshot is present in the indexed accepted data tree.
func LoadAcceptedProviderSnapshot(dataRoot, providerAddress, providerVersion, snapshotID string) (assessment.ProviderSnapshot, error) {
	reference := AcceptedResourceSnapshotReference{ProviderAddress: providerAddress, ProviderVersion: providerVersion, SnapshotID: snapshotID}
	if identity.ValidateRelativePath(providerAddress, false) != nil || len(strings.Split(providerAddress, "/")) != 3 ||
		validateCatalogVersion(providerVersion, nil) != nil || identity.ValidateRecordID("snapshot", snapshotID) != nil {
		return assessment.ProviderSnapshot{}, fmt.Errorf("accepted provider snapshot reference is invalid")
	}
	records, err := index.ReadRecords(dataRoot, time.Time{})
	if err != nil {
		return assessment.ProviderSnapshot{}, fmt.Errorf("read accepted provider data tree: %w", err)
	}
	indexes, err := index.Build(records)
	if err != nil {
		return assessment.ProviderSnapshot{}, fmt.Errorf("validate accepted provider data tree: %w", err)
	}
	if !containsAcceptedProviderSnapshot(indexes.Providers, reference) {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider snapshot is not in the validated accepted provider index")
	}
	path, err := identity.ProviderSnapshotManifestPath(providerAddress, providerVersion, snapshotID)
	if err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	encoded, err := readAcceptedDataRecord(dataRoot, path, records.RecordDigests)
	if err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	var snapshot assessment.ProviderSnapshot
	if json.Unmarshal(encoded, &snapshot) != nil || !acceptedSnapshotMatches(snapshot, reference) {
		return assessment.ProviderSnapshot{}, fmt.Errorf("accepted provider snapshot content does not match its exact reference")
	}
	return cloneAcceptedProviderSnapshot(snapshot), nil
}

// AcceptedResourceIntelligenceInputs contains the exact accepted module facts
// and provider stubs a source collector may enrich. It is read-only input.
type AcceptedResourceIntelligenceInputs struct {
	Facts             terraform.Facts
	Snapshots         []ResourceIntelligenceSnapshot
	ProviderSnapshots []assessment.ProviderSnapshot
	seal              string
}

// LoadAcceptedResourceIntelligenceInputs proves that the requested facts and
// provider snapshots are present in one validated accepted data tree. Stub
// digests bind each module-used provider type to the exact accepted snapshot.
func LoadAcceptedResourceIntelligenceInputs(dataRoot, moduleID, sourceSHA, factsID string, policy CollectionPolicy, references []AcceptedResourceSnapshotReference) (AcceptedResourceIntelligenceInputs, error) {
	if err := policy.Validate(); err != nil {
		return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("validate resource intelligence policy: %w", err)
	}
	if err := identity.ValidateRecordID("facts", factsID); err != nil {
		return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("validate accepted facts reference: %w", err)
	}
	if _, err := identity.ModuleSourcePath(moduleID, sourceSHA); err != nil {
		return AcceptedResourceIntelligenceInputs{}, err
	}
	if len(references) == 0 {
		return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("at least one accepted provider snapshot is required")
	}
	records, err := index.ReadRecords(dataRoot, time.Time{})
	if err != nil {
		return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("read accepted data tree: %w", err)
	}
	indexes, err := index.Build(records)
	if err != nil {
		return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("validate accepted data records: %w", err)
	}
	factsPath, err := identity.ModuleRecordPath(moduleID, sourceSHA, "facts", factsID)
	if err != nil {
		return AcceptedResourceIntelligenceInputs{}, err
	}
	factsBytes, err := readAcceptedDataRecord(dataRoot, factsPath, records.RecordDigests)
	if err != nil {
		return AcceptedResourceIntelligenceInputs{}, err
	}
	var facts terraform.Facts
	if err = json.Unmarshal(factsBytes, &facts); err != nil {
		return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("decode accepted module facts: %w", err)
	}
	if err = facts.Validate(); err != nil || facts.ModuleID != moduleID || facts.SourceSHA != sourceSHA || facts.FactsID != factsID {
		return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("accepted facts do not match the exact module revision")
	}
	if !containsAcceptedFacts(records.Facts, moduleID, sourceSHA, factsID) || !containsAcceptedSource(records.SourceRevisions, moduleID, sourceSHA) {
		return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("accepted facts lack their exact indexed source revision")
	}

	usedResources := resourcesFromFacts(facts)
	if len(usedResources) == 0 {
		return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("accepted facts contain no provider resources for intelligence collection")
	}
	orderedReferences := append([]AcceptedResourceSnapshotReference(nil), references...)
	sort.Slice(orderedReferences, func(left, right int) bool {
		if orderedReferences[left].ProviderAddress != orderedReferences[right].ProviderAddress {
			return orderedReferences[left].ProviderAddress < orderedReferences[right].ProviderAddress
		}
		if orderedReferences[left].ProviderVersion != orderedReferences[right].ProviderVersion {
			return orderedReferences[left].ProviderVersion < orderedReferences[right].ProviderVersion
		}
		return orderedReferences[left].SnapshotID < orderedReferences[right].SnapshotID
	})
	accepted := AcceptedResourceIntelligenceInputs{
		Facts: facts, Snapshots: make([]ResourceIntelligenceSnapshot, 0, len(orderedReferences)),
		ProviderSnapshots: make([]assessment.ProviderSnapshot, 0, len(orderedReferences)),
	}
	for index, reference := range orderedReferences {
		if index > 0 && orderedReferences[index-1] == reference {
			return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("accepted provider snapshot reference is duplicated")
		}
		providerPolicy, found := providerPolicyForAddress(policy, reference.ProviderAddress)
		if !found || validateCatalogVersion(reference.ProviderVersion, providerPolicy.TrackMajors) != nil || identity.ValidateRecordID("snapshot", reference.SnapshotID) != nil {
			return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("provider snapshot reference is outside reviewed policy")
		}
		if !containsAcceptedProviderSnapshot(indexes.Providers, reference) {
			return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("provider snapshot is not in the validated accepted provider index")
		}
		snapshotPath, pathErr := identity.ProviderSnapshotManifestPath(reference.ProviderAddress, reference.ProviderVersion, reference.SnapshotID)
		if pathErr != nil {
			return AcceptedResourceIntelligenceInputs{}, pathErr
		}
		snapshotBytes, readErr := readAcceptedDataRecord(dataRoot, snapshotPath, records.RecordDigests)
		if readErr != nil {
			return AcceptedResourceIntelligenceInputs{}, readErr
		}
		var snapshot assessment.ProviderSnapshot
		if json.Unmarshal(snapshotBytes, &snapshot) != nil || !acceptedSnapshotMatches(snapshot, reference) {
			return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("accepted provider snapshot content does not match its exact reference")
		}
		accepted.ProviderSnapshots = append(accepted.ProviderSnapshots, cloneAcceptedProviderSnapshot(snapshot))
		resources := usedResources[reference.ProviderAddress]
		if len(resources) == 0 {
			return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("accepted provider snapshot is unrelated to module facts")
		}
		stubEntries := make([]assessment.ProviderEntry, 0, len(resources))
		entriesByKey := make(map[string]assessment.ProviderEntry, len(snapshot.Entries))
		for _, entry := range snapshot.Entries {
			key := entry.Kind + "\x00" + entry.Name
			if _, duplicate := entriesByKey[key]; duplicate {
				return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("accepted provider snapshot repeats a provider stub")
			}
			entriesByKey[key] = entry
		}
		for _, resource := range resources {
			entry, exists := entriesByKey[resource.Kind+"\x00"+resource.Name]
			if !exists {
				return AcceptedResourceIntelligenceInputs{}, fmt.Errorf("accepted provider snapshot lacks module-used stub %s/%s", resource.Kind, resource.Name)
			}
			stubEntries = append(stubEntries, entry)
		}
		stubDigest, digestErr := resourceStubDigest(reference, stubEntries)
		if digestErr != nil {
			return AcceptedResourceIntelligenceInputs{}, digestErr
		}
		accepted.Snapshots = append(accepted.Snapshots, ResourceIntelligenceSnapshot{
			ProviderAddress: reference.ProviderAddress, ProviderVersion: reference.ProviderVersion,
			SourceRepository: providerPolicy.SourceRepository, SnapshotID: reference.SnapshotID,
			StubDigest: stubDigest, Resources: resources,
		})
	}
	accepted.seal, err = acceptedResourceInputsSeal(accepted.Facts, accepted.Snapshots, accepted.ProviderSnapshots)
	if err != nil {
		return AcceptedResourceIntelligenceInputs{}, err
	}
	return accepted, nil
}

// LoadAcceptedModuleFacts proves that one exact module facts record and its
// immutable source revision are present in a validated accepted data tree.
func LoadAcceptedModuleFacts(dataRoot, moduleID, sourceSHA, factsID string) (terraform.Facts, error) {
	if err := identity.ValidateRecordID("facts", factsID); err != nil {
		return terraform.Facts{}, fmt.Errorf("validate accepted facts reference: %w", err)
	}
	if _, err := identity.ModuleSourcePath(moduleID, sourceSHA); err != nil {
		return terraform.Facts{}, err
	}
	records, err := index.ReadRecords(dataRoot, time.Time{})
	if err != nil {
		return terraform.Facts{}, fmt.Errorf("read accepted data tree: %w", err)
	}
	if _, err = index.Build(records); err != nil {
		return terraform.Facts{}, fmt.Errorf("validate accepted data records: %w", err)
	}
	factsPath, err := identity.ModuleRecordPath(moduleID, sourceSHA, "facts", factsID)
	if err != nil {
		return terraform.Facts{}, err
	}
	factsBytes, err := readAcceptedDataRecord(dataRoot, factsPath, records.RecordDigests)
	if err != nil {
		return terraform.Facts{}, err
	}
	var facts terraform.Facts
	if err = json.Unmarshal(factsBytes, &facts); err != nil {
		return terraform.Facts{}, fmt.Errorf("decode accepted module facts: %w", err)
	}
	if err = facts.Validate(); err != nil || facts.ModuleID != moduleID || facts.SourceSHA != sourceSHA || facts.FactsID != factsID {
		return terraform.Facts{}, fmt.Errorf("accepted facts do not match the exact module revision")
	}
	if !containsAcceptedFacts(records.Facts, moduleID, sourceSHA, factsID) || !containsAcceptedSource(records.SourceRevisions, moduleID, sourceSHA) {
		return terraform.Facts{}, fmt.Errorf("accepted facts lack their exact indexed source revision")
	}
	return facts, nil
}

func acceptedResourceInputsSeal(facts terraform.Facts, snapshots []ResourceIntelligenceSnapshot, providerSnapshots []assessment.ProviderSnapshot) (string, error) {
	encoded, err := identity.CanonicalJSON("accepted-resource-intelligence-inputs", 1, struct {
		Facts             terraform.Facts                `json:"facts"`
		Snapshots         []ResourceIntelligenceSnapshot `json:"snapshots"`
		ProviderSnapshots []assessment.ProviderSnapshot  `json:"provider_snapshots"`
	}{facts, snapshots, providerSnapshots})
	if err != nil {
		return "", fmt.Errorf("canonicalize accepted resource intelligence inputs: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (inputs AcceptedResourceIntelligenceInputs) validateSeal() error {
	if inputs.seal == "" {
		return fmt.Errorf("resource intelligence inputs were not loaded from an accepted data tree")
	}
	seal, err := acceptedResourceInputsSeal(inputs.Facts, inputs.Snapshots, inputs.ProviderSnapshots)
	if err != nil || seal != inputs.seal {
		return fmt.Errorf("verified resource intelligence inputs were changed after loading")
	}
	return nil
}

func cloneAcceptedProviderSnapshot(snapshot assessment.ProviderSnapshot) assessment.ProviderSnapshot {
	cloned := snapshot
	cloned.Coverage.Capabilities = make(map[string]string, len(snapshot.Coverage.Capabilities))
	for capability, status := range snapshot.Coverage.Capabilities {
		cloned.Coverage.Capabilities[capability] = status
	}
	cloned.Entries = append([]assessment.ProviderEntry(nil), snapshot.Entries...)
	for index := range cloned.Entries {
		cloned.Entries[index].IntelligenceSources = append([]assessment.ProviderIntelligenceSource(nil), snapshot.Entries[index].IntelligenceSources...)
	}
	return cloned
}

func readAcceptedDataRecord(dataRoot, logicalPath string, recordDigests map[string]string) ([]byte, error) {
	expectedDigest, exists := recordDigests[logicalPath]
	if !exists {
		return nil, fmt.Errorf("accepted data record is not indexed: %s", logicalPath)
	}
	pathParts := filepath.FromSlash(strings.TrimPrefix(logicalPath, "data/"))
	data, err := os.ReadFile(filepath.Join(dataRoot, pathParts))
	if err != nil {
		return nil, fmt.Errorf("read accepted data record: %w", err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != expectedDigest {
		return nil, fmt.Errorf("accepted data record digest changed while reading")
	}
	return data, nil
}

func containsAcceptedFacts(records []index.FactRecord, moduleID, sourceSHA, factsID string) bool {
	for _, record := range records {
		if record.ID == factsID && record.ModuleID == moduleID && record.SourceSHA == sourceSHA {
			return true
		}
	}
	return false
}

func containsAcceptedSource(records []index.SourceRevisionRecord, moduleID, sourceSHA string) bool {
	for _, record := range records {
		if record.ModuleID == moduleID && record.SourceSHA == sourceSHA && record.Immutable {
			return true
		}
	}
	return false
}

func containsAcceptedProviderSnapshot(providers []index.ProviderIndex, reference AcceptedResourceSnapshotReference) bool {
	for _, providerIndex := range providers {
		if providerIndex.ProviderAddress != reference.ProviderAddress {
			continue
		}
		for _, snapshot := range providerIndex.Snapshots {
			if snapshot.ID == reference.SnapshotID && snapshot.Address == reference.ProviderAddress && snapshot.Version == reference.ProviderVersion {
				return true
			}
		}
	}
	return false
}

func acceptedSnapshotMatches(snapshot assessment.ProviderSnapshot, reference AcceptedResourceSnapshotReference) bool {
	if snapshot.SchemaVersion != 2 || snapshot.RecordType != "provider_snapshot" || snapshot.SnapshotID != reference.SnapshotID ||
		snapshot.ProviderAddress != reference.ProviderAddress || snapshot.ProviderVersion != reference.ProviderVersion {
		return false
	}
	storedID := snapshot.SnapshotID
	snapshot.SnapshotID = ""
	expectedID, err := identity.ID("snapshot", snapshot.SchemaVersion, snapshot)
	return err == nil && storedID == expectedID
}

func resourcesFromFacts(facts terraform.Facts) map[string][]ResourceIntelligenceResource {
	resources := make(map[string]map[string]ResourceIntelligenceResource)
	for _, unit := range facts.Units {
		for _, symbol := range unit.Symbols {
			kind := symbol.Kind
			if kind == "data" {
				kind = "datasource"
			}
			if kind != "resource" && kind != "datasource" && kind != "ephemeral" || symbol.ProviderAddress == "" {
				continue
			}
			name, _, _ := strings.Cut(symbol.Name, ".")
			if name == "" {
				continue
			}
			if resources[symbol.ProviderAddress] == nil {
				resources[symbol.ProviderAddress] = make(map[string]ResourceIntelligenceResource)
			}
			key := kind + "\x00" + name
			resources[symbol.ProviderAddress][key] = ResourceIntelligenceResource{Kind: kind, Name: name, Sources: []ResourceIntelligenceSource{}}
		}
	}
	ordered := make(map[string][]ResourceIntelligenceResource, len(resources))
	for address, byKey := range resources {
		for _, resource := range byKey {
			ordered[address] = append(ordered[address], resource)
		}
		sort.Slice(ordered[address], func(left, right int) bool {
			return ordered[address][left].Kind+"/"+ordered[address][left].Name < ordered[address][right].Kind+"/"+ordered[address][right].Name
		})
	}
	return ordered
}

func resourceStubDigest(reference AcceptedResourceSnapshotReference, entries []assessment.ProviderEntry) (string, error) {
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Kind+"/"+entries[left].Name < entries[right].Kind+"/"+entries[right].Name
	})
	encoded, err := identity.CanonicalJSON("resource-stubs", 1, struct {
		ProviderAddress string                     `json:"provider_address"`
		ProviderVersion string                     `json:"provider_version"`
		SnapshotID      string                     `json:"snapshot_id"`
		Entries         []assessment.ProviderEntry `json:"entries"`
	}{reference.ProviderAddress, reference.ProviderVersion, reference.SnapshotID, entries})
	if err != nil {
		return "", fmt.Errorf("canonicalize accepted provider stubs: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
