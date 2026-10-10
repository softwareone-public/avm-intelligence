package queue

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

const (
	PhaseAcquireSource WorkPhase = "acquire_source"
	PhaseAssess        WorkPhase = "assess"

	RetryNone        RetryClass = "none"
	RetryTransient   RetryClass = "transient"
	RetryRateLimited RetryClass = "rate_limited"
	RetryBlocked     RetryClass = "blocked"
	RetryNeedsReview RetryClass = "needs_review"

	ApplicabilityApplicable    Applicability = "applicable"
	ApplicabilityNotApplicable Applicability = "not_applicable"
	ApplicabilityUnresolved    Applicability = "unresolved"

	CandidateEligible   CandidateState = "eligible"
	CandidateIneligible CandidateState = "ineligible"
	CandidateUnresolved CandidateState = "unresolved"

	LatestSourceContext = "latest-source"
)

var capabilityPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)

// WorkPhase distinguishes immutable source acquisition from offline assessment.
type WorkPhase string

// RetryClass categorizes processing readiness independently from assessment outcome.
type RetryClass string

// Applicability records whether a configured provider-major context applies.
type Applicability string

// CandidateState records eligibility separately from track applicability.
type CandidateState string

// WorkContract pins the phase-specific input vector and active ownership state.
type WorkContract struct {
	Phase             WorkPhase         `json:"phase"`
	AVMReleaseVersion string            `json:"avm_release_version,omitempty"`
	ContextID         string            `json:"context_id,omitempty"`
	Track             string            `json:"track,omitempty"`
	DesiredGeneration uint64            `json:"desired_generation"`
	RefreshGeneration uint64            `json:"refresh_generation,omitempty"`
	ExtractorContract string            `json:"extractor_contract,omitempty"`
	Capabilities      []string          `json:"capabilities,omitempty"`
	FactsID           string            `json:"facts_id,omitempty"`
	DefinitionIDs     []string          `json:"definition_ids,omitempty"`
	SnapshotIDs       []string          `json:"snapshot_ids,omitempty"`
	TargetVersions    map[string]string `json:"target_versions,omitempty"`
	Applicability     Applicability     `json:"applicability"`
	Candidate         CandidateState    `json:"candidate"`
	AcceptedFactsID   string            `json:"accepted_facts_id,omitempty"`
	AttemptID         string            `json:"attempt_id,omitempty"`
	Fence             uint64            `json:"fence"`
	Lease             *ComputeLease     `json:"lease,omitempty"`
	RetryClass        RetryClass        `json:"retry_class"`
	RetryAt           time.Time         `json:"retry_at,omitempty"`
	Completion        *SealedCompletion `json:"completion,omitempty"`
}

// AcquisitionInput contains only immutable source and extractor inputs. It
// intentionally has no Facts ID, track, or applicability requirement.
type AcquisitionInput struct {
	ModuleID          string
	SourceSHA         string
	AVMReleaseVersion string
	DesiredGeneration uint64
	RefreshGeneration uint64
	ExtractorContract string
	Capabilities      []string
}

// AssessmentInput pins already accepted facts and the context-specific inputs
// needed for offline evaluation. It does not request a source checkout.
type AssessmentInput struct {
	ModuleID          string
	SourceSHA         string
	AVMReleaseVersion string
	ContextID         string
	Track             string
	DesiredGeneration uint64
	FactsID           string
	DefinitionIDs     []string
	SnapshotIDs       []string
	TargetVersions    map[string]string
}

// ComputeLease binds one module request to a concrete owner and fence.
type ComputeLease struct {
	Owner     string    `json:"owner"`
	ExpiresAt time.Time `json:"expires_at"`
	Fence     uint64    `json:"fence"`
}

// SealedCompletion is the trusted validator's durable handoff. Its presence
// permits compute ownership to be released while human acceptance continues.
type SealedCompletion struct {
	ID                string    `json:"id"`
	RequestID         string    `json:"request_id"`
	InputFingerprint  string    `json:"input_fingerprint"`
	DesiredGeneration uint64    `json:"desired_generation"`
	Fence             uint64    `json:"fence"`
	ArtifactDigest    string    `json:"artifact_digest"`
	ValidationID      string    `json:"validation_id"`
	SealedAt          time.Time `json:"sealed_at"`
}

// TrackState keeps applicability and candidate eligibility as separate axes.
type TrackState struct {
	ContextID     string
	Track         string
	Applicability Applicability
	Candidate     CandidateState
}

