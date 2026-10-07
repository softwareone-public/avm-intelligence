package main

import (
	"bytes"
	"context"

	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/github"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
	"github.com/softwareone-public/avm-intelligence/internal/worker"
	"gopkg.in/yaml.v3"
)

func TestFactoryParkingFailureLogsSeparateTriggerFromCheckpoint(t *testing.T) {
	var output bytes.Buffer
	failure := errors.Join(&ratelimit.DeferredError{Resource: "rest", RetryAt: time.Now().Add(time.Minute)}, &discovery.ParkingError{Cause: errors.Join(state.ErrHeadMoved, errors.New("PRIVATE_SENTINEL"))})
	logCommandCompletion(slog.New(slog.NewTextHandler(&output, nil)), "persist-discovery", time.Now(), failure)
	for _, expected := range []string{"deferral checkpoint could not be saved", "parking_failed=true", "parking_failure_reason=state_head_moved", "pacing_resource=rest"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %s: %s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "PRIVATE_SENTINEL") || strings.Count(output.String(), "level=ERROR") != 1 {
		t.Fatalf("unsafe parking diagnostic: %s", output.String())
	}
}

func TestFactoryDeferredRetryAtFindsFarthestDeadline(t *testing.T) {
	far := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Millisecond)
	near := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	for _, fixture := range []struct {
		name  string
		err   error
		check func(time.Time) bool
	}{
		{"joined tree", errors.Join(fmt.Errorf("wrapped: %w", &ratelimit.DeferredError{RetryAt: near}), &ratelimit.DeferredError{RetryAt: far}), func(got time.Time) bool { return got.Equal(far) }},
		{"single", &discovery.ParkingError{Cause: &ratelimit.DeferredError{RetryAt: far}}, func(got time.Time) bool { return got.Equal(far) }},
		{"none", errors.New("plain failure"), func(got time.Time) bool { return got.IsZero() }},
		{"nil", nil, func(got time.Time) bool { return got.IsZero() }},
	} {
		if got := deferredRetryAt(fixture.err); !fixture.check(got.UTC()) {
			t.Fatalf("%s extracted %v", fixture.name, got)
		}
	}
}

func TestFactoryFailureLogsDistinguishLocalBudgetAndGitHubRateLimit(t *testing.T) {
	for _, cause := range []error{github.ErrProjectionBudgetExhausted, github.ErrGitHubRateLimited} {
		var output bytes.Buffer
		budget := &github.ProjectionBudgetError{Cause: cause, Resource: github.ProjectionGraphQL, Remaining: 9939, Reserve: 100, SecondaryRemaining: 20, SecondaryReserve: 20}
		failure := &discovery.CycleError{ModuleID: "avm-res-network-networkinterface", Position: 9, Total: 10, Completed: 8, Cause: &github.ProjectionDeliveryError{Cause: errors.Join(budget, fmt.Errorf("private-api-body-sentinel")), RetryAt: time.Date(2026, 10, 6, 14, 11, 55, 0, time.UTC)}}
		logCommandCompletion(slog.New(slog.NewTextHandler(&output, nil)), "persist-discovery", time.Now(), failure)
		want := "local_projection_budget_exhausted"
		if cause == github.ErrGitHubRateLimited {
			want = "github_rate_limited"
		}
		for _, expected := range []string{want, "module_id=avm-res-network-networkinterface", "position=9", "total=10", "completed=8", "secondary_remaining=20", "retry_at="} {
			if !strings.Contains(output.String(), expected) {
				t.Errorf("missing safe failure detail %q: %s", expected, output.String())
			}
		}
		if strings.Contains(output.String(), "private-api-body-sentinel") || strings.Count(output.String(), "level=ERROR") != 1 || strings.Contains(output.String(), "verify command inputs") {
			t.Fatalf("unsafe or generic terminal budget failure: %s", output.String())
		}
	}
}

func TestFactoryFailureLogsStateAPIStageWithoutRawResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("X-GitHub-Request-Id", "ABCD:0123:4567:89EF")
		response.WriteHeader(http.StatusForbidden)
		_, _ = response.Write([]byte(`{"message":"PRIVATE_BODY_SENTINEL"}`))
	}))
	defer server.Close()
	backend := state.GitHubBackend{Owner: "owner", Repository: "repo", Branch: "factory-state", Token: "PRIVATE_TOKEN_SENTINEL", Endpoint: server.URL}
	_, _, err := backend.Read(context.Background())
	var output bytes.Buffer
	logCommandCompletion(slog.New(slog.NewTextHandler(&output, nil)), "persist-discovery", time.Now(), fmt.Errorf("prepare migration: %w", err))
	for _, expected := range []string{"operation=read_ref", "http_status=403", "reason=forbidden", "github_request_id=ABCD:0123:4567:89EF"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("safe API diagnostic is missing: %s", output.String())
		}
	}
	if strings.Contains(output.String(), "PRIVATE_") || strings.Contains(output.String(), server.URL) || strings.Contains(output.String(), "verify command inputs") || strings.Count(output.String(), "level=ERROR") != 1 {
		t.Fatalf("unsafe or generic terminal API failure: %s", output.String())
	}
}

