// Package progress reports operational boundaries without changing lifecycle state.
package progress

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const heartbeatInterval = 30 * time.Second

type scopeKey struct{}

type scope struct {
	base       *slog.Logger
	logger     *slog.Logger
	attributes []slog.Attr
}

// Logger returns the factory logger enriched with the current operation identity.
func Logger(ctx context.Context) *slog.Logger {
	if current, ok := ctx.Value(scopeKey{}).(scope); ok {
		return current.logger
	}
	return slog.Default()
}

// Start reports a boundary and periodic waiting progress. Call finish on every
// return, with true only after success. It neither cancels work nor renews leases.
func Start(ctx context.Context, component, phase string, attributes ...slog.Attr) (context.Context, func(bool)) {
	ticker := time.NewTicker(heartbeatInterval)
	ctx, finish := start(ctx, component, phase, ticker.C, attributes...)
	return ctx, func(success bool) {
		ticker.Stop()
		finish(success)
	}
}

func start(ctx context.Context, component, phase string, ticks <-chan time.Time, attributes ...slog.Attr) (context.Context, func(bool)) {
	current := operationScope(ctx, component, phase, attributes)
	ctx = context.WithValue(ctx, scopeKey{}, current)
	started := time.Now()
	current.logger.InfoContext(ctx, "operation started")
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticks:
				current.logger.InfoContext(ctx, "operation waiting", "duration", time.Since(started))
			}
		}
	}()
	var once sync.Once
	return ctx, func(success bool) {
		once.Do(func() {
			close(done)
			<-exited
			if success {
				current.logger.InfoContext(ctx, "operation completed", "duration", time.Since(started))
			} else {
				current.logger.WarnContext(ctx, "operation incomplete", "duration", time.Since(started))
			}
		})
	}
}

func operationScope(ctx context.Context, component, phase string, attributes []slog.Attr) scope {
	current, exists := ctx.Value(scopeKey{}).(scope)
	if !exists {
		current.base = slog.Default()
	}
	replacements := append([]slog.Attr{slog.String("component", component), slog.String("phase", phase)}, attributes...)
	merged := append([]slog.Attr(nil), current.attributes...)
	for _, replacement := range replacements {
		merged = replaceAttribute(merged, replacement)
	}
	values := make([]any, len(merged))
	for index, attribute := range merged {
		values[index] = attribute
	}
	return scope{base: current.base, logger: current.base.With(values...), attributes: merged}
}

func replaceAttribute(attributes []slog.Attr, replacement slog.Attr) []slog.Attr {
	for index, attribute := range attributes {
		if attribute.Key == replacement.Key {
			attributes[index] = replacement
			return attributes
		}
	}
	return append(attributes, replacement)
}