// NewAcquireSourceRequest creates source-acquisition work without future facts
// or provider-track assumptions.
func NewAcquireSourceRequest(input AcquisitionInput, now time.Time) (Request, error) {
	if err := validateSourceIdentity(input.ModuleID, input.SourceSHA); err != nil {
		return Request{}, err
	}
	if input.DesiredGeneration == 0 || strings.TrimSpace(input.ExtractorContract) == "" || now.IsZero() {
		return Request{}, fmt.Errorf("acquisition generation, extractor contract, and timestamp are required")
	}
	capabilities, err := normalizeCapabilities(input.Capabilities)
	if err != nil {
		return Request{}, err
	}
	work := &WorkContract{
		Phase: PhaseAcquireSource, AVMReleaseVersion: input.AVMReleaseVersion, DesiredGeneration: input.DesiredGeneration, RefreshGeneration: input.RefreshGeneration,
		ExtractorContract: input.ExtractorContract, Capabilities: capabilities,
		Applicability: ApplicabilityUnresolved, Candidate: CandidateUnresolved, RetryClass: RetryNone,
	}
	fingerprint, err := workFingerprint(input.ModuleID, input.SourceSHA, *work)
	if err != nil {
		return Request{}, err
	}
	request := requestFor(input.ModuleID, input.SourceSHA, fingerprint, 1, now.UTC())
	request.Work = work
	return request, nil
}

// NewAssessmentRequest creates context-scoped offline assessment work pinned
// to accepted facts, definitions, and exact target/snapshot inputs.
func NewAssessmentRequest(input AssessmentInput, now time.Time) (Request, error) {
	if err := validateSourceIdentity(input.ModuleID, input.SourceSHA); err != nil {
		return Request{}, err
	}
	if input.DesiredGeneration == 0 || now.IsZero() || input.ContextID == "" || input.Track == "" {
		return Request{}, fmt.Errorf("assessment context, generation, and timestamp are required")
	}
	if err := identity.ValidateRelativePath(input.ContextID, false); err != nil {
		return Request{}, fmt.Errorf("invalid assessment context: %w", err)
	}
	if err := identity.ValidateRelativePath(input.Track, false); err != nil {
		return Request{}, fmt.Errorf("invalid assessment track: %w", err)
	}
	if err := identity.ValidateRecordID("facts", input.FactsID); err != nil {
		return Request{}, fmt.Errorf("invalid accepted facts ID: %w", err)
	}
	definitions, err := normalizeRecordIDs("definition", input.DefinitionIDs, true)
	if err != nil {
		return Request{}, fmt.Errorf("invalid assessment definitions: %w", err)
	}
	snapshots, err := normalizeRecordIDs("snapshot", input.SnapshotIDs, false)
	if err != nil {
		return Request{}, fmt.Errorf("invalid assessment snapshots: %w", err)
	}
	targetVersions := cloneStringMap(input.TargetVersions)
	for provider, version := range targetVersions {
		if strings.TrimSpace(provider) == "" || strings.TrimSpace(version) == "" {
			return Request{}, fmt.Errorf("target provider and version are required")
		}
	}
	work := &WorkContract{
		Phase: PhaseAssess, AVMReleaseVersion: input.AVMReleaseVersion, ContextID: input.ContextID, Track: input.Track,
		DesiredGeneration: input.DesiredGeneration, FactsID: input.FactsID,
		DefinitionIDs: definitions, SnapshotIDs: snapshots, TargetVersions: targetVersions,
		Applicability: ApplicabilityApplicable, Candidate: CandidateEligible, RetryClass: RetryNone,
	}
	fingerprint, err := workFingerprint(input.ModuleID, input.SourceSHA, *work)
	if err != nil {
		return Request{}, err
	}
	request := requestFor(input.ModuleID, input.SourceSHA, fingerprint, 1, now.UTC())
	request.Work = work
	return request, nil
}

// ClaimModuleRequest grants the sole compute lease for a module. The caller
// supplies the persisted module-wide fence high-water mark as the next token.
func ClaimModuleRequest(requests []Request, requestID, owner string, now, expiresAt time.Time, fence uint64) ([]Request, error) {
	if err := validateClaimParameters(requestID, owner, now, expiresAt, fence); err != nil {
		return nil, err
	}
	selected, moduleID, err := findClaimTarget(requests, requestID)
	if err != nil {
		return nil, err
	}
	if selected < 0 || requests[selected].Work == nil || requests[selected].Status != StatusQueued {
		return nil, fmt.Errorf("queued versioned request %q was not found", requestID)
	}
	if hasNewerDesiredRequest(requests, selected) {
		return nil, fmt.Errorf("request %q is superseded by a newer desired generation", requestID)
	}
	active, highWater := moduleClaimState(requests, moduleID)
	if active {
		return nil, fmt.Errorf("module %s already has an active compute claim", moduleID)
	}
	if fence <= highWater {
		return nil, fmt.Errorf("fencing token %d does not advance module high-water mark %d", fence, highWater)
	}
	return claimRequest(requests, selected, owner, now, expiresAt, fence)
}

