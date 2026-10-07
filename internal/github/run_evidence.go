package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

const (
	EvidencePhaseDiscovery          = "discovery"
	EvidencePhaseProjectionRecovery = "projection_recovery"
	evidenceMarkerPrefix            = "<!-- factory-run-evidence:"
	evidencePageSize                = 100
	EvidenceOutcomeCompleted        = "completed"
	EvidenceOutcomeIncomplete       = "incomplete"
	EvidenceOutcomePending          = "pending"
	evidenceReasonUnacknowledged    = "unacknowledged"
	runEvidenceKind                 = "run-evidence"
	runEvidenceContentKind          = "run-evidence-content"
)

var evidenceIdentityPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var evidenceSourceSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var evidencePhasePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var evidenceRunPattern = regexp.MustCompile(`^[1-9][0-9]*-[1-9][0-9]*$`)

// RunEvidence is durable comment work scoped to one module, phase and run-attempt.
type RunEvidence struct {
	ModuleID      string `json:"module_id"`
	Phase         string `json:"phase"`
	RunID         string `json:"run_id"`
	RunURL        string `json:"run_url"`
	SourceVersion string `json:"source_version"`
	SourceSHA     string `json:"source_sha"`
	StateHead     string `json:"state_head"`
	Status        string `json:"status"`
	Outcome       string `json:"outcome"`
	Reason        string `json:"reason,omitempty"`
}

// RunEvidenceReceipt acknowledges a remote comment without replacing other runs.
type RunEvidenceReceipt struct {
	EvidenceID string       `json:"evidence_id"`
	CommentID  int64        `json:"comment_id"`
	RunID      string       `json:"run_id"`
	Phase      string       `json:"phase"`
	Outcome    string       `json:"outcome"`
	ContentID  string       `json:"content_id"`
	Context    *RunEvidence `json:"context,omitempty"`
}

type evidenceComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Type string `json:"type"`
	} `json:"user"`
	App *struct {
		ID int64 `json:"id"`
	} `json:"performed_via_github_app"`
}

// ID deliberately excludes outcome/source so retry updates the same run-attempt comment.
func (evidence RunEvidence) ID() (string, error) {
	return identity.ID(runEvidenceKind, 1, struct{ ModuleID, Phase, RunID string }{evidence.ModuleID, evidence.Phase, evidence.RunID})
}

func (evidence RunEvidence) Validate(owner, repository string) error {
	if !projectionModuleIDPattern.MatchString(evidence.ModuleID) || !evidencePhasePattern.MatchString(evidence.Phase) || !evidenceRunPattern.MatchString(evidence.RunID) || !evidenceIdentityPattern.MatchString(evidence.StateHead) {
		return fmt.Errorf("run evidence identity is invalid")
	}
	parsed, err := url.Parse(evidence.RunURL)
	prefix := "/" + owner + "/" + repository + "/actions/runs/"
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasPrefix(parsed.Path, prefix) {
		return fmt.Errorf("run evidence URL does not identify the approved repository")
	}
	if _, err := strconv.ParseUint(strings.TrimPrefix(parsed.Path, prefix), 10, 64); err != nil {
		return fmt.Errorf("run evidence URL does not identify a workflow run")
	}
	if strings.SplitN(evidence.RunID, "-", 2)[0] != strings.TrimPrefix(parsed.Path, prefix) {
		return fmt.Errorf("run evidence attempt does not match its workflow URL")
	}
	if len(evidence.SourceVersion) > 128 || strings.ContainsAny(evidence.SourceVersion, "`\r\n") || strings.ContainsAny(evidence.Status, "`\r\n") {
		return fmt.Errorf("run evidence source/status is invalid")
	}
	if evidence.SourceSHA != "" && !evidenceSourceSHAPattern.MatchString(evidence.SourceSHA) {
		return fmt.Errorf("run evidence source SHA is invalid")
	}
	if evidence.Outcome != EvidenceOutcomeCompleted && evidence.Outcome != EvidenceOutcomeIncomplete && evidence.Outcome != EvidenceOutcomePending {
		return fmt.Errorf("run evidence outcome is invalid")
	}
	switch evidence.Reason {
	case "", ProjectionErrorBudget, ProjectionErrorRateLimited, ProjectionErrorTransient, ProjectionErrorNeedsReview, evidenceReasonUnacknowledged:
		return nil
	default:
		return fmt.Errorf("run evidence reason is not allowlisted")
	}
}

