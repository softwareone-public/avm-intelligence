// Package projection defines neutral desired-state and lifecycle handoff
// contracts. It has no GitHub client or remote-delivery implementation.
package projection

import (
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

const (
	// SchemaVersion is the current version for neutral projection records.
	SchemaVersion = 1
	// BarrierTimeout fixes the default wait for the exact projection acknowledgement.
	BarrierTimeout = 15 * time.Minute
	// AcknowledgementDone is the only status that proves a projection was delivered.
	AcknowledgementDone = "projection_done"
	// BarrierReleased records timeout release without claiming projection success.
	BarrierReleased             = "projection_barrier_released"
	intentRecordKind            = "projection-intent"
	fingerprintRecordKind       = "projection-fingerprint"
	effectRecordKind            = "projection-effect"
	effectFingerprintRecordKind = "effect-fingerprint"
	inputFingerprintRecordKind  = "input-fingerprint"
	handoffRecordKind           = "stage-handoff"
)

var (
	fieldNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	workflowIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	stagePattern        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	transitionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	statusPattern       = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	sha1Pattern         = regexp.MustCompile(`^[0-9a-f]{40}$`)
	allowedStages       = map[string]struct{}{
		"acquire_source": {},
		"assessment":     {},
		"acceptance":     {},
		"selection":      {},
		"publication":    {},
		"pages":          {},
	}
)

// OwnedField is one factory-owned, human-facing projection value.
type OwnedField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// EvidenceEffect identifies a required independent evidence projection effect.
type EvidenceEffect struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}

// DesiredIntent is the module-scoped desired projection, separate from
// projector-owned bindings, leases, retries and remote acknowledgements.
type DesiredIntent struct {
	SchemaVersion   int              `json:"schema_version"`
	ModuleID        string           `json:"module_id"`
	Generation      uint64           `json:"desired_generation"`
	TransitionID    string           `json:"transition_id"`
	Status          string           `json:"status"`
	OwnedFields     []OwnedField     `json:"owned_fields"`
	EvidenceEffects []EvidenceEffect `json:"evidence_effects"`
	IntentID        string           `json:"intent_id"`
	Fingerprint     string           `json:"fingerprint"`
}

type desiredFingerprintInput struct {
	ModuleID        string           `json:"module_id"`
	Generation      uint64           `json:"desired_generation"`
	TransitionID    string           `json:"transition_id"`
	Status          string           `json:"status"`
	OwnedFields     []OwnedField     `json:"owned_fields"`
	EvidenceEffects []EvidenceEffect `json:"evidence_effects"`
}

// NewDesiredIntent creates a deterministic desired projection and validates
// its module owner, fields and independently identified evidence effects.
func NewDesiredIntent(moduleID string, generation uint64, transitionID, status string, fields []OwnedField, effects []EvidenceEffect) (DesiredIntent, error) {
	intent := DesiredIntent{
		SchemaVersion: SchemaVersion, ModuleID: moduleID, Generation: generation,
		TransitionID: transitionID, Status: status,
		OwnedFields:     append([]OwnedField(nil), fields...),
		EvidenceEffects: append([]EvidenceEffect(nil), effects...),
	}
	intent.normalize()
	if err := intent.validateContent(); err != nil {
		return DesiredIntent{}, err
	}
	intentID, err := identity.ID(intentRecordKind, SchemaVersion, intent.fingerprintInput())
	if err != nil {
		return DesiredIntent{}, fmt.Errorf("identify desired projection: %w", err)
	}
	fingerprint, err := identity.ID(fingerprintRecordKind, SchemaVersion, intent.fingerprintInput())
	if err != nil {
		return DesiredIntent{}, fmt.Errorf("fingerprint desired projection: %w", err)
	}
	intent.IntentID, intent.Fingerprint = intentID, fingerprint
	return intent, nil
}

// Validate checks the stable identity and canonical contents of a desired intent.
func (intent DesiredIntent) Validate() error {
	if intent.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported projection intent schema version %d", intent.SchemaVersion)
	}
	if err := intent.validateContent(); err != nil {
		return err
	}
	expectedID, err := identity.ID(intentRecordKind, SchemaVersion, intent.fingerprintInput())
	if err != nil {
		return fmt.Errorf("identify desired projection: %w", err)
	}
	expectedFingerprint, err := identity.ID(fingerprintRecordKind, SchemaVersion, intent.fingerprintInput())
	if err != nil {
		return fmt.Errorf("fingerprint desired projection: %w", err)
	}
	if intent.IntentID != expectedID || intent.Fingerprint != expectedFingerprint {
		return fmt.Errorf("projection intent fingerprint does not match its contents")
	}
	return nil
}

