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
	SchemaVersion int                            `json:"schema_version"`
	RecordType    string                         `json:"record_type"`
	ProposalID    string                         `json:"proposal_id"`
	ModuleID      string                         `json:"module_id"`
	FactsID       string                         `json:"facts_id"`
	FactsDigest   string                         `json:"facts_digest"`
	Snapshots     []ResourceIntelligenceSnapshot `json:"snapshots"`
	CollectedAt   time.Time                      `json:"collected_at"`
	ContentDigest string                         `json:"content_digest"`
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

// ResourceIntelligenceResource identifies a resource or data source in the
// accepted acquisition facts and lists deduplicated source proposals.
type ResourceIntelligenceResource struct {
	Kind    string                       `json:"kind"`
	Name    string                       `json:"name"`
	Sources []ResourceIntelligenceSource `json:"sources"`
}

// ResourceIntelligenceSource records a source-scoped, version-pinned public
// artifact. VersionBasis distinguishes a release tag, an issue observed during
// an accepted provider snapshot, and a versioned Registry guide. The issue
// basis describes observation time only, not affected-version evidence. Body
// is normalized source content; callers must enforce each source's reviewed
// allowlist and bounds before constructing this value.
type ResourceIntelligenceSource = assessment.ProviderIntelligenceSource

const resourceIntelligenceSchemaVersion = 1

