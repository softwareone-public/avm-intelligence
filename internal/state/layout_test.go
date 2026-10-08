package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

func moduleLayoutEvent(moduleID, eventID string, fence uint64) Event {
	return Event{ID: eventID, FenceKey: moduleID, FenceToken: fence,
		RecordPath: "state/requests/" + moduleID + "-request.json",
		Record:     Record{Kind: "request", SchemaVersion: 1, Data: json.RawMessage(`{"module_id":"` + moduleID + `","status":"queued"}`)}}
}

func TestModuleLayoutMigrationPreservesHistoryAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	backend := newMemoryBackend(NewSnapshot())
	event := moduleLayoutEvent("avm-res-demo", "before-migration", 7)
	if _, _, err := ApplyEvent(ctx, backend, event); err != nil {
		t.Fatal(err)
	}
	_, before, _ := backend.Read(ctx)
	if _, err := MigrateModuleLayout(ctx, backend); err != nil {
		t.Fatal(err)
	}
	head, after, _ := backend.Read(ctx)
	if !reflect.DeepEqual(before.Records, after.Records) || !reflect.DeepEqual(before.AppliedEvents, after.AppliedEvents) || !reflect.DeepEqual(before.FencingTokens, after.FencingTokens) || before.Generation != after.Generation {
		t.Fatal("migration changed durable logical history")
	}
	files, err := encodeSnapshotFiles(after)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := identity.OperationalModuleRoot("avm-res-demo")
	if _, exists := files[path.Join(root, "requests/avm-res-demo-request.json")]; !exists {
		t.Fatal("request was not relocated under its module")
	}
	if _, exists := files[event.RecordPath]; exists {
		t.Fatal("old physical path survived")
	}
	if retry, err := MigrateModuleLayout(ctx, backend); err != nil || retry != head {
		t.Fatalf("migration retry = %s, %v", retry, err)
	}
	if retry, _, err := ApplyEvent(ctx, backend, event); err != nil || retry != head {
		t.Fatalf("pre-migration replay duplicated work: %s, %v", retry, err)
	}
	stale := moduleLayoutEvent("avm-res-demo", "stale-after-migration", 6)
	if _, _, err := ApplyEvent(ctx, backend, stale); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("migration lost fence: %v", err)
	}
	newEvent := moduleLayoutEvent("avm-res-demo", "after-migration", 8)
	if _, updated, err := ApplyEvent(ctx, backend, newEvent); err != nil {
		t.Fatal(err)
	} else {
		if updated.Aggregates[root].Generation != 1 || updated.Aggregates[root].FencingTokens["avm-res-demo"] != 8 {
			t.Fatal("new event did not advance owner transaction")
		}
		assertModuleLayoutRoundtrip(t, updated)
	}
}

func assertModuleLayoutRoundtrip(t *testing.T, snapshot Snapshot) {
	t.Helper()
	first, err := encodeSnapshotFiles(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSnapshotFiles(first)
	if err != nil || !reflect.DeepEqual(decoded, snapshot) {
		t.Fatalf("roundtrip mismatch: %v", err)
	}
	second, err := encodeSnapshotFiles(decoded)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("second rebuild changed bytes")
	}
}

func TestModuleLayoutRejectsMixedOwnersMissingMetadataAndUnknownContract(t *testing.T) {
	backend := newMemoryBackend(NewSnapshot())
	event := moduleLayoutEvent("avm-res-demo", "seed", 1)
	_, _, _ = ApplyEvent(context.Background(), backend, event)
	if _, err := MigrateModuleLayout(context.Background(), backend); err != nil {
		t.Fatal(err)
	}
	_, snapshot, _ := backend.Read(context.Background())
	for _, mutation := range []string{"legacy-path", "missing-record", "unknown-contract", "cross-owner", "missing-transaction"} {
		t.Run(mutation, func(t *testing.T) {
			files, _ := encodeSnapshotFiles(snapshot)
			root, _ := identity.OperationalModuleRoot("avm-res-demo")
			physical := path.Join(root, "requests/avm-res-demo-request.json")
			switch mutation {
			case "legacy-path":
				files[event.RecordPath] = files[physical]
			case "missing-record":
				delete(files, physical)
			case "unknown-contract":
				files[layoutMarkerPath] = bytes.ReplaceAll(files[layoutMarkerPath], []byte("module-v2"), []byte("module-v9"))
			case "cross-owner":
				files[physical] = bytes.ReplaceAll(files[physical], []byte("avm-res-demo"), []byte("avm-ptn-other"))
			case "missing-transaction":
				delete(files, path.Join(root, "transaction.json"))
			}
			if _, err := decodeSnapshotFiles(files); err == nil {
				t.Fatal("invalid layout was accepted")
			}
		})
	}
}

