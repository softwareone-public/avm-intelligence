// Package queue owns durable per-module analysis requests. It does not run jobs
// or infer authorization from Project card moves.
package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	StatusQueued             = "queued"
	StatusClaimed            = "claimed"
	StatusRunning            = "running"
	StatusFailed             = "failed"
	StatusAccepted           = "accepted"
	StatusAwaitingAcceptance = "awaiting_acceptance"
	StatusFactsAccepted      = "facts_accepted"
	StatusSuperseded         = "superseded"

	RunActive    = "active"
	RunCompleted = "completed"
	RunAbsent    = "absent"
	RunUnknown   = "unknown"
)

var moduleIDPattern = regexp.MustCompile(`^avm-(res|ptn|utl)-[a-z0-9]+(?:-[a-z0-9]+)*$`)

// AnalysisInput contains every deterministic and optional semantic input that
// can change an analysis result. Fingerprinting these fields prevents reuse
// across an incompatible tool, policy, schema, adapter, or model contract.
type AnalysisInput struct {
	ModuleID             string `json:"module_id"`
	SourceSHA            string `json:"source_sha"`
	CLIVersion           string `json:"cli_version"`
	CLICommit            string `json:"cli_commit"`
	BinaryDigest         string `json:"binary_digest"`
	PolicyDigest         string `json:"policy_digest"`
	ReportSchemaVersion  int    `json:"report_schema_version"`
	AdapterVersion       int    `json:"adapter_version"`
	SemanticConfigDigest string `json:"semantic_config_digest,omitempty"`
}

