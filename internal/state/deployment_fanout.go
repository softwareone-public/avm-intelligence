package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/contextset"
	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
	"github.com/softwareone-public/avm-intelligence/internal/projection"
)

const modulePublicationAckSchemaVersion = 1

// ModuleDeploymentAcknowledgement is the module-owned proof that one exact
// AVM release was included in a successfully verified Pages deployment.
type ModuleDeploymentAcknowledgement struct {
	SchemaVersion        int                      `json:"schema_version"`
	RecordType           string                   `json:"record_type"`
	ID                   string                   `json:"acknowledgement_id"`
	ModuleID             string                   `json:"module_id"`
	AVMReleaseVersion    string                   `json:"avm_release_version"`
	DeploymentReceiptID  string                   `json:"deployment_receipt_id"`
	DeploymentGeneration uint64                   `json:"deployment_generation"`
	AcceptedCommit       string                   `json:"accepted_commit"`
	DatasetID            string                   `json:"dataset_id"`
	MembershipDigest     string                   `json:"membership_digest"`
	Membership           []index.ModuleMembership `json:"module_membership"`
	AcknowledgedAt       time.Time                `json:"acknowledged_at"`
}

type moduleLifecycleSlot struct {
	SchemaVersion     int                          `json:"schema_version"`
	ModuleID          string                       `json:"module_id"`
	ReleaseVersion    string                       `json:"release_version"`
	DesiredGeneration uint64                       `json:"desired_generation"`
	RequestID         string                       `json:"request_id"`
	AcquiredAt        time.Time                    `json:"acquired_at"`
	Status            string                       `json:"status"`
	ReleaseProof      *moduleLifecycleReleaseProof `json:"release_proof,omitempty"`
}

type moduleLifecycleReleaseProof struct {
	DeploymentReceiptID string    `json:"deployment_receipt_id"`
	MembershipDigest    string    `json:"membership_digest"`
	VerifiedAt          time.Time `json:"verified_at"`
}

type modulePublicationAckResult struct {
	Acknowledgement ModuleDeploymentAcknowledgement `json:"acknowledgement"`
	Events          []Event                         `json:"events"`
}

// AcknowledgeModuleDeployment writes a replayable module-owned acknowledgement
// only after a successful deployment receipt proves exact current membership.
// The same transaction releases the AVM-version slot only when every required
// assessment context is selected and included in that deployment.
func AcknowledgeModuleDeployment(ctx context.Context, coordinator TransactionCoordinator, moduleID, avmReleaseVersion string) (ModuleDeploymentAcknowledgement, error) {
	if coordinator == nil {
		return ModuleDeploymentAcknowledgement{}, fmt.Errorf("state transaction coordinator is required")
	}
	operationCoordinator, ok := coordinator.(interface {
		RunOperationTransaction(context.Context, string, string, []Event, TransactionDecisionFunc, TransactionResultResolver) (TransactionResult, error)
	})
	if !ok {
		return ModuleDeploymentAcknowledgement{}, fmt.Errorf("module deployment fan-out requires an operation-receipt coordinator")
	}
	if !selectionModulePattern.MatchString(moduleID) || !selectionReleasePattern.MatchString(avmReleaseVersion) {
		return ModuleDeploymentAcknowledgement{}, fmt.Errorf("module publication acknowledgement identity is invalid")
	}
	_, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, err
	}
	if replay, found, replayErr := moduleDeploymentAckReplay(snapshot, moduleID, avmReleaseVersion); replayErr != nil || found {
		return replay, replayErr
	}
	acknowledgement, expected, err := moduleDeploymentAckEvents(snapshot, moduleID, avmReleaseVersion)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, err
	}
	operationID, err := moduleDeploymentAckOperationID(acknowledgement)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, err
	}
	result, err := operationCoordinator.RunOperationTransaction(ctx, moduleID, operationID, expected, func(current Snapshot) (TransactionDecision, error) {
		currentAcknowledgement, events, buildErr := moduleDeploymentAckEvents(current, moduleID, avmReleaseVersion)
		if buildErr != nil {
			return TransactionDecision{}, buildErr
		}
		if currentAcknowledgement.ID != acknowledgement.ID {
			return TransactionDecision{}, fmt.Errorf("module deployment acknowledgement identity changed during transaction planning: %w", ErrTransactionIdentityConflict)
		}
		if !sameEventSet(events, expected) {
			path, detail := firstEventSetDifference(events, expected)
			return TransactionDecision{}, fmt.Errorf("module deployment acknowledgement write set changed at %s (%s): %w", path, detail, ErrTransactionIdentityConflict)
		}
		return TransactionDecision{
			ReadSet: TransactionReadSet{Owners: []string{moduleID}}, Events: expected,
			Result:    modulePublicationAckResult{Acknowledgement: currentAcknowledgement, Events: expected},
			Candidate: CandidateOptions{Subject: "state(publication): acknowledge module deployment", Provenance: GitProvenance{Operation: "publication.pages.acknowledge", Event: operationID, Module: moduleID}},
		}, nil
	}, func(current Snapshot) (TransactionResolution, error) {
		path, pathErr := identity.StateModulePublicationAcknowledgementPath(moduleID, acknowledgement.ID)
		if pathErr != nil {
			return TransactionResolution{}, pathErr
		}
		record, exists := current.Records[path]
		if !exists || record.Kind != "module_publication_ack" || record.SchemaVersion != modulePublicationAckSchemaVersion {
			return TransactionResolution{}, nil
		}
		var stored ModuleDeploymentAcknowledgement
		if decodeErr := json.Unmarshal(record.Data, &stored); decodeErr != nil || !sameModuleDeploymentAcknowledgement(stored, acknowledgement) {
			return TransactionResolution{}, ErrTransactionIdentityConflict
		}
		return TransactionResolution{Found: true, Value: modulePublicationAckResult{Acknowledgement: stored, Events: expected}}, nil
	})
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, err
	}
	acknowledged, ok := result.Value.(modulePublicationAckResult)
	if !ok {
		if raw, rawOK := result.Value.(json.RawMessage); rawOK {
			if json.Unmarshal(raw, &acknowledged) != nil {
				return ModuleDeploymentAcknowledgement{}, fmt.Errorf("module publication acknowledgement result is invalid")
			}
		} else {
			return ModuleDeploymentAcknowledgement{}, fmt.Errorf("module publication acknowledgement result has an invalid type")
		}
	}
	return acknowledged.Acknowledgement, nil
}

