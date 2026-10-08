package state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	maxGitOutputBytes   = 16 << 20
	maxGitErrorBytes    = 4 << 10
	controlCharacters   = "\x00\r\n"
	gitTransportAskpass = "GIT_ASKPASS"
	gitIndexFilePrefix  = "GIT_INDEX_FILE="
)

var (
	gitExecutablePattern = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	gitEnvironmentKey    = regexp.MustCompile(`^GIT_[A-Z0-9_]+$`)
)

var ErrStateCredentialExpired = errors.New("state credential expired")

var gitSubcommands = map[string]bool{
	"cat-file": true, "check-ref-format": true, "commit-tree": true,
	"config": true, "fetch": true, "hash-object": true, "ls-files": true,
	"ls-tree": true, "merge-base": true, "mktree": true, "push": true,
	"read-tree": true, "rev-parse": true,
	"update-index": true, "write-tree": true,
	"--version": true,
}

type GitFailureCategory string

const (
	GitFailureAuthentication GitFailureCategory = "authentication"
	GitFailureContention     GitFailureCategory = "contention"
	GitFailurePolicy         GitFailureCategory = "policy"
	GitFailureIntegrity      GitFailureCategory = "integrity"
	GitFailureCommand        GitFailureCategory = "command"
)

// GitCommandError contains only a safe operation category and exit status.
type GitCommandError struct {
	Operation string
	Category  GitFailureCategory
	ExitCode  int
	Cause     error
}

func (failure *GitCommandError) Error() string {
	return fmt.Sprintf("git %s failed (%s), exit code %d", failure.Operation, failure.Category, failure.ExitCode)
}

func (failure *GitCommandError) Unwrap() error { return failure.Cause }

// SafeGitCommandRunner executes Git with a fixed process environment and no
// ambient configuration, helpers, prompts, replacement objects, or hooks.
type SafeGitCommandRunner struct {
	Executable      string
	testLocalRemote bool
}

func (runner SafeGitCommandRunner) allowLocalRemoteForTest() bool { return runner.testLocalRemote }

func (runner SafeGitCommandRunner) trustedExecutable() string { return runner.Executable }

