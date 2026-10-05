package site

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/publish"
)

type PresentationExportInput struct {
	DataRoot        string
	ProposalRoot    string
	ExpectedBase    string
	AsOf            time.Time
	CollectorHealth []publish.PresentationCollectorHealth
}

type presentationCoverage struct {
	Capabilities map[string]string `json:"capabilities"`
	Complete     bool              `json:"complete"`
	ObservedAt   string            `json:"observed_at"`
	Reason       string            `json:"reason,omitempty"`
}

// ExportPresentation builds a complete safe as-of presentation proposal from
// accepted typed records. It reads no operational state and performs no writes.
func ExportPresentation(input PresentationExportInput) (publish.Proposal, error) {
	if input.DataRoot == "" || input.ProposalRoot == "" || input.ExpectedBase == "" || input.AsOf.IsZero() {
		return publish.Proposal{}, fmt.Errorf("presentation export requires data/proposal roots, exact base, and as-of time")
	}
	records, err := index.ReadRecords(input.DataRoot, input.AsOf)
	if err != nil {
		return publish.Proposal{}, fmt.Errorf("read accepted presentation inputs: %w", err)
	}
	indexes, err := index.Build(records)
	if err != nil {
		return publish.Proposal{}, fmt.Errorf("validate accepted presentation inputs: %w", err)
	}
	modules, err := buildPresentationModules(records, indexes, input.AsOf)
	if err != nil {
		return publish.Proposal{}, err
	}
	return publish.BuildPresentationProposal(publish.PresentationProposalInput{
		ProposalRoot: input.ProposalRoot, BaseDataRoot: input.DataRoot,
		ExpectedBase: input.ExpectedBase, AsOf: input.AsOf,
		Modules: modules, CollectorHealth: input.CollectorHealth,
	})
}

func buildPresentationModules(records index.Records, indexes index.Indexes, asOf time.Time) ([]publish.PresentationModule, error) {
	factsByID := make(map[string]index.FactRecord, len(records.Facts))
	versionsByModule := make(map[string]index.ModuleVersionsIndexRecord, len(records.VersionIndexes))
	for _, facts := range records.Facts {
		factsByID[facts.ID] = facts
	}
	for _, versions := range records.VersionIndexes {
		versionsByModule[versions.ModuleID] = versions
	}
	moduleIndexes := make(map[string]index.ModuleIndex, len(indexes.Modules))
	for _, moduleIndex := range indexes.Modules {
		moduleIndexes[moduleIndex.ModuleID] = moduleIndex
	}
	modules := make([]publish.PresentationModule, 0, len(records.Modules))
	for _, module := range records.Modules {
		moduleIndex, exists := moduleIndexes[module.ModuleID]
		if !exists {
			return nil, fmt.Errorf("presentation module %s has no generated module index", module.ModuleID)
		}
		presentationModule, err := buildPresentationModule(module, moduleIndex, factsByID, versionsByModule, asOf)
		if err != nil {
			return nil, err
		}
		modules = append(modules, presentationModule)
	}
	sort.Slice(modules, func(left, right int) bool { return modules[left].ModuleID < modules[right].ModuleID })
	return modules, nil
}

func buildPresentationModule(module index.ModuleRecord, moduleIndex index.ModuleIndex, factsByID map[string]index.FactRecord, versionsByModule map[string]index.ModuleVersionsIndexRecord, asOf time.Time) (publish.PresentationModule, error) {
	presentationModule := publish.PresentationModule{
		ModuleID: module.ModuleID, Lifecycle: module.Lifecycle,
		SelectedReceiptIDs: []string{}, Coverage: marshalPresentationCoverage(emptyPresentationCoverage(asOf, "facts-unavailable")),
	}
	if versionIndex, found := versionsByModule[module.ModuleID]; found && versionIndex.OverallLatest != nil {
		presentationModule.DesiredSourceSHA = versionIndex.OverallLatest.SourceSHA
	}
	selectedFacts, receiptIDs, selectedSource, err := selectedPresentationFacts(module.ModuleID, moduleIndex.CurrentSelections, factsByID)
	if err != nil {
		return publish.PresentationModule{}, err
	}
	presentationModule.SelectedReceiptIDs = receiptIDs
	if selectedSource != "" {
		presentationModule.DesiredSourceSHA = selectedSource
	}
	if len(selectedFacts) > 0 {
		coverage := mergePresentationCoverage(selectedFacts, asOf)
		presentationModule.Coverage = marshalPresentationCoverage(coverage)
	}
	presentationModule.ReasonCode = presentationModuleReason(module.Lifecycle, presentationModule.DesiredSourceSHA, selectedFacts, presentationModule.Coverage)
	return presentationModule, nil
}

