// Package queue owns durable per-module analysis requests. It does not run jobs
// or infer authorization from Project card moves.
package queue

import (
	"encoding/hex"

	"fmt"

	"regexp"

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

// Request stores desired work, attempt ownership, and last accepted identity.
type Request struct {
	ID                      string `json:"id"`
	ModuleID                string `json:"module_id"`
	SourceSHA               string `json:"source_sha"`
	Fingerprint             string `json:"fingerprint"`
	Status                  string `json:"status"`
	Attempt                 int    `json:"attempt"`
	OwnerRunID              string `json:"owner_run_id,omitempty"`
	LastAcceptedFingerprint string `json:"last_accepted_fingerprint,omitempty"`
	LastAcceptedSourceSHA   string `json:"last_accepted_source_sha,omitempty"`

	Work      *WorkContract `json:"work,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// Validate checks the stored request independently of its writer.
func (r Request) Validate() error {
	if r.Work == nil {
		return fmt.Errorf("native work contract is required")
	}
	if !validStatus(r.Status) {
		return fmt.Errorf("invalid request status %q",

			r.Status,
		)
	}
	if err := validateRequestIdentity(r); err !=
		nil {
		return err

	}
	if err := validateRunOwner(r); err != nil {
		return err
	}
	if err := validateAcceptedFingerprint(r); err != nil {
		return err
	}
	return validateWorkRequest(r)
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