func TestFactoryLoggingPreservesJSONStdout(t *testing.T) {
	t.Setenv(logLevelEnv, "info")
	t.Setenv(logFormatEnv, "text")
	for _, format := range []string{"text", "json"} {
		stdout, stderr, err := captureFactoryOutput(t, []string{"--list-dimensions", "--log-format=" + format})
		if err != nil {
			t.Fatal(err)
		}
		var dimensions []assessment.DimensionDefinition
		if err = json.Unmarshal(stdout, &dimensions); err != nil || len(dimensions) == 0 {
			t.Fatalf("logs corrupted JSON stdout: %s; %v", stdout, err)
		}
		if !bytes.Contains(stderr, []byte("factory command started")) || !bytes.Contains(stderr, []byte("factory command completed")) {
			t.Fatalf("progress missing on stderr: %s", stderr)
		}
	}
	stdout, stderr, err := captureFactoryOutput(t, []string{"--process-work", filepath.Join(t.TempDir(), "missing.json"), "--work-artifact-out", filepath.Join(t.TempDir(), "artifact.json"), "--log-format=json"})
	if err == nil || len(stderr) != 0 || bytes.Count(stdout, []byte("factory command failed")) != 1 || !bytes.Contains(stdout, []byte("required file or directory does not exist")) {
		t.Fatalf("operational failure routing: stdout=%s stderr=%s err=%v", stdout, stderr, err)
	}
	if strings.Contains(safeLogError(fmt.Errorf("parser diagnostic: SECRET_SOURCE_SENTINEL")), "SECRET_SOURCE_SENTINEL") {
		t.Fatal("untrusted error details escaped safe logging")
	}
	for _, name := range []string{"mint-state-token", "mint-projection-token", "dispatch-operation", "operator-command", "list-checks"} {
		flags := flag.NewFlagSet("routing", flag.ContinueOnError)
		flags.String(name, "enabled", "")
		var normal, machine bytes.Buffer
		if factoryLogOutput(flags, &normal, &machine) != &machine {
			t.Fatalf("%s logs would contaminate machine stdout", name)
		}
	}
}

func TestFactoryLoggingPlacesModuleBeforeMessage(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			logger, err := factoryLogger(flag.NewFlagSet("logging", flag.ContinueOnError), "info", format, func(string) string { return "" }, &output)
			if err != nil {
				t.Fatal(err)
			}
			logger.With("component", "state", "phase", "snapshot_read").With("module_id", "avm-res-test").Info("operation started", "position", 1)
			assertFactoryLogFieldOrder(t, output.String(), format, []string{"time", "level", "module_id", "msg", "component", "phase", "position"})
			output.Reset()
			logger.Info("module prepared", "phase", "resolve", "module_id", "avm-res-other", "total", 2)
			assertFactoryLogFieldOrder(t, output.String(), format, []string{"time", "level", "module_id", "msg", "phase", "total"})
			output.Reset()
			logger.Info("catalog validated", "modules", 232)
			assertFactoryLogFieldOrder(t, output.String(), format, []string{"time", "level", "msg", "modules"})
			if strings.Contains(output.String(), "module_id") {
				t.Fatal("module-free log gained a fabricated module identity")
			}
		})
	}
}

func assertFactoryLogFieldOrder(t *testing.T, output, format string, fields []string) {
	t.Helper()
	previous := -1
	for _, field := range fields {
		marker := field + "="
		if format == "json" {
			marker = `"` + field + `":`
		}
		position := strings.Index(output, marker)
		if position <= previous {
			t.Fatalf("field %s is missing or out of order: %s", field, output)
		}
		previous = position
	}
}

func TestFactoryLoggingModuleOrderPreservesStandardEncodingAndGroups(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var output, expected bytes.Buffer
			logger, err := factoryLogger(flag.NewFlagSet("logging", flag.ContinueOnError), "info", format, func(string) string { return "" }, &output)
			if err != nil {
				t.Fatal(err)
			}
			options := &slog.HandlerOptions{Level: slog.LevelInfo, ReplaceAttr: utcLogTime}
			var standard slog.Handler = slog.NewTextHandler(&expected, options)
			if format == "json" {
				standard = slog.NewJSONHandler(&expected, options)
			}
			record := slog.NewRecord(time.Date(2026, 10, 6, 12, 0, 0, 0, time.FixedZone("offset", 3600)), slog.LevelInfo, "message with \"quotes\"\nand a newline", 0)
			record.AddAttrs(slog.String("value", "space = \"quoted\""), slog.Group("", slog.String("other", "unchanged")))
			actual := logger.With("component", "state").WithGroup("details").With("phase", "commit").WithGroup("result").Handler()
			standard = standard.WithAttrs([]slog.Attr{slog.String("component", "state")}).WithGroup("details").WithAttrs([]slog.Attr{slog.String("phase", "commit")}).WithGroup("result")
			handleFactoryTestRecord(t, actual, record)
			handleFactoryTestRecord(t, standard, record)
			if output.String() != expected.String() {
				t.Fatalf("standard module-free encoding changed:\nactual: %s\nexpected: %s", &output, &expected)
			}
			output.Reset()
			moduleLogger := logger.With("module_id", "avm-res-test").WithGroup("details").With("phase", "resolve")
			moduleLogger.Info("quoted \"message\"\n", "version", "v1.0.0")
			assertFactoryLogFieldOrder(t, output.String(), format, []string{"time", "level", "module_id", "msg"})
			if format == "json" {
				assertGroupedModuleLogValues(t, output.Bytes())
			}
		})
	}
}

func assertGroupedModuleLogValues(t *testing.T, output []byte) {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(output, &fields); err != nil {
		t.Fatal(err)
	}
	group := fields["details"].(map[string]any)
	if fields["msg"] != "quoted \"message\"\n" || group["phase"] != "resolve" || group["version"] != "v1.0.0" {
		t.Fatal("message or grouped values changed")
	}
}

type countingBoundLogValue struct{ calls int }

func (value *countingBoundLogValue) String() string {
	value.calls++
	return "bound snapshot"
}

func (value *countingBoundLogValue) MarshalJSON() ([]byte, error) {
	value.calls++
	return []byte(`"bound snapshot"`), nil
}

func TestFactoryLoggingPreservesBoundValueSnapshots(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var output, expected bytes.Buffer
			logger, err := factoryLogger(flag.NewFlagSet("logging", flag.ContinueOnError), "info", format, func(string) string { return "" }, &output)
			if err != nil {
				t.Fatal(err)
			}
			options := &slog.HandlerOptions{ReplaceAttr: utcLogTime}
			var standard slog.Handler = slog.NewTextHandler(&expected, options)
			if format == "json" {
				standard = slog.NewJSONHandler(&expected, options)
			}
			mutable := map[string]string{"status": "before"}
			actualValue, standardValue := &countingBoundLogValue{}, &countingBoundLogValue{}
			actual := logger.With("state", mutable, "value", actualValue).Handler()
			standard = standard.WithAttrs([]slog.Attr{slog.Any("state", mutable), slog.Any("value", standardValue)})
			mutable["status"] = "after"
			record := slog.NewRecord(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), slog.LevelInfo, "snapshot", 0)
			for range 2 {
				handleFactoryTestRecord(t, actual, record)
				handleFactoryTestRecord(t, standard, record)
			}
			if output.String() != expected.String() || actualValue.calls != 1 || standardValue.calls != 1 {
				t.Fatalf("bind-time snapshot changed: actual=%s expected=%s calls=%d/%d", &output, &expected, actualValue.calls, standardValue.calls)
			}
		})
	}
}