const (
	maxResourceIntelligenceSourceBytes = 1 << 20
	maxResourceIntelligenceTotalBytes  = 10 << 20
	maxResourceIntelligenceResults     = 1000
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
	facts := accepted.Facts
	if err := facts.Validate(); err != nil {
		return ResourceIntelligenceProposal{}, fmt.Errorf("validate accepted acquisition facts: %w", err)
	}
	if facts.ModuleID == "" || collectedAt.IsZero() {
		return ResourceIntelligenceProposal{}, fmt.Errorf("resource intelligence proposal input identity is invalid")
	}
	used := factResourceKeys(facts)
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
	proposal := ResourceIntelligenceProposal{
		SchemaVersion: resourceIntelligenceSchemaVersion, RecordType: "resource_intelligence_proposal",
		ModuleID: facts.ModuleID, FactsID: facts.FactsID, FactsDigest: facts.ContentDigest,
		Snapshots: normalized, CollectedAt: collectedAt.UTC(),
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

// EnrichAcceptedProviderSnapshot applies one provider/version slice of a
// validated proposal to its accepted provider stubs, producing a new immutable
// provider snapshot. Callers still stage and human-accept the resulting
// provider-root proposal through the existing provider publication path.
func EnrichAcceptedProviderSnapshot(policy CollectionPolicy, accepted AcceptedResourceIntelligenceInputs, base assessment.ProviderSnapshot, proposal ResourceIntelligenceProposal, providerAddress, providerVersion string, producer assessment.SnapshotProducer, observedAt time.Time) (assessment.ProviderSnapshot, error) {
	if err := accepted.validateSeal(); err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	verifiedProposal, err := NewResourceIntelligenceProposal(policy, accepted, proposal.Snapshots, proposal.CollectedAt)
	if err != nil || verifiedProposal.ProposalID != proposal.ProposalID || verifiedProposal.ContentDigest != proposal.ContentDigest {
		return assessment.ProviderSnapshot{}, fmt.Errorf("resource intelligence proposal is not valid for the sealed accepted inputs and reviewed policy")
	}
	if err := assessment.ValidateProviderSnapshot(base); err != nil {
		return assessment.ProviderSnapshot{}, fmt.Errorf("validate accepted provider snapshot: %w", err)
	}
	base.Entries = append([]assessment.ProviderEntry(nil), base.Entries...)
	for index := range base.Entries {
		base.Entries[index].IntelligenceSources = append([]assessment.ProviderIntelligenceSource(nil), base.Entries[index].IntelligenceSources...)
	}
	if observedAt.IsZero() || producer.ID == "" || producer.Version == "" || producer.ContractVersion < 1 {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider intelligence producer and observation time are required")
	}
	var enriched ResourceIntelligenceSnapshot
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
	addedSources := 0
	for _, resource := range enriched.Resources {
		entryIndex, exists := entriesByKey[resource.Kind+"\x00"+resource.Name]
		if !exists {
			return assessment.ProviderSnapshot{}, fmt.Errorf("provider proposal references a missing accepted stub %s/%s", resource.Kind, resource.Name)
		}
		entry := base.Entries[entryIndex]
		stubEntries = append(stubEntries, entry)
		existingSources := make(map[string]struct{}, len(entry.IntelligenceSources))
		for _, source := range entry.IntelligenceSources {
			existingSources[resourceIntelligenceSourceKey(source)] = struct{}{}
		}
		for _, source := range resource.Sources {
			if _, exists := existingSources[resourceIntelligenceSourceKey(source)]; !exists {
				addedSources++
			}
		}
		entry.IntelligenceSources = append(entry.IntelligenceSources, resource.Sources...)
		sort.Slice(entry.IntelligenceSources, func(left, right int) bool {
			x, y := entry.IntelligenceSources[left], entry.IntelligenceSources[right]
			return resourceIntelligenceSourceKey(x) < resourceIntelligenceSourceKey(y)
		})
		entry.IntelligenceSources = uniqueProviderIntelligenceSources(entry.IntelligenceSources)
		base.Entries[entryIndex] = entry
	}
	stubDigest, err := resourceStubDigest(acceptedRef, stubEntries)
	if err != nil || stubDigest != enriched.StubDigest {
		return assessment.ProviderSnapshot{}, fmt.Errorf("resource intelligence proposal provider stub digest does not match accepted stubs")
	}
	if addedSources == 0 {
		return base, nil
	}
	base.Producer = producer
	base.ObservedAt = observedAt.UTC().Format(time.RFC3339Nano)
	base.SourceDigest, err = enrichedProviderSourceDigest(base.SourceDigest, enriched.Resources)
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

func uniqueProviderIntelligenceSources(sources []assessment.ProviderIntelligenceSource) []assessment.ProviderIntelligenceSource {
	unique := sources[:0]
	for _, source := range sources {
		if len(unique) > 0 {
			previous := unique[len(unique)-1]
			if resourceIntelligenceSourceKey(previous) == resourceIntelligenceSourceKey(source) {
				continue
			}
		}
		unique = append(unique, source)
	}
	return unique
}

func resourceIntelligenceSourceKey(source ResourceIntelligenceSource) string {
	return source.Kind + "\x00" + source.Version + "\x00" + source.VersionBasis + "\x00" + source.URL + "\x00" + source.ContentDigest
}

func enrichedProviderSourceDigest(previous string, resources []ResourceIntelligenceResource) (string, error) {
	encoded, err := identity.CanonicalJSON("provider-resource-intelligence", 1, struct {
		Previous  string                         `json:"previous_source_digest"`
		Resources []ResourceIntelligenceResource `json:"resources"`
	}{previous, resources})
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

// Validate verifies proposal identity, pinned inputs, source digests, and
// canonical ordering so retries cannot silently replace previously proposed data.
func (proposal ResourceIntelligenceProposal) Validate() error {
	if proposal.SchemaVersion != resourceIntelligenceSchemaVersion || proposal.RecordType != "resource_intelligence_proposal" || proposal.ModuleID == "" || identity.ValidateRecordID("facts", proposal.FactsID) != nil || !isSHA256Digest(proposal.FactsDigest) || proposal.CollectedAt.IsZero() || len(proposal.Snapshots) == 0 {
		return fmt.Errorf("resource intelligence proposal metadata is invalid")
	}
	previousSnapshot := ""
	var totalBytes int
	resultCount := 0
	for _, snapshot := range proposal.Snapshots {
		snapshotKey := resourceSnapshotKey(snapshot)
		if !providerAddressPattern.MatchString(snapshot.ProviderAddress) || !providerRepositoryPattern.MatchString(snapshot.SourceRepository) || !validStableSourceVersion(snapshot.ProviderVersion) || identity.ValidateRecordID("snapshot", snapshot.SnapshotID) != nil || !isSHA256Digest(snapshot.StubDigest) || snapshotKey <= previousSnapshot || len(snapshot.Resources) == 0 {
			return fmt.Errorf("resource intelligence snapshot identity or order is invalid")
		}
		previousSnapshot = snapshotKey
		previousResource := ""
		for _, resource := range snapshot.Resources {
			key := resource.Kind + "/" + resource.Name
			if (resource.Kind != "resource" && resource.Kind != "datasource" && resource.Kind != "ephemeral") || resource.Name == "" || key <= previousResource || len(resource.Sources) == 0 {
				return fmt.Errorf("resource intelligence resource identity or order is invalid")
			}
			previousResource = key
			previousSource := ""
			for _, source := range resource.Sources {
				resultCount++
				totalBytes += len(source.Body)
				sourceKey := resourceIntelligenceSourceKey(source)
				if !resourceIntelligenceSourceKinds[source.Kind] || !validStableSourceVersion(source.Version) || !validResourceIntelligenceSourceURL(source.Kind, source.URL, source.Version, ProviderPolicy{Address: snapshot.ProviderAddress, SourceRepository: snapshot.SourceRepository}) || !validResourceIntelligenceVersionBasis(source, snapshot.ProviderVersion) || !isSHA256Digest(source.ContentDigest) || source.ObservedAt.IsZero() || sourceKey <= previousSource || source.Body == "" || len(source.Body) > maxResourceIntelligenceSourceBytes || digestText(source.Body) != source.ContentDigest {
					return fmt.Errorf("resource intelligence source identity, content, or order is invalid")
				}
				previousSource = sourceKey
			}
		}
	}
	if resultCount > maxResourceIntelligenceResults || totalBytes > maxResourceIntelligenceTotalBytes {
		return fmt.Errorf("resource intelligence proposal exceeds its bounded artifact budget")
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
	return identity.ID("resource-intelligence-proposal", 1, struct {
		ModuleID    string                         `json:"module_id"`
		FactsID     string                         `json:"facts_id"`
		FactsDigest string                         `json:"facts_digest"`
		Snapshots   []ResourceIntelligenceSnapshot `json:"snapshots"`
	}{proposal.ModuleID, proposal.FactsID, proposal.FactsDigest, proposal.Snapshots})
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
