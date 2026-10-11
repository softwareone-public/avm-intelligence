package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/progress"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestRunEvidenceUsesDistinctRunPhaseMarkersAndUpdatesRetries(t *testing.T) {
	comments := map[int64]evidenceComment{}
	var nextID int64
	posts, patches := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet {
			values := make([]evidenceComment, 0, len(comments))
			for _, comment := range comments {
				values = append(values, comment)
			}
			_ = json.NewEncoder(response).Encode(values)
			return
		}
		var input map[string]string
		_ = json.NewDecoder(request.Body).Decode(&input)
		var comment evidenceComment
		if request.Method == http.MethodPost {
			nextID++
			posts++
			comment.ID = nextID
		} else {
			patches++
			_, _ = fmt.Sscanf(request.URL.Path, "/repos/example/repo/issues/comments/%d", &comment.ID)
		}
		comment.Body, comment.User.Type = input["body"], "Bot"
		comment.App = &struct {
			ID int64 `json:"id"`
		}{ID: 123}
		comments[comment.ID] = comment
		_ = json.NewEncoder(response).Encode(comment)
	}))
	defer server.Close()
	client := Client{Token: "private-token", EvidenceAppID: "123", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		request.URL.Scheme = "http"
		request.URL.Host = server.Listener.Addr().String()
		return server.Client().Transport.RoundTrip(request)
	})}}
	evidence := evidenceFixture()
	first, err := client.UpsertRunEvidence(context.Background(), "example", "repo", 1, evidence)
	if err != nil {
		t.Fatal(err)
	}
	evidence.StateHead = strings.Repeat("b", 40)
	updated, err := client.UpsertRunEvidence(context.Background(), "example", "repo", 1, evidence)
	if err != nil || first.CommentID != updated.CommentID {
		t.Fatalf("same run retry created another comment: %+v %v", updated, err)
	}
	assertEvidenceVariants(t, client, evidence)
	if posts != 4 || patches != 1 {
		t.Fatalf("run/phase/attempt comments posts=%d patches=%d", posts, patches)
	}
}

func TestRunEvidenceCommentScanStopsAtReviewedPageLimit(t *testing.T) {
	pageCalls := 0
	client := Client{Token: "token", EvidenceAppID: "123", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		pageCalls++
		comments := make([]map[string]any, evidencePageSize)
		for index := range comments {
			comments[index] = map[string]any{"id": int64(pageCalls*evidencePageSize + index + 1), "body": "unrelated human comment"}
		}
		encoded, err := json.Marshal(comments)
		if err != nil {
			return nil, err
		}
		return jsonResponse(string(encoded))
	})}}
	_, err := client.UpsertRunEvidence(context.Background(), "example", "repo", 1, RunEvidence{
		ModuleID: "avm-res-evidence-limit", Phase: EvidencePhaseDiscovery, RunID: "123-1",
		RunURL: "https://github.com/example/repo/actions/runs/123", SourceVersion: "1.0.0",
		SourceSHA: strings.Repeat("a", 40), StateHead: "head-0", Status: "backlog", Outcome: EvidenceOutcomeCompleted,
	})
	if err == nil || !strings.Contains(err.Error(), "reviewed 10-page limit") {
		t.Fatalf("evidence comment scan error = %v", err)
	}
	if pageCalls != maxEvidenceCommentPages {
		t.Fatalf("evidence comment pages requested = %d, want bounded %d", pageCalls, maxEvidenceCommentPages)
	}
}

func assertEvidenceVariants(t *testing.T, client Client, evidence RunEvidence) {
	t.Helper()
	for _, changed := range evidenceVariants(evidence) {
		if _, err := client.UpsertRunEvidence(context.Background(), "example", "repo", 1, changed); err != nil {
			t.Fatal(err)
		}
	}
}

func evidenceVariants(evidence RunEvidence) []RunEvidence {
	newRun, newPhase, newAttempt := evidence, evidence, evidence
	newRun.RunID, newRun.RunURL = "2-1", "https://github.com/example/repo/actions/runs/2"
	newPhase.Phase = "assessment"
	newAttempt.RunID = "1-2"
	return []RunEvidence{newRun, newPhase, newAttempt}
}

