// Package state owns transactional operational state and its expected-head write boundary.
package state

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
)

const SnapshotSchemaVersion = 1

var (
	ErrHeadMoved     = errors.New("state branch head moved")
	ErrEventConflict = errors.New("event ID was reused with different content")
	ErrRecordChanged = errors.New("state record changed before deletion")
	ErrStaleFence    = errors.New("event fencing token is stale")

	eventIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	fenceKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// Backend reads one immutable state snapshot.
type Backend interface {
	Read(context.Context) (head string, snapshot Snapshot, err error)
}

// LegacyCommitBackend supports isolated in-memory state contract fixtures.
// Production state writers use GitBackend and TransactionCoordinator.
type LegacyCommitBackend interface {
	Backend
	Commit(context.Context, string, Snapshot) (newHead string, err error)
}

// PublicationOutcomeKind classifies whether a candidate is known to have been
// published, rejected, or left with an uncertain publication result.
type PublicationOutcomeKind string

const (
	PublicationCommitted PublicationOutcomeKind = "committed"
	PublicationRejected  PublicationOutcomeKind = "rejected"
	PublicationUnknown   PublicationOutcomeKind = "unknown"
)

// PublicationOutcome reports the transport result for one immutable candidate.
// Unknown outcomes require reconciliation before another decision is made.
type PublicationOutcome struct {
	Kind        PublicationOutcomeKind
	Head        string
	CandidateID string
}

// GitCandidate is the complete state image prepared from one verified parent.
// The Git backend publishes it without merging or replaying it over a new head.
type GitCandidate struct {
	ParentHead  string
	CandidateID string
	TreeID      string
	Snapshot    Snapshot
	Message     string
	CommitTime  time.Time
}

const (
	maxPublishAncestryDepth = 4096
	publishAncestryStep     = 64
)

var ErrGitObjectProofUnavailable = errors.New("Git object ancestry proof is unavailable")
var ErrTransactionIdentityConflict = errors.New("transaction operation identity conflicts with durable state")

type TransactionOutcomeUnknownError struct {
	OperationID string
	CandidateID string
	Cause       error
}

func (err *TransactionOutcomeUnknownError) Error() string {
	if err.Cause == nil {
		return fmt.Sprintf("transaction outcome is unknown for operation %s candidate %s", err.OperationID, err.CandidateID)
	}
	return fmt.Sprintf("transaction outcome is unknown for operation %s candidate %s: %v", err.OperationID, err.CandidateID, err.Cause)
}

func (err *TransactionOutcomeUnknownError) Unwrap() error { return err.Cause }

// GitReader exposes verified fresh-head reads without implying that the
// publication portion of GitBackend is available to the caller.
type GitReader interface {
	Read(context.Context) (head string, snapshot Snapshot, err error)
}

// GitBackend is the desired verified-read and single-candidate publication
// boundary. Its implementation owns Git transport and object verification.
type GitBackend interface {
	GitReader
	BuildCandidate(context.Context, CandidateOptions) (GitCandidate, error)
	Publish(context.Context, GitCandidate) (PublicationOutcome, error)
}

// TransactionReadSet declares the owners and shared records that informed a
// decision and therefore must be revalidated after publication contention.
type TransactionReadSet struct {
	Owners []string
	Paths  []string
}

// TransactionDecision is one pure decision against one immutable snapshot.
// Result is returned for a newly committed operation; ResultResolver rebuilds
// it from durable state when the same operation has already committed.
type TransactionDecision struct {
	ReadSet   TransactionReadSet
	Events    []Event
	Result    any
	Noop      bool
	Candidate CandidateOptions
	operation *operationContract
}

// TransactionDecisionFunc decides what to do using only its supplied snapshot.
type TransactionDecisionFunc func(Snapshot) (TransactionDecision, error)

// TransactionResultResolver reconstructs the original result for an operation
// found in durable state before another candidate is prepared.
type TransactionResolution struct {
	Found bool
	Value any
}

type TransactionResultResolver func(Snapshot) (TransactionResolution, error)

// TransactionResult carries the domain result and verified state head.
type TransactionResult struct {
	Head    string
	Value   any
	Changed bool
}

// TransactionCoordinator owns fetch, decision, validation, publication,
// reconciliation, and the bounded whole-decision retry budget.
type TransactionCoordinator interface {
	Read(context.Context) (head string, snapshot Snapshot, err error)
	RunTransaction(context.Context, string, TransactionDecisionFunc, TransactionResultResolver) (TransactionResult, error)
}

// StateCredentialProvider grants a short-lived credential only while run is
// executing for the explicitly named host and repository.
type StateCredentialProvider interface {
	WithCredential(context.Context, string, string, func(credentialFile string) error) error
}

// GitCommand is an argument-vector invocation with an explicit working
// directory and allowlisted environment; it contains no shell command string.
type GitCommand struct {
	Executable  string
	Directory   string
	Arguments   []string
	Environment map[string]string
	Input       []byte

	allowLocalRemote  bool
	allowPrivateIndex bool
}

// GitCommandResult contains bounded process output and exit status.
type GitCommandResult struct {
	Stdout   []byte
	ExitCode int
}

// GitCommandRunner is the sole subprocess boundary used by the Git transport.
type GitCommandRunner interface {
	Run(context.Context, GitCommand) (GitCommandResult, error)
}

// Snapshot is the validated state at one branch head. Records are keyed by
// canonical state paths; event digests and fencing high-water marks survive
// restoration so retries and late workers remain safely fenced.
type Snapshot struct {
	SchemaVersion       int                             `json:"schema_version"`
	Generation          uint64                          `json:"generation"`
	AppliedEvents       map[string]string               `json:"applied_events"`
	FencingTokens       map[string]uint64               `json:"fencing_tokens"`
	Records             map[string]Record               `json:"records"`
	LayoutVersion       string                          `json:"layout_version,omitempty"`
	LayoutMigrationBase string                          `json:"layout_migration_base,omitempty"`
	Aggregates          map[string]AggregateTransaction `json:"aggregates"`
}

// Record is an explicitly typed, versioned JSON document stored under state/.
type Record struct {
	Kind          string          `json:"kind"`
	SchemaVersion int             `json:"schema_version"`
	Data          json.RawMessage `json:"data"`
}

// Event atomically replaces or deletes one typed record and optionally
// advances a resource's fencing high-water mark. A deletion carries the exact
// prior request record as its compare value.
type Event struct {
	ID         string `json:"id"`
	FenceKey   string `json:"fence_key,omitempty"`
	FenceToken uint64 `json:"fence_token,omitempty"`
	RecordPath string `json:"record_path"`
	Record     Record `json:"record"`
	Delete     bool   `json:"delete,omitempty"`
}

// NewSnapshot returns the empty supported state version.
func NewSnapshot() Snapshot {
	return Snapshot{
		SchemaVersion: SnapshotSchemaVersion,
		AppliedEvents: map[string]string{},
		FencingTokens: map[string]uint64{},
		Records:       map[string]Record{},
	}
}

// NewRequestDeletionEvent creates a compare-and-delete event for one exact
// obsolete request record. The event can remove only request records and
// fails if the record has changed since the caller read it.
func NewRequestDeletionEvent(recordPath string, expected Record) (Event, error) {
	event := Event{RecordPath: recordPath, Record: cloneRecord(expected), Delete: true}
	if err := validateEventRecord(event); err != nil {
		return Event{}, fmt.Errorf("validate request deletion: %w", err)
	}
	identityValue := struct {
		RecordPath string `json:"record_path"`
		Record     Record `json:"record"`
	}{recordPath, expected}
	eventID, err := identity.ID("state-delete-request", 1, identityValue)
	if err != nil {
		return Event{}, fmt.Errorf("identify request deletion: %w", err)
	}
	event.ID = eventID
	if _, err = stateEventDigest(event); err != nil {
		return Event{}, fmt.Errorf("identify request deletion: %w", err)
	}
	return event, nil
}

// ReadSnapshot reads and validates state at one immutable backend head.
func ReadSnapshot(ctx context.Context, backend Backend) (string, Snapshot, error) {
	if backend == nil {
		return "", Snapshot{}, fmt.Errorf("state backend is required")
	}
	ctx, finish := progress.Start(ctx, "state", "snapshot_read")
	defer finish(false)
	head, snapshot, err := backend.Read(ctx)
	if err != nil {
		return "", Snapshot{}, fmt.Errorf("read state snapshot: %w", err)
	}
	if head == "" {
		return "", Snapshot{}, fmt.Errorf("state branch head is required")
	}
	snapshot = normalizeSnapshot(snapshot)
	if err = snapshot.Validate(); err != nil {
		return "", Snapshot{}, fmt.Errorf("validate state snapshot at %s: %w", head, err)
	}
	progress.Logger(ctx).InfoContext(ctx, "state snapshot loaded", "state_head", head, "records", len(snapshot.Records))
	finish(true)
	return head, cloneSnapshot(snapshot), nil
}

// ApplyEvent reads the latest snapshot, applies an idempotent event, then
// commits the complete transition against the exact head that was read. A
// head conflict is returned to the caller, which must reload and reapply.
// Production callers use TransactionCoordinator instead.
func ApplyEvent(ctx context.Context, backend Backend, event Event) (string, Snapshot, error) {
	return ApplyEvents(ctx, backend, []Event{event})
}

// ApplyEvents reduces an ordered event batch against one snapshot and commits
// all changed records in one expected-head transaction. Any invalid or stale
// event aborts the complete batch before the backend is called.
func ApplyEvents(ctx context.Context, backend Backend, events []Event) (string, Snapshot, error) {
	return applyEvents(ctx, backend, "", events)
}

// ApplyEventsAtHead is a fixture helper for expected-head state transitions.
func ApplyEventsAtHead(ctx context.Context, backend Backend, expectedHead string, events []Event) (string, Snapshot, error) {
	if expectedHead == "" {
		return "", Snapshot{}, fmt.Errorf("expected state head is required")
	}
	return applyEvents(ctx, backend, expectedHead, events)
}

func applyEvents(ctx context.Context, backend Backend, expectedHead string, events []Event) (string, Snapshot, error) {
	if len(events) == 0 {
		return "", Snapshot{}, fmt.Errorf("state event batch must not be empty")
	}
	head, current, err := ReadSnapshot(ctx, backend)
	if err != nil {
		return "", Snapshot{}, err
	}
	if expectedHead != "" && head != expectedHead {
		return "", Snapshot{}, ErrHeadMoved
	}
	next := current
	changed := false
	for index, event := range events {
		updated, eventChanged, applyErr := applyEvent(next, event)
		if applyErr != nil {
			return "", Snapshot{}, fmt.Errorf("apply state event %d: %w", index, applyErr)
		}
		next = updated
		changed = changed || eventChanged
	}
	if !changed {
		progress.Logger(ctx).InfoContext(ctx, "state commit skipped", "reason", "events already applied", "state_head", head)
		return head, current, nil
	}
	newHead, err := CommitExpectedHead(ctx, backend, head, next)
	if err != nil {
		return "", Snapshot{}, err
	}
	return newHead, next, nil
}

// CommitExpectedHead validates the complete snapshot and delegates an atomic
// compare-and-swap fast-forward commit to the backend.
func CommitExpectedHead(ctx context.Context, backend Backend, expectedHead string, snapshot Snapshot) (string, error) {
	if backend == nil {
		return "", fmt.Errorf("state backend is required")
	}
	if expectedHead == "" {
		return "", fmt.Errorf("expected state head is required")
	}
	if err := snapshot.Validate(); err != nil {
		return "", fmt.Errorf("validate state snapshot before commit: %w", err)
	}
	ctx, finish := progress.Start(ctx, "state", "expected_head_commit", slog.String("expected_head", expectedHead))
	defer finish(false)
	committer, ok := backend.(LegacyCommitBackend)
	if !ok {
		return "", fmt.Errorf("state backend does not support legacy commits")
	}
	newHead, err := committer.Commit(ctx, expectedHead, cloneSnapshot(snapshot))
	if err != nil {
		return "", fmt.Errorf("commit state snapshot: %w", err)
	}
	if newHead == "" || newHead == expectedHead {
		return "", fmt.Errorf("state backend returned invalid new head %q", newHead)
	}
	progress.Logger(ctx).InfoContext(ctx, "state committed", "state_head", newHead)
	finish(true)
	return newHead, nil
}

// Validate checks snapshot metadata, idempotency history, fencing marks, and
// every typed record path before any state can be committed or consumed.
func (snapshot Snapshot) Validate() error {
	if snapshot.SchemaVersion != SnapshotSchemaVersion {
		return fmt.Errorf("unsupported state snapshot schema version %d", snapshot.SchemaVersion)
	}
	if snapshot.AppliedEvents == nil || snapshot.FencingTokens == nil || snapshot.Records == nil {
		return fmt.Errorf("state snapshot maps must be present")
	}
	if err := validateAppliedEvents(snapshot.AppliedEvents); err != nil {
		return err
	}
	if err := validateFencingTokens(snapshot.FencingTokens); err != nil {
		return err
	}
	if err := validateAggregates(snapshot); err != nil {
		return err
	}
	return validateSnapshotRecords(snapshot.Records)
}

func validateAppliedEvents(events map[string]string) error {
	for eventID, digest := range events {
		if !eventIDPattern.MatchString(eventID) {
			return fmt.Errorf("invalid state event ID %q", eventID)
		}
		if len(digest) != 64 {
			return fmt.Errorf("event digest for %q must be a SHA-256", eventID)
		}
		if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
			return fmt.Errorf("event digest for %q must be lowercase hexadecimal", eventID)
		}
	}
	return nil
}