// ValidatePath proves that the intent uses the established module-owned logical path.
func (intent DesiredIntent) ValidatePath(recordPath string) error {
	if err := intent.Validate(); err != nil {
		return err
	}
	expected, err := identity.StateProjectionIntentPath(intent.ModuleID)
	if err != nil {
		return fmt.Errorf("resolve projection intent path: %w", err)
	}
	if recordPath != expected {
		return fmt.Errorf("projection intent path does not match module owner")
	}
	return nil
}

// Identity returns the exact tuple consumers and acknowledgements must bind to.
func (intent DesiredIntent) Identity() (ProjectionIdentity, error) {
	if err := intent.Validate(); err != nil {
		return ProjectionIdentity{}, err
	}
	return ProjectionIdentity{
		ModuleID: intent.ModuleID, Generation: intent.Generation,
		TransitionID: intent.TransitionID, IntentID: intent.IntentID,
		Fingerprint: intent.Fingerprint,
	}, nil
}

func (intent *DesiredIntent) normalize() {
	sort.Slice(intent.OwnedFields, func(i, j int) bool { return intent.OwnedFields[i].Name < intent.OwnedFields[j].Name })
	sort.Slice(intent.EvidenceEffects, func(i, j int) bool { return intent.EvidenceEffects[i].ID < intent.EvidenceEffects[j].ID })
}

func (intent DesiredIntent) fingerprintInput() desiredFingerprintInput {
	return desiredFingerprintInput{
		ModuleID: intent.ModuleID, Generation: intent.Generation,
		TransitionID: intent.TransitionID, Status: intent.Status,
		OwnedFields: intent.OwnedFields, EvidenceEffects: intent.EvidenceEffects,
	}
}

func (intent DesiredIntent) validateContent() error {
	if err := validateModuleOwner(intent.ModuleID); err != nil {
		return fmt.Errorf("invalid projection module owner: %w", err)
	}
	if intent.Generation == 0 || !transitionIDPattern.MatchString(intent.TransitionID) || !statusPattern.MatchString(intent.Status) {
		return fmt.Errorf("projection generation, transition ID and status are required")
	}
	if err := validateOwnedFields(intent.OwnedFields); err != nil {
		return err
	}
	return validateEvidenceEffects(intent.EvidenceEffects)
}

func validateModuleOwner(moduleID string) error {
	_, err := identity.OperationalModuleRoot(moduleID)
	return err
}

func validateOwnedFields(fields []OwnedField) error {
	for index, field := range fields {
		if !fieldNamePattern.MatchString(field.Name) || field.Value == "" {
			return fmt.Errorf("invalid factory-owned projection field")
		}
		if index > 0 && fields[index-1].Name == field.Name {
			return fmt.Errorf("duplicate factory-owned projection field %q", field.Name)
		}
	}
	return nil
}

func validateEvidenceEffects(effects []EvidenceEffect) error {
	for index, effect := range effects {
		if err := identity.ValidateRecordID(effectRecordKind, effect.ID); err != nil {
			return fmt.Errorf("invalid evidence effect ID: %w", err)
		}
		if !statusPattern.MatchString(effect.Kind) {
			return fmt.Errorf("invalid evidence effect kind")
		}
		if err := identity.ValidateRecordID(effectFingerprintRecordKind, effect.Fingerprint); err != nil {
			return fmt.Errorf("invalid evidence effect fingerprint: %w", err)
		}
		if index > 0 && effects[index-1].ID == effect.ID {
			return fmt.Errorf("duplicate evidence effect ID %q", effect.ID)
		}
	}
	return nil
}

// ProjectionIdentity is the exact tuple required for barrier evaluation and acknowledgement.
type ProjectionIdentity struct {
	ModuleID     string `json:"module_id"`
	Generation   uint64 `json:"desired_generation"`
	TransitionID string `json:"transition_id"`
	IntentID     string `json:"projection_intent_id"`
	Fingerprint  string `json:"projection_fingerprint"`
}

