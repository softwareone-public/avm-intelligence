package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/softwareone-public/avm-intelligence/internal/discovery"
	"github.com/softwareone-public/avm-intelligence/internal/github"
	"github.com/softwareone-public/avm-intelligence/internal/ratelimit"
	"github.com/softwareone-public/avm-intelligence/internal/state"
)

// safeLogError keeps arbitrary parser diagnostics, API bodies and input values
// out of Actions logs. Returned errors retain their full context for callers.
func safeLogError(err error) string {
	var parking *discovery.ParkingError
	if errors.As(err, &parking) {
		return parking.Error()
	}
	var deferred *ratelimit.DeferredError
	if errors.As(err, &deferred) {
		return "GitHub request deferred beyond safe wait; resume the durable continuation"
	}
	var stateAPI *state.APIError
	if errors.As(err, &stateAPI) {
		return stateAPI.Error()
	}
	switch {
	case errors.Is(err, github.ErrGitHubRateLimited):
		return "GitHub projection rate limit reached; delivery checkpointed for retry"
	case errors.Is(err, github.ErrProjectionBudgetExhausted):
		return "local projection request budget exhausted; delivery checkpointed for retry"
	case errors.Is(err, os.ErrNotExist):
		return "required file or directory does not exist"
	case errors.Is(err, os.ErrPermission):
		return "filesystem permission denied"
	}
	for _, message := range []string{
		"--log-level / FACTORY_LOG_LEVEL must be debug, info, warn or error",
		"--log-format / FACTORY_LOG_FORMAT must be text or json",
		"GitHub App credentials are required",
		"select an explicit native factory command; use --help to list commands",
	} {
		if err.Error() == message {
			return message
		}
	}
	return "operation failed; verify command inputs and preceding stage events"
}

const (
	defaultLogLevel  = "info"
	defaultLogFormat = "text"
	logLevelEnv      = "FACTORY_LOG_LEVEL"
	logFormatEnv     = "FACTORY_LOG_FORMAT"
)

// logCommandCompletion owns the terminal log; help is not a failed operation.
func logCommandCompletion(logger *slog.Logger, command string, started time.Time, err error) {
	if err == flag.ErrHelp {
		return
	}
	if err != nil {
		attributes := []any{"command", command, "duration", time.Since(started), "error", safeLogError(err), "error_type", fmt.Sprintf("%T", err)}
		attributes = append(attributes, safeFailureAttributes(err)...)
		logger.Error("factory command failed", attributes...)
		return
	}
	logger.Info("factory command completed", "command", command, "duration", time.Since(started))
}

// safeFailureAttributes reports allowlisted identifiers and counters, never error bodies.
func safeFailureAttributes(err error) []any {
	var attributes []any
	var parking *discovery.ParkingError
	if errors.As(err, &parking) {
		cause := "checkpoint_write_failed"
		switch {
		case errors.Is(parking.Cause, state.ErrHeadMoved):
			cause = "state_head_moved"
		case errors.Is(parking.Cause, context.Canceled), errors.Is(parking.Cause, context.DeadlineExceeded):
			cause = "context_ended"
		case errors.Is(parking.Cause, ratelimit.ErrDeferred):
			cause = "pacing_deferred"
		}
		attributes = append(attributes, "parking_failed", true, "parking_failure_reason", cause)
	}
	var deferred *ratelimit.DeferredError
	if errors.As(err, &deferred) {
		attributes = append(attributes, "pacing_resource", deferred.Resource, "pacing_retry_at", deferred.RetryAt.UTC(), "server_limited", deferred.ServerLimited)
	}
	var stateAPI *state.APIError
	if errors.As(err, &stateAPI) {
		attributes = append(attributes, stateAPI.DiagnosticAttributes()...)
	}
	var cycle *discovery.CycleError
	if errors.As(err, &cycle) {
		attributes = append(attributes, "module_id", cycle.ModuleID, "phase", "discovery_module_cycle", "position", cycle.Position, "total", cycle.Total, "completed", cycle.Completed)
	}
	if errors.Is(err, github.ErrGitHubRateLimited) {
		attributes = append(attributes, "reason", "github_rate_limited")
	} else if errors.Is(err, github.ErrProjectionBudgetExhausted) {
		attributes = append(attributes, "reason", "local_projection_budget_exhausted")
	}
	var budget *github.ProjectionBudgetError
	if errors.As(err, &budget) {
		attributes = append(attributes, "resource", budget.Resource, "remaining", budget.Remaining, "reserve", budget.Reserve, "secondary_remaining", budget.SecondaryRemaining, "secondary_reserve", budget.SecondaryReserve)
	}
	var delivery *github.ProjectionDeliveryError
	if errors.As(err, &delivery) && !delivery.RetryAt.IsZero() {
		attributes = append(attributes, "retry_at", delivery.RetryAt.UTC())
	}
	return attributes
}

