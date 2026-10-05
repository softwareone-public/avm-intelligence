package terraform

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

// DiscoverUnits deterministically maps root, modules/, and examples/ Terraform
// files to explicit extraction units. Unexpected Terraform directories fail
// closed rather than disappearing from facts coverage.
func DiscoverUnits(sourceRoot string) ([]UnitInput, error) {
	root, err := filepath.Abs(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve source root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve source root symlinks: %w", err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return nil, fmt.Errorf("source root must be a readable directory")
	}
	filesByUnit := map[string][]string{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		return collectUnitFile(root, path, entry, walkErr, filesByUnit)
	})
	if err != nil {
		return nil, fmt.Errorf("discover Terraform units: %w", err)
	}
	if len(filesByUnit) == 0 {
		return nil, fmt.Errorf("source tree has no Terraform files")
	}
	unitPaths := make([]string, 0, len(filesByUnit))
	for unitPath := range filesByUnit {
		unitPaths = append(unitPaths, unitPath)
	}
	sort.Strings(unitPaths)
	units := make([]UnitInput, 0, len(unitPaths))
	for _, unitPath := range unitPaths {
		role, owner, err := unitRole(unitPath)
		if err != nil {
			return nil, err
		}
		files := filesByUnit[unitPath]
		sort.Strings(files)
		units = append(units, UnitInput{Path: unitPath, Role: role, Owner: owner, Files: files})
	}
	return units, nil
}

// DiscoverLockFiles returns only lockfiles located at the root of discovered units.
func DiscoverLockFiles(sourceRoot string, units []UnitInput) ([]LockFileInput, error) {
	root, err := filepath.Abs(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve source root for lockfiles: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve source root symlinks for lockfiles: %w", err)
	}
	lockFiles := make([]LockFileInput, 0, len(units))
	for _, unit := range units {
		if err = validateUnitInput(unit); err != nil {
			return nil, err
		}
		relative := filepath.ToSlash(filepath.Join(filepath.FromSlash(unit.Path), ".terraform.lock.hcl"))
		absolute := filepath.Join(root, filepath.FromSlash(relative))
		info, statErr := os.Lstat(absolute)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return nil, fmt.Errorf("inspect provider lockfile %q: %w", relative, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("provider lockfile %q must be a regular non-symlink file", relative)
		}
		if err = rejectSymlinkComponents(root, absolute); err != nil {
			return nil, fmt.Errorf("unsafe provider lockfile %q: %w", relative, err)
		}
		lockFiles = append(lockFiles, LockFileInput{Path: relative, UnitPath: unit.Path})
	}
	sort.Slice(lockFiles, func(left, right int) bool { return lockFiles[left].Path < lockFiles[right].Path })
	return lockFiles, nil
}

func collectUnitFile(root, path string, entry fs.DirEntry, walkErr error, filesByUnit map[string][]string) error {
	if walkErr != nil {
		return walkErr
	}
	if path == root {
		return nil
	}
	if entry.Type()&os.ModeSymlink != 0 {
		return fmt.Errorf("source tree contains a symlink: %s", path)
	}
	if entry.IsDir() {
		if entry.Name() == ".git" || entry.Name() == ".terraform" || entry.Name() == "vendor" {
			return filepath.SkipDir
		}
		return nil
	}
	if !entry.Type().IsRegular() || (!strings.HasSuffix(entry.Name(), ".tf") && !strings.HasSuffix(entry.Name(), ".tf.json")) {
		return nil
	}
	relativePath, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	relativePath = filepath.ToSlash(relativePath)
	unitPath := filepath.ToSlash(filepath.Dir(filepath.FromSlash(relativePath)))
	if unitPath == "" {
		unitPath = "."
	}
	filesByUnit[unitPath] = append(filesByUnit[unitPath], filepath.Base(relativePath))
	return nil
}

func unitRole(unitPath string) (string, string, error) {
	if err := identity.ValidateRelativePath(unitPath, true); err != nil {
		return "", "", fmt.Errorf("unsafe discovered unit path %q: %w", unitPath, err)
	}
	if unitPath == "." {
		return RoleComponent, "", nil
	}
	segments := strings.Split(unitPath, "/")
	for index, segment := range segments {
		if segment == "examples" {
			owner := strings.Join(segments[:index], "/")
			if owner == "" {
				owner = "."
			}
			return RoleExample, owner, nil
		}
	}
	if len(segments) > 1 && segments[0] == "modules" {
		return RoleChildComponent, unitPath, nil
	}
	return "", "", fmt.Errorf("Terraform files in unsupported unit path %q", unitPath)
}
