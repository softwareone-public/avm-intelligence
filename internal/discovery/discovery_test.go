package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

func TestPersistEmitsCatalogProposalWithoutStableSources(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(now)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/releases") {
			t.Errorf("unexpected request %s", request.URL.Path)
			http.NotFound(response, request)
			return
		}
		_, _ = response.Write([]byte(`[]`))
	}))
	defer server.Close()
	backend := newDiscoveryMemoryBackend()

	result, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, Options{
		AcceptedAt: now.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertReleaseLessCatalogTransaction(t, backend, result, len(candidate.Modules))
}

func assertReleaseLessCatalogTransaction(t *testing.T, backend *memoryBackend, result Result, moduleCount int) {
	t.Helper()
	if result.Head != "head-1" || backend.commitCount != 1 {
		t.Fatalf("discovery transaction head=%q commits=%d, want one atomic commit", result.Head, backend.commitCount)
	}
	if _, exists := backend.snapshot.Records["state/discovery/catalog.json"]; !exists {
		t.Fatal("accepted discovery state was not persisted")
	}
	if countRecordsWithKind(backend.snapshot, "discovery_observation") != 1 {
		t.Fatal("full catalog observation was not persisted")
	}
	if countRecordsWithKind(backend.snapshot, "export") != 1 || result.ProposalID == "" {
		t.Fatal("catalog proposal was not queued without analysis output")
	}
	if countRecordsWithKind(backend.snapshot, "projection_intent") != moduleCount {
		t.Fatalf("blocked intents = %d, want %d", countRecordsWithKind(backend.snapshot, "projection_intent"), moduleCount)
	}
	for _, record := range backend.snapshot.Records {
		if record.Kind != "projection_intent" {
			continue
		}
		var intent ProjectionIntent
		if err := json.Unmarshal(record.Data, &intent); err != nil {
			t.Fatal(err)
		}
		if intent.Status != IntentBlocked {
			t.Fatalf("release-less module intent status = %q, want blocked", intent.Status)
		}
	}
}

func TestPersistNewOverallPinPreservesOlderTrackWork(t *testing.T) {
	start := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	candidateV4 := discoveryTestSnapshot(start)
	commitV4 := strings.Repeat("a", 40)
	serverV4 := newVersionServer(t, "v4.0.0", commitV4, "", "")
	backend := newDiscoveryMemoryBackend()
	options := Options{AcceptedAt: start.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}}
	first, err := Persist(context.Background(), source.Resolver{APIBase: serverV4.URL}, backend, candidateV4, options)
	serverV4.Close()
	if err != nil {
		t.Fatal(err)
	}
	moduleID := candidateV4.Modules[0].ID
	seededHead, assessmentID, pinPath := seedV4TrackAssessment(t, backend, first.Head, moduleID, commitV4, start)

	commitV5 := strings.Repeat("b", 40)
	serverV5 := newVersionServer(t, "v5.0.0", commitV5, "v4.0.0", commitV4)
	defer serverV5.Close()
	candidateV5 := discoveryTestSnapshot(start.Add(3 * time.Minute))
	options.AcceptedAt = start.Add(4 * time.Minute)
	second, err := Persist(context.Background(), source.Resolver{APIBase: serverV5.URL}, backend, candidateV5, options)
	if err != nil {
		t.Fatal(err)
	}
	assertV5PreservesV4(t, backend, second, v5PreservationExpectation{
		seededHead: seededHead, assessmentID: assessmentID, pinPath: pinPath,
		commitV4: commitV4, commitV5: commitV5,
	})
}

