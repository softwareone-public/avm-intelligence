package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
	"golang.org/x/mod/semver"
)

// ResourceIntelligenceProposal is an immutable candidate enrichment tied to
// one valid acquisition-facts identity and provider snapshot references. Its
// constructor does not prove those records came from the accepted data tree;
// a trusted source reader must establish that before acceptance. It is a
// proposal only and cannot advance provider state or wake an assessment.
type ResourceIntelligenceProposal struct {
	SchemaVersion int                                    `json:"schema_version"`
	RecordType    string                                 `json:"record_type"`
	ProposalID    string                                 `json:"proposal_id"`
	FactsScope    []ResourceIntelligenceFactsReference   `json:"facts_scope"`
	Snapshots     []ResourceIntelligenceProposalSnapshot `json:"snapshots"`
	CollectedAt   time.Time                              `json:"collected_at"`
	ContentDigest string                                 `json:"content_digest"`
}

// ResourceIntelligenceFactsReference binds the collection proposal to exact
// accepted module revisions and their content identities.
type ResourceIntelligenceFactsReference struct {
	ModuleID    string `json:"module_id"`
	SourceSHA   string `json:"source_sha"`
	FactsID     string `json:"facts_id"`
	FactsDigest string `json:"facts_digest"`
}

// ResourceIntelligenceSnapshot carries the asserted accepted-provider
// snapshot reference and exact provider version. The state-backed caller must
// verify SnapshotID and StubDigest against the accepted provider index before
// it treats this reference as trusted.
type ResourceIntelligenceSnapshot struct {
	ProviderAddress  string                         `json:"provider_address"`
	ProviderVersion  string                         `json:"provider_version"`
	SourceRepository string                         `json:"source_repository"`
	SnapshotID       string                         `json:"snapshot_id"`
	StubDigest       string                         `json:"stub_digest"`
	Resources        []ResourceIntelligenceResource `json:"resources"`
}

// ResourceIntelligenceResource identifies an accepted used type and lists its
// deduplicated source proposals; the list is empty when no source matches.
type ResourceIntelligenceResource struct {
	Kind    string                       `json:"kind"`
	Name    string                       `json:"name"`
	Sources []ResourceIntelligenceSource `json:"sources"`
}

// ResourceIntelligenceProposalSnapshot normalizes provider-wide source bodies
// once and uses content-bound IDs to associate them with the used types.
type ResourceIntelligenceProposalSnapshot struct {
	ProviderAddress  string                                    `json:"provider_address"`
	ProviderVersion  string                                    `json:"provider_version"`
	SourceRepository string                                    `json:"source_repository"`
	SnapshotID       string                                    `json:"snapshot_id"`
	StubDigest       string                                    `json:"stub_digest"`
	Sources          []assessment.ProviderIntelligenceDocument `json:"sources"`
	Resources        []ResourceIntelligenceProposalResource    `json:"resources"`
}

type ResourceIntelligenceProposalResource struct {
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	SourceIDs []string `json:"source_ids"`
}

// ResourceIntelligenceSource records a source-scoped, version-pinned public
// artifact. VersionBasis distinguishes a release tag, an issue observed during
// an accepted provider snapshot, and a versioned Registry guide. The issue
// basis describes observation time only, not affected-version evidence. Body
// is normalized source content; callers must enforce each source's reviewed
// allowlist and bounds before constructing this value.
type ResourceIntelligenceSource = assessment.ProviderIntelligenceSource

const resourceIntelligenceSchemaVersion = 3

const (
	maxResourceIntelligenceSourceBytes = 1 << 20
	maxResourceIntelligenceTotalBytes  = 10 << 20
	maxResourceIntelligenceResults     = 1000
	maxResourceIntelligenceReferences  = 10000
)

var resourceIntelligenceSourceKinds = map[string]bool{
	"provider_changelog": true,
	"github_release":     true,
	"resource_issue":     true,
	"registry_upgrade":   true,
}

