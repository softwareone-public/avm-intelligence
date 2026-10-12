package queue

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

// AcceptedProviderSnapshotPin is the validated identity of a provider
// snapshot already accepted into the canonical data tree.
type AcceptedProviderSnapshotPin struct {
	ID              string
	ProviderAddress string
	ProviderVersion string
}

// AssessmentWakeContext binds one configured track decision to the exact
// inputs from which its assess request may be constructed.
type AssessmentWakeContext struct {
	State                  TrackState
	Input                  AssessmentInput
	PrimaryProviderAddress string
	PrimaryProviderMajor   uint64
	AcceptedSnapshots      []AcceptedProviderSnapshotPin
	DeferredReason         string
}

// AssessmentWakeDeferred explains why one configured context did not produce
// an assess request.
type AssessmentWakeDeferred struct {
	ModuleID          string `json:"module_id"`
	AVMReleaseVersion string `json:"avm_release_version"`
	ContextID         string `json:"context_id"`
	Reason            string `json:"reason"`
}

// AssessmentWakePlan contains only in-memory requests; it never writes state
// or dispatches workers.
type AssessmentWakePlan struct {
	Requests []Request
	Deferred []AssessmentWakeDeferred
	Contexts []AssessmentWakeContext
}

// AssessmentWakeContextError identifies the context whose accepted inputs
// could not be turned into a safe assessment request.
type AssessmentWakeContextError struct {
	ContextID string
	Err       error
}

func (failure *AssessmentWakeContextError) Error() string {
	return fmt.Sprintf("assessment context %s is invalid", failure.ContextID)
}

func (failure *AssessmentWakeContextError) Unwrap() error { return failure.Err }

// PlanAssessmentWake prepares requests only for explicitly applicable and
// eligible contexts with exact accepted snapshot targets. Applicability and
// candidate state must come from a reviewed policy evaluator; this function
// does not infer either state from missing or partial facts.
func PlanAssessmentWake(factsID, moduleID, sourceSHA, avmReleaseVersion string, contexts []AssessmentWakeContext, now time.Time) (AssessmentWakePlan, error) {
	if err := identity.ValidateRecordID("facts", factsID); err != nil {
		return AssessmentWakePlan{}, fmt.Errorf("invalid accepted facts ID: %w", err)
	}
	if err := validateSourceIdentity(moduleID, sourceSHA); err != nil {
		return AssessmentWakePlan{}, err
	}
	if !avmReleaseVersionPattern.MatchString(avmReleaseVersion) {
		return AssessmentWakePlan{}, fmt.Errorf("assessment wake requires an exact AVM release version")
	}
	if now.IsZero() {
		return AssessmentWakePlan{}, fmt.Errorf("assessment wake time and configured contexts are required")
	}
	ordered, err := orderedAssessmentWakeContexts(contexts)
	if err != nil {
		return AssessmentWakePlan{}, err
	}
	inputs := make([]AssessmentInput, 0, len(ordered))
	plan := AssessmentWakePlan{
		Requests: []Request{}, Deferred: []AssessmentWakeDeferred{},
		Contexts: cloneAssessmentWakeContexts(ordered),
	}
	for _, context := range ordered {
		input, deferred, prepareErr := prepareAssessmentWakeContext(moduleID, sourceSHA, avmReleaseVersion, factsID, context)
		if prepareErr != nil {
			return AssessmentWakePlan{}, &AssessmentWakeContextError{ContextID: context.State.ContextID, Err: prepareErr}
		}
		if deferred != nil {
			deferred.ModuleID = moduleID
			deferred.AVMReleaseVersion = avmReleaseVersion
			plan.Deferred = append(plan.Deferred, *deferred)
			continue
		}
		inputs = append(inputs, input)
	}
	if len(inputs) == 0 {
		return plan, nil
	}
	requests, err := WakeAssessmentRequests(factsID, inputs, now)
	if err != nil {
		return AssessmentWakePlan{}, err
	}
	plan.Requests = requests
	return plan, nil
}

