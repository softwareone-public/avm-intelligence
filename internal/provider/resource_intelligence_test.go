package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

func TestResourceIntelligenceProposalIsPinnedImmutableAndDeterministic(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	body := "azurerm_resource_group: change in provider 4.2.0"
	source := ResourceIntelligenceSource{Kind: "provider_changelog", URL: "https://github.com/hashicorp/terraform-provider-azurerm/blob/v4.2.0/CHANGELOG.md", Version: "4.2.0", VersionBasis: "release_tag", ContentDigest: digestText(body), Body: body, ObservedAt: now}
	secondary := ResourceIntelligenceSource{Kind: "provider_changelog", URL: "https://github.com/hashicorp/terraform-provider-azurerm/blob/v4.2.0/website/docs/r/resource_group.html.markdown", Version: "4.2.0", VersionBasis: "release_tag", ContentDigest: digestText("resource-specific release notes"), Body: "resource-specific release notes", ObservedAt: now}
	updated := source
	updated.Body = "azurerm_resource_group: corrected release details"
	updated.ContentDigest = digestText(updated.Body)
	updated.ObservedAt = now.Add(time.Hour)
	accepted, policy := loadResourceIntelligenceFixture(t)
	snapshot := accepted.Snapshots[0]
	snapshot.Resources = []ResourceIntelligenceResource{{Kind: "resource", Name: "azurerm_resource_group", Sources: []ResourceIntelligenceSource{source, secondary, source, updated}}}
	first, err := NewResourceIntelligenceProposal(policy, accepted, []ResourceIntelligenceSnapshot{snapshot}, now)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := NewResourceIntelligenceProposal(policy, accepted, []ResourceIntelligenceSnapshot{snapshot}, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProposalID != replayed.ProposalID || first.ContentDigest != replayed.ContentDigest {
		t.Fatal("identical input did not produce stable proposal identity")
	}
	if err = first.Validate(); err != nil {
		t.Fatalf("valid proposal rejected: %v", err)
	}
	if got := len(first.Snapshots[0].Resources[0].Sources); got != 3 {
		t.Fatalf("exact duplicates were not collapsed or revised source content was lost: got %d entries", got)
	}
	first.Snapshots[0].Resources[0].Sources[0].Body = "changed"
	if err = first.Validate(); err == nil {
		t.Fatal("tampered proposal content passed validation")
	}
}

func TestResourceIntelligenceProposalPreservesMultipleVersionsOfOneProvider(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	accepted, policy := loadResourceIntelligenceFixture(t)
	policy.Providers[0].TrackMajors = []uint64{4, 5}
	first := accepted.Snapshots[0]
	first.Resources = []ResourceIntelligenceResource{{Kind: "resource", Name: "azurerm_resource_group", Sources: []ResourceIntelligenceSource{{
		Kind: "github_release", URL: "https://github.com/hashicorp/terraform-provider-azurerm/releases/tag/v4.2.0",
		Version: "4.2.0", VersionBasis: "release_tag", ContentDigest: digestText("release 4.2.0"), Body: "release 4.2.0", ObservedAt: now,
	}}}}
	second := first
	second.ProviderVersion = "5.0.1"
	second.SnapshotID = "snapshot-" + strings.Repeat("c", 64)
	second.StubDigest = strings.Repeat("d", 64)
	second.Resources = []ResourceIntelligenceResource{{Kind: "resource", Name: "azurerm_resource_group", Sources: []ResourceIntelligenceSource{{
		Kind: "github_release", URL: "https://github.com/hashicorp/terraform-provider-azurerm/releases/tag/v5.0.1",
		Version: "5.0.1", VersionBasis: "release_tag", ContentDigest: digestText("release 5.0.1"), Body: "release 5.0.1", ObservedAt: now,
	}}}}
	accepted.Snapshots = []ResourceIntelligenceSnapshot{second, first}
	seal, err := acceptedResourceInputsSeal(accepted.FactsScope, accepted.FactReferences, accepted.Snapshots, accepted.ProviderSnapshots)
	if err != nil {
		t.Fatal(err)
	}
	accepted.seal = seal
	proposal, err := NewResourceIntelligenceProposal(policy, accepted, []ResourceIntelligenceSnapshot{second, first}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = proposal.Validate(); err != nil {
		t.Fatalf("valid multi-version proposal rejected: %v", err)
	}
	if len(proposal.Snapshots) != 2 || proposal.Snapshots[0].ProviderVersion != "4.2.0" || proposal.Snapshots[1].ProviderVersion != "5.0.1" {
		t.Fatalf("provider snapshot history was not retained in canonical order: %#v", proposal.Snapshots)
	}
}

func TestEnrichAcceptedProviderSnapshotWritesEvidenceToVersionedStubs(t *testing.T) {
	root, facts, acceptedSnapshot, policy := acceptedResourceIntelligenceTree(t)
	accepted, err := LoadAcceptedResourceIntelligenceInputs(root, facts.ModuleID, facts.SourceSHA, facts.FactsID, policy, []AcceptedResourceSnapshotReference{{
		ProviderAddress: acceptedSnapshot.ProviderAddress, ProviderVersion: acceptedSnapshot.ProviderVersion, SnapshotID: acceptedSnapshot.SnapshotID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	source := ResourceIntelligenceSource{
		Kind: "github_release", URL: "https://github.com/hashicorp/terraform-provider-azurerm/releases/tag/v4.2.0",
		Version: "4.2.0", VersionBasis: "release_tag", ContentDigest: digestText("release details"),
		Body: "release details", ObservedAt: now,
	}
	collected := cloneResourceSnapshots(accepted.Snapshots)[0]
	collected.Resources[0].Sources = []ResourceIntelligenceSource{source}
	proposal, err := NewResourceIntelligenceProposal(policy, accepted, []ResourceIntelligenceSnapshot{collected}, now)
	if err != nil {
		t.Fatal(err)
	}
	producer := assessment.SnapshotProducer{ID: "resource-intelligence", Version: "1.0.0", Commit: strings.Repeat("f", 40), ContractVersion: 1}
	enriched, err := EnrichAcceptedProviderSnapshot(policy, accepted, acceptedSnapshot, proposal, acceptedSnapshot.ProviderAddress, acceptedSnapshot.ProviderVersion, producer, now)
	if err != nil {
		t.Fatal(err)
	}
	if enriched.SnapshotID == acceptedSnapshot.SnapshotID || len(enriched.Entries[0].IntelligenceSources) != 1 || len(acceptedSnapshot.Entries[0].IntelligenceSources) != 0 {
		t.Fatalf("enriched snapshot did not create isolated versioned stub evidence: enriched=%+v base=%+v", enriched, acceptedSnapshot)
	}
	if err = assessment.ValidateProviderSnapshot(enriched); err != nil {
		t.Fatalf("enriched provider snapshot invalid: %v", err)
	}
}

func TestSharedProviderEvidenceConvergesAcrossModuleConsumers(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	firstRoot, firstFacts, firstBase, policy := acceptedResourceIntelligenceTreeForModule(t, "avm-res-consumer-one", strings.Repeat("a", 40))
	secondRoot, secondFacts, secondBase, secondPolicy := acceptedResourceIntelligenceTreeForModule(t, "avm-res-consumer-two", strings.Repeat("b", 40))
	if firstBase.SnapshotID != secondBase.SnapshotID || firstBase.ProviderAddress != secondBase.ProviderAddress || firstBase.ProviderVersion != secondBase.ProviderVersion {
		t.Fatal("consumer fixtures do not share one exact accepted provider snapshot")
	}
	if secondPolicy.Providers[0].Address != policy.Providers[0].Address {
		t.Fatal("consumer fixtures use different reviewed provider policy")
	}
	source := ResourceIntelligenceSource{
		Kind: "github_release", URL: "https://github.com/hashicorp/terraform-provider-azurerm/releases/tag/v4.2.0",
		Version: "4.2.0", VersionBasis: "release_tag", ContentDigest: digestText("shared resource release details"),
		Body: "shared resource release details", ObservedAt: now,
	}
	collectForConsumer := func(root string, facts terraform.Facts, base assessment.ProviderSnapshot) assessment.ProviderSnapshot {
		t.Helper()
		accepted, err := LoadAcceptedResourceIntelligenceInputs(root, facts.ModuleID, facts.SourceSHA, facts.FactsID, policy, []AcceptedResourceSnapshotReference{{
			ProviderAddress: base.ProviderAddress, ProviderVersion: base.ProviderVersion, SnapshotID: base.SnapshotID,
		}})
		if err != nil {
			t.Fatalf("load exact accepted inputs for %s: %v", facts.ModuleID, err)
		}
		collected := cloneResourceSnapshots(accepted.Snapshots)[0]
		collected.Resources[0].Sources = []ResourceIntelligenceSource{source}
		proposal, err := NewResourceIntelligenceProposal(policy, accepted, []ResourceIntelligenceSnapshot{collected}, now)
		if err != nil {
			t.Fatalf("build consumer-specific proposal for %s: %v", facts.ModuleID, err)
		}
		result, err := EnrichAcceptedProviderSnapshot(policy, accepted, base, proposal,
			base.ProviderAddress, base.ProviderVersion,
			assessment.SnapshotProducer{ID: "resource-intelligence", Version: "1.0.0", Commit: strings.Repeat("c", 40), ContractVersion: 1}, now)
		if err != nil {
			t.Fatalf("enrich shared provider snapshot for %s: %v", facts.ModuleID, err)
		}
		return result
	}
	first := collectForConsumer(firstRoot, firstFacts, firstBase)
	second := collectForConsumer(secondRoot, secondFacts, secondBase)
	if first.SnapshotID != second.SnapshotID || len(first.Entries) != 1 || len(second.Entries) != 1 ||
		len(first.Entries[0].IntelligenceSources) != 1 || len(second.Entries[0].IntelligenceSources) != 1 ||
		first.Entries[0].IntelligenceSources[0] != second.Entries[0].IntelligenceSources[0] {
		t.Fatalf("shared evidence did not converge to one provider-root source: first=%+v second=%+v", first, second)
	}
}

func TestResourceIntelligenceProposalRejectsUnpinnedOrOversizedSource(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	verifiedInputs, policy := loadResourceIntelligenceFixture(t)
	accepted := verifiedInputs.Snapshots[0]
	for name, source := range map[string]ResourceIntelligenceSource{
		"latest URL":        {Kind: "github_release", URL: "https://github.com/hashicorp/terraform-provider-azurerm/releases/latest", Version: "4.2.0", VersionBasis: "release_tag", ContentDigest: digestText("body"), Body: "body", ObservedAt: now},
		"wrong repository":  {Kind: "resource_issue", URL: "https://github.com/attacker/provider/issues/12", Version: "4.2.0", VersionBasis: "provider_snapshot", ContentDigest: digestText("body"), Body: "body", ObservedAt: now},
		"wrong release tag": {Kind: "github_release", URL: "https://github.com/hashicorp/terraform-provider-azurerm/releases/tag/v5.0.0", Version: "4.2.0", VersionBasis: "release_tag", ContentDigest: digestText("body"), Body: "body", ObservedAt: now},
		"oversized":         {Kind: "resource_issue", URL: "https://github.com/hashicorp/terraform-provider-azurerm/issues/12", Version: "4.2.0", VersionBasis: "provider_snapshot", ContentDigest: digestText(strings.Repeat("x", maxResourceIntelligenceSourceBytes+1)), Body: strings.Repeat("x", maxResourceIntelligenceSourceBytes+1), ObservedAt: now},
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := accepted
			snapshot.Resources = []ResourceIntelligenceResource{{Kind: "resource", Name: "azurerm_resource_group", Sources: []ResourceIntelligenceSource{source}}}
			if _, err := NewResourceIntelligenceProposal(policy, verifiedInputs, []ResourceIntelligenceSnapshot{snapshot}, now); err == nil {
				t.Fatal("invalid source produced a proposal")
			}
		})
	}
}

func TestResourceIntelligenceProposalMakesIssueVersionScopeExplicit(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	verifiedInputs, policy := loadResourceIntelligenceFixture(t)
	accepted := verifiedInputs.Snapshots[0]
	issue := ResourceIntelligenceSource{
		Kind: "resource_issue", URL: "https://github.com/hashicorp/terraform-provider-azurerm/issues/123",
		Version: "4.2.0", VersionBasis: "provider_snapshot", ContentDigest: digestText("resource discussion"),
		Body: "resource discussion", ObservedAt: now,
	}
	upgrade := ResourceIntelligenceSource{
		Kind: "registry_upgrade", URL: "https://registry.terraform.io/providers/hashicorp/azurerm/4.0.0/docs/guides/4.0-upgrade",
		Version: "4.0.0", VersionBasis: "registry_path", ContentDigest: digestText("upgrade guidance"),
		Body: "upgrade guidance", ObservedAt: now,
	}
	accepted.Resources = []ResourceIntelligenceResource{{Kind: "resource", Name: "azurerm_resource_group", Sources: []ResourceIntelligenceSource{issue, upgrade}}}
	proposal, err := NewResourceIntelligenceProposal(policy, verifiedInputs, []ResourceIntelligenceSnapshot{accepted}, now)
	if err != nil {
		t.Fatalf("explicitly scoped issue and Registry guidance rejected: %v", err)
	}
	if err = proposal.Validate(); err != nil {
		t.Fatal(err)
	}
	var foundIssue bool
	for _, source := range proposal.Snapshots[0].Resources[0].Sources {
		if source.Kind == "resource_issue" {
			foundIssue = true
			if source.Version != "4.2.0" || source.VersionBasis != "provider_snapshot" {
				t.Fatalf("issue scope = %s/%s, want observed in provider snapshot 4.2.0", source.VersionBasis, source.Version)
			}
		}
	}
	if !foundIssue {
		t.Fatal("provider issue evidence was dropped")
	}

	issue.Version = "4.3.0"
	accepted.Resources[0].Sources[0] = issue
	if _, err = NewResourceIntelligenceProposal(policy, verifiedInputs, []ResourceIntelligenceSnapshot{accepted}, now); err == nil {
		t.Fatal("issue was linked to a provider version other than its observation snapshot")
	}
}

func loadResourceIntelligenceFixture(t *testing.T) (AcceptedResourceIntelligenceInputs, CollectionPolicy) {
	t.Helper()
	root, facts, snapshot, policy := acceptedResourceIntelligenceTree(t)
	inputs, err := LoadAcceptedResourceIntelligenceInputs(root, facts.ModuleID, facts.SourceSHA, facts.FactsID, policy, []AcceptedResourceSnapshotReference{{
		ProviderAddress: snapshot.ProviderAddress, ProviderVersion: snapshot.ProviderVersion, SnapshotID: snapshot.SnapshotID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return inputs, policy
}

func TestResourceIntelligenceWithoutMatchingSourcesProducesNoSnapshotChange(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	root, facts, snapshot, policy := acceptedResourceIntelligenceTree(t)
	accepted, err := LoadAcceptedResourceIntelligenceInputs(root, facts.ModuleID, facts.SourceSHA, facts.FactsID, policy, []AcceptedResourceSnapshotReference{{
		ProviderAddress: snapshot.ProviderAddress, ProviderVersion: snapshot.ProviderVersion, SnapshotID: snapshot.SnapshotID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := NewResourceIntelligenceProposal(policy, accepted, accepted.Snapshots, now)
	if err != nil {
		t.Fatalf("empty per-resource source lists should be a valid bounded result: %v", err)
	}
	if len(proposal.Snapshots) != 1 || len(proposal.Snapshots[0].Resources) != 1 || proposal.Snapshots[0].Resources[0].Sources == nil || len(proposal.Snapshots[0].Resources[0].Sources) != 0 {
		t.Fatalf("no-source proposal did not preserve exact used-type stub scope: %+v", proposal.Snapshots)
	}
	changed, err := EnrichAcceptedProviderSnapshots(policy, accepted, proposal, assessment.SnapshotProducer{
		ID: "resource-intelligence", Version: "1.0.0", Commit: strings.Repeat("f", 40), ContractVersion: 1,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("no-source result changed accepted provider snapshots: %+v", changed)
	}
}

func resourceIntelligenceFixture(t *testing.T) (terraform.Facts, ResourceIntelligenceSnapshot, CollectionPolicy) {
	return resourceIntelligenceFixtureForModule(t, "avm-res-example", strings.Repeat("a", 40))
}

func resourceIntelligenceFixtureForModule(t *testing.T, moduleID, sourceSHA string) (terraform.Facts, ResourceIntelligenceSnapshot, CollectionPolicy) {
	t.Helper()
	providerAddress := "registry.terraform.io/hashicorp/azurerm"
	root := t.TempDir()
	main := `terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

resource "azurerm_resource_group" "main" {
  name     = "fixture"
  location = "eastus"
}
`
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: root, ModuleID: moduleID, SourceSHA: sourceSHA,
		ObservedAt: time.Date(2026, 10, 9, 11, 0, 0, 0, time.UTC),
		Units:      []terraform.UnitInput{{Path: ".", Role: terraform.RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshotID, err := identity.ID("snapshot", 2, struct {
		Fixture string `json:"fixture"`
	}{"provider-intelligence"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := ResourceIntelligenceSnapshot{ProviderAddress: providerAddress, ProviderVersion: "4.2.0", SnapshotID: snapshotID, StubDigest: strings.Repeat("b", 64)}
	policy := CollectionPolicy{SchemaVersion: 1, Providers: []ProviderPolicy{{
		Address: providerAddress, SourceRepository: "hashicorp/terraform-provider-azurerm", SourceLicense: "MPL-2.0",
		TrackMajors: []uint64{4}, CollectionKinds: []string{"resource"},
		Limits: CollectionLimits{MaxReleases: 10, MaxPages: 10, MaxResponseBytes: 1024, MaxTotalBytes: 4096, MaxWallTimeSeconds: 60},
	}}}
	return facts, snapshot, policy
}