// factoryLogger validates effective settings before any command side effects.
// Explicit flags override even invalid environment values.
func factoryLogger(flags *flag.FlagSet, levelValue, formatValue string, getenv func(string) string, output io.Writer) (*slog.Logger, error) {
	explicit := map[string]bool{}
	flags.Visit(func(option *flag.Flag) { explicit[option.Name] = true })
	if !explicit["log-level"] && getenv(logLevelEnv) != "" {
		levelValue = getenv(logLevelEnv)
	}
	if !explicit["log-format"] && getenv(logFormatEnv) != "" {
		formatValue = getenv(logFormatEnv)
	}
	var level slog.Level
	switch strings.ToLower(levelValue) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, fmt.Errorf("--log-level / FACTORY_LOG_LEVEL must be debug, info, warn or error")
	}
	switch strings.ToLower(formatValue) {
	case "text", "json":
		return slog.New(&moduleFirstHandler{writer: &synchronizedLogWriter{output: output}, level: level, format: strings.ToLower(formatValue)}), nil
	default:
		return nil, fmt.Errorf("--log-format / FACTORY_LOG_FORMAT must be text or json")
	}
}

// moduleFirstHandler retains standard slog encoding while promoting module identity before msg.
type moduleFirstHandler struct {
	writer   *synchronizedLogWriter
	level    slog.Level
	format   string
	bindings []logBinding
}

type logBinding struct {
	group      string
	attributes []slog.Attr
}

// synchronizedLogWriter shares serialization across all derived handlers and per-record renderers.
type synchronizedLogWriter struct {
	mu     sync.Mutex
	output io.Writer
}

func (writer *synchronizedLogWriter) Write(content []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.output.Write(content)
}

func (handler *moduleFirstHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= handler.level
}

func (handler *moduleFirstHandler) Handle(ctx context.Context, record slog.Record) error {
	modules, remaining := splitModuleLogAttributes(resolveLogAttributes(handler.recordAttributes(record), handler.format, false))
	messageVisited := false
	options := &slog.HandlerOptions{Level: handler.level, ReplaceAttr: func(groups []string, attribute slog.Attr) slog.Attr {
		if len(groups) == 0 && attribute.Key == slog.MessageKey && !messageVisited {
			messageVisited = true
			if len(modules) > 0 {
				return slog.Attr{Value: slog.GroupValue(append(modules, attribute)...)}
			}
		}
		return utcLogTime(groups, attribute)
	}}
	var renderer slog.Handler
	if handler.format == "json" {
		renderer = slog.NewJSONHandler(handler.writer, options)
	} else {
		renderer = slog.NewTextHandler(handler.writer, options)
	}
	reordered := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	reordered.AddAttrs(remaining...)
	return renderer.Handle(ctx, reordered)
}

func (handler *moduleFirstHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	return handler.withBinding(logBinding{attributes: resolveLogAttributes(attributes, handler.format, true)})
}

func (handler *moduleFirstHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return handler
	}
	return handler.withBinding(logBinding{group: name})
}

func (handler *moduleFirstHandler) withBinding(binding logBinding) slog.Handler {
	derived := *handler
	derived.bindings = append(append([]logBinding(nil), handler.bindings...), binding)
	return &derived
}

// recordAttributes reconstructs nested scopes without splitting one group into duplicate JSON keys.
func (handler *moduleFirstHandler) recordAttributes(record slog.Record) []slog.Attr {
	scopes := []logBinding{{}}
	for _, binding := range handler.bindings {
		if binding.group != "" {
			scopes = append(scopes, binding)
		} else {
			current := &scopes[len(scopes)-1]
			current.attributes = append(current.attributes, binding.attributes...)
		}
	}
	record.Attrs(func(attribute slog.Attr) bool {
		current := &scopes[len(scopes)-1]
		current.attributes = append(current.attributes, attribute)
		return true
	})
	for index := len(scopes) - 1; index > 0; index-- {
		group := slog.Attr{Key: scopes[index].group, Value: slog.GroupValue(scopes[index].attributes...)}
		scopes[index-1].attributes = append(scopes[index-1].attributes, group)
	}
	return scopes[0].attributes
}

