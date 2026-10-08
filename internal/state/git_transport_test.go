package state

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeGitCommandRunnerExecutesAllowlistedGitInCleanEnvironment(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git executable is not installed")
	}
	git, err = filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	command := GitCommand{
		Executable: git,
		Directory:  root,
		Arguments:  []string{"--version"},
	}
	result, err := (SafeGitCommandRunner{Executable: git}).Run(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || !strings.HasPrefix(string(result.Stdout), "git version ") {
		t.Fatalf("unexpected version result: %+v", result)
	}
}

func TestSafeGitCommandRunnerRejectsHostileCommandsAndOverrides(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git executable is not installed")
	}
	git, err = filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	tests := []struct {
		name    string
		command GitCommand
	}{
		{name: "unknown subcommand", command: GitCommand{Directory: root, Arguments: []string{"config", "--global", "credential.helper", "evil"}}},
		{name: "force push", command: GitCommand{Directory: root, Arguments: []string{"push", "--force", "origin", "HEAD:refs/heads/factory-state"}}},
		{name: "config override", command: GitCommand{Directory: root, Arguments: []string{"fetch", "-c", "core.hooksPath=/tmp/hooks", "origin"}}},
		{name: "caller replacement setting", command: GitCommand{Directory: root, Arguments: []string{"fetch", "origin"}, Environment: map[string]string{"GIT_NO_REPLACE_OBJECTS": "0"}}},
		{name: "caller global config", command: GitCommand{Directory: root, Arguments: []string{"fetch", "origin"}, Environment: map[string]string{"GIT_CONFIG_GLOBAL": "/tmp/gitconfig"}}},
		{name: "caller helper", command: GitCommand{Directory: root, Arguments: []string{"fetch", "origin"}, Environment: map[string]string{"GIT_CONFIG_COUNT": "1"}}},
		{name: "caller index injection", command: GitCommand{Directory: root, Arguments: []string{"GIT_INDEX_FILE=/tmp/evil-index", "fetch", "origin"}, allowPrivateIndex: true}},
		{name: "caller shallowing override", command: GitCommand{Directory: root, Arguments: []string{"fetch", "--depth=2", "origin"}}},
		{name: "caller unshallow override", command: GitCommand{Directory: root, Arguments: []string{"fetch", "--unshallow", "origin"}}},
		{name: "argument control", command: GitCommand{Directory: root, Arguments: []string{"fetch", "origin\n--upload-pack=evil"}}},
		{name: "credential URL", command: GitCommand{Directory: root, Arguments: []string{"fetch", "https://token-sentinel@github.com/owner/repository"}}},
		{name: "non HTTPS URL", command: GitCommand{Directory: root, Arguments: []string{"fetch", "http://github.com/owner/repository"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.command.Executable = git
			if _, err := (SafeGitCommandRunner{Executable: git}).Run(context.Background(), test.command); err == nil {
				t.Fatal("hostile command was accepted")
			}
		})
	}
}

func TestSafeGitCommandRunnerReturnsTypedSafeFailureCategory(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git executable is not installed")
	}
	git, err = filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (SafeGitCommandRunner{Executable: git}).Run(context.Background(), GitCommand{
		Directory: t.TempDir(), Arguments: []string{"rev-parse", "--verify", "PRIVATE_DIAGNOSTIC_SENTINEL"},
	})
	var commandError *GitCommandError
	if !errors.As(err, &commandError) || commandError.Category != GitFailureIntegrity || result.ExitCode == 0 {
		t.Fatalf("Git failure = (%+v, %v), want typed integrity failure", result, err)
	}
	if strings.Contains(err.Error(), "PRIVATE_DIAGNOSTIC_SENTINEL") || len(result.Stdout) != 0 {
		t.Fatalf("raw Git diagnostic escaped: output=%+v error=%v", result, err)
	}
}

func TestSafeGitCommandRunnerDoesNotReturnRawGitDiagnostics(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git executable is not installed")
	}
	git, err = filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	command := GitCommand{Directory: root, Arguments: []string{"rev-parse", "--verify", "PRIVATE_DIAGNOSTIC_SENTINEL"}}
	result, err := (SafeGitCommandRunner{Executable: git}).Run(context.Background(), command)
	if err == nil || result.ExitCode == 0 {
		t.Fatal("invalid Git command unexpectedly succeeded")
	}
	if len(result.Stdout) != 0 || strings.Contains(err.Error(), "PRIVATE_DIAGNOSTIC_SENTINEL") {
		t.Fatalf("raw Git diagnostic escaped the boundary: output=%+v error=%q", result, err)
	}
}

func TestFileStateCredentialProviderSanitizesTokenProviderErrors(t *testing.T) {
	provider := FileStateCredentialProvider{Token: func(context.Context, string, string) (string, error) {
		return "", errors.New("private token provider diagnostic")
	}}
	err := provider.WithCredential(context.Background(), "github.com", "owner/repository", func(string) error { return nil })
	if err == nil || strings.Contains(err.Error(), "private token provider diagnostic") {
		t.Fatalf("provider diagnostic was not sanitized: %v", err)
	}
}

