package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

const (
	ProjectionPending                                    = "pending"
	ProjectionProjecting                                 = "projecting"
	ProjectionProjected                                  = "projected"
	ProjectionRetryable                                  = "retryable"
	ProjectionNeedsReview                                = "needs-review"
	ProjectionBatchRunning                               = "running"
	ProjectionBatchComplete                              = "complete"
	ProjectionErrorRateLimited                           = "rate_limited"
	ProjectionErrorTransient                             = "transient"
	ProjectionErrorBudget                                = "budget"
	ProjectionErrorNeedsReview                           = "needs_review"
	projectionRetryOwnerEffectUnknown                    = "owner_terminated_effect_unknown"
	ProjectionPriorityRetry           ProjectionPriority = 1
	ProjectionPriorityChanged         ProjectionPriority = 2
	ProjectionPriorityActive          ProjectionPriority = 3
	ProjectionPriorityCleanup         ProjectionPriority = 4
	projectionStateSchemaVersion                         = 1
	projectionOperation                                  = "projection.delivery"
	projectionDeliveryPrefix                             = "state/projection/delivery/"
	projectionBindingPrefix                              = "state/projection/bindings/"
	MaxProjectionBatchSize                               = 25
)

var (
	ErrProjectionFence        = errors.New("projection intent generation or lease fence is stale")
	projectionRetryableErrors = map[string]struct{}{ProjectionErrorRateLimited: {}, ProjectionErrorTransient: {}, ProjectionErrorBudget: {}}
	projectionModuleIDPattern = regexp.MustCompile(`^avm-(res|ptn|utl)-[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type ProjectionPriority uint8

// ProjectionIntentRecord is the independently retryable desired board state
// stored alongside discovery's lifecycle intent.
type ProjectionIntentRecord struct {
	SchemaVersion               int                            `json:"schema_version"`
	ModuleID                    string                         `json:"module_id"`
	Generation                  uint64                         `json:"generation"`
	Fingerprint                 string                         `json:"fingerprint"`
	DiscoveryFingerprint        string                         `json:"discovery_fingerprint,omitempty"`
	LifecycleFingerprint        string                         `json:"lifecycle_fingerprint,omitempty"`
	Priority                    ProjectionPriority             `json:"priority"`
	Projection                  Projection                     `json:"projection"`
	NeutralProjection           *projection.ProjectionIdentity `json:"neutral_projection,omitempty"`
	DeliveryState               string                         `json:"delivery_state"`
	Attempt                     uint64                         `json:"attempt"`
	Fence                       uint64                         `json:"fence"`
	ClaimID                     string                         `json:"claim_id,omitempty"`
	LeaseOwner                  string                         `json:"lease_owner,omitempty"`
	OwnerRunID                  uint64                         `json:"owner_run_id,omitempty"`
	OwnerRunAttempt             uint64                         `json:"owner_run_attempt,omitempty"`
	LeaseExpiresAt              time.Time                      `json:"lease_expires_at,omitempty"`
	LeaseDuration               time.Duration                  `json:"lease_duration_ns,omitempty"`
	SnapshotCursors             map[string]string              `json:"snapshot_cursors,omitempty"`
	Budget                      BudgetSnapshot                 `json:"budget,omitempty"`
	RetryAt                     time.Time                      `json:"retry_at,omitempty"`
	RetryReason                 string                         `json:"retry_reason,omitempty"`
	RetryRequest                *ratelimit.Request             `json:"retry_request,omitempty"`
	LastErrorClass              string                         `json:"last_error_class,omitempty"`
	LastAcknowledgedFingerprint string                         `json:"last_acknowledged_fingerprint,omitempty"`
	UpdatedAt                   time.Time                      `json:"updated_at"`
	PendingEvidence             map[string]RunEvidence         `json:"pending_evidence,omitempty"`
}

// ProjectionBinding keeps remote IDs separate from the desired projection.
type ProjectionBinding struct {
	SchemaVersion    int                           `json:"schema_version"`
	ModuleID         string                        `json:"module_id"`
	IssueNumber      int                           `json:"issue_number"`
	IssueNodeID      string                        `json:"issue_node_id"`
	IssueURL         string                        `json:"issue_url"`
	ProjectItemID    string                        `json:"project_item_id"`
	Generation       uint64                        `json:"generation"`
	UpdatedAt        time.Time                     `json:"updated_at"`
	EvidenceComments map[string]RunEvidenceReceipt `json:"evidence_comments,omitempty"`
}

// ProjectionBatch is a run-local claim descriptor. Durable ownership and
// resumable remote-read checkpoints live on each module delivery record.
type ProjectionBatch struct {
	ID              string
	RunID           string
	OwnerRunID      uint64
	OwnerRunAttempt uint64
	Status          string
	ModuleIDs       []string
	Budget          BudgetSnapshot
	LeaseDuration   time.Duration
	LeaseExpiresAt  time.Time
	CreatedAt       time.Time
}

// BudgetSnapshot persists resource headroom and usage with one module claim.
type BudgetSnapshot struct {
	RESTRemaining      int `json:"rest_remaining"`
	GraphQLRemaining   int `json:"graphql_remaining"`
	SearchRemaining    int `json:"search_remaining"`
	SecondaryRemaining int `json:"secondary_remaining"`
	RESTCalls          int `json:"rest_calls"`
	GraphQLCalls       int `json:"graphql_calls"`
	SearchCalls        int `json:"search_calls"`
}

// ProjectionBatchClaim is the locally validated work returned from one CAS.
type ProjectionBatchClaim struct {
	Head     string
	Batch    ProjectionBatch
	Intents  []ProjectionIntentRecord
	Bindings map[string]ProjectionBinding
	Changed  bool
}

// ProjectionClaimOptions supplies the validated limits for a projection claim.
type ProjectionClaimOptions struct {
	RunID           string
	OwnerRunID      uint64
	OwnerRunAttempt uint64
	Now             time.Time
	Lease           time.Duration
	Limit           int
	Budget          BudgetSnapshot
}

// ProjectionOwner identifies the exact Actions attempt that owns an expired
// module delivery. It is only a recovery candidate; terminal status must be
// verified with the Actions API before ownership can be released.
type ProjectionOwner struct {
	ModuleID       string    `json:"module_id"`
	ClaimID        string    `json:"claim_id"`
	Fence          uint64    `json:"fence"`
	RunID          uint64    `json:"run_id"`
	RunAttempt     uint64    `json:"run_attempt"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

// ProjectionOwnerObservation is the exact GitHub Actions attempt response
// required before a parked delivery can be retried.
type ProjectionOwnerObservation struct {
	Repository        string `json:"repository"`
	WorkflowPath      string `json:"path"`
	HeadBranch        string `json:"head_branch"`
	RunID             uint64 `json:"id"`
	RunAttempt        uint64 `json:"run_attempt"`
	Status            string `json:"status"`
	Conclusion        string `json:"conclusion"`
	CurrentRunAttempt uint64 `json:"current_run_attempt"`
	CurrentRunStatus  string `json:"current_run_status"`
}

// ProjectionIntentSaveResult returns one atomic desired-projection batch save.
type ProjectionIntentSaveResult struct {
	Head    string
	Intents []ProjectionIntentRecord
	Changed bool
}

// ProjectionOutcome is the verified result of one module's remote projection.
type ProjectionOutcome struct {
	Success           bool
	ErrorClass        string
	RetryAt           time.Time
	RetryReason       string
	RetryRequest      *ratelimit.Request
	CompletedAt       time.Time
	Binding           *ProjectionBinding
	Budget            BudgetSnapshot
	EvidenceRunID     string
	EvidencePhase     string
	DeliveredEvidence map[string]RunEvidenceReceipt
}

// ProjectionOutcomeInput binds an outcome to one claimed module.
type ProjectionOutcomeInput struct {
	ClaimID    string
	ModuleID   string
	RunID      string
	Generation uint64
	Fence      uint64
	Outcome    ProjectionOutcome
}

// ProjectionSnapshotCheckpoint stores resumable read progress on one module delivery.
type ProjectionSnapshotCheckpoint struct {
	ClaimID        string
	RunID          string
	ModuleID       string
	Cursors        map[string]string
	Budget         BudgetSnapshot
	CheckpointedAt time.Time
}

// ProjectionOutcomeResult reports one atomic intent/batch checkpoint.
type ProjectionOutcomeResult struct {
	Head   string
	Batch  ProjectionBatch
	Intent ProjectionIntentRecord
}

// ProjectionRunOptions supplies the approved board target and bounded retry delay.
type ProjectionRunOptions struct {
	Owner         string
	Repository    string
	ProjectNumber int
	RetryDelay    time.Duration
	BudgetPolicy  ProjectionBudgetPolicy
	InitialBudget *ProjectionBudget
	EvidenceRunID string
	EvidencePhase string
}

// ProjectionRunResult reports partial success without invalidating accepted data.
type ProjectionRunResult struct {
	Head      string
	Batch     ProjectionBatch
	Completed int
	Partial   bool
	Errors    []string
	Cause     error `json:"-"`
	RetryAt   time.Time
}

// ProjectionDeliveryError retains a safe typed cause and durable retry checkpoint.
type ProjectionDeliveryError struct {
	Cause   error
	RetryAt time.Time
}

func (failure *ProjectionDeliveryError) Error() string {
	return "module delivery was not durably acknowledged"
}
func (failure *ProjectionDeliveryError) Unwrap() error { return failure.Cause }

type projectionFingerprintInput struct {
	ModuleID       string `json:"module_id"`
	ModuleType     string `json:"module_type"`
	Domain         string `json:"domain"`
	Version        string `json:"version"`
	SourceSHA      string `json:"source_sha"`
	FactoryVersion string `json:"cli_version"`
	Status         string `json:"status"`
	Outcome        string `json:"outcome"`
	WorkType       string `json:"work_type"`
	RunURL         string `json:"run_url,omitempty"`
	Published      string `json:"published,omitempty"`
	LastAnalyzed   string `json:"last_analyzed,omitempty"`
}

type projectionEventIdentity struct {
	Path   string       `json:"path"`
	Record state.Record `json:"record"`
}

// NewProjectionIntent builds an immutable desired fingerprint for one module.
func NewProjectionIntent(projection Projection, generation uint64, priority ProjectionPriority, updatedAt time.Time) (ProjectionIntentRecord, error) {
	if err := projection.Validate(); err != nil {
		return ProjectionIntentRecord{}, err
	}
	if generation == 0 || updatedAt.IsZero() || priority < ProjectionPriorityRetry || priority > ProjectionPriorityCleanup {
		return ProjectionIntentRecord{}, fmt.Errorf("projection generation, priority, and timestamp are required")
	}
	fingerprint, err := ProjectionFingerprint(projection)
	if err != nil {
		return ProjectionIntentRecord{}, err
	}
	return ProjectionIntentRecord{
		SchemaVersion: projectionStateSchemaVersion, ModuleID: projection.ModuleID,
		Generation: generation, Fingerprint: fingerprint, Priority: priority,
		Projection: projection, DeliveryState: ProjectionPending, UpdatedAt: updatedAt.UTC(),
	}, nil
}

// ProjectionFingerprint excludes volatile run URLs for Publishing and Done,
// but retains publication identity and every owned summary/field value.
func ProjectionFingerprint(projection Projection) (string, error) {
	if err := projection.Validate(); err != nil {
		return "", err
	}
	input := projectionFingerprintInput{
		ModuleID: projection.ModuleID, ModuleType: projection.ModuleType, Domain: projection.Domain,
		Version: projection.Version, SourceSHA: projection.SourceSHA, FactoryVersion: projection.FactoryVersion,
		Status: projection.Status, Outcome: projection.Outcome, WorkType: projection.WorkType,
		Published: projection.Published,
	}
	if projection.Status != StatusPublishing && projection.Status != StatusDone {
		input.RunURL = projection.RunURL
	}
	if !projection.LastAnalyzed.IsZero() {
		input.LastAnalyzed = projection.LastAnalyzed.UTC().Format(time.RFC3339Nano)
	}
	return identity.ID("projection", 1, input)
}

// ReplayAcknowledgedProjections reopens only completed delivery for an explicit
// scoped drift check. Pending retries, review states and owned leases are retained.
func ReplayAcknowledgedProjections(ctx context.Context, coordinator state.TransactionCoordinator, moduleIDs []string, now time.Time) (string, error) {
	if coordinator == nil || now.IsZero() || len(moduleIDs) == 0 {
		return "", fmt.Errorf("projection replay requires selected modules and a timestamp")
	}
	reader, ok := coordinator.(state.GitReader)
	if !ok {
		return "", fmt.Errorf("projection replay requires a verified state reader")
	}
	_, snapshot, err := reader.Read(ctx)
	if err != nil {
		return "", err
	}
	events := make([]projectionRecordEvent, 0, len(moduleIDs))
	seen := make(map[string]bool, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		if !projectionModuleIDPattern.MatchString(moduleID) || seen[moduleID] {
			return "", fmt.Errorf("invalid or duplicate projection replay module %q", moduleID)
		}
		seen[moduleID] = true
		path := projectionDeliveryPrefix + moduleID + ".json"
		intent, exists, readErr := readProjectionIntent(snapshot, path)
		if readErr != nil {
			return "", readErr
		}
		if !exists || intent.DeliveryState != ProjectionProjected {
			continue
		}
		if intent.LeaseOwner != "" || now.Before(intent.LeaseExpiresAt) {
			return "", ErrProjectionFence
		}
		intent.DeliveryState = ProjectionPending
		intent.UpdatedAt = now.UTC()
		events = append(events, projectionRecordEvent{path: path, kind: "projection_delivery", value: intent})
	}
	if len(events) == 0 {
		head, _, readErr := reader.Read(ctx)
		return head, readErr
	}
	operationID, err := projectionTransactionID("replay", events)
	if err != nil {
		return "", err
	}
	return runProjectionTransaction(ctx, coordinator, operationID, events, "state(projection): replay acknowledged delivery")
}

// SaveProjectionIntent replaces one durable desired delivery record. Matching
// fingerprints do not reset retries; a new generation fences the prior lease.
func SaveProjectionIntent(ctx context.Context, coordinator state.TransactionCoordinator, intent ProjectionIntentRecord) (ProjectionBatchClaim, error) {
	saved, err := SaveProjectionIntents(ctx, coordinator, []ProjectionIntentRecord{intent})
	if err != nil {
		return ProjectionBatchClaim{}, err
	}
	return ProjectionBatchClaim{Head: saved.Head, Intents: saved.Intents, Changed: saved.Changed}, nil
}

// SaveProjectionIntents merges a targeted set of desired projections in one
// expected-head transaction. Existing delivery checkpoints remain retryable.
func SaveProjectionIntents(ctx context.Context, coordinator state.TransactionCoordinator, intents []ProjectionIntentRecord) (ProjectionIntentSaveResult, error) {
	if coordinator == nil || len(intents) == 0 {
		return ProjectionIntentSaveResult{}, fmt.Errorf("at least one projection intent is required")
	}
	ordered := append([]ProjectionIntentRecord(nil), intents...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].ModuleID < ordered[right].ModuleID })
	for index := range ordered {
		if err := ordered[index].Validate(); err != nil {
			return ProjectionIntentSaveResult{}, err
		}
		if index > 0 && ordered[index-1].ModuleID == ordered[index].ModuleID {
			return ProjectionIntentSaveResult{}, fmt.Errorf("duplicate projection intent for %s", ordered[index].ModuleID)
		}
	}
	reader, ok := coordinator.(state.GitReader)
	if !ok {
		return ProjectionIntentSaveResult{}, fmt.Errorf("projection save requires a verified state reader")
	}
	head, snapshot, err := reader.Read(ctx)
	if err != nil {
		return ProjectionIntentSaveResult{}, err
	}
	merged, events, err := mergeProjectionIntents(snapshot, ordered)
	if err != nil {
		return ProjectionIntentSaveResult{}, err
	}
	if len(events) == 0 {
		return ProjectionIntentSaveResult{Head: head, Intents: merged}, nil
	}
	transactionEvents, err := projectionEvents(events)
	if err != nil {
		return ProjectionIntentSaveResult{}, err
	}
	operationID, err := projectionTransactionID("save", events)
	if err != nil {
		return ProjectionIntentSaveResult{}, err
	}
	initialResult := ProjectionIntentSaveResult{Intents: merged, Changed: true}
	result, err := runProjectionTransactionResultWithDecision(ctx, coordinator, operationID, transactionEvents, "state(projection): save delivery intent", initialResult, func(current state.Snapshot) (any, error) {
		currentMerged, currentEvents, mergeErr := mergeProjectionIntents(current, ordered)
		if mergeErr != nil {
			return nil, mergeErr
		}
		if !reflect.DeepEqual(currentEvents, events) {
			return nil, state.ErrHeadMoved
		}
		return ProjectionIntentSaveResult{Intents: currentMerged, Changed: true}, nil
	})
	if err != nil {
		return ProjectionIntentSaveResult{}, err
	}
	saved, ok := result.Value.(ProjectionIntentSaveResult)
	if !ok {
		return ProjectionIntentSaveResult{}, fmt.Errorf("projection intent save result has an invalid type")
	}
	saved.Head, saved.Changed = result.Head, result.Changed
	if !result.Changed {
		saved.Intents = initialResult.Intents
	}
	return saved, nil
}

