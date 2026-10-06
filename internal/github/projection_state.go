package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

const (
	ProjectionPending                               = "pending"
	ProjectionProjecting                            = "projecting"
	ProjectionProjected                             = "projected"
	ProjectionRetryable                             = "retryable"
	ProjectionNeedsReview                           = "needs-review"
	ProjectionBatchRunning                          = "running"
	ProjectionBatchComplete                         = "complete"
	ProjectionBatchExpired                          = "expired"
	ProjectionErrorRateLimited                      = "rate_limited"
	ProjectionErrorTransient                        = "transient"
	ProjectionErrorBudget                           = "budget"
	ProjectionErrorNeedsReview                      = "needs_review"
	ProjectionPriorityRetry      ProjectionPriority = 1
	ProjectionPriorityChanged    ProjectionPriority = 2
	ProjectionPriorityActive     ProjectionPriority = 3
	ProjectionPriorityCleanup    ProjectionPriority = 4
	projectionStateSchemaVersion                    = 1
	projectionDeliveryPrefix                        = "state/projection/delivery/"
	projectionBindingPrefix                         = "state/projection/bindings/"
	projectionBatchPrefix                           = "state/projection/batches/"
	MaxProjectionBatchSize                          = 25
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
	SchemaVersion               int                `json:"schema_version"`
	ModuleID                    string             `json:"module_id"`
	Generation                  uint64             `json:"generation"`
	Fingerprint                 string             `json:"fingerprint"`
	DiscoveryFingerprint        string             `json:"discovery_fingerprint,omitempty"`
	Priority                    ProjectionPriority `json:"priority"`
	Projection                  Projection         `json:"projection"`
	DeliveryState               string             `json:"delivery_state"`
	Attempt                     uint64             `json:"attempt"`
	Fence                       uint64             `json:"fence"`
	BatchID                     string             `json:"batch_id,omitempty"`
	LeaseOwner                  string             `json:"lease_owner,omitempty"`
	LeaseExpiresAt              time.Time          `json:"lease_expires_at,omitempty"`
	RetryAt                     time.Time          `json:"retry_at,omitempty"`
	LastErrorClass              string             `json:"last_error_class,omitempty"`
	LastAcknowledgedFingerprint string             `json:"last_acknowledged_fingerprint,omitempty"`
	UpdatedAt                   time.Time          `json:"updated_at"`
}

