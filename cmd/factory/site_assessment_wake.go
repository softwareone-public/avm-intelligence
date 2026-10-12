package main

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/assessment"
	"github.com/softwareone-public/avm-intelligence/internal/contextset"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/provider"
	"github.com/softwareone-public/avm-intelligence/internal/queue"
	"github.com/softwareone-public/avm-intelligence/internal/state"
	"github.com/softwareone-public/avm-intelligence/internal/terraform"
)

type acceptedAssessmentCycle struct {
	Requests []queue.Request
	Deferred []queue.AssessmentWakeDeferred
}

type moduleReleaseIdentity struct {
	module  string
	release string
}

type providerSnapshotIdentity struct {
	address string
	id      string
}

// planAcceptedSiteAssessments derives only current, receipt-backed contexts
// whose exact imported provider snapshots exist in this accepted data tree.
func planAcceptedSiteAssessments(dataRoot string, snapshot state.Snapshot, now time.Time, moduleIDs []string) (acceptedAssessmentCycle, error) {
	if len(moduleIDs) == 0 {
		return acceptedAssessmentCycle{}, fmt.Errorf("site assessment planning requires an exact non-empty module scope")
	}
	allowed := moduleIDSet(moduleIDs)
	records, err := index.ReadRecords(dataRoot, time.Time{})
	if err != nil {
		return acceptedAssessmentCycle{}, assessmentWakeFailureAt("read_accepted_data", "", err)
	}
	indexes, err := index.Build(records)
	if err != nil {
		return acceptedAssessmentCycle{}, assessmentWakeFailureAt("validate_accepted_data", "", err)
	}
	activeModules := make(map[string]struct{})
	for _, module := range indexes.Catalog.Modules {
		if module.Lifecycle == "active" {
			activeModules[module.ModuleID] = struct{}{}
		}
	}
	providerSnapshots := make(map[providerSnapshotIdentity]index.ProviderSnapshotRecord, len(records.ProviderSnapshots))
	verifiedSnapshots := make(map[providerSnapshotIdentity]bool, len(records.ProviderSnapshots))
	for _, record := range records.ProviderSnapshots {
		providerSnapshots[providerSnapshotIdentity{address: record.Address, id: record.ID}] = record
	}
	dimensionIDs, err := assessmentDimensionDefinitionIDs()
	if err != nil {
		return acceptedAssessmentCycle{}, assessmentWakeFailureAt("load_assessment_definitions", "", err)
	}
	receipts, err := state.AcceptedFactsContextSets(snapshot, "")
	if err != nil {
		return acceptedAssessmentCycle{}, assessmentWakeFailureAt("read_accepted_facts_receipts", "", err)
	}
	current := make(map[moduleReleaseIdentity]state.EvidenceAcceptanceReceipt)
	for _, receipt := range receipts {
		if _, inScope := allowed[receipt.ModuleID]; !inScope {
			continue
		}
		if _, active := activeModules[receipt.ModuleID]; !active {
			continue
		}
		pinned, pinErr := state.CurrentSourcePinMatches(snapshot, receipt.ModuleID, receipt.AVMReleaseVersion, receipt.SourceSHA)
		if pinErr != nil {
			return acceptedAssessmentCycle{}, assessmentWakeFailureAt("verify_current_source_pin", receipt.ModuleID, pinErr)
		}
		if !pinned {
			continue
		}
		key := moduleReleaseIdentity{module: receipt.ModuleID, release: receipt.AVMReleaseVersion}
		previous, exists := current[key]
		if exists && previous.DesiredGeneration == receipt.DesiredGeneration && (previous.FactsID != receipt.FactsID || previous.RequiredContexts == nil || receipt.RequiredContexts == nil || previous.RequiredContexts.ID != receipt.RequiredContexts.ID) {
			return acceptedAssessmentCycle{}, assessmentWakeFailureAt("reconcile_current_facts_receipts", receipt.ModuleID, fmt.Errorf("current facts receipts conflict"))
		}
		if !exists || receipt.DesiredGeneration > previous.DesiredGeneration {
			current[key] = receipt
		}
	}
	keys := make([]moduleReleaseIdentity, 0, len(current))
	for key := range current {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].module != keys[right].module {
			return keys[left].module < keys[right].module
		}
		return keys[left].release < keys[right].release
	})
	cycle := acceptedAssessmentCycle{Requests: []queue.Request{}, Deferred: []queue.AssessmentWakeDeferred{}}
	for _, key := range keys {
		receipt := current[key]
		set := receipt.RequiredContexts
		if set == nil || set.Validate() != nil || set.ModuleID != receipt.ModuleID || set.SourceSHA != receipt.SourceSHA || set.AVMReleaseVersion != receipt.AVMReleaseVersion || set.FactsID != receipt.FactsID {
			return acceptedAssessmentCycle{}, assessmentWakeFailureAt("validate_accepted_context_set", receipt.ModuleID, fmt.Errorf("accepted facts receipt has an invalid context set"))
		}
		facts, factsErr := provider.LoadAcceptedModuleFacts(dataRoot, receipt.ModuleID, receipt.SourceSHA, receipt.FactsID)
		if factsErr != nil {
			return acceptedAssessmentCycle{}, assessmentWakeFailureAt("verify_accepted_facts", receipt.ModuleID, factsErr)
		}
		modulePlan, planErr := planAssessmentReceiptContexts(dataRoot, snapshot, now, receipt, set, facts, dimensionIDs, providerSnapshots, verifiedSnapshots)
		if planErr != nil {
			return acceptedAssessmentCycle{}, assessmentWakeFailureAt("plan_receipt_contexts", receipt.ModuleID, planErr)
		}
		cycle.Requests = append(cycle.Requests, modulePlan.Requests...)
		cycle.Deferred = append(cycle.Deferred, modulePlan.Deferred...)
	}
	return cycle, nil
}

