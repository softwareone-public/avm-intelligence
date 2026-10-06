package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

const (
	discoveryCatalogStatePath = "state/discovery/catalog.json"
	discoveryExportsPrefix    = "state/exports/"
)

var catalogRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// buildDiscoveryCatalogProposal reads accepted discovery state and writes a
// fresh, reviewable catalog proposal outside the accepted data directory.
func buildDiscoveryCatalogProposal(branch, expectedBase, dataRoot, outputRoot string) error {
	if err := validateCatalogProposalArguments(branch, expectedBase, dataRoot, outputRoot); err != nil {
		return err
	}
	bundle, err := readCurrentDiscoveryCatalog(branch)
	if err != nil {
		return err
	}
	return writeDiscoveryCatalogProposal(outputRoot, expectedBase, dataRoot, bundle)
}

type discoveryCatalogBundle struct {
	accepted catalog.AcceptedState
	outbox   discovery.CatalogProposal
}

func validateCatalogProposalArguments(branch, expectedBase, dataRoot, outputRoot string) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	if !lowerHexSHA(expectedBase, 40) {
		return fmt.Errorf("--catalog-proposal-base must be a lowercase Git SHA")
	}
	if strings.TrimSpace(outputRoot) == "" {
		return fmt.Errorf("--catalog-proposal-out is required")
	}
	if err := ensureOutputOutsideData(dataRoot, filepath.Join(outputRoot, "proposal.json")); err != nil {
		return err
	}
	if _, err := os.Lstat(outputRoot); err == nil {
		return fmt.Errorf("catalog proposal output directory already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func readCurrentDiscoveryCatalog(branch string) (discoveryCatalogBundle, error) {
	backend, err := newFactoryStateBackend(branch)
	if err != nil {
		return discoveryCatalogBundle{}, err
	}
	_, snapshot, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		return discoveryCatalogBundle{}, fmt.Errorf("read accepted discovery state: %w", err)
	}
	accepted, err := acceptedDiscoveryCatalog(snapshot)
	if err != nil {
		return discoveryCatalogBundle{}, err
	}
	outbox, err := currentCatalogOutbox(snapshot, accepted.Generation)
	if err != nil {
		return discoveryCatalogBundle{}, err
	}
	return discoveryCatalogBundle{accepted: accepted, outbox: outbox}, nil
}

func writeDiscoveryCatalogProposal(outputRoot, expectedBase, dataRoot string, bundle discoveryCatalogBundle) error {
	modules, err := catalogIndexEntries(bundle.accepted)
	if err != nil {
		return err
	}
	metadata, err := catalogModuleMetadata(bundle.accepted)
	if err != nil {
		return err
	}
	expectedPointer, err := currentCatalogPointer(dataRoot)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(outputRoot), 0o755); err != nil {
		return fmt.Errorf("create catalog proposal parent: %w", err)
	}
	if err = os.Mkdir(outputRoot, 0o755); err != nil {
		return fmt.Errorf("create fresh catalog proposal directory: %w", err)
	}
	proposal, err := publish.BuildCatalogIndexProposal(publish.CatalogIndexProposalInput{
		ProposalRoot: outputRoot, BaseDataRoot: dataRoot, Outbox: bundle.outbox,
		Generation: bundle.accepted.Generation, Modules: modules, Metadata: metadata,
		ExpectedBase: expectedBase, ExpectedPointer: expectedPointer,
	})
	if err != nil {
		return fmt.Errorf("build discovery catalog proposal: %w", err)
	}
	proposalBytes, err := json.MarshalIndent(proposal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode discovery catalog proposal: %w", err)
	}
	proposalPath := filepath.Join(outputRoot, "proposal.json")
	if err = os.WriteFile(proposalPath, proposalBytes, 0o644); err != nil {
		return fmt.Errorf("write discovery catalog proposal: %w", err)
	}
	slog.Info("catalog proposal built", "generation", bundle.accepted.Generation, "modules", len(modules), "proposal_id", proposal.ProposalID, "output", outputRoot)
	return nil
}

// acceptedDiscoveryCatalog loads and validates the current durable catalog
// reducer state from the immutable discovery state record.
func acceptedDiscoveryCatalog(snapshot state.Snapshot) (catalog.AcceptedState, error) {
	record, exists := snapshot.Records[discoveryCatalogStatePath]
	if !exists || record.Kind != "discovery_catalog" || record.SchemaVersion != 1 {
		return catalog.AcceptedState{}, fmt.Errorf("accepted discovery catalog state is missing or has an unsupported identity")
	}
	var durable discovery.DurableCatalogState
	if err := json.Unmarshal(record.Data, &durable); err != nil {
		return catalog.AcceptedState{}, fmt.Errorf("decode accepted discovery catalog state: %w", err)
	}
	if durable.SchemaVersion != 1 {
		return catalog.AcceptedState{}, fmt.Errorf("accepted discovery catalog state has an unsupported schema")
	}
	if err := durable.Accepted.Validate(); err != nil {
		return catalog.AcceptedState{}, fmt.Errorf("validate accepted discovery catalog state: %w", err)
	}
	return durable.Accepted, nil
}

// currentCatalogOutbox returns the unique catalog proposal for the accepted
// generation, validating its content-addressed state path.
func currentCatalogOutbox(snapshot state.Snapshot, generation uint64) (discovery.CatalogProposal, error) {
	var match *discovery.CatalogProposal
	for recordPath, record := range snapshot.Records {
		outbox, found, err := catalogOutboxForGeneration(recordPath, record, generation)
		if err != nil {
			return discovery.CatalogProposal{}, err
		}
		if !found {
			continue
		}
		if match != nil {
			return discovery.CatalogProposal{}, fmt.Errorf("multiple catalog proposal outboxes exist for generation %d", generation)
		}
		candidateOutbox := outbox
		match = &candidateOutbox
	}
	if match == nil {
		return discovery.CatalogProposal{}, fmt.Errorf("catalog proposal outbox for discovery generation %d is missing", generation)
	}
	return *match, nil
}