func validateFencingTokens(tokens map[string]uint64) error {
	for key, token := range tokens {
		if !fenceKeyPattern.MatchString(key) || token == 0 {
			return fmt.Errorf("invalid fencing high-water mark for %q", key)
		}
	}
	return nil
}

func validateSnapshotRecords(records map[string]Record) error {
	for recordPath, record := range records {
		if err := validateRecord(recordPath, record); err != nil {
			return fmt.Errorf("invalid state record %q: %w", recordPath, err)
		}
	}
	return nil
}

func applyEvent(snapshot Snapshot, event Event) (Snapshot, bool, error) {
	if err := snapshot.Validate(); err != nil {
		return Snapshot{}, false, fmt.Errorf("validate current state: %w", err)
	}
	digest, err := stateEventDigest(event)
	if err != nil {
		return Snapshot{}, false, err
	}
	if previous, exists := snapshot.AppliedEvents[event.ID]; exists {
		if previous != digest {
			return Snapshot{}, false, ErrEventConflict
		}
		return snapshot, false, nil
	}
	if err := validateImmutableHandoffReplacement(snapshot, event); err != nil {
		return Snapshot{}, false, err
	}
	if event.FenceKey != "" && event.FenceToken < snapshot.FencingTokens[event.FenceKey] {
		return Snapshot{}, false, ErrStaleFence
	}
	if snapshot.Generation == math.MaxUint64 {
		return Snapshot{}, false, fmt.Errorf("state generation is exhausted")
	}
	next := cloneSnapshot(snapshot)
	next.Generation++
	next.AppliedEvents[event.ID] = digest
	if event.FenceKey != "" && event.FenceToken > next.FencingTokens[event.FenceKey] {
		next.FencingTokens[event.FenceKey] = event.FenceToken
	}
	if event.Delete {
		current, exists := snapshot.Records[event.RecordPath]
		if !exists || current.Kind != event.Record.Kind || current.SchemaVersion != event.Record.SchemaVersion || !bytes.Equal(current.Data, event.Record.Data) {
			return Snapshot{}, false, ErrRecordChanged
		}
		delete(next.Records, event.RecordPath)
	} else {
		next.Records[event.RecordPath] = cloneRecord(event.Record)
	}
	return finalizeAggregateEvent(next, event, digest)
}