func handleFactoryTestRecord(t *testing.T, handler slog.Handler, record slog.Record) {
	t.Helper()
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}

type inlineModuleLogValue struct{ calls *int }

func (value inlineModuleLogValue) LogValue() slog.Value {
	*value.calls++
	return slog.GroupValue(slog.String("module_id", "avm-res-test"), slog.String("phase", "resolve"))
}

type panickingLogTextMarshaler struct{}

func (*panickingLogTextMarshaler) MarshalText() ([]byte, error) { panic("marshal panic") }

type panickingLogTextAppender struct{}

func (*panickingLogTextAppender) AppendText(_ []byte) ([]byte, error) { panic("append panic") }

func TestFactoryLoggingPreservesEmptyAttributesAndMarshalerRecovery(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, attribute := range []slog.Attr{slog.Attr{}, slog.Any("value", &panickingLogTextMarshaler{}), slog.Any("value", (*panickingLogTextMarshaler)(nil)), slog.Any("value", &panickingLogTextAppender{}), slog.Any("value", (*panickingLogTextAppender)(nil)), slog.Any("value", []byte("quoted"))} {
			assertFactoryBoundAttributeMatchesStandard(t, format, attribute)
		}
	}
}

func assertFactoryBoundAttributeMatchesStandard(t *testing.T, format string, attribute slog.Attr) {
	t.Helper()
	var output, expected bytes.Buffer
	logger, err := factoryLogger(flag.NewFlagSet("logging", flag.ContinueOnError), "info", format, func(string) string { return "" }, &output)
	if err != nil {
		t.Fatal(err)
	}
	options := &slog.HandlerOptions{ReplaceAttr: utcLogTime}
	var standard slog.Handler = slog.NewTextHandler(&expected, options)
	if format == "json" {
		standard = slog.NewJSONHandler(&expected, options)
	}
	actual := logger.With(attribute).Handler()
	standard = standard.WithAttrs([]slog.Attr{attribute})
	record := slog.NewRecord(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), slog.LevelInfo, "standard compatibility", 0)
	handleFactoryTestRecord(t, actual, record)
	handleFactoryTestRecord(t, standard, record)
	if output.String() != expected.String() {
		t.Fatalf("bound encoding/recovery changed: actual=%s expected=%s", &output, &expected)
	}
}

func TestFactoryLoggingResolvesInlineModuleLogValuesOnce(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			logger, err := factoryLogger(flag.NewFlagSet("logging", flag.ContinueOnError), "info", format, func(string) string { return "" }, &output)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			logger.Info("inline identity", slog.Any("", inlineModuleLogValue{calls: &calls}))
			assertFactoryLogFieldOrder(t, output.String(), format, []string{"time", "level", "module_id", "msg", "phase"})
			if calls != 1 {
				t.Fatal("record LogValuer was resolved more than once")
			}
			output.Reset()
			logger.Info("grouped identity", slog.Any("details", inlineModuleLogValue{calls: &calls}))
			assertFactoryLogFieldOrder(t, output.String(), format, []string{"time", "level", "msg"})
			if format == "json" {
				var fields map[string]any
				if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
					t.Fatal(err)
				}
				if _, exists := fields["module_id"]; exists {
					t.Fatal("named metadata group was promoted into a module owner")
				}
			}
		})
	}
}

func TestFactoryLoggingModuleOrderKeepsConcurrentSiblingRecordsIsolated(t *testing.T) {
	var output bytes.Buffer
	logger, err := factoryLogger(flag.NewFlagSet("logging", flag.ContinueOnError), "info", "json", func(string) string { return "" }, &output)
	if err != nil {
		t.Fatal(err)
	}
	var writers sync.WaitGroup
	const recordCount = 32
	for position := range recordCount {
		writers.Add(1)
		go func() {
			defer writers.Done()
			logger.With("module_id", fmt.Sprintf("avm-res-test-%d", position)).Info("module processing", "position", position)
		}()
	}
	writers.Wait()
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != recordCount {
		t.Fatal("concurrent records were split or lost")
	}
	for _, line := range lines {
		assertFactoryLogFieldOrder(t, line, "json", []string{"time", "level", "module_id", "msg", "position"})
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatal(err)
		}
		if fields["module_id"] != fmt.Sprintf("avm-res-test-%d", int(fields["position"].(float64))) {
			t.Fatal("sibling logger leaked another module identity")
		}
	}
	output.Reset()
	logger.Info("catalog processing")
	if strings.Contains(output.String(), "module_id") {
		t.Fatal("module identity leaked into the parent logger")
	}
}

// captureFactoryOutput drains both pipes concurrently so large definition JSON
// cannot block the CLI; only sequential CLI tests may replace process streams.
func captureFactoryOutput(t *testing.T, arguments []string) ([]byte, []byte, error) {
	t.Helper()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previousOut, previousErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter
	defer func() { os.Stdout, os.Stderr = previousOut, previousErr }()
	stdoutDone, stderrDone := make(chan []byte, 1), make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(stdoutReader); stdoutDone <- data }()
	go func() { data, _ := io.ReadAll(stderrReader); stderrDone <- data }()
	runErr := run(arguments)
	if err = stdoutWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err = stderrWriter.Close(); err != nil {
		t.Fatal(err)
	}
	stdout, stderr := <-stdoutDone, <-stderrDone
	if err = stdoutReader.Close(); err != nil {
		t.Fatal(err)
	}
	if err = stderrReader.Close(); err != nil {
		t.Fatal(err)
	}
	return stdout, stderr, runErr
}

