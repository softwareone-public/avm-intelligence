package queue

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/catalog"
	"github.com/softwareone-public/avm-intelligence/internal/source"
	"golang.org/x/mod/semver"
)

const MaxBatchModules = 100

const (
	PlanQueued    = "queued"
	PlanRetry     = "retry"
	PlanPending   = "pending"
	PlanCoalesced = "coalesced"
	PlanReused    = "reused"
	PlanBlocked   = "blocked"
	PlanRetired   = "retired"
)

// AnalysisContract contains the tool and policy identity shared by a planning batch.
type AnalysisContract struct {
	CLIVersion           string
	CLICommit            string
	BinaryDigest         string
	PolicyDigest         string
	ReportSchemaVersion  int
	AdapterVersion       int
	SemanticConfigDigest string
}

// PlanItem describes one accepted module's request decision.
type PlanItem struct {
	ModuleID    string `json:"module_id"`
	Action      string `json:"action"`
	Reason      string `json:"reason,omitempty"`
	SourceSHA   string `json:"source_sha,omitempty"`
	Version     string `json:"source_version,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
}

// DispatchBatch bounds later workflow matrices. Pending requests are not listed
// until their current owner has completed or been reconciled.
type DispatchBatch struct {
	Number    int      `json:"number"`
	ModuleIDs []string `json:"module_ids"`
}

// BatchPlan is an offline planning artifact derived from one accepted catalog.
type BatchPlan struct {
	CatalogGeneration uint64          `json:"catalog_generation"`
	PlannedAt         time.Time       `json:"planned_at"`
	Items             []PlanItem      `json:"items"`
	Batches           []DispatchBatch `json:"batches"`
}

// PlanAcceptedCatalog plans and registers work only for active modules in the
// accepted catalog. It emits explicit blocked/retired records and bounded worker
// batches, but does not start Actions or update GitHub Issues/Projects.
func PlanAcceptedCatalog(state catalog.AcceptedState, pins []source.ResolvedSource, contract AnalysisContract, requestsDir string, now time.Time) (BatchPlan, error) {
	if err := validatePlanningInput(state, contract, now); err != nil {
		return BatchPlan{}, err
	}
	pinByID, err := indexPins(state, pins)
	if err != nil {
		return BatchPlan{}, err
	}
	plan := BatchPlan{CatalogGeneration: state.Generation, PlannedAt: now.UTC()}
	active, retired := splitAcceptedModules(state)
	plan.Items = append(plan.Items, retired...)
	dispatchable := make([]string, 0)
	for _, record := range active {
		item, shouldDispatch, err := planModule(record.Module, pinByID, contract, requestsDir, now)
		if err != nil {
			return BatchPlan{}, err
		}
		plan.Items = append(plan.Items, item)
		if shouldDispatch {
			dispatchable = append(dispatchable, record.Module.ID)
		}
	}
	plan.Batches = makeDispatchBatches(dispatchable)
	sort.Slice(plan.Items, func(i, j int) bool { return plan.Items[i].ModuleID < plan.Items[j].ModuleID })
	return plan, nil
}

func validatePlanningInput(state catalog.AcceptedState, contract AnalysisContract, now time.Time) error {
	if err := state.Validate(); err != nil {
		return fmt.Errorf("accepted catalog: %w", err)
	}
	if now.IsZero() {
		return fmt.Errorf("planning timestamp is required")
	}
	return validateContract(contract)
}

func splitAcceptedModules(state catalog.AcceptedState) ([]catalog.ModuleRecord, []PlanItem) {
	active := make([]catalog.ModuleRecord, 0)
	retired := make([]PlanItem, 0)
	for _, record := range state.Modules {
		if record.Active {
			active = append(active, record)
			continue
		}
		retired = append(retired, PlanItem{ModuleID: record.Module.ID, Action: PlanRetired})
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Module.ID < active[j].Module.ID })
	return active, retired
}

func planModule(module catalog.Module, pinByID map[string]source.ResolvedSource, contract AnalysisContract, requestsDir string, now time.Time) (PlanItem, bool, error) {
	pin, exists := pinByID[module.ID]
	if !exists {
		return PlanItem{ModuleID: module.ID, Action: PlanBlocked, Reason: "no immutable source pin is available"}, false, nil
	}
	if err := validatePin(module, pin); err != nil {
		return PlanItem{ModuleID: module.ID, Action: PlanBlocked, Reason: err.Error()}, false, nil
	}
	input := AnalysisInput{
		ModuleID: module.ID, SourceSHA: pin.Commit, CLIVersion: contract.CLIVersion, CLICommit: contract.CLICommit,
		BinaryDigest: contract.BinaryDigest, PolicyDigest: contract.PolicyDigest,
		ReportSchemaVersion: contract.ReportSchemaVersion, AdapterVersion: contract.AdapterVersion,
		SemanticConfigDigest: contract.SemanticConfigDigest,
	}
	fingerprint, err := input.Fingerprint()
	if err != nil {
		return PlanItem{}, false, err
	}
	current, loadErr := Load(requestsDir, module.ID)
	if loadErr != nil && !os.IsNotExist(loadErr) {
		return PlanItem{}, false, fmt.Errorf("load request for %s: %w", module.ID, loadErr)
	}
	action := planAction(current, loadErr == nil, fingerprint)
	request, changed, err := Register(requestsDir, input, now)
	if err != nil {
		return PlanItem{}, false, fmt.Errorf("register request for %s: %w", module.ID, err)
	}
	if !changed && (action == PlanQueued || action == PlanRetry || action == PlanPending) {
		action = PlanCoalesced
	}
	item := PlanItem{ModuleID: module.ID, Action: action, SourceSHA: pin.Commit, Version: pin.Version, Fingerprint: fingerprint, RequestID: request.ID}
	return item, request.Status == StatusQueued, nil
}

func makeDispatchBatches(moduleIDs []string) []DispatchBatch {
	batches := make([]DispatchBatch, 0, (len(moduleIDs)+MaxBatchModules-1)/MaxBatchModules)
	for offset := 0; offset < len(moduleIDs); offset += MaxBatchModules {
		end := min(offset+MaxBatchModules, len(moduleIDs))
		batchModules := append([]string(nil), moduleIDs[offset:end]...)
		batches = append(batches, DispatchBatch{Number: len(batches) + 1, ModuleIDs: batchModules})
	}
	return batches
}

func validateContract(contract AnalysisContract) error {
	probe := AnalysisInput{
		ModuleID: "avm-res-contract-validation", SourceSHA: strings.Repeat("a", 40),
		CLIVersion: contract.CLIVersion, CLICommit: contract.CLICommit, BinaryDigest: contract.BinaryDigest,
		PolicyDigest: contract.PolicyDigest, ReportSchemaVersion: contract.ReportSchemaVersion,
		AdapterVersion: contract.AdapterVersion, SemanticConfigDigest: contract.SemanticConfigDigest,
	}
	if _, err := probe.Fingerprint(); err != nil {
		return fmt.Errorf("analysis contract: %w", err)
	}
	return nil
}

func indexPins(state catalog.AcceptedState, pins []source.ResolvedSource) (map[string]source.ResolvedSource, error) {
	active := make(map[string]catalog.Module, len(state.Modules))
	for _, record := range state.Modules {
		if record.Active {
			active[record.Module.ID] = record.Module
		}
	}
	indexed := make(map[string]source.ResolvedSource, len(pins))
	for _, pin := range pins {
		if _, ok := active[pin.ModuleID]; !ok {
			return nil, fmt.Errorf("source pin %s is not for an active accepted module", pin.ModuleID)
		}
		if _, exists := indexed[pin.ModuleID]; exists {
			return nil, fmt.Errorf("duplicate source pin for %s", pin.ModuleID)
		}
		indexed[pin.ModuleID] = pin
	}
	return indexed, nil
}

func validatePin(module catalog.Module, pin source.ResolvedSource) error {
	if pin.ModuleID != module.ID || len(pin.Commit) != 40 {
		return fmt.Errorf("invalid source pin identity for %s", module.ID)
	}
	if _, err := hex.DecodeString(pin.Commit); err != nil {
		return fmt.Errorf("invalid source commit for %s", module.ID)
	}
	version := pin.Version
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	if !semver.IsValid(version) || semver.Prerelease(version) != "" || semver.Canonical(version) == "" {
		return fmt.Errorf("source pin for %s is not a stable SemVer release", module.ID)
	}
	if pin.Tag != version && pin.Tag != strings.TrimPrefix(version, "v") {
		return fmt.Errorf("source tag %s does not match version %s for %s", pin.Tag, pin.Version, module.ID)
	}
	parsed, err := url.Parse(module.RepositoryURL)
	if err != nil {
		return fmt.Errorf("accepted repository URL is invalid for %s", module.ID)
	}
	repository := strings.Trim(parsed.Path, "/")
	if pin.Repository != repository {
		return fmt.Errorf("source repository %s does not match accepted repository %s for %s", pin.Repository, repository, module.ID)
	}
	wantArchive := fmt.Sprintf("https://github.com/%s/archive/%s.tar.gz", repository, pin.Commit)
	if pin.ArchiveURL != wantArchive {
		return fmt.Errorf("source archive URL is not commit-pinned for %s", module.ID)
	}
	if pin.ReleaseURL == "" {
		return fmt.Errorf("source release URL is missing for %s", module.ID)
	}
	return nil
}

func planAction(current Request, exists bool, fingerprint string) string {
	if !exists {
		return PlanQueued
	}
	switch current.Status {
	case StatusAccepted:
		if current.LastAcceptedFingerprint == fingerprint {
			return PlanReused
		}
		return PlanQueued
	case StatusQueued:
		if current.Fingerprint == fingerprint {
			return PlanCoalesced
		}
		return PlanQueued
	case StatusRunning:
		if current.Fingerprint == fingerprint || (current.Pending != nil && current.Pending.Fingerprint == fingerprint) {
			return PlanCoalesced
		}
		return PlanPending
	case StatusFailed:
		if current.Fingerprint == fingerprint {
			return PlanRetry
		}
		return PlanQueued
	default:
		return PlanBlocked
	}
}