func mergeProjectionIntents(snapshot state.Snapshot, intents []ProjectionIntentRecord) ([]ProjectionIntentRecord, []projectionRecordEvent, error) {
	merged := make([]ProjectionIntentRecord, 0, len(intents))
	events := make([]projectionRecordEvent, 0, len(intents))
	for _, intent := range intents {
		if err := validateProjectionLifecycle(snapshot, intent); err != nil {
			return nil, nil, err
		}
		if intent.NeutralProjection != nil {
			if err := validateNeutralProjectionIdentity(snapshot, *intent.NeutralProjection); err != nil {
				return nil, nil, err
			}
		}
		boundIntent, err := bindProjectionToDiscovery(snapshot, intent)
		if err != nil {
			return nil, nil, err
		}
		intent = boundIntent
		path := projectionDeliveryPrefix + intent.ModuleID + ".json"
		existing, exists, readErr := readProjectionIntent(snapshot, path)
		if readErr != nil {
			return nil, nil, readErr
		}
		updated, changed, mergeErr := mergeProjectionIntent(existing, exists, intent)
		if mergeErr != nil {
			return nil, nil, mergeErr
		}
		merged = append(merged, updated)
		if changed {
			events = append(events, projectionRecordEvent{path: path, kind: "projection_delivery", value: updated})
		}
	}
	return merged, events, nil
}

// bindProjectionToDiscovery relates delivery to current durable discovery, not
// this invocation's changed/unchanged result. Other lifecycle phases bind only
// when their source matches the current discovery source.
// LifecycleProjectionFingerprint binds delivery to both lifecycle and discovery
// authority; current-request validation also detects removed or superseded work.
func LifecycleProjectionFingerprint(snapshot state.Snapshot, moduleID string) (string, error) {
	_, current, err := queue.ReadCurrentLifecycleIntent(snapshot, moduleID)
	if err != nil || !current {
		return "", ErrProjectionFence
	}
	lifecycle := snapshot.Records["state/projection/lifecycle/"+moduleID+".json"]
	discovery, exists := snapshot.Records["state/projection/intents/"+moduleID+".json"]
	if !exists {
		return "", ErrProjectionFence
	}
	var source struct {
		Status string `json:"status"`
		Source *struct {
			Commit string `json:"commit"`
		} `json:"source"`
	}
	var intent queue.LifecycleIntent
	if json.Unmarshal(discovery.Data, &source) != nil || json.Unmarshal(lifecycle.Data, &intent) != nil || source.Status != "backlog" || source.Source == nil || source.Source.Commit != intent.SourceSHA {
		return "", ErrProjectionFence
	}
	return identity.ID("lifecycle-authority", 1, struct {
		Lifecycle json.RawMessage `json:"lifecycle"`
		Discovery json.RawMessage `json:"discovery"`
	}{lifecycle.Data, discovery.Data})
}

func validateProjectionLifecycle(snapshot state.Snapshot, intent ProjectionIntentRecord) error {
	if intent.NeutralProjection != nil {
		if err := validateNeutralProjectionIdentity(snapshot, *intent.NeutralProjection); err != nil {
			return err
		}
		active, held, reduceErr := queue.ReduceModuleProjectionStatus(snapshot, intent.ModuleID, nil)
		if reduceErr != nil {
			return fmt.Errorf("%w: reduce current controlling module release: %v", ErrProjectionFence, reduceErr)
		}
		if held && (intent.Projection.Status != active.Status || intent.Projection.Version != active.SourceVersion || intent.Projection.SourceSHA != active.SourceSHA) {
			return ErrProjectionFence
		}
	}
	if intent.LifecycleFingerprint == "" {
		return nil
	}
	lifecycle, current, readErr := queue.ReadCurrentLifecycleIntent(snapshot, intent.ModuleID)
	if readErr != nil || !current || lifecycle.Status != intent.Projection.Status || lifecycle.SourceSHA != intent.Projection.SourceSHA {
		return ErrProjectionFence
	}
	fingerprint, err := LifecycleProjectionFingerprint(snapshot, intent.ModuleID)
	if err != nil || fingerprint != intent.LifecycleFingerprint {
		return ErrProjectionFence
	}
	return nil
}

// ValidateProjectionAuthority checks lifecycle binding without remote calls.
func ValidateProjectionAuthority(snapshot state.Snapshot, intent ProjectionIntentRecord) error {
	return validateProjectionLifecycle(snapshot, intent)
}

