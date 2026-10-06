package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/provider"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
)

type providerCollectionOptions struct {
	collect         bool
	policyPath      string
	address         string
	version         string
	revision        string
	collectorCommit string
	expectedBase    string
	observedAt      string
	cacheRoot       string
	proposalRoot    string
	generation      uint64
}

func registerProviderCollectionFlags(flags *flag.FlagSet) *providerCollectionOptions {
	options := &providerCollectionOptions{}
	flags.BoolVar(&options.collect, "collect-provider-catalog", false, "Collect one pinned provider type catalog into a reviewable proposal")
	flags.StringVar(&options.policyPath, "provider-policy", "", "Reviewed provider collection policy")
	flags.StringVar(&options.address, "provider-address", "", "Exact allowlisted registry provider address")
	flags.StringVar(&options.version, "provider-version", "", "Exact provider release version")
	flags.StringVar(&options.revision, "provider-revision", "", "Immutable provider source commit matching the release tag")
	flags.StringVar(&options.collectorCommit, "provider-collector-commit", "", "Exact collector code commit")
	flags.StringVar(&options.expectedBase, "provider-proposal-base", "", "Exact accepted data commit for the proposal")
	flags.StringVar(&options.observedAt, "provider-observed-at", "", "RFC3339 observation time")
	flags.StringVar(&options.cacheRoot, "provider-cache-root", "", "Existing persistent public-response cache directory outside accepted data")
	flags.StringVar(&options.proposalRoot, "provider-proposal-out", "", "Fresh provider snapshot proposal directory")
	flags.Uint64Var(&options.generation, "provider-source-generation", 0, "Explicit positive provider collection generation")
	return options
}

func handleProviderCollectionCommand(flags *flag.FlagSet, options providerCollectionOptions, dataRoot string) (bool, error) {
	if options.collect {
		var conflicting string
		flags.Visit(func(selected *flag.Flag) {
			if !strings.HasPrefix(selected.Name, "provider-") && selected.Name != "collect-provider-catalog" && selected.Name != "data-root" {
				conflicting = selected.Name
			}
		})
		if conflicting != "" {
			return true, fmt.Errorf("--collect-provider-catalog cannot be combined with --%s", conflicting)
		}
	}
	return handleProviderCollection(context.Background(), options, dataRoot, provider.CatalogCollector{})
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
	snapshot, err := collector.CollectTypeCatalog(ctx, policy, options.address, options.version, options.revision)
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
