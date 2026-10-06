package state

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

// SeedCheckout verifies a trusted transport's NUL-delimited git ls-tree output
// and caches immutable blobs only. It never executes Git or trusts a local ref.
func (cache *BlobReadCache) SeedCheckout(root string, listing []byte) error {
	if cache == nil || len(listing) == 0 || listing[len(listing)-1] != 0 {
		return fmt.Errorf("state checkout cache and complete tree listing are required")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("state checkout root must be a real directory")
	}
	verified := map[string][]byte{}
	seen := map[string]bool{}
	for _, entry := range bytes.Split(listing[:len(listing)-1], []byte{0}) {
		parts := bytes.SplitN(entry, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid checkout tree entry")
		}
		fields := strings.Fields(string(parts[0]))
		filename := string(parts[1])
		if len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" || len(fields[2]) != 40 || seen[filename] {
			return fmt.Errorf("unsupported or duplicate checkout tree entry")
		}
		if err := identity.ValidateRelativePath(filename, false); err != nil || !strings.HasPrefix(filename, "state/") || filepath.Ext(filename) != ".json" {
			return fmt.Errorf("checkout contains an invalid state path")
		}
		seen[filename] = true
		current := root
		for _, segment := range strings.Split(filename, "/") {
			current = filepath.Join(current, segment)
			stat, statErr := os.Lstat(current)
			if statErr != nil || stat.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("checkout contains a missing or symbolic path")
			}
		}
		info, err := os.Lstat(current)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("checkout blob is not a regular file")
		}
		content, err := os.ReadFile(current)
		if err != nil {
			return fmt.Errorf("read checkout blob: %w", err)
		}
		if gitBlobID(content) != fields[2] {
			return fmt.Errorf("checkout blob does not match immutable object identity")
		}
		verified[fields[2]] = content
	}
	for objectID, content := range verified {
		cache.store(objectID, content)
	}
	return nil
}

func gitBlobID(content []byte) string {
	digest := sha1.New()
	_, _ = digest.Write([]byte("blob " + strconv.Itoa(len(content)) + "\x00"))
	_, _ = digest.Write(content)
	return fmt.Sprintf("%x", digest.Sum(nil))
}