func (evidence RunEvidence) body(owner, repository, evidenceID string) string {
	return strings.Join([]string{
		evidenceMarkerPrefix + evidenceID + " -->", "## Factory run evidence", "",
		"- Phase: `" + evidence.Phase + "`", "- Run/attempt: `" + evidence.RunID + "`",
		"- Run: " + evidence.RunURL, "- Module: `" + evidence.ModuleID + "`",
		"- Source: `" + evidence.SourceVersion + "` at `" + evidence.SourceSHA + "`",
		"- State checkpoint: https://github.com/" + owner + "/" + repository + "/commit/" + evidence.StateHead,
		"- Lifecycle status: `" + evidence.Status + "`", "- Phase outcome: `" + evidence.Outcome + "`",
		"- Diagnostic category: `" + evidence.Reason + "`",
	}, "\n")
}

// UpsertRunEvidence updates only an exact owned marker; ambiguous or human markers fail closed.
func (client Client) UpsertRunEvidence(ctx context.Context, owner, repository string, issueNumber int, evidence RunEvidence) (RunEvidenceReceipt, error) {
	if err := evidence.Validate(owner, repository); err != nil {
		return RunEvidenceReceipt{}, err
	}
	appID, err := strconv.ParseInt(client.EvidenceAppID, 10, 64)
	if err != nil || appID <= 0 || issueNumber <= 0 {
		return RunEvidenceReceipt{}, fmt.Errorf("approved App identity and Issue number are required for evidence comments")
	}
	evidenceID, err := evidence.ID()
	if err != nil {
		return RunEvidenceReceipt{}, err
	}
	ctx, finish := progress.Start(ctx, "projection", "run_evidence_comment", slog.String("module_id", evidence.ModuleID), slog.String("evidence_phase", evidence.Phase), slog.String("evidence_run_id", evidence.RunID), slog.Int("issue_number", issueNumber))
	defer finish(false)
	marker := evidenceMarkerPrefix + evidenceID + " -->"
	existing, err := client.findEvidenceComment(ctx, owner, repository, issueNumber, marker, appID)
	if err != nil {
		return RunEvidenceReceipt{}, err
	}
	body := evidence.body(owner, repository, evidenceID)
	comment := existing
	if existing.ID == 0 {
		endpoint := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repository, issueNumber)
		if err := client.rest(ctx, http.MethodPost, endpoint, map[string]string{"body": body}, &comment); err != nil {
			return RunEvidenceReceipt{}, err
		}
	} else if existing.Body != body {
		endpoint := fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, repository, existing.ID)
		if err := client.rest(ctx, http.MethodPatch, endpoint, map[string]string{"body": body}, &comment); err != nil {
			return RunEvidenceReceipt{}, err
		}
	}
	if !ownedEvidenceComment(comment, appID) || comment.ID <= 0 || comment.Body != body {
		return RunEvidenceReceipt{}, fmt.Errorf("evidence comment response does not identify the approved App")
	}
	progress.Logger(ctx).InfoContext(ctx, "run evidence comment synchronized", "comment_id", comment.ID, "evidence_id", evidenceID)
	finish(true)
	contentID, err := identity.ID(runEvidenceContentKind, 1, evidence)
	if err != nil {
		return RunEvidenceReceipt{}, err
	}
	return RunEvidenceReceipt{EvidenceID: evidenceID, CommentID: comment.ID, RunID: evidence.RunID, Phase: evidence.Phase, Outcome: evidence.Outcome, ContentID: contentID, Context: &evidence}, nil
}

func (client Client) findEvidenceComment(ctx context.Context, owner, repository string, issueNumber int, marker string, appID int64) (evidenceComment, error) {
	var match evidenceComment
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=%d&page=%d", owner, repository, issueNumber, evidencePageSize, page)
		var comments []evidenceComment
		if err := client.rest(ctx, http.MethodGet, endpoint, nil, &comments); err != nil {
			return match, err
		}
		for _, comment := range comments {
			if !strings.Contains(comment.Body, marker) {
				continue
			}
			if match.ID != 0 || comment.ID <= 0 || strings.Count(comment.Body, marker) != 1 || strings.Count(comment.Body, evidenceMarkerPrefix) != 1 || !ownedEvidenceComment(comment, appID) {
				return match, fmt.Errorf("run evidence marker is ambiguous or not owned by the approved App")
			}
			match = comment
		}
		if len(comments) < evidencePageSize {
			return match, nil
		}
	}
}

func ownedEvidenceComment(comment evidenceComment, appID int64) bool {
	return comment.User.Type == "Bot" && comment.App != nil && comment.App.ID == appID
}

// QueueRunEvidence persists comment work before remote calls and reopens only
// acknowledged delivery. Active ownership, review and retry waits are protected.
func QueueRunEvidence(ctx context.Context, backend state.Backend, expectedHead, owner, repository string, evidence RunEvidence, now time.Time) (string, error) {
	return QueueRunEvidenceBatch(ctx, backend, expectedHead, owner, repository, []RunEvidence{evidence}, now)
}

