package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func buildPendingSelectionProposal(branch, checkoutRoot, selectionID, moduleID, expectedBase, dataRoot, outputRoot string) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	if err := identity.ValidateRecordID("selection", selectionID); err != nil {
		return fmt.Errorf("selection export ID is invalid: %w", err)
	}
	if _, err := identity.ModuleMetadataPath(moduleID); err != nil {
		return fmt.Errorf("selection export module ID is invalid: %w", err)
	}
	if identity.ValidateSHA1(expectedBase) != nil || strings.TrimSpace(dataRoot) == "" || strings.TrimSpace(outputRoot) == "" {
		return fmt.Errorf("selection export requires an exact base commit, accepted data root, and output directory")
	}
	if err := ensureOutputOutsideData(dataRoot, outputRoot); err != nil {
		return fmt.Errorf("unsafe selection proposal output path: %w", err)
	}
	if _, err := os.Lstat(outputRoot); err == nil {
		return fmt.Errorf("selection proposal output directory already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return err
	}
	_, snapshot, err := setup.coordinator.Read(context.Background())
	if err != nil {
		return fmt.Errorf("read durable selection exports: %w", err)
	}
	exports, err := state.PendingSelectionExports(snapshot)
	if err != nil {
		return fmt.Errorf("validate pending selection exports: %w", err)
	}
	var selected *state.SelectionExport
	for position := range exports {
		export := exports[position]
		if export.ID != selectionID {
			continue
		}
		if export.ModuleID != moduleID {
			return fmt.Errorf("selection export ID belongs to a different module")
		}
		if selected != nil {
			return fmt.Errorf("selection export ID is not unique in durable state")
		}
		selected = &export
	}
	if selected == nil {
		return fmt.Errorf("requested selection export is not pending in durable state")
	}
	artifacts, err := publish.BuildSelectionExportProposal(dataRoot, expectedBase, *selected)
	if err != nil {
		return fmt.Errorf("build exact selection export proposal: %w", err)
	}
	proposalBytes, err := json.MarshalIndent(artifacts.Proposal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode selection proposal: %w", err)
	}
	if err = writeFreshSelectionProposal(outputRoot, artifacts.Payloads, proposalBytes); err != nil {
		return err
	}
	slog.Info("selection export proposal built", "module_id", moduleID, "selection_id", selectionID,
		"avm_release_version", selected.Receipt.AVMReleaseVersion, "context_id", selected.ContextID,
		"proposal_id", artifacts.Proposal.ProposalID, "paths", len(artifacts.Proposal.Paths), "output", outputRoot)
	return nil
}

func writeFreshSelectionProposal(outputRoot string, payloads map[string][]byte, proposal []byte) error {
	parent, err := filepath.Abs(filepath.Dir(outputRoot))
	if err != nil {
		return fmt.Errorf("resolve selection proposal parent: %w", err)
	}
	if err = os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create selection proposal parent: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".selection-proposal-")
	if err != nil {
		return fmt.Errorf("create selection proposal staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	paths := make([]string, 0, len(payloads))
	for canonicalPath := range payloads {
		if err = identity.ValidateRelativePath(canonicalPath, false); err != nil || !strings.HasPrefix(canonicalPath, "data/") {
			return fmt.Errorf("selection proposal contains an invalid data path")
		}
		paths = append(paths, canonicalPath)
	}
	sort.Strings(paths)
	for _, canonicalPath := range paths {
		destination := filepath.Join(staging, filepath.FromSlash(canonicalPath))
		if err = os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return fmt.Errorf("create selection proposal payload directory: %w", err)
		}
		if err = os.WriteFile(destination, payloads[canonicalPath], 0o644); err != nil {
			return fmt.Errorf("write selection proposal payload: %w", err)
		}
	}
	if err = os.WriteFile(filepath.Join(staging, "proposal.json"), proposal, 0o644); err != nil {
		return fmt.Errorf("write selection proposal envelope: %w", err)
	}
	if _, err = os.Lstat(outputRoot); err == nil {
		return fmt.Errorf("selection proposal output directory appeared during generation")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = os.Rename(staging, outputRoot); err != nil {
		return fmt.Errorf("publish selection proposal output: %w", err)
	}
	return nil
}
