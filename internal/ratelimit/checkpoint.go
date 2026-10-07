package ratelimit

import (
	"context"
	"time"
)

type checkpointKey struct{}
type cleanupKey struct{}
type parkingKey struct{}

const parkingWaitLimit = 5 * time.Minute

// ParkingContext reserves final cursor cleanup, bounded by the orchestration parent.
func ParkingContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithValue(CheckpointContext(ctx), parkingKey{}, true), parkingWaitLimit)
}

// WithCleanup preserves a bounded parent for outcome writes after a child work deadline.
func WithCleanup(ctx context.Context, parent context.Context) context.Context {
	return context.WithValue(ctx, cleanupKey{}, parent)
}

// CleanupContext never bypasses cancellation of the orchestration parent.
func CleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if parent, found := ctx.Value(cleanupKey{}).(context.Context); found {
		return context.WithTimeout(CheckpointContext(parent), time.Minute)
	}
	return context.WithTimeout(CheckpointContext(ctx), time.Minute)
}

// CheckpointContext identifies bounded durable cleanup, not ordinary API work.
func CheckpointContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, checkpointKey{}, true)
}

// SetRefresh connects a trusted quota probe, never ordinary API work.
func (pacer *Pacer) SetRefresh(refresh func(context.Context) error) {
	pacer.mu.Lock()
	defer pacer.mu.Unlock()
	pacer.refresh = refresh
}

// Seed accepts authoritative primary resources from a rate-limit refresh.
func (pacer *Pacer) Seed(resource string, remaining, limit int, reset time.Time) {
	if pacer == nil || remaining < 0 || limit <= 0 || reset.IsZero() {
		return
	}
	pacer.mu.Lock()
	defer pacer.mu.Unlock()
	if resource == "rest" || resource == "graphql" || resource == "search" {
		pacer.primary[resource] = primary{remaining: remaining, limit: limit, reset: reset}
	}
}

// ResumeAt returns server-directed pause state without exposing request details.
func (pacer *Pacer) ResumeAt() time.Time {
	if pacer == nil {
		return time.Time{}
	}
	pacer.mu.Lock()
	defer pacer.mu.Unlock()
	return later(pacer.pausedUntil, pacer.deferredUntil)
}
