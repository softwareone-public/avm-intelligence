package state

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
)

const snapshotPath = "state/transaction.json"

type snapshotMetadata struct {
	SchemaVersion int               `json:"schema_version"`
	Generation    uint64            `json:"generation"`
	AppliedEvents map[string]string `json:"applied_events"`
	FencingTokens map[string]uint64 `json:"fencing_tokens"`
}

// BlobReadCache reuses immutable object contents within one backend instance.
type BlobReadCache struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func (cache *BlobReadCache) load(objectID string) ([]byte, bool) {
	if cache == nil {
		return nil, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	content, exists := cache.blobs[objectID]
	return append([]byte(nil), content...), exists
}

func (cache *BlobReadCache) store(objectID string, content []byte) {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.blobs == nil {
		cache.blobs = make(map[string][]byte)
	}
	cache.blobs[objectID] = append([]byte(nil), content...)
}

func encodeSnapshotFiles(snapshot Snapshot) (map[string][]byte, error) {
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	if snapshot.LayoutVersion == "module-v2" {
		return encodeModuleSnapshotFiles(snapshot)
	}
	files := make(map[string][]byte, len(snapshot.Records)+1)
	metadata, err := json.Marshal(snapshotMetadata{
		SchemaVersion: snapshot.SchemaVersion,
		Generation:    snapshot.Generation,
		AppliedEvents: snapshot.AppliedEvents,
		FencingTokens: snapshot.FencingTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("encode state transaction metadata: %w", err)
	}
	files[snapshotPath] = append(metadata, '\n')
	for recordPath, record := range snapshot.Records {
		encoded, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode state record %q: %w", recordPath, marshalErr)
		}
		files[recordPath] = append(encoded, '\n')
	}
	return files, nil
}

func decodeSnapshotFiles(files map[string][]byte) (Snapshot, error) {
	if _, exists := files[layoutMarkerPath]; exists {
		return decodeModuleSnapshotFiles(files)
	}
	return decodeLegacySnapshotFiles(files)
}

func decodeLegacySnapshotFiles(files map[string][]byte) (Snapshot, error) {
	metadata, exists := files[snapshotPath]
	if !exists {
		if len(files) == 0 {
			return NewSnapshot(), nil
		}
		return Snapshot{}, fmt.Errorf("state transaction metadata is missing")
	}
	var metadataRecord snapshotMetadata
	if err := decodeOneJSON(metadata, &metadataRecord); err != nil {
		return Snapshot{}, fmt.Errorf("decode transaction metadata: %w", err)
	}
	snapshot := Snapshot{
		SchemaVersion: metadataRecord.SchemaVersion,
		Generation:    metadataRecord.Generation,
		AppliedEvents: metadataRecord.AppliedEvents,
		FencingTokens: metadataRecord.FencingTokens,
		Records:       map[string]Record{},
	}
	for recordPath, content := range files {
		if recordPath == snapshotPath {
			continue
		}
		var record Record
		if err := decodeOneJSON(content, &record); err != nil {
			return Snapshot{}, fmt.Errorf("decode record %q: %w", recordPath, err)
		}
		if _, duplicate := snapshot.Records[recordPath]; duplicate {
			return Snapshot{}, fmt.Errorf("record %q is duplicated in transaction metadata", recordPath)
		}
		snapshot.Records[recordPath] = record
	}
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func decodeOneJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("JSON must contain one document")
	}
	return nil
}

func gitBlobID(content []byte) string {
	digest := sha1.New()
	_, _ = digest.Write([]byte("blob " + strconv.Itoa(len(content)) + "\x00"))
	_, _ = digest.Write(content)
	return fmt.Sprintf("%x", digest.Sum(nil))
}