func selectedPresentationFacts(moduleID string, selections []index.SelectionPointer, factsByID map[string]index.FactRecord) ([]index.FactRecord, []string, string, error) {
	receiptIDs := make([]string, 0, len(selections))
	factsBySelectedID := make(map[string]index.FactRecord)
	selectedSources := make(map[string]struct{})
	for _, selection := range selections {
		receiptIDs = append(receiptIDs, selection.SelectionID)
		if selection.SourceSHA != nil && selection.AnalysisID != nil {
			selectedSources[*selection.SourceSHA] = struct{}{}
		}
		if selection.AnalysisID == nil || selection.FactsID == nil {
			continue
		}
		facts, found := factsByID[*selection.FactsID]
		if !found || facts.ModuleID != moduleID {
			return nil, nil, "", fmt.Errorf("presentation selection for %s references missing facts", moduleID)
		}
		factsBySelectedID[facts.ID] = facts
	}
	facts := make([]index.FactRecord, 0, len(factsBySelectedID))
	for _, fact := range factsBySelectedID {
		facts = append(facts, fact)
	}
	sort.Slice(facts, func(left, right int) bool { return facts[left].ID < facts[right].ID })
	receiptIDs, err := sortedPresentationReceipts(receiptIDs)
	if err != nil {
		return nil, nil, "", err
	}
	selectedSource := ""
	if len(selectedSources) == 1 {
		for sourceSHA := range selectedSources {
			selectedSource = sourceSHA
		}
	}
	return facts, receiptIDs, selectedSource, nil
}

func presentationModuleReason(lifecycle, desiredSourceSHA string, facts []index.FactRecord, rawCoverage json.RawMessage) string {
	if lifecycle == "retired" {
		return "retired"
	}
	if len(facts) == 0 {
		if desiredSourceSHA != "" {
			return "analysis-not-selected"
		}
		return "never-analyzed"
	}
	var coverage presentationCoverage
	if err := json.Unmarshal(rawCoverage, &coverage); err != nil || !coverage.Complete {
		return "coverage-incomplete"
	}
	return ""
}

func emptyPresentationCoverage(asOf time.Time, reason string) presentationCoverage {
	return presentationCoverage{Capabilities: map[string]string{}, Complete: false, ObservedAt: asOf.UTC().Format(time.RFC3339Nano), Reason: reason}
}

func mergePresentationCoverage(facts []index.FactRecord, asOf time.Time) presentationCoverage {
	coverage := emptyPresentationCoverage(asOf, "one-or-more-facts-capabilities-incomplete")
	coverage.Complete = len(facts) > 0
	for _, fact := range facts {
		mergeFactCoverage(&coverage, fact)
	}
	if coverage.Complete {
		coverage.Reason = ""
	}
	return coverage
}

func mergeFactCoverage(coverage *presentationCoverage, fact index.FactRecord) {
	if !fact.FactsCoverageComplete || len(fact.FactsCoverage) == 0 {
		coverage.Complete = false
	}
	if fact.FactsCoverageObservedAt > coverage.ObservedAt {
		coverage.ObservedAt = fact.FactsCoverageObservedAt
	}
	for capability, status := range fact.FactsCoverage {
		previous, exists := coverage.Capabilities[capability]
		if !exists || presentationCoverageRank(status) > presentationCoverageRank(previous) {
			coverage.Capabilities[capability] = status
		}
		if status != "complete" {
			coverage.Complete = false
		}
	}
}

func presentationCoverageRank(status string) int {
	switch status {
	case "unknown":
		return 3
	case "unavailable":
		return 2
	case "partial":
		return 1
	default:
		return 0
	}
}

func marshalPresentationCoverage(coverage presentationCoverage) json.RawMessage {
	data, _ := json.Marshal(coverage)
	return data
}

func sortedPresentationReceipts(receiptIDs []string) ([]string, error) {
	sort.Strings(receiptIDs)
	for position := 1; position < len(receiptIDs); position++ {
		if receiptIDs[position-1] == receiptIDs[position] {
			return nil, fmt.Errorf("presentation contains duplicate selection receipt %s", receiptIDs[position])
		}
	}
	return receiptIDs, nil
}