// Run executes one Git argument vector and returns only bounded captured output.
func (runner SafeGitCommandRunner) Run(ctx context.Context, command GitCommand) (GitCommandResult, error) {
	if ctx == nil {
		return GitCommandResult{}, fmt.Errorf("Git command context is required")
	}
	command = extractPrivateIndex(command)
	executable, err := runner.resolveExecutable()
	if err != nil {
		return GitCommandResult{}, err
	}
	if err = validateGitCommand(command, executable, runner.testLocalRemote); err != nil {
		return GitCommandResult{}, err
	}
	arguments := isolatedGitArguments(command.Arguments, runner.testLocalRemote)
	cmd := exec.CommandContext(ctx, executable, arguments...)
	cmd.Dir = command.Directory
	cmd.Env = safeGitEnvironment(command.Environment)
	cmd.Stdin = bytes.NewReader(command.Input)
	if err = validatePrivateIndexCommand(command, runner.testLocalRemote); err != nil {
		return GitCommandResult{}, err
	}
	stdout := &boundedBuffer{limit: maxGitOutputBytes}
	stderr := &boundedBuffer{limit: maxGitErrorBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err = cmd.Run(); err != nil {
		result := GitCommandResult{ExitCode: -1}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
		category := classifyGitFailure(stderr.Bytes())
		if ctx.Err() != nil {
			return result, &GitCommandError{Operation: command.Arguments[0], Category: GitFailureCommand, ExitCode: result.ExitCode, Cause: ctx.Err()}
		}
		return result, &GitCommandError{Operation: command.Arguments[0], Category: category, ExitCode: result.ExitCode}
	}
	if stdout.Overflowed() || stderr.Overflowed() {
		return GitCommandResult{Stdout: stdout.Bytes(), ExitCode: 0}, fmt.Errorf("git command output exceeded limit")
	}
	return GitCommandResult{Stdout: stdout.Bytes(), ExitCode: 0}, nil
}

func validateGitCommand(command GitCommand, executable string, allowLocalRemote bool) error {
	if !sameExecutable(command.Executable, executable) {
		return fmt.Errorf("git executable override is not allowed")
	}
	if err := validateGitDirectory(command.Directory); err != nil {
		return err
	}
	if err := validateGitSubcommand(command.Arguments); err != nil {
		return err
	}
	if err := validateGitArguments(command.Arguments); err != nil {
		return err
	}
	if err := validateGitArgumentURLs(command.Arguments, allowLocalRemote); err != nil {
		return err
	}
	return validateGitEnvironment(command.Environment)
}

func validatePrivateIndexCommand(command GitCommand, testLocalRemote bool) error {
	if command.allowPrivateIndex && !privateIndexMarkerValid(command) {
		return fmt.Errorf("private Git index command is missing its isolated index")
	}
	if command.allowLocalRemote && !testLocalRemote {
		return fmt.Errorf("test-only Git remote capability is unavailable")
	}
	return nil
}

func privateIndexMarkerValid(command GitCommand) bool {
	return len(command.Arguments) > 0 && strings.HasPrefix(command.Arguments[0], gitIndexFilePrefix) && privateIndexPath(command.Directory, strings.TrimPrefix(command.Arguments[0], gitIndexFilePrefix))
}

func extractPrivateIndex(command GitCommand) GitCommand {
	if !command.allowPrivateIndex || !privateIndexMarkerValid(command) {
		return command
	}
	indexPath := strings.TrimPrefix(command.Arguments[0], gitIndexFilePrefix)
	command.Environment = cloneStringMap(command.Environment)
	command.Environment["GIT_INDEX_FILE"] = indexPath
	command.Arguments = command.Arguments[1:]
	command.allowPrivateIndex = false
	return command
}

func cloneStringMap(values map[string]string) map[string]string {
	clone := make(map[string]string, len(values)+1)
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func privateIndexPath(root, indexPath string) bool {
	if !filepath.IsAbs(indexPath) || filepath.Base(indexPath) != "index" {
		return false
	}
	parent := filepath.Dir(indexPath)
	if filepath.Dir(parent) != root || !strings.HasPrefix(filepath.Base(parent), "avm-state-index-") {
		return false
	}
	info, err := os.Lstat(parent)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0077 == 0
}

func hasPrivateIndexOverride(arguments []string, root string) bool {
	return len(arguments) > 0 && strings.HasPrefix(arguments[0], gitIndexFilePrefix) && privateIndexPath(root, strings.TrimPrefix(arguments[0], gitIndexFilePrefix))
}

func sameExecutable(requested, trusted string) bool {
	if requested == "" {
		return true
	}
	resolved, err := filepath.EvalSymlinks(requested)
	return err == nil && resolved == trusted
}

func validateGitDirectory(directory string) error {
	if directory == "" || !filepath.IsAbs(directory) {
		return fmt.Errorf("git working directory must be absolute")
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("git working directory is unavailable")
	}
	return nil
}

func validateGitSubcommand(arguments []string) error {
	if len(arguments) == 0 || !gitSubcommands[arguments[0]] {
		return fmt.Errorf("Git subcommand is not allowlisted")
	}
	if arguments[0] == "config" && !isSafeLocalConfigRead(arguments) {
		return fmt.Errorf("Git config command is not an allowlisted local read")
	}
	return nil
}

func (runner SafeGitCommandRunner) resolveExecutable() (string, error) {
	executable := runner.Executable
	if executable == "" {
		return "", fmt.Errorf("trusted absolute Git executable is required")
	}
	if !filepath.IsAbs(executable) || !gitExecutablePattern.MatchString(executable) {
		return "", fmt.Errorf("Git executable must be a trusted absolute path")
	}
	for _, segment := range strings.Split(filepath.ToSlash(executable), "/") {
		if segment == ".." {
			return "", fmt.Errorf("Git executable path contains an unresolved segment")
		}
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("resolve trusted Git executable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("trusted Git executable is unavailable")
	}
	return resolved, nil
}

func validateGitArguments(arguments []string) error {
	for _, argument := range arguments {
		if err := validateGitArgument(argument, arguments[0]); err != nil {
			return err
		}
	}
	return nil
}

func validateGitArgument(argument, subcommand string) error {
	if strings.ContainsAny(argument, controlCharacters) {
		return fmt.Errorf("Git arguments must not contain control characters")
	}
	lower := strings.ToLower(argument)
	if isForbiddenGitOption(lower) {
		return fmt.Errorf("Git argument uses a prohibited process or repository override")
	}
	if strings.HasPrefix(lower, "--depth") && (subcommand != "fetch" || argument != "--depth=1") {
		return fmt.Errorf("Git shallow depth is transport-controlled")
	}
	if argument == "-S" || strings.HasPrefix(argument, "-S") {
		return fmt.Errorf("Git argument uses a prohibited signing option")
	}
	if strings.HasPrefix(argument, gitIndexFilePrefix) || strings.HasPrefix(argument, "GIT_DIR=") || strings.HasPrefix(argument, "GIT_WORK_TREE=") {
		return fmt.Errorf("Git environment assignment is not allowed in arguments")
	}
	if argument == "-c" || strings.HasPrefix(argument, "-c") || strings.HasPrefix(lower, "--config") || lower == "--no-replace-objects" {
		return fmt.Errorf("Git isolation options are controlled by the transport")
	}
	return nil
}

func isForbiddenGitOption(argument string) bool {
	for _, forbidden := range []string{
		"--upload-pack", "--receive-pack", "--exec", "--config-env", "--force",
		"--recurse-submodules", "--work-tree", "--git-dir", "--namespace",
		"--filter", "--reference", "--reference-if-able", "--dissociate",
		"--server-option", "--shallow-exclude", "--shallow-since",
		"--unshallow", "--update-shallow", "--no-verify", "--verify-signatures",
		"--signed", "--gpg-sign", "--index-output",
	} {
		if argument == forbidden || strings.HasPrefix(argument, forbidden+"=") {
			return true
		}
	}
	return false
}

func isSafeLocalConfigRead(arguments []string) bool {
	return len(arguments) == 5 && arguments[1] == "--local" && arguments[2] == "--no-includes" && arguments[3] == "--null" && arguments[4] == "--list"
}

func validateGitArgumentURLs(arguments []string, allowLocalRemote bool) error {
	for _, argument := range arguments {
		if err := validateGitArgumentURL(argument, allowLocalRemote); err != nil {
			return err
		}
	}
	return nil
}

func validateGitArgumentURL(argument string, allowLocalRemote bool) error {
	if !strings.Contains(argument, "://") {
		return nil
	}
	parsed, err := url.Parse(argument)
	if err != nil {
		return fmt.Errorf("Git remote URL must be credential-free HTTPS")
	}
	if allowLocalRemote && parsed.Scheme == "file" && parsed.Host == "" {
		if !filepath.IsAbs(parsed.Path) || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("test Git remote path must be absolute")
		}
		return nil
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("Git remote URL must be credential-free HTTPS")
	}
	return nil
}

func classifyGitFailure(stderr []byte) GitFailureCategory {
	diagnostic := strings.ToLower(string(stderr))
	switch {
	case strings.Contains(diagnostic, "authentication failed"), strings.Contains(diagnostic, "could not read username"), strings.Contains(diagnostic, "invalid username or password"), strings.Contains(diagnostic, "repository not found"):
		return GitFailureAuthentication
	case strings.Contains(diagnostic, "non-fast-forward"), strings.Contains(diagnostic, "fetch first"), strings.Contains(diagnostic, "cannot lock ref"):
		return GitFailureContention
	case strings.Contains(diagnostic, "remote rejected"), strings.Contains(diagnostic, "protected branch"), strings.Contains(diagnostic, "permission denied"):
		return GitFailurePolicy
	case strings.Contains(diagnostic, "bad object"), strings.Contains(diagnostic, "invalid object"), strings.Contains(diagnostic, "corrupt"), strings.Contains(diagnostic, "not a git repository"):
		return GitFailureIntegrity
	default:
		return GitFailureCommand
	}
}

func isolatedGitArguments(arguments []string, allowLocalRemote bool) []string {
	configuration := []string{
		"--no-pager",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "credential.helper=",
		"-c", "core.askPass=",
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.http.allow=never",
		"-c", "protocol.https.allow=always",
		"-c", "http.followRedirects=false",
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
	}
	fileProtocol := "protocol.file.allow=never"
	if allowLocalRemote {
		fileProtocol = "protocol.file.allow=always"
	}
	configuration = append(configuration, "-c", fileProtocol)
	return append(configuration, arguments...)
}

func validateGitEnvironment(environment map[string]string) error {
	for key, value := range environment {
		if !gitEnvironmentKey.MatchString(key) || strings.ContainsAny(value, controlCharacters) {
			return fmt.Errorf("Git environment contains an invalid entry")
		}
		if err := validateGitEnvironmentEntry(key, value); err != nil {
			return err
		}
	}
	return nil
}

func validateGitEnvironmentEntry(key, value string) error {
	if !allowedGitEnvironmentKey(key) {
		return fmt.Errorf("Git environment key is not allowlisted")
	}
	switch key {
	case "GIT_TERMINAL_PROMPT", "GIT_NO_REPLACE_OBJECTS", "GIT_CONFIG_NOSYSTEM":
		if value != "1" {
			return fmt.Errorf("Git isolation environment value cannot be overridden")
		}
	case "GIT_CONFIG_GLOBAL":
		if value != os.DevNull {
			return fmt.Errorf("Git global configuration cannot be enabled")
		}
	case gitTransportAskpass:
		if !validPrivateAskpass(value) {
			return fmt.Errorf("Git askpass must be a private transport-owned helper")
		}
	case "GIT_AUTHOR_DATE", "GIT_COMMITTER_DATE":
		if !validGitDate(value) {
			return fmt.Errorf("Git commit date is invalid")
		}
	case "GIT_INDEX_FILE":
		if !privateIndexPathFromValue(value) {
			return fmt.Errorf("Git index environment path is not private")
		}
	default:
		return fmt.Errorf("Git environment key is not needed by the transport")
	}
	return nil
}

func validGitDate(value string) bool {
	if strings.HasPrefix(value, "@") {
		parts := strings.Fields(value)
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "@") || !strings.HasPrefix(parts[1], "+") && !strings.HasPrefix(parts[1], "-") {
			return false
		}
		if _, err := strconv.ParseInt(strings.TrimPrefix(parts[0], "@"), 10, 64); err != nil {
			return false
		}
		_, err := time.Parse("-0700", parts[1])
		return err == nil
	}
	_, err := time.Parse("Mon Jan _2 15:04:05 2006 -0700", value)
	return err == nil
}

func privateIndexPathFromValue(indexPath string) bool {
	if !filepath.IsAbs(indexPath) || filepath.Base(indexPath) != "index" {
		return false
	}
	parent := filepath.Dir(indexPath)
	if filepath.Dir(parent) == "" {
		return false
	}
	info, err := os.Lstat(parent)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0077 == 0 && strings.HasPrefix(filepath.Base(parent), "avm-state-index-")
}

func allowedGitEnvironmentKey(key string) bool {
	switch key {
	case "GIT_TERMINAL_PROMPT", "GIT_NO_REPLACE_OBJECTS", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL", "GIT_ASKPASS", "GIT_AUTHOR_DATE", "GIT_COMMITTER_DATE", "GIT_INDEX_FILE":
		return true
	default:
		return false
	}
}

func safeGitEnvironment(overrides map[string]string) []string {
	environment := []string{"PATH=" + trustedGitPath(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0", "GIT_ATTR_NOSYSTEM=1", "HOME=" + os.DevNull}
	if runtime.GOOS == "windows" {
		environment = append(environment, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"), "WINDIR="+os.Getenv("WINDIR"), "TEMP="+os.TempDir())
	}
	if askpass := overrides[gitTransportAskpass]; askpass != "" {
		environment = append(environment, "GIT_ASKPASS="+askpass, "SSH_ASKPASS="+askpass)
	}
	for _, key := range []string{"GIT_AUTHOR_DATE", "GIT_COMMITTER_DATE"} {
		if date := overrides[key]; date != "" {
			environment = append(environment, key+"="+date)
		}
	}
	if indexPath := overrides["GIT_INDEX_FILE"]; indexPath != "" {
		environment = append(environment, gitIndexFilePrefix+indexPath)
	}
	return environment
}

func trustedGitPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Dir(os.Getenv("SYSTEMROOT"))
	}
	return "/usr/bin:/bin:/usr/local/bin"
}

type boundedBuffer struct {
	buffer   strings.Builder
	limit    int
	overflow bool
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return len(data), nil
	}
	if len(data) > remaining {
		buffer.overflow = true
		data = data[:remaining]
	}
	_, err := buffer.buffer.Write(data)
	return written, err
}

func (buffer *boundedBuffer) Bytes() []byte { return []byte(buffer.buffer.String()) }

func (buffer *boundedBuffer) Overflowed() bool { return buffer.overflow }

// FileStateCredentialProvider exposes an App token only through a short-lived
// private credential file for a single host/repository operation.
type FileStateCredentialProvider struct {
	Token func(context.Context, string, string) (string, error)
}

// WithCredential writes a temporary mode-0600 credential and always removes it.
func (provider FileStateCredentialProvider) WithCredential(ctx context.Context, host, repository string, run func(string) error) (returnErr error) {
	if provider.Token == nil || run == nil || !validCredentialTarget(host, repository) {
		return fmt.Errorf("state credential target or provider is invalid")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("state credential operation cancelled: %w", err)
	}
	token, err := provider.Token(ctx, host, repository)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("state credential operation cancelled: %w", ctx.Err())
		}
		if errors.Is(err, ErrStateCredentialExpired) {
			return fmt.Errorf("state credential expired: %w", ErrStateCredentialExpired)
		}
		return fmt.Errorf("obtain state credential")
	}
	if !validCredentialToken(token) {
		return fmt.Errorf("state credential is empty or invalid")
	}
	directory, err := os.MkdirTemp("", "avm-state-credential-")
	if err != nil {
		return fmt.Errorf("create private credential directory")
	}
	defer func() {
		if err := os.RemoveAll(directory); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("remove temporary state credential: %w", err)
		}
	}()
	if err = os.Chmod(directory, 0700); err != nil {
		return fmt.Errorf("protect temporary state credential directory")
	}
	credentialPath := filepath.Join(directory, "token")
	if err = os.WriteFile(credentialPath, []byte(token), 0600); err != nil {
		return fmt.Errorf("write temporary state credential")
	}
	if err = os.Chmod(credentialPath, 0600); err != nil {
		return fmt.Errorf("protect temporary state credential")
	}
	askpassPath := filepath.Join(directory, "askpass")
	askpass := "#!/bin/sh\ncase \"$1\" in\n  *[Uu]sername*) printf '%s\\n' 'x-access-token' ;;\n  *) exec cat -- " + shellQuote(credentialPath) + " ;;\nesac\n"
	if err = os.WriteFile(askpassPath, []byte(askpass), 0700); err != nil {
		return fmt.Errorf("write temporary Git askpass helper")
	}
	if err = os.Chmod(askpassPath, 0700); err != nil {
		return fmt.Errorf("protect temporary Git askpass helper")
	}
	if err = run(askpassPath); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("state credential operation cancelled: %w", ctx.Err())
		}
		var safeError *GitCommandError
		if errors.As(err, &safeError) {
			return err
		}
		return fmt.Errorf("state credential operation failed")
	}
	return nil
}