func TestModuleLayoutMigrationRejectsUndefinedOwnersBeforeCommit(t *testing.T) {
	snapshot := NewSnapshot()
	snapshot.Records["state/health/collector.json"] = Record{Kind: "health", SchemaVersion: 1, Data: json.RawMessage(`{"status":"unknown"}`)}
	backend := newMemoryBackend(snapshot)
	if _, err := MigrateModuleLayout(context.Background(), backend); err == nil || backend.commitCount != 0 {
		t.Fatal("undefined owner migration did not stop before commit")
	}
}

func TestModuleLayoutMigrationLostAcknowledgmentDoesNotRepeat(t *testing.T) {
	backend := newMemoryBackend(NewSnapshot())
	backend.failAfterCommit = true
	if _, err := MigrateModuleLayout(context.Background(), backend); !errors.Is(err, errAcknowledgmentLost) {
		t.Fatalf("expected lost acknowledgement: %v", err)
	}
	if _, err := MigrateModuleLayout(context.Background(), backend); err != nil || backend.commitCount != 1 {
		t.Fatalf("migration repeated committed conversion: %v", err)
	}
}

func TestModuleLayoutPreservesNestedSelectionContexts(t *testing.T) {
	snapshot := NewSnapshot()
	contexts := []string{"azurerm/4", "azurerm/5", "azapi/2", "registry.terraform.io/hashicorp/azurerm/4"}
	for _, contextID := range contexts {
		logical := "state/selections/avm-res-demo/" + contextID + "/current.json"
		data, err := json.Marshal(map[string]any{"module_id": "avm-res-demo", "context_id": contextID, "sequence": 7})
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Records[logical] = Record{Kind: "selection", SchemaVersion: 1, Data: data}
	}
	backend := newMemoryBackend(snapshot)
	if _, err := MigrateModuleLayout(context.Background(), backend); err != nil {
		t.Fatal(err)
	}
	_, migrated, _ := backend.Read(context.Background())
	files, err := encodeSnapshotFiles(migrated)
	if err != nil {
		t.Fatal(err)
	}
	for _, contextID := range contexts {
		if _, exists := files["state/modules/res/avm-res-demo/selections/"+contextID+"/current.json"]; !exists {
			t.Fatal("provider/major context identity was truncated")
		}
	}
	assertModuleLayoutRoundtrip(t, migrated)
	logical := "state/selections/avm-res-demo/azurerm/4/current.json"
	conflicting := snapshot.Records[logical]
	conflicting.Data = json.RawMessage(`{"module_id":"avm-res-demo","context_id":"azapi/2","sequence":7}`)
	if _, _, err := resolveRecordOwner(logical, conflicting); err == nil {
		t.Fatal("selection payload context disagreed with its path")
	}
}

func TestModuleLayoutInverseMigrationPreservesNewWork(t *testing.T) {
	backend := newMemoryBackend(NewSnapshot())
	ctx := context.Background()
	_, _, _ = ApplyEvent(ctx, backend, moduleLayoutEvent("avm-res-demo", "old-work", 3))
	if _, err := MigrateModuleLayout(ctx, backend); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplyEvent(ctx, backend, moduleLayoutEvent("avm-ptn-new", "new-work", 9)); err != nil {
		t.Fatal(err)
	}
	head, before, _ := backend.Read(ctx)
	if _, err := RestoreLegacyLayoutAtHead(ctx, backend, "wrong-head"); !errors.Is(err, ErrHeadMoved) {
		t.Fatal("inverse migration accepted a stale head")
	}
	if _, err := RestoreLegacyLayoutAtHead(ctx, backend, head); err != nil {
		t.Fatal(err)
	}
	_, after, _ := backend.Read(ctx)
	if !reflect.DeepEqual(before.Records, after.Records) || !reflect.DeepEqual(before.AppliedEvents, after.AppliedEvents) || !reflect.DeepEqual(before.FencingTokens, after.FencingTokens) || before.Generation != after.Generation {
		t.Fatal("inverse migration discarded post-cutover work")
	}
	if _, err := MigrateModuleLayout(ctx, backend); err != nil {
		t.Fatal(err)
	}
}