func seedV4TrackAssessment(t *testing.T, backend *memoryBackend, head, moduleID, commitV4 string, start time.Time) (string, string, string) {
	t.Helper()
	pinPath := "state/discovery/source-pins/" + moduleID + ".json"
	var history PinHistory
	if err := json.Unmarshal(backend.snapshot.Records[pinPath].Data, &history); err != nil {
		t.Fatal(err)
	}
	history.TrackCandidates["azurerm-4"] = *history.OverallLatest
	trackEvent, err := newStateEvent(pinPath, "source_pins", history)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := queue.NewAssessmentRequest(queue.AssessmentInput{
		ModuleID: moduleID, SourceSHA: commitV4, ContextID: "azurerm-4", Track: "azurerm-4",
		DesiredGeneration: 1, FactsID: "facts-" + strings.Repeat("d", 64),
		DefinitionIDs: []string{"definition-" + strings.Repeat("e", 64)},
	}, start.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	assessmentEvent, err := newStateEvent("state/requests/"+assessment.ID+".json", "request", assessment)
	if err != nil {
		t.Fatal(err)
	}
	seededHead, _, err := state.ApplyEventsAtHead(context.Background(), backend, head, []state.Event{trackEvent, assessmentEvent})
	if err != nil {
		t.Fatal(err)
	}
	return seededHead, assessment.ID, pinPath
}

type v5PreservationExpectation struct {
	seededHead   string
	assessmentID string
	pinPath      string
	commitV4     string
	commitV5     string
}

func assertV5PreservesV4(t *testing.T, backend *memoryBackend, second Result, expected v5PreservationExpectation) {
	t.Helper()
	if second.Head == expected.seededHead || second.ProposalID == "" || !second.Delta.NoChange {
		t.Fatalf("pin-only update = %#v, seeded head %s", second, expected.seededHead)
	}

	var updatedHistory PinHistory
	if err := json.Unmarshal(backend.snapshot.Records[expected.pinPath].Data, &updatedHistory); err != nil {
		t.Fatal(err)
	}
	if updatedHistory.OverallLatest == nil || updatedHistory.OverallLatest.Version != "v5.0.0" || len(updatedHistory.ReleaseHistory) != 2 {
		t.Fatalf("overall latest/release history = %#v", updatedHistory)
	}
	if updatedHistory.TrackCandidates["azurerm-4"].Commit != expected.commitV4 {
		t.Fatalf("v4 track candidate was overwritten: %#v", updatedHistory.TrackCandidates)
	}
	if _, exists := backend.snapshot.Records["state/requests/"+expected.assessmentID+".json"]; !exists {
		t.Fatal("pending v4 assessment was discarded by the v5 source update")
	}
	requestBySource := map[string]queue.Request{}
	for _, record := range backend.snapshot.Records {
		if record.Kind != "request" {
			continue
		}
		var request queue.Request
		if err := json.Unmarshal(record.Data, &request); err != nil {
			t.Fatal(err)
		}
		if request.Work != nil && request.Work.Phase == queue.PhaseAcquireSource {
			requestBySource[request.SourceSHA] = request
		}
	}
	if requestBySource[expected.commitV4].Status != queue.StatusQueued || requestBySource[expected.commitV5].Status != queue.StatusQueued {
		t.Fatalf("v4/v5 acquisition requests = %#v", requestBySource)
	}
}

func TestPersistMovedTagCreatesReviewIntentAndPreservesAcceptedPin(t *testing.T) {
	start := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(start)
	acceptedCommit := strings.Repeat("a", 40)
	firstServer := newVersionServer(t, "v4.0.0", acceptedCommit, "", "")
	backend := newDiscoveryMemoryBackend()
	options := Options{AcceptedAt: start.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}}
	first, err := Persist(context.Background(), source.Resolver{APIBase: firstServer.URL}, backend, candidate, options)
	firstServer.Close()
	if err != nil {
		t.Fatal(err)
	}

	movedCommit := strings.Repeat("b", 40)
	movedServer := newVersionServer(t, "v4.0.0", movedCommit, "", "")
	defer movedServer.Close()
	candidate.FetchedAt = start.Add(2 * time.Minute)
	options.AcceptedAt = start.Add(3 * time.Minute)
	second, err := Persist(context.Background(), source.Resolver{APIBase: movedServer.URL}, backend, candidate, options)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Delta.NoChange || second.ProposalID != "" || second.Head == first.Head {
		t.Fatalf("moved-tag transaction = %#v, first head %s", second, first.Head)
	}

	moduleID := candidate.Modules[0].ID
	var history PinHistory
	if err = json.Unmarshal(backend.snapshot.Records["state/discovery/source-pins/"+moduleID+".json"].Data, &history); err != nil {
		t.Fatal(err)
	}
	if history.OverallLatest == nil || history.OverallLatest.Commit != acceptedCommit || len(history.ReleaseHistory) != 1 {
		t.Fatalf("moved tag overwrote accepted pin history: %#v", history)
	}
	var intent ProjectionIntent
	if err = json.Unmarshal(backend.snapshot.Records["state/projection/intents/"+moduleID+".json"].Data, &intent); err != nil {
		t.Fatal(err)
	}
	if intent.Status != IntentNeedsReview || !strings.Contains(intent.Reason, "moved accepted release tag") {
		t.Fatalf("moved-tag intent = %#v", intent)
	}
}