func firstEventSetDifference(left, right []Event) (string, string) {
	leftByID := make(map[string]Event, len(left))
	rightByID := make(map[string]Event, len(right))
	for _, event := range left {
		leftByID[event.ID] = event
	}
	for _, event := range right {
		rightByID[event.ID] = event
	}
	for id, expected := range rightByID {
		actual, exists := leftByID[id]
		actualDigest, actualErr := transactionEventDigest(actual)
		expectedDigest, expectedErr := transactionEventDigest(expected)
		if !exists || actualErr != nil || expectedErr != nil || actualDigest != expectedDigest {
			return expected.RecordPath, fmt.Sprintf("actual=%q expected=%q actual_error=%v expected_error=%v", actualDigest, expectedDigest, actualErr, expectedErr)
		}
	}
	for id, actual := range leftByID {
		if _, exists := rightByID[id]; !exists {
			return actual.RecordPath, "event exists only in recomputed write set"
		}
	}
	return "unknown", "unknown write-set difference"
}

func moduleDeploymentAckEvents(snapshot Snapshot, moduleID, avmReleaseVersion string) (ModuleDeploymentAcknowledgement, []Event, error) {
	deploymentState, err := readDeploymentState(snapshot)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, nil, err
	}
	var receipt DeploymentReceipt
	for _, candidate := range deploymentState.Receipts {
		if candidate.Generation != deploymentState.ActiveGeneration || candidate.Status != DeploymentReceiptActive || !candidate.Succeeded {
			continue
		}
		position := deploymentPosition(deploymentState, candidate.Generation)
		if position < 0 || deploymentState.Generations[position].Status != DeploymentDeployed || !matchesDeployment(deploymentState.Generations[position], candidate) {
			return ModuleDeploymentAcknowledgement{}, nil, ErrDeploymentNotCurrent
		}
		receipt = candidate
		break
	}
	if receipt.RunID == "" || deploymentState.ActiveGeneration == 0 || deploymentState.ActiveGeneration != deploymentState.HighWater {
		return ModuleDeploymentAcknowledgement{}, nil, ErrDeploymentNotCurrent
	}
	receiptID, err := deploymentReceiptIdentity(receipt)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, nil, err
	}
	membership := make([]index.ModuleMembership, 0)
	for _, member := range receipt.Membership {
		if member.ModuleID == moduleID && member.AVMReleaseVersion == avmReleaseVersion {
			membership = append(membership, member)
		}
	}
	if len(membership) == 0 {
		return ModuleDeploymentAcknowledgement{}, nil, ErrDeploymentNotCurrent
	}
	sort.Slice(membership, func(left, right int) bool { return membershipKey(membership[left]) < membershipKey(membership[right]) })
	if err = validateModuleDeploymentSelections(snapshot, moduleID, avmReleaseVersion, membership); err != nil {
		return ModuleDeploymentAcknowledgement{}, nil, err
	}
	membershipDigest, err := moduleMembershipDigest(membership)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, nil, err
	}
	acknowledgement := ModuleDeploymentAcknowledgement{
		SchemaVersion: modulePublicationAckSchemaVersion, RecordType: "module_publication_ack",
		ModuleID: moduleID, AVMReleaseVersion: avmReleaseVersion, DeploymentReceiptID: receiptID,
		DeploymentGeneration: receipt.Generation, AcceptedCommit: receipt.AcceptedCommit,
		DatasetID: receipt.DatasetID, MembershipDigest: membershipDigest,
		Membership: membership, AcknowledgedAt: receipt.DeployedAt.UTC(),
	}
	acknowledgement.ID, err = moduleDeploymentAckID(acknowledgement)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, nil, err
	}
	if err = validateModulePublicationAcknowledgement(acknowledgement); err != nil {
		return ModuleDeploymentAcknowledgement{}, nil, err
	}
	ackPath, err := identity.StateModulePublicationAcknowledgementPath(moduleID, acknowledgement.ID)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, nil, err
	}
	ackData, err := json.Marshal(acknowledgement)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, nil, err
	}
	ackEvent := Event{ID: acknowledgement.ID, RecordPath: ackPath, Record: Record{Kind: "module_publication_ack", SchemaVersion: modulePublicationAckSchemaVersion, Data: ackData}}

	slotPath, err := identity.StateLifecycleSlotPath(moduleID)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, nil, err
	}
	slotRecord, exists := snapshot.Records[slotPath]
	if !exists || slotRecord.Kind != "lifecycle_slot" || slotRecord.SchemaVersion != 1 {
		return ModuleDeploymentAcknowledgement{}, nil, ErrDeploymentNotCurrent
	}
	var slot moduleLifecycleSlot
	if err = json.Unmarshal(slotRecord.Data, &slot); err != nil || slot.SchemaVersion != 1 || slot.ModuleID != moduleID || slot.ReleaseVersion != avmReleaseVersion || slot.Status != "active" || slot.DesiredGeneration == 0 {
		return ModuleDeploymentAcknowledgement{}, nil, ErrDeploymentNotCurrent
	}
	operationID, err := moduleDeploymentAckOperationID(acknowledgement)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, nil, err
	}
	intentEvent, complete, err := modulePublicationProjectionEvent(snapshot, acknowledgement, slot.DesiredGeneration, membership)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, nil, err
	}
	events := []Event{ackEvent}
	if complete {
		slot.Status = "released"
		slot.ReleaseProof = &moduleLifecycleReleaseProof{DeploymentReceiptID: receiptID, MembershipDigest: membershipDigest, VerifiedAt: receipt.DeployedAt.UTC()}
		if err = validateModuleLifecycleSlot(slot); err != nil {
			return ModuleDeploymentAcknowledgement{}, nil, err
		}
		slotData, marshalErr := json.Marshal(slot)
		if marshalErr != nil {
			return ModuleDeploymentAcknowledgement{}, nil, marshalErr
		}
		events = append(events, Event{ID: operationID + ".slot", RecordPath: slotPath, Record: Record{Kind: "lifecycle_slot", SchemaVersion: 1, Data: slotData}})
	}
	events = append(events, intentEvent)
	return acknowledgement, events, nil
}