func planAssessmentReceiptContexts(dataRoot string, snapshot state.Snapshot, now time.Time, receipt state.EvidenceAcceptanceReceipt, set *contextset.Set, facts terraform.Facts, dimensionIDs []string, providerSnapshots map[providerSnapshotIdentity]index.ProviderSnapshotRecord, verifiedSnapshots map[providerSnapshotIdentity]bool) (queue.AssessmentWakePlan, error) {
	contexts := make([]queue.AssessmentWakeContext, 0, len(set.Contexts))
	for _, required := range set.Contexts {
		contextState, err := state.AssessmentContextInputs(snapshot, receipt.ModuleID, receipt.AVMReleaseVersion, required.State.ContextID)
		if err != nil {
			return queue.AssessmentWakePlan{}, assessmentWakeFailureAt("read_context_inputs", receipt.ModuleID, err)
		}
		wakeContext := queue.AssessmentWakeContext{
			State: queue.TrackState{
				ContextID: required.State.ContextID, Track: required.State.Track,
				Applicability: queue.Applicability(required.State.Applicability), Candidate: queue.CandidateState(required.State.Candidate),
			},
			Input: queue.AssessmentInput{
				ModuleID: receipt.ModuleID, SourceSHA: receipt.SourceSHA, AVMReleaseVersion: receipt.AVMReleaseVersion,
				ContextID: required.State.ContextID, Track: required.State.Track,
				DesiredGeneration: contextState.DesiredGeneration, DefinitionIDs: append([]string(nil), dimensionIDs...),
				FactsID: receipt.FactsID, SnapshotIDs: []string{}, TargetVersions: map[string]string{},
			},
			PrimaryProviderAddress: required.ProviderAddress, PrimaryProviderMajor: required.ProviderMajor,
			AcceptedSnapshots: []queue.AcceptedProviderSnapshotPin{},
		}
		for _, input := range contextState.Inputs {
			if input.Kind == state.InputAssessmentPolicy && input.InputID != set.PolicyID {
				wakeContext.DeferredReason = "imported provider policy does not match the accepted facts context set"
				break
			}
			if input.Kind != state.InputProviderSnapshot {
				continue
			}
			snapshotIdentity := providerSnapshotIdentity{address: input.InputKey, id: input.InputID}
			indexed, exists := providerSnapshots[snapshotIdentity]
			if !exists {
				wakeContext.DeferredReason = "imported provider snapshot is absent from the exact accepted data tree"
				break
			}
			if !verifiedSnapshots[snapshotIdentity] {
				if _, loadErr := provider.LoadAcceptedProviderSnapshot(dataRoot, indexed.Address, indexed.Version, indexed.ID); loadErr != nil {
					wakeContext.DeferredReason = "imported provider snapshot failed exact accepted-index validation"
					break
				}
				verifiedSnapshots[snapshotIdentity] = true
			}
			wakeContext.Input.SnapshotIDs = append(wakeContext.Input.SnapshotIDs, indexed.ID)
			wakeContext.Input.TargetVersions[indexed.Address] = indexed.Version
			wakeContext.AcceptedSnapshots = append(wakeContext.AcceptedSnapshots, queue.AcceptedProviderSnapshotPin{
				ID: indexed.ID, ProviderAddress: indexed.Address, ProviderVersion: indexed.Version,
			})
		}
		contexts = append(contexts, wakeContext)
	}
	plan, err := queue.PlanAssessmentWake(receipt.FactsID, receipt.ModuleID, facts.SourceSHA, receipt.AVMReleaseVersion, contexts, now)
	if err != nil {
		var contextFailure *queue.AssessmentWakeContextError
		if errors.As(err, &contextFailure) {
			return queue.AssessmentWakePlan{}, assessmentWakeFailureAt("validate_context_targets", receipt.ModuleID, err, contextFailure.ContextID)
		}
		return queue.AssessmentWakePlan{}, assessmentWakeFailureAt("validate_context_targets", receipt.ModuleID, err)
	}
	return plan, nil
}

func assessmentDimensionDefinitionIDs() ([]string, error) {
	registry, err := assessment.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	definitions := registry.ListDimensions()
	ids := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		ids = append(ids, definition.DefinitionID)
	}
	return ids, nil
}
