package state

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

const (
	DiscoveryWorkReady          = "ready"
	DiscoveryWorkRetryable      = "retryable"
	DiscoveryWorkOutcomeUnknown = "outcome_unknown"
	DiscoveryWorkNeedsReview    = "needs_review"
	DiscoveryWorkCompleted      = "completed"
)

var discoveryWorkFailures = map[string]bool{
	"source_unavailable": true, "rate_limited": true,
	"publication_unknown": true, "policy_rejected": true,
	"state_integrity": true, "none": true,
}

// DiscoveryWork is a module-owned discovery control record. It supplements,
// but never supersedes, the request/attempt lifecycle and operation receipt.
type DiscoveryWork struct {
	SchemaVersion      int       `json:"schema_version"`
	ModuleID           string    `json:"module_id"`
	DesiredFingerprint string    `json:"desired_fingerprint"`
	OperationID        string    `json:"operation_id"`
	ReceiptPath        string    `json:"receipt_path,omitempty"`
	State              string    `json:"state"`
	Attempt            uint64    `json:"attempt"`
	OwnerRunID         string    `json:"owner_run_id,omitempty"`
	OwnerGeneration    uint64    `json:"owner_generation"`
	LeaseExpiresAt     time.Time `json:"lease_expires_at,omitempty"`
	RetryAt            time.Time `json:"retry_at,omitempty"`
	FailureCategory    string    `json:"failure_category"`
}

// Validate enforces module ownership, stable operation/receipt identity,
// fenced ownership fields, and allowlisted failure categories.
func (work DiscoveryWork) Validate(logicalPath string) error {
	if err := work.validateIdentity(logicalPath); err != nil {
		return err
	}
	if err := work.validateOwnershipAndLease(); err != nil {
		return err
	}
	if err := work.validateRetry(); err != nil {
		return err
	}
	return work.validateReceiptPath()
}

func (work DiscoveryWork) validateIdentity(logicalPath string) error {
	if work.SchemaVersion != 1 || strings.TrimSpace(work.ModuleID) == "" || work.DesiredFingerprint == "" {
		return fmt.Errorf("discovery work metadata is incomplete")
	}
	if _, err := identity.OperationalModuleRoot(work.ModuleID); err != nil {
		return err
	}
	expectedPath, err := identity.StateDiscoveryWorkPath(work.ModuleID)
	if err != nil || logicalPath != expectedPath {
		return fmt.Errorf("discovery work path does not match its module owner")
	}
	if len(work.DesiredFingerprint) != 64 || !isLowerHexDigest(work.DesiredFingerprint) {
		return fmt.Errorf("discovery work fingerprint is invalid")
	}
	return identity.ValidateOperationalID(work.OperationID)
}

func (work DiscoveryWork) validateOwnershipAndLease() error {
	if !validDiscoveryWorkState(work.State) || !discoveryWorkFailures[work.FailureCategory] {
		return fmt.Errorf("discovery work state or failure category is not allowlisted")
	}
	if work.Attempt == 0 || work.OwnerGeneration == 0 {
		return fmt.Errorf("discovery work attempt and owner generation must be positive")
	}
	if (work.OwnerRunID == "") != work.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("discovery work owner and lease expiry are inconsistent")
	}
	return nil
}

func (work DiscoveryWork) validateRetry() error {
	if work.State == DiscoveryWorkRetryable && (work.RetryAt.IsZero() || work.FailureCategory == "none") {
		return fmt.Errorf("retryable discovery work requires a retry time and failure category")
	}
	if work.State != DiscoveryWorkRetryable && !work.RetryAt.IsZero() {
		return fmt.Errorf("non-retryable discovery work cannot carry a retry time")
	}
	return nil
}

func (work DiscoveryWork) validateReceiptPath() error {
	if work.ReceiptPath == "" {
		return nil
	}
	if err := identity.ValidateRelativePath(work.ReceiptPath, false); err != nil {
		return fmt.Errorf("discovery work receipt reference is invalid")
	}
	moduleRoot, _ := identity.OperationalModuleRoot(work.ModuleID)
	if !strings.HasPrefix(work.ReceiptPath, moduleRoot+"/operations/receipts/") || path.Ext(work.ReceiptPath) != ".json" {
		return fmt.Errorf("discovery work receipt reference is not owned by its module")
	}
	return nil
}

func validDiscoveryWorkState(value string) bool {
	switch value {
	case DiscoveryWorkReady, DiscoveryWorkRetryable, DiscoveryWorkOutcomeUnknown, DiscoveryWorkNeedsReview, DiscoveryWorkCompleted:
		return true
	default:
		return false
	}
}

// DiscoveryWorkRecord creates the typed logical state record.
func DiscoveryWorkRecord(work DiscoveryWork) (Record, error) {
	logicalPath, err := identity.StateDiscoveryWorkPath(work.ModuleID)
	if err != nil {
		return Record{}, err
	}
	if err = work.Validate(logicalPath); err != nil {
		return Record{}, err
	}
	data, err := json.Marshal(work)
	if err != nil {
		return Record{}, fmt.Errorf("encode discovery work: %w", err)
	}
	return Record{Kind: "discovery_work", SchemaVersion: 1, Data: data}, nil
}