// QueueRunEvidenceBatch queues an exact cohort without repeating full snapshot reads.
func QueueRunEvidenceBatch(ctx context.Context, backend state.Backend, expectedHead, owner, repository string, evidences []RunEvidence, now time.Time) (string, error) {
	head, snapshot, err := state.ReadSnapshot(ctx, backend)
	if err != nil {
		return head, err
	}
	if head != expectedHead {
		return head, state.ErrHeadMoved
	}
	events := make([]projectionRecordEvent, 0, len(evidences))
	seen := map[string]bool{}
	for _, evidence := range evidences {
		if seen[evidence.ModuleID] {
			return head, fmt.Errorf("run evidence scope repeats a module")
		}
		seen[evidence.ModuleID] = true
		event, changed, queueErr := queuedEvidenceEvent(snapshot, owner, repository, evidence, now)
		if queueErr != nil {
			return head, queueErr
		}
		if changed {
			events = append(events, event)
		}
	}
	if len(events) == 0 {
		return head, nil
	}
	return commitProjectionEvents(ctx, backend, head, events)
}

func queuedEvidenceEvent(snapshot state.Snapshot, owner, repository string, evidence RunEvidence, now time.Time) (projectionRecordEvent, bool, error) {
	if err := evidence.Validate(owner, repository); err != nil {
		return projectionRecordEvent{}, false, err
	}
	intent, exists, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+evidence.ModuleID+".json")
	if err != nil || !exists {
		return projectionRecordEvent{}, false, fmt.Errorf("run evidence requires existing durable delivery")
	}
	if err := validateProjectionLifecycle(snapshot, intent); err != nil {
		return projectionRecordEvent{}, false, err
	}
	if protectedEvidenceDelivery(intent, now) {
		return projectionRecordEvent{}, false, fmt.Errorf("run evidence delivery is protected by ownership, retry or review")
	}
	evidenceID, err := evidence.ID()
	if err != nil {
		return projectionRecordEvent{}, false, err
	}
	binding, _, err := readProjectionBinding(snapshot, evidence.ModuleID)
	if err != nil {
		return projectionRecordEvent{}, false, err
	}
	if receipt, found := binding.EvidenceComments[evidenceID]; found && receipt.Outcome == EvidenceOutcomeCompleted {
		completed := evidence
		completed.Outcome, completed.Reason = EvidenceOutcomeCompleted, ""
		if receipt.Context != nil {
			completed.StateHead = receipt.Context.StateHead
		}
		contentID, contentErr := identity.ID(runEvidenceContentKind, 1, completed)
		if contentErr != nil {
			return projectionRecordEvent{}, false, contentErr
		}
		if receipt.ContentID == contentID {
			return projectionRecordEvent{}, false, nil
		}
	}
	if intent.PendingEvidence == nil {
		intent.PendingEvidence = map[string]RunEvidence{}
	}
	intent.PendingEvidence[evidenceID] = evidence
	if intent.DeliveryState == ProjectionProjected {
		intent.DeliveryState = ProjectionPending
	}
	intent.UpdatedAt = now.UTC()
	return projectionRecordEvent{path: projectionDeliveryPrefix + evidence.ModuleID + ".json", kind: "projection_delivery", value: intent}, true, nil
}

func protectedEvidenceDelivery(intent ProjectionIntentRecord, now time.Time) bool {
	return intent.DeliveryState == ProjectionNeedsReview ||
		intent.DeliveryState == ProjectionProjecting && now.Before(intent.LeaseExpiresAt) ||
		intent.DeliveryState == ProjectionRetryable && now.Before(intent.RetryAt)
}

func preservePendingEvidence(existing ProjectionIntentRecord, incoming *ProjectionIntentRecord) {
	if len(existing.PendingEvidence) == 0 {
		return
	}
	merged := make(map[string]RunEvidence, len(existing.PendingEvidence)+len(incoming.PendingEvidence))
	for key, evidence := range existing.PendingEvidence {
		merged[key] = evidence
	}
	for key, evidence := range incoming.PendingEvidence {
		merged[key] = evidence
	}
	incoming.PendingEvidence = merged
}

func validatePendingEvidence(intent ProjectionIntentRecord) error {
	for key, evidence := range intent.PendingEvidence {
		evidenceID, err := evidence.ID()
		if err != nil || evidenceID != key || evidence.ModuleID != intent.ModuleID {
			return fmt.Errorf("pending run evidence identity is invalid")
		}
	}
	if len(intent.PendingEvidence) > 0 && intent.DeliveryState == ProjectionProjected {
		return fmt.Errorf("acknowledged delivery still has pending run evidence")
	}
	return nil
}

