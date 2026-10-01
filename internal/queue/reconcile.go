package queue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	ReconcileUnchanged = "unchanged"
	ReconcileRecovered = "recovered"
)

// RunObservation is the caller's confirmed state for an owning Actions run.
// Missing observations never imply that a run is absent.
type RunObservation struct {
	ModuleID string `json:"module_id"`
	State    string `json:"state"`
}

// ReconcileItem records the result of reconciling one durable request.
type ReconcileItem struct {
	ModuleID       string `json:"module_id"`
	Action         string `json:"action"`
	PreviousStatus string `json:"previous_status"`
	Status         string `json:"status"`
	RunState       string `json:"run_state,omitempty"`
}

// ReconcilePlan records request recovery and the next bounded dispatch set.
type ReconcilePlan struct {
	ReconciledAt time.Time       `json:"reconciled_at"`
	Items        []ReconcileItem `json:"items"`
	Batches      []DispatchBatch `json:"batches"`
}

// ReconcileFleet reconciles all request files using only explicit run states.
// It does not infer absent runs from missing observations and does not start
// jobs or mutate any GitHub projection.
func ReconcileFleet(dir string, observations []RunObservation, now time.Time) (ReconcilePlan, error) {
	if now.IsZero() {
		return ReconcilePlan{}, fmt.Errorf("reconciliation timestamp is required")
	}
	stateByModule, err := indexRunObservations(observations)
	if err != nil {
		return ReconcilePlan{}, err
	}
	modules, err := requestModules(dir)
	if err != nil {
		return ReconcilePlan{}, err
	}
	plan := ReconcilePlan{ReconciledAt: now.UTC()}
	dispatchable := make([]string, 0)
	for _, moduleID := range modules {
		item, queued, err := reconcileModule(dir, moduleID, stateByModule, now)
		if err != nil {
			return ReconcilePlan{}, err
		}
		if queued {
			dispatchable = append(dispatchable, moduleID)
		}
		plan.Items = append(plan.Items, item)
	}
	plan.Batches = makeDispatchBatches(dispatchable)
	return plan, nil
}

func reconcileModule(dir, moduleID string, states map[string]string, now time.Time) (ReconcileItem, bool, error) {
	request, err := Load(dir, moduleID)
	if err != nil {
		return ReconcileItem{}, false, err
	}
	item := ReconcileItem{ModuleID: moduleID, Action: ReconcileUnchanged, PreviousStatus: request.Status, Status: request.Status}
	if request.Status != StatusRunning {
		return item, request.Status == StatusQueued, nil
	}
	state, exists := states[moduleID]
	if !exists {
		return ReconcileItem{}, false, fmt.Errorf("missing run observation for running request %s", moduleID)
	}
	updated, changed, err := Reconcile(dir, moduleID, state, now)
	if err != nil {
		return ReconcileItem{}, false, err
	}
	item.Status = updated.Status
	item.RunState = state
	if changed {
		item.Action = ReconcileRecovered
	}
	return item, updated.Status == StatusQueued, nil
}

func indexRunObservations(observations []RunObservation) (map[string]string, error) {
	indexed := make(map[string]string, len(observations))
	for _, observation := range observations {
		if !moduleIDPattern.MatchString(observation.ModuleID) {
			return nil, fmt.Errorf("invalid run observation module ID %q", observation.ModuleID)
		}
		if observation.State != RunActive && observation.State != RunCompleted && observation.State != RunAbsent {
			return nil, fmt.Errorf("invalid run observation state %q for %s", observation.State, observation.ModuleID)
		}
		if _, exists := indexed[observation.ModuleID]; exists {
			return nil, fmt.Errorf("duplicate run observation for %s", observation.ModuleID)
		}
		indexed[observation.ModuleID] = observation.State
	}
	return indexed, nil
}

func requestModules(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	modules := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		moduleID := strings.TrimSuffix(entry.Name(), ".json")
		if !moduleIDPattern.MatchString(moduleID) {
			return nil, fmt.Errorf("invalid request filename %q", entry.Name())
		}
		modules = append(modules, moduleID)
	}
	sort.Strings(modules)
	return modules, nil
}

// DecodeRunObservations strictly decodes one JSON document for CLI callers.
func DecodeRunObservations(data []byte) ([]RunObservation, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var observations []RunObservation
	if err := decoder.Decode(&observations); err != nil {
		return nil, fmt.Errorf("parse run observations: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("run observations must contain one JSON document")
	}
	return observations, nil
}