func modulePublicationProjectionEvent(snapshot Snapshot, acknowledgement ModuleDeploymentAcknowledgement, desiredGeneration uint64, membership []index.ModuleMembership) (Event, bool, error) {
	var required, unresolved, selected, published int
	var current SelectionReceipt
	for _, member := range membership {
		if member.SelectionID == nil || member.AnalysisID == nil {
			continue
		}
		var readErr error
		current, readErr = readSelectionReceipt(snapshot, member.ModuleID, member.ContextID, *member.SelectionID)
		if readErr != nil || current.AnalysisID == nil || *current.AnalysisID != *member.AnalysisID {
			if readErr != nil {
				return Event{}, false, readErr
			}
			return Event{}, false, ErrDeploymentNotCurrent
		}
		break
	}
	status := "done"
	var sourceSHA string
	if current.ID == "" {
		facts, requiredCount, proofErr := exactRetiredMembershipProof(snapshot, acknowledgement.ModuleID, acknowledgement.AVMReleaseVersion, membership)
		if proofErr != nil {
			return Event{}, false, proofErr
		}
		status, sourceSHA, required = "retired", facts.SourceSHA, requiredCount
	} else {
		request, err := latestCurrentAssessment(snapshot, acknowledgement.ModuleID, acknowledgement.AVMReleaseVersion, current.ContextID)
		if err != nil {
			return Event{}, false, err
		}
		set, err := acceptedRequiredContextSet(snapshot, request)
		if err != nil {
			return Event{}, false, err
		}
		selected, required, unresolved, err = selectedContextCoverage(snapshot, set, current)
		if err != nil {
			return Event{}, false, err
		}
		publishedContexts := make(map[string]struct{}, len(membership))
		for _, member := range membership {
			if member.SelectionID != nil && member.AnalysisID != nil {
				publishedContexts[member.ContextID] = struct{}{}
			}
		}
		for _, context := range set.Contexts {
			if context.State.Applicability == contextset.ApplicabilityNotApplicable || context.State.Candidate == contextset.CandidateIneligible {
				continue
			}
			if _, exists := publishedContexts[context.State.ContextID]; exists {
				published++
			}
		}
		if required == 0 || selected < required || published < required || unresolved != 0 {
			status = "partial"
		}
		if current.SourceSHA == nil || *current.SourceSHA == "" {
			return Event{}, false, ErrDeploymentNotCurrent
		}
		sourceSHA = *current.SourceSHA
	}
	fields := []projection.OwnedField{
		{Name: "avm_release_version", Value: acknowledgement.AVMReleaseVersion},
		{Name: "source_version", Value: acknowledgement.AVMReleaseVersion},
		{Name: "dataset_id", Value: acknowledgement.DatasetID},
		{Name: "deployment_generation", Value: fmt.Sprint(acknowledgement.DeploymentGeneration)},
		{Name: "deployment_receipt_id", Value: acknowledgement.DeploymentReceiptID},
		{Name: "membership_digest", Value: acknowledgement.MembershipDigest},
		{Name: "selected_contexts", Value: fmt.Sprint(selected)},
		{Name: "published_contexts", Value: fmt.Sprint(published)},
		{Name: "required_contexts", Value: fmt.Sprint(required)},
		{Name: "unresolved_contexts", Value: fmt.Sprint(unresolved)},
	}
	fields = append(fields, projection.OwnedField{Name: "source_sha", Value: sourceSHA})
	if status == "done" {
		fields = append(fields, projection.OwnedField{Name: "published_commit", Value: acknowledgement.AcceptedCommit})
	}
	projectionGeneration, err := ModuleProjectionGeneration(snapshot, acknowledgement.ModuleID, desiredGeneration)
	if err != nil {
		return Event{}, false, err
	}
	intent, err := projection.NewDesiredIntent(acknowledgement.ModuleID, projectionGeneration, "pages:"+acknowledgement.ID, status, fields, nil)
	if err != nil {
		return Event{}, false, err
	}
	path, err := identity.StateProjectionIntentPath(acknowledgement.ModuleID)
	if err != nil {
		return Event{}, false, err
	}
	if record, exists := snapshot.Records[path]; exists {
		var currentIntent projection.DesiredIntent
		if record.Kind != "projection_intent" || json.Unmarshal(record.Data, &currentIntent) != nil || currentIntent.ValidatePath(path) != nil || currentIntent.Generation > intent.Generation {
			return Event{}, false, ErrSelectionStale
		}
	}
	data, err := json.Marshal(intent)
	if err != nil {
		return Event{}, false, err
	}
	return Event{ID: intent.IntentID, RecordPath: path, Record: Record{Kind: "projection_intent", SchemaVersion: projection.SchemaVersion, Data: data}}, status == "done" || status == "retired", nil
}

