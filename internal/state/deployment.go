package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
	"github.com/softwareone-public/avm-intelligence/internal/index"
)

const (
	DeploymentAwaitingApproval   = "awaiting_approval"
	DeploymentAuthorized         = "authorized"
	DeploymentDeployed           = "deployed"
	DeploymentFailed             = "failed"
	DeploymentReceiptActive      = "active"
	DeploymentReceiptHistorical  = "historical"
	DeploymentReceiptFailed      = "failed"
	deploymentStateSchemaVersion = 1
	deploymentStatePath          = "state/publications/deployments.json"
)

var (
	ErrDeploymentBaseMoved            = errors.New("deployment target moved after preparation")
	ErrDeploymentCandidateNotAncestor = errors.New("accepted commit is not an ancestor of the deployment target")
	ErrDeploymentRollbackApproval     = errors.New("rollback requires explicit approval of a previously deployed generation")
	ErrDeploymentNotCurrent           = errors.New("deployment does not certify current selected work")
	ErrDeploymentReceiptConflict      = errors.New("deployment run receipt conflicts with prior history")
	deploymentSHA1Pattern             = regexp.MustCompile(`^[0-9a-f]{40}$`)
	deploymentSHA256Pattern           = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// DeploymentPlan is the immutable input to one serialized publication
// generation. TargetCommit is rechecked after approval before activation.
type DeploymentPlan struct {
	AcceptedCommit       string                   `json:"accepted_commit"`
	DatasetID            string                   `json:"dataset_id"`
	DataDigest           string                   `json:"data_digest"`
	ModuleDataDigest     string                   `json:"module_data_digest"`
	ProviderDataDigest   string                   `json:"provider_data_digest"`
	SiteDigest           string                   `json:"site_digest"`
	Generator            index.Producer           `json:"generator"`
	ModuleIDs            []string                 `json:"module_ids"`
	Membership           []index.ModuleMembership `json:"module_membership"`
	TargetCommit         string                   `json:"target_commit"`
	RollbackOfGeneration uint64                   `json:"rollback_of_generation,omitempty"`
	PreparedAt           time.Time                `json:"prepared_at"`
}

// DeploymentApproval captures the post-review target check and explicit
// rollback authorization for one generation.
type DeploymentApproval struct {
	Generation                 uint64
	CurrentTarget              string
	CandidateIsAncestor        bool
	ExpectedAcceptedCommit     string
	ExpectedDatasetID          string
	ExpectedDataDigest         string
	ExpectedModuleDataDigest   string
	ExpectedProviderDataDigest string
	ExpectedSiteDigest         string
	ApprovedBy                 string
	RollbackApproved           bool
	AuthorizedAt               time.Time
}

// DeploymentGeneration records the state of one monotonic publication attempt.
type DeploymentGeneration struct {
	Generation           uint64                   `json:"generation"`
	AcceptedCommit       string                   `json:"accepted_commit"`
	DatasetID            string                   `json:"dataset_id"`
	DataDigest           string                   `json:"data_digest"`
	ModuleDataDigest     string                   `json:"module_data_digest"`
	ProviderDataDigest   string                   `json:"provider_data_digest"`
	SiteDigest           string                   `json:"site_digest"`
	Generator            index.Producer           `json:"generator"`
	ModuleIDs            []string                 `json:"module_ids"`
	Membership           []index.ModuleMembership `json:"module_membership"`
	TargetCommit         string                   `json:"target_commit"`
	RollbackOfGeneration uint64                   `json:"rollback_of_generation,omitempty"`
	Status               string                   `json:"status"`
	PreparedAt           time.Time                `json:"prepared_at"`
	ApprovedBy           string                   `json:"approved_by,omitempty"`
	AuthorizedAt         time.Time                `json:"authorized_at,omitempty"`
}

// DeploymentReceipt binds a successful or failed deployment run to the exact
// indexed artifact and generation that produced it.
type DeploymentReceipt struct {
	RunID              string                   `json:"run_id"`
	PagesDeploymentID  string                   `json:"pages_deployment_id"`
	Generation         uint64                   `json:"generation"`
	AcceptedCommit     string                   `json:"accepted_commit"`
	DatasetID          string                   `json:"dataset_id"`
	DataDigest         string                   `json:"data_digest"`
	ModuleDataDigest   string                   `json:"module_data_digest"`
	ProviderDataDigest string                   `json:"provider_data_digest"`
	SiteDigest         string                   `json:"site_digest"`
	Generator          index.Producer           `json:"generator"`
	ModuleIDs          []string                 `json:"module_ids"`
	Membership         []index.ModuleMembership `json:"module_membership"`
	Succeeded          bool                     `json:"succeeded"`
	Status             string                   `json:"status"`
	DeployedAt         time.Time                `json:"deployed_at"`
}

// DeploymentState contains the generation high-water mark, active pointer,
// immutable preparation history, and imported callback history.
type DeploymentState struct {
	SchemaVersion    int                    `json:"schema_version"`
	HighWater        uint64                 `json:"high_water"`
	ActiveGeneration uint64                 `json:"active_generation"`
	Generations      []DeploymentGeneration `json:"generations"`
	Receipts         []DeploymentReceipt    `json:"receipts"`
}

// DeploymentResult reports the committed state transition.
type DeploymentResult struct {
	Head       string
	State      DeploymentState
	Deployment DeploymentGeneration
	Receipt    DeploymentReceipt
	Changed    bool
}

// DeploymentReceiptIdentity is the exact external identity required to read
// one already imported active Pages receipt for receipt-only recovery.
type DeploymentReceiptIdentity struct {
	RunID             string
	PagesDeploymentID string
	AcceptedCommit    string
	Generation        uint64
}

// ReadActiveDeploymentReceipt returns only the exact successful receipt for
// the currently active high-water deployment generation. It does not mutate
// state or infer identity from the latest receipt.
func ReadActiveDeploymentReceipt(ctx context.Context, coordinator TransactionCoordinator, expected DeploymentReceiptIdentity) (string, DeploymentReceipt, error) {
	if coordinator == nil || expected.RunID == "" || !deploymentSHA1Pattern.MatchString(expected.PagesDeploymentID) || !deploymentSHA1Pattern.MatchString(expected.AcceptedCommit) || expected.PagesDeploymentID != expected.AcceptedCommit || expected.Generation == 0 {
		return "", DeploymentReceipt{}, fmt.Errorf("exact active deployment receipt identity is incomplete")
	}
	head, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return "", DeploymentReceipt{}, err
	}
	current, err := readDeploymentState(snapshot)
	if err != nil {
		return "", DeploymentReceipt{}, err
	}
	if current.ActiveGeneration != expected.Generation || current.HighWater != expected.Generation {
		return "", DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	position := deploymentPosition(current, expected.Generation)
	if position < 0 || current.Generations[position].Status != DeploymentDeployed {
		return "", DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	receipt, exists := activeDeploymentReceipt(current, expected.Generation)
	if !exists || receipt.RunID != expected.RunID || receipt.PagesDeploymentID != expected.PagesDeploymentID || receipt.AcceptedCommit != expected.AcceptedCommit || receipt.Generation != expected.Generation || receipt.Status != DeploymentReceiptActive || !receipt.Succeeded || !matchesDeployment(current.Generations[position], receipt) {
		return "", DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	return head, receipt, nil
}

// PrepareDeployment reserves the next generation in the state CAS. A prepared
// generation immediately fences Done from inheriting an older deployment.
func PrepareDeployment(ctx context.Context, coordinator TransactionCoordinator, plan DeploymentPlan) (DeploymentResult, error) {
	if err := validateDeploymentPlan(plan); err != nil {
		return DeploymentResult{}, err
	}
	if coordinator == nil {
		return DeploymentResult{}, fmt.Errorf("deployment transaction coordinator is required")
	}
	_, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return DeploymentResult{}, err
	}
	current, err := readDeploymentState(snapshot)
	if err != nil {
		return DeploymentResult{}, err
	}
	if current.HighWater == math.MaxUint64 {
		return DeploymentResult{}, fmt.Errorf("deployment generation is exhausted")
	}
	generation := current.HighWater + 1
	deployment := deploymentFromPlan(generation, plan)
	deployment.Status = DeploymentAwaitingApproval
	operationID, err := identity.ID("deployment-prepare", 1, struct {
		AcceptedCommit string    `json:"accepted_commit"`
		DatasetID      string    `json:"dataset_id"`
		PreparedAt     time.Time `json:"prepared_at"`
	}{plan.AcceptedCommit, plan.DatasetID, plan.PreparedAt.UTC()})
	if err != nil {
		return DeploymentResult{}, err
	}
	result, err := runDeploymentTransaction(ctx, coordinator, operationID, "deployment.prepare", deployment, func(current DeploymentState) (DeploymentState, error) {
		if current.HighWater == math.MaxUint64 || current.HighWater+1 != generation {
			return DeploymentState{}, ErrHeadMoved
		}
		next := cloneDeploymentState(current)
		next.HighWater = generation
		next.Generations = append(next.Generations, deployment)
		return next, next.Validate()
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		current, readErr := readDeploymentState(snapshot)
		if readErr != nil {
			return TransactionResolution{}, readErr
		}
		position := deploymentPositionFromPlan(current, plan)
		if position < 0 {
			return TransactionResolution{}, nil
		}
		return TransactionResolution{Found: true, Value: DeploymentResult{State: current, Deployment: current.Generations[position]}}, nil
	})
	if err != nil {
		return DeploymentResult{}, err
	}
	return result, nil
}

// AuthorizeDeployment activates a prepared generation only after the reviewer
// still sees the same target and confirms accepted-commit ancestry. Rollbacks
// require explicit approval and must target a previously deployed generation.
func AuthorizeDeployment(ctx context.Context, coordinator TransactionCoordinator, approval DeploymentApproval) (DeploymentResult, error) {
	if coordinator == nil {
		return DeploymentResult{}, fmt.Errorf("deployment transaction coordinator is required")
	}
	_, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return DeploymentResult{}, err
	}
	current, err := readDeploymentState(snapshot)
	if err != nil {
		return DeploymentResult{}, err
	}
	position := deploymentPosition(current, approval.Generation)
	if position < 0 || approval.Generation != current.HighWater || current.Generations[position].Status != DeploymentAwaitingApproval {
		return DeploymentResult{}, ErrDeploymentNotCurrent
	}
	deployment := current.Generations[position]
	if approval.CurrentTarget != deployment.TargetCommit {
		return DeploymentResult{}, ErrDeploymentBaseMoved
	}
	if !approvalMatchesDeployment(approval, deployment) {
		return DeploymentResult{}, ErrDeploymentNotCurrent
	}
	if !approval.CandidateIsAncestor {
		return DeploymentResult{}, ErrDeploymentCandidateNotAncestor
	}
	if approval.ApprovedBy == "" || approval.AuthorizedAt.IsZero() {
		return DeploymentResult{}, fmt.Errorf("deployment reviewer and authorization time are required")
	}
	if deployment.RollbackOfGeneration != 0 && (!approval.RollbackApproved || !matchesDeployedRollback(current, deployment)) {
		return DeploymentResult{}, ErrDeploymentRollbackApproval
	}
	operationID, err := identity.ID("deployment-authorize", 1, approval)
	if err != nil {
		return DeploymentResult{}, err
	}
	result, err := runDeploymentTransaction(ctx, coordinator, operationID, "deployment.prepare", deployment, func(current DeploymentState) (DeploymentState, error) {
		position := deploymentPosition(current, approval.Generation)
		if position < 0 || approval.Generation != current.HighWater || current.Generations[position].Status != DeploymentAwaitingApproval || approval.CurrentTarget != current.Generations[position].TargetCommit || !approvalMatchesDeployment(approval, current.Generations[position]) {
			return DeploymentState{}, ErrDeploymentNotCurrent
		}
		if !approval.CandidateIsAncestor {
			return DeploymentState{}, ErrDeploymentCandidateNotAncestor
		}
		if approval.ApprovedBy == "" || approval.AuthorizedAt.IsZero() {
			return DeploymentState{}, fmt.Errorf("deployment reviewer and authorization time are required")
		}
		deployment := current.Generations[position]
		if deployment.RollbackOfGeneration != 0 && (!approval.RollbackApproved || !matchesDeployedRollback(current, deployment)) {
			return DeploymentState{}, ErrDeploymentRollbackApproval
		}
		deployment.Status = DeploymentAuthorized
		deployment.ApprovedBy = approval.ApprovedBy
		deployment.AuthorizedAt = approval.AuthorizedAt.UTC()
		next := cloneDeploymentState(current)
		next.Generations[position] = deployment
		next.ActiveGeneration = approval.Generation
		return next, next.Validate()
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		current, readErr := readDeploymentState(snapshot)
		if readErr != nil {
			return TransactionResolution{}, readErr
		}
		position := deploymentPosition(current, approval.Generation)
		if position < 0 || current.Generations[position].ApprovedBy != approval.ApprovedBy || !current.Generations[position].AuthorizedAt.Equal(approval.AuthorizedAt.UTC()) {
			return TransactionResolution{}, ErrTransactionIdentityConflict
		}
		return TransactionResolution{Found: true, Value: DeploymentResult{State: current, Deployment: current.Generations[position]}}, nil
	})
	if err != nil {
		return DeploymentResult{}, err
	}
	return result, nil
}

func approvalMatchesDeployment(approval DeploymentApproval, deployment DeploymentGeneration) bool {
	return approval.ExpectedAcceptedCommit == deployment.AcceptedCommit && approval.ExpectedDatasetID == deployment.DatasetID &&
		approval.ExpectedDataDigest == deployment.DataDigest && approval.ExpectedModuleDataDigest == deployment.ModuleDataDigest &&
		approval.ExpectedProviderDataDigest == deployment.ProviderDataDigest && approval.ExpectedSiteDigest == deployment.SiteDigest
}

// ImportDeploymentReceipt reconciles one deployment callback. Only a
// successful callback for the active latest generation can update it to
// deployed; all superseded callbacks remain historical.
func ImportDeploymentReceipt(ctx context.Context, coordinator TransactionCoordinator, receipt DeploymentReceipt) (DeploymentResult, error) {
	if err := validateDeploymentReceipt(receipt); err != nil {
		return DeploymentResult{}, err
	}
	if coordinator == nil {
		return DeploymentResult{}, fmt.Errorf("deployment transaction coordinator is required")
	}
	head, snapshot, err := coordinator.Read(ctx)
	if err != nil {
		return DeploymentResult{}, err
	}
	current, err := readDeploymentState(snapshot)
	if err != nil {
		return DeploymentResult{}, err
	}
	if replay, found, replayErr := deploymentReceiptReplay(head, current, receipt); replayErr != nil || found {
		return replay, replayErr
	}
	position := deploymentPosition(current, receipt.Generation)
	if position < 0 || !matchesDeployment(current.Generations[position], receipt) {
		return DeploymentResult{}, fmt.Errorf("deployment receipt does not match a prepared generation")
	}
	deployment := current.Generations[position]
	operationID := "deployment:receipt:" + receipt.RunID
	var committedDeployment DeploymentGeneration
	var committedReceipt DeploymentReceipt
	result, err := runDeploymentTransaction(ctx, coordinator, operationID, "deployment.receipt", deployment, func(current DeploymentState) (DeploymentState, error) {
		for _, prior := range current.Receipts {
			if prior.RunID != receipt.RunID {
				continue
			}
			if !sameDeploymentReceipt(prior, receipt) {
				return DeploymentState{}, ErrDeploymentReceiptConflict
			}
			position := deploymentPosition(current, prior.Generation)
			if position < 0 {
				return DeploymentState{}, ErrDeploymentNotCurrent
			}
			committedDeployment = current.Generations[position]
			committedReceipt = prior
			return current, nil
		}
		position := deploymentPosition(current, receipt.Generation)
		if position < 0 || !matchesDeployment(current.Generations[position], receipt) {
			return DeploymentState{}, fmt.Errorf("deployment receipt does not match a prepared generation")
		}
		updatedReceipt, updatedDeployment := classifyDeploymentReceipt(current, current.Generations[position], receipt)
		next := cloneDeploymentState(current)
		next.Generations[position] = updatedDeployment
		next.Receipts = append(next.Receipts, updatedReceipt)
		committedDeployment = updatedDeployment
		committedReceipt = updatedReceipt
		return next, next.Validate()
	}, func(snapshot Snapshot) (TransactionResolution, error) {
		current, readErr := readDeploymentState(snapshot)
		if readErr != nil {
			return TransactionResolution{}, readErr
		}
		for _, prior := range current.Receipts {
			if prior.RunID == receipt.RunID {
				if !sameDeploymentReceipt(prior, receipt) {
					return TransactionResolution{}, ErrDeploymentReceiptConflict
				}
				position := deploymentPosition(current, prior.Generation)
				if position < 0 {
					return TransactionResolution{}, ErrDeploymentNotCurrent
				}
				return TransactionResolution{Found: true, Value: DeploymentResult{State: current, Deployment: current.Generations[position], Receipt: prior}}, nil
			}
		}
		return TransactionResolution{}, nil
	})
	if err != nil {
		return DeploymentResult{}, err
	}
	if result.Receipt.RunID == "" {
		result.Deployment = committedDeployment
		result.Receipt = committedReceipt
	}
	return result, nil
}

func runDeploymentTransaction(ctx context.Context, coordinator TransactionCoordinator, operationID, provenanceOperation string, proposed DeploymentGeneration, update func(DeploymentState) (DeploymentState, error), resolve TransactionResultResolver) (DeploymentResult, error) {
	path := deploymentStatePath
	eventFor := func(current DeploymentState) (Event, error) {
		data, err := json.Marshal(current)
		if err != nil {
			return Event{}, err
		}
		record := Record{Kind: "publication", SchemaVersion: deploymentStateSchemaVersion, Data: data}
		return Event{ID: operationID, RecordPath: path, Record: record}, nil
	}
	transaction, err := coordinator.RunTransaction(ctx, operationID, func(snapshot Snapshot) (TransactionDecision, error) {
		current, err := readDeploymentState(snapshot)
		if err != nil {
			return TransactionDecision{}, err
		}
		next, err := update(current)
		if err != nil {
			return TransactionDecision{}, err
		}
		if reflect.DeepEqual(current, next) {
			return TransactionDecision{Noop: true, Result: DeploymentResult{State: current, Deployment: proposed}}, nil
		}
		event, err := eventFor(next)
		if err != nil {
			return TransactionDecision{}, err
		}
		return TransactionDecision{Events: []Event{event}, Result: DeploymentResult{State: next, Deployment: proposed, Changed: true}, Candidate: CandidateOptions{Subject: "state(publication): update deployment state", Provenance: GitProvenance{Operation: provenanceOperation, Event: event.ID}}}, nil
	}, resolve)
	if err != nil {
		return DeploymentResult{}, err
	}
	result, ok := transaction.Value.(DeploymentResult)
	if !ok {
		return DeploymentResult{}, fmt.Errorf("deployment transaction result has an invalid type")
	}
	result.Head = transaction.Head
	result.Changed = transaction.Changed
	return result, nil
}

func deploymentReceiptReplay(head string, current DeploymentState, receipt DeploymentReceipt) (DeploymentResult, bool, error) {
	for _, prior := range current.Receipts {
		if prior.RunID != receipt.RunID {
			continue
		}
		if !sameDeploymentReceipt(prior, receipt) {
			return DeploymentResult{}, false, ErrDeploymentReceiptConflict
		}
		position := deploymentPosition(current, prior.Generation)
		if position < 0 {
			return DeploymentResult{}, false, ErrDeploymentNotCurrent
		}
		return DeploymentResult{Head: head, State: current, Deployment: current.Generations[position], Receipt: prior}, true, nil
	}
	return DeploymentResult{}, false, nil
}

func classifyDeploymentReceipt(current DeploymentState, deployment DeploymentGeneration, receipt DeploymentReceipt) (DeploymentReceipt, DeploymentGeneration) {
	if !receipt.Succeeded {
		receipt.Status = DeploymentReceiptFailed
		if receipt.Generation == current.ActiveGeneration {
			deployment.Status = DeploymentFailed
		}
		return receipt, deployment
	}
	if receipt.Generation == current.ActiveGeneration && receipt.Generation == current.HighWater && deployment.Status == DeploymentAuthorized {
		receipt.Status = DeploymentReceiptActive
		deployment.Status = DeploymentDeployed
		return receipt, deployment
	}
	receipt.Status = DeploymentReceiptHistorical
	return receipt, deployment
}

// VerifyDoneSelection checks the currently deployed module/context selection
// against the current accepted request and exact analysis/receipt membership.
func VerifyDoneSelection(snapshot Snapshot, moduleID, avmReleaseVersion, contextID string) (DeploymentReceipt, error) {
	deploymentState, err := readDeploymentState(snapshot)
	if err != nil {
		return DeploymentReceipt{}, err
	}
	if deploymentState.HighWater == 0 || deploymentState.ActiveGeneration != deploymentState.HighWater {
		return DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	position := deploymentPosition(deploymentState, deploymentState.ActiveGeneration)
	if position < 0 || deploymentState.Generations[position].Status != DeploymentDeployed {
		return DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	receipt, exists := activeDeploymentReceipt(deploymentState, deploymentState.ActiveGeneration)
	if !exists {
		return DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	member, exists := deploymentMembership(deploymentState.Generations[position].Membership, moduleID, avmReleaseVersion, contextID)
	if !exists || member.AnalysisID == nil || member.SelectionID == nil {
		return DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	acknowledgement, ackErr := moduleDeploymentAcknowledgementFor(snapshot, receipt, member)
	if ackErr != nil || acknowledgement.AVMReleaseVersion != avmReleaseVersion {
		return DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	slotPath, pathErr := identity.StateLifecycleSlotPath(moduleID)
	if pathErr != nil {
		return DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	slotRecord, slotExists := snapshot.Records[slotPath]
	var slot moduleLifecycleSlot
	if !slotExists || slotRecord.Kind != "lifecycle_slot" || json.Unmarshal(slotRecord.Data, &slot) != nil || slot.Status != "released" || slot.ReleaseVersion != avmReleaseVersion || slot.ReleaseProof == nil || slot.ReleaseProof.DeploymentReceiptID != acknowledgement.DeploymentReceiptID || slot.ReleaseProof.MembershipDigest != acknowledgement.MembershipDigest {
		return DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	pointer, exists, err := readSelectionPointer(snapshot, selectionPointerPath(moduleID, avmReleaseVersion, contextID))
	if err != nil || !exists || pointer.AVMReleaseVersion != avmReleaseVersion {
		return DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	selected, err := readSelectionReceipt(snapshot, moduleID, contextID, pointer.SelectionID)
	if err != nil || selected.ID != *member.SelectionID || selected.AnalysisID == nil || *selected.AnalysisID != *member.AnalysisID {
		return DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	request, err := latestCurrentAssessment(snapshot, moduleID, avmReleaseVersion, contextID)
	if err != nil || request.Status != "accepted" || request.ID != pointer.RequestID || request.Fingerprint != selected.InputFingerprint || request.Work.DesiredGeneration != pointer.DesiredGeneration {
		return DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	if err = validateImportedInputs(snapshot, request); err != nil {
		return DeploymentReceipt{}, ErrDeploymentNotCurrent
	}
	return receipt, nil
}

// Validate checks the append-only deployment high-water mark and callback history.
func (deploymentState DeploymentState) Validate() error {
	if deploymentState.SchemaVersion != deploymentStateSchemaVersion || deploymentState.HighWater != uint64(len(deploymentState.Generations)) || deploymentState.ActiveGeneration > deploymentState.HighWater {
		return fmt.Errorf("deployment state high-water mark is invalid")
	}
	for position, deployment := range deploymentState.Generations {
		if deployment.Generation != uint64(position+1) || deployment.Status == "" {
			return fmt.Errorf("deployment generation history is not contiguous")
		}
		if err := validateDeploymentGeneration(deployment); err != nil {
			return err
		}
	}
	seenRuns := make(map[string]struct{}, len(deploymentState.Receipts))
	for _, receipt := range deploymentState.Receipts {
		if receipt.Status == "" {
			return fmt.Errorf("deployment state contains an unclassified callback")
		}
		if err := validateDeploymentReceipt(receipt); err != nil {
			return err
		}
		if _, exists := seenRuns[receipt.RunID]; exists {
			return fmt.Errorf("deployment state repeats producer run %s", receipt.RunID)
		}
		seenRuns[receipt.RunID] = struct{}{}
	}
	if deploymentState.ActiveGeneration != 0 {
		active := deploymentState.Generations[deploymentState.ActiveGeneration-1]
		if active.Status != DeploymentAuthorized && active.Status != DeploymentDeployed && active.Status != DeploymentFailed {
			return fmt.Errorf("active deployment generation has an invalid status")
		}
	}
	return nil
}

func validateDeploymentPlan(plan DeploymentPlan) error {
	if !deploymentSHA1Pattern.MatchString(plan.AcceptedCommit) || !deploymentSHA1Pattern.MatchString(plan.TargetCommit) || identity.ValidateRecordID("dataset", plan.DatasetID) != nil || !deploymentSHA256Pattern.MatchString(plan.DataDigest) || !deploymentSHA256Pattern.MatchString(plan.ModuleDataDigest) || !deploymentSHA256Pattern.MatchString(plan.ProviderDataDigest) || !deploymentSHA256Pattern.MatchString(plan.SiteDigest) || plan.Generator.ID == "" || plan.Generator.Version == "" || plan.Generator.ContractVersion < 1 || plan.PreparedAt.IsZero() || len(plan.ModuleIDs) == 0 {
		return fmt.Errorf("deployment plan identity is incomplete")
	}
	if err := validateModuleIDs(plan.ModuleIDs); err != nil {
		return err
	}
	return validateMembership(plan.ModuleIDs, plan.Membership)
}

func validateDeploymentGeneration(deployment DeploymentGeneration) error {
	if deployment.Generation == 0 || !deploymentSHA1Pattern.MatchString(deployment.AcceptedCommit) || !deploymentSHA1Pattern.MatchString(deployment.TargetCommit) || identity.ValidateRecordID("dataset", deployment.DatasetID) != nil || !deploymentSHA256Pattern.MatchString(deployment.DataDigest) || !deploymentSHA256Pattern.MatchString(deployment.ModuleDataDigest) || !deploymentSHA256Pattern.MatchString(deployment.ProviderDataDigest) || !deploymentSHA256Pattern.MatchString(deployment.SiteDigest) || deployment.Generator.ID == "" || deployment.Generator.Version == "" || deployment.Generator.ContractVersion < 1 || deployment.PreparedAt.IsZero() {
		return fmt.Errorf("deployment generation identity is invalid")
	}
	if err := validateModuleIDs(deployment.ModuleIDs); err != nil {
		return err
	}
	if err := validateMembership(deployment.ModuleIDs, deployment.Membership); err != nil {
		return err
	}
	switch deployment.Status {
	case DeploymentAwaitingApproval:
		if deployment.ApprovedBy != "" || !deployment.AuthorizedAt.IsZero() {
			return fmt.Errorf("unapproved deployment contains authorization data")
		}
	case DeploymentAuthorized, DeploymentDeployed, DeploymentFailed:
		if deployment.ApprovedBy == "" || deployment.AuthorizedAt.IsZero() {
			return fmt.Errorf("authorized deployment lacks reviewer evidence")
		}
	default:
		return fmt.Errorf("deployment status %q is invalid", deployment.Status)
	}
	return nil
}

func validateDeploymentReceipt(receipt DeploymentReceipt) error {
	if receipt.RunID == "" || !deploymentSHA1Pattern.MatchString(receipt.PagesDeploymentID) || receipt.PagesDeploymentID != receipt.AcceptedCommit || receipt.Generation == 0 || !deploymentSHA1Pattern.MatchString(receipt.AcceptedCommit) || identity.ValidateRecordID("dataset", receipt.DatasetID) != nil || !deploymentSHA256Pattern.MatchString(receipt.DataDigest) || !deploymentSHA256Pattern.MatchString(receipt.ModuleDataDigest) || !deploymentSHA256Pattern.MatchString(receipt.ProviderDataDigest) || !deploymentSHA256Pattern.MatchString(receipt.SiteDigest) || receipt.Generator.ID == "" || receipt.Generator.Version == "" || receipt.Generator.ContractVersion < 1 || receipt.DeployedAt.IsZero() {
		return fmt.Errorf("deployment receipt identity is incomplete")
	}
	if err := validateModuleIDs(receipt.ModuleIDs); err != nil {
		return err
	}
	if err := validateMembership(receipt.ModuleIDs, receipt.Membership); err != nil {
		return err
	}
	switch receipt.Status {
	case "":
		return nil
	case DeploymentReceiptActive, DeploymentReceiptHistorical:
		if !receipt.Succeeded {
			return fmt.Errorf("successful deployment receipt status lacks success")
		}
	case DeploymentReceiptFailed:
		if receipt.Succeeded {
			return fmt.Errorf("failed deployment receipt status has success")
		}
	default:
		return fmt.Errorf("deployment receipt status %q is invalid", receipt.Status)
	}
	return nil
}

func validateModuleIDs(moduleIDs []string) error {
	if len(moduleIDs) == 0 {
		return fmt.Errorf("deployment module membership is empty")
	}
	previous := ""
	for _, moduleID := range moduleIDs {
		if !selectionModulePattern.MatchString(moduleID) || (previous != "" && moduleID <= previous) {
			return fmt.Errorf("deployment module IDs must be valid, unique, and sorted")
		}
		previous = moduleID
	}
	return nil
}

func validateMembership(moduleIDs []string, membership []index.ModuleMembership) error {
	previous := ""
	for _, member := range membership {
		key := member.ModuleID + "\x00" + member.AVMReleaseVersion + "\x00" + member.ContextID
		if !selectionModulePattern.MatchString(member.ModuleID) || !selectionReleasePattern.MatchString(member.AVMReleaseVersion) || !containsDeploymentModule(moduleIDs, member.ModuleID) || identity.ValidateRelativePath(member.ContextID, false) != nil || (previous != "" && key <= previous) || member.SelectionID == nil {
			return fmt.Errorf("deployment module/context membership is invalid")
		}
		if member.AnalysisID != nil && identity.ValidateRecordID("analysis", *member.AnalysisID) != nil {
			return fmt.Errorf("deployment analysis ID is invalid")
		}
		if member.SelectionID != nil && identity.ValidateRecordID("selection", *member.SelectionID) != nil {
			return fmt.Errorf("deployment selection ID is invalid")
		}
		previous = key
	}
	return nil
}

func containsDeploymentModule(moduleIDs []string, moduleID string) bool {
	for _, candidate := range moduleIDs {
		if candidate == moduleID {
			return true
		}
	}
	return false
}

func deploymentFromPlan(generation uint64, plan DeploymentPlan) DeploymentGeneration {
	return DeploymentGeneration{
		Generation: generation, AcceptedCommit: plan.AcceptedCommit, DatasetID: plan.DatasetID,
		DataDigest: plan.DataDigest, ModuleDataDigest: plan.ModuleDataDigest,
		ProviderDataDigest: plan.ProviderDataDigest, SiteDigest: plan.SiteDigest, Generator: plan.Generator,
		ModuleIDs: append([]string(nil), plan.ModuleIDs...), Membership: cloneDeploymentMembership(plan.Membership),
		TargetCommit: plan.TargetCommit, RollbackOfGeneration: plan.RollbackOfGeneration,
		PreparedAt: plan.PreparedAt.UTC(),
	}
}

func cloneDeploymentMembership(membership []index.ModuleMembership) []index.ModuleMembership {
	cloned := make([]index.ModuleMembership, len(membership))
	for position, member := range membership {
		cloned[position] = member
		cloned[position].AnalysisID = cloneStringPointer(member.AnalysisID)
		cloned[position].SelectionID = cloneStringPointer(member.SelectionID)
	}
	return cloned
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func matchesDeployedRollback(current DeploymentState, rollback DeploymentGeneration) bool {
	position := deploymentPosition(current, rollback.RollbackOfGeneration)
	if position < 0 {
		return false
	}
	previous := current.Generations[position]
	return previous.Status == DeploymentDeployed && previous.AcceptedCommit == rollback.AcceptedCommit && previous.DatasetID == rollback.DatasetID && previous.DataDigest == rollback.DataDigest && previous.ModuleDataDigest == rollback.ModuleDataDigest && previous.ProviderDataDigest == rollback.ProviderDataDigest && previous.SiteDigest == rollback.SiteDigest && previous.Generator == rollback.Generator && sameDeploymentModuleIDs(previous.ModuleIDs, rollback.ModuleIDs) && sameDeploymentMembership(previous.Membership, rollback.Membership)
}

func deploymentPosition(current DeploymentState, generation uint64) int {
	if generation == 0 || generation > uint64(len(current.Generations)) {
		return -1
	}
	return int(generation - 1)
}

func matchesDeployment(deployment DeploymentGeneration, receipt DeploymentReceipt) bool {
	return deployment.Generation == receipt.Generation && deployment.AcceptedCommit == receipt.AcceptedCommit && deployment.DatasetID == receipt.DatasetID && deployment.DataDigest == receipt.DataDigest && deployment.ModuleDataDigest == receipt.ModuleDataDigest && deployment.ProviderDataDigest == receipt.ProviderDataDigest && deployment.SiteDigest == receipt.SiteDigest && deployment.Generator == receipt.Generator && sameDeploymentModuleIDs(deployment.ModuleIDs, receipt.ModuleIDs) && sameDeploymentMembership(deployment.Membership, receipt.Membership)
}

func sameDeploymentModuleIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for position := range left {
		if left[position] != right[position] {
			return false
		}
	}
	return true
}

func sameDeploymentMembership(left, right []index.ModuleMembership) bool {
	if len(left) != len(right) {
		return false
	}
	for position := range left {
		if left[position].ModuleID != right[position].ModuleID || left[position].AVMReleaseVersion != right[position].AVMReleaseVersion || left[position].ContextID != right[position].ContextID || !sameDeploymentNullable(left[position].AnalysisID, right[position].AnalysisID) || !sameDeploymentNullable(left[position].SelectionID, right[position].SelectionID) {
			return false
		}
	}
	return true
}

func sameDeploymentNullable(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameDeploymentReceipt(left, right DeploymentReceipt) bool {
	return left.RunID == right.RunID && left.PagesDeploymentID == right.PagesDeploymentID && left.Generation == right.Generation && left.AcceptedCommit == right.AcceptedCommit && left.DatasetID == right.DatasetID && left.DataDigest == right.DataDigest && left.ModuleDataDigest == right.ModuleDataDigest && left.ProviderDataDigest == right.ProviderDataDigest && left.SiteDigest == right.SiteDigest && left.Generator == right.Generator && sameDeploymentModuleIDs(left.ModuleIDs, right.ModuleIDs) && sameDeploymentMembership(left.Membership, right.Membership) && left.Succeeded == right.Succeeded && left.DeployedAt.Equal(right.DeployedAt)
}

func deploymentMembership(membership []index.ModuleMembership, moduleID, avmReleaseVersion, contextID string) (index.ModuleMembership, bool) {
	for _, member := range membership {
		if member.ModuleID == moduleID && member.AVMReleaseVersion == avmReleaseVersion && member.ContextID == contextID {
			return member, true
		}
	}
	return index.ModuleMembership{}, false
}

func activeDeploymentReceipt(current DeploymentState, generation uint64) (DeploymentReceipt, bool) {
	for _, receipt := range current.Receipts {
		if receipt.Generation == generation && receipt.Status == DeploymentReceiptActive {
			return receipt, true
		}
	}
	return DeploymentReceipt{}, false
}

func latestCurrentAssessment(snapshot Snapshot, moduleID, avmReleaseVersion, contextID string) (selectionRequest, error) {
	var latest selectionRequest
	found := false
	for recordPath, record := range snapshot.Records {
		if record.Kind != "request" {
			continue
		}
		var request selectionRequest
		if err := json.Unmarshal(record.Data, &request); err != nil {
			return selectionRequest{}, fmt.Errorf("decode current assessment request %s: %w", recordPath, err)
		}
		if request.ModuleID != moduleID || request.Work == nil || request.Work.Phase != "assess" || request.Work.AVMReleaseVersion != avmReleaseVersion || request.Work.ContextID != contextID {
			continue
		}
		if !found || request.Work.DesiredGeneration > latest.Work.DesiredGeneration {
			latest, found = request, true
		} else if request.Work.DesiredGeneration == latest.Work.DesiredGeneration && request.Fingerprint != latest.Fingerprint {
			return selectionRequest{}, fmt.Errorf("assessment context has conflicting current requests")
		}
	}
	if !found {
		return selectionRequest{}, ErrDeploymentNotCurrent
	}
	return latest, nil
}

func readSelectionReceipt(snapshot Snapshot, moduleID, contextID, selectionID string) (SelectionReceipt, error) {
	path := selectionStatePrefix + moduleID + "/" + contextID + "/" + selectionID + ".json"
	record, exists := snapshot.Records[path]
	if !exists || record.Kind != "selection" || record.SchemaVersion != selectionRecordSchemaVersion {
		return SelectionReceipt{}, ErrDeploymentNotCurrent
	}
	var receipt SelectionReceipt
	if err := json.Unmarshal(record.Data, &receipt); err != nil {
		return SelectionReceipt{}, err
	}
	if err := receipt.Validate(); err != nil {
		return SelectionReceipt{}, err
	}
	if receipt.ID != selectionID || receipt.ModuleID != moduleID || receipt.ContextID != contextID {
		return SelectionReceipt{}, fmt.Errorf("selection receipt is stored at a noncanonical subject path")
	}
	if receipt.CompletionID != nil {
		eventID, identityErr := identity.ID(selectionStateEventKind, 1, selectionEventIdentity{Path: path, Record: record})
		if identityErr != nil {
			return SelectionReceipt{}, fmt.Errorf("identify applied selection receipt: %w", identityErr)
		}
		digest, digestErr := transactionEventDigest(Event{ID: eventID, RecordPath: path, Record: record})
		if digestErr != nil {
			return SelectionReceipt{}, fmt.Errorf("digest applied selection receipt: %w", digestErr)
		}
		if historical, applied := appliedEventDigest(snapshot, eventID); !applied || historical != digest {
			return SelectionReceipt{}, ErrTransactionIdentityConflict
		}
	}
	return receipt, nil
}

func readDeploymentState(snapshot Snapshot) (DeploymentState, error) {
	current := DeploymentState{SchemaVersion: deploymentStateSchemaVersion, Generations: []DeploymentGeneration{}, Receipts: []DeploymentReceipt{}}
	record, exists := snapshot.Records[deploymentStatePath]
	if !exists {
		return current, nil
	}
	if record.Kind != "publication" || record.SchemaVersion != deploymentStateSchemaVersion {
		return DeploymentState{}, fmt.Errorf("deployment state record identity is invalid")
	}
	if err := json.Unmarshal(record.Data, &current); err != nil {
		return DeploymentState{}, fmt.Errorf("decode deployment state: %w", err)
	}
	if err := current.Validate(); err != nil {
		return DeploymentState{}, err
	}
	return current, nil
}

func readExpectedDeploymentState(ctx context.Context, backend Backend, expectedHead string) (string, Snapshot, DeploymentState, error) {
	if expectedHead == "" {
		return "", Snapshot{}, DeploymentState{}, fmt.Errorf("expected state head is required")
	}
	head, snapshot, err := ReadSnapshot(ctx, backend)
	if err != nil {
		return "", Snapshot{}, DeploymentState{}, err
	}
	if head != expectedHead {
		return "", Snapshot{}, DeploymentState{}, ErrHeadMoved
	}
	current, err := readDeploymentState(snapshot)
	return head, snapshot, current, err
}

func deploymentPositionFromPlan(current DeploymentState, plan DeploymentPlan) int {
	for position := range current.Generations {
		deployment := current.Generations[position]
		if deployment.AcceptedCommit == plan.AcceptedCommit && deployment.DatasetID == plan.DatasetID && deployment.PreparedAt.Equal(plan.PreparedAt.UTC()) {
			return position
		}
	}
	return -1
}

func cloneDeploymentState(current DeploymentState) DeploymentState {
	cloned := current
	cloned.Generations = append([]DeploymentGeneration(nil), current.Generations...)
	for position := range cloned.Generations {
		cloned.Generations[position].ModuleIDs = append([]string(nil), current.Generations[position].ModuleIDs...)
		cloned.Generations[position].Membership = cloneDeploymentMembership(current.Generations[position].Membership)
	}
	cloned.Receipts = append([]DeploymentReceipt(nil), current.Receipts...)
	for position := range cloned.Receipts {
		cloned.Receipts[position].ModuleIDs = append([]string(nil), current.Receipts[position].ModuleIDs...)
		cloned.Receipts[position].Membership = cloneDeploymentMembership(current.Receipts[position].Membership)
	}
	return cloned
}