func TestFactoryLoggingFormatAndFiltering(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			flags := flag.NewFlagSet("logging", flag.ContinueOnError)
			logger, err := factoryLogger(flags, "info", format, func(string) string { return "" }, &output)
			if err != nil {
				t.Fatal(err)
			}
			logger.Debug("hidden debug")
			logger.Info("visible progress", "module_id", "avm-res-test")
			logger.Warn("visible warning")
			logger.Error("visible error")
			if strings.Contains(output.String(), "hidden debug") || !strings.Contains(output.String(), "visible progress") || !strings.Contains(output.String(), "WARN") || !strings.Contains(output.String(), "ERROR") {
				t.Fatalf("incorrect filtering: %s", &output)
			}
			assertFactoryLogRecord(t, output.Bytes(), format)
		})
	}
}

// assertFactoryLogRecord checks the format-specific timestamp and identity fields.
func assertFactoryLogRecord(t *testing.T, output []byte, format string) {
	t.Helper()
	if format == "text" {
		if !bytes.Contains(output, []byte("Z level=INFO")) {
			t.Fatalf("missing UTC time: %s", output)
		}
		return
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.Split(output, []byte("\n"))[0], &record); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(record["time"].(string), "Z") || record["level"] != "INFO" || record["module_id"] != "avm-res-test" {
		t.Fatalf("missing UTC fields: %v", record)
	}
}

func TestFactoryLoggingFlagsOverrideEnvironment(t *testing.T) {
	flags := flag.NewFlagSet("logging", flag.ContinueOnError)
	level := flags.String("log-level", "info", "")
	format := flags.String("log-format", "text", "")
	if err := flags.Parse([]string{"--log-level=debug", "--log-format=json"}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	logger, err := factoryLogger(flags, *level, *format, func(string) string { return "invalid" }, &output)
	if err != nil || !logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatalf("flags did not override environment: %v", err)
	}
}

func TestFactoryLoggingEnvironmentAndInvalidSettings(t *testing.T) {
	flags := flag.NewFlagSet("logging", flag.ContinueOnError)
	var output bytes.Buffer
	logger, err := factoryLogger(flags, "info", "text", func(name string) string {
		if name == logLevelEnv {
			return "error"
		}
		return "json"
	}, &output)
	if err != nil || logger.Enabled(context.Background(), slog.LevelWarn) {
		t.Fatalf("environment severity was not applied: %v", err)
	}
	for _, arguments := range [][]string{{"--list-dimensions", "--log-level=invalid"}, {"--mint-state-token", "--log-format=invalid"}} {
		if err = run(arguments); err == nil || !strings.Contains(err.Error(), "must be") {
			t.Fatalf("invalid logging reached command: args=%v err=%v", arguments, err)
		}
	}
}

func TestProjectionTokenDoesNotLoadProducerConfiguration(t *testing.T) {
	for _, name := range []string{"AVM_WRITER_APP_ID", "AVM_WRITER_PRIVATE_KEY", "APP_ID", "APP_PRIVATE_KEY"} {
		t.Setenv(name, "")
	}
	err := run([]string{"--mint-projection-token"})
	if err == nil || err.Error() != "GitHub App credentials are required" {
		t.Fatalf("token minting loaded unrelated producer configuration: %v", err)
	}
}

func TestNativeCommandsRejectRetiredFlags(t *testing.T) {
	for _, name := range []string{"show-toolchain", "show-pilot", "register-pilot", "analyze-pilot", "project-fleet", "project-accepted", "build-site", "plan-legacy-migration"} {
		t.Run(name, func(t *testing.T) {
			err := run([]string{"--" + name})
			if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Fatalf("retired command %s was accepted: %v", name, err)
			}
		})
	}
}

func TestNativeCommandsRejectMixedModesBeforeSideEffects(t *testing.T) {
	for _, arguments := range [][]string{
		{"--mint-projection-token", "--mint-state-token"},
		{"--list-dimensions", "--discover-catalog"},
		{"--build-indexes", "--prepare-worker-input"},
	} {
		if err := run(arguments); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("mixed native commands reached a side-effect path: args=%v err=%v", arguments, err)
		}
	}
}

