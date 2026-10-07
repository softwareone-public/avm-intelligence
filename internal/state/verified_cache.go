package state

import "context"

// HeadReader checks the mutable branch ref without loading its immutable contents.
type HeadReader interface {
	Head(context.Context) (string, error)
}

// VerifiedReadBackend reuses a decoded snapshot only after checking the live head.
// It is owned by one sequential discovery cycle, not shared between goroutines.
type VerifiedReadBackend struct {
	Backend
	head     string
	snapshot Snapshot
}

func (backend *VerifiedReadBackend) Read(ctx context.Context) (string, Snapshot, error) {
	if reader, ok := backend.Backend.(HeadReader); ok && backend.head != "" {
		head, err := reader.Head(ctx)
		if err != nil {
			return "", Snapshot{}, err
		}
		if head == backend.head {
			return head, cloneSnapshot(backend.snapshot), nil
		}
	}
	head, snapshot, err := backend.Backend.Read(ctx)
	if err == nil {
		backend.head, backend.snapshot = head, cloneSnapshot(snapshot)
	}
	return head, snapshot, err
}

func (backend *VerifiedReadBackend) Commit(ctx context.Context, head string, snapshot Snapshot) (string, error) {
	newHead, err := backend.Backend.Commit(ctx, head, snapshot)
	backend.head = ""
	return newHead, err
}