func bindProjectionToDiscovery(snapshot state.Snapshot, intent ProjectionIntentRecord) (ProjectionIntentRecord, error) {
	if intent.DiscoveryFingerprint != "" {
		return intent, nil
	}
	record, exists := snapshot.Records["state/projection/intents/"+intent.ModuleID+".json"]
	if !exists {
		return intent, nil
	}
	var lifecycle struct {
		SchemaVersion     int    `json:"schema_version"`
		ModuleID          string `json:"module_id"`
		DesiredGeneration uint64 `json:"desired_generation"`
		Source            *struct {
			Commit  string `json:"commit"`
			Version string `json:"version"`
		} `json:"source"`
	}
	if record.Kind != "projection_intent" || record.SchemaVersion != 1 || json.Unmarshal(record.Data, &lifecycle) != nil || lifecycle.SchemaVersion != 1 || lifecycle.ModuleID != intent.ModuleID || lifecycle.DesiredGeneration == 0 {
		return intent, fmt.Errorf("discovery lifecycle binding is invalid")
	}
	commit, version := "", ""
	if lifecycle.Source != nil {
		commit, version = lifecycle.Source.Commit, lifecycle.Source.Version
	}
	if intent.Projection.SourceSHA != commit || intent.Projection.Version != version {
		return intent, nil
	}
	fingerprint, err := identity.ID("discovery-intent", 1, record.Data)
	if err != nil {
		return intent, err
	}
	intent.DiscoveryFingerprint = fingerprint
	return intent, nil
}

func mergeProjectionIntent(existing ProjectionIntentRecord, exists bool, intent ProjectionIntentRecord) (ProjectionIntentRecord, bool, error) {
	if !exists {
		return intent, true, nil
	}
	preservePendingEvidence(existing, &intent)
	if intent.Generation < existing.Generation || (intent.Generation == existing.Generation && intent.Fingerprint != existing.Fingerprint) {
		return ProjectionIntentRecord{}, false, ErrProjectionFence
	}
	if intent.Fingerprint != existing.Fingerprint {
		updated := changedProjectionIntent(existing, intent)
		if existing.DeliveryState == ProjectionNeedsReview && existing.RetryReason == projectionRetryOwnerEffectUnknown {
			// A newer desired status cannot resolve whether an old remote mutation
			// is still pending at GitHub. Keep the module parked until a reviewed
			// recovery explicitly reconciles that effect.
			updated.DeliveryState = ProjectionNeedsReview
			updated.LastErrorClass = ProjectionErrorNeedsReview
			updated.RetryReason = projectionRetryOwnerEffectUnknown
		}
		return updated, true, nil
	}
	if intent.Generation == existing.Generation {
		if !reflect.DeepEqual(intent.NeutralProjection, existing.NeutralProjection) {
			return ProjectionIntentRecord{}, false, ErrProjectionFence
		}
		if intent.DiscoveryFingerprint != existing.DiscoveryFingerprint || intent.LifecycleFingerprint != existing.LifecycleFingerprint {
			existing.DiscoveryFingerprint = intent.DiscoveryFingerprint
			existing.LifecycleFingerprint = intent.LifecycleFingerprint
			return existing, true, nil
		}
		return existing, false, nil
	}
	return newerEquivalentProjectionIntent(existing, intent), true, nil
}

func changedProjectionIntent(existing, intent ProjectionIntentRecord) ProjectionIntentRecord {
	intent.DeliveryState = ProjectionPending
	intent.Attempt = existing.Attempt
	intent.Fence = existing.Fence
	intent.LastAcknowledgedFingerprint = existing.LastAcknowledgedFingerprint
	intent.ClaimID = ""
	intent.LeaseOwner = ""
	intent.OwnerRunID = 0
	intent.OwnerRunAttempt = 0
	intent.LeaseExpiresAt = time.Time{}
	intent.LeaseDuration = 0
	intent.SnapshotCursors = nil
	intent.RetryAt = time.Time{}
	intent.LastErrorClass = ""
	intent.RetryReason, intent.RetryRequest = "", nil
	if existing.DeliveryState == ProjectionProjecting {
		// A changed desired status fences the old callback, but it does not prove
		// that the old process or its remote request has stopped. Keep the module
		// parked until owner termination and effect reconciliation are verified.
		intent.DeliveryState = ProjectionProjecting
		intent.ClaimID = existing.ClaimID
		intent.LeaseOwner = existing.LeaseOwner
		intent.OwnerRunID = existing.OwnerRunID
		intent.OwnerRunAttempt = existing.OwnerRunAttempt
		intent.LeaseExpiresAt = existing.LeaseExpiresAt
		intent.LeaseDuration = existing.LeaseDuration
		intent.SnapshotCursors = existing.SnapshotCursors
		intent.Budget = existing.Budget
	}
	return intent
}

func newerEquivalentProjectionIntent(existing, intent ProjectionIntentRecord) ProjectionIntentRecord {
	neutralChanged := !reflect.DeepEqual(existing.NeutralProjection, intent.NeutralProjection)
	intent.DeliveryState = existing.DeliveryState
	intent.Attempt = existing.Attempt
	intent.LastErrorClass = existing.LastErrorClass
	intent.LastAcknowledgedFingerprint = existing.LastAcknowledgedFingerprint
	intent.RetryAt = existing.RetryAt
	intent.RetryReason, intent.RetryRequest = existing.RetryReason, existing.RetryRequest
	intent.Fence = existing.Fence + 1
	if existing.DeliveryState == ProjectionProjecting {
		// Keep ownership visible while invalidating its fence. Expiry is not
		// proof that an in-flight GitHub write has stopped.
		intent.ClaimID = existing.ClaimID
		intent.LeaseOwner = existing.LeaseOwner
		intent.OwnerRunID = existing.OwnerRunID
		intent.OwnerRunAttempt = existing.OwnerRunAttempt
		intent.LeaseExpiresAt = existing.LeaseExpiresAt
		intent.LeaseDuration = existing.LeaseDuration
		intent.SnapshotCursors = existing.SnapshotCursors
		intent.Budget = existing.Budget
	}
	ownerEffectUnknown := existing.DeliveryState == ProjectionNeedsReview && existing.RetryReason == projectionRetryOwnerEffectUnknown
	if ownerEffectUnknown {
		// An older acknowledgement cannot settle an effect that may still arrive
		// after its owner terminated, even when desired content is unchanged.
		intent.DeliveryState = ProjectionNeedsReview
		intent.LastErrorClass = ProjectionErrorNeedsReview
		intent.RetryReason = projectionRetryOwnerEffectUnknown
	} else if neutralChanged && existing.DeliveryState == ProjectionProjected {
		intent.DeliveryState = ProjectionPending
		intent.LeaseOwner = ""
		intent.OwnerRunID = 0
		intent.OwnerRunAttempt = 0
		intent.LeaseExpiresAt = time.Time{}
	} else if existing.LastAcknowledgedFingerprint == intent.Fingerprint && len(intent.PendingEvidence) == 0 {
		intent.DeliveryState = ProjectionProjected
		intent.LeaseOwner = ""
		intent.OwnerRunID = 0
		intent.OwnerRunAttempt = 0
		intent.LeaseExpiresAt = time.Time{}
	}
	if len(intent.PendingEvidence) > 0 && intent.DeliveryState == ProjectionProjected {
		intent.DeliveryState = ProjectionPending
	}
	return intent
}

// ClaimProjectionBatch claims an ordered, bounded set of due work in one CAS.
// Empty queues do not write state, allowing callers to make zero API calls.
func ClaimProjectionBatch(ctx context.Context, coordinator state.TransactionCoordinator, runID string, now time.Time, lease time.Duration, limit int) (ProjectionBatchClaim, error) {
	return claimProjectionBatch(ctx, coordinator, ProjectionClaimOptions{
		RunID: runID, Now: now, Lease: lease, Limit: limit,
	}, nil)
}

// ClaimProjectionBatchForModules limits one claim to the supplied module IDs.
// Other due intents remain untouched for their own targeted run.
func ClaimProjectionBatchForModules(ctx context.Context, coordinator state.TransactionCoordinator, options ProjectionClaimOptions, moduleIDs []string) (ProjectionBatchClaim, error) {
	if len(moduleIDs) == 0 {
		return ProjectionBatchClaim{}, fmt.Errorf("target module IDs are required for a scoped projection claim")
	}
	selected := make(map[string]struct{}, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		if !projectionModuleIDPattern.MatchString(moduleID) {
			return ProjectionBatchClaim{}, fmt.Errorf("invalid projection module ID %q", moduleID)
		}
		if _, duplicate := selected[moduleID]; duplicate {
			return ProjectionBatchClaim{}, fmt.Errorf("duplicate projection module ID %q", moduleID)
		}
		selected[moduleID] = struct{}{}
	}
	return claimProjectionBatch(ctx, coordinator, options, selected)
}

// DueProjectionIntents returns the current due count for an exact module set.
// Callers use it before reading GitHub's API budget so an empty run remains a
// state-only no-op.
func DueProjectionIntents(ctx context.Context, coordinator state.TransactionCoordinator, now time.Time, moduleIDs []string) (int, error) {
	if coordinator == nil || now.IsZero() || len(moduleIDs) == 0 {
		return 0, fmt.Errorf("projection due check requires state, time, and module IDs")
	}
	selected := make(map[string]struct{}, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		if !projectionModuleIDPattern.MatchString(moduleID) {
			return 0, fmt.Errorf("invalid projection module ID %q", moduleID)
		}
		if _, duplicate := selected[moduleID]; duplicate {
			return 0, fmt.Errorf("duplicate projection module ID %q", moduleID)
		}
		selected[moduleID] = struct{}{}
	}
	reader, ok := coordinator.(state.GitReader)
	if !ok {
		return 0, fmt.Errorf("projection due check requires a verified state reader")
	}
	_, snapshot, err := reader.Read(ctx)
	if err != nil {
		return 0, err
	}
	intents, err := readProjectionIntents(snapshot)
	if err != nil {
		return 0, err
	}
	due := 0
	for _, intent := range intents {
		if _, targeted := selected[intent.ModuleID]; targeted && projectionIntentDue(intent, now) {
			due++
		}
	}
	return due, nil
}