// ProjectionBinding keeps remote IDs separate from the desired projection.
type ProjectionBinding struct {
	SchemaVersion int       `json:"schema_version"`
	ModuleID      string    `json:"module_id"`
	IssueNumber   int       `json:"issue_number"`
	IssueNodeID   string    `json:"issue_node_id"`
	IssueURL      string    `json:"issue_url"`
	ProjectItemID string    `json:"project_item_id"`
	Generation    uint64    `json:"generation"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ProjectionBatch stores resumable module and remote-snapshot cursors.
type ProjectionBatch struct {
	SchemaVersion   int               `json:"schema_version"`
	ID              string            `json:"batch_id"`
	RunID           string            `json:"run_id"`
	Status          string            `json:"status"`
	ModuleIDs       []string          `json:"module_ids"`
	Cursor          int               `json:"cursor"`
	SnapshotCursors map[string]string `json:"snapshot_cursors"`
	Budget          BudgetSnapshot    `json:"budget"`
	LeaseDuration   time.Duration     `json:"lease_duration_ns"`
	LeaseExpiresAt  time.Time         `json:"lease_expires_at"`
	CreatedAt       time.Time         `json:"created_at"`
}

// BudgetSnapshot persists resource headroom and usage with each batch.
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

// ProjectionClaimOptions binds a projection claim to one durable state head.
type ProjectionClaimOptions struct {
	ExpectedHead string
	RunID        string
	Now          time.Time
	Lease        time.Duration
	Limit        int
}

// ProjectionIntentSaveResult returns one atomic desired-projection batch save.
type ProjectionIntentSaveResult struct {
	Head    string
	Intents []ProjectionIntentRecord
	Changed bool
}

// ProjectionOutcome is the verified result of one module's remote projection.
type ProjectionOutcome struct {
	Success     bool
	ErrorClass  string
	RetryAt     time.Time
	CompletedAt time.Time
	Binding     *ProjectionBinding
	Budget      BudgetSnapshot
}

// ProjectionOutcomeInput binds an outcome to one claimed batch target.
type ProjectionOutcomeInput struct {
	BatchID    string
	ModuleID   string
	RunID      string
	Generation uint64
	Fence      uint64
	Outcome    ProjectionOutcome
}

// ProjectionSnapshotCheckpoint is a resumable cursor update for shared remote reads.
type ProjectionSnapshotCheckpoint struct {
	BatchID        string
	RunID          string
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
}

// ProjectionRunResult reports partial success without invalidating accepted data.
type ProjectionRunResult struct {
	Head      string
	Batch     ProjectionBatch
	Completed int
	Partial   bool
	Errors    []string
}

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
func ReplayAcknowledgedProjections(ctx context.Context, backend state.Backend, expectedHead string, moduleIDs []string, now time.Time) (string, error) {
	if now.IsZero() || len(moduleIDs) == 0 {
		return "", fmt.Errorf("projection replay requires selected modules and a timestamp")
	}
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return "", err
	}
	if head != expectedHead {
		return "", state.ErrHeadMoved
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
		return head, nil
	}
	return commitProjectionEvents(ctx, backend, head, events)
}

// SaveProjectionIntent replaces one durable desired delivery record. Matching
// fingerprints do not reset retries; a new generation fences the prior lease.
func SaveProjectionIntent(ctx context.Context, backend state.Backend, expectedHead string, intent ProjectionIntentRecord) (ProjectionBatchClaim, error) {
	saved, err := SaveProjectionIntents(ctx, backend, expectedHead, []ProjectionIntentRecord{intent})
	if err != nil {
		return ProjectionBatchClaim{}, err
	}
	return ProjectionBatchClaim{Head: saved.Head, Intents: saved.Intents, Changed: saved.Changed}, nil
}

// SaveProjectionIntents merges a targeted set of desired projections in one
// expected-head transaction. Existing delivery checkpoints remain retryable.
func SaveProjectionIntents(ctx context.Context, backend state.Backend, expectedHead string, intents []ProjectionIntentRecord) (ProjectionIntentSaveResult, error) {
	if len(intents) == 0 {
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
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return ProjectionIntentSaveResult{}, err
	}
	if head != expectedHead {
		return ProjectionIntentSaveResult{}, state.ErrHeadMoved
	}
	merged := make([]ProjectionIntentRecord, 0, len(ordered))
	events := make([]projectionRecordEvent, 0, len(ordered))
	for _, intent := range ordered {
		intent, err = bindProjectionToDiscovery(snapshot, intent)
		if err != nil {
			return ProjectionIntentSaveResult{}, err
		}
		path := projectionDeliveryPrefix + intent.ModuleID + ".json"
		existing, exists, readErr := readProjectionIntent(snapshot, path)
		if readErr != nil {
			return ProjectionIntentSaveResult{}, readErr
		}
		updated, changed, mergeErr := mergeProjectionIntent(existing, exists, intent)
		if mergeErr != nil {
			return ProjectionIntentSaveResult{}, mergeErr
		}
		merged = append(merged, updated)
		if changed {
			events = append(events, projectionRecordEvent{path: path, kind: "projection_delivery", value: updated})
		}
	}
	if len(events) == 0 {
		return ProjectionIntentSaveResult{Head: head, Intents: merged}, nil
	}
	newHead, err := commitProjectionEvents(ctx, backend, head, events)
	if err != nil {
		return ProjectionIntentSaveResult{}, err
	}
	return ProjectionIntentSaveResult{Head: newHead, Intents: merged, Changed: true}, nil
}

// bindProjectionToDiscovery relates delivery to current durable discovery, not
// this invocation's changed/unchanged result. Other lifecycle phases bind only
// when their source matches the current discovery source.
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
	if intent.Generation < existing.Generation || (intent.Generation == existing.Generation && intent.Fingerprint != existing.Fingerprint) {
		return ProjectionIntentRecord{}, false, ErrProjectionFence
	}
	if intent.Fingerprint != existing.Fingerprint {
		return changedProjectionIntent(existing, intent), true, nil
	}
	if intent.Generation == existing.Generation {
		if intent.DiscoveryFingerprint != existing.DiscoveryFingerprint {
			existing.DiscoveryFingerprint = intent.DiscoveryFingerprint
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
	intent.BatchID = ""
	intent.LeaseOwner = ""
	intent.LeaseExpiresAt = time.Time{}
	intent.RetryAt = time.Time{}
	intent.LastErrorClass = ""
	return intent
}

func newerEquivalentProjectionIntent(existing, intent ProjectionIntentRecord) ProjectionIntentRecord {
	intent.DeliveryState = existing.DeliveryState
	intent.Attempt = existing.Attempt
	intent.LastErrorClass = existing.LastErrorClass
	intent.LastAcknowledgedFingerprint = existing.LastAcknowledgedFingerprint
	intent.RetryAt = existing.RetryAt
	intent.Fence = existing.Fence + 1
	if existing.LastAcknowledgedFingerprint == intent.Fingerprint {
		intent.DeliveryState = ProjectionProjected
		intent.LeaseOwner = ""
		intent.LeaseExpiresAt = time.Time{}
	} else if existing.DeliveryState == ProjectionProjecting {
		intent.DeliveryState = ProjectionPending
		intent.LeaseOwner = ""
		intent.LeaseExpiresAt = time.Time{}
	}
	return intent
}

// ClaimProjectionBatch claims an ordered, bounded set of due work in one CAS.
// Empty queues do not write state, allowing callers to make zero API calls.
func ClaimProjectionBatch(ctx context.Context, backend state.Backend, expectedHead, runID string, now time.Time, lease time.Duration, limit int) (ProjectionBatchClaim, error) {
	return claimProjectionBatch(ctx, backend, ProjectionClaimOptions{
		ExpectedHead: expectedHead, RunID: runID, Now: now, Lease: lease, Limit: limit,
	}, nil)
}

// ClaimProjectionBatchForModules limits one claim to the supplied module IDs.
// Other due intents remain untouched for their own targeted run.
func ClaimProjectionBatchForModules(ctx context.Context, backend state.Backend, options ProjectionClaimOptions, moduleIDs []string) (ProjectionBatchClaim, error) {
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
	return claimProjectionBatch(ctx, backend, options, selected)
}

func claimProjectionBatch(ctx context.Context, backend state.Backend, options ProjectionClaimOptions, selected map[string]struct{}) (ProjectionBatchClaim, error) {
	if err := validateProjectionClaim(options.ExpectedHead, options.RunID, options.Now, options.Lease, options.Limit); err != nil {
		return ProjectionBatchClaim{}, err
	}
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return ProjectionBatchClaim{}, err
	}
	if head != options.ExpectedHead {
		return ProjectionBatchClaim{}, state.ErrHeadMoved
	}
	claim, events, err := buildProjectionBatchClaim(snapshot, head, options.RunID, options.Now, options.Lease, options.Limit, selected)
	if err != nil {
		return ProjectionBatchClaim{}, err
	}
	if len(events) == 0 {
		claim.Head = head
		return claim, nil
	}
	newHead, err := commitProjectionEvents(ctx, backend, head, events)
	if err != nil {
		return ProjectionBatchClaim{}, err
	}
	claim.Head = newHead
	claim.Changed = true
	return claim, nil
}

func validateProjectionClaim(expectedHead, runID string, now time.Time, lease time.Duration, limit int) error {
	if expectedHead == "" || strings.TrimSpace(runID) == "" || now.IsZero() || lease <= 0 || limit < 1 || limit > MaxProjectionBatchSize {
		return fmt.Errorf("projection run, lease, and bounded batch size are required")
	}
	return nil
}

func buildProjectionBatchClaim(snapshot state.Snapshot, head, runID string, now time.Time, lease time.Duration, limit int, selected map[string]struct{}) (ProjectionBatchClaim, []projectionRecordEvent, error) {
	intents, err := readProjectionIntents(snapshot)
	if err != nil {
		return ProjectionBatchClaim{}, nil, err
	}
	eligible := make([]ProjectionIntentRecord, 0, len(intents))
	for _, intent := range intents {
		if _, targeted := selected[intent.ModuleID]; (selected == nil || targeted) && projectionIntentDue(intent, now) {
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
	batchID, err := identity.ID("projection-batch", 1, struct {
		RunID   string    `json:"run_id"`
		Head    string    `json:"head"`
		Modules []string  `json:"modules"`
		At      time.Time `json:"at"`
	}{runID, head, moduleIDs, now.UTC()})
	if err != nil {
		return ProjectionBatchClaim{}, nil, err
	}
	batch := ProjectionBatch{
		SchemaVersion: projectionStateSchemaVersion, ID: batchID, RunID: runID,
		Status: ProjectionBatchRunning, ModuleIDs: moduleIDs, Cursor: 0,
		SnapshotCursors: map[string]string{}, Budget: BudgetSnapshot{}, LeaseDuration: lease,
		LeaseExpiresAt: now.Add(lease).UTC(), CreatedAt: now.UTC(),
	}
	claimed, events, err := claimProjectionIntents(eligible, batch, runID, now)
	if err != nil {
		return ProjectionBatchClaim{}, nil, err
	}
	batch.Budget = BudgetSnapshot{}
	events = append(events, projectionRecordEvent{path: projectionBatchPrefix + batchID + ".json", kind: "projection_batch", value: batch})
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
	events := make([]projectionRecordEvent, 0, len(eligible)+1)
	for _, intent := range eligible {
		if intent.Fence == math.MaxUint64 || intent.Attempt == math.MaxUint64 {
			return nil, nil, fmt.Errorf("projection fence or attempt is exhausted for %s", intent.ModuleID)
		}
		intent.DeliveryState = ProjectionProjecting
		intent.Attempt++
		intent.Fence++
		intent.BatchID = batch.ID
		intent.LeaseOwner = runID
		intent.LeaseExpiresAt = batch.LeaseExpiresAt
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
func RecordProjectionOutcome(ctx context.Context, backend state.Backend, expectedHead string, input ProjectionOutcomeInput) (ProjectionOutcomeResult, error) {
	if expectedHead == "" || strings.TrimSpace(input.BatchID) == "" || strings.TrimSpace(input.ModuleID) == "" || strings.TrimSpace(input.RunID) == "" || input.Generation == 0 || input.Fence == 0 || input.Outcome.CompletedAt.IsZero() {
		return ProjectionOutcomeResult{}, fmt.Errorf("projection outcome ownership and timestamp are required")
	}
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return ProjectionOutcomeResult{}, err
	}
	if head != expectedHead {
		return ProjectionOutcomeResult{}, state.ErrHeadMoved
	}
	batch, intent, err := claimedProjectionTarget(snapshot, input)
	if err != nil {
		return ProjectionOutcomeResult{}, err
	}
	if err = applyProjectionOutcome(&intent, input.Outcome); err != nil {
		return ProjectionOutcomeResult{}, err
	}
	intent.LeaseOwner = ""
	intent.LeaseExpiresAt = time.Time{}
	intent.UpdatedAt = input.Outcome.CompletedAt.UTC()
	events := []projectionRecordEvent{{path: projectionDeliveryPrefix + input.ModuleID + ".json", kind: "projection_delivery", value: intent}}
	if input.Outcome.Binding != nil {
		binding := *input.Outcome.Binding
		if err = binding.Validate(input.ModuleID, input.Generation); err != nil {
			return ProjectionOutcomeResult{}, err
		}
		events = append(events, projectionRecordEvent{path: projectionBindingPrefix + input.ModuleID + ".json", kind: "projection_binding", value: binding})
	}
	batch.Cursor++
	batch.Budget = input.Outcome.Budget
	if batch.Cursor == len(batch.ModuleIDs) {
		batch.Status = ProjectionBatchComplete
		batch.LeaseExpiresAt = time.Time{}
	} else {
		renewalEvents, renewErr := renewProjectionBatchLease(snapshot, &batch, input.Outcome.CompletedAt)
		if renewErr != nil {
			return ProjectionOutcomeResult{}, renewErr
		}
		events = append(events, renewalEvents...)
	}
	events = append(events, projectionRecordEvent{path: projectionBatchPrefix + input.BatchID + ".json", kind: "projection_batch", value: batch})
	newHead, err := commitProjectionEvents(ctx, backend, head, events)
	if err != nil {
		return ProjectionOutcomeResult{}, err
	}
	return ProjectionOutcomeResult{Head: newHead, Batch: batch, Intent: intent}, nil
}

func claimedProjectionTarget(snapshot state.Snapshot, input ProjectionOutcomeInput) (ProjectionBatch, ProjectionIntentRecord, error) {
	batch, err := readProjectionBatch(snapshot, input.BatchID)
	if err != nil {
		return ProjectionBatch{}, ProjectionIntentRecord{}, err
	}
	if batch.Status != ProjectionBatchRunning || batch.RunID != input.RunID || batch.Cursor >= len(batch.ModuleIDs) || batch.ModuleIDs[batch.Cursor] != input.ModuleID {
		return ProjectionBatch{}, ProjectionIntentRecord{}, ErrProjectionFence
	}
	intent, exists, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+input.ModuleID+".json")
	if err != nil || !exists {
		return ProjectionBatch{}, ProjectionIntentRecord{}, ErrProjectionFence
	}
	if intent.Generation != input.Generation || intent.Fence != input.Fence || intent.BatchID != input.BatchID || intent.LeaseOwner != input.RunID || intent.DeliveryState != ProjectionProjecting || !input.Outcome.CompletedAt.Before(intent.LeaseExpiresAt) {
		return ProjectionBatch{}, ProjectionIntentRecord{}, ErrProjectionFence
	}
	return batch, intent, nil
}

// CheckpointProjectionSnapshot persists remote pagination cursors and the
// latest separate API budgets before resuming a later read or mutation phase.
func CheckpointProjectionSnapshot(ctx context.Context, backend state.Backend, expectedHead string, checkpoint ProjectionSnapshotCheckpoint) (string, error) {
	if checkpoint.CheckpointedAt.IsZero() {
		return "", fmt.Errorf("projection checkpoint time is required")
	}
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return "", err
	}
	if head != expectedHead {
		return "", state.ErrHeadMoved
	}
	batch, err := readProjectionBatch(snapshot, checkpoint.BatchID)
	if err != nil {
		return "", err
	}
	if batch.Status != ProjectionBatchRunning || batch.RunID != checkpoint.RunID || !checkpoint.CheckpointedAt.Before(batch.LeaseExpiresAt) {
		return "", ErrProjectionFence
	}
	if batch.SnapshotCursors == nil {
		batch.SnapshotCursors = map[string]string{}
	}
	for name, cursor := range checkpoint.Cursors {
		if strings.TrimSpace(name) == "" {
			return "", fmt.Errorf("projection snapshot cursor name is required")
		}
		batch.SnapshotCursors[name] = cursor
	}
	batch.Budget = checkpoint.Budget
	renewalEvents, err := renewProjectionBatchLease(snapshot, &batch, checkpoint.CheckpointedAt)
	if err != nil {
		return "", err
	}
	events := append(renewalEvents, projectionRecordEvent{path: projectionBatchPrefix + checkpoint.BatchID + ".json", kind: "projection_batch", value: batch})
	return commitProjectionEvents(ctx, backend, head, events)
}

// renewProjectionBatchLease extends the batch and every still-claimed target
// together so slow durable checkpoints cannot leave a live worker fenced out.
func renewProjectionBatchLease(snapshot state.Snapshot, batch *ProjectionBatch, renewedAt time.Time) ([]projectionRecordEvent, error) {
	if batch.Status != ProjectionBatchRunning || batch.LeaseDuration <= 0 || renewedAt.IsZero() {
		return nil, fmt.Errorf("running projection batch lease renewal is invalid")
	}
	batch.LeaseExpiresAt = renewedAt.Add(batch.LeaseDuration).UTC()
	events := make([]projectionRecordEvent, 0, len(batch.ModuleIDs)-batch.Cursor)
	for _, moduleID := range batch.ModuleIDs[batch.Cursor:] {
		path := projectionDeliveryPrefix + moduleID + ".json"
		intent, exists, err := readProjectionIntent(snapshot, path)
		if err != nil || !exists || intent.DeliveryState != ProjectionProjecting || intent.BatchID != batch.ID || intent.LeaseOwner != batch.RunID {
			return nil, ErrProjectionFence
		}
		intent.LeaseExpiresAt = batch.LeaseExpiresAt
		intent.UpdatedAt = renewedAt.UTC()
		events = append(events, projectionRecordEvent{path: path, kind: "projection_delivery", value: intent})
	}
	return events, nil
}

// ProjectClaimedBatch performs no network call for an empty local batch.
// Progress is persisted separately after each target via RecordProjectionOutcome.
func (client Client) ProjectClaimedBatch(ctx context.Context, backend state.Backend, claim ProjectionBatchClaim, options ProjectionRunOptions) (ProjectionRunResult, error) {
	result := ProjectionRunResult{Head: claim.Head, Batch: claim.Batch, Errors: []string{}}
	if len(claim.Batch.ModuleIDs) == 0 {
		return result, nil
	}
	var err error
	client, err = client.prepareProjectionBatch(ctx, backend, claim, options, &result)
	if err != nil {
		return result, err
	}
	for position, intent := range claim.Intents {
		if intent.ModuleID != claim.Batch.ModuleIDs[position] {
			return result, fmt.Errorf("projection batch target order does not match the durable cursor")
		}
		moduleCtx, finish := progress.Start(ctx, "projection", "module_delivery", slog.String("module_id", intent.ModuleID), slog.Int("position", position+1), slog.Int("total", len(claim.Intents)), slog.String("run_id", claim.Batch.RunID))
		checkpoint, projectErr, checkpointErr := client.projectClaimedIntent(moduleCtx, backend, result.Head, claim, options, intent)
		finish(projectErr == nil && checkpointErr == nil)
		if checkpointErr != nil {
			return result, checkpointErr
		}
		result.Head = checkpoint.Head
		result.Batch = checkpoint.Batch
		if projectErr == nil {
			result.Completed++
		} else {
			progress.Logger(moduleCtx).WarnContext(moduleCtx, "projection module deferred", "reason", classifyProjectionError(projectErr))
			result.Errors = append(result.Errors, projectErr.Error())
		}
		if errors.Is(projectErr, ErrProjectionBudgetExhausted) {
			progress.Logger(ctx).WarnContext(ctx, "projection batch paused", "reason", "API budget exhausted", "completed", result.Completed, "remaining", len(claim.Intents)-position-1)
			result.Partial = true
			return result, nil
		}
	}
	progress.Logger(ctx).InfoContext(ctx, "projection batch outcomes recorded", "completed", result.Completed, "failed", len(result.Errors), "total", len(claim.Intents))
	return result, nil
}

func (client Client) prepareProjectionBatch(ctx context.Context, backend state.Backend, claim ProjectionBatchClaim, options ProjectionRunOptions, result *ProjectionRunResult) (prepared Client, returnErr error) {
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
		newHead, checkpointErr := CheckpointProjectionSnapshot(ctx, backend, result.Head, ProjectionSnapshotCheckpoint{
			BatchID: claim.Batch.ID, RunID: claim.Batch.RunID, Cursors: cursors,
			Budget: client.budgetSnapshot(), CheckpointedAt: time.Now().UTC(),
		})
		if checkpointErr != nil {
			return checkpointErr
		}
		result.Head = newHead
		if result.Batch.SnapshotCursors == nil {
			result.Batch.SnapshotCursors = map[string]string{}
		}
		result.Batch.SnapshotCursors[name] = cursor
		client.MetadataCache.updateStateHead(newHead)
		return nil
	})
	return client, nil
}

func validateProjectionRun(claim ProjectionBatchClaim, options ProjectionRunOptions) error {
	if options.Owner == "" || options.Repository == "" || options.ProjectNumber < 1 || options.RetryDelay <= 0 || len(claim.Intents) != len(claim.Batch.ModuleIDs) {
		return fmt.Errorf("projection batch target and retry options are required")
	}
	return nil
}

func (client Client) projectClaimedIntent(ctx context.Context, backend state.Backend, expectedHead string, claim ProjectionBatchClaim, options ProjectionRunOptions, intent ProjectionIntentRecord) (ProjectionOutcomeResult, error, error) {
	currentHead, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return ProjectionOutcomeResult{}, nil, err
	}
	if currentHead != expectedHead {
		return ProjectionOutcomeResult{}, nil, state.ErrHeadMoved
	}
	current, exists, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+intent.ModuleID+".json")
	if err != nil || !exists || current.Generation != intent.Generation || current.Fence != intent.Fence || current.BatchID != claim.Batch.ID || current.LeaseOwner != claim.Batch.RunID {
		return ProjectionOutcomeResult{}, nil, ErrProjectionFence
	}
	issue, projectErr := client.UpsertFleet(ctx, options.Owner, options.Repository, options.ProjectNumber, intent.Projection)
	completedAt := time.Now().UTC()
	outcome := ProjectionOutcome{CompletedAt: completedAt, Budget: client.budgetSnapshot()}
	if projectErr == nil {
		outcome.Success = true
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
		binding.UpdatedAt = completedAt
		outcome.Binding = &binding
	} else {
		outcome.ErrorClass = classifyProjectionError(projectErr)
		if _, retryable := projectionRetryableErrors[outcome.ErrorClass]; retryable {
			outcome.RetryAt = client.projectionResumeAt(completedAt, options.RetryDelay)
		}
	}
	checkpointHead := client.MetadataCache.stateHead(expectedHead)
	checkpoint, checkpointErr := RecordProjectionOutcome(ctx, backend, checkpointHead, ProjectionOutcomeInput{
		BatchID: claim.Batch.ID, ModuleID: intent.ModuleID, RunID: claim.Batch.RunID,
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
	if err := validateDiscoveryLifecycleFingerprint(intent.DiscoveryFingerprint); err != nil {
		return err
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
	if intent.DeliveryState == ProjectionProjecting && (intent.LeaseOwner == "" || intent.BatchID == "" || intent.Fence == 0 || intent.LeaseExpiresAt.IsZero()) {
		return fmt.Errorf("projecting intent is missing its fenced lease")
	}
	if intent.DeliveryState != ProjectionProjecting && (intent.LeaseOwner != "" || !intent.LeaseExpiresAt.IsZero()) {
		return fmt.Errorf("non-projecting intent retains a lease")
	}
	if intent.DeliveryState == ProjectionRetryable && intent.RetryAt.IsZero() {
		return fmt.Errorf("retryable projection intent lacks a retry time")
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
		return !intent.LeaseExpiresAt.After(now)
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
		return nil
	}
	if _, retryable := projectionRetryableErrors[outcome.ErrorClass]; retryable {
		if outcome.RetryAt.IsZero() || !outcome.RetryAt.After(outcome.CompletedAt) {
			return fmt.Errorf("retryable projection outcome requires a future retry time")
		}
		intent.DeliveryState = ProjectionRetryable
		intent.LastErrorClass = outcome.ErrorClass
		intent.RetryAt = outcome.RetryAt.UTC()
		return nil
	}
	if outcome.ErrorClass == "" {
		return fmt.Errorf("failed projection outcome requires an error class")
	}
	intent.DeliveryState = ProjectionNeedsReview
	intent.LastErrorClass = outcome.ErrorClass
	intent.RetryAt = time.Time{}
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
	if errors.Is(err, ErrProjectionBudgetExhausted) || strings.Contains(strings.ToLower(err.Error()), "rate limit") || strings.Contains(err.Error(), "status 429") {
		return ProjectionErrorRateLimited
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
	if client.Budget != nil {
		if resumeAt := client.Budget.ResumeAt(); resumeAt.After(now) {
			return resumeAt
		}
	}
	return now.Add(delay)
}

func readProjectionBatch(snapshot state.Snapshot, batchID string) (ProjectionBatch, error) {
	record, exists := snapshot.Records[projectionBatchPrefix+batchID+".json"]
	if !exists || record.Kind != "projection_batch" || record.SchemaVersion != projectionStateSchemaVersion {
		return ProjectionBatch{}, fmt.Errorf("projection batch %s was not found", batchID)
	}
	var batch ProjectionBatch
	if err := json.Unmarshal(record.Data, &batch); err != nil {
		return ProjectionBatch{}, err
	}
	if err := batch.Validate(); err != nil {
		return ProjectionBatch{}, err
	}
	return batch, nil
}

func (batch ProjectionBatch) Validate() error {
	if batch.SchemaVersion != projectionStateSchemaVersion || batch.ID == "" || batch.RunID == "" || batch.CreatedAt.IsZero() || batch.Cursor < 0 || batch.Cursor > len(batch.ModuleIDs) || batch.SnapshotCursors == nil {
		return fmt.Errorf("projection batch identity or cursor is invalid")
	}
	if batch.Status != ProjectionBatchRunning && batch.Status != ProjectionBatchComplete && batch.Status != ProjectionBatchExpired {
		return fmt.Errorf("projection batch status %q is invalid", batch.Status)
	}
	seen := map[string]bool{}
	for _, moduleID := range batch.ModuleIDs {
		if !projectionModuleIDPattern.MatchString(moduleID) || seen[moduleID] {
			return fmt.Errorf("projection batch module IDs must be valid and unique")
		}
		seen[moduleID] = true
	}
	if batch.Status == ProjectionBatchRunning && batch.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("running projection batch has no lease")
	}
	return nil
}

type projectionRecordEvent struct {
	path  string
	kind  string
	value any
}

func commitProjectionEvents(ctx context.Context, backend state.Backend, expectedHead string, records []projectionRecordEvent) (string, error) {
	if len(records) == 0 {
		return "", fmt.Errorf("projection state transaction must not be empty")
	}
	events := make([]state.Event, 0, len(records))
	for _, item := range records {
		data, err := json.Marshal(item.value)
		if err != nil {
			return "", fmt.Errorf("encode %s: %w", item.kind, err)
		}
		record := state.Record{Kind: item.kind, SchemaVersion: projectionStateSchemaVersion, Data: data}
		eventID, err := identity.ID("state-event", 1, projectionEventIdentity{Path: item.path, Record: record})
		if err != nil {
			return "", err
		}
		events = append(events, state.Event{ID: eventID, RecordPath: item.path, Record: record})
	}
	newHead, _, err := state.ApplyEventsAtHead(ctx, backend, expectedHead, events)
	return newHead, err
}
