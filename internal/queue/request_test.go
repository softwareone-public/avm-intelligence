package queue

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRequestFingerprintIncludesAllAnalysisInputs(t *testing.T) {
	t.Parallel()
	base := testInput()
	want, err := base.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	variants := []func(*AnalysisInput){
		func(input *AnalysisInput) { input.SourceSHA = repeat('d') },
		func(input *AnalysisInput) { input.CLICommit = repeat('e') },
		func(input *AnalysisInput) { input.BinaryDigest = repeatDigest('f') },
		func(input *AnalysisInput) { input.PolicyDigest = repeatDigest('1') },
		func(input *AnalysisInput) { input.ReportSchemaVersion++ },
		func(input *AnalysisInput) { input.AdapterVersion++ },
		func(input *AnalysisInput) { input.SemanticConfigDigest = repeatDigest('2') },
	}
	for index, change := range variants {
		input := base
		change(&input)
		got, err := input.Fingerprint()
		if err != nil {
			t.Fatal(err)
		}
		if got == want {
			t.Errorf("input change %d did not change analysis fingerprint", index)
		}
	}
	if _, err = New(base, time.Time{}); err == nil {
		t.Fatal("zero creation time was accepted")
	}
	if _, err = base.Fingerprint(); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterCoalescesAndQueuesOneNewerInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first, changed, err := Register(dir, testInput(), start)
	if err != nil || !changed || first.Status != StatusQueued {
		t.Fatalf("first registration=%#v changed=%t err=%v", first, changed, err)
	}
	second, changed, err := Register(dir, testInput(), start.Add(time.Second))
	if err != nil || changed || second.UpdatedAt != first.UpdatedAt {
		t.Fatalf("unchanged registration=%#v changed=%t err=%v", second, changed, err)
	}
	active, err := Start(dir, first.ModuleID, first.Fingerprint, "run-1", start.Add(2*time.Second))
	if err != nil || active.Status != StatusRunning {
		t.Fatalf("start=%#v err=%v", active, err)
	}
	newerInput := testInput()
	newerInput.SourceSHA = repeat('d')
	pending, changed, err := Register(dir, newerInput, start.Add(3*time.Second))
	if err != nil || !changed || pending.Fingerprint != first.Fingerprint || pending.Pending == nil || pending.Pending.SourceSHA != newerInput.SourceSHA {
		t.Fatalf("pending registration=%#v changed=%t err=%v", pending, changed, err)
	}
	newestInput := newerInput
	newestInput.SourceSHA = repeat('e')
	pending, changed, err = Register(dir, newestInput, start.Add(4*time.Second))
	if err != nil || !changed || pending.Pending == nil || pending.Pending.SourceSHA != newestInput.SourceSHA {
		t.Fatalf("coalesced pending registration=%#v changed=%t err=%v", pending, changed, err)
	}
	finished, err := Finish(dir, first.ModuleID, first.Fingerprint, "run-1", true, start.Add(5*time.Second))
	if err != nil || finished.Status != StatusQueued || finished.SourceSHA != newestInput.SourceSHA || finished.LastAcceptedFingerprint != "" || finished.Pending != nil {
		t.Fatalf("finish/promote=%#v err=%v", finished, err)
	}
}