// DueProjectionModuleIDs derives the complete deterministic polling scope from
// one verified state snapshot. It performs no writes or GitHub calls.
func DueProjectionModuleIDs(snapshot state.Snapshot, now time.Time) ([]string, error) {
	if now.IsZero() {
		return nil, fmt.Errorf("projection polling time is required")
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	intents, err := readProjectionIntents(snapshot)
	if err != nil {
		return nil, err
	}
	dueIntents := make([]ProjectionIntentRecord, 0, len(intents))
	for _, intent := range intents {
		if err := ValidateProjectionAuthority(snapshot, intent); err != nil {
			return nil, fmt.Errorf("validate due projection authority for %s: %w", intent.ModuleID, err)
		}
		if projectionIntentDue(intent, now) {
			dueIntents = append(dueIntents, intent)
		}
	}
	sortProjectionIntents(dueIntents)
	moduleIDs := make([]string, 0, len(dueIntents))
	for _, intent := range dueIntents {
		moduleIDs = append(moduleIDs, intent.ModuleID)
	}
	return moduleIDs, nil
}

// DueProjectionIntentRecords returns fresh due delivery records for an exact
// candidate set while retaining the queue's deterministic priority order.
func DueProjectionIntentRecords(snapshot state.Snapshot, now time.Time, moduleIDs []string) ([]ProjectionIntentRecord, error) {
	if now.IsZero() || len(moduleIDs) == 0 {
		return nil, fmt.Errorf("projection due records require time and module IDs")
	}
	selected := make(map[string]struct{}, len(moduleIDs))
	for _, moduleID := range moduleIDs {
		if !projectionModuleIDPattern.MatchString(moduleID) {
			return nil, fmt.Errorf("invalid projection module ID %q", moduleID)
		}
		if _, duplicate := selected[moduleID]; duplicate {
			return nil, fmt.Errorf("duplicate projection module ID %q", moduleID)
		}
		selected[moduleID] = struct{}{}
	}
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	intents, err := readProjectionIntents(snapshot)
	if err != nil {
		return nil, err
	}
	due := make([]ProjectionIntentRecord, 0, len(selected))
	for _, intent := range intents {
		if _, targeted := selected[intent.ModuleID]; !targeted {
			continue
		}
		if err := ValidateProjectionAuthority(snapshot, intent); err != nil {
			return nil, fmt.Errorf("validate due projection authority for %s: %w", intent.ModuleID, err)
		}
		if projectionIntentDue(intent, now) {
			due = append(due, intent)
		}
	}
	sortProjectionIntents(due)
	return due, nil
}

// ExpiredProjectionOwners returns only claims with an exact persisted Actions
// attempt identity. Older or incomplete records remain parked for review.
func ExpiredProjectionOwners(snapshot state.Snapshot, now time.Time) ([]ProjectionOwner, error) {
	if now.IsZero() {
		return nil, fmt.Errorf("projection recovery time is required")
	}
	intents, err := readProjectionIntents(snapshot)
	if err != nil {
		return nil, err
	}
	owners := make([]ProjectionOwner, 0)
	for _, intent := range intents {
		if intent.DeliveryState != ProjectionProjecting || now.Before(intent.LeaseExpiresAt) {
			continue
		}
		if intent.OwnerRunID == 0 || intent.OwnerRunAttempt == 0 {
			continue
		}
		owners = append(owners, ProjectionOwner{ModuleID: intent.ModuleID, ClaimID: intent.ClaimID, Fence: intent.Fence,
			RunID: intent.OwnerRunID, RunAttempt: intent.OwnerRunAttempt, LeaseExpiresAt: intent.LeaseExpiresAt})
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].ModuleID < owners[j].ModuleID })
	return owners, nil
}

// RecoverProjectionOwner fences a terminated owner but keeps the module parked
// for review. A terminal Actions attempt does not prove that an accepted remote
// mutation has finished propagating, so automatic retry could duplicate an
// Issue or overwrite a newer value.
func RecoverProjectionOwner(ctx context.Context, coordinator state.TransactionCoordinator, owner ProjectionOwner, observation ProjectionOwnerObservation, now time.Time) (ProjectionIntentRecord, error) {
	if coordinator == nil || now.IsZero() || owner.ModuleID == "" || owner.ClaimID == "" || owner.Fence == 0 || owner.RunID == 0 || owner.RunAttempt == 0 {
		return ProjectionIntentRecord{}, fmt.Errorf("exact expired projection owner and recovery time are required")
	}
	if observation.Repository != "softwareone-public/avm-intelligence" || observation.WorkflowPath != ".github/workflows/factory-projection.yml" || observation.HeadBranch != "main" || observation.RunID != owner.RunID || observation.RunAttempt != owner.RunAttempt || observation.Status != "completed" || observation.CurrentRunAttempt < owner.RunAttempt || observation.CurrentRunStatus != "completed" || !validProjectionRunConclusion(observation.Conclusion) {
		return ProjectionIntentRecord{}, fmt.Errorf("Actions observation does not prove the exact projector attempt is terminal")
	}
	reader, ok := coordinator.(state.GitReader)
	if !ok {
		return ProjectionIntentRecord{}, fmt.Errorf("projection recovery requires a verified state reader")
	}
	_, snapshot, err := reader.Read(ctx)
	if err != nil {
		return ProjectionIntentRecord{}, err
	}
	intent, exists, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+owner.ModuleID+".json")
	if err != nil || !exists {
		return ProjectionIntentRecord{}, ErrProjectionFence
	}
	if intent.DeliveryState != ProjectionProjecting || intent.ClaimID != owner.ClaimID || intent.Fence != owner.Fence || intent.OwnerRunID != owner.RunID || intent.OwnerRunAttempt != owner.RunAttempt || now.Before(intent.LeaseExpiresAt) {
		return ProjectionIntentRecord{}, ErrProjectionFence
	}
	updated := intent
	if updated.Fence == math.MaxUint64 {
		return ProjectionIntentRecord{}, fmt.Errorf("projection fence is exhausted for %s", owner.ModuleID)
	}
	updated.Fence++
	updated.DeliveryState = ProjectionNeedsReview
	updated.ClaimID, updated.LeaseOwner = "", ""
	updated.OwnerRunID, updated.OwnerRunAttempt = 0, 0
	updated.LeaseExpiresAt, updated.LeaseDuration = time.Time{}, 0
	updated.SnapshotCursors = nil
	updated.RetryAt = time.Time{}
	updated.RetryReason = projectionRetryOwnerEffectUnknown
	updated.RetryRequest = nil
	updated.LastErrorClass = ProjectionErrorNeedsReview
	updated.UpdatedAt = now.UTC()
	if err := updated.Validate(); err != nil {
		return ProjectionIntentRecord{}, err
	}
	records := []projectionRecordEvent{{path: projectionDeliveryPrefix + owner.ModuleID + ".json", kind: "projection_delivery", value: updated}}
	events, err := projectionEvents(records)
	if err != nil {
		return ProjectionIntentRecord{}, err
	}
	operationID, err := identity.ID("projection-recovery", 1, struct {
		Owner       ProjectionOwner            `json:"owner"`
		Observation ProjectionOwnerObservation `json:"observation"`
		Updated     ProjectionIntentRecord     `json:"updated"`
	}{owner, observation, updated})
	if err != nil {
		return ProjectionIntentRecord{}, err
	}
	events = withProjectionOperationID(events, operationID)
	result, err := coordinator.RunTransaction(ctx, operationID, func(current state.Snapshot) (state.TransactionDecision, error) {
		currentIntent, found, readErr := readProjectionIntent(current, projectionDeliveryPrefix+owner.ModuleID+".json")
		if readErr != nil || !found || !reflect.DeepEqual(currentIntent, intent) {
			return state.TransactionDecision{}, ErrProjectionFence
		}
		return state.TransactionDecision{Events: events, Result: updated, Candidate: state.CandidateOptions{Subject: "state(projection): park terminated owner with unresolved effect", Provenance: state.GitProvenance{Operation: projectionOperation, Event: operationID, Module: owner.ModuleID}}}, nil
	}, func(current state.Snapshot) (state.TransactionResolution, error) {
		currentIntent, found, readErr := readProjectionIntent(current, projectionDeliveryPrefix+owner.ModuleID+".json")
		if readErr != nil || !found || currentIntent.Fence != updated.Fence || currentIntent.ClaimID != "" {
			return state.TransactionResolution{}, ErrProjectionFence
		}
		return state.TransactionResolution{Found: true, Value: currentIntent}, nil
	})
	if err != nil {
		return ProjectionIntentRecord{}, err
	}
	resolved, ok := result.Value.(ProjectionIntentRecord)
	if !ok {
		return ProjectionIntentRecord{}, fmt.Errorf("projection recovery result has an invalid type")
	}
	return resolved, nil
}

func validProjectionRunConclusion(conclusion string) bool {
	switch conclusion {
	case "success", "failure", "cancelled", "skipped", "timed_out", "action_required", "neutral", "stale":
		return true
	default:
		return false
	}
}

func claimProjectionBatch(ctx context.Context, coordinator state.TransactionCoordinator, options ProjectionClaimOptions, selected map[string]struct{}) (ProjectionBatchClaim, error) {
	if coordinator == nil || options.RunID == "" {
		return ProjectionBatchClaim{}, fmt.Errorf("projection transaction coordinator and run ID are required")
	}
	if err := validateProjectionClaim(options.RunID, options.Now, options.Lease, options.Limit); err != nil {
		return ProjectionBatchClaim{}, err
	}
	reader, ok := coordinator.(state.GitReader)
	if !ok {
		return ProjectionBatchClaim{}, fmt.Errorf("projection claim requires a verified state reader")
	}
	head, snapshot, err := reader.Read(ctx)
	if err != nil {
		return ProjectionBatchClaim{}, err
	}
	claim, preparedEvents, err := buildProjectionBatchClaim(snapshot, head, options, selected)
	if err != nil {
		return ProjectionBatchClaim{}, err
	}
	if len(preparedEvents) == 0 {
		claim.Head = head
		return claim, nil
	}
	events, err := projectionEvents(preparedEvents)
	if err != nil {
		return ProjectionBatchClaim{}, err
	}
	operationID, err := projectionTransactionID("claim", preparedEvents)
	if err != nil {
		return ProjectionBatchClaim{}, err
	}
	events = withProjectionOperationID(events, operationID)
	result, err := coordinator.RunTransaction(ctx, operationID, func(current state.Snapshot) (state.TransactionDecision, error) {
		fresh, _, buildErr := buildProjectionBatchClaim(current, head, options, selected)
		if buildErr != nil {
			return state.TransactionDecision{}, buildErr
		}
		if !reflect.DeepEqual(fresh.Batch, claim.Batch) || !reflect.DeepEqual(fresh.Intents, claim.Intents) {
			return state.TransactionDecision{}, state.ErrHeadMoved
		}
		return state.TransactionDecision{Events: events, Result: claim, Candidate: state.CandidateOptions{Subject: "state(projection): claim module deliveries", Provenance: state.GitProvenance{Operation: projectionOperation, Event: operationID}}}, nil
	}, func(current state.Snapshot) (state.TransactionResolution, error) {
		intents := make([]ProjectionIntentRecord, 0, len(claim.Batch.ModuleIDs))
		bindings := make(map[string]ProjectionBinding, len(claim.Batch.ModuleIDs))
		for _, moduleID := range claim.Batch.ModuleIDs {
			intent, exists, intentErr := readProjectionIntent(current, projectionDeliveryPrefix+moduleID+".json")
			if intentErr != nil || !exists || intent.ClaimID != claim.Batch.ID || intent.LeaseOwner != claim.Batch.RunID {
				return state.TransactionResolution{}, ErrProjectionFence
			}
			intents = append(intents, intent)
			binding, exists, bindingErr := readProjectionBinding(current, moduleID)
			if bindingErr != nil {
				return state.TransactionResolution{}, bindingErr
			}
			if exists {
				bindings[moduleID] = binding
			}
		}
		return state.TransactionResolution{Found: true, Value: ProjectionBatchClaim{Batch: claim.Batch, Intents: intents, Bindings: bindings}}, nil
	})
	if err != nil {
		return ProjectionBatchClaim{}, err
	}
	resolved, ok := result.Value.(ProjectionBatchClaim)
	if !ok {
		return ProjectionBatchClaim{}, fmt.Errorf("projection claim result has an invalid type")
	}
	resolved.Head, resolved.Changed = result.Head, result.Changed
	return resolved, nil
}

