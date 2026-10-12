package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/provider"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
)

type providerCollectionOptions struct {
	collect                     bool
	collectResourceIntelligence bool
	policyPath                  string
	address                     string
	version                     string
	moduleID                    string
	sourceSHA                   string
	factsID                     string
	snapshotID                  string
	revision                    string
	collectorCommit             string
	expectedBase                string
	observedAt                  string
	cacheRoot                   string
	proposalRoot                string
	factsScopePath              string
	generation                  uint64
}

func registerProviderCollectionFlags(flags *flag.FlagSet) *providerCollectionOptions {
	options := &providerCollectionOptions{}
	flags.BoolVar(&options.collect, "collect-provider-catalog", false, "Collect one pinned provider type catalog into a reviewable proposal")
	flags.BoolVar(&options.collectResourceIntelligence, "collect-provider-resource-intelligence", false, "Collect version-pinned resource intelligence into an accepted provider snapshot proposal")
	flags.StringVar(&options.policyPath, "provider-policy", "", "Reviewed provider collection policy")
	flags.StringVar(&options.address, "provider-address", "", "Exact allowlisted registry provider address")
	flags.StringVar(&options.version, "provider-version", "", "Exact provider release version")
	flags.StringVar(&options.moduleID, "resource-module-id", "", "Exact module whose accepted facts identify used provider resources")
	flags.StringVar(&options.sourceSHA, "resource-source-sha", "", "Exact accepted module source revision")
	flags.StringVar(&options.factsID, "resource-facts-id", "", "Exact accepted acquisition facts record")
	flags.StringVar(&options.snapshotID, "resource-provider-snapshot-id", "", "Exact accepted provider snapshot containing the module-used stubs")
	flags.StringVar(&options.revision, "provider-revision", "", "Immutable provider source commit matching the release tag")
	flags.StringVar(&options.collectorCommit, "provider-collector-commit", "", "Exact collector code commit")
	flags.StringVar(&options.expectedBase, "provider-proposal-base", "", "Exact accepted data commit for the proposal")
	flags.StringVar(&options.observedAt, "provider-observed-at", "", "RFC3339 observation time")
	flags.StringVar(&options.cacheRoot, "provider-cache-root", "", "Existing persistent public-response cache directory outside accepted data")
	flags.StringVar(&options.proposalRoot, "provider-proposal-out", "", "Fresh provider snapshot proposal directory")
	flags.StringVar(&options.factsScopePath, "provider-facts-scope", "", "JSON file listing exact accepted module facts that scope provider types")
	flags.Uint64Var(&options.generation, "provider-source-generation", 0, "Explicit positive provider collection generation")
	return options
}

func handleProviderCollectionCommand(flags *flag.FlagSet, options providerCollectionOptions, dataRoot string) (bool, error) {
	if options.collect && options.collectResourceIntelligence {
		return true, fmt.Errorf("provider catalog and resource intelligence collection are separate commands")
	}
	if options.collect || options.collectResourceIntelligence {
		var conflicting string
		flags.Visit(func(selected *flag.Flag) {
			if !strings.HasPrefix(selected.Name, "provider-") && !strings.HasPrefix(selected.Name, "resource-") && selected.Name != "collect-provider-catalog" && selected.Name != "collect-provider-resource-intelligence" && selected.Name != "data-root" {
				conflicting = selected.Name
			}
		})
		if conflicting != "" {
			return true, fmt.Errorf("provider collection cannot be combined with --%s", conflicting)
		}
	}
	if options.collectResourceIntelligence {
		return handleProviderResourceIntelligence(context.Background(), options, dataRoot, provider.ResourceIntelligenceCollector{})
	}
	return handleProviderCollection(context.Background(), options, dataRoot, provider.CatalogCollector{})
}