// NewResourceIntelligenceProposal validates collected source results against
// inputs loaded from one accepted data tree, then calculates content-bound IDs.
// It performs no acceptance or state transition.
func NewResourceIntelligenceProposal(policy CollectionPolicy, accepted AcceptedResourceIntelligenceInputs, snapshots []ResourceIntelligenceSnapshot, collectedAt time.Time) (ResourceIntelligenceProposal, error) {
	if err := policy.Validate(); err != nil {
		return ResourceIntelligenceProposal{}, fmt.Errorf("validate resource intelligence collection policy: %w", err)
	}
	if err := accepted.validateSeal(); err != nil {
		return ResourceIntelligenceProposal{}, err
	}
	if len(accepted.FactsScope) == 0 || len(accepted.FactsScope) != len(accepted.FactReferences) || collectedAt.IsZero() {
		return ResourceIntelligenceProposal{}, fmt.Errorf("resource intelligence proposal input identity is invalid")
	}
	used := make(map[string]bool)
	for _, facts := range accepted.FactsScope {
		if err := facts.Validate(); err != nil {
			return ResourceIntelligenceProposal{}, fmt.Errorf("validate accepted acquisition facts: %w", err)
		}
		for key := range factResourceKeys(facts) {
			used[key] = true
		}
	}
	acceptedSnapshots := make(map[string]ResourceIntelligenceSnapshot, len(accepted.Snapshots))
	for _, acceptedSnapshot := range accepted.Snapshots {
		acceptedSnapshots[acceptedSnapshot.ProviderAddress+"\x00"+acceptedSnapshot.ProviderVersion+"\x00"+acceptedSnapshot.SnapshotID] = acceptedSnapshot
	}
	for index := range snapshots {
		snapshot := &snapshots[index]
		providerPolicy, found := providerPolicyForAddress(policy, snapshot.ProviderAddress)
		if !found || validateCatalogVersion(snapshot.ProviderVersion, providerPolicy.TrackMajors) != nil || identity.ValidateRecordID("snapshot", snapshot.SnapshotID) != nil {
			return ResourceIntelligenceProposal{}, fmt.Errorf("resource intelligence proposal does not reference an exact allowlisted provider snapshot")
		}
		acceptedSnapshot, acceptedFound := acceptedSnapshots[snapshot.ProviderAddress+"\x00"+snapshot.ProviderVersion+"\x00"+snapshot.SnapshotID]
		if !acceptedFound || snapshot.StubDigest != acceptedSnapshot.StubDigest || snapshot.SourceRepository != acceptedSnapshot.SourceRepository {
			return ResourceIntelligenceProposal{}, fmt.Errorf("resource intelligence proposal does not match its verified accepted provider stubs")
		}
		verifiedResources := make(map[string]struct{}, len(acceptedSnapshot.Resources))
		for _, resource := range acceptedSnapshot.Resources {
			verifiedResources[resource.Kind+"\x00"+resource.Name] = struct{}{}
		}
		for _, resource := range snapshot.Resources {
			key := snapshot.ProviderAddress + "\x00" + resource.Kind + "\x00" + resource.Name
			if !used[key] {
				return ResourceIntelligenceProposal{}, fmt.Errorf("resource intelligence proposal includes a resource absent from accepted facts")
			}
			if _, exists := verifiedResources[resource.Kind+"\x00"+resource.Name]; !exists {
				return ResourceIntelligenceProposal{}, fmt.Errorf("resource intelligence proposal includes a resource absent from verified provider stubs")
			}
			for _, source := range resource.Sources {
				if !validResourceIntelligenceSourceURL(source.Kind, source.URL, source.Version, providerPolicy) || !validResourceIntelligenceVersionBasis(source, snapshot.ProviderVersion) {
					return ResourceIntelligenceProposal{}, fmt.Errorf("resource intelligence source URL or version linkage is invalid")
				}
			}
		}
	}
	normalized, err := normalizeResourceSnapshots(snapshots)
	if err != nil {
		return ResourceIntelligenceProposal{}, err
	}
	proposalSnapshots, err := normalizeProposalSnapshots(normalized)
	if err != nil {
		return ResourceIntelligenceProposal{}, err
	}
	proposal := ResourceIntelligenceProposal{
		SchemaVersion: resourceIntelligenceSchemaVersion, RecordType: "resource_intelligence_proposal",
		FactsScope: append([]ResourceIntelligenceFactsReference(nil), accepted.FactReferences...),
		Snapshots:  proposalSnapshots, CollectedAt: collectedAt.UTC(),
	}
	proposal.ProposalID, err = resourceProposalID(proposal)
	if err != nil {
		return ResourceIntelligenceProposal{}, err
	}
	proposal.ContentDigest, err = resourceProposalDigest(proposal)
	if err != nil {
		return ResourceIntelligenceProposal{}, err
	}
	if err = proposal.Validate(); err != nil {
		return ResourceIntelligenceProposal{}, err
	}
	return proposal, nil
}

// ResourceIntelligenceProposalFailureClass returns an allowlisted category for
// operational logs without exposing validation details or source content.
func ResourceIntelligenceProposalFailureClass(err error) string {
	if err == nil {
		return "none"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "document count bound"):
		return "source_document_count_budget"
	case strings.Contains(message, "body byte bound"):
		return "source_document_byte_budget"
	case strings.Contains(message, "reference bound"):
		return "source_reference_budget"
	case strings.Contains(message, "bounded artifact budget"):
		return "artifact_budget"
	case strings.Contains(message, "source identity, content, or order"):
		return "source_integrity"
	case strings.Contains(message, "accepted facts") || strings.Contains(message, "provider stubs") || strings.Contains(message, "exact allowlisted provider snapshot"):
		return "accepted_input_scope"
	case strings.Contains(message, "digest") || strings.Contains(message, "proposal id"):
		return "proposal_identity"
	default:
		return "proposal_contract"
	}
}

