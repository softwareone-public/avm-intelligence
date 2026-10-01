package queue

import (
	"testing"
	"time"
)

func TestRequestRoundTripAndAttemptGuard(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	request, err := New("avm-res-demo", repeat('a'), repeat('b'), repeat('c'), time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err = Save(dir, request); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir, request.ModuleID)
	if err != nil || loaded.Status != StatusQueued || loaded.Attempt != 1 {
		t.Fatalf("loaded = %#v, err = %v", loaded, err)
	}
	request.Attempt = 0
	if err = Save(dir, request); err == nil {
		t.Fatal("older attempt replaced the request")
	}
}

func repeat(value byte) string {
	raw := make([]byte, 40)
	for i := range raw {
		raw[i] = value
	}
	if value == 'c' {
		raw = make([]byte, 64)
		for i := range raw {
			raw[i] = value
		}
	}
	return string(raw)
}
