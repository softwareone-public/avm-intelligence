package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

func TestColdStartSealsFactsBeforeIndependentAssessments(t *testing.T) {
	root := t.TempDir()
	writeWorkerSource(t, root, "versions.tf", `terraform {
  required_providers {
    azurerm = {
      source = "hashicorp/azurerm"
    }
  }
}`)
	writeWorkerSource(t, root, "main.tf", `resource "azurerm_resource_group" "main" {}`)
	writeWorkerSource(t, root, ".terraform.lock.hcl", `provider "registry.terraform.io/hashicorp/azurerm" {
  version = "4.2.0"
  hashes  = ["h1:NOT_SERIALIZED"]
}`)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	moduleID := "avm-res-worker-demo"
	sourceSHA := strings.Repeat("a", 40)
	claimedAcquisition, owner := claimColdStartAcquisition(t, moduleID, sourceSHA, now)
	facts := processAndAcceptColdStartAcquisition(t, claimedAcquisition, owner, root, now)
	runIndependentAssessments(t, facts, moduleID, sourceSHA, now)
}

func claimColdStartAcquisition(t *testing.T, moduleID, sourceSHA string, now time.Time) (queue.Request, string) {
	t.Helper()
	acquisition, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: moduleID, SourceSHA: sourceSHA, DesiredGeneration: 1,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{terraform.CapabilityProviderRequirements, terraform.CapabilityProviderLockSelections},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	owner := "actions-run-acquire"
	claimed, err := queue.ClaimModuleRequest([]queue.Request{acquisition}, acquisition.ID, owner, now, now.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	return claimed[0], owner
}

func processAndAcceptColdStartAcquisition(t *testing.T, request queue.Request, owner, root string, now time.Time) *terraform.Facts {
	t.Helper()
	artifact, err := Process(Input{
		Request: request, Owner: owner, Fence: 1, SourceRoot: root, ObservedAt: now,
		Producer: Producer{ID: "avm-intelligence", Version: "2.0.0", ContractVersion: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Facts == nil || len(artifact.DimensionResults) != 0 || artifact.Analysis != nil {
		t.Fatalf("acquisition artifact mixed assessment outputs: %#v", artifact)
	}
	assertProviderLockSelection(t, *artifact.Facts)
	sealedAcquisition, err := Seal(request, owner, 1, artifact, "trusted-validator.v1", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if sealedAcquisition.Status != queue.StatusAwaitingAcceptance || sealedAcquisition.Work.Lease != nil || sealedAcquisition.Work.Completion == nil {
		t.Fatalf("facts completion did not seal before lease release: %#v", sealedAcquisition)
	}
	acceptedFacts, err := queue.AcceptFacts(sealedAcquisition, sealedAcquisition.Work.Completion.ID, artifact.Facts.FactsID, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if acceptedFacts.Status != queue.StatusFactsAccepted || acceptedFacts.Status == queue.StatusAccepted {
		t.Fatalf("accepted facts were confused with accepted analysis: %#v", acceptedFacts)
	}
	return artifact.Facts
}

func assertProviderLockSelection(t *testing.T, facts terraform.Facts) {
	t.Helper()
	for _, unit := range facts.Units {
		for _, symbol := range unit.Symbols {
			if symbol.Kind == "provider_lock" && symbol.ProviderAddress == "registry.terraform.io/hashicorp/azurerm" && symbol.ProviderVersionSelection == "4.2.0" {
				return
			}
		}
	}
	t.Fatalf("trusted acquisition did not include its exact provider lock selection: %#v", facts.Units)
}

func runIndependentAssessments(t *testing.T, facts *terraform.Facts, moduleID, sourceSHA string, now time.Time) {
	t.Helper()
	assessmentRequests := buildAssessmentRequests(t, facts.FactsID, moduleID, sourceSHA, now)
	for position, request := range assessmentRequests {
		assessmentRequests[position] = processAndSealAssessment(t, request, position, facts, now)
	}
	if assessmentRequests[0].Work.ContextID == assessmentRequests[1].Work.ContextID || assessmentRequests[0].Fingerprint == assessmentRequests[1].Fingerprint {
		t.Fatalf("context assessments were not independently identified: %#v", assessmentRequests)
	}
}

func buildAssessmentRequests(t *testing.T, factsID, moduleID, sourceSHA string, now time.Time) []queue.Request {
	t.Helper()
	registry, err := assessment.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var providerDefinitionID string
	for _, definition := range registry.ListDimensions() {
		if definition.DimensionID == assessment.DimensionProviderRequirements {
			providerDefinitionID = definition.DefinitionID
		}
	}
	if providerDefinitionID == "" {
		t.Fatal("provider requirements definition is missing")
	}
	requests, err := queue.WakeAssessmentRequests(factsID, []queue.AssessmentInput{
		{ModuleID: moduleID, SourceSHA: sourceSHA, ContextID: "azurerm-4", Track: "azurerm-4", DesiredGeneration: 2, DefinitionIDs: []string{providerDefinitionID}},
		{ModuleID: moduleID, SourceSHA: sourceSHA, ContextID: "azurerm-5", Track: "azurerm-5", DesiredGeneration: 2, DefinitionIDs: []string{providerDefinitionID}},
	}, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return requests
}

func processAndSealAssessment(t *testing.T, request queue.Request, position int, facts *terraform.Facts, now time.Time) queue.Request {
	t.Helper()
	owner := "actions-run-assess-" + request.Work.ContextID
	claimed, err := queue.ClaimModuleRequest(
		[]queue.Request{request}, request.ID, owner,
		now.Add(4*time.Minute+time.Duration(position)*time.Minute),
		now.Add(5*time.Minute+time.Duration(position)*time.Minute), uint64(position+2),
	)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Process(Input{
		Request: claimed[0], Owner: owner, Fence: uint64(position + 2),
		Facts: facts, ObservedAt: now.Add(4 * time.Minute),
		Producer: Producer{ID: "avm-intelligence", Version: "2.0.0", ContractVersion: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Facts != nil || artifact.FactsID != facts.FactsID || artifact.Analysis == nil || len(artifact.DimensionResults) == 0 {
		t.Fatalf("assessment did not reuse accepted facts: %#v", artifact)
	}
	sealed, err := Seal(claimed[0], owner, uint64(position+2), artifact, "trusted-validator.v1", now.Add(6*time.Minute+time.Duration(position)*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Status != queue.StatusAwaitingAcceptance || sealed.Work.Lease != nil {
		t.Fatalf("assessment compute lease was not released after trusted seal: %#v", sealed)
	}
	return sealed
}

func writeWorkerSource(t *testing.T, root, relativePath, source string) {
	t.Helper()
	path := filepath.Join(root, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
}