func TestPersistRetiresOnlyAfterTwoSuccessfulObservations(t *testing.T) {
	start := time.Date(2026, 10, 2, 16, 0, 0, 0, time.UTC)
	candidate := discoveryTestSnapshot(start)
	retiringID := "avm-res-resources-storage"
	server := newVersionServer(t, "v1.0.0", strings.Repeat("c", 40), "", "")
	defer server.Close()
	backend := newDiscoveryMemoryBackend()
	options := Options{AcceptedAt: start.Add(time.Minute), ExtractorContract: "factory-hcl-v1", Capabilities: []string{"native-hcl-v1"}}
	if _, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, candidate, options); err != nil {
		t.Fatal(err)
	}

	withoutRetiring := removeDiscoveryModule(candidate, retiringID, start.Add(2*time.Minute))
	options.AcceptedAt = start.Add(3 * time.Minute)
	second, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, withoutRetiring, options)
	if err != nil {
		t.Fatal(err)
	}
	var pendingIntent ProjectionIntent
	if err = json.Unmarshal(backend.snapshot.Records["state/projection/intents/"+retiringID+".json"].Data, &pendingIntent); err != nil {
		t.Fatal(err)
	}
	if pendingIntent.Status == IntentRetired || !containsModule(second.Delta.PendingRemoval, retiringID) {
		t.Fatalf("first omission retired module: intent=%#v delta=%#v", pendingIntent, second.Delta)
	}

	withoutRetiring.FetchedAt = start.Add(4 * time.Minute)
	options.AcceptedAt = start.Add(5 * time.Minute)
	third, err := Persist(context.Background(), source.Resolver{APIBase: server.URL}, backend, withoutRetiring, options)
	if err != nil {
		t.Fatal(err)
	}
	var retiredIntent ProjectionIntent
	if err = json.Unmarshal(backend.snapshot.Records["state/projection/intents/"+retiringID+".json"].Data, &retiredIntent); err != nil {
		t.Fatal(err)
	}
	if retiredIntent.Status != IntentRetired || !containsModule(third.Delta.Retired, retiringID) {
		t.Fatalf("second omission did not persist retirement: intent=%#v delta=%#v", retiredIntent, third.Delta)
	}
}

func newVersionServer(t *testing.T, currentVersion, currentCommit, previousVersion, previousCommit string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/releases") {
			_, _ = fmt.Fprintf(response, `[{"tag_name":%q,"html_url":"https://example.invalid/%s","published_at":"2026-01-01T00:00:00Z"}]`, currentVersion, currentVersion)
			return
		}
		if strings.Contains(request.URL.Path, "/git/ref/tags/") {
			tag := request.URL.Path[strings.LastIndex(request.URL.Path, "/git/ref/tags/")+len("/git/ref/tags/"):]
			if tag == currentVersion {
				_, _ = fmt.Fprintf(response, `{"object":{"type":"commit","sha":%q}}`, currentCommit)
				return
			}
			if tag == previousVersion && previousVersion != "" {
				_, _ = fmt.Fprintf(response, `{"object":{"type":"commit","sha":%q}}`, previousCommit)
				return
			}
			http.NotFound(response, request)
			return
		}
		t.Errorf("unexpected request %s", request.URL.Path)
		http.NotFound(response, request)
	}))
}

