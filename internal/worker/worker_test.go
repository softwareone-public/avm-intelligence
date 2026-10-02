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

	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	moduleID := "avm-res-worker-demo"
	sourceSHA := strings.Repeat("a", 40)
	acquisition, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: moduleID, SourceSHA: sourceSHA, DesiredGeneration: 1,
		ExtractorContract: "factory-hcl-v1", Capabilities: []string{terraform.CapabilityProviderRequirements},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	owner := "actions-run-acquire"
	claimed, err := queue.ClaimModuleRequest([]queue.Request{acquisition}, acquisition.ID, owner, now, now.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	acquisitionArtifact, err := Process(Input{
		Request: claimed[0], Owner: owner, Fence: 1, SourceRoot: root, ObservedAt: now,
		Producer: Producer{ID: "avm-intelligence", Version: "2.0.0", ContractVersion: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if acquisitionArtifact.Facts == nil || len(acquisitionArtifact.DimensionResults) != 0 || acquisitionArtifact.Analysis != nil {
		t.Fatalf("acquisition artifact mixed assessment outputs: %#v", acquisitionArtifact)
	}
	sealedAcquisition, err := Seal(claimed[0], owner, 1, acquisitionArtifact, "trusted-validator.v1", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if sealedAcquisition.Status != queue.StatusAwaitingAcceptance || sealedAcquisition.Work.Lease != nil || sealedAcquisition.Work.Completion == nil {
		t.Fatalf("facts completion did not seal before lease release: %#v", sealedAcquisition)
	}
	acceptedFacts, err := queue.AcceptFacts(sealedAcquisition, sealedAcquisition.Work.Completion.ID, acquisitionArtifact.Facts.FactsID, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if acceptedFacts.Status != queue.StatusFactsAccepted || acceptedFacts.Status == queue.StatusAccepted {
		t.Fatalf("accepted facts were confused with accepted analysis: %#v", acceptedFacts)
	}

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
	assessmentRequests, err := queue.WakeAssessmentRequests(acquisitionArtifact.Facts.FactsID, []queue.AssessmentInput{
		{ModuleID: moduleID, SourceSHA: sourceSHA, ContextID: "azurerm-4", Track: "azurerm-4", DesiredGeneration: 2, DefinitionIDs: []string{providerDefinitionID}},
		{ModuleID: moduleID, SourceSHA: sourceSHA, ContextID: "azurerm-5", Track: "azurerm-5", DesiredGeneration: 2, DefinitionIDs: []string{providerDefinitionID}},
	}, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for position := range assessmentRequests {
		assessmentOwner := "actions-run-assess-" + assessmentRequests[position].Work.ContextID
		claimedAssessments, claimErr := queue.ClaimModuleRequest(
			assessmentRequests, assessmentRequests[position].ID, assessmentOwner,
			now.Add(4*time.Minute+time.Duration(position)*time.Minute),
			now.Add(5*time.Minute+time.Duration(position)*time.Minute), uint64(position+2),
		)
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		assessmentArtifact, processErr := Process(Input{
			Request: claimedAssessments[position], Owner: assessmentOwner, Fence: uint64(position + 2),
			Facts: acquisitionArtifact.Facts, ObservedAt: now.Add(4 * time.Minute),
			Producer: Producer{ID: "avm-intelligence", Version: "2.0.0", ContractVersion: 1},
		})
		if processErr != nil {
			t.Fatal(processErr)
		}
		if assessmentArtifact.Facts != nil || assessmentArtifact.FactsID != acquisitionArtifact.Facts.FactsID || assessmentArtifact.Analysis == nil || len(assessmentArtifact.DimensionResults) == 0 {
			t.Fatalf("assessment did not reuse accepted facts: %#v", assessmentArtifact)
		}
		sealedAssessment, sealErr := Seal(claimedAssessments[position], assessmentOwner, uint64(position+2), assessmentArtifact, "trusted-validator.v1", now.Add(6*time.Minute+time.Duration(position)*time.Minute))
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		if sealedAssessment.Status != queue.StatusAwaitingAcceptance || sealedAssessment.Work.Lease != nil {
			t.Fatalf("assessment compute lease was not released after trusted seal: %#v", sealedAssessment)
		}
		assessmentRequests[position] = sealedAssessment
	}
	if assessmentRequests[0].Work.ContextID == assessmentRequests[1].Work.ContextID || assessmentRequests[0].Fingerprint == assessmentRequests[1].Fingerprint {
		t.Fatalf("context assessments were not independently identified: %#v", assessmentRequests)
	}
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
