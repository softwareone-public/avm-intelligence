// Package queue stores one durable analysis request. It does not schedule work
// from Project card moves or treat a failed attempt as complete.
package queue

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// StatusQueued means the request is registered and has no accepted result.
	StatusQueued = "queued"
	// StatusFailed means the latest attempt failed and must not satisfy the fingerprint.
	StatusFailed = "failed"
	// StatusAccepted means the fingerprint has a validated result.
	StatusAccepted = "accepted"
)

// Request is the durable single-module record for this slice.
type Request struct {
	ID          string    `json:"id"`
	ModuleID    string    `json:"module_id"`
	SourceSHA   string    `json:"source_sha"`
	Fingerprint string    `json:"fingerprint"`
	Status      string    `json:"status"`
	Attempt     int       `json:"attempt"`
	CreatedAt   time.Time `json:"created_at"`
}

// New creates a queued request. The fingerprint separates source identity from
// the attempt number so a retry cannot erase a failed fingerprint.
func New(moduleID, sourceSHA, cliCommit, rulesetDigest string, now time.Time) (Request, error) {
	if moduleID == "" || len(sourceSHA) != 40 || len(cliCommit) != 40 || len(rulesetDigest) != 64 {
		return Request{}, fmt.Errorf("request identity is incomplete")
	}
	sum := sha256.Sum256([]byte(moduleID + "\n" + sourceSHA + "\n" + cliCommit + "\n" + rulesetDigest))
	return Request{
		ID: fmt.Sprintf("%s-%x", moduleID, sum[:4]), ModuleID: moduleID, SourceSHA: sourceSHA,
		Fingerprint: fmt.Sprintf("%x", sum), Status: StatusQueued, Attempt: 1, CreatedAt: now.UTC(),
	}, nil
}

// Save writes the request atomically and refuses to replace an accepted record
// with an older attempt.
func Save(dir string, request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, request.ModuleID+".json")
	if current, err := Load(dir, request.ModuleID); err == nil {
		if current.Fingerprint != request.Fingerprint || request.Attempt < current.Attempt {
			return fmt.Errorf("refusing to replace request %s with an older attempt", request.ModuleID)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

// Load reads one module request.
func Load(dir, moduleID string) (Request, error) {
	data, err := os.ReadFile(filepath.Join(dir, moduleID+".json"))
	if err != nil {
		return Request{}, err
	}
	var request Request
	if err = json.Unmarshal(data, &request); err != nil {
		return Request{}, fmt.Errorf("parse request: %w", err)
	}
	return request, request.Validate()
}

// Validate checks the stored request independently of its writer.
func (r Request) Validate() error {
	switch r.Status {
	case StatusQueued, StatusFailed, StatusAccepted:
	default:
		return fmt.Errorf("invalid request status %q", r.Status)
	}
	if r.ID == "" || r.ModuleID == "" || len(r.SourceSHA) != 40 || len(r.Fingerprint) != 64 || r.Attempt < 1 {
		return fmt.Errorf("request record is incomplete")
	}
	return nil
}

func writeAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".request-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