func stateEventDigest(event Event) (string, error) {
	if !eventIDPattern.MatchString(event.ID) {
		return "", fmt.Errorf("invalid state event ID %q", event.ID)
	}
	if err := validateEventRecord(event); err != nil {
		return "", fmt.Errorf("invalid event record: %w", err)
	}
	if event.FenceKey == "" {
		if event.FenceToken != 0 {
			return "", fmt.Errorf("fencing token requires a fence key")
		}
	} else if !fenceKeyPattern.MatchString(event.FenceKey) || event.FenceToken == 0 {
		return "", fmt.Errorf("fence key and positive fencing token are required")
	}
	digest, err := identity.ID("state-event", 1, event)
	if err != nil {
		return "", fmt.Errorf("identify state event: %w", err)
	}
	return strings.TrimPrefix(digest, "state-event-"), nil
}

func validateEventRecord(event Event) error {
	if err := validateRecord(event.RecordPath, event.Record); err != nil {
		return err
	}
	if event.Delete && event.Record.Kind != "request" {
		return fmt.Errorf("only obsolete request records may be deleted by an event")
	}
	return nil
}

func validateImmutableHandoffReplacement(snapshot Snapshot, event Event) error {
	if event.Record.Kind != "stage_handoff" {
		return nil
	}
	existing, exists := snapshot.Records[event.RecordPath]
	if exists && !bytes.Equal(existing.Data, event.Record.Data) {
		return fmt.Errorf("immutable stage handoff cannot be replaced")
	}
	return nil
}

