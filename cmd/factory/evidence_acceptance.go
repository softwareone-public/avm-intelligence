package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/provider"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func importEvidenceAcceptance(branch, checkoutRoot, inputPath, acceptedDataRoot, assessmentTrackPolicy string) error {
	if err := validateFactoryStateBranch(branch); err != nil {
		return err
	}
	if strings.TrimSpace(inputPath) == "" {
		return fmt.Errorf("--evidence-acceptance-input is required")
	}
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("read evidence acceptance input: %w", err)
	}
	var input state.EvidenceAcceptanceInput
	if err = decodeStrictJSON(data, &input); err != nil {
		return fmt.Errorf("decode evidence acceptance input: %w", err)
	}
	if input.Phase == "acquire_source" {
		if strings.TrimSpace(acceptedDataRoot) == "" || strings.TrimSpace(assessmentTrackPolicy) == "" {
			return fmt.Errorf("facts acceptance requires an exact accepted data tree and reviewed assessment track policy")
		}
		facts, loadErr := provider.LoadAcceptedModuleFacts(acceptedDataRoot, input.ModuleID, input.SourceSHA, input.FactsID)
		if loadErr != nil {
			return fmt.Errorf("load exact accepted facts for context derivation: %w", loadErr)
		}
		policy, policyErr := provider.LoadCollectionPolicy(assessmentTrackPolicy)
		if policyErr != nil {
			return fmt.Errorf("load reviewed assessment track policy: %w", policyErr)
		}
		contexts, deriveErr := provider.DeriveRequiredAssessmentContexts(facts, input.AVMReleaseVersion, policy)
		if deriveErr != nil {
			return fmt.Errorf("derive required assessment contexts: %w", deriveErr)
		}
		input.RequiredContexts = &contexts
	}
	setup, err := newFactoryStateSetup(branch, checkoutRoot)
	if err != nil {
		return err
	}
	result, err := state.ImportEvidenceReceipt(context.Background(), setup.coordinator, input)
	if err != nil {
		return fmt.Errorf("import merged evidence receipt: %w", err)
	}
	slog.Info("evidence receipt imported", "receipt_id", result.Receipt.ID, "module_id", result.Receipt.ModuleID, "state_head", result.Head, "changed", result.Changed)
	return nil
}