func TestRunEvidenceRefusesHumanOrOtherAppMarker(t *testing.T) {
	for _, author := range []string{"human", "other-app"} {
		t.Run(author, func(t *testing.T) {
			evidence := evidenceFixture()
			evidenceID, _ := evidence.ID()
			client := Client{Token: "fixture", EvidenceAppID: "123", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet {
					t.Fatal("unowned comment was mutated")
				}
				userType, appID := "Bot", 456
				if author == "human" {
					userType, appID = "User", 123
				}
				return jsonResponse(fmt.Sprintf(`[{"id":1,"body":%q,"user":{"type":%q},"performed_via_github_app":{"id":%d}}]`, evidenceMarkerPrefix+evidenceID+" -->", userType, appID))
			})}}
			if _, err := client.UpsertRunEvidence(context.Background(), "example", "repo", 1, evidence); err == nil {
				t.Fatal("unowned marker was accepted")
			}
		})
	}
}

func TestEvidenceCommentCreateAndUpdateRecoverLostResponses(t *testing.T) {
	for _, mutation := range []string{"create", "update"} {
		t.Run(mutation, func(t *testing.T) {
			evidence := evidenceFixture()
			evidenceID, err := evidence.ID()
			if err != nil {
				t.Fatal(err)
			}
			marker := evidenceMarkerPrefix + evidenceID + " -->"
			transport := &lostEvidenceMutationTransport{lose: mutation}
			if mutation == "update" {
				transport.comments = []evidenceComment{ownedEvidenceFixtureComment(9, marker+"\nold content")}
			}
			client := Client{Token: "fixture", EvidenceAppID: "123", HTTPClient: &http.Client{Transport: transport}}
			if _, err = client.UpsertRunEvidence(context.Background(), "example", "repo", 1, evidence); err == nil {
				t.Fatal("first comment mutation response was not lost")
			}
			receipt, err := client.UpsertRunEvidence(context.Background(), "example", "repo", 1, evidence)
			if err != nil || receipt.CommentID != 9 {
				t.Fatalf("comment retry receipt=%+v error=%v", receipt, err)
			}
			if transport.posts != boolInt(mutation == "create") || transport.patches != boolInt(mutation == "update") {
				t.Fatalf("comment retry duplicated mutation: posts=%d patches=%d", transport.posts, transport.patches)
			}
			if len(transport.comments) != 1 || transport.comments[0].Body != evidence.body("example", "repo", evidenceID) {
				t.Fatalf("comment effect was not reconciled exactly: %#v", transport.comments)
			}
		})
	}
}

func ownedEvidenceFixtureComment(id int64, body string) evidenceComment {
	comment := evidenceComment{ID: id, Body: body}
	comment.User.Type = "Bot"
	comment.App = &struct {
		ID int64 `json:"id"`
	}{ID: 123}
	return comment
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

type lostEvidenceMutationTransport struct {
	comments []evidenceComment
	lose     string
	failed   bool
	posts    int
	patches  int
}

func (transport *lostEvidenceMutationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodGet {
		encoded, err := json.Marshal(transport.comments)
		if err != nil {
			return nil, err
		}
		return jsonResponse(string(encoded))
	}
	var input struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
		return nil, err
	}
	if request.Method == http.MethodPost {
		transport.posts++
		transport.comments = append(transport.comments, ownedEvidenceFixtureComment(9, input.Body))
		if transport.lose == "create" && !transport.failed {
			transport.failed = true
			return nil, fmt.Errorf("fixture lost response after creating evidence comment")
		}
	} else {
		transport.patches++
		transport.comments[0].Body = input.Body
		if transport.lose == "update" && !transport.failed {
			transport.failed = true
			return nil, fmt.Errorf("fixture lost response after updating evidence comment")
		}
	}
	encoded, err := json.Marshal(transport.comments[0])
	if err != nil {
		return nil, err
	}
	return jsonResponse(string(encoded))
}

type evidenceDeliveryTransport struct {
	board       projectionBatchTransport
	comments    []evidenceComment
	failComment bool
	posts       int
}