// exactRetiredMembershipProof binds an all-deselected Pages membership to the
// exact accepted acquire-source receipt and every required context in its
// reviewed set. It proves terminal catalog retirement, not assessment success.
func exactRetiredMembershipProof(snapshot Snapshot, moduleID, avmReleaseVersion string, membership []index.ModuleMembership) (EvidenceAcceptanceReceipt, int, error) {
	if validateRetiredCatalogModule(snapshot, moduleID) != nil || len(membership) == 0 {
		return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
	}
	slotPath, err := identity.StateLifecycleSlotPath(moduleID)
	if err != nil {
		return EvidenceAcceptanceReceipt{}, 0, err
	}
	slotRecord, exists := snapshot.Records[slotPath]
	var slot moduleLifecycleSlot
	if !exists || slotRecord.Kind != "lifecycle_slot" || slotRecord.SchemaVersion != 1 || json.Unmarshal(slotRecord.Data, &slot) != nil || slot.SchemaVersion != 1 || slot.ModuleID != moduleID || slot.ReleaseVersion != avmReleaseVersion || (slot.Status != "active" && slot.Status != "released") {
		return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
	}
	requestPath := "state/requests/" + slot.RequestID + ".json"
	requestRecord, exists := snapshot.Records[requestPath]
	var request selectionRequest
	if !exists || requestRecord.Kind != "request" || requestRecord.SchemaVersion != 1 || json.Unmarshal(requestRecord.Data, &request) != nil || request.ID != slot.RequestID || request.ModuleID != moduleID || request.Status != "accepted" || request.Work == nil || (request.Work.Phase != "acquire_source" && request.Work.Phase != "assess") || request.Work.AVMReleaseVersion != avmReleaseVersion || request.Work.FactsID == "" || request.SourceSHA == "" {
		return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
	}
	receipts, err := AcceptedFactsContextSets(snapshot, moduleID)
	if err != nil {
		return EvidenceAcceptanceReceipt{}, 0, err
	}
	var facts *EvidenceAcceptanceReceipt
	for i := range receipts {
		candidate := receipts[i]
		requestMatches := candidate.RequestID == slot.RequestID || request.Work.Phase == "assess"
		if !requestMatches || candidate.AVMReleaseVersion != avmReleaseVersion || candidate.SourceSHA != request.SourceSHA || candidate.FactsID != request.Work.FactsID {
			continue
		}
		if facts != nil {
			return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
		}
		copy := candidate
		facts = &copy
	}
	if facts == nil || facts.RequiredContexts == nil {
		return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
	}
	set := *facts.RequiredContexts
	if set.Validate() != nil || !set.Complete || set.ModuleID != moduleID || set.AVMReleaseVersion != avmReleaseVersion || set.SourceSHA != facts.SourceSHA || set.FactsID != facts.FactsID {
		return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
	}
	byContext := make(map[string]index.ModuleMembership, len(membership))
	for _, member := range membership {
		if member.ModuleID != moduleID || member.AVMReleaseVersion != avmReleaseVersion || member.SelectionID == nil || member.AnalysisID != nil {
			return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
		}
		if _, duplicate := byContext[member.ContextID]; duplicate {
			return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
		}
		knownContext := false
		for _, context := range set.Contexts {
			if context.State.ContextID == member.ContextID {
				knownContext = true
				break
			}
		}
		if !knownContext {
			return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
		}
		pointer, exists, readErr := readSelectionPointer(snapshot, selectionPointerPath(moduleID, avmReleaseVersion, member.ContextID))
		if readErr != nil || !exists || pointer.SelectionID != *member.SelectionID || pointer.AVMReleaseVersion != avmReleaseVersion {
			return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
		}
		deselection, readErr := readSelectionReceipt(snapshot, moduleID, member.ContextID, *member.SelectionID)
		if readErr != nil || deselection.AVMReleaseVersion != avmReleaseVersion || deselection.AnalysisID != nil {
			return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
		}
		byContext[member.ContextID] = member
	}
	required := 0
	for _, context := range set.Contexts {
		if context.State.Applicability == contextset.ApplicabilityUnresolved || context.State.Candidate == contextset.CandidateUnresolved {
			return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
		}
		if context.State.Applicability == contextset.ApplicabilityNotApplicable || context.State.Candidate == contextset.CandidateIneligible {
			continue
		}
		required++
		if _, covered := byContext[context.State.ContextID]; !covered {
			return EvidenceAcceptanceReceipt{}, 0, ErrDeploymentNotCurrent
		}
	}
	return *facts, required, nil
}

func validateModuleDeploymentSelections(snapshot Snapshot, moduleID, avmReleaseVersion string, membership []index.ModuleMembership) error {
	for _, member := range membership {
		if member.SelectionID == nil {
			return ErrDeploymentNotCurrent
		}
		pointer, exists, err := readSelectionPointer(snapshot, selectionPointerPath(moduleID, avmReleaseVersion, member.ContextID))
		if err != nil || !exists || pointer.SelectionID != *member.SelectionID || pointer.AVMReleaseVersion != avmReleaseVersion {
			return ErrDeploymentNotCurrent
		}
		receipt, err := readSelectionReceipt(snapshot, moduleID, member.ContextID, *member.SelectionID)
		if err != nil || receipt.AVMReleaseVersion != avmReleaseVersion {
			return ErrDeploymentNotCurrent
		}
		if member.AnalysisID == nil {
			if receipt.AnalysisID != nil || validateRetiredCatalogModule(snapshot, moduleID) != nil {
				return ErrDeploymentNotCurrent
			}
			continue
		}
		if receipt.AnalysisID == nil || *receipt.AnalysisID != *member.AnalysisID {
			return ErrDeploymentNotCurrent
		}
		request, err := latestCurrentAssessment(snapshot, moduleID, avmReleaseVersion, member.ContextID)
		if err != nil || request.Status != "accepted" || request.ID != pointer.RequestID || request.Fingerprint != receipt.InputFingerprint || request.Work.DesiredGeneration != pointer.DesiredGeneration {
			return ErrDeploymentNotCurrent
		}
		if err = validateImportedInputs(snapshot, request); err != nil {
			return ErrDeploymentNotCurrent
		}
	}
	return nil
}