func validateClaimParameters(requestID, owner string, now, expiresAt time.Time, fence uint64) error {
	if requestID == "" || strings.TrimSpace(owner) == "" || now.IsZero() || !expiresAt.After(now) || fence == 0 {
		return fmt.Errorf("request, owner, positive fence, and valid lease interval are required")
	}
	return nil
}

func findClaimTarget(requests []Request, requestID string) (int, string, error) {
	selected := -1
	moduleID := ""
	for index, request := range requests {
		if err := request.Validate(); err != nil {
			return -1, "", fmt.Errorf("invalid module request at index %d: %w", index, err)
		}
		if request.ID != requestID {
			continue
		}
		if selected >= 0 {
			return -1, "", fmt.Errorf("request ID %q is duplicated", requestID)
		}
		selected = index
		moduleID = request.ModuleID
	}
	return selected, moduleID, nil
}

func moduleClaimState(requests []Request, moduleID string) (bool, uint64) {
	var highWater uint64
	active := false
	for _, request := range requests {
		if request.ModuleID != moduleID {
			continue
		}
		if request.Status == StatusRunning {
			active = true
		}
		if request.Work != nil && request.Work.Fence > highWater {
			highWater = request.Work.Fence
		}
	}
	return active, highWater
}

func hasNewerDesiredRequest(requests []Request, selected int) bool {
	request := requests[selected]
	for _, candidate := range requests {
		if candidate.Status == StatusSuperseded || !sameRequestContext(candidate, request) {
			continue
		}
		if candidate.Work.DesiredGeneration > request.Work.DesiredGeneration {
			return true
		}
	}
	return false
}

func claimRequest(requests []Request, selected int, owner string, now, expiresAt time.Time, fence uint64) ([]Request, error) {
	claimed := cloneRequests(requests)
	claimed[selected].Status = StatusRunning
	claimed[selected].OwnerRunID = owner
	claimed[selected].UpdatedAt = now.UTC()
	claimed[selected].Work.Fence = fence
	claimed[selected].Work.Lease = &ComputeLease{Owner: owner, ExpiresAt: expiresAt.UTC(), Fence: fence}
	if err := claimed[selected].Validate(); err != nil {
		return nil, err
	}
	return claimed, nil
}

// SealCompletion records trusted validation before releasing compute. A later
// acceptance transition can proceed independently of the released lease.
func SealCompletion(request Request, owner string, fence uint64, completion SealedCompletion) (Request, error) {
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	if request.Work == nil || request.Status != StatusRunning || request.Work.Lease == nil {
		return Request{}, fmt.Errorf("only a running versioned request can seal completion")
	}
	if request.OwnerRunID != owner || request.Work.Lease.Owner != owner || request.Work.Lease.Fence != fence || request.Work.Fence != fence {
		return Request{}, fmt.Errorf("completion does not match the active owner and fence")
	}
	if completion.ID == "" || completion.RequestID != request.ID || completion.InputFingerprint != request.Fingerprint || completion.DesiredGeneration != request.Work.DesiredGeneration || completion.Fence != fence || !isLowerHexDigest(completion.ArtifactDigest) || strings.TrimSpace(completion.ValidationID) == "" || completion.SealedAt.IsZero() {
		return Request{}, fmt.Errorf("sealed completion identity is incomplete or mismatched")
	}
	sealed := cloneRequest(request)
	sealed.Status = StatusAwaitingAcceptance
	sealed.OwnerRunID = ""
	sealed.Work.Lease = nil
	completion.SealedAt = completion.SealedAt.UTC()
	sealed.Work.Completion = &completion
	sealed.UpdatedAt = completion.SealedAt
	if err := sealed.Validate(); err != nil {
		return Request{}, err
	}
	return sealed, nil
}

