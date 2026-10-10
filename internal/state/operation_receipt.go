package state

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

// OperationWriteSetEntry is the immutable proof for one non-receipt event in
// a module operation's committed write set.
type OperationWriteSetEntry struct {
	EventID    string `json:"event_id"`
	RecordPath string `json:"record_path"`
	RecordKind string `json:"record_kind"`
	Digest     string `json:"digest"`
}

// OperationReceipt is the immutable replay result for one module-owned state
// operation. The receipt event itself is excluded from WriteSet.
type OperationReceipt struct {
	SchemaVersion int                      `json:"schema_version"`
	ModuleID      string                   `json:"module_id"`
	OperationID   string                   `json:"operation_id"`
	Result        json.RawMessage          `json:"result"`
	ResultDigest  string                   `json:"result_digest"`
	WriteSet      []OperationWriteSetEntry `json:"write_set"`
}

// NewOperationReceipt builds the receipt event after hashing the immutable
// result and sorting the complete non-receipt write set by event ID.
func NewOperationReceipt(moduleID, operationID string, result json.RawMessage, events []Event) (Event, error) {
	if _, err := identity.OperationalModuleRoot(moduleID); err != nil {
		return Event{}, err
	}
	if err := identity.ValidateOperationalID(operationID); err != nil {
		return Event{}, err
	}
	if !json.Valid(result) {
		return Event{}, fmt.Errorf("operation result must be one valid JSON value")
	}
	resultDigest, err := identity.ID("operation-result", 1, json.RawMessage(result))
	if err != nil {
		return Event{}, fmt.Errorf("identify operation result: %w", err)
	}
	writeSet := make([]OperationWriteSetEntry, 0, len(events))
	for _, event := range events {
		digest, digestErr := transactionEventDigest(event)
		if digestErr != nil {
			return Event{}, fmt.Errorf("digest operation write-set event %q: %w", event.ID, digestErr)
		}
		writeSet = append(writeSet, OperationWriteSetEntry{EventID: event.ID, RecordPath: event.RecordPath, RecordKind: event.Record.Kind, Digest: digest})
	}
	sort.Slice(writeSet, func(left, right int) bool { return writeSet[left].EventID < writeSet[right].EventID })
	for index := 1; index < len(writeSet); index++ {
		if writeSet[index-1].EventID == writeSet[index].EventID {
			return Event{}, fmt.Errorf("operation write set repeats event ID %q", writeSet[index].EventID)
		}
	}
	for _, entry := range writeSet {
		if entry.EventID == operationID {
			return Event{}, fmt.Errorf("operation write set must not contain its receipt event")
		}
	}
	receipt := OperationReceipt{SchemaVersion: 1, ModuleID: moduleID, OperationID: operationID, Result: append(json.RawMessage(nil), result...), ResultDigest: resultDigest, WriteSet: writeSet}
	data, err := json.Marshal(receipt)
	if err != nil {
		return Event{}, fmt.Errorf("encode operation receipt: %w", err)
	}
	logicalPath, err := identity.StateOperationReceiptPath(moduleID, operationID)
	if err != nil {
		return Event{}, err
	}
	return Event{ID: operationID, RecordPath: logicalPath, Record: Record{Kind: "operation_receipt", SchemaVersion: 1, Data: data}}, nil
}

// Validate checks receipt identity, immutable result digest, sorted write-set
// uniqueness, and that every referenced data record belongs to this module.
func (receipt OperationReceipt) Validate(logicalPath string) error {
	if err := receipt.validateIdentity(logicalPath); err != nil {
		return err
	}
	return receipt.validateWriteSet()
}

func (receipt OperationReceipt) validateIdentity(logicalPath string) error {
	if receipt.SchemaVersion != 1 || receipt.ModuleID == "" {
		return fmt.Errorf("operation receipt metadata is invalid")
	}
	if _, err := identity.OperationalModuleRoot(receipt.ModuleID); err != nil {
		return err
	}
	if err := identity.ValidateOperationalID(receipt.OperationID); err != nil {
		return err
	}
	expectedPath, err := identity.StateOperationReceiptPath(receipt.ModuleID, receipt.OperationID)
	if err != nil || logicalPath != expectedPath {
		return fmt.Errorf("operation receipt path does not match its owner and identity")
	}
	if !json.Valid(receipt.Result) {
		return fmt.Errorf("operation receipt result is invalid JSON")
	}
	resultDigest, err := identity.ID("operation-result", 1, json.RawMessage(receipt.Result))
	if err != nil || resultDigest != receipt.ResultDigest {
		return fmt.Errorf("operation receipt result digest mismatch")
	}
	return nil
}

func (receipt OperationReceipt) validateWriteSet() error {
	if receipt.WriteSet == nil {
		return fmt.Errorf("operation receipt write set is required")
	}
	moduleRoot, err := identity.OperationalModuleRoot(receipt.ModuleID)
	if err != nil {
		return err
	}
	previousEventID := ""
	for _, entry := range receipt.WriteSet {
		if err := validateOperationWriteSetEntry(entry, receipt.OperationID, receipt.ModuleID, moduleRoot, previousEventID); err != nil {
			return err
		}
		previousEventID = entry.EventID
	}
	return nil
}

func validateOperationWriteSetEntry(entry OperationWriteSetEntry, operationID, moduleID, moduleRoot, previousEventID string) error {
	if entry.EventID == "" || entry.EventID == operationID || entry.EventID <= previousEventID {
		return fmt.Errorf("operation receipt write set is not uniquely sorted")
	}
	if err := identity.ValidateRelativePath(entry.RecordPath, false); err != nil {
		return fmt.Errorf("operation receipt write-set path is invalid")
	}
	if len(entry.Digest) != 64 || !isLowerHexDigest(entry.Digest) {
		return fmt.Errorf("operation receipt write-set digest is invalid")
	}
	physical, err := identity.OperationalRecordPath(entry.RecordPath, entry.RecordKind, moduleID)
	if err != nil || !strings.HasPrefix(physical, moduleRoot+"/") {
		return fmt.Errorf("operation receipt write-set record is not owned by its module")
	}
	return nil
}

func isLowerHexDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}