func validateProjectionClaim(runID string, now time.Time, lease time.Duration, limit int) error {
	if strings.TrimSpace(runID) == "" || now.IsZero() || lease <= 0 || limit != 1 {
		return fmt.Errorf("projection run and lease are required for a single-module claim")
	}
	return nil
}

func buildProjectionBatchClaim(snapshot state.Snapshot, head string, options ProjectionClaimOptions, selected map[string]struct{}) (ProjectionBatchClaim, []projectionRecordEvent, error) {
	runID, now, lease, limit, budget := options.RunID, options.Now, options.Lease, options.Limit, options.Budget
	intents, err := readProjectionIntents(snapshot)
	if err != nil {
		return ProjectionBatchClaim{}, nil, err
	}
	eligible := make([]ProjectionIntentRecord, 0, len(intents))
	for _, intent := range intents {
		if _, targeted := selected[intent.ModuleID]; (selected == nil || targeted) && projectionIntentDue(intent, now) {
			if err := validateProjectionLifecycle(snapshot, intent); err != nil {
				return ProjectionBatchClaim{}, nil, err
			}
			eligible = append(eligible, intent)
		}
	}
	if len(eligible) == 0 {
		return ProjectionBatchClaim{Head: head, Intents: []ProjectionIntentRecord{}}, nil, nil
	}
	sortProjectionIntents(eligible)
	if len(eligible) > limit {
		eligible = eligible[:limit]
	}
	moduleIDs := make([]string, 0, len(eligible))
	for _, intent := range eligible {
		moduleIDs = append(moduleIDs, intent.ModuleID)
	}
	batchID, err := identity.ID("projection-claim", 1, struct {
		RunID   string    `json:"run_id"`
		Head    string    `json:"head"`
		Modules []string  `json:"modules"`
		At      time.Time `json:"at"`
	}{runID, head, moduleIDs, now.UTC()})
	if err != nil {
		return ProjectionBatchClaim{}, nil, err
	}
	batch := ProjectionBatch{
		ID: batchID, RunID: runID, OwnerRunID: options.OwnerRunID, OwnerRunAttempt: options.OwnerRunAttempt,
		Status: ProjectionBatchRunning, ModuleIDs: moduleIDs, Budget: budget, LeaseDuration: lease,
		LeaseExpiresAt: now.Add(lease).UTC(), CreatedAt: now.UTC(),
	}
	claimed, events, err := claimProjectionIntents(eligible, batch, runID, now)
	if err != nil {
		return ProjectionBatchClaim{}, nil, err
	}
	bindings := make(map[string]ProjectionBinding)
	for _, intent := range claimed {
		binding, exists, bindingErr := readProjectionBinding(snapshot, intent.ModuleID)
		if bindingErr != nil {
			return ProjectionBatchClaim{}, nil, bindingErr
		}
		if exists {
			bindings[intent.ModuleID] = binding
		}
	}
	return ProjectionBatchClaim{Batch: batch, Intents: claimed, Bindings: bindings}, events, nil
}

func claimProjectionIntents(eligible []ProjectionIntentRecord, batch ProjectionBatch, runID string, now time.Time) ([]ProjectionIntentRecord, []projectionRecordEvent, error) {
	claimed := make([]ProjectionIntentRecord, 0, len(eligible))
	events := make([]projectionRecordEvent, 0, len(eligible))
	for _, intent := range eligible {
		if intent.Fence == math.MaxUint64 || intent.Attempt == math.MaxUint64 {
			return nil, nil, fmt.Errorf("projection fence or attempt is exhausted for %s", intent.ModuleID)
		}
		intent.DeliveryState = ProjectionProjecting
		intent.Attempt++
		intent.Fence++
		intent.ClaimID = batch.ID
		intent.LeaseOwner = runID
		intent.OwnerRunID = batch.OwnerRunID
		intent.OwnerRunAttempt = batch.OwnerRunAttempt
		intent.LeaseExpiresAt = batch.LeaseExpiresAt
		intent.LeaseDuration = batch.LeaseDuration
		intent.Budget = batch.Budget
		intent.SnapshotCursors = nil
		intent.UpdatedAt = now.UTC()
		if err := intent.Validate(); err != nil {
			return nil, nil, err
		}
		claimed = append(claimed, intent)
		events = append(events, projectionRecordEvent{path: projectionDeliveryPrefix + intent.ModuleID + ".json", kind: "projection_delivery", value: intent})
	}
	return claimed, events, nil
}

// RecordProjectionOutcome atomically acknowledges one module and advances the
// batch cursor, preserving partial success for the next bounded flush.
func RecordProjectionOutcome(ctx context.Context, coordinator state.TransactionCoordinator, input ProjectionOutcomeInput) (ProjectionOutcomeResult, error) {
	ctx = ratelimit.CheckpointContext(ctx)
	if coordinator == nil || strings.TrimSpace(input.ClaimID) == "" || strings.TrimSpace(input.ModuleID) == "" || strings.TrimSpace(input.RunID) == "" || input.Generation == 0 || input.Fence == 0 || input.Outcome.CompletedAt.IsZero() {
		return ProjectionOutcomeResult{}, fmt.Errorf("projection outcome ownership and timestamp are required")
	}
	reader, ok := coordinator.(state.GitReader)
	if !ok {
		return ProjectionOutcomeResult{}, fmt.Errorf("projection outcome requires a verified state reader")
	}
	_, snapshot, err := reader.Read(ctx)
	if err != nil {
		return ProjectionOutcomeResult{}, err
	}
	batch, intent, err := claimedProjectionTarget(snapshot, input)
	if err != nil {
		return ProjectionOutcomeResult{}, err
	}
	if err = applyProjectionOutcome(&intent, input.Outcome); err != nil {
		return ProjectionOutcomeResult{}, err
	}
	if err = applyEvidenceOutcome(&intent, input.Outcome); err != nil {
		return ProjectionOutcomeResult{}, err
	}
	intent.LeaseOwner = ""
	intent.OwnerRunID = 0
	intent.OwnerRunAttempt = 0
	intent.LeaseExpiresAt = time.Time{}
	intent.UpdatedAt = input.Outcome.CompletedAt.UTC()
	intent.Budget = input.Outcome.Budget
	batch.Status = ProjectionBatchComplete
	batch.RunID = input.RunID
	batch.LeaseExpiresAt = time.Time{}
	batch.Budget = input.Outcome.Budget
	events := []projectionRecordEvent{{path: projectionDeliveryPrefix + input.ModuleID + ".json", kind: "projection_delivery", value: intent}}
	if input.Outcome.Binding != nil {
		binding := *input.Outcome.Binding
		if err = binding.Validate(input.ModuleID, input.Generation); err != nil {
			return ProjectionOutcomeResult{}, err
		}
		events = append(events, projectionRecordEvent{path: projectionBindingPrefix + input.ModuleID + ".json", kind: "projection_binding", value: binding})
	}
	if input.Outcome.Success && intent.NeutralProjection != nil {
		acknowledgement, ackErr := neutralProjectionAcknowledgement(snapshot, *intent.NeutralProjection, input.Outcome.CompletedAt)
		if ackErr != nil {
			return ProjectionOutcomeResult{}, ackErr
		}
		ackPath, pathErr := identity.StateProjectionAcknowledgementPath(input.ModuleID, intent.NeutralProjection.IntentID)
		if pathErr != nil {
			return ProjectionOutcomeResult{}, pathErr
		}
		events = append(events, projectionRecordEvent{path: ackPath, kind: "projection_acknowledgement", value: acknowledgement})
	}
	preparedEvents, err := projectionEvents(events)
	if err != nil {
		return ProjectionOutcomeResult{}, err
	}
	operationID, err := projectionTransactionID("outcome", events)
	if err != nil {
		return ProjectionOutcomeResult{}, err
	}
	preparedEvents = withProjectionOperationID(preparedEvents, operationID)
	result, err := coordinator.RunTransaction(ctx, operationID, func(current state.Snapshot) (state.TransactionDecision, error) {
		if replayed, exists, replayErr := projectionOutcomeReplay(current, input); replayErr != nil {
			return state.TransactionDecision{}, replayErr
		} else if exists {
			return state.TransactionDecision{Noop: true, Result: replayed}, nil
		}
		_, currentIntent, targetErr := claimedProjectionTarget(current, input)
		if targetErr != nil {
			return state.TransactionDecision{}, targetErr
		}
		if currentIntent.ClaimID != batch.ID || currentIntent.Fence != input.Fence || currentIntent.Generation != input.Generation {
			return state.TransactionDecision{}, ErrProjectionFence
		}
		if input.Outcome.Success && currentIntent.NeutralProjection != nil {
			if _, ackErr := neutralProjectionAcknowledgement(current, *currentIntent.NeutralProjection, input.Outcome.CompletedAt); ackErr != nil {
				return state.TransactionDecision{}, ackErr
			}
		}
		return state.TransactionDecision{Events: preparedEvents, Result: ProjectionOutcomeResult{Batch: batch, Intent: intent}, Candidate: state.CandidateOptions{Subject: "state(projection): record delivery outcome", Provenance: state.GitProvenance{Operation: projectionOperation, Event: operationID, Module: input.ModuleID}}}, nil
	}, func(current state.Snapshot) (state.TransactionResolution, error) {
		currentIntent, exists, err := readProjectionIntent(current, projectionDeliveryPrefix+input.ModuleID+".json")
		if err != nil || !exists || currentIntent.ClaimID != input.ClaimID {
			return state.TransactionResolution{}, ErrProjectionFence
		}
		return state.TransactionResolution{Found: true, Value: ProjectionOutcomeResult{Batch: claimDescriptor(currentIntent), Intent: currentIntent}}, nil
	})
	if err != nil {
		return ProjectionOutcomeResult{}, err
	}
	resolved, ok := result.Value.(ProjectionOutcomeResult)
	if !ok {
		return ProjectionOutcomeResult{}, fmt.Errorf("projection outcome result has an invalid type")
	}
	resolved.Head = result.Head
	return resolved, nil
}

func neutralProjectionAcknowledgement(snapshot state.Snapshot, expected projection.ProjectionIdentity, acknowledgedAt time.Time) (projection.Acknowledgement, error) {
	if err := validateNeutralProjectionIdentity(snapshot, expected); err != nil {
		return projection.Acknowledgement{}, err
	}
	acknowledgement := projection.Acknowledgement{
		SchemaVersion: projection.SchemaVersion, Projection: expected,
		Status: projection.AcknowledgementDone, AcknowledgedAt: acknowledgedAt.UTC(),
	}
	if err := acknowledgement.Validate(expected, acknowledgedAt.UTC()); err != nil {
		return projection.Acknowledgement{}, err
	}
	return acknowledgement, nil
}