func TestIndexesCLIRebuildsNativeTreeDeterministically(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	moduleID := "avm-res-native-test"
	metadataPath, err := identity.ModuleMetadataPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	metadataFile := filepath.Join(root, filepath.FromSlash(metadataPath))
	if err = os.MkdirAll(filepath.Dir(metadataFile), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"schema_version":2,"record_type":"module_metadata","module_id":"avm-res-native-test"}`)
	if err = os.WriteFile(metadataFile, metadata, 0o644); err != nil {
		t.Fatal(err)
	}
	catalogBytes, err := json.Marshal(index.CatalogIndex{
		SchemaVersion: 1, RecordType: "catalog_index", CatalogGeneration: 1,
		Modules: []index.CatalogModuleEntry{{ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-native-test", Lifecycle: "active"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dataRoot, "catalog.json"), catalogBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	datasetBytes, err := json.Marshal(index.DatasetManifest{
		SchemaVersion: 2, RecordType: "dataset_manifest", CatalogGeneration: 1,
		Generator: index.Producer{ID: "factory", Version: "2.0.0", ContractVersion: 1},
		CreatedAt: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dataRoot, "dataset.json"), datasetBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	var outputs [][]byte
	for _, name := range []string{"first", "second"} {
		output := filepath.Join(root, name)
		if err = run([]string{"--build-indexes", "--data-root", dataRoot, "--indexes-out", output}); err != nil {
			t.Fatal(err)
		}
		generated, readErr := os.ReadFile(filepath.Join(output, "data", "dataset.json"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		outputs = append(outputs, generated)
	}
	if !bytes.Equal(outputs[0], outputs[1]) {
		t.Fatal("native tree rebuild changed deterministic dataset bytes")
	}
	unchanged, err := os.ReadFile(metadataFile)
	if err != nil || !bytes.Equal(unchanged, metadata) {
		t.Fatalf("native source record was modified: %v", err)
	}
}

func TestIndexesCLIWritesFreshCandidateOutsideAcceptedData(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "accepted-data")
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	moduleID := "avm-res-network-vnet"
	metadataPath, err := identity.ModuleMetadataPath(moduleID)
	if err != nil {
		t.Fatal(err)
	}
	records := index.Records{
		CatalogGeneration: 1,
		CreatedAt:         time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
		Generator:         index.Producer{ID: "factory", Version: "2.0.0", ContractVersion: 1},
		RecordDigests:     map[string]string{metadataPath: strings.Repeat("a", 64)},
		Modules: []index.ModuleRecord{{
			ModuleID: moduleID, Repository: "Azure/terraform-azurerm-avm-res-network-vnet", Lifecycle: "active",
		}},
	}
	inputBytes, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(root, "accepted-records.json")
	if err = os.WriteFile(inputPath, inputBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(root, "work", "indexes")
	if err = run([]string{
		"--build-indexes", "--index-records", inputPath,
		"--data-root", dataRoot, "--indexes-out", outputRoot,
	}); err != nil {
		t.Fatal(err)
	}
	datasetPath, err := identity.CanonicalDataPath("dataset")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(outputRoot, filepath.FromSlash(datasetPath))); err != nil {
		t.Fatalf("generated candidate is missing dataset manifest: %v", err)
	}
	if entries, err := os.ReadDir(dataRoot); err != nil || len(entries) != 0 {
		t.Fatalf("index CLI changed accepted data root: entries=%v err=%v", entries, err)
	}
	if err = run([]string{
		"--build-indexes", "--index-records", inputPath,
		"--data-root", dataRoot, "--indexes-out", outputRoot,
	}); err == nil {
		t.Fatal("index CLI replaced an existing candidate output")
	}
}

func TestProcessWorkCLIProducesFactsArtifactWithoutStateCredentials(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := `terraform {
  required_providers {
    azurerm = { source = "hashicorp/azurerm" }
  }
}
resource "azurerm_resource_group" "main" {}`
	if err := os.WriteFile(filepath.Join(sourceRoot, "main.tf"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	request, err := queue.NewAcquireSourceRequest(queue.AcquisitionInput{
		ModuleID: "avm-res-worker-cli", SourceSHA: strings.Repeat("a", 40),
		DesiredGeneration: 1, ExtractorContract: "factory-hcl-v1",
		Capabilities: []string{terraform.CapabilityProviderRequirements},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	owner := "worker-cli-run"
	claimed, err := queue.ClaimModuleRequest([]queue.Request{request}, request.ID, owner, now, now.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	input := worker.Input{
		Request: claimed[0], Owner: owner, Fence: 1, SourceRoot: sourceRoot, ObservedAt: now,
		Producer: worker.Producer{ID: "avm-intelligence", Version: "2.0.0", ContractVersion: 1},
	}
	inputBytes, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(root, "work-input.json")
	if err = os.WriteFile(inputPath, inputBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(root, "module-evidence.json")
	if err = run([]string{"--process-work", inputPath, "--work-artifact-out", artifactPath}); err != nil {
		t.Fatal(err)
	}
	artifactBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	var artifact worker.Artifact
	if err = decodeOne(artifactBytes, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.RecordType != "module_evidence" || artifact.Phase != queue.PhaseAcquireSource || artifact.Facts == nil || artifact.ArtifactDigest == "" {
		t.Fatalf("worker CLI did not emit facts-only module evidence: %#v", artifact)
	}
}

type workerWorkflowStep struct {
	Name string            `yaml:"name"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

type workerWorkflowJob struct {
	Environment string               `yaml:"environment"`
	Permissions map[string]string    `yaml:"permissions"`
	Env         map[string]string    `yaml:"env"`
	Steps       []workerWorkflowStep `yaml:"steps"`
}

func TestV2WorkerWorkflowKeepsComputeReadOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "factory-worker.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]workerWorkflowJob `yaml:"jobs"`
	}
	if err = yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse v2 worker workflow: %v", err)
	}
	compute, exists := workflow.Jobs["compute"]
	if !exists {
		t.Fatal("compute job is missing")
	}
	assertComputeWorkerReadOnly(t, compute)
	assertTrustedWorkerJobsUsePublisherEnvironment(t, workflow.Jobs)
}

func TestApplyProposalCLIRequiresPinnedBaseAndMatchingMode(t *testing.T) {
	for _, args := range [][]string{
		{"--apply-proposal"},
		{"--proposal-root", "work/proposal"},
	} {
		if err := run(args); err == nil {
			t.Errorf("run(%q) accepted incomplete proposal options", args)
		}
	}
}

func TestV2ProposalValidationWorkflowUsesReadOnlyBaseCode(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "factory-proposal-validation.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse proposal validation workflow: %v", err)
	}
	steps := proposalValidationWorkflowSteps(t, document)
	assertProposalValidationWorkflowSteps(t, steps)
}

func proposalValidationWorkflowSteps(t *testing.T, document map[string]any) []any {
	t.Helper()
	on, ok := document["on"].(map[string]any)
	if !ok || on["pull_request_target"] == nil || on["merge_group"] == nil {
		t.Fatal("proposal validation workflow must cover pull_request_target and merge_group")
	}
	jobs, ok := document["jobs"].(map[string]any)
	if !ok {
		t.Fatal("proposal validation workflow has no jobs")
	}
	job, ok := jobs["validate"].(map[string]any)
	if !ok || job["environment"] != nil {
		t.Fatal("proposal validation job is missing or has a privileged environment")
	}
	permissions, ok := job["permissions"].(map[string]any)
	if !ok || len(permissions) != 1 || permissions["contents"] != "read" {
		t.Fatalf("proposal validation permissions are not read-only: %#v", job["permissions"])
	}
	steps, ok := job["steps"].([]any)
	if !ok {
		t.Fatal("proposal validation job has no steps")
	}
	return steps
}

func assertProposalValidationWorkflowSteps(t *testing.T, steps []any) {
	t.Helper()
	evidence := collectProposalWorkflowEvidence(steps)
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if ok {
			run, _ := step["run"].(string)
			assertProposalRunDoesNotCheckoutCandidate(t, run)
			assertProposalStepHasNoCredentials(t, step)
		}
	}
	if !evidence.complete() {
		t.Fatalf("proposal workflow trust boundary is incomplete: %#v", evidence)
	}
}