func finalizeAggregateEvent(next Snapshot, event Event, digest string) (Snapshot, bool, error) {
	if err := recordAggregateEvent(&next, event, digest); err != nil {
		return Snapshot{}, false, err
	}
	return next, true, nil
}

func validateRecord(recordPath string, record Record) error {
	if err := identity.ValidateRelativePath(recordPath, false); err != nil {
		return err
	}
	if !strings.HasPrefix(recordPath, "state/") || path.Ext(recordPath) != ".json" {
		return fmt.Errorf("record path must be a JSON file beneath state/")
	}
	expectedKind, ok := stateRecordKind(recordPath)
	if !ok {
		return fmt.Errorf("record path is not an allowed operational state path")
	}
	if record.Kind != expectedKind {
		return fmt.Errorf("record kind %q does not match path kind %q", record.Kind, expectedKind)
	}
	if record.SchemaVersion != 1 {
		return fmt.Errorf("unsupported %s schema version %d", record.Kind, record.SchemaVersion)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(record.Data, &object); err != nil {
		return fmt.Errorf("decode record data: %w", err)
	}
	if object == nil {
		return fmt.Errorf("record data must be a JSON object")
	}
	var decoded map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(record.Data))
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("decode record data: %w", err)
	}
	if decoded == nil {
		return fmt.Errorf("record data must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("record data must contain one JSON document")
	}
	if record.Kind == "operation_receipt" {
		var receipt OperationReceipt
		if err := json.Unmarshal(record.Data, &receipt); err != nil {
			return fmt.Errorf("decode operation receipt: %w", err)
		}
		if err := receipt.Validate(recordPath); err != nil {
			return err
		}
	}
	if record.Kind == "discovery_work" {
		var work DiscoveryWork
		if err := json.Unmarshal(record.Data, &work); err != nil {
			return fmt.Errorf("decode discovery work: %w", err)
		}
		if err := work.Validate(recordPath); err != nil {
			return err
		}
	}
	if record.Kind == "module_publication_ack" {
		parts := strings.Split(recordPath, "/")
		if len(parts) != 5 {
			return fmt.Errorf("module publication acknowledgement path is invalid")
		}
		if _, err := decodeModuleDeploymentAcknowledgement(record, recordPath, parts[3]); err != nil {
			return err
		}
	}
	if err := validateProjectionRecord(recordPath, record); err != nil {
		return err
	}
	return nil
}