func (client Client) synchronizePendingEvidence(ctx context.Context, intent ProjectionIntentRecord, issue Issue, options ProjectionRunOptions, binding *ProjectionBinding) (map[string]RunEvidenceReceipt, error) {
	delivered := make(map[string]RunEvidenceReceipt)
	keys := make([]string, 0, len(intent.PendingEvidence))
	currentKey := ""
	for key := range intent.PendingEvidence {
		evidence := intent.PendingEvidence[key]
		if evidence.RunID == options.EvidenceRunID && evidence.Phase == options.EvidencePhase {
			currentKey = key
		} else {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if currentKey != "" {
		keys = append(keys, currentKey)
	}
	if binding.EvidenceComments == nil {
		binding.EvidenceComments = map[string]RunEvidenceReceipt{}
	}
	for _, key := range keys {
		evidence := deliveredEvidenceContent(intent.PendingEvidence[key], options.EvidenceRunID, options.EvidencePhase)
		receipt, err := client.UpsertRunEvidence(ctx, options.Owner, options.Repository, issue.Number, evidence)
		if err != nil {
			return delivered, err
		}
		binding.EvidenceComments[key] = receipt
		delivered[key] = receipt
	}
	return delivered, nil
}

func applyEvidenceOutcome(intent *ProjectionIntentRecord, outcome ProjectionOutcome) error {
	for key, receipt := range outcome.DeliveredEvidence {
		evidence, pending := intent.PendingEvidence[key]
		if err := validateDeliveredEvidence(key, evidence, pending, receipt, outcome); err != nil {
			return err
		}
		delete(intent.PendingEvidence, key)
	}
	if outcome.Success && len(intent.PendingEvidence) > 0 {
		return fmt.Errorf("delivery cannot be acknowledged before run evidence comments")
	}
	if !outcome.Success {
		for key, evidence := range intent.PendingEvidence {
			if evidence.RunID == outcome.EvidenceRunID && evidence.Phase == outcome.EvidencePhase {
				evidence.Outcome, evidence.Reason = EvidenceOutcomeIncomplete, outcome.ErrorClass
				intent.PendingEvidence[key] = evidence
			}
		}
	}
	return nil
}

func validateDeliveredEvidence(key string, evidence RunEvidence, pending bool, receipt RunEvidenceReceipt, outcome ProjectionOutcome) error {
	if !pending || receipt.EvidenceID != key || receipt.CommentID <= 0 || receipt.RunID != evidence.RunID || receipt.Phase != evidence.Phase {
		return fmt.Errorf("run evidence receipt does not match pending work")
	}
	evidence = deliveredEvidenceContent(evidence, outcome.EvidenceRunID, outcome.EvidencePhase)
	contentID, err := identity.ID(runEvidenceContentKind, 1, evidence)
	if err != nil || receipt.ContentID != contentID || receipt.Outcome != evidence.Outcome {
		return fmt.Errorf("run evidence receipt does not match delivered content")
	}
	return nil
}

func deliveredEvidenceContent(evidence RunEvidence, runID, phase string) RunEvidence {
	if evidence.RunID == runID && evidence.Phase == phase {
		evidence.Outcome, evidence.Reason = EvidenceOutcomeCompleted, ""
	} else if evidence.Outcome == EvidenceOutcomePending {
		evidence.Outcome, evidence.Reason = EvidenceOutcomeIncomplete, evidenceReasonUnacknowledged
	}
	return evidence
}

func validateEvidenceReceipts(receipts map[string]RunEvidenceReceipt) error {
	for key, receipt := range receipts {
		if receipt.EvidenceID != key || receipt.CommentID <= 0 || !evidenceRunPattern.MatchString(receipt.RunID) || !evidencePhasePattern.MatchString(receipt.Phase) {
			return fmt.Errorf("run evidence receipt identity is invalid")
		}
		if err := identity.ValidateRecordID(runEvidenceKind, key); err != nil {
			return err
		}
		if err := identity.ValidateRecordID(runEvidenceContentKind, receipt.ContentID); err != nil {
			return err
		}
		if receipt.Outcome != EvidenceOutcomeCompleted && receipt.Outcome != EvidenceOutcomeIncomplete && receipt.Outcome != EvidenceOutcomePending {
			return fmt.Errorf("run evidence receipt outcome is invalid")
		}
		if receipt.Context != nil {
			if err := validateReceiptContext(receipt); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateReceiptContext(receipt RunEvidenceReceipt) error {
	contentID, err := identity.ID(runEvidenceContentKind, 1, *receipt.Context)
	if err != nil || contentID != receipt.ContentID {
		return fmt.Errorf("run evidence receipt context is invalid")
	}
	evidenceID, err := receipt.Context.ID()
	if err != nil || evidenceID != receipt.EvidenceID {
		return fmt.Errorf("run evidence receipt context identity is invalid")
	}
	return nil
}