func splitModuleLogAttributes(attributes []slog.Attr) ([]slog.Attr, []slog.Attr) {
	var modules, remaining []slog.Attr
	for _, attribute := range attributes {
		switch {
		case attribute.Key == "module_id":
			modules = append(modules, attribute)
		case attribute.Key == "" && attribute.Value.Kind() == slog.KindGroup:
			nestedModules, nestedRemaining := splitModuleLogAttributes(attribute.Value.Group())
			modules = append(modules, nestedModules...)
			remaining = append(remaining, nestedRemaining...)
		default:
			remaining = append(remaining, attribute)
		}
	}
	return modules, remaining
}

func resolveLogAttributes(attributes []slog.Attr, format string, bound bool) []slog.Attr {
	resolved := make([]slog.Attr, len(attributes))
	for index, attribute := range attributes {
		attribute.Value = attribute.Value.Resolve()
		if attribute.Equal(slog.Attr{}) {
			continue
		}
		if attribute.Value.Kind() == slog.KindGroup {
			attribute.Value = slog.GroupValue(resolveLogAttributes(attribute.Value.Group(), format, bound)...)
		} else if bound && attribute.Value.Kind() == slog.KindAny {
			attribute.Value = snapshotBoundLogValue(attribute.Value, format)
		}
		resolved[index] = attribute
	}
	return resolved
}

// snapshotBoundLogValue preserves standard WithAttrs bind-time formatting of mutable Any values.
func snapshotBoundLogValue(value slog.Value, format string) slog.Value {
	var output bytes.Buffer
	options := &slog.HandlerOptions{ReplaceAttr: func(_ []string, attribute slog.Attr) slog.Attr {
		if attribute.Key == slog.TimeKey || attribute.Key == slog.LevelKey || attribute.Key == slog.MessageKey {
			return slog.Attr{}
		}
		return attribute
	}}
	record := slog.NewRecord(time.Time{}, slog.LevelInfo, "", 0)
	record.AddAttrs(slog.Attr{Key: "value", Value: value})
	var renderer slog.Handler = slog.NewTextHandler(&output, options)
	if format == "json" {
		renderer = slog.NewJSONHandler(&output, options)
	}
	_ = renderer.Handle(context.Background(), record)
	if format == "json" {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
			return slog.StringValue("!ERROR: bound log value could not be encoded")
		}
		return slog.AnyValue(fields["value"])
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(output.String(), "value="), "\n")
	if strings.HasPrefix(encoded, `"`) {
		decoded, err := strconv.Unquote(encoded)
		if err != nil {
			return slog.StringValue("!ERROR: bound log value could not be encoded")
		}
		return slog.AnyValue([]byte(decoded))
	}
	return slog.StringValue(encoded)
}

// utcLogTime normalizes telemetry timestamps without touching evidence times.
func utcLogTime(groups []string, attribute slog.Attr) slog.Attr {
	if len(groups) == 0 && attribute.Key == slog.TimeKey {
		return slog.Time(slog.TimeKey, attribute.Value.Time().UTC())
	}
	return attribute
}

// factoryLogOutput reserves stdout for token and JSON result consumers.
func factoryLogOutput(flags *flag.FlagSet, stdout, stderr io.Writer) io.Writer {
	for _, name := range []string{"mint-projection-token", "mint-state-token", "list-dimensions", "list-checks", "dispatch-operation", "operator-command"} {
		if value := flags.Lookup(name); value != nil && value.Value.String() != "false" && value.Value.String() != "" {
			return stderr
		}
	}
	if flags.Lookup("assess-facts").Value.String() != "" && flags.Lookup("assessment-out").Value.String() == "" {
		return stderr
	}
	return stdout
}

// factoryCommandName reports the selected mode, never argument values or secrets.
func factoryCommandName(flags *flag.FlagSet) string {
	command := "factory"
	flags.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "mint-projection-token", "mint-state-token", "discover-catalog", "persist-discovery", "dispatch-operation", "operator-command", "build-indexed-site", "export-presentation", "project-discovery-modules", "project-discovery-selection", "list-dimensions", "list-checks", "assess-facts", "build-indexes", "process-work", "prepare-worker-input", "seal-worker", "apply-proposal", "build-catalog-proposal", "build-module-evidence-proposal", "import-evidence-receipt", "collect-provider-catalog":
			if option.Value.String() != "false" && option.Value.String() != "" {
				command = option.Name
			}
		}
	})
	return command
}