func deploymentReceiptIdentity(receipt DeploymentReceipt) (string, error) {
	return identity.ID("deployment-receipt", 1, receipt)
}

func moduleMembershipDigest(membership []index.ModuleMembership) (string, error) {
	encoded, err := json.Marshal(membership)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func moduleDeploymentAckID(acknowledgement ModuleDeploymentAcknowledgement) (string, error) {
	return identity.ID("module-publication-ack", modulePublicationAckSchemaVersion, struct {
		ModuleID             string `json:"module_id"`
		AVMReleaseVersion    string `json:"avm_release_version"`
		DeploymentReceiptID  string `json:"deployment_receipt_id"`
		DeploymentGeneration uint64 `json:"deployment_generation"`
		AcceptedCommit       string `json:"accepted_commit"`
		DatasetID            string `json:"dataset_id"`
		MembershipDigest     string `json:"membership_digest"`
	}{acknowledgement.ModuleID, acknowledgement.AVMReleaseVersion, acknowledgement.DeploymentReceiptID, acknowledgement.DeploymentGeneration, acknowledgement.AcceptedCommit, acknowledgement.DatasetID, acknowledgement.MembershipDigest})
}

func moduleDeploymentAckOperationID(acknowledgement ModuleDeploymentAcknowledgement) (string, error) {
	return identity.ID("module-publication-operation", 1, struct {
		ModuleID          string `json:"module_id"`
		AVMReleaseVersion string `json:"avm_release_version"`
		AcknowledgementID string `json:"acknowledgement_id"`
	}{acknowledgement.ModuleID, acknowledgement.AVMReleaseVersion, acknowledgement.ID})
}

func validateModulePublicationAcknowledgement(acknowledgement ModuleDeploymentAcknowledgement) error {
	if acknowledgement.SchemaVersion != modulePublicationAckSchemaVersion || acknowledgement.RecordType != "module_publication_ack" || identity.ValidateRecordID("module-publication-ack", acknowledgement.ID) != nil || !selectionModulePattern.MatchString(acknowledgement.ModuleID) || !selectionReleasePattern.MatchString(acknowledgement.AVMReleaseVersion) || identity.ValidateRecordID("deployment-receipt", acknowledgement.DeploymentReceiptID) != nil || acknowledgement.DeploymentGeneration == 0 || !deploymentSHA1Pattern.MatchString(acknowledgement.AcceptedCommit) || identity.ValidateRecordID("dataset", acknowledgement.DatasetID) != nil || !deploymentSHA256Pattern.MatchString(acknowledgement.MembershipDigest) || len(acknowledgement.Membership) == 0 || acknowledgement.AcknowledgedAt.IsZero() {
		return fmt.Errorf("module publication acknowledgement is invalid")
	}
	if digest, err := moduleMembershipDigest(acknowledgement.Membership); err != nil || digest != acknowledgement.MembershipDigest {
		return fmt.Errorf("module publication membership digest is invalid")
	}
	for i, member := range acknowledgement.Membership {
		if member.ModuleID != acknowledgement.ModuleID || member.AVMReleaseVersion != acknowledgement.AVMReleaseVersion || member.SelectionID == nil || (i > 0 && membershipKey(acknowledgement.Membership[i-1]) >= membershipKey(member)) {
			return fmt.Errorf("module publication acknowledgement membership is invalid")
		}
	}
	id, err := moduleDeploymentAckID(acknowledgement)
	if err != nil || id != acknowledgement.ID {
		return fmt.Errorf("module publication acknowledgement identity does not match its contents")
	}
	return nil
}

func validateModuleLifecycleSlot(slot moduleLifecycleSlot) error {
	requestFingerprint := strings.TrimPrefix(slot.RequestID, slot.ModuleID+"-")
	if slot.SchemaVersion != 1 || !selectionModulePattern.MatchString(slot.ModuleID) || !selectionReleasePattern.MatchString(slot.ReleaseVersion) || slot.DesiredGeneration == 0 || len(requestFingerprint) != 64 || !deploymentSHA256Pattern.MatchString(requestFingerprint) || slot.RequestID != slot.ModuleID+"-"+requestFingerprint || slot.AcquiredAt.IsZero() || slot.Status != "released" || slot.ReleaseProof == nil || identity.ValidateRecordID("deployment-receipt", slot.ReleaseProof.DeploymentReceiptID) != nil || !deploymentSHA256Pattern.MatchString(slot.ReleaseProof.MembershipDigest) || slot.ReleaseProof.VerifiedAt.IsZero() {
		return fmt.Errorf("released module lifecycle slot is invalid")
	}
	return nil
}

func membershipKey(member index.ModuleMembership) string {
	return member.ModuleID + "\x00" + member.AVMReleaseVersion + "\x00" + member.ContextID
}

func sameModuleDeploymentAcknowledgement(left, right ModuleDeploymentAcknowledgement) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

func decodeModuleDeploymentAcknowledgement(record Record, logicalPath, moduleID string) (ModuleDeploymentAcknowledgement, error) {
	if record.Kind != "module_publication_ack" || record.SchemaVersion != modulePublicationAckSchemaVersion {
		return ModuleDeploymentAcknowledgement{}, ErrDeploymentNotCurrent
	}
	var acknowledgement ModuleDeploymentAcknowledgement
	if err := json.Unmarshal(record.Data, &acknowledgement); err != nil {
		return ModuleDeploymentAcknowledgement{}, err
	}
	expectedPath, err := identity.StateModulePublicationAcknowledgementPath(moduleID, acknowledgement.ID)
	if err != nil || logicalPath != expectedPath || acknowledgement.ModuleID != moduleID {
		return ModuleDeploymentAcknowledgement{}, fmt.Errorf("module publication acknowledgement path does not match its owner")
	}
	if err = validateModulePublicationAcknowledgement(acknowledgement); err != nil {
		return ModuleDeploymentAcknowledgement{}, err
	}
	return acknowledgement, nil
}

// VerifyDoneModule proves the current active deployment included every member
// in a complete module acknowledgement and that the lifecycle slot was
// released by that exact receipt and membership digest.
func VerifyDoneModule(snapshot Snapshot, moduleID, avmReleaseVersion, acceptedCommit string) (ModuleDeploymentAcknowledgement, error) {
	for logicalPath, record := range snapshot.Records {
		if record.Kind != "module_publication_ack" {
			continue
		}
		acknowledgement, err := decodeModuleDeploymentAcknowledgement(record, logicalPath, moduleID)
		if err != nil {
			return ModuleDeploymentAcknowledgement{}, err
		}
		if acknowledgement.AVMReleaseVersion != avmReleaseVersion || acknowledgement.AcceptedCommit != acceptedCommit {
			continue
		}
		allDeselected := len(acknowledgement.Membership) != 0
		for _, member := range acknowledgement.Membership {
			if member.AnalysisID != nil {
				allDeselected = false
				break
			}
		}
		if allDeselected {
			continue
		}
		current := verifyCompleteDeploymentMembership(snapshot, acknowledgement)
		if !current {
			continue
		}
		slotPath, pathErr := identity.StateLifecycleSlotPath(moduleID)
		if pathErr != nil {
			return ModuleDeploymentAcknowledgement{}, pathErr
		}
		record, exists := snapshot.Records[slotPath]
		var slot moduleLifecycleSlot
		if !exists || record.Kind != "lifecycle_slot" || json.Unmarshal(record.Data, &slot) != nil || slot.Status != "released" || slot.ReleaseVersion != avmReleaseVersion || slot.ReleaseProof == nil || slot.ReleaseProof.DeploymentReceiptID != acknowledgement.DeploymentReceiptID || slot.ReleaseProof.MembershipDigest != acknowledgement.MembershipDigest {
			continue
		}
		return acknowledgement, nil
	}
	return ModuleDeploymentAcknowledgement{}, ErrDeploymentNotCurrent
}

// VerifyRetiredModule proves a current combined Pages receipt contains only
// exact null-selection membership for a retired module and that the slot was
// released from that same receipt. This proves terminal retirement only.
func VerifyRetiredModule(snapshot Snapshot, moduleID, avmReleaseVersion, sourceSHA, deploymentID, membershipDigest string) (ModuleDeploymentAcknowledgement, error) {
	if !selectionModulePattern.MatchString(moduleID) || !selectionReleasePattern.MatchString(avmReleaseVersion) || !selectionSHA1Pattern.MatchString(sourceSHA) || identity.ValidateRecordID("deployment-receipt", deploymentID) != nil || !deploymentSHA256Pattern.MatchString(membershipDigest) {
		return ModuleDeploymentAcknowledgement{}, ErrDeploymentNotCurrent
	}
	deploymentState, err := readDeploymentState(snapshot)
	if err != nil || deploymentState.ActiveGeneration == 0 || deploymentState.ActiveGeneration != deploymentState.HighWater {
		return ModuleDeploymentAcknowledgement{}, ErrDeploymentNotCurrent
	}
	var activeReceipt *DeploymentReceipt
	for i := range deploymentState.Receipts {
		receipt := &deploymentState.Receipts[i]
		if receipt.Generation == deploymentState.ActiveGeneration && receipt.Status == DeploymentReceiptActive && receipt.Succeeded {
			if activeReceipt != nil {
				return ModuleDeploymentAcknowledgement{}, ErrDeploymentNotCurrent
			}
			activeReceipt = receipt
		}
	}
	if activeReceipt == nil {
		return ModuleDeploymentAcknowledgement{}, ErrDeploymentNotCurrent
	}
	activeReceiptID, err := deploymentReceiptIdentity(*activeReceipt)
	if err != nil || activeReceiptID != deploymentID {
		return ModuleDeploymentAcknowledgement{}, ErrDeploymentNotCurrent
	}
	expectedMembership := make([]index.ModuleMembership, 0)
	for _, member := range activeReceipt.Membership {
		if member.ModuleID == moduleID && member.AVMReleaseVersion == avmReleaseVersion {
			expectedMembership = append(expectedMembership, member)
		}
	}
	sort.Slice(expectedMembership, func(left, right int) bool {
		return membershipKey(expectedMembership[left]) < membershipKey(expectedMembership[right])
	})
	expectedDigest, err := moduleMembershipDigest(expectedMembership)
	if err != nil || expectedDigest != membershipDigest || len(expectedMembership) == 0 {
		return ModuleDeploymentAcknowledgement{}, ErrDeploymentNotCurrent
	}
	for logicalPath, record := range snapshot.Records {
		if record.Kind != "module_publication_ack" {
			continue
		}
		acknowledgement, decodeErr := decodeModuleDeploymentAcknowledgement(record, logicalPath, moduleID)
		if decodeErr != nil {
			return ModuleDeploymentAcknowledgement{}, decodeErr
		}
		if acknowledgement.AVMReleaseVersion != avmReleaseVersion || acknowledgement.DeploymentReceiptID != deploymentID || acknowledgement.DeploymentGeneration != activeReceipt.Generation || acknowledgement.MembershipDigest != membershipDigest || acknowledgement.AcceptedCommit != activeReceipt.AcceptedCommit || acknowledgement.DatasetID != activeReceipt.DatasetID || !sameDeploymentMembership(acknowledgement.Membership, expectedMembership) {
			continue
		}
		allDeselected := len(acknowledgement.Membership) != 0
		for _, member := range acknowledgement.Membership {
			if member.AnalysisID != nil {
				allDeselected = false
				break
			}
		}
		if !allDeselected || !verifyCompleteDeploymentMembership(snapshot, acknowledgement) {
			continue
		}
		facts, _, proofErr := exactRetiredMembershipProof(snapshot, moduleID, avmReleaseVersion, acknowledgement.Membership)
		if proofErr != nil || facts.SourceSHA != sourceSHA {
			continue
		}
		slotPath, pathErr := identity.StateLifecycleSlotPath(moduleID)
		if pathErr != nil {
			return ModuleDeploymentAcknowledgement{}, pathErr
		}
		slotRecord, exists := snapshot.Records[slotPath]
		var slot moduleLifecycleSlot
		if !exists || slotRecord.Kind != "lifecycle_slot" || json.Unmarshal(slotRecord.Data, &slot) != nil || slot.Status != "released" || slot.ReleaseVersion != avmReleaseVersion || slot.ReleaseProof == nil || slot.ReleaseProof.DeploymentReceiptID != deploymentID || slot.ReleaseProof.MembershipDigest != membershipDigest {
			continue
		}
		return acknowledgement, nil
	}
	return ModuleDeploymentAcknowledgement{}, ErrDeploymentNotCurrent
}

func verifyCompleteDeploymentMembership(snapshot Snapshot, acknowledgement ModuleDeploymentAcknowledgement) bool {
	allDeselected := len(acknowledgement.Membership) != 0
	for _, member := range acknowledgement.Membership {
		if member.AnalysisID != nil {
			allDeselected = false
			break
		}
	}
	if allDeselected {
		_, _, err := exactRetiredMembershipProof(snapshot, acknowledgement.ModuleID, acknowledgement.AVMReleaseVersion, acknowledgement.Membership)
		return err == nil
	}
	var required contextset.Set
	var hasRequired bool
	for _, member := range acknowledgement.Membership {
		if member.AnalysisID == nil || member.SelectionID == nil {
			continue
		}
		selected, err := readSelectionReceipt(snapshot, acknowledgement.ModuleID, member.ContextID, *member.SelectionID)
		if err != nil || selected.AnalysisID == nil || *selected.AnalysisID != *member.AnalysisID {
			return false
		}
		request, err := latestCurrentAssessment(snapshot, acknowledgement.ModuleID, acknowledgement.AVMReleaseVersion, member.ContextID)
		if err != nil || request.Status != "accepted" || request.ID == "" {
			return false
		}
		required, err = acceptedRequiredContextSet(snapshot, request)
		if err != nil {
			return false
		}
		hasRequired = true
		break
	}
	if !hasRequired {
		return false
	}
	var currentSelection SelectionReceipt
	for _, member := range acknowledgement.Membership {
		if member.AnalysisID == nil || member.SelectionID == nil {
			continue
		}
		var err error
		currentSelection, err = readSelectionReceipt(snapshot, acknowledgement.ModuleID, member.ContextID, *member.SelectionID)
		if err != nil {
			return false
		}
		break
	}
	selected, requiredCount, unresolved, err := selectedContextCoverage(snapshot, required, currentSelection)
	if err != nil || selected != requiredCount || unresolved != 0 {
		return false
	}
	published := make(map[string]struct{}, len(acknowledgement.Membership))
	for _, member := range acknowledgement.Membership {
		if member.AnalysisID != nil {
			published[member.ContextID] = struct{}{}
		}
	}
	for _, item := range required.Contexts {
		if item.State.Applicability == contextset.ApplicabilityNotApplicable || item.State.Candidate == contextset.CandidateIneligible {
			continue
		}
		if _, ok := published[item.State.ContextID]; !ok {
			return false
		}
	}
	for _, member := range acknowledgement.Membership {
		if member.AnalysisID == nil {
			if member.SelectionID == nil || validateRetiredCatalogModule(snapshot, acknowledgement.ModuleID) != nil {
				return false
			}
			pointer, exists, err := readSelectionPointer(snapshot, selectionPointerPath(acknowledgement.ModuleID, acknowledgement.AVMReleaseVersion, member.ContextID))
			if err != nil || !exists || pointer.SelectionID != *member.SelectionID {
				return false
			}
			deselection, err := readSelectionReceipt(snapshot, acknowledgement.ModuleID, member.ContextID, *member.SelectionID)
			if err != nil || deselection.AnalysisID != nil || !contextIsExplicitlyNonRequired(required, member.ContextID) {
				return false
			}
			continue
		}
		verified, err := VerifyDoneSelection(snapshot, acknowledgement.ModuleID, acknowledgement.AVMReleaseVersion, member.ContextID)
		selectedReceipt, receiptErr := readSelectionReceipt(snapshot, acknowledgement.ModuleID, member.ContextID, *member.SelectionID)
		if err != nil || receiptErr != nil || verified.AcceptedCommit != acknowledgement.AcceptedCommit || selectedReceipt.AnalysisID == nil || *selectedReceipt.AnalysisID != *member.AnalysisID {
			return false
		}
	}
	return true
}

func contextIsExplicitlyNonRequired(set contextset.Set, contextID string) bool {
	for _, item := range set.Contexts {
		if item.State.ContextID != contextID {
			continue
		}
		return item.State.Applicability == contextset.ApplicabilityNotApplicable || item.State.Candidate == contextset.CandidateIneligible
	}
	return false
}

func moduleDeploymentAcknowledgementFor(snapshot Snapshot, receipt DeploymentReceipt, member index.ModuleMembership) (ModuleDeploymentAcknowledgement, error) {
	receiptID, err := deploymentReceiptIdentity(receipt)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, err
	}
	wantMembership := make([]index.ModuleMembership, 0)
	for _, candidate := range receipt.Membership {
		if candidate.ModuleID == member.ModuleID && candidate.AVMReleaseVersion == member.AVMReleaseVersion {
			wantMembership = append(wantMembership, candidate)
		}
	}
	sort.Slice(wantMembership, func(left, right int) bool {
		return membershipKey(wantMembership[left]) < membershipKey(wantMembership[right])
	})
	wantDigest, err := moduleMembershipDigest(wantMembership)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, err
	}
	var found *ModuleDeploymentAcknowledgement
	for logicalPath, record := range snapshot.Records {
		if record.Kind != "module_publication_ack" {
			continue
		}
		acknowledgement, decodeErr := decodeModuleDeploymentAcknowledgement(record, logicalPath, member.ModuleID)
		if decodeErr != nil {
			return ModuleDeploymentAcknowledgement{}, decodeErr
		}
		if acknowledgement.AVMReleaseVersion != member.AVMReleaseVersion || acknowledgement.DeploymentReceiptID != receiptID {
			continue
		}
		if acknowledgement.DeploymentGeneration != receipt.Generation || acknowledgement.DatasetID != receipt.DatasetID || acknowledgement.AcceptedCommit != receipt.AcceptedCommit || acknowledgement.MembershipDigest != wantDigest || !sameDeploymentMembership(acknowledgement.Membership, wantMembership) {
			return ModuleDeploymentAcknowledgement{}, ErrDeploymentNotCurrent
		}
		if found != nil {
			return ModuleDeploymentAcknowledgement{}, ErrDeploymentReceiptConflict
		}
		copy := acknowledgement
		found = &copy
	}
	if found == nil {
		return ModuleDeploymentAcknowledgement{}, ErrDeploymentNotCurrent
	}
	selected := false
	for _, candidate := range found.Membership {
		if candidate.ContextID == member.ContextID && sameDeploymentNullable(candidate.SelectionID, member.SelectionID) && sameDeploymentNullable(candidate.AnalysisID, member.AnalysisID) {
			selected = true
			break
		}
	}
	if !selected {
		return ModuleDeploymentAcknowledgement{}, ErrDeploymentNotCurrent
	}
	return *found, nil
}