// Validate checks a projection identity against its desired intent.
func (projection ProjectionIdentity) Validate(intent DesiredIntent) error {
	if err := intent.Validate(); err != nil {
		return err
	}
	if projection.ModuleID != intent.ModuleID || projection.Generation != intent.Generation ||
		projection.TransitionID != intent.TransitionID || projection.IntentID != intent.IntentID ||
		projection.Fingerprint != intent.Fingerprint {
		return fmt.Errorf("projection identity does not match desired intent")
	}
	if err := identity.ValidateRecordID(intentRecordKind, projection.IntentID); err != nil {
		return fmt.Errorf("invalid projection intent ID: %w", err)
	}
	return nil
}

// WorkflowProvenance identifies reviewed producer code and records run metadata
// without making the workflow run ID part of stable logical operation identity.
type WorkflowProvenance struct {
	WorkflowID string `json:"workflow_id"`
	Commit     string `json:"commit"`
	RunID      string `json:"run_id"`
	RunAttempt uint64 `json:"run_attempt"`
}

// Validate checks producer workflow identity and run provenance independently
// of whether a handoff is new or being replayed.
func (provenance WorkflowProvenance) Validate() error {
	if !workflowIDPattern.MatchString(provenance.WorkflowID) || !validSHA1(provenance.Commit) || provenance.RunID == "" || provenance.RunAttempt == 0 {
		return fmt.Errorf("workflow producer provenance is incomplete")
	}
	return nil
}

// Handoff is a durable producer-to-consumer contract bound to one projection identity.
type Handoff struct {
	SchemaVersion    int                `json:"schema_version"`
	ID               string             `json:"intent_id"`
	ModuleID         string             `json:"module_id"`
	Generation       uint64             `json:"desired_generation"`
	TransitionID     string             `json:"transition_id"`
	InputFingerprint string             `json:"input_fingerprint"`
	OperationID      string             `json:"operation_id"`
	AcceptedFactsID  string             `json:"accepted_facts_id,omitempty"`
	Producer         WorkflowProvenance `json:"producer"`
	ConsumerStage    string             `json:"consumer_stage"`
	Projection       ProjectionIdentity `json:"required_projection"`
	CreatedAt        time.Time          `json:"created_at"`
	BarrierDeadline  time.Time          `json:"barrier_deadline"`
}

type handoffIdentityInput struct {
	ModuleID         string             `json:"module_id"`
	Generation       uint64             `json:"desired_generation"`
	TransitionID     string             `json:"transition_id"`
	InputFingerprint string             `json:"input_fingerprint"`
	OperationID      string             `json:"operation_id"`
	AcceptedFactsID  string             `json:"accepted_facts_id,omitempty"`
	ConsumerStage    string             `json:"consumer_stage"`
	Projection       ProjectionIdentity `json:"required_projection"`
}

// NewHandoff creates a replay-stable handoff whose deadline is fixed from its
// original creation time; workflow run provenance does not alter its identity.
func NewHandoff(input Handoff, now time.Time) (Handoff, error) {
	input.SchemaVersion = SchemaVersion
	input.CreatedAt = now.UTC()
	input.BarrierDeadline = input.CreatedAt.Add(BarrierTimeout)
	if err := input.validateContent(); err != nil {
		return Handoff{}, err
	}
	handoffID, err := identity.ID(handoffRecordKind, SchemaVersion, input.identityInput())
	if err != nil {
		return Handoff{}, fmt.Errorf("identify stage handoff: %w", err)
	}
	input.ID = handoffID
	return input, nil
}

// Validate checks the stable handoff identity and fixed timeout contract.
func (handoff Handoff) Validate() error {
	if handoff.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported stage handoff schema version %d", handoff.SchemaVersion)
	}
	if err := handoff.validateContent(); err != nil {
		return err
	}
	expected, err := identity.ID(handoffRecordKind, SchemaVersion, handoff.identityInput())
	if err != nil {
		return fmt.Errorf("fingerprint stage handoff: %w", err)
	}
	if handoff.ID != expected {
		return fmt.Errorf("stage handoff ID does not match its logical identity")
	}
	return nil
}

func (handoff Handoff) identityInput() handoffIdentityInput {
	return handoffIdentityInput{
		ModuleID: handoff.ModuleID, Generation: handoff.Generation,
		TransitionID: handoff.TransitionID, InputFingerprint: handoff.InputFingerprint,
		OperationID: handoff.OperationID, AcceptedFactsID: handoff.AcceptedFactsID, ConsumerStage: handoff.ConsumerStage,
		Projection: handoff.Projection,
	}
}

