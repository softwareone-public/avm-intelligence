// Package identity implements versioned canonical JSON identities and path
// validation for immutable factory records.
package identity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	pathpkg "path"
	"regexp"
	"sort"
	"strings"
)

const (
	// CanonicalEncodingVersion identifies the canonical JSON rules used in IDs.
	CanonicalEncodingVersion = 1
	CanonicalEncodingName    = "avm-canonical-json"
)

var (
	recordKindPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	sha1Pattern       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type canonicalEnvelope struct {
	Encoding        string          `json:"encoding"`
	EncodingVersion int             `json:"encoding_version"`
	RecordKind      string          `json:"record_kind"`
	SchemaVersion   int             `json:"schema_version"`
	Value           json.RawMessage `json:"value"`
}

// CanonicalJSON serializes a record with deterministic object-key ordering and
// domain-separates it by encoding version, record kind, and schema version.
// Arrays retain their order; callers must use SortedSet for set-like arrays.
// Struct fields should not use omitempty where absence differs from null/value.
func CanonicalJSON(recordKind string, schemaVersion int, value any) ([]byte, error) {
	if !recordKindPattern.MatchString(recordKind) {
		return nil, fmt.Errorf("invalid record kind %q", recordKind)
	}
	if schemaVersion < 1 {
		return nil, fmt.Errorf("schema version must be positive")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical record: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err = decoder.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("decode canonical record: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("canonical record must contain one JSON value")
	}
	canonicalValue, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode canonical value: %w", err)
	}
	envelope := canonicalEnvelope{
		Encoding:        CanonicalEncodingName,
		EncodingVersion: CanonicalEncodingVersion,
		RecordKind:      recordKind,
		SchemaVersion:   schemaVersion,
		Value:           canonicalValue,
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode canonical envelope: %w", err)
	}
	return canonical, nil
}

// ID returns a content-addressed record ID in the form
// `<record-kind>-<sha256>`. The encoding and schema versions are part of the
// hashed envelope, so incompatible contracts cannot share an identity.
func ID(recordKind string, schemaVersion int, value any) (string, error) {
	canonical, err := CanonicalJSON(recordKind, schemaVersion, value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return recordKind + "-" + hex.EncodeToString(digest[:]), nil
}

// SortedSet returns a copy of values in canonical order and rejects duplicates
// rather than silently changing the represented record.
func SortedSet(values []string) ([]string, error) {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	for index := 1; index < len(sorted); index++ {
		if sorted[index-1] == sorted[index] {
			return nil, fmt.Errorf("set contains duplicate value %q", sorted[index])
		}
	}
	return sorted, nil
}

// ValidateAppendOnlyIDs ensures an updated observation index preserves its
// existing order and appends only new, unique content-addressed IDs.
func ValidateAppendOnlyIDs(previous, next []string) error {
	if len(next) < len(previous) {
		return fmt.Errorf("append-only history cannot shrink")
	}
	seen := make(map[string]struct{}, len(next))
	for index, id := range next {
		separator := strings.LastIndex(id, "-")
		if separator < 1 || !recordKindPattern.MatchString(id[:separator]) || !sha256Pattern.MatchString(id[separator+1:]) {
			return fmt.Errorf("invalid append-only record ID %q", id)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("append-only history duplicates record ID %s", id)
		}
		seen[id] = struct{}{}
		if index < len(previous) && id != previous[index] {
			return fmt.Errorf("append-only history rewrites record at index %d", index)
		}
	}
	return nil
}

// ValidateRelativePath rejects non-canonical, absolute, platform-specific, and
// parent-traversing paths. A module root path of "." is optional by contract.
func ValidateRelativePath(value string, allowRoot bool) error {
	if value == "" || strings.ContainsAny(value, "\\\x00") {
		return fmt.Errorf("relative path is empty or contains an invalid character")
	}
	if strings.HasPrefix(value, "/") || pathpkg.IsAbs(value) {
		return fmt.Errorf("absolute path is not allowed")
	}
	firstSegment := strings.SplitN(value, "/", 2)[0]
	if strings.Contains(firstSegment, ":") {
		return fmt.Errorf("drive-qualified or URI path is not allowed")
	}
	if value == "." {
		if allowRoot {
			return nil
		}
		return fmt.Errorf("root path is not allowed here")
	}
	if value == ".." || strings.HasPrefix(value, "../") || strings.Contains(value, "/../") || strings.HasSuffix(value, "/..") {
		return fmt.Errorf("parent traversal is not allowed")
	}
	if pathpkg.Clean(value) != value {
		return fmt.Errorf("path is not normalized")
	}
	return nil
}