func validateNeutralProjectionIdentity(snapshot state.Snapshot, expected projection.ProjectionIdentity) error {
	intentPath, err := identity.StateProjectionIntentPath(expected.ModuleID)
	if err != nil {
		return err
	}
	record, exists := snapshot.Records[intentPath]
	if !exists || record.Kind != "projection_intent" || record.SchemaVersion != projection.SchemaVersion {
		return ErrProjectionFence
	}
	var intent projection.DesiredIntent
	if json.Unmarshal(record.Data, &intent) != nil || intent.ValidatePath(intentPath) != nil || expected.Validate(intent) != nil {
		return ErrProjectionFence
	}
	if len(intent.EvidenceEffects) != 0 {
		return ErrProjectionFence
	}
	if intent.Status == "done" {
		fields := make(map[string]string, len(intent.OwnedFields))
		for _, field := range intent.OwnedFields {
			fields[field.Name] = field.Value
		}
		acknowledgement, err := state.VerifyDoneModule(snapshot, expected.ModuleID, fields["avm_release_version"], fields["published_commit"])
		if err != nil || fields["published_commit"] == "" || acknowledgement.AcceptedCommit != fields["published_commit"] {
			return ErrProjectionFence
		}
	}
	return nil
}

func projectionOutcomeReplay(snapshot state.Snapshot, input ProjectionOutcomeInput) (ProjectionOutcomeResult, bool, error) {
	intent, exists, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+input.ModuleID+".json")
	if err != nil || !exists {
		return ProjectionOutcomeResult{}, false, nil
	}
	if intent.Generation != input.Generation || intent.Fence != input.Fence || intent.ClaimID != input.ClaimID || intent.DeliveryState == ProjectionProjecting {
		return ProjectionOutcomeResult{}, false, nil
	}
	if input.Outcome.Success != (intent.LastAcknowledgedFingerprint == intent.Fingerprint) || (input.Outcome.Success && (input.Outcome.Binding == nil || intent.DeliveryState != ProjectionProjected)) {
		return ProjectionOutcomeResult{}, false, state.ErrTransactionIdentityConflict
	}
	if input.Outcome.Success && intent.NeutralProjection != nil {
		path, pathErr := identity.StateProjectionAcknowledgementPath(input.ModuleID, intent.NeutralProjection.IntentID)
		if pathErr != nil {
			return ProjectionOutcomeResult{}, false, pathErr
		}
		record, exists := snapshot.Records[path]
		var acknowledgement projection.Acknowledgement
		if !exists || record.Kind != "projection_acknowledgement" || record.SchemaVersion != projection.SchemaVersion || json.Unmarshal(record.Data, &acknowledgement) != nil || acknowledgement.Validate(*intent.NeutralProjection, input.Outcome.CompletedAt.UTC()) != nil {
			return ProjectionOutcomeResult{}, false, state.ErrTransactionIdentityConflict
		}
	}
	claim := claimDescriptor(intent)
	claim.RunID, claim.Status = input.RunID, ProjectionBatchComplete
	return ProjectionOutcomeResult{Batch: claim, Intent: intent}, true, nil
}

func claimedProjectionTarget(snapshot state.Snapshot, input ProjectionOutcomeInput) (ProjectionBatch, ProjectionIntentRecord, error) {
	intent, exists, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+input.ModuleID+".json")
	if err != nil || !exists {
		return ProjectionBatch{}, ProjectionIntentRecord{}, ErrProjectionFence
	}
	if intent.Generation != input.Generation || intent.Fence != input.Fence || intent.ClaimID != input.ClaimID || intent.LeaseOwner != input.RunID || intent.DeliveryState != ProjectionProjecting || !input.Outcome.CompletedAt.Before(intent.LeaseExpiresAt) {
		return ProjectionBatch{}, ProjectionIntentRecord{}, ErrProjectionFence
	}
	if err := validateProjectionLifecycle(snapshot, intent); err != nil {
		return ProjectionBatch{}, ProjectionIntentRecord{}, err
	}
	return claimDescriptor(intent), intent, nil
}

func claimDescriptor(intent ProjectionIntentRecord) ProjectionBatch {
	status := ProjectionBatchRunning
	if intent.DeliveryState != ProjectionProjecting {
		status = ProjectionBatchComplete
	}
	return ProjectionBatch{ID: intent.ClaimID, RunID: intent.LeaseOwner, Status: status,
		ModuleIDs: []string{intent.ModuleID}, Budget: intent.Budget, LeaseDuration: intent.LeaseDuration,
		LeaseExpiresAt: intent.LeaseExpiresAt, CreatedAt: intent.UpdatedAt}
}

// CheckpointProjectionSnapshot persists remote pagination cursors and the
// latest separate API budgets before resuming a later read or mutation phase.
func CheckpointProjectionSnapshot(ctx context.Context, coordinator state.TransactionCoordinator, checkpoint ProjectionSnapshotCheckpoint) (string, error) {
	if coordinator == nil || checkpoint.ClaimID == "" || checkpoint.RunID == "" || checkpoint.ModuleID == "" || checkpoint.CheckpointedAt.IsZero() {
		return "", fmt.Errorf("module projection claim and checkpoint time are required")
	}
	reader, ok := coordinator.(state.GitReader)
	if !ok {
		return "", fmt.Errorf("projection checkpoint requires a verified state reader")
	}
	_, snapshot, err := reader.Read(ctx)
	if err != nil {
		return "", err
	}
	path := projectionDeliveryPrefix + checkpoint.ModuleID + ".json"
	intent, exists, err := readProjectionIntent(snapshot, path)
	if err != nil || !exists || intent.DeliveryState != ProjectionProjecting || intent.ClaimID != checkpoint.ClaimID || intent.LeaseOwner != checkpoint.RunID || intent.LeaseDuration <= 0 || !checkpoint.CheckpointedAt.Before(intent.LeaseExpiresAt) {
		return "", ErrProjectionFence
	}
	if intent.SnapshotCursors == nil {
		intent.SnapshotCursors = map[string]string{}
	}
	for name, cursor := range checkpoint.Cursors {
		if strings.TrimSpace(name) == "" {
			return "", fmt.Errorf("projection snapshot cursor name is required")
		}
		intent.SnapshotCursors[name] = cursor
	}
	intent.Budget = checkpoint.Budget
	intent.LeaseExpiresAt = checkpoint.CheckpointedAt.Add(intent.LeaseDuration).UTC()
	intent.UpdatedAt = checkpoint.CheckpointedAt.UTC()
	events, err := projectionEvents([]projectionRecordEvent{{path: path, kind: "projection_delivery", value: intent}})
	if err != nil {
		return "", err
	}
	operationID, err := projectionTransactionID("checkpoint", []projectionRecordEvent{{path: path, kind: "projection_delivery", value: intent}})
	if err != nil {
		return "", err
	}
	events = withProjectionOperationID(events, operationID)
	result, err := coordinator.RunTransaction(ctx, operationID, func(current state.Snapshot) (state.TransactionDecision, error) {
		currentIntent, currentExists, currentErr := readProjectionIntent(current, path)
		if currentErr != nil || !currentExists || currentIntent.DeliveryState != ProjectionProjecting || currentIntent.ClaimID != checkpoint.ClaimID || currentIntent.LeaseOwner != checkpoint.RunID || currentIntent.Fence != intent.Fence || currentIntent.Generation != intent.Generation || !checkpoint.CheckpointedAt.Before(currentIntent.LeaseExpiresAt) {
			return state.TransactionDecision{}, ErrProjectionFence
		}
		return state.TransactionDecision{Events: events, Result: intent, Candidate: state.CandidateOptions{Subject: "state(projection): checkpoint module remote snapshot", Provenance: state.GitProvenance{Operation: projectionOperation, Event: operationID, Module: checkpoint.ModuleID}}}, nil
	}, func(current state.Snapshot) (state.TransactionResolution, error) {
		currentIntent, currentExists, currentErr := readProjectionIntent(current, path)
		if currentErr != nil || !currentExists || currentIntent.ClaimID != checkpoint.ClaimID || currentIntent.Fence != intent.Fence || currentIntent.Generation != intent.Generation {
			return state.TransactionResolution{}, ErrProjectionFence
		}
		return state.TransactionResolution{Found: true, Value: currentIntent}, nil
	})
	if err != nil {
		return "", err
	}
	return result.Head, nil
}

// ProjectClaimedBatch performs no network call for an empty local batch.
// Progress is persisted separately after each target via RecordProjectionOutcome.
func (client Client) ProjectClaimedBatch(ctx context.Context, backend state.Backend, coordinator state.TransactionCoordinator, claim ProjectionBatchClaim, options ProjectionRunOptions) (ProjectionRunResult, error) {
	result := ProjectionRunResult{Head: claim.Head, Batch: claim.Batch, Errors: []string{}}
	if len(claim.Batch.ModuleIDs) == 0 {
		return result, nil
	}
	if client.Budget == nil && options.InitialBudget != nil {
		client.Budget = options.InitialBudget
	}
	for _, intent := range claim.Intents {
		head, snapshot, err := state.ReadSnapshot(ctx, backend)
		if err != nil {
			return result, err
		}
		if head != claim.Head {
			return result, state.ErrHeadMoved
		}
		if err := validateProjectionLifecycle(snapshot, intent); err != nil {
			return result, err
		}
		if intent.NeutralProjection != nil {
			if err := validateNeutralProjectionIdentity(snapshot, *intent.NeutralProjection); err != nil {
				return result, err
			}
		}
	}
	var err error
	client, err = client.prepareProjectionBatch(ctx, coordinator, claim, options, &result)
	if err != nil {
		return result, err
	}
	for position, intent := range claim.Intents {
		if position >= len(claim.Batch.ModuleIDs) || intent.ModuleID != claim.Batch.ModuleIDs[position] {
			return result, fmt.Errorf("projection claim targets do not match the run-local order")
		}
		moduleCtx, finish := progress.Start(ctx, "projection", "module_delivery", slog.String("module_id", intent.ModuleID), slog.Int("position", position+1), slog.Int("total", len(claim.Intents)), slog.String("run_id", claim.Batch.RunID))
		checkpoint, projectErr, checkpointErr := client.projectClaimedIntent(moduleCtx, backend, coordinator, result.Head, claim, options, intent)
		finish(projectErr == nil && checkpointErr == nil)
		if checkpointErr != nil {
			if projectErr != nil {
				result.Cause = errors.Join(result.Cause, projectErr)
				result.RetryAt = client.projectionResumeAt(time.Now().UTC(), options.RetryDelay)
				return result, errors.Join(checkpointErr, &ProjectionDeliveryError{Cause: projectErr, RetryAt: result.RetryAt})
			}
			return result, checkpointErr
		}
		result.Head = checkpoint.Head
		result.Batch = checkpoint.Batch
		if projectErr == nil {
			result.Completed++
		} else {
			progress.Logger(moduleCtx).WarnContext(moduleCtx, "projection module deferred", "reason", classifyProjectionError(projectErr))
			result.Errors = append(result.Errors, projectErr.Error())
			result.Cause = errors.Join(result.Cause, projectErr)
			result.RetryAt = checkpoint.Intent.RetryAt
		}
		if errors.Is(projectErr, ErrProjectionBudgetExhausted) {
			progress.Logger(ctx).WarnContext(ctx, "projection batch paused", "reason", classifyProjectionError(projectErr), "completed", result.Completed, "remaining", len(claim.Intents)-position-1, "retry_at", result.RetryAt)
			result.Partial = true
			return result, nil
		}
	}
	progress.Logger(ctx).InfoContext(ctx, "projection batch outcomes recorded", "completed", result.Completed, "failed", len(result.Errors), "total", len(claim.Intents))
	return result, nil
}