// ResourceIntelligencePayloadStats measures the normalized proposal pool
// without returning or logging source content. Bodies are counted once per
// exact provider snapshot; references count each resource-to-source edge.
func ResourceIntelligencePayloadStats(snapshots []ResourceIntelligenceSnapshot) (documents, bodyBytes, references int, err error) {
	normalized, err := normalizeResourceSnapshots(snapshots)
	if err != nil {
		return 0, 0, 0, err
	}
	proposalSnapshots, err := normalizeProposalSnapshots(normalized)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, snapshot := range proposalSnapshots {
		documents += len(snapshot.Sources)
		for _, document := range snapshot.Sources {
			bodyBytes += len(document.Body)
		}
		for _, resource := range snapshot.Resources {
			references += len(resource.SourceIDs)
		}
	}
	return documents, bodyBytes, references, nil
}

// EnrichAcceptedProviderSnapshot applies one provider/version slice of a
// validated proposal to its accepted provider stubs, producing a new immutable
// provider snapshot. Callers still stage and human-accept the resulting
// provider-root proposal through the existing provider publication path.
func EnrichAcceptedProviderSnapshot(policy CollectionPolicy, accepted AcceptedResourceIntelligenceInputs, base assessment.ProviderSnapshot, proposal ResourceIntelligenceProposal, providerAddress, providerVersion string, producer assessment.SnapshotProducer, observedAt time.Time) (assessment.ProviderSnapshot, error) {
	if err := accepted.validateSeal(); err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	collectedSnapshots, err := proposalSnapshotsToCollected(proposal.Snapshots)
	if err != nil {
		return assessment.ProviderSnapshot{}, fmt.Errorf("resource intelligence proposal references are invalid")
	}
	verifiedProposal, err := NewResourceIntelligenceProposal(policy, accepted, collectedSnapshots, proposal.CollectedAt)
	if err != nil || verifiedProposal.ProposalID != proposal.ProposalID || verifiedProposal.ContentDigest != proposal.ContentDigest {
		return assessment.ProviderSnapshot{}, fmt.Errorf("resource intelligence proposal is not valid for the sealed accepted inputs and reviewed policy")
	}
	if err := assessment.ValidateProviderSnapshot(base); err != nil {
		return assessment.ProviderSnapshot{}, fmt.Errorf("validate accepted provider snapshot: %w", err)
	}
	original := base
	base.Entries = append([]assessment.ProviderEntry(nil), base.Entries...)
	for index := range base.Entries {
		base.Entries[index].IntelligenceSourceIDs = append([]string(nil), base.Entries[index].IntelligenceSourceIDs...)
		base.Entries[index].IntelligenceSources = append([]assessment.ProviderIntelligenceSource(nil), base.Entries[index].IntelligenceSources...)
	}
	base.IntelligenceSources = append([]assessment.ProviderIntelligenceDocument(nil), base.IntelligenceSources...)
	if observedAt.IsZero() || producer.ID == "" || producer.Version == "" || producer.ContractVersion < 1 {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider intelligence producer and observation time are required")
	}
	var enriched ResourceIntelligenceProposalSnapshot
	found := false
	for _, snapshot := range proposal.Snapshots {
		if snapshot.ProviderAddress == providerAddress && snapshot.ProviderVersion == providerVersion && snapshot.SnapshotID == base.SnapshotID {
			if found {
				return assessment.ProviderSnapshot{}, fmt.Errorf("resource intelligence proposal repeats a provider snapshot")
			}
			enriched, found = snapshot, true
		}
	}
	if !found || base.ProviderAddress != providerAddress || base.ProviderVersion != providerVersion {
		return assessment.ProviderSnapshot{}, fmt.Errorf("resource intelligence proposal does not reference the exact accepted provider snapshot")
	}
	acceptedSnapshotFound := false
	for _, snapshot := range accepted.Snapshots {
		if snapshot.ProviderAddress == providerAddress && snapshot.ProviderVersion == providerVersion && snapshot.SnapshotID == base.SnapshotID && snapshot.StubDigest == enriched.StubDigest && snapshot.SourceRepository == enriched.SourceRepository {
			acceptedSnapshotFound = true
			break
		}
	}
	if !acceptedSnapshotFound || !acceptedSnapshotMatches(base, AcceptedResourceSnapshotReference{ProviderAddress: providerAddress, ProviderVersion: providerVersion, SnapshotID: base.SnapshotID}) {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider snapshot does not match the sealed accepted provider stubs")
	}
	entriesByKey := make(map[string]int, len(base.Entries))
	for index := range base.Entries {
		entriesByKey[base.Entries[index].Kind+"\x00"+base.Entries[index].Name] = index
	}
	acceptedRef := AcceptedResourceSnapshotReference{ProviderAddress: providerAddress, ProviderVersion: providerVersion, SnapshotID: base.SnapshotID}
	stubEntries := make([]assessment.ProviderEntry, 0, len(enriched.Resources))
	proposalSources := make(map[string]assessment.ProviderIntelligenceDocument, len(enriched.Sources))
	for _, document := range enriched.Sources {
		proposalSources[document.SourceID] = document
	}
	documents := make(map[string]assessment.ProviderIntelligenceDocument, len(base.IntelligenceSources)+len(enriched.Sources))
	for _, document := range base.IntelligenceSources {
		documents[document.SourceID] = document
	}
	changed := false
	if base.SchemaVersion == 2 {
		for index := range base.Entries {
			entry := &base.Entries[index]
			for _, source := range entry.IntelligenceSources {
				sourceID, idErr := assessment.ProviderIntelligenceDocumentID(source)
				if idErr != nil {
					return assessment.ProviderSnapshot{}, fmt.Errorf("identify existing provider intelligence source")
				}
				documents[sourceID] = assessment.ProviderIntelligenceDocument{SourceID: sourceID, ProviderIntelligenceSource: source}
				entry.IntelligenceSourceIDs = append(entry.IntelligenceSourceIDs, sourceID)
			}
			if len(entry.IntelligenceSources) != 0 {
				entry.IntelligenceSources = nil
				entry.IntelligenceSourceIDs = uniqueStrings(entry.IntelligenceSourceIDs)
				changed = true
			}
		}
	}
	for _, resource := range enriched.Resources {
		entryIndex, exists := entriesByKey[resource.Kind+"\x00"+resource.Name]
		if !exists {
			return assessment.ProviderSnapshot{}, fmt.Errorf("provider proposal references a missing accepted stub %s/%s", resource.Kind, resource.Name)
		}
		entry := base.Entries[entryIndex]
		stubEntries = append(stubEntries, original.Entries[entryIndex])
		before := append([]string(nil), entry.IntelligenceSourceIDs...)
		for _, sourceID := range resource.SourceIDs {
			document, exists := proposalSources[sourceID]
			if !exists {
				return assessment.ProviderSnapshot{}, fmt.Errorf("provider proposal source reference has no matching document")
			}
			if existing, exists := documents[sourceID]; exists && existing != document {
				return assessment.ProviderSnapshot{}, fmt.Errorf("provider proposal source conflicts with an existing document")
			}
			documents[sourceID] = document
			entry.IntelligenceSourceIDs = append(entry.IntelligenceSourceIDs, sourceID)
		}
		sort.Strings(entry.IntelligenceSourceIDs)
		entry.IntelligenceSourceIDs = uniqueStrings(entry.IntelligenceSourceIDs)
		if !equalStrings(before, entry.IntelligenceSourceIDs) {
			changed = true
		}
		base.Entries[entryIndex] = entry
	}
	stubDigest, err := resourceStubDigest(acceptedRef, stubEntries)
	if err != nil || stubDigest != enriched.StubDigest {
		return assessment.ProviderSnapshot{}, fmt.Errorf("resource intelligence proposal provider stub digest does not match accepted stubs")
	}
	if !changed {
		return original, nil
	}
	base.SchemaVersion = 3
	base.IntelligenceSources = make([]assessment.ProviderIntelligenceDocument, 0, len(documents))
	for _, document := range documents {
		base.IntelligenceSources = append(base.IntelligenceSources, document)
	}
	sort.Slice(base.IntelligenceSources, func(left, right int) bool {
		return base.IntelligenceSources[left].SourceID < base.IntelligenceSources[right].SourceID
	})
	base.Producer = producer
	base.ObservedAt = observedAt.UTC().Format(time.RFC3339Nano)
	base.SourceDigest, err = enrichedProviderSourceDigest(base.SourceDigest, enriched.Sources, enriched.Resources)
	if err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	base.SnapshotID = ""
	base.SnapshotID, err = identity.ID("snapshot", base.SchemaVersion, base)
	if err != nil {
		return assessment.ProviderSnapshot{}, fmt.Errorf("identify enriched provider snapshot: %w", err)
	}
	if err = assessment.ValidateProviderSnapshot(base); err != nil {
		return assessment.ProviderSnapshot{}, fmt.Errorf("validate enriched provider snapshot: %w", err)
	}
	return base, nil
}

