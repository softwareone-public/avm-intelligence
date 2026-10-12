package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

const maxScopedFactsReferences = 50

// AcceptedFactsReference identifies one exact accepted module facts record.
type AcceptedFactsReference struct {
	ModuleID  string `json:"module_id"`
	SourceSHA string `json:"source_sha"`
	FactsID   string `json:"facts_id"`
}

// ProviderTypeReference is a module-used provider type selected from accepted
// facts. It contains no provider documentation or inferred metadata.
type ProviderTypeReference struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// LoadAcceptedProviderTypeScope derives a deduplicated type scope from exact
// facts records already present in the accepted data tree.
func LoadAcceptedProviderTypeScope(dataRoot string, references []AcceptedFactsReference, policy CollectionPolicy, providerAddress string) ([]ProviderTypeReference, error) {
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("validate scoped provider policy: %w", err)
	}
	providerPolicy, found := providerPolicyForAddress(policy, providerAddress)
	if !found {
		return nil, fmt.Errorf("provider address is outside reviewed collection policy")
	}
	providerAddress = providerPolicy.Address
	if len(references) == 0 || len(references) > maxScopedFactsReferences {
		return nil, fmt.Errorf("scoped provider collection requires 1 to %d exact accepted facts references", maxScopedFactsReferences)
	}
	orderedReferences := append([]AcceptedFactsReference(nil), references...)
	sort.Slice(orderedReferences, func(left, right int) bool {
		return orderedReferences[left].ModuleID+"\x00"+orderedReferences[left].SourceSHA+"\x00"+orderedReferences[left].FactsID <
			orderedReferences[right].ModuleID+"\x00"+orderedReferences[right].SourceSHA+"\x00"+orderedReferences[right].FactsID
	})
	types := make(map[string]ProviderTypeReference)
	for index, reference := range orderedReferences {
		if index > 0 && orderedReferences[index-1] == reference {
			return nil, fmt.Errorf("scoped accepted facts reference is duplicated")
		}
		if err := identity.ValidateRecordID("facts", reference.FactsID); err != nil {
			return nil, fmt.Errorf("validate scoped accepted facts identity: %w", err)
		}
		facts, err := LoadAcceptedModuleFacts(dataRoot, reference.ModuleID, reference.SourceSHA, reference.FactsID)
		if err != nil {
			return nil, fmt.Errorf("load exact accepted module facts: %w", err)
		}
		for _, reference := range providerTypesFromFacts(facts, providerAddress) {
			if !containsString(providerPolicy.CollectionKinds, reference.Kind) {
				continue
			}
			key := reference.Kind + "\x00" + reference.Name
			types[key] = reference
		}
	}
	if len(types) == 0 {
		return nil, fmt.Errorf("accepted facts do not use a configured resource kind for this provider")
	}
	result := make([]ProviderTypeReference, 0, len(types))
	for _, reference := range types {
		result = append(result, reference)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Kind+"\x00"+result[left].Name < result[right].Kind+"\x00"+result[right].Name
	})
	return result, nil
}

func providerTypesFromFacts(facts terraform.Facts, providerAddress string) []ProviderTypeReference {
	providerAddress = terraform.CanonicalProviderAddress(providerAddress)
	types := make(map[string]ProviderTypeReference)
	for _, unit := range facts.Units {
		for _, symbol := range unit.Symbols {
			if terraform.IsBuiltinTerraformSymbol(symbol) || terraform.CanonicalProviderAddress(symbol.ProviderAddress) != providerAddress {
				continue
			}
			var kind string
			switch symbol.Kind {
			case "resource":
				kind = "resource"
			case "data":
				kind = "datasource"
			case "ephemeral", "action", "function":
				kind = symbol.Kind
			default:
				continue
			}
			name, _, _ := strings.Cut(symbol.Name, ".")
			if name == "" || !providerTypeNamePattern.MatchString(name) {
				continue
			}
			key := kind + "\x00" + name
			types[key] = ProviderTypeReference{Kind: kind, Name: name}
		}
	}
	result := make([]ProviderTypeReference, 0, len(types))
	for _, reference := range types {
		result = append(result, reference)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Kind+"\x00"+result[left].Name < result[right].Kind+"\x00"+result[right].Name
	})
	return result
}