func cloneAssessmentWakeContexts(contexts []AssessmentWakeContext) []AssessmentWakeContext {
	cloned := append([]AssessmentWakeContext(nil), contexts...)
	for index := range cloned {
		cloned[index].Input.DefinitionIDs = append([]string(nil), contexts[index].Input.DefinitionIDs...)
		cloned[index].Input.SnapshotIDs = append([]string(nil), contexts[index].Input.SnapshotIDs...)
		cloned[index].AcceptedSnapshots = append([]AcceptedProviderSnapshotPin(nil), contexts[index].AcceptedSnapshots...)
		cloned[index].DeferredReason = contexts[index].DeferredReason
		cloned[index].Input.TargetVersions = make(map[string]string, len(contexts[index].Input.TargetVersions))
		for address, version := range contexts[index].Input.TargetVersions {
			cloned[index].Input.TargetVersions[address] = version
		}
	}
	return cloned
}

func orderedAssessmentWakeContexts(contexts []AssessmentWakeContext) ([]AssessmentWakeContext, error) {
	if len(contexts) == 0 {
		return nil, fmt.Errorf("assessment wake time and configured contexts are required")
	}
	ordered := append([]AssessmentWakeContext(nil), contexts...)
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].State.ContextID < ordered[right].State.ContextID
	})
	seen := make(map[string]struct{}, len(ordered))
	for _, context := range ordered {
		if err := validateAssessmentTrackState(context.State); err != nil {
			return nil, &AssessmentWakeContextError{ContextID: context.State.ContextID, Err: err}
		}
		if _, exists := seen[context.State.ContextID]; exists {
			return nil, &AssessmentWakeContextError{ContextID: context.State.ContextID, Err: fmt.Errorf("duplicate assessment context")}
		}
		seen[context.State.ContextID] = struct{}{}
	}
	return ordered, nil
}

func validateAssessmentTrackState(track TrackState) error {
	if track.ContextID == "" || track.Track == "" || !validApplicability(track.Applicability) || !validCandidate(track.Candidate) {
		return fmt.Errorf("assessment context policy state is incomplete")
	}
	if err := identity.ValidateRelativePath(track.ContextID, false); err != nil {
		return fmt.Errorf("invalid assessment context ID: %w", err)
	}
	if err := identity.ValidateRelativePath(track.Track, false); err != nil {
		return fmt.Errorf("invalid assessment track: %w", err)
	}
	return nil
}

func prepareAssessmentWakeContext(moduleID, sourceSHA, avmReleaseVersion, factsID string, context AssessmentWakeContext) (AssessmentInput, *AssessmentWakeDeferred, error) {
	track := context.State
	if context.DeferredReason != "" {
		return AssessmentInput{}, &AssessmentWakeDeferred{ContextID: track.ContextID, Reason: context.DeferredReason}, nil
	}
	if track.Applicability != ApplicabilityApplicable {
		return AssessmentInput{}, &AssessmentWakeDeferred{ContextID: track.ContextID, Reason: "applicability is not proven"}, nil
	}
	if track.Candidate != CandidateEligible {
		return AssessmentInput{}, &AssessmentWakeDeferred{ContextID: track.ContextID, Reason: "candidate eligibility is not proven"}, nil
	}
	if err := validateAssessmentWakeInputIdentity(moduleID, sourceSHA, avmReleaseVersion, factsID, context); err != nil {
		return AssessmentInput{}, nil, err
	}
	reason, err := validateAssessmentSnapshotVector(context)
	if err != nil {
		return AssessmentInput{}, nil, err
	}
	if reason != "" {
		return AssessmentInput{}, &AssessmentWakeDeferred{ContextID: track.ContextID, Reason: reason}, nil
	}
	input := context.Input
	input.AVMReleaseVersion = avmReleaseVersion
	input.FactsID = factsID
	return input, nil, nil
}

func validateAssessmentWakeInputIdentity(moduleID, sourceSHA, avmReleaseVersion, factsID string, context AssessmentWakeContext) error {
	track, input := context.State, context.Input
	if input.ModuleID != moduleID || input.SourceSHA != sourceSHA || input.ContextID != track.ContextID || input.Track != track.Track {
		return fmt.Errorf("assessment input does not match configured context %s", track.ContextID)
	}
	if input.FactsID != "" && input.FactsID != factsID {
		return fmt.Errorf("assessment context %s is pinned to different accepted facts", track.ContextID)
	}
	if input.AVMReleaseVersion != "" && input.AVMReleaseVersion != avmReleaseVersion {
		return fmt.Errorf("assessment context %s is pinned to a different AVM release", track.ContextID)
	}
	return nil
}

