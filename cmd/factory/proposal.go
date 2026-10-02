package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/publish"
)

func handleProposalCommand(apply bool, proposalRoot, expectedBase, baseDataRoot, outputDataRoot, changedPathsPath string) (bool, error) {
	if !apply {
		if proposalRoot != "" || expectedBase != "" || changedPathsPath != "" {
			return true, fmt.Errorf("proposal options require --apply-proposal")
		}
		return false, nil
	}
	if proposalRoot == "" || expectedBase == "" || baseDataRoot == "" || outputDataRoot == "" || changedPathsPath == "" {
		return true, fmt.Errorf("--apply-proposal requires proposal root, expected base, accepted data base, candidate output, and changed-paths file")
	}
	changedPaths, err := readProposalChangedPaths(changedPathsPath)
	if err != nil {
		return true, err
	}
	if err = publish.ApplyProposalPatch(proposalRoot, expectedBase, baseDataRoot, outputDataRoot, changedPaths); err != nil {
		return true, err
	}
	fmt.Printf("applied typed proposal to candidate data at %s\n", outputDataRoot)
	return true, nil
}

func readProposalChangedPaths(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read proposal changed paths: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] != 0 {
		return nil, fmt.Errorf("proposal changed-paths file must be NUL-delimited")
	}
	parts := strings.Split(string(data[:len(data)-1]), "\x00")
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("proposal changed-paths file contains an empty path")
		}
	}
	return parts, nil
}