func moduleDeploymentAckReplay(snapshot Snapshot, moduleID, avmReleaseVersion string) (ModuleDeploymentAcknowledgement, bool, error) {
	deploymentState, err := readDeploymentState(snapshot)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, false, err
	}
	var activeReceipt DeploymentReceipt
	for _, candidate := range deploymentState.Receipts {
		if candidate.Generation == deploymentState.ActiveGeneration && candidate.Status == DeploymentReceiptActive && candidate.Succeeded {
			activeReceipt = candidate
			break
		}
	}
	if activeReceipt.RunID == "" {
		return ModuleDeploymentAcknowledgement{}, false, nil
	}
	deploymentID, err := deploymentReceiptIdentity(activeReceipt)
	if err != nil {
		return ModuleDeploymentAcknowledgement{}, false, err
	}
	for logicalPath, record := range snapshot.Records {
		if record.Kind != "module_publication_ack" {
			continue
		}
		var owner struct {
			ModuleID string `json:"module_id"`
		}
		if err = json.Unmarshal(record.Data, &owner); err != nil {
			return ModuleDeploymentAcknowledgement{}, false, fmt.Errorf("decode module publication acknowledgement owner: %w", err)
		}
		if owner.ModuleID != moduleID {
			continue
		}
		acknowledgement, decodeErr := decodeModuleDeploymentAcknowledgement(record, logicalPath, moduleID)
		if decodeErr != nil {
			return ModuleDeploymentAcknowledgement{}, false, decodeErr
		}
		if acknowledgement.AVMReleaseVersion != avmReleaseVersion || acknowledgement.DeploymentReceiptID != deploymentID {
			continue
		}
		operationID, idErr := moduleDeploymentAckOperationID(acknowledgement)
		if idErr != nil {
			return ModuleDeploymentAcknowledgement{}, false, idErr
		}
		operationPath, pathErr := identity.StateOperationReceiptPath(moduleID, operationID)
		if pathErr != nil {
			return ModuleDeploymentAcknowledgement{}, false, pathErr
		}
		receipt, exists := operationReceiptRecord(snapshot, moduleID, operationID)
		if !exists || receipt.Validate(operationPath) != nil || verifyReceiptEvent(snapshot, operationID, operationPath) != nil {
			return ModuleDeploymentAcknowledgement{}, false, ErrTransactionIdentityConflict
		}
		var result modulePublicationAckResult
		if json.Unmarshal(receipt.Result, &result) != nil || !sameModuleDeploymentAcknowledgement(result.Acknowledgement, acknowledgement) || verifyReceiptWriteSet(snapshot, receipt, result.Events) != nil {
			return ModuleDeploymentAcknowledgement{}, false, ErrTransactionIdentityConflict
		}
		return acknowledgement, true, nil
	}
	return ModuleDeploymentAcknowledgement{}, false, nil
}
