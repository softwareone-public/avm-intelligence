package identity

import (
	"fmt"
	"path"
	"strings"
)

const (
	LegacyLayout = "legacy-v1"
	ModuleLayout = "module-v2"
)

// LayoutContract fixes the physical ownership contract independently of runtime configuration.
type LayoutContract struct {
	SchemaVersion int    `json:"schema_version" yaml:"schema_version"`
	Profile       string `json:"profile" yaml:"profile"`
	DataModules   string `json:"data_modules" yaml:"data_modules"`
	StateModules  string `json:"state_modules" yaml:"state_modules"`
	StateGlobal   string `json:"state_global" yaml:"state_global"`
}

// ModuleLayoutContract returns the supported, declarative module layout.
func ModuleLayoutContract() LayoutContract {
	return LayoutContract{SchemaVersion: 1, Profile: ModuleLayout, DataModules: "data/modules", StateModules: "state/modules", StateGlobal: "state/global"}
}

// Digest binds a persisted tree to an exact supported contract.
func (contract LayoutContract) Digest() (string, error) {
	if contract != ModuleLayoutContract() {
		return "", fmt.Errorf("unsupported layout contract")
	}
	return ID("layout-contract", 1, contract)
}

// OperationalModuleRoot resolves one validated canonical module owner.
func OperationalModuleRoot(moduleID string) (string, error) {
	moduleType, err := moduleTypeForID(moduleID)
	if err != nil {
		return "", err
	}
	return path.Join("state", "modules", moduleType, moduleID), nil
}

// OperationalRecordPath maps stable logical event paths to their physical owner.
// Logical paths remain unchanged so historical event and proposal identities survive.
func OperationalRecordPath(logicalPath, kind, moduleID string) (string, error) {
	if err := ValidateRelativePath(logicalPath, false); err != nil {
		return "", err
	}
	if !strings.HasPrefix(logicalPath, "state/") || path.Ext(logicalPath) != ".json" {
		return "", fmt.Errorf("invalid logical operational path")
	}
	filename := path.Base(logicalPath)
	moduleLeaf := ""
	switch kind {
	case "source_pins":
		moduleLeaf = "source/pins.json"
	case "module":
		moduleLeaf = "assessment-inputs.json"
	case "request":
		moduleLeaf = path.Join("requests", filename)
	case "attempt":
		moduleLeaf = path.Join("attempts", path.Base(path.Dir(logicalPath)), filename)
	case "selection":
		var err error
		moduleLeaf, err = operationalSelectionLeaf(logicalPath, moduleID)
		if err != nil {
			return "", err
		}
	case "module_control":
		moduleLeaf = "controls.json"
	case "projection_intent":
		moduleLeaf = "projection/intent.json"
	case "projection_delivery":
		moduleLeaf = "projection/delivery.json"
	case "projection_binding":
		moduleLeaf = "projection/binding.json"
	case "export":
		if moduleID != "" {
			moduleLeaf = path.Join("outboxes/selections", filename)
		} else {
			return path.Join("state/global/outboxes/catalog", filename), nil
		}
	case "operational_command", "operational_wakeup":
		leaf := path.Join("operations/commands", filename)
		if kind == "operational_wakeup" {
			leaf = path.Join("outboxes/wakeups", filename)
		}
		if moduleID == "" {
			return path.Join("state/global", leaf), nil
		}
		moduleLeaf = leaf
	case "publication":
		if logicalPath == "state/publications/deployments.json" && moduleID == "" {
			return "state/global/publications/deployments.json", nil
		}
		moduleLeaf = path.Join("publications/evidence", filename)
	case "discovery_catalog":
		return "state/global/catalog/current.json", nil
	case "global_control":
		return "state/global/controls.json", nil
	case "discovery_observation":
		return path.Join("state/global/catalog/observations", filename), nil
	case "discovery_cycle":
		return path.Join("state/global/discovery/cycles", filename), nil
	case "input_import":
		return path.Join("state/global/inputs/imports", filename), nil
	case "projection_batch":
		return path.Join("state/global/projection/batches", filename), nil
	default:
		return "", fmt.Errorf("record kind %q has no approved operational owner", kind)
	}
	root, err := OperationalModuleRoot(moduleID)
	if err != nil {
		return "", err
	}
	return path.Join(root, moduleLeaf), nil
}

func operationalSelectionLeaf(logicalPath, moduleID string) (string, error) {
	parts := strings.Split(logicalPath, "/")
	if len(parts) < 5 || parts[2] != moduleID {
		return "", fmt.Errorf("selection logical owner is invalid")
	}
	return path.Join(append([]string{"selections"}, parts[3:]...)...), nil
}