func (transport *evidenceDeliveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if strings.Contains(request.URL.Path, "/comments") {
		if request.Method == http.MethodGet {
			data, err := json.Marshal(transport.comments)
			if err != nil {
				return nil, err
			}
			return jsonResponse(string(data))
		}
		if transport.failComment {
			transport.failComment = false
			response := statusResponse(http.StatusTooManyRequests, `{"message":"private-api-diagnostic"}`)
			response.Header.Set("Retry-After", "60")
			return response, nil
		}
		if request.Method == http.MethodPost {
			transport.posts++
		}
		var input map[string]string
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			return nil, err
		}
		comment := evidenceComment{ID: 1, Body: input["body"]}
		comment.User.Type = "Bot"
		comment.App = &struct {
			ID int64 `json:"id"`
		}{ID: 123}
		transport.comments = []evidenceComment{comment}
		data, err := json.Marshal(comment)
		if err != nil {
			return nil, err
		}
		return jsonResponse(string(data))
	}
	return transport.board.RoundTrip(request)
}

func TestEvidenceFailurePreventsAcknowledgementAndRetryUsesSameIssue(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Now().UTC()
	projection := projectionForModule("avm-res-example")
	projection.Status = StatusPublishing
	projection.IssueBody = projection.body()
	intent, err := NewProjectionIntent(projection, 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
	if err != nil {
		t.Fatal(err)
	}
	evidence := evidenceFixture()
	evidence.RunURL = "https://github.com/softwareone-public/avm-intelligence/actions/runs/1"
	evidence.StateHead, evidence.Status = saved.Head, projection.Status
	head, err := QueueRunEvidence(context.Background(), projectionCoordinator(backend), "softwareone-public", "avm-intelligence", evidence, now)
	if err != nil {
		t.Fatal(err)
	}
	transport := &evidenceDeliveryTransport{comments: []evidenceComment{}, failComment: true}
	client := Client{Token: "fixture", EvidenceAppID: "123", Budget: evidenceTestBudget(), MetadataCache: &ProjectionMetadataCache{}, HTTPClient: &http.Client{Transport: transport}}
	first := deliverEvidenceFixture(t, backend, client, head, now)
	stored, _, err := readProjectionIntent(backend.snapshot, projectionDeliveryPrefix+intent.ModuleID+".json")
	if err != nil || first.Completed != 0 || !errors.Is(first.Cause, ErrGitHubRateLimited) || stored.DeliveryState != ProjectionRetryable || len(stored.PendingEvidence) != 1 || stored.LastAcknowledgedFingerprint != "" {
		t.Fatalf("comment failure was acknowledged or lost: %+v %+v %v", first, stored, err)
	}
	client.Budget = evidenceTestBudget()
	resumeAt := stored.RetryAt.Add(time.Second)
	head, err = QueueRunEvidence(context.Background(), projectionCoordinator(backend), "softwareone-public", "avm-intelligence", evidence, resumeAt)
	if err != nil {
		t.Fatal(err)
	}
	second := deliverEvidenceFixture(t, backend, client, head, resumeAt)
	stored, _, err = readProjectionIntent(backend.snapshot, projectionDeliveryPrefix+intent.ModuleID+".json")
	if err != nil || second.Completed != 1 || stored.DeliveryState != ProjectionProjected || len(stored.PendingEvidence) != 0 || transport.posts != 1 || transport.board.created != 1 {
		t.Fatalf("evidence retry duplicated Issue/comment or failed acknowledgement: %+v %+v posts=%d issues=%d %v", second, stored, transport.posts, transport.board.created, err)
	}
	if !strings.Contains(transport.comments[0].Body, "- Phase outcome: `completed`") {
		t.Fatal("same-run recovery did not update the completed phase outcome")
	}
}

func deliverEvidenceFixture(t *testing.T, backend state.Backend, client Client, head string, now time.Time) ProjectionRunResult {
	t.Helper()
	claim, err := ClaimProjectionBatchForModules(context.Background(), projectionCoordinator(backend), ProjectionClaimOptions{RunID: "1-1", Now: now, Lease: time.Minute, Limit: 1}, []string{"avm-res-example"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), claim, ProjectionRunOptions{Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute, EvidenceRunID: "1-1", EvidencePhase: EvidencePhaseDiscovery})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type lostEvidenceCheckpointBackend struct {
	inner       *projectionMemoryBackend
	coordinator *projectionTestCoordinator
	moduleID    string
	fail        bool
	failRetry   bool
}

func (backend *lostEvidenceCheckpointBackend) Read(ctx context.Context) (string, state.Snapshot, error) {
	return backend.inner.Read(ctx)
}

func (backend *lostEvidenceCheckpointBackend) Commit(ctx context.Context, head string, snapshot state.Snapshot) (string, error) {
	intent, _, err := readProjectionIntent(snapshot, projectionDeliveryPrefix+backend.moduleID+".json")
	if err != nil {
		return "", err
	}
	if backend.fail && (intent.DeliveryState == ProjectionProjected || backend.failRetry && intent.DeliveryState == ProjectionRetryable) {
		backend.fail = false
		return "", state.ErrHeadMoved
	}
	return backend.inner.Commit(ctx, head, snapshot)
}

func (backend *lostEvidenceCheckpointBackend) RunTransaction(ctx context.Context, operationID string, decide state.TransactionDecisionFunc, resolve state.TransactionResultResolver) (state.TransactionResult, error) {
	if backend.coordinator == nil {
		coordinator := projectionTestCoordinator{Backend: backend}
		backend.coordinator = &coordinator
	}
	return backend.coordinator.RunTransaction(ctx, operationID, decide, resolve)
}

func TestPostedEvidenceWithLostCheckpointIsNotDuplicated(t *testing.T) {
	inner := newProjectionMemoryBackend()
	now := time.Now().UTC()
	projection := projectionForModule("avm-res-example")
	projection.Status, projection.IssueBody = StatusPublishing, ""
	projection.IssueBody = projection.body()
	intent, err := NewProjectionIntent(projection, 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(inner), intent)
	if err != nil {
		t.Fatal(err)
	}
	evidence := evidenceFixture()
	evidence.RunURL, evidence.StateHead, evidence.Status = "https://github.com/softwareone-public/avm-intelligence/actions/runs/1", saved.Head, projection.Status
	_, err = QueueRunEvidence(context.Background(), projectionCoordinator(inner), "softwareone-public", "avm-intelligence", evidence, now)
	if err != nil {
		t.Fatal(err)
	}
	backend := &lostEvidenceCheckpointBackend{inner: inner, moduleID: intent.ModuleID, fail: true}
	transport := &evidenceDeliveryTransport{comments: []evidenceComment{}}
	client := Client{Token: "fixture", EvidenceAppID: "123", Budget: evidenceTestBudget(), MetadataCache: &ProjectionMetadataCache{}, HTTPClient: &http.Client{Transport: transport}}
	claim, err := ClaimProjectionBatchForModules(context.Background(), projectionCoordinator(backend), ProjectionClaimOptions{RunID: "1-1", Now: now, Lease: time.Minute, Limit: 1}, []string{intent.ModuleID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), claim, ProjectionRunOptions{Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute, EvidenceRunID: "1-1", EvidencePhase: EvidencePhaseDiscovery})
	if err != nil || transport.posts != 1 {
		t.Fatalf("projection checkpoint retry failed after contention: posts=%d error=%v", transport.posts, err)
	}
	stored, _, err := readProjectionIntent(inner.snapshot, projectionDeliveryPrefix+intent.ModuleID+".json")
	if err != nil || stored.DeliveryState != ProjectionProjected || len(stored.PendingEvidence) != 0 {
		t.Fatal("projection outcome did not resolve after contention retry")
	}
	result := deliverEvidenceFixture(t, backend, client, inner.head, now.Add(2*time.Minute))
	if result.Completed != 0 || transport.posts != 1 || transport.board.created != 1 {
		t.Fatalf("resolved checkpoint duplicated remote work: %+v comments=%d issues=%d", result, transport.posts, transport.board.created)
	}
}

func TestEvidencePaginationAndAmbiguousMarkers(t *testing.T) {
	evidence := evidenceFixture()
	evidenceID, _ := evidence.ID()
	marker := evidenceMarkerPrefix + evidenceID + " -->"
	pages := 0
	client := Client{Token: "fixture", EvidenceAppID: "123", HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		pages++
		if request.URL.Query().Get("page") == "1" {
			comments := make([]map[string]any, evidencePageSize)
			for index := range comments {
				comments[index] = map[string]any{"id": index + 1, "body": "human notes"}
			}
			data, err := json.Marshal(comments)
			if err != nil {
				return nil, err
			}
			return jsonResponse(string(data))
		}
		return jsonResponse(fmt.Sprintf(`[{"id":101,"body":%q,"user":{"type":"Bot"},"performed_via_github_app":{"id":123}}]`, marker))
	})}}
	comment, err := client.findEvidenceComment(context.Background(), "example", "repo", 1, marker, 123)
	if err != nil || comment.ID != 101 || pages != 2 {
		t.Fatalf("evidence marker pagination=%+v pages=%d error=%v", comment, pages, err)
	}
	client.HTTPClient.Transport = roundTrip(func(request *http.Request) (*http.Response, error) {
		return jsonResponse(fmt.Sprintf(`[{"id":1,"body":%q,"user":{"type":"Bot"},"performed_via_github_app":{"id":123}},{"id":2,"body":%q,"user":{"type":"Bot"},"performed_via_github_app":{"id":123}}]`, marker, marker))
	})
	if _, err := client.findEvidenceComment(context.Background(), "example", "repo", 1, marker, 123); err == nil {
		t.Fatal("duplicate evidence markers did not fail closed")
	}
}

