package projection

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

func TestDesiredIntentCanonicalizesAndBindsOwnedContent(t *testing.T) {
	first, err := NewDesiredIntent("avm-res-example", 3, "transition_3", "analyzing", []OwnedField{
		{Name: "summary", Value: "Module summary"},
		{Name: "status", Value: "Analyzing"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDesiredIntent("avm-res-example", 3, "transition_3", "analyzing", []OwnedField{
		{Name: "status", Value: "Analyzing"},
		{Name: "summary", Value: "Module summary"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatal("field order changed desired intent fingerprint")
	}
	if _, err = NewDesiredIntent("avm-res-example", 4, "transition-4", "needs-review", nil, nil); err != nil {
		t.Fatalf("hyphenated transition and status were rejected: %v", err)
	}
	if err = first.ValidatePath("state/projection/intents/avm-res-example.json"); err != nil {
		t.Fatalf("valid owner path rejected: %v", err)
	}
	if err = first.ValidatePath("state/projection/intents/avm-res-other.json"); err == nil {
		t.Fatal("mismatched owner path was accepted")
	}
	first.OwnedFields[0].Value = "human note"
	if err = first.Validate(); err == nil {
		t.Fatal("changed factory-owned content retained its fingerprint")
	}
}

func TestDesiredIntentRejectsDuplicateAndInvalidEffects(t *testing.T) {
	if _, err := NewDesiredIntent("avm-res-example", 1, "transition_1", "queued", []OwnedField{
		{Name: "status", Value: "Queued"}, {Name: "status", Value: "Backlog"},
	}, nil); err == nil {
		t.Fatal("duplicate owned field was accepted")
	}
	if _, err := NewDesiredIntent("avm-res-example", 1, "transition_1", "queued", nil, []EvidenceEffect{{
		ID: "not-an-effect-id", Kind: "comment", Fingerprint: "not-a-fingerprint",
	}}); err == nil {
		t.Fatal("invalid evidence effect was accepted")
	}
}

func TestHandoffIdentityExcludesWorkflowRunProvenance(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	intent := fixtureIntent(t)
	projectionIdentity, err := intent.Identity()
	if err != nil {
		t.Fatal(err)
	}
	base := Handoff{
		ModuleID: intent.ModuleID, Generation: intent.Generation + 1,
		TransitionID: "assessment:release-context-transition", InputFingerprint: testRecordID("input-fingerprint"),
		OperationID: "discovery:avm-res-example:g3", ConsumerStage: "acquire_source",
		Producer:   WorkflowProvenance{WorkflowID: "discovery.yml", Commit: "0123456789abcdef0123456789abcdef01234567", RunID: "run-1", RunAttempt: 1},
		Projection: projectionIdentity,
	}
	first, err := NewHandoff(base, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.InputFingerprint != testRecordID("input-fingerprint") {
		t.Fatal("handoff did not retain its input-fingerprint identity")
	}
	if first.Projection != projectionIdentity {
		t.Fatal("handoff did not retain its separate projection identity")
	}
	base.Producer.RunID = "run-2"
	base.Producer.RunAttempt = 2
	second, err := NewHandoff(base, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatal("workflow run provenance changed stable handoff identity")
	}
	if first.BarrierDeadline.Sub(first.CreatedAt) != BarrierTimeout {
		t.Fatalf("barrier deadline delta = %s, want %s", first.BarrierDeadline.Sub(first.CreatedAt), BarrierTimeout)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Handoff
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err = decoded.Validate(); err != nil {
		t.Fatalf("round-tripped handoff rejected: %v", err)
	}
	decoded.Producer.Commit = "invalid"
	if err = decoded.Validate(); err == nil {
		t.Fatal("invalid producer commit was accepted")
	}
}

func TestProjectionAcknowledgementAndBarrierReleaseRequireExactTuple(t *testing.T) {
	intent := fixtureIntent(t)
	required, err := intent.Identity()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 12, 20, 0, 0, time.UTC)
	ack := Acknowledgement{SchemaVersion: SchemaVersion, Projection: required, Status: AcknowledgementDone, AcknowledgedAt: now}
	if err = ack.Validate(required, now); err != nil {
		t.Fatalf("exact acknowledgement rejected: %v", err)
	}
	ack.Projection.Generation++
	if err = ack.Validate(required, now); err == nil {
		t.Fatal("acknowledgement for another generation was accepted")
	}
	deadline := now.Add(-time.Minute)
	release := BarrierRelease{
		SchemaVersion: SchemaVersion, Projection: required, Status: BarrierReleased,
		Deadline: deadline, ReleasedAt: now,
	}
	if err = release.Validate(required, deadline, now); err != nil {
		t.Fatalf("on-time barrier release rejected: %v", err)
	}
	release.ReleasedAt = deadline.Add(-time.Nanosecond)
	if err = release.Validate(required, deadline, now); err == nil {
		t.Fatal("premature barrier release was accepted")
	}
	if err = release.Validate(required, deadline.Add(-time.Second), now); err == nil {
		t.Fatal("release with a different persisted deadline was accepted")
	}
}

func TestProjectionIdentityRequiresExactIntentID(t *testing.T) {
	intent := fixtureIntent(t)
	projectionIdentity, err := intent.Identity()
	if err != nil {
		t.Fatal(err)
	}
	projectionIdentity.IntentID = testRecordID("projection-intent")
	if err = projectionIdentity.Validate(intent); err == nil {
		t.Fatal("projection identity with a different intent ID was accepted")
	}
}

func fixtureIntent(t *testing.T) DesiredIntent {
	t.Helper()
	intent, err := NewDesiredIntent("avm-res-example", 2, "transition_2", "queued", []OwnedField{{Name: "status", Value: "Queued"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func testRecordID(kind string) string {
	value, err := identity.ID(kind, 1, struct {
		Value string `json:"value"`
	}{Value: "fixture"})
	if err != nil {
		panic(err)
	}
	return value
}