func TestNewerRunRevertingToAcceptedInputDoesNotAcceptStaleResult(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	acceptedInput := testInput()
	request, _, err := Register(dir, acceptedInput, start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Start(dir, request.ModuleID, request.Fingerprint, "run-accepted", start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	accepted, err := Finish(dir, request.ModuleID, request.Fingerprint, "run-accepted", true, start.Add(2*time.Second))
	if err != nil || accepted.Status != StatusAccepted || accepted.LastAcceptedSourceSHA != acceptedInput.SourceSHA {
		t.Fatalf("accepted baseline=%#v err=%v", accepted, err)
	}
	newInput := acceptedInput
	newInput.SourceSHA = repeat('e')
	newRequest, changed, err := Register(dir, newInput, start.Add(3*time.Second))
	if err != nil || !changed || newRequest.Status != StatusQueued {
		t.Fatalf("new input=%#v changed=%t err=%v", newRequest, changed, err)
	}
	if _, err = Start(dir, newRequest.ModuleID, newRequest.Fingerprint, "run-new", start.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	backToAccepted, changed, err := Register(dir, acceptedInput, start.Add(5*time.Second))
	if err != nil || !changed || backToAccepted.Pending == nil || backToAccepted.Pending.Fingerprint != accepted.LastAcceptedFingerprint {
		t.Fatalf("reverted desired input=%#v changed=%t err=%v", backToAccepted, changed, err)
	}
	restored, err := Finish(dir, newRequest.ModuleID, newRequest.Fingerprint, "run-new", true, start.Add(6*time.Second))
	if err != nil || restored.Status != StatusAccepted || restored.Fingerprint != accepted.LastAcceptedFingerprint || restored.SourceSHA != acceptedInput.SourceSHA || restored.LastAcceptedFingerprint != accepted.LastAcceptedFingerprint {
		t.Fatalf("stale run replaced accepted input: %#v err=%v", restored, err)
	}
}

func TestFailedRequestRetriesAndAcceptedFingerprintCoalesces(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	request, _, err := Register(dir, testInput(), start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Start(dir, request.ModuleID, request.Fingerprint, "run-old", start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = Finish(dir, request.ModuleID, request.Fingerprint, "wrong-run", true, start.Add(2*time.Second)); err == nil {
		t.Fatal("non-owning run completed active request")
	}
	failed, err := Finish(dir, request.ModuleID, request.Fingerprint, "run-old", false, start.Add(3*time.Second))
	if err != nil || failed.Status != StatusFailed {
		t.Fatalf("failed request=%#v err=%v", failed, err)
	}
	retry, changed, err := Register(dir, testInput(), start.Add(4*time.Second))
	if err != nil || !changed || retry.Status != StatusQueued || retry.Attempt != 2 {
		t.Fatalf("retry=%#v changed=%t err=%v", retry, changed, err)
	}
	if _, err = Start(dir, retry.ModuleID, retry.Fingerprint, "run-retry", start.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	accepted, err := Finish(dir, retry.ModuleID, retry.Fingerprint, "run-retry", true, start.Add(6*time.Second))
	if err != nil || accepted.Status != StatusAccepted || accepted.LastAcceptedFingerprint != retry.Fingerprint {
		t.Fatalf("accepted=%#v err=%v", accepted, err)
	}
	unchanged, changed, err := Register(dir, testInput(), start.Add(7*time.Second))
	if err != nil || changed || unchanged.Status != StatusAccepted {
		t.Fatalf("accepted no-op=%#v changed=%t err=%v", unchanged, changed, err)
	}
}

func TestReconcileWaitsForTerminalRunAndPromotesPending(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	request, _, err := Register(dir, testInput(), start)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Start(dir, request.ModuleID, request.Fingerprint, "run-1", start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	pendingInput := testInput()
	pendingInput.SourceSHA = repeat('e')
	if _, _, err = Register(dir, pendingInput, start.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	active, changed, err := Reconcile(dir, request.ModuleID, RunActive, start.Add(3*time.Second))
	if err != nil || changed || active.Status != StatusRunning || active.Pending == nil {
		t.Fatalf("active reconcile=%#v changed=%t err=%v", active, changed, err)
	}
	if _, _, err = Reconcile(dir, request.ModuleID, "unknown", start.Add(4*time.Second)); err == nil {
		t.Fatal("unknown run state was accepted")
	}
	recovered, changed, err := Reconcile(dir, request.ModuleID, RunAbsent, start.Add(5*time.Second))
	if err != nil || !changed || recovered.Status != StatusQueued || recovered.SourceSHA != pendingInput.SourceSHA || recovered.Attempt != 1 {
		t.Fatalf("absent-run reconcile=%#v changed=%t err=%v", recovered, changed, err)
	}
}

func TestRequestRoundTripAndPathValidation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	request, err := New(testInput(), time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err = Save(dir, request); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir, request.ModuleID)
	if err != nil || loaded.Status != StatusQueued || loaded.Attempt != 1 {
		t.Fatalf("loaded = %#v, err = %v", loaded, err)
	}
	if _, err = Load(dir, "../../outside"); err == nil {
		t.Fatal("path-like module identity was accepted")
	}
}

func TestLoadRejectsTrailingJSONAndTamperedID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	request, err := New(testInput(), time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err = Save(dir, request); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, request.ModuleID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(data, []byte("{}\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(dir, request.ModuleID); err == nil {
		t.Fatal("trailing JSON document was accepted")
	}
	request.ID = "wrong-id"
	if err = request.Validate(); err == nil {
		t.Fatal("request ID unrelated to the fingerprint was accepted")
	}
}

func testInput() AnalysisInput {
	return AnalysisInput{
		ModuleID: "avm-res-demo", SourceSHA: repeat('a'), CLIVersion: "0.1.0", CLICommit: repeat('b'),
		BinaryDigest: repeatDigest('c'), PolicyDigest: repeatDigest('d'), ReportSchemaVersion: 2, AdapterVersion: 1,
	}
}

func repeatDigest(value byte) string {
	raw := make([]byte, 64)
	for index := range raw {
		raw[index] = value
	}
	return string(raw)
}

func repeat(value byte) string {
	raw := make([]byte, 40)
	for i := range raw {
		raw[i] = value
	}
	return string(raw)
}