// CompleteAttempt releases the exact dispatched run only after its matching
// running request has been sealed by the trusted validator.
func CompleteAttempt(attempt Attempt, runningRequest, sealedRequest Request, runID string, runAttempt, fence uint64, completedAt time.Time) (Attempt, error) {
	if err := attempt.Validate(); err != nil {
		return Attempt{}, err
	}
	if err := runningRequest.Validate(); err != nil {
		return Attempt{}, err
	}
	if err := sealedRequest.Validate(); err != nil {
		return Attempt{}, err
	}
	if attempt.Status != AttemptDispatched || attempt.RunID != runID || attempt.RunAttempt != runAttempt || attempt.Fence != fence ||
		runningRequest.ID != attempt.RequestID || runningRequest.Status != StatusRunning || runningRequest.OwnerRunID != runID || runningRequest.Work == nil ||
		runningRequest.Work.Phase != attempt.Phase || runningRequest.Work.AttemptID != attempt.ID || runningRequest.Work.Fence != fence || runningRequest.Work.Lease == nil ||
		runningRequest.Work.Lease.Owner != runID || runningRequest.Work.Lease.Fence != fence ||
		sealedRequest.ID != runningRequest.ID || sealedRequest.Status != StatusAwaitingAcceptance || sealedRequest.OwnerRunID != "" || sealedRequest.Work == nil ||
		sealedRequest.Work.Completion == nil || sealedRequest.Work.Completion.Fence != fence || sealedRequest.Work.Completion.SealedAt.IsZero() ||
		completedAt.IsZero() || !completedAt.Equal(sealedRequest.Work.Completion.SealedAt) {
		return Attempt{}, ErrAttemptOwnership
	}
	completed := attempt
	completed.Status, completed.CompletedAt = AttemptCompleted, completedAt.UTC()
	if err := completed.Validate(); err != nil {
		return Attempt{}, err
	}
	return completed, nil
}

// AcceptCompletion records acceptance separately from trusted worker
// completion and never reacquires the compute lease.
func AcceptCompletion(request Request, completionID string, acceptedAt time.Time) (Request, error) {
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	if request.Work == nil || request.Work.Phase != PhaseAssess || request.Status != StatusAwaitingAcceptance || request.Work.Completion == nil || request.Work.Completion.ID != completionID || acceptedAt.IsZero() || acceptedAt.Before(request.Work.Completion.SealedAt) {
		return Request{}, fmt.Errorf("accepted completion does not match a sealed awaiting-acceptance request")
	}
	accepted := cloneRequest(request)
	accepted.Status = StatusAccepted
	accepted.LastAcceptedFingerprint = accepted.Fingerprint
	accepted.LastAcceptedSourceSHA = accepted.SourceSHA
	accepted.UpdatedAt = acceptedAt.UTC()
	if err := accepted.Validate(); err != nil {
		return Request{}, err
	}
	return accepted, nil
}

// AcceptFacts records a merged facts receipt without marking analysis accepted.
func AcceptFacts(request Request, completionID, factsID string, acceptedAt time.Time) (Request, error) {
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	if request.Work == nil || request.Work.Phase != PhaseAcquireSource || request.Status != StatusAwaitingAcceptance || request.Work.Completion == nil || request.Work.Completion.ID != completionID || acceptedAt.IsZero() || acceptedAt.Before(request.Work.Completion.SealedAt) {
		return Request{}, fmt.Errorf("accepted facts do not match a sealed source-acquisition completion")
	}
	if err := identity.ValidateRecordID("facts", factsID); err != nil {
		return Request{}, fmt.Errorf("invalid accepted facts ID: %w", err)
	}
	accepted := cloneRequest(request)
	accepted.Status = StatusFactsAccepted
	accepted.Work.AcceptedFactsID = factsID
	accepted.UpdatedAt = acceptedAt.UTC()
	if err := accepted.Validate(); err != nil {
		return Request{}, err
	}
	return accepted, nil
}