// CollectScopedTypeCatalog reads documentation only for exact module-used
// types. Whole provider kinds remain partial; types outside the supplied scope
// are not asserted present or absent.
func (collector CatalogCollector) CollectScopedTypeCatalog(ctx context.Context, policy CollectionPolicy, providerAddress, version, revision string, scope []ProviderTypeReference) (assessment.ProviderSnapshot, error) {
	if err := policy.Validate(); err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	providerPolicy, found := providerPolicyForAddress(policy, providerAddress)
	if !found {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider address %q is not allowlisted", providerAddress)
	}
	providerAddress = providerPolicy.Address
	if err := validateCatalogVersion(version, providerPolicy.TrackMajors); err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	if !validCommitSHA1(revision) || !validCommitSHA1(collector.Commit) {
		return assessment.ProviderSnapshot{}, fmt.Errorf("provider and collector revisions must be immutable commit SHAs")
	}
	if len(scope) == 0 {
		return assessment.ProviderSnapshot{}, fmt.Errorf("scoped provider catalog requires exact module-used types")
	}
	apiBase, err := catalogBase(collector.APIBase, defaultCatalogAPIBase)
	if err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	rawBase, err := catalogBase(collector.RawBase, defaultCatalogRawBase)
	if err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	collectionContext, cancel := context.WithTimeout(ctx, time.Duration(providerPolicy.Limits.MaxWallTimeSeconds)*time.Second)
	defer cancel()
	run := catalogCollectionRun{
		client: catalogHTTPClient(collector.Client), cache: collector.Cache, apiBase: apiBase, rawBase: rawBase,
		providerPolicy: providerPolicy, revision: revision, budget: &catalogBudget{},
	}
	artifacts, err := resolveProviderVersion(collectionContext, run, version, revision)
	if err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	orderedScope := append([]ProviderTypeReference(nil), scope...)
	sort.Slice(orderedScope, func(left, right int) bool {
		return orderedScope[left].Kind+"\x00"+orderedScope[left].Name < orderedScope[right].Kind+"\x00"+orderedScope[right].Name
	})
	entries := make([]assessment.ProviderEntry, 0, len(orderedScope))
	scopedCoverage := make([]assessment.ProviderTypeCoverage, 0, len(orderedScope))
	coverageKinds := make(map[string]string, len(providerPolicy.CollectionKinds))
	for _, kind := range providerPolicy.CollectionKinds {
		coverageKinds[kind] = terraform.CoverageUnavailable
	}
	previous := ""
	for _, requested := range orderedScope {
		key := requested.Kind + "\x00" + requested.Name
		if !containsString(providerPolicy.CollectionKinds, requested.Kind) || !providerTypeNamePattern.MatchString(requested.Name) || key == previous {
			return assessment.ProviderSnapshot{}, fmt.Errorf("scoped provider types contain an invalid or duplicate identity")
		}
		previous = key
		status := assessment.ProviderTypeUnavailable
		if directory, supported := catalogDocumentationDirectory(providerPolicy, requested.Kind); supported {
			entry, observed, collectedArtifacts, collectErr := collector.collectScopedProviderType(collectionContext, run, directory, providerAddress, requested)
			if collectErr != nil {
				return assessment.ProviderSnapshot{}, fmt.Errorf("collect scoped %s type: %w", requested.Kind, collectErr)
			}
			artifacts = append(artifacts, collectedArtifacts...)
			status = observed
			if observed == assessment.ProviderTypePresent {
				entries = append(entries, entry)
			}
		}
		scopedCoverage = append(scopedCoverage, assessment.ProviderTypeCoverage{Kind: requested.Kind, Name: requested.Name, Status: status})
		if status != assessment.ProviderTypeUnavailable {
			coverageKinds[requested.Kind] = terraform.CoveragePartial
		}
	}
	observedAt := collector.observedAt()
	coverage := assessment.SnapshotCoverage{
		Capabilities: coverageKinds, ScopedTypes: scopedCoverage, Complete: false, ObservedAt: observedAt,
		Reason: "only exact types referenced by accepted module facts were checked; other provider catalog types are unavailable",
	}
	sourceDigest, err := scopedCatalogSourceDigest(providerPolicy, version, revision, coverageKinds, scopedCoverage, artifacts)
	if err != nil {
		return assessment.ProviderSnapshot{}, err
	}
	return assessment.ProviderSnapshot{
		SchemaVersion: 2, RecordType: "provider_snapshot", ProviderAddress: providerAddress,
		ProviderVersion: version, Coverage: coverage, Entries: entries,
		Producer: assessment.SnapshotProducer{
			ID: "avm-intelligence-provider-scoped", Version: "1.0.0", Commit: collector.Commit, ContractVersion: 1,
		},
		SourceDigest: sourceDigest, ObservedAt: observedAt,
	}, nil
}