func TestOldReceiptCannotClearUndeliveredEvidenceUpdate(t *testing.T) {
	evidence := evidenceFixture()
	evidenceID, _ := evidence.ID()
	intent := ProjectionIntentRecord{PendingEvidence: map[string]RunEvidence{evidenceID: evidence}}
	outcome := ProjectionOutcome{ErrorClass: ProjectionErrorBudget, Binding: &ProjectionBinding{EvidenceComments: map[string]RunEvidenceReceipt{evidenceID: {EvidenceID: evidenceID, CommentID: 1}}}}
	if err := applyEvidenceOutcome(&intent, outcome); err != nil || len(intent.PendingEvidence) != 1 {
		t.Fatal("old receipt removed an undelivered update")
	}
}

func evidenceTestBudget() *ProjectionBudget {
	return &ProjectionBudget{RESTRemaining: 10000, GraphQLRemaining: 10000, SearchRemaining: 10000, SecondaryRemaining: 10000}
}

func TestRunEvidenceQueuePreservesProtectedDelivery(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Now().UTC()
	intent, err := NewProjectionIntent(projectionForModule("avm-res-example"), 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimProjectionBatch(context.Background(), projectionCoordinator(backend), "existing-owner", now, time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	evidence := evidenceFixture()
	evidence.StateHead = claim.Head
	_, err = QueueRunEvidence(context.Background(), projectionCoordinator(backend), "example", "repo", evidence, now)
	if err == nil {
		t.Fatal("evidence queue bypassed an active owner")
	}
	head, _, err := state.ReadSnapshot(context.Background(), backend)
	if err != nil || head != claim.Head {
		t.Fatal("protected evidence queue mutated state")
	}
}

func TestBudgetWarningsDistinguishLocalAndGitHubWithModuleContext(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, serverLimit := range []bool{false, true} {
		var output bytes.Buffer
		slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
		ctx, finish := progress.Start(context.Background(), "projection", "project_field_update", slog.String("module_id", "avm-res-example"))
		calls := 0
		client := Client{Token: "private-token-sentinel", Budget: &ProjectionBudget{}, HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
			calls++
			response := statusResponse(http.StatusTooManyRequests, `{"message":"private-api-body-sentinel"}`)
			response.Header.Set("Retry-After", "60")
			return response, nil
		})}}
		if serverLimit {
			client.Budget = evidenceTestBudget()
		}
		err := client.rest(ctx, http.MethodGet, "/fixture", nil, nil)
		finish(false)
		want := "local_projection_budget_exhausted"
		if serverLimit {
			want = "github_rate_limited"
		}
		if !errors.Is(err, ErrProjectionBudgetExhausted) || errors.Is(err, ErrGitHubRateLimited) != serverLimit || (calls != 0) != serverLimit {
			t.Fatalf("budget origin or request count changed: calls=%d error=%v", calls, err)
		}
		assertBudgetWarningOutput(t, output.String(), want)
	}
}