func handleProviderResourceIntelligence(ctx context.Context, options providerCollectionOptions, dataRoot string, collector provider.ResourceIntelligenceCollector) (bool, error) {
	if !options.collectResourceIntelligence {
		return false, nil
	}
	observedAt, err := validateProviderResourceIntelligenceOptions(options, dataRoot)
	if err != nil {
		return true, err
	}
	policy, err := provider.LoadCollectionPolicy(options.policyPath)
	if err != nil {
		return true, err
	}
	accepted, err := provider.LoadAcceptedResourceIntelligenceInputs(dataRoot, options.moduleID, options.sourceSHA, options.factsID, policy, []provider.AcceptedResourceSnapshotReference{{
		ProviderAddress: options.address, ProviderVersion: options.version, SnapshotID: options.snapshotID,
	}})
	if err != nil {
		return true, err
	}
	collector.Now = func() time.Time { return observedAt }
	collector.Cache = &provider.FileCatalogCache{Root: options.cacheRoot}
	sources, err := collector.Collect(ctx, policy, accepted, options.collectorCommit)
	if err != nil {
		return true, err
	}
	proposal, err := provider.NewResourceIntelligenceProposal(policy, accepted, sources, observedAt)
	if err != nil {
		return true, err
	}
	enriched, err := provider.EnrichAcceptedProviderSnapshots(policy, accepted, proposal, assessment.SnapshotProducer{
		ID: "avm-intelligence-resource-collector", Version: "1.0.0", Commit: options.collectorCommit, ContractVersion: 1,
	}, observedAt)
	if err != nil {
		return true, err
	}
	if len(enriched) == 0 {
		slog.Info("provider resource intelligence collection had no new sources", "proposal_id", proposal.ProposalID, "provider", options.address, "version", options.version)
		return true, nil
	}
	if len(enriched) != 1 {
		return true, fmt.Errorf("resource intelligence command expected exactly one provider snapshot")
	}
	providerProposal, err := publish.BuildProviderSnapshotProposal(publish.ProviderSnapshotProposalInput{
		ProposalRoot: options.proposalRoot, BaseDataRoot: dataRoot, ExpectedBase: options.expectedBase,
		SourceGeneration: options.generation, Snapshot: enriched[0],
	})
	if err != nil {
		return true, err
	}
	slog.Info("provider resource intelligence proposal built", "proposal_id", providerProposal.ProposalID, "source_proposal_id", proposal.ProposalID, "provider", options.address, "version", options.version, "generation", providerProposal.SourceGeneration, "paths", len(providerProposal.Paths))
	return true, nil
}

func validateProviderResourceIntelligenceOptions(options providerCollectionOptions, dataRoot string) (time.Time, error) {
	if options.policyPath == "" || options.address == "" || options.version == "" || options.moduleID == "" || options.sourceSHA == "" || options.factsID == "" || options.snapshotID == "" || options.generation == 0 {
		return time.Time{}, fmt.Errorf("resource intelligence collection requires policy, exact provider snapshot, accepted module facts, and positive source generation")
	}
	for _, commit := range []string{options.expectedBase, options.collectorCommit} {
		if len(commit) != 40 || strings.ToLower(commit) != commit {
			return time.Time{}, fmt.Errorf("resource intelligence collection requires lowercase 40-character base and collector commits")
		}
		if _, err := hex.DecodeString(commit); err != nil {
			return time.Time{}, fmt.Errorf("resource intelligence collection commit is invalid: %w", err)
		}
	}
	observedAt, err := time.Parse(time.RFC3339, options.observedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("provider observation time must be RFC3339: %w", err)
	}
	if err = validateProviderCollectionRoots(dataRoot, options.cacheRoot, options.proposalRoot); err != nil {
		return time.Time{}, err
	}
	return observedAt, nil
}