func scopedCatalogSourceDigest(policy ProviderPolicy, version, revision string, coverage map[string]string, scopedTypes []assessment.ProviderTypeCoverage, artifacts []catalogSourceArtifact) (string, error) {
	orderedTypes := append([]assessment.ProviderTypeCoverage(nil), scopedTypes...)
	sort.Slice(orderedTypes, func(left, right int) bool {
		return orderedTypes[left].Kind+"\x00"+orderedTypes[left].Name < orderedTypes[right].Kind+"\x00"+orderedTypes[right].Name
	})
	orderedArtifacts := append([]catalogSourceArtifact(nil), artifacts...)
	sort.Slice(orderedArtifacts, func(left, right int) bool { return orderedArtifacts[left].URL < orderedArtifacts[right].URL })
	manifest := struct {
		ProviderAddress string                            `json:"provider_address"`
		Repository      string                            `json:"repository"`
		License         string                            `json:"license"`
		Version         string                            `json:"version"`
		Revision        string                            `json:"revision"`
		Coverage        map[string]string                 `json:"coverage"`
		ScopedTypes     []assessment.ProviderTypeCoverage `json:"scoped_types"`
		Artifacts       []catalogSourceArtifact           `json:"artifacts"`
	}{policy.Address, policy.SourceRepository, policy.SourceLicense, version, revision, coverage, orderedTypes, orderedArtifacts}
	canonical, err := identity.CanonicalJSON("provider-scoped-source-manifest", 1, manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func (collector CatalogCollector) collectScopedProviderType(ctx context.Context, run catalogCollectionRun, directory, providerAddress string, requested ProviderTypeReference) (assessment.ProviderEntry, string, []catalogSourceArtifact, error) {
	owner, repository, _ := strings.Cut(run.providerPolicy.SourceRepository, "/")
	baseName := requested.Name
	if run.providerPolicy.SourceRepository == "hashicorp/terraform-provider-azurerm" && (requested.Kind == "resource" || requested.Kind == "datasource") {
		baseName = strings.TrimPrefix(baseName, "azurerm_")
	}
	if baseName == "" || strings.Contains(baseName, "/") {
		return assessment.ProviderEntry{}, "", nil, fmt.Errorf("scoped provider type name is invalid")
	}
	artifacts := make([]catalogSourceArtifact, 0, 3)
	for _, extension := range []string{".html.markdown", ".markdown", ".md"} {
		documentPath := path.Join(directory, baseName+extension)
		documentURL, err := catalogDocumentURL(run.rawBase, owner, repository, run.revision, documentPath)
		if err != nil {
			return assessment.ProviderEntry{}, "", nil, err
		}
		status, _, body, err := catalogGet(ctx, run.client, run.cache, documentURL, "text/plain", run.providerPolicy.Limits, run.budget)
		if err != nil {
			return assessment.ProviderEntry{}, "", nil, err
		}
		artifacts = append(artifacts, newCatalogArtifact(documentURL, body, status))
		switch status {
		case http.StatusNotFound:
			continue
		case http.StatusOK:
			if len(body) == 0 {
				return assessment.ProviderEntry{}, "", nil, fmt.Errorf("provider documentation is empty")
			}
			name, valid := providerTypeName(documentPath, directory, requested.Kind, providerAddress)
			if !valid || name != requested.Name {
				return assessment.ProviderEntry{}, "", nil, fmt.Errorf("provider documentation path does not match its exact requested type")
			}
			return assessment.ProviderEntry{
				Kind: requested.Kind, Name: requested.Name,
				DocumentationRef: providerDocumentationRef(owner, repository, run.revision, documentPath),
				ContentDigest:    catalogDigest(body),
			}, assessment.ProviderTypePresent, artifacts, nil
		default:
			return assessment.ProviderEntry{}, "", nil, fmt.Errorf("provider documentation returned HTTP %d", status)
		}
	}
	return assessment.ProviderEntry{}, assessment.ProviderTypeAbsent, artifacts, nil
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
