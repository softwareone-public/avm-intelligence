package progress

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordHandler struct {
	records chan slog.Record
	attrs   []slog.Attr
}

func (handler recordHandler) Enabled(context.Context, slog.Level) bool { return true }
func (handler recordHandler) WithGroup(string) slog.Handler            { return handler }
func (handler recordHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handler.attrs = append(append([]slog.Attr(nil), handler.attrs...), attrs...)
	return handler
}
func (handler recordHandler) Handle(_ context.Context, record slog.Record) error {
	record = record.Clone()
	record.AddAttrs(handler.attrs...)
	handler.records <- record
	return nil
}

func TestProgressWaitingCompletionAndNestedIdentity(t *testing.T) {
	records := make(chan slog.Record, 20)
	previous := slog.Default()
	slog.SetDefault(slog.New(recordHandler{records: records}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ticks := make(chan time.Time)
	ctx, finish := start(context.Background(), "discovery", "resolve", ticks, slog.String("module_id", "avm-res-example"))
	defer finish(false)
	assertRecord(t, records, "operation started", slog.LevelInfo)
	ticks <- time.Now()
	assertRecord(t, records, "operation waiting", slog.LevelInfo)
	_, nestedFinish := start(ctx, "state", "commit", ticks)
	record := assertRecord(t, records, "operation started", slog.LevelInfo)
	attrs := map[string]string{}
	record.Attrs(func(attribute slog.Attr) bool {
		if _, duplicate := attrs[attribute.Key]; duplicate {
			t.Errorf("duplicate identity key %s", attribute.Key)
		}
		attrs[attribute.Key] = attribute.Value.String()
		return true
	})
	if attrs["module_id"] != "avm-res-example" || attrs["phase"] != "commit" || attrs["component"] != "state" {
		t.Fatalf("nested identity=%v", attrs)
	}
	nestedFinish(true)
	assertRecord(t, records, "operation completed", slog.LevelInfo)
	finish(true)
	finish(false)
	assertRecord(t, records, "operation completed", slog.LevelInfo)
	if len(records) != 0 {
		t.Fatal("duplicate completion or late heartbeat")
	}
}

func TestProgressCancellationAndConcurrentFinish(t *testing.T) {
	records := make(chan slog.Record, 20)
	previous := slog.Default()
	slog.SetDefault(slog.New(recordHandler{records: records}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	_, finish := start(ctx, "github", "issue_update", ticks)
	assertRecord(t, records, "operation started", slog.LevelInfo)
	cancel()
	var workers sync.WaitGroup
	for count := 0; count < 3; count++ {
		workers.Go(func() { finish(false) })
	}
	workers.Wait()
	assertRecord(t, records, "operation incomplete", slog.LevelWarn)
	if len(records) != 0 {
		t.Fatal("cancelled reporter continued or completed twice")
	}
}

func TestProgressFiltering(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	_, finish := Start(context.Background(), "github", "project_fields")
	finish(false)
	if strings.Contains(output.String(), "operation started") || !strings.Contains(output.String(), "operation incomplete") {
		t.Fatalf("incorrect severity filtering: %s", output.String())
	}
}

func assertRecord(t *testing.T, records <-chan slog.Record, message string, level slog.Level) slog.Record {
	t.Helper()
	select {
	case record := <-records:
		if record.Message != message || record.Level != level {
			t.Fatalf("record=%s %s, want %s %s", record.Level, record.Message, level, message)
		}
		return record
	case <-time.After(time.Second):
		t.Fatal("missing progress event")
		return slog.Record{}
	}
}
