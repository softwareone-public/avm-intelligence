package main

import (
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/provider"
)

func TestValidateProviderSnapshotInputImportBindsWorkflowToAcceptedCommit(t *testing.T) {
	snapshotID, err := identity.ID("snapshot", 1, struct {
		Fixture string `json:"fixture"`
	}{"provider-input-import"})
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	factsScope := make([]provider.AcceptedFactsReference, 0, len(approvedProviderFactsScopeModules))
	for moduleID := range approvedProviderFactsScopeModules {
		factsScope = append(factsScope, provider.AcceptedFactsReference{ModuleID: moduleID, SourceSHA: strings.Repeat("b", 40), FactsID: "facts-" + strings.Repeat("c", 64)})
	}
	input := providerSnapshotInputImport{
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", ProviderVersion: "4.20.0",
		SnapshotID: snapshotID, AcceptedCommit: commit, ImportedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
		FactsScope: factsScope,
		Producer:   projection.WorkflowProvenance{WorkflowID: "manual-import-provider-snapshot", Commit: commit, RunID: "12345", RunAttempt: 1},
	}
	if err = validateProviderSnapshotInputImport(input); err != nil {
		t.Fatalf("valid exact provider input import: %v", err)
	}
	input.Producer.Commit = strings.Repeat("b", 40)
	if err = validateProviderSnapshotInputImport(input); err == nil {
		t.Fatal("provider import accepted producer provenance from a different commit")
	}
}

func TestProviderVersionMajorAcceptsMajorZero(t *testing.T) {
	major, err := providerVersionMajor("0.4.0")
	if err != nil || major != 0 {
		t.Fatalf("major-zero provider version = %d, error=%v", major, err)
	}
	if _, err = providerVersionMajor("0.4.0-rc1"); err == nil {
		t.Fatal("prerelease provider version was accepted")
	}
}

func TestProviderSnapshotInputImportRequiresPayloadAndIsExclusive(t *testing.T) {
	if err := run([]string{"--import-provider-snapshot-inputs"}); err == nil {
		t.Fatal("provider snapshot input import without exact payload was accepted")
	}
	if err := run([]string{"--import-provider-snapshot-inputs", "--provider-snapshot-input-import", "input.json", "--assess-facts", "facts.json"}); err == nil {
		t.Fatal("provider snapshot input import combined with assessment was accepted")
	}
}