func (client Client) prepareProjectionBatch(ctx context.Context, coordinator state.TransactionCoordinator, claim ProjectionBatchClaim, options ProjectionRunOptions, result *ProjectionRunResult) (prepared Client, returnErr error) {
	ctx, finish := progress.Start(ctx, "projection", "batch_prepare", slog.String("run_id", claim.Batch.RunID), slog.Int("total", len(claim.Intents)))
	defer func() { finish(returnErr == nil) }()
	if err := validateProjectionRun(claim, options); err != nil {
		return client, err
	}
	if client.Budget == nil {
		progress.Logger(ctx).InfoContext(ctx, "projection API budget read started")
		budget, err := client.ReadProjectionBudget(ctx, options.BudgetPolicy)
		if err != nil {
			return client, err
		}
		client.Budget = budget
		progress.Logger(ctx).InfoContext(ctx, "projection API budget read completed")
	}
	if client.MetadataCache == nil {
		client.MetadataCache = &ProjectionMetadataCache{}
	}
	client.MetadataCache.setSnapshotCheckpoint(result.Head, func(ctx context.Context, name, cursor string) error {
		cursors := map[string]string{name: cursor}
		newHead, checkpointErr := CheckpointProjectionSnapshot(ctx, coordinator, ProjectionSnapshotCheckpoint{
			ClaimID: claim.Batch.ID, RunID: claim.Batch.RunID, ModuleID: claim.Intents[0].ModuleID, Cursors: cursors,
			Budget: client.budgetSnapshot(), CheckpointedAt: time.Now().UTC(),
		})
		if checkpointErr != nil {
			return checkpointErr
		}
		result.Head = newHead
		client.MetadataCache.updateStateHead(newHead)
		return nil
	})
	return client, nil
}

func validateProjectionRun(claim ProjectionBatchClaim, options ProjectionRunOptions) error {
	if options.Owner == "" || options.Repository == "" || options.ProjectNumber < 1 || options.RetryDelay <= 0 || len(claim.Intents) != 1 || len(claim.Batch.ModuleIDs) != 1 || claim.Intents[0].ModuleID != claim.Batch.ModuleIDs[0] {
		return fmt.Errorf("single-module projection claim and retry options are required")
	}
	return nil
}

func (client Client) projectClaimedIntent(ctx context.Context, backend state.Backend, coordinator state.TransactionCoordinator, expectedHead string, claim ProjectionBatchClaim, options ProjectionRunOptions, intent ProjectionIntentRecord) (ProjectionOutcomeResult, error, error) {
	currentHead, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return ProjectionOutcomeResult{}, nil, err
	}
	if currentHead != expectedHead {
		return ProjectionOutcomeResult{}, nil, state.ErrHeadMoved
	}
	current, exists, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+intent.ModuleID+".json")
	if err != nil || !exists || current.Generation != intent.Generation || current.Fence != intent.Fence || current.ClaimID != claim.Batch.ID || current.LeaseOwner != claim.Batch.RunID {
		return ProjectionOutcomeResult{}, nil, ErrProjectionFence
	}
	if err := validateProjectionLifecycle(snapshot, current); err != nil {
		return ProjectionOutcomeResult{}, nil, err
	}
	if current.NeutralProjection != nil {
		if err := validateNeutralProjectionIdentity(snapshot, *current.NeutralProjection); err != nil {
			return ProjectionOutcomeResult{}, nil, err
		}
	}
	previousMutationGuard := client.MutationGuard
	client.MutationGuard = func(writeCtx context.Context) error {
		if previousMutationGuard != nil {
			if err := previousMutationGuard(writeCtx); err != nil {
				return err
			}
		}
		head, latest, readErr := state.ReadSnapshot(writeCtx, backend)
		if readErr != nil {
			return readErr
		}
		owned, found, readErr := readProjectionIntent(latest, projectionDeliveryPrefix+intent.ModuleID+".json")
		if readErr != nil || !found || owned.Generation != intent.Generation || owned.Fence != intent.Fence || owned.ClaimID != claim.Batch.ID || owned.LeaseOwner != claim.Batch.RunID || !time.Now().Before(owned.LeaseExpiresAt) {
			return ErrProjectionFence
		}
		if err := validateProjectionLifecycle(latest, owned); err != nil {
			return err
		}
		if owned.NeutralProjection != nil {
			if err := validateNeutralProjectionIdentity(latest, *owned.NeutralProjection); err != nil {
				return err
			}
		}
		client.MetadataCache.updateStateHead(head)
		return nil
	}
	issue, projectErr := client.UpsertFleet(ctx, options.Owner, options.Repository, options.ProjectNumber, intent.Projection)
	outcome := ProjectionOutcome{EvidenceRunID: options.EvidenceRunID, EvidencePhase: options.EvidencePhase}
	if projectErr == nil {
		binding := claim.Bindings[intent.ModuleID]
		binding.SchemaVersion = projectionStateSchemaVersion
		binding.ModuleID = intent.ModuleID
		binding.IssueNumber = issue.Number
		binding.IssueNodeID = issue.NodeID
		binding.IssueURL = issue.URL
		if issue.ProjectItemID != "" {
			binding.ProjectItemID = issue.ProjectItemID
		}
		binding.Generation = intent.Generation
		outcome.DeliveredEvidence, projectErr = client.synchronizePendingEvidence(ctx, current, issue, options, &binding)
		binding.UpdatedAt = time.Now().UTC()
		outcome.Binding = &binding
	}
	completedAt := time.Now().UTC()
	outcome.CompletedAt, outcome.Budget = completedAt, client.budgetSnapshot()
	if projectErr == nil {
		outcome.Success = true
	} else {
		outcome.ErrorClass = classifyProjectionError(projectErr)
		if _, retryable := projectionRetryableErrors[outcome.ErrorClass]; retryable {
			outcome.RetryAt = client.projectionResumeAt(completedAt, options.RetryDelay)
			var deferred *ratelimit.DeferredError
			if errors.As(projectErr, &deferred) {
				outcome.RetryReason = deferred.Reason
				if deferred.ServerLimited {
					outcome.RetryReason = "server_retry_after"
				}
				request := deferred.Request
				if !deferred.ServerLimited && request.Resource != "" && request.Points > 0 {
					outcome.RetryRequest = &request
				}
			}
		}
	}
	cleanupCtx, cleanupCancel := ratelimit.CleanupContext(ctx)
	defer cleanupCancel()
	checkpoint, checkpointErr := RecordProjectionOutcome(cleanupCtx, coordinator, ProjectionOutcomeInput{
		ClaimID: claim.Batch.ID, ModuleID: intent.ModuleID, RunID: claim.Batch.RunID,
		Generation: intent.Generation, Fence: intent.Fence, Outcome: outcome,
	})
	if checkpointErr == nil {
		client.MetadataCache.updateStateHead(checkpoint.Head)
		progress.Logger(ctx).InfoContext(ctx, "projection outcome committed", "state_head", checkpoint.Head, "delivery_state", checkpoint.Intent.DeliveryState, "issue_number", issue.Number, "retry_at", checkpoint.Intent.RetryAt)
	}
	return checkpoint, projectErr, checkpointErr
}

// Validate checks one serialized delivery record independently of its writer.
func (intent ProjectionIntentRecord) Validate() error {
	switch intent.RetryReason {
	case "", "primary_reserve", "primary_exhausted", "local_hourly_mutations", "local_minute_mutations", "local_minute_points", "mutation_spacing", "server_retry_after", "owner_terminated", projectionRetryOwnerEffectUnknown:
	default:
		return fmt.Errorf("projection retry reason is invalid")
	}
	if intent.RetryRequest != nil {
		request := intent.RetryRequest
		if request.Points <= 0 || request.Refresh || request.Checkpoint || (request.Resource != "rest" && request.Resource != "graphql" && request.Resource != "search") {
			return fmt.Errorf("projection retry request is invalid")
		}
	}
	if err := validatePendingEvidence(intent); err != nil {
		return err
	}
	if err := validateDiscoveryLifecycleFingerprint(intent.DiscoveryFingerprint); err != nil {
		return err
	}
	if intent.LifecycleFingerprint != "" {
		if err := identity.ValidateRecordID("lifecycle-authority", intent.LifecycleFingerprint); err != nil {
			return fmt.Errorf("projection lifecycle authority is invalid")
		}
	}
	if intent.SchemaVersion != projectionStateSchemaVersion || intent.ModuleID != intent.Projection.ModuleID || !projectionModuleIDPattern.MatchString(intent.ModuleID) || intent.Generation == 0 || intent.Priority < ProjectionPriorityRetry || intent.Priority > ProjectionPriorityCleanup || intent.UpdatedAt.IsZero() || intent.DeliveryState == "" {
		return fmt.Errorf("projection intent identity is invalid")
	}
	if err := intent.Projection.Validate(); err != nil {
		return err
	}
	fingerprint, err := ProjectionFingerprint(intent.Projection)
	if err != nil || fingerprint != intent.Fingerprint {
		return fmt.Errorf("projection intent fingerprint does not match its desired projection")
	}
	switch intent.DeliveryState {
	case ProjectionPending, ProjectionProjecting, ProjectionProjected, ProjectionRetryable, ProjectionNeedsReview:
	default:
		return fmt.Errorf("projection delivery state %q is invalid", intent.DeliveryState)
	}
	if intent.DeliveryState == ProjectionProjecting && (intent.LeaseOwner == "" || intent.ClaimID == "" || intent.Fence == 0 || intent.LeaseExpiresAt.IsZero() || intent.LeaseDuration <= 0 || (intent.OwnerRunID == 0) != (intent.OwnerRunAttempt == 0)) {
		return fmt.Errorf("projecting intent is missing its fenced lease")
	}
	if intent.DeliveryState == ProjectionProjecting && intent.OwnerRunID != 0 && intent.LeaseOwner != fmt.Sprintf("%d-%d", intent.OwnerRunID, intent.OwnerRunAttempt) {
		return fmt.Errorf("projection Actions owner identity does not match the lease owner")
	}
	if intent.DeliveryState != ProjectionProjecting && (intent.LeaseOwner != "" || !intent.LeaseExpiresAt.IsZero() || intent.OwnerRunID != 0 || intent.OwnerRunAttempt != 0) {
		return fmt.Errorf("non-projecting intent retains a lease")
	}
	if intent.DeliveryState == ProjectionRetryable && intent.RetryAt.IsZero() {
		return fmt.Errorf("retryable projection intent lacks a retry time")
	}
	if intent.NeutralProjection != nil && (intent.NeutralProjection.ModuleID != intent.ModuleID || identity.ValidateRecordID("projection-intent", intent.NeutralProjection.IntentID) != nil || identity.ValidateRecordID("projection-fingerprint", intent.NeutralProjection.Fingerprint) != nil) {
		return fmt.Errorf("neutral projection identity does not match delivery owner")
	}
	return nil
}

