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

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
)

// ResolveAccepted resolves every active module in accepted discovery state.
// Previous pins are optional on the first run and are verified when present.
// Resolution is all-or-nothing so a partial pin set cannot be mistaken for a
// complete fleet input by the planner.
func (r Resolver) ResolveAccepted(ctx context.Context, state catalog.AcceptedState, previousDir string) ([]ResolvedSource, error) {
	if err := state.Validate(); err != nil {
		return nil, fmt.Errorf("accepted discovery state: %w", err)
	}
	active := make([]catalog.Module, 0, len(state.Modules))
	for _, record := range state.Modules {
		if record.Active {
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
