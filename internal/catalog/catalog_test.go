package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseOfficialFeedFixtures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		kind Kind
		name string
		want string
	}{
		{Resource, "resource.csv", "avm-res-resources-resourcegroup"},
		{Pattern, "pattern.csv", "avm-ptn-example"},
		{Utility, "utility.csv", "avm-utl-example"},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "catalog", test.name))
			if err != nil {
				t.Fatal(err)
			}
			modules, err := ParseFeed(test.kind, data)
			if err != nil {
				t.Fatalf("ParseFeed() error = %v", err)
			}
			if len(modules) != 1 || modules[0].ID != test.want {
				t.Fatalf("modules = %#v", modules)
			}
		})
	}
}

func TestParseFeedAcceptsBOMAndQuotedCSV(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "catalog", "resource.csv"))
	if err != nil {
		t.Fatal(err)
	}
	data = append([]byte{0xef, 0xbb, 0xbf}, data...)
	modules, err := ParseFeed(Resource, data)
	if err != nil {
		t.Fatal(err)
	}
	if modules[0].Description != "AVM resource module, for groups" {
		t.Fatalf("description = %q", modules[0].Description)
	}
}

func TestParseFeedsRejectsPartialAndDuplicateCatalogs(t *testing.T) {
	t.Parallel()
	resource := readFixture(t, "resource.csv")
	pattern := readFixture(t, "pattern.csv")
	utility := readFixture(t, "utility.csv")
	if _, err := ParseFeeds(map[Kind][]byte{Resource: resource}); err == nil {
		t.Fatal("partial feed set was accepted")
	}
	rows := strings.SplitN(strings.TrimSpace(string(resource)), "\n", 2)
	duplicateResource := []byte(rows[0] + "\n" + rows[1] + "\n" + rows[1] + "\n")
	if _, err := ParseFeeds(map[Kind][]byte{Resource: duplicateResource, Pattern: pattern, Utility: utility}); err == nil || !strings.Contains(err.Error(), "duplicate module ID") {
		t.Fatalf("duplicate identity error = %v", err)
	}
}

func TestBuildSnapshotRecordsEachFeedDigest(t *testing.T) {
	t.Parallel()
	feeds := map[Kind][]byte{
		Resource: readFixture(t, "resource.csv"),
		Pattern:  readFixture(t, "pattern.csv"),
		Utility:  readFixture(t, "utility.csv"),
	}
	modules, err := ParseFeeds(feeds)
	if err != nil {
		t.Fatal(err)
	}
	fetchedAt := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	snapshot := buildSnapshot(feeds, modules, fetchedAt)
	if snapshot.SchemaVersion != SnapshotSchemaVersion || !snapshot.FetchedAt.Equal(fetchedAt) || len(snapshot.Feeds) != 3 {
		t.Fatalf("snapshot metadata = %#v", snapshot)
	}
	for _, feed := range snapshot.Feeds {
		if len(feed.SHA256) != 64 || feed.ModuleCount != 1 || !strings.Contains(feed.URL, "Azure-Verified-Modules/main") {
			t.Errorf("feed provenance = %#v", feed)
		}
	}
}

func TestParseFeedRejectsInvalidHeadersStatusAndRepositoryURL(t *testing.T) {
	t.Parallel()
	resource := string(readFixture(t, "resource.csv"))
	cases := []struct {
		name string
		csv  string
	}{
		{"missing header", strings.Replace(resource, "ModuleStatus,", "", 1)},
		{"unknown status", strings.Replace(resource, ",Available,", ",Active,", 1)},
		{"non-Azure repository", strings.Replace(resource, "github.com/Azure/", "github.com/other/", 1)},
		{"wrong registry module", strings.Replace(resource, "modules/Azure/avm-res-resources-resourcegroup", "modules/Azure/other", 1)},
		{"empty stable ID", strings.Replace(resource, ",avm-res-resources-resourcegroup,", ",,", 1)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseFeed(Resource, []byte(test.csv)); err == nil {
				t.Fatal("invalid feed was accepted")
			}
		})
	}
}

func TestFetchRetriesTransientResponse(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte("feed"))
	}))
	defer server.Close()
	data, err := fetch(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "feed" || requests.Load() != 2 {
		t.Fatalf("data %q after %d attempts", data, requests.Load())
	}
}

func TestRetryDelayHonorsRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	if delay := retryDelay(1, "2", now); delay != 2*time.Second {
		t.Fatalf("numeric Retry-After delay = %s", delay)
	}
	date := now.Add(3 * time.Second).Format(http.TimeFormat)
	if delay := retryDelay(1, date, now); delay != 3*time.Second {
		t.Fatalf("date Retry-After delay = %s", delay)
	}
	if delay := retryDelay(1, "invalid", now); delay < 250*time.Millisecond || delay > 375*time.Millisecond {
		t.Fatalf("jittered backoff out of range: %s", delay)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "catalog", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