func assertBudgetWarningOutput(t *testing.T, output, reason string) {
	t.Helper()
	for _, expected := range []string{"level=WARN", "projection request paused", reason, "module_id=avm-res-example", "phase=project_field_update", "secondary_remaining="} {
		if !strings.Contains(output, expected) {
			t.Errorf("missing budget warning %q: %s", expected, output)
		}
	}
	if strings.Contains(output, "private-token-sentinel") || strings.Contains(output, "private-api-body-sentinel") {
		t.Fatal("budget warning leaked private material")
	}
}

func TestEquivalentGenerationRetainsUnacknowledgedEvidence(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Now().UTC()
	intent, err := NewProjectionIntent(projectionForModule("avm-res-example"), 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	intent.DeliveryState, intent.LastAcknowledgedFingerprint = ProjectionProjected, intent.Fingerprint
	saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
	if err != nil {
		t.Fatal(err)
	}
	evidence := evidenceFixture()
	evidence.StateHead, evidence.Outcome = saved.Head, EvidenceOutcomePending
	_, err = QueueRunEvidence(context.Background(), projectionCoordinator(backend), "example", "repo", evidence, now)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := NewProjectionIntent(intent.Projection, 2, ProjectionPriorityChanged, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), newer); err != nil {
		t.Fatal(err)
	}
	stored, _, err := readProjectionIntent(backend.snapshot, projectionDeliveryPrefix+intent.ModuleID+".json")
	if err != nil || stored.DeliveryState != ProjectionPending || len(stored.PendingEvidence) != 1 {
		t.Fatalf("equivalent generation acknowledged pending evidence: %+v %v", stored, err)
	}
}

