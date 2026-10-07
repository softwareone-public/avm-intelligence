package ratelimit

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// SessionCharge preserves local rolling costs between segments of the same job.
type SessionCharge struct {
	At       time.Time `json:"at"`
	Resource string    `json:"resource"`
	Points   int       `json:"points"`
	Mutation bool      `json:"mutation"`
}

// sessionState contains no credentials or authoritative lifecycle state.
type sessionState struct {
	Version      int             `json:"version"`
	RunID        string          `json:"run_id"`
	Charges      []SessionCharge `json:"charges"`
	PausedUntil  time.Time       `json:"paused_until"`
	LastMutation time.Time       `json:"last_mutation"`
}

// LoadSession restores only the same job's local rolling window; primary quotas are refreshed live.
func (pacer *Pacer) LoadSession(path, runID string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read pacing session: %w", err)
	}
	var session sessionState
	if err := json.Unmarshal(data, &session); err != nil {
		return fmt.Errorf("decode pacing session: %w", err)
	}
	if session.Version != 1 || session.RunID != runID {
		return fmt.Errorf("pacing session identity mismatch")
	}
	pacer.mu.Lock()
	defer pacer.mu.Unlock()
	for _, item := range session.Charges {
		if item.At.IsZero() || item.At.After(pacer.now()) || item.Points <= 0 || (item.Resource != "rest" && item.Resource != "graphql" && item.Resource != "search") {
			return fmt.Errorf("pacing session cost invalid")
		}
		if pacer.now().Sub(item.At) < hourWindow {
			pacer.charges = append(pacer.charges, charge{at: item.At, resource: item.Resource, points: item.Points, mutation: item.Mutation})
		}
	}
	pacer.pausedUntil, pacer.lastMutation = session.PausedUntil, session.LastMutation
	return nil
}

// SaveSession atomically saves rolling costs before the workflow starts another segment.
func (pacer *Pacer) SaveSession(path, runID string) error {
	pacer.mu.Lock()
	session := sessionState{Version: 1, RunID: runID, PausedUntil: pacer.pausedUntil, LastMutation: pacer.lastMutation}
	for _, item := range pacer.charges {
		session.Charges = append(session.Charges, SessionCharge{At: item.at, Resource: item.resource, Points: item.points, Mutation: item.mutation})
	}
	pacer.mu.Unlock()
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		return fmt.Errorf("write pacing session: %w", err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return fmt.Errorf("replace pacing session: %w", err)
	}
	return nil
}