func TestFileStateCredentialProviderPreservesTypedExpiry(t *testing.T) {
	provider := FileStateCredentialProvider{Token: func(context.Context, string, string) (string, error) {
		return "", ErrStateCredentialExpired
	}}
	err := provider.WithCredential(context.Background(), "github.com", "owner/repository", func(string) error { return nil })
	if !errors.Is(err, ErrStateCredentialExpired) {
		t.Fatalf("expired credential error = %v, want ErrStateCredentialExpired", err)
	}
}

func TestFileStateCredentialProviderCleansUpForAllCallbackOutcomes(t *testing.T) {
	for _, test := range []struct {
		name        string
		callbackErr error
	}{
		{name: "success"},
		{name: "callback failure", callbackErr: errors.New("sentinel callback failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertCredentialCallbackCleanup(t, test.callbackErr)
		})
	}
}

func assertCredentialCallbackCleanup(t *testing.T, callbackErr error) {
	t.Helper()
	provider := FileStateCredentialProvider{Token: func(context.Context, string, string) (string, error) {
		return "token-sentinel", nil
	}}
	var askpassPath string
	err := provider.WithCredential(context.Background(), "github.com", "owner/repository", func(path string) error {
		askpassPath = path
		return assertCredentialFiles(t, path, callbackErr)
	})
	if callbackErr == nil && err != nil {
		t.Fatal(err)
	}
	if callbackErr != nil && (err == nil || err.Error() != "state credential operation failed") {
		t.Fatalf("callback diagnostic was not sanitized: %v", err)
	}
	if _, statErr := os.Stat(filepath.Dir(askpassPath)); !os.IsNotExist(statErr) {
		t.Fatalf("temporary credential directory still exists: %v", statErr)
	}
}

func assertCredentialFiles(t *testing.T, askpassPath string, callbackErr error) error {
	t.Helper()
	fileInfo, err := os.Stat(askpassPath)
	if err != nil {
		return err
	}
	if fileInfo.Mode().Perm() != 0700 {
		t.Fatalf("askpass mode = %o, want 700", fileInfo.Mode().Perm())
	}
	credentialPath := filepath.Join(filepath.Dir(askpassPath), "token")
	contents, err := os.ReadFile(credentialPath)
	if err != nil || string(contents) != "token-sentinel" {
		t.Fatalf("credential content = %q, error = %v", contents, err)
	}
	return callbackErr
}

func TestFileStateCredentialProviderCleansUpOnCancellationAndRejectsUnsafeTarget(t *testing.T) {
	provider := FileStateCredentialProvider{Token: func(context.Context, string, string) (string, error) {
		return "token-sentinel", nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var credentialPath string
	err := provider.WithCredential(ctx, "github.com", "owner/repository", func(path string) error {
		credentialPath = path
		return ctx.Err()
	})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("cancellation error = %v, want context canceled", err)
	}
	if credentialPath != "" {
		t.Fatal("cancelled credential operation created a temporary credential")
	}
	if err = provider.WithCredential(context.Background(), "https://github.com", "owner/repository", func(string) error { return nil }); err == nil {
		t.Fatal("credential provider accepted URL in host field")
	}
}

func TestFileStateCredentialProviderCleansUpWhenCallbackCancelsContext(t *testing.T) {
	provider := FileStateCredentialProvider{Token: func(context.Context, string, string) (string, error) {
		return "token-sentinel", nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var askpassPath string
	err := provider.WithCredential(ctx, "github.com", "owner/repository", func(path string) error {
		askpassPath = path
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled callback error = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(filepath.Dir(askpassPath)); !os.IsNotExist(statErr) {
		t.Fatalf("credential directory still exists after callback cancellation: %v", statErr)
	}
}

func TestFileStateCredentialProviderAskpassReturnsTokenAndCleansUp(t *testing.T) {
	provider := FileStateCredentialProvider{Token: func(context.Context, string, string) (string, error) {
		return "token-sentinel", nil
	}}
	var helperPath string
	err := provider.WithCredential(context.Background(), "github.com", "owner/repository", func(path string) error {
		helperPath = path
		environment, err := GitCredentialEnvironment(path)
		if err != nil {
			return err
		}
		if environment["GIT_ASKPASS"] != path {
			t.Fatal("credential environment does not reference the generated helper")
		}
		command := exec.Command(path, "Password for https://github.com")
		output, err := command.Output()
		if err != nil || strings.TrimSpace(string(output)) != "token-sentinel" {
			t.Fatalf("askpass output = %q, error = %v", output, err)
		}
		usernameCommand := exec.Command(path, "Username for https://github.com")
		username, err := usernameCommand.Output()
		if err != nil || strings.TrimSpace(string(username)) != "x-access-token" {
			t.Fatalf("askpass username = %q, error = %v", username, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(helperPath)); !os.IsNotExist(err) {
		t.Fatalf("askpass credential directory was not removed: %v", err)
	}
}

func TestGitCredentialEnvironmentRequiresPrivateTransportAskpass(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "askpass")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := GitCredentialEnvironment(path); err == nil {
		t.Fatal("credential helper outside the private transport directory was accepted")
	}
}
