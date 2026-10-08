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
)

const SnapshotSchemaVersion = 1

var (
	ErrHeadMoved     = errors.New("state branch head moved")
	ErrEventConflict = errors.New("event ID was reused with different content")
	ErrStaleFence    = errors.New("event fencing token is stale")

	eventIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	fenceKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// Backend reads one immutable snapshot and commits only when expectedHead is
// still the current branch tip. Commit must create a fast-forward child of that
// exact head and must never force-update the branch.
type Backend interface {
	Read(context.Context) (head string, snapshot Snapshot, err error)
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

// TransactionReplayValidator verifies fixed caller inputs against an already
// durable operation before its result is returned without rerunning eligibility.
type TransactionReplayValidator func(Snapshot) error

// TransactionResult carries the domain result and verified state head.
type TransactionResult struct {
	Head    string
	Value   any
	Changed bool
}

// TransactionCoordinator owns fetch, decision, validation, publication,
// reconciliation, and the bounded whole-decision retry budget.
type TransactionCoordinator interface {
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

// Event atomically replaces one typed record and optionally advances a
// resource's fencing high-water mark.
type Event struct {
	ID         string `json:"id"`
	FenceKey   string `json:"fence_key,omitempty"`
	FenceToken uint64 `json:"fence_token,omitempty"`
	RecordPath string `json:"record_path"`
	Record     Record `json:"record"`
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
func ApplyEvent(ctx context.Context, backend Backend, event Event) (string, Snapshot, error) {
	return ApplyEvents(ctx, backend, []Event{event})
}

// ApplyEvents reduces an ordered event batch against one snapshot and commits
// all changed records in one expected-head transaction. Any invalid or stale
// event aborts the complete batch before the backend is called.
func ApplyEvents(ctx context.Context, backend Backend, events []Event) (string, Snapshot, error) {
	return applyEvents(ctx, backend, "", events)
}

// ApplyEventsAtHead commits the batch only if the branch still matches the
// snapshot head used to prepare the events.
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
	newHead, err := backend.Commit(ctx, expectedHead, cloneSnapshot(snapshot))
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
	if !eventIDPattern.MatchString(event.ID) {
		return Snapshot{}, false, fmt.Errorf("invalid state event ID %q", event.ID)
	}
	if err := validateRecord(event.RecordPath, event.Record); err != nil {
		return Snapshot{}, false, fmt.Errorf("invalid event record: %w", err)
	}
	if event.FenceKey == "" {
		if event.FenceToken != 0 {
			return Snapshot{}, false, fmt.Errorf("fencing token requires a fence key")
		}
	} else if !fenceKeyPattern.MatchString(event.FenceKey) || event.FenceToken == 0 {
		return Snapshot{}, false, fmt.Errorf("fence key and positive fencing token are required")
	}
	digest, err := identity.ID("state-event", 1, event)
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("identify state event: %w", err)
	}
	digest = strings.TrimPrefix(digest, "state-event-")
	if previous, exists := snapshot.AppliedEvents[event.ID]; exists {
		if previous != digest {
			return Snapshot{}, false, ErrEventConflict
		}
		return snapshot, false, nil
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
	next.Records[event.RecordPath] = cloneRecord(event.Record)
	return finalizeAggregateEvent(next, event, digest)
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
	return nil
}

func stateRecordKind(recordPath string) (string, bool) {
	parts := strings.Split(recordPath, "/")
	if len(parts) < 3 || parts[0] != "state" {
		return "", false
	}
	filename := parts[len(parts)-1]
	if filename == ".json" || !strings.HasSuffix(filename, ".json") {
		return "", false
	}
	if recordPath == "state/discovery/catalog.json" {
		return "discovery_catalog", true
	}
	if recordPath == "state/operations/global.json" {
		return "global_control", true
	}
	if len(parts) == 4 && parts[1] == "attempts" {
		return "attempt", true
	}
	if len(parts) >= 5 && parts[1] == "selections" {
		return "selection", true
	}
	rule, exists := statePathRules[strings.Join(parts[1:len(parts)-1], "/")]
	if !exists || len(parts) != rule.segmentCount {
		return "", false
	}
	return rule.kind, true
}

type statePathRule struct {
	segmentCount int
	kind         string
}

var statePathRules = map[string]statePathRule{
	"discovery/source-pins":  {segmentCount: 4, kind: "source_pins"},
	"discovery/observations": {segmentCount: 4, kind: "discovery_observation"},
	"discovery/cycles":       {segmentCount: 4, kind: "discovery_cycle"},
	"modules":                {segmentCount: 3, kind: "module"},
	"requests":               {segmentCount: 3, kind: "request"},
	"exports":                {segmentCount: 3, kind: "export"},
	"providers":              {segmentCount: 3, kind: "provider"},
	"operations/commands":    {segmentCount: 4, kind: "operational_command"},
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