// WakeAssessmentRequests binds accepted facts to each eligible context. Shared
// facts can therefore produce independent track-scoped assessment requests.
func WakeAssessmentRequests(factsID string, inputs []AssessmentInput, now time.Time) ([]Request, error) {
	if err := identity.ValidateRecordID("facts", factsID); err != nil {
		return nil, fmt.Errorf("invalid accepted facts ID: %w", err)
	}
	if len(inputs) == 0 || now.IsZero() {
		return nil, fmt.Errorf("assessment contexts and wake-up time are required")
	}
	requests := make([]Request, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	seenContexts := make(map[string]struct{}, len(inputs))
	for index, input := range inputs {
		if input.FactsID != "" && input.FactsID != factsID {
			return nil, fmt.Errorf("assessment context %d is pinned to different facts", index)
		}
		input.FactsID = factsID
		request, err := NewAssessmentRequest(input, now)
		if err != nil {
			return nil, fmt.Errorf("create assessment for context %d: %w", index, err)
		}
		if _, exists := seen[request.ID]; exists {
			return nil, fmt.Errorf("duplicate assessment context request %q", request.ID)
		}
		contextKey := request.ModuleID + "\x00" + request.Work.ContextID
		if _, exists := seenContexts[contextKey]; exists {
			return nil, fmt.Errorf("duplicate assessment context %q", request.Work.ContextID)
		}
		seen[request.ID] = struct{}{}
		seenContexts[contextKey] = struct{}{}
		requests = append(requests, request)
	}
	return requests, nil
}

// CoalesceRequests preserves request history and other contexts while
// superseding only older, unclaimed work for the same module/context/phase.
func CoalesceRequests(existing []Request, desired Request) ([]Request, bool, error) {
	if err := desired.Validate(); err != nil {
		return nil, false, fmt.Errorf("invalid desired request: %w", err)
	}
	if desired.Work == nil || desired.Status != StatusQueued {
		return nil, false, fmt.Errorf("desired request must be a queued versioned request")
	}
	requests := cloneRequests(existing)
	scan, err := scanRequestContext(requests, desired)
	if err != nil {
		return nil, false, err
	}
	if desired.Work.DesiredGeneration < scan.maxGeneration {
		return nil, false, fmt.Errorf("desired generation %d is older than context generation %d", desired.Work.DesiredGeneration, scan.maxGeneration)
	}
	if scan.sameFingerprintIndex >= 0 {
		return coalesceSameFingerprint(requests, scan.sameFingerprintIndex, desired)
	}
	supersedeQueuedRequests(requests, desired)
	requests = append(requests, cloneRequest(desired))
	return requests, true, nil
}

type requestContextScan struct {
	maxGeneration        uint64
	sameFingerprintIndex int
}

func scanRequestContext(requests []Request, desired Request) (requestContextScan, error) {
	scan := requestContextScan{sameFingerprintIndex: -1}
	for index, request := range requests {
		if err := request.Validate(); err != nil {
			return requestContextScan{}, fmt.Errorf("invalid existing request at index %d: %w", index, err)
		}
		if !sameRequestContext(request, desired) {
			continue
		}
		if request.Work.DesiredGeneration > scan.maxGeneration {
			scan.maxGeneration = request.Work.DesiredGeneration
		}
		if request.Fingerprint == desired.Fingerprint {
			scan.sameFingerprintIndex = index
		} else if request.Work.DesiredGeneration == desired.Work.DesiredGeneration {
			return requestContextScan{}, fmt.Errorf("context reused desired generation %d for different inputs", desired.Work.DesiredGeneration)
		}
	}
	return scan, nil
}

func coalesceSameFingerprint(requests []Request, index int, desired Request) ([]Request, bool, error) {
	request := requests[index]
	if request.Status != StatusQueued || request.Work.DesiredGeneration == desired.Work.DesiredGeneration {
		return requests, false, nil
	}
	requests[index].Work.DesiredGeneration = desired.Work.DesiredGeneration
	requests[index].UpdatedAt = desired.UpdatedAt
	return requests, true, nil
}

func supersedeQueuedRequests(requests []Request, desired Request) {
	for index := range requests {
		if sameRequestContext(requests[index], desired) && requests[index].Status == StatusQueued {
			requests[index].Status = StatusSuperseded
			requests[index].UpdatedAt = desired.UpdatedAt
		}
	}
}

// RequiredContexts excludes proven-inapplicable tracks and adds latest-source
// only when every configured track is proven not applicable.
func RequiredContexts(tracks []TrackState) ([]TrackState, error) {
	if len(tracks) == 0 {
		return nil, fmt.Errorf("track applicability is unresolved for an empty track set")
	}
	ordered := append([]TrackState(nil), tracks...)
	allNotApplicable, err := validateTrackStates(ordered)
	if err != nil {
		return nil, err
	}
	if allNotApplicable {
		return []TrackState{{
			ContextID: LatestSourceContext, Track: LatestSourceContext,
			Applicability: ApplicabilityApplicable, Candidate: CandidateEligible,
		}}, nil
	}
	return applicableTrackContexts(ordered), nil
}

func validateTrackStates(tracks []TrackState) (bool, error) {
	seen := make(map[string]struct{}, len(tracks))
	allNotApplicable := true
	for _, track := range tracks {
		if track.ContextID == "" || track.Track == "" || !validApplicability(track.Applicability) || !validCandidate(track.Candidate) {
			return false, fmt.Errorf("track state is incomplete")
		}
		if err := identity.ValidateRelativePath(track.ContextID, false); err != nil {
			return false, fmt.Errorf("invalid track context: %w", err)
		}
		if err := identity.ValidateRelativePath(track.Track, false); err != nil {
			return false, fmt.Errorf("invalid track identity: %w", err)
		}
		if _, exists := seen[track.ContextID]; exists {
			return false, fmt.Errorf("duplicate track context %q", track.ContextID)
		}
		seen[track.ContextID] = struct{}{}
		if track.Applicability != ApplicabilityNotApplicable {
			allNotApplicable = false
		}
	}
	return allNotApplicable, nil
}

func applicableTrackContexts(tracks []TrackState) []TrackState {
	filtered := make([]TrackState, 0, len(tracks))
	for _, track := range tracks {
		if track.Applicability != ApplicabilityNotApplicable {
			filtered = append(filtered, track)
		}
	}
	sort.Slice(filtered, func(left, right int) bool { return filtered[left].ContextID < filtered[right].ContextID })
	return filtered
}

func validateWorkRequest(request Request) error {
	work := request.Work
	if err := validateWorkMetadata(request, work); err != nil {
		return err
	}
	if err := validateWorkPhaseInput(request, work); err != nil {
		return err
	}
	if err := validateWorkLease(request, work); err != nil {
		return err
	}
	return validateWorkCompletion(request, work)
}

func validateWorkMetadata(request Request, work *WorkContract) error {
	if work.Phase != PhaseAcquireSource && work.Phase != PhaseAssess {
		return fmt.Errorf("invalid request phase %q", work.Phase)
	}
	if work.DesiredGeneration == 0 || !validRetryClass(work.RetryClass) || !validApplicability(work.Applicability) || !validCandidate(work.Candidate) {
		return fmt.Errorf("versioned work contract is incomplete")
	}

	fingerprint, err := workFingerprint(request.ModuleID, request.SourceSHA, *work)
	if err != nil {
		return err
	}
	if request.Fingerprint != fingerprint {
		return fmt.Errorf("request fingerprint does not match its pinned work inputs")
	}
	return nil
}

func validateWorkPhaseInput(request Request, work *WorkContract) error {
	if work.Phase == PhaseAcquireSource {
		return validateAcquisitionInput(request, work)
	}
	return validateAssessmentInput(work)
}

func validateAcquisitionInput(request Request, work *WorkContract) error {
	if work.ContextID != "" || work.Track != "" || work.FactsID != "" || len(work.DefinitionIDs) != 0 || len(work.SnapshotIDs) != 0 || len(work.TargetVersions) != 0 || strings.TrimSpace(work.ExtractorContract) == "" || len(work.Capabilities) == 0 {
		return fmt.Errorf("acquisition input must pin source/extractor and omit future assessment inputs")
	}
	if work.RefreshGeneration != 0 && work.RefreshGeneration != work.DesiredGeneration {
		return fmt.Errorf("forced acquisition refresh generation must match the desired generation")
	}
	if work.Applicability != ApplicabilityUnresolved || work.Candidate != CandidateUnresolved {
		return fmt.Errorf("acquisition cannot assume track applicability or candidate eligibility")
	}
	if err := validateSourceIdentity(request.ModuleID, request.SourceSHA); err != nil {
		return err
	}
	if err := validateNormalizedStrings(work.Capabilities); err != nil {
		return fmt.Errorf("invalid extractor capabilities: %w", err)
	}
	for _, capability := range work.Capabilities {
		if !capabilityPattern.MatchString(capability) {
			return fmt.Errorf("invalid extractor capability %q", capability)
		}
	}
	return nil
}

func validateAssessmentInput(work *WorkContract) error {
	if work.ContextID == "" || work.Track == "" || work.FactsID == "" || work.ExtractorContract != "" || len(work.Capabilities) != 0 || work.Applicability != ApplicabilityApplicable || work.Candidate != CandidateEligible {
		return fmt.Errorf("assessment must pin accepted facts and an applicable eligible context without extractor inputs")
	}
	if err := identity.ValidateRelativePath(work.ContextID, false); err != nil {
		return fmt.Errorf("invalid assessment context: %w", err)
	}
	if err := identity.ValidateRelativePath(work.Track, false); err != nil {
		return fmt.Errorf("invalid assessment track: %w", err)
	}
	if err := identity.ValidateRecordID("facts", work.FactsID); err != nil {
		return fmt.Errorf("invalid assessment facts ID: %w", err)
	}
	if err := validateRecordIDSet("definition", work.DefinitionIDs, true); err != nil {
		return err
	}
	if err := validateRecordIDSet("snapshot", work.SnapshotIDs, false); err != nil {
		return err
	}
	for provider, version := range work.TargetVersions {
		if strings.TrimSpace(provider) == "" || strings.TrimSpace(version) == "" {
			return fmt.Errorf("assessment target provider and version are required")
		}
	}
	return nil
}

func validateWorkLease(request Request, work *WorkContract) error {
	if request.Status == StatusClaimed {
		if work.Lease == nil || work.AttemptID == "" || work.Lease.Owner != work.AttemptID || work.Lease.Fence != work.Fence || work.Fence == 0 || work.Lease.ExpiresAt.IsZero() || request.OwnerRunID != "" {
			return fmt.Errorf("claimed work has no matching pre-dispatch attempt lease")
		}
		if err := identity.ValidateRecordID("attempt", work.AttemptID); err != nil {
			return fmt.Errorf("claimed work attempt ID is invalid: %w", err)
		}
	} else if request.Status == StatusRunning {
		if work.Lease == nil || work.Lease.Owner != request.OwnerRunID || work.Lease.Fence != work.Fence || work.Fence == 0 || work.Lease.ExpiresAt.IsZero() {
			return fmt.Errorf("running work has no matching fenced lease")
		}
	} else if work.Lease != nil {
		return fmt.Errorf("non-running work retains a compute lease")
	}
	return nil
}

func validateWorkCompletion(request Request, work *WorkContract) error {
	if request.Status == StatusAwaitingAcceptance && work.Completion == nil {
		return fmt.Errorf("awaiting-acceptance work has no sealed completion")
	}
	if request.Status == StatusAccepted {
		if work.Phase != PhaseAssess || work.Completion == nil || request.LastAcceptedFingerprint != request.Fingerprint || request.LastAcceptedSourceSHA != request.SourceSHA {
			return fmt.Errorf("accepted work lacks its sealed completion identity")
		}
	}
	if request.Status == StatusFactsAccepted {
		if work.Phase != PhaseAcquireSource || work.Completion == nil || identity.ValidateRecordID("facts", work.AcceptedFactsID) != nil {
			return fmt.Errorf("facts-accepted work lacks its merged facts identity")
		}
	} else if work.AcceptedFactsID != "" {
		return fmt.Errorf("accepted facts identity is set outside facts-accepted state")
	}
	if work.Completion != nil {
		if identity.ValidateRecordID("completion", work.Completion.ID) != nil || work.Completion.RequestID != request.ID || work.Completion.InputFingerprint != request.Fingerprint || work.Completion.DesiredGeneration != work.DesiredGeneration || work.Completion.Fence != work.Fence || work.Completion.Fence == 0 || !isLowerHexDigest(work.Completion.ArtifactDigest) || work.Completion.ValidationID == "" || work.Completion.SealedAt.IsZero() {
			return fmt.Errorf("sealed completion is invalid")
		}
	}
	return nil
}

func workFingerprint(moduleID, sourceSHA string, work WorkContract) (string, error) {
	if work.Phase == PhaseAcquireSource {
		contract := struct {
			ModuleID          string   `json:"module_id"`
			SourceSHA         string   `json:"source_sha"`
			AVMReleaseVersion string   `json:"avm_release_version"`
			ExtractorContract string   `json:"extractor_contract"`
			Capabilities      []string `json:"capabilities"`
			RefreshGeneration uint64   `json:"refresh_generation,omitempty"`
		}{moduleID, sourceSHA, work.AVMReleaseVersion, work.ExtractorContract, work.Capabilities, work.RefreshGeneration}
		id, err := identity.ID("acquisition-input", 1, contract)
		if err != nil {
			return "", err
		}
		return strings.TrimPrefix(id, "acquisition-input-"), nil
	}
	contract := struct {
		ModuleID          string            `json:"module_id"`
		SourceSHA         string            `json:"source_sha"`
		AVMReleaseVersion string            `json:"avm_release_version"`
		ContextID         string            `json:"context_id"`
		Track             string            `json:"track"`
		FactsID           string            `json:"facts_id"`
		DefinitionIDs     []string          `json:"definition_ids"`
		SnapshotIDs       []string          `json:"snapshot_ids"`
		TargetVersions    map[string]string `json:"target_versions"`
	}{moduleID, sourceSHA, work.AVMReleaseVersion, work.ContextID, work.Track, work.FactsID, work.DefinitionIDs, work.SnapshotIDs, work.TargetVersions}
	id, err := identity.ID("assessment-input", 1, contract)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(id, "assessment-input-"), nil
}

func validateSourceIdentity(moduleID, sourceSHA string) error {
	if !moduleIDPattern.MatchString(moduleID) || len(sourceSHA) != 40 {
		return fmt.Errorf("module ID and immutable source SHA are required")
	}
	if _, err := hex.DecodeString(sourceSHA); err != nil || strings.ToLower(sourceSHA) != sourceSHA {
		return fmt.Errorf("source SHA must be lowercase hexadecimal")
	}
	return nil
}

func normalizeCapabilities(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one extractor capability is required")
	}
	normalized := append([]string(nil), values...)
	sort.Strings(normalized)
	if err := validateNormalizedStrings(normalized); err != nil {
		return nil, err
	}
	for _, value := range normalized {
		if !capabilityPattern.MatchString(value) {
			return nil, fmt.Errorf("invalid extractor capability %q", value)
		}
	}
	return normalized, nil
}

