package queue

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"golang.org/x/mod/semver"
)

// LifecycleSlot is the durable module-wide owner of one released AVM version.
// It intentionally has no time-based release operation: only verified Pages
// membership may release it (implemented by the publication fan-out packet).
type LifecycleSlot struct {
	SchemaVersion     int                        `json:"schema_version"`
	ModuleID          string                     `json:"module_id"`
	ReleaseVersion    string                     `json:"release_version"`
	DesiredGeneration uint64                     `json:"desired_generation"`
	RequestID         string                     `json:"request_id"`
	AcquiredAt        time.Time                  `json:"acquired_at"`
	Status            string                     `json:"status"`
	ReleaseProof      *LifecycleSlotReleaseProof `json:"release_proof,omitempty"`
}

// LifecycleSlotReleaseProof is the immutable deployment evidence WP-21 must
// write when clearing a held slot. All three identities are required so a
// release never becomes an ambiguous boolean or lease-expiry inference.
type LifecycleSlotReleaseProof struct {
	DeploymentReceiptID string    `json:"deployment_receipt_id"`
	MembershipDigest    string    `json:"membership_digest"`
	VerifiedAt          time.Time `json:"verified_at"`
}

var avmReleaseVersionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func (slot LifecycleSlot) Validate() error {
	requestFingerprint := strings.TrimPrefix(slot.RequestID, slot.ModuleID+"-")
	if slot.SchemaVersion != 1 || !moduleIDPattern.MatchString(slot.ModuleID) || !avmReleaseVersionPattern.MatchString(slot.ReleaseVersion) || slot.DesiredGeneration == 0 || len(requestFingerprint) != 64 || !isLowerHexDigest(requestFingerprint) || slot.RequestID != slot.ModuleID+"-"+requestFingerprint || slot.AcquiredAt.IsZero() {
		return fmt.Errorf("AVM lifecycle slot is incomplete")
	}
	switch slot.Status {
	case "active":
		if slot.ReleaseProof != nil {
			return fmt.Errorf("active AVM lifecycle slot contains release proof")
		}
	case "released":
		if slot.ReleaseProof == nil || identity.ValidateRecordID("deployment-receipt", slot.ReleaseProof.DeploymentReceiptID) != nil || !isLowerHexDigest(slot.ReleaseProof.MembershipDigest) || slot.ReleaseProof.VerifiedAt.IsZero() {
			return fmt.Errorf("released AVM lifecycle slot lacks exact deployment proof")
		}
	default:
		return fmt.Errorf("AVM lifecycle slot has invalid status")
	}
	return nil
}

func lifecycleSlotFromSnapshot(snapshot state.Snapshot, moduleID string) (LifecycleSlot, bool, error) {
	logicalPath := "state/lifecycle/slots/" + moduleID + ".json"
	record, exists := snapshot.Records[logicalPath]
	if !exists {
		return LifecycleSlot{}, false, nil
	}
	if record.Kind != "lifecycle_slot" || record.SchemaVersion != 1 {
		return LifecycleSlot{}, false, fmt.Errorf("module lifecycle slot record is invalid")
	}
	var slot LifecycleSlot
	if err := json.Unmarshal(record.Data, &slot); err != nil {
		return LifecycleSlot{}, false, fmt.Errorf("decode module lifecycle slot: %w", err)
	}
	if err := slot.Validate(); err != nil || slot.ModuleID != moduleID {
		return LifecycleSlot{}, false, fmt.Errorf("module lifecycle slot owner or payload is invalid")
	}
	return slot, true, nil
}

func slotAllowsRequest(snapshot state.Snapshot, request Request) (bool, error) {
	if request.Work == nil || strings.TrimSpace(request.Work.AVMReleaseVersion) == "" || !avmReleaseVersionPattern.MatchString(request.Work.AVMReleaseVersion) {
		return false, nil
	}
	slot, exists, err := lifecycleSlotFromSnapshot(snapshot, request.ModuleID)
	if err != nil {
		return false, err
	}
	if !exists {
		return request.Work.Phase == PhaseAcquireSource, nil
	}
	if slot.Status == "released" {
		versionOrder := compareAVMReleases(request.Work.AVMReleaseVersion, slot.ReleaseVersion)
		return versionOrder > 0 || versionOrder == 0 && (request.Work.Phase == PhaseAssess || request.Work.DesiredGeneration >= slot.DesiredGeneration), nil
	}
	// The slot serializes AVM releases, while assessment generations belong to
	// independent provider contexts. Queue coalescing rejects stale generations
	// for the same context before dispatch.
	if slot.ReleaseVersion != request.Work.AVMReleaseVersion {
		return false, nil
	}
	return request.Work.Phase == PhaseAssess || request.Work.DesiredGeneration >= slot.DesiredGeneration, nil
}