func TestCheckoutCacheRejectsCorruptionSymlinksAndUnsafeEntries(t *testing.T) {
	for _, mutation := range []string{"valid", "corrupt", "symlink", "traversal", "executable", "truncated", "duplicate"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "state"), 0700); err != nil {
				t.Fatal(err)
			}
			content := []byte("{}\n")
			filename := filepath.Join(root, "state", "transaction.json")
			if err := os.WriteFile(filename, content, 0600); err != nil {
				t.Fatal(err)
			}
			objectID := gitBlobID(content)
			entry := fmt.Sprintf("100644 blob %s\tstate/transaction.json\x00", objectID)
			switch mutation {
			case "corrupt":
				_ = os.WriteFile(filename, []byte("changed"), 0600)
			case "symlink":
				_ = os.Remove(filename)
				_ = os.Symlink("other.json", filename)
			case "traversal":
				entry = fmt.Sprintf("100644 blob %s\tstate/../transaction.json\x00", objectID)
			case "executable":
				entry = fmt.Sprintf("100755 blob %s\tstate/transaction.json\x00", objectID)
			case "truncated":
				entry = entry[:len(entry)-1]
			case "duplicate":
				entry += entry
			}
			cache := &BlobReadCache{}
			err := cache.SeedCheckout(root, []byte(entry))
			_, cached := cache.load(objectID)
			if mutation == "valid" && (err != nil || !cached) {
				t.Fatalf("valid immutable checkout rejected: %v", err)
			}
			if mutation != "valid" && (err == nil || cached) {
				t.Fatal("invalid checkout populated cache")
			}
		})
	}
}