func handleProviderCollection(ctx context.Context, options providerCollectionOptions, dataRoot string, collector provider.CatalogCollector) (bool, error) {
	if !options.collect {
		if options != (providerCollectionOptions{}) {
			return true, fmt.Errorf("provider collection options require --collect-provider-catalog")
		}
		return false, nil
	}
	observedAt, err := validateProviderCollectionOptions(options, dataRoot)
	if err != nil {
		return true, err
	}
	policy, err := provider.LoadCollectionPolicy(options.policyPath)
	if err != nil {
		return true, err
	}
	collector.Commit = options.collectorCommit
	collector.Now = func() time.Time { return observedAt }
	collector.Cache = &provider.FileCatalogCache{Root: options.cacheRoot}
	var snapshot assessment.ProviderSnapshot
	if options.factsScopePath == "" {
		snapshot, err = collector.CollectTypeCatalog(ctx, policy, options.address, options.version, options.revision)
	} else {
		factReferences, readErr := loadProviderFactsScope(options.factsScopePath)
		if readErr != nil {
			return true, readErr
		}
		typeScope, scopeErr := provider.LoadAcceptedProviderTypeScope(dataRoot, factReferences, policy, options.address)
		if scopeErr != nil {
			return true, scopeErr
		}
		snapshot, err = collector.CollectScopedTypeCatalog(ctx, policy, options.address, options.version, options.revision, typeScope)
	}
	if err != nil {
		return true, err
	}
	proposal, err := publish.BuildProviderSnapshotProposal(publish.ProviderSnapshotProposalInput{
		ProposalRoot: options.proposalRoot, BaseDataRoot: dataRoot, ExpectedBase: options.expectedBase,
		SourceGeneration: options.generation, Snapshot: snapshot,
	})
	if err != nil {
		return true, err
	}
	slog.Info("provider proposal built", "proposal_id", proposal.ProposalID, "provider", proposal.Subject, "generation", proposal.SourceGeneration, "paths", len(proposal.Paths))
	return true, nil
}

func loadProviderFactsScope(path string) ([]provider.AcceptedFactsReference, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read provider facts scope: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var references []provider.AcceptedFactsReference
	if err = decoder.Decode(&references); err != nil {
		return nil, fmt.Errorf("decode provider facts scope: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("provider facts scope must contain one JSON value")
	}
	return references, nil
}

func validateProviderCollectionOptions(options providerCollectionOptions, dataRoot string) (time.Time, error) {
	if options.policyPath == "" || options.address == "" || options.version == "" || options.generation == 0 {
		return time.Time{}, fmt.Errorf("provider collection requires policy, address, exact version, and positive source generation")
	}
	for _, commit := range []string{options.expectedBase, options.revision, options.collectorCommit} {
		if len(commit) != 40 || strings.ToLower(commit) != commit {
			return time.Time{}, fmt.Errorf("provider collection requires lowercase 40-character base, provider, and collector commits")
		}
		if _, err := hex.DecodeString(commit); err != nil {
			return time.Time{}, fmt.Errorf("provider collection commit is invalid: %w", err)
		}
	}
	observedAt, err := time.Parse(time.RFC3339, options.observedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("provider observation time must be RFC3339: %w", err)
	}
	if err = validateProviderCollectionRoots(dataRoot, options.cacheRoot, options.proposalRoot); err != nil {
		return time.Time{}, err
	}
	return observedAt, nil
}

func validateProviderCollectionRoots(dataRoot, cacheRoot, proposalRoot string) error {
	dataPath, err := providerCollectionDirectory(dataRoot)
	if err != nil {
		return err
	}
	cachePath, err := providerCollectionDirectory(cacheRoot)
	if err != nil {
		return err
	}
	if proposalRoot == "" {
		return fmt.Errorf("fresh provider proposal directory is required")
	}
	if _, err = os.Lstat(proposalRoot); !os.IsNotExist(err) {
		return fmt.Errorf("provider proposal output must not already exist")
	}
	parent, err := providerCollectionDirectory(filepath.Dir(proposalRoot))
	if err != nil {
		return err
	}
	proposalPath := filepath.Join(parent, filepath.Base(proposalRoot))
	for _, pair := range [][2]string{{dataPath, cachePath}, {dataPath, proposalPath}, {cachePath, proposalPath}} {
		if providerCollectionPathsOverlap(pair[0], pair[1]) {
			return fmt.Errorf("accepted data, provider cache, and proposal directories must not overlap")
		}
	}
	return nil
}

func providerCollectionDirectory(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("provider collection directory is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("provider collection directory must be a real non-symlink directory")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func providerCollectionPathsOverlap(left, right string) bool {
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
