package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ensureOutputOutsideData keeps generated candidates separate from the
// accepted input tree; publishing remains a distinct reviewed operation.
func ensureOutputOutsideData(dataRoot, outputPath string) error {
	rootAbs, err := filepath.Abs(dataRoot)
	if err != nil {
		return fmt.Errorf("resolve data root: %w", err)
	}
	outputAbs, err := filepath.Abs(outputPath)
	if err != nil {
		return fmt.Errorf("resolve candidate output: %w", err)
	}
	relative, err := filepath.Rel(rootAbs, outputAbs)
	if err != nil {
		return fmt.Errorf("compare candidate output and data root: %w", err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("candidate output must be outside the accepted data root")
	}
	return nil
}