type proposalWorkflowEvidence struct {
	checkedOutBase, pinsEventBase, validatesProposalBase, usesMergeCandidate                   bool
	persistsCandidateSHA, candidateSHAFromFetch, validatesCandidateTree, validatedChangedPaths bool
	detectsRenames, rejectsNonRegularBlobs                                                     bool
	persistsFetchCredentials, skipsCodeOnlyChanges, exportsProposalApplicability               bool
	gatesProposalApplier                                                                       bool
}

func collectProposalWorkflowEvidence(steps []any) proposalWorkflowEvidence {
	var evidence proposalWorkflowEvidence
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			continue
		}
		stepEvidence := inspectProposalWorkflowStep(step)
		evidence.checkedOutBase = evidence.checkedOutBase || stepEvidence.checkedOutBase
		evidence.pinsEventBase = evidence.pinsEventBase || stepEvidence.pinsEventBase
		evidence.validatesProposalBase = evidence.validatesProposalBase || stepEvidence.validatesProposalBase
		evidence.usesMergeCandidate = evidence.usesMergeCandidate || stepEvidence.usesMergeCandidate
		evidence.persistsCandidateSHA = evidence.persistsCandidateSHA || stepEvidence.persistsCandidateSHA
		evidence.candidateSHAFromFetch = evidence.candidateSHAFromFetch || stepEvidence.candidateSHAFromFetch
		evidence.validatesCandidateTree = evidence.validatesCandidateTree || stepEvidence.validatesCandidateTree
		evidence.validatedChangedPaths = evidence.validatedChangedPaths || stepEvidence.validatedChangedPaths
		evidence.detectsRenames = evidence.detectsRenames || stepEvidence.detectsRenames
		evidence.rejectsNonRegularBlobs = evidence.rejectsNonRegularBlobs || stepEvidence.rejectsNonRegularBlobs
		evidence.persistsFetchCredentials = evidence.persistsFetchCredentials || stepEvidence.persistsFetchCredentials
		evidence.skipsCodeOnlyChanges = evidence.skipsCodeOnlyChanges || stepEvidence.skipsCodeOnlyChanges
		evidence.exportsProposalApplicability = evidence.exportsProposalApplicability || stepEvidence.exportsProposalApplicability
		evidence.gatesProposalApplier = evidence.gatesProposalApplier || stepEvidence.gatesProposalApplier
	}
	return evidence
}

func inspectProposalWorkflowStep(step map[string]any) proposalWorkflowEvidence {
	run, _ := step["run"].(string)
	stepID, _ := step["id"].(string)
	condition, _ := step["if"].(string)
	return proposalWorkflowEvidence{
		checkedOutBase:               checksOutTrustedMain(step),
		pinsEventBase:                strings.Contains(run, "jq -er '.merge_group.base_sha'") && strings.Contains(run, `git reset --hard "$BASE_SHA"`),
		validatesProposalBase:        strings.Contains(run, `git merge-base --is-ancestor "$proposal_base" "$BASE_SHA"`),
		usesMergeCandidate:           usesPullRequestMergeCandidate(step) || strings.Contains(run, `refs/pull/${PULL_REQUEST_NUMBER}/merge`),
		persistsCandidateSHA:         strings.Contains(run, `echo "CANDIDATE_SHA=$CANDIDATE_SHA" >> "$GITHUB_ENV"`),
		candidateSHAFromFetch:        strings.Contains(run, `CANDIDATE_SHA="$(git rev-parse 'FETCH_HEAD^{commit}')"`),
		validatesCandidateTree:       strings.Contains(run, "find \"$accepted_root\" -type f -print0") && strings.Contains(run, "cmp -s \"$accepted_file\" \"$comparison_file\""),
		validatedChangedPaths:        strings.Contains(run, "--proposal-changed-paths"),
		detectsRenames:               strings.Contains(run, "git diff --no-renames --name-only -z"),
		rejectsNonRegularBlobs:       strings.Contains(run, "git ls-tree -z") && strings.Contains(run, `"100644"`),
		persistsFetchCredentials:     trustedBaseCheckoutPersistsCredentials(step),
		skipsCodeOnlyChanges:         strings.Contains(run, "No typed proposal paths changed; validation is not applicable."),
		exportsProposalApplicability: stepID == "fetch-candidate" && strings.Contains(run, `echo "has_proposal_change=$has_proposal_change" >> "$GITHUB_OUTPUT"`),
		gatesProposalApplier:         strings.Contains(run, "--apply-proposal") && strings.Contains(condition, "steps.fetch-candidate.outputs.has_proposal_change == 'true'"),
	}
}

func (evidence proposalWorkflowEvidence) complete() bool {
	return evidence.checkedOutBase && evidence.pinsEventBase && evidence.validatesProposalBase && evidence.usesMergeCandidate && evidence.persistsCandidateSHA && evidence.candidateSHAFromFetch && evidence.validatesCandidateTree && evidence.validatedChangedPaths && evidence.detectsRenames && evidence.rejectsNonRegularBlobs && evidence.persistsFetchCredentials && evidence.skipsCodeOnlyChanges && evidence.exportsProposalApplicability && evidence.gatesProposalApplier
}

func trustedBaseCheckoutPersistsCredentials(step map[string]any) bool {
	uses, _ := step["uses"].(string)
	if !strings.HasPrefix(uses, "actions/checkout@") {
		return false
	}
	with, _ := step["with"].(map[string]any)
	return with["ref"] == "main" && with["persist-credentials"] == true
}

func usesPullRequestMergeCandidate(step map[string]any) bool {
	env, _ := step["env"].(map[string]any)
	candidateSHA, _ := env["CANDIDATE_SHA"].(string)
	return strings.Contains(candidateSHA, "pull_request.merge_commit_sha")
}

func checksOutTrustedMain(step map[string]any) bool {
	uses, _ := step["uses"].(string)
	if !strings.HasPrefix(uses, "actions/checkout@") {
		return false
	}
	with, _ := step["with"].(map[string]any)
	ref, _ := with["ref"].(string)
	return ref == "main"
}

func assertProposalRunDoesNotCheckoutCandidate(t *testing.T, run string) {
	t.Helper()
	if strings.Contains(run, "git checkout") {
		t.Fatal("proposal validation must not check out candidate code")
	}
}