func validateProjectionRecord(recordPath string, record Record) error {
	switch record.Kind {
	case "stage_handoff":
		return validateStageHandoffRecord(recordPath, record.Data)
	case "projection_acknowledgement":
		return validateProjectionAcknowledgementRecord(recordPath, record.Data)
	case "projection_barrier_release":
		return validateProjectionBarrierReleaseRecord(recordPath, record.Data)
	}
	return nil
}

func validateStageHandoffRecord(recordPath string, data json.RawMessage) error {
	var handoff projection.Handoff
	if err := json.Unmarshal(data, &handoff); err != nil {
		return fmt.Errorf("decode stage handoff: %w", err)
	}
	if err := handoff.Validate(); err != nil {
		return err
	}
	expected, err := identity.StateProjectionHandoffPath(handoff.ModuleID, handoff.ID)
	if err != nil || recordPath != expected {
		return fmt.Errorf("stage handoff path does not match its module and identity")
	}
	return nil
}

func validateProjectionAcknowledgementRecord(recordPath string, data json.RawMessage) error {
	var acknowledgement projection.Acknowledgement
	if err := json.Unmarshal(data, &acknowledgement); err != nil {
		return fmt.Errorf("decode projection acknowledgement: %w", err)
	}
	if err := validateProjectionIdentity(acknowledgement.Projection); err != nil {
		return err
	}
	if err := acknowledgement.Validate(acknowledgement.Projection, acknowledgement.AcknowledgedAt); err != nil {
		return err
	}
	expected, err := identity.StateProjectionAcknowledgementPath(acknowledgement.Projection.ModuleID, acknowledgement.Projection.IntentID)
	if err != nil || recordPath != expected {
		return fmt.Errorf("projection acknowledgement path does not match its exact tuple")
	}
	return nil
}