// EnrichAcceptedProviderSnapshots returns only changed immutable provider
// snapshots. Each result remains independently staged and accepted by the
// provider publication path.
func EnrichAcceptedProviderSnapshots(policy CollectionPolicy, accepted AcceptedResourceIntelligenceInputs, proposal ResourceIntelligenceProposal, producer assessment.SnapshotProducer, observedAt time.Time) ([]assessment.ProviderSnapshot, error) {
	if err := accepted.validateSeal(); err != nil {
		return nil, err
	}
	if len(accepted.ProviderSnapshots) != len(accepted.Snapshots) {
		return nil, fmt.Errorf("sealed provider snapshot manifests do not match accepted stub references")
	}
	results := make([]assessment.ProviderSnapshot, 0, len(accepted.ProviderSnapshots))
	for _, base := range accepted.ProviderSnapshots {
		enriched, err := EnrichAcceptedProviderSnapshot(policy, accepted, base, proposal, base.ProviderAddress, base.ProviderVersion, producer, observedAt)
		if err != nil {
			return nil, err
		}
		if enriched.SnapshotID != base.SnapshotID {
			results = append(results, enriched)
		}
	}
	return results, nil
}

func resourceIntelligenceSourceKey(source ResourceIntelligenceSource) string {
	return source.Kind + "\x00" + source.Version + "\x00" + source.VersionBasis + "\x00" + source.URL + "\x00" + source.ContentDigest + "\x00" + source.ObservedAt.UTC().Format(time.RFC3339Nano)
}