func normalizeRecordIDs(kind string, values []string, required bool) ([]string, error) {
	if len(values) == 0 && required {
		return nil, fmt.Errorf("at least one %s ID is required", kind)
	}
	normalized := append([]string(nil), values...)
	sort.Strings(normalized)
	if err := validateRecordIDSet(kind, normalized, required); err != nil {
		return nil, err
	}
	return normalized, nil
}

func validateRecordIDSet(kind string, values []string, required bool) error {
	if len(values) == 0 && required {
		return fmt.Errorf("at least one %s ID is required", kind)
	}
	if err := validateNormalizedStrings(values); err != nil {
		return err
	}
	for _, value := range values {
		if err := identity.ValidateRecordID(kind, value); err != nil {
			return err
		}
	}
	return nil
}

func validateNormalizedStrings(values []string) error {
	for index, value := range values {
		if strings.TrimSpace(value) == "" || (index > 0 && values[index-1] >= value) {
			return fmt.Errorf("values must be non-empty, unique, and sorted")
		}
	}
	return nil
}

func validRetryClass(class RetryClass) bool {
	switch class {
	case RetryNone, RetryTransient, RetryRateLimited, RetryBlocked, RetryNeedsReview:
		return true
	default:
		return false
	}
}

func validApplicability(value Applicability) bool {
	switch value {
	case ApplicabilityApplicable, ApplicabilityNotApplicable, ApplicabilityUnresolved:
		return true
	default:
		return false
	}
}

