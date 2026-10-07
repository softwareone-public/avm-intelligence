package queue

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/state"
)

const lifecycleIntentPrefix = "state/projection/lifecycle/"

// LifecycleIntent retains current work status independently of discovery and
// delivery generations. Input identity fences stale projection consumers.
type LifecycleIntent struct {
	SchemaVersion     int       `json:"schema_version"`
	ModuleID          string    `json:"module_id"`
	Generation        uint64    `json:"generation"`
	RequestID         string    `json:"request_id"`
	DesiredGeneration uint64    `json:"desired_generation"`
	InputFingerprint  string    `json:"input_fingerprint"`
	SourceSHA         string    `json:"source_sha"`
	Status            string    `json:"status"`
	Reason            string    `json:"reason"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Validate checks lifecycle identity before a record can control delivery.
func (intent LifecycleIntent) Validate() error {
	if intent.SchemaVersion != 1 || intent.Generation == 0 || intent.DesiredGeneration == 0 || intent.UpdatedAt.IsZero() {
		return fmt.Errorf("lifecycle metadata is incomplete")
	}
	if err := validateSourceIdentity(intent.ModuleID, intent.SourceSHA); err != nil {
		return err
	}
	fingerprint, err := hex.DecodeString(intent.InputFingerprint)
	if err != nil || len(fingerprint) != 32 || intent.RequestID != intent.ModuleID+"-"+intent.InputFingerprint {
		return fmt.Errorf("lifecycle request identity is invalid")
	}
	switch intent.Status {
	case ModuleStatusBacklog, ModuleStatusQueued, ModuleStatusAnalyzing, ModuleStatusNeedsReview, ModuleStatusBlocked, ModuleStatusFailed, ModuleStatusPublishing:
		return nil
	default:
		return fmt.Errorf("unsupported lifecycle status")
	}
}

// AcquisitionLifecycleEvents co-commits work progress for acquisition-only
// modules. Mixed assessment contexts require the full module reducer adapter.
func AcquisitionLifecycleEvents(snapshot state.Snapshot, request Request, events []state.Event, now time.Time) ([]state.Event, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.Work.Phase != PhaseAcquireSource {
		return events, nil
	}
	discoveryRecord, exists := snapshot.Records["state/projection/intents/"+request.ModuleID+".json"]
	if !exists {
		return events, nil
	}
	var discovery struct {
		SchemaVersion int    `json:"schema_version"`
		ModuleID      string `json:"module_id"`
		Status        string `json:"status"`
		Source        *struct {
			Commit string `json:"commit"`
		} `json:"source"`
	}
	if discoveryRecord.Kind != "projection_intent" || discoveryRecord.SchemaVersion != 1 || json.Unmarshal(discoveryRecord.Data, &discovery) != nil || discovery.SchemaVersion != 1 || discovery.ModuleID != request.ModuleID {
		return nil, fmt.Errorf("invalid discovery lifecycle precondition")
	}
	if discovery.Status != "backlog" || discovery.Source == nil || discovery.Source.Commit != request.SourceSHA {
		return events, nil
	}
	requests, err := loadVersionedRequests(snapshot)
	if err != nil {
		return nil, err
	}
	for _, other := range requests {
		if other.ModuleID != request.ModuleID || other.Status == StatusSuperseded {
			continue
		}
		if other.Work.Phase == PhaseAssess || other.Work.DesiredGeneration > request.Work.DesiredGeneration {
			return events, nil
		}
		if other.ID != request.ID && other.Work.DesiredGeneration == request.Work.DesiredGeneration {
			return nil, fmt.Errorf("acquisition lifecycle has ambiguous desired work")
		}
	}
	context, err := ContextFromRequest(request)
	if err != nil {
		return nil, err
	}
	status, err := ReduceModuleStatus(ModuleStatusInput{ModuleID: request.ModuleID, Lifecycle: ModuleLifecycleActive, Contexts: []ModuleContext{context}})
	if err != nil {
		return nil, err
	}
	intent := LifecycleIntent{
		SchemaVersion: 1, ModuleID: request.ModuleID, Generation: 1, RequestID: request.ID,
		DesiredGeneration: request.Work.DesiredGeneration, InputFingerprint: request.Fingerprint,
		SourceSHA: request.SourceSHA, Status: status.Status, Reason: status.Reason, UpdatedAt: now.UTC(),
	}
	path := lifecycleIntentPrefix + request.ModuleID + ".json"
	if record, exists := snapshot.Records[path]; exists {
		var previous LifecycleIntent
		if record.Kind != "projection_lifecycle" || record.SchemaVersion != 1 || json.Unmarshal(record.Data, &previous) != nil || previous.Validate() != nil || previous.ModuleID != request.ModuleID {
			return nil, fmt.Errorf("invalid existing lifecycle intent")
		}
		if previous.DesiredGeneration > intent.DesiredGeneration {
			return events, nil
		}
		if previous.RequestID == intent.RequestID && previous.Status == intent.Status && previous.Reason == intent.Reason && previous.InputFingerprint == intent.InputFingerprint {
			return events, nil
		}
		if previous.Generation == math.MaxUint64 {
			return nil, fmt.Errorf("lifecycle generation exhausted")
		}
		intent.Generation = previous.Generation + 1
	}
	if now.IsZero() {
		return nil, fmt.Errorf("lifecycle timestamp is required")
	}
	if err := intent.Validate(); err != nil {
		return nil, err
	}
	event, err := stateRecordEvent(path, "projection_lifecycle", intent, "", 0)
	if err != nil {
		return nil, err
	}
	return append(events, event), nil
}
