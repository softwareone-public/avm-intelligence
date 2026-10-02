package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func importEvidenceAcceptance(branch, inputPath string) error {
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
	backend, err := newFactoryStateBackend(branch)
	if err != nil {
		return err
	}
	head, _, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil {
		return fmt.Errorf("read current factory-state head: %w", err)
	}
	result, err := state.ImportEvidenceReceipt(context.Background(), backend, head, input)
	if err != nil {
		return fmt.Errorf("import merged evidence receipt: %w", err)
	}
	fmt.Printf("evidence receipt %s for %s imported at state head %s (changed=%t)\n", result.Receipt.ID, result.Receipt.ModuleID, result.Head, result.Changed)
	return nil
}
