package state

import (
	"fmt"
	"sort"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

type gitStateTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type gitStateTree struct {
	Truncated bool                `json:"truncated"`
	Tree      []gitStateTreeEntry `json:"tree"`
}

const stateTreePrefix = "state/"

// stateTreeDelta compares a complete validated snapshot against its immutable parent tree.
// Every removed file receives an explicit null-SHA deletion; unchanged paths are not resent.
func stateTreeDelta(files map[string][]byte, parent gitStateTree, cache *BlobReadCache) ([]map[string]any, int, error) {
	previous, objects, err := validatedStateTreeObjects(parent)
	if err != nil {
		return nil, 0, err
	}
	changes, err := changedStateTreeEntries(files, previous, objects, cache)
	if err != nil {
		return nil, 0, err
	}
	deleted := 0
	for filename := range previous {
		if _, retained := files[filename]; !retained {
			changes[filename] = map[string]any{"path": filename, "mode": "100644", "type": "blob", "sha": nil}
			deleted++
		}
	}
	paths := make([]string, 0, len(changes))
	for filename := range changes {
		paths = append(paths, filename)
	}
	sort.Strings(paths)
	entries := make([]map[string]any, 0, len(paths))
	for _, filename := range paths {
		entries = append(entries, changes[filename])
	}
	return entries, deleted, nil
}

func validatedStateTreeObjects(parent gitStateTree) (map[string]string, map[string]bool, error) {
	if parent.Truncated {
		return nil, nil, fmt.Errorf("expected state tree response was truncated")
	}
	if parent.Tree == nil {
		return nil, nil, fmt.Errorf("expected state tree manifest is missing")
	}
	previous := map[string]string{}
	objects := map[string]bool{}
	for _, entry := range parent.Tree {
		if err := identity.ValidateRelativePath(entry.Path, false); err != nil {
			return nil, nil, fmt.Errorf("expected state tree has an invalid path")
		}
		if entry.Type == "tree" && entry.Mode == "040000" && entry.SHA != "" && (entry.Path == "state" || strings.HasPrefix(entry.Path, stateTreePrefix)) {
			continue
		}
		if entry.Type != "blob" || entry.Mode != "100644" || entry.SHA == "" || !strings.HasPrefix(entry.Path, stateTreePrefix) || !strings.HasSuffix(entry.Path, ".json") {
			return nil, nil, fmt.Errorf("expected state tree has an unsupported entry")
		}
		if _, duplicate := previous[entry.Path]; duplicate {
			return nil, nil, fmt.Errorf("expected state tree has duplicate file paths")
		}
		previous[entry.Path] = entry.SHA
		objects[entry.SHA] = true
	}
	return previous, objects, nil
}

func changedStateTreeEntries(files map[string][]byte, previous map[string]string, objects map[string]bool, cache *BlobReadCache) (map[string]map[string]any, error) {
	changes := map[string]map[string]any{}
	for filename, content := range files {
		if err := identity.ValidateRelativePath(filename, false); err != nil || !strings.HasPrefix(filename, stateTreePrefix) || !strings.HasSuffix(filename, ".json") {
			return nil, fmt.Errorf("state delta has an invalid output path")
		}
		objectID := gitBlobID(content)
		if previous[filename] == objectID {
			continue
		}
		entry := map[string]any{"path": filename, "mode": "100644", "type": "blob"}
		if _, cached := cache.load(objectID); cached || objects[objectID] {
			entry["sha"] = objectID
		} else {
			entry["content"] = string(content)
		}
		changes[filename] = entry
	}
	return changes, nil
}