// slotAllowsRequestInQueue preserves release order before the first claim has
// created the durable slot. Once a slot exists, slotAllowsRequest is the sole
// authority and later releases remain queued until exact publication release.
func slotAllowsRequestInQueue(snapshot state.Snapshot, request Request, requests []Request) (bool, error) {
	allowed, err := slotAllowsRequest(snapshot, request)
	if err != nil || !allowed || request.Work.Phase != PhaseAcquireSource {
		return allowed, err
	}
	slot, exists, err := lifecycleSlotFromSnapshot(snapshot, request.ModuleID)
	if err != nil || exists && slot.Status == "active" {
		return allowed, err
	}
	for _, other := range requests {
		if other.ID == request.ID || other.ModuleID != request.ModuleID || other.Status != StatusQueued || other.Work == nil || other.Work.Phase != PhaseAcquireSource {
			continue
		}
		if compareAVMReleases(other.Work.AVMReleaseVersion, request.Work.AVMReleaseVersion) < 0 {
			return false, nil
		}
	}
	return true, nil
}

func compareAVMReleases(left, right string) int {
	if !strings.HasPrefix(left, "v") {
		left = "v" + left
	}
	if !strings.HasPrefix(right, "v") {
		right = "v" + right
	}
	if compared := semver.Compare(left, right); compared != 0 {
		return compared
	}
	return strings.Compare(left, right)
}

func lifecycleSlotEvent(snapshot state.Snapshot, request Request, operationID string, now time.Time) (state.Event, error) {
	return buildLifecycleSlotEvent(snapshot, request, operationID, now, false)
}

func lifecycleSlotEventForAssessmentWake(snapshot state.Snapshot, request Request, operationID string, now time.Time) (state.Event, error) {
	return buildLifecycleSlotEvent(snapshot, request, operationID, now, true)
}

func lifecycleSlotSnapshotEvent(snapshot state.Snapshot, moduleID, operationID string) (state.Event, error) {
	slot, exists, err := lifecycleSlotFromSnapshot(snapshot, moduleID)
	if err != nil || !exists {
		if err == nil {
			err = fmt.Errorf("module lifecycle slot is missing")
		}
		return state.Event{}, err
	}
	path, err := identity.StateLifecycleSlotPath(moduleID)
	if err != nil {
		return state.Event{}, err
	}
	data, err := json.Marshal(slot)
	if err != nil {
		return state.Event{}, err
	}
	return state.Event{ID: operationID + ".lifecycle-slot", RecordPath: path, Record: state.Record{Kind: "lifecycle_slot", SchemaVersion: 1, Data: data}}, nil
}

func buildLifecycleSlotEvent(snapshot state.Snapshot, request Request, operationID string, now time.Time, acceptedAssessmentWake bool) (state.Event, error) {
	allowed, err := slotAllowsRequest(snapshot, request)
	if err != nil {
		return state.Event{}, err
	}
	if !allowed && acceptedAssessmentWake && request.Work != nil && request.Work.Phase == PhaseAssess {
		_, exists, slotErr := lifecycleSlotFromSnapshot(snapshot, request.ModuleID)
		if slotErr != nil {
			return state.Event{}, slotErr
		}
		allowed = !exists
	}
	if !allowed {
		return state.Event{}, ErrNoDispatchableWork
	}
	path, err := identity.StateLifecycleSlotPath(request.ModuleID)
	if err != nil {
		return state.Event{}, err
	}
	slot, exists, err := lifecycleSlotFromSnapshot(snapshot, request.ModuleID)
	if err != nil {
		return state.Event{}, err
	}
	if !exists || slot.Status == "released" {
		slot = LifecycleSlot{SchemaVersion: 1, ModuleID: request.ModuleID, ReleaseVersion: request.Work.AVMReleaseVersion, DesiredGeneration: request.Work.DesiredGeneration, RequestID: request.ID, AcquiredAt: now.UTC(), Status: "active"}
	} else if request.Work.DesiredGeneration > slot.DesiredGeneration {
		slot.DesiredGeneration = request.Work.DesiredGeneration
	}
	if err = slot.Validate(); err != nil {
		return state.Event{}, err
	}
	data, err := json.Marshal(slot)
	if err != nil {
		return state.Event{}, err
	}
	return state.Event{ID: operationID + ".lifecycle-slot", RecordPath: path, Record: state.Record{Kind: "lifecycle_slot", SchemaVersion: 1, Data: data}}, nil
}

// LifecycleSlotPath exposes the stable logical path for tests and readers.
func LifecycleSlotPath(moduleID string) (string, error) {
	return identity.StateLifecycleSlotPath(moduleID)
}