func validCandidate(value CandidateState) bool {
	switch value {
	case CandidateEligible, CandidateIneligible, CandidateUnresolved:
		return true
	default:
		return false
	}
}

func sameRequestContext(left, right Request) bool {
	if left.Work == nil || right.Work == nil || left.ModuleID != right.ModuleID || left.Work.Phase != right.Work.Phase {
		return false
	}
	if left.Work.Phase == PhaseAcquireSource {
		return left.Work.AVMReleaseVersion == right.Work.AVMReleaseVersion && left.SourceSHA == right.SourceSHA
	}
	return left.Work.AVMReleaseVersion == right.Work.AVMReleaseVersion && left.Work.ContextID == right.Work.ContextID && left.Work.Track == right.Work.Track
}

func cloneRequests(requests []Request) []Request {
	cloned := make([]Request, len(requests))
	for index, request := range requests {
		cloned[index] = cloneRequest(request)
	}
	return cloned
}

func cloneRequest(request Request) Request {
	cloned := request
	if request.Work != nil {
		work := *request.Work
		work.Capabilities = append([]string(nil), request.Work.Capabilities...)
		work.DefinitionIDs = append([]string(nil), request.Work.DefinitionIDs...)
		work.SnapshotIDs = append([]string(nil), request.Work.SnapshotIDs...)
		work.TargetVersions = cloneStringMap(request.Work.TargetVersions)
		if request.Work.Lease != nil {
			lease := *request.Work.Lease
			work.Lease = &lease
		}
		if request.Work.Completion != nil {
			completion := *request.Work.Completion
			work.Completion = &completion
		}
		cloned.Work = &work
	}

	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func isLowerHexDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