func validateProjectionBarrierReleaseRecord(recordPath string, data json.RawMessage) error {
	var release projection.BarrierRelease
	if err := json.Unmarshal(data, &release); err != nil {
		return fmt.Errorf("decode projection barrier release: %w", err)
	}
	if err := validateProjectionIdentity(release.Projection); err != nil {
		return err
	}
	if err := release.Validate(release.Projection, release.Deadline, release.ReleasedAt); err != nil {
		return err
	}
	expected, err := identity.StateProjectionBarrierReleasePath(release.Projection.ModuleID, release.Projection.IntentID)
	if err != nil || recordPath != expected {
		return fmt.Errorf("projection barrier release path does not match its exact tuple")
	}
	return nil
}

func validateProjectionIdentity(projectionIdentity projection.ProjectionIdentity) error {
	if _, err := identity.OperationalModuleRoot(projectionIdentity.ModuleID); err != nil || projectionIdentity.Generation == 0 {
		return fmt.Errorf("projection tuple has an invalid module or generation")
	}
	if projectionIdentity.TransitionID == "" ||
		identity.ValidateRecordID("projection-intent", projectionIdentity.IntentID) != nil ||
		identity.ValidateRecordID("projection-fingerprint", projectionIdentity.Fingerprint) != nil {
		return fmt.Errorf("projection tuple is incomplete")
	}
	return nil
}

func stateRecordKind(recordPath string) (string, bool) {
	parts := strings.Split(recordPath, "/")
	if !validOperationalPath(parts, recordPath) {
		return "", false
	}
	if kind, found := specialStateRecordKind(recordPath, parts); found {
		return kind, true
	}
	rule, exists := statePathRules[strings.Join(parts[1:len(parts)-1], "/")]
	if !exists || len(parts) != rule.segmentCount {
		return "", false
	}
	return rule.kind, true
}

func validOperationalPath(parts []string, recordPath string) bool {
	if len(parts) < 3 || parts[0] != "state" {
		return false
	}
	filename := parts[len(parts)-1]
	return filename != ".json" && strings.HasSuffix(filename, ".json")
}

