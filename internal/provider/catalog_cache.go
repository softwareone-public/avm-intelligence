package provider

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// CatalogCacheEntry is a verified response body plus the validators and headers
// required to reuse it when a source returns HTTP 304.
type CatalogCacheEntry struct {
	ETag         string
	LastModified string
	Digest       string
	Body         []byte
	Headers      http.Header
}

// CatalogCache stores response bodies by exact request URL and representation.
type CatalogCache interface {
	Load(context.Context, string) (CatalogCacheEntry, bool, error)
	Store(context.Context, string, CatalogCacheEntry) error
}

// MemoryCatalogCache is a process-local cache suitable for tests and callers
// that provide a separate persistence layer around collection runs.
type MemoryCatalogCache struct {
	mu      sync.RWMutex
	entries map[string]CatalogCacheEntry
}

// FileCatalogCache persists verified public-source responses outside accepted
// data so later collection runs can revalidate them conditionally.
type FileCatalogCache struct {
	Root string
	mu   sync.Mutex
}

func (cache *FileCatalogCache) Load(ctx context.Context, key string) (CatalogCacheEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		return CatalogCacheEntry{}, false, err
	}
	if err := validateCatalogCacheKey(key); err != nil {
		return CatalogCacheEntry{}, false, err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if err := validateCatalogCacheDirectory(cache.Root); err != nil {
		return CatalogCacheEntry{}, false, err
	}
	cachePath := filepath.Join(cache.Root, key+".json")
	info, err := os.Lstat(cachePath)
	if os.IsNotExist(err) {
		return CatalogCacheEntry{}, false, nil
	}
	if err != nil {
		return CatalogCacheEntry{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return CatalogCacheEntry{}, false, fmt.Errorf("provider cache entry must be a regular non-symlink file")
	}
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return CatalogCacheEntry{}, false, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var entry CatalogCacheEntry
	if err = decoder.Decode(&entry); err != nil {
		return CatalogCacheEntry{}, false, fmt.Errorf("decode provider cache entry: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return CatalogCacheEntry{}, false, fmt.Errorf("provider cache entry must contain one JSON value")
	}
	if err = validateCatalogCacheEntry(entry); err != nil {
		return CatalogCacheEntry{}, false, fmt.Errorf("provider cache entry is invalid: %w", err)
	}
	return cloneCatalogCacheEntry(entry), true, nil
}

func (cache *FileCatalogCache) Store(ctx context.Context, key string, entry CatalogCacheEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCatalogCacheKey(key); err != nil {
		return err
	}
	if err := validateCatalogCacheEntry(entry); err != nil {
		return err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if err := validateCatalogCacheDirectory(cache.Root); err != nil {
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(cache.Root, ".provider-cache-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(cache.Root, key+".json"))
}

func validateCatalogCacheKey(key string) error {
	if len(key) != 64 || strings.ToLower(key) != key {
		return fmt.Errorf("provider cache key must be a lowercase SHA-256 digest")
	}
	if _, err := hex.DecodeString(key); err != nil {
		return fmt.Errorf("provider cache key is invalid: %w", err)
	}
	return nil
}

func validateCatalogCacheDirectory(root string) error {
	if root == "" {
		return fmt.Errorf("provider cache directory is required")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("provider cache root must be a real directory")
	}
	return nil
}

func (cache *MemoryCatalogCache) Load(ctx context.Context, key string) (CatalogCacheEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		return CatalogCacheEntry{}, false, err
	}
	cache.mu.RLock()
	entry, found := cache.entries[key]
	cache.mu.RUnlock()
	if !found {
		return CatalogCacheEntry{}, false, nil
	}
	if err := validateCatalogCacheEntry(entry); err != nil {
		return CatalogCacheEntry{}, false, fmt.Errorf("cached provider response is invalid: %w", err)
	}
	return cloneCatalogCacheEntry(entry), true, nil
}

func (cache *MemoryCatalogCache) Store(ctx context.Context, key string, entry CatalogCacheEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("provider cache key is required")
	}
	if err := validateCatalogCacheEntry(entry); err != nil {
		return err
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = make(map[string]CatalogCacheEntry)
	}
	cache.entries[key] = cloneCatalogCacheEntry(entry)
	return nil
}

func validateCatalogCacheEntry(entry CatalogCacheEntry) error {
	if entry.ETag == "" && entry.LastModified == "" {
		return fmt.Errorf("cached provider response has no HTTP validator")
	}
	if entry.Digest == "" || entry.Digest != catalogDigest(entry.Body) {
		return fmt.Errorf("cached provider response digest does not match its body")
	}
	return nil
}

func cloneCatalogCacheEntry(entry CatalogCacheEntry) CatalogCacheEntry {
	entry.Body = append([]byte(nil), entry.Body...)
	entry.Headers = entry.Headers.Clone()
	return entry
}