// PendingRequest holds the single newest desired input while another request
// owns an active run for the module.
type PendingRequest struct {
	ID          string    `json:"id"`
	SourceSHA   string    `json:"source_sha"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
}

// Request stores desired work, attempt ownership, and last accepted identity.
type Request struct {
	ID                      string          `json:"id"`
	ModuleID                string          `json:"module_id"`
	SourceSHA               string          `json:"source_sha"`
	Fingerprint             string          `json:"fingerprint"`
	Status                  string          `json:"status"`
	Attempt                 int             `json:"attempt"`
	OwnerRunID              string          `json:"owner_run_id,omitempty"`
	LastAcceptedFingerprint string          `json:"last_accepted_fingerprint,omitempty"`
	LastAcceptedSourceSHA   string          `json:"last_accepted_source_sha,omitempty"`
	Pending                 *PendingRequest `json:"pending,omitempty"`
	Work                    *WorkContract   `json:"work,omitempty"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

// Fingerprint returns a canonical SHA-256 over the complete analysis contract.
func (input AnalysisInput) Fingerprint() (string, error) {
	if err := input.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// Validate checks all required analysis identities before they enter durable state.
func (input AnalysisInput) Validate() error {
	if !moduleIDPattern.MatchString(input.ModuleID) || len(input.SourceSHA) != 40 || len(input.CLICommit) != 40 || len(input.BinaryDigest) != 64 || len(input.PolicyDigest) != 64 || input.CLIVersion == "" || input.ReportSchemaVersion < 1 || input.AdapterVersion < 1 {
		return fmt.Errorf("analysis fingerprint input is incomplete or invalid")
	}
	if _, err := hex.DecodeString(input.SourceSHA); err != nil {
		return fmt.Errorf("source SHA must be lowercase hexadecimal")
	}
	if _, err := hex.DecodeString(input.CLICommit); err != nil {
		return fmt.Errorf("CLI commit must be lowercase hexadecimal")
	}
	if _, err := hex.DecodeString(input.BinaryDigest); err != nil {
		return fmt.Errorf("binary digest must be lowercase hexadecimal")
	}
	if _, err := hex.DecodeString(input.PolicyDigest); err != nil {
		return fmt.Errorf("policy digest must be lowercase hexadecimal")
	}
	if input.SemanticConfigDigest != "" {
		if len(input.SemanticConfigDigest) != 64 {
			return fmt.Errorf("semantic configuration digest must be a SHA-256")
		}
		if _, err := hex.DecodeString(input.SemanticConfigDigest); err != nil {
			return fmt.Errorf("semantic configuration digest must be lowercase hexadecimal")
		}
	}
	return nil
}

// New creates the first queued attempt for an analysis contract.
func New(input AnalysisInput, now time.Time) (Request, error) {
	fingerprint, err := input.Fingerprint()
	if err != nil {
		return Request{}, err
	}
	if now.IsZero() {
		return Request{}, fmt.Errorf("request timestamp is required")
	}
	return requestFor(input.ModuleID, input.SourceSHA, fingerprint, 1, now.UTC()), nil
}

// Register coalesces unchanged queued/running work, retries failed identical
// work, and keeps at most one newer pending request while a run is active.
// The bool reports whether durable state changed; no-op coalescing never writes.
func Register(dir string, input AnalysisInput, now time.Time) (Request, bool, error) {
	desired, err := New(input, now)
	if err != nil {
		return Request{}, false, err
	}
	current, err := Load(dir, input.ModuleID)
	if os.IsNotExist(err) {
		return saveChanged(dir, desired)
	}
	if err != nil {
		return Request{}, false, err
	}
	return registerExisting(dir, current, desired, now)
}

func registerExisting(dir string, current, desired Request, now time.Time) (Request, bool, error) {
	desired.LastAcceptedFingerprint = current.LastAcceptedFingerprint
	desired.LastAcceptedSourceSHA = current.LastAcceptedSourceSHA
	switch current.Status {
	case StatusRunning:
		return registerWhileRunning(dir, current, desired, now)
	case StatusQueued:
		if desired.Fingerprint == current.Fingerprint {
			return current, false, nil
		}
		desired.LastAcceptedFingerprint = current.LastAcceptedFingerprint
	case StatusFailed:
		if desired.Fingerprint == current.Fingerprint {
			desired.Attempt = current.Attempt + 1
			desired.LastAcceptedFingerprint = current.LastAcceptedFingerprint
			desired.LastAcceptedSourceSHA = current.LastAcceptedSourceSHA
		}
	case StatusAccepted:
		if desired.Fingerprint == current.LastAcceptedFingerprint || desired.Fingerprint == current.Fingerprint {
			return current, false, nil
		}
		desired.LastAcceptedFingerprint = current.Fingerprint
		desired.LastAcceptedSourceSHA = current.SourceSHA
	default:
		return Request{}, false, fmt.Errorf("invalid stored request status %q", current.Status)
	}
	return saveChanged(dir, desired)
}

func registerWhileRunning(dir string, current, desired Request, now time.Time) (Request, bool, error) {
	if desired.Fingerprint == current.Fingerprint {
		if current.Pending == nil {
			return current, false, nil
		}
		current.Pending = nil
		current.UpdatedAt = now.UTC()
		return saveChanged(dir, current)
	}
	if current.Pending != nil && current.Pending.Fingerprint == desired.Fingerprint {
		return current, false, nil
	}
	current.Pending = &PendingRequest{ID: desired.ID, SourceSHA: desired.SourceSHA, Fingerprint: desired.Fingerprint, CreatedAt: desired.CreatedAt}
	current.UpdatedAt = now.UTC()
	return saveChanged(dir, current)
}

func saveChanged(dir string, request Request) (Request, bool, error) {
	if err := saveAtomic(dir, request); err != nil {
		return Request{}, false, err
	}
	return request, true, nil
}

// Start claims one queued fingerprint for a concrete run ID.
func Start(dir, moduleID, fingerprint, runID string, now time.Time) (Request, error) {
	if runID == "" || now.IsZero() {
		return Request{}, fmt.Errorf("run ID and start time are required")
	}
	request, err := Load(dir, moduleID)
	if err != nil {
		return Request{}, err
	}
	if request.Status != StatusQueued || request.Fingerprint != fingerprint {
		return Request{}, fmt.Errorf("queued request ownership mismatch for %s", moduleID)
	}
	request.Status = StatusRunning
	request.OwnerRunID = runID
	request.UpdatedAt = now.UTC()
	if err = saveAtomic(dir, request); err != nil {
		return Request{}, err
	}
	return request, nil
}

// Finish accepts a terminal result only from the current owning run. If a newer
// desired input accumulated while running, it becomes the next queued request.
func Finish(dir, moduleID, fingerprint, runID string, succeeded bool, now time.Time) (Request, error) {
	if now.IsZero() {
		return Request{}, fmt.Errorf("finish time is required")
	}
	request, err := Load(dir, moduleID)
	if err != nil {
		return Request{}, err
	}
	if request.Status != StatusRunning || request.Fingerprint != fingerprint || request.OwnerRunID != runID {
		return Request{}, fmt.Errorf("run does not own the active request for %s", moduleID)
	}
	request.OwnerRunID = ""
	request.UpdatedAt = now.UTC()
	if request.Pending != nil {
		pending := request.Pending
		request.Pending = nil
		request.ID = pending.ID
		request.SourceSHA = pending.SourceSHA
		request.Fingerprint = pending.Fingerprint
		request.Attempt = 1
		request.CreatedAt = pending.CreatedAt
		if pending.Fingerprint == request.LastAcceptedFingerprint && request.LastAcceptedFingerprint != "" {
			request.SourceSHA = request.LastAcceptedSourceSHA
			request.Status = StatusAccepted
		} else {
			request.Status = StatusQueued
		}
	} else if succeeded {
		request.Status = StatusAccepted
		request.LastAcceptedFingerprint = request.Fingerprint
		request.LastAcceptedSourceSHA = request.SourceSHA
	} else {
		request.Status = StatusFailed
	}
	if err = saveAtomic(dir, request); err != nil {
		return Request{}, err
	}
	return request, nil
}

// Reconcile releases stale ownership only after the caller has confirmed that
// the run is terminal or absent. A terminal run without an accepted validated
// result is treated as failed; any newer pending input is promoted for retry.
func Reconcile(dir, moduleID, runState string, now time.Time) (Request, bool, error) {
	request, err := Load(dir, moduleID)
	if err != nil {
		return Request{}, false, err
	}
	if request.Status != StatusRunning {
		return request, false, nil
	}
	switch runState {
	case RunActive:
		return request, false, nil
	case RunCompleted, RunAbsent:
		updated, finishErr := Finish(dir, moduleID, request.Fingerprint, request.OwnerRunID, false, now)
		return updated, finishErr == nil, finishErr
	default:
		return Request{}, false, fmt.Errorf("cannot reconcile unknown run state %q", runState)
	}
}

// Save writes a request atomically. It refuses changed fingerprints so callers
// use Register for coalescing and freshness-order rules.
func Save(dir string, request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	current, err := Load(dir, request.ModuleID)
	if err == nil {
		if current.Fingerprint != request.Fingerprint || request.Attempt < current.Attempt {
			return fmt.Errorf("refusing to replace request %s with an older or different attempt", request.ModuleID)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return saveAtomic(dir, request)
}

// Load reads one module request with strict JSON decoding.
func Load(dir, moduleID string) (Request, error) {
	if !moduleIDPattern.MatchString(moduleID) {
		return Request{}, fmt.Errorf("invalid module ID %q", moduleID)
	}
	data, err := os.ReadFile(filepath.Join(dir, moduleID+".json"))
	if err != nil {
		return Request{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var request Request
	if err = decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("parse request: %w", err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return Request{}, fmt.Errorf("request must contain one JSON document")
	}
	if err = request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

// Validate checks the stored request independently of its writer.
func (r Request) Validate() error {
	if !validStatus(r.Status) {
		return fmt.Errorf("invalid request status %q", r.Status)
	}
	if err := validateRequestIdentity(r); err != nil {
		return err
	}
	if err := validateRunOwner(r); err != nil {
		return err
	}
	if err := validateAcceptedFingerprint(r); err != nil {
		return err
	}
	if err := validatePendingRequest(r); err != nil {
		return err
	}
	if r.Work != nil {
		return validateWorkRequest(r)
	}
	if r.Status == StatusClaimed || r.Status == StatusAwaitingAcceptance || r.Status == StatusFactsAccepted || r.Status == StatusSuperseded {
		return fmt.Errorf("status %q requires a versioned work contract", r.Status)
	}
	if r.Status == StatusAccepted && r.LastAcceptedFingerprint != r.Fingerprint {
		return fmt.Errorf("accepted request fingerprint is not recorded as accepted")
	}
	return nil
}

func validStatus(status string) bool {
	switch status {
	case StatusQueued, StatusClaimed, StatusRunning, StatusFailed, StatusAccepted, StatusAwaitingAcceptance, StatusFactsAccepted, StatusSuperseded:
		return true
	default:
		return false
	}
}

func validateRequestIdentity(request Request) error {
	if !moduleIDPattern.MatchString(request.ModuleID) || request.ID != requestID(request.ModuleID, request.Fingerprint) || len(request.SourceSHA) != 40 || len(request.Fingerprint) != 64 || request.Attempt < 1 || request.CreatedAt.IsZero() || request.UpdatedAt.IsZero() {
		return fmt.Errorf("request record is incomplete")
	}
	if _, err := hex.DecodeString(request.SourceSHA); err != nil {
		return fmt.Errorf("request source SHA is invalid")
	}
	if _, err := hex.DecodeString(request.Fingerprint); err != nil {
		return fmt.Errorf("request fingerprint is invalid")
	}
	return nil
}

func validateRunOwner(request Request) error {
	if request.Status == StatusRunning && request.OwnerRunID == "" {
		return fmt.Errorf("running request has no owner run ID")
	}
	if request.Status != StatusRunning && request.OwnerRunID != "" {
		return fmt.Errorf("non-running request has an owner run ID")
	}
	return nil
}

func validateAcceptedFingerprint(request Request) error {
	if request.LastAcceptedFingerprint == "" {
		if request.LastAcceptedSourceSHA != "" {
			return fmt.Errorf("last accepted source SHA has no fingerprint")
		}
		return nil
	}
	if len(request.LastAcceptedFingerprint) != 64 {
		return fmt.Errorf("last accepted fingerprint is invalid")
	}
	if _, err := hex.DecodeString(request.LastAcceptedFingerprint); err != nil {
		return fmt.Errorf("last accepted fingerprint is invalid")
	}
	if len(request.LastAcceptedSourceSHA) != 40 {
		return fmt.Errorf("last accepted source SHA is invalid")
	}
	if _, err := hex.DecodeString(request.LastAcceptedSourceSHA); err != nil {
		return fmt.Errorf("last accepted source SHA is invalid")
	}
	return nil
}

func validatePendingRequest(request Request) error {
	if request.Pending == nil {
		return nil
	}
	pending := request.Pending
	if request.Status != StatusRunning || pending.ID != requestID(request.ModuleID, pending.Fingerprint) || len(pending.SourceSHA) != 40 || len(pending.Fingerprint) != 64 || pending.CreatedAt.IsZero() {
		return fmt.Errorf("pending request is invalid")
	}
	if _, err := hex.DecodeString(pending.SourceSHA); err != nil {
		return fmt.Errorf("pending request source SHA is invalid")
	}
	if _, err := hex.DecodeString(pending.Fingerprint); err != nil {
		return fmt.Errorf("pending request fingerprint is invalid")
	}
	return nil
}

func requestFor(moduleID, sourceSHA, fingerprint string, attempt int, now time.Time) Request {
	return Request{
		ID: requestID(moduleID, fingerprint), ModuleID: moduleID, SourceSHA: sourceSHA,
		Fingerprint: fingerprint, Status: StatusQueued, Attempt: attempt, CreatedAt: now, UpdatedAt: now,
	}
}

func requestID(moduleID, fingerprint string) string {
	if len(fingerprint) != 64 {
		return ""
	}
	return fmt.Sprintf("%s-%s", moduleID, fingerprint)
}

func saveAtomic(dir string, request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, request.ModuleID+".json")
	temp, err := os.CreateTemp(dir, ".request-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