func catalogOutboxForGeneration(recordPath string, record state.Record, generation uint64) (discovery.CatalogProposal, bool, error) {
	if record.Kind != "export" || record.SchemaVersion != 1 {
		return discovery.CatalogProposal{}, false, nil
	}
	var header struct {
		ProposalKind string `json:"proposal_kind"`
	}
	if err := json.Unmarshal(record.Data, &header); err != nil {
		return discovery.CatalogProposal{}, false, fmt.Errorf("decode export record %s: %w", recordPath, err)
	}
	if header.ProposalKind != "catalog" {
		return discovery.CatalogProposal{}, false, nil
	}
	var outbox discovery.CatalogProposal
	if err := json.Unmarshal(record.Data, &outbox); err != nil {
		return discovery.CatalogProposal{}, false, fmt.Errorf("decode catalog proposal outbox %s: %w", recordPath, err)
	}
	if outbox.Generation != generation {
		return discovery.CatalogProposal{}, false, nil
	}
	proposalID, err := identity.ID("catalog-proposal", 1, outbox)
	if err != nil {
		return discovery.CatalogProposal{}, false, fmt.Errorf("identify catalog proposal outbox: %w", err)
	}
	if recordPath != discoveryExportsPrefix+proposalID+".json" {
		return discovery.CatalogProposal{}, false, fmt.Errorf("catalog proposal outbox path does not match its identity")
	}
	return outbox, true, nil
}

// catalogIndexEntries maps accepted module metadata to the exact catalog
// membership and lifecycle recorded in discovery state.
func catalogIndexEntries(accepted catalog.AcceptedState) ([]index.CatalogModuleEntry, error) {
	modules := make([]index.CatalogModuleEntry, 0, len(accepted.Modules))
	for _, record := range accepted.Modules {
		repository, err := repositoryIdentity(record.Module.RepositoryURL)
		if err != nil {
			return nil, fmt.Errorf("module %s repository identity: %w", record.Module.ID, err)
		}
		lifecycle := "active"
		if !record.Active {
			lifecycle = "retired"
		}
		modules = append(modules, index.CatalogModuleEntry{ModuleID: record.Module.ID, Repository: repository, Lifecycle: lifecycle})
	}
	sort.Slice(modules, func(left, right int) bool { return modules[left].ModuleID < modules[right].ModuleID })
	return modules, nil
}

func catalogModuleMetadata(accepted catalog.AcceptedState) ([]publish.CatalogModuleMetadata, error) {
	metadata := make([]publish.CatalogModuleMetadata, 0, len(accepted.Modules))
	for _, record := range accepted.Modules {
		module := record.Module
		repository, err := repositoryIdentity(module.RepositoryURL)
		if err != nil {
			return nil, fmt.Errorf("module %s repository identity: %w", module.ID, err)
		}
		lifecycle := "active"
		if !record.Active {
			lifecycle = "retired"
		}
		metadata = append(metadata, publish.CatalogModuleMetadata{
			SchemaVersion: 2, RecordType: "module_metadata", ModuleID: module.ID,
			ModuleType: string(module.Type), Status: module.Status, Repository: repository,
			Lifecycle: lifecycle, DisplayName: module.DisplayName, RegistryURL: module.RegistryURL,
			AlternativeNames: module.AlternativeNames, Description: module.Description, Comments: module.Comments,
			FirstPublished: module.FirstPublished, ProviderNamespace: module.ProviderNamespace,
			ResourceType: module.ResourceType, ParentModule: module.ParentModule,
			PrimaryOwnerHandle: module.PrimaryOwnerHandle, PrimaryOwnerName: module.PrimaryOwnerName,
			SecondaryOwnerHandle: module.SecondaryOwnerHandle, SecondaryOwnerName: module.SecondaryOwnerName,
			MissingObservations: record.MissingObservations, LastSeenGeneration: record.LastSeenGeneration,
		})
	}
	sort.Slice(metadata, func(left, right int) bool { return metadata[left].ModuleID < metadata[right].ModuleID })
	return metadata, nil
}

// repositoryIdentity accepts only ordinary HTTPS GitHub owner/repository URLs.
func repositoryIdentity(repositoryURL string) (string, error) {
	parsed, err := url.Parse(repositoryURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("repository URL is not an ordinary GitHub HTTPS URL")
	}
	repository := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	if !catalogRepositoryPattern.MatchString(repository) {
		return "", fmt.Errorf("repository URL does not contain one owner/repository pair")
	}
	return repository, nil
}

// currentCatalogPointer reads the existing canonical pointer without mutating it.
func currentCatalogPointer(dataRoot string) (*string, error) {
	data, err := os.ReadFile(filepath.Join(dataRoot, "catalog.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read current catalog pointer: %w", err)
	}
	var current index.CatalogIndex
	if err = json.Unmarshal(data, &current); err != nil {
		return nil, fmt.Errorf("decode current catalog pointer: %w", err)
	}
	if current.SchemaVersion != 1 || current.RecordType != "catalog_index" || current.IndexID == "" {
		return nil, fmt.Errorf("current catalog pointer has an unsupported identity")
	}
	return &current.IndexID, nil
}

// lowerHexSHA validates a lowercase hexadecimal SHA of the requested length.
func lowerHexSHA(value string, length int) bool {
	if len(value) != length || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
