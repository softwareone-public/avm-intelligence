package state

import (
	"context"
	"fmt"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

const globalAggregateOwner = "state/global"

// AggregateDelta declares a module transition's exact base, module/global read
// owners and additional shared logical paths. Events declare its atomic write set.
type AggregateDelta struct {
	ExpectedHead string
	Base         Snapshot
	ReadOwners   []string
	ReadPaths    []string
	Events       []Event
}

// ApplyAggregateDelta applies one prepared delta at its declared head. It never
// retries a stale event set; callers that can recompute decisions use the coordinator.
func ApplyAggregateDelta(ctx context.Context, backend Backend, delta AggregateDelta) (string, Snapshot, error) {
	if delta.ExpectedHead == "" || len(delta.Events) == 0 || delta.Base.LayoutVersion != identity.ModuleLayout {
		return "", Snapshot{}, fmt.Errorf("aggregate delta requires module layout, exact base and events")
	}
	if err := delta.Base.Validate(); err != nil {
		return "", Snapshot{}, err
	}
	if _, err := validateAggregateDelta(delta); err != nil {
		return "", Snapshot{}, err
	}
	head, current, err := ReadSnapshot(ctx, backend)
	if err != nil {
		return "", Snapshot{}, err
	}
	if current.LayoutVersion != delta.Base.LayoutVersion || current.LayoutMigrationBase != delta.Base.LayoutMigrationBase {
		return "", Snapshot{}, ErrHeadMoved
	}
	if head != delta.ExpectedHead {
		return "", Snapshot{}, ErrHeadMoved
	}
	next := current
	changed := false
	for _, event := range delta.Events {
		updated, eventChanged, err := applyEvent(next, event)
		if err != nil {
			return "", Snapshot{}, err
		}
		next = updated
		changed = changed || eventChanged
	}
	if !changed {
		if !deltaAlreadyApplied(current, delta.Events) {
			return "", Snapshot{}, ErrEventConflict
		}
		return head, current, nil
	}
	newHead, err := CommitExpectedHead(ctx, backend, head, next)
	if err != nil {
		return "", Snapshot{}, err
	}
	return newHead, next, nil
}

func deltaAlreadyApplied(snapshot Snapshot, events []Event) bool {
	for _, event := range events {
		digest, exists := appliedEventDigest(snapshot, event.ID)
		if !exists {
			return false
		}
		expected, err := transactionEventDigest(event)
		if err != nil || digest != expected {
			return false
		}
	}
	return true
}

func validateAggregateDelta(delta AggregateDelta) (map[string]bool, error) {
	owners, err := validateAggregateOwners(delta.ReadOwners)
	if err != nil {
		return nil, err
	}
	if err = validateAggregateEventOwners(delta.Events, owners); err != nil {
		return nil, err
	}
	if err = validateAggregateReadPaths(delta.Base, delta.ReadPaths); err != nil {
		return nil, err
	}
	return owners, nil
}

func validateAggregateOwners(readOwners []string) (map[string]bool, error) {
	owners := map[string]bool{globalAggregateOwner: true}
	for _, owner := range readOwners {
		if owner != globalAggregateOwner {
			root, err := identity.OperationalModuleRoot(ownerModuleID(owner))
			if err != nil || root != owner {
				return nil, fmt.Errorf("invalid aggregate read owner")
			}
		}
		owners[owner] = true
	}
	return owners, nil
}

func validateAggregateEventOwners(events []Event, owners map[string]bool) error {
	for _, event := range events {
		owner, _, err := resolveRecordOwner(event.RecordPath, event.Record)
		if err != nil || !owners[owner] {
			return fmt.Errorf("event writes undeclared aggregate")
		}
	}
	return nil
}

func validateAggregateReadPaths(base Snapshot, readPaths []string) error {
	for _, logicalPath := range readPaths {
		if _, exists := base.Records[logicalPath]; !exists {
			return fmt.Errorf("shared read path is missing from delta base")
		}
	}
	return nil
}

func ownerModuleID(owner string) string {
	for index := len(owner) - 1; index >= 0; index-- {
		if owner[index] == '/' {
			return owner[index+1:]
		}
	}
	return ""
}