func assertProposalStepHasNoCredentials(t *testing.T, step map[string]any) {
	t.Helper()
	env, _ := step["env"].(map[string]any)
	for name, value := range env {
		if strings.Contains(strings.ToLower(name), "token") || strings.Contains(strings.ToLower(name), "private_key") || strings.Contains(fmt.Sprint(value), "secrets.") {
			t.Fatalf("proposal validation step receives credential-like environment %s", name)
		}
	}
}

func assertComputeWorkerReadOnly(t *testing.T, compute workerWorkflowJob) {
	t.Helper()
	if compute.Environment != "" || compute.Permissions["contents"] != "read" {
		t.Fatalf("compute job is not isolated with read-only contents: %#v", compute)
	}
	if _, hasWriterID := compute.Env["AVM_WRITER_APP_ID"]; hasWriterID {
		t.Fatal("compute job received the state writer App ID")
	}
	if _, hasWriterKey := compute.Env["AVM_WRITER_PRIVATE_KEY"]; hasWriterKey {
		t.Fatal("compute job received the state writer private key")
	}
	for permission, access := range compute.Permissions {
		if access != "read" {
			t.Fatalf("compute job has non-read permission %s=%s", permission, access)
		}
	}
	assertComputeStepsHaveNoCredentials(t, compute.Steps)
}

func assertComputeStepsHaveNoCredentials(t *testing.T, steps []workerWorkflowStep) {
	t.Helper()
	for _, step := range steps {
		for name, value := range step.Env {
			if strings.Contains(strings.ToLower(name), "token") || strings.Contains(strings.ToLower(name), "private_key") || strings.Contains(value, "secrets.") {
				t.Fatalf("compute step %q receives credential-like environment %s", step.Name, name)
			}
		}
		if strings.Contains(step.Run, "--mint-state-token") || strings.Contains(step.Run, "AVM_WRITER_PRIVATE_KEY") {
			t.Fatalf("compute step %q can mint a state credential", step.Name)
		}
	}
}

func assertTrustedWorkerJobsUsePublisherEnvironment(t *testing.T, jobs map[string]workerWorkflowJob) {
	t.Helper()
	if jobs["prepare"].Environment != "data-publisher" || jobs["validate-and-seal"].Environment != "data-publisher" {
		t.Fatal("trusted preparation and sealing jobs must use data-publisher")
	}
}

func TestAssessmentCLIListsCompiledDimensionsAndChecks(t *testing.T) {
	var dimensionsJSON bytes.Buffer
	if err := listAssessmentDefinitions(&dimensionsJSON, ""); err != nil {
		t.Fatal(err)
	}
	var dimensions []assessment.DimensionDefinition
	if err := json.Unmarshal(dimensionsJSON.Bytes(), &dimensions); err != nil {
		t.Fatal(err)
	}
	if len(dimensions) != 7 {
		t.Fatalf("listed dimensions = %d, want 7", len(dimensions))
	}
	var checksJSON bytes.Buffer
	if err := listAssessmentDefinitions(&checksJSON, assessment.DimensionProviderRequirements); err != nil {
		t.Fatal(err)
	}
	var checks []assessment.CheckDefinition
	if err := json.Unmarshal(checksJSON.Bytes(), &checks); err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].CheckID != assessment.CheckDeclaredProviderSources {
		t.Fatalf("listed provider-requirements checks = %#v", checks)
	}
}