func (handoff Handoff) validateContent() error {
	if _, err := identity.OperationalModuleRoot(handoff.ModuleID); err != nil {
		return fmt.Errorf("invalid handoff module owner: %w", err)
	}
	if handoff.Generation == 0 || !transitionIDPattern.MatchString(handoff.TransitionID) || handoff.CreatedAt.IsZero() ||
		handoff.BarrierDeadline.IsZero() || !handoff.BarrierDeadline.Equal(handoff.CreatedAt.Add(BarrierTimeout)) {
		return fmt.Errorf("handoff identity and fixed barrier deadline are required")
	}
	if identity.ValidateRecordID(inputFingerprintRecordKind, handoff.InputFingerprint) != nil {
		return fmt.Errorf("invalid handoff input fingerprint")
	}
	if err := identity.ValidateOperationalID(handoff.OperationID); err != nil {
		return fmt.Errorf("invalid handoff operation ID: %w", err)
	}
	if _, err := identity.StateOperationReceiptPath(handoff.ModuleID, handoff.OperationID); err != nil {
		return fmt.Errorf("invalid operation receipt reference: %w", err)
	}
	if !stagePattern.MatchString(handoff.ConsumerStage) {
		return fmt.Errorf("invalid handoff consumer stage")
	}
	if _, exists := allowedStages[handoff.ConsumerStage]; !exists {
		return fmt.Errorf("handoff consumer stage is not allowlisted")
	}
	if err := handoff.Producer.Validate(); err != nil {
		return fmt.Errorf("handoff producer provenance is incomplete: %w", err)
	}
	if handoff.Projection.ModuleID != handoff.ModuleID || handoff.Projection.Generation != handoff.Generation ||
		handoff.Projection.TransitionID != handoff.TransitionID ||
		!validRecordFingerprint(handoff.Projection.Fingerprint) {
		return fmt.Errorf("handoff projection identity does not match its owner and transition")
	}
	if err := identity.ValidateRecordID(intentRecordKind, handoff.Projection.IntentID); err != nil {
		return fmt.Errorf("invalid handoff projection intent ID: %w", err)
	}
	return nil
}

// Acknowledgement proves only the exact desired projection tuple was delivered.
type Acknowledgement struct {
	SchemaVersion  int                `json:"schema_version"`
	Projection     ProjectionIdentity `json:"projection"`
	Status         string             `json:"status"`
	AcknowledgedAt time.Time          `json:"acknowledged_at"`
}

// Validate checks exact tuple equality and rejects acknowledgements for another generation.
func (acknowledgement Acknowledgement) Validate(required ProjectionIdentity, now time.Time) error {
	if acknowledgement.SchemaVersion != SchemaVersion || acknowledgement.Status != AcknowledgementDone ||
		acknowledgement.AcknowledgedAt.IsZero() || now.IsZero() || acknowledgement.AcknowledgedAt.After(now) {
		return fmt.Errorf("projection acknowledgement is incomplete or not yet observed")
	}
	if acknowledgement.Projection != required {
		return fmt.Errorf("projection acknowledgement does not match the required tuple")
	}
	return nil
}

// BarrierRelease records timeout eligibility without synthesizing an acknowledgement.
type BarrierRelease struct {
	SchemaVersion int                `json:"schema_version"`
	Projection    ProjectionIdentity `json:"projection"`
	Status        string             `json:"status"`
	Deadline      time.Time          `json:"deadline"`
	ReleasedAt    time.Time          `json:"released_at"`
}

// Validate accepts release only at or after the durable fixed deadline.
func (release BarrierRelease) Validate(required ProjectionIdentity, deadline, now time.Time) error {
	if release.SchemaVersion != SchemaVersion || release.Status != BarrierReleased ||
		release.Deadline.IsZero() || deadline.IsZero() || !release.Deadline.Equal(deadline) ||
		release.ReleasedAt.IsZero() || now.IsZero() ||
		release.ReleasedAt.Before(release.Deadline) || release.ReleasedAt.After(now) {
		return fmt.Errorf("projection barrier release is premature or incomplete")
	}
	if release.Projection != required {
		return fmt.Errorf("projection barrier release does not match the required tuple")
	}
	return nil
}

func validRecordFingerprint(value string) bool {
	return identity.ValidateRecordID(fingerprintRecordKind, value) == nil
}

func validSHA1(value string) bool {
	return sha1Pattern.MatchString(value)
}