func validCredentialTarget(host, repository string) bool {
	if !validCredentialHost(host) || repository == "" || strings.ContainsAny(repository, controlCharacters+"@?#") {
		return false
	}
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && validRepositorySegment(parts[0]) && validRepositorySegment(strings.TrimSuffix(parts[1], ".git"))
}

func validCredentialHost(host string) bool {
	if host == "" || strings.Contains(host, "/") || strings.ContainsAny(host, controlCharacters+"@?#") || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !validHostLabel(label) {
			return false
		}
	}
	return true
}

func validHostLabel(label string) bool {
	if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, character := range label {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-') {
			return false
		}
	}
	return true
}

func validRepositorySegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for _, character := range segment {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("_.-", character)) {
			return false
		}
	}
	return true
}

func validCredentialToken(token string) bool {
	return token != "" && !strings.ContainsAny(token, controlCharacters) && !strings.Contains(token, " ")
}

// GitCredentialEnvironment binds Git prompts to the transport-owned helper
// created by FileStateCredentialProvider. The token is never an argument or
// environment value.
func GitCredentialEnvironment(askpassPath string) (map[string]string, error) {
	if !validPrivateAskpass(askpassPath) {
		return nil, fmt.Errorf("Git askpass helper must be a private transport-owned file")
	}
	return map[string]string{"GIT_ASKPASS": askpassPath}, nil
}

func validPrivateAskpass(askpassPath string) bool {
	if !filepath.IsAbs(askpassPath) || filepath.Base(askpassPath) != "askpass" {
		return false
	}
	info, err := os.Stat(askpassPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0100 == 0 {
		return false
	}
	directory := filepath.Base(filepath.Dir(askpassPath))
	return strings.HasPrefix(directory, "avm-state-credential-")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

var _ GitCommandRunner = SafeGitCommandRunner{}
var _ StateCredentialProvider = FileStateCredentialProvider{}
var _ io.Writer = (*boundedBuffer)(nil)
