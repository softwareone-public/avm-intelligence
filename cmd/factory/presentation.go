package main

import (
	"fmt"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/site"
)

func exportPresentationProposal(dataRoot, proposalRoot, expectedBase, asOf string) error {
	observedAt, err := time.Parse(time.RFC3339, asOf)
	if err != nil {
		return fmt.Errorf("presentation as-of must be RFC3339: %w", err)
	}
	proposal, err := site.ExportPresentation(site.PresentationExportInput{
		DataRoot: dataRoot, ProposalRoot: proposalRoot, ExpectedBase: expectedBase, AsOf: observedAt,
	})
	if err != nil {
		return err
	}
	fmt.Printf("presentation proposal %s generation=%d paths=%d\n", proposal.ProposalID, proposal.SourceGeneration, len(proposal.Paths))
	return nil
}