func enrichedProviderSourceDigest(previous string, sources []assessment.ProviderIntelligenceDocument, resources []ResourceIntelligenceProposalResource) (string, error) {
	encoded, err := identity.CanonicalJSON("provider-resource-intelligence", 1, struct {
		Previous  string                                    `json:"previous_source_digest"`
		Sources   []assessment.ProviderIntelligenceDocument `json:"sources"`
		Resources []ResourceIntelligenceProposalResource    `json:"resources"`
	}{previous, sources, resources})
	if err != nil {
		return "", fmt.Errorf("canonicalize enriched provider sources: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func factResourceKeys(facts terraform.Facts) map[string]bool {
	keys := make(map[string]bool)
	for _, unit := range facts.Units {
		for _, symbol := range unit.Symbols {
			if terraform.IsBuiltinTerraformSymbol(symbol) {
				continue
			}
			kind := symbol.Kind
			if kind == "data" {
				kind = "datasource"
			}
			if kind != "resource" && kind != "datasource" && kind != "ephemeral" {
				continue
			}
			name, _, _ := strings.Cut(symbol.Name, ".")
			if name == "" || symbol.ProviderAddress == "" {
				continue
			}
			address := terraform.CanonicalProviderAddress(symbol.ProviderAddress)
			if address != "" {
				keys[address+"\x00"+kind+"\x00"+name] = true
			}
		}
	}
	return keys
}

func normalizeResourceSnapshots(input []ResourceIntelligenceSnapshot) ([]ResourceIntelligenceSnapshot, error) {
	output := cloneResourceSnapshots(input)
	for i := range output {
		resources := output[i].Resources[:0]
		for _, resource := range output[i].Resources {
			if len(resources) == 0 || resources[len(resources)-1].Kind+"/"+resources[len(resources)-1].Name != resource.Kind+"/"+resource.Name {
				resources = append(resources, resource)
				continue
			}
			last := &resources[len(resources)-1]
			last.Sources = append(last.Sources, resource.Sources...)
		}
		output[i].Resources = resources
		for j := range output[i].Resources {
			sources := output[i].Resources[j].Sources
			if sources == nil {
				sources = []ResourceIntelligenceSource{}
				output[i].Resources[j].Sources = sources
			}
			unique := sources[:0]
			for _, source := range sources {
				if len(unique) > 0 {
					prior := unique[len(unique)-1]
					if resourceIntelligenceSourceKey(prior) == resourceIntelligenceSourceKey(source) {
						continue
					}
				}
				unique = append(unique, source)
			}
			output[i].Resources[j].Sources = unique
		}
	}
	return output, nil
}

func normalizeProposalSnapshots(input []ResourceIntelligenceSnapshot) ([]ResourceIntelligenceProposalSnapshot, error) {
	output := make([]ResourceIntelligenceProposalSnapshot, 0, len(input))
	for _, snapshot := range input {
		documents := make(map[string]assessment.ProviderIntelligenceDocument)
		resources := make([]ResourceIntelligenceProposalResource, 0, len(snapshot.Resources))
		for _, resource := range snapshot.Resources {
			resourceIDs := make([]string, 0, len(resource.Sources))
			for _, source := range resource.Sources {
				sourceID, err := assessment.ProviderIntelligenceDocumentID(source)
				if err != nil {
					return nil, fmt.Errorf("identify provider intelligence source: %w", err)
				}
				document := assessment.ProviderIntelligenceDocument{SourceID: sourceID, ProviderIntelligenceSource: source}
				if previous, exists := documents[sourceID]; exists && previous != document {
					return nil, fmt.Errorf("provider intelligence source ID collision")
				}
				documents[sourceID] = document
				resourceIDs = append(resourceIDs, sourceID)
			}
			sort.Strings(resourceIDs)
			resourceIDs = uniqueStrings(resourceIDs)
			resources = append(resources, ResourceIntelligenceProposalResource{Kind: resource.Kind, Name: resource.Name, SourceIDs: resourceIDs})
		}
		sources := make([]assessment.ProviderIntelligenceDocument, 0, len(documents))
		for _, document := range documents {
			sources = append(sources, document)
		}
		sort.Slice(sources, func(left, right int) bool { return sources[left].SourceID < sources[right].SourceID })
		output = append(output, ResourceIntelligenceProposalSnapshot{
			ProviderAddress: snapshot.ProviderAddress, ProviderVersion: snapshot.ProviderVersion,
			SourceRepository: snapshot.SourceRepository, SnapshotID: snapshot.SnapshotID,
			StubDigest: snapshot.StubDigest, Sources: sources, Resources: resources,
		})
	}
	return output, nil
}

func proposalSnapshotsToCollected(input []ResourceIntelligenceProposalSnapshot) ([]ResourceIntelligenceSnapshot, error) {
	output := make([]ResourceIntelligenceSnapshot, 0, len(input))
	for _, snapshot := range input {
		documents := make(map[string]ResourceIntelligenceSource, len(snapshot.Sources))
		for _, document := range snapshot.Sources {
			expected, err := assessment.ProviderIntelligenceDocumentID(document.ProviderIntelligenceSource)
			if err != nil || document.SourceID != expected {
				return nil, fmt.Errorf("provider intelligence source identity is invalid")
			}
			if _, duplicate := documents[document.SourceID]; duplicate {
				return nil, fmt.Errorf("provider intelligence source is duplicated")
			}
			documents[document.SourceID] = document.ProviderIntelligenceSource
		}
		resources := make([]ResourceIntelligenceResource, 0, len(snapshot.Resources))
		for _, resource := range snapshot.Resources {
			sources := make([]ResourceIntelligenceSource, 0, len(resource.SourceIDs))
			for _, sourceID := range resource.SourceIDs {
				source, exists := documents[sourceID]
				if !exists {
					return nil, fmt.Errorf("provider intelligence source reference is unresolved")
				}
				sources = append(sources, source)
			}
			resources = append(resources, ResourceIntelligenceResource{Kind: resource.Kind, Name: resource.Name, Sources: sources})
		}
		output = append(output, ResourceIntelligenceSnapshot{
			ProviderAddress: snapshot.ProviderAddress, ProviderVersion: snapshot.ProviderVersion,
			SourceRepository: snapshot.SourceRepository, SnapshotID: snapshot.SnapshotID,
			StubDigest: snapshot.StubDigest, Resources: resources,
		})
	}
	return output, nil
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	unique := values[:1]
	for _, value := range values[1:] {
		if value != unique[len(unique)-1] {
			unique = append(unique, value)
		}
	}
	return unique
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// Validate verifies proposal identity, pinned inputs, source digests, and
// canonical ordering so retries cannot silently replace previously proposed data.
func (proposal ResourceIntelligenceProposal) Validate() error {
	if proposal.SchemaVersion != resourceIntelligenceSchemaVersion || proposal.RecordType != "resource_intelligence_proposal" || len(proposal.FactsScope) == 0 || len(proposal.FactsScope) > maxScopedFactsReferences || proposal.CollectedAt.IsZero() || len(proposal.Snapshots) == 0 {
		return fmt.Errorf("resource intelligence proposal metadata is invalid")
	}
	previousModule := ""
	for _, reference := range proposal.FactsScope {
		if _, err := identity.ModuleSourcePath(reference.ModuleID, reference.SourceSHA); err != nil || reference.ModuleID <= previousModule || identity.ValidateRecordID("facts", reference.FactsID) != nil || !isSHA256Digest(reference.FactsDigest) {
			return fmt.Errorf("resource intelligence facts scope identity or order is invalid")
		}
		previousModule = reference.ModuleID
	}
	previousSnapshot := ""
	var totalBytes int
	resultCount := 0
	referenceCount := 0
	for _, snapshot := range proposal.Snapshots {
		snapshotKey := resourceProposalSnapshotKey(snapshot)
		if !providerAddressPattern.MatchString(snapshot.ProviderAddress) || !providerRepositoryPattern.MatchString(snapshot.SourceRepository) || !validStableSourceVersion(snapshot.ProviderVersion) || identity.ValidateRecordID("snapshot", snapshot.SnapshotID) != nil || !isSHA256Digest(snapshot.StubDigest) || snapshotKey <= previousSnapshot || len(snapshot.Resources) == 0 {
			return fmt.Errorf("resource intelligence snapshot identity or order is invalid")
		}
		previousSnapshot = snapshotKey
		documents := make(map[string]assessment.ProviderIntelligenceDocument, len(snapshot.Sources))
		referenced := make(map[string]struct{})
		previousSourceID := ""
		for _, document := range snapshot.Sources {
			if document.SourceID <= previousSourceID {
				return fmt.Errorf("resource intelligence source documents are unordered")
			}
			expectedID, idErr := assessment.ProviderIntelligenceDocumentID(document.ProviderIntelligenceSource)
			source := document.ProviderIntelligenceSource
			if idErr != nil || document.SourceID != expectedID || !resourceIntelligenceSourceKinds[source.Kind] || !validStableSourceVersion(source.Version) || !validResourceIntelligenceSourceURL(source.Kind, source.URL, source.Version, ProviderPolicy{Address: snapshot.ProviderAddress, SourceRepository: snapshot.SourceRepository}) || !validResourceIntelligenceVersionBasis(source, snapshot.ProviderVersion) || !isSHA256Digest(source.ContentDigest) || source.ObservedAt.IsZero() || source.Body == "" || len(source.Body) > maxResourceIntelligenceSourceBytes || digestText(source.Body) != source.ContentDigest {
				return fmt.Errorf("resource intelligence source identity, content, or order is invalid")
			}
			previousSourceID = document.SourceID
			documents[document.SourceID] = document
			totalBytes += len(source.Body)
			resultCount++
		}
		previousResource := ""
		for _, resource := range snapshot.Resources {
			key := resource.Kind + "/" + resource.Name
			if (resource.Kind != "resource" && resource.Kind != "datasource" && resource.Kind != "ephemeral") || resource.Name == "" || key <= previousResource {
				return fmt.Errorf("resource intelligence resource identity or order is invalid")
			}
			previousResource = key
			previousReference := ""
			for _, sourceID := range resource.SourceIDs {
				if sourceID <= previousReference {
					return fmt.Errorf("resource intelligence source references are invalid or unordered")
				}
				if _, exists := documents[sourceID]; !exists {
					return fmt.Errorf("resource intelligence source reference has no matching document")
				}
				referenced[sourceID] = struct{}{}
				referenceCount++
				previousReference = sourceID
			}
		}
		if len(referenced) != len(documents) {
			return fmt.Errorf("resource intelligence proposal contains unreferenced source documents")
		}
	}
	if resultCount > maxResourceIntelligenceResults {
		return fmt.Errorf("resource intelligence proposal exceeds its source document count bound")
	}
	if totalBytes > maxResourceIntelligenceTotalBytes {
		return fmt.Errorf("resource intelligence proposal exceeds its source document body byte bound")
	}
	if referenceCount > maxResourceIntelligenceReferences {
		return fmt.Errorf("resource intelligence proposal exceeds its source reference bound")
	}
	if proposal.ProposalID == "" || proposal.ContentDigest == "" {
		return fmt.Errorf("resource intelligence proposal identity is missing")
	}
	contentDigest, err := resourceProposalDigest(proposal)
	if err != nil || contentDigest != proposal.ContentDigest {
		return fmt.Errorf("resource intelligence proposal content digest does not match")
	}
	proposalID, err := resourceProposalID(proposal)
	if err != nil || proposalID != proposal.ProposalID {
		return fmt.Errorf("resource intelligence proposal ID does not match")
	}
	return nil
}

// validResourceIntelligenceVersionBasis keeps the meaning of a source's
// provider version explicit. An issue's observed snapshot version is temporal
// context only; it does not assert that the issue affects that version.
func validResourceIntelligenceVersionBasis(source ResourceIntelligenceSource, snapshotVersion string) bool {
	switch source.Kind {
	case "provider_changelog", "github_release":
		return source.VersionBasis == "release_tag"
	case "resource_issue":
		return source.VersionBasis == "provider_snapshot" && source.Version == snapshotVersion
	case "registry_upgrade":
		if source.VersionBasis != "registry_path" {
			return false
		}
		parsed, err := url.Parse(source.URL)
		if err != nil {
			return false
		}
		want := strings.TrimPrefix(source.Version, "v")
		for _, segment := range strings.Split(strings.Trim(parsed.Path, "/"), "/") {
			if strings.TrimPrefix(segment, "v") == want {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func resourceProposalID(proposal ResourceIntelligenceProposal) (string, error) {
	return identity.ID("resource-intelligence-proposal", resourceIntelligenceSchemaVersion, struct {
		FactsScope []ResourceIntelligenceFactsReference   `json:"facts_scope"`
		Snapshots  []ResourceIntelligenceProposalSnapshot `json:"snapshots"`
	}{proposal.FactsScope, proposal.Snapshots})
}

func resourceProposalDigest(proposal ResourceIntelligenceProposal) (string, error) {
	proposal.ProposalID = ""
	proposal.ContentDigest = ""
	encoded, err := identity.CanonicalJSON("resource-intelligence-proposal", resourceIntelligenceSchemaVersion, proposal)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneResourceSnapshots(input []ResourceIntelligenceSnapshot) []ResourceIntelligenceSnapshot {
	output := append([]ResourceIntelligenceSnapshot(nil), input...)
	for i := range output {
		output[i].Resources = append([]ResourceIntelligenceResource(nil), input[i].Resources...)
		for j := range output[i].Resources {
			output[i].Resources[j].Sources = append([]ResourceIntelligenceSource(nil), input[i].Resources[j].Sources...)
		}
		sort.Slice(output[i].Resources, func(a, b int) bool {
			return output[i].Resources[a].Kind+"/"+output[i].Resources[a].Name < output[i].Resources[b].Kind+"/"+output[i].Resources[b].Name
		})
		for j := range output[i].Resources {
			sort.Slice(output[i].Resources[j].Sources, func(a, b int) bool {
				x, y := output[i].Resources[j].Sources[a], output[i].Resources[j].Sources[b]
				return resourceIntelligenceSourceKey(x) < resourceIntelligenceSourceKey(y)
			})
		}
	}
	sort.Slice(output, func(i, j int) bool { return resourceSnapshotKey(output[i]) < resourceSnapshotKey(output[j]) })
	return output
}

func resourceSnapshotKey(snapshot ResourceIntelligenceSnapshot) string {
	return snapshot.ProviderAddress + "\x00" + snapshot.ProviderVersion + "\x00" + snapshot.SnapshotID
}

func resourceProposalSnapshotKey(snapshot ResourceIntelligenceProposalSnapshot) string {
	return snapshot.ProviderAddress + "\x00" + snapshot.ProviderVersion + "\x00" + snapshot.SnapshotID
}

func validPublicHTTPSURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(raw, "\r\n") {
		return false
	}
	for _, segment := range strings.Split(strings.ToLower(parsed.Path), "/") {
		if segment == "latest" || segment == "main" || segment == "master" || segment == "head" {
			return false
		}
	}
	return true
}

func validStableSourceVersion(version string) bool {
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return semver.IsValid(version) && semver.Canonical(version) == version && semver.Prerelease(version) == "" && semver.Build(version) == ""
}

func validResourceIntelligenceSourceURL(kind, rawURL, version string, policy ProviderPolicy) bool {
	if !validPublicHTTPSURL(rawURL) || !providerRepositoryPattern.MatchString(policy.SourceRepository) {
		return false
	}
	parsed, _ := url.Parse(rawURL)
	owner, repository, _ := strings.Cut(policy.SourceRepository, "/")
	path := strings.Trim(parsed.Path, "/")
	segments := strings.Split(path, "/")
	githubRepo := len(segments) >= 2 && segments[0] == owner && segments[1] == repository
	switch kind {
	case "provider_changelog":
		return parsed.Host == "github.com" && githubRepo && len(segments) >= 5 && segments[2] == "blob" && versionMatchesTag(segments[3], version)
	case "github_release":
		return parsed.Host == "github.com" && githubRepo && len(segments) == 5 && segments[2] == "releases" && segments[3] == "tag" && versionMatchesTag(segments[4], version)
	case "resource_issue":
		if parsed.Host != "github.com" || !githubRepo || len(segments) != 4 || segments[2] != "issues" {
			return false
		}
		for _, digit := range segments[3] {
			if digit < '0' || digit > '9' {
				return false
			}
		}
		return segments[3] != "" && segments[3] != "0"
	case "registry_upgrade":
		parts := strings.Split(policy.Address, "/")
		return parsed.Host == "registry.terraform.io" && len(parts) == 3 && len(segments) >= 4 && segments[0] == "providers" && segments[1] == parts[1] && segments[2] == parts[2]
	default:
		return false
	}
}

func versionMatchesTag(tag, version string) bool {
	return tag == version || tag == strings.TrimPrefix(version, "v") || "v"+tag == strings.TrimPrefix(version, "v") || tag == "v"+version
}

func digestText(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}
func isSHA256Digest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
