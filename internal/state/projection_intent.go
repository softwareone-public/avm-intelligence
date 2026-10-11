package state

import (
	"encoding/json"
	"fmt"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
)

// ModuleProjectionGeneration keeps a release-scoped work generation from
// moving the module-wide desired projection backward. Projection transitions
// at the same generation remain distinct through their intent identities.
func ModuleProjectionGeneration(snapshot Snapshot, moduleID string, workGeneration uint64) (uint64, error) {
	if workGeneration == 0 {
		return 0, fmt.Errorf("module projection work generation is required")
	}
	path, err := identity.StateProjectionIntentPath(moduleID)
	if err != nil {
		return 0, err
	}
	record, exists := snapshot.Records[path]
	if !exists {
		return workGeneration, nil
	}
	if record.Kind != "projection_intent" || record.SchemaVersion != projection.SchemaVersion {
		return 0, fmt.Errorf("module projection intent has an invalid record type")
	}
	var current projection.DesiredIntent
	if json.Unmarshal(record.Data, &current) != nil || current.ValidatePath(path) != nil {
		return 0, fmt.Errorf("module projection intent is invalid")
	}
	if current.Generation > workGeneration {
		return current.Generation, nil
	}
	return workGeneration, nil
}