func validateDiscoveryLifecycleFingerprint(fingerprint string) error {
	if fingerprint == "" {
		return nil
	}
	if err := identity.ValidateRecordID("discovery-intent", fingerprint); err != nil {
		return fmt.Errorf("invalid discovery lifecycle fingerprint: %w", err)
	}
	return nil
}

func (binding ProjectionBinding) Validate(moduleID string, generation uint64) error {
	if err := validateEvidenceReceipts(binding.EvidenceComments); err != nil {
		return err
	}
	if binding.SchemaVersion != projectionStateSchemaVersion || binding.ModuleID != moduleID || binding.IssueNumber < 1 || binding.IssueNodeID == "" || binding.IssueURL == "" || binding.ProjectItemID == "" || binding.Generation != generation || binding.UpdatedAt.IsZero() {
		return fmt.Errorf("projection binding identity is invalid")
	}
	return nil
}

func projectionIntentDue(intent ProjectionIntentRecord, now time.Time) bool {
	switch intent.DeliveryState {
	case ProjectionPending:
		return true
	case ProjectionRetryable:
		return !intent.RetryAt.After(now)
	case ProjectionProjecting:
		// Expiry alone cannot retire an owner: a process may have an in-flight
		// remote mutation whose outcome is not yet known.
		return false
	default:
		return false
	}
}

func sortProjectionIntents(intents []ProjectionIntentRecord) {
	sort.Slice(intents, func(left, right int) bool {
		leftRetry := intents[left].DeliveryState == ProjectionRetryable
		rightRetry := intents[right].DeliveryState == ProjectionRetryable
		if leftRetry != rightRetry {
			return leftRetry
		}
		if intents[left].Priority != intents[right].Priority {
			return intents[left].Priority < intents[right].Priority
		}
		return intents[left].ModuleID < intents[right].ModuleID
	})
}

func applyProjectionOutcome(intent *ProjectionIntentRecord, outcome ProjectionOutcome) error {
	if outcome.Success {
		if outcome.Binding == nil {
			return fmt.Errorf("successful projection outcome requires a remote binding")
		}
		intent.DeliveryState = ProjectionProjected
		intent.LastAcknowledgedFingerprint = intent.Fingerprint
		intent.LastErrorClass = ""
		intent.RetryAt = time.Time{}
		intent.RetryReason, intent.RetryRequest = "", nil
		return nil
	}
	if _, retryable := projectionRetryableErrors[outcome.ErrorClass]; retryable {
		if outcome.RetryAt.IsZero() || !outcome.RetryAt.After(outcome.CompletedAt) {
			return fmt.Errorf("retryable projection outcome requires a future retry time")
		}
		intent.DeliveryState = ProjectionRetryable
		intent.LastErrorClass = outcome.ErrorClass
		intent.RetryAt = outcome.RetryAt.UTC()
		intent.RetryReason, intent.RetryRequest = outcome.RetryReason, outcome.RetryRequest
		return nil
	}
	if outcome.ErrorClass == "" {
		return fmt.Errorf("failed projection outcome requires an error class")
	}
	intent.DeliveryState = ProjectionNeedsReview
	intent.LastErrorClass = outcome.ErrorClass
	intent.RetryAt = time.Time{}
	intent.RetryReason, intent.RetryRequest = "", nil
	return nil
}

func readProjectionIntent(snapshot state.Snapshot, recordPath string) (ProjectionIntentRecord, bool, error) {
	record, exists := snapshot.Records[recordPath]
	if !exists {
		return ProjectionIntentRecord{}, false, nil
	}
	if record.Kind != "projection_delivery" || record.SchemaVersion != projectionStateSchemaVersion {
		return ProjectionIntentRecord{}, false, fmt.Errorf("projection delivery record identity is invalid")
	}
	var intent ProjectionIntentRecord
	if err := json.Unmarshal(record.Data, &intent); err != nil {
		return ProjectionIntentRecord{}, false, err
	}
	if err := intent.Validate(); err != nil {
		return ProjectionIntentRecord{}, false, err
	}
	if recordPath != projectionDeliveryPrefix+intent.ModuleID+".json" {
		return ProjectionIntentRecord{}, false, fmt.Errorf("projection delivery record is stored at a noncanonical path")
	}
	return intent, true, nil
}

func readProjectionIntents(snapshot state.Snapshot) ([]ProjectionIntentRecord, error) {
	intents := make([]ProjectionIntentRecord, 0)
	for path, record := range snapshot.Records {
		if record.Kind != "projection_delivery" {
			continue
		}
		intent, exists, err := readProjectionIntent(snapshot, path)
		if err != nil {
			return nil, err
		}
		if exists {
			intents = append(intents, intent)
		}
	}
	return intents, nil
}

func readProjectionBinding(snapshot state.Snapshot, moduleID string) (ProjectionBinding, bool, error) {
	path := projectionBindingPrefix + moduleID + ".json"
	record, exists := snapshot.Records[path]
	if !exists {
		return ProjectionBinding{}, false, nil
	}
	if record.Kind != "projection_binding" || record.SchemaVersion != projectionStateSchemaVersion {
		return ProjectionBinding{}, false, fmt.Errorf("projection binding record identity is invalid")
	}
	var binding ProjectionBinding
	if err := json.Unmarshal(record.Data, &binding); err != nil {
		return ProjectionBinding{}, false, err
	}
	if err := binding.Validate(moduleID, binding.Generation); err != nil {
		return ProjectionBinding{}, false, err
	}
	return binding, true, nil
}

func classifyProjectionError(err error) string {
	var preSend *ProjectionPreSendError
	if errors.As(err, &preSend) {
		return ProjectionErrorTransient
	}
	if errors.Is(err, ErrGitHubRateLimited) || strings.Contains(strings.ToLower(err.Error()), "rate limit") || strings.Contains(err.Error(), "status 429") {
		return ProjectionErrorRateLimited
	}
	if errors.Is(err, ErrProjectionBudgetExhausted) {
		return ProjectionErrorBudget
	}
	if strings.Contains(err.Error(), "status 5") || strings.Contains(err.Error(), "Something went wrong") {
		return ProjectionErrorTransient
	}
	return ProjectionErrorNeedsReview
}

func (client Client) budgetSnapshot() BudgetSnapshot {
	if client.Budget == nil {
		return BudgetSnapshot{}
	}
	return client.Budget.Snapshot()
}

func (client Client) projectionResumeAt(now time.Time, delay time.Duration) time.Time {
	if client.Pacer != nil {
		if due := client.Pacer.ResumeAt(); due.After(now) {
			return due
		}
	}
	if client.Budget != nil {
		if resumeAt := client.Budget.ResumeAt(); resumeAt.After(now) {
			return resumeAt
		}
	}
	return now.Add(delay)
}

type projectionRecordEvent struct {
	path  string
	kind  string
	value any
}

func projectionEvents(records []projectionRecordEvent) ([]state.Event, error) {
	if len(records) == 0 {
		return nil, fmt.Errorf("projection state transaction must not be empty")
	}
	events := make([]state.Event, 0, len(records))
	for _, item := range records {
		data, err := json.Marshal(item.value)
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", item.kind, err)
		}
		record := state.Record{Kind: item.kind, SchemaVersion: projectionStateSchemaVersion, Data: data}
		eventID, err := identity.ID("state-event", 1, projectionEventIdentity{Path: item.path, Record: record})
		if err != nil {
			return nil, err
		}
		events = append(events, state.Event{ID: eventID, RecordPath: item.path, Record: record})
	}
	return events, nil
}

func projectionTransactionID(operation string, records []projectionRecordEvent) (string, error) {
	events, err := projectionEvents(records)
	if err != nil {
		return "", err
	}
	id, err := identity.ID("projection-operation", 1, struct {
		Operation string        `json:"operation"`
		Events    []state.Event `json:"events"`
	}{operation, events})
	if err != nil {
		return "", err
	}
	return id, nil
}

func runProjectionTransaction(ctx context.Context, coordinator state.TransactionCoordinator, operationID string, records []projectionRecordEvent, subject string) (string, error) {
	events, err := projectionEvents(records)
	if err != nil {
		return "", err
	}
	events = withProjectionOperationID(events, operationID)
	result, err := coordinator.RunTransaction(ctx, operationID, func(state.Snapshot) (state.TransactionDecision, error) {
		return state.TransactionDecision{Events: events, Result: operationID, Candidate: state.CandidateOptions{Subject: subject, Provenance: state.GitProvenance{Operation: projectionOperation, Event: operationID}}}, nil
	}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
		for _, event := range events {
			record, exists := snapshot.Records[event.RecordPath]
			if !exists || !reflect.DeepEqual(record, event.Record) {
				return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
			}
		}
		return state.TransactionResolution{Found: true, Value: operationID}, nil
	})
	return result.Head, err
}

func runProjectionTransactionResult(ctx context.Context, coordinator state.TransactionCoordinator, operationID string, events []state.Event, subject string, resultValue any) (state.TransactionResult, error) {
	return runProjectionTransactionResultWithDecision(ctx, coordinator, operationID, events, subject, resultValue, func(state.Snapshot) (any, error) {
		return resultValue, nil
	})
}

func runProjectionTransactionResultWithDecision(ctx context.Context, coordinator state.TransactionCoordinator, operationID string, events []state.Event, subject string, replayValue any, decideResult func(state.Snapshot) (any, error)) (state.TransactionResult, error) {
	events = withProjectionOperationID(events, operationID)
	result, err := coordinator.RunTransaction(ctx, operationID, func(snapshot state.Snapshot) (state.TransactionDecision, error) {
		value, err := decideResult(snapshot)
		if err != nil {
			return state.TransactionDecision{}, err
		}
		return state.TransactionDecision{Events: events, Result: value, Candidate: state.CandidateOptions{Subject: subject, Provenance: state.GitProvenance{Operation: projectionOperation, Event: operationID}}}, nil
	}, func(snapshot state.Snapshot) (state.TransactionResolution, error) {
		for _, event := range events {
			record, exists := snapshot.Records[event.RecordPath]
			if !exists || !reflect.DeepEqual(record, event.Record) {
				return state.TransactionResolution{}, state.ErrTransactionIdentityConflict
			}
		}
		return state.TransactionResolution{Found: true, Value: replayValue}, nil
	})
	return result, err
}

func withProjectionOperationID(events []state.Event, operationID string) []state.Event {
	updated := append([]state.Event(nil), events...)
	if len(updated) > 0 {
		updated[0].ID = operationID
	}
	return updated
}