func TestSameAttemptReceiptSkipsOnlyIdenticalEvidenceContent(t *testing.T) {
	backend := newProjectionMemoryBackend()
	now := time.Now().UTC()
	evidence := evidenceFixture()
	intent, err := NewProjectionIntent(projectionForModule(evidence.ModuleID), 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	intent.DeliveryState, intent.LastAcknowledgedFingerprint = ProjectionProjected, intent.Fingerprint
	saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(backend), intent)
	if err != nil {
		t.Fatal(err)
	}
	evidence.StateHead = saved.Head
	evidenceID, _ := evidence.ID()
	contentID, _ := identity.ID(runEvidenceContentKind, 1, evidence)
	binding := projectionTestBinding(intent, now)
	binding.EvidenceComments = map[string]RunEvidenceReceipt{evidenceID: {EvidenceID: evidenceID, CommentID: 1, RunID: evidence.RunID, Phase: evidence.Phase, Outcome: EvidenceOutcomeCompleted, ContentID: contentID, Context: &evidence}}
	data, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	backend.snapshot.Records[projectionBindingPrefix+evidence.ModuleID+".json"] = state.Record{Kind: "projection_binding", SchemaVersion: 1, Data: data}
	unchanged := evidence
	unchanged.StateHead = "another-head"
	head, err := QueueRunEvidence(context.Background(), projectionCoordinator(backend), "example", "repo", unchanged, now)
	if err != nil || head != saved.Head {
		t.Fatal("checkpoint-only difference reopened identical evidence")
	}
	changed := unchanged
	changed.SourceSHA = strings.Repeat("b", 40)
	head, err = QueueRunEvidence(context.Background(), projectionCoordinator(backend), "example", "repo", changed, now)
	if err != nil || head == saved.Head {
		t.Fatal("same-attempt changed source was suppressed by an old receipt")
	}
	stored, _, err := readProjectionIntent(backend.snapshot, projectionDeliveryPrefix+evidence.ModuleID+".json")
	if err != nil || len(stored.PendingEvidence) != 1 || stored.DeliveryState != ProjectionPending {
		t.Fatal("changed evidence did not retain the same pending marker")
	}
}

func TestRecoveryKeepsEarlierRunPendingOutcomeIncomplete(t *testing.T) {
	evidence := evidenceFixture()
	evidence.Outcome = EvidenceOutcomePending
	converted := deliveredEvidenceContent(evidence, "2-1", EvidencePhaseProjectionRecovery)
	if converted.Outcome != EvidenceOutcomeIncomplete || converted.Reason != evidenceReasonUnacknowledged || converted.RunURL != evidence.RunURL {
		t.Fatal("recovery claimed completion for an untouched earlier run")
	}
	current := deliveredEvidenceContent(evidence, evidence.RunID, evidence.Phase)
	if current.Outcome != EvidenceOutcomeCompleted {
		t.Fatal("current successfully synchronized phase did not complete")
	}
}

