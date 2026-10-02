package queue

import (
	"strings"
	"testing"
	"time"
)

func TestAttemptValidationRejectsMalformedLifecycle(t *testing.T) {
	now := time.Date(2026, 10, 2, 20, 0, 0, 0, time.UTC)
	request := dispatchAcquisition(t, "avm-res-attempt-validation", 'a', 1, now)
	attempt := Attempt{
		SchemaVersion: 1, ID: "attempt-" + strings.Repeat("b", 64),
		RequestID: request.ID, ModuleID: request.ModuleID, InputFingerprint: request.Fingerprint,
		SourceSHA: request.SourceSHA, Phase: PhaseAcquireSource, DesiredGeneration: 1,
		Number: 1, Fence: 1, Dispatcher: "dispatcher", Status: AttemptClaimed,
		ClaimedAt: now, LeaseExpiresAt: now.Add(time.Minute),
	}
	if err := attempt.Validate(); err != nil {
		t.Fatalf("valid claimed attempt rejected: %v", err)
	}
	attempt.RunID = "unbound-run"
	if err := attempt.Validate(); err == nil {
		t.Fatal("pre-dispatch attempt with a run binding was accepted")
	}
}
