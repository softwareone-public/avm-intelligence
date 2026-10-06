package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

// safeLogError keeps arbitrary parser diagnostics, API bodies and input values
// out of Actions logs. Returned errors retain their full context for callers.
func safeLogError(err error) string {
	switch {
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
		logger.Error("factory command failed", "command", command, "duration", time.Since(started), "error", safeLogError(err), "error_type", fmt.Sprintf("%T", err))
		return
	}
	logger.Info("factory command completed", "command", command, "duration", time.Since(started))
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
	options := &slog.HandlerOptions{Level: level, ReplaceAttr: utcLogTime}
	switch strings.ToLower(formatValue) {
	case "text":
		return slog.New(slog.NewTextHandler(output, options)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(output, options)), nil
	default:
		return nil, fmt.Errorf("--log-format / FACTORY_LOG_FORMAT must be text or json")
	}
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
		case "mint-projection-token", "mint-state-token", "discover-catalog", "persist-discovery", "dispatch-operation", "operator-command", "build-indexed-site", "export-presentation", "project-discovery-modules", "list-dimensions", "list-checks", "assess-facts", "build-indexes", "process-work", "prepare-worker-input", "seal-worker", "apply-proposal", "build-catalog-proposal", "build-module-evidence-proposal", "import-evidence-receipt", "collect-provider-catalog":
			if option.Value.String() != "false" && option.Value.String() != "" {
				command = option.Name
			}
		}
	})
	return command
}