func TestGraphQLRateLimitPreservesTypedCauseAndWarning(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	resetAt := time.Now().UTC().Add(15 * time.Minute).Truncate(time.Second)
	client := Client{Token: "fixture", Budget: evidenceTestBudget(), HTTPClient: &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
		response, err := jsonResponse(`{"errors":[{"type":"RATE_LIMITED","message":"private-diagnostic-sentinel"}]}`)
		response.Header.Set("X-RateLimit-Reset", fmt.Sprint(resetAt.Unix()))
		return response, err
	})}}
	err := client.graph(context.Background(), map[string]any{"query": "fixture"}, nil)
	if !errors.Is(err, ErrGitHubRateLimited) || !errors.Is(err, ErrProjectionBudgetExhausted) || !strings.Contains(output.String(), "github_rate_limited") || strings.Contains(output.String(), "private-diagnostic-sentinel") {
		t.Fatalf("GraphQL rate limit lost safe typed diagnostics: %v %s", err, output.String())
	}
	if next := client.Budget.Take(ProjectionGraphQL, time.Now()); !errors.Is(next, ErrGitHubRateLimited) {
		t.Fatal("GraphQL server pause became a local budget error")
	}
	var failure *ProjectionBudgetError
	if !errors.As(err, &failure) || !failure.RetryAt.Equal(resetAt) || !client.projectionResumeAt(time.Now().UTC(), time.Minute).Equal(resetAt) {
		t.Fatal("HTTP-200 GraphQL rate limit lost its server reset deadline")
	}
}

func TestCommentRateLimitAndLostCheckpointPreserveBothCauses(t *testing.T) {
	inner := newProjectionMemoryBackend()
	now := time.Now().UTC()
	projection := projectionForModule("avm-res-example")
	projection.Status = StatusPublishing
	projection.IssueBody = projection.body()
	intent, err := NewProjectionIntent(projection, 1, ProjectionPriorityChanged, now)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := SaveProjectionIntent(context.Background(), projectionCoordinator(inner), intent)
	if err != nil {
		t.Fatal(err)
	}
	evidence := evidenceFixture()
	evidence.StateHead, evidence.Status = saved.Head, projection.Status
	evidence.RunURL = "https://github.com/softwareone-public/avm-intelligence/actions/runs/1"
	_, err = QueueRunEvidence(context.Background(), projectionCoordinator(inner), "softwareone-public", "avm-intelligence", evidence, now)
	if err != nil {
		t.Fatal(err)
	}
	backend := &lostEvidenceCheckpointBackend{inner: inner, moduleID: intent.ModuleID, fail: true, failRetry: true}
	backend.coordinator = func() *projectionTestCoordinator {
		coordinator := projectionTestCoordinator{Backend: backend}
		return &coordinator
	}()
	claim, err := ClaimProjectionBatchForModules(context.Background(), projectionCoordinator(backend), ProjectionClaimOptions{RunID: "1-1", Now: now, Lease: time.Minute, Limit: 1}, []string{intent.ModuleID})
	if err != nil {
		t.Fatal(err)
	}
	transport := &evidenceDeliveryTransport{comments: []evidenceComment{}, failComment: true}
	client := Client{Token: "fixture", EvidenceAppID: "123", Budget: evidenceTestBudget(), HTTPClient: &http.Client{Transport: transport}}
	result, err := client.ProjectClaimedBatch(context.Background(), backend, projectionCoordinator(backend), claim, ProjectionRunOptions{Owner: "softwareone-public", Repository: "avm-intelligence", ProjectNumber: 1, RetryDelay: time.Minute, EvidenceRunID: "1-1", EvidencePhase: EvidencePhaseDiscovery})
	if !errors.Is(result.Cause, ErrGitHubRateLimited) || transport.posts != 0 || len(result.Errors) != 1 {
		t.Fatalf("checkpoint hid comment limit: result=%+v error=%v", result, err)
	}
}

func evidenceFixture() RunEvidence {
	return RunEvidence{ModuleID: "avm-res-example", Phase: EvidencePhaseDiscovery, RunID: "1-1", RunURL: "https://github.com/example/repo/actions/runs/1", SourceVersion: "v1.0.0", SourceSHA: strings.Repeat("a", 40), StateHead: strings.Repeat("a", 40), Status: StatusBacklog, Outcome: EvidenceOutcomeCompleted}
}