func specialStateRecordKind(recordPath string, parts []string) (string, bool) {
	switch {
	case recordPath == "state/discovery/catalog.json":
		return "discovery_catalog", true
	case recordPath == "state/operations/global.json":
		return "global_control", true
	case len(parts) == 4 && parts[1] == "attempts":
		return "attempt", true
	case len(parts) >= 5 && parts[1] == "selections":
		return "selection", true
	case len(parts) == 5 && parts[1] == "operations" && parts[2] == "receipts":
		return "operation_receipt", true
	case len(parts) == 5 && parts[1] == "discovery" && parts[2] == "work":
		return "discovery_work", true
	case len(parts) == 5 && parts[1] == "projection" && parts[2] == "handoffs":
		return "stage_handoff", true
	case len(parts) == 5 && parts[1] == "projection" && parts[2] == "acknowledgements":
		return "projection_acknowledgement", true
	case len(parts) == 5 && parts[1] == "projection" && parts[2] == "barrier-releases":
		return "projection_barrier_release", true
	case len(parts) == 5 && parts[1] == "publications" && parts[2] == "modules":
		return "module_publication_ack", true
	default:
		return "", false
	}
}

type statePathRule struct {
	segmentCount int
	kind         string
}

var statePathRules = map[string]statePathRule{
	"discovery/source-pins":  {segmentCount: 4, kind: "source_pins"},
	"discovery/work":         {segmentCount: 4, kind: "discovery_work"},
	"discovery/observations": {segmentCount: 4, kind: "discovery_observation"},
	"discovery/cycles":       {segmentCount: 4, kind: "discovery_cycle"},
	"modules":                {segmentCount: 3, kind: "module"},
	"lifecycle/slots":        {segmentCount: 4, kind: "lifecycle_slot"},
	"requests":               {segmentCount: 3, kind: "request"},
	"exports":                {segmentCount: 3, kind: "export"},
	"providers":              {segmentCount: 3, kind: "provider"},
	"operations/commands":    {segmentCount: 4, kind: "operational_command"},
	"operations/receipts":    {segmentCount: 5, kind: "operation_receipt"},
	"operations/modules":     {segmentCount: 4, kind: "module_control"},
	"operations/wakeups":     {segmentCount: 4, kind: "operational_wakeup"},
	"inputs":                 {segmentCount: 3, kind: "input_import"},
	"projection/intents":     {segmentCount: 4, kind: "projection_intent"},
	"projection/lifecycle":   {segmentCount: 4, kind: "projection_lifecycle"},
	"projection/delivery":    {segmentCount: 4, kind: "projection_delivery"},
	"projection/bindings":    {segmentCount: 4, kind: "projection_binding"},
	"projection/batches":     {segmentCount: 4, kind: "projection_batch"},
	"publications":           {segmentCount: 3, kind: "publication"},
	"health":                 {segmentCount: 3, kind: "health"},
}

func normalizeSnapshot(snapshot Snapshot) Snapshot {
	if snapshot.SchemaVersion == 0 && snapshot.Generation == 0 && snapshot.AppliedEvents == nil && snapshot.FencingTokens == nil && snapshot.Records == nil {
		return NewSnapshot()
	}
	return snapshot
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	clone := snapshot
	if snapshot.Aggregates != nil {
		clone.Aggregates = make(map[string]AggregateTransaction, len(snapshot.Aggregates))
		for owner, aggregate := range snapshot.Aggregates {
			clonedAggregate := emptyAggregate()
			clonedAggregate.Generation = aggregate.Generation
			for eventID, digest := range aggregate.AppliedEvents {
				clonedAggregate.AppliedEvents[eventID] = digest
			}
			for key, token := range aggregate.FencingTokens {
				clonedAggregate.FencingTokens[key] = token
			}
			clone.Aggregates[owner] = clonedAggregate
		}
	}
	clone.AppliedEvents = make(map[string]string, len(snapshot.AppliedEvents))
	for eventID, digest := range snapshot.AppliedEvents {
		clone.AppliedEvents[eventID] = digest
	}
	clone.FencingTokens = make(map[string]uint64, len(snapshot.FencingTokens))
	for key, token := range snapshot.FencingTokens {
		clone.FencingTokens[key] = token
	}
	clone.Records = make(map[string]Record, len(snapshot.Records))
	for recordPath, record := range snapshot.Records {
		clone.Records[recordPath] = cloneRecord(record)
	}
	return clone
}

func cloneRecord(record Record) Record {
	clone := record
	clone.Data = append(json.RawMessage(nil), record.Data...)
	return clone
}
