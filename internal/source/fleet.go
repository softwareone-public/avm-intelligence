package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
)

// ResolveAccepted resolves every active module in accepted discovery state.
// Previous pins are optional on the first run and are verified when present.
// Modules without a published stable release are omitted for the planner to
// mark blocked; malformed input, API failures, and moved tags remain fatal.
func (r Resolver) ResolveAccepted(ctx context.Context, state catalog.AcceptedState, previousDir string) ([]ResolvedSource, error) {
	return r.resolveAccepted(ctx, state, previousDir, nil, nil)
}

// ResolveAcceptedKinds resolves only active modules whose catalog kind is in
// allowedKinds. A nil map resolves every active module.
func (r Resolver) ResolveAcceptedKinds(ctx context.Context, state catalog.AcceptedState, previousDir string, allowedKinds map[catalog.Kind]bool) ([]ResolvedSource, error) {
	return r.resolveAccepted(ctx, state, previousDir, allowedKinds, nil)
}

// ResolveAcceptedSelection resolves active modules matching both optional
// filters. A nil filter means all values are allowed.
func (r Resolver) ResolveAcceptedSelection(ctx context.Context, state catalog.AcceptedState, previousDir string, allowedKinds map[catalog.Kind]bool, allowedIDs map[string]bool) ([]ResolvedSource, error) {
	return r.resolveAccepted(ctx, state, previousDir, allowedKinds, allowedIDs)
}

func (r Resolver) resolveAccepted(ctx context.Context, state catalog.AcceptedState, previousDir string, allowedKinds map[catalog.Kind]bool, allowedIDs map[string]bool) ([]ResolvedSource, error) {
	if err := state.Validate(); err != nil {
		return nil, fmt.Errorf("accepted discovery state: %w", err)
	}
	active := make([]catalog.Module, 0, len(state.Modules))
	for _, record := range state.Modules {
		if record.Active && (allowedKinds == nil || allowedKinds[record.Module.Type]) && (allowedIDs == nil || allowedIDs[record.Module.ID]) {
			active = append(active, record.Module)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].ID < active[j].ID })

	resolved := make([]ResolvedSource, 0, len(active))
	var resolutionErrors []error
	for _, module := range active {
		previous, err := loadPrevious(previousDir, module.ID)
		if err != nil {
			resolutionErrors = append(resolutionErrors, err)
			continue
		}
		pin, err := r.ResolveLatest(ctx, module, previous)
		if err != nil {
			if isExpectedUnresolved(err) {
				continue
			}
			resolutionErrors = append(resolutionErrors, fmt.Errorf("resolve %s: %w", module.ID, err))
			continue
		}
		resolved = append(resolved, pin)
	}
	if len(resolutionErrors) > 0 {
		return nil, errors.Join(resolutionErrors...)
	}
	return resolved, nil
}

func isExpectedUnresolved(err error) bool {
	return strings.Contains(err.Error(), "no published stable SemVer release")
}

func loadPrevious(directory, moduleID string) (*ResolvedSource, error) {
	if directory == "" {
		return nil, nil
	}
	path := filepath.Join(directory, moduleID+".json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read previous source pin for %s: %w", moduleID, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var pin ResolvedSource
	if err = decoder.Decode(&pin); err != nil {
		return nil, fmt.Errorf("parse previous source pin for %s: %w", moduleID, err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("previous source pin for %s must contain one JSON document", moduleID)
	}
	if pin.ModuleID != moduleID {
		return nil, fmt.Errorf("previous source pin identity mismatch for %s", moduleID)
	}
	return &pin, nil
}