func validateAssessmentSnapshotVector(context AssessmentWakeContext) (string, error) {
	input := context.Input
	if len(context.AcceptedSnapshots) == 0 || len(input.SnapshotIDs) == 0 || len(input.TargetVersions) == 0 {
		return "accepted provider snapshots and exact target versions are required", nil
	}
	if context.PrimaryProviderAddress == "" {
		return "primary provider address and major are not configured", nil
	}
	if len(context.AcceptedSnapshots) != len(input.SnapshotIDs) || len(context.AcceptedSnapshots) != len(input.TargetVersions) {
		return "accepted snapshot IDs and target-version vector do not match", nil
	}
	acceptedIDs, acceptedTargets, primaryMajor, err := collectAcceptedSnapshotPins(context.AcceptedSnapshots)
	if err != nil {
		return "", err
	}
	primaryProviderFound := false
	if major, exists := primaryMajor[context.PrimaryProviderAddress]; exists {
		primaryProviderFound = true
		if major != context.PrimaryProviderMajor {
			return "accepted primary provider snapshot does not match the configured track major", nil
		}
	}
	if !primaryProviderFound {
		return "accepted snapshot for the configured primary provider is missing", nil
	}
	if !sameStringSet(input.SnapshotIDs, acceptedIDs) || !sameStringMap(input.TargetVersions, acceptedTargets) {
		return "request snapshot IDs and target versions do not exactly match accepted snapshots", nil
	}
	if len(input.DefinitionIDs) == 0 {
		return "assessment definition IDs are required", nil
	}
	return "", nil
}

func collectAcceptedSnapshotPins(snapshots []AcceptedProviderSnapshotPin) (map[string]struct{}, map[string]string, map[string]uint64, error) {
	acceptedIDs := make(map[string]struct{}, len(snapshots))
	acceptedTargets := make(map[string]string, len(snapshots))
	primaryMajors := make(map[string]uint64, len(snapshots))
	for _, snapshot := range snapshots {
		if err := identity.ValidateRecordID("snapshot", snapshot.ID); err != nil {
			return nil, nil, nil, fmt.Errorf("invalid accepted provider snapshot ID: %w", err)
		}
		if err := validateFullProviderAddress(snapshot.ProviderAddress); err != nil {
			return nil, nil, nil, err
		}
		major, ok := providerVersionMajor(snapshot.ProviderVersion)
		if !ok {
			return nil, nil, nil, fmt.Errorf("accepted provider snapshot has an invalid version")
		}
		if _, duplicate := acceptedIDs[snapshot.ID]; duplicate {
			return nil, nil, nil, fmt.Errorf("duplicate accepted provider snapshot ID %s", snapshot.ID)
		}
		if _, duplicate := acceptedTargets[snapshot.ProviderAddress]; duplicate {
			return nil, nil, nil, fmt.Errorf("accepted target vector contains multiple snapshots for %s", snapshot.ProviderAddress)
		}
		acceptedIDs[snapshot.ID] = struct{}{}
		acceptedTargets[snapshot.ProviderAddress] = snapshot.ProviderVersion
		primaryMajors[snapshot.ProviderAddress] = major
	}
	return acceptedIDs, acceptedTargets, primaryMajors, nil
}

func validateFullProviderAddress(address string) error {
	if err := identity.ValidateRelativePath(address, false); err != nil || len(strings.Split(address, "/")) != 3 {
		return fmt.Errorf("accepted provider snapshot has an invalid full provider address")
	}
	return nil
}

func sameStringSet(values []string, expected map[string]struct{}) bool {
	if len(values) != len(expected) {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		if _, exists := expected[value]; !exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func sameStringMap(actual, expected map[string]string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for key, value := range actual {
		if expected[key] != value {
			return false
		}
	}
	return true
}

func providerVersionMajor(version string) (uint64, bool) {
	majorText, _, hasMinor := strings.Cut(version, ".")
	if !hasMinor || majorText == "" {
		return 0, false
	}
	major, err := strconv.ParseUint(majorText, 10, 64)
	return major, err == nil
}
