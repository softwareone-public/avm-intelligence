package state

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/identity"
)

const aggregateCommitAttempts = 3
const aggregateRetryFloor = 10 * time.Millisecond
const aggregateRetryJitter = 20 * time.Millisecond

// AggregateDelta declares a module transition's exact base, module/global read
// owners and additional shared logical paths. Events declare its atomic write set.
type AggregateDelta struct {
	ExpectedHead string
	Base         Snapshot
	ReadOwners   []string
	ReadPaths    []string
	Events       []Event
}

// ApplyAggregateDelta preserves the latest complete tree, retrying only unrelated
// branch movement. Side effects and event preparation are never repeated here.
func ApplyAggregateDelta(ctx context.Context, backend Backend, delta AggregateDelta) (string, Snapshot, error) {
	if delta.ExpectedHead == "" || len(delta.Events) == 0 || delta.Base.LayoutVersion != identity.ModuleLayout {
		return "", Snapshot{}, fmt.Errorf("aggregate delta requires module layout, exact base and events")
	}
	if err := delta.Base.Validate(); err != nil {
		return "", Snapshot{}, err
	}
	owners := map[string]bool{"state/global": true}
	for _, owner := range delta.ReadOwners {
		if owner != "state/global" {
			root, err := identity.OperationalModuleRoot(ownerModuleID(owner))
			if err != nil || root != owner {
				return "", Snapshot{}, fmt.Errorf("invalid aggregate read owner")
			}
		}
		owners[owner] = true
	}
	for _, event := range delta.Events {
		owner, _, err := resolveRecordOwner(event.RecordPath, event.Record)
		if err != nil || !owners[owner] {
			return "", Snapshot{}, fmt.Errorf("event writes undeclared aggregate")
		}
	}
	for _, logicalPath := range delta.ReadPaths {
		if _, exists := delta.Base.Records[logicalPath]; !exists {
			return "", Snapshot{}, fmt.Errorf("shared read path is missing from delta base")
		}
	}
	for attempt := 0; attempt < aggregateCommitAttempts; attempt++ {
		head, current, err := ReadSnapshot(ctx, backend)
		if err != nil {
			return "", Snapshot{}, err
		}
		if current.LayoutVersion != delta.Base.LayoutVersion || current.LayoutMigrationBase != delta.Base.LayoutMigrationBase {
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
			return head, current, nil
		}
		if head != delta.ExpectedHead && !deltaPreconditionsMatch(delta, current, owners) {
			return "", Snapshot{}, ErrHeadMoved
		}
		newHead, err := CommitExpectedHead(ctx, backend, head, next)
		if err == nil {
			return newHead, next, nil
		}
		if !errors.Is(err, ErrHeadMoved) || attempt+1 == aggregateCommitAttempts {
			return "", Snapshot{}, err
		}
		delay := aggregateRetryFloor + time.Duration(rand.Int64N(int64(aggregateRetryJitter)))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", Snapshot{}, ctx.Err()
		case <-timer.C:
		}
	}
	return "", Snapshot{}, ErrHeadMoved
}

func ownerModuleID(owner string) string {
	for index := len(owner) - 1; index >= 0; index-- {
		if owner[index] == '/' {
			return owner[index+1:]
		}
	}
	return ""
}

func deltaPreconditionsMatch(delta AggregateDelta, current Snapshot, owners map[string]bool) bool {
	for _, logicalPath := range delta.ReadPaths {
		if !reflect.DeepEqual(delta.Base.Records[logicalPath], current.Records[logicalPath]) {
			return false
		}
	}
	for owner := range owners {
		if owner != "state/global" && !reflect.DeepEqual(delta.Base.Aggregates[owner], current.Aggregates[owner]) {
			return false
		}
	}
	return reflect.DeepEqual(ownedRecords(delta.Base, owners), ownedRecords(current, owners)) &&
		reflect.DeepEqual(globalReplayMetadata(delta.Base), globalReplayMetadata(current))
}

func ownedRecords(snapshot Snapshot, owners map[string]bool) map[string]Record {
	records := map[string]Record{}
	for logicalPath, record := range snapshot.Records {
		owner, _, err := resolveRecordOwner(logicalPath, record)
		if err != nil || owners[owner] {
			records[logicalPath] = record
		}
	}
	return records
}

func globalReplayMetadata(snapshot Snapshot) AggregateTransaction {
	global := emptyAggregate()
	for eventID, digest := range snapshot.AppliedEvents {
		global.AppliedEvents[eventID] = digest
	}
	for key, token := range snapshot.FencingTokens {
		global.FencingTokens[key] = token
	}
	for _, aggregate := range snapshot.Aggregates {
		for eventID := range aggregate.AppliedEvents {
			delete(global.AppliedEvents, eventID)
		}
		for key := range aggregate.FencingTokens {
			delete(global.FencingTokens, key)
		}
	}
	return global
}