func TestModuleLayoutExactHeadFixture(t *testing.T) {
	root := os.Getenv("FACTORY_LAYOUT_AUDIT_ROOT")
	if root == "" {
		t.Skip("optional read-only exact-head state fixture not supplied")
	}
	files := map[string][]byte{}
	err := filepath.WalkDir(filepath.Join(root, "state"), func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("fixture contains non-regular entry")
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(filename)
		files[filepath.ToSlash(relative)] = content
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := decodeSnapshotFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	backend := newMemoryBackend(snapshot)
	if _, err := MigrateModuleLayout(context.Background(), backend); err != nil {
		t.Fatal(err)
	}
	_, migrated, _ := backend.Read(context.Background())
	assertModuleLayoutRoundtrip(t, migrated)
	encoded, err := encodeSnapshotFiles(migrated)
	if err != nil {
		t.Fatal(err)
	}
	for logicalPath, record := range snapshot.Records {
		_, physical, err := resolveRecordOwner(logicalPath, record)
		if err != nil || !bytes.Equal(files[logicalPath], encoded[physical]) {
			t.Fatalf("migration changed exact record bytes: %s, %v", logicalPath, err)
		}
	}
	if !reflect.DeepEqual(snapshot.AppliedEvents, migrated.AppliedEvents) || !reflect.DeepEqual(snapshot.FencingTokens, migrated.FencingTokens) || snapshot.Generation != migrated.Generation {
		t.Fatal("exact-head replay/fence inventory changed")
	}
	t.Logf("verified %d records, %d modules, %d replay digests, %d fence keys", len(snapshot.Records), len(migrated.Aggregates), len(snapshot.AppliedEvents), len(snapshot.FencingTokens))
}

func TestAggregateDeltaPreservesUnrelatedModuleAndRejectsRelevantChanges(t *testing.T) {
	for _, intervening := range []string{"other-module", "same-module", "global", "shared-pin"} {
		t.Run(intervening, func(t *testing.T) {
			ctx := context.Background()
			backend := newMemoryBackend(NewSnapshot())
			_, _, _ = ApplyEvent(ctx, backend, moduleLayoutEvent("avm-res-demo", "seed-demo", 1))
			_, _, _ = ApplyEvent(ctx, backend, moduleLayoutEvent("avm-ptn-other", "seed-other", 1))
			pin := Event{ID: "seed-pin", RecordPath: "state/discovery/source-pins/avm-ptn-other.json", Record: Record{Kind: "source_pins", SchemaVersion: 1, Data: json.RawMessage(`{"module_id":"avm-ptn-other","generation":1}`)}}
			_, _, _ = ApplyEvent(ctx, backend, pin)
			if _, err := MigrateModuleLayout(ctx, backend); err != nil {
				t.Fatal(err)
			}
			head, base, _ := backend.Read(ctx)
			owner, _ := identity.OperationalModuleRoot("avm-res-demo")
			delta := AggregateDelta{ExpectedHead: head, Base: base, ReadOwners: []string{owner}, ReadPaths: []string{pin.RecordPath}, Events: []Event{moduleLayoutEvent("avm-res-demo", "delta-demo", 2)}}
			change := moduleLayoutEvent("avm-ptn-other", "intervening", 2)
			switch intervening {
			case "same-module":
				change = moduleLayoutEvent("avm-res-demo", "intervening", 2)
			case "global":
				change = Event{ID: "global-pause", RecordPath: "state/operations/global.json", Record: Record{Kind: "global_control", SchemaVersion: 1, Data: json.RawMessage(`{"enabled":false}`)}}
			case "shared-pin":
				change = pin
				change.ID = "updated-pin"
				change.Record.Data = json.RawMessage(`{"module_id":"avm-ptn-other","generation":2}`)
			}
			if _, _, err := ApplyEvent(ctx, backend, change); err != nil {
				t.Fatal(err)
			}
			_, latest, _ := backend.Read(ctx)
			if _, _, err := ApplyAggregateDelta(ctx, backend, delta); !errors.Is(err, ErrHeadMoved) {
				t.Fatalf("stale precomputed delta was not rejected after %s changed: %v", intervening, err)
			}
			if intervening == "other-module" {
				if _, exists := latest.Aggregates[owner].AppliedEvents[delta.Events[0].ID]; exists {
					t.Fatal("stale module delta event was recorded after unrelated head movement")
				}
			}
		})
	}
}

func TestAggregateDeltaReplaysIdenticalEventOnlyAtCurrentHead(t *testing.T) {
	ctx := context.Background()
	backend := newMemoryBackend(NewSnapshot())
	if _, _, err := ApplyEvent(ctx, backend, moduleLayoutEvent("avm-res-delta-replay", "seed-delta-replay", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateModuleLayout(ctx, backend); err != nil {
		t.Fatal(err)
	}
	head, snapshot, err := backend.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	event := moduleLayoutEvent("avm-res-delta-replay", "delta-replay-event", 2)
	delta := AggregateDelta{ExpectedHead: head, Base: snapshot, ReadOwners: []string{"state/modules/res/avm-res-delta-replay"}, Events: []Event{event}}
	committedHead, _, err := ApplyAggregateDelta(ctx, backend, delta)
	if err != nil {
		t.Fatal(err)
	}
	_, afterCommit, err := backend.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replay := AggregateDelta{ExpectedHead: committedHead, Base: afterCommit, ReadOwners: delta.ReadOwners, Events: []Event{event}}
	replayHead, _, err := ApplyAggregateDelta(ctx, backend, replay)
	if err != nil || replayHead != committedHead || backend.commitCount != 3 {
		t.Fatalf("identical aggregate replay head=%q err=%v commits=%d", replayHead, err, backend.commitCount)
	}
	changed := event
	changed.Record.Data = json.RawMessage(`{"module_id":"avm-res-delta-replay","status":"failed"}`)
	conflict := AggregateDelta{ExpectedHead: committedHead, Base: afterCommit, ReadOwners: delta.ReadOwners, Events: []Event{changed}}
	if _, _, err = ApplyAggregateDelta(ctx, backend, conflict); !errors.Is(err, ErrEventConflict) || backend.commitCount != 3 {
		t.Fatalf("changed aggregate replay err=%v commits=%d", err, backend.commitCount)
	}
}