func discoveryTestSnapshot(fetchedAt time.Time) catalog.Snapshot {
	modules := []catalog.Module{
		{
			ID: "avm-res-resources-resourcegroup", Type: catalog.Resource, Status: "Available",
			DisplayName: "Resource Group", Description: "AVM resource module",
			RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-resources-resourcegroup",
			RegistryURL:   "https://registry.terraform.io/modules/Azure/avm-res-resources-resourcegroup",
		},
		{ID: "avm-res-resources-storage", Type: catalog.Resource, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-azurerm-avm-res-resources-storage", RegistryURL: "https://registry.terraform.io/modules/Azure/avm-res-resources-storage", DisplayName: "Storage", Description: "Storage module"},
		{ID: "avm-ptn-test", Type: catalog.Pattern, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-avm-ptn-test", RegistryURL: "https://registry.terraform.io/modules/Azure/avm-ptn-test", DisplayName: "Pattern", Description: "Pattern module"},
		{ID: "avm-utl-test", Type: catalog.Utility, Status: "Available", RepositoryURL: "https://github.com/Azure/terraform-avm-utl-test", RegistryURL: "https://registry.terraform.io/modules/Azure/avm-utl-test", DisplayName: "Utility", Description: "Utility module"},
	}
	feeds := make([]catalog.FeedSnapshot, 0, len(catalog.OfficialSources))
	for _, sourceInfo := range catalog.OfficialSources {
		count := 0
		for _, module := range modules {
			if module.Type == sourceInfo.Kind {
				count++
			}
		}
		digest := strings.Repeat("a", 64)
		if sourceInfo.Kind == catalog.Pattern {
			digest = strings.Repeat("b", 64)
		} else if sourceInfo.Kind == catalog.Utility {
			digest = strings.Repeat("c", 64)
		}
		feeds = append(feeds, catalog.FeedSnapshot{Kind: sourceInfo.Kind, URL: sourceInfo.URL, SHA256: digest, ModuleCount: count})
	}
	return catalog.Snapshot{SchemaVersion: catalog.SnapshotSchemaVersion, FetchedAt: fetchedAt, Feeds: feeds, Modules: modules}
}

func removeDiscoveryModule(snapshot catalog.Snapshot, moduleID string, fetchedAt time.Time) catalog.Snapshot {
	updated := snapshot
	updated.FetchedAt = fetchedAt
	updated.Modules = make([]catalog.Module, 0, len(snapshot.Modules)-1)
	counts := make(map[catalog.Kind]int)
	for _, module := range snapshot.Modules {
		if module.ID == moduleID {
			continue
		}
		updated.Modules = append(updated.Modules, module)
		counts[module.Type]++
	}
	for index := range updated.Feeds {
		updated.Feeds[index].ModuleCount = counts[updated.Feeds[index].Kind]
		if updated.Feeds[index].Kind == catalog.Resource {
			updated.Feeds[index].SHA256 = strings.Repeat("d", 64)
		}
	}
	return updated
}

func countRecordsWithKind(snapshot state.Snapshot, kind string) int {
	count := 0
	for _, record := range snapshot.Records {
		if record.Kind == kind {
			count++
		}
	}
	return count
}

type memoryBackend struct {
	mu          sync.Mutex
	head        string
	snapshot    state.Snapshot
	commitCount int
}

func newDiscoveryMemoryBackend() *memoryBackend {
	return &memoryBackend{head: "head-0", snapshot: state.NewSnapshot()}
}

func (backend *memoryBackend) Read(ctx context.Context) (string, state.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return "", state.Snapshot{}, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.head, cloneSnapshot(backend.snapshot), nil
}

func (backend *memoryBackend) Commit(ctx context.Context, expectedHead string, snapshot state.Snapshot) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.head != expectedHead {
		return "", state.ErrHeadMoved
	}
	backend.commitCount++
	backend.head = fmt.Sprintf("head-%d", backend.commitCount)
	backend.snapshot = cloneSnapshot(snapshot)
	return backend.head, nil
}

func cloneSnapshot(snapshot state.Snapshot) state.Snapshot {
	encoded, _ := json.Marshal(snapshot)
	var cloned state.Snapshot
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}