func TestAssessmentCLISelectsDimensionsFromFactsFile(t *testing.T) {
	root := t.TempDir()
	factsPath := writeAssessmentFactsFile(t, root)
	resultsPath := filepath.Join(root, "results.json")
	selection := assessment.DimensionProviderRequirements + "," + assessment.DimensionProviderCatalog
	if err := run([]string{"--assess-facts", factsPath, "--dimension-ids", selection, "--assessment-out", resultsPath}); err != nil {
		t.Fatal(err)
	}
	results := readAssessmentResults(t, resultsPath)
	if len(results) != 2 {
		t.Fatalf("selected dimensions produced %d results, want 2", len(results))
	}
	assertAssessmentResultStatuses(t, results, assessment.StatusUnchecked)

	snapshotPath := filepath.Join(root, "provider-snapshots.json")
	observedAt := "2026-10-02T23:00:00Z"
	snapshots := []assessment.ProviderSnapshot{{
		SchemaVersion: 2, RecordType: "provider_snapshot",
		SnapshotID:      "snapshot-" + strings.Repeat("b", 64),
		ProviderAddress: "registry.terraform.io/hashicorp/azurerm", ProviderVersion: "4.0.0",
		Coverage:     assessment.SnapshotCoverage{Capabilities: map[string]string{"resource": "complete"}, Complete: true, ObservedAt: observedAt},
		Entries:      []assessment.ProviderEntry{{Kind: "resource", Name: "azurerm_resource_group"}},
		Producer:     assessment.SnapshotProducer{ID: "fixture", Version: "1.0.0", ContractVersion: 1},
		SourceDigest: strings.Repeat("c", 64), ObservedAt: observedAt,
	}}
	snapshotData, err := json.Marshal(snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(snapshotPath, snapshotData, 0o600); err != nil {
		t.Fatal(err)
	}
	withSnapshotPath := filepath.Join(root, "results-with-snapshot.json")
	if err = run([]string{
		"--assess-facts", factsPath, "--dimension-ids", selection,
		"--provider-snapshots", snapshotPath, "--assessment-out", withSnapshotPath,
	}); err != nil {
		t.Fatal(err)
	}
	assertAssessmentResultStatuses(t, readAssessmentResults(t, withSnapshotPath), assessment.StatusPass)
}

func TestAssessmentCLIRequiresReviewedClassificationForModuleChecks(t *testing.T) {
	root := t.TempDir()
	factsPath := writeAssessmentFactsFile(t, root)
	dimension := assessment.DimensionInterfaceDocumentation
	unreviewedPath := filepath.Join(root, "unreviewed.json")
	if err := run([]string{"--assess-facts", factsPath, "--dimension-ids", dimension, "--assessment-out", unreviewedPath}); err != nil {
		t.Fatal(err)
	}
	unreviewed := readAssessmentResults(t, unreviewedPath)
	if len(unreviewed) != 1 {
		t.Fatalf("unreviewed dimension results = %#v", unreviewed)
	}
	for _, check := range unreviewed[0].Checks {
		if check.Status != assessment.StatusUnchecked || check.Execution != assessment.ExecutionMissingInput {
			t.Fatalf("unreviewed module check = %#v", check)
		}
	}

	classificationPath := filepath.Join(root, "classifications.json")
	if err := os.WriteFile(classificationPath, []byte(`{".":"module"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reviewedPath := filepath.Join(root, "reviewed.json")
	if err := run([]string{
		"--assess-facts", factsPath, "--dimension-ids", dimension,
		"--classifications", classificationPath, "--assessment-out", reviewedPath,
	}); err != nil {
		t.Fatal(err)
	}
	reviewed := readAssessmentResults(t, reviewedPath)
	if len(reviewed) != 1 || len(reviewed[0].Checks) != 2 {
		t.Fatalf("reviewed dimension results = %#v", reviewed)
	}
	for _, check := range reviewed[0].Checks {
		if check.Status != assessment.StatusNotApplicable || check.Execution != assessment.ExecutionCompleted {
			t.Fatalf("reviewed module check = %#v", check)
		}
	}
}

func writeAssessmentFactsFile(t *testing.T, root string) string {
	t.Helper()
	configuration := `terraform {
  required_providers {
    azurerm = {
      source = "hashicorp/azurerm"
    }
  }
}

resource "azurerm_resource_group" "main" {}`
	if err := os.WriteFile(filepath.Join(root, "main.tf"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := terraform.ExtractFacts(terraform.Input{
		RootDir: root, ModuleID: "avm-res-cli-assessment", SourceSHA: strings.Repeat("a", 40),
		ObservedAt: time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC),
		Units:      []terraform.UnitInput{{Path: ".", Role: terraform.RoleComponent, Files: []string{"main.tf"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	factsPath := filepath.Join(root, "facts.json")
	factsData, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(factsPath, factsData, 0o600); err != nil {
		t.Fatal(err)
	}
	return factsPath
}

func readAssessmentResults(t *testing.T, path string) []assessment.DimensionResult {
	t.Helper()
	resultsData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var results []assessment.DimensionResult
	if err = json.Unmarshal(resultsData, &results); err != nil {
		t.Fatal(err)
	}
	return results
}

func assertAssessmentResultStatuses(t *testing.T, results []assessment.DimensionResult, providerCatalogStatus string) {
	t.Helper()
	statuses := map[string]string{}
	registry, err := assessment.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		for _, definition := range registry.ListDimensions() {
			if definition.DefinitionID == result.DefinitionID {
				statuses[definition.DimensionID] = result.Checks[0].Status
			}
		}
	}
	if statuses[assessment.DimensionProviderRequirements] != assessment.StatusPass || statuses[assessment.DimensionProviderCatalog] != providerCatalogStatus {
		t.Fatalf("selected dimension statuses = %#v", statuses)
	}
}

func TestPersistDiscoveryRequiresExplicitStateBranch(t *testing.T) {
	err := run([]string{
		"--persist-discovery", "missing-candidate.json",
		"--extractor-contract", "factory-hcl-v1",
		"--extractor-capabilities", "native-hcl-v1",
	})
	if err == nil || !strings.Contains(err.Error(), "--state-branch is required") {
		t.Fatalf("missing state branch error = %v", err)
	}
}

func TestPersistDiscoveryRejectsMainBeforeReadingCredentialsOrCandidate(t *testing.T) {
	err := persistAVMCatalog("missing-candidate.json", "main", "factory-hcl-v1", "native-hcl-v1", "", "")
	if err == nil || !strings.Contains(err.Error(), `must be "factory-state"`) {
		t.Fatalf("main branch error = %v", err)
	}
}

func TestDispatchOperationRequiresFactoryStateBranch(t *testing.T) {
	err := run([]string{"--dispatch-operation", "claim", "--state-branch", "main"})
	if err == nil || !strings.Contains(err.Error(), `must be "factory-state"`) {
		t.Fatalf("unsafe dispatcher branch error = %v", err)
	}
}

func TestDispatchClaimRequiresRequestID(t *testing.T) {
	_, err := claimDispatchRequest(context.Background(), nil, dispatchOperationOptions{
		dispatcherID: "dispatcher-test", leaseSeconds: 1800,
	}, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "--request-id") {
		t.Fatalf("missing targeted request error = %v", err)
	}
	_, err = claimDispatchRequest(context.Background(), nil, dispatchOperationOptions{
		dispatcherID: "dispatcher-test", requestID: "request-1", moduleID: "avm-res-example", leaseSeconds: 1800,
	}, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("combined target error = %v", err)
	}
	_, err = claimNextDispatchRequest(context.Background(), nil, dispatchOperationOptions{
		dispatcherID: "dispatcher-test", moduleID: "avm-res-example", leaseSeconds: 1800,
	}, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "without a request or module target") {
		t.Fatalf("targeted claim-next error = %v", err)
	}
}

func TestDispatchOperationRequiresDedicatedStateToken(t *testing.T) {
	t.Setenv("FACTORY_STATE_TOKEN", "")
	err := run([]string{"--dispatch-operation", "claim", "--state-branch", "factory-state"})
	if err == nil || !strings.Contains(err.Error(), "FACTORY_STATE_TOKEN is required") {
		t.Fatalf("missing dispatcher state token error = %v", err)
	}
}

func TestMintStateTokenRequiresAppCredentials(t *testing.T) {
	t.Setenv("AVM_WRITER_APP_ID", "")
	t.Setenv("AVM_WRITER_PRIVATE_KEY", "")
	t.Setenv("APP_ID", "")
	t.Setenv("APP_PRIVATE_KEY", "")
	err := run([]string{"--mint-state-token"})
	if err == nil || !strings.Contains(err.Error(), "GitHub App credentials are required") {
		t.Fatalf("missing App credentials error = %v", err)
	}
}
