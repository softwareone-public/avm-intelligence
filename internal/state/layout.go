package state

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"reflect"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"gopkg.in/yaml.v3"
)

const layoutMarkerPath = "state/layout.json"
const globalTransactionPath = "state/global/transaction.json"

// AggregateTransaction retains owner-local replay history and fencing high-water marks.
type AggregateTransaction struct {
	Generation    uint64            `json:"generation"`
	AppliedEvents map[string]string `json:"applied_events"`
	FencingTokens map[string]uint64 `json:"fencing_tokens"`
}

type layoutMarker struct {
	SchemaVersion  int               `json:"schema_version"`
	Profile        string            `json:"profile"`
	ContractDigest string            `json:"contract_digest"`
	MigrationBase  string            `json:"migration_base"`
	LogicalPaths   map[string]string `json:"logical_paths"`
}

// LoadLayoutContract accepts only the compiled declarative contract, never arbitrary paths.
func LoadLayoutContract(filename string) (identity.LayoutContract, error) {
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() {
		return identity.LayoutContract{}, fmt.Errorf("layout contract must be a regular file")
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		return identity.LayoutContract{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var contract identity.LayoutContract
	if err = decoder.Decode(&contract); err != nil {
		return contract, fmt.Errorf("decode layout contract: %w", err)
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return contract, fmt.Errorf("layout contract must contain one document")
	}
	if _, err = contract.Digest(); err != nil {
		return contract, err
	}
	return contract, nil
}

// MigrateModuleLayout converts storage at the exact observed head without changing logical records.
// Every retained backend reader understands both profiles; discovery remains separate from dispatch.
func MigrateModuleLayout(ctx context.Context, backend Backend) (string, error) {
	head, snapshot, err := ReadSnapshot(ctx, backend)
	if err != nil {
		return "", err
	}
	if snapshot.LayoutVersion == identity.ModuleLayout {
		return head, nil
	}
	next := cloneSnapshot(snapshot)
	next.LayoutVersion = identity.ModuleLayout
	next.LayoutMigrationBase = head
	next.Aggregates = map[string]AggregateTransaction{}
	for logicalPath, record := range next.Records {
		owner, _, resolveErr := resolveRecordOwner(logicalPath, record)
		if resolveErr != nil {
			return "", resolveErr
		}
		if owner != "state/global" {
			next.Aggregates[owner] = emptyAggregate()
		}
	}
	for key, token := range next.FencingTokens {
		if owner, ownerErr := identity.OperationalModuleRoot(key); ownerErr == nil {
			aggregate, exists := next.Aggregates[owner]
			if !exists {
				return "", fmt.Errorf("module fence has no durable aggregate")
			}
			aggregate.FencingTokens[key] = token
			next.Aggregates[owner] = aggregate
		}
	}
	files, err := encodeSnapshotFiles(next)
	if err != nil {
		return "", err
	}
	roundtrip, err := decodeSnapshotFiles(files)
	if err != nil || !reflect.DeepEqual(next, roundtrip) {
		return "", fmt.Errorf("layout migration failed lossless round-trip verification")
	}
	newHead, err := CommitExpectedHead(ctx, backend, head, next)
	if err != nil {
		return "", err
	}
	progress.Logger(ctx).InfoContext(ctx, "state layout migrated", "layout", identity.ModuleLayout, "state_head", newHead, "modules", len(next.Aggregates), "records", len(next.Records))
	return newHead, nil
}

func emptyAggregate() AggregateTransaction {
	return AggregateTransaction{AppliedEvents: map[string]string{}, FencingTokens: map[string]uint64{}}
}

// RestoreLegacyLayoutAtHead is a forward inverse migration preserving all post-cutover writes.
// It requires the exact current head; it never restores an earlier snapshot or force-updates a ref.
func RestoreLegacyLayoutAtHead(ctx context.Context, backend Backend, expectedHead string) (string, error) {
	head, snapshot, err := ReadSnapshot(ctx, backend)
	if err != nil {
		return "", err
	}
	if expectedHead == "" || head != expectedHead {
		return "", ErrHeadMoved
	}
	if snapshot.LayoutVersion != identity.ModuleLayout {
		return head, nil
	}
	snapshot.LayoutVersion = ""
	snapshot.LayoutMigrationBase = ""
	snapshot.Aggregates = nil
	return CommitExpectedHead(ctx, backend, head, snapshot)
}

func resolveRecordOwner(logicalPath string, record Record) (string, string, error) {
	if err := validateRecord(logicalPath, record); err != nil {
		return "", "", err
	}
	var fields struct {
		ModuleID  string `json:"module_id"`
		ContextID string `json:"context_id"`
	}
	if err := json.Unmarshal(record.Data, &fields); err != nil {
		return "", "", err
	}
	if record.Kind == "selection" {
		if err := identity.ValidateRelativePath(fields.ContextID, false); err != nil {
			return "", "", fmt.Errorf("selection context is invalid")
		}
		expected := path.Join("state/selections", fields.ModuleID, fields.ContextID, path.Base(logicalPath))
		if expected != logicalPath {
			return "", "", fmt.Errorf("selection context and logical path disagree")
		}
	}
	physical, err := identity.OperationalRecordPath(logicalPath, record.Kind, fields.ModuleID)
	if err != nil {
		return "", "", err
	}
	owner := "state/global"
	if strings.HasPrefix(physical, "state/modules/") {
		owner, err = identity.OperationalModuleRoot(fields.ModuleID)
		if err != nil {
			return "", "", err
		}
		if record.Kind == "source_pins" || record.Kind == "module" || record.Kind == "module_control" || strings.HasPrefix(record.Kind, "projection_") {
			if path.Base(logicalPath) != fields.ModuleID+".json" {
				return "", "", fmt.Errorf("logical path and module owner disagree")
			}
		}
	} else if fields.ModuleID != "" {
		return "", "", fmt.Errorf("global record unexpectedly claims a module owner")
	}
	return owner, physical, nil
}

func encodeModuleSnapshotFiles(snapshot Snapshot) (map[string][]byte, error) {
	digest, err := identity.ModuleLayoutContract().Digest()
	if err != nil {
		return nil, err
	}
	marker := layoutMarker{SchemaVersion: 1, Profile: identity.ModuleLayout, ContractDigest: digest, MigrationBase: snapshot.LayoutMigrationBase, LogicalPaths: map[string]string{}}
	files := map[string][]byte{}
	for logicalPath, record := range snapshot.Records {
		owner, physical, resolveErr := resolveRecordOwner(logicalPath, record)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if owner != "state/global" {
			if _, exists := snapshot.Aggregates[owner]; !exists {
				return nil, fmt.Errorf("module record has no transaction metadata")
			}
		}
		if _, duplicate := files[physical]; duplicate {
			return nil, fmt.Errorf("duplicate physical record owner")
		}
		content, encodeErr := json.Marshal(record)
		if encodeErr != nil {
			return nil, encodeErr
		}
		files[physical] = append(content, '\n')
		marker.LogicalPaths[physical] = logicalPath
	}
	global := snapshotMetadata{SchemaVersion: snapshot.SchemaVersion, Generation: snapshot.Generation, AppliedEvents: map[string]string{}, FencingTokens: map[string]uint64{}}
	for eventID, eventDigest := range snapshot.AppliedEvents {
		global.AppliedEvents[eventID] = eventDigest
	}
	for key, token := range snapshot.FencingTokens {
		global.FencingTokens[key] = token
	}
	for owner, aggregate := range snapshot.Aggregates {
		for eventID := range aggregate.AppliedEvents {
			delete(global.AppliedEvents, eventID)
		}
		for key := range aggregate.FencingTokens {
			delete(global.FencingTokens, key)
		}
		content, encodeErr := json.Marshal(aggregate)
		if encodeErr != nil {
			return nil, encodeErr
		}
		files[path.Join(owner, "transaction.json")] = append(content, '\n')
	}
	metadata, err := json.Marshal(global)
	if err != nil {
		return nil, err
	}
	files[globalTransactionPath] = append(metadata, '\n')
	content, err := json.Marshal(marker)
	if err != nil {
		return nil, err
	}
	files[layoutMarkerPath] = append(content, '\n')
	return files, nil
}

func decodeModuleSnapshotFiles(files map[string][]byte) (Snapshot, error) {
	var marker layoutMarker
	if err := decodeOneJSON(files[layoutMarkerPath], &marker); err != nil {
		return Snapshot{}, err
	}
	digest, _ := identity.ModuleLayoutContract().Digest()
	if marker.SchemaVersion != 1 || marker.Profile != identity.ModuleLayout || marker.ContractDigest != digest || marker.MigrationBase == "" || marker.LogicalPaths == nil {
		return Snapshot{}, fmt.Errorf("unsupported or incomplete persisted layout")
	}
	legacyFiles := map[string][]byte{snapshotPath: files[globalTransactionPath]}
	aggregates := map[string]AggregateTransaction{}
	for physical, content := range files {
		if physical == layoutMarkerPath || physical == globalTransactionPath {
			continue
		}
		if strings.HasPrefix(physical, "state/modules/") && path.Base(physical) == "transaction.json" {
			owner := path.Dir(physical)
			var aggregate AggregateTransaction
			if err := decodeOneJSON(content, &aggregate); err != nil {
				return Snapshot{}, err
			}
			aggregates[owner] = aggregate
			continue
		}
		logical, exists := marker.LogicalPaths[physical]
		if !exists {
			return Snapshot{}, fmt.Errorf("unmapped or mixed-layout state record")
		}
		var record Record
		if err := decodeOneJSON(content, &record); err != nil {
			return Snapshot{}, err
		}
		_, expected, err := resolveRecordOwner(logical, record)
		if err != nil || physical != expected {
			return Snapshot{}, fmt.Errorf("physical record owner or kind is invalid")
		}
		if _, duplicate := legacyFiles[logical]; duplicate {
			return Snapshot{}, fmt.Errorf("duplicate logical state owner")
		}
		legacyFiles[logical] = content
	}
	if len(legacyFiles)-1 != len(marker.LogicalPaths) {
		return Snapshot{}, fmt.Errorf("layout manifest references missing records")
	}
	snapshot, err := decodeLegacySnapshotFiles(legacyFiles)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.LayoutVersion = identity.ModuleLayout
	snapshot.LayoutMigrationBase = marker.MigrationBase
	snapshot.Aggregates = aggregates
	for _, aggregate := range aggregates {
		for eventID, eventDigest := range aggregate.AppliedEvents {
			if _, duplicate := snapshot.AppliedEvents[eventID]; duplicate {
				return Snapshot{}, fmt.Errorf("duplicate replay ownership")
			}
			snapshot.AppliedEvents[eventID] = eventDigest
		}
		for key, token := range aggregate.FencingTokens {
			if _, duplicate := snapshot.FencingTokens[key]; duplicate {
				return Snapshot{}, fmt.Errorf("duplicate fence ownership")
			}
			snapshot.FencingTokens[key] = token
		}
	}
	return snapshot, snapshot.Validate()
}

func validateAggregates(snapshot Snapshot) error {
	if snapshot.LayoutVersion == "" || snapshot.LayoutVersion == identity.LegacyLayout {
		if len(snapshot.Aggregates) != 0 || snapshot.LayoutMigrationBase != "" {
			return fmt.Errorf("legacy snapshot has module layout metadata")
		}
		return nil
	}
	if snapshot.LayoutVersion != identity.ModuleLayout || snapshot.LayoutMigrationBase == "" || snapshot.Aggregates == nil {
		return fmt.Errorf("unsupported snapshot layout")
	}
	seenEvents, seenFences := map[string]bool{}, map[string]bool{}
	for owner, aggregate := range snapshot.Aggregates {
		expected, err := identity.OperationalModuleRoot(path.Base(owner))
		if err != nil || expected != owner || aggregate.AppliedEvents == nil || aggregate.FencingTokens == nil {
			return fmt.Errorf("invalid aggregate metadata owner")
		}
		if err := validateAppliedEvents(aggregate.AppliedEvents); err != nil {
			return err
		}
		if err := validateFencingTokens(aggregate.FencingTokens); err != nil {
			return err
		}
		for eventID, digest := range aggregate.AppliedEvents {
			if seenEvents[eventID] || snapshot.AppliedEvents[eventID] != digest {
				return fmt.Errorf("aggregate replay history is inconsistent")
			}
			seenEvents[eventID] = true
		}
		for key, token := range aggregate.FencingTokens {
			if key != path.Base(owner) || seenFences[key] || snapshot.FencingTokens[key] != token {
				return fmt.Errorf("aggregate fence history is inconsistent")
			}
			seenFences[key] = true
		}
	}
	for logicalPath, record := range snapshot.Records {
		owner, _, err := resolveRecordOwner(logicalPath, record)
		if err != nil {
			return err
		}
		if owner != "state/global" {
			if _, exists := snapshot.Aggregates[owner]; !exists {
				return fmt.Errorf("module record has no aggregate transaction")
			}
		}
	}
	return nil
}

func recordAggregateEvent(snapshot *Snapshot, event Event, digest string) error {
	if snapshot.LayoutVersion != identity.ModuleLayout {
		return nil
	}
	owner, _, err := resolveRecordOwner(event.RecordPath, event.Record)
	if err != nil || owner == "state/global" {
		return err
	}
	aggregate, exists := snapshot.Aggregates[owner]
	if !exists {
		aggregate = emptyAggregate()
	}
	if aggregate.Generation == math.MaxUint64 {
		return fmt.Errorf("aggregate generation exhausted")
	}
	if event.FenceKey != "" && event.FenceKey != path.Base(owner) {
		return fmt.Errorf("event fence belongs to another aggregate")
	}
	aggregate.Generation++
	aggregate.AppliedEvents[event.ID] = digest
	if event.FenceKey != "" {
		aggregate.FencingTokens[event.FenceKey] = snapshot.FencingTokens[event.FenceKey]
	}
	snapshot.Aggregates[owner] = aggregate
	return nil
}
